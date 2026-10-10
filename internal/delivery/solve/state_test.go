package solve

import (
	"slices"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestCandidateStateKeyNormalizesEnvelopeAndUnorderedCollections(t *testing.T) {
	plan := stateKeyTestPlan()
	first, err := candidateStateFromPlan(plan)
	if err != nil {
		t.Fatal(err)
	}

	equivalent := stateKeyTestPlan()
	equivalent.PlanID = "another-plan"
	equivalent.RevisionID = "another-revision"
	equivalent.ProblemDigest = digestOf('b')
	equivalent.PolicyDigest = digestOf('c')
	equivalent.CommitmentDigest = digestOf('d')
	equivalent.ConfigDigest = digestOf('e')
	equivalent.PlanDigest = digestOf('f')
	equivalent.Solver = domain.SolverIdentity{Name: "other", Version: "9", Build: "other"}
	equivalent.Duties = append([]domain.VehicleDuty(nil), plan.Duties...)
	slices.Reverse(equivalent.Duties)
	equivalent.Unassigned = append([]domain.UnassignedUnit(nil), plan.Unassigned...)
	slices.Reverse(equivalent.Unassigned)
	equivalent.Duties[0].DriverIDs = []domain.DriverID{"driver-b", "driver-a"}
	slices.Reverse(equivalent.Duties[1].Trips[0].LoadStages[0].Placements)
	slices.Reverse(equivalent.Duties[1].Trips[0].LoadStages[0].Rehandles)
	shiftPlanTimesToEquivalentZone(&equivalent, time.FixedZone("CST", 8*60*60))

	second, err := candidateStateFromPlan(equivalent)
	if err != nil {
		t.Fatal(err)
	}
	if first.stateKey != second.stateKey {
		t.Fatalf("equivalent state keys differ: %q != %q", first.stateKey, second.stateKey)
	}
	if plan.Duties[0].VehicleID != "vehicle-b" {
		t.Fatalf("candidateStateFromPlan mutated source duties: %+v", plan.Duties)
	}
}

func TestCandidateStateKeyCoversMaterializedPlanBody(t *testing.T) {
	basePlan := stateKeyTestPlan()
	base, err := candidateStateFromPlan(basePlan)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*domain.Plan)
	}{
		{
			name: "route order",
			mutate: func(plan *domain.Plan) {
				slices.Reverse(plan.Duties[0].Trips[0].Stops)
			},
		},
		{
			name: "trip boundary",
			mutate: func(plan *domain.Plan) {
				trip := plan.Duties[0].Trips[0]
				plan.Duties[0].Trips = []domain.Trip{trip, trip}
				plan.Duties[0].Trips[1].ID = "trip-2"
			},
		},
		{
			name: "depot",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].EndDepotID = "depot-2"
			},
		},
		{
			name: "vehicle",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].VehicleID = "vehicle-c"
			},
		},
		{
			name: "driver",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].DriverIDs[0] = "driver-c"
			},
		},
		{
			name: "timestamp",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].Stops[0].ServiceAt =
					plan.Duties[0].Trips[0].Stops[0].ServiceAt.Add(time.Second)
			},
		},
		{
			name: "break",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].Schedule[0].Kind = domain.SegmentBreak
			},
		},
		{
			name: "charge",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].Schedule[0].ChargedWh++
			},
		},
		{
			name: "state of charge",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].Energy[0].EndSOCWh++
			},
		},
		{
			name: "placement",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].LoadStages[0].Placements[0].PositionMM.X++
			},
		},
		{
			name: "rehandle",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].LoadStages[0].Rehandles =
					append(plan.Duties[0].Trips[0].LoadStages[0].Rehandles,
						domain.RehandleOperation{
							Sequence:        3,
							CargoID:         "cargo-c",
							StopIndex:       1,
							DurationSeconds: 30,
							CostCents:       40,
						},
					)
			},
		},
		{
			name: "metrics",
			mutate: func(plan *domain.Plan) {
				plan.Metrics.TotalCostCents++
			},
		},
		{
			name: "objective",
			mutate: func(plan *domain.Plan) {
				plan.Objective.TotalCostCents++
			},
		},
		{
			name: "unassigned",
			mutate: func(plan *domain.Plan) {
				plan.Unassigned[0].Reason = domain.UnassignedLoading
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changedPlan := stateKeyTestPlan()
			test.mutate(&changedPlan)
			changed, buildErr := candidateStateFromPlan(changedPlan)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			if changed.stateKey == base.stateKey {
				t.Fatalf("state key did not change after %s mutation", test.name)
			}
		})
	}
}

func stateKeyTestPlan() domain.Plan {
	start := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	trip := domain.Trip{
		ID:           "trip-1",
		StartDepotID: "depot-1",
		EndDepotID:   "depot-1",
		StartAt:      start,
		EndAt:        start.Add(time.Hour),
		Stops: []domain.Stop{
			{
				LocationID:  "depot-location",
				TaskIDs:     []domain.TaskID{"pickup"},
				ArrivalAt:   start,
				ServiceAt:   start,
				DepartureAt: start.Add(time.Minute),
			},
			{
				LocationID:  "customer",
				TaskIDs:     []domain.TaskID{"delivery"},
				ArrivalAt:   start.Add(30 * time.Minute),
				ServiceAt:   start.Add(30 * time.Minute),
				DepartureAt: start.Add(31 * time.Minute),
			},
		},
		Schedule: []domain.DutySegment{{
			Kind:      domain.SegmentCharge,
			DriverID:  "driver-a",
			From:      "depot-location",
			To:        "customer",
			StartAt:   start,
			EndAt:     start.Add(30 * time.Minute),
			TaskIDs:   []domain.TaskID{"pickup"},
			ChargerID: "charger-1",
			ChargedWh: 1_000,
		}},
		Energy: []domain.EnergyLeg{{
			FromStopIndex: 0,
			ToStopIndex:   1,
			StartSOCWh:    10_000,
			ConsumedWh:    2_000,
			ChargedWh:     1_000,
			EndSOCWh:      9_000,
			ChargerID:     "charger-1",
		}},
		LoadStages: []domain.LoadStage{{
			AfterStopIndex: 0,
			Placements: []domain.Placement{
				{
					CargoID:        "cargo-a",
					CompartmentID:  "box",
					PositionMM:     domain.Point3{X: 1, Y: 2, Z: 3},
					SizeMM:         domain.Box{Length: 4, Width: 5, Height: 6},
					Orientation:    domain.OrientationLWH,
					LoadAtTaskID:   "pickup",
					UnloadAtTaskID: "delivery",
					DoorID:         "rear",
				},
				{
					CargoID:        "cargo-b",
					CompartmentID:  "box",
					PositionMM:     domain.Point3{X: 10, Y: 20, Z: 30},
					SizeMM:         domain.Box{Length: 40, Width: 50, Height: 60},
					Orientation:    domain.OrientationWLH,
					LoadAtTaskID:   "pickup",
					UnloadAtTaskID: "delivery",
					DoorID:         "rear",
				},
			},
			AxleLoadsG:     []int64{100, 200},
			CenterOfMassMM: domain.Point3{X: 7, Y: 8, Z: 9},
			Rehandles: []domain.RehandleOperation{
				{
					Sequence:        1,
					CargoID:         "cargo-a",
					StopIndex:       1,
					Before:          domain.Placement{CargoID: "cargo-a"},
					After:           domain.Placement{CargoID: "cargo-a"},
					DurationSeconds: 30,
					CostCents:       40,
				},
				{
					Sequence:        2,
					CargoID:         "cargo-b",
					StopIndex:       1,
					Before:          domain.Placement{CargoID: "cargo-b"},
					After:           domain.Placement{CargoID: "cargo-b"},
					DurationSeconds: 30,
					CostCents:       40,
				},
			},
		}},
	}
	return domain.Plan{
		SchemaVersion:    domain.PlanSchemaVersion,
		PlanID:           "plan-1",
		RevisionID:       "revision-1",
		ProblemDigest:    digestOf('1'),
		PolicyDigest:     digestOf('2'),
		CommitmentDigest: digestOf('3'),
		Solver:           domain.SolverIdentity{Name: "builtin", Version: "1", Build: "test"},
		ConfigDigest:     digestOf('4'),
		Duties: []domain.VehicleDuty{
			{VehicleID: "vehicle-b", DriverIDs: []domain.DriverID{"driver-a"}, Trips: []domain.Trip{trip}},
			{VehicleID: "vehicle-a", DriverIDs: []domain.DriverID{"driver-a", "driver-b"}, Trips: []domain.Trip{trip}},
		},
		Unassigned: []domain.UnassignedUnit{
			{UnitID: "unit-b", Reason: domain.UnassignedCapacity, Detail: "full"},
			{UnitID: "unit-a", Reason: domain.UnassignedEnergy, Detail: "reserve"},
		},
		Objective: domain.ObjectiveVector{
			VehiclesUsed:         2,
			TotalCostCents:       100,
			TotalDistanceMeters:  1_000,
			TotalWaitSeconds:     10,
			NegativeMinVolumePPM: -500_000,
			StabilityCostCents:   5,
		},
		Metrics: domain.PlanMetrics{
			AssignedUnits:       2,
			UnassignedUnits:     2,
			VehiclesUsed:        2,
			Trips:               2,
			Stops:               4,
			TotalDistanceMeters: 1_000,
			TotalCostCents:      100,
		},
		PlanDigest: digestOf('5'),
	}
}

func shiftPlanTimesToEquivalentZone(plan *domain.Plan, location *time.Location) {
	for dutyIndex := range plan.Duties {
		for tripIndex := range plan.Duties[dutyIndex].Trips {
			trip := &plan.Duties[dutyIndex].Trips[tripIndex]
			trip.StartAt = trip.StartAt.In(location)
			trip.EndAt = trip.EndAt.In(location)
			for stopIndex := range trip.Stops {
				stop := &trip.Stops[stopIndex]
				stop.ArrivalAt = stop.ArrivalAt.In(location)
				stop.ServiceAt = stop.ServiceAt.In(location)
				stop.DepartureAt = stop.DepartureAt.In(location)
			}
			for segmentIndex := range trip.Schedule {
				segment := &trip.Schedule[segmentIndex]
				segment.StartAt = segment.StartAt.In(location)
				segment.EndAt = segment.EndAt.In(location)
			}
		}
	}
}

func digestOf(value byte) domain.ArtifactDigest {
	bytes := make([]byte, 64)
	for index := range bytes {
		bytes[index] = value
	}
	return domain.ArtifactDigest(bytes)
}
