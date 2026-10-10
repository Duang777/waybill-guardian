package validate

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (state *validationState) validateBindings() {
	if state.problem.SchemaVersion != domain.ProblemSchemaVersion ||
		state.plan.SchemaVersion != domain.PlanSchemaVersion {
		state.add("V001", domain.SeverityError, "plan", ref(state.plan.PlanID),
			"supported problem and plan schemas",
			state.problem.SchemaVersion+" / "+state.plan.SchemaVersion,
			noPosition())
	}
	policyDigest, policyErr := domain.ComputePolicyDigest(state.problem.Policy)
	commitmentDigest, commitmentErr :=
		domain.ComputeCommitmentDigest(state.problem.Commitments)
	problemDigest, problemErr := domain.ComputeProblemDigest(state.problem)
	planDigest, planErr := domain.ComputePlanDigest(state.plan)
	if policyErr != nil || commitmentErr != nil || problemErr != nil || planErr != nil {
		state.add("V004", domain.SeverityError, "plan", ref(state.plan.PlanID),
			"canonical problem and plan", "digest computation failed", noPosition())
		return
	}
	if state.problem.PolicyDigest != policyDigest ||
		state.problem.CommitmentDigest != commitmentDigest ||
		state.problem.ProblemDigest != problemDigest ||
		state.plan.ProblemDigest != state.problem.ProblemDigest ||
		state.plan.PolicyDigest != state.problem.PolicyDigest ||
		state.plan.CommitmentDigest != state.problem.CommitmentDigest ||
		state.plan.PlanDigest != planDigest ||
		!domain.ValidArtifactDigest(state.plan.ConfigDigest) {
		state.add("V001", domain.SeverityError, "plan", ref(state.plan.PlanID),
			"all schema and digest bindings match canonical content",
			"one or more bindings differ", noPosition())
	}

	for taskID, visits := range state.visits {
		if _, exists := state.tasks[taskID]; !exists {
			for _, visit := range visits {
				state.add("V003", domain.SeverityError, "task", ref(taskID),
					"task exists in problem", "unknown task", atStop(
						visit.dutyIndex, visit.tripIndex, visit.stopIndex,
					))
			}
		}
	}
	for dutyIndex, duty := range state.plan.Duties {
		if _, exists := state.vehicles[duty.VehicleID]; !exists {
			state.add("V003", domain.SeverityError, "vehicle", ref(duty.VehicleID),
				"vehicle exists in problem", "unknown vehicle",
				position{duty: dutyIndex, trip: -1, stop: -1, segment: -1})
		}
		for _, driverID := range duty.DriverIDs {
			if _, exists := state.drivers[driverID]; !exists {
				state.add("V003", domain.SeverityError, "driver", ref(driverID),
					"driver exists in problem", "unknown driver",
					position{duty: dutyIndex, trip: -1, stop: -1, segment: -1})
			}
		}
	}
}

func (state *validationState) validateOrderConservation() {
	unassigned := make(map[domain.FulfillmentUnitID]int, len(state.plan.Unassigned))
	for _, value := range state.plan.Unassigned {
		unassigned[value.UnitID]++
		if _, exists := state.units[value.UnitID]; !exists {
			state.add("V103", domain.SeverityError, "fulfillment_unit", ref(value.UnitID),
				"unassigned unit exists in problem", "unknown unit", noPosition())
		}
	}

	for _, request := range state.problem.Requests {
		for _, unitID := range request.UnitIDs {
			requiredTasks := tasksForUnit(request.Tasks, unitID)
			assigned := len(requiredTasks) > 0
			distinctAssignments := make(map[string]struct{})
			for _, task := range requiredTasks {
				visits := state.visits[task.ID]
				if len(visits) != 1 {
					assigned = false
				}
				for _, visit := range visits {
					key := fmt.Sprintf("%d/%d/%s", visit.dutyIndex, visit.tripIndex, visit.vehicleID)
					distinctAssignments[key] = struct{}{}
				}
			}
			if request.Required && !assigned {
				state.add("V101", domain.SeverityError, "fulfillment_unit", ref(unitID),
					"every required task occurs exactly once", "coverage is incomplete",
					noPosition())
			}
			if assigned && unassigned[unitID] > 0 {
				state.add("V103", domain.SeverityError, "fulfillment_unit", ref(unitID),
					"assigned or unassigned, not both", "unit appears in both sets",
					noPosition())
			}
			if !assigned && unassigned[unitID] != 1 {
				state.add("V103", domain.SeverityError, "fulfillment_unit", ref(unitID),
					"one unassigned reason for uncovered unit",
					fmt.Sprintf("%d reasons", unassigned[unitID]), noPosition())
			}
			if request.Required && unassigned[unitID] > 0 {
				state.add("V102", domain.SeverityError, "fulfillment_unit", ref(unitID),
					"required unit is assigned", "required unit is unassigned", noPosition())
			}
			if request.Split.Mode == domain.SplitForbidden && len(distinctAssignments) > 1 {
				state.add("V104", domain.SeverityError, "fulfillment_unit", ref(unitID),
					"non-splittable unit stays in one trip",
					fmt.Sprintf("%d assignments", len(distinctAssignments)), noPosition())
			}
		}
		state.validateRequestSplits(request)
	}
}

type assignmentKey struct {
	dutyIndex int
	tripIndex int
	vehicleID domain.VehicleID
}

func (state *validationState) validateRequestSplits(request domain.TransportRequest) {
	assignments := make(map[assignmentKey]map[domain.FulfillmentUnitID]struct{})
	atomicAssignments := make(map[string]assignmentKey)
	for _, unitID := range request.UnitIDs {
		tasks := tasksForUnit(request.Tasks, unitID)
		var key assignmentKey
		complete := len(tasks) > 0
		for taskIndex, task := range tasks {
			visits := state.visits[task.ID]
			if len(visits) != 1 {
				complete = false
				break
			}
			candidate := assignmentKey{
				dutyIndex: visits[0].dutyIndex,
				tripIndex: visits[0].tripIndex,
				vehicleID: visits[0].vehicleID,
			}
			if taskIndex == 0 {
				key = candidate
			} else if candidate != key {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		if assignments[key] == nil {
			assignments[key] = make(map[domain.FulfillmentUnitID]struct{})
		}
		assignments[key][unitID] = struct{}{}
		unit := state.units[unitID]
		if unit.AtomicGroupID != "" {
			if existing, exists := atomicAssignments[unit.AtomicGroupID]; exists &&
				existing != key {
				state.add("V105", domain.SeverityError, "fulfillment_unit", ref(unitID),
					"atomic fulfillment group stays in one assignment",
					"atomic group was split", noPosition())
			} else {
				atomicAssignments[unit.AtomicGroupID] = key
			}
		}
	}
	if request.Split.MaxSplits > 0 && len(assignments) > int(request.Split.MaxSplits) {
		state.add("V105", domain.SeverityError, "request", ref(request.ID),
			fmt.Sprintf("at most %d splits", request.Split.MaxSplits),
			fmt.Sprintf("%d splits", len(assignments)), noPosition())
	}
	if request.Split.Mode == domain.SplitForbidden && len(assignments) > 1 {
		state.add("V105", domain.SeverityError, "request", ref(request.ID),
			"non-splittable request uses one assignment", "multiple assignments", noPosition())
	}
	for _, units := range assignments {
		if request.Split.MinUnitsPerSplit > 0 &&
			len(units) < int(request.Split.MinUnitsPerSplit) {
			state.add("V105", domain.SeverityError, "request", ref(request.ID),
				fmt.Sprintf("at least %d units per split", request.Split.MinUnitsPerSplit),
				fmt.Sprintf("%d units", len(units)), noPosition())
		}
	}
	if request.Split.SameVehicle {
		var vehicleID domain.VehicleID
		for key := range assignments {
			if vehicleID == "" {
				vehicleID = key.vehicleID
			} else if key.vehicleID != vehicleID {
				state.add("V105", domain.SeverityError, "request", ref(request.ID),
					"all request units use one vehicle", "multiple vehicles", noPosition())
				break
			}
		}
	}
	if request.Split.SameTrip && len(assignments) > 1 {
		state.add("V105", domain.SeverityError, "request", ref(request.ID),
			"all request units use one trip", "multiple trips", noPosition())
	}
}

func tasksForUnit(
	tasks []domain.ServiceTask,
	unitID domain.FulfillmentUnitID,
) []domain.ServiceTask {
	result := make([]domain.ServiceTask, 0)
	for _, task := range tasks {
		if slices.Contains(task.UnitIDs, unitID) {
			result = append(result, task)
		}
	}
	return result
}

func (state *validationState) validatePickupDelivery() {
	for _, definition := range state.tasks {
		currentVisits := state.visits[definition.task.ID]
		for _, predecessorID := range definition.task.PredecessorIDs {
			predecessorVisits := state.visits[predecessorID]
			if len(currentVisits) != 1 || len(predecessorVisits) != 1 {
				continue
			}
			current := currentVisits[0]
			predecessor := predecessorVisits[0]
			if !visitBefore(predecessor, current) {
				state.add("V201", domain.SeverityError, "task", ref(definition.task.ID),
					"all predecessors occur first", "predecessor order is violated",
					atStop(current.dutyIndex, current.tripIndex, current.stopIndex),
					domain.ObjectRef{Kind: "task", ID: string(predecessorID)})
			}
			if definition.request.Split.SameVehicle &&
				predecessor.vehicleID != current.vehicleID {
				state.add("V202", domain.SeverityError, "task", ref(definition.task.ID),
					"pickup and delivery use the same vehicle",
					string(predecessor.vehicleID)+" / "+string(current.vehicleID),
					atStop(current.dutyIndex, current.tripIndex, current.stopIndex))
			}
			if definition.request.Split.SameTrip &&
				(predecessor.dutyIndex != current.dutyIndex ||
					predecessor.tripIndex != current.tripIndex) {
				state.add("V203", domain.SeverityError, "task", ref(definition.task.ID),
					"pickup and delivery use the same trip", "different trips",
					atStop(current.dutyIndex, current.tripIndex, current.stopIndex))
			}
			if definition.task.MaxRideSeconds > 0 {
				rideSeconds := durationSeconds(
					predecessor.stop.DepartureAt,
					current.stop.ServiceAt,
				)
				if rideSeconds > definition.task.MaxRideSeconds {
					state.add("V204", domain.SeverityError, "task", ref(definition.task.ID),
						formatInt(definition.task.MaxRideSeconds),
						formatInt(rideSeconds),
						atStop(current.dutyIndex, current.tripIndex, current.stopIndex))
				}
			}
		}
	}
}

func visitBefore(left, right taskVisit) bool {
	if left.dutyIndex != right.dutyIndex {
		return left.stop.ServiceAt.Before(right.stop.ServiceAt)
	}
	if left.tripIndex != right.tripIndex {
		return left.tripIndex < right.tripIndex
	}
	return left.stopIndex < right.stopIndex
}

func (state *validationState) validateResources() {
	for dutyIndex, duty := range state.plan.Duties {
		vehicle, vehicleExists := state.vehicles[duty.VehicleID]
		if !vehicleExists {
			state.add("V301", domain.SeverityError, "vehicle", ref(duty.VehicleID),
				"known vehicle", "unknown vehicle",
				position{duty: dutyIndex, trip: -1, stop: -1, segment: -1})
			continue
		}
		if len(duty.Trips) > int(vehicle.MaxTrips) {
			state.add("V304", domain.SeverityError, "vehicle", ref(duty.VehicleID),
				fmt.Sprintf("at most %d trips", vehicle.MaxTrips),
				fmt.Sprintf("%d trips", len(duty.Trips)),
				position{duty: dutyIndex, trip: -1, stop: -1, segment: -1})
		}
		driverSet := make(map[domain.DriverID]domain.Driver, len(duty.DriverIDs))
		for _, driverID := range duty.DriverIDs {
			driver, exists := state.drivers[driverID]
			if !exists {
				state.add("V302", domain.SeverityError, "driver", ref(driverID),
					"known eligible driver", "unknown driver",
					position{duty: dutyIndex, trip: -1, stop: -1, segment: -1})
				continue
			}
			driverSet[driverID] = driver
		}
		for tripIndex, trip := range duty.Trips {
			tripRange := domain.TimeRange{Start: trip.StartAt, End: trip.EndAt}
			if !withinAnyRange(vehicle.Availability, tripRange) {
				state.add("V304", domain.SeverityError, "vehicle", ref(duty.VehicleID),
					"trip is within vehicle availability", "trip is outside availability",
					position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
			}
			for stopIndex, stop := range trip.Stops {
				for _, taskID := range stop.TaskIDs {
					definition, exists := state.tasks[taskID]
					if !exists {
						continue
					}
					required := append(
						append(domain.SkillSet(nil), definition.request.RequiredSkills...),
						definition.task.RequiredSkills...,
					)
					if !containsAllSkills(vehicle.Skills, required) {
						state.add("V303", domain.SeverityError, "vehicle", ref(duty.VehicleID),
							"vehicle has all task skills", strings.Join(required, ","),
							atStop(dutyIndex, tripIndex, stopIndex))
					}
					for driverID, driver := range driverSet {
						if !containsAllSkills(driver.Skills, required) {
							state.add("V303", domain.SeverityError, "driver", ref(driverID),
								"driver has all task skills", strings.Join(required, ","),
								atStop(dutyIndex, tripIndex, stopIndex))
						}
					}
				}
			}
			state.validateCompartmentCompatibility(dutyIndex, tripIndex, vehicle, trip)
		}
	}
}

func withinAnyRange(values []domain.TimeRange, wanted domain.TimeRange) bool {
	for _, value := range values {
		if value.ContainsRange(wanted) {
			return true
		}
	}
	return false
}

func (state *validationState) validateCompartmentCompatibility(
	dutyIndex int,
	tripIndex int,
	vehicle domain.Vehicle,
	trip domain.Trip,
) {
	compartments := make(map[domain.CompartmentID]domain.Compartment, len(vehicle.Compartments))
	for _, value := range vehicle.Compartments {
		compartments[value.ID] = value
	}
	for _, stage := range trip.LoadStages {
		cargoByCompartment := make(map[domain.CompartmentID][]domain.CargoItem)
		for _, placement := range stage.Placements {
			cargo, cargoExists := state.cargo[placement.CargoID]
			compartment, compartmentExists := compartments[placement.CompartmentID]
			if !cargoExists || !compartmentExists {
				continue
			}
			if !slices.Contains(compartment.TemperatureZones, cargo.TemperatureZone) ||
				!slices.Contains(compartment.AllowedCargoClasses, cargo.CargoClass) {
				state.add("V305", domain.SeverityError, "cargo", ref(cargo.ID),
					"cargo is compatible with compartment",
					string(compartment.ID),
					atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
			}
			cargoByCompartment[compartment.ID] =
				append(cargoByCompartment[compartment.ID], cargo)
		}
		for compartmentID, compartmentCargo := range cargoByCompartment {
			classes := make([]string, 0, len(compartmentCargo))
			incompatible := false
			for _, cargo := range compartmentCargo {
				classes = append(classes, cargo.CargoClass)
			}
			for left := 0; left < len(compartmentCargo); left++ {
				for right := left + 1; right < len(compartmentCargo); right++ {
					if slices.Contains(
						compartmentCargo[left].IncompatibleClasses,
						compartmentCargo[right].CargoClass,
					) || slices.Contains(
						compartmentCargo[right].IncompatibleClasses,
						compartmentCargo[left].CargoClass,
					) {
						incompatible = true
					}
				}
			}
			if !mixedClassesAllowed(classes, state.problem.Policy.AllowedMixedCargoClasses) {
				incompatible = true
			}
			if incompatible {
				state.add("V306", domain.SeverityError, "compartment", ref(compartmentID),
					"cargo classes are compatible", strings.Join(classes, ","),
					atStop(dutyIndex, tripIndex, int(stage.AfterStopIndex)))
			}
		}
	}
}

func mixedClassesAllowed(classes []string, allowed [][]string) bool {
	classes = slices.Compact(slices.Sorted(slices.Values(classes)))
	if len(classes) <= 1 {
		return true
	}
	for _, group := range allowed {
		if len(group) != len(classes) {
			continue
		}
		candidate := slices.Compact(slices.Sorted(slices.Values(group)))
		if slices.Equal(candidate, classes) {
			return true
		}
	}
	return false
}

func (state *validationState) validateRoutes() {
	for dutyIndex, duty := range state.plan.Duties {
		for tripIndex, trip := range duty.Trips {
			startDepot, startExists := state.depots[trip.StartDepotID]
			endDepot, endExists := state.depots[trip.EndDepotID]
			if !startExists || !endExists || !startDepot.AllowTripStart || !endDepot.AllowTripEnd ||
				len(trip.Stops) == 0 ||
				trip.Stops[0].LocationID != startDepot.LocationID ||
				trip.Stops[len(trip.Stops)-1].LocationID != endDepot.LocationID {
				state.add("V401", domain.SeverityError, "trip", ref(trip.ID),
					"trip starts and ends at allowed depots", "depot boundary mismatch",
					position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
			}
			for stopIndex, stop := range trip.Stops {
				if _, exists := state.locations[stop.LocationID]; !exists {
					state.add("V402", domain.SeverityError, "location", ref(stop.LocationID),
						"known matrix location", "unknown location",
						atStop(dutyIndex, tripIndex, stopIndex))
				}
				for _, taskID := range stop.TaskIDs {
					definition, exists := state.tasks[taskID]
					if exists && definition.task.LocationID != stop.LocationID {
						state.add("V401", domain.SeverityError, "task", ref(taskID),
							string(definition.task.LocationID), string(stop.LocationID),
							atStop(dutyIndex, tripIndex, stopIndex))
					}
				}
				if stopIndex > 0 {
					if _, exists := state.matrixValue(
						state.problem.Travel.TravelSeconds,
						trip.Stops[stopIndex-1].LocationID,
						stop.LocationID,
					); !exists {
						state.add("V402", domain.SeverityError, "trip", ref(trip.ID),
							"matrix arc exists", "matrix arc is missing",
							atStop(dutyIndex, tripIndex, stopIndex))
					}
				}
			}
			for segmentIndex, segment := range trip.Schedule {
				if segment.Kind != domain.SegmentDrive {
					continue
				}
				if _, exists := state.matrixValue(
					state.problem.Travel.TravelSeconds, segment.From, segment.To,
				); !exists {
					state.add("V402", domain.SeverityError, "trip", ref(trip.ID),
						"drive segment uses a matrix arc", "matrix arc is missing",
						atSegment(dutyIndex, tripIndex, segmentIndex))
				}
			}
			if tripIndex > 0 {
				previous := duty.Trips[tripIndex-1]
				if previous.EndDepotID != trip.StartDepotID ||
					trip.StartAt.Before(previous.EndAt) {
					state.add("V403", domain.SeverityError, "trip", ref(trip.ID),
						"vehicle location and time are continuous across trips",
						"trip continuity is broken",
						position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
				}
			}
		}
	}
}
