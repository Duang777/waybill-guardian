package solve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type cargoAssignment struct {
	cargo      domain.CargoItem
	loadTask   domain.TaskID
	unloadTask domain.TaskID
	loadStop   int
	unloadStop int
}

type packedItem struct {
	assignment cargoAssignment
	placement  domain.Placement
}

type placementCandidate struct {
	placement domain.Placement
	supports  []domain.CargoID
	score     placementScore
}

type placementScore struct {
	z             int64
	extraction    int64
	transverse    int64
	compartmentID domain.CompartmentID
	doorID        domain.DoorID
	orientation   domain.Orientation
}

func (engine *engine) packTrip(
	ctx context.Context,
	vehicle domain.Vehicle,
	trip domain.Trip,
) ([]domain.LoadStage, error) {
	assignments, err := engine.tripCargoAssignments(ctx, trip)
	if err != nil {
		return nil, err
	}
	placements, err := engine.packAssignments(ctx, vehicle, assignments)
	if err != nil {
		return nil, err
	}
	active := make(map[domain.CargoID]bool, len(assignments))
	stages := make([]domain.LoadStage, 0, len(trip.Stops))
	for stopIndex, stop := range trip.Stops {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, taskID := range stop.TaskIDs {
			task := engine.index.tasks[taskID]
			for _, unitID := range task.UnitIDs {
				unit := engine.index.units[unitID]
				for _, cargoID := range unit.CargoIDs {
					switch task.Kind {
					case domain.TaskPickup, domain.TaskDepotLoad:
						active[cargoID] = true
					case domain.TaskDelivery, domain.TaskDepotUnload:
						delete(active, cargoID)
					}
				}
			}
		}
		stagePlacements := make([]domain.Placement, 0, len(active))
		for _, packed := range placements {
			if active[packed.assignment.cargo.ID] {
				stagePlacements = append(stagePlacements, packed.placement)
			}
		}
		slices.SortFunc(stagePlacements, func(left, right domain.Placement) int {
			return strings.Compare(string(left.CargoID), string(right.CargoID))
		})
		center, axles, ok := stageLoads(vehicle, engine.index.cargo, stagePlacements)
		if !ok {
			return nil, fmt.Errorf("load stage %d violates gross weight, axle, or center of mass", stopIndex)
		}
		stage := domain.LoadStage{
			AfterStopIndex: uint32(stopIndex),
			Placements:     stagePlacements,
			AxleLoadsG:     axles,
			CenterOfMassMM: center,
			Rehandles:      []domain.RehandleOperation{},
		}
		if len(stages) > 0 {
			stage.Rehandles, err = engine.rehandlesForTransition(
				ctx,
				vehicle,
				stages[len(stages)-1],
				stage,
			)
			if err != nil {
				return nil, fmt.Errorf("load stage %d: %w", stopIndex, err)
			}
		}
		stages = append(stages, stage)
	}
	return stages, nil
}

func (engine *engine) compileLoadStages(
	ctx context.Context,
	vehicle domain.Vehicle,
	trip domain.Trip,
	decisions []loadStageDecision,
) ([]domain.LoadStage, error) {
	if len(decisions) != len(trip.Stops) {
		return nil, fmt.Errorf(
			"load decision has %d stages for %d stops",
			len(decisions),
			len(trip.Stops),
		)
	}
	assignments, err := engine.tripCargoAssignments(ctx, trip)
	if err != nil {
		return nil, err
	}
	compartments := make(map[domain.CompartmentID]domain.Compartment, len(vehicle.Compartments))
	for _, compartment := range vehicle.Compartments {
		compartments[compartment.ID] = compartment
	}
	doors := make(map[domain.DoorID]domain.Door, len(vehicle.Doors))
	for _, door := range vehicle.Doors {
		doors[door.ID] = door
	}

	result := make([]domain.LoadStage, 0, len(decisions))
	for stageIndex, decision := range decisions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if decision.AfterStopIndex != uint32(stageIndex) {
			return nil, fmt.Errorf(
				"load decision stage %d has after-stop index %d",
				stageIndex,
				decision.AfterStopIndex,
			)
		}
		expected := make(map[domain.CargoID]cargoAssignment)
		for _, assignment := range assignments {
			if assignment.loadStop <= stageIndex && stageIndex < assignment.unloadStop {
				expected[assignment.cargo.ID] = assignment
			}
		}
		placements := make([]domain.Placement, 0, len(decision.Placements))
		seen := make(map[domain.CargoID]struct{}, len(decision.Placements))
		for _, placementDecision := range decision.Placements {
			if _, duplicate := seen[placementDecision.CargoID]; duplicate {
				return nil, fmt.Errorf(
					"load stage %d repeats cargo %q",
					stageIndex,
					placementDecision.CargoID,
				)
			}
			seen[placementDecision.CargoID] = struct{}{}
			assignment, active := expected[placementDecision.CargoID]
			if !active {
				return nil, fmt.Errorf(
					"cargo %q is not active at load stage %d",
					placementDecision.CargoID,
					stageIndex,
				)
			}
			compartment, exists := compartments[placementDecision.CompartmentID]
			if !exists {
				return nil, fmt.Errorf(
					"cargo %q references unknown compartment %q",
					placementDecision.CargoID,
					placementDecision.CompartmentID,
				)
			}
			door, exists := doors[placementDecision.DoorID]
			if !exists || door.CompartmentID != compartment.ID {
				return nil, fmt.Errorf(
					"cargo %q references incompatible door %q",
					placementDecision.CargoID,
					placementDecision.DoorID,
				)
			}
			if !slices.Contains(
				assignment.cargo.AllowedOrientations,
				placementDecision.Orientation,
			) {
				return nil, fmt.Errorf(
					"cargo %q disallows orientation %q",
					placementDecision.CargoID,
					placementDecision.Orientation,
				)
			}
			size, ok := assignment.cargo.SizeMM.Oriented(placementDecision.Orientation)
			if !ok {
				return nil, fmt.Errorf(
					"cargo %q has invalid orientation %q",
					placementDecision.CargoID,
					placementDecision.Orientation,
				)
			}
			placement := domain.Placement{
				CargoID:        placementDecision.CargoID,
				CompartmentID:  compartment.ID,
				PositionMM:     placementDecision.PositionMM,
				SizeMM:         size,
				Orientation:    placementDecision.Orientation,
				LoadAtTaskID:   assignment.loadTask,
				UnloadAtTaskID: assignment.unloadTask,
				DoorID:         door.ID,
			}
			if !compartment.Bounds.Contains(placement.Cuboid()) {
				return nil, fmt.Errorf(
					"cargo %q is outside compartment %q",
					placement.CargoID,
					compartment.ID,
				)
			}
			placements = append(placements, placement)
		}
		if len(seen) != len(expected) {
			missing := make([]domain.CargoID, 0)
			for cargoID := range expected {
				if _, exists := seen[cargoID]; !exists {
					missing = append(missing, cargoID)
				}
			}
			slices.Sort(missing)
			return nil, fmt.Errorf(
				"load stage %d omits active cargo %v",
				stageIndex,
				missing,
			)
		}
		slices.SortFunc(placements, func(left, right domain.Placement) int {
			return strings.Compare(string(left.CargoID), string(right.CargoID))
		})
		center, axles, valid := stageLoads(vehicle, engine.index.cargo, placements)
		if !valid {
			return nil, fmt.Errorf(
				"load stage %d violates gross weight, axle, or center of mass",
				stageIndex,
			)
		}
		stage := domain.LoadStage{
			AfterStopIndex: uint32(stageIndex),
			Placements:     placements,
			AxleLoadsG:     axles,
			CenterOfMassMM: center,
			Rehandles:      []domain.RehandleOperation{},
		}
		if len(result) > 0 {
			stage.Rehandles, err = engine.rehandlesForTransition(
				ctx,
				vehicle,
				result[len(result)-1],
				stage,
			)
			if err != nil {
				return nil, fmt.Errorf("load stage %d: %w", stageIndex, err)
			}
		}
		result = append(result, stage)
	}
	return result, nil
}

func (engine *engine) rehandlesForTransition(
	ctx context.Context,
	vehicle domain.Vehicle,
	previous domain.LoadStage,
	current domain.LoadStage,
) ([]domain.RehandleOperation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	before := make(map[domain.CargoID]domain.Placement, len(previous.Placements))
	for _, placement := range previous.Placements {
		before[placement.CargoID] = placement
	}
	after := make(map[domain.CargoID]domain.Placement, len(current.Placements))
	for _, placement := range current.Placements {
		after[placement.CargoID] = placement
	}
	required := make(map[domain.CargoID]struct{})
	for cargoID, placement := range after {
		if prior, existed := before[cargoID]; existed && prior != placement {
			required[cargoID] = struct{}{}
		}
	}
	doors := make(map[domain.DoorID]domain.Door, len(vehicle.Doors))
	for _, door := range vehicle.Doors {
		doors[door.ID] = door
	}
	for cargoID, placement := range before {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, remains := after[cargoID]; remains {
			continue
		}
		door, exists := doors[placement.DoorID]
		if !exists {
			return nil, fmt.Errorf(
				"removed cargo %q references unknown door %q",
				cargoID,
				placement.DoorID,
			)
		}
		corridor, reachable := extractionCorridor(placement.Cuboid(), door)
		if !reachable {
			return nil, fmt.Errorf("removed cargo %q cannot reach door %q", cargoID, door.ID)
		}
		for blockerID, blocker := range after {
			if blocker.CompartmentID == placement.CompartmentID &&
				corridor.IntersectsOpen(blocker.Cuboid()) {
				required[blockerID] = struct{}{}
			}
		}
	}
	if len(required) > int(engine.problem.Policy.MaxRehandlesPerStop) {
		return nil, fmt.Errorf(
			"%d rehandles exceed policy limit %d",
			len(required),
			engine.problem.Policy.MaxRehandlesPerStop,
		)
	}
	if len(required) == 0 {
		return []domain.RehandleOperation{}, nil
	}
	if engine.problem.Policy.RehandleSecondsPerCargo <= 0 {
		return nil, fmt.Errorf("rehandle duration must be positive")
	}
	if engine.problem.Policy.RehandleCostCentsPerCargo < 0 {
		return nil, fmt.Errorf("rehandle cost must be non-negative")
	}
	cargoIDs := make([]domain.CargoID, 0, len(required))
	for cargoID := range required {
		cargoIDs = append(cargoIDs, cargoID)
	}
	slices.SortFunc(cargoIDs, func(left, right domain.CargoID) int {
		leftPlacement := before[left]
		rightPlacement := before[right]
		if result := strings.Compare(
			string(leftPlacement.CompartmentID),
			string(rightPlacement.CompartmentID),
		); result != 0 {
			return result
		}
		if result := strings.Compare(
			string(leftPlacement.DoorID),
			string(rightPlacement.DoorID),
		); result != 0 {
			return result
		}
		leftDistance := rehandleDoorDistance(leftPlacement, doors[leftPlacement.DoorID])
		rightDistance := rehandleDoorDistance(rightPlacement, doors[rightPlacement.DoorID])
		if leftDistance < rightDistance {
			return -1
		}
		if leftDistance > rightDistance {
			return 1
		}
		return strings.Compare(string(left), string(right))
	})
	result := make([]domain.RehandleOperation, 0, len(cargoIDs))
	for index, cargoID := range cargoIDs {
		result = append(result, domain.RehandleOperation{
			Sequence:        uint16(index + 1),
			CargoID:         cargoID,
			StopIndex:       current.AfterStopIndex,
			Before:          before[cargoID],
			After:           after[cargoID],
			DurationSeconds: engine.problem.Policy.RehandleSecondsPerCargo,
			CostCents:       engine.problem.Policy.RehandleCostCentsPerCargo,
		})
	}
	return result, nil
}

func rehandleDoorDistance(placement domain.Placement, door domain.Door) int64 {
	placementMax := placement.Cuboid().Max()
	doorMax := door.Opening.Max()
	switch {
	case door.ExtractionAxis == domain.AxisX && door.Direction > 0:
		return door.Opening.Origin.X - placementMax.X
	case door.ExtractionAxis == domain.AxisX && door.Direction < 0:
		return placement.PositionMM.X - doorMax.X
	case door.ExtractionAxis == domain.AxisY && door.Direction > 0:
		return door.Opening.Origin.Y - placementMax.Y
	case door.ExtractionAxis == domain.AxisY && door.Direction < 0:
		return placement.PositionMM.Y - doorMax.Y
	default:
		return 0
	}
}

func removeRehandleSchedule(trip *domain.Trip) {
	for segmentIndex := len(trip.Schedule) - 1; segmentIndex >= 0; segmentIndex-- {
		segment := trip.Schedule[segmentIndex]
		if segment.Kind != domain.SegmentRehandle {
			continue
		}
		duration := segment.EndAt.Sub(segment.StartAt)
		trip.Schedule = append(
			trip.Schedule[:segmentIndex],
			trip.Schedule[segmentIndex+1:]...,
		)
		shiftScheduleAndStops(
			trip,
			segmentIndex,
			segment.EndAt,
			-duration,
		)
	}
}

func (engine *engine) applyRehandleSchedule(
	ctx context.Context,
	trip *domain.Trip,
	driverID domain.DriverID,
) error {
	for _, stage := range trip.LoadStages {
		if err := ctx.Err(); err != nil {
			return err
		}
		var seconds int64
		for _, operation := range stage.Rehandles {
			if operation.DurationSeconds <= 0 {
				return fmt.Errorf(
					"cargo %q has invalid rehandle duration %d",
					operation.CargoID,
					operation.DurationSeconds,
				)
			}
			seconds += operation.DurationSeconds
		}
		if seconds == 0 {
			continue
		}
		stopIndex := int(stage.AfterStopIndex)
		if stopIndex < 0 || stopIndex >= len(trip.Stops) {
			return fmt.Errorf("rehandle stop index %d is out of range", stage.AfterStopIndex)
		}
		stop := trip.Stops[stopIndex]
		startAt := stop.DepartureAt
		insertAt := len(trip.Schedule)
		for segmentIndex, segment := range trip.Schedule {
			if !segment.StartAt.Before(startAt) {
				insertAt = segmentIndex
				break
			}
		}
		duration := time.Duration(seconds) * time.Second
		shiftScheduleAndStops(trip, insertAt, startAt, duration)
		trip.Schedule = slices.Insert(trip.Schedule, insertAt, domain.DutySegment{
			Kind:     domain.SegmentRehandle,
			DriverID: driverID,
			From:     stop.LocationID,
			To:       stop.LocationID,
			StartAt:  startAt,
			EndAt:    startAt.Add(duration),
			TaskIDs:  []domain.TaskID{},
		})
	}
	return nil
}

func (engine *engine) tripCargoAssignments(
	ctx context.Context,
	trip domain.Trip,
) ([]cargoAssignment, error) {
	type taskStop struct {
		taskID    domain.TaskID
		stopIndex int
	}
	taskStops := make([]taskStop, 0)
	for stopIndex, stop := range trip.Stops {
		taskIDs := append([]domain.TaskID(nil), stop.TaskIDs...)
		slices.Sort(taskIDs)
		for _, taskID := range taskIDs {
			taskStops = append(taskStops, taskStop{taskID: taskID, stopIndex: stopIndex})
		}
	}
	assignments := make([]cargoAssignment, 0)
	for _, unit := range engine.problem.Units {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var loadTask domain.TaskID
		var unloadTask domain.TaskID
		loadStop := -1
		unloadStop := -1
		for _, current := range taskStops {
			task := engine.index.tasks[current.taskID]
			if !slices.Contains(task.UnitIDs, unit.ID) {
				continue
			}
			switch task.Kind {
			case domain.TaskPickup, domain.TaskDepotLoad:
				if loadTask == "" || current.stopIndex < loadStop {
					loadTask = current.taskID
					loadStop = current.stopIndex
				}
			case domain.TaskDelivery, domain.TaskDepotUnload:
				if unloadTask == "" || current.stopIndex > unloadStop ||
					(current.stopIndex == unloadStop && current.taskID < unloadTask) {
					unloadTask = current.taskID
					unloadStop = current.stopIndex
				}
			}
		}
		if loadTask == "" && unloadTask == "" {
			continue
		}
		if loadTask == "" || unloadTask == "" || loadStop >= unloadStop {
			return nil, fmt.Errorf("unit %q lacks an ordered load and unload task", unit.ID)
		}
		for _, cargoID := range unit.CargoIDs {
			cargo, exists := engine.index.cargo[cargoID]
			if !exists {
				return nil, fmt.Errorf("unit %q references unknown cargo %q", unit.ID, cargoID)
			}
			assignments = append(assignments, cargoAssignment{
				cargo:      cargo,
				loadTask:   loadTask,
				unloadTask: unloadTask,
				loadStop:   loadStop,
				unloadStop: unloadStop,
			})
		}
	}
	slices.SortFunc(assignments, func(left, right cargoAssignment) int {
		if left.unloadStop != right.unloadStop {
			if left.unloadStop > right.unloadStop {
				return -1
			}
			return 1
		}
		if left.cargo.WeightG != right.cargo.WeightG {
			if left.cargo.WeightG > right.cargo.WeightG {
				return -1
			}
			return 1
		}
		leftVolume := left.cargo.SizeMM.VolumeMM3()
		rightVolume := right.cargo.SizeMM.VolumeMM3()
		if leftVolume != rightVolume {
			if leftVolume > rightVolume {
				return -1
			}
			return 1
		}
		return strings.Compare(string(left.cargo.ID), string(right.cargo.ID))
	})
	return assignments, nil
}

func (engine *engine) packAssignments(
	ctx context.Context,
	vehicle domain.Vehicle,
	assignments []cargoAssignment,
) ([]packedItem, error) {
	packed := make([]packedItem, 0, len(assignments))
	supportParents := make(map[domain.CargoID][]domain.CargoID)
	for _, assignment := range assignments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		candidates := make([]placementCandidate, 0)
		for _, compartment := range vehicle.Compartments {
			if !slices.Contains(compartment.TemperatureZones, assignment.cargo.TemperatureZone) ||
				!slices.Contains(compartment.AllowedCargoClasses, assignment.cargo.CargoClass) ||
				!classesCompatible(engine.problem.Policy, compartment.ID, assignment, packed) {
				continue
			}
			for _, door := range vehicle.Doors {
				if door.CompartmentID != compartment.ID {
					continue
				}
				for _, orientation := range assignment.cargo.AllowedOrientations {
					size, valid := assignment.cargo.SizeMM.Oriented(orientation)
					if !valid {
						continue
					}
					points, err := extremePoints(ctx, compartment, door, size, packed)
					if err != nil {
						return nil, err
					}
					for _, point := range points {
						if err := ctx.Err(); err != nil {
							return nil, err
						}
						placement := domain.Placement{
							CargoID:        assignment.cargo.ID,
							CompartmentID:  compartment.ID,
							PositionMM:     point,
							SizeMM:         size,
							Orientation:    orientation,
							LoadAtTaskID:   assignment.loadTask,
							UnloadAtTaskID: assignment.unloadTask,
							DoorID:         door.ID,
						}
						supports, ok, err := engine.placementFeasible(
							ctx,
							compartment,
							door,
							assignment,
							placement,
							packed,
							supportParents,
						)
						if err != nil {
							return nil, err
						}
						if !ok {
							continue
						}
						candidates = append(candidates, placementCandidate{
							placement: placement,
							supports:  supports,
							score:     scorePlacement(placement, door),
						})
					}
				}
			}
		}
		if len(candidates) == 0 {
			return nil, fmt.Errorf("cargo %q has no feasible three-dimensional placement", assignment.cargo.ID)
		}
		slices.SortFunc(candidates, comparePlacementScore)
		chosen := candidates[0]
		packed = append(packed, packedItem{assignment: assignment, placement: chosen.placement})
		supportParents[assignment.cargo.ID] = append([]domain.CargoID(nil), chosen.supports...)
	}
	return packed, nil
}

func classesCompatible(
	policy domain.PlanningPolicy,
	compartmentID domain.CompartmentID,
	assignment cargoAssignment,
	packed []packedItem,
) bool {
	classes := []string{assignment.cargo.CargoClass}
	for _, existing := range packed {
		if existing.placement.CompartmentID != compartmentID ||
			!assignmentsOverlap(assignment, existing.assignment) {
			continue
		}
		if slices.Contains(
			assignment.cargo.IncompatibleClasses,
			existing.assignment.cargo.CargoClass,
		) ||
			slices.Contains(
				existing.assignment.cargo.IncompatibleClasses,
				assignment.cargo.CargoClass,
			) {
			return false
		}
		classes = append(classes, existing.assignment.cargo.CargoClass)
	}
	classes = slices.Compact(slices.Sorted(slices.Values(classes)))
	if len(classes) <= 1 {
		return true
	}
	for _, allowed := range policy.AllowedMixedCargoClasses {
		candidate := slices.Compact(slices.Sorted(slices.Values(allowed)))
		if slices.Equal(candidate, classes) {
			return true
		}
	}
	return false
}

func extremePoints(
	ctx context.Context,
	compartment domain.Compartment,
	door domain.Door,
	size domain.Box,
	packed []packedItem,
) ([]domain.Point3, error) {
	points := []domain.Point3{farFloorPoint(compartment.Bounds, door, size)}
	appendAround := func(box domain.Cuboid) {
		switch door.ExtractionAxis {
		case domain.AxisX:
			if door.Direction > 0 {
				points = append(points, domain.Point3{
					X: box.Max().X,
					Y: compartment.Bounds.Origin.Y,
					Z: compartment.Bounds.Origin.Z,
				})
			} else {
				points = append(points, domain.Point3{
					X: box.Origin.X - size.Length,
					Y: compartment.Bounds.Origin.Y,
					Z: compartment.Bounds.Origin.Z,
				})
			}
			points = append(points, domain.Point3{
				X: farFloorPoint(compartment.Bounds, door, size).X,
				Y: box.Max().Y,
				Z: compartment.Bounds.Origin.Z,
			})
		case domain.AxisY:
			if door.Direction > 0 {
				points = append(points, domain.Point3{
					X: compartment.Bounds.Origin.X,
					Y: box.Max().Y,
					Z: compartment.Bounds.Origin.Z,
				})
			} else {
				points = append(points, domain.Point3{
					X: compartment.Bounds.Origin.X,
					Y: box.Origin.Y - size.Width,
					Z: compartment.Bounds.Origin.Z,
				})
			}
			points = append(points, domain.Point3{
				X: box.Max().X,
				Y: farFloorPoint(compartment.Bounds, door, size).Y,
				Z: compartment.Bounds.Origin.Z,
			})
		}
		points = append(points, domain.Point3{
			X: box.Origin.X,
			Y: box.Origin.Y,
			Z: box.Max().Z,
		})
	}
	for _, existing := range packed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if existing.placement.CompartmentID == compartment.ID {
			appendAround(existing.placement.Cuboid())
		}
	}
	for _, obstacle := range compartment.Obstacles {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		appendAround(obstacle)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	slices.SortFunc(points, func(left, right domain.Point3) int {
		if left.X != right.X {
			if left.X < right.X {
				return -1
			}
			return 1
		}
		if left.Y != right.Y {
			if left.Y < right.Y {
				return -1
			}
			return 1
		}
		if left.Z < right.Z {
			return -1
		}
		if left.Z > right.Z {
			return 1
		}
		return 0
	})
	return slices.Compact(points), nil
}

func farFloorPoint(
	bounds domain.Cuboid,
	door domain.Door,
	size domain.Box,
) domain.Point3 {
	point := bounds.Origin
	switch door.ExtractionAxis {
	case domain.AxisX:
		if door.Direction < 0 {
			point.X = bounds.Max().X - size.Length
		}
	case domain.AxisY:
		if door.Direction < 0 {
			point.Y = bounds.Max().Y - size.Width
		}
	}
	return point
}

func (engine *engine) placementFeasible(
	ctx context.Context,
	compartment domain.Compartment,
	door domain.Door,
	assignment cargoAssignment,
	placement domain.Placement,
	packed []packedItem,
	supportParents map[domain.CargoID][]domain.CargoID,
) ([]domain.CargoID, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	cargo := assignment.cargo
	box := placement.Cuboid()
	if !compartment.Bounds.Contains(box) {
		return nil, false, nil
	}
	for _, obstacle := range compartment.Obstacles {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if box.IntersectsOpen(obstacle) {
			return nil, false, nil
		}
	}
	for _, existing := range packed {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if existing.placement.CompartmentID == compartment.ID &&
			assignmentsOverlap(assignment, existing.assignment) &&
			box.IntersectsOpen(existing.placement.Cuboid()) {
			return nil, false, nil
		}
	}
	corridor, ok := extractionCorridor(box, door)
	if !ok {
		return nil, false, nil
	}
	for _, existing := range packed {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if existing.placement.CompartmentID == compartment.ID &&
			existing.assignment.loadStop < assignment.unloadStop &&
			existing.assignment.unloadStop > assignment.unloadStop &&
			corridor.IntersectsOpen(existing.placement.Cuboid()) {
			return nil, false, nil
		}
	}
	if placement.PositionMM.Z == compartment.Bounds.Origin.Z {
		return []domain.CargoID{}, true, nil
	}
	supports := make([]domain.CargoID, 0)
	var supportArea int64
	for _, existing := range packed {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		existingBox := existing.placement.Cuboid()
		if existing.placement.CompartmentID != compartment.ID ||
			existingBox.Max().Z != placement.PositionMM.Z ||
			!supportCoversLifetime(existing.assignment, assignment) {
			continue
		}
		area := box.IntersectionXYArea(existingBox)
		if area == 0 {
			continue
		}
		supportArea += area
		supports = append(supports, existing.assignment.cargo.ID)
	}
	required := cargo.MinSupportPPM
	if required == 0 {
		required = engine.problem.Policy.DefaultMinSupportPPM
	}
	if supportArea*1_000_000 < placement.SizeMM.Length*placement.SizeMM.Width*required {
		return nil, false, nil
	}
	trialParents := make(map[domain.CargoID][]domain.CargoID, len(supportParents)+1)
	for cargoID, values := range supportParents {
		trialParents[cargoID] = values
	}
	trialParents[cargo.ID] = supports
	withinLimits, err := topLoadsWithinLimits(
		ctx,
		packed,
		assignment,
		placement,
		trialParents,
		engine.index.cargo,
	)
	if err != nil {
		return nil, false, err
	}
	if !withinLimits {
		return nil, false, nil
	}
	return supports, true, nil
}

func assignmentsOverlap(left, right cargoAssignment) bool {
	return left.loadStop < right.unloadStop && right.loadStop < left.unloadStop
}

func supportCoversLifetime(support, child cargoAssignment) bool {
	return support.loadStop <= child.loadStop && support.unloadStop >= child.unloadStop
}

func topLoadsWithinLimits(
	ctx context.Context,
	packed []packedItem,
	assignment cargoAssignment,
	placement domain.Placement,
	parents map[domain.CargoID][]domain.CargoID,
	cargo map[domain.CargoID]domain.CargoItem,
) (bool, error) {
	trial := append(
		append([]packedItem(nil), packed...),
		packedItem{assignment: assignment, placement: placement},
	)
	maxStage := 0
	for _, item := range trial {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if item.assignment.unloadStop > maxStage {
			maxStage = item.assignment.unloadStop
		}
	}
	for stage := 0; stage < maxStage; stage++ {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		active := make(map[domain.CargoID]struct{})
		for _, item := range trial {
			if item.assignment.loadStop <= stage && stage < item.assignment.unloadStop {
				active[item.assignment.cargo.ID] = struct{}{}
			}
		}
		children := make(map[domain.CargoID][]domain.CargoID)
		for childID, supportIDs := range parents {
			if _, exists := active[childID]; !exists {
				continue
			}
			for _, supportID := range supportIDs {
				if _, exists := active[supportID]; exists {
					children[supportID] = append(children[supportID], childID)
				}
			}
		}
		for cargoID := range active {
			topLoad, err := supportedWeight(
				ctx,
				cargoID,
				children,
				cargo,
				make(map[domain.CargoID]struct{}),
			)
			if err != nil {
				return false, err
			}
			item := cargo[cargoID]
			if (item.FragileTopOnly && topLoad > 0) || topLoad > item.MaxTopLoadG {
				return false, nil
			}
		}
	}
	return true, nil
}

func supportedWeight(
	ctx context.Context,
	cargoID domain.CargoID,
	children map[domain.CargoID][]domain.CargoID,
	cargo map[domain.CargoID]domain.CargoItem,
	seen map[domain.CargoID]struct{},
) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var result int64
	for _, childID := range children[cargoID] {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if _, exists := seen[childID]; exists {
			continue
		}
		seen[childID] = struct{}{}
		result += cargo[childID].WeightG
		childWeight, err := supportedWeight(ctx, childID, children, cargo, seen)
		if err != nil {
			return 0, err
		}
		result += childWeight
	}
	return result, nil
}

func extractionCorridor(cargo domain.Cuboid, door domain.Door) (domain.Cuboid, bool) {
	cargoMax := cargo.Max()
	doorMax := door.Opening.Max()
	switch {
	case door.ExtractionAxis == domain.AxisX && door.Direction > 0:
		if door.Opening.Origin.X < cargoMax.X ||
			cargo.Origin.Y < door.Opening.Origin.Y || cargoMax.Y > doorMax.Y ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: cargoMax.X, Y: cargo.Origin.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: door.Opening.Origin.X - cargoMax.X,
				Width:  cargo.Size.Width,
				Height: cargo.Size.Height,
			},
		}, true
	case door.ExtractionAxis == domain.AxisX && door.Direction < 0:
		if doorMax.X > cargo.Origin.X ||
			cargo.Origin.Y < door.Opening.Origin.Y || cargoMax.Y > doorMax.Y ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: doorMax.X, Y: cargo.Origin.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: cargo.Origin.X - doorMax.X,
				Width:  cargo.Size.Width,
				Height: cargo.Size.Height,
			},
		}, true
	case door.ExtractionAxis == domain.AxisY && door.Direction > 0:
		if door.Opening.Origin.Y < cargoMax.Y ||
			cargo.Origin.X < door.Opening.Origin.X || cargoMax.X > doorMax.X ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: cargo.Origin.X, Y: cargoMax.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: cargo.Size.Length,
				Width:  door.Opening.Origin.Y - cargoMax.Y,
				Height: cargo.Size.Height,
			},
		}, true
	case door.ExtractionAxis == domain.AxisY && door.Direction < 0:
		if doorMax.Y > cargo.Origin.Y ||
			cargo.Origin.X < door.Opening.Origin.X || cargoMax.X > doorMax.X ||
			cargo.Origin.Z < door.Opening.Origin.Z || cargoMax.Z > doorMax.Z {
			return domain.Cuboid{}, false
		}
		return domain.Cuboid{
			Origin: domain.Point3{X: cargo.Origin.X, Y: doorMax.Y, Z: cargo.Origin.Z},
			Size: domain.Box{
				Length: cargo.Size.Length,
				Width:  cargo.Origin.Y - doorMax.Y,
				Height: cargo.Size.Height,
			},
		}, true
	default:
		return domain.Cuboid{}, false
	}
}

func scorePlacement(placement domain.Placement, door domain.Door) placementScore {
	extraction := placement.PositionMM.X
	transverse := placement.PositionMM.Y
	if door.ExtractionAxis == domain.AxisX {
		if door.Direction < 0 {
			extraction = -placement.PositionMM.X
		}
	} else {
		extraction = placement.PositionMM.Y
		transverse = placement.PositionMM.X
		if door.Direction < 0 {
			extraction = -placement.PositionMM.Y
		}
	}
	return placementScore{
		z:             placement.PositionMM.Z,
		extraction:    extraction,
		transverse:    transverse,
		compartmentID: placement.CompartmentID,
		doorID:        placement.DoorID,
		orientation:   placement.Orientation,
	}
}

func comparePlacementScore(left, right placementCandidate) int {
	switch {
	case left.score.z < right.score.z:
		return -1
	case left.score.z > right.score.z:
		return 1
	case left.score.extraction < right.score.extraction:
		return -1
	case left.score.extraction > right.score.extraction:
		return 1
	case left.score.transverse < right.score.transverse:
		return -1
	case left.score.transverse > right.score.transverse:
		return 1
	default:
		if result := strings.Compare(
			string(left.score.compartmentID),
			string(right.score.compartmentID),
		); result != 0 {
			return result
		}
		if result := strings.Compare(
			string(left.score.doorID),
			string(right.score.doorID),
		); result != 0 {
			return result
		}
		return strings.Compare(string(left.score.orientation), string(right.score.orientation))
	}
}

func stageLoads(
	vehicle domain.Vehicle,
	cargo map[domain.CargoID]domain.CargoItem,
	placements []domain.Placement,
) (domain.Point3, []int64, bool) {
	payloadByCompartment := make(map[domain.CompartmentID]int64)
	compartmentCapacity := make(map[domain.CompartmentID]int64)
	for _, compartment := range vehicle.Compartments {
		compartmentCapacity[compartment.ID] = compartment.MaxPayloadG
	}
	var totalWeight int64
	var weightedX int64
	var weightedY int64
	var weightedZ int64
	for _, placement := range placements {
		item := cargo[placement.CargoID]
		payloadByCompartment[placement.CompartmentID] += item.WeightG
		totalWeight += item.WeightG
		weightedX += (placement.PositionMM.X + placement.SizeMM.Length/2) * item.WeightG
		weightedY += (placement.PositionMM.Y + placement.SizeMM.Width/2) * item.WeightG
		weightedZ += (placement.PositionMM.Z + placement.SizeMM.Height/2) * item.WeightG
	}
	for compartmentID, payload := range payloadByCompartment {
		if payload > compartmentCapacity[compartmentID] {
			return domain.Point3{}, nil, false
		}
	}
	if vehicle.TareWeightG+totalWeight > vehicle.MaxGrossWeightG {
		return domain.Point3{}, nil, false
	}
	center := domain.Point3{}
	if totalWeight > 0 {
		center = domain.Point3{
			X: weightedX / totalWeight,
			Y: weightedY / totalWeight,
			Z: weightedZ / totalWeight,
		}
	}
	if !insideEnvelope(center, vehicle.CGEnvelope) {
		return domain.Point3{}, nil, false
	}
	axles := computeAxleLoads(vehicle.Axles, totalWeight, center.X)
	for index, load := range axles {
		if load > vehicle.Axles[index].MaxLoadG {
			return domain.Point3{}, nil, false
		}
	}
	return center, axles, true
}

func insideEnvelope(point domain.Point3, envelope domain.CGEnvelope) bool {
	return point.X >= envelope.Min.X && point.X <= envelope.Max.X &&
		point.Y >= envelope.Min.Y && point.Y <= envelope.Max.Y &&
		point.Z >= envelope.Min.Z && point.Z <= envelope.Max.Z
}

func computeAxleLoads(
	axles []domain.Axle,
	totalWeight int64,
	centerX int64,
) []int64 {
	result := make([]int64, len(axles))
	if totalWeight == 0 || len(axles) == 0 {
		return result
	}
	type indexedAxle struct {
		index int
		axle  domain.Axle
	}
	ordered := make([]indexedAxle, len(axles))
	for index, axle := range axles {
		ordered[index] = indexedAxle{index: index, axle: axle}
	}
	slices.SortFunc(ordered, func(left, right indexedAxle) int {
		if left.axle.PositionXMM < right.axle.PositionXMM {
			return -1
		}
		if left.axle.PositionXMM > right.axle.PositionXMM {
			return 1
		}
		return 0
	})
	if centerX <= ordered[0].axle.PositionXMM {
		result[ordered[0].index] = totalWeight
		return result
	}
	last := ordered[len(ordered)-1]
	if centerX >= last.axle.PositionXMM {
		result[last.index] = totalWeight
		return result
	}
	for index := 0; index+1 < len(ordered); index++ {
		left := ordered[index]
		right := ordered[index+1]
		if centerX < left.axle.PositionXMM || centerX > right.axle.PositionXMM {
			continue
		}
		distance := right.axle.PositionXMM - left.axle.PositionXMM
		if distance == 0 {
			result[left.index] = totalWeight
			return result
		}
		rightLoad := totalWeight * (centerX - left.axle.PositionXMM) / distance
		result[left.index] = totalWeight - rightLoad
		result[right.index] = rightLoad
		break
	}
	return result
}
