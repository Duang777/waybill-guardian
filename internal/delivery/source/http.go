package source

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const (
	defaultHTTPTimeout        = 10 * time.Second
	defaultHTTPBodyLimit      = 16 << 20
	defaultHTTPAttempts       = 3
	defaultHTTPRetryBase      = 50 * time.Millisecond
	defaultHTTPStartupTimeout = 5 * time.Second
)

type IPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type HTTPConfig struct {
	BaseURL              string
	BearerToken          string
	RequestTimeout       time.Duration
	StartupTimeout       time.Duration
	MaxResponseBytes     int64
	MaxAttempts          int
	RetryBase            time.Duration
	TLSConfig            *tls.Config
	Resolver             IPResolver
	AllowPrivateNetworks bool
}

type HTTPProvider struct {
	baseURL          *url.URL
	token            string
	requestTimeout   time.Duration
	maxResponseBytes int64
	maxAttempts      int
	retryBase        time.Duration
	client           *http.Client
}

type SourceHTTPError struct {
	StatusCode int
	Retryable  bool
}

func (err *SourceHTTPError) Error() string {
	if err.StatusCode == 0 {
		return "delivery source HTTP request failed"
	}
	return fmt.Sprintf("delivery source HTTP returned status %d", err.StatusCode)
}

func NewHTTPProvider(ctx context.Context, config HTTPConfig) (*HTTPProvider, error) {
	baseURL, err := validateHTTPConfig(&config)
	if err != nil {
		return nil, err
	}
	resolver := config.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	startupCtx, cancel := context.WithTimeout(ctx, config.StartupTimeout)
	defer cancel()
	if _, err := resolveTrustedIPs(
		startupCtx,
		resolver,
		baseURL.Hostname(),
		config.AllowPrivateNetworks,
	); err != nil {
		return nil, err
	}

	tlsConfig := config.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	if tlsConfig.MinVersion == 0 || tlsConfig.MinVersion < tls.VersionTLS12 {
		tlsConfig.MinVersion = tls.VersionTLS12
	}
	if tlsConfig.ServerName == "" && net.ParseIP(baseURL.Hostname()) == nil {
		tlsConfig.ServerName = baseURL.Hostname()
	}
	dialer := &trustedDialer{
		host:         baseURL.Hostname(),
		resolver:     resolver,
		allowPrivate: config.AllowPrivateNetworks,
		base: net.Dialer{
			Timeout:   config.RequestTimeout,
			KeepAlive: 30 * time.Second,
		},
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   config.RequestTimeout,
		ResponseHeaderTimeout: config.RequestTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       tlsConfig,
	}
	return &HTTPProvider{
		baseURL:          baseURL,
		token:            strings.TrimSpace(config.BearerToken),
		requestTimeout:   config.RequestTimeout,
		maxResponseBytes: config.MaxResponseBytes,
		maxAttempts:      config.MaxAttempts,
		retryBase:        config.RetryBase,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (provider *HTTPProvider) OpenSnapshot(
	ctx context.Context,
	tenantID domain.TenantID,
	ref SnapshotRef,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(tenantID)) == "" ||
		strings.TrimSpace(string(tenantID)) != string(tenantID) ||
		strings.TrimSpace(string(ref)) == "" ||
		strings.TrimSpace(string(ref)) != string(ref) {
		return nil, ErrNotFound
	}
	return &httpSnapshot{
		provider: provider,
		tenantID: tenantID,
		ref:      ref,
	}, nil
}

type httpSnapshot struct {
	provider *HTTPProvider
	tenantID domain.TenantID
	ref      SnapshotRef
}

func (snapshot *httpSnapshot) ReadManifest(ctx context.Context) (Manifest, error) {
	var manifest Manifest
	err := snapshot.provider.get(
		ctx,
		snapshot.endpoint("manifest"),
		&manifest,
	)
	return manifest, err
}

func (snapshot *httpSnapshot) ReadOrders(
	ctx context.Context,
	revision Revision,
) (Orders, Stamp, error) {
	return getHTTPBlock[Orders](ctx, snapshot, revision)
}

func (snapshot *httpSnapshot) ReadDepots(
	ctx context.Context,
	revision Revision,
) (Depots, Stamp, error) {
	return getHTTPBlock[Depots](ctx, snapshot, revision)
}

func (snapshot *httpSnapshot) ReadFleet(
	ctx context.Context,
	revision Revision,
) (Fleet, Stamp, error) {
	return getHTTPBlock[Fleet](ctx, snapshot, revision)
}

func (snapshot *httpSnapshot) ReadDrivers(
	ctx context.Context,
	revision Revision,
) (Drivers, Stamp, error) {
	return getHTTPBlock[Drivers](ctx, snapshot, revision)
}

func (snapshot *httpSnapshot) ReadTravel(
	ctx context.Context,
	revision Revision,
) (Travel, Stamp, error) {
	return getHTTPBlock[Travel](ctx, snapshot, revision)
}

func (snapshot *httpSnapshot) ReadChargers(
	ctx context.Context,
	revision Revision,
) (Chargers, Stamp, error) {
	return getHTTPBlock[Chargers](ctx, snapshot, revision)
}

func (snapshot *httpSnapshot) ReadPolicy(
	ctx context.Context,
	revision Revision,
) (Policy, Stamp, error) {
	return getHTTPBlock[Policy](ctx, snapshot, revision)
}

func (snapshot *httpSnapshot) Close(context.Context) error {
	return nil
}

func getHTTPBlock[T any](
	ctx context.Context,
	snapshot *httpSnapshot,
	revision Revision,
) (T, Stamp, error) {
	var block Block[T]
	err := snapshot.provider.get(
		ctx,
		snapshot.endpoint(string(revision.Kind), revision.Revision),
		&block,
	)
	return block.Data, block.Stamp, err
}

func (snapshot *httpSnapshot) endpoint(segments ...string) *url.URL {
	escaped := strings.TrimSuffix(snapshot.provider.baseURL.EscapedPath(), "/")
	escaped += "/v1/delivery/tenants/" + url.PathEscape(string(snapshot.tenantID))
	escaped += "/snapshots/" + url.PathEscape(string(snapshot.ref))
	for _, segment := range segments {
		escaped += "/" + url.PathEscape(segment)
	}
	pathValue, err := url.PathUnescape(escaped)
	if err != nil {
		panic("escaped delivery source path is invalid: " + err.Error())
	}
	endpoint := *snapshot.provider.baseURL
	endpoint.Path = pathValue
	endpoint.RawPath = escaped
	return &endpoint
}

func (provider *HTTPProvider) get(
	ctx context.Context,
	endpoint *url.URL,
	destination any,
) error {
	var lastErr error
	for attempt := 1; attempt <= provider.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, provider.requestTimeout)
		request, err := http.NewRequestWithContext(
			attemptCtx,
			http.MethodGet,
			endpoint.String(),
			nil,
		)
		if err != nil {
			cancel()
			return fmt.Errorf("build delivery source HTTP request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+provider.token)
		request.Header.Set("Accept", "application/json")
		response, err := provider.client.Do(request)
		if err != nil {
			cancel()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrUntrustedEndpoint) {
				return err
			}
			lastErr = &SourceHTTPError{Retryable: true}
			if attempt == provider.maxAttempts {
				return fmt.Errorf("%w: %w", lastErr, err)
			}
			if err := waitHTTPRetry(ctx, provider.retryDelay(attempt)); err != nil {
				return err
			}
			continue
		}
		raw, readErr := readHTTPResponse(response, provider.maxResponseBytes)
		cancel()
		if readErr != nil {
			if errors.Is(readErr, ErrSourceTooLarge) {
				return readErr
			}
			lastErr = &SourceHTTPError{Retryable: true}
			if attempt == provider.maxAttempts {
				return fmt.Errorf("%w: %v", lastErr, readErr)
			}
			if err := waitHTTPRetry(ctx, provider.retryDelay(attempt)); err != nil {
				return err
			}
			continue
		}
		if response.StatusCode != http.StatusOK {
			httpErr := &SourceHTTPError{
				StatusCode: response.StatusCode,
				Retryable:  retryableHTTPStatus(response.StatusCode),
			}
			if !httpErr.Retryable || attempt == provider.maxAttempts {
				return httpErr
			}
			delay := retryAfter(response.Header.Get("Retry-After"), time.Now())
			if delay <= 0 {
				delay = provider.retryDelay(attempt)
			}
			if err := waitHTTPRetry(ctx, delay); err != nil {
				return err
			}
			continue
		}
		mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			return fmt.Errorf("delivery source HTTP response must use application/json")
		}
		if err := decodeStrictJSON(raw, destination); err != nil {
			return fmt.Errorf("decode delivery source HTTP response: %w", err)
		}
		return nil
	}
	return lastErr
}

func validateHTTPConfig(config *HTTPConfig) (*url.URL, error) {
	baseURL, err := url.Parse(strings.TrimSpace(config.BaseURL))
	if err != nil ||
		baseURL == nil ||
		!baseURL.IsAbs() ||
		baseURL.Scheme != "https" ||
		baseURL.Host == "" ||
		baseURL.User != nil ||
		baseURL.RawQuery != "" ||
		baseURL.Fragment != "" ||
		baseURL.RawPath != "" ||
		strings.Contains(baseURL.Path, "..") {
		return nil, fmt.Errorf(
			"%w: base URL must be a fixed HTTPS origin without credentials, query, or fragment",
			ErrUntrustedEndpoint,
		)
	}
	token := strings.TrimSpace(config.BearerToken)
	if token == "" || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return nil, fmt.Errorf("delivery source bearer token is required without whitespace")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = defaultHTTPTimeout
	}
	if config.StartupTimeout == 0 {
		config.StartupTimeout = defaultHTTPStartupTimeout
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = defaultHTTPBodyLimit
	}
	if config.MaxAttempts == 0 {
		config.MaxAttempts = defaultHTTPAttempts
	}
	if config.RetryBase == 0 {
		config.RetryBase = defaultHTTPRetryBase
	}
	if config.RequestTimeout < 0 ||
		config.StartupTimeout < 0 ||
		config.MaxResponseBytes < 0 ||
		config.MaxAttempts < 1 ||
		config.MaxAttempts > 10 ||
		config.RetryBase < 0 {
		return nil, fmt.Errorf("delivery source HTTP limits are invalid")
	}
	if config.TLSConfig != nil &&
		config.TLSConfig.MaxVersion != 0 &&
		config.TLSConfig.MaxVersion < tls.VersionTLS12 {
		return nil, fmt.Errorf("%w: TLS 1.2 or newer is required", ErrUntrustedEndpoint)
	}
	baseURL.Path = strings.TrimSuffix(baseURL.Path, "/")
	return baseURL, nil
}

func readHTTPResponse(response *http.Response, maxBytes int64) ([]byte, error) {
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("%w: HTTP response exceeds %d bytes",
			ErrSourceTooLarge, maxBytes)
	}
	return raw, nil
}

func decodeStrictJSON(raw []byte, destination any) error {
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func retryableHTTPStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		status >= 500
}

func (provider *HTTPProvider) retryDelay(attempt int) time.Duration {
	if attempt <= 0 || provider.retryBase <= 0 {
		return 0
	}
	shift := min(attempt-1, 10)
	if provider.retryBase > time.Duration(math.MaxInt64>>shift) {
		return time.Minute
	}
	return min(provider.retryBase*time.Duration(1<<shift), time.Minute)
}

func waitHTTPRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 || seconds > int64(time.Minute/time.Second) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	instant, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	delay := instant.Sub(now)
	if delay <= 0 || delay > time.Minute {
		return 0
	}
	return delay
}
