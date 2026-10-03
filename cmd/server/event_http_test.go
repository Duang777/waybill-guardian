package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/events"
	"github.com/Duang777/waybill-guardian/internal/httpauth"
	"github.com/Duang777/waybill-guardian/internal/metrics"
	"github.com/golang-jwt/jwt/v5"
)

func TestJSONLHandlerDoesNotRegisterEventIngress(t *testing.T) {
	request := newEventRequest(t, localEventBody(time.Now().UTC(), "event-1"))
	response := httptest.NewRecorder()

	newHandler(nil, newLocalAccess(t)).ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestJWTEventIngressRequiresAuthenticationBeforeReadingEvent(t *testing.T) {
	access, _ := newJWTAccess(t)
	store := &stubEventStore{}
	handler := newHandlerWithEvents(nil, access, store, nil)
	request := newEventRequest(t, localEventBody(time.Now().UTC(), "event-1"))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertRecorderProblem(t, response, http.StatusUnauthorized, "unauthenticated")
	if store.callCount() != 0 {
		t.Fatalf("ingest calls = %d, want 0", store.callCount())
	}
}

func TestEventIngressReturnsStoredResultAndReplayHeader(t *testing.T) {
	rawResult := []byte(
		`{"disposition":"applied","event_id":"event-1","incident_id":"incident-1","incident_version":1}`,
	)
	result, err := events.RestoreResult(rawResult)
	if err != nil {
		t.Fatal(err)
	}
	result.Replayed = true
	store := &stubEventStore{result: result}
	observer := &stubIngestObserver{}
	handler := newHandlerWithEvents(nil, newLocalAccess(t), store, observer)
	request := newEventRequest(t, localEventBody(time.Now().UTC(), "event-1"))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", response.Code, response.Body.Bytes())
	}
	if got := response.Header().Get("Idempotent-Replayed"); got != "true" {
		t.Fatalf("Idempotent-Replayed = %q, want true", got)
	}
	if !bytes.Equal(response.Body.Bytes(), rawResult) {
		t.Fatalf("response body = %s, want exact stored bytes %s", response.Body.Bytes(), rawResult)
	}
	if store.callCount() != 1 {
		t.Fatalf("ingest calls = %d, want 1", store.callCount())
	}
	if observer.count(metrics.IngestReplayed) != 1 {
		t.Fatalf("ingest outcomes = %v", observer.outcomes)
	}
}

func TestEventIngressRejectsUnauthorizedSourceBeforeStorage(t *testing.T) {
	store := &stubEventStore{}
	observer := &stubIngestObserver{}
	handler := newHandlerWithEvents(nil, newLocalAccess(t), store, observer)
	body := bytes.ReplaceAll(
		localEventBody(time.Now().UTC(), "event-1"),
		[]byte(httpauth.LocalEventSource),
		[]byte("urn:tms:region-east"),
	)
	request := newEventRequest(t, body)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertRecorderProblem(t, response, http.StatusForbidden, "forbidden")
	if store.callCount() != 0 {
		t.Fatalf("ingest calls = %d, want 0", store.callCount())
	}
	if observer.count(metrics.IngestRejected) != 1 {
		t.Fatalf("ingest outcomes = %v", observer.outcomes)
	}
}

func TestEventIngressMapsDecodeAndStoreErrors(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        []byte
		storeErr    error
		status      int
		code        string
	}{
		{
			name:        "content type",
			contentType: "application/json",
			body:        localEventBody(time.Now().UTC(), "event-1"),
			status:      http.StatusUnsupportedMediaType,
			code:        "invalid_content_type",
		},
		{
			name:        "invalid event",
			contentType: "application/cloudevents+json",
			body:        []byte(`{}`),
			status:      http.StatusBadRequest,
			code:        "invalid_cloudevent",
		},
		{
			name:        "identity conflict",
			contentType: "application/cloudevents+json",
			body:        localEventBody(time.Now().UTC(), "event-conflict"),
			storeErr:    events.ErrEventIdentityConflict,
			status:      http.StatusConflict,
			code:        "event_identity_conflict",
		},
		{
			name:        "legacy identity",
			contentType: "application/cloudevents+json",
			body:        localEventBody(time.Now().UTC(), "event-legacy"),
			storeErr:    events.ErrLegacyEventIdentity,
			status:      http.StatusConflict,
			code:        "event_identity_legacy",
		},
		{
			name:        "store unavailable",
			contentType: "application/cloudevents+json",
			body:        localEventBody(time.Now().UTC(), "event-unavailable"),
			storeErr:    events.ErrEventsUnavailable,
			status:      http.StatusServiceUnavailable,
			code:        "events_unavailable",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &stubEventStore{err: test.storeErr}
			observer := &stubIngestObserver{}
			handler := newHandlerWithEvents(nil, newLocalAccess(t), store, observer)
			request := httptest.NewRequest(
				http.MethodPost,
				"http://127.0.0.1/v1/events",
				bytes.NewReader(test.body),
			)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			assertRecorderProblem(t, response, test.status, test.code)
			wantOutcome := metrics.IngestRejected
			if test.storeErr != nil {
				wantOutcome = metrics.IngestFailed
			}
			if observer.count(wantOutcome) != 1 {
				t.Fatalf("ingest outcomes = %v", observer.outcomes)
			}
		})
	}
}

func TestJWTEventIngressRequiresProducerClaimsAndWaybillScope(t *testing.T) {
	access, privateKey := newJWTAccess(t)
	store := &stubEventStore{result: events.Result{
		EventID:         "event-1",
		IncidentID:      domain.IncidentID("incident-1"),
		IncidentVersion: 1,
		Disposition:     events.DispositionApplied,
	}}
	handler := newHandlerWithEvents(nil, access, store, nil)
	now := time.Now().UTC()
	body := localEventBody(now, "event-1")

	allowed := eventProducerToken(
		t,
		privateKey,
		[]string{httpauth.LocalEventSource},
		[]string{string(events.DelayDetectedType)},
		[]string{"YD2026101001"},
	)
	request := newEventRequest(t, body)
	request.Header.Set("Authorization", "Bearer "+allowed)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("allowed status = %d, want 202; body=%s", response.Code, response.Body.Bytes())
	}

	wrongWaybill := eventProducerToken(
		t,
		privateKey,
		[]string{httpauth.LocalEventSource},
		[]string{string(events.DelayDetectedType)},
		[]string{"YD2026101002"},
	)
	request = newEventRequest(t, body)
	request.Header.Set("Authorization", "Bearer "+wrongWaybill)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertRecorderProblem(t, response, http.StatusForbidden, "forbidden")

	viewer := authToken(t, privateKey, "tenant-a", []string{"viewer"}, true, nil)
	request = newEventRequest(t, body)
	request.Header.Set("Authorization", "Bearer "+viewer)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertRecorderProblem(t, response, http.StatusForbidden, "forbidden")
	if store.callCount() != 1 {
		t.Fatalf("ingest calls = %d, want only the authorized request", store.callCount())
	}
}

func TestEventIngressRejectsDeclaredOversizedBody(t *testing.T) {
	store := &stubEventStore{}
	handler := newHandlerWithEvents(nil, newLocalAccess(t), store, nil)
	request := newEventRequest(t, []byte(`{}`))
	request.ContentLength = events.MaxBodyBytes + 1
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	assertRecorderProblem(t, response, http.StatusRequestEntityTooLarge, "body_too_large")
	if store.callCount() != 0 {
		t.Fatalf("ingest calls = %d, want 0", store.callCount())
	}
}

type stubEventStore struct {
	mu         sync.Mutex
	result     events.Result
	err        error
	calls      int
	submission events.Submission
}

func (s *stubEventStore) IngestEvent(
	_ context.Context,
	submission events.Submission,
) (events.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.submission = submission
	return s.result, s.err
}

func (s *stubEventStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type stubIngestObserver struct {
	mu       sync.Mutex
	outcomes []metrics.IngestOutcome
}

func (o *stubIngestObserver) ObserveIngest(outcome metrics.IngestOutcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.outcomes = append(o.outcomes, outcome)
}

func (o *stubIngestObserver) count(want metrics.IngestOutcome) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	count := 0
	for _, outcome := range o.outcomes {
		if outcome == want {
			count++
		}
	}
	return count
}

func newEventRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1/v1/events",
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/cloudevents+json")
	return request
}

func localEventBody(now time.Time, eventID string) []byte {
	recordTime := now.Add(-time.Minute).UTC()
	eventTime := recordTime.Add(-time.Minute)
	return []byte(fmt.Sprintf(`{
		"specversion":"1.0",
		"id":%q,
		"source":%q,
		"type":%q,
		"subject":"waybill/YD2026101001",
		"time":%q,
		"datacontenttype":"application/json",
		"dataschema":%q,
		"data":{
			"waybill_id":"YD2026101001",
			"incident_key":"delay-http-1",
			"source_version":1,
			"event_time":%q,
			"record_time":%q,
			"location":{"code":"MY-N-SERVICE","name":"Mianyang North Service Area"},
			"business_step":"transporting",
			"reason_code":"stop_duration_exceeded",
			"observations":{"stop_minutes":360}
		}
	}`,
		eventID,
		httpauth.LocalEventSource,
		events.DelayDetectedType,
		recordTime.Format(time.RFC3339Nano),
		events.DelayDetectedSchema,
		eventTime.Format(time.RFC3339Nano),
		recordTime.Format(time.RFC3339Nano),
	))
}

func eventProducerToken(
	t *testing.T,
	privateKey any,
	sources []string,
	eventTypes []string,
	waybillIDs []string,
) string {
	t.Helper()
	now := time.Now()
	claims := testJWTClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://issuer.example",
			Subject:   "producer-42",
			Audience:  jwt.ClaimStrings{"waybill-guardian"},
			IssuedAt:  jwt.NewNumericDate(now.Add(-time.Minute)),
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		TenantID:     "tenant-a",
		Roles:        []string{"event_producer"},
		WaybillIDs:   waybillIDs,
		EventSources: sources,
		EventTypes:   eventTypes,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func assertRecorderProblem(
	t *testing.T,
	response *httptest.ResponseRecorder,
	status int,
	code string,
) {
	t.Helper()
	assertProblem(t, response.Result(), status, code)
}
