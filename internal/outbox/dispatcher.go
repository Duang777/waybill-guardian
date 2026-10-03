package outbox

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	defaultBatchSize     = 10
	defaultConcurrency   = 4
	defaultPollInterval  = 250 * time.Millisecond
	defaultLeaseTTL      = 30 * time.Second
	defaultStatsInterval = 15 * time.Second
	defaultRetryBase     = time.Second
	defaultRetryMax      = 5 * time.Minute
)

type DispatcherConfig struct {
	Store         Store
	Publisher     Publisher
	Observer      Observer
	BatchSize     int
	Concurrency   int
	PollInterval  time.Duration
	LeaseTTL      time.Duration
	StatsInterval time.Duration
	RetryBase     time.Duration
	RetryMax      time.Duration
}

type Dispatcher struct {
	store         Store
	publisher     Publisher
	observer      Observer
	batchSize     int
	concurrency   int
	pollInterval  time.Duration
	leaseTTL      time.Duration
	statsInterval time.Duration
	retryBase     time.Duration
	retryMax      time.Duration
}

func NewDispatcher(config DispatcherConfig) (*Dispatcher, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("outbox store is required")
	}
	if config.Publisher == nil {
		return nil, fmt.Errorf("outbox publisher is required")
	}
	if config.Observer == nil {
		config.Observer = NopObserver{}
	}
	if config.BatchSize == 0 {
		config.BatchSize = defaultBatchSize
	}
	if config.Concurrency == 0 {
		config.Concurrency = defaultConcurrency
	}
	if config.PollInterval == 0 {
		config.PollInterval = defaultPollInterval
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = defaultLeaseTTL
	}
	if config.StatsInterval == 0 {
		config.StatsInterval = defaultStatsInterval
	}
	if config.RetryBase == 0 {
		config.RetryBase = defaultRetryBase
	}
	if config.RetryMax == 0 {
		config.RetryMax = defaultRetryMax
	}
	switch {
	case config.BatchSize < 1 || config.BatchSize > 100:
		return nil, fmt.Errorf("outbox batch size must be between 1 and 100")
	case config.Concurrency < 1 || config.Concurrency > 100:
		return nil, fmt.Errorf("outbox concurrency must be between 1 and 100")
	case config.PollInterval < 0:
		return nil, fmt.Errorf("outbox poll interval must be positive")
	case config.LeaseTTL < 0:
		return nil, fmt.Errorf("outbox lease TTL must be positive")
	case config.StatsInterval < 0:
		return nil, fmt.Errorf("outbox stats interval must be positive")
	case config.RetryBase < 0:
		return nil, fmt.Errorf("outbox retry base must be positive")
	case config.RetryMax < config.RetryBase:
		return nil, fmt.Errorf("outbox retry maximum must not be shorter than the base")
	}
	return &Dispatcher{
		store:         config.Store,
		publisher:     config.Publisher,
		observer:      config.Observer,
		batchSize:     config.BatchSize,
		concurrency:   config.Concurrency,
		pollInterval:  config.PollInterval,
		leaseTTL:      config.LeaseTTL,
		statsInterval: config.StatsInterval,
		retryBase:     config.RetryBase,
		retryMax:      config.RetryMax,
	}, nil
}

func (d *Dispatcher) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return nil
	}

	poll := time.NewTimer(0)
	defer poll.Stop()
	stats := time.NewTicker(d.statsInterval)
	defer stats.Stop()
	completed := make(chan struct{}, d.concurrency)
	var workers sync.WaitGroup
	active := 0

	d.refreshStats(ctx)
	for {
		if ctx.Err() != nil {
			workers.Wait()
			return nil
		}
		var pollC <-chan time.Time
		if active < d.concurrency {
			pollC = poll.C
		}
		select {
		case <-ctx.Done():
			workers.Wait()
			return nil
		case <-stats.C:
			d.refreshStats(ctx)
		case <-completed:
			active--
			resetTimer(poll, 0)
		case <-pollC:
			available := d.concurrency - active
			limit := min(d.batchSize, available)
			claims, err := d.store.ClaimOutbox(ctx, limit)
			if err != nil {
				if ctx.Err() == nil {
					d.observer.ObserveStoreError(StoreClaim)
				}
				resetTimer(poll, d.pollInterval)
				continue
			}
			for _, claim := range claims {
				if claim == nil || active == d.concurrency {
					break
				}
				active++
				workers.Go(func() {
					defer func() { completed <- struct{}{} }()
					d.dispatch(ctx, claim)
				})
			}
			resetTimer(poll, d.pollInterval)
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, claim Claim) {
	publishCtx, cancelPublish := context.WithCancel(ctx)
	stopRenewal := make(chan struct{})
	renewed := make(chan error, 1)
	go func() {
		renewed <- d.renew(publishCtx, stopRenewal, claim, cancelPublish)
	}()

	result := d.publisher.Publish(publishCtx, claim.Event())
	close(stopRenewal)
	renewErr := <-renewed
	cancelPublish()
	if renewErr != nil {
		d.observer.ObserveStoreError(StoreRenew)
		d.observer.ObservePublish(PublishLeaseLost)
		return
	}
	if err := result.Validate(); err != nil {
		result = PublishResult{
			Disposition: RetryableFailed,
			ErrorCode:   "invalid_publisher_result",
		}
	}

	outcome := PublishSucceeded
	completion := Completion{
		Disposition: result.Disposition,
		ErrorCode:   result.ErrorCode,
	}
	switch result.Disposition {
	case RetryableFailed:
		outcome = PublishRetryableFailed
		completion.RetryAfter = retryDelay(claim.Event().Attempt, d.retryBase, d.retryMax)
	case PermanentFailed:
		outcome = PublishPermanentFailed
	}
	d.observer.ObservePublish(outcome)

	completeCtx, cancelComplete := context.WithTimeout(
		context.Background(),
		leaseInterval(d.leaseTTL),
	)
	defer cancelComplete()
	if err := d.store.CompleteOutbox(completeCtx, claim, completion); err != nil {
		d.observer.ObserveStoreError(StoreComplete)
	}
}

func (d *Dispatcher) renew(
	ctx context.Context,
	stop <-chan struct{},
	claim Claim,
	cancelPublish context.CancelFunc,
) error {
	interval := leaseInterval(d.leaseTTL)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-stop:
			return nil
		case <-ticker.C:
			renewCtx, cancelRenew := context.WithTimeout(ctx, interval)
			err := d.store.RenewOutbox(renewCtx, claim)
			cancelRenew()
			if err == nil {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			cancelPublish()
			return err
		}
	}
}

func leaseInterval(ttl time.Duration) time.Duration {
	interval := ttl / 3
	if interval < time.Nanosecond {
		return time.Nanosecond
	}
	return interval
}

func (d *Dispatcher) refreshStats(ctx context.Context) {
	stats, err := d.store.OutboxStats(ctx)
	if err != nil {
		if ctx.Err() == nil {
			d.observer.ObserveStoreError(StoreStats)
		}
		return
	}
	d.observer.ObserveStats(stats.Clone())
}

func retryDelay(attempt int, base, maximum time.Duration) time.Duration {
	delay := base
	for current := 1; current < attempt && delay < maximum; current++ {
		if delay > maximum/2 {
			return maximum
		}
		delay *= 2
	}
	return min(delay, maximum)
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}
