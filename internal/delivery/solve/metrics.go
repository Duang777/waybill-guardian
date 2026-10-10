package solve

import (
	"fmt"
	"slices"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type planTaskVisit struct {
	vehicleID domain.VehicleID
	driverIDs []domain.DriverID
	stopIndex int
	serviceAt time.Time
}

func (engine *engine) recomputePlanMetrics(plan domain.Plan) (domain.PlanMetrics, error) {
	visits := make(map[domain.TaskID][]planTaskVisit, len(engine.index.tasks))
	for _, duty := range plan.Duties {
		if _, exists := engine.index.vehicles[duty.VehicleID]; !exists {
			return domain.PlanMetrics{}, fmt.Errorf("unknown vehicle %q", duty.VehicleID)
		}
		for _, trip := range duty.Trips {
			for stopIndex, stop := range trip.Stops {
				for _, taskID := range stop.TaskIDs {
					if _, exists := engine.index.tasks[taskID]; !exists {
						return domain.PlanMetrics{}, fmt.Errorf("unknown task %q", taskID)
					}
					visits[taskID] = append(visits[taskID], planTaskVisit{
						vehicleID: duty.VehicleID,
						driverIDs: append([]domain.DriverID{}, duty.DriverIDs...),
						stopIndex: stopIndex,
						serviceAt: stop.ServiceAt,
					})
				}
			}
		}
	}

	var metrics domain.PlanMetrics
	for _, request := range engine.problem.Requests {
		for _, unitID := range request.UnitIDs {
			complete := true
			for _, task := range tasksForPlanUnit(request.Tasks, unitID) {
				if len(visits[task.ID]) != 1 {
					complete = false
					break
				}
			}
			if complete {
				metrics.AssignedUnits++
			}
		}
	}
	metrics.UnassignedUnits = uint32(len(plan.Unassigned))
	unassignedRequests := make(map[domain.RequestID]bool)
	for _, value := range plan.Unassigned {
		unit, exists := engine.index.units[value.UnitID]
		if !exists {
			return domain.PlanMetrics{}, fmt.Errorf("unknown unassigned unit %q", value.UnitID)
		}
		request, exists := engine.index.requests[unit.RequestID]
		if !exists {
			return domain.PlanMetrics{}, fmt.Errorf(
				"unit %q references unknown request %q",
				unit.ID,
				unit.RequestID,
			)
		}
		unassignedRequests[request.ID] = request.Required
	}
	for _, required := range unassignedRequests {
		if required {
			metrics.TotalCostCents += engine.problem.Policy.RequiredOrderPenaltyCents
		} else {
			metrics.TotalCostCents += engine.problem.Policy.OptionalOrderPenaltyCents
		}
	}

	volumeUtilizations := make([]int64, 0, len(plan.Duties))
	payloadUtilizations := make([]int64, 0, len(plan.Duties))
	for _, duty := range plan.Duties {
		vehicle := engine.index.vehicles[duty.VehicleID]
		if len(duty.Trips) == 0 {
			continue
		}
		metrics.VehiclesUsed++
		metrics.TotalCostCents += vehicle.FixedCostCents
		var capacityVolume int64
		var capacityPayload int64
		for _, compartment := range vehicle.Compartments {
			capacityVolume += compartment.Bounds.Size.VolumeMM3()
			capacityPayload += compartment.MaxPayloadG
		}
		var maxVolume int64
		var maxPayload int64
		var dutyEnergyWh int64
		for _, trip := range duty.Trips {
			metrics.Trips++
			metrics.Stops += uint32(len(trip.Stops))
			dutySeconds := planDurationSeconds(trip.StartAt, trip.EndAt)
			if dutySeconds < 0 {
				return domain.PlanMetrics{}, fmt.Errorf("trip %q ends before it starts", trip.ID)
			}
			metrics.TotalCostCents += dutySeconds * vehicle.WorkCostCPH / 3_600
			for stopIndex := 1; stopIndex < len(trip.Stops); stopIndex++ {
				distance, exists := engine.travelDistance(
					trip.Stops[stopIndex-1].LocationID,
					trip.Stops[stopIndex].LocationID,
				)
				if !exists {
					return domain.PlanMetrics{}, fmt.Errorf(
						"trip %q has no distance arc %q -> %q",
						trip.ID,
						trip.Stops[stopIndex-1].LocationID,
						trip.Stops[stopIndex].LocationID,
					)
				}
				metrics.TotalDistanceMeters += distance
				metrics.TotalCostCents += distance * vehicle.DistanceCostCPKM / 1_000
				if vehicle.Energy.Kind != domain.EnergyElectric {
					payloadG := payloadAtStage(
						trip.LoadStages,
						stopIndex-1,
						engine.index.cargo,
					)
					dutyEnergyWh +=
						distance*vehicle.Energy.ConsumptionWhPerKM/1_000 +
							distance*vehicle.Energy.LoadWhPerKMPerTonne*payloadG/
								1_000_000_000
				}
			}
			if vehicle.Energy.Kind == domain.EnergyElectric {
				for _, leg := range trip.Energy {
					dutyEnergyWh += leg.ConsumedWh
				}
			}
			for _, segment := range trip.Schedule {
				seconds := planDurationSeconds(segment.StartAt, segment.EndAt)
				if seconds < 0 {
					return domain.PlanMetrics{}, fmt.Errorf(
						"trip %q has a negative schedule segment",
						trip.ID,
					)
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
				var volume int64
				var payload int64
				metrics.Rehandles += uint32(len(stage.Rehandles))
				for _, operation := range stage.Rehandles {
					metrics.TotalRehandleCostCents += operation.CostCents
					metrics.TotalCostCents += operation.CostCents
				}
				for _, placement := range stage.Placements {
					cargo, exists := engine.index.cargo[placement.CargoID]
					if !exists {
						return domain.PlanMetrics{}, fmt.Errorf(
							"trip %q places unknown cargo %q",
							trip.ID,
							placement.CargoID,
						)
					}
					volume += placement.SizeMM.VolumeMM3()
					payload += cargo.WeightG
				}
				if capacityVolume > 0 {
					maxVolume = maxPlanMetric(
						maxVolume,
						volume*1_000_000/capacityVolume,
					)
				}
				if capacityPayload > 0 {
					maxPayload = maxPlanMetric(
						maxPayload,
						payload*1_000_000/capacityPayload,
					)
				}
			}
		}
		metrics.TotalEnergyWh += dutyEnergyWh
		metrics.TotalCostCents += dutyEnergyWh * vehicle.EnergyCostCPKWh / 1_000
		volumeUtilizations = append(volumeUtilizations, maxVolume)
		payloadUtilizations = append(payloadUtilizations, maxPayload)
	}

	for taskID, task := range engine.index.tasks {
		for _, visit := range visits[taskID] {
			if len(task.HardWindows) == 0 ||
				instantInRanges(task.HardWindows, visit.serviceAt) {
				metrics.OnTimeTasks++
			} else {
				metrics.LateTasks++
			}
			metrics.TotalCostCents += planSoftWindowPenalty(
				task.SoftWindows,
				visit.serviceAt,
			)
		}
	}
	totalTimed := metrics.OnTimeTasks + metrics.LateTasks
	if totalTimed > 0 {
		metrics.OnTimeRatePPM = int64(metrics.OnTimeTasks) * 1_000_000 / int64(totalTimed)
	}
	if len(volumeUtilizations) > 0 {
		metrics.MinVolumeUtilizationPPM = slices.Min(volumeUtilizations)
		metrics.MeanVolumeUtilizationPPM = planMean(volumeUtilizations)
	}
	if len(payloadUtilizations) > 0 {
		metrics.MeanPayloadUtilizationPPM = planMean(payloadUtilizations)
		metrics.MaxPayloadUtilizationPPM = slices.Max(payloadUtilizations)
	}
	metrics.StabilityCostCents = engine.planStabilityCost(visits)
	metrics.TotalCostCents += metrics.StabilityCostCents
	return metrics, nil
}

func (engine *engine) objectiveForPlan(
	plan domain.Plan,
	metrics domain.PlanMetrics,
) domain.ObjectiveVector {
	requiredUnassigned := engine.unassignedRequired(plan.Unassigned)
	return domain.ObjectiveVector{
		UnassignedRequiredUnits: requiredUnassigned,
		HardViolationCount:      requiredUnassigned * 2,
		VehiclesUsed:            metrics.VehiclesUsed,
		TotalCostCents:          metrics.TotalCostCents,
		TotalDistanceMeters:     metrics.TotalDistanceMeters,
		TotalWaitSeconds:        metrics.TotalWaitSeconds,
		NegativeMinVolumePPM:    -metrics.MinVolumeUtilizationPPM,
		StabilityCostCents:      metrics.StabilityCostCents,
	}
}

func (engine *engine) planStabilityCost(visits map[domain.TaskID][]planTaskVisit) int64 {
	var result int64
	for _, commitment := range engine.problem.Commitments.Soft {
		taskVisits := visits[commitment.TaskID]
		if len(taskVisits) != 1 {
			continue
		}
		visit := taskVisits[0]
		if visit.vehicleID != commitment.VehicleID {
			result += engine.problem.Policy.Stability.VehicleChangeCents
		}
		if !slices.Contains(visit.driverIDs, commitment.DriverID) {
			result += engine.problem.Policy.Stability.DriverChangeCents
		}
		if visit.stopIndex != int(commitment.Sequence) {
			result += engine.problem.Policy.Stability.SequenceChangeCents
		}
		result += planAbsoluteSeconds(visit.serviceAt.Sub(commitment.PlannedServiceAt)) *
			engine.problem.Policy.Stability.ETADriftCentsPerSec
	}
	return result
}

func tasksForPlanUnit(
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

func planSoftWindowPenalty(values []domain.SoftTimeWindow, serviceAt time.Time) int64 {
	if len(values) == 0 {
		return 0
	}
	var best int64
	for index, value := range values {
		var penalty int64
		switch {
		case serviceAt.Before(value.Window.Start):
			penalty = planDurationSeconds(serviceAt, value.Window.Start) *
				value.EarlyPenaltyCentsPerSec
		case serviceAt.After(value.Window.End):
			penalty = planDurationSeconds(value.Window.End, serviceAt) *
				value.LatePenaltyCentsPerSec
		}
		if index == 0 || penalty < best {
			best = penalty
		}
	}
	return best
}

func planDurationSeconds(start, end time.Time) int64 {
	if end.Before(start) {
		return -1
	}
	return int64(end.Sub(start) / time.Second)
}

func planAbsoluteSeconds(value time.Duration) int64 {
	seconds := int64(value / time.Second)
	if seconds < 0 {
		return -seconds
	}
	return seconds
}

func planMean(values []int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total / int64(len(values))
}

func maxPlanMetric(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
