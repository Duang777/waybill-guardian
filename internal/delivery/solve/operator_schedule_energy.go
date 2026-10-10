package solve

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type breakMoveAction uint8

const (
	breakMoveRemove breakMoveAction = iota + 1
	breakMoveInsert
)

type breakMove struct {
	duty    int
	trip    int
	segment int
	action  breakMoveAction
}

func (move breakMove) Operator() OperatorID {
	return OperatorBreak
}

func (move breakMove) Key() string {
	return moveKey(
		move.Operator(),
		move.duty,
		move.trip,
		int(move.action),
		move.segment,
	)
}

func (move breakMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return domain.Plan{}, err
	}
	plan := normalizeCandidatePlan(state.plan)
	if move.duty < 0 || move.duty >= len(plan.Duties) ||
		move.trip < 0 || move.trip >= len(plan.Duties[move.duty].Trips) {
		return domain.Plan{}, fmt.Errorf("break move target is out of range")
	}
	trip := &plan.Duties[move.duty].Trips[move.trip]
	if move.segment < 0 || move.segment >= len(trip.Schedule) {
		return domain.Plan{}, fmt.Errorf("break move segment is out of range")
	}
	switch move.action {
	case breakMoveRemove:
		segment := trip.Schedule[move.segment]
		if segment.Kind != domain.SegmentBreak {
			return domain.Plan{}, fmt.Errorf("selected segment is not a break")
		}
		duration := segment.EndAt.Sub(segment.StartAt)
		trip.Schedule = append(
			trip.Schedule[:move.segment],
			trip.Schedule[move.segment+1:]...,
		)
		shiftScheduleAndStops(trip, move.segment, segment.EndAt, -duration)
	case breakMoveInsert:
		drive := trip.Schedule[move.segment]
		if drive.Kind != domain.SegmentDrive {
			return domain.Plan{}, fmt.Errorf("break insertion target is not a drive")
		}
		driver := engine.index.drivers[drive.DriverID]
		seconds := driver.Regulation.RequiredBreakSeconds
		if seconds <= 0 {
			return domain.Plan{}, fmt.Errorf("driver %q has no break duration", driver.ID)
		}
		duration := time.Duration(seconds) * time.Second
		segment := domain.DutySegment{
			Kind:     domain.SegmentBreak,
			DriverID: drive.DriverID,
			From:     drive.From,
			To:       drive.From,
			StartAt:  drive.StartAt,
			EndAt:    drive.StartAt.Add(duration),
			TaskIDs:  []domain.TaskID{},
		}
		shiftScheduleAndStops(trip, move.segment, drive.StartAt, duration)
		trip.Schedule = slices.Insert(trip.Schedule, move.segment, segment)
	default:
		return domain.Plan{}, fmt.Errorf("unknown break move action")
	}
	plan.Metrics = domain.PlanMetrics{}
	plan.Objective = domain.ObjectiveVector{}
	plan.PlanDigest = ""
	return plan, nil
}

type chargeMove struct {
	duty      int
	trip      int
	segment   int
	chargerID domain.ChargerID
}

func (move chargeMove) Operator() OperatorID {
	return OperatorCharge
}

func (move chargeMove) Key() string {
	return fmt.Sprintf(
		"%s/%06d/%06d/%06d/%s",
		move.Operator(),
		move.duty,
		move.trip,
		move.segment,
		move.chargerID,
	)
}

func (move chargeMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return domain.Plan{}, err
	}
	plan := normalizeCandidatePlan(state.plan)
	if move.duty < 0 || move.duty >= len(plan.Duties) ||
		move.trip < 0 || move.trip >= len(plan.Duties[move.duty].Trips) {
		return domain.Plan{}, fmt.Errorf("charge move target is out of range")
	}
	trip := &plan.Duties[move.duty].Trips[move.trip]
	if move.segment < 0 || move.segment >= len(trip.Schedule) {
		return domain.Plan{}, fmt.Errorf("charge move segment is out of range")
	}
	segment := &trip.Schedule[move.segment]
	if segment.Kind != domain.SegmentCharge {
		return domain.Plan{}, fmt.Errorf("selected segment is not a charge")
	}
	charger, exists := engine.index.chargers[move.chargerID]
	if !exists || charger.LocationID != segment.From {
		return domain.Plan{}, fmt.Errorf("charger %q is unavailable at charge location", move.chargerID)
	}
	vehicle := engine.index.vehicles[plan.Duties[move.duty].VehicleID]
	if !hasSharedValue(vehicle.Energy.ConnectorTypes, charger.ConnectorTypes) {
		return domain.Plan{}, fmt.Errorf("charger %q has no compatible connector", charger.ID)
	}
	duration, ok := chargingDuration(vehicle, charger, segment.ChargedWh)
	if !ok {
		return domain.Plan{}, fmt.Errorf("charger %q cannot deliver the charge", charger.ID)
	}
	oldEnd := segment.EndAt
	delta := duration - segment.EndAt.Sub(segment.StartAt)
	segment.EndAt = segment.StartAt.Add(duration)
	segment.ChargerID = charger.ID
	shiftScheduleAndStops(trip, move.segment+1, oldEnd, delta)
	for legIndex := range trip.Energy {
		if trip.Energy[legIndex].ChargerID != "" &&
			trip.Energy[legIndex].ChargedWh == segment.ChargedWh {
			trip.Energy[legIndex].ChargerID = charger.ID
		}
	}
	plan.Metrics = domain.PlanMetrics{}
	plan.Objective = domain.ObjectiveVector{}
	plan.PlanDigest = ""
	return plan, nil
}

func newScheduleEnergyOperator(operatorID OperatorID) searchOperator {
	return operatorFunc{
		operatorID: operatorID,
		enumerate: func(
			ctx context.Context,
			engine *engine,
			state candidateState,
			yield func(searchMove) bool,
		) error {
			enumerateScheduleEnergyMoves(engine, operatorID, state.plan, func(move searchMove) bool {
				if ctx.Err() != nil {
					return false
				}
				return yield(move)
			})
			return ctx.Err()
		},
	}
}

func enumerateScheduleEnergyMoves(
	engine *engine,
	operatorID OperatorID,
	plan domain.Plan,
	yield func(searchMove) bool,
) {
	switch operatorID {
	case OperatorBreak:
		for dutyIndex, duty := range plan.Duties {
			for tripIndex, trip := range duty.Trips {
				for segmentIndex, segment := range trip.Schedule {
					switch segment.Kind {
					case domain.SegmentBreak:
						if !yield(breakMove{
							duty: dutyIndex, trip: tripIndex,
							segment: segmentIndex, action: breakMoveRemove,
						}) {
							return
						}
					case domain.SegmentDrive:
						if segmentIndex > 0 &&
							trip.Schedule[segmentIndex-1].Kind == domain.SegmentBreak {
							continue
						}
						if !yield(breakMove{
							duty: dutyIndex, trip: tripIndex,
							segment: segmentIndex, action: breakMoveInsert,
						}) {
							return
						}
					}
				}
			}
		}
	case OperatorCharge:
		chargers := append([]domain.ChargingStation{}, engine.problem.Chargers...)
		slices.SortFunc(chargers, func(left, right domain.ChargingStation) int {
			return strings.Compare(string(left.ID), string(right.ID))
		})
		for dutyIndex, duty := range plan.Duties {
			for tripIndex, trip := range duty.Trips {
				for segmentIndex, segment := range trip.Schedule {
					if segment.Kind != domain.SegmentCharge {
						continue
					}
					for _, charger := range chargers {
						if charger.ID == segment.ChargerID ||
							charger.LocationID != segment.From {
							continue
						}
						if !yield(chargeMove{
							duty: dutyIndex, trip: tripIndex,
							segment: segmentIndex, chargerID: charger.ID,
						}) {
							return
						}
					}
				}
			}
		}
	}
}

func shiftScheduleAndStops(
	trip *domain.Trip,
	fromSegment int,
	stopThreshold time.Time,
	delta time.Duration,
) {
	if delta == 0 {
		return
	}
	for segmentIndex := fromSegment; segmentIndex < len(trip.Schedule); segmentIndex++ {
		trip.Schedule[segmentIndex].StartAt =
			trip.Schedule[segmentIndex].StartAt.Add(delta)
		trip.Schedule[segmentIndex].EndAt =
			trip.Schedule[segmentIndex].EndAt.Add(delta)
	}
	for stopIndex := range trip.Stops {
		stop := &trip.Stops[stopIndex]
		if !stop.ArrivalAt.Before(stopThreshold) {
			stop.ArrivalAt = stop.ArrivalAt.Add(delta)
		}
		if !stop.ServiceAt.Before(stopThreshold) {
			stop.ServiceAt = stop.ServiceAt.Add(delta)
		}
		if !stop.DepartureAt.Before(stopThreshold) {
			stop.DepartureAt = stop.DepartureAt.Add(delta)
		}
	}
	trip.EndAt = trip.EndAt.Add(delta)
	if len(trip.Schedule) > 0 {
		trip.StartAt = trip.Schedule[0].StartAt
	}
}
