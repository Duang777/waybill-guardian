package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Disposition string

const (
	Published       Disposition = "published"
	RetryableFailed Disposition = "retryable_failed"
	PermanentFailed Disposition = "permanent_failed"
)

type Event struct {
	Source           string
	ID               string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Type             string
	Subject          string
	Time             time.Time
	DataContentType  string
	DataSchema       string
	Data             json.RawMessage
	Attempt          int
	CreatedAt        time.Time
}

func (e Event) Clone() Event {
	cloned := e
	cloned.Data = append(json.RawMessage(nil), e.Data...)
	return cloned
}

type Claim interface {
	Event() Event
}

type PublishResult struct {
	Disposition Disposition
	ErrorCode   string
}

func (r PublishResult) Validate() error {
	switch r.Disposition {
	case Published:
		if r.ErrorCode != "" {
			return fmt.Errorf("published result cannot include an error code")
		}
	case RetryableFailed, PermanentFailed:
		if strings.TrimSpace(r.ErrorCode) == "" {
			return fmt.Errorf("%s result requires an error code", r.Disposition)
		}
	default:
		return fmt.Errorf("unsupported outbox disposition %q", r.Disposition)
	}
	return nil
}

type Completion struct {
	Disposition Disposition
	RetryAfter  time.Duration
	ErrorCode   string
}

func (c Completion) Validate() error {
	if c.RetryAfter < 0 {
		return fmt.Errorf("outbox retry delay cannot be negative")
	}
	if err := (PublishResult{
		Disposition: c.Disposition,
		ErrorCode:   c.ErrorCode,
	}).Validate(); err != nil {
		return err
	}
	if c.Disposition != RetryableFailed && c.RetryAfter != 0 {
		return fmt.Errorf("%s result cannot include a retry delay", c.Disposition)
	}
	return nil
}

type Stats struct {
	Pending             int64
	Publishing          int64
	Published           int64
	RetryableFailed     int64
	PermanentFailed     int64
	OldestUnpublishedAt *time.Time
}

func (s Stats) Clone() Stats {
	cloned := s
	if s.OldestUnpublishedAt != nil {
		oldest := s.OldestUnpublishedAt.UTC()
		cloned.OldestUnpublishedAt = &oldest
	}
	return cloned
}

type RequeueRequest struct {
	Source  string
	EventID string
	Actor   string
	Reason  string
}

func (r RequeueRequest) Validate() error {
	switch {
	case strings.TrimSpace(r.Source) == "":
		return fmt.Errorf("outbox source is required")
	case strings.TrimSpace(r.EventID) == "":
		return fmt.Errorf("outbox event ID is required")
	case strings.TrimSpace(r.Actor) == "":
		return fmt.Errorf("outbox requeue actor is required")
	case strings.TrimSpace(r.Reason) == "":
		return fmt.Errorf("outbox requeue reason is required")
	}
	return nil
}

type Store interface {
	ClaimOutbox(context.Context, int) ([]Claim, error)
	RenewOutbox(context.Context, Claim) error
	CompleteOutbox(context.Context, Claim, Completion) error
	OutboxStats(context.Context) (Stats, error)
	RequeueOutbox(context.Context, RequeueRequest) error
}

type Publisher interface {
	Publish(context.Context, Event) PublishResult
}

type PublishOutcome string

const (
	PublishSucceeded       PublishOutcome = "published"
	PublishRetryableFailed PublishOutcome = "retryable_failed"
	PublishPermanentFailed PublishOutcome = "permanent_failed"
	PublishLeaseLost       PublishOutcome = "lease_lost"
)

type StoreOperation string

const (
	StoreClaim    StoreOperation = "claim"
	StoreRenew    StoreOperation = "renew"
	StoreComplete StoreOperation = "complete"
	StoreStats    StoreOperation = "stats"
	StoreRequeue  StoreOperation = "requeue"
)

type Observer interface {
	ObservePublish(PublishOutcome)
	ObserveStoreError(StoreOperation)
	ObserveStats(Stats)
}

type NopObserver struct{}

func (NopObserver) ObservePublish(PublishOutcome) {}

func (NopObserver) ObserveStoreError(StoreOperation) {}

func (NopObserver) ObserveStats(Stats) {}
