package solve

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type placementMove struct {
	duty        int
	trip        int
	stage       int
	cargoID     domain.CargoID
	orientation domain.Orientation
}

func (move placementMove) Operator() OperatorID {
	return OperatorPlacement
}

func (move placementMove) Key() string {
	return fmt.Sprintf(
		"%s/%06d/%06d/%06d/%s/%s",
		move.Operator(),
		move.duty,
		move.trip,
		move.stage,
		move.cargoID,
		move.orientation,
	)
}

func (move placementMove) Apply(
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
		return domain.Plan{}, fmt.Errorf("placement move target is out of range")
	}
	cargo, exists := engine.index.cargo[move.cargoID]
	if !exists || !slices.Contains(cargo.AllowedOrientations, move.orientation) {
		return domain.Plan{}, fmt.Errorf(
			"cargo %q does not allow orientation %q",
			move.cargoID,
			move.orientation,
		)
	}
	size, ok := cargo.SizeMM.Oriented(move.orientation)
	if !ok {
		return domain.Plan{}, fmt.Errorf("orientation %q is invalid", move.orientation)
	}
	trip := &plan.Duties[move.duty].Trips[move.trip]
	if move.stage <= 0 || move.stage >= len(trip.LoadStages) {
		return domain.Plan{}, fmt.Errorf("placement move stage is out of range")
	}
	previouslyLoaded := false
	for _, placement := range trip.LoadStages[move.stage-1].Placements {
		if placement.CargoID == move.cargoID {
			previouslyLoaded = true
			break
		}
	}
	if !previouslyLoaded {
		return domain.Plan{}, fmt.Errorf(
			"cargo %q is not loaded before stage %d",
			move.cargoID,
			move.stage,
		)
	}
	removeRehandleSchedule(trip)
	changed := false
	for stageIndex := move.stage; stageIndex < len(trip.LoadStages); stageIndex++ {
		if err := ctx.Err(); err != nil {
			return domain.Plan{}, err
		}
		stage := &trip.LoadStages[stageIndex]
		stageChanged := false
		for placementIndex := range stage.Placements {
			placement := &stage.Placements[placementIndex]
			if placement.CargoID != move.cargoID {
				continue
			}
			placement.Orientation = move.orientation
			placement.SizeMM = size
			changed = true
			stageChanged = true
		}
		if !stageChanged {
			continue
		}
		center, axles, valid := stageLoads(
			engine.index.vehicles[plan.Duties[move.duty].VehicleID],
			engine.index.cargo,
			stage.Placements,
		)
		if !valid {
			return domain.Plan{}, fmt.Errorf(
				"orientation %q violates stage %d load limits",
				move.orientation,
				stageIndex,
			)
		}
		stage.CenterOfMassMM = center
		stage.AxleLoadsG = axles
	}
	if !changed {
		return domain.Plan{}, fmt.Errorf("cargo %q is not placed in the trip", move.cargoID)
	}
	vehicle := engine.index.vehicles[plan.Duties[move.duty].VehicleID]
	for stageIndex := range trip.LoadStages {
		if err := ctx.Err(); err != nil {
			return domain.Plan{}, err
		}
		stage := &trip.LoadStages[stageIndex]
		stage.Rehandles = []domain.RehandleOperation{}
		if stageIndex == 0 {
			continue
		}
		operations, err := engine.rehandlesForTransition(
			ctx,
			vehicle,
			trip.LoadStages[stageIndex-1],
			*stage,
		)
		if err != nil {
			return domain.Plan{}, err
		}
		stage.Rehandles = operations
	}
	driverID := domain.DriverID("")
	for _, segment := range trip.Schedule {
		if segment.DriverID != "" {
			driverID = segment.DriverID
			break
		}
	}
	if driverID == "" {
		return domain.Plan{}, fmt.Errorf("trip has no scheduled driver")
	}
	if err := engine.applyRehandleSchedule(ctx, trip, driverID); err != nil {
		return domain.Plan{}, err
	}
	plan.Metrics = domain.PlanMetrics{}
	plan.Objective = domain.ObjectiveVector{}
	plan.PlanDigest = ""
	return plan, nil
}

func newLoadOperator() searchOperator {
	return operatorFunc{
		operatorID: OperatorPlacement,
		enumerate: func(
			ctx context.Context,
			engine *engine,
			state candidateState,
			yield func(searchMove) bool,
		) error {
			for dutyIndex, duty := range state.plan.Duties {
				for tripIndex, trip := range duty.Trips {
					for stageIndex := 1; stageIndex < len(trip.LoadStages); stageIndex++ {
						if err := ctx.Err(); err != nil {
							return err
						}
						previous := make(map[domain.CargoID]struct{})
						for _, placement := range trip.LoadStages[stageIndex-1].Placements {
							previous[placement.CargoID] = struct{}{}
						}
						current := make(map[domain.CargoID]domain.Orientation)
						for _, placement := range trip.LoadStages[stageIndex].Placements {
							if _, survives := previous[placement.CargoID]; survives {
								current[placement.CargoID] = placement.Orientation
							}
						}
						cargoIDs := make([]domain.CargoID, 0, len(current))
						for cargoID := range current {
							cargoIDs = append(cargoIDs, cargoID)
						}
						slices.SortFunc(cargoIDs, func(left, right domain.CargoID) int {
							return strings.Compare(string(left), string(right))
						})
						for _, cargoID := range cargoIDs {
							orientations := append(
								[]domain.Orientation{},
								engine.index.cargo[cargoID].AllowedOrientations...,
							)
							slices.SortFunc(
								orientations,
								func(left, right domain.Orientation) int {
									return strings.Compare(string(left), string(right))
								},
							)
							orientations = slices.Compact(orientations)
							for _, orientation := range orientations {
								if orientation == current[cargoID] {
									continue
								}
								if !yield(placementMove{
									duty: dutyIndex, trip: tripIndex, stage: stageIndex,
									cargoID: cargoID, orientation: orientation,
								}) {
									return nil
								}
							}
						}
					}
				}
			}
			return ctx.Err()
		},
	}
}
