package solve

import (
	"context"
	"fmt"
	"slices"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type routeMove struct {
	operator  OperatorID
	dutyA     int
	tripA     int
	positionA int
	dutyB     int
	tripB     int
	positionB int
}

func (move routeMove) Operator() OperatorID {
	return move.operator
}

func (move routeMove) Key() string {
	return moveKey(
		move.operator,
		move.dutyA,
		move.tripA,
		move.positionA,
		move.dutyB,
		move.tripB,
		move.positionB,
	)
}

func (move routeMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	if err := ctx.Err(); err != nil {
		return domain.Plan{}, err
	}
	plan := normalizeCandidatePlan(state.plan)
	if err := validateTripPosition(
		plan,
		move.dutyA,
		move.tripA,
		move.positionA,
	); err != nil {
		return domain.Plan{}, err
	}
	first := tripTaskIDs(plan.Duties[move.dutyA].Trips[move.tripA])
	rebuilds := []dutyRebuild{{DutyIndex: move.dutyA, FromTrip: move.tripA}}
	switch move.operator {
	case OperatorRelocate:
		if move.positionB < 0 || move.positionB >= len(first) {
			return domain.Plan{}, fmt.Errorf("relocate destination is out of range")
		}
		taskID := first[move.positionA]
		first = append(first[:move.positionA], first[move.positionA+1:]...)
		first = slices.Insert(first, move.positionB, taskID)
		rewriteTripTaskIDs(engine, &plan.Duties[move.dutyA].Trips[move.tripA], first)
	case OperatorSwap:
		if move.positionB < 0 || move.positionB >= len(first) {
			return domain.Plan{}, fmt.Errorf("swap position is out of range")
		}
		first[move.positionA], first[move.positionB] =
			first[move.positionB], first[move.positionA]
		rewriteTripTaskIDs(engine, &plan.Duties[move.dutyA].Trips[move.tripA], first)
	case OperatorTwoOpt:
		if move.positionB <= move.positionA || move.positionB >= len(first) {
			return domain.Plan{}, fmt.Errorf("2-opt range is invalid")
		}
		slices.Reverse(first[move.positionA : move.positionB+1])
		rewriteTripTaskIDs(engine, &plan.Duties[move.dutyA].Trips[move.tripA], first)
	case OperatorCrossExchange:
		if err := validateTripPosition(
			plan,
			move.dutyB,
			move.tripB,
			move.positionB,
		); err != nil {
			return domain.Plan{}, err
		}
		second := tripTaskIDs(plan.Duties[move.dutyB].Trips[move.tripB])
		first[move.positionA], second[move.positionB] =
			second[move.positionB], first[move.positionA]
		rewriteTripTaskIDs(engine, &plan.Duties[move.dutyA].Trips[move.tripA], first)
		rewriteTripTaskIDs(engine, &plan.Duties[move.dutyB].Trips[move.tripB], second)
		rebuilds = append(rebuilds, dutyRebuild{
			DutyIndex: move.dutyB,
			FromTrip:  move.tripB,
		})
	default:
		return domain.Plan{}, fmt.Errorf("unsupported route operator %q", move.operator)
	}
	return engine.rebuildPlanDuties(ctx, plan, rebuilds)
}

func newRouteOperator(operatorID OperatorID) searchOperator {
	return operatorFunc{
		operatorID: operatorID,
		enumerate: func(
			ctx context.Context,
			_ *engine,
			state candidateState,
			yield func(searchMove) bool,
		) error {
			enumerateRouteMoves(operatorID, state.plan, func(move searchMove) bool {
				if ctx.Err() != nil {
					return false
				}
				return yield(move)
			})
			return ctx.Err()
		},
	}
}

func enumerateRouteMoves(
	operatorID OperatorID,
	plan domain.Plan,
	yield func(searchMove) bool,
) {
	switch operatorID {
	case OperatorRelocate:
		for dutyIndex, duty := range plan.Duties {
			for tripIndex, trip := range duty.Trips {
				count := len(tripTaskIDs(trip))
				for from := 0; from < count; from++ {
					for to := 0; to < count; to++ {
						if from == to || !yield(routeMove{
							operator: operatorID,
							dutyA:    dutyIndex, tripA: tripIndex, positionA: from,
							dutyB: dutyIndex, tripB: tripIndex, positionB: to,
						}) {
							if from != to {
								return
							}
						}
					}
				}
			}
		}
	case OperatorSwap, OperatorTwoOpt:
		for dutyIndex, duty := range plan.Duties {
			for tripIndex, trip := range duty.Trips {
				count := len(tripTaskIDs(trip))
				for left := 0; left < count; left++ {
					for right := left + 1; right < count; right++ {
						if !yield(routeMove{
							operator: operatorID,
							dutyA:    dutyIndex, tripA: tripIndex, positionA: left,
							dutyB: dutyIndex, tripB: tripIndex, positionB: right,
						}) {
							return
						}
					}
				}
			}
		}
	case OperatorCrossExchange:
		for dutyA, firstDuty := range plan.Duties {
			for tripA, firstTrip := range firstDuty.Trips {
				firstCount := len(tripTaskIDs(firstTrip))
				for dutyB := dutyA; dutyB < len(plan.Duties); dutyB++ {
					startTrip := 0
					if dutyA == dutyB {
						startTrip = tripA + 1
					}
					for tripB := startTrip; tripB < len(plan.Duties[dutyB].Trips); tripB++ {
						secondCount := len(tripTaskIDs(plan.Duties[dutyB].Trips[tripB]))
						for positionA := 0; positionA < firstCount; positionA++ {
							for positionB := 0; positionB < secondCount; positionB++ {
								if !yield(routeMove{
									operator: operatorID,
									dutyA:    dutyA, tripA: tripA, positionA: positionA,
									dutyB: dutyB, tripB: tripB, positionB: positionB,
								}) {
									return
								}
							}
						}
					}
				}
			}
		}
	}
}

func validateTripPosition(
	plan domain.Plan,
	dutyIndex int,
	tripIndex int,
	position int,
) error {
	if dutyIndex < 0 || dutyIndex >= len(plan.Duties) {
		return fmt.Errorf("duty index %d is out of range", dutyIndex)
	}
	if tripIndex < 0 || tripIndex >= len(plan.Duties[dutyIndex].Trips) {
		return fmt.Errorf("trip index %d is out of range", tripIndex)
	}
	count := len(tripTaskIDs(plan.Duties[dutyIndex].Trips[tripIndex]))
	if position < 0 || position >= count {
		return fmt.Errorf("task position %d is out of range", position)
	}
	return nil
}

func rewriteTripTaskIDs(
	engine *engine,
	trip *domain.Trip,
	taskIDs []domain.TaskID,
) {
	stops := make([]domain.Stop, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		stops = append(stops, domain.Stop{
			LocationID: engine.index.tasks[taskID].LocationID,
			TaskIDs:    []domain.TaskID{taskID},
		})
	}
	trip.Stops = stops
}
