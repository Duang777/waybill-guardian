package solve

import (
	"context"
	"errors"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const OperatorRegistrySchemaVersion = "delivery.operator-registry.v1"

var ErrOperatorRegistry = errors.New("invalid operator registry")

type OperatorID string

const (
	OperatorRelocate      OperatorID = "relocate"
	OperatorSwap          OperatorID = "swap"
	OperatorTwoOpt        OperatorID = "2-opt"
	OperatorCrossExchange OperatorID = "cross-exchange"
	OperatorTripSplit     OperatorID = "trip-split"
	OperatorTripMerge     OperatorID = "trip-merge"
	OperatorDepot         OperatorID = "depot"
	OperatorVehicle       OperatorID = "vehicle"
	OperatorDriver        OperatorID = "driver"
	OperatorBreak         OperatorID = "break"
	OperatorCharge        OperatorID = "charge"
	OperatorPlacement     OperatorID = "placement"
)

var requiredOperatorOrder = [...]OperatorID{
	OperatorRelocate,
	OperatorSwap,
	OperatorTwoOpt,
	OperatorCrossExchange,
	OperatorTripSplit,
	OperatorTripMerge,
	OperatorDepot,
	OperatorVehicle,
	OperatorDriver,
	OperatorBreak,
	OperatorCharge,
	OperatorPlacement,
}

type searchOperator interface {
	ID() OperatorID
	Enumerate(context.Context, *engine, candidateState, func(searchMove) bool) error
}

type operatorRegistry struct {
	operators []searchOperator
	ids       []OperatorID
	digest    domain.ArtifactDigest
}

func newProductionOperatorRegistry() (operatorRegistry, error) {
	return newOperatorRegistry([]searchOperator{
		newRouteOperator(OperatorRelocate),
		newRouteOperator(OperatorSwap),
		newRouteOperator(OperatorTwoOpt),
		newRouteOperator(OperatorCrossExchange),
		newTripResourceOperator(OperatorTripSplit),
		newTripResourceOperator(OperatorTripMerge),
		newTripResourceOperator(OperatorDepot),
		newTripResourceOperator(OperatorVehicle),
		newTripResourceOperator(OperatorDriver),
		newScheduleEnergyOperator(OperatorBreak),
		newScheduleEnergyOperator(OperatorCharge),
		newLoadOperator(),
	})
}

func newOperatorRegistry(values []searchOperator) (operatorRegistry, error) {
	if len(values) != len(requiredOperatorOrder) {
		return operatorRegistry{}, fmt.Errorf(
			"%w: got %d operators, want %d",
			ErrOperatorRegistry,
			len(values),
			len(requiredOperatorOrder),
		)
	}
	operators := append([]searchOperator(nil), values...)
	ids := make([]OperatorID, len(operators))
	seen := make(map[OperatorID]struct{}, len(operators))
	for index, operator := range operators {
		if operator == nil {
			return operatorRegistry{}, fmt.Errorf(
				"%w: operator %d is nil",
				ErrOperatorRegistry,
				index,
			)
		}
		id := operator.ID()
		if _, duplicate := seen[id]; duplicate {
			return operatorRegistry{}, fmt.Errorf(
				"%w: duplicate operator %q",
				ErrOperatorRegistry,
				id,
			)
		}
		seen[id] = struct{}{}
		if id != requiredOperatorOrder[index] {
			return operatorRegistry{}, fmt.Errorf(
				"%w: operator %d is %q, want %q",
				ErrOperatorRegistry,
				index,
				id,
				requiredOperatorOrder[index],
			)
		}
		ids[index] = id
	}
	digest, err := computeOperatorRegistryDigest(ids)
	if err != nil {
		return operatorRegistry{}, err
	}
	return operatorRegistry{operators: operators, ids: ids, digest: digest}, nil
}

func requiredOperatorIDs() []OperatorID {
	return append([]OperatorID(nil), requiredOperatorOrder[:]...)
}

func requiredOperatorRegistryDigest() (domain.ArtifactDigest, error) {
	return computeOperatorRegistryDigest(requiredOperatorIDs())
}

func computeOperatorRegistryDigest(ids []OperatorID) (domain.ArtifactDigest, error) {
	digest, err := domain.Digest(struct {
		SchemaVersion string       `json:"schema_version"`
		Operators     []OperatorID `json:"operators"`
	}{
		SchemaVersion: OperatorRegistrySchemaVersion,
		Operators:     ids,
	})
	if err != nil {
		return "", fmt.Errorf("%w: digest: %v", ErrOperatorRegistry, err)
	}
	return digest, nil
}

func (registry operatorRegistry) IDs() []OperatorID {
	return append([]OperatorID(nil), registry.ids...)
}

func (registry operatorRegistry) Operators() []searchOperator {
	return append([]searchOperator(nil), registry.operators...)
}

func (registry operatorRegistry) Digest() domain.ArtifactDigest {
	return registry.digest
}
