package service

import (
	"fmt"
	"slices"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type cargoStageKey struct {
	cargoID domain.CargoID
	stage   uint32
}

func compileFreezeOverrideUse(input RevisionComparisonInput) (*domain.FreezeOverrideUse, error) {
	if input.Override == nil {
		return nil, nil
	}
	visits := taskVisits(input.CandidatePlan)
	basePlacements := planCargoPlacementsByStage(input.BasePlan)
	candidatePlacements := planCargoPlacementsByStage(input.CandidatePlan)
	use := domain.FreezeOverrideUse{
		SchemaVersion: domain.FreezeOverrideUseSchemaVersion,
		GrantDigest:   input.Override.grant.Digest,
		TaskChanges:   []domain.FrozenCommitmentChange{},
		LoadChanges:   []domain.CargoPlacementChange{},
	}
	for _, scope := range input.Override.grant.Scopes {
		visit, exists := visits[scope.TaskID]
		if !exists || len(visit.driverIDs) == 0 {
			return nil, fmt.Errorf("override task %q is absent from candidate plan", scope.TaskID)
		}
		after := domain.FrozenTaskCommitment{
			TaskID:            scope.TaskID,
			VehicleID:         visit.vehicleID,
			DriverID:          visit.driverIDs[0],
			Sequence:          visit.index,
			PromisedServiceAt: visit.serviceAt.UTC(),
			ToleranceSeconds:  scope.Before.ToleranceSeconds,
		}
		requiresOverride := after.VehicleID != scope.Before.VehicleID ||
			after.DriverID != scope.Before.DriverID ||
			after.Sequence != scope.Before.Sequence ||
			comparisonAbsoluteSeconds(after.PromisedServiceAt.Sub(scope.Before.PromisedServiceAt)) > scope.Before.ToleranceSeconds
		if requiresOverride {
			if err := validateFrozenChangeWithinScope(scope, after); err != nil {
				return nil, err
			}
			use.TaskChanges = append(
				use.TaskChanges,
				domain.FrozenCommitmentChange{
					TaskID: scope.TaskID,
					Before: scope.Before,
					After:  after,
				},
			)
		}
		for _, cargoID := range scope.CargoIDs {
			stageSet := make(map[uint32]struct{})
			for key := range basePlacements {
				if key.cargoID == cargoID {
					stageSet[key.stage] = struct{}{}
				}
			}
			for key := range candidatePlacements {
				if key.cargoID == cargoID {
					stageSet[key.stage] = struct{}{}
				}
			}
			stages := make([]uint32, 0, len(stageSet))
			for stage := range stageSet {
				stages = append(stages, stage)
			}
			slices.Sort(stages)
			for _, stage := range stages {
				key := cargoStageKey{cargoID: cargoID, stage: stage}
				before, beforeExists := basePlacements[key]
				afterPlacement, afterExists := candidatePlacements[key]
				if beforeExists == afterExists && (!beforeExists || before == afterPlacement) {
					continue
				}
				if !scope.AllowCargoRepack {
					return nil, fmt.Errorf(
						"cargo %q changed outside approved repack scope",
						cargoID,
					)
				}
				if afterExists &&
					!slices.Contains(scope.AllowedCompartmentIDs, afterPlacement.CompartmentID) {
					return nil, fmt.Errorf("cargo %q moved to an unapproved compartment", cargoID)
				}
				change := domain.CargoPlacementChange{CargoID: cargoID, AfterStopIndex: stage}
				if beforeExists {
					value := before
					change.Before = &value
				}
				if afterExists {
					value := afterPlacement
					change.After = &value
				}
				use.LoadChanges = append(use.LoadChanges, change)
			}
		}
	}
	digest, err := domain.ComputeFreezeOverrideUseDigest(use)
	if err != nil {
		return nil, err
	}
	use.Digest = digest
	return &use, nil
}

func validateFrozenChangeWithinScope(
	scope domain.FreezeOverrideScope,
	after domain.FrozenTaskCommitment,
) error {
	if after.VehicleID != scope.Before.VehicleID &&
		(!scope.AllowVehicleChange || !slices.Contains(scope.AllowedVehicleIDs, after.VehicleID)) {
		return fmt.Errorf("task %q moved to an unapproved vehicle", scope.TaskID)
	}
	if after.DriverID != scope.Before.DriverID &&
		(!scope.AllowDriverChange || !slices.Contains(scope.AllowedDriverIDs, after.DriverID)) {
		return fmt.Errorf("task %q moved to an unapproved driver", scope.TaskID)
	}
	if comparisonSequenceShift(
		scope.Before.Sequence,
		int(after.Sequence),
	) > scope.MaxSequenceShift {
		return fmt.Errorf("task %q exceeded approved sequence shift", scope.TaskID)
	}
	if comparisonAbsoluteSeconds(
		after.PromisedServiceAt.Sub(scope.Before.PromisedServiceAt),
	) > scope.MaxETADriftSeconds {
		return fmt.Errorf("task %q exceeded approved ETA drift", scope.TaskID)
	}
	return nil
}

func planCargoPlacementsByStage(plan domain.Plan) map[cargoStageKey]domain.Placement {
	result := make(map[cargoStageKey]domain.Placement)
	for _, duty := range plan.Duties {
		for _, trip := range duty.Trips {
			for _, stage := range trip.LoadStages {
				for _, placement := range stage.Placements {
					result[cargoStageKey{cargoID: placement.CargoID, stage: stage.AfterStopIndex}] = placement
				}
			}
		}
	}
	return result
}
