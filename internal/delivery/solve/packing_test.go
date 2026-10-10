package solve

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestTripCargoAssignmentsUsesStableTaskIDsAtSameStop(t *testing.T) {
	problem := solverProblem(t)
	index := buildProblemIndex(problem)
	index.tasks["pickup-a"] = domain.ServiceTask{
		ID: "pickup-a", Kind: domain.TaskPickup,
		UnitIDs: []domain.FulfillmentUnitID{"unit-1"},
	}
	index.tasks["pickup-z"] = domain.ServiceTask{
		ID: "pickup-z", Kind: domain.TaskPickup,
		UnitIDs: []domain.FulfillmentUnitID{"unit-1"},
	}
	index.tasks["delivery-a"] = domain.ServiceTask{
		ID: "delivery-a", Kind: domain.TaskDelivery,
		UnitIDs: []domain.FulfillmentUnitID{"unit-1"},
	}
	index.tasks["delivery-z"] = domain.ServiceTask{
		ID: "delivery-z", Kind: domain.TaskDelivery,
		UnitIDs: []domain.FulfillmentUnitID{"unit-1"},
	}
	engine := engine{problem: problem, index: index}
	trip := domain.Trip{Stops: []domain.Stop{
		{TaskIDs: []domain.TaskID{"pickup-z", "pickup-a"}},
		{TaskIDs: []domain.TaskID{"delivery-z", "delivery-a"}},
	}}

	for run := 0; run < 20; run++ {
		assignments, err := engine.tripCargoAssignments(context.Background(), trip)
		if err != nil {
			t.Fatal(err)
		}
		if len(assignments) != 1 ||
			assignments[0].loadTask != "pickup-a" ||
			assignments[0].unloadTask != "delivery-a" {
			t.Fatalf("run %d assignments = %+v", run, assignments)
		}
	}
}

func TestPackAssignmentsReusesSpaceForNonOverlappingCargoIntervals(t *testing.T) {
	first := domain.CargoItem{
		ID:                  "cargo-a",
		SizeMM:              domain.Box{Length: 1_000, Width: 1_000, Height: 1_000},
		WeightG:             100,
		AllowedOrientations: []domain.Orientation{domain.OrientationLWH},
		TemperatureZone:     "ambient",
		CargoClass:          "general",
	}
	second := first
	second.ID = "cargo-b"
	vehicle := domain.Vehicle{
		Compartments: []domain.Compartment{{
			ID:                  "compartment-1",
			Bounds:              domain.Cuboid{Size: first.SizeMM},
			MaxPayloadG:         1_000,
			TemperatureZones:    []string{"ambient"},
			AllowedCargoClasses: []string{"general"},
		}},
		Doors: []domain.Door{{
			ID:             "door-1",
			CompartmentID:  "compartment-1",
			Opening:        domain.Cuboid{Origin: domain.Point3{X: 1_000}, Size: domain.Box{Length: 1, Width: 1_000, Height: 1_000}},
			ExtractionAxis: domain.AxisX,
			Direction:      1,
		}},
	}
	solverEngine := engine{
		problem: domain.ProblemSnapshot{Policy: domain.PlanningPolicy{
			DefaultMinSupportPPM: 1_000_000,
		}},
		index: problemIndex{cargo: map[domain.CargoID]domain.CargoItem{
			first.ID: first, second.ID: second,
		}},
	}

	packed, err := solverEngine.packAssignments(context.Background(), vehicle, []cargoAssignment{
		{cargo: first, loadStop: 0, unloadStop: 1},
		{cargo: second, loadStop: 1, unloadStop: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) != 2 ||
		packed[0].placement.PositionMM != (domain.Point3{}) ||
		packed[1].placement.PositionMM != packed[0].placement.PositionMM {
		t.Fatalf("placements = %+v, want sequential cargo to reuse the only cargo slot", packed)
	}
}

func TestRehandlesForTransitionBuildsExecutableOperationAndSchedule(t *testing.T) {
	const (
		rehandleSeconds = int64(90)
		rehandleCost    = int64(250)
	)
	target := domain.Placement{
		CargoID:       "cargo-target",
		CompartmentID: "compartment-1",
		PositionMM:    domain.Point3{},
		SizeMM:        domain.Box{Length: 1_000, Width: 1_000, Height: 1_000},
		DoorID:        "door-1",
	}
	blocker := domain.Placement{
		CargoID:       "cargo-blocker",
		CompartmentID: "compartment-1",
		PositionMM:    domain.Point3{X: 1_500},
		SizeMM:        domain.Box{Length: 500, Width: 1_000, Height: 1_000},
		DoorID:        "door-1",
	}
	vehicle := domain.Vehicle{
		Doors: []domain.Door{{
			ID:             "door-1",
			CompartmentID:  "compartment-1",
			Opening:        domain.Cuboid{Origin: domain.Point3{X: 3_000}, Size: domain.Box{Length: 1, Width: 2_000, Height: 2_000}},
			ExtractionAxis: domain.AxisX,
			Direction:      1,
		}},
	}
	solverEngine := engine{problem: domain.ProblemSnapshot{
		Policy: domain.PlanningPolicy{
			MaxRehandlesPerStop:       1,
			RehandleSecondsPerCargo:   rehandleSeconds,
			RehandleCostCentsPerCargo: rehandleCost,
		},
	}}
	previous := domain.LoadStage{
		AfterStopIndex: 0,
		Placements:     []domain.Placement{target, blocker},
	}
	current := domain.LoadStage{
		AfterStopIndex: 1,
		Placements:     []domain.Placement{blocker},
	}

	operations, err := solverEngine.rehandlesForTransition(
		context.Background(),
		vehicle,
		previous,
		current,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("rehandle operations = %+v, want one blocker operation", operations)
	}
	operation := operations[0]
	if operation.Sequence != 1 ||
		operation.CargoID != blocker.CargoID ||
		operation.StopIndex != 1 ||
		operation.Before != blocker ||
		operation.After != blocker ||
		operation.DurationSeconds != rehandleSeconds ||
		operation.CostCents != rehandleCost {
		t.Fatalf("rehandle operation = %+v", operation)
	}

	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	trip := domain.Trip{
		StartAt: base,
		EndAt:   base.Add(10 * time.Minute),
		Stops: []domain.Stop{
			{LocationID: "depot", ArrivalAt: base, ServiceAt: base, DepartureAt: base},
			{
				LocationID:  "customer",
				ArrivalAt:   base.Add(10 * time.Minute),
				ServiceAt:   base.Add(10 * time.Minute),
				DepartureAt: base.Add(10 * time.Minute),
			},
		},
		Schedule: []domain.DutySegment{{
			Kind: domain.SegmentDrive, DriverID: "driver-1",
			From: "depot", To: "customer",
			StartAt: base, EndAt: base.Add(10 * time.Minute),
			TaskIDs: []domain.TaskID{},
		}},
		LoadStages: []domain.LoadStage{
			previous,
			{
				AfterStopIndex: current.AfterStopIndex,
				Placements:     current.Placements,
				Rehandles:      operations,
			},
		},
	}
	if err := solverEngine.applyRehandleSchedule(
		context.Background(),
		&trip,
		"driver-1",
	); err != nil {
		t.Fatal(err)
	}
	wantEnd := base.Add(10*time.Minute + time.Duration(rehandleSeconds)*time.Second)
	if len(trip.Schedule) != 2 ||
		trip.Schedule[1].Kind != domain.SegmentRehandle ||
		trip.Schedule[1].StartAt != base.Add(10*time.Minute) ||
		trip.Schedule[1].EndAt != wantEnd ||
		trip.Stops[1].DepartureAt != wantEnd ||
		trip.EndAt != wantEnd {
		t.Fatalf("trip after rehandle scheduling = %+v", trip)
	}
}

func TestPackAssignmentsCompletesThreeHundredCargoCaseWithinDeadline(t *testing.T) {
	solverEngine, vehicle, assignments := sequentialPackingCase(300)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	packed, err := solverEngine.packAssignments(ctx, vehicle, assignments)
	if err != nil {
		t.Fatal(err)
	}
	if len(packed) != 300 {
		t.Fatalf("packed cargo = %d, want 300", len(packed))
	}
	for index, item := range packed {
		if item.placement.PositionMM != (domain.Point3{}) {
			t.Fatalf("cargo %d placement = %+v, want reusable origin", index, item.placement)
		}
	}
}

func TestPackAssignmentsPollsCancellationInsidePacking(t *testing.T) {
	solverEngine, vehicle, assignments := sequentialPackingCase(300)
	ctx := &cancelAfterChecksContext{remaining: 100}

	_, err := solverEngine.packAssignments(ctx, vehicle, assignments)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("packAssignments error = %v, want context cancellation", err)
	}
	if ctx.remaining >= 100 {
		t.Fatal("packing did not poll the context")
	}
}

type cancelAfterChecksContext struct {
	remaining int
}

func (ctx *cancelAfterChecksContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (ctx *cancelAfterChecksContext) Done() <-chan struct{} {
	return nil
}

func (ctx *cancelAfterChecksContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func (ctx *cancelAfterChecksContext) Value(any) any {
	return nil
}

func sequentialPackingCase(
	count int,
) (*engine, domain.Vehicle, []cargoAssignment) {
	size := domain.Box{Length: 100, Width: 100, Height: 100}
	vehicle := domain.Vehicle{
		Compartments: []domain.Compartment{{
			ID:                  "compartment-1",
			Bounds:              domain.Cuboid{Size: size},
			MaxPayloadG:         1_000,
			TemperatureZones:    []string{"ambient"},
			AllowedCargoClasses: []string{"general"},
		}},
		Doors: []domain.Door{{
			ID:             "door-1",
			CompartmentID:  "compartment-1",
			Opening:        domain.Cuboid{Origin: domain.Point3{X: 100}, Size: domain.Box{Length: 1, Width: 100, Height: 100}},
			ExtractionAxis: domain.AxisX,
			Direction:      1,
		}},
	}
	assignments := make([]cargoAssignment, 0, count)
	cargo := make(map[domain.CargoID]domain.CargoItem, count)
	for index := 0; index < count; index++ {
		item := domain.CargoItem{
			ID:                  domain.CargoID(fmt.Sprintf("cargo-%03d", index)),
			SizeMM:              size,
			WeightG:             1,
			AllowedOrientations: []domain.Orientation{domain.OrientationLWH},
			TemperatureZone:     "ambient",
			CargoClass:          "general",
		}
		cargo[item.ID] = item
		assignments = append(assignments, cargoAssignment{
			cargo:      item,
			loadTask:   domain.TaskID(fmt.Sprintf("load-%03d", index)),
			unloadTask: domain.TaskID(fmt.Sprintf("unload-%03d", index)),
			loadStop:   index,
			unloadStop: index + 1,
		})
	}
	return &engine{
		problem: domain.ProblemSnapshot{Policy: domain.PlanningPolicy{
			DefaultMinSupportPPM: 1_000_000,
		}},
		index: problemIndex{cargo: cargo},
	}, vehicle, assignments
}
