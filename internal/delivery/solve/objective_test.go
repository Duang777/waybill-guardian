package solve

import (
	"math"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestObjectiveComparisonUsesCompleteLexicographicOrder(t *testing.T) {
	base := domain.ObjectiveVector{
		UnassignedRequiredUnits: 10,
		HardViolationCount:      20,
		VehiclesUsed:            30,
		TotalCostCents:          40,
		TotalDistanceMeters:     50,
		TotalWaitSeconds:        60,
		NegativeMinVolumePPM:    70,
		StabilityCostCents:      80,
	}
	tests := []struct {
		name      string
		candidate domain.ObjectiveVector
	}{
		{
			name: "required coverage",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 9,
				HardViolationCount:      math.MaxUint32,
				VehiclesUsed:            math.MaxUint32,
				TotalCostCents:          math.MaxInt64,
				TotalDistanceMeters:     math.MaxInt64,
				TotalWaitSeconds:        math.MaxInt64,
				NegativeMinVolumePPM:    math.MaxInt64,
				StabilityCostCents:      math.MaxInt64,
			},
		},
		{
			name: "hard feasibility",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 10,
				HardViolationCount:      19,
				VehiclesUsed:            math.MaxUint32,
				TotalCostCents:          math.MaxInt64,
				TotalDistanceMeters:     math.MaxInt64,
				TotalWaitSeconds:        math.MaxInt64,
				NegativeMinVolumePPM:    math.MaxInt64,
				StabilityCostCents:      math.MaxInt64,
			},
		},
		{
			name: "fleet size",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 10,
				HardViolationCount:      20,
				VehiclesUsed:            29,
				TotalCostCents:          math.MaxInt64,
				TotalDistanceMeters:     math.MaxInt64,
				TotalWaitSeconds:        math.MaxInt64,
				NegativeMinVolumePPM:    math.MaxInt64,
				StabilityCostCents:      math.MaxInt64,
			},
		},
		{
			name: "cost",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 10,
				HardViolationCount:      20,
				VehiclesUsed:            30,
				TotalCostCents:          39,
				TotalDistanceMeters:     math.MaxInt64,
				TotalWaitSeconds:        math.MaxInt64,
				NegativeMinVolumePPM:    math.MaxInt64,
				StabilityCostCents:      math.MaxInt64,
			},
		},
		{
			name: "distance",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 10,
				HardViolationCount:      20,
				VehiclesUsed:            30,
				TotalCostCents:          40,
				TotalDistanceMeters:     49,
				TotalWaitSeconds:        math.MaxInt64,
				NegativeMinVolumePPM:    math.MaxInt64,
				StabilityCostCents:      math.MaxInt64,
			},
		},
		{
			name: "wait",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 10,
				HardViolationCount:      20,
				VehiclesUsed:            30,
				TotalCostCents:          40,
				TotalDistanceMeters:     50,
				TotalWaitSeconds:        59,
				NegativeMinVolumePPM:    math.MaxInt64,
				StabilityCostCents:      math.MaxInt64,
			},
		},
		{
			name: "minimum utilization",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 10,
				HardViolationCount:      20,
				VehiclesUsed:            30,
				TotalCostCents:          40,
				TotalDistanceMeters:     50,
				TotalWaitSeconds:        60,
				NegativeMinVolumePPM:    69,
				StabilityCostCents:      math.MaxInt64,
			},
		},
		{
			name: "stability",
			candidate: domain.ObjectiveVector{
				UnassignedRequiredUnits: 10,
				HardViolationCount:      20,
				VehiclesUsed:            30,
				TotalCostCents:          40,
				TotalDistanceMeters:     50,
				TotalWaitSeconds:        60,
				NegativeMinVolumePPM:    70,
				StabilityCostCents:      79,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := compareObjective(test.candidate, base); got != -1 {
				t.Fatalf("compareObjective(candidate, base) = %d, want -1", got)
			}
			if got := compareObjective(base, test.candidate); got != 1 {
				t.Fatalf("compareObjective(base, candidate) = %d, want 1", got)
			}
			if !strictObjectiveImprovement(test.candidate, base) {
				t.Fatal("strictObjectiveImprovement(candidate, base) = false, want true")
			}
		})
	}
	if got := compareObjective(base, base); got != 0 {
		t.Fatalf("compareObjective(base, base) = %d, want 0", got)
	}
	if strictObjectiveImprovement(base, base) {
		t.Fatal("equal objective was accepted as a strict improvement")
	}
}

func TestObjectiveRegretComparisonDoesNotOverflowInt64(t *testing.T) {
	best := domain.ObjectiveVector{TotalCostCents: math.MinInt64}
	largeSecond := domain.ObjectiveVector{TotalCostCents: math.MaxInt64}
	smallSecond := domain.ObjectiveVector{TotalCostCents: math.MinInt64 + 1}

	large := objectiveRegretBetween(largeSecond, best)
	small := objectiveRegretBetween(smallSecond, best)
	if got := compareObjectiveRegret(large, small); got != 1 {
		t.Fatalf("compareObjectiveRegret(large, small) = %d, want 1", got)
	}
}
