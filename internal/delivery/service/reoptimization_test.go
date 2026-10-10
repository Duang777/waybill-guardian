package service

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/solve"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func TestBuildSuccessorSnapshotDerivesExecutionFreezeAndInTransitCommitments(
	t *testing.T,
) {
	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active := solvedActivePlan(t, base)
	createdAt := base.Add(10 * time.Minute)
	completed := domain.TaskCompletedFact{
		Meta:        factHeader("fact-completed", base.Add(6*time.Minute), "watermark-1"),
		TaskID:      "pickup-1",
		VehicleID:   "vehicle-1",
		DriverID:    "driver-1",
		CompletedAt: base.Add(5 * time.Minute),
	}
	eta := domain.ETADeviationFact{
		Meta:               factHeader("fact-eta", base.Add(9*time.Minute), "watermark-2"),
		TaskID:             "delivery-1",
		ProjectedServiceAt: base.Add(75 * time.Minute),
	}

	successor, diff, err := BuildSuccessorSnapshot(
		problem,
		active,
		[]domain.OperationalFact{eta, completed},
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if successor.Version != 2 ||
		successor.Commitments.BasePlanDigest != active.PlanDigest ||
		successor.Commitments.FactWatermark != "watermark-2" {
		t.Fatalf("successor identity and watermark = %+v", successor.Commitments)
	}
	if len(successor.Commitments.Executed) != 1 ||
		successor.Commitments.Executed[0].TaskID != "pickup-1" {
		t.Fatalf("executed commitments = %+v", successor.Commitments.Executed)
	}
	if len(successor.Commitments.Frozen) != 1 ||
		successor.Commitments.Frozen[0].TaskID != "delivery-1" ||
		successor.Commitments.Frozen[0].VehicleID != "vehicle-1" ||
		successor.Commitments.Frozen[0].DriverID != "driver-1" {
		t.Fatalf("frozen commitments = %+v", successor.Commitments.Frozen)
	}
	if len(successor.Commitments.InTransit) != 1 ||
		successor.Commitments.InTransit[0].CargoID != "cargo-1" ||
		successor.Commitments.InTransit[0].CompartmentID != "compartment-1" {
		t.Fatalf("in-transit commitments = %+v", successor.Commitments.InTransit)
	}
	if !slices.Equal(diff.FactIDs, []string{"fact-completed", "fact-eta"}) ||
		!slices.Equal(diff.CompletedTasks, []domain.TaskID{"pickup-1"}) ||
		diff.NewProblemDigest != successor.ProblemDigest {
		t.Fatalf("reoptimization diff = %+v", diff)
	}

	replayed, _, err := BuildSuccessorSnapshot(
		problem,
		active,
		[]domain.OperationalFact{completed, eta},
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if successor.ProblemDigest != replayed.ProblemDigest {
		t.Fatalf("fact order changed successor digest: %q != %q",
			successor.ProblemDigest, replayed.ProblemDigest)
	}
}

func TestBuildSuccessorSnapshotAppliesResourceAndCancellationFacts(t *testing.T) {
	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active := solvedActivePlan(t, base)
	createdAt := base.Add(20 * time.Minute)
	successor, diff, err := BuildSuccessorSnapshot(
		problem,
		active,
		[]domain.OperationalFact{
			domain.VehicleUnavailableFact{
				Meta:            factHeader("fact-vehicle", base.Add(15*time.Minute), "watermark-1"),
				VehicleID:       "vehicle-1",
				UnavailableFrom: base.Add(30 * time.Minute),
			},
			domain.RequestCanceledFact{
				Meta:      factHeader("fact-cancel", base.Add(16*time.Minute), "watermark-2"),
				RequestID: "request-1",
			},
		},
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(successor.Requests) != 0 ||
		len(successor.Units) != 0 ||
		len(successor.Cargo) != 0 {
		t.Fatalf("canceled request remains in successor: %+v", successor)
	}
	if len(successor.Vehicles[0].Availability) != 1 ||
		!successor.Vehicles[0].Availability[0].End.Equal(base.Add(30*time.Minute)) {
		t.Fatalf("vehicle availability = %+v", successor.Vehicles[0].Availability)
	}
	if !slices.Equal(diff.CanceledRequests, []domain.RequestID{"request-1"}) ||
		len(diff.ChangedResources) != 1 ||
		diff.ChangedResources[0] != (domain.ObjectRef{Kind: "vehicle", ID: "vehicle-1"}) {
		t.Fatalf("reoptimization diff = %+v", diff)
	}
}

func TestSuccessorSolvePreservesCommitmentsOrReturnsManualReview(t *testing.T) {
	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, active := solvedActivePlan(t, base)
	createdAt := base.Add(10 * time.Minute)
	completed := domain.TaskCompletedFact{
		Meta:        factHeader("fact-completed", base.Add(6*time.Minute), "watermark-1"),
		TaskID:      "pickup-1",
		VehicleID:   "vehicle-1",
		DriverID:    "driver-1",
		CompletedAt: base.Add(5 * time.Minute),
	}
	successor, _, err := BuildSuccessorSnapshot(
		problem,
		active,
		[]domain.OperationalFact{completed},
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := solveProblem(t, successor, "revision-successor")
	if result.Status != solve.SolveCompleted || !result.Validation.Valid {
		t.Fatalf("successor solve status = %q, violations = %+v",
			result.Status, result.Validation.Violations)
	}

	conflicted, _, err := BuildSuccessorSnapshot(
		problem,
		active,
		[]domain.OperationalFact{
			completed,
			domain.VehicleUnavailableFact{
				Meta:            factHeader("fact-vehicle", base.Add(7*time.Minute), "watermark-2"),
				VehicleID:       "vehicle-1",
				UnavailableFrom: base.Add(20 * time.Minute),
			},
		},
		createdAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	result = solveProblem(t, conflicted, "revision-conflict")
	if result.Status != solve.SolveManualReview ||
		!slices.Contains(result.Evidence.ConflictSet, domain.ObjectRef{
			Kind: "task", ID: "delivery-1",
		}) {
		t.Fatalf("conflicted solve status = %q, conflicts = %+v, violations = %+v",
			result.Status, result.Evidence.ConflictSet, result.Validation.Violations)
	}
}

func solvedActivePlan(
	t testing.TB,
	base time.Time,
) (domain.ProblemSnapshot, domain.Plan) {
	t.Helper()
	problem, err := BuildProblemSnapshot(validProblemDraft())
	if err != nil {
		t.Fatal(err)
	}
	solver, err := solve.NewBuiltin(
		domain.SolverIdentity{Name: "builtin", Version: "1.0.0", Build: "test"},
		validate.New(domain.ValidatorIdentity{
			Name: "independent", Version: "1.0.0", Build: "test",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := solver.Solve(context.Background(), problem, solve.SolveConfig{
		SchemaVersion:    solve.SolveConfigSchemaVersion,
		Strategy:         "deterministic-regret",
		EvaluationBudget: 100,
		Seed:             1,
		PlanID:           "plan-active",
		RevisionID:       "revision-active",
		ValidationAt:     base.Add(-time.Minute),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != solve.SolveCompleted {
		t.Fatalf("active solve status = %q, violations = %+v",
			result.Status, result.Validation.Violations)
	}
	return problem, result.Plan
}

func solveProblem(
	t testing.TB,
	problem domain.ProblemSnapshot,
	revisionID domain.PlanRevisionID,
) solve.SolveResult {
	t.Helper()
	solver, err := solve.NewBuiltin(
		domain.SolverIdentity{Name: "builtin", Version: "1.0.0", Build: "test"},
		validate.New(domain.ValidatorIdentity{
			Name: "independent", Version: "1.0.0", Build: "test",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := solver.Solve(context.Background(), problem, solve.SolveConfig{
		SchemaVersion:    solve.SolveConfigSchemaVersion,
		Strategy:         "deterministic-regret",
		EvaluationBudget: 100,
		Seed:             1,
		PlanID:           "plan-active",
		RevisionID:       revisionID,
		ValidationAt:     problem.CreatedAt,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func factHeader(
	id string,
	occurredAt time.Time,
	watermark string,
) domain.OperationalFactHeader {
	return domain.OperationalFactHeader{
		SchemaVersion: domain.OperationalFactSchemaVersion,
		FactID:        id,
		OccurredAt:    occurredAt,
		Watermark:     watermark,
		SourceSystem:  "operations",
	}
}
