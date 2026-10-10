package solve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (engine *engine) buildTrip(
	ctx context.Context,
	request domain.TransportRequest,
	vehicle domain.Vehicle,
	driver domain.Driver,
	duty domain.VehicleDuty,
) (domain.Trip, error) {
	if err := ctx.Err(); err != nil {
		return domain.Trip{}, err
	}
	depot, exists := engine.index.depots[vehicle.HomeDepotID]
	if !exists || !depot.AllowTripStart || !depot.AllowTripEnd {
		return domain.Trip{}, fmt.Errorf("vehicle home depot cannot bound a trip")
	}
	if driver.StartLocation != depot.LocationID ||
		(len(driver.EndLocations) > 0 && !slices.Contains(driver.EndLocations, depot.LocationID)) {
		return domain.Trip{}, fmt.Errorf("driver is not based at vehicle home depot")
	}
	if err := engine.checkCommitmentAssignment(request, vehicle.ID, driver.ID); err != nil {
		return domain.Trip{}, err
	}
	for _, task := range request.Tasks {
		required := append(append(domain.SkillSet(nil), request.RequiredSkills...), task.RequiredSkills...)
		if !containsSkills(vehicle.Skills, required) || !containsSkills(driver.Skills, required) {
			return domain.Trip{}, fmt.Errorf("vehicle or driver lacks task skills")
		}
	}

	tasks, err := engine.orderTasks(request, depot.LocationID)
	if err != nil {
		return domain.Trip{}, err
	}
	startAt, err := engine.tripStart(vehicle, driver, duty)
	if err != nil {
		return domain.Trip{}, err
	}
	tripID := domain.TripID(fmt.Sprintf(
		"%s-trip-%03d",
		vehicle.ID,
		len(duty.Trips)+1,
	))
	trip, _, err := engine.scheduleTrip(
		tripID,
		depot,
		depot,
		tasks,
		driver,
		startAt,
		len(duty.Trips) > 0,
	)
	if err != nil {
		return domain.Trip{}, err
	}
	trip.LoadStages, err = engine.packTrip(ctx, vehicle, trip)
	if err != nil {
		return domain.Trip{}, err
	}
	if err := engine.applyRehandleSchedule(ctx, &trip, driver.ID); err != nil {
		return domain.Trip{}, err
	}
	startSOC, err := tripStartSOC(vehicle, duty)
	if err != nil {
		return domain.Trip{}, err
	}
	trip.Energy, err = engine.buildEnergyPlan(vehicle, &trip, startSOC)
	if err != nil {
		return domain.Trip{}, err
	}
	if !withinRange(vehicle.Availability, trip.StartAt, trip.EndAt) ||
		!driver.Shift.ContainsRange(domain.TimeRange{Start: trip.StartAt, End: trip.EndAt}) {
		return domain.Trip{}, fmt.Errorf("trip is outside vehicle or driver availability")
	}
	if err := checkDriverLimits(driver, appendDutyTrips(duty.Trips, trip)); err != nil {
		return domain.Trip{}, err
	}
	return trip, nil
}

func tripStartSOC(vehicle domain.Vehicle, duty domain.VehicleDuty) (int64, error) {
	if vehicle.Energy.Kind != domain.EnergyElectric || len(duty.Trips) == 0 {
		return vehicle.Energy.InitialSOCWh, nil
	}
	previous := duty.Trips[len(duty.Trips)-1]
	if len(previous.Energy) == 0 {
		return 0, fmt.Errorf("previous electric trip has no energy state")
	}
	return previous.Energy[len(previous.Energy)-1].EndSOCWh, nil
}

func (engine *engine) checkCommitmentAssignment(
	request domain.TransportRequest,
	vehicleID domain.VehicleID,
	driverID domain.DriverID,
) error {
	taskIDs := make(map[domain.TaskID]struct{}, len(request.Tasks))
	for _, task := range request.Tasks {
		taskIDs[task.ID] = struct{}{}
	}
	for _, commitment := range engine.problem.Commitments.Executed {
		if _, applies := taskIDs[commitment.TaskID]; applies &&
			(commitment.VehicleID != vehicleID || commitment.DriverID != driverID) {
			return fmt.Errorf("executed commitment fixes another vehicle or driver")
		}
	}
	for _, commitment := range engine.problem.Commitments.Frozen {
		if _, applies := taskIDs[commitment.TaskID]; applies &&
			(commitment.VehicleID != vehicleID || commitment.DriverID != driverID) {
			return fmt.Errorf("frozen commitment fixes another vehicle or driver")
		}
	}
	return nil
}

func (engine *engine) tripStart(
	vehicle domain.Vehicle,
	driver domain.Driver,
	duty domain.VehicleDuty,
) (time.Time, error) {
	earliest := engine.problem.Horizon.Start
	if driver.Shift.Start.After(earliest) {
		earliest = driver.Shift.Start
	}
	if len(duty.Trips) > 0 {
		previousEnd := duty.Trips[len(duty.Trips)-1].EndAt
		if previousEnd.After(earliest) {
			earliest = previousEnd
		}
	}
	for _, availability := range vehicle.Availability {
		candidate := earliest
		if availability.Start.After(candidate) {
			candidate = availability.Start
		}
		if !candidate.After(availability.End) &&
			!candidate.After(driver.Shift.End) &&
			!candidate.After(engine.problem.Horizon.End) {
			return candidate, nil
		}
	}
	return time.Time{}, fmt.Errorf("no common vehicle, driver, and horizon start")
}

func (engine *engine) orderTasks(
	request domain.TransportRequest,
	start domain.LocationID,
) ([]domain.ServiceTask, error) {
	byID := make(map[domain.TaskID]domain.ServiceTask, len(request.Tasks))
	remainingPredecessors := make(map[domain.TaskID]int, len(request.Tasks))
	successors := make(map[domain.TaskID][]domain.TaskID)
	for _, task := range request.Tasks {
		byID[task.ID] = task
		remainingPredecessors[task.ID] = len(task.PredecessorIDs)
		for _, predecessorID := range task.PredecessorIDs {
			successors[predecessorID] = append(successors[predecessorID], task.ID)
		}
	}
	result := make([]domain.ServiceTask, 0, len(request.Tasks))
	current := start
	for len(result) < len(request.Tasks) {
		available := make([]domain.ServiceTask, 0)
		for taskID, count := range remainingPredecessors {
			if count == 0 {
				available = append(available, byID[taskID])
			}
		}
		if len(available) == 0 {
			return nil, fmt.Errorf("task graph has no available task")
		}
		slices.SortFunc(available, func(left, right domain.ServiceTask) int {
			leftFrozen, leftSequence := engine.frozenSequence(left.ID)
			rightFrozen, rightSequence := engine.frozenSequence(right.ID)
			if leftFrozen != rightFrozen {
				if leftFrozen {
					return -1
				}
				return 1
			}
			if leftFrozen && leftSequence != rightSequence {
				if leftSequence < rightSequence {
					return -1
				}
				return 1
			}
			leftDistance, _ := engine.travelDistance(current, left.LocationID)
			rightDistance, _ := engine.travelDistance(current, right.LocationID)
			if leftDistance != rightDistance {
				if leftDistance < rightDistance {
					return -1
				}
				return 1
			}
			leftDeadline := earliestDeadline(left.HardWindows)
			rightDeadline := earliestDeadline(right.HardWindows)
			if !leftDeadline.Equal(rightDeadline) {
				return leftDeadline.Compare(rightDeadline)
			}
			return strings.Compare(string(left.ID), string(right.ID))
		})
		chosen := available[0]
		result = append(result, chosen)
		current = chosen.LocationID
		delete(remainingPredecessors, chosen.ID)
		for _, successorID := range successors[chosen.ID] {
			remainingPredecessors[successorID]--
		}
	}
	return result, nil
}

func (engine *engine) frozenSequence(taskID domain.TaskID) (bool, uint32) {
	for _, commitment := range engine.problem.Commitments.Frozen {
		if commitment.TaskID == taskID {
			return true, commitment.Sequence
		}
	}
	return false, 0
}

func earliestDeadline(windows []domain.TimeRange) time.Time {
	if len(windows) == 0 {
		return time.Unix(1<<62, 0)
	}
	result := windows[0].End
	for _, window := range windows[1:] {
		if window.End.Before(result) {
			result = window.End
		}
	}
	return result
}

func (engine *engine) scheduleTrip(
	tripID domain.TripID,
	startDepot domain.Depot,
	endDepot domain.Depot,
	tasks []domain.ServiceTask,
	driver domain.Driver,
	startAt time.Time,
	resetDriver bool,
) (domain.Trip, int64, error) {
	return engine.scheduleTripControlled(
		tripID,
		startDepot,
		endDepot,
		tasks,
		driver,
		startAt,
		resetDriver,
		nil,
		false,
	)
}

func (engine *engine) scheduleTripControlled(
	tripID domain.TripID,
	startDepot domain.Depot,
	endDepot domain.Depot,
	tasks []domain.ServiceTask,
	driver domain.Driver,
	startAt time.Time,
	resetDriver bool,
	breaks []breakDirective,
	exactBreaks bool,
) (domain.Trip, int64, error) {
	stops := make([]domain.Stop, 0, len(tasks)+2)
	schedule := make([]domain.DutySegment, 0, len(tasks)*3+2)
	currentLocation := startDepot.LocationID
	currentTime := startAt
	continuousDrive := int64(0)
	totalDrive := int64(0)
	totalDistance := int64(0)
	breakIndex := 0
	if !exactBreaks && resetDriver && driver.Regulation.RequiredBreakSeconds > 0 {
		end := currentTime.Add(
			time.Duration(driver.Regulation.RequiredBreakSeconds) * time.Second,
		)
		schedule = append(schedule, domain.DutySegment{
			Kind:     domain.SegmentBreak,
			DriverID: driver.ID,
			From:     startDepot.LocationID,
			To:       startDepot.LocationID,
			StartAt:  currentTime,
			EndAt:    end,
			TaskIDs:  []domain.TaskID{},
		})
		currentTime = end
	}

	hasInitialStop := len(tasks) == 0 || tasks[0].LocationID != startDepot.LocationID
	if exactBreaks && hasInitialStop {
		var reset bool
		currentTime, reset, breakIndex = applyBreaksBeforeStop(
			breaks,
			breakIndex,
			0,
			currentLocation,
			driver,
			currentTime,
			&schedule,
		)
		if reset {
			continuousDrive = 0
		}
	}
	if hasInitialStop {
		stops = append(stops, domain.Stop{
			LocationID:  startDepot.LocationID,
			TaskIDs:     []domain.TaskID{},
			ArrivalAt:   currentTime,
			ServiceAt:   currentTime,
			DepartureAt: currentTime,
		})
	}

	for _, task := range tasks {
		reset := false
		if exactBreaks {
			currentTime, reset, breakIndex = applyBreaksBeforeStop(
				breaks,
				breakIndex,
				uint32(len(stops)),
				currentLocation,
				driver,
				currentTime,
				&schedule,
			)
			if reset {
				continuousDrive = 0
			}
		}
		arrival, nextTime, driveSeconds, distance, err := engine.travel(
			currentLocation,
			task.LocationID,
			currentTime,
			driver,
			continuousDrive,
			&schedule,
			!exactBreaks,
		)
		if err != nil {
			return domain.Trip{}, 0, err
		}
		if exactBreaks {
			continuousDrive += driveSeconds
		} else if driveSeconds > 0 && continuousDrive+driveSeconds >
			driver.Regulation.MaxContinuousDriveSeconds &&
			driver.Regulation.MaxContinuousDriveSeconds > 0 {
			continuousDrive = driveSeconds
		} else {
			continuousDrive += driveSeconds
		}
		totalDrive += driveSeconds
		totalDistance += distance
		currentTime = nextTime
		serviceAt, ok := serviceTime(currentTime, task.HardWindows)
		if !ok {
			return domain.Trip{}, 0, fmt.Errorf("task %q misses every hard window", task.ID)
		}
		if serviceAt.After(currentTime) {
			schedule = append(schedule, domain.DutySegment{
				Kind:     domain.SegmentWait,
				DriverID: driver.ID,
				From:     task.LocationID,
				To:       task.LocationID,
				StartAt:  currentTime,
				EndAt:    serviceAt,
				TaskIDs:  []domain.TaskID{},
			})
		}
		departure := serviceAt.Add(time.Duration(task.ServiceSeconds) * time.Second)
		if task.ServiceSeconds > 0 {
			schedule = append(schedule, domain.DutySegment{
				Kind:     domain.SegmentService,
				DriverID: driver.ID,
				From:     task.LocationID,
				To:       task.LocationID,
				StartAt:  serviceAt,
				EndAt:    departure,
				TaskIDs:  []domain.TaskID{task.ID},
			})
		}
		stops = append(stops, domain.Stop{
			LocationID:  task.LocationID,
			TaskIDs:     []domain.TaskID{task.ID},
			ArrivalAt:   arrival,
			ServiceAt:   serviceAt,
			DepartureAt: departure,
		})
		currentLocation = task.LocationID
		currentTime = departure
	}

	if exactBreaks {
		var reset bool
		currentTime, reset, breakIndex = applyBreaksBeforeStop(
			breaks,
			breakIndex,
			uint32(len(stops)),
			currentLocation,
			driver,
			currentTime,
			&schedule,
		)
		if reset {
			continuousDrive = 0
		}
	}
	arrival, nextTime, driveSeconds, distance, err := engine.travel(
		currentLocation,
		endDepot.LocationID,
		currentTime,
		driver,
		continuousDrive,
		&schedule,
		!exactBreaks,
	)
	if err != nil {
		return domain.Trip{}, 0, err
	}
	if exactBreaks {
		continuousDrive += driveSeconds
	}
	totalDrive += driveSeconds
	totalDistance += distance
	currentTime = nextTime
	if len(stops) == 0 || stops[len(stops)-1].LocationID != endDepot.LocationID ||
		len(stops[len(stops)-1].TaskIDs) > 0 {
		stops = append(stops, domain.Stop{
			LocationID:  endDepot.LocationID,
			TaskIDs:     []domain.TaskID{},
			ArrivalAt:   arrival,
			ServiceAt:   arrival,
			DepartureAt: arrival,
		})
	}
	if len(schedule) == 0 {
		end := currentTime.Add(time.Second)
		schedule = append(schedule, domain.DutySegment{
			Kind:     domain.SegmentWait,
			DriverID: driver.ID,
			From:     endDepot.LocationID,
			To:       endDepot.LocationID,
			StartAt:  currentTime,
			EndAt:    end,
			TaskIDs:  []domain.TaskID{},
		})
		currentTime = end
		stops[len(stops)-1].DepartureAt = end
	}
	if exactBreaks && breakIndex != len(breaks) {
		return domain.Trip{}, 0, fmt.Errorf(
			"break before stop %d is outside the compiled route",
			breaks[breakIndex].BeforeStopIndex,
		)
	}
	if driver.Regulation.MaxDriveSeconds > 0 &&
		totalDrive > driver.Regulation.MaxDriveSeconds {
		return domain.Trip{}, 0, fmt.Errorf("trip exceeds driver maximum drive time")
	}
	if driver.Regulation.MaxDutySeconds > 0 &&
		int64(currentTime.Sub(startAt)/time.Second) > driver.Regulation.MaxDutySeconds {
		return domain.Trip{}, 0, fmt.Errorf("trip exceeds driver maximum duty time")
	}
	return domain.Trip{
		ID:           tripID,
		StartDepotID: startDepot.ID,
		EndDepotID:   endDepot.ID,
		StartAt:      schedule[0].StartAt,
		EndAt:        currentTime,
		Stops:        stops,
		Schedule:     schedule,
		Energy:       []domain.EnergyLeg{},
		LoadStages:   []domain.LoadStage{},
	}, totalDistance, nil
}

func applyBreaksBeforeStop(
	breaks []breakDirective,
	start int,
	stopIndex uint32,
	location domain.LocationID,
	driver domain.Driver,
	at time.Time,
	schedule *[]domain.DutySegment,
) (time.Time, bool, int) {
	current := at
	reset := false
	index := start
	for index < len(breaks) && breaks[index].BeforeStopIndex == stopIndex {
		seconds := breaks[index].DurationSeconds
		end := current.Add(time.Duration(seconds) * time.Second)
		*schedule = append(*schedule, domain.DutySegment{
			Kind:     domain.SegmentBreak,
			DriverID: driver.ID,
			From:     location,
			To:       location,
			StartAt:  current,
			EndAt:    end,
			TaskIDs:  []domain.TaskID{},
		})
		if seconds >= driver.Regulation.RequiredBreakSeconds {
			reset = true
		}
		current = end
		index++
	}
	return current, reset, index
}

func (engine *engine) travel(
	from domain.LocationID,
	to domain.LocationID,
	start time.Time,
	driver domain.Driver,
	continuousDrive int64,
	schedule *[]domain.DutySegment,
	automaticBreaks bool,
) (time.Time, time.Time, int64, int64, error) {
	seconds, ok := engine.travelSeconds(from, to)
	if !ok {
		return time.Time{}, time.Time{}, 0, 0, fmt.Errorf("travel arc %q -> %q is missing", from, to)
	}
	distance, ok := engine.travelDistance(from, to)
	if !ok {
		return time.Time{}, time.Time{}, 0, 0, fmt.Errorf("distance arc %q -> %q is missing", from, to)
	}
	current := start
	if automaticBreaks &&
		seconds > 0 && driver.Regulation.MaxContinuousDriveSeconds > 0 &&
		continuousDrive+seconds > driver.Regulation.MaxContinuousDriveSeconds {
		breakSeconds := driver.Regulation.RequiredBreakSeconds
		if breakSeconds <= 0 {
			breakSeconds = 1
		}
		end := current.Add(time.Duration(breakSeconds) * time.Second)
		*schedule = append(*schedule, domain.DutySegment{
			Kind:     domain.SegmentBreak,
			DriverID: driver.ID,
			From:     from,
			To:       from,
			StartAt:  current,
			EndAt:    end,
			TaskIDs:  []domain.TaskID{},
		})
		current = end
	}
	arrival := current.Add(time.Duration(seconds) * time.Second)
	if seconds > 0 {
		*schedule = append(*schedule, domain.DutySegment{
			Kind:     domain.SegmentDrive,
			DriverID: driver.ID,
			From:     from,
			To:       to,
			StartAt:  current,
			EndAt:    arrival,
			TaskIDs:  []domain.TaskID{},
		})
	}
	return arrival, arrival, seconds, distance, nil
}

func serviceTime(arrival time.Time, windows []domain.TimeRange) (time.Time, bool) {
	if len(windows) == 0 {
		return arrival, true
	}
	for _, window := range windows {
		candidate := arrival
		if window.Start.After(candidate) {
			candidate = window.Start
		}
		if !candidate.After(window.End) {
			return candidate, true
		}
	}
	return time.Time{}, false
}

func (engine *engine) travelSeconds(from, to domain.LocationID) (int64, bool) {
	return engine.matrixValue(engine.problem.Travel.TravelSeconds, engine.index.travelIndex, from, to)
}

func (engine *engine) travelDistance(from, to domain.LocationID) (int64, bool) {
	return engine.matrixValue(engine.problem.Travel.DistanceMeters, engine.index.travelIndex, from, to)
}

func (engine *engine) matrixValue(
	values []int64,
	index map[domain.LocationID]int,
	from domain.LocationID,
	to domain.LocationID,
) (int64, bool) {
	fromIndex, fromExists := index[from]
	toIndex, toExists := index[to]
	size := len(index)
	if !fromExists || !toExists || len(values) != size*size {
		return 0, false
	}
	return values[fromIndex*size+toIndex], true
}

func withinRange(values []domain.TimeRange, start, end time.Time) bool {
	wanted := domain.TimeRange{Start: start, End: end}
	for _, value := range values {
		if value.ContainsRange(wanted) {
			return true
		}
	}
	return false
}

func appendDutyTrips(trips []domain.Trip, trip domain.Trip) []domain.Trip {
	result := append([]domain.Trip(nil), trips...)
	return append(result, trip)
}

func checkDriverLimits(driver domain.Driver, trips []domain.Trip) error {
	var segments []domain.DutySegment
	for _, trip := range trips {
		for _, segment := range trip.Schedule {
			if segment.DriverID == driver.ID {
				segments = append(segments, segment)
			}
		}
	}
	if len(segments) == 0 {
		return fmt.Errorf("driver has no schedule")
	}
	slices.SortFunc(segments, func(left, right domain.DutySegment) int {
		return left.StartAt.Compare(right.StartAt)
	})
	var totalDrive int64
	var continuousDrive int64
	for _, segment := range segments {
		seconds := int64(segment.EndAt.Sub(segment.StartAt) / time.Second)
		switch segment.Kind {
		case domain.SegmentDrive:
			totalDrive += seconds
			continuousDrive += seconds
			if driver.Regulation.MaxContinuousDriveSeconds > 0 &&
				continuousDrive > driver.Regulation.MaxContinuousDriveSeconds {
				return fmt.Errorf("driver continuous drive limit exceeded")
			}
		case domain.SegmentBreak:
			if seconds >= driver.Regulation.RequiredBreakSeconds {
				continuousDrive = 0
			}
		}
	}
	if driver.Regulation.MaxDriveSeconds > 0 &&
		totalDrive > driver.Regulation.MaxDriveSeconds {
		return fmt.Errorf("driver total drive limit exceeded")
	}
	dutySeconds := int64(segments[len(segments)-1].EndAt.Sub(segments[0].StartAt) / time.Second)
	if driver.Regulation.MaxDutySeconds > 0 &&
		dutySeconds > driver.Regulation.MaxDutySeconds {
		return fmt.Errorf("driver duty limit exceeded")
	}
	return nil
}
