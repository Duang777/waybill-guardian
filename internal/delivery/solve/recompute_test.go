package solve

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
)

func TestRebuildPlanDutiesRecomputesCompleteAffectedDuty(t *testing.T) {
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
	plan := normalizeCandidatePlan(solved.Plan)
	trip := &plan.Duties[0].Trips[0]
	taskStops := make([]int, 0)
	for stopIndex, stop := range trip.Stops {
		if len(stop.TaskIDs) > 0 {
			taskStops = append(taskStops, stopIndex)
		}
	}
	left := taskStops[len(taskStops)-2]
	right := taskStops[len(taskStops)-1]
	trip.Stops[left].TaskIDs, trip.Stops[right].TaskIDs =
		trip.Stops[right].TaskIDs, trip.Stops[left].TaskIDs
	trip.Stops[left].LocationID, trip.Stops[right].LocationID =
		trip.Stops[right].LocationID, trip.Stops[left].LocationID

	rebuilt, err := engine.rebuildPlanDuties(context.Background(), plan, []dutyRebuild{{
		DutyIndex: 0,
		FromTrip:  0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	evaluation := engine.evaluateMaterializedPlan(rebuilt)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		t.Fatalf("evaluation = %#v, want feasible candidate", evaluation)
	}
	certified, _, err := engine.certifyCandidate(feasible.state)
	if err != nil {
		t.Fatal(err)
	}
	gotTasks := tripTaskIDs(certified.plan.Duties[0].Trips[0])
	wantTasks := []domain.TaskID{"pickup-1", "pickup-2", "delivery-2", "delivery-1"}
	if !slices.Equal(gotTasks, wantTasks) {
		t.Fatalf("rebuilt task order = %v, want %v", gotTasks, wantTasks)
	}
	if certified.plan.Duties[0].Trips[0].Schedule[0].StartAt !=
		solved.Plan.Duties[0].Trips[0].Schedule[0].StartAt {
		t.Fatalf("rebuild changed stable duty start: %s != %s",
			certified.plan.Duties[0].Trips[0].Schedule[0].StartAt,
			solved.Plan.Duties[0].Trips[0].Schedule[0].StartAt)
	}
	if tripTaskIDs(solved.Plan.Duties[0].Trips[0])[2] != "delivery-1" {
		t.Fatal("rebuildPlanDuties mutated the source plan")
	}
}

func TestRebuildPlanDutiesRejectsPickupDeliveryOrderViolation(t *testing.T) {
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
	plan := normalizeCandidatePlan(solved.Plan)
	trip := &plan.Duties[0].Trips[0]
	taskStops := make([]int, 0)
	for stopIndex, stop := range trip.Stops {
		if len(stop.TaskIDs) > 0 {
			taskStops = append(taskStops, stopIndex)
		}
	}
	first := taskStops[0]
	last := taskStops[len(taskStops)-1]
	trip.Stops[first].TaskIDs, trip.Stops[last].TaskIDs =
		trip.Stops[last].TaskIDs, trip.Stops[first].TaskIDs
	trip.Stops[first].LocationID, trip.Stops[last].LocationID =
		trip.Stops[last].LocationID, trip.Stops[first].LocationID

	if _, err := engine.rebuildPlanDuties(context.Background(), plan, []dutyRebuild{{
		DutyIndex: 0,
		FromTrip:  0,
	}}); err == nil {
		t.Fatal("rebuildPlanDuties accepted delivery before pickup")
	}
}

func TestEveryMaterializedMoveMatchesFullRecomputation(t *testing.T) {
	problem := recomputeOperatorProblem(t)
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
	state, err := candidateStateFromPlan(solved.Plan)
	if err != nil {
		t.Fatal(err)
	}
	generated := make(map[OperatorID]int)
	materialized := make(map[OperatorID]int)
	for _, operator := range engine.registry.Operators() {
		operator := operator
		t.Run(string(operator.ID()), func(t *testing.T) {
			err := operator.Enumerate(
				context.Background(),
				engine,
				state,
				func(move searchMove) bool {
					generated[operator.ID()]++
					candidate, applyErr := move.Apply(
						context.Background(),
						engine,
						state,
					)
					if applyErr != nil {
						return true
					}
					materialized[operator.ID()]++
					_, recomputeErr := engine.recomputeMaterializedCandidate(
						context.Background(),
						state,
						candidate,
					)
					if errors.Is(recomputeErr, ErrRecomputeMismatch) {
						t.Fatalf(
							"move %q diverged between incremental and full recomputation: %v",
							move.Key(),
							recomputeErr,
						)
					}
					return true
				},
			)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, operatorID := range requiredOperatorIDs() {
		if generated[operatorID] == 0 {
			t.Errorf("operator %q generated no equivalence probes", operatorID)
		}
		if materialized[operatorID] == 0 && operatorID != OperatorTripSplit {
			t.Errorf("operator %q materialized no equivalence probe", operatorID)
		}
	}
}

func TestMissingDependencyEdgesCauseRecomputeMismatch(t *testing.T) {
	t.Run("previous end time", func(t *testing.T) {
		problem := twoTripProblem(t)
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
		state, err := candidateStateFromPlan(solved.Plan)
		if err != nil {
			t.Fatal(err)
		}
		segment := firstSegmentOfKind(
			t,
			solved.Plan.Duties[0].Trips[0],
			domain.SegmentDrive,
		)
		candidate, err := (breakMove{
			duty:    0,
			trip:    0,
			segment: segment,
			action:  breakMoveInsert,
		}).Apply(context.Background(), engine, state)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = engine.recomputeMaterializedCandidate(
			context.Background(),
			state,
			candidate,
		); err != nil {
			t.Fatalf("complete dependency graph rejected candidate: %v", err)
		}
		_, err = engine.recomputeMaterializedCandidateWithDependencies(
			context.Background(),
			state,
			candidate,
			allDependencyEdges&^dependencyPreviousEndTime,
		)
		if !errors.Is(err, ErrRecomputeMismatch) {
			t.Fatalf("missing end-time edge error = %v, want ErrRecomputeMismatch", err)
		}
	})

	t.Run("previous end depot", func(t *testing.T) {
		problem := twoTripProblem(t)
		problem.Depots = append(problem.Depots, domain.Depot{
			ID:             "depot-2",
			LocationID:     "depot-1",
			AllowTripStart: true,
			AllowTripEnd:   true,
			Docks:          append([]domain.Dock{}, problem.Depots[0].Docks...),
		})
		problem = rebuildProblem(t, problem)
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
		state, err := candidateStateFromPlan(solved.Plan)
		if err != nil {
			t.Fatal(err)
		}
		candidate := normalizeCandidatePlan(solved.Plan)
		candidate.Duties[0].Trips[0].EndDepotID = "depot-2"
		_, completeErr := engine.recomputeMaterializedCandidate(
			context.Background(),
			state,
			candidate,
		)
		if completeErr == nil || errors.Is(completeErr, ErrRecomputeMismatch) {
			t.Fatalf(
				"complete dependency graph error = %v, want matching continuity rejection",
				completeErr,
			)
		}
		_, err = engine.recomputeMaterializedCandidateWithDependencies(
			context.Background(),
			state,
			candidate,
			allDependencyEdges&^dependencyPreviousEndDepot,
		)
		if !errors.Is(err, ErrRecomputeMismatch) {
			t.Fatalf("missing end-depot edge error = %v, want ErrRecomputeMismatch", err)
		}
	})

	t.Run("previous end state of charge", func(t *testing.T) {
		problem := twoTripElectricRouteProblem(t)
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
		state, err := candidateStateFromPlan(solved.Plan)
		if err != nil {
			t.Fatal(err)
		}
		firstTasks := tripTaskIDs(solved.Plan.Duties[0].Trips[0])
		if len(firstTasks) != 3 {
			t.Fatalf("first trip tasks = %v, want three route decisions", firstTasks)
		}
		candidate, err := (routeMove{
			operator:  OperatorSwap,
			dutyA:     0,
			tripA:     0,
			positionA: 1,
			dutyB:     0,
			tripB:     0,
			positionB: 2,
		}).Apply(context.Background(), engine, state)
		if err != nil {
			t.Fatal(err)
		}
		before := solved.Plan.Duties[0].Trips[0]
		after := candidate.Duties[0].Trips[0]
		if !before.EndAt.Equal(after.EndAt) {
			t.Fatalf("SOC fixture also changed end time: %s != %s", before.EndAt, after.EndAt)
		}
		if tripFinalSOC(before) == tripFinalSOC(after) {
			t.Fatalf("SOC fixture did not change first-trip final SOC")
		}
		if _, err = engine.recomputeMaterializedCandidate(
			context.Background(),
			state,
			candidate,
		); err != nil {
			t.Fatalf("complete dependency graph rejected candidate: %v", err)
		}
		_, err = engine.recomputeMaterializedCandidateWithDependencies(
			context.Background(),
			state,
			candidate,
			allDependencyEdges&^dependencyPreviousEndSOC,
		)
		if !errors.Is(err, ErrRecomputeMismatch) {
			t.Fatalf("missing end-SOC edge error = %v, want ErrRecomputeMismatch", err)
		}
	})
}

func twoTripProblem(t testing.TB) domain.ProblemSnapshot {
	t.Helper()
	problem := solverProblem(t)
	for taskIndex := range problem.Requests[0].Tasks {
		task := &problem.Requests[0].Tasks[taskIndex]
		if len(task.HardWindows) == 0 {
			continue
		}
		if task.Kind == domain.TaskPickup || task.Kind == domain.TaskDepotLoad {
			task.HardWindows[0].End = problem.Horizon.Start.Add(8 * time.Hour)
		} else {
			task.HardWindows[0].End = problem.Horizon.Start.Add(11 * time.Hour)
		}
	}
	problem.Requests[0].Split = domain.SplitPolicy{
		Mode:             domain.SplitByUnit,
		MinUnitsPerSplit: 1,
		MaxSplits:        2,
		SameVehicle:      true,
		SameTrip:         false,
	}
	problem.Units[0].AtomicGroupID = "atomic-1"
	problem.Units[1].AtomicGroupID = "atomic-2"
	problem.Cargo[0].SizeMM = domain.Box{Length: 2_500, Width: 1_500, Height: 1_000}
	problem.Cargo[1].SizeMM = domain.Box{Length: 2_500, Width: 1_500, Height: 1_000}
	return rebuildProblem(t, problem)
}

func recomputeOperatorProblem(t testing.TB) domain.ProblemSnapshot {
	t.Helper()
	problem := twoTripElectricRouteProblem(t)
	base := problem.Horizon.Start
	problem.Requests[0].Tasks = append(
		problem.Requests[0].Tasks,
		domain.ServiceTask{
			ID:             "service-2",
			Kind:           domain.TaskService,
			LocationID:     "customer-1",
			HardWindows:    []domain.TimeRange{{Start: base, End: base.Add(10 * time.Hour)}},
			ServiceSeconds: 60,
			PredecessorIDs: []domain.TaskID{"pickup-2"},
			UnitIDs:        []domain.FulfillmentUnitID{"unit-2"},
			RequiredSkills: domain.SkillSet{"cold"},
		},
	)
	problem.Cargo[0].AllowedOrientations = append(
		problem.Cargo[0].AllowedOrientations,
		domain.OrientationLHW,
	)
	for taskIndex := range problem.Requests[0].Tasks {
		task := &problem.Requests[0].Tasks[taskIndex]
		switch task.ID {
		case "delivery-1":
			task.PredecessorIDs = []domain.TaskID{"service-1"}
		case "delivery-2":
			task.PredecessorIDs = []domain.TaskID{"service-2"}
		}
	}
	problem.Depots = append(problem.Depots, domain.Depot{
		ID:             "depot-2",
		LocationID:     "depot-1",
		AllowTripStart: true,
		AllowTripEnd:   true,
		Docks:          append([]domain.Dock{}, problem.Depots[0].Docks...),
	})
	secondDriver := problem.Drivers[0]
	secondDriver.ID = "driver-2"
	problem.Drivers = append(problem.Drivers, secondDriver)
	secondVehicle := problem.Vehicles[0]
	secondVehicle.ID = "vehicle-2"
	problem.Vehicles = append(problem.Vehicles, secondVehicle)
	problem.Vehicles[0].Energy.InitialSOCWh = 20_000
	problem.Vehicles[1].Energy.InitialSOCWh = 20_000
	problem.Chargers = []domain.ChargingStation{
		{
			ID:             "charger-a",
			LocationID:     "depot-1",
			ConnectorTypes: []string{"ccs2"},
			Availability:   []domain.TimeRange{problem.Horizon},
			Capacity:       2,
			MaxPowerW:      50_000,
		},
		{
			ID:             "charger-b",
			LocationID:     "depot-1",
			ConnectorTypes: []string{"ccs2"},
			Availability:   []domain.TimeRange{problem.Horizon},
			Capacity:       2,
			MaxPowerW:      25_000,
		},
	}
	problem.Policy.MaxRehandlesPerStop = 4
	problem.Policy.RehandleSecondsPerCargo = 60
	problem.Policy.RehandleCostCentsPerCargo = 100
	return rebuildProblem(t, problem)
}

func twoTripElectricRouteProblem(t testing.TB) domain.ProblemSnapshot {
	t.Helper()
	problem := twoTripProblem(t)
	base := problem.Horizon.Start
	problem.Requests[0].Tasks = append(
		problem.Requests[0].Tasks,
		domain.ServiceTask{
			ID:             "service-1",
			Kind:           domain.TaskService,
			LocationID:     "customer-2",
			HardWindows:    []domain.TimeRange{{Start: base, End: base.Add(8 * time.Hour)}},
			ServiceSeconds: 120,
			PredecessorIDs: []domain.TaskID{"pickup-1"},
			UnitIDs:        []domain.FulfillmentUnitID{"unit-1"},
			RequiredSkills: domain.SkillSet{"cold"},
		},
	)
	problem.Vehicles[0].Energy = domain.EnergySpec{
		Kind:              domain.EnergyElectric,
		MatrixProfileID:   "ev-asymmetric",
		BatteryCapacityWh: 100_000,
		InitialSOCWh:      100_000,
		ReserveSOCWh:      1_000,
		ConnectorTypes:    []string{"ccs2"},
		ChargingCurve: []domain.ChargingBand{{
			FromSOCPPM: 0,
			ToSOCPPM:   1_000_000,
			PowerW:     50_000,
		}},
	}
	problem.Energy.Profiles = []domain.EnergyProfileMatrix{{
		ProfileID: "ev-asymmetric",
		BaseWh: []int64{
			0, 8_000, 10_000,
			8_000, 0, 12_000,
			5_000, 12_000, 0,
		},
		LoadWhPerTonne: make([]int64, 9),
	}}
	return rebuildProblem(t, problem)
}

func rebuildProblem(
	t testing.TB,
	problem domain.ProblemSnapshot,
) domain.ProblemSnapshot {
	t.Helper()
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	rebuilt, err := service.BuildProblemSnapshot(problem)
	if err != nil {
		t.Fatal(err)
	}
	return rebuilt
}

func firstSegmentOfKind(
	t testing.TB,
	trip domain.Trip,
	kind domain.SegmentKind,
) int {
	t.Helper()
	for index, segment := range trip.Schedule {
		if segment.Kind == kind {
			return index
		}
	}
	t.Fatalf("trip has no %q segment", kind)
	return -1
}

func tripFinalSOC(trip domain.Trip) int64 {
	if len(trip.Energy) == 0 {
		return 0
	}
	return trip.Energy[len(trip.Energy)-1].EndSOCWh
}
