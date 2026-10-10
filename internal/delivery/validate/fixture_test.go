package validate

import (
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func validationProblemDraft(base time.Time) domain.ProblemSnapshot {
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
			Skills:       domain.SkillSet{"cold", "liftgate"},
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
			EnergyCostCPKWh:  0,
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
