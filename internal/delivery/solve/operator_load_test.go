package solve

import (
	"context"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func TestPlacementMoveMaterializesCertifiedRehandleOperation(t *testing.T) {
	problem := solverProblem(t)
	problem.Cargo[0].AllowedOrientations = append(
		problem.Cargo[0].AllowedOrientations,
		domain.OrientationWLH,
	)
	problem.Policy.MaxRehandlesPerStop = 1
	problem.Policy.RehandleSecondsPerCargo = 90
	problem.Policy.RehandleCostCentsPerCargo = 250
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	var err error
	problem, err = service.BuildProblemSnapshot(problem)
	if err != nil {
		t.Fatal(err)
	}

	result, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		solveConfig(problem),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	state, err := candidateStateFromPlan(result.Plan)
	if err != nil {
		t.Fatal(err)
	}
	stageIndex := survivingCargoStage(
		result.Plan.Duties[0].Trips[0],
		problem.Cargo[0].ID,
	)
	if stageIndex < 1 {
		t.Fatal("solved trip has no stage where cargo-1 survives")
	}
	solverEngine := &engine{
		problem: problem,
		index:   buildProblemIndex(problem),
	}
	plan, err := (placementMove{
		duty:        0,
		trip:        0,
		stage:       stageIndex,
		cargoID:     problem.Cargo[0].ID,
		orientation: domain.OrientationWLH,
	}).Apply(context.Background(), solverEngine, state)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := solverEngine.recomputePlanMetrics(plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.Metrics = metrics
	plan.Objective = solverEngine.objectiveForPlan(plan, metrics)
	if err := sealPlan(&plan); err != nil {
		t.Fatal(err)
	}

	stage := plan.Duties[0].Trips[0].LoadStages[stageIndex]
	if len(stage.Rehandles) != 1 {
		t.Fatalf("stage rehandles = %+v, want one explicit operation", stage.Rehandles)
	}
	operation := stage.Rehandles[0]
	if operation.CargoID != problem.Cargo[0].ID ||
		operation.StopIndex != uint32(stageIndex) ||
		operation.Before.Orientation != domain.OrientationLWH ||
		operation.After.Orientation != domain.OrientationWLH ||
		operation.DurationSeconds != 90 ||
		operation.CostCents != 250 {
		t.Fatalf("placement operation = %+v", operation)
	}
	if metrics.Rehandles != 1 ||
		metrics.TotalRehandleSeconds != 90 ||
		metrics.TotalRehandleCostCents != 250 {
		t.Fatalf("placement metrics = %+v", metrics)
	}
	report := validate.New(domain.ValidatorIdentity{
		Name: "independent", Version: "1.0.0", Build: "test",
	}).Validate(problem, plan, problem.CreatedAt)
	if !report.Valid {
		t.Fatalf("placement candidate violations = %+v", report.Violations)
	}
}

func survivingCargoStage(trip domain.Trip, cargoID domain.CargoID) int {
	for stageIndex := 1; stageIndex < len(trip.LoadStages); stageIndex++ {
		before := false
		for _, placement := range trip.LoadStages[stageIndex-1].Placements {
			before = before || placement.CargoID == cargoID
		}
		after := false
		for _, placement := range trip.LoadStages[stageIndex].Placements {
			after = after || placement.CargoID == cargoID
		}
		if before && after {
			return stageIndex
		}
	}
	return -1
}
