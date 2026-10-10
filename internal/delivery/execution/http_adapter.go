package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const maxAdapterResponseBytes = 2 << 20

var (
	ErrInvalidAdapterConfig = errors.New("invalid delivery HTTP adapter configuration")
	ErrInvalidAdapterResult = errors.New("invalid delivery HTTP adapter result")
)

type HTTPAdapterConfig struct {
	BaseURL                 string
	Token                   string
	AdapterID               string
	ContractVersion         string
	Actions                 []domain.EffectAction
	KeyRetention            time.Duration
	LookupConsistencyWindow time.Duration
	RequestTimeout          time.Duration
	Transport               http.RoundTripper
	Clock                   func() time.Time
}

type HTTPAdapter struct {
	baseURL        string
	token          string
	capability     Capability
	requestTimeout time.Duration
	clock          func() time.Time
	mutationClient *http.Client
	queryClient    *http.Client
}

type adapterRequest = BoundRequest

type adapterEnvelope struct {
	SchemaVersion     string                `json:"schema_version"`
	Status            string                `json:"status"`
	Action            domain.EffectAction   `json:"action"`
	RequestDigest     domain.ArtifactDigest `json:"request_digest"`
	ExternalRef       string                `json:"external_ref,omitempty"`
	ErrorCode         string                `json:"error_code,omitempty"`
	RetryAfterSeconds int64                 `json:"retry_after_seconds,omitempty"`
	NoSideEffect      bool                  `json:"no_side_effect,omitempty"`
	Authoritative     bool                  `json:"authoritative,omitempty"`
	Result            json.RawMessage       `json:"result,omitempty"`
}

func NewHTTPAdapter(config HTTPAdapterConfig) (*HTTPAdapter, error) {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	baseURL, err := validateHTTPAdapterConfig(config)
	if err != nil {
		return nil, err
	}
	transport := config.Transport
	if transport == nil {
		transport, err = pinnedHTTPTransport(baseURL)
		if err != nil {
			return nil, err
		}
	}
	noRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	actions := append([]domain.EffectAction(nil), config.Actions...)
	capability := Capability{
		AdapterID:               config.AdapterID,
		ContractVersion:         config.ContractVersion,
		Actions:                 actions,
		SameRequestReplays:      true,
		MismatchRejected:        true,
		LookupByKey:             true,
		KeyRetention:            config.KeyRetention,
		LookupConsistencyWindow: config.LookupConsistencyWindow,
		SupportsRecovery:        true,
	}
	if err := validateCapability(capability); err != nil {
		return nil, err
	}
	return &HTTPAdapter{
		baseURL:        baseURL,
		token:          strings.TrimSpace(config.Token),
		capability:     capability,
		requestTimeout: config.RequestTimeout,
		clock:          config.Clock,
		mutationClient: &http.Client{
			Transport:     nonReplayTransport{base: transport},
			CheckRedirect: noRedirect,
		},
		queryClient: &http.Client{
			Transport:     transport,
			CheckRedirect: noRedirect,
		},
	}, nil
}

func (adapter *HTTPAdapter) Capability() Capability {
	value := adapter.capability
	value.Actions = append([]domain.EffectAction(nil), value.Actions...)
	return value
}

func (adapter *HTTPAdapter) Bind(
	preview domain.EffectPreview,
	key string,
	createdAt time.Time,
) (Binding, error) {
	if key == "" ||
		createdAt.IsZero() ||
		preview.Target == "" ||
		preview.ParametersDigest == "" ||
		!adapter.supports(preview.Action) ||
		preview.AdapterID != adapter.capability.AdapterID ||
		preview.ContractVersion != adapter.capability.ContractVersion {
		return Binding{}, ErrInvalidAdapterConfig
	}
	binding, err := NewBinding(preview, key, createdAt, adapter.capability)
	if err != nil {
		return Binding{}, ErrInvalidAdapterConfig
	}
	return binding, nil
}

func (adapter *HTTPAdapter) Dispatch(
	ctx context.Context,
	binding Binding,
) (Result, error) {
	if err := adapter.validateBinding(binding); err != nil {
		return Result{
			Disposition: DispositionPermanentFailed,
			ErrorCode:   "binding_conflict",
		}, nil
	}
	if !adapter.clock().UTC().Before(binding.ExpiresAt) {
		return Result{
			Disposition: DispositionUnknown,
			ErrorCode:   "idempotency_key_expired",
			ObservedAt:  adapter.clock().UTC(),
		}, nil
	}
	payload := append([]byte(nil), binding.Request...)
	requestCtx, cancel := context.WithTimeout(ctx, adapter.requestTimeout)
	defer cancel()
	var wroteRequest atomic.Bool
	requestCtx = httptrace.WithClientTrace(requestCtx, &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			wroteRequest.Store(true)
		},
	})
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		adapter.baseURL+"/v1/delivery/effects",
		nil,
	)
	if err != nil {
		return Result{}, err
	}
	request.Body = io.NopCloser(bytes.NewReader(payload))
	request.ContentLength = int64(len(payload))
	request.GetBody = nil
	adapter.setHeaders(request, binding)
	request.Header.Set("Content-Type", "application/json")
	response, err := adapter.mutationClient.Do(request)
	if err != nil {
		disposition := DispositionRetryableFailed
		code := "transport_before_send"
		if wroteRequest.Load() {
			disposition = DispositionUnknown
			code = "transport_after_send"
		}
		return Result{
			Disposition: disposition,
			ErrorCode:   code,
			ObservedAt:  adapter.clock().UTC(),
		}, nil
	}
	defer response.Body.Close()
	return adapter.classify(response, binding, false, time.Time{})
}

func (adapter *HTTPAdapter) Lookup(
	ctx context.Context,
	binding Binding,
	dispatchStartedAt time.Time,
) (Result, error) {
	if err := adapter.validateBinding(binding); err != nil {
		return Result{
			Disposition: DispositionPermanentFailed,
			ErrorCode:   "binding_conflict",
			ObservedAt:  adapter.clock().UTC(),
		}, nil
	}
	if dispatchStartedAt.IsZero() {
		return Result{
			Disposition: DispositionUnknown,
			ErrorCode:   "dispatch_time_missing",
			ObservedAt:  adapter.clock().UTC(),
		}, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, adapter.requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		adapter.baseURL+"/v1/delivery/effects/"+url.PathEscape(binding.Key),
		nil,
	)
	if err != nil {
		return Result{}, err
	}
	adapter.setHeaders(request, binding)
	response, err := adapter.queryClient.Do(request)
	if err != nil {
		return Result{
			Disposition: DispositionPending,
			ErrorCode:   "lookup_unavailable",
			ObservedAt:  adapter.clock().UTC(),
		}, nil
	}
	defer response.Body.Close()
	return adapter.classify(response, binding, true, dispatchStartedAt.UTC())
}

func (adapter *HTTPAdapter) classify(
	response *http.Response,
	binding Binding,
	lookup bool,
	dispatchStartedAt time.Time,
) (Result, error) {
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAdapterResponseBytes+1))
	if err != nil || len(raw) > maxAdapterResponseBytes {
		return Result{
			Disposition: DispositionUnknown,
			ErrorCode:   "invalid_response",
			ObservedAt:  adapter.clock().UTC(),
		}, nil
	}
	responseDigest := digestBytes(raw)
	var envelope adapterEnvelope
	decoded := decodeStrictJSON(raw, &envelope) == nil
	retryAfter := responseRetryAfter(response, adapter.clock().UTC())
	if decoded && envelope.RetryAfterSeconds > 0 {
		retryAfter = time.Duration(envelope.RetryAfterSeconds) * time.Second
	}
	result := Result{
		Response:       append([]byte(nil), raw...),
		ResponseDigest: responseDigest,
		ExternalRef:    envelope.ExternalRef,
		ErrorCode:      envelope.ErrorCode,
		RetryAfter:     retryAfter,
		ObservedAt:     adapter.clock().UTC(),
	}
	if !decoded ||
		envelope.SchemaVersion != domain.EffectResultSchemaVersion ||
		envelope.Action != binding.Action ||
		envelope.RequestDigest != binding.RequestDigest {
		result.Disposition = DispositionUnknown
		result.ErrorCode = "invalid_response"
		return result, nil
	}
	switch envelope.Status {
	case "applied":
		if response.StatusCode < 200 || response.StatusCode >= 300 ||
			envelope.ExternalRef == "" {
			result.Disposition = DispositionUnknown
			result.ErrorCode = "invalid_response"
			return result, nil
		}
		result.Disposition = DispositionSucceeded
	case "pending":
		result.Disposition = DispositionPending
	case "rejected":
		if !envelope.NoSideEffect {
			result.Disposition = DispositionUnknown
			result.ErrorCode = "rejection_not_authoritative"
			return result, nil
		}
		result.Disposition = DispositionPermanentFailed
	case "conflict":
		result.Disposition = DispositionPermanentFailed
		result.ErrorCode = "idempotency_conflict"
	case "absent":
		visibleAfter := dispatchStartedAt.Add(binding.LookupConsistencyWindow)
		if !lookup ||
			response.StatusCode != http.StatusNotFound ||
			!envelope.Authoritative ||
			adapter.clock().UTC().Before(visibleAfter) {
			result.Disposition = DispositionPending
			result.ErrorCode = "lookup_not_authoritative"
			return result, nil
		}
		result.Disposition = DispositionAuthoritativeAbsent
	default:
		result.Disposition = DispositionUnknown
		result.ErrorCode = "unsupported_status"
	}
	return result, nil
}

func (adapter *HTTPAdapter) validateBinding(binding Binding) error {
	if binding.SchemaVersion != domain.EffectBindingSchemaVersion ||
		binding.AdapterID != adapter.capability.AdapterID ||
		binding.ContractVersion != adapter.capability.ContractVersion ||
		binding.Target == "" ||
		binding.Key == "" ||
		!adapter.supports(binding.Action) ||
		binding.RequestDigest == "" ||
		binding.CreatedAt.IsZero() ||
		!binding.ExpiresAt.Equal(binding.CreatedAt.Add(adapter.capability.KeyRetention)) ||
		binding.LookupConsistencyWindow != adapter.capability.LookupConsistencyWindow {
		return ErrInvalidAdapterConfig
	}
	canonical, err := domain.CanonicalizeJSON(binding.Request)
	if err != nil {
		return ErrInvalidAdapterConfig
	}
	digest, err := domain.DigestCanonicalJSON(canonical)
	if err != nil || digest != binding.RequestDigest {
		return ErrInvalidAdapterConfig
	}
	var request BoundRequest
	if err := decodeStrictJSON(canonical, &request); err != nil ||
		request.SchemaVersion != domain.EffectRequestSchemaVersion ||
		request.Action != binding.Action ||
		request.Target != binding.Target {
		return ErrInvalidAdapterConfig
	}
	return nil
}

func (adapter *HTTPAdapter) supports(action domain.EffectAction) bool {
	for _, candidate := range adapter.capability.Actions {
		if candidate == action {
			return true
		}
	}
	return false
}

func (adapter *HTTPAdapter) setHeaders(request *http.Request, binding Binding) {
	request.Header.Set("Authorization", "Bearer "+adapter.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Idempotency-Key", binding.Key)
	request.Header.Set("Delivery-Request-SHA256", string(binding.RequestDigest))
}

func validateHTTPAdapterConfig(config HTTPAdapterConfig) (string, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" ||
		(parsed.Path != "" && parsed.Path != "/") ||
		(parsed.Scheme != "https" &&
			!(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) ||
		strings.TrimSpace(config.Token) == "" ||
		config.Token != strings.TrimSpace(config.Token) ||
		strings.TrimSpace(config.AdapterID) == "" ||
		config.AdapterID != strings.TrimSpace(config.AdapterID) ||
		strings.TrimSpace(config.ContractVersion) == "" ||
		config.ContractVersion != strings.TrimSpace(config.ContractVersion) ||
		len(config.Actions) == 0 ||
		config.KeyRetention <= 0 ||
		config.LookupConsistencyWindow < 0 ||
		config.RequestTimeout <= 0 ||
		config.KeyRetention <= config.LookupConsistencyWindow+config.RequestTimeout {
		return "", ErrInvalidAdapterConfig
	}
	known := map[domain.EffectAction]struct{}{
		domain.EffectCreateRoute:   {},
		domain.EffectAssignVehicle: {},
		domain.EffectAssignDriver:  {},
		domain.EffectPublishStops:  {},
		domain.EffectPublishLoad:   {},
		domain.EffectNotifyETA:     {},
	}
	seen := make(map[domain.EffectAction]struct{}, len(config.Actions))
	for _, action := range config.Actions {
		if _, ok := known[action]; !ok {
			return "", ErrInvalidAdapterConfig
		}
		if _, duplicate := seen[action]; duplicate {
			return "", ErrInvalidAdapterConfig
		}
		seen[action] = struct{}{}
	}
	return strings.TrimSuffix(baseURL, "/"), nil
}

func pinnedHTTPTransport(baseURL string) (http.RoundTripper, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, ErrInvalidAdapterConfig
	}
	addresses, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip", parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return nil, fmt.Errorf("%w: resolve adapter origin", ErrInvalidAdapterConfig)
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
		if parsed.Scheme == "http" {
			port = "80"
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var failures []error
		for _, address := range addresses {
			connection, dialErr := dialer.DialContext(
				ctx,
				network,
				net.JoinHostPort(address.String(), port),
			)
			if dialErr == nil {
				return connection, nil
			}
			failures = append(failures, dialErr)
		}
		return nil, errors.Join(failures...)
	}
	return transport, nil
}

type nonReplayTransport struct {
	base http.RoundTripper
}

func (transport nonReplayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.GetBody != nil {
		return nil, fmt.Errorf("delivery mutation request must not be replayable")
	}
	return transport.base.RoundTrip(request)
}

func decodeStrictJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidAdapterResult
	}
	return nil
}

func responseRetryAfter(response *http.Response, now time.Time) time.Duration {
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}

func digestBytes(raw []byte) domain.ArtifactDigest {
	sum := sha256.Sum256(raw)
	return domain.ArtifactDigest(hex.EncodeToString(sum[:]))
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	if address == nil {
		return false
	}
	return address.IsLoopback()
}

var _ Adapter = (*HTTPAdapter)(nil)
