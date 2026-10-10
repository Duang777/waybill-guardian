package solve

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const (
	SolveConfigSchemaVersion      = "delivery.solve-config.v1"
	SolveEvidenceVersion          = "delivery.solve-evidence.v2"
	SolverCapabilitiesVersion     = "delivery.solver-capabilities.v1"
	RouteSeedSchemaVersion        = "delivery.route-seed.v1"
	RouteSeedRequestSchemaVersion = "delivery.route-seed-request.v1"
	RouteSeedJobSchemaVersion     = "delivery.route-seed-job.v1"
)

var (
	ErrAborted              = errors.New("solve aborted")
	ErrCapability           = errors.New("solver capability mismatch")
	ErrInvalidConfig        = errors.New("invalid solve configuration")
	ErrInvalidRouteSeed     = errors.New("invalid route seed")
	ErrRouteSeedUnavailable = errors.New("route seed provider unavailable")
	ErrRouteSeedRejected    = errors.New("route seed request rejected")
	ErrRouteSeedIntegrity   = errors.New("route seed integrity check failed")
)

type EvaluationBudget uint64

type SolveStatus string

type TerminationReason string

type SearchPhase string

type RouteSeedErrorCode string

type RouteSeedProtocol string

const (
	RouteSeedVROOM   RouteSeedProtocol = "vroom"
	RouteSeedORTools RouteSeedProtocol = "ortools"
)

const (
	RouteSeedErrorCapability       RouteSeedErrorCode = "capability_mismatch"
	RouteSeedErrorCanceled         RouteSeedErrorCode = "canceled"
	RouteSeedErrorDeadline         RouteSeedErrorCode = "deadline_exceeded"
	RouteSeedErrorUnavailable      RouteSeedErrorCode = "unavailable"
	RouteSeedErrorUnauthorized     RouteSeedErrorCode = "unauthorized"
	RouteSeedErrorRejected         RouteSeedErrorCode = "rejected"
	RouteSeedErrorProtocol         RouteSeedErrorCode = "protocol_invalid"
	RouteSeedErrorBinding          RouteSeedErrorCode = "binding_mismatch"
	RouteSeedErrorInvalidPersisted RouteSeedErrorCode = "invalid_persisted_job"
)

type RouteSeedError struct {
	Code         RouteSeedErrorCode
	Operation    string
	HTTPStatus   int
	Retryable    bool
	ManualReview bool
	cause        error
}

func (value *RouteSeedError) Error() string {
	if value == nil {
		return "<nil>"
	}
	message := "route seed " + value.Operation + ": " + string(value.Code)
	if value.HTTPStatus != 0 {
		message = fmt.Sprintf("%s (HTTP %d)", message, value.HTTPStatus)
	}
	if value.cause != nil {
		message += ": " + value.cause.Error()
	}
	return message
}

func (value *RouteSeedError) Unwrap() error {
	if value == nil {
		return nil
	}
	return value.cause
}

func (value *RouteSeedError) Is(target error) bool {
	if value == nil {
		return false
	}
	switch target {
	case ErrCapability:
		return value.Code == RouteSeedErrorCapability
	case ErrInvalidRouteSeed:
		return value.Code == RouteSeedErrorProtocol ||
			value.Code == RouteSeedErrorBinding ||
			value.Code == RouteSeedErrorInvalidPersisted
	case ErrRouteSeedUnavailable:
		return value.Code == RouteSeedErrorDeadline ||
			value.Code == RouteSeedErrorUnavailable
	case ErrRouteSeedRejected:
		return value.Code == RouteSeedErrorUnauthorized ||
			value.Code == RouteSeedErrorRejected
	case ErrRouteSeedIntegrity:
		return value.Code == RouteSeedErrorBinding
	case context.Canceled:
		return value.Code == RouteSeedErrorCanceled
	case context.DeadlineExceeded:
		return value.Code == RouteSeedErrorDeadline
	default:
		return false
	}
}

const (
	SolveCompleted    SolveStatus = "completed"
	SolveInfeasible   SolveStatus = "infeasible"
	SolveExhausted    SolveStatus = "exhausted"
	SolveAborted      SolveStatus = "aborted"
	SolveUnsupported  SolveStatus = "unsupported"
	SolveManualReview SolveStatus = "manual_review"
)

const (
	TerminationConstructionComplete TerminationReason = "construction_complete"
	TerminationLocalOptimum         TerminationReason = "local_optimum"
	TerminationBudgetExhausted      TerminationReason = "budget_exhausted"
	TerminationCanceled             TerminationReason = "context_canceled"
	TerminationDeadlineExceeded     TerminationReason = "deadline_exceeded"
	TerminationCommitmentConflict   TerminationReason = "commitment_conflict"
	TerminationInfeasible           TerminationReason = "presolve_infeasible"
	TerminationUnsupported          TerminationReason = "unsupported"
	TerminationInternalFailure      TerminationReason = "internal_failure"
)

const (
	SearchPhaseConstruction  SearchPhase = "construction"
	SearchPhaseLocalSearch   SearchPhase = "local_search"
	SearchPhaseCertification SearchPhase = "certification"
)

type Solver interface {
	Identity() domain.SolverIdentity
	Capabilities(context.Context) (Capabilities, error)
	Solve(
		context.Context,
		domain.ProblemSnapshot,
		SolveConfig,
		ProgressSink,
	) (SolveResult, error)
}

type Capabilities struct {
	SchemaVersion         string `json:"schema_version"`
	MultiDepot            bool   `json:"multi_depot"`
	MultiTrip             bool   `json:"multi_trip"`
	PickupDelivery        bool   `json:"pickup_delivery"`
	SplitByUnit           bool   `json:"split_by_unit"`
	HeterogeneousFleet    bool   `json:"heterogeneous_fleet"`
	DriverRegulations     bool   `json:"driver_regulations"`
	ElectricVehicles      bool   `json:"electric_vehicles"`
	ChargingCapacity      bool   `json:"charging_capacity"`
	ThreeDimensionalLoad  bool   `json:"three_dimensional_load"`
	AxleAndCenterOfMass   bool   `json:"axle_and_center_of_mass"`
	StopAccessibility     bool   `json:"stop_accessibility"`
	DynamicCommitments    bool   `json:"dynamic_commitments"`
	DeterministicReplay   bool   `json:"deterministic_replay"`
	RemoteJobContinuation bool   `json:"remote_job_continuation"`
}

type RouteSeedRequest struct {
	SchemaVersion string                 `json:"schema_version"`
	Protocol      RouteSeedProtocol      `json:"protocol"`
	Provider      domain.SolverIdentity  `json:"provider"`
	ProblemDigest domain.ArtifactDigest  `json:"problem_digest"`
	Problem       domain.ProblemSnapshot `json:"problem"`
	RequestDigest domain.ArtifactDigest  `json:"request_digest"`
}

func ComputeRouteSeedRequestDigest(
	value RouteSeedRequest,
) (domain.ArtifactDigest, error) {
	value.RequestDigest = ""
	return domain.Digest(value)
}

type SolveConfig struct {
	SchemaVersion    string                `json:"schema_version"`
	Strategy         string                `json:"strategy"`
	EvaluationBudget EvaluationBudget      `json:"evaluation_budget"`
	Seed             uint64                `json:"seed"`
	PlanID           domain.PlanID         `json:"plan_id"`
	RevisionID       domain.PlanRevisionID `json:"revision_id"`
	ValidationAt     time.Time             `json:"validation_at"`
	RouteSeedDigest  domain.ArtifactDigest `json:"route_seed_digest"`
}

func BuildSolveConfig(value SolveConfig) (SolveConfig, domain.ArtifactDigest, error) {
	value.ValidationAt = value.ValidationAt.UTC()
	if value.SchemaVersion != SolveConfigSchemaVersion {
		return SolveConfig{}, "", fmt.Errorf(
			"%w: schema_version must be %q",
			ErrInvalidConfig,
			SolveConfigSchemaVersion,
		)
	}
	if value.Strategy == "" {
		return SolveConfig{}, "", fmt.Errorf("%w: strategy is required", ErrInvalidConfig)
	}
	if value.EvaluationBudget == 0 {
		return SolveConfig{}, "", fmt.Errorf(
			"%w: evaluation_budget must be positive",
			ErrInvalidConfig,
		)
	}
	if value.PlanID == "" || value.RevisionID == "" {
		return SolveConfig{}, "", fmt.Errorf(
			"%w: plan_id and revision_id are required",
			ErrInvalidConfig,
		)
	}
	if value.ValidationAt.IsZero() {
		return SolveConfig{}, "", fmt.Errorf(
			"%w: validation_at is required",
			ErrInvalidConfig,
		)
	}
	if value.RouteSeedDigest != "" && !domain.ValidArtifactDigest(value.RouteSeedDigest) {
		return SolveConfig{}, "", fmt.Errorf(
			"%w: route_seed_digest must be a lowercase SHA-256 value",
			ErrInvalidConfig,
		)
	}
	digest, err := domain.Digest(struct {
		SchemaVersion    string                `json:"schema_version"`
		Strategy         string                `json:"strategy"`
		EvaluationBudget EvaluationBudget      `json:"evaluation_budget"`
		Seed             uint64                `json:"seed"`
		RouteSeedDigest  domain.ArtifactDigest `json:"route_seed_digest"`
	}{
		SchemaVersion:    value.SchemaVersion,
		Strategy:         value.Strategy,
		EvaluationBudget: value.EvaluationBudget,
		Seed:             value.Seed,
		RouteSeedDigest:  value.RouteSeedDigest,
	})
	if err != nil {
		return SolveConfig{}, "", fmt.Errorf("%w: digest: %v", ErrInvalidConfig, err)
	}
	return value, digest, nil
}

type ProgressPhase string

const (
	ProgressBaseline   ProgressPhase = "baseline"
	ProgressRouting    ProgressPhase = "routing"
	ProgressLoading    ProgressPhase = "loading"
	ProgressImproving  ProgressPhase = "improving"
	ProgressValidating ProgressPhase = "validating"
)

type Progress struct {
	Phase              ProgressPhase          `json:"phase"`
	Evaluations        EvaluationBudget       `json:"evaluations"`
	Budget             EvaluationBudget       `json:"budget"`
	BestObjective      domain.ObjectiveVector `json:"best_objective"`
	BestPlanDigest     domain.ArtifactDigest  `json:"best_plan_digest"`
	FeasibleCandidates uint64                 `json:"feasible_candidates"`
	RejectedCandidates uint64                 `json:"rejected_candidates"`
	NoGoodCuts         uint64                 `json:"no_good_cuts"`
}

type ProgressSink interface {
	Report(context.Context, Progress) error
}

type ProgressSinkFunc func(context.Context, Progress) error

func (function ProgressSinkFunc) Report(ctx context.Context, progress Progress) error {
	return function(ctx, progress)
}

type SolveEvidence struct {
	SchemaVersion        string                  `json:"schema_version"`
	ProblemDigest        domain.ArtifactDigest   `json:"problem_digest"`
	ConfigDigest         domain.ArtifactDigest   `json:"config_digest"`
	Solver               domain.SolverIdentity   `json:"solver"`
	Status               SolveStatus             `json:"status"`
	Termination          TerminationReason       `json:"termination"`
	BudgetLimit          EvaluationBudget        `json:"budget_limit"`
	Evaluations          EvaluationBudget        `json:"evaluations"`
	ConstructionAttempts uint64                  `json:"construction_attempts"`
	ConstructionAccepted uint64                  `json:"construction_accepted"`
	FeasibleCandidates   uint64                  `json:"feasible_candidates"`
	RejectedCandidates   uint64                  `json:"rejected_candidates"`
	RegistryDigest       domain.ArtifactDigest   `json:"registry_digest"`
	TerminalCursor       SearchCursor            `json:"terminal_cursor"`
	IncumbentStateKey    domain.ArtifactDigest   `json:"incumbent_state_key"`
	AcceptedStateKeys    []domain.ArtifactDigest `json:"accepted_state_keys"`
	LearnedNoGoods       uint64                  `json:"learned_no_goods"`
	AppliedNoGoods       uint64                  `json:"applied_no_goods"`
	NoGoodDigest         domain.ArtifactDigest   `json:"no_good_digest"`
	RouteSeedDigest      domain.ArtifactDigest   `json:"route_seed_digest"`
	OperatorEvaluations  []OperatorEvaluation    `json:"operator_evaluations"`
	ConflictSet          []domain.ObjectRef      `json:"conflict_set"`
	EvidenceDigest       domain.ArtifactDigest   `json:"evidence_digest"`
}

type OperatorEvaluation struct {
	Operator OperatorID `json:"operator"`
	Attempts uint64     `json:"attempts"`
	Feasible uint64     `json:"feasible"`
	Rejected uint64     `json:"rejected"`
	Accepted uint64     `json:"accepted"`
}

type SearchCursor struct {
	Phase    SearchPhase `json:"phase"`
	Operator OperatorID  `json:"operator"`
	MoveKey  string      `json:"move_key"`
}

func ComputeEvidenceDigest(value SolveEvidence) (domain.ArtifactDigest, error) {
	value.EvidenceDigest = ""
	return domain.Digest(value)
}

type SolveResult struct {
	Status     SolveStatus             `json:"status"`
	Plan       domain.Plan             `json:"plan"`
	Validation domain.ValidationReport `json:"validation"`
	Evidence   SolveEvidence           `json:"evidence"`
}

type RouteSeed struct {
	SchemaVersion   string                     `json:"schema_version"`
	Provider        domain.SolverIdentity      `json:"provider"`
	Routes          []SeedRoute                `json:"routes"`
	Unassigned      []domain.FulfillmentUnitID `json:"unassigned"`
	RouteSeedDigest domain.ArtifactDigest      `json:"route_seed_digest"`
}

type SeedRoute struct {
	VehicleID domain.VehicleID `json:"vehicle_id"`
	DriverID  domain.DriverID  `json:"driver_id"`
	DepotID   domain.DepotID   `json:"depot_id"`
	TripIndex uint16           `json:"trip_index"`
	TaskIDs   []domain.TaskID  `json:"task_ids"`
}

func ComputeRouteSeedDigest(value RouteSeed) (domain.ArtifactDigest, error) {
	value.RouteSeedDigest = ""
	return domain.Digest(value)
}

type RouteSeedJobStatus string

const (
	RouteSeedPending   RouteSeedJobStatus = "pending"
	RouteSeedCompleted RouteSeedJobStatus = "completed"
	RouteSeedFailed    RouteSeedJobStatus = "failed"
	RouteSeedCanceled  RouteSeedJobStatus = "canceled"
)

type RouteSeedJob struct {
	SchemaVersion  string                `json:"schema_version"`
	Provider       domain.SolverIdentity `json:"provider"`
	RemoteJobID    string                `json:"remote_job_id"`
	Status         RouteSeedJobStatus    `json:"status"`
	ProblemDigest  domain.ArtifactDigest `json:"problem_digest"`
	RequestDigest  domain.ArtifactDigest `json:"request_digest"`
	ResponseDigest domain.ArtifactDigest `json:"response_digest"`
	BackendVersion string                `json:"backend_version"`
	BackendBuild   string                `json:"backend_build"`
	Seed           RouteSeed             `json:"seed"`
	FailureCode    string                `json:"failure_code"`
	JobDigest      domain.ArtifactDigest `json:"job_digest"`
}

func ComputeRouteSeedJobDigest(value RouteSeedJob) (domain.ArtifactDigest, error) {
	value.JobDigest = ""
	return domain.Digest(value)
}

type RouteSeedProvider interface {
	Identity() domain.SolverIdentity
	Capabilities(context.Context) (Capabilities, error)
	Prepare(context.Context, domain.ProblemSnapshot) (RouteSeedRequest, error)
	Submit(context.Context, RouteSeedRequest) (RouteSeedJob, error)
	Poll(context.Context, RouteSeedJob) (RouteSeedJob, error)
	Cancel(context.Context, RouteSeedJob) (RouteSeedJob, error)
}
