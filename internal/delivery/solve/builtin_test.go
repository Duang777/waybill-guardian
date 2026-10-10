package solve

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

func TestBuiltinProducesValidatorAcceptedRouteAndLoadPlan(t *testing.T) {
	problem := solverProblem(t)
	solver := newTestBuiltin(t)
	var progress []Progress
	result, err := solver.Solve(
		context.Background(),
		problem,
		solveConfig(problem),
		ProgressSinkFunc(func(_ context.Context, value Progress) error {
			progress = append(progress, value)
			return nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SolveCompleted || !result.Validation.Valid {
		t.Fatalf("solve status = %q, violations = %+v", result.Status, result.Validation.Violations)
	}
	if result.Plan.PlanDigest !=
		"24ccadbbd50af9f01abc242ddee472719aa88973a329b6854557caf04d383a86" {
		t.Fatalf("plan digest = %q, want stable solved artifact", result.Plan.PlanDigest)
	}
	if len(result.Plan.Duties) != 1 || len(result.Plan.Duties[0].Trips) != 1 {
		t.Fatalf("duties = %+v, want one complete trip", result.Plan.Duties)
	}
	trip := result.Plan.Duties[0].Trips[0]
	gotTasks := make([]domain.TaskID, 0)
	for _, stop := range trip.Stops {
		gotTasks = append(gotTasks, stop.TaskIDs...)
	}
	wantTasks := []domain.TaskID{"pickup-1", "pickup-2", "delivery-1", "delivery-2"}
	if !slices.Equal(gotTasks, wantTasks) {
		t.Fatalf("route tasks = %v, want %v", gotTasks, wantTasks)
	}
	if len(trip.LoadStages) != 5 {
		t.Fatalf("load stages = %d, want 5", len(trip.LoadStages))
	}
	loaded := trip.LoadStages[1].Placements
	if len(loaded) != 2 ||
		loaded[0].CargoID != "cargo-1" ||
		loaded[0].PositionMM != (domain.Point3{X: 0, Y: 800, Z: 0}) ||
		loaded[1].CargoID != "cargo-2" ||
		loaded[1].PositionMM != (domain.Point3{X: 0, Y: 0, Z: 0}) {
		t.Fatalf("stable extraction layout = %+v", loaded)
	}
	if result.Plan.Metrics.AssignedUnits != 2 ||
		result.Plan.Metrics.TotalDistanceMeters != 30_000 ||
		result.Plan.Metrics.Rehandles != 0 {
		t.Fatalf("plan metrics = %+v", result.Plan.Metrics)
	}
	if result.Evidence.SchemaVersion != SolveEvidenceVersion ||
		result.Evidence.Termination != TerminationLocalOptimum ||
		result.Evidence.BudgetLimit != solveConfig(problem).EvaluationBudget ||
		result.Evidence.Evaluations == 0 ||
		result.Evidence.Evaluations <=
			EvaluationBudget(result.Evidence.ConstructionAttempts) ||
		result.Evidence.IncumbentStateKey == "" {
		t.Fatalf("solve evidence = %+v", result.Evidence)
	}
	if len(result.Evidence.AcceptedStateKeys) == 0 ||
		result.Evidence.AcceptedStateKeys[len(result.Evidence.AcceptedStateKeys)-1] !=
			result.Evidence.IncumbentStateKey {
		t.Fatalf("accepted state sequence = %v, incumbent = %q",
			result.Evidence.AcceptedStateKeys, result.Evidence.IncumbentStateKey)
	}
	wantOperators := []OperatorID{
		"relocate",
		"swap",
		"2-opt",
		"cross-exchange",
		"trip-split",
		"trip-merge",
		"depot",
		"vehicle",
		"driver",
		"break",
		"charge",
		"placement",
	}
	gotOperators := make([]OperatorID, 0, len(result.Evidence.OperatorEvaluations))
	for _, evaluation := range result.Evidence.OperatorEvaluations {
		gotOperators = append(gotOperators, evaluation.Operator)
		if evaluation.Attempts != evaluation.Feasible+evaluation.Rejected {
			t.Fatalf("operator accounting is not conserved: %+v", evaluation)
		}
	}
	if !slices.Equal(gotOperators, wantOperators) {
		t.Fatalf("evidence operators = %v, want %v", gotOperators, wantOperators)
	}
	if result.Evidence.OperatorEvaluations[0].Attempts == 0 {
		t.Fatal("local search did not evaluate relocate moves")
	}
	gotPhases := make([]ProgressPhase, 0, len(progress))
	for _, value := range progress {
		gotPhases = append(gotPhases, value.Phase)
	}
	if !slices.Equal(gotPhases, []ProgressPhase{
		ProgressBaseline,
		ProgressRouting,
		ProgressImproving,
		ProgressValidating,
	}) {
		t.Fatalf("progress phases = %v", gotPhases)
	}
}

func TestBuiltinReplayIsIndependentOfOutputEnvelope(t *testing.T) {
	problem := solverProblem(t)
	solver := newTestBuiltin(t)
	firstConfig := solveConfig(problem)
	first, err := solver.Solve(context.Background(), problem, firstConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondConfig := firstConfig
	secondConfig.PlanID = "plan-2"
	secondConfig.RevisionID = "revision-2"
	secondConfig.ValidationAt = secondConfig.ValidationAt.Add(time.Hour)
	second, err := solver.Solve(context.Background(), problem, secondConfig, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Plan.PlanDigest != second.Plan.PlanDigest {
		t.Fatalf("replayed plan digests differ: %q != %q",
			first.Plan.PlanDigest, second.Plan.PlanDigest)
	}
	if first.Evidence.EvidenceDigest != second.Evidence.EvidenceDigest {
		t.Fatalf("replayed evidence digests differ: %q != %q",
			first.Evidence.EvidenceDigest, second.Evidence.EvidenceDigest)
	}
}

func TestBuiltinBuildsValidatedEVChargingPlan(t *testing.T) {
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
	problem.Chargers = []domain.ChargingStation{{
		ID:             "charger-depot",
		LocationID:     "depot-1",
		ConnectorTypes: []string{"ccs2"},
		Availability:   []domain.TimeRange{problem.Horizon},
		Capacity:       2,
		MaxPowerW:      50_000,
	}}
	problem.Energy.Profiles = []domain.EnergyProfileMatrix{{
		ProfileID: "ev-main",
		BaseWh: []int64{
			0, 8_000, 10_000,
			8_000, 0, 12_000,
			10_000, 12_000, 0,
		},
		LoadWhPerTonne: []int64{
			0, 0, 0,
			0, 0, 0,
			0, 0, 0,
		},
	}}
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	problem, err := service.BuildProblemSnapshot(problem)
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
	if result.Status != SolveCompleted || !result.Validation.Valid {
		t.Fatalf("EV solve status = %q, violations = %+v",
			result.Status, result.Validation.Violations)
	}
	trip := result.Plan.Duties[0].Trips[0]
	var chargedWh int64
	var chargeSegments int
	for _, segment := range trip.Schedule {
		if segment.Kind == domain.SegmentCharge {
			chargeSegments++
			chargedWh += segment.ChargedWh
		}
	}
	if chargeSegments != 1 || chargedWh != 27_000 {
		t.Fatalf("charging plan has %d sessions and %d Wh, want 1 and 27000",
			chargeSegments, chargedWh)
	}
	if trip.Energy[len(trip.Energy)-1].EndSOCWh != 2_000 {
		t.Fatalf("final SOC = %d, want reserve 2000",
			trip.Energy[len(trip.Energy)-1].EndSOCWh)
	}
}

func TestBuiltinCarriesEVSOCBetweenTrips(t *testing.T) {
	problem := solverProblem(t)
	problem.Requests[0].Split = domain.SplitPolicy{
		Mode:             domain.SplitByUnit,
		MinUnitsPerSplit: 1,
		MaxSplits:        2,
		SameVehicle:      true,
	}
	problem.Units[0].AtomicGroupID = "atomic-1"
	problem.Units[1].AtomicGroupID = "atomic-2"
	problem.Cargo[0].SizeMM = domain.Box{Length: 2_500, Width: 1_500, Height: 1_000}
	problem.Cargo[1].SizeMM = domain.Box{Length: 2_500, Width: 1_500, Height: 1_000}
	problem.Vehicles[0].Energy = domain.EnergySpec{
		Kind:              domain.EnergyElectric,
		MatrixProfileID:   "ev-main",
		BatteryCapacityWh: 30_000,
		InitialSOCWh:      30_000,
		ReserveSOCWh:      2_000,
		ConnectorTypes:    []string{"ccs2"},
		ChargingCurve: []domain.ChargingBand{{
			FromSOCPPM: 0,
			ToSOCPPM:   1_000_000,
			PowerW:     50_000,
		}},
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
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	problem, err := service.BuildProblemSnapshot(problem)
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
	if result.Status != SolveInfeasible {
		t.Fatalf("solve status = %q, want infeasible", result.Status)
	}
	if len(result.Plan.Duties) != 1 ||
		len(result.Plan.Duties[0].Trips) != 1 ||
		len(result.Plan.Unassigned) != 1 ||
		result.Plan.Unassigned[0].UnitID != "unit-2" {
		t.Fatalf("cross-trip SOC plan = %+v", result.Plan)
	}
	if got := result.Plan.Duties[0].Trips[0].Energy[1].EndSOCWh; got != 10_000 {
		t.Fatalf("first trip final SOC = %d, want 10000", got)
	}
}

func TestBuiltinSplitsOnlyAtFulfillmentUnitBoundaries(t *testing.T) {
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
	problem.Cargo[0].SizeMM = domain.Box{Length: 2_500, Width: 1_500, Height: 1_000}
	problem.Cargo[1].SizeMM = domain.Box{Length: 2_500, Width: 1_500, Height: 1_000}
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	problem, err := service.BuildProblemSnapshot(problem)
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
	if result.Status != SolveCompleted || !result.Validation.Valid {
		t.Fatalf("split solve status = %q, violations = %+v",
			result.Status, result.Validation.Violations)
	}
	if len(result.Plan.Duties) != 1 || len(result.Plan.Duties[0].Trips) != 2 {
		t.Fatalf("split plan duties = %+v, want two trips on one vehicle", result.Plan.Duties)
	}
	firstTasks := result.Plan.Duties[0].Trips[0].Stops
	secondTasks := result.Plan.Duties[0].Trips[1].Stops
	if !stopsContainOnly(firstTasks, "pickup-1", "delivery-1") ||
		!stopsContainOnly(secondTasks, "pickup-2", "delivery-2") {
		t.Fatalf("split trip tasks = %+v / %+v", firstTasks, secondTasks)
	}
}

func TestBuiltinInsertionMergesCompatibleRequestsIntoOneTrip(t *testing.T) {
	problem := solverProblem(t)
	base := problem.Horizon.Start
	problem.Requests = append(problem.Requests, domain.TransportRequest{
		ID:       "request-2",
		Priority: 90,
		Required: true,
		Tasks: []domain.ServiceTask{
			{
				ID:             "pickup-3",
				Kind:           domain.TaskPickup,
				LocationID:     "depot-1",
				HardWindows:    []domain.TimeRange{{Start: base, End: base.Add(2 * time.Hour)}},
				ServiceSeconds: 60,
				UnitIDs:        []domain.FulfillmentUnitID{"unit-3"},
				RequiredSkills: domain.SkillSet{"cold"},
			},
			{
				ID:             "delivery-3",
				Kind:           domain.TaskDelivery,
				LocationID:     "customer-1",
				HardWindows:    []domain.TimeRange{{Start: base.Add(30 * time.Minute), End: base.Add(5 * time.Hour)}},
				ServiceSeconds: 60,
				PredecessorIDs: []domain.TaskID{"pickup-3"},
				UnitIDs:        []domain.FulfillmentUnitID{"unit-3"},
				RequiredSkills: domain.SkillSet{"cold"},
				MaxRideSeconds: 14_400,
			},
		},
		UnitIDs: []domain.FulfillmentUnitID{"unit-3"},
		Split: domain.SplitPolicy{
			Mode:             domain.SplitForbidden,
			MinUnitsPerSplit: 1,
			MaxSplits:        1,
			SameVehicle:      true,
			SameTrip:         true,
		},
		RequiredSkills: domain.SkillSet{"cold"},
	})
	problem.Units = append(problem.Units, domain.FulfillmentUnit{
		ID:            "unit-3",
		RequestID:     "request-2",
		AtomicGroupID: "atomic-3",
		CargoIDs:      []domain.CargoID{"cargo-3"},
		Quantity:      1,
	})
	problem.Cargo = append(problem.Cargo, domain.CargoItem{
		ID:                  "cargo-3",
		UnitID:              "unit-3",
		SizeMM:              domain.Box{Length: 500, Width: 500, Height: 500},
		WeightG:             50_000,
		AllowedOrientations: []domain.Orientation{domain.OrientationLWH},
		MaxTopLoadG:         0,
		MinSupportPPM:       1_000_000,
		TemperatureZone:     "cold",
		CargoClass:          "food",
		IncompatibleClasses: []string{},
	})
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	problem, err := service.BuildProblemSnapshot(problem)
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
	if result.Status != SolveCompleted || !result.Validation.Valid {
		t.Fatalf("merged solve status = %q, violations = %+v",
			result.Status, result.Validation.Violations)
	}
	if len(result.Plan.Duties) != 1 ||
		len(result.Plan.Duties[0].Trips) != 1 ||
		result.Plan.Metrics.AssignedUnits != 3 {
		t.Fatalf("merged plan = %+v", result.Plan)
	}
	if result.Plan.Metrics.TotalDistanceMeters != 30_000 {
		t.Fatalf("merged distance = %d, want 30000",
			result.Plan.Metrics.TotalDistanceMeters)
	}
}

func TestBuiltinCancellationNeverReturnsIncumbentAsCompleted(t *testing.T) {
	problem := solverProblem(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := newTestBuiltin(t).Solve(ctx, problem, solveConfig(problem), nil)
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("Solve error = %v, want ErrAborted", err)
	}
	if result.Status != SolveAborted || result.Evidence.Status != SolveAborted {
		t.Fatalf("aborted result = %+v", result)
	}
	if result.Evidence.Termination != TerminationCanceled {
		t.Fatalf("aborted termination = %q, want context_canceled",
			result.Evidence.Termination)
	}
	if result.Plan.PlanDigest != "" || result.Validation.Valid {
		t.Fatalf("aborted solve exposed a terminal incumbent: %+v", result)
	}
}

func TestBuiltinBudgetExhaustionDoesNotCommitPartialCandidateRound(t *testing.T) {
	problem := solverProblem(t)
	problem.Requests[0].Split = domain.SplitPolicy{
		Mode:             domain.SplitByUnit,
		MinUnitsPerSplit: 1,
		MaxSplits:        2,
		SameVehicle:      true,
	}
	problem.Units[0].AtomicGroupID = "atomic-1"
	problem.Units[1].AtomicGroupID = "atomic-2"
	secondDriver := problem.Drivers[0]
	secondDriver.ID = "driver-2"
	problem.Drivers = append(problem.Drivers, secondDriver)
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	problem, err := service.BuildProblemSnapshot(problem)
	if err != nil {
		t.Fatal(err)
	}
	config := solveConfig(problem)
	config.EvaluationBudget = 1

	result, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		config,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != SolveExhausted || result.Evidence.Status != SolveExhausted {
		t.Fatalf("solve status = %q, evidence status = %q, want exhausted",
			result.Status, result.Evidence.Status)
	}
	if result.Evidence.Termination != TerminationBudgetExhausted ||
		result.Evidence.BudgetLimit != 1 ||
		result.Evidence.Evaluations != 1 {
		t.Fatalf("budget evidence = %+v", result.Evidence)
	}
	if len(result.Plan.Duties) != 0 {
		t.Fatalf("budget-exhausted solve committed a partial candidate round: %+v",
			result.Plan.Duties)
	}
	if len(result.Plan.Unassigned) != 2 ||
		result.Plan.Unassigned[0].Reason != domain.UnassignedSearchExhausted ||
		result.Plan.Unassigned[1].Reason != domain.UnassignedSearchExhausted {
		t.Fatalf("unassigned = %+v, want both units retained as search_exhausted",
			result.Plan.Unassigned)
	}
}

func TestBuiltinUnsupportedStrategyHasDistinctTerminalStatus(t *testing.T) {
	problem := solverProblem(t)
	config := solveConfig(problem)
	config.Strategy = "unknown-strategy"

	result, err := newTestBuiltin(t).Solve(
		context.Background(),
		problem,
		config,
		nil,
	)
	if !errors.Is(err, ErrCapability) {
		t.Fatalf("Solve error = %v, want ErrCapability", err)
	}
	if result.Status != SolveUnsupported || result.Evidence.Status != SolveUnsupported {
		t.Fatalf("unsupported result = %+v", result)
	}
	if result.Evidence.Termination != TerminationUnsupported {
		t.Fatalf("unsupported termination = %q, want unsupported",
			result.Evidence.Termination)
	}
	if result.Plan.PlanDigest != "" || result.Validation.Valid {
		t.Fatalf("unsupported solve exposed a candidate: %+v", result)
	}
}

func stopsContainOnly(stops []domain.Stop, wanted ...domain.TaskID) bool {
	var actual []domain.TaskID
	for _, stop := range stops {
		actual = append(actual, stop.TaskIDs...)
	}
	return slices.Equal(actual, wanted)
}

func newTestBuiltin(t testing.TB) *Builtin {
	t.Helper()
	solver, err := NewBuiltin(
		domain.SolverIdentity{Name: "builtin", Version: "1.0.0", Build: "test"},
		validate.New(domain.ValidatorIdentity{
			Name: "independent", Version: "1.0.0", Build: "test",
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return solver
}
