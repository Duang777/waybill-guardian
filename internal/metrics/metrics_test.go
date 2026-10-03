package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/outbox"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecorderExposesFixedCardinalityMetrics(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 2, 0, 0, time.UTC)
	recorder := New(func() time.Time { return now })
	oldest := now.Add(-90 * time.Second)

	recorder.ObserveIngest(IngestAccepted)
	recorder.ObserveIngest(IngestReplayed)
	recorder.ObservePublish(outbox.PublishRetryableFailed)
	recorder.ObserveStoreError(outbox.StoreClaim)
	recorder.ObserveStats(outbox.Stats{
		Pending:             3,
		Publishing:          2,
		Published:           7,
		RetryableFailed:     1,
		PermanentFailed:     4,
		OldestUnpublishedAt: &oldest,
	})

	if got := testutil.ToFloat64(
		recorder.ingest.WithLabelValues(string(IngestAccepted)),
	); got != 1 {
		t.Fatalf("accepted ingest count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(
		recorder.publish.WithLabelValues(string(outbox.PublishRetryableFailed)),
	); got != 1 {
		t.Fatalf("retryable publish count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(
		recorder.outboxErrors.WithLabelValues(string(outbox.StoreClaim)),
	); got != 1 {
		t.Fatalf("claim error count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(
		recorder.outboxEvents.WithLabelValues("permanent_failed"),
	); got != 4 {
		t.Fatalf("permanent failed gauge = %v, want 4", got)
	}
	if got := testutil.ToFloat64(recorder.outboxOldest); got != 90 {
		t.Fatalf("oldest event age = %v, want 90", got)
	}

	now = now.Add(30 * time.Second)
	if got := testutil.ToFloat64(recorder.outboxOldest); got != 120 {
		t.Fatalf("oldest event age after clock advance = %v, want 120", got)
	}
	recorder.ObserveStats(outbox.Stats{})
	if got := testutil.ToFloat64(recorder.outboxOldest); got != 0 {
		t.Fatalf("oldest event age after drain = %v, want 0", got)
	}
}

func TestRecorderIgnoresUnknownLabels(t *testing.T) {
	recorder := New(nil)
	before := metricCounts(t, recorder)
	recorder.ObserveIngest(IngestOutcome("tenant-a"))
	recorder.ObservePublish(outbox.PublishOutcome("event-123"))
	recorder.ObserveStoreError(outbox.StoreOperation("raw-error"))
	after := metricCounts(t, recorder)
	if before != after {
		t.Fatalf("metric counts changed from %+v to %+v", before, after)
	}
}

func TestHandlerServesPrometheusTextWithoutExternalReads(t *testing.T) {
	recorder := New(nil)
	recorder.ObserveIngest(IngestRejected)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://metrics.local/metrics", nil)
	recorder.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	for _, name := range []string{
		"waybill_event_ingest_requests_total",
		"waybill_outbox_publish_attempts_total",
		"waybill_outbox_events",
		"waybill_outbox_oldest_unpublished_age_seconds",
		"waybill_outbox_store_errors_total",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("metrics response is missing %q", name)
		}
	}
}

type metricFamilyCounts struct {
	ingest       int
	publish      int
	outboxEvents int
	storeErrors  int
}

func metricCounts(t *testing.T, recorder *Recorder) metricFamilyCounts {
	t.Helper()
	families, err := recorder.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	var counts metricFamilyCounts
	for _, family := range families {
		switch family.GetName() {
		case "waybill_event_ingest_requests_total":
			counts.ingest = len(family.Metric)
		case "waybill_outbox_publish_attempts_total":
			counts.publish = len(family.Metric)
		case "waybill_outbox_events":
			counts.outboxEvents = len(family.Metric)
		case "waybill_outbox_store_errors_total":
			counts.storeErrors = len(family.Metric)
		}
	}
	return counts
}
