package solve

import (
	"cmp"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func compareObjective(left, right domain.ObjectiveVector) int {
	if result := cmp.Compare(
		left.UnassignedRequiredUnits,
		right.UnassignedRequiredUnits,
	); result != 0 {
		return result
	}
	if result := cmp.Compare(left.HardViolationCount, right.HardViolationCount); result != 0 {
		return result
	}
	if result := cmp.Compare(left.VehiclesUsed, right.VehiclesUsed); result != 0 {
		return result
	}
	if result := cmp.Compare(left.TotalCostCents, right.TotalCostCents); result != 0 {
		return result
	}
	if result := cmp.Compare(
		left.TotalDistanceMeters,
		right.TotalDistanceMeters,
	); result != 0 {
		return result
	}
	if result := cmp.Compare(left.TotalWaitSeconds, right.TotalWaitSeconds); result != 0 {
		return result
	}
	if result := cmp.Compare(
		left.NegativeMinVolumePPM,
		right.NegativeMinVolumePPM,
	); result != 0 {
		return result
	}
	return cmp.Compare(left.StabilityCostCents, right.StabilityCostCents)
}

func strictObjectiveImprovement(
	candidate domain.ObjectiveVector,
	incumbent domain.ObjectiveVector,
) bool {
	return compareObjective(candidate, incumbent) < 0
}

type signedDifference struct {
	sign      int8
	magnitude uint64
}

type objectiveRegret struct {
	values [8]signedDifference
}

func objectiveRegretBetween(
	second domain.ObjectiveVector,
	best domain.ObjectiveVector,
) objectiveRegret {
	return objectiveRegret{values: [8]signedDifference{
		differenceUint32(second.UnassignedRequiredUnits, best.UnassignedRequiredUnits),
		differenceUint32(second.HardViolationCount, best.HardViolationCount),
		differenceUint32(second.VehiclesUsed, best.VehiclesUsed),
		differenceInt64(second.TotalCostCents, best.TotalCostCents),
		differenceInt64(second.TotalDistanceMeters, best.TotalDistanceMeters),
		differenceInt64(second.TotalWaitSeconds, best.TotalWaitSeconds),
		differenceInt64(second.NegativeMinVolumePPM, best.NegativeMinVolumePPM),
		differenceInt64(second.StabilityCostCents, best.StabilityCostCents),
	}}
}

func compareObjectiveRegret(left, right objectiveRegret) int {
	for index := range left.values {
		if result := compareSignedDifference(left.values[index], right.values[index]); result != 0 {
			return result
		}
	}
	return 0
}

func differenceUint32(left, right uint32) signedDifference {
	switch {
	case left > right:
		return signedDifference{sign: 1, magnitude: uint64(left - right)}
	case left < right:
		return signedDifference{sign: -1, magnitude: uint64(right - left)}
	default:
		return signedDifference{}
	}
}

func differenceInt64(left, right int64) signedDifference {
	switch {
	case left > right:
		return signedDifference{sign: 1, magnitude: uint64(left) - uint64(right)}
	case left < right:
		return signedDifference{sign: -1, magnitude: uint64(right) - uint64(left)}
	default:
		return signedDifference{}
	}
}

func compareSignedDifference(left, right signedDifference) int {
	if result := cmp.Compare(left.sign, right.sign); result != 0 {
		return result
	}
	if left.sign < 0 {
		return cmp.Compare(right.magnitude, left.magnitude)
	}
	return cmp.Compare(left.magnitude, right.magnitude)
}
