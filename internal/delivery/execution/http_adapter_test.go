package execution

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestHTTPAdapterDispatchAndLookupUseOneStableBinding(t *testing.T) {
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	var (
		mu            sync.Mutex
		dispatchCalls int
		lookupCalls   int
		requestDigest domain.ArtifactDigest
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Authorization") != "Bearer secret" ||
			request.Header.Get("Idempotency-Key") != "effect-key" {
			t.Errorf("headers = %#v", request.Header)
		}
		switch request.Method {
		case http.MethodPost:
			mu.Lock()
			dispatchCalls++
			mu.Unlock()
			raw, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
			}
			var payload adapterRequest
			if err := json.Unmarshal(raw, &payload); err != nil {
				t.Error(err)
			}
			requestDigest, err = domain.DigestCanonicalJSON(raw)
			if err != nil {
				t.Error(err)
			}
			writeAdapterEnvelope(t, response, http.StatusOK, adapterEnvelope{
				SchemaVersion: domain.EffectResultSchemaVersion,
				Status:        "applied",
				Action:        payload.Action,
				RequestDigest: requestDigest,
				ExternalRef:   "route-42",
				Result:        json.RawMessage(`{"accepted":true}`),
			})
		case http.MethodGet:
			mu.Lock()
			lookupCalls++
			mu.Unlock()
			writeAdapterEnvelope(t, response, http.StatusNotFound, adapterEnvelope{
				SchemaVersion: domain.EffectResultSchemaVersion,
				Status:        "absent",
				Action:        domain.EffectCreateRoute,
				RequestDigest: requestDigest,
				Authoritative: true,
			})
		default:
			response.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	adapter, err := NewHTTPAdapter(HTTPAdapterConfig{
		BaseURL:                 server.URL,
		Token:                   "secret",
		AdapterID:               "tms-v1",
		ContractVersion:         "v1",
		Actions:                 []domain.EffectAction{domain.EffectCreateRoute},
		KeyRetention:            48 * time.Hour,
		LookupConsistencyWindow: time.Minute,
		RequestTimeout:          5 * time.Second,
		Clock:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	parameters := json.RawMessage(`{"plan_id":"plan-1"}`)
	digest, err := domain.DigestCanonicalJSON(parameters)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := adapter.Bind(domain.EffectPreview{
		Action:                         domain.EffectCreateRoute,
		Target:                         "plan-1/duty/0",
		Parameters:                     parameters,
		ParametersDigest:               digest,
		AdapterID:                      "tms-v1",
		ContractVersion:                "v1",
		KeyRetentionSeconds:            int64((48 * time.Hour) / time.Second),
		LookupConsistencyWindowSeconds: int64(time.Minute / time.Second),
	}, "effect-key", now)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, err := adapter.Dispatch(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	var dispatchedEnvelope adapterEnvelope
	if err := json.Unmarshal(dispatched.Response, &dispatchedEnvelope); err != nil {
		t.Fatal(err)
	}
	if dispatched.Disposition != DispositionSucceeded ||
		dispatched.ExternalRef != "route-42" ||
		string(dispatchedEnvelope.Result) != `{"accepted":true}` ||
		dispatched.ResponseDigest != digestBytes(dispatched.Response) {
		t.Fatalf("dispatch result = %+v", dispatched)
	}

	now = now.Add(2 * time.Minute)
	lookedUp, err := adapter.Lookup(t.Context(), binding, binding.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if lookedUp.Disposition != DispositionAuthoritativeAbsent {
		t.Fatalf("lookup result = %+v", lookedUp)
	}
	mu.Lock()
	defer mu.Unlock()
	if dispatchCalls != 1 || lookupCalls != 1 {
		t.Fatalf("calls = dispatch:%d lookup:%d", dispatchCalls, lookupCalls)
	}
}

func TestHTTPAdapterRequiresAuthoritativeAbsentAfterConsistencyWindow(t *testing.T) {
	now := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	parameters := json.RawMessage(`{"trip_id":"trip-1"}`)
	digest, err := domain.DigestCanonicalJSON(parameters)
	if err != nil {
		t.Fatal(err)
	}
	var bindingRequestDigest domain.ArtifactDigest
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		writeAdapterEnvelope(t, response, http.StatusNotFound, adapterEnvelope{
			SchemaVersion: domain.EffectResultSchemaVersion,
			Status:        "absent",
			Action:        domain.EffectPublishLoad,
			RequestDigest: bindingRequestDigest,
			Authoritative: true,
		})
	}))
	defer server.Close()
	adapter, err := NewHTTPAdapter(HTTPAdapterConfig{
		BaseURL:                 server.URL,
		Token:                   "secret",
		AdapterID:               "wms-v1",
		ContractVersion:         "v1",
		Actions:                 []domain.EffectAction{domain.EffectPublishLoad},
		KeyRetention:            48 * time.Hour,
		LookupConsistencyWindow: time.Hour,
		RequestTimeout:          time.Second,
		Clock:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := adapter.Bind(domain.EffectPreview{
		Action:                         domain.EffectPublishLoad,
		Target:                         "trip-1",
		Parameters:                     parameters,
		ParametersDigest:               digest,
		AdapterID:                      "wms-v1",
		ContractVersion:                "v1",
		KeyRetentionSeconds:            int64((48 * time.Hour) / time.Second),
		LookupConsistencyWindowSeconds: int64(time.Hour / time.Second),
	}, "effect-key", now)
	if err != nil {
		t.Fatal(err)
	}
	bindingRequestDigest = binding.RequestDigest
	dispatchStartedAt := now.Add(2 * time.Hour)
	now = dispatchStartedAt.Add(30 * time.Minute)
	result, err := adapter.Lookup(t.Context(), binding, dispatchStartedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != DispositionPending ||
		result.ErrorCode != "lookup_not_authoritative" {
		t.Fatalf("lookup result = %+v", result)
	}
}

func TestHTTPAdapterRejectsUntrustedConfigurationAndBindingDrift(t *testing.T) {
	if _, err := NewHTTPAdapter(HTTPAdapterConfig{
		BaseURL:                 "http://example.com",
		Token:                   "secret",
		AdapterID:               "tms-v1",
		ContractVersion:         "v1",
		Actions:                 []domain.EffectAction{domain.EffectCreateRoute},
		KeyRetention:            time.Hour,
		LookupConsistencyWindow: time.Second,
		RequestTimeout:          time.Second,
	}); err == nil {
		t.Fatal("adapter accepted cleartext non-loopback origin")
	}
}

func writeAdapterEnvelope(
	t *testing.T,
	response http.ResponseWriter,
	status int,
	value adapterEnvelope,
) {
	t.Helper()
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		t.Error(err)
	}
}
