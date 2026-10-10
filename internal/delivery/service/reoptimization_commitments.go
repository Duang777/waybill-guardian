package service

import (
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type protectedExecutionClosure struct {
	requests map[domain.RequestID]struct{}
	tasks    map[domain.TaskID]struct{}
	units    map[domain.FulfillmentUnitID]struct{}
	cargo    map[domain.CargoID]struct{}
	vehicles map[domain.VehicleID]struct{}
	drivers  map[domain.DriverID]struct{}
	chargers map[domain.ChargerID]struct{}
}

func deriveCommitments(
	problem domain.ProblemSnapshot,
	active domain.Plan,
	at time.Time,
	guardian map[domain.TaskID]domain.FrozenTaskCommitment,
	projectedETA map[domain.TaskID]time.Time,
	unloaded map[domain.CargoID]struct{},
) domain.CommitmentSet {
	result := problem.Commitments
	result.BasePlanDigest = active.PlanDigest
	result.Frozen = []domain.FrozenTaskCommitment{}
	result.InTransit = append(
		[]domain.InTransitCargoCommitment(nil),
		problem.Commitments.InTransit...)
	result.Soft = []domain.SoftTaskCommitment{}
	result.SoftCargo = []domain.SoftCargoCommitment{}
	result.FreezeOverride = nil
	executed := make(map[domain.TaskID]struct{}, len(result.Executed))
	for _, commitment := range result.Executed {
		executed[commitment.TaskID] = struct{}{}
	}
	frozen := make(map[domain.TaskID]domain.FrozenTaskCommitment, len(guardian))
	for taskID, commitment := range guardian {
		if _, done := executed[taskID]; !done && problemHasTask(problem, taskID) {
			frozen[taskID] = commitment
		}
	}
	soft := make(map[domain.TaskID]domain.SoftTaskCommitment)
	type cargoStage struct {
		cargoID domain.CargoID
		stage   uint32
	}
	softCargo := make(map[cargoStage]domain.SoftCargoCommitment)
	inTransit := make(map[domain.CargoID]domain.InTransitCargoCommitment)
	for _, commitment := range result.InTransit {
		if _, delivered := unloaded[commitment.CargoID]; !delivered &&
			problemHasCargo(problem, commitment.CargoID) {
			inTransit[commitment.CargoID] = commitment
		}
	}
	freezeUntil := at.Add(time.Duration(problem.Policy.FreezeWindowSeconds) * time.Second)
	for _, duty := range active.Duties {
		for _, trip := range duty.Trips {
			driverID := firstDriver(duty.DriverIDs)
			for stopIndex, stop := range trip.Stops {
				for _, taskID := range stop.TaskIDs {
					if !problemHasTask(problem, taskID) {
						continue
					}
					if _, done := executed[taskID]; done {
						delete(frozen, taskID)
						continue
					}
					taskDriver := driverForTask(trip.Schedule, taskID, driverID)
					if _, fixed := frozen[taskID]; !fixed && !stop.ServiceAt.After(freezeUntil) {
						frozen[taskID] = domain.FrozenTaskCommitment{
							TaskID:            taskID,
							VehicleID:         duty.VehicleID,
							DriverID:          taskDriver,
							Sequence:          uint32(stopIndex),
							PromisedServiceAt: stop.ServiceAt.UTC(),
							ToleranceSeconds:  problem.Policy.ETAToleranceSeconds,
						}
					}
					plannedAt := stop.ServiceAt.UTC()
					if projected, exists := projectedETA[taskID]; exists {
						plannedAt = projected
					}
					if _, fixed := frozen[taskID]; !fixed || projectedETA[taskID] != (time.Time{}) {
						soft[taskID] = domain.SoftTaskCommitment{
							TaskID:           taskID,
							VehicleID:        duty.VehicleID,
							DriverID:         taskDriver,
							Sequence:         uint32(stopIndex),
							PlannedServiceAt: plannedAt,
						}
					}
				}
			}
			for _, loadStage := range trip.LoadStages {
				for _, placement := range loadStage.Placements {
					if !problemHasCargo(problem, placement.CargoID) {
						continue
					}
					key := cargoStage{cargoID: placement.CargoID, stage: loadStage.AfterStopIndex}
					if _, exists := softCargo[key]; !exists {
						softCargo[key] = domain.SoftCargoCommitment{
							CargoID:        placement.CargoID,
							VehicleID:      duty.VehicleID,
							AfterStopIndex: loadStage.AfterStopIndex,
							CompartmentID:  placement.CompartmentID,
							DoorID:         placement.DoorID,
							PositionMM:     placement.PositionMM,
							Orientation:    placement.Orientation,
						}
					}
				}
			}
			stage, exists := loadStageAt(trip, at)
			if !exists {
				continue
			}
			for _, placement := range stage.Placements {
				if _, delivered := unloaded[placement.CargoID]; delivered {
					continue
				}
				if !problemHasCargo(problem, placement.CargoID) {
					continue
				}
				if _, fixed := inTransit[placement.CargoID]; !fixed {
					inTransit[placement.CargoID] = domain.InTransitCargoCommitment{
						CargoID:       placement.CargoID,
						VehicleID:     duty.VehicleID,
						CompartmentID: placement.CompartmentID,
					}
				}
			}
		}
	}
	for _, commitment := range frozen {
		result.Frozen = append(result.Frozen, commitment)
	}
	for _, commitment := range soft {
		result.Soft = append(result.Soft, commitment)
	}
	for _, commitment := range softCargo {
		result.SoftCargo = append(result.SoftCargo, commitment)
	}
	result.InTransit = result.InTransit[:0]
	for _, commitment := range inTransit {
		result.InTransit = append(result.InTransit, commitment)
	}
	return result
}

func buildProtectedExecutionClosure(
	problem domain.ProblemSnapshot,
	active domain.Plan,
	at time.Time,
) protectedExecutionClosure {
	result := protectedExecutionClosure{
		requests: make(map[domain.RequestID]struct{}),
		tasks:    make(map[domain.TaskID]struct{}),
		units:    make(map[domain.FulfillmentUnitID]struct{}),
		cargo:    make(map[domain.CargoID]struct{}),
		vehicles: make(map[domain.VehicleID]struct{}),
		drivers:  make(map[domain.DriverID]struct{}),
		chargers: make(map[domain.ChargerID]struct{}),
	}
	taskDefinitions := make(map[domain.TaskID]domain.ServiceTask)
	taskRequests := make(map[domain.TaskID]domain.RequestID)
	unitRequests := make(map[domain.FulfillmentUnitID]domain.RequestID)
	cargoUnits := make(map[domain.CargoID]domain.FulfillmentUnitID)
	for _, request := range problem.Requests {
		for _, task := range request.Tasks {
			taskDefinitions[task.ID] = task
			taskRequests[task.ID] = request.ID
		}
	}
	for _, unit := range problem.Units {
		unitRequests[unit.ID] = unit.RequestID
		for _, cargoID := range unit.CargoIDs {
			cargoUnits[cargoID] = unit.ID
		}
	}
	protectTask := func(taskID domain.TaskID) {
		result.tasks[taskID] = struct{}{}
		if requestID, exists := taskRequests[taskID]; exists {
			result.requests[requestID] = struct{}{}
		}
		if task, exists := taskDefinitions[taskID]; exists {
			for _, unitID := range task.UnitIDs {
				result.units[unitID] = struct{}{}
			}
		}
	}
	for _, commitment := range problem.Commitments.Executed {
		protectTask(commitment.TaskID)
		result.vehicles[commitment.VehicleID] = struct{}{}
		result.drivers[commitment.DriverID] = struct{}{}
		task := taskDefinitions[commitment.TaskID]
		if task.Kind == domain.TaskPickup || task.Kind == domain.TaskDepotLoad {
			protectRequestGraph(&result, problem, taskRequests[commitment.TaskID])
		}
	}
	for _, commitment := range problem.Commitments.Frozen {
		protectTask(commitment.TaskID)
	}
	for _, commitment := range problem.Commitments.InTransit {
		result.cargo[commitment.CargoID] = struct{}{}
		result.vehicles[commitment.VehicleID] = struct{}{}
		if unitID, exists := cargoUnits[commitment.CargoID]; exists {
			result.units[unitID] = struct{}{}
			if requestID, requestExists := unitRequests[unitID]; requestExists {
				protectRequestGraph(&result, problem, requestID)
			}
		}
	}
	for _, duty := range active.Duties {
		for _, trip := range duty.Trips {
			for _, stop := range trip.Stops {
				completed := !stop.DepartureAt.After(at)
				current := !stop.ArrivalAt.After(at) && !stop.DepartureAt.Before(at)
				if !completed && !current {
					continue
				}
				result.vehicles[duty.VehicleID] = struct{}{}
				for _, driverID := range duty.DriverIDs {
					result.drivers[driverID] = struct{}{}
				}
				for _, taskID := range stop.TaskIDs {
					protectTask(taskID)
					task := taskDefinitions[taskID]
					if task.Kind == domain.TaskPickup || task.Kind == domain.TaskDepotLoad {
						protectRequestGraph(&result, problem, taskRequests[taskID])
					}
				}
			}
			for _, segment := range trip.Schedule {
				if segment.Kind == domain.SegmentCharge && !segment.StartAt.After(at) &&
					segment.EndAt.After(at) {
					result.chargers[segment.ChargerID] = struct{}{}
				}
			}
		}
	}
	changed := true
	for changed {
		changed = false
		for taskID := range result.tasks {
			for _, predecessorID := range taskDefinitions[taskID].PredecessorIDs {
				if _, exists := result.tasks[predecessorID]; !exists {
					protectTask(predecessorID)
					changed = true
				}
			}
		}
	}
	for unitID := range result.units {
		for _, unit := range problem.Units {
			if unit.ID != unitID {
				continue
			}
			for _, cargoID := range unit.CargoIDs {
				result.cargo[cargoID] = struct{}{}
			}
		}
	}
	return result
}

func protectRequestGraph(
	closure *protectedExecutionClosure,
	problem domain.ProblemSnapshot,
	requestID domain.RequestID,
) {
	if requestID == "" {
		return
	}
	closure.requests[requestID] = struct{}{}
	for _, request := range problem.Requests {
		if request.ID != requestID {
			continue
		}
		for _, task := range request.Tasks {
			closure.tasks[task.ID] = struct{}{}
			for _, unitID := range task.UnitIDs {
				closure.units[unitID] = struct{}{}
			}
		}
		for _, unitID := range request.UnitIDs {
			closure.units[unitID] = struct{}{}
		}
	}
	for _, unit := range problem.Units {
		if unit.RequestID != requestID {
			continue
		}
		closure.units[unit.ID] = struct{}{}
		for _, cargoID := range unit.CargoIDs {
			closure.cargo[cargoID] = struct{}{}
		}
	}
}

func (closure protectedExecutionClosure) objectsForRequest(
	problem domain.ProblemSnapshot,
	requestID domain.RequestID,
) []domain.ObjectRef {
	objects := []domain.ObjectRef{}
	if _, exists := closure.requests[requestID]; exists {
		objects = append(objects, domain.ObjectRef{Kind: "request", ID: string(requestID)})
	}
	for _, request := range problem.Requests {
		if request.ID != requestID {
			continue
		}
		for _, task := range request.Tasks {
			if _, exists := closure.tasks[task.ID]; exists {
				objects = append(objects, domain.ObjectRef{Kind: "task", ID: string(task.ID)})
			}
		}
		for _, unitID := range request.UnitIDs {
			if _, exists := closure.units[unitID]; exists {
				objects = append(objects, domain.ObjectRef{Kind: "unit", ID: string(unitID)})
			}
		}
	}
	for _, unit := range problem.Units {
		if unit.RequestID != requestID {
			continue
		}
		for _, cargoID := range unit.CargoIDs {
			if _, exists := closure.cargo[cargoID]; exists {
				objects = append(objects, domain.ObjectRef{Kind: "cargo", ID: string(cargoID)})
			}
		}
	}
	slices.SortFunc(objects, func(left, right domain.ObjectRef) int {
		if result := strings.Compare(left.Kind, right.Kind); result != 0 {
			return result
		}
		return strings.Compare(left.ID, right.ID)
	})
	return slices.Compact(objects)
}

func indexFrozenCommitments(
	values []domain.FrozenTaskCommitment,
) map[domain.TaskID]domain.FrozenTaskCommitment {
	result := make(map[domain.TaskID]domain.FrozenTaskCommitment, len(values))
	for _, value := range values {
		result[value.TaskID] = value
	}
	return result
}

func activeAssignmentMatches(
	active domain.Plan,
	taskID domain.TaskID,
	vehicleID domain.VehicleID,
	driverID domain.DriverID,
) bool {
	for _, duty := range active.Duties {
		if duty.VehicleID != vehicleID {
			continue
		}
		for _, trip := range duty.Trips {
			for _, stop := range trip.Stops {
				if !slices.Contains(stop.TaskIDs, taskID) {
					continue
				}
				return slices.Contains(duty.DriverIDs, driverID) ||
					driverForTask(trip.Schedule, taskID, "") == driverID
			}
		}
	}
	return false
}

func problemHasTask(problem domain.ProblemSnapshot, taskID domain.TaskID) bool {
	for _, request := range problem.Requests {
		for _, task := range request.Tasks {
			if task.ID == taskID {
				return true
			}
		}
	}
	return false
}

func problemHasCargo(problem domain.ProblemSnapshot, cargoID domain.CargoID) bool {
	for _, cargo := range problem.Cargo {
		if cargo.ID == cargoID {
			return true
		}
	}
	return false
}

func problemHasVehicle(problem domain.ProblemSnapshot, vehicleID domain.VehicleID) bool {
	for _, vehicle := range problem.Vehicles {
		if vehicle.ID == vehicleID {
			return true
		}
	}
	return false
}

func problemHasDriver(problem domain.ProblemSnapshot, driverID domain.DriverID) bool {
	for _, driver := range problem.Drivers {
		if driver.ID == driverID {
			return true
		}
	}
	return false
}

func cargoUnloadedByTask(
	problem domain.ProblemSnapshot,
	taskID domain.TaskID,
) ([]domain.CargoID, bool) {
	unitIDs := make(map[domain.FulfillmentUnitID]struct{})
	for _, request := range problem.Requests {
		for _, task := range request.Tasks {
			if task.ID != taskID {
				continue
			}
			if task.Kind != domain.TaskDelivery &&
				task.Kind != domain.TaskDepotUnload {
				return nil, false
			}
			for _, unitID := range task.UnitIDs {
				unitIDs[unitID] = struct{}{}
			}
		}
	}
	cargoIDs := make([]domain.CargoID, 0)
	for _, unit := range problem.Units {
		if _, unload := unitIDs[unit.ID]; unload {
			cargoIDs = append(cargoIDs, unit.CargoIDs...)
		}
	}
	slices.Sort(cargoIDs)
	return slices.Compact(cargoIDs), true
}

func firstDriver(values []domain.DriverID) domain.DriverID {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func driverForTask(
	schedule []domain.DutySegment,
	taskID domain.TaskID,
	fallback domain.DriverID,
) domain.DriverID {
	for _, segment := range schedule {
		if slices.Contains(segment.TaskIDs, taskID) {
			return segment.DriverID
		}
	}
	return fallback
}

func loadStageAt(trip domain.Trip, at time.Time) (domain.LoadStage, bool) {
	var result domain.LoadStage
	found := false
	for _, stage := range trip.LoadStages {
		if int(stage.AfterStopIndex) >= len(trip.Stops) ||
			trip.Stops[stage.AfterStopIndex].DepartureAt.After(at) {
			continue
		}
		if !found || stage.AfterStopIndex > result.AfterStopIndex {
			result = stage
			found = true
		}
	}
	return result, found
}
