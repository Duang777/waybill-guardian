package solve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type dutyRebuild struct {
	DutyIndex int
	FromTrip  int
}

type recomputeMode uint8

const (
	recomputeIncremental recomputeMode = iota + 1
	recomputeFull
)

type candidateMaterialBody struct {
	Duties     []domain.VehicleDuty    `json:"duties"`
	Unassigned []domain.UnassignedUnit `json:"unassigned"`
}

func (engine *engine) recomputeMaterializedCandidate(
	ctx context.Context,
	incumbent candidateState,
	candidate domain.Plan,
) (domain.Plan, error) {
	return engine.recomputeMaterializedCandidateWithDependencies(
		ctx,
		incumbent,
		candidate,
		allDependencyEdges,
	)
}

func (engine *engine) recomputeMaterializedCandidateWithDependencies(
	ctx context.Context,
	incumbent candidateState,
	candidate domain.Plan,
	edges dependencyEdges,
) (domain.Plan, error) {
	incremental, incrementalErr := engine.compileCandidatePlan(
		ctx,
		candidate,
		&incumbent,
		recomputeIncremental,
		edges,
	)
	if err := ctx.Err(); err != nil {
		return domain.Plan{}, err
	}
	full, fullErr := engine.compileCandidatePlan(
		ctx,
		candidate,
		nil,
		recomputeFull,
		edges,
	)
	if err := ctx.Err(); err != nil {
		return domain.Plan{}, err
	}
	switch {
	case incrementalErr != nil && fullErr != nil:
		if incrementalErr.Error() == fullErr.Error() {
			return domain.Plan{}, incrementalErr
		}
		return domain.Plan{}, fmt.Errorf(
			"%w: incremental error %q, full error %q",
			ErrRecomputeMismatch,
			incrementalErr,
			fullErr,
		)
	case incrementalErr != nil:
		return domain.Plan{}, fmt.Errorf(
			"%w: incremental failed while full recomputation succeeded: %v",
			ErrRecomputeMismatch,
			incrementalErr,
		)
	case fullErr != nil:
		return domain.Plan{}, fmt.Errorf(
			"%w: full recomputation failed while incremental succeeded: %v",
			ErrRecomputeMismatch,
			fullErr,
		)
	}
	incrementalDigest, err := materialDigest(incremental)
	if err != nil {
		return domain.Plan{}, err
	}
	fullDigest, err := materialDigest(full)
	if err != nil {
		return domain.Plan{}, err
	}
	if incrementalDigest != fullDigest {
		return domain.Plan{}, fmt.Errorf(
			"%w: incremental material digest %q, full material digest %q",
			ErrRecomputeMismatch,
			incrementalDigest,
			fullDigest,
		)
	}
	return incremental, nil
}

func materialDigest(plan domain.Plan) (domain.ArtifactDigest, error) {
	normalized := normalizeCandidatePlan(plan)
	return domain.Digest(candidateMaterialBody{
		Duties:     normalized.Duties,
		Unassigned: normalized.Unassigned,
	})
}

func (engine *engine) compileCandidatePlan(
	ctx context.Context,
	source domain.Plan,
	incumbent *candidateState,
	mode recomputeMode,
	edges dependencyEdges,
) (domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return domain.Plan{}, err
	}
	source = normalizeCandidatePlan(source)
	type dutyDecisions struct {
		duty      domain.VehicleDuty
		decisions []tripDecision
	}
	inputs := make([]dutyDecisions, 0, len(source.Duties))
	for dutyIndex, duty := range source.Duties {
		decisions := make([]tripDecision, 0, len(duty.Trips))
		for tripIndex, trip := range duty.Trips {
			decision, err := materializedTripDecision(duty, trip)
			if err != nil {
				return domain.Plan{}, fmt.Errorf(
					"duty %d trip %d decision: %w",
					dutyIndex,
					tripIndex,
					err,
				)
			}
			decisions = append(decisions, decision)
		}
		inputs = append(inputs, dutyDecisions{duty: duty, decisions: decisions})
	}

	var incumbentComponents componentIndex
	incumbentByVehicle := make(map[domain.VehicleID]domain.VehicleDuty)
	if incumbent != nil {
		incumbentComponents = incumbent.components
		if edges != allDependencyEdges {
			var err error
			incumbentComponents, err = indexCandidateComponentsWithDependencies(
				incumbent.plan,
				edges,
			)
			if err != nil {
				return domain.Plan{}, fmt.Errorf("index incumbent components: %w", err)
			}
		}
		for _, duty := range incumbent.plan.Duties {
			incumbentByVehicle[duty.VehicleID] = duty
		}
	}

	result := source
	result.Duties = make([]domain.VehicleDuty, 0, len(inputs))
	for dutyIndex, input := range inputs {
		if err := ctx.Err(); err != nil {
			return domain.Plan{}, err
		}
		vehicle, exists := engine.index.vehicles[input.duty.VehicleID]
		if !exists {
			return domain.Plan{}, fmt.Errorf(
				"duty %d references unknown vehicle %q",
				dutyIndex,
				input.duty.VehicleID,
			)
		}
		rebuilt := make([]domain.Trip, 0, len(input.decisions))
		for tripIndex, decision := range input.decisions {
			if err := ctx.Err(); err != nil {
				return domain.Plan{}, err
			}
			workingDuty := input.duty
			workingDuty.Trips = rebuilt
			inputDigest, err := digestTripInput(
				source.ProblemDigest,
				workingDuty,
				decision,
				previousDependency(rebuilt, len(rebuilt), edges),
			)
			if err != nil {
				return domain.Plan{}, fmt.Errorf(
					"digest duty %d trip %d input: %w",
					dutyIndex,
					tripIndex,
					err,
				)
			}
			key := tripComponentKey{
				VehicleID: input.duty.VehicleID,
				TripIndex: uint32(tripIndex),
			}
			if mode == recomputeIncremental &&
				incumbent != nil &&
				incumbentComponents.tripInputDigests[key] == inputDigest {
				previousDuty, found := incumbentByVehicle[input.duty.VehicleID]
				if !found || tripIndex >= len(previousDuty.Trips) {
					return domain.Plan{}, fmt.Errorf(
						"%w: reusable trip %q/%d is absent from incumbent",
						ErrRecomputeMismatch,
						input.duty.VehicleID,
						tripIndex,
					)
				}
				rebuilt = append(rebuilt, previousDuty.Trips[tripIndex])
				continue
			}
			trip, err := engine.compileTripBlueprint(
				ctx,
				vehicle,
				workingDuty,
				rebuilt,
				blueprintFromDecision(decision),
			)
			if err != nil {
				return domain.Plan{}, fmt.Errorf(
					"compile duty %d trip %d: %w",
					dutyIndex,
					tripIndex,
					err,
				)
			}
			rebuilt = append(rebuilt, trip)
		}
		rebuiltDuty := input.duty
		rebuiltDuty.Trips = rebuilt
		for _, driverID := range rebuiltDuty.DriverIDs {
			driver, exists := engine.index.drivers[driverID]
			if !exists {
				return domain.Plan{}, fmt.Errorf(
					"duty %d references unknown driver %q",
					dutyIndex,
					driverID,
				)
			}
			if err := checkDriverLimits(driver, rebuilt); err != nil {
				return domain.Plan{}, fmt.Errorf(
					"duty %d driver %q: %w",
					dutyIndex,
					driverID,
					err,
				)
			}
		}
		result.Duties = append(result.Duties, rebuiltDuty)
	}
	result.Metrics = domain.PlanMetrics{}
	result.Objective = domain.ObjectiveVector{}
	result.PlanDigest = ""
	return result, nil
}

func (engine *engine) rebuildPlanDuties(
	ctx context.Context,
	plan domain.Plan,
	rebuilds []dutyRebuild,
) (domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return domain.Plan{}, err
	}
	result := normalizeCandidatePlan(plan)
	fromByDuty := make(map[int]int, len(rebuilds))
	for _, rebuild := range rebuilds {
		if rebuild.DutyIndex < 0 || rebuild.DutyIndex >= len(result.Duties) {
			return domain.Plan{}, fmt.Errorf("duty index %d is out of range", rebuild.DutyIndex)
		}
		if rebuild.FromTrip < 0 ||
			rebuild.FromTrip >= len(result.Duties[rebuild.DutyIndex].Trips) {
			return domain.Plan{}, fmt.Errorf(
				"duty %d trip index %d is out of range",
				rebuild.DutyIndex,
				rebuild.FromTrip,
			)
		}
		current, exists := fromByDuty[rebuild.DutyIndex]
		if !exists || rebuild.FromTrip < current {
			fromByDuty[rebuild.DutyIndex] = rebuild.FromTrip
		}
	}
	for dutyIndex := range result.Duties {
		if err := ctx.Err(); err != nil {
			return domain.Plan{}, err
		}
		fromTrip, rebuild := fromByDuty[dutyIndex]
		if !rebuild {
			continue
		}
		if err := engine.rebuildDuty(ctx, &result.Duties[dutyIndex], fromTrip); err != nil {
			return domain.Plan{}, fmt.Errorf("rebuild duty %d: %w", dutyIndex, err)
		}
	}
	result.Metrics = domain.PlanMetrics{}
	result.Objective = domain.ObjectiveVector{}
	result.PlanDigest = ""
	return result, nil
}

func (engine *engine) rebuildDuty(
	ctx context.Context,
	duty *domain.VehicleDuty,
	fromTrip int,
) error {
	vehicle, exists := engine.index.vehicles[duty.VehicleID]
	if !exists {
		return fmt.Errorf("unknown vehicle %q", duty.VehicleID)
	}
	blueprints := make([]tripBlueprint, len(duty.Trips))
	for tripIndex, trip := range duty.Trips {
		if err := ctx.Err(); err != nil {
			return err
		}
		blueprint, err := engine.tripBlueprint(*duty, trip)
		if err != nil {
			return fmt.Errorf("trip %d: %w", tripIndex, err)
		}
		blueprints[tripIndex] = blueprint
	}
	rebuilt := append([]domain.Trip{}, duty.Trips[:fromTrip]...)
	for tripIndex := fromTrip; tripIndex < len(blueprints); tripIndex++ {
		trip, err := engine.compileTripBlueprint(
			ctx,
			vehicle,
			*duty,
			rebuilt,
			blueprints[tripIndex],
		)
		if err != nil {
			return fmt.Errorf("trip %d: %w", tripIndex, err)
		}
		rebuilt = append(rebuilt, trip)
	}
	duty.Trips = rebuilt
	for _, driverID := range duty.DriverIDs {
		driver, exists := engine.index.drivers[driverID]
		if !exists {
			return fmt.Errorf("unknown driver %q", driverID)
		}
		if err := checkDriverLimits(driver, rebuilt); err != nil {
			return fmt.Errorf("driver %q: %w", driverID, err)
		}
	}
	return nil
}

type tripBlueprint struct {
	id           domain.TripID
	startDepotID domain.DepotID
	endDepotID   domain.DepotID
	driverID     domain.DriverID
	taskIDs      []domain.TaskID
	controls     *tripControls
}

func (engine *engine) tripBlueprint(
	duty domain.VehicleDuty,
	trip domain.Trip,
) (tripBlueprint, error) {
	driverID, err := scheduledDriver(duty, trip)
	if err != nil {
		return tripBlueprint{}, err
	}
	taskIDs := make([]domain.TaskID, 0)
	seen := make(map[domain.TaskID]struct{})
	for _, stop := range trip.Stops {
		for _, taskID := range stop.TaskIDs {
			if _, duplicate := seen[taskID]; duplicate {
				return tripBlueprint{}, fmt.Errorf("task %q occurs more than once", taskID)
			}
			if _, exists := engine.index.tasks[taskID]; !exists {
				return tripBlueprint{}, fmt.Errorf("unknown task %q", taskID)
			}
			seen[taskID] = struct{}{}
			taskIDs = append(taskIDs, taskID)
		}
	}
	if len(taskIDs) == 0 {
		return tripBlueprint{}, fmt.Errorf("trip has no service tasks")
	}
	return tripBlueprint{
		id:           trip.ID,
		startDepotID: trip.StartDepotID,
		endDepotID:   trip.EndDepotID,
		driverID:     driverID,
		taskIDs:      taskIDs,
	}, nil
}

func scheduledDriver(
	duty domain.VehicleDuty,
	trip domain.Trip,
) (domain.DriverID, error) {
	driverID, err := materializedDriver(duty, trip)
	if err != nil {
		return "", err
	}
	if !slices.Contains(duty.DriverIDs, driverID) {
		return "", fmt.Errorf("trip driver is not bound to the duty")
	}
	return driverID, nil
}

func materializedDriver(
	duty domain.VehicleDuty,
	trip domain.Trip,
) (domain.DriverID, error) {
	driverID := domain.DriverID("")
	for _, segment := range trip.Schedule {
		if segment.DriverID == "" {
			continue
		}
		if driverID != "" && segment.DriverID != driverID {
			return "", fmt.Errorf("trip uses more than one driver")
		}
		driverID = segment.DriverID
	}
	if driverID == "" && len(duty.DriverIDs) == 1 {
		driverID = duty.DriverIDs[0]
	}
	if driverID == "" {
		return "", fmt.Errorf("trip has no driver decision")
	}
	return driverID, nil
}

func materializedTripDecision(
	duty domain.VehicleDuty,
	trip domain.Trip,
) (tripDecision, error) {
	driverID, err := materializedDriver(duty, trip)
	if err != nil {
		return tripDecision{}, err
	}
	breaks, err := breakDirectivesFromTrip(trip)
	if err != nil {
		return tripDecision{}, err
	}
	chargers, err := chargerDirectivesFromTrip(trip)
	if err != nil {
		return tripDecision{}, err
	}
	loadStages := make([]loadStageDecision, 0, len(trip.LoadStages))
	for _, stage := range trip.LoadStages {
		placements := make([]placementDecision, 0, len(stage.Placements))
		for _, placement := range stage.Placements {
			placements = append(placements, placementDecision{
				CargoID:       placement.CargoID,
				CompartmentID: placement.CompartmentID,
				PositionMM:    placement.PositionMM,
				Orientation:   placement.Orientation,
				DoorID:        placement.DoorID,
			})
		}
		slices.SortFunc(placements, func(left, right placementDecision) int {
			return strings.Compare(string(left.CargoID), string(right.CargoID))
		})
		loadStages = append(loadStages, loadStageDecision{
			AfterStopIndex: stage.AfterStopIndex,
			Placements:     placements,
		})
	}
	return tripDecision{
		ID:           trip.ID,
		StartDepotID: trip.StartDepotID,
		EndDepotID:   trip.EndDepotID,
		DriverID:     driverID,
		TaskIDs:      tripTaskIDs(trip),
		Controls: tripControls{
			Breaks:     breaks,
			Chargers:   chargers,
			LoadStages: loadStages,
		},
	}, nil
}

func breakDirectivesFromTrip(trip domain.Trip) ([]breakDirective, error) {
	result := make([]breakDirective, 0)
	for _, segment := range trip.Schedule {
		if segment.Kind != domain.SegmentBreak {
			continue
		}
		seconds := int64(segment.EndAt.Sub(segment.StartAt) / time.Second)
		if seconds <= 0 {
			return nil, fmt.Errorf("break has non-positive duration")
		}
		stopIndex := -1
		for index, stop := range trip.Stops {
			if !stop.ArrivalAt.Before(segment.EndAt) ||
				!stop.ServiceAt.Before(segment.EndAt) ||
				!stop.DepartureAt.Before(segment.EndAt) {
				stopIndex = index
				break
			}
		}
		if stopIndex < 0 {
			return nil, fmt.Errorf(
				"break ending at %s has no following stop",
				segment.EndAt.UTC().Format(time.RFC3339Nano),
			)
		}
		result = append(result, breakDirective{
			BeforeStopIndex: uint32(stopIndex),
			DurationSeconds: seconds,
		})
	}
	return result, nil
}

func chargerDirectivesFromTrip(
	trip domain.Trip,
) ([]chargerDirective, error) {
	byStop := make(map[uint32]domain.ChargerID)
	for _, leg := range trip.Energy {
		if leg.ChargedWh == 0 {
			continue
		}
		if leg.ChargerID == "" {
			return nil, fmt.Errorf(
				"charged energy leg from stop %d has no charger",
				leg.FromStopIndex,
			)
		}
		if existing, duplicate := byStop[leg.FromStopIndex]; duplicate &&
			existing != leg.ChargerID {
			return nil, fmt.Errorf(
				"stop %d uses multiple chargers",
				leg.FromStopIndex,
			)
		}
		byStop[leg.FromStopIndex] = leg.ChargerID
	}
	stops := make([]uint32, 0, len(byStop))
	for stopIndex := range byStop {
		stops = append(stops, stopIndex)
	}
	slices.Sort(stops)
	result := make([]chargerDirective, 0, len(stops))
	for _, stopIndex := range stops {
		result = append(result, chargerDirective{
			FromStopIndex: stopIndex,
			ChargerID:     byStop[stopIndex],
		})
	}
	return result, nil
}

func blueprintFromDecision(decision tripDecision) tripBlueprint {
	controls := decision.Controls
	return tripBlueprint{
		id:           decision.ID,
		startDepotID: decision.StartDepotID,
		endDepotID:   decision.EndDepotID,
		driverID:     decision.DriverID,
		taskIDs:      append([]domain.TaskID{}, decision.TaskIDs...),
		controls:     &controls,
	}
}

func (engine *engine) compileTripBlueprint(
	ctx context.Context,
	vehicle domain.Vehicle,
	duty domain.VehicleDuty,
	rebuilt []domain.Trip,
	blueprint tripBlueprint,
) (domain.Trip, error) {
	if err := ctx.Err(); err != nil {
		return domain.Trip{}, err
	}
	startDepot, startExists := engine.index.depots[blueprint.startDepotID]
	endDepot, endExists := engine.index.depots[blueprint.endDepotID]
	if !startExists || !startDepot.AllowTripStart {
		return domain.Trip{}, fmt.Errorf("start depot %q is unavailable", blueprint.startDepotID)
	}
	if !endExists || !endDepot.AllowTripEnd {
		return domain.Trip{}, fmt.Errorf("end depot %q is unavailable", blueprint.endDepotID)
	}
	driver, exists := engine.index.drivers[blueprint.driverID]
	if !exists {
		return domain.Trip{}, fmt.Errorf("unknown driver %q", blueprint.driverID)
	}
	if !slices.Contains(duty.DriverIDs, driver.ID) {
		return domain.Trip{}, fmt.Errorf(
			"driver %q is not bound to vehicle duty %q",
			driver.ID,
			duty.VehicleID,
		)
	}
	if len(rebuilt) == 0 {
		if driver.StartLocation != startDepot.LocationID {
			return domain.Trip{}, fmt.Errorf(
				"driver %q does not start at depot %q",
				driver.ID,
				startDepot.ID,
			)
		}
	} else if rebuilt[len(rebuilt)-1].EndDepotID != blueprint.startDepotID {
		return domain.Trip{}, fmt.Errorf("trip start depot breaks duty continuity")
	}
	tasks := make([]domain.ServiceTask, 0, len(blueprint.taskIDs))
	requests := make(map[domain.RequestID]domain.TransportRequest)
	for _, taskID := range blueprint.taskIDs {
		task := engine.index.tasks[taskID]
		requestID := engine.index.taskRequests[taskID]
		request := engine.index.requests[requestID]
		required := append(
			append(domain.SkillSet{}, request.RequiredSkills...),
			task.RequiredSkills...,
		)
		if !containsSkills(vehicle.Skills, required) ||
			!containsSkills(driver.Skills, required) {
			return domain.Trip{}, fmt.Errorf("vehicle or driver lacks task %q skills", taskID)
		}
		requests[requestID] = request
		tasks = append(tasks, task)
	}
	for _, request := range requests {
		if err := engine.checkCommitmentAssignment(
			requestForTasks(request, blueprint.taskIDs),
			vehicle.ID,
			driver.ID,
		); err != nil {
			return domain.Trip{}, err
		}
	}
	prefix := duty
	prefix.Trips = rebuilt
	startAt, err := engine.tripStart(vehicle, driver, prefix)
	if err != nil {
		return domain.Trip{}, err
	}
	var breaks []breakDirective
	exactBreaks := false
	if blueprint.controls != nil {
		breaks = blueprint.controls.Breaks
		exactBreaks = true
	}
	trip, _, err := engine.scheduleTripControlled(
		blueprint.id,
		startDepot,
		endDepot,
		tasks,
		driver,
		startAt,
		len(rebuilt) > 0,
		breaks,
		exactBreaks,
	)
	if err != nil {
		return domain.Trip{}, err
	}
	if blueprint.controls == nil {
		trip.LoadStages, err = engine.packTrip(ctx, vehicle, trip)
	} else {
		trip.LoadStages, err = engine.compileLoadStages(
			ctx,
			vehicle,
			trip,
			blueprint.controls.LoadStages,
		)
	}
	if err != nil {
		return domain.Trip{}, err
	}
	if err := engine.applyRehandleSchedule(ctx, &trip, driver.ID); err != nil {
		return domain.Trip{}, err
	}
	startSOC, err := tripStartSOC(vehicle, prefix)
	if err != nil {
		return domain.Trip{}, err
	}
	if blueprint.controls == nil {
		trip.Energy, err = engine.buildEnergyPlan(vehicle, &trip, startSOC)
	} else {
		trip.Energy, err = engine.buildEnergyPlanControlled(
			vehicle,
			&trip,
			startSOC,
			blueprint.controls.Chargers,
		)
	}
	if err != nil {
		return domain.Trip{}, err
	}
	if !withinRange(vehicle.Availability, trip.StartAt, trip.EndAt) ||
		!driver.Shift.ContainsRange(domain.TimeRange{Start: trip.StartAt, End: trip.EndAt}) {
		return domain.Trip{}, fmt.Errorf("trip is outside vehicle or driver availability")
	}
	return trip, nil
}

func requestForTasks(
	request domain.TransportRequest,
	taskIDs []domain.TaskID,
) domain.TransportRequest {
	wanted := make(map[domain.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		wanted[taskID] = struct{}{}
	}
	tasks := make([]domain.ServiceTask, 0, len(request.Tasks))
	unitSet := make(map[domain.FulfillmentUnitID]struct{})
	for _, task := range request.Tasks {
		if _, include := wanted[task.ID]; !include {
			continue
		}
		tasks = append(tasks, task)
		for _, unitID := range task.UnitIDs {
			unitSet[unitID] = struct{}{}
		}
	}
	units := make([]domain.FulfillmentUnitID, 0, len(unitSet))
	for unitID := range unitSet {
		units = append(units, unitID)
	}
	slices.Sort(units)
	request.Tasks = tasks
	request.UnitIDs = units
	return request
}

func tripTaskIDs(trip domain.Trip) []domain.TaskID {
	result := make([]domain.TaskID, 0)
	for _, stop := range trip.Stops {
		result = append(result, stop.TaskIDs...)
	}
	return result
}
