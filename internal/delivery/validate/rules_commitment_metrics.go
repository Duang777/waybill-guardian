package validate

import (
	"fmt"
	"slices"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (state *validationState) validateCommitments() {
	for _, commitment := range state.problem.Commitments.Executed {
		visit, exists := state.singleVisit(commitment.TaskID)
		if !exists ||
			visit.vehicleID != commitment.VehicleID ||
			!slices.Contains(visit.driverIDs, commitment.DriverID) ||
			visit.stop.ServiceAt.After(commitment.CompletedAt) {
			state.add("V1101", domain.SeverityError, "task", ref(commitment.TaskID),
				"executed task assignment and completion are immutable",
				"plan rewrites an executed task", noPosition())
		}
	}
	for _, commitment := range state.problem.Commitments.Frozen {
		visit, exists := state.singleVisit(commitment.TaskID)
		if !exists || !state.frozenCommitmentAllows(commitment, visit) {
			state.add("V1101", domain.SeverityError, "task", ref(commitment.TaskID),
				"frozen assignment, sequence, and ETA remain within tolerance",
				"frozen commitment changed", noPosition())
		}
	}
	for _, commitment := range state.problem.Commitments.InTransit {
		found := false
		for _, duty := range state.plan.Duties {
			if duty.VehicleID != commitment.VehicleID {
				continue
			}
			for _, trip := range duty.Trips {
				if len(trip.LoadStages) == 0 {
					continue
				}
				for _, placement := range trip.LoadStages[0].Placements {
					if placement.CargoID == commitment.CargoID &&
						placement.CompartmentID == commitment.CompartmentID {
						found = true
					}
				}
			}
		}
		if !found {
			state.add("V1102", domain.SeverityError, "cargo", ref(commitment.CargoID),
				"in-transit cargo stays on committed vehicle and compartment",
				"cargo commitment is missing", noPosition())
		}
	}
}

func (state *validationState) frozenCommitmentAllows(
	commitment domain.FrozenTaskCommitment,
	visit taskVisit,
) bool {
	vehicleAllowed := visit.vehicleID == commitment.VehicleID
	driverAllowed := slices.Contains(visit.driverIDs, commitment.DriverID)
	maxSequenceShift := uint32(0)
	maxETADriftSeconds := commitment.ToleranceSeconds
	if scope, exists := state.freezeOverrideScope(commitment.TaskID); exists {
		vehicleAllowed = vehicleAllowed ||
			(scope.AllowVehicleChange &&
				slices.Contains(scope.AllowedVehicleIDs, visit.vehicleID))
		if !driverAllowed && scope.AllowDriverChange {
			for _, driverID := range visit.driverIDs {
				if slices.Contains(scope.AllowedDriverIDs, driverID) {
					driverAllowed = true
					break
				}
			}
		}
		maxSequenceShift = scope.MaxSequenceShift
		if scope.MaxETADriftSeconds > maxETADriftSeconds {
			maxETADriftSeconds = scope.MaxETADriftSeconds
		}
	}
	return vehicleAllowed &&
		driverAllowed &&
		sequenceShift(commitment.Sequence, visit.stopIndex) <= maxSequenceShift &&
		absoluteSeconds(visit.stop.ServiceAt.Sub(commitment.PromisedServiceAt)) <=
			maxETADriftSeconds
}

func (state *validationState) freezeOverrideScope(
	taskID domain.TaskID,
) (domain.FreezeOverrideScope, bool) {
	if state.problem.Commitments.FreezeOverride == nil {
		return domain.FreezeOverrideScope{}, false
	}
	for _, scope := range state.problem.Commitments.FreezeOverride.Scopes {
		if scope.TaskID == taskID {
			return scope, true
		}
	}
	return domain.FreezeOverrideScope{}, false
}

func sequenceShift(before uint32, after int) uint32 {
	if after < 0 {
		return ^uint32(0)
	}
	current := uint64(after)
	expected := uint64(before)
	if current >= expected {
		difference := current - expected
		if difference > uint64(^uint32(0)) {
			return ^uint32(0)
		}
		return uint32(difference)
	}
	return uint32(expected - current)
}

func (state *validationState) singleVisit(taskID domain.TaskID) (taskVisit, bool) {
	visits := state.visits[taskID]
	if len(visits) != 1 {
		return taskVisit{}, false
	}
	return visits[0], true
}

func absoluteSeconds(value time.Duration) int64 {
	seconds := int64(value / time.Second)
	if seconds < 0 {
		return -seconds
	}
	return seconds
}

func (state *validationState) validateMetrics() {
	metrics := state.recomputeMetrics()
	state.metrics = metrics
	if metrics != state.plan.Metrics {
		state.add("V1201", domain.SeverityError, "plan", ref(state.plan.PlanID),
			fmt.Sprintf("%+v", metrics), fmt.Sprintf("%+v", state.plan.Metrics), noPosition())
	}
	objective := domain.ObjectiveVector{
		UnassignedRequiredUnits: state.unassignedRequiredUnits(),
		HardViolationCount:      uint32(state.errorCount()),
		VehiclesUsed:            metrics.VehiclesUsed,
		TotalCostCents:          metrics.TotalCostCents,
		TotalDistanceMeters:     metrics.TotalDistanceMeters,
		TotalWaitSeconds:        metrics.TotalWaitSeconds,
		NegativeMinVolumePPM:    -metrics.MinVolumeUtilizationPPM,
		StabilityCostCents:      metrics.StabilityCostCents,
	}
	if objective != state.plan.Objective {
		state.add("V1202", domain.SeverityError, "plan", ref(state.plan.PlanID),
			fmt.Sprintf("%+v", objective), fmt.Sprintf("%+v", state.plan.Objective), noPosition())
	}
}

func (state *validationState) recomputeMetrics() domain.PlanMetrics {
	var metrics domain.PlanMetrics
	assigned := make(map[domain.FulfillmentUnitID]struct{})
	for _, request := range state.problem.Requests {
		for _, unitID := range request.UnitIDs {
			complete := true
			for _, task := range tasksForUnit(request.Tasks, unitID) {
				if len(state.visits[task.ID]) != 1 {
					complete = false
				}
			}
			if complete {
				assigned[unitID] = struct{}{}
			}
		}
	}
	metrics.AssignedUnits = uint32(len(assigned))
	metrics.UnassignedUnits = uint32(len(state.plan.Unassigned))
	unassignedRequests := make(map[domain.RequestID]bool)
	for _, value := range state.plan.Unassigned {
		unit, unitExists := state.units[value.UnitID]
		request, requestExists := state.requests[unit.RequestID]
		if !unitExists || !requestExists {
			continue
		}
		unassignedRequests[request.ID] = request.Required
	}
	for _, required := range unassignedRequests {
		if required {
			metrics.TotalCostCents += state.problem.Policy.RequiredOrderPenaltyCents
		} else {
			metrics.TotalCostCents += state.problem.Policy.OptionalOrderPenaltyCents
		}
	}

	volumeUtilizations := make([]int64, 0)
	payloadUtilizations := make([]int64, 0)
	for _, duty := range state.plan.Duties {
		vehicle, exists := state.vehicles[duty.VehicleID]
		if !exists || len(duty.Trips) == 0 {
			continue
		}
		metrics.VehiclesUsed++
		metrics.TotalCostCents += vehicle.FixedCostCents
		var maxVolume, maxPayload int64
		var capacityVolume, capacityPayload int64
		var dutyEnergyWh int64
		for _, compartment := range vehicle.Compartments {
			capacityVolume += compartment.Bounds.Size.VolumeMM3()
			capacityPayload += compartment.MaxPayloadG
		}
		for _, trip := range duty.Trips {
			metrics.Trips++
			metrics.Stops += uint32(len(trip.Stops))
			dutySeconds := durationSeconds(trip.StartAt, trip.EndAt)
			if dutySeconds > 0 {
				metrics.TotalCostCents +=
					dutySeconds * vehicle.WorkCostCPH / 3_600
			}
			for stopIndex := 1; stopIndex < len(trip.Stops); stopIndex++ {
				distance, distanceExists := state.matrixValue(
					state.problem.Travel.DistanceMeters,
					trip.Stops[stopIndex-1].LocationID,
					trip.Stops[stopIndex].LocationID,
				)
				if distanceExists {
					metrics.TotalDistanceMeters += distance
					metrics.TotalCostCents += distance * vehicle.DistanceCostCPKM / 1_000
					if vehicle.Energy.Kind != domain.EnergyElectric {
						payloadG := payloadAfterStop(state.cargo, trip, stopIndex-1)
						dutyEnergyWh +=
							distance*vehicle.Energy.ConsumptionWhPerKM/1_000 +
								distance*vehicle.Energy.LoadWhPerKMPerTonne*payloadG/
									1_000_000_000
					}
				}
			}
			if vehicle.Energy.Kind == domain.EnergyElectric {
				for _, leg := range trip.Energy {
					dutyEnergyWh += leg.ConsumedWh
				}
			}
			for _, segment := range trip.Schedule {
				seconds := durationSeconds(segment.StartAt, segment.EndAt)
				if seconds < 0 {
					continue
				}
				switch segment.Kind {
				case domain.SegmentDrive:
					metrics.TotalDriveSeconds += seconds
				case domain.SegmentService:
					metrics.TotalServiceSeconds += seconds
				case domain.SegmentWait:
					metrics.TotalWaitSeconds += seconds
				case domain.SegmentBreak:
					metrics.TotalBreakSeconds += seconds
				case domain.SegmentCharge:
					metrics.TotalChargeSeconds += seconds
				case domain.SegmentRehandle:
					metrics.TotalRehandleSeconds += seconds
				}
			}
			for _, stage := range trip.LoadStages {
				var volume, payload int64
				metrics.Rehandles += uint32(len(stage.Rehandles))
				for _, operation := range stage.Rehandles {
					metrics.TotalRehandleCostCents += operation.CostCents
					metrics.TotalCostCents += operation.CostCents
				}
				for _, placement := range stage.Placements {
					cargo, cargoExists := state.cargo[placement.CargoID]
					if !cargoExists {
						continue
					}
					volume += placement.SizeMM.VolumeMM3()
					payload += cargo.WeightG
				}
				if capacityVolume > 0 {
					maxVolume = max64(maxVolume, volume*1_000_000/capacityVolume)
				}
				if capacityPayload > 0 {
					maxPayload = max64(maxPayload, payload*1_000_000/capacityPayload)
				}
			}
		}
		metrics.TotalEnergyWh += dutyEnergyWh
		volumeUtilizations = append(volumeUtilizations, maxVolume)
		payloadUtilizations = append(payloadUtilizations, maxPayload)
		metrics.TotalCostCents += dutyEnergyWh * vehicle.EnergyCostCPKWh / 1_000
	}

	for taskID, definition := range state.tasks {
		visits := state.visits[taskID]
		for _, visit := range visits {
			if len(definition.task.HardWindows) == 0 ||
				instantInAnyRange(definition.task.HardWindows, visit.stop.ServiceAt) {
				metrics.OnTimeTasks++
			} else {
				metrics.LateTasks++
			}
			metrics.TotalCostCents += softWindowPenalty(
				definition.task.SoftWindows,
				visit.stop.ServiceAt,
			)
		}
	}
	totalTimed := metrics.OnTimeTasks + metrics.LateTasks
	if totalTimed > 0 {
		metrics.OnTimeRatePPM = int64(metrics.OnTimeTasks) * 1_000_000 / int64(totalTimed)
	}
	if len(volumeUtilizations) > 0 {
		metrics.MinVolumeUtilizationPPM = slices.Min(volumeUtilizations)
		metrics.MeanVolumeUtilizationPPM = meanInt64(volumeUtilizations)
	}
	if len(payloadUtilizations) > 0 {
		metrics.MeanPayloadUtilizationPPM = meanInt64(payloadUtilizations)
		metrics.MaxPayloadUtilizationPPM = slices.Max(payloadUtilizations)
	}
	metrics.StabilityCostCents = state.stabilityCost()
	metrics.TotalCostCents += metrics.StabilityCostCents
	return metrics
}

func softWindowPenalty(values []domain.SoftTimeWindow, serviceAt time.Time) int64 {
	if len(values) == 0 {
		return 0
	}
	var best int64
	for index, value := range values {
		var penalty int64
		switch {
		case serviceAt.Before(value.Window.Start):
			penalty = durationSeconds(serviceAt, value.Window.Start) *
				value.EarlyPenaltyCentsPerSec
		case serviceAt.After(value.Window.End):
			penalty = durationSeconds(value.Window.End, serviceAt) *
				value.LatePenaltyCentsPerSec
		}
		if index == 0 || penalty < best {
			best = penalty
		}
	}
	return best
}

func payloadAfterStop(
	cargo map[domain.CargoID]domain.CargoItem,
	trip domain.Trip,
	stopIndex int,
) int64 {
	for _, stage := range trip.LoadStages {
		if int(stage.AfterStopIndex) != stopIndex {
			continue
		}
		var payload int64
		for _, placement := range stage.Placements {
			payload += cargo[placement.CargoID].WeightG
		}
		return payload
	}
	return 0
}

func meanInt64(values []int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total / int64(len(values))
}

func (state *validationState) stabilityCost() int64 {
	var cost int64
	for _, commitment := range state.problem.Commitments.Soft {
		visit, exists := state.singleVisit(commitment.TaskID)
		if !exists {
			continue
		}
		if visit.vehicleID != commitment.VehicleID {
			cost += state.problem.Policy.Stability.VehicleChangeCents
		}
		if !slices.Contains(visit.driverIDs, commitment.DriverID) {
			cost += state.problem.Policy.Stability.DriverChangeCents
		}
		if visit.stopIndex != int(commitment.Sequence) {
			cost += state.problem.Policy.Stability.SequenceChangeCents
		}
		cost += absoluteSeconds(visit.stop.ServiceAt.Sub(commitment.PlannedServiceAt)) *
			state.problem.Policy.Stability.ETADriftCentsPerSec
	}
	type cargoStage struct {
		cargoID domain.CargoID
		stage   uint32
	}
	cargoPlacements := make(map[cargoStage]domain.SoftCargoCommitment)
	for _, duty := range state.plan.Duties {
		for _, trip := range duty.Trips {
			for _, stage := range trip.LoadStages {
				for _, placement := range stage.Placements {
					key := cargoStage{
						cargoID: placement.CargoID,
						stage:   stage.AfterStopIndex,
					}
					if _, exists := cargoPlacements[key]; exists {
						continue
					}
					cargoPlacements[key] = domain.SoftCargoCommitment{
						CargoID:        placement.CargoID,
						VehicleID:      duty.VehicleID,
						AfterStopIndex: stage.AfterStopIndex,
						CompartmentID:  placement.CompartmentID,
						DoorID:         placement.DoorID,
						PositionMM:     placement.PositionMM,
						Orientation:    placement.Orientation,
					}
				}
			}
		}
	}
	reloaded := make(map[domain.CargoID]struct{})
	for _, commitment := range state.problem.Commitments.SoftCargo {
		key := cargoStage{
			cargoID: commitment.CargoID,
			stage:   commitment.AfterStopIndex,
		}
		if current, exists := cargoPlacements[key]; !exists || current != commitment {
			reloaded[commitment.CargoID] = struct{}{}
		}
	}
	cost += int64(len(reloaded)) * state.problem.Policy.Stability.ReloadCents
	return cost
}

func (state *validationState) unassignedRequiredUnits() uint32 {
	required := make(map[domain.FulfillmentUnitID]bool)
	for _, request := range state.problem.Requests {
		for _, unitID := range request.UnitIDs {
			required[unitID] = request.Required
		}
	}
	var count uint32
	for _, value := range state.plan.Unassigned {
		if required[value.UnitID] {
			count++
		}
	}
	return count
}

func (state *validationState) errorCount() int {
	count := 0
	for _, violation := range state.violations {
		if violation.Severity == domain.SeverityError {
			count++
		}
	}
	return count
}
