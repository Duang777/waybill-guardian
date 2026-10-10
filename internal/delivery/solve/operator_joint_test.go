package solve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type operatorProbe struct {
	engine    *engine
	incumbent certifiedState
	move      searchMove
}

func TestEveryOperatorHasFeasibleAndCoupledRejection(t *testing.T) {
	tests := []struct {
		operator OperatorID
		build    func(testing.TB) (operatorProbe, operatorProbe)
	}{
		{OperatorRelocate, buildRelocateProbes},
		{OperatorSwap, buildSwapProbes},
		{OperatorTwoOpt, buildTwoOptProbes},
		{OperatorCrossExchange, buildCrossExchangeProbes},
		{OperatorTripSplit, buildTripSplitProbes},
		{OperatorTripMerge, buildTripMergeProbes},
		{OperatorDepot, buildDepotProbes},
		{OperatorVehicle, buildVehicleProbes},
		{OperatorDriver, buildDriverProbes},
		{OperatorBreak, buildBreakProbes},
		{OperatorCharge, buildChargeProbes},
		{OperatorPlacement, buildPlacementProbes},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.operator), func(t *testing.T) {
			feasible, rejected := test.build(t)
			if feasible.move.Operator() != test.operator ||
				rejected.move.Operator() != test.operator {
				t.Fatalf(
					"probe operators = %q/%q, want %q",
					feasible.move.Operator(),
					rejected.move.Operator(),
					test.operator,
				)
			}
			assertProbeFeasible(t, feasible)
			assertProbeRejected(t, rejected)
		})
	}
}

func assertProbeFeasible(t testing.TB, probe operatorProbe) {
	t.Helper()
	plan, err := probe.move.Apply(
		context.Background(),
		probe.engine,
		probe.incumbent.candidateState,
	)
	if err != nil {
		t.Fatalf("feasible move %q apply: %v", probe.move.Key(), err)
	}
	plan, err = probe.engine.recomputeMaterializedCandidate(
		context.Background(),
		probe.incumbent.candidateState,
		plan,
	)
	if err != nil {
		t.Fatalf("feasible move %q recompute: %v", probe.move.Key(), err)
	}
	evaluation := probe.engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		t.Fatalf("feasible move %q evaluation = %#v", probe.move.Key(), evaluation)
	}
	certified, report, err := probe.engine.certifyCandidate(feasible.state)
	if err != nil {
		t.Fatalf(
			"feasible move %q certification: %v, violations=%+v",
			probe.move.Key(),
			err,
			report.Violations,
		)
	}
	if !certified.report.Valid {
		t.Fatalf("feasible move %q was not certified", probe.move.Key())
	}
}

func assertProbeRejected(t testing.TB, probe operatorProbe) {
	t.Helper()
	plan, err := probe.move.Apply(
		context.Background(),
		probe.engine,
		probe.incumbent.candidateState,
	)
	if err != nil {
		return
	}
	plan, err = probe.engine.recomputeMaterializedCandidate(
		context.Background(),
		probe.incumbent.candidateState,
		plan,
	)
	if err != nil {
		if errors.Is(err, ErrRecomputeMismatch) {
			t.Fatalf("rejected move %q exposed internal mismatch: %v", probe.move.Key(), err)
		}
		return
	}
	evaluation := probe.engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		return
	}
	_, _, err = probe.engine.certifyCandidate(feasible.state)
	if !errors.Is(err, ErrCandidateRejected) {
		t.Fatalf(
			"rejection probe %q produced certification error %v, want ErrCandidateRejected",
			probe.move.Key(),
			err,
		)
	}
}

func buildRelocateProbes(t testing.TB) (operatorProbe, operatorProbe) {
	engine, incumbent := solvedCertifiedState(t, solverProblem(t))
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorRelocate,
				dutyA:    0, tripA: 0, positionA: 2,
				dutyB: 0, tripB: 0, positionB: 3,
			},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorRelocate,
				dutyA:    0, tripA: 0, positionA: 2,
				dutyB: 0, tripB: 0, positionB: 0,
			},
		}
}

func buildSwapProbes(t testing.TB) (operatorProbe, operatorProbe) {
	engine, incumbent := solvedCertifiedState(t, solverProblem(t))
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorSwap,
				dutyA:    0, tripA: 0, positionA: 2,
				dutyB: 0, tripB: 0, positionB: 3,
			},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorSwap,
				dutyA:    0, tripA: 0, positionA: 0,
				dutyB: 0, tripB: 0, positionB: 2,
			},
		}
}

func buildTwoOptProbes(t testing.TB) (operatorProbe, operatorProbe) {
	engine, incumbent := solvedCertifiedState(t, solverProblem(t))
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorTwoOpt,
				dutyA:    0, tripA: 0, positionA: 2,
				dutyB: 0, tripB: 0, positionB: 3,
			},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorTwoOpt,
				dutyA:    0, tripA: 0, positionA: 0,
				dutyB: 0, tripB: 0, positionB: 2,
			},
		}
}

func buildCrossExchangeProbes(t testing.TB) (operatorProbe, operatorProbe) {
	problem := solverProblem(t)
	problem.Requests[0].Split = domain.SplitPolicy{
		Mode:             domain.SplitByUnit,
		MinUnitsPerSplit: 1,
		MaxSplits:        2,
		SameVehicle:      true,
		SameTrip:         false,
	}
	problem.Units[0].AtomicGroupID = "atomic-1"
	problem.Units[1].AtomicGroupID = "atomic-2"
	problem.Requests[0].Tasks = append(
		problem.Requests[0].Tasks,
		domain.ServiceTask{
			ID:             "service-a",
			Kind:           domain.TaskService,
			LocationID:     "customer-1",
			HardWindows:    []domain.TimeRange{problem.Horizon},
			ServiceSeconds: 60,
			UnitIDs:        []domain.FulfillmentUnitID{},
			RequiredSkills: domain.SkillSet{"cold"},
		},
		domain.ServiceTask{
			ID:             "service-b",
			Kind:           domain.TaskService,
			LocationID:     "customer-2",
			HardWindows:    []domain.TimeRange{problem.Horizon},
			ServiceSeconds: 60,
			UnitIDs:        []domain.FulfillmentUnitID{},
			RequiredSkills: domain.SkillSet{"cold"},
		},
	)
	problem = rebuildProblem(t, problem)
	engine, original := solvedCertifiedState(t, problem)
	plan := normalizeCandidatePlan(original.plan)
	template := plan.Duties[0].Trips[0]
	first := template
	first.ID = "vehicle-1-trip-001"
	rewriteTripTaskIDs(
		engine,
		&first,
		[]domain.TaskID{"pickup-1", "delivery-1", "service-a"},
	)
	second := template
	second.ID = "vehicle-1-trip-002"
	rewriteTripTaskIDs(
		engine,
		&second,
		[]domain.TaskID{"pickup-2", "delivery-2", "service-b"},
	)
	plan.Duties[0].Trips = []domain.Trip{first, second}
	plan, err := engine.rebuildPlanDuties(
		context.Background(),
		plan,
		[]dutyRebuild{{DutyIndex: 0, FromTrip: 0}},
	)
	if err != nil {
		t.Fatal(err)
	}
	incumbent := certifyPlan(t, engine, plan)
	service1 := locateTask(t, incumbent.plan, "service-a")
	service2 := locateTask(t, incumbent.plan, "service-b")
	pickup1 := locateTask(t, incumbent.plan, "pickup-1")
	pickup2 := locateTask(t, incumbent.plan, "pickup-2")
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorCrossExchange,
				dutyA:    service1.duty, tripA: service1.trip, positionA: service1.position,
				dutyB: service2.duty, tripB: service2.trip, positionB: service2.position,
			},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: routeMove{
				operator: OperatorCrossExchange,
				dutyA:    pickup1.duty, tripA: pickup1.trip, positionA: pickup1.position,
				dutyB: pickup2.duty, tripB: pickup2.trip, positionB: pickup2.position,
			},
		}
}

func buildTripSplitProbes(t testing.TB) (operatorProbe, operatorProbe) {
	problem := solverProblem(t)
	problem.Requests[0].Split = domain.SplitPolicy{
		Mode:             domain.SplitByUnit,
		MinUnitsPerSplit: 1,
		MaxSplits:        2,
		SameVehicle:      true,
		SameTrip:         false,
	}
	problem.Units[0].AtomicGroupID = "atomic-1"
	problem.Units[1].AtomicGroupID = "atomic-2"
	problem = rebuildProblem(t, problem)
	engine, original := solvedCertifiedState(t, problem)
	pairedPlan := normalizeCandidatePlan(original.plan)
	rewriteTripTaskIDs(
		engine,
		&pairedPlan.Duties[0].Trips[0],
		[]domain.TaskID{"pickup-1", "delivery-1", "pickup-2", "delivery-2"},
	)
	pairedPlan, err := engine.rebuildPlanDuties(
		context.Background(),
		pairedPlan,
		[]dutyRebuild{{DutyIndex: 0, FromTrip: 0}},
	)
	if err != nil {
		t.Fatal(err)
	}
	paired := certifyPlan(t, engine, pairedPlan)
	return operatorProbe{
			engine: engine, incumbent: paired,
			move: tripSplitMove{duty: 0, trip: 0, cut: 2},
		}, operatorProbe{
			engine: engine, incumbent: original,
			move: tripSplitMove{duty: 0, trip: 0, cut: 2},
		}
}

func buildTripMergeProbes(t testing.TB) (operatorProbe, operatorProbe) {
	splitFeasible, _ := buildTripSplitProbes(t)
	splitPlan, err := splitFeasible.move.Apply(
		context.Background(),
		splitFeasible.engine,
		splitFeasible.incumbent.candidateState,
	)
	if err != nil {
		t.Fatal(err)
	}
	splitPlan, err = splitFeasible.engine.recomputeMaterializedCandidate(
		context.Background(),
		splitFeasible.incumbent.candidateState,
		splitPlan,
	)
	if err != nil {
		t.Fatal(err)
	}
	splitState := certifyPlan(t, splitFeasible.engine, splitPlan)

	rejectEngine, rejectState := mergeSkillConflictState(t)
	return operatorProbe{
			engine: splitFeasible.engine, incumbent: splitState,
			move: tripMergeMove{duty: 0, trip: 0},
		}, operatorProbe{
			engine: rejectEngine, incumbent: rejectState,
			move: tripMergeMove{duty: 0, trip: 0},
		}
}

func mergeSkillConflictState(t testing.TB) (*engine, certifiedState) {
	t.Helper()
	problem := solverProblem(t)
	problem.Requests[0].Split = domain.SplitPolicy{
		Mode:             domain.SplitByUnit,
		MinUnitsPerSplit: 1,
		MaxSplits:        2,
		SameVehicle:      true,
		SameTrip:         false,
	}
	problem.Units[0].AtomicGroupID = "atomic-1"
	problem.Units[1].AtomicGroupID = "atomic-2"
	secondDriver := problem.Drivers[0]
	secondDriver.ID = "driver-2"
	problem.Drivers = append(problem.Drivers, secondDriver)
	problem.Commitments.Frozen = []domain.FrozenTaskCommitment{{
		TaskID:            "pickup-2",
		VehicleID:         "vehicle-1",
		DriverID:          "driver-2",
		Sequence:          0,
		PromisedServiceAt: problem.Horizon.Start,
		ToleranceSeconds:  int64(problem.Horizon.End.Sub(problem.Horizon.Start) / time.Second),
	}}
	problem = rebuildProblem(t, problem)
	engine := newTestEngine(t, problem)
	vehicle := problem.Vehicles[0]
	duty := domain.VehicleDuty{
		VehicleID: vehicle.ID,
		DriverIDs: []domain.DriverID{"driver-1", "driver-2"},
		Trips:     []domain.Trip{},
	}
	first, err := engine.compileTripBlueprint(
		context.Background(),
		vehicle,
		duty,
		nil,
		tripBlueprint{
			id: "vehicle-1-trip-001", startDepotID: "depot-1", endDepotID: "depot-1",
			driverID: "driver-1", taskIDs: []domain.TaskID{"pickup-1", "delivery-1"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	duty.Trips = append(duty.Trips, first)
	second, err := engine.compileTripBlueprint(
		context.Background(),
		vehicle,
		duty,
		duty.Trips,
		tripBlueprint{
			id: "vehicle-1-trip-002", startDepotID: "depot-1", endDepotID: "depot-1",
			driverID: "driver-2", taskIDs: []domain.TaskID{"pickup-2", "delivery-2"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	duty.Trips = append(duty.Trips, second)
	plan := engine.buildPlan(
		map[domain.VehicleID]domain.VehicleDuty{vehicle.ID: duty},
		nil,
	)
	return engine, certifyPlan(t, engine, plan)
}

func buildDepotProbes(t testing.TB) (operatorProbe, operatorProbe) {
	problem := solverProblem(t)
	problem.Depots = append(
		problem.Depots,
		domain.Depot{
			ID:             "depot-2",
			LocationID:     "depot-1",
			AllowTripStart: true,
			AllowTripEnd:   true,
			Docks:          append([]domain.Dock{}, problem.Depots[0].Docks...),
		},
		domain.Depot{
			ID:             "depot-bad",
			LocationID:     "customer-1",
			AllowTripStart: true,
			AllowTripEnd:   true,
			Docks:          append([]domain.Dock{}, problem.Depots[0].Docks...),
		},
	)
	problem = rebuildProblem(t, problem)
	engine, incumbent := solvedCertifiedState(t, problem)
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: depotMove{
				duty: 0, trip: 0, boundary: depotBoundaryEnd, depotID: "depot-2",
			},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: depotMove{
				duty: 0, trip: 0, boundary: depotBoundaryStart, depotID: "depot-bad",
			},
		}
}

func buildVehicleProbes(t testing.TB) (operatorProbe, operatorProbe) {
	problem := solverProblem(t)
	compatible := problem.Vehicles[0]
	compatible.ID = "vehicle-2"
	incompatible := problem.Vehicles[0]
	incompatible.ID = "vehicle-bad"
	incompatible.Skills = domain.SkillSet{"liftgate"}
	problem.Vehicles = append(problem.Vehicles, compatible, incompatible)
	problem = rebuildProblem(t, problem)
	engine, incumbent := solvedCertifiedState(t, problem)
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: vehicleMove{duty: 0, vehicleID: "vehicle-2"},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: vehicleMove{duty: 0, vehicleID: "vehicle-bad"},
		}
}

func buildDriverProbes(t testing.TB) (operatorProbe, operatorProbe) {
	problem := solverProblem(t)
	compatible := problem.Drivers[0]
	compatible.ID = "driver-2"
	incompatible := problem.Drivers[0]
	incompatible.ID = "driver-bad"
	incompatible.Skills = domain.SkillSet{"liftgate"}
	problem.Drivers = append(problem.Drivers, compatible, incompatible)
	problem = rebuildProblem(t, problem)
	engine, incumbent := solvedCertifiedState(t, problem)
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: driverMove{duty: 0, driverID: "driver-2"},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: driverMove{duty: 0, driverID: "driver-bad"},
		}
}

func buildBreakProbes(t testing.TB) (operatorProbe, operatorProbe) {
	positiveProblem := solverProblem(t)
	for taskIndex := range positiveProblem.Requests[0].Tasks {
		for windowIndex := range positiveProblem.Requests[0].Tasks[taskIndex].HardWindows {
			positiveProblem.Requests[0].Tasks[taskIndex].HardWindows[windowIndex].End =
				positiveProblem.Horizon.End
		}
	}
	positiveProblem = rebuildProblem(t, positiveProblem)
	positiveEngine, positiveState := solvedCertifiedState(t, positiveProblem)
	drive := firstSegmentOfKind(
		t,
		positiveState.plan.Duties[0].Trips[0],
		domain.SegmentDrive,
	)

	negativeEngine, negativeState := solvedCertifiedState(t, twoTripProblem(t))
	breakSegment := firstSegmentOfKind(
		t,
		negativeState.plan.Duties[0].Trips[1],
		domain.SegmentBreak,
	)
	return operatorProbe{
			engine: positiveEngine, incumbent: positiveState,
			move: breakMove{
				duty: 0, trip: 0, segment: drive, action: breakMoveInsert,
			},
		}, operatorProbe{
			engine: negativeEngine, incumbent: negativeState,
			move: breakMove{
				duty: 0, trip: 1, segment: breakSegment, action: breakMoveRemove,
			},
		}
}

func buildChargeProbes(t testing.TB) (operatorProbe, operatorProbe) {
	problem := chargingOperatorProblem(t)
	engine, incumbent := solvedCertifiedState(t, problem)
	charge := firstSegmentOfKind(
		t,
		incumbent.plan.Duties[0].Trips[0],
		domain.SegmentCharge,
	)
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: chargeMove{
				duty: 0, trip: 0, segment: charge, chargerID: "charger-b",
			},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: chargeMove{
				duty: 0, trip: 0, segment: charge, chargerID: "charger-bad",
			},
		}
}

func buildPlacementProbes(t testing.TB) (operatorProbe, operatorProbe) {
	problem := solverProblem(t)
	problem.Cargo[0].SizeMM = domain.Box{Length: 2_500, Width: 1_500, Height: 500}
	problem.Cargo[0].AllowedOrientations = []domain.Orientation{
		domain.OrientationLWH,
		domain.OrientationLHW,
		domain.OrientationWLH,
	}
	problem.Policy.MaxRehandlesPerStop = 4
	problem.Policy.RehandleSecondsPerCargo = 60
	problem.Policy.RehandleCostCentsPerCargo = 100
	problem = rebuildProblem(t, problem)
	engine, incumbent := solvedCertifiedState(t, problem)
	stage := survivingCargoStage(
		incumbent.plan.Duties[0].Trips[0],
		problem.Cargo[0].ID,
	)
	if stage < 1 {
		t.Fatal("placement probe has no surviving cargo stage")
	}
	return operatorProbe{
			engine: engine, incumbent: incumbent,
			move: placementMove{
				duty: 0, trip: 0, stage: stage,
				cargoID: problem.Cargo[0].ID, orientation: domain.OrientationLHW,
			},
		}, operatorProbe{
			engine: engine, incumbent: incumbent,
			move: placementMove{
				duty: 0, trip: 0, stage: stage,
				cargoID: problem.Cargo[0].ID, orientation: domain.OrientationWLH,
			},
		}
}

func chargingOperatorProblem(t testing.TB) domain.ProblemSnapshot {
	t.Helper()
	problem := solverProblem(t)
	problem.Vehicles[0].Energy = domain.EnergySpec{
		Kind:               domain.EnergyElectric,
		MatrixProfileID:    "ev-main",
		BatteryCapacityWh:  50_000,
		InitialSOCWh:       5_000,
		ReserveSOCWh:       2_000,
		ConnectorTypes:     []string{"ccs2"},
		ConsumptionWhPerKM: 1_000,
		ChargingCurve: []domain.ChargingBand{{
			FromSOCPPM: 0,
			ToSOCPPM:   1_000_000,
			PowerW:     50_000,
		}},
	}
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
		{
			ID:             "charger-bad",
			LocationID:     "depot-1",
			ConnectorTypes: []string{"ccs2"},
			Availability: []domain.TimeRange{{
				Start: problem.Horizon.End.Add(time.Hour),
				End:   problem.Horizon.End.Add(2 * time.Hour),
			}},
			Capacity:  2,
			MaxPowerW: 50_000,
		},
	}
	problem.Energy.Profiles = []domain.EnergyProfileMatrix{{
		ProfileID: "ev-main",
		BaseWh: []int64{
			0, 8_000, 10_000,
			8_000, 0, 12_000,
			10_000, 12_000, 0,
		},
		LoadWhPerTonne: make([]int64, 9),
	}}
	return rebuildProblem(t, problem)
}

func solvedCertifiedState(
	t testing.TB,
	problem domain.ProblemSnapshot,
) (*engine, certifiedState) {
	t.Helper()
	result, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		solveConfig(problem),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Validation.Valid {
		t.Fatalf("fixture solve violations = %+v", result.Validation.Violations)
	}
	engine := newTestEngine(t, problem)
	return engine, certifyPlan(t, engine, result.Plan)
}

func certifyPlan(
	t testing.TB,
	engine *engine,
	plan domain.Plan,
) certifiedState {
	t.Helper()
	evaluation := engine.evaluateMaterializedPlan(plan)
	feasible, ok := evaluation.(feasibleCandidate)
	if !ok {
		t.Fatalf("fixture plan evaluation = %#v", evaluation)
	}
	certified, report, err := engine.certifyCandidate(feasible.state)
	if err != nil {
		t.Fatalf("fixture certification: %v, violations=%+v", err, report.Violations)
	}
	return certified
}

type taskPosition struct {
	duty     int
	trip     int
	position int
}

func locateTask(
	t testing.TB,
	plan domain.Plan,
	taskID domain.TaskID,
) taskPosition {
	t.Helper()
	for dutyIndex, duty := range plan.Duties {
		for tripIndex, trip := range duty.Trips {
			for position, current := range tripTaskIDs(trip) {
				if current == taskID {
					return taskPosition{
						duty: dutyIndex, trip: tripIndex, position: position,
					}
				}
			}
		}
	}
	t.Fatalf("task %q is absent from plan", taskID)
	return taskPosition{}
}
