package tmssandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

const (
	statusApplied  = "applied"
	statusRejected = "rejected"
	statusPending  = "pending"
	statusConflict = "conflict"
	statusAbsent   = "absent"
)

var metadataIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}$`)

type effectEnvelope struct {
	Status            string                  `json:"status"`
	Action            domain.Action           `json:"action"`
	RequestHash       string                  `json:"request_sha256"`
	ExternalRef       string                  `json:"external_ref,omitempty"`
	RequestID         string                  `json:"request_id,omitempty"`
	ErrorCode         string                  `json:"error_code,omitempty"`
	RetryAfterSeconds int64                   `json:"retry_after_seconds,omitempty"`
	NoSideEffect      bool                    `json:"no_side_effect,omitempty"`
	Authoritative     bool                    `json:"authoritative,omitempty"`
	Result            *platform.ReassignOrder `json:"result,omitempty"`
}

func (a *Adapter) Dispatch(
	ctx context.Context,
	binding platform.EffectBinding,
	request platform.EffectRequest,
	key domain.IdempotencyKey,
) platform.DispatchResult {
	payload, mutation, err := mutationPayload(request)
	if err != nil {
		return platform.DispatchResult{
			Disposition: platform.EffectPermanentFailed,
			ErrorCode:   "invalid_request",
		}
	}
	if key == "" ||
		!a.SupportsRecovery(binding) ||
		binding.ProviderRequestHash != digest(payload) {
		return platform.DispatchResult{
			Disposition: platform.EffectPermanentFailed,
			ErrorCode:   "binding_conflict",
		}
	}
	if !a.clock().UTC().Before(binding.KeyExpiresAt) {
		return platform.DispatchResult{
			Disposition: platform.EffectUnknown,
			ErrorCode:   "key_expired",
		}
	}

	requestCtx, cancel := context.WithTimeout(ctx, a.requestTimeout)
	defer cancel()
	var wroteRequest atomic.Bool
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			wroteRequest.Store(true)
		},
	}
	requestCtx = httptrace.WithClientTrace(requestCtx, trace)
	httpRequest, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		a.baseURL+mutationPath,
		nil,
	)
	if err != nil {
		return platform.DispatchResult{
			Disposition: platform.EffectPermanentFailed,
			ErrorCode:   "invalid_request",
		}
	}
	httpRequest.Body = io.NopCloser(bytes.NewReader(payload))
	httpRequest.ContentLength = int64(len(payload))
	httpRequest.GetBody = nil
	a.setHeaders(httpRequest)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Idempotency-Key", string(key))
	httpRequest.Header.Set("Waybill-Request-SHA256", binding.ProviderRequestHash)

	response, err := a.mutationClient.Do(httpRequest)
	if err != nil {
		disposition := platform.EffectRetryableFailed
		code := "transport_before_send"
		if wroteRequest.Load() {
			disposition = platform.EffectUnknown
			code = "transport_after_send"
		}
		return platform.DispatchResult{
			Disposition: disposition,
			ErrorCode:   code,
		}
	}
	defer response.Body.Close()
	raw, err := readLimited(response.Body)
	if err != nil {
		return platform.DispatchResult{
			Disposition: platform.EffectUnknown,
			ErrorCode:   "invalid_response",
		}
	}
	responseDigest := digest(raw)
	retryAfter := parseRetryAfter(response, nil, a.clock())

	var envelope effectEnvelope
	decoded := decodeStrict(raw, &envelope) == nil
	if decoded {
		retryAfter = parseRetryAfter(response, &envelope, a.clock())
	}
	retryAfter = capRetryAfter(
		retryAfter,
		binding.KeyExpiresAt,
		a.clock().UTC(),
	)
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		result, ok := validateApplied(envelope, binding.ProviderRequestHash, &mutation)
		if !decoded || !ok {
			return platform.DispatchResult{
				Disposition:    platform.EffectUnknown,
				ResponseDigest: responseDigest,
				ErrorCode:      "invalid_response",
				RetryAfter:     retryAfter,
			}
		}
		return platform.DispatchResult{
			Disposition:       platform.EffectSucceeded,
			Response:          result,
			ExternalRef:       envelope.ExternalRef,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
		}
	}
	if decoded && isConflictResponse(
		response.StatusCode,
		envelope,
		binding.ProviderRequestHash,
	) {
		return platform.DispatchResult{
			Disposition:       platform.EffectPermanentFailed,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
			ErrorCode:         "idempotency_conflict",
		}
	}
	if decoded && isRejectedResponse(response.StatusCode, envelope, binding.ProviderRequestHash) {
		return platform.DispatchResult{
			Disposition:       platform.EffectPermanentFailed,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
			ErrorCode:         "provider_rejected",
		}
	}
	return platform.DispatchResult{
		Disposition:       platform.EffectUnknown,
		ExternalRequestID: requestID(response, envelope.RequestID),
		ResponseDigest:    responseDigest,
		ErrorCode:         "provider_unknown",
		RetryAfter:        retryAfter,
	}
}

func (a *Adapter) Lookup(
	ctx context.Context,
	binding platform.EffectBinding,
	key domain.IdempotencyKey,
) platform.LookupResult {
	if key == "" || !a.SupportsRecovery(binding) {
		return platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   "binding_conflict",
		}
	}
	requestCtx, cancel := context.WithTimeout(ctx, a.requestTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodGet,
		a.baseURL+lookupPrefix+url.PathEscape(string(key)),
		nil,
	)
	if err != nil {
		return platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   "invalid_request",
		}
	}
	a.setHeaders(httpRequest)
	httpRequest.Header.Set("Waybill-Request-SHA256", binding.ProviderRequestHash)
	response, err := a.queryClient.Do(httpRequest)
	if err != nil {
		return platform.LookupResult{
			Disposition: platform.LookupPending,
			ErrorCode:   "lookup_unavailable",
		}
	}
	defer response.Body.Close()
	raw, err := readLimited(response.Body)
	if err != nil {
		return platform.LookupResult{
			Disposition: platform.LookupPending,
			ErrorCode:   "invalid_response",
		}
	}
	responseDigest := digest(raw)
	var envelope effectEnvelope
	decoded := decodeStrict(raw, &envelope) == nil
	retryAfter := parseRetryAfter(response, nil, a.clock())
	if decoded {
		retryAfter = parseRetryAfter(response, &envelope, a.clock())
	}
	retryAfter = capRetryAfter(
		retryAfter,
		binding.KeyExpiresAt,
		a.clock().UTC(),
	)

	if response.StatusCode == http.StatusNotFound {
		if a.clock().UTC().Before(binding.KeyCreatedAt.Add(binding.LookupConsistencyWindow)) {
			return platform.LookupResult{
				Disposition:    platform.LookupPending,
				ResponseDigest: responseDigest,
				ErrorCode:      "lookup_not_visible",
				RetryAfter:     retryAfter,
			}
		}
		if decoded &&
			envelope.Status == statusAbsent &&
			envelope.Action == binding.Action &&
			envelope.RequestHash == binding.ProviderRequestHash &&
			envelope.Authoritative {
			return platform.LookupResult{
				Disposition:       platform.LookupAbsent,
				ExternalRequestID: requestID(response, envelope.RequestID),
				ResponseDigest:    responseDigest,
			}
		}
		return platform.LookupResult{
			Disposition:    platform.LookupPending,
			ResponseDigest: responseDigest,
			ErrorCode:      "lookup_not_authoritative",
			RetryAfter:     retryAfter,
		}
	}
	if response.StatusCode != http.StatusOK || !decoded {
		return platform.LookupResult{
			Disposition:       platform.LookupPending,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
			ErrorCode:         "lookup_unavailable",
			RetryAfter:        retryAfter,
		}
	}
	if envelope.Action != binding.Action ||
		envelope.RequestHash != binding.ProviderRequestHash {
		return platform.LookupResult{
			Disposition:       platform.LookupConflict,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
			ErrorCode:         "lookup_conflict",
		}
	}
	switch envelope.Status {
	case statusApplied:
		result, ok := validateApplied(envelope, binding.ProviderRequestHash, nil)
		if !ok {
			return platform.LookupResult{
				Disposition:    platform.LookupConflict,
				ResponseDigest: responseDigest,
				ErrorCode:      "lookup_conflict",
			}
		}
		return platform.LookupResult{
			Disposition:       platform.LookupApplied,
			Response:          result,
			ExternalRef:       envelope.ExternalRef,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
		}
	case statusRejected:
		if !envelope.NoSideEffect {
			return platform.LookupResult{
				Disposition:    platform.LookupConflict,
				ResponseDigest: responseDigest,
				ErrorCode:      "lookup_conflict",
			}
		}
		return platform.LookupResult{
			Disposition:       platform.LookupRejected,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
			ErrorCode:         "provider_rejected",
		}
	case statusPending:
		return platform.LookupResult{
			Disposition:       platform.LookupPending,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
			ErrorCode:         "lookup_pending",
			RetryAfter:        retryAfter,
		}
	case statusConflict:
		return platform.LookupResult{
			Disposition:       platform.LookupConflict,
			ExternalRequestID: requestID(response, envelope.RequestID),
			ResponseDigest:    responseDigest,
			ErrorCode:         "lookup_conflict",
		}
	default:
		return platform.LookupResult{
			Disposition:    platform.LookupPending,
			ResponseDigest: responseDigest,
			ErrorCode:      "invalid_response",
			RetryAfter:     retryAfter,
		}
	}
}

func validateApplied(
	envelope effectEnvelope,
	requestHash string,
	mutation *reassignMutation,
) (json.RawMessage, bool) {
	if envelope.Status != statusApplied ||
		envelope.Action != domain.ActionReassign ||
		envelope.RequestHash != requestHash ||
		!validMetadataID(envelope.ExternalRef) ||
		envelope.Result == nil ||
		envelope.Result.OrderID == "" ||
		envelope.Result.WaybillID == "" ||
		envelope.Result.CarrierID == "" ||
		envelope.Result.Status == "" {
		return nil, false
	}
	if mutation != nil &&
		(envelope.Result.WaybillID != mutation.WaybillID ||
			envelope.Result.CarrierID != mutation.CarrierID) {
		return nil, false
	}
	resultMutation := reassignMutation{
		Action:    domain.ActionReassign,
		WaybillID: envelope.Result.WaybillID,
		CarrierID: envelope.Result.CarrierID,
	}
	payload, err := json.Marshal(resultMutation)
	if err != nil || digest(payload) != requestHash {
		return nil, false
	}
	result, err := json.Marshal(envelope.Result)
	return result, err == nil
}

func isConflictResponse(
	statusCode int,
	envelope effectEnvelope,
	requestHash string,
) bool {
	return statusCode == http.StatusConflict &&
		envelope.Status == statusConflict &&
		envelope.Action == domain.ActionReassign &&
		validDigest(envelope.RequestHash) &&
		envelope.RequestHash != requestHash &&
		envelope.ErrorCode == "idempotency_conflict"
}

func isRejectedResponse(
	statusCode int,
	envelope effectEnvelope,
	requestHash string,
) bool {
	switch statusCode {
	case http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusUnprocessableEntity:
	default:
		return false
	}
	return envelope.Status == statusRejected &&
		envelope.Action == domain.ActionReassign &&
		envelope.RequestHash == requestHash &&
		envelope.NoSideEffect
}

func parseRetryAfter(response *http.Response, envelope *effectEnvelope, now time.Time) time.Duration {
	if envelope != nil && envelope.RetryAfterSeconds > 0 {
		if envelope.RetryAfterSeconds <= int64(^uint64(0)>>1)/int64(time.Second) {
			return time.Duration(envelope.RetryAfterSeconds) * time.Second
		}
	}
	value := strings.TrimSpace(response.Header.Get("Retry-After"))
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
		if seconds <= int64(^uint64(0)>>1)/int64(time.Second) {
			return time.Duration(seconds) * time.Second
		}
	}
	if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}

func capRetryAfter(value time.Duration, expiresAt, now time.Time) time.Duration {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	if value > remaining {
		return remaining
	}
	return value
}

func requestID(response *http.Response, bodyValue string) string {
	for _, value := range []string{bodyValue, response.Header.Get("X-Request-ID")} {
		if validMetadataID(value) {
			return value
		}
	}
	return ""
}

func validMetadataID(value string) bool {
	return metadataIDPattern.MatchString(value)
}
