package solve

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type tripSplitMove struct {
	duty int
	trip int
	cut  int
}

func (move tripSplitMove) Operator() OperatorID {
	return OperatorTripSplit
}

func (move tripSplitMove) Key() string {
	return moveKey(move.Operator(), move.duty, move.trip, move.cut)
}

func (move tripSplitMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	plan := normalizeCandidatePlan(state.plan)
	if move.duty < 0 || move.duty >= len(plan.Duties) ||
		move.trip < 0 || move.trip >= len(plan.Duties[move.duty].Trips) {
		return domain.Plan{}, fmt.Errorf("trip split target is out of range")
	}
	duty := &plan.Duties[move.duty]
	vehicle := engine.index.vehicles[duty.VehicleID]
	if len(duty.Trips) >= int(vehicle.MaxTrips) {
		return domain.Plan{}, fmt.Errorf("vehicle %q reached its trip limit", vehicle.ID)
	}
	original := duty.Trips[move.trip]
	tasks := tripTaskIDs(original)
	if move.cut <= 0 || move.cut >= len(tasks) {
		return domain.Plan{}, fmt.Errorf("trip split cut is out of range")
	}
	first := original
	second := original
	first.EndDepotID = original.EndDepotID
	second.ID = domain.TripID(fmt.Sprintf("%s-split-%03d", original.ID, move.cut))
	second.StartDepotID = first.EndDepotID
	rewriteTripTaskIDs(engine, &first, tasks[:move.cut])
	rewriteTripTaskIDs(engine, &second, tasks[move.cut:])
	duty.Trips[move.trip] = first
	duty.Trips = slices.Insert(duty.Trips, move.trip+1, second)
	return engine.rebuildPlanDuties(ctx, plan, []dutyRebuild{{
		DutyIndex: move.duty,
		FromTrip:  move.trip,
	}})
}

type tripMergeMove struct {
	duty int
	trip int
}

func (move tripMergeMove) Operator() OperatorID {
	return OperatorTripMerge
}

func (move tripMergeMove) Key() string {
	return moveKey(move.Operator(), move.duty, move.trip)
}

func (move tripMergeMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	plan := normalizeCandidatePlan(state.plan)
	if move.duty < 0 || move.duty >= len(plan.Duties) ||
		move.trip < 0 || move.trip+1 >= len(plan.Duties[move.duty].Trips) {
		return domain.Plan{}, fmt.Errorf("trip merge target is out of range")
	}
	duty := &plan.Duties[move.duty]
	first := duty.Trips[move.trip]
	second := duty.Trips[move.trip+1]
	first.EndDepotID = second.EndDepotID
	tasks := append(tripTaskIDs(first), tripTaskIDs(second)...)
	rewriteTripTaskIDs(engine, &first, tasks)
	duty.Trips[move.trip] = first
	duty.Trips = append(duty.Trips[:move.trip+1], duty.Trips[move.trip+2:]...)
	return engine.rebuildPlanDuties(ctx, plan, []dutyRebuild{{
		DutyIndex: move.duty,
		FromTrip:  move.trip,
	}})
}

type depotBoundary uint8

const (
	depotBoundaryStart depotBoundary = iota + 1
	depotBoundaryEnd
)

type depotMove struct {
	duty     int
	trip     int
	boundary depotBoundary
	depotID  domain.DepotID
}

func (move depotMove) Operator() OperatorID {
	return OperatorDepot
}

func (move depotMove) Key() string {
	return fmt.Sprintf(
		"%s/%06d/%06d/%d/%s",
		move.Operator(),
		move.duty,
		move.trip,
		move.boundary,
		move.depotID,
	)
}

func (move depotMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	plan := normalizeCandidatePlan(state.plan)
	if move.duty < 0 || move.duty >= len(plan.Duties) ||
		move.trip < 0 || move.trip >= len(plan.Duties[move.duty].Trips) {
		return domain.Plan{}, fmt.Errorf("depot move target is out of range")
	}
	depot, exists := engine.index.depots[move.depotID]
	if !exists {
		return domain.Plan{}, fmt.Errorf("unknown depot %q", move.depotID)
	}
	duty := &plan.Duties[move.duty]
	fromTrip := move.trip
	switch move.boundary {
	case depotBoundaryStart:
		if !depot.AllowTripStart {
			return domain.Plan{}, fmt.Errorf("depot %q cannot start a trip", depot.ID)
		}
		duty.Trips[move.trip].StartDepotID = depot.ID
		if move.trip > 0 {
			duty.Trips[move.trip-1].EndDepotID = depot.ID
			fromTrip--
		}
	case depotBoundaryEnd:
		if !depot.AllowTripEnd {
			return domain.Plan{}, fmt.Errorf("depot %q cannot end a trip", depot.ID)
		}
		duty.Trips[move.trip].EndDepotID = depot.ID
		if move.trip+1 < len(duty.Trips) {
			duty.Trips[move.trip+1].StartDepotID = depot.ID
		}
	default:
		return domain.Plan{}, fmt.Errorf("unknown depot boundary")
	}
	return engine.rebuildPlanDuties(ctx, plan, []dutyRebuild{{
		DutyIndex: move.duty,
		FromTrip:  fromTrip,
	}})
}

type vehicleMove struct {
	duty      int
	vehicleID domain.VehicleID
}

func (move vehicleMove) Operator() OperatorID {
	return OperatorVehicle
}

func (move vehicleMove) Key() string {
	return fmt.Sprintf("%s/%06d/%s", move.Operator(), move.duty, move.vehicleID)
}

func (move vehicleMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	plan := normalizeCandidatePlan(state.plan)
	if move.duty < 0 || move.duty >= len(plan.Duties) {
		return domain.Plan{}, fmt.Errorf("vehicle move duty is out of range")
	}
	vehicle, exists := engine.index.vehicles[move.vehicleID]
	if !exists {
		return domain.Plan{}, fmt.Errorf("unknown vehicle %q", move.vehicleID)
	}
	for dutyIndex, duty := range plan.Duties {
		if dutyIndex != move.duty && duty.VehicleID == move.vehicleID {
			return domain.Plan{}, fmt.Errorf("vehicle %q already has a duty", move.vehicleID)
		}
	}
	if len(plan.Duties[move.duty].Trips) > int(vehicle.MaxTrips) {
		return domain.Plan{}, fmt.Errorf("vehicle %q has too few trip slots", vehicle.ID)
	}
	plan.Duties[move.duty].VehicleID = vehicle.ID
	plan = normalizeCandidatePlan(plan)
	target := -1
	for dutyIndex, duty := range plan.Duties {
		if duty.VehicleID == vehicle.ID {
			target = dutyIndex
			break
		}
	}
	if target < 0 {
		return domain.Plan{}, fmt.Errorf("moved vehicle duty disappeared")
	}
	return engine.rebuildPlanDuties(ctx, plan, []dutyRebuild{{
		DutyIndex: target,
		FromTrip:  0,
	}})
}

type driverMove struct {
	duty     int
	driverID domain.DriverID
}

func (move driverMove) Operator() OperatorID {
	return OperatorDriver
}

func (move driverMove) Key() string {
	return fmt.Sprintf("%s/%06d/%s", move.Operator(), move.duty, move.driverID)
}

func (move driverMove) Apply(
	ctx context.Context,
	engine *engine,
	state candidateState,
) (domain.Plan, error) {
	plan := normalizeCandidatePlan(state.plan)
	if move.duty < 0 || move.duty >= len(plan.Duties) {
		return domain.Plan{}, fmt.Errorf("driver move duty is out of range")
	}
	if _, exists := engine.index.drivers[move.driverID]; !exists {
		return domain.Plan{}, fmt.Errorf("unknown driver %q", move.driverID)
	}
	for dutyIndex, duty := range plan.Duties {
		if dutyIndex != move.duty && slices.Contains(duty.DriverIDs, move.driverID) {
			return domain.Plan{}, fmt.Errorf("driver %q already has a duty", move.driverID)
		}
	}
	duty := &plan.Duties[move.duty]
	duty.DriverIDs = []domain.DriverID{move.driverID}
	for tripIndex := range duty.Trips {
		for segmentIndex := range duty.Trips[tripIndex].Schedule {
			duty.Trips[tripIndex].Schedule[segmentIndex].DriverID = move.driverID
		}
	}
	return engine.rebuildPlanDuties(ctx, plan, []dutyRebuild{{
		DutyIndex: move.duty,
		FromTrip:  0,
	}})
}

func newTripResourceOperator(operatorID OperatorID) searchOperator {
	return operatorFunc{
		operatorID: operatorID,
		enumerate: func(
			ctx context.Context,
			engine *engine,
			state candidateState,
			yield func(searchMove) bool,
		) error {
			enumerateTripResourceMoves(engine, operatorID, state.plan, func(move searchMove) bool {
				if ctx.Err() != nil {
					return false
				}
				return yield(move)
			})
			return ctx.Err()
		},
	}
}

func enumerateTripResourceMoves(
	engine *engine,
	operatorID OperatorID,
	plan domain.Plan,
	yield func(searchMove) bool,
) {
	switch operatorID {
	case OperatorTripSplit:
		for dutyIndex, duty := range plan.Duties {
			for tripIndex, trip := range duty.Trips {
				count := len(tripTaskIDs(trip))
				for cut := 1; cut < count; cut++ {
					if !yield(tripSplitMove{duty: dutyIndex, trip: tripIndex, cut: cut}) {
						return
					}
				}
			}
		}
	case OperatorTripMerge:
		for dutyIndex, duty := range plan.Duties {
			for tripIndex := 0; tripIndex+1 < len(duty.Trips); tripIndex++ {
				if !yield(tripMergeMove{duty: dutyIndex, trip: tripIndex}) {
					return
				}
			}
		}
	case OperatorDepot:
		depotIDs := make([]domain.DepotID, 0, len(engine.problem.Depots))
		for _, depot := range engine.problem.Depots {
			depotIDs = append(depotIDs, depot.ID)
		}
		slices.SortFunc(depotIDs, func(left, right domain.DepotID) int {
			return strings.Compare(string(left), string(right))
		})
		for dutyIndex, duty := range plan.Duties {
			for tripIndex, trip := range duty.Trips {
				for _, depotID := range depotIDs {
					if depotID != trip.StartDepotID && !yield(depotMove{
						duty: dutyIndex, trip: tripIndex,
						boundary: depotBoundaryStart, depotID: depotID,
					}) {
						return
					}
					if depotID != trip.EndDepotID && !yield(depotMove{
						duty: dutyIndex, trip: tripIndex,
						boundary: depotBoundaryEnd, depotID: depotID,
					}) {
						return
					}
				}
			}
		}
	case OperatorVehicle:
		vehicleIDs := make([]domain.VehicleID, 0, len(engine.problem.Vehicles))
		for _, vehicle := range engine.problem.Vehicles {
			vehicleIDs = append(vehicleIDs, vehicle.ID)
		}
		slices.SortFunc(vehicleIDs, func(left, right domain.VehicleID) int {
			return strings.Compare(string(left), string(right))
		})
		for dutyIndex, duty := range plan.Duties {
			for _, vehicleID := range vehicleIDs {
				if vehicleID == duty.VehicleID {
					continue
				}
				if !yield(vehicleMove{duty: dutyIndex, vehicleID: vehicleID}) {
					return
				}
			}
		}
	case OperatorDriver:
		driverIDs := make([]domain.DriverID, 0, len(engine.problem.Drivers))
		for _, driver := range engine.problem.Drivers {
			driverIDs = append(driverIDs, driver.ID)
		}
		slices.SortFunc(driverIDs, func(left, right domain.DriverID) int {
			return strings.Compare(string(left), string(right))
		})
		for dutyIndex, duty := range plan.Duties {
			for _, driverID := range driverIDs {
				if len(duty.DriverIDs) == 1 && duty.DriverIDs[0] == driverID {
					continue
				}
				if !yield(driverMove{duty: dutyIndex, driverID: driverID}) {
					return
				}
			}
		}
	}
}
