package solve

import (
	"context"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestSolverObjectiveChargesReloadStabilityPenalty(t *testing.T) {
	problem := solverProblem(t)
	result, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		solveConfig(problem),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	placement := result.Plan.Duties[0].Trips[0].LoadStages[1].Placements[0]
	secondDoor := problem.Vehicles[0].Doors[0]
	secondDoor.ID = "door-2"
	problem.Vehicles[0].Doors = append(problem.Vehicles[0].Doors, secondDoor)
	problem.Policy.Stability.ReloadCents = 250
	problem.Commitments.SoftCargo = []domain.SoftCargoCommitment{{
		CargoID:        placement.CargoID,
		VehicleID:      result.Plan.Duties[0].VehicleID,
		AfterStopIndex: 1,
		CompartmentID:  placement.CompartmentID,
		DoorID:         placement.DoorID,
		PositionMM:     placement.PositionMM,
		Orientation:    placement.Orientation,
	}}
	problem = rebuildProblem(t, problem)
	candidate := result.Plan
	candidate.Duties[0].Trips[0].LoadStages[1].Placements[0].DoorID = "door-2"

	metrics, err := newTestEngine(t, problem).recomputePlanMetrics(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.StabilityCostCents != 250 ||
		metrics.TotalCostCents != result.Plan.Metrics.TotalCostCents+250 {
		t.Fatalf(
			"reload metrics = %+v, soft cargo = %+v, stages = %+v",
			metrics,
			problem.Commitments.SoftCargo,
			candidate.Duties[0].Trips[0].LoadStages,
		)
	}
}

func TestCandidateAssignmentCannotEscapeFreezeOverrideScope(t *testing.T) {
	problem := solverProblem(t)
	secondVehicle := problem.Vehicles[0]
	secondVehicle.ID = "vehicle-2"
	problem.Vehicles = append(problem.Vehicles, secondVehicle)
	secondDriver := problem.Drivers[0]
	secondDriver.ID = "driver-2"
	problem.Drivers = append(problem.Drivers, secondDriver)
	before := domain.FrozenTaskCommitment{
		TaskID:            "pickup-1",
		VehicleID:         "vehicle-1",
		DriverID:          "driver-1",
		Sequence:          0,
		PromisedServiceAt: problem.Horizon.Start,
		ToleranceSeconds:  300,
	}
	grantDigest, err := domain.Digest("freeze-override-grant")
	if err != nil {
		t.Fatal(err)
	}
	problem.Commitments.Frozen = []domain.FrozenTaskCommitment{before}
	problem.Commitments.FreezeOverride = &domain.FreezeOverrideConstraint{
		ApprovalID:  "approval-override-1",
		GrantDigest: grantDigest,
		Scopes: []domain.FreezeOverrideScope{{
			TaskID:             "pickup-1",
			Before:             before,
			AllowVehicleChange: true,
			AllowedVehicleIDs:  []domain.VehicleID{"vehicle-2"},
			AllowDriverChange:  true,
			AllowedDriverIDs:   []domain.DriverID{"driver-2"},
		}},
	}
	problem = rebuildProblem(t, problem)
	engine := newTestEngine(t, problem)

	if err := engine.checkCommitmentAssignment(
		problem.Requests[0],
		"vehicle-2",
		"driver-2",
	); err != nil {
		t.Fatalf("approved assignment was rejected: %v", err)
	}
	if err := engine.checkCommitmentAssignment(
		problem.Requests[0],
		"vehicle-1",
		"driver-2",
	); err != nil {
		t.Fatalf("partially changed approved assignment was rejected: %v", err)
	}
	if err := engine.checkCommitmentAssignment(
		problem.Requests[0],
		"vehicle-unknown",
		"driver-2",
	); err == nil {
		t.Fatal("assignment outside approved vehicle IDs was accepted")
	}
}
