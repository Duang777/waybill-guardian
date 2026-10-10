package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type PlatformConfig struct {
	Store     Store
	Artifacts artifact.Store
	Sources   SourceProfiles
	Solvers   SolverProfiles
	Validator Validator
	Clock     func() time.Time
	NewID     func(string) string
}

type Application struct {
	store     Store
	artifacts artifact.Store
	sources   SourceProfiles
	solvers   SolverProfiles
	validator Validator
	clock     func() time.Time
	newID     func(string) string
	closed    atomic.Bool
}

func NewPlatform(config PlatformConfig) (*Application, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("delivery store is required")
	}
	if config.Artifacts == nil {
		return nil, fmt.Errorf("delivery artifact store is required")
	}
	if config.Validator == nil {
		return nil, fmt.Errorf("delivery validator is required")
	}
	if len(config.Sources) == 0 {
		return nil, fmt.Errorf("at least one delivery source profile is required")
	}
	for profile, source := range config.Sources {
		if strings.TrimSpace(profile) == "" || source == nil {
			return nil, fmt.Errorf("delivery source profile is invalid")
		}
	}
	if len(config.Solvers) == 0 {
		return nil, fmt.Errorf("at least one delivery solver profile is required")
	}
	for profile, binding := range config.Solvers {
		if strings.TrimSpace(profile) == "" ||
			binding.Solver == nil ||
			!validArtifactDigest(binding.ConfigDigest) {
			return nil, fmt.Errorf("delivery solver profile %q is invalid", profile)
		}
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.NewID == nil {
		config.NewID = func(prefix string) string {
			return prefix + "-" + uuid.NewString()
		}
	}
	sources := make(SourceProfiles, len(config.Sources))
	for profile, value := range config.Sources {
		sources[profile] = value
	}
	solvers := make(SolverProfiles, len(config.Solvers))
	for profile, value := range config.Solvers {
		solvers[profile] = value
	}
	return &Application{
		store:     config.Store,
		artifacts: config.Artifacts,
		sources:   sources,
		solvers:   solvers,
		validator: config.Validator,
		clock:     config.Clock,
		newID:     config.NewID,
	}, nil
}

func (application *Application) Commands() Commands {
	return application
}

func (application *Application) Queries() Queries {
	return application
}

func (application *Application) EventStreams() EventStreams {
	return application
}

func (application *Application) CreateProblem(
	ctx context.Context,
	request CreateProblem,
) (domain.ProblemVersion, Replay, error) {
	if err := application.checkOpen(); err != nil {
		return domain.ProblemVersion{}, Replay{}, err
	}
	sourceProfile := strings.TrimSpace(request.SourceProfile)
	builder, exists := application.sources[sourceProfile]
	if !exists {
		return domain.ProblemVersion{}, Replay{}, fmt.Errorf("unknown source profile")
	}
	if err := validateCreateProblem(request); err != nil {
		return domain.ProblemVersion{}, Replay{}, err
	}
	built, err := builder.BuildProblem(ctx, SourceBuildRequest{
		TenantID:    request.TenantID,
		ProblemID:   request.ProblemID,
		Version:     request.Version,
		SnapshotRef: request.SnapshotRef,
		Horizon:     request.Horizon,
		Commitments: request.Commitments,
	})
	if err != nil {
		return domain.ProblemVersion{}, Replay{}, err
	}
	value, err := artifact.New(
		artifact.KindProblem,
		domain.ProblemSchemaVersion,
		built.Problem,
	)
	if err != nil {
		return domain.ProblemVersion{}, Replay{}, fmt.Errorf("create problem artifact: %w", err)
	}
	ref, err := application.artifacts.Put(ctx, request.TenantID, value)
	if err != nil {
		return domain.ProblemVersion{}, Replay{}, fmt.Errorf("put problem artifact: %w", err)
	}
	if err := application.artifacts.Verify(ctx, request.TenantID, ref.Digest); err != nil {
		return domain.ProblemVersion{}, Replay{}, fmt.Errorf("verify problem artifact: %w", err)
	}
	requestDigest, err := domain.Digest(struct {
		Operation      string
		TenantID       domain.TenantID
		ProblemID      domain.ProblemID
		Version        uint64
		SourceProfile  string
		SnapshotRef    string
		Horizon        domain.TimeRange
		CommitmentHash domain.ArtifactDigest
	}{
		Operation:      "delivery.create_problem.v1",
		TenantID:       request.TenantID,
		ProblemID:      request.ProblemID,
		Version:        request.Version,
		SourceProfile:  sourceProfile,
		SnapshotRef:    request.SnapshotRef,
		Horizon:        request.Horizon,
		CommitmentHash: built.Problem.CommitmentDigest,
	})
	if err != nil {
		return domain.ProblemVersion{}, Replay{}, fmt.Errorf("digest create problem request: %w", err)
	}
	version := domain.ProblemVersion{
		TenantID:         request.TenantID,
		ProblemID:        request.ProblemID,
		Version:          request.Version,
		ProblemDigest:    built.Problem.ProblemDigest,
		PolicyDigest:     built.Problem.PolicyDigest,
		CommitmentDigest: built.Problem.CommitmentDigest,
		ManifestDigest:   built.ManifestDigest,
		ProblemArtifact:  ref.Digest,
		SourceProfile:    sourceProfile,
		SourceRef:        request.SnapshotRef,
		CreatedAt:        built.Problem.CreatedAt,
	}
	return application.store.CommitProblem(ctx, CommitProblemTx{
		IdempotencyKey: request.IdempotencyKey,
		RequestDigest:  requestDigest,
		Actor:          request.Actor,
		Problem:        version,
	})
}

func (application *Application) RequestOptimization(
	ctx context.Context,
	request RequestOptimization,
) (domain.OptimizationRun, Replay, error) {
	if err := application.checkOpen(); err != nil {
		return domain.OptimizationRun{}, Replay{}, err
	}
	if err := validateRequestOptimization(request); err != nil {
		return domain.OptimizationRun{}, Replay{}, err
	}
	binding, exists := application.solvers[strings.TrimSpace(request.SolverProfile)]
	if !exists {
		return domain.OptimizationRun{}, Replay{}, fmt.Errorf("unknown solver profile")
	}
	problem, err := application.store.GetProblem(
		ctx,
		request.TenantID,
		request.ProblemID,
		request.ProblemVersion,
	)
	if err != nil {
		return domain.OptimizationRun{}, Replay{}, err
	}
	requestDigest, err := domain.Digest(struct {
		Operation      string
		TenantID       domain.TenantID
		ProblemID      domain.ProblemID
		ProblemVersion uint64
		ProblemDigest  domain.ArtifactDigest
		SolverProfile  string
		ConfigDigest   domain.ArtifactDigest
	}{
		Operation:      "delivery.request_optimization.v1",
		TenantID:       request.TenantID,
		ProblemID:      request.ProblemID,
		ProblemVersion: request.ProblemVersion,
		ProblemDigest:  problem.ProblemDigest,
		SolverProfile:  request.SolverProfile,
		ConfigDigest:   binding.ConfigDigest,
	})
	if err != nil {
		return domain.OptimizationRun{}, Replay{}, fmt.Errorf(
			"digest optimization request: %w",
			err,
		)
	}
	now := application.clock().UTC()
	run := domain.OptimizationRun{
		TenantID:       request.TenantID,
		ID:             domain.OptimizationRunID(application.newID("run")),
		ProblemID:      request.ProblemID,
		ProblemVersion: request.ProblemVersion,
		ProblemDigest:  problem.ProblemDigest,
		SolverProfile:  request.SolverProfile,
		ConfigDigest:   binding.ConfigDigest,
		Status:         domain.RunQueued,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	return application.store.CreateRun(ctx, CreateRunTx{
		IdempotencyKey: request.IdempotencyKey,
		RequestDigest:  requestDigest,
		Actor:          request.Actor,
		Run:            run,
	})
}

func (application *Application) CancelOptimization(
	ctx context.Context,
	request CancelOptimization,
) (domain.OptimizationRun, error) {
	if err := application.checkOpen(); err != nil {
		return domain.OptimizationRun{}, err
	}
	if request.TenantID == "" ||
		request.Actor.Subject == "" ||
		request.RunID == "" ||
		request.ExpectedVersion == 0 {
		return domain.OptimizationRun{}, fmt.Errorf("cancel optimization request is incomplete")
	}
	return application.store.RequestRunCancellation(
		ctx,
		request.TenantID,
		request.RunID,
		request.ExpectedVersion,
		request.Actor,
		application.clock().UTC(),
	)
}

func (application *Application) GetProblem(
	ctx context.Context,
	tenantID domain.TenantID,
	problemID domain.ProblemID,
	version uint64,
) (domain.ProblemVersion, error) {
	if err := application.checkOpen(); err != nil {
		return domain.ProblemVersion{}, err
	}
	return application.store.GetProblem(ctx, tenantID, problemID, version)
}

func (application *Application) GetRun(
	ctx context.Context,
	tenantID domain.TenantID,
	runID domain.OptimizationRunID,
) (domain.OptimizationRun, error) {
	if err := application.checkOpen(); err != nil {
		return domain.OptimizationRun{}, err
	}
	return application.store.GetRun(ctx, tenantID, runID)
}

func (application *Application) GetPlan(
	ctx context.Context,
	tenantID domain.TenantID,
	planID domain.PlanID,
) (domain.DispatchPlan, error) {
	if err := application.checkOpen(); err != nil {
		return domain.DispatchPlan{}, err
	}
	return application.store.GetPlan(ctx, tenantID, planID)
}

func (application *Application) GetRevision(
	ctx context.Context,
	tenantID domain.TenantID,
	revisionID domain.PlanRevisionID,
) (domain.PlanRevision, error) {
	if err := application.checkOpen(); err != nil {
		return domain.PlanRevision{}, err
	}
	return application.store.GetRevision(ctx, tenantID, revisionID)
}

func (application *Application) OpenArtifact(
	ctx context.Context,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
) (io.ReadCloser, artifact.Metadata, error) {
	if err := application.checkOpen(); err != nil {
		return nil, artifact.Metadata{}, err
	}
	reachable, err := application.store.ArtifactReachable(ctx, tenantID, digest)
	if err != nil {
		return nil, artifact.Metadata{}, err
	}
	if !reachable {
		return nil, artifact.Metadata{}, ErrArtifactUnreachable
	}
	if err := application.artifacts.Verify(ctx, tenantID, digest); err != nil {
		return nil, artifact.Metadata{}, err
	}
	return application.artifacts.Open(ctx, tenantID, digest)
}

func (application *Application) Replay(
	ctx context.Context,
	cursor StreamCursor,
) ([]Event, error) {
	if err := application.checkOpen(); err != nil {
		return nil, err
	}
	return application.store.Replay(ctx, cursor)
}

func (application *Application) Subscribe(
	ctx context.Context,
	cursor StreamCursor,
) (Subscription, error) {
	if err := application.checkOpen(); err != nil {
		return nil, err
	}
	return application.store.Subscribe(ctx, cursor)
}

func (application *Application) Recover(ctx context.Context) error {
	if err := application.checkOpen(); err != nil {
		return err
	}
	_, err := application.store.ScanRecovery(ctx, RecoveryScan{
		Limit: 1_000,
		Now:   application.clock().UTC(),
	})
	return err
}

func (application *Application) Close() {
	application.closed.Store(true)
}

func (application *Application) checkOpen() error {
	if application.closed.Load() {
		return ErrServiceClosed
	}
	return nil
}

func validateCreateProblem(request CreateProblem) error {
	if request.TenantID == "" ||
		request.Actor.Subject == "" ||
		request.IdempotencyKey == "" ||
		request.ProblemID == "" ||
		request.Version == 0 ||
		strings.TrimSpace(request.SourceProfile) == "" ||
		strings.TrimSpace(request.SnapshotRef) == "" {
		return fmt.Errorf("create problem request is incomplete")
	}
	if strings.TrimSpace(string(request.IdempotencyKey)) != string(request.IdempotencyKey) {
		return fmt.Errorf("idempotency key must not contain surrounding whitespace")
	}
	return nil
}

func validateRequestOptimization(request RequestOptimization) error {
	if request.TenantID == "" ||
		request.Actor.Subject == "" ||
		request.IdempotencyKey == "" ||
		request.ProblemID == "" ||
		request.ProblemVersion == 0 ||
		strings.TrimSpace(request.SolverProfile) == "" {
		return fmt.Errorf("optimization request is incomplete")
	}
	if strings.TrimSpace(string(request.IdempotencyKey)) != string(request.IdempotencyKey) {
		return fmt.Errorf("idempotency key must not contain surrounding whitespace")
	}
	return nil
}

func decodeProblemArtifact(raw []byte) (domain.ProblemSnapshot, error) {
	var problem domain.ProblemSnapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&problem); err != nil {
		return domain.ProblemSnapshot{}, fmt.Errorf("decode problem artifact: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domain.ProblemSnapshot{}, fmt.Errorf("problem artifact has trailing JSON")
	}
	rebuilt, err := BuildProblemSnapshot(problem)
	if err != nil {
		return domain.ProblemSnapshot{}, fmt.Errorf("validate problem artifact: %w", err)
	}
	if rebuilt.ProblemDigest != problem.ProblemDigest ||
		rebuilt.PolicyDigest != problem.PolicyDigest ||
		rebuilt.CommitmentDigest != problem.CommitmentDigest {
		return domain.ProblemSnapshot{}, fmt.Errorf("problem artifact digest mismatch")
	}
	return rebuilt, nil
}
