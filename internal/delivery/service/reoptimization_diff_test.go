package service

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestBuildRevisionComparisonCoversEveryPlanDomainAndAttributesChanges(
	t *testing.T,
) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	baseProblem, basePlan, baseFrontier := solvedActivePlan(t, baseTime)
	createdAt := baseTime.Add(10 * time.Minute)
	eta := domain.ETADeviationFact{
		Meta:               factHeader("eta", 1, "fact-eta", baseTime, createdAt),
		TaskID:             "delivery-1",
		ProjectedServiceAt: baseTime.Add(75 * time.Minute),
	}
	successor, err := BuildSuccessor(successorInput(
		t,
		baseProblem,
		basePlan,
		baseFrontier,
		[]domain.OperationalFact{eta},
		createdAt,
	))
	if err != nil {
		t.Fatal(err)
	}
	candidate := solveProblem(t, successor.Problem, "revision-successor").Plan
	trip := &candidate.Duties[0].Trips[0]
	trip.EndDepotID = "depot-alternate"
	trip.Stops[1], trip.Stops[2] = trip.Stops[2], trip.Stops[1]
	for index := range trip.Stops {
		if slices.Contains(trip.Stops[index].TaskIDs, domain.TaskID("delivery-1")) {
			trip.Stops[index].ServiceAt =
				trip.Stops[index].ServiceAt.Add(time.Minute)
		}
	}
	candidate.Duties[0].DriverIDs = []domain.DriverID{"driver-2"}
	trip.Energy = append(trip.Energy, domain.EnergyLeg{
		FromStopIndex: 0,
		ToStopIndex:   1,
		StartSOCWh:    1_000,
		ConsumedWh:    100,
		EndSOCWh:      900,
	})
	if len(trip.LoadStages) == 0 || len(trip.LoadStages[0].Placements) == 0 {
		t.Fatal("candidate fixture has no cargo placement")
	}
	trip.LoadStages[0].Placements[0].PositionMM.X++
	bumpAllPlanMetrics(&candidate.Metrics)
	sealComparisonPlan(t, &candidate)

	baseEffects, err := BuildComparableEffectSet([]ComparableEffect{{
		ID:               "effect-route",
		Action:           "tms.route.publish",
		Target:           "plan-active",
		Required:         true,
		ParametersDigest: digestForTest(t, "base-route"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	candidateEffects, err := BuildComparableEffectSet([]ComparableEffect{
		{
			ID:               "effect-route",
			Action:           "tms.route.publish",
			Target:           "plan-active",
			Required:         true,
			ParametersDigest: digestForTest(t, "candidate-route"),
		},
		{
			ID:               "effect-notify",
			Action:           "notify.eta.publish",
			Target:           "request-1",
			Required:         false,
			ParametersDigest: digestForTest(t, "candidate-notify"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	outcome, err := BuildRevisionComparison(RevisionComparisonInput{
		BaseProblem:      baseProblem,
		CandidateProblem: successor.Problem,
		BasePlan:         basePlan,
		CandidatePlan:    candidate,
		Frontier:         successor.Frontier,
		AppliedFacts:     successor.AppliedFacts,
		Applications:     successor.Applications,
		BaseEffects:      baseEffects,
		CandidateEffects: candidateEffects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !domain.ValidArtifactDigest(outcome.Comparison.Digest) ||
		outcome.Comparison.ApplicationDigest != successor.ApplicationDigest {
		t.Fatalf("comparison binding = %+v", outcome.Comparison)
	}
	domains := make(map[ComparisonDomain]struct{})
	metricChanges := 0
	for _, change := range outcome.Comparison.Changes {
		domains[change.Domain] = struct{}{}
		if change.Domain == ComparisonMetric {
			metricChanges++
		}
		if change.Executable &&
			len(change.Meta.Facts) == 0 &&
			len(change.Meta.OverrideIDs) == 0 {
			t.Fatalf("unattributed executable change = %+v", change)
		}
		if change.Meta.ChangeID == "" || change.Meta.Mode == "" {
			t.Fatalf("incomplete change metadata = %+v", change.Meta)
		}
	}
	for _, comparisonDomain := range []ComparisonDomain{
		ComparisonRoute,
		ComparisonSchedule,
		ComparisonEnergy,
		ComparisonLoad,
		ComparisonEffect,
		ComparisonMetric,
	} {
		if _, exists := domains[comparisonDomain]; !exists {
			t.Fatalf("comparison domain %q is missing", comparisonDomain)
		}
	}
	if metricChanges != len(planMetricRegistry) {
		t.Fatalf("metric changes = %d, want %d", metricChanges, len(planMetricRegistry))
	}
	assertAgent3ProjectionContract(t, outcome.Projection)
}

func TestPlanMetricRegistryIsClosedOverPlanMetrics(t *testing.T) {
	if len(planMetricRegistry) != planMetricRegistryFieldCount() {
		t.Fatalf(
			"metric registry contains %d fields, PlanMetrics contains %d",
			len(planMetricRegistry),
			planMetricRegistryFieldCount(),
		)
	}
	names := make(map[string]struct{}, len(planMetricRegistry))
	for _, metric := range planMetricRegistry {
		if _, duplicate := names[metric.name]; duplicate {
			t.Fatalf("duplicate metric registry entry %q", metric.name)
		}
		names[metric.name] = struct{}{}
	}
}

func TestAgent3ProjectionEmitsVehicleAssignmentWithoutExtraKinds(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, base, _ := solvedActivePlan(t, baseTime)
	candidate := base
	candidate.RevisionID = "revision-candidate"
	candidate.Duties = append([]domain.VehicleDuty(nil), base.Duties...)
	candidate.Duties[0] = base.Duties[0]
	candidate.Duties[0].VehicleID = "vehicle-2"

	projection := buildAgent3Projection(problem, problem, base, candidate)
	available, ok := projection.(Agent3ComparisonAvailable)
	if !ok {
		t.Fatalf("projection type = %T", projection)
	}
	found := false
	for _, change := range available.Changes {
		if assignment, assignmentOK := change.(Agent3VehicleAssignmentChange); assignmentOK && assignment.UnitID == "unit-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("vehicle assignment change missing: %+v", available.Changes)
	}
	assertAgent3ProjectionContract(t, projection)
}

func TestRevisionComparisonFreezesExactOverrideUse(t *testing.T) {
	baseTime := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	initialProblem, initialPlan, frontier := solvedActivePlan(t, baseTime)
	visit := taskVisits(initialPlan)["delivery-1"]
	before := domain.FrozenTaskCommitment{
		TaskID:            "delivery-1",
		VehicleID:         visit.vehicleID,
		DriverID:          visit.driverIDs[0],
		Sequence:          visit.index,
		PromisedServiceAt: visit.serviceAt,
		ToleranceSeconds:  300,
	}
	initialProblem.Commitments.BasePlanDigest = initialPlan.PlanDigest
	initialProblem.Commitments.Frozen = []domain.FrozenTaskCommitment{before}
	initialProblem.ProblemDigest = ""
	initialProblem.PolicyDigest = ""
	initialProblem.CommitmentDigest = ""
	baseProblem, err := BuildProblemSnapshot(initialProblem)
	if err != nil {
		t.Fatal(err)
	}
	basePlan := solveProblem(t, baseProblem, "revision-frozen").Plan
	createdAt := baseTime.Add(10 * time.Minute)
	eta := domain.ETADeviationFact{
		Meta:               factHeader("eta", 1, "fact-eta", baseTime, createdAt),
		TaskID:             "delivery-1",
		ProjectedServiceAt: before.PromisedServiceAt.Add(10 * time.Minute),
	}
	buildInput := successorInput(
		t,
		baseProblem,
		basePlan,
		frontier,
		[]domain.OperationalFact{eta},
		createdAt,
	)
	grant := domain.FreezeOverrideGrant{
		SchemaVersion:        domain.FreezeOverrideSchemaVersion,
		ApprovalID:           "approval-override-1",
		TenantID:             baseProblem.TenantID,
		PlanID:               basePlan.PlanID,
		BaseRevisionID:       basePlan.RevisionID,
		BaseActiveVersion:    1,
		ProblemDigest:        baseProblem.ProblemDigest,
		PolicyDigest:         baseProblem.PolicyDigest,
		TargetFrontierDigest: buildInput.TargetFrontier.Digest,
		Scopes: []domain.FreezeOverrideScope{{
			TaskID:             "delivery-1",
			Before:             before,
			MaxETADriftSeconds: 900,
		}},
		RequestedBy: "dispatcher-a",
		ApprovedBy:  "supervisor-b",
		Reason:      "recover downstream appointment",
		ApprovedAt:  createdAt.Add(-time.Minute),
		ExpiresAt:   createdAt.Add(time.Hour),
	}
	grant.Digest, err = domain.ComputeFreezeOverrideGrantDigest(grant)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifyFreezeOverride(FreezeOverrideVerificationInput{
		Grant:                  grant,
		Status:                 FreezeOverrideApprovalConfirmed,
		ExpectedTenantID:       grant.TenantID,
		ExpectedPlanID:         grant.PlanID,
		ExpectedRevisionID:     grant.BaseRevisionID,
		ExpectedActiveVersion:  1,
		ExpectedProblemDigest:  grant.ProblemDigest,
		ExpectedPolicyDigest:   grant.PolicyDigest,
		ExpectedFrontierDigest: grant.TargetFrontierDigest,
		BaseFrozen:             baseProblem.Commitments.Frozen,
		BaseExecuted:           baseProblem.Commitments.Executed,
		PlanCreatedBy:          "planner-c",
		ApproverPermissions:    []string{FreezeOverrideApprovePermission},
		At:                     createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	buildInput.Override = verified
	successor, err := BuildSuccessor(buildInput)
	if err != nil {
		t.Fatal(err)
	}
	candidate := cloneComparisonPlan(t, basePlan)
	candidate.RevisionID = "revision-override-candidate"
	candidate.ProblemDigest = successor.Problem.ProblemDigest
	candidate.PolicyDigest = successor.Problem.PolicyDigest
	candidate.CommitmentDigest = successor.Problem.CommitmentDigest
	for dutyIndex := range candidate.Duties {
		for tripIndex := range candidate.Duties[dutyIndex].Trips {
			trip := &candidate.Duties[dutyIndex].Trips[tripIndex]
			for stopIndex := range trip.Stops {
				if slices.Contains(
					trip.Stops[stopIndex].TaskIDs,
					domain.TaskID("delivery-1"),
				) {
					trip.Stops[stopIndex].ServiceAt =
						before.PromisedServiceAt.Add(10 * time.Minute)
				}
			}
		}
	}
	sealComparisonPlan(t, &candidate)
	emptyEffects, err := BuildComparableEffectSet(nil)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := BuildRevisionComparison(RevisionComparisonInput{
		BaseProblem:      baseProblem,
		CandidateProblem: successor.Problem,
		BasePlan:         basePlan,
		CandidatePlan:    candidate,
		Frontier:         successor.Frontier,
		AppliedFacts:     successor.AppliedFacts,
		Applications:     successor.Applications,
		BaseEffects:      emptyEffects,
		CandidateEffects: emptyEffects,
		Override:         verified,
	})
	if err != nil {
		t.Fatal(err)
	}
	if comparison.OverrideUse == nil ||
		len(comparison.OverrideUse.TaskChanges) != 1 ||
		comparison.OverrideUse.TaskChanges[0].Before != before ||
		comparison.OverrideUse.TaskChanges[0].After.PromisedServiceAt !=
			before.PromisedServiceAt.Add(10*time.Minute) ||
		!domain.ValidArtifactDigest(comparison.OverrideUse.Digest) ||
		comparison.Comparison.OverrideUseDigest != comparison.OverrideUse.Digest ||
		comparison.Comparison.OverrideGrantDigest != grant.Digest {
		t.Fatalf("override use = %+v, comparison = %+v",
			comparison.OverrideUse, comparison.Comparison)
	}
}

func assertAgent3ProjectionContract(
	t testing.TB,
	projection Agent3ComparisonProjection,
) {
	t.Helper()
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Kind    string `json:"kind"`
		Changes []struct {
			Kind string `json:"kind"`
		} `json:"changes"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != "available" && decoded.Kind != "unavailable" {
		t.Fatalf("projection kind = %q", decoded.Kind)
	}
	allowed := map[string]struct{}{
		"vehicle_assignment": {},
		"driver_assignment":  {},
		"stop_sequence":      {},
		"eta":                {},
		"cargo_placement":    {},
		"metric":             {},
	}
	for _, change := range decoded.Changes {
		if _, exists := allowed[change.Kind]; !exists {
			t.Fatalf("unsupported Agent-3 change kind %q in %s", change.Kind, raw)
		}
	}
}

func sealComparisonPlan(t testing.TB, plan *domain.Plan) {
	t.Helper()
	plan.PlanDigest = ""
	digest, err := domain.ComputePlanDigest(*plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.PlanDigest = digest
}

func cloneComparisonPlan(t testing.TB, value domain.Plan) domain.Plan {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result domain.Plan
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func bumpAllPlanMetrics(value *domain.PlanMetrics) {
	value.AssignedUnits++
	value.UnassignedUnits++
	value.VehiclesUsed++
	value.Trips++
	value.Stops++
	value.TotalDistanceMeters++
	value.TotalDriveSeconds++
	value.TotalServiceSeconds++
	value.TotalWaitSeconds++
	value.TotalBreakSeconds++
	value.TotalChargeSeconds++
	value.TotalRehandleSeconds++
	value.TotalEnergyWh++
	value.TotalCostCents++
	value.TotalRehandleCostCents++
	value.StabilityCostCents++
	value.OnTimeTasks++
	value.LateTasks++
	value.OnTimeRatePPM++
	value.MinVolumeUtilizationPPM++
	value.MeanVolumeUtilizationPPM++
	value.MeanPayloadUtilizationPPM++
	value.MaxPayloadUtilizationPPM++
	value.Rehandles++
}
