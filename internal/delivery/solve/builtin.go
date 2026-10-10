package solve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/validate"
)

const builtinStrategy = "deterministic-regret"

type Builtin struct {
	identity  domain.SolverIdentity
	validator validate.Validator
	routeSeed *RouteSeed
}

type BuiltinOption func(*Builtin) error

func WithRouteSeed(seed RouteSeed) BuiltinOption {
	return func(solver *Builtin) error {
		normalized, err := normalizeRouteSeed(seed)
		if err != nil {
			return err
		}
		solver.routeSeed = &normalized
		return nil
	}
}

func NewBuiltin(
	identity domain.SolverIdentity,
	validator validate.Validator,
	options ...BuiltinOption,
) (*Builtin, error) {
	if identity.Name == "" || identity.Version == "" {
		return nil, fmt.Errorf("solver identity name and version are required")
	}
	solver := &Builtin{identity: identity, validator: validator}
	for _, option := range options {
		if err := option(solver); err != nil {
			return nil, err
		}
	}
	return solver, nil
}

func (solver *Builtin) Identity() domain.SolverIdentity {
	return solver.identity
}

func (solver *Builtin) Capabilities(context.Context) (Capabilities, error) {
	return Capabilities{
		SchemaVersion:         "delivery.solver-capabilities.v1",
		MultiDepot:            true,
		MultiTrip:             true,
		PickupDelivery:        true,
		SplitByUnit:           true,
		HeterogeneousFleet:    true,
		DriverRegulations:     true,
		ElectricVehicles:      true,
		ChargingCapacity:      true,
		ThreeDimensionalLoad:  true,
		AxleAndCenterOfMass:   true,
		StopAccessibility:     true,
		DynamicCommitments:    true,
		DeterministicReplay:   true,
		RemoteJobContinuation: false,
	}, nil
}

func (solver *Builtin) Solve(
	ctx context.Context,
	problem domain.ProblemSnapshot,
	config SolveConfig,
	sink ProgressSink,
) (SolveResult, error) {
	config, configDigest, err := BuildSolveConfig(config)
	if err != nil {
		return SolveResult{}, err
	}
	engine, err := newEngine(problem, config, configDigest, solver.identity, solver.validator, sink)
	if err != nil {
		return SolveResult{}, err
	}
	if config.Strategy != builtinStrategy {
		result, resultErr := engine.terminalResult(
			SolveUnsupported,
			TerminationUnsupported,
		)
		return result, errors.Join(resultErr, fmt.Errorf(
			"%w: builtin strategy must be %q",
			ErrCapability,
			builtinStrategy,
		))
	}
	if solver.routeSeed == nil && config.RouteSeedDigest != "" {
		return SolveResult{}, fmt.Errorf(
			"%w: route seed digest was configured without a seed",
			ErrInvalidRouteSeed,
		)
	}
	if solver.routeSeed != nil && solver.routeSeed.RouteSeedDigest != config.RouteSeedDigest {
		return SolveResult{}, fmt.Errorf(
			"%w: route seed digest does not match configured digest",
			ErrInvalidRouteSeed,
		)
	}

	if solver.routeSeed != nil {
		engine.routeSeed = solver.routeSeed
	}
	result, err := engine.solve(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return result, fmt.Errorf("%w: %v", ErrAborted, err)
	}
	return result, err
}

func normalizeRouteSeed(value RouteSeed) (RouteSeed, error) {
	if value.SchemaVersion != RouteSeedSchemaVersion {
		return RouteSeed{}, fmt.Errorf(
			"%w: schema_version must be %q",
			ErrInvalidRouteSeed,
			RouteSeedSchemaVersion,
		)
	}
	if value.Provider.Name == "" || value.Provider.Version == "" {
		return RouteSeed{}, fmt.Errorf("%w: provider identity is required", ErrInvalidRouteSeed)
	}
	value.Routes = append([]SeedRoute(nil), value.Routes...)
	for index := range value.Routes {
		route := &value.Routes[index]
		route.TaskIDs = append([]domain.TaskID(nil), route.TaskIDs...)
		if route.VehicleID == "" || route.DepotID == "" || len(route.TaskIDs) == 0 {
			return RouteSeed{}, fmt.Errorf(
				"%w: route %d has incomplete assignment",
				ErrInvalidRouteSeed,
				index,
			)
		}
	}
	slices.SortFunc(value.Routes, func(left, right SeedRoute) int {
		if result := strings.Compare(string(left.VehicleID), string(right.VehicleID)); result != 0 {
			return result
		}
		if left.TripIndex < right.TripIndex {
			return -1
		}
		if left.TripIndex > right.TripIndex {
			return 1
		}
		return strings.Compare(string(left.DriverID), string(right.DriverID))
	})
	value.Unassigned = append([]domain.FulfillmentUnitID(nil), value.Unassigned...)
	slices.Sort(value.Unassigned)
	digest, err := ComputeRouteSeedDigest(value)
	if err != nil {
		return RouteSeed{}, fmt.Errorf("%w: digest: %v", ErrInvalidRouteSeed, err)
	}
	if value.RouteSeedDigest != "" && value.RouteSeedDigest != digest {
		return RouteSeed{}, fmt.Errorf("%w: digest mismatch", ErrInvalidRouteSeed)
	}
	value.RouteSeedDigest = digest
	return value, nil
}
