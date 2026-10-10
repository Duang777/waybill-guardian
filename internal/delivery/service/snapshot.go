package service

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func BuildProblemSnapshot(draft domain.ProblemSnapshot) (domain.ProblemSnapshot, error) {
	snapshot, err := cloneProblem(draft)
	if err != nil {
		return domain.ProblemSnapshot{}, err
	}
	normalizeProblem(&snapshot)
	if err := validateProblem(snapshot); err != nil {
		return domain.ProblemSnapshot{}, err
	}

	snapshot.PolicyDigest, err = domain.ComputePolicyDigest(snapshot.Policy)
	if err != nil {
		return domain.ProblemSnapshot{}, fmt.Errorf("digest policy: %w", err)
	}
	snapshot.CommitmentDigest, err = domain.ComputeCommitmentDigest(snapshot.Commitments)
	if err != nil {
		return domain.ProblemSnapshot{}, fmt.Errorf("digest commitments: %w", err)
	}
	snapshot.ProblemDigest = ""
	snapshot.ProblemDigest, err = domain.ComputeProblemDigest(snapshot)
	if err != nil {
		return domain.ProblemSnapshot{}, fmt.Errorf("digest problem: %w", err)
	}
	return snapshot, nil
}

func cloneProblem(value domain.ProblemSnapshot) (domain.ProblemSnapshot, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return domain.ProblemSnapshot{}, fmt.Errorf("copy problem: %w", err)
	}
	var result domain.ProblemSnapshot
	if err := json.Unmarshal(raw, &result); err != nil {
		return domain.ProblemSnapshot{}, fmt.Errorf("copy problem: %w", err)
	}
	return result, nil
}

func normalizeProblem(value *domain.ProblemSnapshot) {
	value.Horizon = utcRange(value.Horizon)
	value.CreatedAt = value.CreatedAt.UTC()
	value.ProblemDigest = ""
	value.PolicyDigest = ""
	value.CommitmentDigest = ""

	value.Locations = ensureSlice(value.Locations)
	slices.SortFunc(value.Locations, func(left, right domain.Location) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	value.Depots = ensureSlice(value.Depots)
	slices.SortFunc(value.Depots, func(left, right domain.Depot) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for depotIndex := range value.Depots {
		depot := &value.Depots[depotIndex]
		depot.Docks = ensureSlice(depot.Docks)
		slices.SortFunc(depot.Docks, func(left, right domain.Dock) int {
			return strings.Compare(string(left.ID), string(right.ID))
		})
		for dockIndex := range depot.Docks {
			dock := &depot.Docks[dockIndex]
			dock.Availability = normalizeRanges(dock.Availability)
		}
	}

	value.Requests = ensureSlice(value.Requests)
	slices.SortFunc(value.Requests, func(left, right domain.TransportRequest) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for requestIndex := range value.Requests {
		request := &value.Requests[requestIndex]
		request.RequiredSkills = normalizeStrings(request.RequiredSkills)
		request.UnitIDs = normalizeIDs(request.UnitIDs)
		request.Tasks = ensureSlice(request.Tasks)
		slices.SortFunc(request.Tasks, func(left, right domain.ServiceTask) int {
			return strings.Compare(string(left.ID), string(right.ID))
		})
		for taskIndex := range request.Tasks {
			task := &request.Tasks[taskIndex]
			task.HardWindows = normalizeRanges(task.HardWindows)
			task.SoftWindows = ensureSlice(task.SoftWindows)
			slices.SortFunc(task.SoftWindows, func(left, right domain.SoftTimeWindow) int {
				return left.Window.Start.Compare(right.Window.Start)
			})
			for windowIndex := range task.SoftWindows {
				task.SoftWindows[windowIndex].Window =
					utcRange(task.SoftWindows[windowIndex].Window)
			}
			task.PredecessorIDs = normalizeIDs(task.PredecessorIDs)
			task.UnitIDs = normalizeIDs(task.UnitIDs)
			task.RequiredSkills = normalizeStrings(task.RequiredSkills)
		}
	}

	value.Units = ensureSlice(value.Units)
	slices.SortFunc(value.Units, func(left, right domain.FulfillmentUnit) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for index := range value.Units {
		value.Units[index].CargoIDs = normalizeIDs(value.Units[index].CargoIDs)
	}

	value.Cargo = ensureSlice(value.Cargo)
	slices.SortFunc(value.Cargo, func(left, right domain.CargoItem) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for index := range value.Cargo {
		value.Cargo[index].AllowedOrientations =
			normalizeStrings(value.Cargo[index].AllowedOrientations)
		value.Cargo[index].IncompatibleClasses =
			normalizeStrings(value.Cargo[index].IncompatibleClasses)
	}

	value.Vehicles = ensureSlice(value.Vehicles)
	slices.SortFunc(value.Vehicles, func(left, right domain.Vehicle) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for vehicleIndex := range value.Vehicles {
		vehicle := &value.Vehicles[vehicleIndex]
		vehicle.Availability = normalizeRanges(vehicle.Availability)
		vehicle.Skills = normalizeStrings(vehicle.Skills)
		vehicle.Compartments = ensureSlice(vehicle.Compartments)
		slices.SortFunc(vehicle.Compartments, func(left, right domain.Compartment) int {
			return strings.Compare(string(left.ID), string(right.ID))
		})
		for compartmentIndex := range vehicle.Compartments {
			compartment := &vehicle.Compartments[compartmentIndex]
			compartment.TemperatureZones = normalizeStrings(compartment.TemperatureZones)
			compartment.AllowedCargoClasses =
				normalizeStrings(compartment.AllowedCargoClasses)
			compartment.Obstacles = ensureSlice(compartment.Obstacles)
		}
		vehicle.Doors = ensureSlice(vehicle.Doors)
		slices.SortFunc(vehicle.Doors, func(left, right domain.Door) int {
			return strings.Compare(string(left.ID), string(right.ID))
		})
		vehicle.Axles = ensureSlice(vehicle.Axles)
		slices.SortFunc(vehicle.Axles, func(left, right domain.Axle) int {
			return strings.Compare(string(left.ID), string(right.ID))
		})
		vehicle.Energy.ConnectorTypes = normalizeStrings(vehicle.Energy.ConnectorTypes)
		vehicle.Energy.ChargingCurve = ensureSlice(vehicle.Energy.ChargingCurve)
		slices.SortFunc(vehicle.Energy.ChargingCurve, func(left, right domain.ChargingBand) int {
			if left.FromSOCPPM < right.FromSOCPPM {
				return -1
			}
			if left.FromSOCPPM > right.FromSOCPPM {
				return 1
			}
			return 0
		})
	}

	value.Drivers = ensureSlice(value.Drivers)
	slices.SortFunc(value.Drivers, func(left, right domain.Driver) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for index := range value.Drivers {
		value.Drivers[index].Skills = normalizeStrings(value.Drivers[index].Skills)
		value.Drivers[index].EndLocations = normalizeIDs(value.Drivers[index].EndLocations)
		value.Drivers[index].Shift = utcRange(value.Drivers[index].Shift)
	}

	value.Chargers = ensureSlice(value.Chargers)
	slices.SortFunc(value.Chargers, func(left, right domain.ChargingStation) int {
		return strings.Compare(string(left.ID), string(right.ID))
	})
	for index := range value.Chargers {
		value.Chargers[index].ConnectorTypes =
			normalizeStrings(value.Chargers[index].ConnectorTypes)
		value.Chargers[index].Availability =
			normalizeRanges(value.Chargers[index].Availability)
	}

	value.Travel.NodeIDs = ensureSlice(value.Travel.NodeIDs)
	value.Travel.DistanceMeters = ensureSlice(value.Travel.DistanceMeters)
	value.Travel.TravelSeconds = ensureSlice(value.Travel.TravelSeconds)
	value.Energy.NodeIDs = ensureSlice(value.Energy.NodeIDs)
	value.Energy.Profiles = ensureSlice(value.Energy.Profiles)
	slices.SortFunc(value.Energy.Profiles, func(left, right domain.EnergyProfileMatrix) int {
		return strings.Compare(left.ProfileID, right.ProfileID)
	})
	for index := range value.Energy.Profiles {
		value.Energy.Profiles[index].BaseWh = ensureSlice(value.Energy.Profiles[index].BaseWh)
		value.Energy.Profiles[index].LoadWhPerTonne =
			ensureSlice(value.Energy.Profiles[index].LoadWhPerTonne)
	}

	value.Policy.AllowedMixedCargoClasses =
		normalizeStringGroups(value.Policy.AllowedMixedCargoClasses)
	normalizeCommitments(&value.Commitments)
	value.SourceRefs = ensureSlice(value.SourceRefs)
	slices.SortFunc(value.SourceRefs, func(left, right domain.SourceRef) int {
		if result := strings.Compare(left.System, right.System); result != 0 {
			return result
		}
		if result := strings.Compare(left.ResourceType, right.ResourceType); result != 0 {
			return result
		}
		return strings.Compare(left.ResourceID, right.ResourceID)
	})
	for index := range value.SourceRefs {
		value.SourceRefs[index].ObservedAt = value.SourceRefs[index].ObservedAt.UTC()
	}
}

func normalizeCommitments(value *domain.CommitmentSet) {
	value.Executed = ensureSlice(value.Executed)
	slices.SortFunc(value.Executed, func(left, right domain.ExecutedTaskCommitment) int {
		return strings.Compare(string(left.TaskID), string(right.TaskID))
	})
	for index := range value.Executed {
		value.Executed[index].CompletedAt = value.Executed[index].CompletedAt.UTC()
	}
	value.Frozen = ensureSlice(value.Frozen)
	slices.SortFunc(value.Frozen, func(left, right domain.FrozenTaskCommitment) int {
		return strings.Compare(string(left.TaskID), string(right.TaskID))
	})
	for index := range value.Frozen {
		value.Frozen[index].PromisedServiceAt = value.Frozen[index].PromisedServiceAt.UTC()
	}
	value.InTransit = ensureSlice(value.InTransit)
	slices.SortFunc(value.InTransit, func(left, right domain.InTransitCargoCommitment) int {
		return strings.Compare(string(left.CargoID), string(right.CargoID))
	})
	value.Soft = ensureSlice(value.Soft)
	slices.SortFunc(value.Soft, func(left, right domain.SoftTaskCommitment) int {
		return strings.Compare(string(left.TaskID), string(right.TaskID))
	})
	for index := range value.Soft {
		value.Soft[index].PlannedServiceAt = value.Soft[index].PlannedServiceAt.UTC()
	}
}

func validateProblem(value domain.ProblemSnapshot) error {
	if value.SchemaVersion != domain.ProblemSchemaVersion {
		return fmt.Errorf("schema_version must be %q", domain.ProblemSchemaVersion)
	}
	if err := requiredID("tenant_id", string(value.TenantID)); err != nil {
		return err
	}
	if err := requiredID("problem_id", string(value.ProblemID)); err != nil {
		return err
	}
	if value.Version == 0 {
		return fmt.Errorf("version must be positive")
	}
	if err := validRange("horizon", value.Horizon); err != nil {
		return err
	}
	if value.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}

	locations, err := indexLocations(value.Locations)
	if err != nil {
		return err
	}
	depots, err := indexDepots(value.Depots, locations)
	if err != nil {
		return err
	}
	units, err := indexUnits(value.Units)
	if err != nil {
		return err
	}
	if err := validateRequests(value.Requests, locations, units); err != nil {
		return err
	}
	if err := validateCargo(value.Cargo, units); err != nil {
		return err
	}
	if err := validateVehicles(value.Vehicles, depots); err != nil {
		return err
	}
	if err := validateDrivers(value.Drivers, locations); err != nil {
		return err
	}
	if err := validateChargers(value.Chargers, locations); err != nil {
		return err
	}
	if err := validateMatrix("travel matrix", value.Travel.NodeIDs, locations,
		value.Travel.DistanceMeters, value.Travel.TravelSeconds); err != nil {
		return err
	}
	if err := validateEnergyMatrix(value.Energy, locations); err != nil {
		return err
	}
	if !slices.Equal(value.Travel.NodeIDs, value.Energy.NodeIDs) {
		return fmt.Errorf("travel and energy matrices must use the same ordered nodes")
	}
	if err := validateMatrixCoverage(value.Travel.NodeIDs, locations); err != nil {
		return err
	}
	energyProfiles := make(map[string]struct{}, len(value.Energy.Profiles))
	for _, profile := range value.Energy.Profiles {
		energyProfiles[profile.ProfileID] = struct{}{}
	}
	for _, vehicle := range value.Vehicles {
		if vehicle.Energy.Kind != domain.EnergyElectric {
			continue
		}
		if _, exists := energyProfiles[vehicle.Energy.MatrixProfileID]; !exists {
			return fmt.Errorf(
				"vehicle %q references unknown energy profile %q",
				vehicle.ID,
				vehicle.Energy.MatrixProfileID,
			)
		}
	}
	if value.Policy.ID == "" || value.Policy.Version == 0 {
		return fmt.Errorf("policy identity and version are required")
	}
	if value.Policy.DefaultMinSupportPPM < 0 ||
		value.Policy.DefaultMinSupportPPM > 1_000_000 {
		return fmt.Errorf("policy default_min_support_ppm must be between 0 and 1000000")
	}
	if err := validateCommitments(
		value.Commitments,
		value.Requests,
		value.Vehicles,
		value.Drivers,
		value.Cargo,
	); err != nil {
		return err
	}
	if len(value.SourceRefs) == 0 {
		return fmt.Errorf("at least one source_ref is required")
	}
	_, err = domain.CanonicalJSON(value)
	if err != nil {
		return fmt.Errorf("problem is not canonicalizable: %w", err)
	}
	return nil
}

func indexLocations(values []domain.Location) (map[domain.LocationID]domain.Location, error) {
	result := make(map[domain.LocationID]domain.Location, len(values))
	for _, value := range values {
		if err := requiredID("location id", string(value.ID)); err != nil {
			return nil, err
		}
		if _, exists := result[value.ID]; exists {
			return nil, fmt.Errorf("duplicate location %q", value.ID)
		}
		result[value.ID] = value
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one location is required")
	}
	return result, nil
}

func indexDepots(
	values []domain.Depot,
	locations map[domain.LocationID]domain.Location,
) (map[domain.DepotID]domain.Depot, error) {
	result := make(map[domain.DepotID]domain.Depot, len(values))
	for _, value := range values {
		if _, exists := result[value.ID]; exists {
			return nil, fmt.Errorf("duplicate depot %q", value.ID)
		}
		if _, exists := locations[value.LocationID]; !exists {
			return nil, fmt.Errorf("depot %q references unknown location %q", value.ID, value.LocationID)
		}
		result[value.ID] = value
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("at least one depot is required")
	}
	return result, nil
}

func indexUnits(
	values []domain.FulfillmentUnit,
) (map[domain.FulfillmentUnitID]domain.FulfillmentUnit, error) {
	result := make(map[domain.FulfillmentUnitID]domain.FulfillmentUnit, len(values))
	for _, value := range values {
		if _, exists := result[value.ID]; exists {
			return nil, fmt.Errorf("duplicate unit %q", value.ID)
		}
		if value.ID == "" || value.RequestID == "" || value.Quantity <= 0 {
			return nil, fmt.Errorf("unit %q has invalid identity, request, or quantity", value.ID)
		}
		result[value.ID] = value
	}
	return result, nil
}

func validateRequests(
	values []domain.TransportRequest,
	locations map[domain.LocationID]domain.Location,
	units map[domain.FulfillmentUnitID]domain.FulfillmentUnit,
) error {
	requests := make(map[domain.RequestID]struct{}, len(values))
	requestUnits := make(
		map[domain.RequestID]map[domain.FulfillmentUnitID]struct{},
		len(values),
	)
	tasks := make(map[domain.TaskID]struct{})
	for _, request := range values {
		if err := requiredID("request id", string(request.ID)); err != nil {
			return err
		}
		if _, exists := requests[request.ID]; exists {
			return fmt.Errorf("duplicate request %q", request.ID)
		}
		requests[request.ID] = struct{}{}
		requestUnits[request.ID] = make(
			map[domain.FulfillmentUnitID]struct{},
			len(request.UnitIDs),
		)
		switch request.Split.Mode {
		case domain.SplitForbidden:
			if request.Split.MaxSplits != 1 {
				return fmt.Errorf("request %q forbids splitting but max_splits is not 1", request.ID)
			}
		case domain.SplitByUnit:
			if request.Split.MinUnitsPerSplit == 0 || request.Split.MaxSplits == 0 {
				return fmt.Errorf("request %q has an incomplete split policy", request.ID)
			}
		default:
			return fmt.Errorf("request %q has unsupported split mode %q", request.ID, request.Split.Mode)
		}
		for _, unitID := range request.UnitIDs {
			unit, exists := units[unitID]
			if !exists || unit.RequestID != request.ID {
				return fmt.Errorf("request %q references unknown or foreign unit %q", request.ID, unitID)
			}
			requestUnits[request.ID][unitID] = struct{}{}
		}
		for _, task := range request.Tasks {
			if err := requiredID("task id", string(task.ID)); err != nil {
				return err
			}
			if _, exists := tasks[task.ID]; exists {
				return fmt.Errorf("duplicate task %q", task.ID)
			}
			tasks[task.ID] = struct{}{}
			if _, exists := locations[task.LocationID]; !exists {
				return fmt.Errorf("task %q references unknown location %q", task.ID, task.LocationID)
			}
			if task.ServiceSeconds < 0 {
				return fmt.Errorf("task %q has negative service time", task.ID)
			}
			for _, window := range task.HardWindows {
				if err := validRange("task hard window", window); err != nil {
					return fmt.Errorf("task %q: %w", task.ID, err)
				}
			}
			for _, unitID := range task.UnitIDs {
				unit, exists := units[unitID]
				if !exists || unit.RequestID != request.ID {
					return fmt.Errorf(
						"task %q references unknown or foreign unit %q",
						task.ID,
						unitID,
					)
				}
			}
		}
		for _, task := range request.Tasks {
			for _, predecessorID := range task.PredecessorIDs {
				if _, exists := tasks[predecessorID]; !exists {
					return fmt.Errorf("task %q references unknown predecessor %q", task.ID, predecessorID)
				}
			}
		}
		if err := validateTaskGraph(request); err != nil {
			return err
		}
	}
	for _, unit := range units {
		if _, exists := requests[unit.RequestID]; !exists {
			return fmt.Errorf("unit %q references unknown request %q", unit.ID, unit.RequestID)
		}
		if _, listed := requestUnits[unit.RequestID][unit.ID]; !listed {
			return fmt.Errorf(
				"unit %q is not listed by request %q",
				unit.ID,
				unit.RequestID,
			)
		}
	}
	return nil
}

func validateTaskGraph(request domain.TransportRequest) error {
	tasks := make(map[domain.TaskID]domain.ServiceTask, len(request.Tasks))
	for _, task := range request.Tasks {
		tasks[task.ID] = task
	}
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[domain.TaskID]int, len(tasks))
	var visit func(domain.TaskID) error
	visit = func(taskID domain.TaskID) error {
		switch state[taskID] {
		case visiting:
			return fmt.Errorf("request %q task graph contains a cycle at %q", request.ID, taskID)
		case visited:
			return nil
		}
		state[taskID] = visiting
		for _, predecessorID := range tasks[taskID].PredecessorIDs {
			if _, exists := tasks[predecessorID]; !exists {
				return fmt.Errorf(
					"request %q task %q references predecessor outside the request",
					request.ID,
					taskID,
				)
			}
			if err := visit(predecessorID); err != nil {
				return err
			}
		}
		state[taskID] = visited
		return nil
	}
	for taskID := range tasks {
		if err := visit(taskID); err != nil {
			return err
		}
	}
	return nil
}

func validateCargo(
	values []domain.CargoItem,
	units map[domain.FulfillmentUnitID]domain.FulfillmentUnit,
) error {
	seen := make(map[domain.CargoID]struct{}, len(values))
	owners := make(map[domain.CargoID]domain.FulfillmentUnitID, len(values))
	for _, value := range values {
		if _, exists := seen[value.ID]; exists {
			return fmt.Errorf("duplicate cargo %q", value.ID)
		}
		seen[value.ID] = struct{}{}
		owners[value.ID] = value.UnitID
		if _, exists := units[value.UnitID]; !exists {
			return fmt.Errorf("cargo %q references unknown unit %q", value.ID, value.UnitID)
		}
		if !value.SizeMM.Valid() || value.WeightG <= 0 || len(value.AllowedOrientations) == 0 {
			return fmt.Errorf("cargo %q has invalid size, weight, or orientations", value.ID)
		}
		if value.MinSupportPPM < 0 || value.MinSupportPPM > 1_000_000 {
			return fmt.Errorf("cargo %q has invalid support ratio", value.ID)
		}
	}
	referenced := make(map[domain.CargoID]domain.FulfillmentUnitID, len(values))
	for _, unit := range units {
		for _, cargoID := range unit.CargoIDs {
			if _, exists := seen[cargoID]; !exists {
				return fmt.Errorf("unit %q references unknown cargo %q", unit.ID, cargoID)
			}
			if owners[cargoID] != unit.ID {
				return fmt.Errorf(
					"unit %q references cargo %q owned by unit %q",
					unit.ID,
					cargoID,
					owners[cargoID],
				)
			}
			if previous, exists := referenced[cargoID]; exists {
				return fmt.Errorf(
					"cargo %q is listed by units %q and %q",
					cargoID,
					previous,
					unit.ID,
				)
			}
			referenced[cargoID] = unit.ID
		}
	}
	for cargoID, owner := range owners {
		if referenced[cargoID] != owner {
			return fmt.Errorf("cargo %q is not listed by owning unit %q", cargoID, owner)
		}
	}
	return nil
}

func validateVehicles(
	values []domain.Vehicle,
	depots map[domain.DepotID]domain.Depot,
) error {
	seen := make(map[domain.VehicleID]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value.ID]; exists {
			return fmt.Errorf("duplicate vehicle %q", value.ID)
		}
		seen[value.ID] = struct{}{}
		if _, exists := depots[value.HomeDepotID]; !exists {
			return fmt.Errorf("vehicle %q references unknown home depot %q", value.ID, value.HomeDepotID)
		}
		if value.MaxTrips == 0 || len(value.Compartments) == 0 ||
			value.MaxGrossWeightG <= value.TareWeightG {
			return fmt.Errorf("vehicle %q has invalid trips, compartments, or gross weight", value.ID)
		}
		for _, availability := range value.Availability {
			if err := validRange("vehicle availability", availability); err != nil {
				return fmt.Errorf("vehicle %q: %w", value.ID, err)
			}
		}
		switch value.Energy.Kind {
		case domain.EnergyCombustion:
			if value.Energy.ConsumptionWhPerKM < 0 ||
				value.Energy.LoadWhPerKMPerTonne < 0 {
				return fmt.Errorf("vehicle %q has invalid combustion energy coefficients", value.ID)
			}
		case domain.EnergyElectric:
			if value.Energy.MatrixProfileID == "" ||
				value.Energy.BatteryCapacityWh <= 0 ||
				value.Energy.InitialSOCWh < value.Energy.ReserveSOCWh ||
				value.Energy.InitialSOCWh > value.Energy.BatteryCapacityWh ||
				value.Energy.ReserveSOCWh < 0 ||
				len(value.Energy.ConnectorTypes) == 0 ||
				len(value.Energy.ChargingCurve) == 0 {
				return fmt.Errorf("vehicle %q has an incomplete electric energy specification", value.ID)
			}
			expectedFrom := int64(0)
			for _, band := range value.Energy.ChargingCurve {
				if band.FromSOCPPM != expectedFrom ||
					band.ToSOCPPM <= band.FromSOCPPM ||
					band.ToSOCPPM > 1_000_000 ||
					band.PowerW <= 0 {
					return fmt.Errorf("vehicle %q has an invalid charging curve", value.ID)
				}
				expectedFrom = band.ToSOCPPM
			}
			if expectedFrom != 1_000_000 {
				return fmt.Errorf("vehicle %q charging curve does not cover the full SOC range", value.ID)
			}
		default:
			return fmt.Errorf("vehicle %q has unsupported energy kind %q", value.ID, value.Energy.Kind)
		}
		compartments := make(map[domain.CompartmentID]struct{}, len(value.Compartments))
		for _, compartment := range value.Compartments {
			if _, exists := compartments[compartment.ID]; exists {
				return fmt.Errorf("vehicle %q has duplicate compartment %q", value.ID, compartment.ID)
			}
			compartments[compartment.ID] = struct{}{}
			if !compartment.Bounds.Size.Valid() || compartment.MaxPayloadG <= 0 {
				return fmt.Errorf("vehicle %q compartment %q is invalid", value.ID, compartment.ID)
			}
		}
		for _, door := range value.Doors {
			if _, exists := compartments[door.CompartmentID]; !exists {
				return fmt.Errorf("vehicle %q door %q references unknown compartment", value.ID, door.ID)
			}
			if door.Direction != -1 && door.Direction != 1 {
				return fmt.Errorf("vehicle %q door %q has invalid direction", value.ID, door.ID)
			}
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("at least one vehicle is required")
	}
	return nil
}

func validateDrivers(
	values []domain.Driver,
	locations map[domain.LocationID]domain.Location,
) error {
	seen := make(map[domain.DriverID]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value.ID]; exists {
			return fmt.Errorf("duplicate driver %q", value.ID)
		}
		seen[value.ID] = struct{}{}
		if _, exists := locations[value.StartLocation]; !exists {
			return fmt.Errorf("driver %q references unknown start location", value.ID)
		}
		for _, locationID := range value.EndLocations {
			if _, exists := locations[locationID]; !exists {
				return fmt.Errorf(
					"driver %q references unknown end location %q",
					value.ID,
					locationID,
				)
			}
		}
		if err := validRange("driver shift", value.Shift); err != nil {
			return fmt.Errorf("driver %q: %w", value.ID, err)
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("at least one driver is required")
	}
	return nil
}

func validateChargers(
	values []domain.ChargingStation,
	locations map[domain.LocationID]domain.Location,
) error {
	seen := make(map[domain.ChargerID]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value.ID]; exists {
			return fmt.Errorf("duplicate charger %q", value.ID)
		}
		seen[value.ID] = struct{}{}
		if _, exists := locations[value.LocationID]; !exists {
			return fmt.Errorf("charger %q references unknown location", value.ID)
		}
		if value.Capacity == 0 || value.MaxPowerW <= 0 {
			return fmt.Errorf("charger %q has invalid capacity or power", value.ID)
		}
		for _, availability := range value.Availability {
			if err := validRange("charger availability", availability); err != nil {
				return fmt.Errorf("charger %q: %w", value.ID, err)
			}
		}
	}
	return nil
}

func validateMatrix(
	name string,
	nodes []domain.LocationID,
	locations map[domain.LocationID]domain.Location,
	matrices ...[]int64,
) error {
	size := len(nodes)
	if size == 0 {
		return fmt.Errorf("%s has no nodes", name)
	}
	if size > int(^uint(0)>>1)/size {
		return fmt.Errorf("%s cardinality overflows platform integer", name)
	}
	seen := make(map[domain.LocationID]struct{}, size)
	for _, nodeID := range nodes {
		if _, exists := locations[nodeID]; !exists {
			return fmt.Errorf("%s references unknown location %q", name, nodeID)
		}
		if _, exists := seen[nodeID]; exists {
			return fmt.Errorf("%s has duplicate node %q", name, nodeID)
		}
		seen[nodeID] = struct{}{}
	}
	for _, matrix := range matrices {
		if len(matrix) != size*size {
			return fmt.Errorf("%s has %d values, want %d", name, len(matrix), size*size)
		}
		for _, value := range matrix {
			if value < 0 {
				return fmt.Errorf("%s contains a negative value", name)
			}
		}
	}
	return nil
}

func validateMatrixCoverage(
	nodes []domain.LocationID,
	locations map[domain.LocationID]domain.Location,
) error {
	if len(nodes) != len(locations) {
		return fmt.Errorf(
			"travel and energy matrices have %d nodes, want all %d locations",
			len(nodes),
			len(locations),
		)
	}
	return nil
}

func validateCommitments(
	value domain.CommitmentSet,
	requests []domain.TransportRequest,
	vehicles []domain.Vehicle,
	drivers []domain.Driver,
	cargo []domain.CargoItem,
) error {
	tasks := make(map[domain.TaskID]struct{})
	for _, request := range requests {
		for _, task := range request.Tasks {
			tasks[task.ID] = struct{}{}
		}
	}
	vehicleCompartments := make(
		map[domain.VehicleID]map[domain.CompartmentID]struct{},
		len(vehicles),
	)
	for _, vehicle := range vehicles {
		compartments := make(map[domain.CompartmentID]struct{}, len(vehicle.Compartments))
		for _, compartment := range vehicle.Compartments {
			compartments[compartment.ID] = struct{}{}
		}
		vehicleCompartments[vehicle.ID] = compartments
	}
	driverIDs := make(map[domain.DriverID]struct{}, len(drivers))
	for _, driver := range drivers {
		driverIDs[driver.ID] = struct{}{}
	}
	cargoIDs := make(map[domain.CargoID]struct{}, len(cargo))
	for _, item := range cargo {
		cargoIDs[item.ID] = struct{}{}
	}
	if value.BasePlanDigest != "" && !validArtifactDigest(value.BasePlanDigest) {
		return fmt.Errorf("commitment base_plan_digest must be a lowercase SHA-256 value")
	}
	hasFacts := len(value.Executed) > 0 ||
		len(value.Frozen) > 0 ||
		len(value.InTransit) > 0 ||
		len(value.Soft) > 0
	if hasFacts && strings.TrimSpace(value.FactWatermark) == "" {
		return fmt.Errorf("commitment fact_watermark is required when facts are present")
	}

	committedTasks := make(map[domain.TaskID]string)
	checkTask := func(
		taskID domain.TaskID,
		vehicleID domain.VehicleID,
		driverID domain.DriverID,
		kind string,
	) error {
		if _, exists := tasks[taskID]; !exists {
			return fmt.Errorf("%s commitment references unknown task %q", kind, taskID)
		}
		if _, exists := vehicleCompartments[vehicleID]; !exists {
			return fmt.Errorf("%s commitment references unknown vehicle %q", kind, vehicleID)
		}
		if _, exists := driverIDs[driverID]; !exists {
			return fmt.Errorf("%s commitment references unknown driver %q", kind, driverID)
		}
		if previous, exists := committedTasks[taskID]; exists {
			return fmt.Errorf(
				"task %q has both %s and %s commitments",
				taskID,
				previous,
				kind,
			)
		}
		committedTasks[taskID] = kind
		return nil
	}
	for _, commitment := range value.Executed {
		if err := checkTask(
			commitment.TaskID,
			commitment.VehicleID,
			commitment.DriverID,
			"executed",
		); err != nil {
			return err
		}
		if commitment.CompletedAt.IsZero() {
			return fmt.Errorf("executed commitment %q has no completed_at", commitment.TaskID)
		}
	}
	for _, commitment := range value.Frozen {
		if err := checkTask(
			commitment.TaskID,
			commitment.VehicleID,
			commitment.DriverID,
			"frozen",
		); err != nil {
			return err
		}
		if commitment.PromisedServiceAt.IsZero() || commitment.ToleranceSeconds < 0 {
			return fmt.Errorf("frozen commitment %q has invalid promise", commitment.TaskID)
		}
	}
	for _, commitment := range value.Soft {
		if err := checkTask(
			commitment.TaskID,
			commitment.VehicleID,
			commitment.DriverID,
			"soft",
		); err != nil {
			return err
		}
		if commitment.PlannedServiceAt.IsZero() {
			return fmt.Errorf("soft commitment %q has no planned_service_at", commitment.TaskID)
		}
	}
	committedCargo := make(map[domain.CargoID]struct{}, len(value.InTransit))
	for _, commitment := range value.InTransit {
		if _, exists := cargoIDs[commitment.CargoID]; !exists {
			return fmt.Errorf(
				"in-transit commitment references unknown cargo %q",
				commitment.CargoID,
			)
		}
		compartments, exists := vehicleCompartments[commitment.VehicleID]
		if !exists {
			return fmt.Errorf(
				"in-transit commitment references unknown vehicle %q",
				commitment.VehicleID,
			)
		}
		if _, exists := compartments[commitment.CompartmentID]; !exists {
			return fmt.Errorf(
				"in-transit commitment references unknown compartment %q on vehicle %q",
				commitment.CompartmentID,
				commitment.VehicleID,
			)
		}
		if _, exists := committedCargo[commitment.CargoID]; exists {
			return fmt.Errorf(
				"cargo %q has duplicate in-transit commitments",
				commitment.CargoID,
			)
		}
		committedCargo[commitment.CargoID] = struct{}{}
	}
	return nil
}

func validArtifactDigest(value domain.ArtifactDigest) bool {
	decoded, err := hex.DecodeString(string(value))
	return err == nil &&
		len(decoded) == 32 &&
		strings.ToLower(string(value)) == string(value)
}

func validateEnergyMatrix(
	value domain.EnergyMatrix,
	locations map[domain.LocationID]domain.Location,
) error {
	size := len(value.NodeIDs)
	if size == 0 {
		return fmt.Errorf("energy matrix has no nodes")
	}
	if err := validateMatrix("energy matrix", value.NodeIDs, locations); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(value.Profiles))
	for _, profile := range value.Profiles {
		if profile.ProfileID == "" {
			return fmt.Errorf("energy matrix profile id is required")
		}
		if _, exists := seen[profile.ProfileID]; exists {
			return fmt.Errorf("energy matrix has duplicate profile %q", profile.ProfileID)
		}
		seen[profile.ProfileID] = struct{}{}
		if len(profile.BaseWh) != size*size || len(profile.LoadWhPerTonne) != size*size {
			return fmt.Errorf("energy matrix profile %q has invalid cardinality", profile.ProfileID)
		}
	}
	return nil
}

func requiredID(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is required and must not contain surrounding whitespace", name)
	}
	return nil
}

func validRange(name string, value domain.TimeRange) error {
	if value.Start.IsZero() || value.End.IsZero() || !value.Start.Before(value.End) {
		return fmt.Errorf("%s must have a non-empty increasing interval", name)
	}
	return nil
}

func utcRange(value domain.TimeRange) domain.TimeRange {
	return domain.TimeRange{Start: value.Start.UTC(), End: value.End.UTC()}
}

func normalizeRanges(values []domain.TimeRange) []domain.TimeRange {
	values = ensureSlice(values)
	for index := range values {
		values[index] = utcRange(values[index])
	}
	slices.SortFunc(values, func(left, right domain.TimeRange) int {
		return left.Start.Compare(right.Start)
	})
	return values
}

func normalizeStringGroups(values [][]string) [][]string {
	values = ensureSlice(values)
	for index := range values {
		values[index] = normalizeStrings(values[index])
	}
	slices.SortFunc(values, func(left, right []string) int {
		return strings.Compare(strings.Join(left, "\x00"), strings.Join(right, "\x00"))
	})
	return values
}

func normalizeStrings[T ~string](values []T) []T {
	values = ensureSlice(values)
	slices.Sort(values)
	return slices.Compact(values)
}

func normalizeIDs[T ~string](values []T) []T {
	return normalizeStrings(values)
}

func ensureSlice[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
