package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

var (
	ErrNotFound            = errors.New("delivery resource not found")
	ErrConflict            = errors.New("delivery resource version conflict")
	ErrIdempotencyConflict = errors.New("delivery idempotency key has different input")
	ErrLeaseLost           = errors.New("delivery worker lease was lost")
	ErrCursorAhead         = errors.New("delivery event cursor is ahead")
	ErrArtifactUnreachable = errors.New("delivery artifact is not reachable")
	ErrServiceClosed       = errors.New("delivery service is closed")
	ErrNoWork              = errors.New("delivery worker has no work")
	ErrSolverExhausted     = errors.New("delivery solver exhausted its fixed budget")
	ErrSolverAborted       = errors.New("delivery solver was aborted")
)

type IdempotencyKey string

type Replay struct {
	Replayed bool `json:"replayed"`
}

type Actor struct {
	Subject string `json:"subject"`
}

type EventType string

const (
	EventProblemCreated     EventType = "problem.created"
	EventRunRequested       EventType = "run.requested"
	EventRunClaimed         EventType = "run.claimed"
	EventRunCheckpointed    EventType = "run.checkpointed"
	EventRunCancelRequested EventType = "run.cancel_requested"
	EventRunFailed          EventType = "run.failed"
	EventRevisionPublished  EventType = "revision.published"
	EventRevisionActivated  EventType = "revision.activated"
)

type AggregateType string

const (
	AggregateProblem   AggregateType = "problem"
	AggregateRun       AggregateType = "run"
	AggregatePlan      AggregateType = "plan"
	AggregateRevision  AggregateType = "revision"
	AggregateExecution AggregateType = "execution"
)

type Event struct {
	SchemaVersion int             `json:"schema_version"`
	TenantID      domain.TenantID `json:"tenant_id"`
	AggregateType AggregateType   `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	Seq           uint64          `json:"seq"`
	EventID       string          `json:"event_id"`
	Type          EventType       `json:"type"`
	Actor         string          `json:"actor"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
	PrevHash      string          `json:"prev_hash"`
	Hash          string          `json:"hash"`
}

type Subscription interface {
	Events() <-chan Event
	Close()
}

type CommitProblemTx struct {
	IdempotencyKey IdempotencyKey
	RequestDigest  domain.ArtifactDigest
	Actor          Actor
	Problem        domain.ProblemVersion
}

type CreateRunTx struct {
	IdempotencyKey IdempotencyKey
	RequestDigest  domain.ArtifactDigest
	Actor          Actor
	Run            domain.OptimizationRun
}

type ClaimRuns struct {
	TenantID domain.TenantID
	WorkerID string
	Limit    int
	LeaseTTL time.Duration
	Now      time.Time
}

type RunClaim struct {
	Run          domain.OptimizationRun
	WorkerID     string
	FencingToken uint64
}

type SaveCheckpointTx struct {
	Status           domain.RunStatus
	CheckpointDigest domain.ArtifactDigest
	Now              time.Time
}

type PublishRevisionTx struct {
	Actor     Actor
	Plan      domain.DispatchPlan
	Revision  domain.PlanRevision
	RunStatus domain.RunStatus
	Now       time.Time
}

type FinishRunTx struct {
	Actor       Actor
	Status      domain.RunStatus
	FailureCode string
	Now         time.Time
}

type StreamCursor struct {
	TenantID      domain.TenantID
	AggregateType AggregateType
	AggregateID   string
	After         uint64
}

type RecoveryItem struct {
	Kind       string
	ResourceID string
	Status     string
}

type RecoveryScan struct {
	TenantID domain.TenantID
	Limit    int
	Now      time.Time
}

type Store interface {
	CommitProblem(context.Context, CommitProblemTx) (
		domain.ProblemVersion,
		Replay,
		error,
	)
	CreateRun(context.Context, CreateRunTx) (
		domain.OptimizationRun,
		Replay,
		error,
	)
	ClaimRuns(context.Context, ClaimRuns) ([]RunClaim, error)
	RenewRunLease(context.Context, RunClaim, time.Time) error
	SaveRunCheckpoint(context.Context, RunClaim, SaveCheckpointTx) error
	PublishRevision(context.Context, RunClaim, PublishRevisionTx) (
		domain.PlanRevision,
		error,
	)
	FinishRun(context.Context, RunClaim, FinishRunTx) error
	RequestRunCancellation(
		context.Context,
		domain.TenantID,
		domain.OptimizationRunID,
		uint64,
		Actor,
		time.Time,
	) (domain.OptimizationRun, error)

	GetProblem(
		context.Context,
		domain.TenantID,
		domain.ProblemID,
		uint64,
	) (domain.ProblemVersion, error)
	GetRun(
		context.Context,
		domain.TenantID,
		domain.OptimizationRunID,
	) (domain.OptimizationRun, error)
	GetPlan(
		context.Context,
		domain.TenantID,
		domain.PlanID,
	) (domain.DispatchPlan, error)
	GetRevision(
		context.Context,
		domain.TenantID,
		domain.PlanRevisionID,
	) (domain.PlanRevision, error)
	ArtifactReachable(
		context.Context,
		domain.TenantID,
		domain.ArtifactDigest,
	) (bool, error)

	Replay(context.Context, StreamCursor) ([]Event, error)
	Subscribe(context.Context, StreamCursor) (Subscription, error)
	ScanRecovery(context.Context, RecoveryScan) ([]RecoveryItem, error)
}

type CreateProblem struct {
	TenantID       domain.TenantID
	Actor          Actor
	IdempotencyKey IdempotencyKey
	ProblemID      domain.ProblemID
	Version        uint64
	SourceProfile  string
	SnapshotRef    string
	Horizon        domain.TimeRange
	Commitments    domain.CommitmentSet
}

type RequestOptimization struct {
	TenantID       domain.TenantID
	Actor          Actor
	IdempotencyKey IdempotencyKey
	RunID          domain.OptimizationRunID
	ProblemID      domain.ProblemID
	ProblemVersion uint64
	SolverProfile  string
}

type CancelOptimization struct {
	TenantID        domain.TenantID
	Actor           Actor
	RunID           domain.OptimizationRunID
	ExpectedVersion uint64
}

type Commands interface {
	CreateProblem(context.Context, CreateProblem) (domain.ProblemVersion, Replay, error)
	RequestOptimization(context.Context, RequestOptimization) (
		domain.OptimizationRun,
		Replay,
		error,
	)
	CancelOptimization(context.Context, CancelOptimization) (
		domain.OptimizationRun,
		error,
	)
}

type Queries interface {
	GetProblem(
		context.Context,
		domain.TenantID,
		domain.ProblemID,
		uint64,
	) (domain.ProblemVersion, error)
	GetRun(
		context.Context,
		domain.TenantID,
		domain.OptimizationRunID,
	) (domain.OptimizationRun, error)
	GetPlan(
		context.Context,
		domain.TenantID,
		domain.PlanID,
	) (domain.DispatchPlan, error)
	GetRevision(
		context.Context,
		domain.TenantID,
		domain.PlanRevisionID,
	) (domain.PlanRevision, error)
	OpenArtifact(
		context.Context,
		domain.TenantID,
		domain.ArtifactDigest,
	) (io.ReadCloser, artifact.Metadata, error)
}

type EventStreams interface {
	Replay(context.Context, StreamCursor) ([]Event, error)
	Subscribe(context.Context, StreamCursor) (Subscription, error)
}

type Platform interface {
	Commands() Commands
	Queries() Queries
	EventStreams() EventStreams
	RunWorker(WorkerConfig) (*RunWorker, error)
	Recover(context.Context) error
}

type ProgressSink interface {
	Checkpoint(context.Context, any) error
}

type SolveRequest struct {
	RunID      domain.OptimizationRunID
	PlanID     domain.PlanID
	RevisionID domain.PlanRevisionID
	Profile    string
}

type Solver interface {
	Identity() domain.SolverIdentity
	Solve(
		context.Context,
		domain.ProblemSnapshot,
		SolveRequest,
		ProgressSink,
	) (domain.Plan, error)
}

type Validator interface {
	Validate(
		domain.ProblemSnapshot,
		domain.Plan,
		time.Time,
	) domain.ValidationReport
}

type SourceBuildRequest struct {
	TenantID    domain.TenantID
	ProblemID   domain.ProblemID
	Version     uint64
	SnapshotRef string
	Horizon     domain.TimeRange
	Commitments domain.CommitmentSet
}

type SourceBuildResult struct {
	Problem        domain.ProblemSnapshot
	ManifestDigest domain.ArtifactDigest
}

type ProblemSource interface {
	BuildProblem(context.Context, SourceBuildRequest) (SourceBuildResult, error)
}

type SourceProfiles map[string]ProblemSource

type SolverBinding struct {
	Solver       Solver
	ConfigDigest domain.ArtifactDigest
}

type SolverProfiles map[string]SolverBinding
