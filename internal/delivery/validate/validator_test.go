package validate

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
)

func TestValidatorAcceptsCompletePlanAndProducesStableReport(t *testing.T) {
	problem, plan, at := validCase(t)
	validator := New(domain.ValidatorIdentity{Name: "independent", Version: "1.0.0", Build: "test"})

	first := validator.Validate(problem, plan, at)
	second := validator.Validate(problem, plan, at)
	if !first.Valid || len(first.Violations) != 0 {
		t.Fatalf("valid plan rejected: %+v", first.Violations)
	}
	if first.Metrics != plan.Metrics {
		t.Fatalf("recomputed metrics = %+v, want %+v", first.Metrics, plan.Metrics)
	}
	if first.ReportDigest == "" || first.ReportDigest != second.ReportDigest {
		t.Fatalf("report digest is unstable: %q != %q", first.ReportDigest, second.ReportDigest)
	}
}

func TestValidatorGoldenReport(t *testing.T) {
	problem, plan, at := validCase(t)
	report := New(domain.ValidatorIdentity{
		Name: "independent", Version: "1.0.0", Build: "test",
	}).Validate(problem, plan, at)
	raw, err := domain.CanonicalJSON(report)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "valid-report.json")
	if os.Getenv("UPDATE_DELIVERY_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, bytes.TrimSpace(want)) {
		t.Fatalf("validation report differs from golden corpus:\ngot  %s\nwant %s", raw, want)
	}
}

func TestValidatorCoversEveryRuleFamily(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*domain.ProblemSnapshot, *domain.Plan)
	}{
		{
			name: "V0 digest binding",
			code: "V001",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.ProblemDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				sealPlan(t, plan)
			},
		},
		{
			name: "V1 order conservation",
			code: "V101",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.Duties[0].Trips[0].Stops[1].TaskIDs = nil
				sealPlan(t, plan)
			},
		},
		{
			name: "V2 pickup precedence",
			code: "V201",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				trip := &plan.Duties[0].Trips[0]
				trip.Stops[0].TaskIDs, trip.Stops[1].TaskIDs =
					trip.Stops[1].TaskIDs, trip.Stops[0].TaskIDs
				sealPlan(t, plan)
			},
		},
		{
			name: "V3 driver eligibility",
			code: "V302",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.Duties[0].DriverIDs = []domain.DriverID{"driver-unknown"}
				for index := range plan.Duties[0].Trips[0].Schedule {
					plan.Duties[0].Trips[0].Schedule[index].DriverID = "driver-unknown"
				}
				sealPlan(t, plan)
			},
		},
		{
			name: "V4 route continuity",
			code: "V401",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.Duties[0].Trips[0].Stops[1].LocationID = "depot-1"
				sealPlan(t, plan)
			},
		},
		{
			name: "V5 hard time window",
			code: "V501",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				stop := &plan.Duties[0].Trips[0].Stops[1]
				stop.ServiceAt = stop.ServiceAt.Add(5 * time.Hour)
				stop.DepartureAt = stop.DepartureAt.Add(5 * time.Hour)
				sealPlan(t, plan)
			},
		},
		{
			name: "V6 EV reserve",
			code: "V601",
			mutate: func(problem *domain.ProblemSnapshot, plan *domain.Plan) {
				problem.Vehicles[0].Energy = domain.EnergySpec{
					Kind:               domain.EnergyElectric,
					MatrixProfileID:    "ev-main",
					BatteryCapacityWh:  20_000,
					InitialSOCWh:       10_000,
					ReserveSOCWh:       2_000,
					ConnectorTypes:     []string{"ccs2"},
					ConsumptionWhPerKM: 1_000,
					ChargingCurve:      []domain.ChargingBand{{FromSOCPPM: 0, ToSOCPPM: 1_000_000, PowerW: 50_000}},
				}
				problem.Energy.Profiles = []domain.EnergyProfileMatrix{{
					ProfileID:      "ev-main",
					BaseWh:         []int64{0, 9_000, 9_000, 0},
					LoadWhPerTonne: []int64{0, 0, 0, 0},
				}}
				rebuildProblemAndBind(t, problem, plan)
				plan.Duties[0].Trips[0].Energy = []domain.EnergyLeg{
					{FromStopIndex: 0, ToStopIndex: 1, StartSOCWh: 10_000, ConsumedWh: 9_000, EndSOCWh: 1_000},
					{FromStopIndex: 1, ToStopIndex: 2, StartSOCWh: 1_000, ConsumedWh: 9_000, EndSOCWh: -8_000},
				}
				sealPlan(t, plan)
			},
		},
		{
			name: "V7 compartment boundary",
			code: "V701",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.Duties[0].Trips[0].LoadStages[0].Placements[0].PositionMM.X = 3_500
				sealPlan(t, plan)
			},
		},
		{
			name: "V8 support",
			code: "V801",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.Duties[0].Trips[0].LoadStages[0].Placements[0].PositionMM.Z = 100
				sealPlan(t, plan)
			},
		},
		{
			name: "V9 extraction door",
			code: "V901",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.Duties[0].Trips[0].LoadStages[0].Placements[0].DoorID = "door-unknown"
				sealPlan(t, plan)
			},
		},
		{
			name: "V10 payload",
			code: "V1001",
			mutate: func(problem *domain.ProblemSnapshot, plan *domain.Plan) {
				problem.Vehicles[0].Compartments[0].MaxPayloadG = 50_000
				rebuildProblemAndBind(t, problem, plan)
			},
		},
		{
			name: "V11 frozen commitment",
			code: "V1101",
			mutate: func(problem *domain.ProblemSnapshot, plan *domain.Plan) {
				problem.Commitments.Frozen = []domain.FrozenTaskCommitment{{
					TaskID:            "delivery-1",
					VehicleID:         "vehicle-1",
					DriverID:          "driver-1",
					Sequence:          1,
					PromisedServiceAt: plan.Duties[0].Trips[0].Stops[1].ServiceAt,
					ToleranceSeconds:  300,
				}}
				rebuildProblemAndBind(t, problem, plan)
				plan.Duties[0].VehicleID = "vehicle-other"
				sealPlan(t, plan)
			},
		},
		{
			name: "V12 metrics",
			code: "V1201",
			mutate: func(_ *domain.ProblemSnapshot, plan *domain.Plan) {
				plan.Metrics.TotalDistanceMeters++
				sealPlan(t, plan)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problem, plan, at := validCase(t)
			test.mutate(&problem, &plan)
			report := New(domain.ValidatorIdentity{
				Name: "independent", Version: "1.0.0", Build: "test",
			}).Validate(problem, plan, at)
			if report.Valid {
				t.Fatalf("invalid plan was accepted")
			}
			codes := make([]string, 0, len(report.Violations))
			for _, violation := range report.Violations {
				codes = append(codes, violation.Code)
			}
			if !slices.Contains(codes, test.code) {
				t.Fatalf("violation codes = %v, want %s", codes, test.code)
			}
		})
	}
}

func TestValidatorDoesNotMutateProblemOrPlan(t *testing.T) {
	problem, plan, at := validCase(t)
	problemDigest := problem.ProblemDigest
	planDigest := plan.PlanDigest
	placement := plan.Duties[0].Trips[0].LoadStages[0].Placements[0]

	_ = New(domain.ValidatorIdentity{Name: "independent", Version: "1.0.0"}).
		Validate(problem, plan, at)

	if problem.ProblemDigest != problemDigest || plan.PlanDigest != planDigest ||
		plan.Duties[0].Trips[0].LoadStages[0].Placements[0] != placement {
		t.Fatal("validator mutated caller-owned input")
	}
}

func TestValidatorRejectsScheduleAndLoadReferenceBypasses(t *testing.T) {
	tests := []struct {
		name   string
		code   string
		mutate func(*domain.Plan)
	}{
		{
			name: "empty schedule",
			code: "V504",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].Schedule = nil
			},
		},
		{
			name: "duplicate load stage",
			code: "V903",
			mutate: func(plan *domain.Plan) {
				trip := &plan.Duties[0].Trips[0]
				trip.LoadStages = append(trip.LoadStages, trip.LoadStages[0])
			},
		},
		{
			name: "unknown load task",
			code: "V904",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].LoadStages[0].
					Placements[0].LoadAtTaskID = "task-unknown"
			},
		},
		{
			name: "unknown unload task",
			code: "V904",
			mutate: func(plan *domain.Plan) {
				plan.Duties[0].Trips[0].LoadStages[0].
					Placements[0].UnloadAtTaskID = "task-unknown"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problem, plan, at := validCase(t)
			test.mutate(&plan)
			sealPlan(t, &plan)
			report := New(domain.ValidatorIdentity{
				Name: "independent", Version: "1.0.0", Build: "test",
			}).Validate(problem, plan, at)
			if report.Valid || !hasViolationCode(report, test.code) {
				t.Fatalf("report valid=%v codes=%v, want %s",
					report.Valid, violationCodes(report), test.code)
			}
		})
	}
}

func TestValidatorIncludesUnassignedPenaltyInRecomputedCost(t *testing.T) {
	problem, plan, at := validCase(t)
	problem.Requests[0].Required = false
	problem.Requests[0].UnitIDs = append(
		problem.Requests[0].UnitIDs,
		"unit-2",
	)
	problem.Units = append(problem.Units, domain.FulfillmentUnit{
		ID:        "unit-2",
		RequestID: "request-1",
		CargoIDs:  []domain.CargoID{},
		Quantity:  1,
	})
	rebuildProblemAndBind(t, &problem, &plan)
	plan.Unassigned = []domain.UnassignedUnit{
		{UnitID: "unit-1", Reason: domain.UnassignedCapacity},
		{UnitID: "unit-2", Reason: domain.UnassignedCapacity},
	}
	sealPlan(t, &plan)

	report := New(domain.ValidatorIdentity{
		Name: "independent", Version: "1.0.0", Build: "test",
	}).Validate(problem, plan, at)
	const wantCost = int64(110_333)
	if report.Metrics.TotalCostCents != wantCost {
		t.Fatalf("recomputed cost = %d, want %d", report.Metrics.TotalCostCents, wantCost)
	}
}

func hasViolationCode(report domain.ValidationReport, code string) bool {
	return slices.Contains(violationCodes(report), code)
}

func violationCodes(report domain.ValidationReport) []string {
	result := make([]string, 0, len(report.Violations))
	for _, violation := range report.Violations {
		result = append(result, violation.Code)
	}
	return result
}

type testHelper interface {
	Helper()
	Fatal(args ...any)
}

func validCase(t testHelper) (domain.ProblemSnapshot, domain.Plan, time.Time) {
	t.Helper()
	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	problem, err := service.BuildProblemSnapshot(validationProblemDraft(base))
	if err != nil {
		t.Fatal(err)
	}
	plan := domain.Plan{
		SchemaVersion:    domain.PlanSchemaVersion,
		PlanID:           "plan-1",
		RevisionID:       "revision-1",
		ProblemDigest:    problem.ProblemDigest,
		PolicyDigest:     problem.PolicyDigest,
		CommitmentDigest: problem.CommitmentDigest,
		Solver:           domain.SolverIdentity{Name: "builtin", Version: "1.0.0", Build: "test"},
		ConfigDigest:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Duties: []domain.VehicleDuty{{
			VehicleID: "vehicle-1",
			DriverIDs: []domain.DriverID{"driver-1"},
			Trips: []domain.Trip{{
				ID:           "trip-1",
				StartDepotID: "depot-1",
				EndDepotID:   "depot-1",
				StartAt:      base,
				EndAt:        base.Add(100 * time.Minute),
				Stops: []domain.Stop{
					{
						LocationID:  "depot-1",
						TaskIDs:     []domain.TaskID{"pickup-1"},
						ArrivalAt:   base,
						ServiceAt:   base,
						DepartureAt: base.Add(5 * time.Minute),
					},
					{
						LocationID:  "customer-1",
						TaskIDs:     []domain.TaskID{"delivery-1"},
						ArrivalAt:   base.Add(35 * time.Minute),
						ServiceAt:   base.Add(time.Hour),
						DepartureAt: base.Add(70 * time.Minute),
					},
					{
						LocationID:  "depot-1",
						TaskIDs:     []domain.TaskID{},
						ArrivalAt:   base.Add(100 * time.Minute),
						ServiceAt:   base.Add(100 * time.Minute),
						DepartureAt: base.Add(100 * time.Minute),
					},
				},
				Schedule: []domain.DutySegment{
					{Kind: domain.SegmentService, DriverID: "driver-1", From: "depot-1", To: "depot-1", StartAt: base, EndAt: base.Add(5 * time.Minute), TaskIDs: []domain.TaskID{"pickup-1"}},
					{Kind: domain.SegmentDrive, DriverID: "driver-1", From: "depot-1", To: "customer-1", StartAt: base.Add(5 * time.Minute), EndAt: base.Add(35 * time.Minute), TaskIDs: []domain.TaskID{}},
					{Kind: domain.SegmentWait, DriverID: "driver-1", From: "customer-1", To: "customer-1", StartAt: base.Add(35 * time.Minute), EndAt: base.Add(time.Hour), TaskIDs: []domain.TaskID{}},
					{Kind: domain.SegmentService, DriverID: "driver-1", From: "customer-1", To: "customer-1", StartAt: base.Add(time.Hour), EndAt: base.Add(70 * time.Minute), TaskIDs: []domain.TaskID{"delivery-1"}},
					{Kind: domain.SegmentDrive, DriverID: "driver-1", From: "customer-1", To: "depot-1", StartAt: base.Add(70 * time.Minute), EndAt: base.Add(100 * time.Minute), TaskIDs: []domain.TaskID{}},
				},
				Energy: []domain.EnergyLeg{},
				LoadStages: []domain.LoadStage{
					{
						AfterStopIndex: 0,
						Placements: []domain.Placement{{
							CargoID:        "cargo-1",
							CompartmentID:  "compartment-1",
							PositionMM:     domain.Point3{X: 1_500},
							SizeMM:         domain.Box{Length: 1_000, Width: 800, Height: 600},
							Orientation:    domain.OrientationLWH,
							LoadAtTaskID:   "pickup-1",
							UnloadAtTaskID: "delivery-1",
							DoorID:         "door-1",
						}},
						AxleLoadsG:     []int64{50_000, 50_000},
						CenterOfMassMM: domain.Point3{X: 2_000, Y: 400, Z: 300},
						RehandledCargo: []domain.CargoID{},
					},
					{AfterStopIndex: 1, Placements: []domain.Placement{}, AxleLoadsG: []int64{0, 0}, RehandledCargo: []domain.CargoID{}},
					{AfterStopIndex: 2, Placements: []domain.Placement{}, AxleLoadsG: []int64{0, 0}, RehandledCargo: []domain.CargoID{}},
				},
			}},
		}},
		Unassigned: []domain.UnassignedUnit{},
		Metrics: domain.PlanMetrics{
			AssignedUnits:             1,
			UnassignedUnits:           0,
			VehiclesUsed:              1,
			Trips:                     1,
			Stops:                     3,
			TotalDistanceMeters:       20_000,
			TotalDriveSeconds:         3_600,
			TotalServiceSeconds:       900,
			TotalWaitSeconds:          1_500,
			TotalEnergyWh:             20_000,
			TotalCostCents:            10_333,
			OnTimeTasks:               2,
			LateTasks:                 0,
			OnTimeRatePPM:             1_000_000,
			MinVolumeUtilizationPPM:   30_000,
			MeanVolumeUtilizationPPM:  30_000,
			MeanPayloadUtilizationPPM: 100_000,
			MaxPayloadUtilizationPPM:  100_000,
		},
		Objective: domain.ObjectiveVector{
			VehiclesUsed:         1,
			TotalCostCents:       10_333,
			TotalDistanceMeters:  20_000,
			TotalWaitSeconds:     1_500,
			NegativeMinVolumePPM: -30_000,
		},
	}
	sealPlan(t, &plan)
	return problem, plan, base.Add(-time.Minute)
}

func rebuildProblemAndBind(
	t testHelper,
	problem *domain.ProblemSnapshot,
	plan *domain.Plan,
) {
	t.Helper()
	problem.ProblemDigest = ""
	problem.PolicyDigest = ""
	problem.CommitmentDigest = ""
	rebuilt, err := service.BuildProblemSnapshot(*problem)
	if err != nil {
		t.Fatal(err)
	}
	*problem = rebuilt
	plan.ProblemDigest = rebuilt.ProblemDigest
	plan.PolicyDigest = rebuilt.PolicyDigest
	plan.CommitmentDigest = rebuilt.CommitmentDigest
	sealPlan(t, plan)
}

func sealPlan(t testHelper, plan *domain.Plan) {
	t.Helper()
	digest, err := domain.ComputePlanDigest(*plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.PlanDigest = digest
}
