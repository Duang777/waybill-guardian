package solve

import (
	"fmt"
	"slices"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func (engine *engine) buildEnergyPlan(
	vehicle domain.Vehicle,
	trip *domain.Trip,
	startSOCWh int64,
) ([]domain.EnergyLeg, error) {
	return engine.buildEnergyPlanControlled(vehicle, trip, startSOCWh, nil)
}

func (engine *engine) buildEnergyPlanControlled(
	vehicle domain.Vehicle,
	trip *domain.Trip,
	startSOCWh int64,
	directives []chargerDirective,
) ([]domain.EnergyLeg, error) {
	if vehicle.Energy.Kind != domain.EnergyElectric {
		if len(directives) > 0 {
			return nil, fmt.Errorf("combustion vehicle cannot use charger directives")
		}
		return []domain.EnergyLeg{}, nil
	}
	if len(trip.Stops) < 2 {
		if len(directives) > 0 {
			return nil, fmt.Errorf("trip without energy legs cannot use charger directives")
		}
		return []domain.EnergyLeg{}, nil
	}
	chargerByStop := make(map[uint32]domain.ChargerID, len(directives))
	for _, directive := range directives {
		if _, duplicate := chargerByStop[directive.FromStopIndex]; duplicate {
			return nil, fmt.Errorf(
				"stop %d has duplicate charger directives",
				directive.FromStopIndex,
			)
		}
		chargerByStop[directive.FromStopIndex] = directive.ChargerID
	}
	usedDirectives := make(map[uint32]struct{}, len(directives))
	consumption := make([]int64, len(trip.Stops)-1)
	for index := range consumption {
		value, ok := engine.energyForArc(
			vehicle.Energy.MatrixProfileID,
			trip.Stops[index].LocationID,
			trip.Stops[index+1].LocationID,
			payloadAtStage(trip.LoadStages, index, engine.index.cargo),
		)
		if !ok {
			return nil, fmt.Errorf(
				"energy arc %q -> %q is missing",
				trip.Stops[index].LocationID,
				trip.Stops[index+1].LocationID,
			)
		}
		consumption[index] = value
	}

	legs := make([]domain.EnergyLeg, 0, len(consumption))
	soc := startSOCWh
	for index, consumedWh := range consumption {
		chargedWh := int64(0)
		charger := domain.ChargingStation{}
		chargerExists := false
		if chargerID, directed := chargerByStop[uint32(index)]; directed {
			charger, chargerExists = engine.index.chargers[chargerID]
			if !chargerExists ||
				charger.LocationID != trip.Stops[index].LocationID ||
				!hasSharedValue(vehicle.Energy.ConnectorTypes, charger.ConnectorTypes) {
				return nil, fmt.Errorf(
					"charger %q is unavailable at stop %d",
					chargerID,
					index,
				)
			}
		} else {
			charger, chargerExists = engine.compatibleCharger(
				vehicle,
				trip.Stops[index].LocationID,
			)
		}
		if chargerExists {
			needed := engine.energyUntilNextCharge(
				vehicle,
				*trip,
				consumption,
				index,
			) + vehicle.Energy.ReserveSOCWh - soc
			if needed > 0 {
				if needed > vehicle.Energy.BatteryCapacityWh-soc {
					return nil, fmt.Errorf("route exceeds EV battery capacity between chargers")
				}
				duration, ok := chargingDuration(vehicle, charger, needed)
				if !ok {
					return nil, fmt.Errorf("charger %q cannot deliver required energy", charger.ID)
				}
				if err := engine.insertChargeSegment(
					trip,
					index,
					vehicle,
					charger,
					needed,
					duration,
				); err != nil {
					return nil, err
				}
				chargedWh = needed
				if _, directed := chargerByStop[uint32(index)]; directed {
					usedDirectives[uint32(index)] = struct{}{}
				}
			}
		}
		endSOC := soc - consumedWh + chargedWh
		if endSOC < vehicle.Energy.ReserveSOCWh ||
			endSOC > vehicle.Energy.BatteryCapacityWh {
			return nil, fmt.Errorf("EV reserve cannot be maintained on route")
		}
		legs = append(legs, domain.EnergyLeg{
			FromStopIndex: uint32(index),
			ToStopIndex:   uint32(index + 1),
			StartSOCWh:    soc,
			ConsumedWh:    consumedWh,
			ChargedWh:     chargedWh,
			EndSOCWh:      endSOC,
			ChargerID: func() domain.ChargerID {
				if chargedWh > 0 {
					return charger.ID
				}
				return ""
			}(),
		})
		soc = endSOC
	}
	for stopIndex, chargerID := range chargerByStop {
		if _, used := usedDirectives[stopIndex]; !used {
			return nil, fmt.Errorf(
				"charger directive %q at stop %d did not produce a charge",
				chargerID,
				stopIndex,
			)
		}
	}
	return legs, nil
}

func (engine *engine) energyUntilNextCharge(
	vehicle domain.Vehicle,
	trip domain.Trip,
	consumption []int64,
	start int,
) int64 {
	var total int64
	for index := start; index < len(consumption); index++ {
		total += consumption[index]
		if index+1 < len(trip.Stops)-1 {
			if _, exists := engine.compatibleCharger(
				vehicle,
				trip.Stops[index+1].LocationID,
			); exists {
				break
			}
		}
	}
	return total
}

func (engine *engine) compatibleCharger(
	vehicle domain.Vehicle,
	locationID domain.LocationID,
) (domain.ChargingStation, bool) {
	chargers := append([]domain.ChargingStation(nil), engine.problem.Chargers...)
	slices.SortFunc(chargers, func(left, right domain.ChargingStation) int {
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	for _, charger := range chargers {
		if charger.LocationID == locationID &&
			hasSharedValue(vehicle.Energy.ConnectorTypes, charger.ConnectorTypes) {
			return charger, true
		}
	}
	return domain.ChargingStation{}, false
}

func chargingDuration(
	vehicle domain.Vehicle,
	charger domain.ChargingStation,
	energyWh int64,
) (time.Duration, bool) {
	powerW := charger.MaxPowerW
	for _, band := range vehicle.Energy.ChargingCurve {
		if band.PowerW < powerW {
			powerW = band.PowerW
		}
	}
	if powerW <= 0 || energyWh <= 0 {
		return 0, false
	}
	seconds := (energyWh*3_600 + powerW - 1) / powerW
	if seconds <= 0 {
		seconds = 1
	}
	return time.Duration(seconds) * time.Second, true
}

func (engine *engine) insertChargeSegment(
	trip *domain.Trip,
	stopIndex int,
	vehicle domain.Vehicle,
	charger domain.ChargingStation,
	chargedWh int64,
	duration time.Duration,
) error {
	if stopIndex >= len(trip.Stops)-1 {
		return fmt.Errorf("cannot charge after final stop")
	}
	insertAt := trip.Stops[stopIndex].DepartureAt
	endAt := insertAt.Add(duration)
	if !withinRange(charger.Availability, insertAt, endAt) {
		return fmt.Errorf("charger %q is unavailable for required interval", charger.ID)
	}
	if !driverSegmentInsertionFits(trip.Schedule, insertAt) {
		return fmt.Errorf("charge insertion does not align with route schedule")
	}
	driverID := trip.Schedule[0].DriverID
	segment := domain.DutySegment{
		Kind:      domain.SegmentCharge,
		DriverID:  driverID,
		From:      charger.LocationID,
		To:        charger.LocationID,
		StartAt:   insertAt,
		EndAt:     endAt,
		TaskIDs:   []domain.TaskID{},
		ChargerID: charger.ID,
		ChargedWh: chargedWh,
	}
	insertIndex := len(trip.Schedule)
	for index, current := range trip.Schedule {
		if !current.StartAt.Before(insertAt) {
			insertIndex = index
			break
		}
	}
	for index := insertIndex; index < len(trip.Schedule); index++ {
		trip.Schedule[index].StartAt = trip.Schedule[index].StartAt.Add(duration)
		trip.Schedule[index].EndAt = trip.Schedule[index].EndAt.Add(duration)
	}
	trip.Schedule = append(trip.Schedule, domain.DutySegment{})
	copy(trip.Schedule[insertIndex+1:], trip.Schedule[insertIndex:])
	trip.Schedule[insertIndex] = segment
	for index := stopIndex + 1; index < len(trip.Stops); index++ {
		trip.Stops[index].ArrivalAt = trip.Stops[index].ArrivalAt.Add(duration)
		trip.Stops[index].ServiceAt = trip.Stops[index].ServiceAt.Add(duration)
		trip.Stops[index].DepartureAt = trip.Stops[index].DepartureAt.Add(duration)
		for _, taskID := range trip.Stops[index].TaskIDs {
			task := engine.index.tasks[taskID]
			if len(task.HardWindows) > 0 &&
				!instantInRanges(task.HardWindows, trip.Stops[index].ServiceAt) {
				return fmt.Errorf("charging makes task %q miss its hard window", taskID)
			}
		}
	}
	trip.EndAt = trip.EndAt.Add(duration)
	if !withinRange(vehicle.Availability, trip.StartAt, trip.EndAt) {
		return fmt.Errorf("charging extends trip beyond vehicle availability")
	}
	return nil
}

func driverSegmentInsertionFits(schedule []domain.DutySegment, at time.Time) bool {
	for _, segment := range schedule {
		if at.Equal(segment.StartAt) {
			return true
		}
	}
	return false
}

func payloadAtStage(
	stages []domain.LoadStage,
	stopIndex int,
	cargo map[domain.CargoID]domain.CargoItem,
) int64 {
	for _, stage := range stages {
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

func (engine *engine) energyForArc(
	profileID string,
	from domain.LocationID,
	to domain.LocationID,
	payloadG int64,
) (int64, bool) {
	profile, profileExists := engine.index.energyProfile[profileID]
	fromIndex, fromExists := engine.index.energyIndex[from]
	toIndex, toExists := engine.index.energyIndex[to]
	size := len(engine.problem.Energy.NodeIDs)
	if !profileExists || !fromExists || !toExists ||
		len(profile.BaseWh) != size*size ||
		len(profile.LoadWhPerTonne) != size*size {
		return 0, false
	}
	position := fromIndex*size + toIndex
	return profile.BaseWh[position] +
		profile.LoadWhPerTonne[position]*payloadG/1_000_000, true
}

func hasSharedValue(left, right []string) bool {
	for _, value := range left {
		if slices.Contains(right, value) {
			return true
		}
	}
	return false
}

func instantInRanges(values []domain.TimeRange, instant time.Time) bool {
	for _, value := range values {
		if value.Contains(instant) {
			return true
		}
	}
	return false
}
