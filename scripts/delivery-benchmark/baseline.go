package main

import (
	"fmt"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func buildReferencePlan(
	problem domain.ProblemSnapshot,
	datasetID string,
) (domain.Plan, error) {
	if len(problem.Depots) != 1 {
		return domain.Plan{}, fmt.Errorf("reference baseline requires one depot")
	}
	depot := problem.Depots[0]
	cargoByID := make(map[domain.CargoID]domain.CargoItem, len(problem.Cargo))
	for _, item := range problem.Cargo {
		cargoByID[item.ID] = item
	}

	plan := domain.Plan{
		SchemaVersion:    domain.PlanSchemaVersion,
		PlanID:           domain.PlanID("reference-" + datasetID),
		RevisionID:       domain.PlanRevisionID("reference-" + datasetID + "-r1"),
		ProblemDigest:    problem.ProblemDigest,
		PolicyDigest:     problem.PolicyDigest,
		CommitmentDigest: problem.CommitmentDigest,
		Solver: domain.SolverIdentity{
			Name:    "reference-baseline",
			Version: "1.0.0",
			Build:   "delivery-benchmark-v1",
		},
		Duties:     make([]domain.VehicleDuty, 0, len(problem.Requests)),
		Unassigned: []domain.UnassignedUnit{},
	}
	configDigest, err := domain.Digest(struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Budget  string `json:"budget"`
	}{
		Name:    "one-request-per-vehicle",
		Version: "1.0.0",
		Budget:  "deterministic",
	})
	if err != nil {
		return domain.Plan{}, err
	}
	plan.ConfigDigest = configDigest

	volumeUtilizations := make([]int64, 0, len(problem.Requests))
	payloadUtilizations := make([]int64, 0, len(problem.Requests))
	for index, request := range problem.Requests {
		if len(request.Tasks) != 2 || len(request.UnitIDs) != 1 {
			return domain.Plan{}, fmt.Errorf("request %q is outside reference profile", request.ID)
		}
		pickup, delivery, err := pickupAndDelivery(request)
		if err != nil {
			return domain.Plan{}, err
		}
		unit, cargoItem, err := unitAndCargo(problem, request.UnitIDs[0], cargoByID)
		if err != nil {
			return domain.Plan{}, err
		}
		if index >= len(problem.Vehicles) || index >= len(problem.Drivers) {
			return domain.Plan{}, fmt.Errorf("request %q has no dedicated vehicle or driver", request.ID)
		}
		vehicle := problem.Vehicles[index]
		driver := problem.Drivers[index]
		duty, metrics, volumePPM, payloadPPM, err := buildReferenceDuty(
			problem,
			depot,
			request,
			pickup,
			delivery,
			unit,
			cargoItem,
			vehicle,
			driver,
		)
		if err != nil {
			return domain.Plan{}, fmt.Errorf("request %q: %w", request.ID, err)
		}
		plan.Duties = append(plan.Duties, duty)
		addMetrics(&plan.Metrics, metrics)
		volumeUtilizations = append(volumeUtilizations, volumePPM)
		payloadUtilizations = append(payloadUtilizations, payloadPPM)
	}
	if len(volumeUtilizations) > 0 {
		plan.Metrics.MinVolumeUtilizationPPM = minimumInt64(volumeUtilizations)
		plan.Metrics.MeanVolumeUtilizationPPM = meanValues(volumeUtilizations)
		plan.Metrics.MeanPayloadUtilizationPPM = meanValues(payloadUtilizations)
		plan.Metrics.MaxPayloadUtilizationPPM = maximumInt64(payloadUtilizations)
	}
	totalTimed := plan.Metrics.OnTimeTasks + plan.Metrics.LateTasks
	if totalTimed > 0 {
		plan.Metrics.OnTimeRatePPM =
			int64(plan.Metrics.OnTimeTasks) * 1_000_000 / int64(totalTimed)
	}
	plan.Objective = domain.ObjectiveVector{
		UnassignedRequiredUnits: 0,
		HardViolationCount:      0,
		VehiclesUsed:            plan.Metrics.VehiclesUsed,
		TotalCostCents:          plan.Metrics.TotalCostCents,
		TotalDistanceMeters:     plan.Metrics.TotalDistanceMeters,
		TotalWaitSeconds:        plan.Metrics.TotalWaitSeconds,
		NegativeMinVolumePPM:    -plan.Metrics.MinVolumeUtilizationPPM,
		StabilityCostCents:      plan.Metrics.StabilityCostCents,
	}
	plan.PlanDigest, err = domain.ComputePlanDigest(plan)
	if err != nil {
		return domain.Plan{}, fmt.Errorf("digest reference plan: %w", err)
	}
	return plan, nil
}

func pickupAndDelivery(
	request domain.TransportRequest,
) (domain.ServiceTask, domain.ServiceTask, error) {
	var pickup domain.ServiceTask
	var delivery domain.ServiceTask
	for _, task := range request.Tasks {
		switch task.Kind {
		case domain.TaskPickup:
			pickup = task
		case domain.TaskDelivery:
			delivery = task
		}
	}
	if pickup.ID == "" || delivery.ID == "" {
		return domain.ServiceTask{}, domain.ServiceTask{},
			fmt.Errorf("request %q must have one pickup and one delivery", request.ID)
	}
	return pickup, delivery, nil
}

func unitAndCargo(
	problem domain.ProblemSnapshot,
	unitID domain.FulfillmentUnitID,
	cargoByID map[domain.CargoID]domain.CargoItem,
) (domain.FulfillmentUnit, domain.CargoItem, error) {
	for _, unit := range problem.Units {
		if unit.ID != unitID {
			continue
		}
		if len(unit.CargoIDs) != 1 {
			return domain.FulfillmentUnit{}, domain.CargoItem{},
				fmt.Errorf("unit %q must contain one cargo item", unit.ID)
		}
		item, exists := cargoByID[unit.CargoIDs[0]]
		if !exists {
			return domain.FulfillmentUnit{}, domain.CargoItem{},
				fmt.Errorf("unit %q references unknown cargo", unit.ID)
		}
		return unit, item, nil
	}
	return domain.FulfillmentUnit{}, domain.CargoItem{},
		fmt.Errorf("unknown unit %q", unitID)
}

func buildReferenceDuty(
	problem domain.ProblemSnapshot,
	depot domain.Depot,
	request domain.TransportRequest,
	pickup domain.ServiceTask,
	delivery domain.ServiceTask,
	unit domain.FulfillmentUnit,
	cargoItem domain.CargoItem,
	vehicle domain.Vehicle,
	driver domain.Driver,
) (domain.VehicleDuty, domain.PlanMetrics, int64, int64, error) {
	if len(vehicle.Compartments) != 1 || len(vehicle.Doors) != 1 ||
		len(vehicle.Axles) != 2 {
		return domain.VehicleDuty{}, domain.PlanMetrics{}, 0, 0,
			fmt.Errorf("vehicle %q is outside reference profile", vehicle.ID)
	}
	outboundDistance, outboundSeconds, exists := matrixArc(
		problem,
		depot.LocationID,
		delivery.LocationID,
	)
	if !exists {
		return domain.VehicleDuty{}, domain.PlanMetrics{}, 0, 0,
			fmt.Errorf("outbound matrix arc is missing")
	}
	returnDistance, returnSeconds, exists := matrixArc(
		problem,
		delivery.LocationID,
		depot.LocationID,
	)
	if !exists {
		return domain.VehicleDuty{}, domain.PlanMetrics{}, 0, 0,
			fmt.Errorf("return matrix arc is missing")
	}
	start := problem.Horizon.Start
	pickupEnd := start.Add(timeSeconds(pickup.ServiceSeconds))
	customerArrival := pickupEnd.Add(timeSeconds(outboundSeconds))
	customerEnd := customerArrival.Add(timeSeconds(delivery.ServiceSeconds))
	end := customerEnd.Add(timeSeconds(returnSeconds))
	if end.After(problem.Horizon.End) {
		return domain.VehicleDuty{}, domain.PlanMetrics{}, 0, 0,
			fmt.Errorf("reference trip exceeds the planning horizon")
	}
	compartment := vehicle.Compartments[0]
	position := domain.Point3{
		X: 3_000 - cargoItem.SizeMM.Length/2,
		Y: 0,
		Z: 0,
	}
	center := domain.Point3{
		X: position.X + cargoItem.SizeMM.Length/2,
		Y: position.Y + cargoItem.SizeMM.Width/2,
		Z: position.Z + cargoItem.SizeMM.Height/2,
	}
	loadedAxles := benchmarkAxleLoads(vehicle.Axles, cargoItem.WeightG, center.X)
	emptyAxles := make([]int64, len(vehicle.Axles))
	tripID := domain.TripID("trip-" + string(request.ID))
	trip := domain.Trip{
		ID:           tripID,
		StartDepotID: depot.ID,
		EndDepotID:   depot.ID,
		StartAt:      start,
		EndAt:        end,
		Stops: []domain.Stop{
			{
				LocationID:  pickup.LocationID,
				TaskIDs:     []domain.TaskID{pickup.ID},
				ArrivalAt:   start,
				ServiceAt:   start,
				DepartureAt: pickupEnd,
			},
			{
				LocationID:  delivery.LocationID,
				TaskIDs:     []domain.TaskID{delivery.ID},
				ArrivalAt:   customerArrival,
				ServiceAt:   customerArrival,
				DepartureAt: customerEnd,
			},
			{
				LocationID:  depot.LocationID,
				TaskIDs:     []domain.TaskID{},
				ArrivalAt:   end,
				ServiceAt:   end,
				DepartureAt: end,
			},
		},
		Schedule: []domain.DutySegment{
			{
				Kind: domain.SegmentService, DriverID: driver.ID,
				From: pickup.LocationID, To: pickup.LocationID,
				StartAt: start, EndAt: pickupEnd, TaskIDs: []domain.TaskID{pickup.ID},
			},
			{
				Kind: domain.SegmentDrive, DriverID: driver.ID,
				From: pickup.LocationID, To: delivery.LocationID,
				StartAt: pickupEnd, EndAt: customerArrival, TaskIDs: []domain.TaskID{},
			},
			{
				Kind: domain.SegmentService, DriverID: driver.ID,
				From: delivery.LocationID, To: delivery.LocationID,
				StartAt: customerArrival, EndAt: customerEnd,
				TaskIDs: []domain.TaskID{delivery.ID},
			},
			{
				Kind: domain.SegmentDrive, DriverID: driver.ID,
				From: delivery.LocationID, To: depot.LocationID,
				StartAt: customerEnd, EndAt: end, TaskIDs: []domain.TaskID{},
			},
		},
		Energy: []domain.EnergyLeg{},
		LoadStages: []domain.LoadStage{
			{
				AfterStopIndex: 0,
				Placements: []domain.Placement{{
					CargoID:        cargoItem.ID,
					CompartmentID:  compartment.ID,
					PositionMM:     position,
					SizeMM:         cargoItem.SizeMM,
					Orientation:    domain.OrientationLWH,
					LoadAtTaskID:   pickup.ID,
					UnloadAtTaskID: delivery.ID,
					DoorID:         vehicle.Doors[0].ID,
				}},
				AxleLoadsG:     loadedAxles,
				CenterOfMassMM: center,
				RehandledCargo: []domain.CargoID{},
			},
			{
				AfterStopIndex: 1,
				Placements:     []domain.Placement{},
				AxleLoadsG:     emptyAxles,
				RehandledCargo: []domain.CargoID{},
			},
			{
				AfterStopIndex: 2,
				Placements:     []domain.Placement{},
				AxleLoadsG:     append([]int64(nil), emptyAxles...),
				RehandledCargo: []domain.CargoID{},
			},
		},
	}
	totalDistance := outboundDistance + returnDistance
	dutySeconds := pickup.ServiceSeconds + delivery.ServiceSeconds +
		outboundSeconds + returnSeconds
	totalEnergy := outboundDistance*vehicle.Energy.ConsumptionWhPerKM/1_000 +
		returnDistance*vehicle.Energy.ConsumptionWhPerKM/1_000
	totalCost := vehicle.FixedCostCents +
		dutySeconds*vehicle.WorkCostCPH/3_600 +
		outboundDistance*vehicle.DistanceCostCPKM/1_000 +
		returnDistance*vehicle.DistanceCostCPKM/1_000 +
		totalEnergy*vehicle.EnergyCostCPKWh/1_000
	capacityVolume := compartment.Bounds.Size.VolumeMM3()
	volumePPM := cargoItem.SizeMM.VolumeMM3() * 1_000_000 / capacityVolume
	payloadPPM := cargoItem.WeightG * 1_000_000 / compartment.MaxPayloadG
	metrics := domain.PlanMetrics{
		AssignedUnits:       uint32(unit.Quantity),
		VehiclesUsed:        1,
		Trips:               1,
		Stops:               3,
		TotalDistanceMeters: totalDistance,
		TotalDriveSeconds:   outboundSeconds + returnSeconds,
		TotalServiceSeconds: pickup.ServiceSeconds + delivery.ServiceSeconds,
		TotalEnergyWh:       totalEnergy,
		TotalCostCents:      totalCost,
		OnTimeTasks:         2,
	}
	return domain.VehicleDuty{
		VehicleID: vehicle.ID,
		DriverIDs: []domain.DriverID{driver.ID},
		Trips:     []domain.Trip{trip},
	}, metrics, volumePPM, payloadPPM, nil
}

func matrixArc(
	problem domain.ProblemSnapshot,
	from domain.LocationID,
	to domain.LocationID,
) (int64, int64, bool) {
	fromIndex := -1
	toIndex := -1
	for index, nodeID := range problem.Travel.NodeIDs {
		if nodeID == from {
			fromIndex = index
		}
		if nodeID == to {
			toIndex = index
		}
	}
	size := len(problem.Travel.NodeIDs)
	if fromIndex < 0 || toIndex < 0 ||
		len(problem.Travel.DistanceMeters) != size*size ||
		len(problem.Travel.TravelSeconds) != size*size {
		return 0, 0, false
	}
	offset := fromIndex*size + toIndex
	return problem.Travel.DistanceMeters[offset], problem.Travel.TravelSeconds[offset], true
}

func benchmarkAxleLoads(
	axles []domain.Axle,
	totalWeight int64,
	centerX int64,
) []int64 {
	result := make([]int64, len(axles))
	if totalWeight == 0 || len(axles) == 0 {
		return result
	}
	if len(axles) != 2 || axles[0].PositionXMM >= axles[1].PositionXMM {
		return result
	}
	switch {
	case centerX <= axles[0].PositionXMM:
		result[0] = totalWeight
	case centerX >= axles[1].PositionXMM:
		result[1] = totalWeight
	default:
		span := axles[1].PositionXMM - axles[0].PositionXMM
		result[1] = totalWeight * (centerX - axles[0].PositionXMM) / span
		result[0] = totalWeight - result[1]
	}
	return result
}

func addMetrics(target *domain.PlanMetrics, value domain.PlanMetrics) {
	target.AssignedUnits += value.AssignedUnits
	target.UnassignedUnits += value.UnassignedUnits
	target.VehiclesUsed += value.VehiclesUsed
	target.Trips += value.Trips
	target.Stops += value.Stops
	target.TotalDistanceMeters += value.TotalDistanceMeters
	target.TotalDriveSeconds += value.TotalDriveSeconds
	target.TotalServiceSeconds += value.TotalServiceSeconds
	target.TotalWaitSeconds += value.TotalWaitSeconds
	target.TotalBreakSeconds += value.TotalBreakSeconds
	target.TotalChargeSeconds += value.TotalChargeSeconds
	target.TotalEnergyWh += value.TotalEnergyWh
	target.TotalCostCents += value.TotalCostCents
	target.StabilityCostCents += value.StabilityCostCents
	target.OnTimeTasks += value.OnTimeTasks
	target.LateTasks += value.LateTasks
	target.Rehandles += value.Rehandles
}

func timeSeconds(value int64) time.Duration {
	return time.Duration(value) * time.Second
}

func minimumInt64(values []int64) int64 {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func maximumInt64(values []int64) int64 {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}

func meanValues(values []int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total / int64(len(values))
}
