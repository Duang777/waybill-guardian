package source

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPProviderBuildsFromExactRevisionEndpoints(t *testing.T) {
	document := sourceTestDocument(t)
	var (
		lock  sync.Mutex
		paths = make(map[string]int)
	)
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", request.Method)
		}
		if request.Header.Get("Authorization") != "Bearer source-token" {
			t.Errorf("authorization header = %q", request.Header.Get("Authorization"))
		}
		lock.Lock()
		paths[request.URL.EscapedPath()]++
		lock.Unlock()
		serveHTTPDocument(t, writer, request, document)
	}))
	defer server.Close()

	provider := newTestHTTPProvider(t, server, HTTPConfig{})
	coordinator := newTestCoordinator(t, provider)
	result, err := coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Problem.ProblemDigest == "" {
		t.Fatal("problem digest is empty")
	}

	wantPrefix := "/v1/delivery/tenants/tenant-a/snapshots/cut-1/"
	lock.Lock()
	defer lock.Unlock()
	if paths[wantPrefix+"manifest"] != 1 {
		t.Fatalf("manifest calls = %d, want 1; paths = %v",
			paths[wantPrefix+"manifest"], paths)
	}
	for _, revision := range document.Manifest.Revisions {
		path := wantPrefix + string(revision.Kind) + "/" + revision.Revision
		if paths[path] != 1 {
			t.Fatalf("%s calls = %d, want 1; paths = %v", path, paths[path], paths)
		}
	}
}

func TestHTTPProviderRetriesOnlyRetryableReadStatus(t *testing.T) {
	document := sourceTestDocument(t)
	var manifestCalls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if strings.HasSuffix(request.URL.Path, "/manifest") &&
			manifestCalls.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		serveHTTPDocument(t, writer, request, document)
	}))
	defer server.Close()
	provider := newTestHTTPProvider(t, server, HTTPConfig{
		MaxAttempts: 2,
		RetryBase:   time.Millisecond,
	})
	coordinator := newTestCoordinator(t, provider)

	if _, err := coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	); err != nil {
		t.Fatal(err)
	}
	if got := manifestCalls.Load(); got != 2 {
		t.Fatalf("manifest calls = %d, want 2", got)
	}
}

func TestHTTPProviderDoesNotFollowRedirects(t *testing.T) {
	document := sourceTestDocument(t)
	var redirected atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		redirected.Add(1)
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	provider := newTestHTTPProvider(t, origin, HTTPConfig{MaxAttempts: 1})
	coordinator := newTestCoordinator(t, provider)

	_, err := coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	var httpErr *SourceHTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusFound {
		t.Fatalf("Build error = %v, want HTTP 302", err)
	}
	if got := redirected.Load(); got != 0 {
		t.Fatalf("redirect target calls = %d, want 0", got)
	}
}

func TestHTTPProviderRejectsOversizedAndMalformedResponses(t *testing.T) {
	document := sourceTestDocument(t)
	tests := []struct {
		name      string
		body      string
		maxBytes  int64
		want      string
		wantError error
	}{
		{
			name:      "oversized",
			body:      strings.Repeat("x", 1024),
			maxBytes:  128,
			wantError: ErrSourceTooLarge,
		},
		{
			name: "unknown field",
			body: `{"unknown":true}`,
			want: "unknown field",
		},
		{
			name: "duplicate field",
			body: `{"schema_version":"delivery.source-manifest.v1",` +
				`"schema_version":"delivery.source-manifest.v1"}`,
			want: "duplicate field",
		},
		{
			name: "trailing value",
			body: `{}` + `{}`,
			want: "multiple JSON values",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			provider := newTestHTTPProvider(t, server, HTTPConfig{
				MaxAttempts:      1,
				MaxResponseBytes: test.maxBytes,
			})
			coordinator := newTestCoordinator(t, provider)
			_, err := coordinator.Build(
				context.Background(),
				sourceTestBuildRequest(document.Manifest),
			)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("Build error = %v, want %v", err, test.wantError)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestHTTPProviderHonorsSharedDeadline(t *testing.T) {
	document := sourceTestDocument(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		select {
		case <-request.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	provider := newTestHTTPProvider(t, server, HTTPConfig{
		MaxAttempts:    1,
		RequestTimeout: time.Second,
	})
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Provider:    provider,
		ReadTimeout: 20 * time.Millisecond,
		Limits:      DefaultLimits(),
		Clock: func() time.Time {
			return document.Manifest.IssuedAt.Add(time.Minute)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	_, err = coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Build error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Build took %s after deadline", elapsed)
	}
}

func TestHTTPProviderRejectsUntrustedOrigins(t *testing.T) {
	tests := []string{
		"http://example.com",
		"https://127.0.0.1",
		"https://10.0.0.1",
		"https://169.254.169.254",
		"https://[::1]",
		"https://[fc00::1]",
	}
	for _, endpoint := range tests {
		t.Run(endpoint, func(t *testing.T) {
			_, err := NewHTTPProvider(context.Background(), HTTPConfig{
				BaseURL:     endpoint,
				BearerToken: "source-token",
				MaxAttempts: 1,
				Resolver:    staticResolver{addresses: []netip.Addr{}},
			})
			if !errors.Is(err, ErrUntrustedEndpoint) {
				t.Fatalf("NewHTTPProvider(%q) error = %v, want ErrUntrustedEndpoint",
					endpoint, err)
			}
		})
	}
}

func TestHTTPProviderRejectsDNSRebindingAtDial(t *testing.T) {
	resolver := &sequenceResolver{
		results: [][]netip.Addr{
			{netip.MustParseAddr("93.184.216.34")},
			{netip.MustParseAddr("127.0.0.1")},
		},
	}
	provider, err := NewHTTPProvider(context.Background(), HTTPConfig{
		BaseURL:        "https://source.example",
		BearerToken:    "source-token",
		MaxAttempts:    1,
		RequestTimeout: 100 * time.Millisecond,
		Resolver:       resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := provider.OpenSnapshot(context.Background(), "tenant-a", "cut-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = snapshot.ReadManifest(context.Background())
	if !errors.Is(err, ErrUntrustedEndpoint) {
		t.Fatalf("ReadManifest error = %v, want ErrUntrustedEndpoint", err)
	}
}

func TestHTTPProviderRequiresTLS12OrNewer(t *testing.T) {
	_, err := NewHTTPProvider(context.Background(), HTTPConfig{
		BaseURL:     "https://127.0.0.1",
		BearerToken: "source-token",
		TLSConfig: &tls.Config{
			MaxVersion: tls.VersionTLS11,
		},
		AllowPrivateNetworks: true,
	})
	if !errors.Is(err, ErrUntrustedEndpoint) {
		t.Fatalf("NewHTTPProvider error = %v, want ErrUntrustedEndpoint", err)
	}
}

func newTestHTTPProvider(
	t *testing.T,
	server *httptest.Server,
	overrides HTTPConfig,
) *HTTPProvider {
	t.Helper()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("test server transport = %T", server.Client().Transport)
	}
	config := HTTPConfig{
		BaseURL:              server.URL,
		BearerToken:          "source-token",
		RequestTimeout:       time.Second,
		MaxResponseBytes:     1 << 20,
		MaxAttempts:          1,
		RetryBase:            time.Millisecond,
		TLSConfig:            transport.TLSClientConfig,
		AllowPrivateNetworks: true,
	}
	if overrides.RequestTimeout != 0 {
		config.RequestTimeout = overrides.RequestTimeout
	}
	if overrides.MaxResponseBytes != 0 {
		config.MaxResponseBytes = overrides.MaxResponseBytes
	}
	if overrides.MaxAttempts != 0 {
		config.MaxAttempts = overrides.MaxAttempts
	}
	if overrides.RetryBase != 0 {
		config.RetryBase = overrides.RetryBase
	}
	provider, err := NewHTTPProvider(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func serveHTTPDocument(
	t *testing.T,
	writer http.ResponseWriter,
	request *http.Request,
	document JSONDocument,
) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(segments) < 7 {
		http.NotFound(writer, request)
		return
	}
	if segments[0] != "v1" ||
		segments[1] != "delivery" ||
		segments[2] != "tenants" ||
		segments[3] != string(document.Manifest.TenantID) ||
		segments[4] != "snapshots" ||
		segments[5] != string(document.Manifest.Ref) {
		http.NotFound(writer, request)
		return
	}
	var value any
	if segments[6] == "manifest" && len(segments) == 7 {
		value = document.Manifest
	} else if len(segments) == 8 {
		kind := Kind(segments[6])
		if _, exists := kindRank[kind]; !exists {
			http.NotFound(writer, request)
			return
		}
		if segments[7] != revisionFor(document.Manifest, kind).Revision {
			http.NotFound(writer, request)
			return
		}
		switch kind {
		case KindOrders:
			value = document.Orders
		case KindDepots:
			value = document.Depots
		case KindFleet:
			value = document.Fleet
		case KindDrivers:
			value = document.Drivers
		case KindTravel:
			value = document.Travel
		case KindChargers:
			value = document.Chargers
		case KindPolicy:
			value = document.Policy
		default:
			http.NotFound(writer, request)
			return
		}
	} else {
		http.NotFound(writer, request)
		return
	}
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

type staticResolver struct {
	addresses []netip.Addr
	err       error
}

func (resolver staticResolver) LookupNetIP(
	context.Context,
	string,
	string,
) ([]netip.Addr, error) {
	return resolver.addresses, resolver.err
}

type sequenceResolver struct {
	lock    sync.Mutex
	results [][]netip.Addr
	index   int
}

func (resolver *sequenceResolver) LookupNetIP(
	context.Context,
	string,
	string,
) ([]netip.Addr, error) {
	resolver.lock.Lock()
	defer resolver.lock.Unlock()
	if resolver.index >= len(resolver.results) {
		return nil, fmt.Errorf("unexpected resolver call %d", resolver.index)
	}
	result := resolver.results[resolver.index]
	resolver.index++
	return result, nil
}

var _ IPResolver = staticResolver{}
var _ IPResolver = (*sequenceResolver)(nil)
