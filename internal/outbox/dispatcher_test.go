package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDispatcherBoundsConcurrencyAndProcessesClaimedEvents(t *testing.T) {
	store := &fakeStore{
		claims: []Claim{
			fakeClaim{event: Event{ID: "event-1", Attempt: 1}},
			fakeClaim{event: Event{ID: "event-2", Attempt: 1}},
			fakeClaim{event: Event{ID: "event-3", Attempt: 1}},
		},
	}
	publisher := &blockingPublisher{
		started: make(chan string, 3),
		release: make(chan struct{}, 3),
	}
	dispatcher := newTestDispatcher(t, DispatcherConfig{
		Store:       store,
		Publisher:   publisher,
		BatchSize:   3,
		Concurrency: 2,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Run(ctx)
	}()

	first := receive(t, publisher.started)
	second := receive(t, publisher.started)
	if first == second {
		t.Fatalf("duplicate started event %q", first)
	}
	select {
	case third := <-publisher.started:
		t.Fatalf("third event %q started before capacity was available", third)
	case <-time.After(25 * time.Millisecond):
	}

	publisher.release <- struct{}{}
	publisher.release <- struct{}{}
	third := receive(t, publisher.started)
	if third == first || third == second {
		t.Fatalf("third event = %q, already started", third)
	}
	publisher.release <- struct{}{}
	waitFor(t, func() bool {
		return store.completionCount() == 3
	})
	cancel()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if maximum := publisher.maximumConcurrency(); maximum != 2 {
		t.Fatalf("maximum publish concurrency = %d, want 2", maximum)
	}
}

func TestDispatcherDrainsActivePublishOnShutdown(t *testing.T) {
	store := &fakeStore{
		claims: []Claim{fakeClaim{event: Event{ID: "event-1", Attempt: 1}}},
	}
	publisher := &blockingPublisher{
		started: make(chan string, 1),
		release: make(chan struct{}, 1),
	}
	dispatcher := newTestDispatcher(t, DispatcherConfig{
		Store:       store,
		Publisher:   publisher,
		Concurrency: 1,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Run(ctx)
	}()
	receive(t, publisher.started)

	cancel()
	select {
	case err := <-done:
		t.Fatalf("Run returned before active publish drained: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	publisher.release <- struct{}{}
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if store.completionCount() != 1 {
		t.Fatalf("completion count = %d, want 1", store.completionCount())
	}
}

func TestDispatcherCancelsPublishAndSkipsCompletionWhenRenewalFails(t *testing.T) {
	renewErr := errors.New("lease lost")
	store := &fakeStore{
		claims:   []Claim{fakeClaim{event: Event{ID: "event-1", Attempt: 1}}},
		renewErr: renewErr,
	}
	publisher := &cancelPublisher{canceled: make(chan struct{}, 1)}
	observer := &fakeObserver{}
	dispatcher := newTestDispatcher(t, DispatcherConfig{
		Store:       store,
		Publisher:   publisher,
		Observer:    observer,
		Concurrency: 1,
		LeaseTTL:    15 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Run(ctx)
	}()

	receive(t, publisher.canceled)
	waitFor(t, func() bool {
		return observer.hasStoreError(StoreRenew)
	})
	cancel()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if store.completionCount() != 0 {
		t.Fatalf("completion count = %d, want 0", store.completionCount())
	}
	if !observer.hasPublishOutcome(PublishLeaseLost) {
		t.Fatalf("publish outcomes = %v, want lease_lost", observer.publishOutcomes())
	}
}

func TestDispatcherUsesDeterministicCappedRetry(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: time.Second},
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 10, want: 5 * time.Minute},
	}
	for _, test := range tests {
		if got := retryDelay(test.attempt, time.Second, 5*time.Minute); got != test.want {
			t.Fatalf("retryDelay(%d) = %s, want %s", test.attempt, got, test.want)
		}
	}
}

func TestDispatcherRefreshesStatsThroughObserver(t *testing.T) {
	oldest := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		stats: Stats{
			Pending:             2,
			PermanentFailed:     1,
			OldestUnpublishedAt: &oldest,
		},
	}
	observer := &fakeObserver{statsObserved: make(chan Stats, 1)}
	dispatcher := newTestDispatcher(t, DispatcherConfig{
		Store:       store,
		Publisher:   fixedPublisher{result: PublishResult{Disposition: Published}},
		Observer:    observer,
		Concurrency: 1,
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Run(ctx)
	}()

	got := receive(t, observer.statsObserved)
	cancel()
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if got.Pending != 2 || got.PermanentFailed != 1 ||
		got.OldestUnpublishedAt == nil || !got.OldestUnpublishedAt.Equal(oldest) {
		t.Fatalf("observed stats = %+v", got)
	}
}

func TestNewDispatcherRejectsInvalidConfiguration(t *testing.T) {
	store := &fakeStore{}
	publisher := fixedPublisher{result: PublishResult{Disposition: Published}}
	tests := []DispatcherConfig{
		{Publisher: publisher},
		{Store: store},
		{Store: store, Publisher: publisher, BatchSize: -1},
		{Store: store, Publisher: publisher, Concurrency: 101},
		{Store: store, Publisher: publisher, LeaseTTL: -time.Second},
		{
			Store:     store,
			Publisher: publisher,
			RetryBase: 2 * time.Second,
			RetryMax:  time.Second,
		},
	}
	for _, config := range tests {
		if _, err := NewDispatcher(config); err == nil {
			t.Fatalf("NewDispatcher accepted invalid config %+v", config)
		}
	}
}

func newTestDispatcher(t *testing.T, config DispatcherConfig) *Dispatcher {
	t.Helper()
	if config.PollInterval == 0 {
		config.PollInterval = time.Millisecond
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 300 * time.Millisecond
	}
	if config.StatsInterval == 0 {
		config.StatsInterval = time.Hour
	}
	dispatcher, err := NewDispatcher(config)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

type fakeClaim struct {
	event Event
}

func (c fakeClaim) Event() Event {
	return c.event.Clone()
}

type fakeStore struct {
	mu          sync.Mutex
	claims      []Claim
	completions []Completion
	renewErr    error
	stats       Stats
}

func (s *fakeStore) ClaimOutbox(_ context.Context, limit int) ([]Claim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit = min(limit, len(s.claims))
	claims := append([]Claim(nil), s.claims[:limit]...)
	s.claims = s.claims[limit:]
	return claims, nil
}

func (s *fakeStore) RenewOutbox(context.Context, Claim) error {
	return s.renewErr
}

func (s *fakeStore) CompleteOutbox(
	_ context.Context,
	_ Claim,
	completion Completion,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completions = append(s.completions, completion)
	return nil
}

func (s *fakeStore) OutboxStats(context.Context) (Stats, error) {
	return s.stats.Clone(), nil
}

func (s *fakeStore) RequeueOutbox(context.Context, RequeueRequest) error {
	return nil
}

func (s *fakeStore) completionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.completions)
}

type blockingPublisher struct {
	mu        sync.Mutex
	active    int
	maxActive int
	started   chan string
	release   chan struct{}
}

func (p *blockingPublisher) Publish(_ context.Context, event Event) PublishResult {
	p.mu.Lock()
	p.active++
	p.maxActive = max(p.maxActive, p.active)
	p.mu.Unlock()
	p.started <- event.ID
	<-p.release
	p.mu.Lock()
	p.active--
	p.mu.Unlock()
	return PublishResult{Disposition: Published}
}

func (p *blockingPublisher) maximumConcurrency() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxActive
}

type cancelPublisher struct {
	canceled chan struct{}
}

func (p *cancelPublisher) Publish(ctx context.Context, _ Event) PublishResult {
	<-ctx.Done()
	p.canceled <- struct{}{}
	return PublishResult{
		Disposition: RetryableFailed,
		ErrorCode:   "request_canceled",
	}
}

type fixedPublisher struct {
	result PublishResult
}

func (p fixedPublisher) Publish(context.Context, Event) PublishResult {
	return p.result
}

type fakeObserver struct {
	mu            sync.Mutex
	publish       []PublishOutcome
	storeErrors   []StoreOperation
	statsObserved chan Stats
}

func (o *fakeObserver) ObservePublish(outcome PublishOutcome) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.publish = append(o.publish, outcome)
}

func (o *fakeObserver) ObserveStoreError(operation StoreOperation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.storeErrors = append(o.storeErrors, operation)
}

func (o *fakeObserver) ObserveStats(stats Stats) {
	if o.statsObserved != nil {
		o.statsObserved <- stats.Clone()
	}
}

func (o *fakeObserver) hasStoreError(want StoreOperation) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, operation := range o.storeErrors {
		if operation == want {
			return true
		}
	}
	return false
}

func (o *fakeObserver) hasPublishOutcome(want PublishOutcome) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, outcome := range o.publish {
		if outcome == want {
			return true
		}
	}
	return false
}

func (o *fakeObserver) publishOutcomes() []PublishOutcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]PublishOutcome(nil), o.publish...)
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for channel value")
		var zero T
		return zero
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(time.Millisecond)
	}
}
