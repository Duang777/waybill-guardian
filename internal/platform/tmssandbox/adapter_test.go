package tmssandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

var testNow = time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)

func TestNewValidatesManifestAndExposesCapability(t *testing.T) {
	var authorization, account string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != manifestPath {
			http.NotFound(w, r)
			return
		}
		authorization = r.Header.Get("Authorization")
		account = r.Header.Get("Waybill-Account")
		writeJSON(t, w, http.StatusOK, validManifest())
	}))
	defer server.Close()

	adapter := openTestAdapter(t, server.URL, func() time.Time { return testNow })
	capability := adapter.Capability()
	if capability.Action != domain.ActionReassign ||
		capability.AdapterID != AdapterID ||
		capability.ContractVersion != ContractVersion ||
		capability.Environment != Environment ||
		capability.KeyScope != requiredKeyScope ||
		capability.KeyRetention != 2*time.Hour ||
		capability.LookupConsistencyWindow != 5*time.Second ||
		!capability.SameRequestReplays ||
		!capability.MismatchedRequestRejects ||
		!capability.LookupByKey {
		t.Fatalf("capability = %+v", capability)
	}
	actions := adapter.AdvertisedActions()
	if len(actions) != 1 || actions[0] != domain.ActionReassign {
		t.Fatalf("advertised actions = %v", actions)
	}
	if authorization != "Bearer test-token" || account != "tenant-a" {
		t.Fatalf("manifest headers = authorization:%q account:%q", authorization, account)
	}
}

func TestValidateManifestRejectsUnsafeContracts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*manifest)
	}{
		{
			name: "adapter",
			mutate: func(value *manifest) {
				value.AdapterID = "other"
			},
		},
		{
			name: "contract version",
			mutate: func(value *manifest) {
				value.ContractVersion = "v2"
			},
		},
		{
			name: "environment",
			mutate: func(value *manifest) {
				value.Environment = "production"
			},
		},
		{
			name: "operation",
			mutate: func(value *manifest) {
				value.Capabilities[0].Operation = "claims"
			},
		},
		{
			name: "scope",
			mutate: func(value *manifest) {
				value.Capabilities[0].KeyScope = "tenant"
			},
		},
		{
			name: "same request replay",
			mutate: func(value *manifest) {
				value.Capabilities[0].SameRequestReplays = false
			},
		},
		{
			name: "mismatch rejection",
			mutate: func(value *manifest) {
				value.Capabilities[0].MismatchedRequestRejects = false
			},
		},
		{
			name: "lookup",
			mutate: func(value *manifest) {
				value.Capabilities[0].LookupByKey = false
			},
		},
		{
			name: "retention budget",
			mutate: func(value *manifest) {
				value.Capabilities[0].KeyRetentionSeconds = 3607
			},
		},
		{
			name: "consistency window",
			mutate: func(value *manifest) {
				value.Capabilities[0].ConsistencyWindowSeconds = 11
			},
		},
		{
			name: "duplicate action",
			mutate: func(value *manifest) {
				value.Capabilities = append(value.Capabilities, value.Capabilities[0])
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validManifest()
			test.mutate(&value)
			_, err := validateManifest(
				value,
				2*time.Second,
				time.Hour,
				10*time.Second,
			)
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func TestBindLocksProviderRequestAndScope(t *testing.T) {
	server := manifestServer(t)
	defer server.Close()
	adapter := openTestAdapter(t, server.URL, func() time.Time { return testNow })
	request := testEffectRequest()

	binding, err := adapter.Bind(request, "effect-key", testNow)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte(
		`{"action":"tms.reassign","waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`,
	)
	if binding.SchemaVersion != 1 ||
		binding.Action != domain.ActionReassign ||
		binding.AdapterID != AdapterID ||
		binding.ContractVersion != ContractVersion ||
		binding.ProviderOperation != providerOperation ||
		binding.ProviderScopeDigest != scopeDigest(AdapterID, "tenant-a", providerOperation) ||
		binding.ProviderRequestHash != digest(payload) ||
		!binding.KeyCreatedAt.Equal(testNow) ||
		!binding.KeyExpiresAt.Equal(testNow.Add(2*time.Hour)) ||
		binding.LookupConsistencyWindow != 5*time.Second {
		t.Fatalf("binding = %+v", binding)
	}
	if !adapter.SupportsRecovery(binding) {
		t.Fatal("adapter rejected its own binding")
	}
	binding.ProviderScopeDigest = scopeDigest(AdapterID, "tenant-b", providerOperation)
	if adapter.SupportsRecovery(binding) {
		t.Fatal("adapter accepted a binding from another account")
	}
	binding, err = adapter.Bind(request, "effect-key", testNow)
	if err != nil {
		t.Fatal(err)
	}
	binding.KeyExpiresAt = binding.KeyExpiresAt.Add(time.Hour)
	if adapter.SupportsRecovery(binding) {
		t.Fatal("adapter accepted a binding with an extended key lifetime")
	}
}

func TestBindRejectsInvalidEffectRequest(t *testing.T) {
	server := manifestServer(t)
	defer server.Close()
	adapter := openTestAdapter(t, server.URL, func() time.Time { return testNow })

	tests := []struct {
		name    string
		request platform.EffectRequest
		key     domain.IdempotencyKey
		created time.Time
	}{
		{name: "missing key", request: testEffectRequest(), created: testNow},
		{name: "missing timestamp", request: testEffectRequest(), key: "key"},
		{
			name: "wrong action",
			request: platform.EffectRequest{
				Action:        domain.ActionCreateClaim,
				Arguments:     testEffectRequest().Arguments,
				ArgumentsHash: "hash",
			},
			key:     "key",
			created: testNow,
		},
		{
			name: "unknown field",
			request: platform.EffectRequest{
				Action: domain.ActionReassign,
				Arguments: json.RawMessage(
					`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42","extra":true}`,
				),
				ArgumentsHash: "hash",
			},
			key:     "key",
			created: testNow,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := adapter.Bind(test.request, test.key, test.created); !errors.Is(
				err,
				ErrInvalidEffectRequest,
			) {
				t.Fatalf("error = %v, want ErrInvalidEffectRequest", err)
			}
		})
	}
}

func validManifest() manifest {
	return manifest{
		AdapterID:       AdapterID,
		ContractVersion: ContractVersion,
		Environment:     Environment,
		Capabilities: []manifestCapability{{
			Action:                   domain.ActionReassign,
			Operation:                providerOperation,
			KeyScope:                 requiredKeyScope,
			KeyRetentionSeconds:      int64((2 * time.Hour) / time.Second),
			ConsistencyWindowSeconds: 5,
			SameRequestReplays:       true,
			MismatchedRequestRejects: true,
			LookupByKey:              true,
		}},
	}
}

func openTestAdapter(
	t *testing.T,
	baseURL string,
	clock func() time.Time,
) *Adapter {
	t.Helper()
	adapter, err := New(context.Background(), Config{
		BaseURL:               baseURL,
		Token:                 "test-token",
		Account:               "tenant-a",
		RequestTimeout:        2 * time.Second,
		StartupTimeout:        2 * time.Second,
		ReconciliationHorizon: time.Hour,
		MaxConsistencyWindow:  10 * time.Second,
		Clock:                 clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func manifestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != manifestPath {
			http.NotFound(w, r)
			return
		}
		writeJSON(t, w, http.StatusOK, validManifest())
	}))
}

func testEffectRequest() platform.EffectRequest {
	return platform.EffectRequest{
		Action: domain.ActionReassign,
		Arguments: json.RawMessage(
			`{"carrier_id":"CARRIER-SW-42","waybill_id":"YD2026101001"}`,
		),
		ArgumentsHash: "authorized-arguments-hash",
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}
