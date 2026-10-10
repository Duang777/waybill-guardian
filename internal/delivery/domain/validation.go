package domain

import "time"

type ValidatorIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Build   string `json:"build"`
}

type ObjectRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

type Violation struct {
	Code         string      `json:"code"`
	Severity     Severity    `json:"severity"`
	Object       ObjectRef   `json:"object"`
	Related      []ObjectRef `json:"related"`
	Expected     string      `json:"expected"`
	Actual       string      `json:"actual"`
	DutyIndex    int32       `json:"duty_index"`
	TripIndex    int32       `json:"trip_index"`
	StopIndex    int32       `json:"stop_index"`
	SegmentIndex int32       `json:"segment_index"`
}

type ValidationReport struct {
	SchemaVersion    string            `json:"schema_version"`
	ProblemDigest    ArtifactDigest    `json:"problem_digest"`
	PolicyDigest     ArtifactDigest    `json:"policy_digest"`
	CommitmentDigest ArtifactDigest    `json:"commitment_digest"`
	PlanDigest       ArtifactDigest    `json:"plan_digest"`
	Validator        ValidatorIdentity `json:"validator"`
	Valid            bool              `json:"valid"`
	Violations       []Violation       `json:"violations"`
	Metrics          PlanMetrics       `json:"metrics"`
	ReportDigest     ArtifactDigest    `json:"report_digest"`
	CreatedAt        time.Time         `json:"created_at"`
}
