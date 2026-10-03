package metrics

import (
	"net/http"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/outbox"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type IngestOutcome string

const (
	IngestAccepted IngestOutcome = "accepted"
	IngestReplayed IngestOutcome = "replayed"
	IngestRejected IngestOutcome = "rejected"
	IngestFailed   IngestOutcome = "failed"
)

type Recorder struct {
	registry       *prometheus.Registry
	ingest         *prometheus.CounterVec
	publish        *prometheus.CounterVec
	outboxEvents   *prometheus.GaugeVec
	outboxOldest   prometheus.GaugeFunc
	outboxErrors   *prometheus.CounterVec
	now            func() time.Time
	oldestMu       sync.RWMutex
	oldestUnsentAt time.Time
}

var _ outbox.Observer = (*Recorder)(nil)

func New(clock func() time.Time) *Recorder {
	if clock == nil {
		clock = time.Now
	}
	recorder := &Recorder{
		registry: prometheus.NewRegistry(),
		ingest: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "waybill_event_ingest_requests_total",
			Help: "Number of authenticated CloudEvents ingestion requests by outcome.",
		}, []string{"outcome"}),
		publish: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "waybill_outbox_publish_attempts_total",
			Help: "Number of outbox publish attempts by outcome.",
		}, []string{"outcome"}),
		outboxEvents: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "waybill_outbox_events",
			Help: "Current outbox event count by status.",
		}, []string{"status"}),
		outboxErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "waybill_outbox_store_errors_total",
			Help: "Number of outbox store errors by operation.",
		}, []string{"operation"}),
		now: clock,
	}
	recorder.outboxOldest = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "waybill_outbox_oldest_unpublished_age_seconds",
		Help: "Age in seconds of the oldest unpublished outbox event.",
	}, recorder.oldestAge)
	recorder.registry.MustRegister(
		recorder.ingest,
		recorder.publish,
		recorder.outboxEvents,
		recorder.outboxOldest,
		recorder.outboxErrors,
	)
	recorder.initializeLabels()
	return recorder
}

func (r *Recorder) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

func (r *Recorder) Gatherer() prometheus.Gatherer {
	return r.registry
}

func (r *Recorder) ObserveIngest(outcome IngestOutcome) {
	if !validIngestOutcome(outcome) {
		return
	}
	r.ingest.WithLabelValues(string(outcome)).Inc()
}

func (r *Recorder) ObservePublish(outcome outbox.PublishOutcome) {
	if !validPublishOutcome(outcome) {
		return
	}
	r.publish.WithLabelValues(string(outcome)).Inc()
}

func (r *Recorder) ObserveStoreError(operation outbox.StoreOperation) {
	if !validStoreOperation(operation) {
		return
	}
	r.outboxErrors.WithLabelValues(string(operation)).Inc()
}

func (r *Recorder) ObserveStats(stats outbox.Stats) {
	r.outboxEvents.WithLabelValues("pending").Set(float64(stats.Pending))
	r.outboxEvents.WithLabelValues("publishing").Set(float64(stats.Publishing))
	r.outboxEvents.WithLabelValues("published").Set(float64(stats.Published))
	r.outboxEvents.WithLabelValues("retryable_failed").Set(float64(stats.RetryableFailed))
	r.outboxEvents.WithLabelValues("permanent_failed").Set(float64(stats.PermanentFailed))

	r.oldestMu.Lock()
	defer r.oldestMu.Unlock()
	r.oldestUnsentAt = time.Time{}
	if stats.OldestUnpublishedAt != nil {
		r.oldestUnsentAt = stats.OldestUnpublishedAt.UTC()
	}
}

func (r *Recorder) oldestAge() float64 {
	r.oldestMu.RLock()
	oldest := r.oldestUnsentAt
	r.oldestMu.RUnlock()
	if oldest.IsZero() {
		return 0
	}
	age := r.now().UTC().Sub(oldest).Seconds()
	if age < 0 {
		return 0
	}
	return age
}

func (r *Recorder) initializeLabels() {
	for _, outcome := range []IngestOutcome{
		IngestAccepted,
		IngestReplayed,
		IngestRejected,
		IngestFailed,
	} {
		r.ingest.WithLabelValues(string(outcome))
	}
	for _, outcome := range []outbox.PublishOutcome{
		outbox.PublishSucceeded,
		outbox.PublishRetryableFailed,
		outbox.PublishPermanentFailed,
		outbox.PublishLeaseLost,
	} {
		r.publish.WithLabelValues(string(outcome))
	}
	for _, status := range []string{
		"pending",
		"publishing",
		"published",
		"retryable_failed",
		"permanent_failed",
	} {
		r.outboxEvents.WithLabelValues(status)
	}
	for _, operation := range []outbox.StoreOperation{
		outbox.StoreClaim,
		outbox.StoreRenew,
		outbox.StoreComplete,
		outbox.StoreStats,
		outbox.StoreRequeue,
	} {
		r.outboxErrors.WithLabelValues(string(operation))
	}
}

func validIngestOutcome(outcome IngestOutcome) bool {
	switch outcome {
	case IngestAccepted, IngestReplayed, IngestRejected, IngestFailed:
		return true
	default:
		return false
	}
}

func validPublishOutcome(outcome outbox.PublishOutcome) bool {
	switch outcome {
	case outbox.PublishSucceeded,
		outbox.PublishRetryableFailed,
		outbox.PublishPermanentFailed,
		outbox.PublishLeaseLost:
		return true
	default:
		return false
	}
}

func validStoreOperation(operation outbox.StoreOperation) bool {
	switch operation {
	case outbox.StoreClaim,
		outbox.StoreRenew,
		outbox.StoreComplete,
		outbox.StoreStats,
		outbox.StoreRequeue:
		return true
	default:
		return false
	}
}
