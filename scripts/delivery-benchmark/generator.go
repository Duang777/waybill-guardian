package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

type generatedDataset struct {
	spec      datasetSpec
	problem   domain.ProblemSnapshot
	canonical []byte
}

type gridPoint struct {
	x int64
	y int64
}

type splitMix64 struct {
	state uint64
}

func (generator *splitMix64) next() uint64 {
	generator.state += 0x9e3779b97f4a7c15
	value := generator.state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func (generator *splitMix64) between(minimum, maximum int64) int64 {
	width := uint64(maximum - minimum + 1)
	return minimum + int64(generator.next()%width)
}

func generateDatasets(config suiteConfig) ([]generatedDataset, datasetManifest, error) {
	base, err := time.Parse(time.RFC3339, config.Generator.BaseTime)
	if err != nil {
		return nil, datasetManifest{}, err
	}
	datasets := make([]generatedDataset, 0, len(config.Datasets))
	manifest := datasetManifest{
		SchemaVersion: manifestSchemaVersion,
		Generator:     config.Generator,
		Datasets:      make([]datasetManifestRecord, 0, len(config.Datasets)),
	}
	validator := validate.New(config.Validator)
	for _, spec := range config.Datasets {
		problem, err := generateProblem(config.Generator.Version, spec, base)
		if err != nil {
			return nil, datasetManifest{}, fmt.Errorf("generate %s: %w", spec.ID, err)
		}
		canonical, err := domain.CanonicalJSON(problem)
		if err != nil {
			return nil, datasetManifest{}, fmt.Errorf("encode %s: %w", spec.ID, err)
		}
		plan, err := buildReferencePlan(problem, spec.ID)
		if err != nil {
			return nil, datasetManifest{}, fmt.Errorf("reference plan %s: %w", spec.ID, err)
		}
		report := validator.Validate(problem, plan, base.Add(-time.Minute))
		hardViolations := countHardViolations(report)
		if !report.Valid || hardViolations != 0 {
			return nil, datasetManifest{}, fmt.Errorf(
				"reference plan %s has %d hard violations",
				spec.ID,
				hardViolations,
			)
		}
		sum := sha256.Sum256(canonical)
		manifest.Datasets = append(manifest.Datasets, datasetManifestRecord{
			ID:                     spec.ID,
			Seed:                   spec.Seed,
			RequestCount:           len(problem.Requests),
			TaskCount:              countTasks(problem),
			CargoCount:             len(problem.Cargo),
			VehicleCount:           len(problem.Vehicles),
			DriverCount:            len(problem.Drivers),
			LocationCount:          len(problem.Locations),
			CanonicalBytes:         len(canonical),
			CanonicalSHA256:        hex.EncodeToString(sum[:]),
			ProblemDigest:          problem.ProblemDigest,
			PolicyDigest:           problem.PolicyDigest,
			CommitmentDigest:       problem.CommitmentDigest,
			ReferencePlanDigest:    plan.PlanDigest,
			ReferenceReportDigest:  report.ReportDigest,
			ReferenceHardViolation: hardViolations,
		})
		datasets = append(datasets, generatedDataset{
			spec:      spec,
			problem:   problem,
			canonical: canonical,
		})
	}
	return datasets, manifest, nil
}

func generateProblem(
	generatorVersion string,
	spec datasetSpec,
	base time.Time,
) (domain.ProblemSnapshot, error) {
	random := splitMix64{state: spec.Seed}
	horizon := domain.TimeRange{Start: base, End: base.Add(24 * time.Hour)}
	depotLocationID := domain.LocationID("depot-001")
	depotID := domain.DepotID("depot-001")
	locations := []domain.Location{{
		ID:                depotLocationID,
		Name:              "Synthetic Depot 001",
		Kind:              domain.LocationDepot,
		LatitudeMicroDeg:  30_274_000,
		LongitudeMicroDeg: 120_155_000,
	}}
	points := []gridPoint{{x: 0, y: 0}}
	requests := make([]domain.TransportRequest, 0, spec.RequestCount)
	units := make([]domain.FulfillmentUnit, 0, spec.RequestCount)
	cargo := make([]domain.CargoItem, 0, spec.RequestCount)
	vehicles := make([]domain.Vehicle, 0, spec.RequestCount)
	drivers := make([]domain.Driver, 0, spec.RequestCount)
	for index := 1; index <= spec.RequestCount; index++ {
		suffix := fmt.Sprintf("%03d", index)
		locationID := domain.LocationID("customer-" + suffix)
		requestID := domain.RequestID("request-" + suffix)
		unitID := domain.FulfillmentUnitID("unit-" + suffix)
		cargoID := domain.CargoID("cargo-" + suffix)
		pickupID := domain.TaskID("pickup-" + suffix)
		deliveryID := domain.TaskID("delivery-" + suffix)
		vehicleID := domain.VehicleID("vehicle-" + suffix)
		driverID := domain.DriverID("driver-" + suffix)
		compartmentID := domain.CompartmentID("compartment-" + suffix)
		doorID := domain.DoorID("door-" + suffix)

		point := gridPoint{
			x: random.between(2_000, 45_000),
			y: random.between(2_000, 45_000),
		}
		points = append(points, point)
		locations = append(locations, domain.Location{
			ID:                locationID,
			Name:              "Synthetic Customer " + suffix,
			Kind:              domain.LocationCustomer,
			LatitudeMicroDeg:  30_274_000 + point.y*9,
			LongitudeMicroDeg: 120_155_000 + point.x*10,
		})
		length := random.between(800, 1_400)
		width := random.between(600, 1_000)
		height := random.between(500, 1_000)
		weight := random.between(40, 180) * 1_000
		requests = append(requests, domain.TransportRequest{
			ID:       requestID,
			Priority: int32(100 + index%5),
			Required: true,
			Tasks: []domain.ServiceTask{
				{
					ID:             pickupID,
					Kind:           domain.TaskPickup,
					LocationID:     depotLocationID,
					HardWindows:    []domain.TimeRange{horizon},
					SoftWindows:    []domain.SoftTimeWindow{},
					ServiceSeconds: 300,
					PredecessorIDs: []domain.TaskID{},
					UnitIDs:        []domain.FulfillmentUnitID{unitID},
					RequiredSkills: domain.SkillSet{"ambient"},
				},
				{
					ID:             deliveryID,
					Kind:           domain.TaskDelivery,
					LocationID:     locationID,
					HardWindows:    []domain.TimeRange{horizon},
					SoftWindows:    []domain.SoftTimeWindow{},
					ServiceSeconds: 600,
					PredecessorIDs: []domain.TaskID{pickupID},
					UnitIDs:        []domain.FulfillmentUnitID{unitID},
					RequiredSkills: domain.SkillSet{"ambient"},
					MaxRideSeconds: 20_000,
				},
			},
			UnitIDs:        []domain.FulfillmentUnitID{unitID},
			Split:          domain.SplitPolicy{Mode: domain.SplitForbidden, MinUnitsPerSplit: 1, MaxSplits: 1, SameVehicle: true, SameTrip: true},
			RequiredSkills: domain.SkillSet{"ambient"},
		})
		units = append(units, domain.FulfillmentUnit{
			ID:            unitID,
			RequestID:     requestID,
			AtomicGroupID: "atomic-" + suffix,
			CargoIDs:      []domain.CargoID{cargoID},
			Quantity:      1,
		})
		cargo = append(cargo, domain.CargoItem{
			ID:                  cargoID,
			UnitID:              unitID,
			SizeMM:              domain.Box{Length: length, Width: width, Height: height},
			WeightG:             weight,
			AllowedOrientations: []domain.Orientation{domain.OrientationLWH},
			FragileTopOnly:      index%7 == 0,
			MaxTopLoadG:         0,
			MinSupportPPM:       1_000_000,
			TemperatureZone:     "ambient",
			CargoClass:          "general",
			IncompatibleClasses: []string{},
		})
		vehicles = append(vehicles, benchmarkVehicle(
			vehicleID,
			depotID,
			compartmentID,
			doorID,
			horizon,
		))
		drivers = append(drivers, benchmarkDriver(driverID, depotLocationID, horizon))
	}
	distance, travel := buildMatrices(points)
	draft := domain.ProblemSnapshot{
		SchemaVersion: domain.ProblemSchemaVersion,
		TenantID:      "benchmark",
		ProblemID:     domain.ProblemID("problem-" + spec.ID),
		Version:       1,
		Horizon:       horizon,
		Locations:     locations,
		Depots: []domain.Depot{{
			ID:             depotID,
			LocationID:     depotLocationID,
			Docks:          []domain.Dock{{ID: "dock-001", Availability: []domain.TimeRange{horizon}}},
			AllowTripStart: true,
			AllowTripEnd:   true,
		}},
		Requests: requests,
		Units:    units,
		Cargo:    cargo,
		Vehicles: vehicles,
		Drivers:  drivers,
		Chargers: []domain.ChargingStation{},
		Travel: domain.TravelMatrix{
			NodeIDs:        locationIDs(locations),
			DistanceMeters: distance,
			TravelSeconds:  travel,
		},
		Energy: domain.EnergyMatrix{
			NodeIDs:  locationIDs(locations),
			Profiles: []domain.EnergyProfileMatrix{},
		},
		Policy: domain.PlanningPolicy{
			ID:                        "benchmark-policy",
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
			FactWatermark: generatorVersion + ":" + spec.ID,
			Executed:      []domain.ExecutedTaskCommitment{},
			Frozen:        []domain.FrozenTaskCommitment{},
			InTransit:     []domain.InTransitCargoCommitment{},
			Soft:          []domain.SoftTaskCommitment{},
		},
		SourceRefs: []domain.SourceRef{{
			System:       generatorVersion,
			ResourceType: "synthetic_dataset",
			ResourceID:   spec.ID,
			Version:      fmt.Sprintf("seed-%d", spec.Seed),
			ObservedAt:   base.Add(-time.Hour),
		}},
		CreatedAt: base.Add(-time.Hour),
	}
	return service.BuildProblemSnapshot(draft)
}

func benchmarkVehicle(
	id domain.VehicleID,
	depotID domain.DepotID,
	compartmentID domain.CompartmentID,
	doorID domain.DoorID,
	availability domain.TimeRange,
) domain.Vehicle {
	return domain.Vehicle{
		ID:           id,
		HomeDepotID:  depotID,
		Availability: []domain.TimeRange{availability},
		Skills:       domain.SkillSet{"ambient"},
		Compartments: []domain.Compartment{{
			ID:                  compartmentID,
			Bounds:              domain.Cuboid{Size: domain.Box{Length: 6_000, Width: 2_400, Height: 2_400}},
			MaxPayloadG:         2_000_000,
			TemperatureZones:    []string{"ambient"},
			AllowedCargoClasses: []string{"general"},
			Obstacles:           []domain.Cuboid{},
		}},
		Doors: []domain.Door{{
			ID:             doorID,
			CompartmentID:  compartmentID,
			Opening:        domain.Cuboid{Origin: domain.Point3{X: 6_000}, Size: domain.Box{Length: 1, Width: 2_400, Height: 2_400}},
			ExtractionAxis: domain.AxisX,
			Direction:      1,
		}},
		Axles: []domain.Axle{
			{ID: domain.AxleID("front-" + string(id)), PositionXMM: 1_500, MaxLoadG: 2_000_000},
			{ID: domain.AxleID("rear-" + string(id)), PositionXMM: 4_500, MaxLoadG: 2_000_000},
		},
		CGEnvelope:       domain.CGEnvelope{Min: domain.Point3{}, Max: domain.Point3{X: 6_000, Y: 2_400, Z: 2_400}},
		MaxTrips:         1,
		MaxGrossWeightG:  3_000_000,
		TareWeightG:      800_000,
		FixedCostCents:   5_000,
		DistanceCostCPKM: 100,
		WorkCostCPH:      2_000,
		EnergyCostCPKWh:  0,
		Energy: domain.EnergySpec{
			Kind:                domain.EnergyCombustion,
			ConnectorTypes:      []string{},
			ChargingCurve:       []domain.ChargingBand{},
			ConsumptionWhPerKM:  1_000,
			LoadWhPerKMPerTonne: 0,
		},
	}
}

func benchmarkDriver(
	id domain.DriverID,
	depotID domain.LocationID,
	shift domain.TimeRange,
) domain.Driver {
	return domain.Driver{
		ID:            id,
		Skills:        domain.SkillSet{"ambient"},
		Shift:         shift,
		StartLocation: depotID,
		EndLocations:  []domain.LocationID{depotID},
		Regulation: domain.DriverRegulation{
			MaxContinuousDriveSeconds: 28_800,
			RequiredBreakSeconds:      1_800,
			MaxDutySeconds:            43_200,
			MaxDriveSeconds:           36_000,
			MinRestBetweenDutySeconds: 39_600,
		},
	}
}

func buildMatrices(points []gridPoint) ([]int64, []int64) {
	size := len(points)
	distance := make([]int64, 0, size*size)
	travel := make([]int64, 0, size*size)
	for _, from := range points {
		for _, to := range points {
			meters := absolute(from.x-to.x) + absolute(from.y-to.y)
			distance = append(distance, meters)
			travel = append(travel, (meters+9)/10)
		}
	}
	return distance, travel
}

func locationIDs(locations []domain.Location) []domain.LocationID {
	result := make([]domain.LocationID, len(locations))
	for index, location := range locations {
		result[index] = location.ID
	}
	return result
}

func absolute(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func countTasks(problem domain.ProblemSnapshot) int {
	count := 0
	for _, request := range problem.Requests {
		count += len(request.Tasks)
	}
	return count
}

func countHardViolations(report domain.ValidationReport) int {
	count := 0
	for _, violation := range report.Violations {
		if violation.Severity == domain.SeverityError {
			count++
		}
	}
	return count
}
