package tmssandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
)

func TestDispatchSendsStableIdentityAndValidatesReceipt(t *testing.T) {
	var binding platform.EffectBinding
	var postCount atomic.Int32
	server := providerServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != mutationPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		postCount.Add(1)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" ||
			r.Header.Get("Waybill-Account") != "tenant-a" ||
			r.Header.Get("Idempotency-Key") != "effect-key" ||
			r.Header.Get("Waybill-Request-SHA256") != digest(raw) {
			t.Errorf("mutation headers = %#v", r.Header)
		}
		var request reassignMutation
		if err := decodeStrict(raw, &request); err != nil {
			t.Error(err)
		}
		writeJSON(t, w, http.StatusCreated, appliedEnvelope(binding.ProviderRequestHash))
	})
	defer server.Close()

	adapter := openTestAdapter(t, server.URL, func() time.Time { return testNow })
	var err error
	binding, err = adapter.Bind(testEffectRequest(), "effect-key", testNow)
	if err != nil {
		t.Fatal(err)
	}
	result := adapter.Dispatch(
		context.Background(),
		binding,
		testEffectRequest(),
		"effect-key",
	)
	if result.Disposition != platform.EffectSucceeded ||
		result.ExternalRef != "reassign/RA-42" ||
		result.ExternalRequestID != "request-42" ||
		result.ResponseDigest == "" ||
		result.ErrorCode != "" {
		t.Fatalf("dispatch result = %+v", result)
	}
	if string(result.Response) !=
		`{"order_id":"RA-42","waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42","status":"accepted"}` {
		t.Fatalf("dispatch response = %s", result.Response)
	}
	if postCount.Load() != 1 {
		t.Fatalf("POST count = %d, want 1", postCount.Load())
	}
}

func TestDispatchClassifiesProviderResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		body        func(platform.EffectBinding) any
		rawBody     string
		retryAfter  string
		disposition platform.EffectDisposition
		errorCode   string
		wantRetry   time.Duration
	}{
		{
			name:   "idempotency conflict",
			status: http.StatusConflict,
			body: func(binding platform.EffectBinding) any {
				return effectEnvelope{
					Status:      statusConflict,
					Action:      domain.ActionReassign,
					RequestHash: strings.Repeat("b", 64),
					RequestID:   "request-conflict",
					ErrorCode:   "idempotency_conflict",
				}
			},
			disposition: platform.EffectPermanentFailed,
			errorCode:   "idempotency_conflict",
		},
		{
			name:   "conflict repeats current hash",
			status: http.StatusConflict,
			body: func(binding platform.EffectBinding) any {
				return effectEnvelope{
					Status:      statusConflict,
					Action:      domain.ActionReassign,
					RequestHash: binding.ProviderRequestHash,
					ErrorCode:   "idempotency_conflict",
				}
			},
			disposition: platform.EffectUnknown,
			errorCode:   "provider_unknown",
		},
		{
			name:   "conflict has invalid hash",
			status: http.StatusConflict,
			body: func(platform.EffectBinding) any {
				return effectEnvelope{
					Status:      statusConflict,
					Action:      domain.ActionReassign,
					RequestHash: "different",
					ErrorCode:   "idempotency_conflict",
				}
			},
			disposition: platform.EffectUnknown,
			errorCode:   "provider_unknown",
		},
		{
			name:   "explicit rejection",
			status: http.StatusUnprocessableEntity,
			body: func(binding platform.EffectBinding) any {
				return effectEnvelope{
					Status:       statusRejected,
					Action:       domain.ActionReassign,
					RequestHash:  binding.ProviderRequestHash,
					RequestID:    "request-rejected",
					ErrorCode:    "carrier_unavailable",
					NoSideEffect: true,
				}
			},
			disposition: platform.EffectPermanentFailed,
			errorCode:   "provider_rejected",
		},
		{
			name:        "server error",
			status:      http.StatusServiceUnavailable,
			rawBody:     `{"error":"unavailable"}`,
			retryAfter:  "3",
			disposition: platform.EffectUnknown,
			errorCode:   "provider_unknown",
			wantRetry:   3 * time.Second,
		},
		{
			name:        "malformed success",
			status:      http.StatusOK,
			rawBody:     `{"status":`,
			disposition: platform.EffectUnknown,
			errorCode:   "invalid_response",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var binding platform.EffectBinding
			server := providerServer(t, func(w http.ResponseWriter, r *http.Request) {
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				if test.body != nil {
					writeJSON(t, w, test.status, test.body(binding))
					return
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.rawBody)
			})
			defer server.Close()
			adapter := openTestAdapter(t, server.URL, func() time.Time { return testNow })
			var err error
			binding, err = adapter.Bind(testEffectRequest(), "effect-key", testNow)
			if err != nil {
				t.Fatal(err)
			}

			result := adapter.Dispatch(
				context.Background(),
				binding,
				testEffectRequest(),
				"effect-key",
			)
			if result.Disposition != test.disposition ||
				result.ErrorCode != test.errorCode ||
				result.RetryAfter != test.wantRetry {
				t.Fatalf("dispatch result = %+v", result)
			}
		})
	}
}

func TestDispatchDistinguishesBeforeAndAfterSendFailures(t *testing.T) {
	server := manifestServer(t)
	defer server.Close()
	adapter := openTestAdapter(t, server.URL, func() time.Time { return testNow })
	binding, err := adapter.Bind(testEffectRequest(), "effect-key", testNow)
	if err != nil {
		t.Fatal(err)
	}
	adapter.mutationClient.Transport = mutationRoundTripper{
		base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	}
	result := adapter.Dispatch(
		context.Background(),
		binding,
		testEffectRequest(),
		"effect-key",
	)
	if result.Disposition != platform.EffectRetryableFailed ||
		result.ErrorCode != "transport_before_send" {
		t.Fatalf("dispatch result = %+v", result)
	}
}

func TestDispatchDoesNotSendExpiredKey(t *testing.T) {
	var postCount atomic.Int32
	server := providerServer(t, func(w http.ResponseWriter, r *http.Request) {
		postCount.Add(1)
		http.Error(w, "unexpected mutation", http.StatusInternalServerError)
	})
	defer server.Close()
	adapter := openTestAdapter(t, server.URL, func() time.Time {
		return testNow.Add(3 * time.Hour)
	})
	binding, err := adapter.Bind(testEffectRequest(), "effect-key", testNow)
	if err != nil {
		t.Fatal(err)
	}

	result := adapter.Dispatch(
		context.Background(),
		binding,
		testEffectRequest(),
		"effect-key",
	)
	if result.Disposition != platform.EffectUnknown || result.ErrorCode != "key_expired" {
		t.Fatalf("dispatch result = %+v", result)
	}
	if postCount.Load() != 0 {
		t.Fatalf("expired key sent %d mutations", postCount.Load())
	}
}

func TestLookupUsesExplicitProviderEvidence(t *testing.T) {
	tests := []struct {
		name        string
		now         time.Time
		status      int
		body        func(platform.EffectBinding) any
		disposition platform.LookupDisposition
		errorCode   string
	}{
		{
			name:   "applied",
			now:    testNow.Add(6 * time.Second),
			status: http.StatusOK,
			body: func(binding platform.EffectBinding) any {
				return appliedEnvelope(binding.ProviderRequestHash)
			},
			disposition: platform.LookupApplied,
		},
		{
			name:   "rejected",
			now:    testNow.Add(6 * time.Second),
			status: http.StatusOK,
			body: func(binding platform.EffectBinding) any {
				return effectEnvelope{
					Status:       statusRejected,
					Action:       domain.ActionReassign,
					RequestHash:  binding.ProviderRequestHash,
					RequestID:    "request-rejected",
					NoSideEffect: true,
				}
			},
			disposition: platform.LookupRejected,
			errorCode:   "provider_rejected",
		},
		{
			name:   "pending",
			now:    testNow.Add(6 * time.Second),
			status: http.StatusOK,
			body: func(binding platform.EffectBinding) any {
				return effectEnvelope{
					Status:            statusPending,
					Action:            domain.ActionReassign,
					RequestHash:       binding.ProviderRequestHash,
					RequestID:         "request-pending",
					RetryAfterSeconds: 4,
				}
			},
			disposition: platform.LookupPending,
			errorCode:   "lookup_pending",
		},
		{
			name:   "authoritative absent",
			now:    testNow.Add(6 * time.Second),
			status: http.StatusNotFound,
			body: func(binding platform.EffectBinding) any {
				return effectEnvelope{
					Status:        statusAbsent,
					Action:        domain.ActionReassign,
					RequestHash:   binding.ProviderRequestHash,
					RequestID:     "request-absent",
					Authoritative: true,
				}
			},
			disposition: platform.LookupAbsent,
		},
		{
			name:   "not visible inside consistency window",
			now:    testNow.Add(2 * time.Second),
			status: http.StatusNotFound,
			body: func(binding platform.EffectBinding) any {
				return effectEnvelope{
					Status:        statusAbsent,
					Action:        domain.ActionReassign,
					RequestHash:   binding.ProviderRequestHash,
					Authoritative: true,
				}
			},
			disposition: platform.LookupPending,
			errorCode:   "lookup_not_visible",
		},
		{
			name:   "hash conflict",
			now:    testNow.Add(6 * time.Second),
			status: http.StatusOK,
			body: func(platform.EffectBinding) any {
				return effectEnvelope{
					Status:      statusApplied,
					Action:      domain.ActionReassign,
					RequestHash: "different",
				}
			},
			disposition: platform.LookupConflict,
			errorCode:   "lookup_conflict",
		},
		{
			name:   "applied result target conflict",
			now:    testNow.Add(6 * time.Second),
			status: http.StatusOK,
			body: func(binding platform.EffectBinding) any {
				envelope := appliedEnvelope(binding.ProviderRequestHash)
				envelope.Result.CarrierID = "CARRIER-OTHER"
				return envelope
			},
			disposition: platform.LookupConflict,
			errorCode:   "lookup_conflict",
		},
		{
			name:   "lookup unavailable",
			now:    testNow.Add(6 * time.Second),
			status: http.StatusServiceUnavailable,
			body: func(platform.EffectBinding) any {
				return map[string]string{"error": "unavailable"}
			},
			disposition: platform.LookupPending,
			errorCode:   "lookup_unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var binding platform.EffectBinding
			server := providerServer(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSON(t, w, test.status, test.body(binding))
			})
			defer server.Close()
			adapter := openTestAdapter(t, server.URL, func() time.Time { return test.now })
			var err error
			binding, err = adapter.Bind(testEffectRequest(), "effect-key", testNow)
			if err != nil {
				t.Fatal(err)
			}
			result := adapter.Lookup(context.Background(), binding, "effect-key")
			if result.Disposition != test.disposition || result.ErrorCode != test.errorCode {
				t.Fatalf("lookup result = %+v", result)
			}
			if test.disposition == platform.LookupApplied &&
				(string(result.Response) == "" || result.ExternalRef == "") {
				t.Fatalf("applied lookup omitted result metadata: %+v", result)
			}
			if test.name == "pending" && result.RetryAfter != 4*time.Second {
				t.Fatalf("retry after = %s, want 4s", result.RetryAfter)
			}
		})
	}
}

func TestMutationIsNotReplayedAfterProviderCommitsAndDisconnects(t *testing.T) {
	var binding platform.EffectBinding
	var postCount atomic.Int32
	var lookupCount atomic.Int32
	var remoteMu sync.Mutex
	var manifestRemote, mutationRemote string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == manifestPath:
			remoteMu.Lock()
			manifestRemote = r.RemoteAddr
			remoteMu.Unlock()
			writeJSON(t, w, http.StatusOK, validManifest())
		case r.URL.Path == mutationPath && r.Method == http.MethodPost:
			_, _ = io.Copy(io.Discard, r.Body)
			postCount.Add(1)
			remoteMu.Lock()
			mutationRemote = r.RemoteAddr
			remoteMu.Unlock()
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
		case r.URL.Path == lookupPrefix+"effect-key":
			lookupCount.Add(1)
			writeJSON(t, w, http.StatusOK, appliedEnvelope(binding.ProviderRequestHash))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter := openTestAdapter(t, server.URL, func() time.Time {
		return testNow.Add(6 * time.Second)
	})
	var err error
	binding, err = adapter.Bind(testEffectRequest(), "effect-key", testNow)
	if err != nil {
		t.Fatal(err)
	}
	dispatched := adapter.Dispatch(
		context.Background(),
		binding,
		testEffectRequest(),
		"effect-key",
	)
	if dispatched.Disposition != platform.EffectUnknown {
		t.Fatalf("dispatch result = %+v, want unknown", dispatched)
	}
	lookedUp := adapter.Lookup(context.Background(), binding, "effect-key")
	if lookedUp.Disposition != platform.LookupApplied {
		t.Fatalf("lookup result = %+v, want applied", lookedUp)
	}
	if postCount.Load() != 1 || lookupCount.Load() != 1 {
		t.Fatalf("requests = POST:%d GET:%d, want 1 each", postCount.Load(), lookupCount.Load())
	}
	remoteMu.Lock()
	defer remoteMu.Unlock()
	if manifestRemote == "" || manifestRemote != mutationRemote {
		t.Fatalf(
			"manifest connection %q was not reused for mutation connection %q",
			manifestRemote,
			mutationRemote,
		)
	}
}

func TestMutationTransportRejectsReplayableBody(t *testing.T) {
	var called atomic.Bool
	transport := mutationRoundTripper{base: roundTripFunc(func(
		*http.Request,
	) (*http.Response, error) {
		called.Store(true)
		return nil, fmt.Errorf("unexpected transport call")
	})}
	request, err := http.NewRequest(
		http.MethodPost,
		"https://sandbox.example.test/v1/reassignments",
		bytes.NewReader([]byte(`{"ok":true}`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); !errors.Is(err, ErrReplayableMutation) {
		t.Fatalf("error = %v, want ErrReplayableMutation", err)
	}
	if called.Load() {
		t.Fatal("replayable request reached the base transport")
	}
}

func appliedEnvelope(requestHash string) effectEnvelope {
	return effectEnvelope{
		Status:      statusApplied,
		Action:      domain.ActionReassign,
		RequestHash: requestHash,
		ExternalRef: "reassign/RA-42",
		RequestID:   "request-42",
		Result: &platform.ReassignOrder{
			OrderID:   "RA-42",
			WaybillID: "YD2026101001",
			CarrierID: "CARRIER-SW-42",
			Status:    "accepted",
		},
	}
}

func providerServer(
	t *testing.T,
	effectHandler http.HandlerFunc,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == manifestPath {
			writeJSON(t, w, http.StatusOK, validManifest())
			return
		}
		effectHandler(w, r)
	}))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
