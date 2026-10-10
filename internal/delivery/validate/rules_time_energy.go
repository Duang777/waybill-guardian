package validate

import (
	"fmt"
	"slices"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (state *validationState) validateSchedules() {
	segmentsByDriver := make(map[domain.DriverID][]domain.DutySegment)
	for dutyIndex, duty := range state.plan.Duties {
		for tripIndex, trip := range duty.Trips {
			if trip.StartAt.After(trip.EndAt) || len(trip.Stops) == 0 ||
				trip.Stops[0].ArrivalAt.Before(trip.StartAt) ||
				trip.Stops[len(trip.Stops)-1].DepartureAt.After(trip.EndAt) {
				state.add("V502", domain.SeverityError, "trip", ref(trip.ID),
					"increasing trip and stop timestamps", "trip timestamps are inconsistent",
					position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
			}
			if len(trip.Schedule) == 0 {
				state.add("V504", domain.SeverityError, "trip", ref(trip.ID),
					"non-empty schedule covering the trip", "schedule is empty",
					position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
			}
			for stopIndex, stop := range trip.Stops {
				if stop.ArrivalAt.After(stop.ServiceAt) ||
					stop.ServiceAt.After(stop.DepartureAt) {
					state.add("V502", domain.SeverityError, "trip", ref(trip.ID),
						"arrival <= service <= departure", "stop timestamps are inconsistent",
						atStop(dutyIndex, tripIndex, stopIndex))
				}
				requiredService := int64(0)
				for _, taskID := range stop.TaskIDs {
					definition, exists := state.tasks[taskID]
					if !exists {
						continue
					}
					requiredService += definition.task.ServiceSeconds
					if len(definition.task.HardWindows) > 0 &&
						!instantInAnyRange(definition.task.HardWindows, stop.ServiceAt) {
						state.add("V501", domain.SeverityError, "task", ref(taskID),
							"service begins inside a hard window",
							stop.ServiceAt.Format(time.RFC3339Nano),
							atStop(dutyIndex, tripIndex, stopIndex))
					}
				}
				for _, stage := range trip.LoadStages {
					if int(stage.AfterStopIndex) != stopIndex {
						continue
					}
					for _, operation := range stage.Rehandles {
						requiredService += operation.DurationSeconds
					}
				}
				actualService := durationSeconds(stop.ServiceAt, stop.DepartureAt)
				if actualService < requiredService {
					state.add("V502", domain.SeverityError, "trip", ref(trip.ID),
						fmt.Sprintf("at least %d service seconds", requiredService),
						fmt.Sprintf("%d service seconds", actualService),
						atStop(dutyIndex, tripIndex, stopIndex))
				}
				if stopIndex > 0 {
					previous := trip.Stops[stopIndex-1]
					travelSeconds, exists := state.matrixValue(
						state.problem.Travel.TravelSeconds,
						previous.LocationID,
						stop.LocationID,
					)
					if exists {
						earliestArrival := previous.DepartureAt.Add(
							time.Duration(travelSeconds) * time.Second,
						)
						if stop.ArrivalAt.Before(earliestArrival) {
							state.add("V503", domain.SeverityError, "trip", ref(trip.ID),
								earliestArrival.Format(time.RFC3339Nano),
								stop.ArrivalAt.Format(time.RFC3339Nano),
								atStop(dutyIndex, tripIndex, stopIndex))
						}
					}
				}
			}

			for segmentIndex, segment := range trip.Schedule {
				if !segment.StartAt.Before(segment.EndAt) {
					state.add("V504", domain.SeverityError, "trip", ref(trip.ID),
						"positive segment duration", "zero or negative duration",
						atSegment(dutyIndex, tripIndex, segmentIndex))
				}
				if segmentIndex == 0 && !segment.StartAt.Equal(trip.StartAt) {
					state.add("V504", domain.SeverityError, "trip", ref(trip.ID),
						"schedule starts with trip", segment.StartAt.Format(time.RFC3339Nano),
						atSegment(dutyIndex, tripIndex, segmentIndex))
				}
				if segmentIndex > 0 &&
					!segment.StartAt.Equal(trip.Schedule[segmentIndex-1].EndAt) {
					state.add("V504", domain.SeverityError, "trip", ref(trip.ID),
						"schedule segments are contiguous",
						"gap or overlap between segments",
						atSegment(dutyIndex, tripIndex, segmentIndex))
				}
				if segmentIndex == len(trip.Schedule)-1 &&
					!segment.EndAt.Equal(trip.EndAt) {
					state.add("V504", domain.SeverityError, "trip", ref(trip.ID),
						"schedule ends with trip", segment.EndAt.Format(time.RFC3339Nano),
						atSegment(dutyIndex, tripIndex, segmentIndex))
				}
				if !slices.Contains(duty.DriverIDs, segment.DriverID) {
					state.add("V505", domain.SeverityError, "driver", ref(segment.DriverID),
						"segment driver belongs to duty", "driver is not assigned to duty",
						atSegment(dutyIndex, tripIndex, segmentIndex))
					continue
				}
				driver, exists := state.drivers[segment.DriverID]
				if !exists {
					continue
				}
				if !driver.Shift.ContainsRange(domain.TimeRange{
					Start: segment.StartAt,
					End:   segment.EndAt,
				}) {
					state.add("V505", domain.SeverityError, "driver", ref(segment.DriverID),
						"segment is within driver shift", "segment is outside shift",
						atSegment(dutyIndex, tripIndex, segmentIndex))
				}
				segmentsByDriver[segment.DriverID] =
					append(segmentsByDriver[segment.DriverID], segment)
			}
			state.validateDriveSegments(dutyIndex, tripIndex, trip)
			state.validateRehandleSegments(dutyIndex, tripIndex, trip)
		}
	}
	for driverID, segments := range segmentsByDriver {
		state.validateDriverRegulation(driverID, segments)
	}
}

func (state *validationState) validateRehandleSegments(
	dutyIndex int,
	tripIndex int,
	trip domain.Trip,
) {
	type expectedRehandle struct {
		stopIndex int
		location  domain.LocationID
		seconds   int64
		endAt     time.Time
	}
	expected := make([]expectedRehandle, 0)
	for _, stage := range trip.LoadStages {
		var seconds int64
		for _, operation := range stage.Rehandles {
			seconds += operation.DurationSeconds
		}
		if seconds == 0 || int(stage.AfterStopIndex) >= len(trip.Stops) {
			continue
		}
		stopIndex := int(stage.AfterStopIndex)
		expected = append(expected, expectedRehandle{
			stopIndex: stopIndex,
			location:  trip.Stops[stopIndex].LocationID,
			seconds:   seconds,
			endAt:     trip.Stops[stopIndex].DepartureAt,
		})
	}
	actual := make([]scheduledDrive, 0, len(expected))
	for segmentIndex, segment := range trip.Schedule {
		if segment.Kind == domain.SegmentRehandle {
			actual = append(actual, scheduledDrive{
				segmentIndex: segmentIndex,
				segment:      segment,
			})
		}
	}
	if len(actual) != len(expected) {
		state.add("V905", domain.SeverityError, "trip", ref(trip.ID),
			fmt.Sprintf("%d rehandle schedule segments", len(expected)),
			fmt.Sprintf("%d rehandle schedule segments", len(actual)),
			position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
		return
	}
	for index, wanted := range expected {
		current := actual[index]
		seconds := durationSeconds(current.segment.StartAt, current.segment.EndAt)
		if current.segment.From != wanted.location ||
			current.segment.To != wanted.location ||
			seconds != wanted.seconds ||
			!current.segment.EndAt.Equal(wanted.endAt) ||
			len(current.segment.TaskIDs) != 0 ||
			current.segment.ChargerID != "" ||
			current.segment.ChargedWh != 0 {
			state.add("V905", domain.SeverityError, "trip", ref(trip.ID),
				fmt.Sprintf(
					"stop %d at %s for %d seconds ending %s",
					wanted.stopIndex,
					wanted.location,
					wanted.seconds,
					wanted.endAt.Format(time.RFC3339Nano),
				),
				fmt.Sprintf(
					"%s -> %s for %d seconds ending %s",
					current.segment.From,
					current.segment.To,
					seconds,
					current.segment.EndAt.Format(time.RFC3339Nano),
				),
				atSegment(dutyIndex, tripIndex, current.segmentIndex))
		}
	}
}

type expectedDrive struct {
	from    domain.LocationID
	to      domain.LocationID
	seconds int64
}

type scheduledDrive struct {
	segmentIndex int
	segment      domain.DutySegment
}

func (state *validationState) validateDriveSegments(
	dutyIndex int,
	tripIndex int,
	trip domain.Trip,
) {
	expected := make([]expectedDrive, 0, len(trip.Stops))
	for stopIndex := 1; stopIndex < len(trip.Stops); stopIndex++ {
		previous := trip.Stops[stopIndex-1]
		current := trip.Stops[stopIndex]
		seconds, exists := state.matrixValue(
			state.problem.Travel.TravelSeconds,
			previous.LocationID,
			current.LocationID,
		)
		if exists && seconds > 0 {
			expected = append(expected, expectedDrive{
				from: previous.LocationID, to: current.LocationID, seconds: seconds,
			})
		}
	}
	actual := make([]scheduledDrive, 0, len(expected))
	for segmentIndex, segment := range trip.Schedule {
		if segment.Kind == domain.SegmentDrive {
			actual = append(actual, scheduledDrive{
				segmentIndex: segmentIndex,
				segment:      segment,
			})
		}
	}
	if len(actual) != len(expected) {
		state.add("V503", domain.SeverityError, "trip", ref(trip.ID),
			fmt.Sprintf("%d matrix-bound drive segments", len(expected)),
			fmt.Sprintf("%d drive segments", len(actual)),
			position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
		return
	}
	for index, wanted := range expected {
		current := actual[index]
		seconds := durationSeconds(current.segment.StartAt, current.segment.EndAt)
		if current.segment.From != wanted.from ||
			current.segment.To != wanted.to ||
			seconds != wanted.seconds {
			state.add("V503", domain.SeverityError, "trip", ref(trip.ID),
				fmt.Sprintf("%s -> %s in %d seconds", wanted.from, wanted.to, wanted.seconds),
				fmt.Sprintf("%s -> %s in %d seconds",
					current.segment.From, current.segment.To, seconds),
				atSegment(dutyIndex, tripIndex, current.segmentIndex))
		}
	}
}

func instantInAnyRange(values []domain.TimeRange, instant time.Time) bool {
	for _, value := range values {
		if value.Contains(instant) {
			return true
		}
	}
	return false
}

func (state *validationState) validateDriverRegulation(
	driverID domain.DriverID,
	segments []domain.DutySegment,
) {
	driver, exists := state.drivers[driverID]
	if !exists || len(segments) == 0 {
		return
	}
	slices.SortFunc(segments, func(left, right domain.DutySegment) int {
		return left.StartAt.Compare(right.StartAt)
	})
	regulation := driver.Regulation
	blockStart := 0
	for index := 1; index < len(segments); index++ {
		previous := segments[index-1]
		current := segments[index]
		if current.StartAt.Before(previous.EndAt) {
			state.add("V506", domain.SeverityError, "driver", ref(driverID),
				"driver segments do not overlap", "overlapping assignments", noPosition())
			continue
		}
		restSeconds := durationSeconds(previous.EndAt, current.StartAt)
		if regulation.MinRestBetweenDutySeconds > 0 &&
			restSeconds >= regulation.MinRestBetweenDutySeconds {
			state.validateDriverDutyBlock(driver, segments[blockStart:index])
			blockStart = index
			continue
		}
		if previous.To != current.From {
			state.add("V507", domain.SeverityError, "driver", ref(driverID),
				string(previous.To), string(current.From), noPosition())
		}
	}
	state.validateDriverDutyBlock(driver, segments[blockStart:])
}

func (state *validationState) validateDriverDutyBlock(
	driver domain.Driver,
	segments []domain.DutySegment,
) {
	if len(segments) == 0 {
		return
	}
	regulation := driver.Regulation
	if segments[0].From != driver.StartLocation {
		state.add("V507", domain.SeverityError, "driver", ref(driver.ID),
			string(driver.StartLocation), string(segments[0].From), noPosition())
	}
	if len(driver.EndLocations) > 0 &&
		!slices.Contains(driver.EndLocations, segments[len(segments)-1].To) {
		state.add("V507", domain.SeverityError, "driver", ref(driver.ID),
			fmt.Sprintf("one of %v", driver.EndLocations),
			string(segments[len(segments)-1].To), noPosition())
	}
	var totalDrive int64
	var continuousDrive int64
	for _, segment := range segments {
		seconds := durationSeconds(segment.StartAt, segment.EndAt)
		switch segment.Kind {
		case domain.SegmentDrive:
			totalDrive += seconds
			continuousDrive += seconds
			if regulation.MaxContinuousDriveSeconds > 0 &&
				continuousDrive > regulation.MaxContinuousDriveSeconds {
				state.add("V506", domain.SeverityError, "driver", ref(driver.ID),
					formatInt(regulation.MaxContinuousDriveSeconds),
					formatInt(continuousDrive), noPosition())
			}
		case domain.SegmentBreak:
			if seconds >= regulation.RequiredBreakSeconds {
				continuousDrive = 0
			}
		}
	}
	dutySeconds := durationSeconds(segments[0].StartAt, segments[len(segments)-1].EndAt)
	if regulation.MaxDriveSeconds > 0 && totalDrive > regulation.MaxDriveSeconds {
		state.add("V506", domain.SeverityError, "driver", ref(driver.ID),
			formatInt(regulation.MaxDriveSeconds), formatInt(totalDrive), noPosition())
	}
	if regulation.MaxDutySeconds > 0 && dutySeconds > regulation.MaxDutySeconds {
		state.add("V506", domain.SeverityError, "driver", ref(driver.ID),
			formatInt(regulation.MaxDutySeconds), formatInt(dutySeconds), noPosition())
	}
}

type chargeUse struct {
	chargerID domain.ChargerID
	start     time.Time
	end       time.Time
}

func (state *validationState) validateEnergy() {
	chargeUses := make([]chargeUse, 0)
	for dutyIndex, duty := range state.plan.Duties {
		vehicle, exists := state.vehicles[duty.VehicleID]
		if !exists {
			continue
		}
		previousEnd := vehicle.Energy.InitialSOCWh
		for tripIndex, trip := range duty.Trips {
			var scheduledChargeWh int64
			for segmentIndex, segment := range trip.Schedule {
				if segment.Kind == domain.SegmentCharge {
					scheduledChargeWh += segment.ChargedWh
					chargeUses = append(chargeUses, chargeUse{
						chargerID: segment.ChargerID,
						start:     segment.StartAt,
						end:       segment.EndAt,
					})
					state.validateChargeSegment(
						dutyIndex, tripIndex, segmentIndex, vehicle, segment,
					)
				}
			}
			if vehicle.Energy.Kind != domain.EnergyElectric {
				if scheduledChargeWh != 0 || len(trip.Energy) != 0 {
					state.add("V602", domain.SeverityError, "trip", ref(trip.ID),
						"combustion trip has no SOC or charging plan",
						"unexpected energy plan",
						position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
				}
				continue
			}
			if len(trip.Stops) > 0 && len(trip.Energy) != len(trip.Stops)-1 {
				state.add("V602", domain.SeverityError, "trip", ref(trip.ID),
					fmt.Sprintf("%d energy legs", len(trip.Stops)-1),
					fmt.Sprintf("%d energy legs", len(trip.Energy)),
					position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
			}
			var legChargeWh int64
			for legIndex, leg := range trip.Energy {
				at := atSegment(dutyIndex, tripIndex, legIndex)
				if leg.StartSOCWh != previousEnd ||
					leg.EndSOCWh != leg.StartSOCWh-leg.ConsumedWh+leg.ChargedWh {
					state.add("V602", domain.SeverityError, "trip", ref(trip.ID),
						"continuous SOC arithmetic", "SOC values are inconsistent", at)
				}
				if leg.EndSOCWh < vehicle.Energy.ReserveSOCWh ||
					leg.EndSOCWh > vehicle.Energy.BatteryCapacityWh {
					state.add("V601", domain.SeverityError, "trip", ref(trip.ID),
						fmt.Sprintf("SOC in [%d,%d]",
							vehicle.Energy.ReserveSOCWh,
							vehicle.Energy.BatteryCapacityWh,
						),
						formatInt(leg.EndSOCWh), at)
				}
				if legIndex < len(trip.Stops)-1 {
					expected, expectedExists := state.energyForArc(
						vehicle.Energy.MatrixProfileID,
						trip.Stops[legIndex].LocationID,
						trip.Stops[legIndex+1].LocationID,
						payloadAfterStop(state.cargo, trip, legIndex),
					)
					if !expectedExists {
						state.add("V603", domain.SeverityError, "trip", ref(trip.ID),
							"energy profile and arc exist", "energy matrix entry is missing", at)
					} else if leg.ConsumedWh != expected {
						state.add("V603", domain.SeverityError, "trip", ref(trip.ID),
							formatInt(expected), formatInt(leg.ConsumedWh), at)
					}
				}
				legChargeWh += leg.ChargedWh
				previousEnd = leg.EndSOCWh
			}
			if scheduledChargeWh != legChargeWh {
				state.add("V604", domain.SeverityError, "trip", ref(trip.ID),
					formatInt(scheduledChargeWh), formatInt(legChargeWh),
					position{duty: dutyIndex, trip: tripIndex, stop: -1, segment: -1})
			}
		}
	}
	state.validateChargerCapacity(chargeUses)
}

func (state *validationState) validateChargeSegment(
	dutyIndex int,
	tripIndex int,
	segmentIndex int,
	vehicle domain.Vehicle,
	segment domain.DutySegment,
) {
	charger, exists := state.chargers[segment.ChargerID]
	if !exists {
		state.add("V604", domain.SeverityError, "charger", ref(segment.ChargerID),
			"known charger", "unknown charger",
			atSegment(dutyIndex, tripIndex, segmentIndex))
		return
	}
	if segment.From != charger.LocationID || segment.To != charger.LocationID ||
		!withinAnyRange(charger.Availability, domain.TimeRange{
			Start: segment.StartAt,
			End:   segment.EndAt,
		}) ||
		!hasSharedString(vehicle.Energy.ConnectorTypes, charger.ConnectorTypes) {
		state.add("V604", domain.SeverityError, "charger", ref(segment.ChargerID),
			"compatible connector, location, and availability",
			"charge segment is incompatible",
			atSegment(dutyIndex, tripIndex, segmentIndex))
	}
	maxPowerW := charger.MaxPowerW
	curvePowerW := int64(0)
	for _, band := range vehicle.Energy.ChargingCurve {
		if band.PowerW > curvePowerW {
			curvePowerW = band.PowerW
		}
	}
	if curvePowerW < maxPowerW {
		maxPowerW = curvePowerW
	}
	maxEnergy := maxPowerW * durationSeconds(segment.StartAt, segment.EndAt) / 3_600
	if segment.ChargedWh <= 0 || segment.ChargedWh > maxEnergy {
		state.add("V604", domain.SeverityError, "charger", ref(segment.ChargerID),
			fmt.Sprintf("charged_wh in [1,%d]", maxEnergy),
			formatInt(segment.ChargedWh),
			atSegment(dutyIndex, tripIndex, segmentIndex))
	}
}

func hasSharedString(left, right []string) bool {
	for _, value := range left {
		if slices.Contains(right, value) {
			return true
		}
	}
	return false
}

func (state *validationState) validateChargerCapacity(uses []chargeUse) {
	for chargerID, charger := range state.chargers {
		times := make([]time.Time, 0)
		for _, use := range uses {
			if use.chargerID == chargerID {
				times = append(times, use.start, use.end)
			}
		}
		for _, instant := range times {
			active := 0
			for _, use := range uses {
				if use.chargerID == chargerID &&
					!instant.Before(use.start) && instant.Before(use.end) {
					active++
				}
			}
			if active > int(charger.Capacity) {
				state.add("V605", domain.SeverityError, "charger", ref(chargerID),
					fmt.Sprintf("at most %d concurrent sessions", charger.Capacity),
					fmt.Sprintf("%d concurrent sessions", active), noPosition())
				break
			}
		}
	}
}

func (state *validationState) energyForArc(
	profileID string,
	from domain.LocationID,
	to domain.LocationID,
	payloadG int64,
) (int64, bool) {
	profile, profileExists := state.energyProfiles[profileID]
	fromIndex, fromExists := state.energyIndex[from]
	toIndex, toExists := state.energyIndex[to]
	size := len(state.problem.Energy.NodeIDs)
	if !profileExists || !fromExists || !toExists ||
		len(profile.BaseWh) != size*size ||
		len(profile.LoadWhPerTonne) != size*size {
		return 0, false
	}
	index := fromIndex*size + toIndex
	return profile.BaseWh[index] +
		profile.LoadWhPerTonne[index]*payloadG/1_000_000, true
}
