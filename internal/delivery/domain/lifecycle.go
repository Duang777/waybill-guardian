package domain

import "time"

type ProblemVersion struct {
	TenantID         TenantID       `json:"tenant_id"`
	ProblemID        ProblemID      `json:"problem_id"`
	Version          uint64         `json:"version"`
	ProblemDigest    ArtifactDigest `json:"problem_digest"`
	PolicyDigest     ArtifactDigest `json:"policy_digest"`
	CommitmentDigest ArtifactDigest `json:"commitment_digest"`
	ManifestDigest   ArtifactDigest `json:"manifest_digest"`
	ProblemArtifact  ArtifactDigest `json:"problem_artifact_digest"`
	SourceProfile    string         `json:"source_profile"`
	SourceRef        string         `json:"source_ref"`
	CreatedAt        time.Time      `json:"created_at"`
}

type RunStatus string

const (
	RunRequested         RunStatus = "requested"
	RunQueued            RunStatus = "queued"
	RunSolving           RunStatus = "solving"
	RunValidating        RunStatus = "validating"
	RunSucceeded         RunStatus = "succeeded"
	RunCandidateRejected RunStatus = "candidate_rejected"
	RunExhausted         RunStatus = "exhausted"
	RunAborted           RunStatus = "aborted"
	RunFailed            RunStatus = "failed"
	RunCancelled         RunStatus = "cancelled"
	RunManualReview      RunStatus = "manual_review"
)

func (status RunStatus) Terminal() bool {
	switch status {
	case RunSucceeded,
		RunCandidateRejected,
		RunExhausted,
		RunAborted,
		RunFailed,
		RunCancelled,
		RunManualReview:
		return true
	default:
		return false
	}
}

type OptimizationRun struct {
	TenantID          TenantID          `json:"tenant_id"`
	ID                OptimizationRunID `json:"run_id"`
	ProblemID         ProblemID         `json:"problem_id"`
	ProblemVersion    uint64            `json:"problem_version"`
	ProblemDigest     ArtifactDigest    `json:"problem_digest"`
	SolverProfile     string            `json:"solver_profile"`
	ConfigDigest      ArtifactDigest    `json:"config_digest"`
	RequestedBy       string            `json:"requested_by"`
	Status            RunStatus         `json:"status"`
	Version           uint64            `json:"version"`
	CancelRequestedAt *time.Time        `json:"cancel_requested_at,omitempty"`
	CheckpointDigest  ArtifactDigest    `json:"checkpoint_digest,omitempty"`
	ResultRevisionID  PlanRevisionID    `json:"result_revision_id,omitempty"`
	FailureCode       string            `json:"failure_code,omitempty"`
	LeaseOwner        string            `json:"lease_owner,omitempty"`
	LeaseDeadline     *time.Time        `json:"lease_deadline,omitempty"`
	FencingToken      uint64            `json:"fencing_token"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	ClosedAt          *time.Time        `json:"closed_at,omitempty"`
}

type RevisionStatus string

const (
	RevisionCandidate              RevisionStatus = "candidate"
	RevisionValidated              RevisionStatus = "validated"
	RevisionAwaitingApproval       RevisionStatus = "awaiting_approval"
	RevisionApproved               RevisionStatus = "approved"
	RevisionApplying               RevisionStatus = "applying"
	RevisionActive                 RevisionStatus = "active"
	RevisionRejected               RevisionStatus = "rejected"
	RevisionExpired                RevisionStatus = "expired"
	RevisionStale                  RevisionStatus = "stale"
	RevisionReconciliationRequired RevisionStatus = "reconciliation_required"
	RevisionPartiallyApplied       RevisionStatus = "partially_applied"
	RevisionSuperseded             RevisionStatus = "superseded"
	RevisionCompleted              RevisionStatus = "completed"
)

type PlanRevision struct {
	TenantID               TenantID          `json:"tenant_id"`
	ID                     PlanRevisionID    `json:"revision_id"`
	PlanID                 PlanID            `json:"plan_id"`
	BaseRevisionID         PlanRevisionID    `json:"base_revision_id,omitempty"`
	RunID                  OptimizationRunID `json:"run_id"`
	ProblemDigest          ArtifactDigest    `json:"problem_digest"`
	PolicyDigest           ArtifactDigest    `json:"policy_digest"`
	CommitmentDigest       ArtifactDigest    `json:"commitment_digest"`
	PlanArtifactDigest     ArtifactDigest    `json:"plan_artifact_digest"`
	PlanDigest             ArtifactDigest    `json:"plan_digest"`
	ValidationArtifact     ArtifactDigest    `json:"validation_artifact_digest"`
	ValidationReportDigest ArtifactDigest    `json:"validation_report_digest"`
	EffectSetArtifact      ArtifactDigest    `json:"effect_set_artifact_digest,omitempty"`
	EffectSetDigest        ArtifactDigest    `json:"effect_set_digest,omitempty"`
	Status                 RevisionStatus    `json:"status"`
	Version                uint64            `json:"version"`
	CreatedAt              time.Time         `json:"created_at"`
	UpdatedAt              time.Time         `json:"updated_at"`
}

type DispatchPlan struct {
	TenantID       TenantID       `json:"tenant_id"`
	ID             PlanID         `json:"plan_id"`
	ProblemID      ProblemID      `json:"problem_id"`
	ActiveRevision PlanRevisionID `json:"active_revision_id,omitempty"`
	ActiveVersion  uint64         `json:"active_version"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}
