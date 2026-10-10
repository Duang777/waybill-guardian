package service

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestBuildProblemSnapshotNormalizesUnorderedFactsAndBindsDigests(t *testing.T) {
	first := validProblemDraft()
	first.Locations[0], first.Locations[1] = first.Locations[1], first.Locations[0]
	first.Vehicles[0].Skills = domain.SkillSet{"liftgate", "cold", "liftgate"}
	first.Requests[0].Tasks[0].RequiredSkills = domain.SkillSet{"cold", "cold"}

	got, err := BuildProblemSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Vehicles[0].Skills, domain.SkillSet{"cold", "liftgate"}) {
		t.Fatalf("vehicle skills = %v", got.Vehicles[0].Skills)
	}
	if !slices.Equal(got.Requests[0].Tasks[0].RequiredSkills, domain.SkillSet{"cold"}) {
		t.Fatalf("task skills = %v", got.Requests[0].Tasks[0].RequiredSkills)
	}
	if got.ProblemDigest == "" || got.PolicyDigest == "" || got.CommitmentDigest == "" {
		t.Fatalf("snapshot has incomplete digests: %+v", got)
	}
	recomputed, err := domain.ComputeProblemDigest(got)
	if err != nil {
		t.Fatal(err)
	}
	if recomputed != got.ProblemDigest {
		t.Fatalf("problem digest = %q, recomputed %q", got.ProblemDigest, recomputed)
	}

	equivalent := validProblemDraft()
	equivalent.Vehicles[0].Skills = domain.SkillSet{"cold", "liftgate"}
	equivalent.Requests[0].Tasks[0].RequiredSkills = domain.SkillSet{"cold"}
	again, err := BuildProblemSnapshot(equivalent)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProblemDigest != again.ProblemDigest {
		t.Fatalf("equivalent problems differ: %q != %q", got.ProblemDigest, again.ProblemDigest)
	}
	if first.Locations[0].ID == got.Locations[0].ID {
		t.Fatal("BuildProblemSnapshot mutated or reused caller-owned ordering")
	}
}

func TestBuildProblemSnapshotPreservesOrderedMatrixMeaning(t *testing.T) {
	first, err := BuildProblemSnapshot(validProblemDraft())
	if err != nil {
		t.Fatal(err)
	}
	changedDraft := validProblemDraft()
	changedDraft.Travel.NodeIDs[0], changedDraft.Travel.NodeIDs[1] =
		changedDraft.Travel.NodeIDs[1], changedDraft.Travel.NodeIDs[0]
	changedDraft.Energy.NodeIDs[0], changedDraft.Energy.NodeIDs[1] =
		changedDraft.Energy.NodeIDs[1], changedDraft.Energy.NodeIDs[0]
	changed, err := BuildProblemSnapshot(changedDraft)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProblemDigest == changed.ProblemDigest {
		t.Fatal("matrix node order did not affect problem digest")
	}
}

func TestBuildProblemSnapshotRejectsBrokenBoundaryData(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.ProblemSnapshot)
		want   string
	}{
		{
			name: "duplicate task",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Requests[0].Tasks[1].ID = value.Requests[0].Tasks[0].ID
			},
			want: "duplicate task",
		},
		{
			name: "unknown cargo unit",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Cargo[0].UnitID = "unit-unknown"
			},
			want: "unknown unit",
		},
		{
			name: "matrix cardinality",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Travel.TravelSeconds = value.Travel.TravelSeconds[:3]
			},
			want: "travel matrix",
		},
		{
			name: "invalid horizon",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Horizon.End = value.Horizon.Start
			},
			want: "horizon",
		},
		{
			name: "unsafe integer",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Vehicles[0].FixedCostCents = 9_007_199_254_740_992
			},
			want: "safe integer",
		},
		{
			name: "cyclic task graph",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Requests[0].Tasks[0].PredecessorIDs = []domain.TaskID{"delivery-1"}
			},
			want: "contains a cycle",
		},
		{
			name: "unit not listed by request",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Requests[0].UnitIDs = nil
			},
			want: "is not listed by request",
		},
		{
			name: "cargo not listed by unit",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Units[0].CargoIDs = nil
			},
			want: "is not listed by owning unit",
		},
		{
			name: "unknown driver end",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Drivers[0].EndLocations = []domain.LocationID{"unknown"}
			},
			want: "unknown end location",
		},
		{
			name: "different energy node order",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Energy.NodeIDs[0], value.Energy.NodeIDs[1] =
					value.Energy.NodeIDs[1], value.Energy.NodeIDs[0]
			},
			want: "same ordered nodes",
		},
		{
			name: "matrix omits location",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Travel.NodeIDs = value.Travel.NodeIDs[:1]
				value.Travel.DistanceMeters = []int64{0}
				value.Travel.TravelSeconds = []int64{0}
				value.Energy.NodeIDs = value.Energy.NodeIDs[:1]
			},
			want: "want all 2 locations",
		},
		{
			name: "commitment unknown task",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Commitments.FactWatermark = "facts-2"
				value.Commitments.Executed = []domain.ExecutedTaskCommitment{{
					TaskID:      "unknown",
					VehicleID:   "vehicle-1",
					DriverID:    "driver-1",
					CompletedAt: value.CreatedAt,
				}}
			},
			want: "unknown task",
		},
		{
			name: "commitment wrong compartment",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Commitments.FactWatermark = "facts-2"
				value.Commitments.InTransit = []domain.InTransitCargoCommitment{{
					CargoID:       "cargo-1",
					VehicleID:     "vehicle-1",
					CompartmentID: "unknown",
				}}
			},
			want: "unknown compartment",
		},
		{
			name: "commitment missing watermark",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Commitments.FactWatermark = ""
				value.Commitments.Soft = []domain.SoftTaskCommitment{{
					TaskID:           "delivery-1",
					VehicleID:        "vehicle-1",
					DriverID:         "driver-1",
					PlannedServiceAt: value.CreatedAt,
				}}
			},
			want: "fact_watermark",
		},
		{
			name: "invalid base plan digest",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Commitments.BasePlanDigest = "invalid"
			},
			want: "base_plan_digest",
		},
		{
			name: "contradictory split policy",
			mutate: func(value *domain.ProblemSnapshot) {
				value.Requests[0].Split.MaxSplits = 2
			},
			want: "forbids splitting",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := validProblemDraft()
			test.mutate(&draft)
			_, err := BuildProblemSnapshot(draft)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildProblemSnapshot error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestBuildProblemSnapshotAcceptsBoundCommitments(t *testing.T) {
	draft := validProblemDraft()
	draft.Commitments = domain.CommitmentSet{
		BasePlanDigest: domain.ArtifactDigest(strings.Repeat("a", 64)),
		FactWatermark:  "facts-2",
		Executed: []domain.ExecutedTaskCommitment{{
			TaskID:      "pickup-1",
			VehicleID:   "vehicle-1",
			DriverID:    "driver-1",
			CompletedAt: draft.CreatedAt,
		}},
		Frozen: []domain.FrozenTaskCommitment{{
			TaskID:            "delivery-1",
			VehicleID:         "vehicle-1",
			DriverID:          "driver-1",
			Sequence:          1,
			PromisedServiceAt: draft.CreatedAt.Add(time.Hour),
			ToleranceSeconds:  300,
		}},
		InTransit: []domain.InTransitCargoCommitment{{
			CargoID:       "cargo-1",
			VehicleID:     "vehicle-1",
			CompartmentID: "compartment-1",
		}},
		Soft: []domain.SoftTaskCommitment{},
	}

	snapshot, err := BuildProblemSnapshot(draft)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CommitmentDigest == "" {
		t.Fatal("commitment digest is empty")
	}
}

func validProblemDraft() domain.ProblemSnapshot {
	base := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	return domain.ProblemSnapshot{
		SchemaVersion: domain.ProblemSchemaVersion,
		TenantID:      "tenant-a",
		ProblemID:     "problem-1",
		Version:       1,
		Horizon:       domain.TimeRange{Start: base, End: base.Add(12 * time.Hour)},
		CreatedAt:     base.Add(-time.Hour),
		Locations: []domain.Location{
			{ID: "customer-1", Name: "Customer 1", Kind: domain.LocationCustomer},
			{ID: "depot-1", Name: "Depot 1", Kind: domain.LocationDepot},
		},
		Depots: []domain.Depot{{
			ID:             "depot-1",
			LocationID:     "depot-1",
			AllowTripStart: true,
			AllowTripEnd:   true,
			Docks: []domain.Dock{{
				ID:           "dock-1",
				Availability: []domain.TimeRange{{Start: base, End: base.Add(12 * time.Hour)}},
			}},
		}},
		Requests: []domain.TransportRequest{{
			ID:       "request-1",
			Priority: 100,
			Required: true,
			Tasks: []domain.ServiceTask{
				{
					ID:             "pickup-1",
					Kind:           domain.TaskPickup,
					LocationID:     "depot-1",
					HardWindows:    []domain.TimeRange{{Start: base, End: base.Add(time.Hour)}},
					ServiceSeconds: 300,
					UnitIDs:        []domain.FulfillmentUnitID{"unit-1"},
					RequiredSkills: domain.SkillSet{"cold"},
				},
				{
					ID:             "delivery-1",
					Kind:           domain.TaskDelivery,
					LocationID:     "customer-1",
					HardWindows:    []domain.TimeRange{{Start: base.Add(time.Hour), End: base.Add(4 * time.Hour)}},
					ServiceSeconds: 600,
					PredecessorIDs: []domain.TaskID{"pickup-1"},
					UnitIDs:        []domain.FulfillmentUnitID{"unit-1"},
					RequiredSkills: domain.SkillSet{"cold"},
					MaxRideSeconds: 3_600,
				},
			},
			UnitIDs:        []domain.FulfillmentUnitID{"unit-1"},
			Split:          domain.SplitPolicy{Mode: domain.SplitForbidden, MinUnitsPerSplit: 1, MaxSplits: 1, SameVehicle: true, SameTrip: true},
			RequiredSkills: domain.SkillSet{"cold"},
		}},
		Units: []domain.FulfillmentUnit{{
			ID:            "unit-1",
			RequestID:     "request-1",
			AtomicGroupID: "atomic-1",
			CargoIDs:      []domain.CargoID{"cargo-1"},
			Quantity:      1,
		}},
		Cargo: []domain.CargoItem{{
			ID:                  "cargo-1",
			UnitID:              "unit-1",
			SizeMM:              domain.Box{Length: 1_000, Width: 800, Height: 600},
			WeightG:             100_000,
			AllowedOrientations: []domain.Orientation{domain.OrientationLWH},
			MaxTopLoadG:         0,
			MinSupportPPM:       1_000_000,
			TemperatureZone:     "cold",
			CargoClass:          "food",
			IncompatibleClasses: []string{},
		}},
		Vehicles: []domain.Vehicle{{
			ID:           "vehicle-1",
			HomeDepotID:  "depot-1",
			Availability: []domain.TimeRange{{Start: base, End: base.Add(12 * time.Hour)}},
			Skills:       domain.SkillSet{"liftgate", "cold"},
			Compartments: []domain.Compartment{{
				ID:                  "compartment-1",
				Bounds:              domain.Cuboid{Size: domain.Box{Length: 4_000, Width: 2_000, Height: 2_000}},
				MaxPayloadG:         1_000_000,
				TemperatureZones:    []string{"cold"},
				AllowedCargoClasses: []string{"food"},
				Obstacles:           []domain.Cuboid{},
			}},
			Doors: []domain.Door{{
				ID:             "door-1",
				CompartmentID:  "compartment-1",
				Opening:        domain.Cuboid{Origin: domain.Point3{X: 4_000}, Size: domain.Box{Length: 1, Width: 2_000, Height: 2_000}},
				ExtractionAxis: domain.AxisX,
				Direction:      1,
			}},
			Axles: []domain.Axle{
				{ID: "front", PositionXMM: 800, MaxLoadG: 1_000_000},
				{ID: "rear", PositionXMM: 3_200, MaxLoadG: 1_000_000},
			},
			CGEnvelope:       domain.CGEnvelope{Min: domain.Point3{}, Max: domain.Point3{X: 4_000, Y: 2_000, Z: 2_000}},
			MaxTrips:         2,
			MaxGrossWeightG:  2_000_000,
			TareWeightG:      500_000,
			FixedCostCents:   5_000,
			DistanceCostCPKM: 100,
			WorkCostCPH:      2_000,
			Energy: domain.EnergySpec{
				Kind:               domain.EnergyCombustion,
				ConsumptionWhPerKM: 1_000,
			},
		}},
		Drivers: []domain.Driver{{
			ID:            "driver-1",
			Skills:        domain.SkillSet{"cold", "liftgate"},
			Shift:         domain.TimeRange{Start: base, End: base.Add(12 * time.Hour)},
			StartLocation: "depot-1",
			EndLocations:  []domain.LocationID{"depot-1"},
			Regulation: domain.DriverRegulation{
				MaxContinuousDriveSeconds: 14_400,
				RequiredBreakSeconds:      1_800,
				MaxDutySeconds:            36_000,
				MaxDriveSeconds:           28_800,
				MinRestBetweenDutySeconds: 39_600,
			},
		}},
		Chargers: []domain.ChargingStation{},
		Travel: domain.TravelMatrix{
			NodeIDs:        []domain.LocationID{"depot-1", "customer-1"},
			DistanceMeters: []int64{0, 10_000, 10_000, 0},
			TravelSeconds:  []int64{0, 1_800, 1_800, 0},
		},
		Energy: domain.EnergyMatrix{
			NodeIDs:  []domain.LocationID{"depot-1", "customer-1"},
			Profiles: []domain.EnergyProfileMatrix{},
		},
		Policy: domain.PlanningPolicy{
			ID:                        "policy-main",
			Version:                   1,
			DefaultMinSupportPPM:      1_000_000,
			MaxRehandlesPerStop:       0,
			FreezeWindowSeconds:       3_600,
			ETAToleranceSeconds:       300,
			RequiredOrderPenaltyCents: 1_000_000,
			OptionalOrderPenaltyCents: 100_000,
			AllowedMixedCargoClasses:  [][]string{},
		},
		Commitments: domain.CommitmentSet{
			FactWatermark: "facts-1",
			Executed:      []domain.ExecutedTaskCommitment{},
			Frozen:        []domain.FrozenTaskCommitment{},
			InTransit:     []domain.InTransitCargoCommitment{},
			Soft:          []domain.SoftTaskCommitment{},
		},
		SourceRefs: []domain.SourceRef{{
			System:       "orders",
			ResourceType: "request",
			ResourceID:   "request-1",
			Version:      "1",
			ObservedAt:   base.Add(-time.Hour),
		}},
	}
}
