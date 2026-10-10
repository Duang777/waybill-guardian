package solve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func TestFullEvaluatorRecomputesAndCertifiesCompletePlan(t *testing.T) {
	problem := solverProblem(t)
	solver := newTestBuiltin(t)
	solved, err := solver.Solve(
		context.Background(),
		problem,
		solveConfig(problem),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t, problem)
	plan := solved.Plan
	plan.Metrics = domain.PlanMetrics{}
	plan.Objective = domain.ObjectiveVector{}
	plan.PlanDigest = ""

	evaluation := engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		t.Fatalf("evaluation = %#v, want feasible candidate", evaluation)
	}
	if feasible.state.plan.Metrics.AssignedUnits != 2 ||
		feasible.state.plan.Metrics.VehiclesUsed != 1 ||
		feasible.state.plan.Metrics.TotalDistanceMeters != 30_000 ||
		feasible.state.plan.Metrics.TotalWaitSeconds != 360 ||
		feasible.state.plan.Metrics.TotalCostCents != 12_077 ||
		feasible.state.plan.Objective != (domain.ObjectiveVector{
			VehiclesUsed:            1,
			TotalCostCents:          12_077,
			TotalDistanceMeters:     30_000,
			TotalWaitSeconds:        360,
			NegativeMinVolumePPM:    -60_000,
			StabilityCostCents:      0,
			UnassignedRequiredUnits: 0,
			HardViolationCount:      0,
		}) {
		t.Fatalf("recomputed plan = %+v / %+v",
			feasible.state.plan.Metrics, feasible.state.plan.Objective)
	}
	certified, report, err := engine.certifyCandidate(feasible.state)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Valid || !certified.report.Valid {
		t.Fatalf("certification report = %+v", report)
	}
	if certified.plan.PlanDigest == "" || certified.stateKey == "" {
		t.Fatalf("certified state = %+v", certified)
	}
}

func TestCertificationRejectsJointlyInvalidMaterializedCandidate(t *testing.T) {
	problem := solverProblem(t)
	solved, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		solveConfig(problem),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	engine := newTestEngine(t, problem)
	plan := solved.Plan
	for segmentIndex := range plan.Duties[0].Trips[0].Schedule {
		segment := &plan.Duties[0].Trips[0].Schedule[segmentIndex]
		if segment.Kind == domain.SegmentDrive {
			segment.EndAt = segment.EndAt.Add(-time.Second)
			break
		}
	}
	evaluation := engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		t.Fatalf("evaluation = %#v, want materialized candidate", evaluation)
	}
	_, report, err := engine.certifyCandidate(feasible.state)
	if !errors.Is(err, ErrCandidateRejected) {
		t.Fatalf("certifyCandidate error = %v, want ErrCandidateRejected", err)
	}
	if report.Valid || !reportHasViolation(report, "V503") {
		t.Fatalf("certification report = %+v, want V503", report)
	}
}

func newTestEngine(t testing.TB, problem domain.ProblemSnapshot) *engine {
	t.Helper()
	config, digest, err := BuildSolveConfig(solveConfig(problem))
	if err != nil {
		t.Fatal(err)
	}
	result, err := newEngine(
		problem,
		config,
		digest,
		domain.SolverIdentity{Name: "builtin", Version: "1.0.0", Build: "test"},
		validate.New(domain.ValidatorIdentity{
			Name: "independent", Version: "1.0.0", Build: "test",
		}),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func reportHasViolation(report domain.ValidationReport, code string) bool {
	for _, violation := range report.Violations {
		if violation.Code == code {
			return true
		}
	}
	return false
}
