package routeseed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/solve"
)

const (
	CheckpointSchemaVersion  = "delivery.route-seed-checkpoint.v1"
	maxRouteSeedArtifactSize = 128 << 20
	maxCASAttempts           = 8
)

var (
	ErrCheckpointNotFound  = errors.New("route seed checkpoint not found")
	ErrCheckpointConflict  = errors.New("route seed checkpoint version conflict")
	ErrCheckpointIntegrity = errors.New("route seed checkpoint integrity check failed")
	remoteJobIDPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type State string

const (
	StatePrepared        State = "prepared"
	StatePending         State = "pending"
	StateCancelRequested State = "cancel_requested"
	StateCompleted       State = "completed"
	StateFailed          State = "failed"
	StateCanceled        State = "canceled"
	StateManualReview    State = "manual_review"
)

type Checkpoint struct {
	SchemaVersion    string                `json:"schema_version"`
	TenantID         domain.TenantID       `json:"tenant_id"`
	Provider         domain.SolverIdentity `json:"provider"`
	ProblemDigest    domain.ArtifactDigest `json:"problem_digest"`
	RequestDigest    domain.ArtifactDigest `json:"request_digest"`
	RequestArtifact  domain.ArtifactDigest `json:"request_artifact_digest"`
	JobArtifact      domain.ArtifactDigest `json:"job_artifact_digest,omitempty"`
	RemoteJobID      string                `json:"remote_job_id,omitempty"`
	JobDigest        domain.ArtifactDigest `json:"job_digest,omitempty"`
	ResponseDigest   domain.ArtifactDigest `json:"response_digest,omitempty"`
	State            State                 `json:"state"`
	FailureCode      string                `json:"failure_code,omitempty"`
	FailureOperation string                `json:"failure_operation,omitempty"`
	Version          uint64                `json:"version"`
	CheckpointDigest domain.ArtifactDigest `json:"checkpoint_digest"`
}

func ComputeCheckpointDigest(value Checkpoint) (domain.ArtifactDigest, error) {
	value.CheckpointDigest = ""
	return domain.Digest(value)
}

type Repository interface {
	Load(
		context.Context,
		domain.TenantID,
		domain.ArtifactDigest,
	) (Checkpoint, error)
	Create(context.Context, Checkpoint) (Checkpoint, error)
	CompareAndSwap(
		context.Context,
		uint64,
		Checkpoint,
	) (Checkpoint, error)
}

type Coordinator struct {
	provider   solve.RouteSeedProvider
	artifacts  artifact.Store
	repository Repository
}

func NewCoordinator(
	provider solve.RouteSeedProvider,
	artifacts artifact.Store,
	repository Repository,
) (*Coordinator, error) {
	if provider == nil {
		return nil, fmt.Errorf("route seed provider is required")
	}
	if artifacts == nil {
		return nil, fmt.Errorf("route seed artifact store is required")
	}
	if repository == nil {
		return nil, fmt.Errorf("route seed checkpoint repository is required")
	}
	identity := provider.Identity()
	if identity.Name == "" || identity.Version == "" || identity.Build == "" {
		return nil, fmt.Errorf("route seed provider identity is incomplete")
	}
	return &Coordinator{
		provider:   provider,
		artifacts:  artifacts,
		repository: repository,
	}, nil
}

func (coordinator *Coordinator) Advance(
	ctx context.Context,
	tenantID domain.TenantID,
	problem domain.ProblemSnapshot,
) (Checkpoint, solve.RouteSeedJob, error) {
	request, checkpoint, err := coordinator.prepare(ctx, tenantID, problem)
	if err != nil {
		return Checkpoint{}, solve.RouteSeedJob{}, err
	}
	for range maxCASAttempts {
		switch checkpoint.State {
		case StatePrepared:
			job, submitErr := coordinator.provider.Submit(ctx, request)
			if submitErr != nil {
				return coordinator.handleProviderError(
					ctx,
					checkpoint,
					"submit",
					submitErr,
				)
			}
			updated, saveErr := coordinator.saveJob(ctx, checkpoint, job)
			if errors.Is(saveErr, ErrCheckpointConflict) {
				checkpoint, err = coordinator.reload(ctx, checkpoint)
				if err != nil {
					return Checkpoint{}, solve.RouteSeedJob{}, err
				}
				continue
			}
			return updated, job, saveErr
		case StatePending:
			job, loadErr := coordinator.loadJob(ctx, checkpoint)
			if loadErr != nil {
				return coordinator.handleJobLoadError(
					ctx,
					checkpoint,
					"resume",
					loadErr,
				)
			}
			job, pollErr := coordinator.provider.Poll(ctx, job)
			if pollErr != nil {
				return coordinator.handleProviderError(
					ctx,
					checkpoint,
					"poll",
					pollErr,
				)
			}
			updated, saveErr := coordinator.saveJob(ctx, checkpoint, job)
			if errors.Is(saveErr, ErrCheckpointConflict) {
				checkpoint, err = coordinator.reload(ctx, checkpoint)
				if err != nil {
					return Checkpoint{}, solve.RouteSeedJob{}, err
				}
				continue
			}
			return updated, job, saveErr
		case StateCancelRequested:
			job, loadErr := coordinator.loadJob(ctx, checkpoint)
			if loadErr != nil {
				return coordinator.handleJobLoadError(
					ctx,
					checkpoint,
					"cancel",
					loadErr,
				)
			}
			job, cancelErr := coordinator.provider.Cancel(ctx, job)
			if cancelErr != nil {
				return coordinator.handleProviderError(
					ctx,
					checkpoint,
					"cancel",
					cancelErr,
				)
			}
			updated, saveErr := coordinator.saveJob(ctx, checkpoint, job)
			if errors.Is(saveErr, ErrCheckpointConflict) {
				checkpoint, err = coordinator.reload(ctx, checkpoint)
				if err != nil {
					return Checkpoint{}, solve.RouteSeedJob{}, err
				}
				continue
			}
			return updated, job, saveErr
		case StateCompleted, StateFailed, StateCanceled:
			if checkpoint.JobArtifact == "" {
				return checkpoint, solve.RouteSeedJob{}, nil
			}
			job, loadErr := coordinator.loadJob(ctx, checkpoint)
			if loadErr != nil {
				return coordinator.handleJobLoadError(
					ctx,
					checkpoint,
					"resume",
					loadErr,
				)
			}
			return checkpoint, job, nil
		case StateManualReview:
			return checkpoint, solve.RouteSeedJob{}, nil
		default:
			return coordinator.recordManualReview(
				ctx,
				checkpoint,
				"resume",
				solve.RouteSeedErrorInvalidPersisted,
			)
		}
	}
	return Checkpoint{}, solve.RouteSeedJob{}, ErrCheckpointConflict
}

func (coordinator *Coordinator) Cancel(
	ctx context.Context,
	tenantID domain.TenantID,
	problem domain.ProblemSnapshot,
) (Checkpoint, solve.RouteSeedJob, error) {
	_, checkpoint, err := coordinator.prepare(ctx, tenantID, problem)
	if err != nil {
		return Checkpoint{}, solve.RouteSeedJob{}, err
	}
	for range maxCASAttempts {
		switch checkpoint.State {
		case StatePrepared:
			next := checkpoint
			next.State = StateCanceled
			next.Version++
			next.CheckpointDigest = ""
			next.CheckpointDigest, err = ComputeCheckpointDigest(next)
			if err != nil {
				return Checkpoint{}, solve.RouteSeedJob{}, err
			}
			updated, swapErr := coordinator.repository.CompareAndSwap(
				ctx,
				checkpoint.Version,
				next,
			)
			if errors.Is(swapErr, ErrCheckpointConflict) {
				checkpoint, err = coordinator.reload(ctx, checkpoint)
				if err != nil {
					return Checkpoint{}, solve.RouteSeedJob{}, err
				}
				continue
			}
			return updated, solve.RouteSeedJob{}, swapErr
		case StatePending:
			next := checkpoint
			next.State = StateCancelRequested
			next.Version++
			next.CheckpointDigest = ""
			next.CheckpointDigest, err = ComputeCheckpointDigest(next)
			if err != nil {
				return Checkpoint{}, solve.RouteSeedJob{}, err
			}
			updated, swapErr := coordinator.repository.CompareAndSwap(
				ctx,
				checkpoint.Version,
				next,
			)
			if errors.Is(swapErr, ErrCheckpointConflict) {
				checkpoint, err = coordinator.reload(ctx, checkpoint)
				if err != nil {
					return Checkpoint{}, solve.RouteSeedJob{}, err
				}
				continue
			}
			if swapErr != nil {
				return Checkpoint{}, solve.RouteSeedJob{}, swapErr
			}
			checkpoint = updated
			return coordinator.Advance(ctx, tenantID, problem)
		case StateCancelRequested:
			return coordinator.Advance(ctx, tenantID, problem)
		case StateCompleted, StateFailed, StateCanceled, StateManualReview:
			if checkpoint.JobArtifact == "" {
				return checkpoint, solve.RouteSeedJob{}, nil
			}
			job, loadErr := coordinator.loadJob(ctx, checkpoint)
			if loadErr != nil {
				return coordinator.handleJobLoadError(
					ctx,
					checkpoint,
					"cancel",
					loadErr,
				)
			}
			return checkpoint, job, nil
		default:
			return coordinator.recordManualReview(
				ctx,
				checkpoint,
				"cancel",
				solve.RouteSeedErrorInvalidPersisted,
			)
		}
	}
	return Checkpoint{}, solve.RouteSeedJob{}, ErrCheckpointConflict
}

func (coordinator *Coordinator) prepare(
	ctx context.Context,
	tenantID domain.TenantID,
	problem domain.ProblemSnapshot,
) (solve.RouteSeedRequest, Checkpoint, error) {
	if tenantID == "" || tenantID != problem.TenantID {
		return solve.RouteSeedRequest{}, Checkpoint{},
			fmt.Errorf("route seed tenant does not match problem")
	}
	request, err := coordinator.provider.Prepare(ctx, problem)
	if err != nil {
		return solve.RouteSeedRequest{}, Checkpoint{}, err
	}
	requestArtifact, err := artifact.New(
		artifact.KindRouteSeedRequest,
		solve.RouteSeedRequestSchemaVersion,
		request,
	)
	if err != nil {
		return solve.RouteSeedRequest{}, Checkpoint{},
			fmt.Errorf("create route seed request artifact: %w", err)
	}
	requestRef := requestArtifact.Ref()
	checkpoint, err := coordinator.repository.Load(
		ctx,
		tenantID,
		request.RequestDigest,
	)
	if err == nil {
		if validationErr := coordinator.validateCheckpoint(
			ctx,
			checkpoint,
			request,
			requestRef.Digest,
		); validationErr != nil {
			return solve.RouteSeedRequest{}, Checkpoint{}, validationErr
		}
		return request, checkpoint, nil
	}
	if !errors.Is(err, ErrCheckpointNotFound) {
		return solve.RouteSeedRequest{}, Checkpoint{}, err
	}
	publishedRef, err := coordinator.artifacts.Put(ctx, tenantID, requestArtifact)
	if err != nil {
		return solve.RouteSeedRequest{}, Checkpoint{},
			fmt.Errorf("put route seed request artifact: %w", err)
	}
	if publishedRef != requestRef {
		return solve.RouteSeedRequest{}, Checkpoint{},
			fmt.Errorf("%w: published request artifact changed identity",
				ErrCheckpointIntegrity)
	}
	if err := coordinator.artifacts.Verify(
		ctx,
		tenantID,
		requestRef.Digest,
	); err != nil {
		return solve.RouteSeedRequest{}, Checkpoint{},
			fmt.Errorf("verify route seed request artifact: %w", err)
	}
	checkpoint = Checkpoint{
		SchemaVersion:   CheckpointSchemaVersion,
		TenantID:        tenantID,
		Provider:        coordinator.provider.Identity(),
		ProblemDigest:   problem.ProblemDigest,
		RequestDigest:   request.RequestDigest,
		RequestArtifact: requestRef.Digest,
		State:           StatePrepared,
		Version:         1,
	}
	checkpoint.CheckpointDigest, err = ComputeCheckpointDigest(checkpoint)
	if err != nil {
		return solve.RouteSeedRequest{}, Checkpoint{}, err
	}
	checkpoint, err = coordinator.repository.Create(ctx, checkpoint)
	if errors.Is(err, ErrCheckpointConflict) {
		checkpoint, err = coordinator.repository.Load(
			ctx,
			tenantID,
			request.RequestDigest,
		)
	}
	if err != nil {
		return solve.RouteSeedRequest{}, Checkpoint{}, err
	}
	if err := coordinator.validateCheckpoint(
		ctx,
		checkpoint,
		request,
		requestRef.Digest,
	); err != nil {
		return solve.RouteSeedRequest{}, Checkpoint{}, err
	}
	return request, checkpoint, nil
}

func (coordinator *Coordinator) saveJob(
	ctx context.Context,
	checkpoint Checkpoint,
	job solve.RouteSeedJob,
) (Checkpoint, error) {
	if err := validateJobBinding(job, checkpoint); err != nil {
		return Checkpoint{}, err
	}
	jobArtifact, err := artifact.New(
		artifact.KindRouteSeedJob,
		solve.RouteSeedJobSchemaVersion,
		job,
	)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("create route seed job artifact: %w", err)
	}
	jobRef, err := coordinator.artifacts.Put(
		ctx,
		checkpoint.TenantID,
		jobArtifact,
	)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("put route seed job artifact: %w", err)
	}
	if err := coordinator.artifacts.Verify(
		ctx,
		checkpoint.TenantID,
		jobRef.Digest,
	); err != nil {
		return Checkpoint{}, fmt.Errorf("verify route seed job artifact: %w", err)
	}
	nextState, err := stateForJob(job.Status)
	if err != nil {
		return Checkpoint{}, err
	}
	if checkpoint.JobArtifact == jobRef.Digest &&
		checkpoint.RemoteJobID == job.RemoteJobID &&
		checkpoint.JobDigest == job.JobDigest &&
		checkpoint.ResponseDigest == job.ResponseDigest &&
		checkpoint.State == nextState &&
		checkpoint.FailureCode == job.FailureCode &&
		checkpoint.FailureOperation == "" {
		return checkpoint, nil
	}
	next := checkpoint
	next.JobArtifact = jobRef.Digest
	next.RemoteJobID = job.RemoteJobID
	next.JobDigest = job.JobDigest
	next.ResponseDigest = job.ResponseDigest
	next.State = nextState
	next.FailureCode = job.FailureCode
	next.FailureOperation = ""
	next.Version++
	next.CheckpointDigest = ""
	next.CheckpointDigest, err = ComputeCheckpointDigest(next)
	if err != nil {
		return Checkpoint{}, err
	}
	return coordinator.repository.CompareAndSwap(
		ctx,
		checkpoint.Version,
		next,
	)
}

func (coordinator *Coordinator) handleProviderError(
	ctx context.Context,
	checkpoint Checkpoint,
	operation string,
	providerErr error,
) (Checkpoint, solve.RouteSeedJob, error) {
	var classified *solve.RouteSeedError
	if !errors.As(providerErr, &classified) || !classified.ManualReview {
		return checkpoint, solve.RouteSeedJob{}, providerErr
	}
	return coordinator.recordManualReview(
		ctx,
		checkpoint,
		operation,
		classified.Code,
	)
}

func (coordinator *Coordinator) handleJobLoadError(
	ctx context.Context,
	checkpoint Checkpoint,
	operation string,
	loadErr error,
) (Checkpoint, solve.RouteSeedJob, error) {
	if !errors.Is(loadErr, ErrCheckpointIntegrity) {
		return checkpoint, solve.RouteSeedJob{}, loadErr
	}
	return coordinator.recordManualReview(
		ctx,
		checkpoint,
		operation,
		solve.RouteSeedErrorInvalidPersisted,
	)
}

func (coordinator *Coordinator) recordManualReview(
	ctx context.Context,
	checkpoint Checkpoint,
	operation string,
	code solve.RouteSeedErrorCode,
) (Checkpoint, solve.RouteSeedJob, error) {
	for range maxCASAttempts {
		if checkpoint.State == StateManualReview {
			return checkpoint, solve.RouteSeedJob{}, nil
		}
		next := checkpoint
		next.State = StateManualReview
		next.FailureCode = string(code)
		next.FailureOperation = operation
		next.Version++
		next.CheckpointDigest = ""
		digest, err := ComputeCheckpointDigest(next)
		if err != nil {
			return Checkpoint{}, solve.RouteSeedJob{}, err
		}
		next.CheckpointDigest = digest
		updated, err := coordinator.repository.CompareAndSwap(
			ctx,
			checkpoint.Version,
			next,
		)
		if err == nil {
			return updated, solve.RouteSeedJob{}, nil
		}
		if !errors.Is(err, ErrCheckpointConflict) {
			return Checkpoint{}, solve.RouteSeedJob{}, err
		}
		checkpoint, err = coordinator.reload(ctx, checkpoint)
		if err != nil {
			return Checkpoint{}, solve.RouteSeedJob{}, err
		}
	}
	return Checkpoint{}, solve.RouteSeedJob{}, ErrCheckpointConflict
}

func (coordinator *Coordinator) reload(
	ctx context.Context,
	checkpoint Checkpoint,
) (Checkpoint, error) {
	current, err := coordinator.repository.Load(
		ctx,
		checkpoint.TenantID,
		checkpoint.RequestDigest,
	)
	if err != nil {
		return Checkpoint{}, err
	}
	if err := validateCheckpointShape(current); err != nil {
		return Checkpoint{}, err
	}
	if current.Provider != checkpoint.Provider ||
		current.ProblemDigest != checkpoint.ProblemDigest ||
		current.RequestDigest != checkpoint.RequestDigest ||
		current.RequestArtifact != checkpoint.RequestArtifact {
		return Checkpoint{}, fmt.Errorf(
			"%w: reloaded checkpoint changed identity",
			ErrCheckpointIntegrity,
		)
	}
	return current, nil
}

func (coordinator *Coordinator) validateCheckpoint(
	ctx context.Context,
	checkpoint Checkpoint,
	request solve.RouteSeedRequest,
	requestArtifact domain.ArtifactDigest,
) error {
	if err := validateCheckpointShape(checkpoint); err != nil {
		return err
	}
	if checkpoint.TenantID != request.Problem.TenantID ||
		checkpoint.Provider != coordinator.provider.Identity() ||
		checkpoint.ProblemDigest != request.ProblemDigest ||
		checkpoint.RequestDigest != request.RequestDigest ||
		checkpoint.RequestArtifact != requestArtifact {
		return fmt.Errorf(
			"%w: checkpoint identity does not match prepared request",
			ErrCheckpointIntegrity,
		)
	}
	storedRequest, err := coordinator.loadRequest(ctx, checkpoint)
	if err != nil {
		return err
	}
	if storedRequest.RequestDigest != request.RequestDigest ||
		storedRequest.Provider != request.Provider ||
		storedRequest.ProblemDigest != request.ProblemDigest {
		return fmt.Errorf(
			"%w: request artifact does not match checkpoint",
			ErrCheckpointIntegrity,
		)
	}
	return nil
}

func validateCheckpointShape(value Checkpoint) error {
	digest, err := ComputeCheckpointDigest(value)
	if err != nil ||
		value.SchemaVersion != CheckpointSchemaVersion ||
		value.TenantID == "" ||
		value.Provider.Name == "" ||
		value.Provider.Version == "" ||
		value.Provider.Build == "" ||
		!domain.ValidArtifactDigest(value.ProblemDigest) ||
		!domain.ValidArtifactDigest(value.RequestDigest) ||
		!domain.ValidArtifactDigest(value.RequestArtifact) ||
		value.Version == 0 ||
		value.CheckpointDigest != digest {
		return fmt.Errorf("%w: checkpoint shape is invalid", ErrCheckpointIntegrity)
	}
	switch value.State {
	case StatePrepared:
		if value.JobArtifact != "" ||
			value.RemoteJobID != "" ||
			value.JobDigest != "" ||
			value.ResponseDigest != "" ||
			value.FailureCode != "" ||
			value.FailureOperation != "" {
			return fmt.Errorf(
				"%w: prepared checkpoint has result fields",
				ErrCheckpointIntegrity,
			)
		}
	case StatePending, StateCancelRequested, StateCompleted:
		if !hasJobIdentity(value) {
			return fmt.Errorf(
				"%w: route seed checkpoint is missing job identity",
				ErrCheckpointIntegrity,
			)
		}
		if value.FailureCode != "" || value.FailureOperation != "" {
			return fmt.Errorf(
				"%w: non-failed checkpoint has failure fields",
				ErrCheckpointIntegrity,
			)
		}
	case StateFailed:
		if !hasJobIdentity(value) ||
			value.FailureCode == "" ||
			value.FailureOperation != "" {
			return fmt.Errorf(
				"%w: failed checkpoint has invalid failure fields",
				ErrCheckpointIntegrity,
			)
		}
	case StateCanceled:
		if value.JobArtifact != "" && !hasJobIdentity(value) {
			return fmt.Errorf(
				"%w: canceled checkpoint has partial job identity",
				ErrCheckpointIntegrity,
			)
		}
		if value.FailureCode != "" || value.FailureOperation != "" {
			return fmt.Errorf(
				"%w: canceled checkpoint has failure fields",
				ErrCheckpointIntegrity,
			)
		}
	case StateManualReview:
		if value.FailureCode == "" || value.FailureOperation == "" {
			return fmt.Errorf(
				"%w: manual-review checkpoint has no failure classification",
				ErrCheckpointIntegrity,
			)
		}
	default:
		return fmt.Errorf("%w: checkpoint state is invalid", ErrCheckpointIntegrity)
	}
	return nil
}

func hasJobIdentity(value Checkpoint) bool {
	return domain.ValidArtifactDigest(value.JobArtifact) &&
		value.RemoteJobID != "" &&
		domain.ValidArtifactDigest(value.JobDigest) &&
		domain.ValidArtifactDigest(value.ResponseDigest)
}

func (coordinator *Coordinator) loadRequest(
	ctx context.Context,
	checkpoint Checkpoint,
) (solve.RouteSeedRequest, error) {
	var request solve.RouteSeedRequest
	if err := coordinator.loadArtifact(
		ctx,
		checkpoint.TenantID,
		checkpoint.RequestArtifact,
		artifact.KindRouteSeedRequest,
		solve.RouteSeedRequestSchemaVersion,
		&request,
	); err != nil {
		return solve.RouteSeedRequest{}, err
	}
	digest, err := solve.ComputeRouteSeedRequestDigest(request)
	if err != nil ||
		request.SchemaVersion != solve.RouteSeedRequestSchemaVersion ||
		request.Provider != checkpoint.Provider ||
		request.ProblemDigest != checkpoint.ProblemDigest ||
		request.RequestDigest != checkpoint.RequestDigest ||
		request.RequestDigest != digest {
		return solve.RouteSeedRequest{}, fmt.Errorf(
			"%w: request artifact binding mismatch",
			ErrCheckpointIntegrity,
		)
	}
	return request, nil
}

func (coordinator *Coordinator) loadJob(
	ctx context.Context,
	checkpoint Checkpoint,
) (solve.RouteSeedJob, error) {
	var job solve.RouteSeedJob
	if err := coordinator.loadArtifact(
		ctx,
		checkpoint.TenantID,
		checkpoint.JobArtifact,
		artifact.KindRouteSeedJob,
		solve.RouteSeedJobSchemaVersion,
		&job,
	); err != nil {
		return solve.RouteSeedJob{}, err
	}
	if err := validateJobBinding(job, checkpoint); err != nil {
		return solve.RouteSeedJob{}, err
	}
	if job.RemoteJobID != checkpoint.RemoteJobID ||
		job.ResponseDigest != checkpoint.ResponseDigest ||
		job.JobDigest != checkpoint.JobDigest ||
		job.BackendVersion != checkpoint.Provider.Version ||
		job.BackendBuild != checkpoint.Provider.Build ||
		job.FailureCode != checkpoint.FailureCode {
		return solve.RouteSeedJob{}, fmt.Errorf(
			"%w: job artifact binding mismatch",
			ErrCheckpointIntegrity,
		)
	}
	return job, nil
}

func validateJobBinding(
	job solve.RouteSeedJob,
	checkpoint Checkpoint,
) error {
	digest, err := solve.ComputeRouteSeedJobDigest(job)
	if err != nil ||
		job.SchemaVersion != solve.RouteSeedJobSchemaVersion ||
		job.Provider != checkpoint.Provider ||
		job.ProblemDigest != checkpoint.ProblemDigest ||
		job.RequestDigest != checkpoint.RequestDigest ||
		!domain.ValidArtifactDigest(job.ResponseDigest) ||
		job.BackendVersion != checkpoint.Provider.Version ||
		job.BackendBuild != checkpoint.Provider.Build ||
		job.JobDigest != digest {
		return fmt.Errorf(
			"%w: route seed job does not match checkpoint",
			ErrCheckpointIntegrity,
		)
	}
	if !remoteJobIDPattern.MatchString(job.RemoteJobID) {
		return fmt.Errorf(
			"%w: route seed remote job ID is invalid",
			ErrCheckpointIntegrity,
		)
	}
	if _, err := stateForJob(job.Status); err != nil {
		return err
	}
	return validateLoadedJobResult(job)
}

func validateLoadedJobResult(job solve.RouteSeedJob) error {
	invalid := func() error {
		return fmt.Errorf(
			"%w: job artifact result fields are invalid",
			ErrCheckpointIntegrity,
		)
	}
	switch job.Status {
	case solve.RouteSeedPending, solve.RouteSeedCanceled:
		if job.Seed.RouteSeedDigest != "" || job.FailureCode != "" {
			return invalid()
		}
	case solve.RouteSeedCompleted:
		digest, err := solve.ComputeRouteSeedDigest(job.Seed)
		if err != nil ||
			job.Seed.SchemaVersion != solve.RouteSeedSchemaVersion ||
			job.Seed.Provider.Name == "" ||
			job.Seed.Provider.Version == "" ||
			job.Seed.Provider.Build == "" ||
			job.Seed.RouteSeedDigest != digest ||
			job.FailureCode != "" {
			return invalid()
		}
	case solve.RouteSeedFailed:
		if job.Seed.RouteSeedDigest != "" || job.FailureCode == "" {
			return invalid()
		}
	default:
		return invalid()
	}
	return nil
}

func (coordinator *Coordinator) loadArtifact(
	ctx context.Context,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
	kind artifact.Kind,
	schemaVersion string,
	target any,
) error {
	reader, metadata, err := coordinator.artifacts.Open(ctx, tenantID, digest)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled),
			errors.Is(err, context.DeadlineExceeded):
			return err
		case errors.Is(err, artifact.ErrNotFound),
			errors.Is(err, artifact.ErrIntegrity):
			return fmt.Errorf(
				"%w: open artifact: %v",
				ErrCheckpointIntegrity,
				err,
			)
		default:
			return fmt.Errorf("open route seed artifact: %w", err)
		}
	}
	defer reader.Close()
	if metadata.Kind != kind || metadata.SchemaVersion != schemaVersion {
		return fmt.Errorf(
			"%w: artifact metadata mismatch",
			ErrCheckpointIntegrity,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxRouteSeedArtifactSize+1))
	if err != nil {
		return fmt.Errorf("read route seed artifact: %w", err)
	}
	if len(raw) > maxRouteSeedArtifactSize {
		return fmt.Errorf("%w: artifact exceeds size limit", ErrCheckpointIntegrity)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: decode artifact: %v", ErrCheckpointIntegrity, err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: artifact has trailing JSON", ErrCheckpointIntegrity)
	}
	return nil
}

func stateForJob(status solve.RouteSeedJobStatus) (State, error) {
	switch status {
	case solve.RouteSeedPending:
		return StatePending, nil
	case solve.RouteSeedCompleted:
		return StateCompleted, nil
	case solve.RouteSeedFailed:
		return StateFailed, nil
	case solve.RouteSeedCanceled:
		return StateCanceled, nil
	default:
		return "", fmt.Errorf(
			"%w: unsupported route seed job status %q",
			ErrCheckpointIntegrity,
			status,
		)
	}
}
