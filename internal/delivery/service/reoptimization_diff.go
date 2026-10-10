package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const (
	RevisionComparisonSchemaVersion  = "delivery.revision-comparison.v1"
	ComparableEffectSetSchemaVersion = "delivery.comparable-effect-set.v1"
)

type ComparisonDomain string

const (
	ComparisonRoute    ComparisonDomain = "route"
	ComparisonSchedule ComparisonDomain = "schedule"
	ComparisonEnergy   ComparisonDomain = "energy"
	ComparisonLoad     ComparisonDomain = "load"
	ComparisonEffect   ComparisonDomain = "effect"
	ComparisonMetric   ComparisonDomain = "metric"
)

type AttributionMode string

const (
	AttributionDirect  AttributionMode = "direct"
	AttributionDerived AttributionMode = "derived"
)

type PolicyRef struct {
	PolicyID    domain.PolicyID       `json:"policy_id"`
	Version     uint64                `json:"version"`
	FieldPath   string                `json:"field_path"`
	ValueDigest domain.ArtifactDigest `json:"value_digest"`
}

type ChangeMeta struct {
	ChangeID    string              `json:"change_id"`
	Facts       []domain.FactRef    `json:"facts"`
	Policy      []PolicyRef         `json:"policy"`
	OverrideIDs []domain.ApprovalID `json:"override_ids"`
	Mode        AttributionMode     `json:"mode"`
}

type RevisionChange struct {
	Domain     ComparisonDomain `json:"domain"`
	Object     domain.ObjectRef `json:"object"`
	FieldPath  string           `json:"field_path"`
	Before     json.RawMessage  `json:"before"`
	After      json.RawMessage  `json:"after"`
	Executable bool             `json:"executable"`
	Meta       ChangeMeta       `json:"meta"`
}

type RevisionComparison struct {
	SchemaVersion         string                `json:"schema_version"`
	BaseRevisionID        domain.PlanRevisionID `json:"base_revision_id"`
	CandidateRevisionID   domain.PlanRevisionID `json:"candidate_revision_id"`
	BasePlanDigest        domain.ArtifactDigest `json:"base_plan_digest"`
	CandidatePlanDigest   domain.ArtifactDigest `json:"candidate_plan_digest"`
	FactFrontierDigest    domain.ArtifactDigest `json:"fact_frontier_digest"`
	AppliedFactDigest     domain.ArtifactDigest `json:"applied_fact_digest"`
	ApplicationDigest     domain.ArtifactDigest `json:"application_digest"`
	PolicyDigest          domain.ArtifactDigest `json:"policy_digest"`
	OverrideGrantDigest   domain.ArtifactDigest `json:"override_grant_digest,omitempty"`
	OverrideUseDigest     domain.ArtifactDigest `json:"override_use_digest,omitempty"`
	BaseEffectSetDigest   domain.ArtifactDigest `json:"base_effect_set_digest"`
	CandidateEffectDigest domain.ArtifactDigest `json:"candidate_effect_set_digest"`
	Changes               []RevisionChange      `json:"changes"`
	Digest                domain.ArtifactDigest `json:"digest"`
}

type ComparableEffect struct {
	ID               domain.EffectID       `json:"effect_id"`
	Action           string                `json:"action"`
	Target           string                `json:"target"`
	Required         bool                  `json:"required"`
	ParametersDigest domain.ArtifactDigest `json:"parameters_digest"`
}

type ComparableEffectSet struct {
	SchemaVersion string                `json:"schema_version"`
	Effects       []ComparableEffect    `json:"effects"`
	Digest        domain.ArtifactDigest `json:"digest"`
}

type RevisionComparisonInput struct {
	BaseProblem      domain.ProblemSnapshot
	CandidateProblem domain.ProblemSnapshot
	BasePlan         domain.Plan
	CandidatePlan    domain.Plan
	Frontier         domain.FactFrontier
	AppliedFacts     []domain.FactRef
	Applications     []domain.FactApplication
	BaseEffects      ComparableEffectSet
	CandidateEffects ComparableEffectSet
	Override         *VerifiedFreezeOverride
}

type RevisionComparisonBuildOutput struct {
	Comparison  RevisionComparison
	OverrideUse *domain.FreezeOverrideUse
	Projection  Agent3ComparisonProjection
}

type metricDescriptor struct {
	name  string
	value func(domain.PlanMetrics) int64
}

var planMetricRegistry = []metricDescriptor{
	{name: "assigned_units", value: func(value domain.PlanMetrics) int64 {
		return int64(value.AssignedUnits)
	}},
	{name: "unassigned_units", value: func(value domain.PlanMetrics) int64 {
		return int64(value.UnassignedUnits)
	}},
	{name: "vehicles_used", value: func(value domain.PlanMetrics) int64 {
		return int64(value.VehiclesUsed)
	}},
	{name: "trips", value: func(value domain.PlanMetrics) int64 {
		return int64(value.Trips)
	}},
	{name: "stops", value: func(value domain.PlanMetrics) int64 {
		return int64(value.Stops)
	}},
	{name: "total_distance_meters", value: func(value domain.PlanMetrics) int64 {
		return value.TotalDistanceMeters
	}},
	{name: "total_drive_seconds", value: func(value domain.PlanMetrics) int64 {
		return value.TotalDriveSeconds
	}},
	{name: "total_service_seconds", value: func(value domain.PlanMetrics) int64 {
		return value.TotalServiceSeconds
	}},
	{name: "total_wait_seconds", value: func(value domain.PlanMetrics) int64 {
		return value.TotalWaitSeconds
	}},
	{name: "total_break_seconds", value: func(value domain.PlanMetrics) int64 {
		return value.TotalBreakSeconds
	}},
	{name: "total_charge_seconds", value: func(value domain.PlanMetrics) int64 {
		return value.TotalChargeSeconds
	}},
	{name: "total_rehandle_seconds", value: func(value domain.PlanMetrics) int64 {
		return value.TotalRehandleSeconds
	}},
	{name: "total_energy_wh", value: func(value domain.PlanMetrics) int64 {
		return value.TotalEnergyWh
	}},
	{name: "total_cost_cents", value: func(value domain.PlanMetrics) int64 {
		return value.TotalCostCents
	}},
	{name: "total_rehandle_cost_cents", value: func(value domain.PlanMetrics) int64 {
		return value.TotalRehandleCostCents
	}},
	{name: "stability_cost_cents", value: func(value domain.PlanMetrics) int64 {
		return value.StabilityCostCents
	}},
	{name: "on_time_tasks", value: func(value domain.PlanMetrics) int64 {
		return int64(value.OnTimeTasks)
	}},
	{name: "late_tasks", value: func(value domain.PlanMetrics) int64 {
		return int64(value.LateTasks)
	}},
	{name: "on_time_rate_ppm", value: func(value domain.PlanMetrics) int64 {
		return value.OnTimeRatePPM
	}},
	{name: "min_volume_utilization_ppm", value: func(value domain.PlanMetrics) int64 {
		return value.MinVolumeUtilizationPPM
	}},
	{name: "mean_volume_utilization_ppm", value: func(value domain.PlanMetrics) int64 {
		return value.MeanVolumeUtilizationPPM
	}},
	{name: "mean_payload_utilization_ppm", value: func(value domain.PlanMetrics) int64 {
		return value.MeanPayloadUtilizationPPM
	}},
	{name: "max_payload_utilization_ppm", value: func(value domain.PlanMetrics) int64 {
		return value.MaxPayloadUtilizationPPM
	}},
	{name: "rehandles", value: func(value domain.PlanMetrics) int64 {
		return int64(value.Rehandles)
	}},
}

func BuildComparableEffectSet(effects []ComparableEffect) (ComparableEffectSet, error) {
	result := ComparableEffectSet{
		SchemaVersion: ComparableEffectSetSchemaVersion,
		Effects:       append([]ComparableEffect(nil), effects...),
	}
	slices.SortFunc(result.Effects, func(left, right ComparableEffect) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for index, effect := range result.Effects {
		if effect.ID == "" || strings.TrimSpace(effect.Action) == "" ||
			strings.TrimSpace(effect.Target) == "" ||
			!domain.ValidArtifactDigest(effect.ParametersDigest) {
			return ComparableEffectSet{}, fmt.Errorf("comparable effect %d is invalid", index)
		}
		if index > 0 && result.Effects[index-1].ID == effect.ID {
			return ComparableEffectSet{}, fmt.Errorf("duplicate comparable effect %q", effect.ID)
		}
	}
	digest, err := computeComparableEffectSetDigest(result)
	if err != nil {
		return ComparableEffectSet{}, err
	}
	result.Digest = digest
	return result, nil
}

func BuildRevisionComparison(input RevisionComparisonInput) (RevisionComparisonBuildOutput, error) {
	if err := validateRevisionComparisonInput(input); err != nil {
		return RevisionComparisonBuildOutput{}, err
	}
	overrideUse, err := compileFreezeOverrideUse(input)
	if err != nil {
		return RevisionComparisonBuildOutput{}, err
	}
	baseLeaves, err := flattenComparisonLeaves(input.BaseProblem, input.BasePlan, input.BaseEffects)
	if err != nil {
		return RevisionComparisonBuildOutput{}, err
	}
	candidateLeaves, err := flattenComparisonLeaves(
		input.CandidateProblem,
		input.CandidatePlan,
		input.CandidateEffects,
	)
	if err != nil {
		return RevisionComparisonBuildOutput{}, err
	}
	changes, err := compareLeaves(baseLeaves, candidateLeaves, input)
	if err != nil {
		return RevisionComparisonBuildOutput{}, err
	}
	appliedFactDigest, err := domain.Digest(input.AppliedFacts)
	if err != nil {
		return RevisionComparisonBuildOutput{}, fmt.Errorf(
			"digest applied fact references: %w",
			err,
		)
	}
	applicationDigest, err := domain.ComputeFactApplicationsDigest(input.Applications)
	if err != nil {
		return RevisionComparisonBuildOutput{}, fmt.Errorf("digest fact applications: %w", err)
	}
	comparison := RevisionComparison{
		SchemaVersion:         RevisionComparisonSchemaVersion,
		BaseRevisionID:        input.BasePlan.RevisionID,
		CandidateRevisionID:   input.CandidatePlan.RevisionID,
		BasePlanDigest:        input.BasePlan.PlanDigest,
		CandidatePlanDigest:   input.CandidatePlan.PlanDigest,
		FactFrontierDigest:    input.Frontier.Digest,
		AppliedFactDigest:     appliedFactDigest,
		ApplicationDigest:     applicationDigest,
		PolicyDigest:          input.CandidateProblem.PolicyDigest,
		BaseEffectSetDigest:   input.BaseEffects.Digest,
		CandidateEffectDigest: input.CandidateEffects.Digest,
		Changes:               changes,
	}
	if input.Override != nil {
		comparison.OverrideGrantDigest = input.Override.GrantDigest()
	}
	if overrideUse != nil {
		comparison.OverrideUseDigest = overrideUse.Digest
	}
	comparison.Digest, err = computeRevisionComparisonDigest(comparison)
	if err != nil {
		return RevisionComparisonBuildOutput{}, err
	}
	projection := buildAgent3Projection(
		input.BaseProblem,
		input.CandidateProblem,
		input.BasePlan,
		input.CandidatePlan,
	)
	return RevisionComparisonBuildOutput{
		Comparison:  comparison,
		OverrideUse: overrideUse,
		Projection:  projection,
	}, nil
}

func validateRevisionComparisonInput(input RevisionComparisonInput) error {
	if err := validatePlanForComparison(input.BaseProblem, input.BasePlan); err != nil {
		return fmt.Errorf("base plan: %w", err)
	}
	if err := validatePlanForComparison(input.CandidateProblem, input.CandidatePlan); err != nil {
		return fmt.Errorf("candidate plan: %w", err)
	}
	if input.BasePlan.PlanID == "" || input.BasePlan.PlanID != input.CandidatePlan.PlanID ||
		input.BasePlan.RevisionID == "" ||
		input.CandidatePlan.RevisionID == "" ||
		input.BasePlan.RevisionID == input.CandidatePlan.RevisionID {
		return fmt.Errorf("comparison plans must be distinct revisions of one plan")
	}
	if err := domain.ValidateFactFrontier(input.Frontier); err != nil {
		return err
	}
	if input.CandidateProblem.Commitments.FactWatermark != string(input.Frontier.Digest) {
		return fmt.Errorf("candidate problem is not bound to the fact frontier")
	}
	if input.CandidateProblem.PolicyDigest != input.BaseProblem.PolicyDigest {
		return fmt.Errorf("cross-policy revision comparison is not supported")
	}
	if err := validateComparableEffectSet(input.BaseEffects); err != nil {
		return fmt.Errorf("base effect set: %w", err)
	}
	if err := validateComparableEffectSet(input.CandidateEffects); err != nil {
		return fmt.Errorf("candidate effect set: %w", err)
	}
	if len(input.AppliedFacts) == 0 {
		return fmt.Errorf("at least one applied fact is required")
	}
	if len(input.Applications) != len(input.AppliedFacts) {
		return fmt.Errorf("every applied fact must have one application record")
	}
	for index := range input.AppliedFacts {
		if input.Applications[index].Fact != input.AppliedFacts[index] {
			return fmt.Errorf("fact application %d does not match applied fact order", index)
		}
	}
	if input.Override != nil {
		grant := input.Override.grant
		constraint := input.CandidateProblem.Commitments.FreezeOverride
		if grant.TenantID != input.BaseProblem.TenantID || grant.PlanID != input.BasePlan.PlanID ||
			grant.BaseRevisionID != input.BasePlan.RevisionID ||
			grant.ProblemDigest != input.BaseProblem.ProblemDigest ||
			grant.PolicyDigest != input.BaseProblem.PolicyDigest ||
			grant.TargetFrontierDigest != input.Frontier.Digest ||
			constraint == nil ||
			constraint.ApprovalID != grant.ApprovalID ||
			constraint.GrantDigest != grant.Digest {
			return fmt.Errorf("freeze override does not bind the revision comparison")
		}
	} else if input.CandidateProblem.Commitments.FreezeOverride != nil {
		return fmt.Errorf("candidate problem contains an unverified freeze override")
	}
	return nil
}

func validatePlanForComparison(problem domain.ProblemSnapshot, plan domain.Plan) error {
	digest, err := domain.ComputePlanDigest(plan)
	if err != nil {
		return err
	}
	if plan.SchemaVersion != domain.PlanSchemaVersion ||
		plan.ProblemDigest != problem.ProblemDigest ||
		plan.PolicyDigest != problem.PolicyDigest ||
		plan.CommitmentDigest != problem.CommitmentDigest ||
		!domain.ValidArtifactDigest(plan.PlanDigest) ||
		digest != plan.PlanDigest {
		return fmt.Errorf("plan digest or problem binding is invalid")
	}
	return nil
}

func validateComparableEffectSet(value ComparableEffectSet) error {
	if value.SchemaVersion != ComparableEffectSetSchemaVersion ||
		!domain.ValidArtifactDigest(value.Digest) {
		return fmt.Errorf("effect set schema or digest is invalid")
	}
	rebuilt, err := BuildComparableEffectSet(value.Effects)
	if err != nil {
		return err
	}
	if rebuilt.Digest != value.Digest || !slices.Equal(rebuilt.Effects, value.Effects) {
		return fmt.Errorf("effect set is not canonical")
	}
	return nil
}

func computeComparableEffectSetDigest(value ComparableEffectSet) (domain.ArtifactDigest, error) {
	value.Digest = ""
	return domain.Digest(value)
}

func computeRevisionComparisonDigest(value RevisionComparison) (domain.ArtifactDigest, error) {
	value.Digest = ""
	return domain.Digest(value)
}

func comparisonAbsoluteSeconds(value time.Duration) int64 {
	seconds := int64(value / time.Second)
	if seconds < 0 {
		return -seconds
	}
	return seconds
}
