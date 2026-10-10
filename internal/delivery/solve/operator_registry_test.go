package solve

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type registryTestOperator struct {
	operatorID OperatorID
}

func (operator registryTestOperator) ID() OperatorID {
	return operator.operatorID
}

func (operator registryTestOperator) Enumerate(
	context.Context,
	*engine,
	candidateState,
	func(searchMove) bool,
) error {
	return nil
}

func TestOperatorRegistryPinsProductionOrderAndDigest(t *testing.T) {
	operators := []searchOperator{
		registryTestOperator{operatorID: OperatorRelocate},
		registryTestOperator{operatorID: OperatorSwap},
		registryTestOperator{operatorID: OperatorTwoOpt},
		registryTestOperator{operatorID: OperatorCrossExchange},
		registryTestOperator{operatorID: OperatorTripSplit},
		registryTestOperator{operatorID: OperatorTripMerge},
		registryTestOperator{operatorID: OperatorDepot},
		registryTestOperator{operatorID: OperatorVehicle},
		registryTestOperator{operatorID: OperatorDriver},
		registryTestOperator{operatorID: OperatorBreak},
		registryTestOperator{operatorID: OperatorCharge},
		registryTestOperator{operatorID: OperatorPlacement},
	}
	registry, err := newOperatorRegistry(operators)
	if err != nil {
		t.Fatal(err)
	}
	want := []OperatorID{
		"relocate",
		"swap",
		"2-opt",
		"cross-exchange",
		"trip-split",
		"trip-merge",
		"depot",
		"vehicle",
		"driver",
		"break",
		"charge",
		"placement",
	}
	if got := registry.IDs(); !slices.Equal(got, want) {
		t.Fatalf("registry IDs = %v, want %v", got, want)
	}
	if got := registry.Digest(); got !=
		"adcea6553aeed6a7d11ad011273c23fa7f1ab5efaa032cced4995eca16b5d809" {
		t.Fatalf("registry digest = %q, want stable protocol digest", got)
	}
}

func TestOperatorRegistryRejectsMissingDuplicateUnknownAndReorderedOperators(t *testing.T) {
	valid := []searchOperator{
		registryTestOperator{operatorID: OperatorRelocate},
		registryTestOperator{operatorID: OperatorSwap},
		registryTestOperator{operatorID: OperatorTwoOpt},
		registryTestOperator{operatorID: OperatorCrossExchange},
		registryTestOperator{operatorID: OperatorTripSplit},
		registryTestOperator{operatorID: OperatorTripMerge},
		registryTestOperator{operatorID: OperatorDepot},
		registryTestOperator{operatorID: OperatorVehicle},
		registryTestOperator{operatorID: OperatorDriver},
		registryTestOperator{operatorID: OperatorBreak},
		registryTestOperator{operatorID: OperatorCharge},
		registryTestOperator{operatorID: OperatorPlacement},
	}
	tests := []struct {
		name    string
		mutate  func([]searchOperator) []searchOperator
		wantErr error
	}{
		{
			name: "missing",
			mutate: func(values []searchOperator) []searchOperator {
				return values[:len(values)-1]
			},
			wantErr: ErrOperatorRegistry,
		},
		{
			name: "duplicate",
			mutate: func(values []searchOperator) []searchOperator {
				values[len(values)-1] = registryTestOperator{operatorID: OperatorCharge}
				return values
			},
			wantErr: ErrOperatorRegistry,
		},
		{
			name: "unknown",
			mutate: func(values []searchOperator) []searchOperator {
				values[len(values)-1] = registryTestOperator{operatorID: "unknown"}
				return values
			},
			wantErr: ErrOperatorRegistry,
		},
		{
			name: "reordered",
			mutate: func(values []searchOperator) []searchOperator {
				values[0], values[1] = values[1], values[0]
				return values
			},
			wantErr: ErrOperatorRegistry,
		},
		{
			name: "nil",
			mutate: func(values []searchOperator) []searchOperator {
				values[0] = nil
				return values
			},
			wantErr: ErrOperatorRegistry,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := append([]searchOperator(nil), valid...)
			_, err := newOperatorRegistry(test.mutate(values))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("newOperatorRegistry error = %v, want %v", err, test.wantErr)
			}
		})
	}
}
