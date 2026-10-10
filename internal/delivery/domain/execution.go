package domain

import (
	"encoding/json"
	"time"
)

const (
	EffectSetSchemaVersion     = "delivery.effect-set.v1"
	EffectBindingSchemaVersion = "delivery.effect-binding.v1"
	EffectRequestSchemaVersion = "delivery.effect-request.v1"
	EffectResultSchemaVersion  = "delivery.effect-result.v1"
)

type ApprovalStatus string

const (
	ApprovalPending   ApprovalStatus = "pending"
	ApprovalConfirmed ApprovalStatus = "confirmed"
	ApprovalRejected  ApprovalStatus = "rejected"
	ApprovalExpired   ApprovalStatus = "expired"
	ApprovalStale     ApprovalStatus = "stale"
)

type ApprovalBinding struct {
	TenantID               TenantID       `json:"tenant_id"`
	PlanID                 PlanID         `json:"plan_id"`
	RevisionID             PlanRevisionID `json:"revision_id"`
	BaseRevisionID         PlanRevisionID `json:"base_revision_id,omitempty"`
	ActiveVersion          uint64         `json:"active_version"`
	ProblemDigest          ArtifactDigest `json:"problem_digest"`
	PolicyDigest           ArtifactDigest `json:"policy_digest"`
	CommitmentDigest       ArtifactDigest `json:"commitment_digest"`
	PlanDigest             ArtifactDigest `json:"plan_digest"`
	ValidationReportDigest ArtifactDigest `json:"validation_report_digest"`
	EffectSetDigest        ArtifactDigest `json:"effect_set_digest"`
}

type PlanApproval struct {
	TenantID          TenantID        `json:"tenant_id"`
	ID                ApprovalID      `json:"approval_id"`
	Binding           ApprovalBinding `json:"binding"`
	EffectSetArtifact ArtifactDigest  `json:"effect_set_artifact_digest"`
	Status            ApprovalStatus  `json:"status"`
	Version           uint64          `json:"version"`
	RequestedBy       string          `json:"requested_by"`
	PlanCreatedBy     string          `json:"plan_created_by"`
	Reason            string          `json:"reason"`
	RequestedAt       time.Time       `json:"requested_at"`
	ExpiresAt         time.Time       `json:"expires_at"`
	DecidedBy         string          `json:"decided_by,omitempty"`
	DecidedAt         *time.Time      `json:"decided_at,omitempty"`
	RejectReason      string          `json:"reject_reason,omitempty"`
	OverrideReason    string          `json:"override_reason,omitempty"`
	ExecutionID       ExecutionID     `json:"execution_id,omitempty"`
}

type EffectAction string

const (
	EffectCreateRoute   EffectAction = "tms.route.create"
	EffectAssignVehicle EffectAction = "tms.vehicle.assign"
	EffectAssignDriver  EffectAction = "tms.driver.assign"
	EffectPublishStops  EffectAction = "tms.stops.publish"
	EffectPublishLoad   EffectAction = "wms.load.publish"
	EffectNotifyETA     EffectAction = "notify.eta.publish"
)

type EffectPreview struct {
	ID                             EffectID        `json:"effect_id"`
	Ordinal                        uint32          `json:"ordinal"`
	Action                         EffectAction    `json:"action"`
	Target                         string          `json:"target"`
	Parameters                     json.RawMessage `json:"parameters"`
	ParametersDigest               ArtifactDigest  `json:"parameters_digest"`
	Required                       bool            `json:"required"`
	AdapterID                      string          `json:"adapter_id"`
	ContractVersion                string          `json:"contract_version"`
	KeyRetentionSeconds            int64           `json:"key_retention_seconds"`
	LookupConsistencyWindowSeconds int64           `json:"lookup_consistency_window_seconds"`
}

type EffectSet struct {
	SchemaVersion string          `json:"schema_version"`
	TenantID      TenantID        `json:"tenant_id"`
	RevisionID    PlanRevisionID  `json:"revision_id"`
	Effects       []EffectPreview `json:"effects"`
	Digest        ArtifactDigest  `json:"digest"`
}

type ExecutionStatus string

const (
	ExecutionPrepared               ExecutionStatus = "prepared"
	ExecutionExecuting              ExecutionStatus = "executing"
	ExecutionCommitted              ExecutionStatus = "committed"
	ExecutionReconciliationRequired ExecutionStatus = "reconciliation_required"
	ExecutionPartiallyApplied       ExecutionStatus = "partially_applied"
	ExecutionManualReview           ExecutionStatus = "manual_review"
)

type DispatchExecution struct {
	TenantID        TenantID        `json:"tenant_id"`
	ID              ExecutionID     `json:"execution_id"`
	ApprovalID      ApprovalID      `json:"approval_id"`
	PlanID          PlanID          `json:"plan_id"`
	RevisionID      PlanRevisionID  `json:"revision_id"`
	EffectSetDigest ArtifactDigest  `json:"effect_set_digest"`
	Status          ExecutionStatus `json:"status"`
	Version         uint64          `json:"version"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
}

type EffectStatus string

const (
	EffectPrepared        EffectStatus = "prepared"
	EffectDispatching     EffectStatus = "dispatching"
	EffectSucceeded       EffectStatus = "succeeded"
	EffectUnknown         EffectStatus = "unknown"
	EffectReconciling     EffectStatus = "reconciling"
	EffectRetryWait       EffectStatus = "retry_wait"
	EffectPermanentFailed EffectStatus = "permanent_failed"
	EffectManualReview    EffectStatus = "manual_review"
)

type EffectOperation string

const (
	EffectOperationDispatch EffectOperation = "dispatch"
	EffectOperationLookup   EffectOperation = "lookup"
)

type EffectRecord struct {
	TenantID                       TenantID        `json:"tenant_id"`
	ID                             EffectID        `json:"effect_id"`
	ExecutionID                    ExecutionID     `json:"execution_id"`
	RevisionID                     PlanRevisionID  `json:"revision_id"`
	Ordinal                        uint32          `json:"ordinal"`
	Action                         EffectAction    `json:"action"`
	Target                         string          `json:"target"`
	Parameters                     json.RawMessage `json:"parameters"`
	ParametersDigest               ArtifactDigest  `json:"parameters_digest"`
	Required                       bool            `json:"required"`
	AdapterID                      string          `json:"adapter_id"`
	ContractVersion                string          `json:"contract_version"`
	AdapterBinding                 json.RawMessage `json:"adapter_binding"`
	AdapterBindingDigest           ArtifactDigest  `json:"adapter_binding_digest"`
	IdempotencyKey                 string          `json:"idempotency_key"`
	RequestDigest                  ArtifactDigest  `json:"request_digest"`
	KeyCreatedAt                   time.Time       `json:"key_created_at"`
	KeyExpiresAt                   time.Time       `json:"key_expires_at"`
	LookupConsistencyWindowSeconds int64           `json:"lookup_consistency_window_seconds"`
	Status                         EffectStatus    `json:"status"`
	NextOperation                  EffectOperation `json:"next_operation"`
	Attempt                        uint32          `json:"attempt"`
	ExternalRef                    string          `json:"external_ref,omitempty"`
	ResponseDigest                 ArtifactDigest  `json:"response_digest,omitempty"`
	ErrorCode                      string          `json:"error_code,omitempty"`
	RetryAt                        *time.Time      `json:"retry_at,omitempty"`
	DispatchStartedAt              *time.Time      `json:"dispatch_started_at,omitempty"`
	LastLookupAt                   *time.Time      `json:"last_lookup_at,omitempty"`
	LeaseOwner                     string          `json:"lease_owner,omitempty"`
	LeaseDeadline                  *time.Time      `json:"lease_deadline,omitempty"`
	FencingToken                   uint64          `json:"fencing_token"`
	UpdatedAt                      time.Time       `json:"updated_at"`
}
