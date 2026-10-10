package source

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
)

const (
	defaultReadTimeout  = 30 * time.Second
	defaultCloseTimeout = 5 * time.Second
)

type Limits struct {
	MaxLocations   int
	MaxDepots      int
	MaxRequests    int
	MaxUnits       int
	MaxCargo       int
	MaxVehicles    int
	MaxDrivers     int
	MaxChargers    int
	MaxMatrixNodes int
}

func DefaultLimits() Limits {
	return Limits{
		MaxLocations:   100_000,
		MaxDepots:      10_000,
		MaxRequests:    100_000,
		MaxUnits:       500_000,
		MaxCargo:       1_000_000,
		MaxVehicles:    100_000,
		MaxDrivers:     100_000,
		MaxChargers:    100_000,
		MaxMatrixNodes: 10_000,
	}
}

type CoordinatorConfig struct {
	Provider     Provider
	ReadTimeout  time.Duration
	CloseTimeout time.Duration
	Limits       Limits
	Clock        func() time.Time
}

type Coordinator struct {
	provider     Provider
	readTimeout  time.Duration
	closeTimeout time.Duration
	limits       Limits
	clock        func() time.Time
}

type BuildRequest struct {
	TenantID    domain.TenantID
	ProblemID   domain.ProblemID
	Version     uint64
	SourceRef   SnapshotRef
	Horizon     domain.TimeRange
	Commitments domain.CommitmentSet
}

type BuildResult struct {
	Problem  domain.ProblemSnapshot
	Manifest Manifest
}

func NewCoordinator(config CoordinatorConfig) (*Coordinator, error) {
	if config.Provider == nil {
		return nil, fmt.Errorf("delivery source provider is required")
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = defaultReadTimeout
	}
	if config.CloseTimeout == 0 {
		config.CloseTimeout = defaultCloseTimeout
	}
	if config.ReadTimeout < 0 || config.CloseTimeout < 0 {
		return nil, fmt.Errorf("delivery source timeouts must be positive")
	}
	if config.Limits == (Limits{}) {
		config.Limits = DefaultLimits()
	}
	if err := validateLimits(config.Limits); err != nil {
		return nil, err
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Coordinator{
		provider:     config.Provider,
		readTimeout:  config.ReadTimeout,
		closeTimeout: config.CloseTimeout,
		limits:       config.Limits,
		clock:        config.Clock,
	}, nil
}

func (coordinator *Coordinator) Build(
	ctx context.Context,
	request BuildRequest,
) (result BuildResult, err error) {
	if err := validateBuildRequest(request); err != nil {
		return BuildResult{}, err
	}
	readCtx, cancelRead := context.WithTimeout(ctx, coordinator.readTimeout)
	defer cancelRead()

	snapshot, err := coordinator.provider.OpenSnapshot(
		readCtx,
		request.TenantID,
		request.SourceRef,
	)
	if err != nil {
		return BuildResult{}, fmt.Errorf("open delivery source snapshot: %w", err)
	}
	defer func() {
		closeCtx, cancelClose := context.WithTimeout(
			context.WithoutCancel(ctx),
			coordinator.closeTimeout,
		)
		defer cancelClose()
		if closeErr := snapshot.Close(closeCtx); err == nil && closeErr != nil {
			result = BuildResult{}
			err = fmt.Errorf("close delivery source snapshot: %w", closeErr)
		}
	}()

	manifest, err := snapshot.ReadManifest(readCtx)
	if err != nil {
		return BuildResult{}, fmt.Errorf("read delivery source manifest: %w", err)
	}
	manifest, err = ValidateManifest(
		manifest,
		request.TenantID,
		request.SourceRef,
		coordinator.clock().UTC(),
	)
	if err != nil {
		return BuildResult{}, err
	}

	var (
		orders   Orders
		depots   Depots
		fleet    Fleet
		drivers  Drivers
		travel   Travel
		chargers Chargers
		policy   Policy
	)
	group, sourceCtx := errgroup.WithContext(readCtx)
	group.Go(func() error {
		revision := revisionFor(manifest, KindOrders)
		value, stamp, readErr := snapshot.ReadOrders(sourceCtx, revision)
		if readErr != nil {
			return fmt.Errorf("read orders source: %w", readErr)
		}
		if readErr = validateBlock(manifest, revision, stamp, value); readErr != nil {
			return readErr
		}
		orders = value
		return nil
	})
	group.Go(func() error {
		revision := revisionFor(manifest, KindDepots)
		value, stamp, readErr := snapshot.ReadDepots(sourceCtx, revision)
		if readErr != nil {
			return fmt.Errorf("read depots source: %w", readErr)
		}
		if readErr = validateBlock(manifest, revision, stamp, value); readErr != nil {
			return readErr
		}
		depots = value
		return nil
	})
	group.Go(func() error {
		revision := revisionFor(manifest, KindFleet)
		value, stamp, readErr := snapshot.ReadFleet(sourceCtx, revision)
		if readErr != nil {
			return fmt.Errorf("read fleet source: %w", readErr)
		}
		if readErr = validateBlock(manifest, revision, stamp, value); readErr != nil {
			return readErr
		}
		fleet = value
		return nil
	})
	group.Go(func() error {
		revision := revisionFor(manifest, KindDrivers)
		value, stamp, readErr := snapshot.ReadDrivers(sourceCtx, revision)
		if readErr != nil {
			return fmt.Errorf("read drivers source: %w", readErr)
		}
		if readErr = validateBlock(manifest, revision, stamp, value); readErr != nil {
			return readErr
		}
		drivers = value
		return nil
	})
	group.Go(func() error {
		revision := revisionFor(manifest, KindTravel)
		value, stamp, readErr := snapshot.ReadTravel(sourceCtx, revision)
		if readErr != nil {
			return fmt.Errorf("read travel source: %w", readErr)
		}
		if readErr = validateBlock(manifest, revision, stamp, value); readErr != nil {
			return readErr
		}
		travel = value
		return nil
	})
	group.Go(func() error {
		revision := revisionFor(manifest, KindChargers)
		value, stamp, readErr := snapshot.ReadChargers(sourceCtx, revision)
		if readErr != nil {
			return fmt.Errorf("read chargers source: %w", readErr)
		}
		if readErr = validateBlock(manifest, revision, stamp, value); readErr != nil {
			return readErr
		}
		chargers = value
		return nil
	})
	group.Go(func() error {
		revision := revisionFor(manifest, KindPolicy)
		value, stamp, readErr := snapshot.ReadPolicy(sourceCtx, revision)
		if readErr != nil {
			return fmt.Errorf("read policy source: %w", readErr)
		}
		if readErr = validateBlock(manifest, revision, stamp, value); readErr != nil {
			return readErr
		}
		policy = value
		return nil
	})
	if err := group.Wait(); err != nil {
		return BuildResult{}, err
	}
	if err := coordinator.checkLimits(orders, depots, fleet, drivers, travel, chargers); err != nil {
		return BuildResult{}, err
	}

	locations := make(
		[]domain.Location,
		0,
		len(orders.Locations)+len(depots.Locations)+
			len(travel.Locations)+len(chargers.Locations),
	)
	locations = append(locations, orders.Locations...)
	locations = append(locations, depots.Locations...)
	locations = append(locations, travel.Locations...)
	locations = append(locations, chargers.Locations...)

	draft := domain.ProblemSnapshot{
		SchemaVersion: domain.ProblemSchemaVersion,
		TenantID:      request.TenantID,
		ProblemID:     request.ProblemID,
		Version:       request.Version,
		Horizon:       request.Horizon,
		Locations:     locations,
		Depots:        depots.Depots,
		Requests:      orders.Requests,
		Units:         orders.Units,
		Cargo:         orders.Cargo,
		Vehicles:      fleet.Vehicles,
		Drivers:       drivers.Drivers,
		Chargers:      chargers.Chargers,
		Travel:        travel.Travel,
		Energy:        travel.Energy,
		Policy:        policy.Policy,
		Commitments:   request.Commitments,
		SourceRefs:    sourceRefs(manifest),
		CreatedAt:     manifest.IssuedAt,
	}
	problem, err := service.BuildProblemSnapshot(draft)
	if err != nil {
		return BuildResult{}, fmt.Errorf("build problem snapshot: %w", err)
	}
	return BuildResult{Problem: problem, Manifest: manifest}, nil
}

func validateBuildRequest(request BuildRequest) error {
	if strings.TrimSpace(string(request.TenantID)) == "" ||
		strings.TrimSpace(string(request.TenantID)) != string(request.TenantID) {
		return fmt.Errorf("tenant_id is required without surrounding whitespace")
	}
	if strings.TrimSpace(string(request.ProblemID)) == "" ||
		strings.TrimSpace(string(request.ProblemID)) != string(request.ProblemID) {
		return fmt.Errorf("problem_id is required without surrounding whitespace")
	}
	if request.Version == 0 {
		return fmt.Errorf("problem version must be positive")
	}
	if strings.TrimSpace(string(request.SourceRef)) == "" ||
		strings.TrimSpace(string(request.SourceRef)) != string(request.SourceRef) {
		return fmt.Errorf("source_ref is required without surrounding whitespace")
	}
	if request.Horizon.Start.IsZero() ||
		request.Horizon.End.IsZero() ||
		!request.Horizon.Start.Before(request.Horizon.End) ||
		!isUTC(request.Horizon.Start) ||
		!isUTC(request.Horizon.End) {
		return fmt.Errorf("horizon must be a non-empty UTC interval")
	}
	return nil
}

func validateLimits(limits Limits) error {
	values := []struct {
		name  string
		value int
	}{
		{"locations", limits.MaxLocations},
		{"depots", limits.MaxDepots},
		{"requests", limits.MaxRequests},
		{"units", limits.MaxUnits},
		{"cargo", limits.MaxCargo},
		{"vehicles", limits.MaxVehicles},
		{"drivers", limits.MaxDrivers},
		{"chargers", limits.MaxChargers},
		{"matrix nodes", limits.MaxMatrixNodes},
	}
	for _, limit := range values {
		if limit.value <= 0 {
			return fmt.Errorf("delivery source limit for %s must be positive", limit.name)
		}
	}
	return nil
}

func (coordinator *Coordinator) checkLimits(
	orders Orders,
	depots Depots,
	fleet Fleet,
	drivers Drivers,
	travel Travel,
	chargers Chargers,
) error {
	counts := []struct {
		name  string
		value int
		limit int
	}{
		{
			"locations",
			len(orders.Locations) + len(depots.Locations) +
				len(travel.Locations) + len(chargers.Locations),
			coordinator.limits.MaxLocations,
		},
		{"depots", len(depots.Depots), coordinator.limits.MaxDepots},
		{"requests", len(orders.Requests), coordinator.limits.MaxRequests},
		{"units", len(orders.Units), coordinator.limits.MaxUnits},
		{"cargo", len(orders.Cargo), coordinator.limits.MaxCargo},
		{"vehicles", len(fleet.Vehicles), coordinator.limits.MaxVehicles},
		{"drivers", len(drivers.Drivers), coordinator.limits.MaxDrivers},
		{"chargers", len(chargers.Chargers), coordinator.limits.MaxChargers},
		{"travel matrix nodes", len(travel.Travel.NodeIDs), coordinator.limits.MaxMatrixNodes},
		{"energy matrix nodes", len(travel.Energy.NodeIDs), coordinator.limits.MaxMatrixNodes},
	}
	for _, count := range counts {
		if count.value > count.limit {
			return fmt.Errorf("%w: %s has %d entries, limit is %d",
				ErrSourceTooLarge, count.name, count.value, count.limit)
		}
	}
	return nil
}

func sourceRefs(manifest Manifest) []domain.SourceRef {
	result := make([]domain.SourceRef, 0, len(manifest.Revisions))
	for _, revision := range manifest.Revisions {
		result = append(result, domain.SourceRef{
			System:       revision.System,
			ResourceType: string(revision.Kind),
			ResourceID:   string(manifest.Ref),
			Version:      revision.Revision,
			ObservedAt:   revision.EffectiveAt,
		})
	}
	return result
}
