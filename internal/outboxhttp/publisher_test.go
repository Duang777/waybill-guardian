package outboxhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/outbox"
)

func TestPublisherSendsStructuredCloudEventWithBearerToken(t *testing.T) {
	var received map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/cloudevents+json" {
			t.Errorf("Content-Type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	publisher, err := New(Config{
		URL:    server.URL + "/events",
		Token:  "secret-token",
		Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := publisher.Publish(t.Context(), testEvent())
	if result != (outbox.PublishResult{Disposition: outbox.Published}) {
		t.Fatalf("publish result = %+v", result)
	}
	if received["specversion"] != "1.0" ||
		received["id"] != "event-1" ||
		received["source"] != "urn:waybill-guardian:incidents" ||
		received["type"] != "com.waybill.incident.snapshot.v1" ||
		received["subject"] != "incident/incident-1" ||
		received["time"] != "2026-10-10T12:34:56.123Z" ||
		received["datacontenttype"] != "application/json" ||
		received["dataschema"] != "urn:test:schema:v1" {
		t.Fatalf("structured event = %#v", received)
	}
	data, ok := received["data"].(map[string]any)
	if !ok || data["incident_id"] != "incident-1" {
		t.Fatalf("structured event data = %#v", received["data"])
	}
}

func TestPublisherDoesNotFollowRedirects(t *testing.T) {
	var targetRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	publisher, err := New(Config{
		URL:    redirect.URL,
		Token:  "secret-token",
		Client: redirect.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result := publisher.Publish(t.Context(), testEvent())
	if result != (outbox.PublishResult{
		Disposition: outbox.PermanentFailed,
		ErrorCode:   "http_permanent_status",
	}) {
		t.Fatalf("publish result = %+v", result)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", targetRequests.Load())
	}
}

func TestPublisherStatusClassification(t *testing.T) {
	tests := []struct {
		status int
		want   outbox.PublishResult
	}{
		{status: 199, want: outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "http_unexpected_status",
		}},
		{status: 200, want: outbox.PublishResult{Disposition: outbox.Published}},
		{status: 299, want: outbox.PublishResult{Disposition: outbox.Published}},
		{status: 300, want: outbox.PublishResult{
			Disposition: outbox.PermanentFailed,
			ErrorCode:   "http_permanent_status",
		}},
		{status: 408, want: outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "http_retryable_status",
		}},
		{status: 425, want: outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "http_retryable_status",
		}},
		{status: 429, want: outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "http_retryable_status",
		}},
		{status: 499, want: outbox.PublishResult{
			Disposition: outbox.PermanentFailed,
			ErrorCode:   "http_permanent_status",
		}},
		{status: 500, want: outbox.PublishResult{
			Disposition: outbox.RetryableFailed,
			ErrorCode:   "http_retryable_status",
		}},
	}
	for _, test := range tests {
		if got := classifyStatus(test.status); got != test.want {
			t.Fatalf("classifyStatus(%d) = %+v, want %+v", test.status, got, test.want)
		}
	}
}

func TestPublisherCapsResponseBodyRead(t *testing.T) {
	body := &countingBody{remaining: responseBodyLimit * 2}
	publisher, err := New(Config{
		URL:   "https://events.example.test/v1/events",
		Token: "secret-token",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusAccepted,
				Body:       body,
				Header:     make(http.Header),
			}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := publisher.Publish(t.Context(), testEvent())
	if result.Disposition != outbox.Published {
		t.Fatalf("publish result = %+v", result)
	}
	if got := body.read.Load(); got != responseBodyLimit {
		t.Fatalf("response bytes read = %d, want %d", got, responseBodyLimit)
	}
}

func TestPublisherClassifiesNetworkFailureAsRetryable(t *testing.T) {
	publisher, err := New(Config{
		URL:   "https://events.example.test/v1/events",
		Token: "secret-token",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := publisher.Publish(t.Context(), testEvent())
	if result != (outbox.PublishResult{
		Disposition: outbox.RetryableFailed,
		ErrorCode:   "network_error",
	}) {
		t.Fatalf("publish result = %+v", result)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	tests := []Config{
		{},
		{URL: "https://events.example.test"},
		{URL: "http://events.example.test", Token: "token"},
		{URL: "https://user@events.example.test", Token: "token"},
		{URL: "https://events.example.test?token=bad", Token: "token"},
		{URL: "https://events.example.test", Token: "two words"},
		{URL: "https://events.example.test", Token: "token", Timeout: -time.Second},
	}
	for _, config := range tests {
		if _, err := New(config); err == nil {
			t.Fatalf("New accepted invalid config %+v", config)
		}
	}
	if _, err := New(Config{
		URL:   "http://127.0.0.1:8081/v1/events",
		Token: "token",
	}); err != nil {
		t.Fatalf("New rejected loopback test endpoint: %v", err)
	}
}

func TestPublisherRejectsInvalidStoredEventWithoutRequest(t *testing.T) {
	var requests atomic.Int64
	publisher, err := New(Config{
		URL:   "https://events.example.test/v1/events",
		Token: "secret-token",
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, context.Canceled
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := testEvent()
	event.Data = json.RawMessage(`not-json`)
	result := publisher.Publish(t.Context(), event)
	if result != (outbox.PublishResult{
		Disposition: outbox.PermanentFailed,
		ErrorCode:   "invalid_event",
	}) {
		t.Fatalf("publish result = %+v", result)
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
}

func testEvent() outbox.Event {
	return outbox.Event{
		Source:          "urn:waybill-guardian:incidents",
		ID:              "event-1",
		Type:            "com.waybill.incident.snapshot.v1",
		Subject:         "incident/incident-1",
		Time:            time.Date(2026, 10, 10, 12, 34, 56, 123000000, time.UTC),
		DataContentType: "application/json",
		DataSchema:      "urn:test:schema:v1",
		Data:            json.RawMessage(`{"incident_id":"incident-1"}`),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type countingBody struct {
	remaining int64
	read      atomic.Int64
}

func (b *countingBody) Read(buffer []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	count := min(int64(len(buffer)), b.remaining)
	for index := range int(count) {
		buffer[index] = 'x'
	}
	b.remaining -= count
	b.read.Add(count)
	return int(count), nil
}

func (*countingBody) Close() error {
	return nil
}

func TestMarshalStructuredEventRejectsMissingFields(t *testing.T) {
	event := testEvent()
	tests := []outbox.Event{
		{},
		func() outbox.Event {
			value := event
			value.ID = strings.Repeat(" ", 3)
			return value
		}(),
		func() outbox.Event {
			value := event
			value.Time = time.Time{}
			return value
		}(),
	}
	for _, value := range tests {
		if _, err := marshalStructuredEvent(value); err == nil {
			t.Fatalf("marshalStructuredEvent accepted %+v", value)
		}
	}
}
