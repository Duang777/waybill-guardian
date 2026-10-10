package agent

import (
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type GetProblemSummaryInput struct {
	ProblemID domain.ProblemID `json:"problem_id" jsonschema_description:"Existing delivery problem identifier"`
	Version   uint64           `json:"version" jsonschema_description:"Existing problem version"`
}

type ProblemSummary struct {
	ProblemID        domain.ProblemID      `json:"problem_id"`
	Version          uint64                `json:"version"`
	ProblemDigest    domain.ArtifactDigest `json:"problem_digest"`
	PolicyDigest     domain.ArtifactDigest `json:"policy_digest"`
	CommitmentDigest domain.ArtifactDigest `json:"commitment_digest"`
	Horizon          domain.TimeRange      `json:"horizon"`
	Locations        int                   `json:"locations"`
	Depots           int                   `json:"depots"`
	Requests         int                   `json:"requests"`
	Units            int                   `json:"units"`
	Cargo            int                   `json:"cargo_items"`
	Vehicles         int                   `json:"vehicles"`
	Drivers          int                   `json:"drivers"`
	Chargers         int                   `json:"chargers"`
}

type RequestOptimizationInput struct {
	ProblemID      domain.ProblemID `json:"problem_id" jsonschema_description:"Existing delivery problem identifier"`
	ProblemVersion uint64           `json:"problem_version" jsonschema_description:"Existing problem version"`
	SolverProfile  string           `json:"solver_profile" jsonschema_description:"Server-registered solver profile"`
}

type OptimizationRequested struct {
	RunID          domain.OptimizationRunID `json:"run_id"`
	ProblemID      domain.ProblemID         `json:"problem_id"`
	ProblemVersion uint64                   `json:"problem_version"`
	Status         domain.RunStatus         `json:"status"`
	Version        uint64                   `json:"version"`
	Replayed       bool                     `json:"replayed"`
}

type GetCandidateInput struct {
	RunID domain.OptimizationRunID `json:"run_id" jsonschema_description:"Existing optimization run identifier"`
}

type CandidateSummary struct {
	RunID                  domain.OptimizationRunID `json:"run_id"`
	RevisionID             domain.PlanRevisionID    `json:"revision_id"`
	PlanID                 domain.PlanID            `json:"plan_id"`
	RevisionStatus         domain.RevisionStatus    `json:"revision_status"`
	PlanDigest             domain.ArtifactDigest    `json:"plan_digest"`
	ValidationReportDigest domain.ArtifactDigest    `json:"validation_report_digest"`
	Valid                  bool                     `json:"valid"`
	ViolationCount         int                      `json:"violation_count"`
	Objective              domain.ObjectiveVector   `json:"objective"`
	Metrics                domain.PlanMetrics       `json:"metrics"`
}

type CompareRevisionsInput struct {
	BaseRevisionID      domain.PlanRevisionID `json:"base_revision_id" jsonschema_description:"Existing base revision identifier"`
	CandidateRevisionID domain.PlanRevisionID `json:"candidate_revision_id" jsonschema_description:"Existing candidate revision identifier"`
}

type PlanMetricDelta struct {
	VehiclesUsed              int64 `json:"vehicles_used"`
	Trips                     int64 `json:"trips"`
	Stops                     int64 `json:"stops"`
	TotalDistanceMeters       int64 `json:"total_distance_meters"`
	TotalDriveSeconds         int64 `json:"total_drive_seconds"`
	TotalCostCents            int64 `json:"total_cost_cents"`
	OnTimeTasks               int64 `json:"on_time_tasks"`
	LateTasks                 int64 `json:"late_tasks"`
	OnTimeRatePPM             int64 `json:"on_time_rate_ppm"`
	MeanVolumeUtilizationPPM  int64 `json:"mean_volume_utilization_ppm"`
	MeanPayloadUtilizationPPM int64 `json:"mean_payload_utilization_ppm"`
	Rehandles                 int64 `json:"rehandles"`
}

type RevisionComparison struct {
	BaseRevisionID      domain.PlanRevisionID `json:"base_revision_id"`
	CandidateRevisionID domain.PlanRevisionID `json:"candidate_revision_id"`
	BasePlanDigest      domain.ArtifactDigest `json:"base_plan_digest"`
	CandidatePlanDigest domain.ArtifactDigest `json:"candidate_plan_digest"`
	BaseMetrics         domain.PlanMetrics    `json:"base_metrics"`
	CandidateMetrics    domain.PlanMetrics    `json:"candidate_metrics"`
	Delta               PlanMetricDelta       `json:"candidate_minus_base"`
}

type ExplainValidationInput struct {
	RevisionID domain.PlanRevisionID `json:"revision_id" jsonschema_description:"Existing plan revision identifier"`
}

type ValidationExplanation struct {
	RevisionID     domain.PlanRevisionID    `json:"revision_id"`
	Valid          bool                     `json:"valid"`
	ViolationCount int                      `json:"violation_count"`
	Violations     []domain.Violation       `json:"violations"`
	Metrics        domain.PlanMetrics       `json:"metrics"`
	Validator      domain.ValidatorIdentity `json:"validator"`
	ReportDigest   domain.ArtifactDigest    `json:"report_digest"`
	Truncated      bool                     `json:"truncated"`
}

type RequestPlanApprovalInput struct {
	RevisionID domain.PlanRevisionID `json:"revision_id" jsonschema_description:"Existing validated revision identifier"`
	Reason     string                `json:"reason" jsonschema_description:"Short business explanation for the human reviewer"`
}

type ApprovalRequested struct {
	ApprovalID      domain.ApprovalID     `json:"approval_id"`
	RevisionID      domain.PlanRevisionID `json:"revision_id"`
	Status          domain.ApprovalStatus `json:"status"`
	Version         uint64                `json:"version"`
	ExpiresAt       time.Time             `json:"expires_at"`
	EffectSetDigest domain.ArtifactDigest `json:"effect_set_digest"`
	Replayed        bool                  `json:"replayed"`
}
