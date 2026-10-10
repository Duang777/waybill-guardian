package source

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

func TestCoordinatorBuildsDeterministicProblemFromExactManifest(t *testing.T) {
	document := sourceTestDocument(t)
	snapshot := &memorySnapshot{document: document}
	coordinator := newTestCoordinator(t, memoryProvider{snapshot: snapshot})
	request := sourceTestBuildRequest(document.Manifest)

	var first domain.ArtifactDigest
	for attempt := 0; attempt < 20; attempt++ {
		fetchedAt := document.Manifest.IssuedAt.Add(time.Duration(attempt) * time.Minute)
		snapshot.setFetchedAt(fetchedAt)
		result, err := coordinator.Build(context.Background(), request)
		if err != nil {
			t.Fatalf("Build attempt %d: %v", attempt, err)
		}
		if attempt == 0 {
			first = result.Problem.ProblemDigest
		} else if result.Problem.ProblemDigest != first {
			t.Fatalf(
				"Build attempt %d digest = %s, want %s",
				attempt,
				result.Problem.ProblemDigest,
				first,
			)
		}
		if !result.Problem.CreatedAt.Equal(document.Manifest.IssuedAt) {
			t.Fatalf("CreatedAt = %s, want manifest issued_at %s",
				result.Problem.CreatedAt, document.Manifest.IssuedAt)
		}
		if len(result.Problem.SourceRefs) != len(allKinds) {
			t.Fatalf("SourceRefs count = %d, want %d",
				len(result.Problem.SourceRefs), len(allKinds))
		}
		for _, sourceRef := range result.Problem.SourceRefs {
			if sourceRef.ResourceID != string(document.Manifest.Ref) {
				t.Fatalf("SourceRef resource id = %q", sourceRef.ResourceID)
			}
		}
	}
	if got := snapshot.closeCount.Load(); got != 20 {
		t.Fatalf("snapshot close count = %d, want 20", got)
	}
}

func TestCoordinatorRejectsMixedSourceRevision(t *testing.T) {
	document := sourceTestDocument(t)
	document.Drivers.Stamp.Revision = "drivers-other"
	coordinator := newTestCoordinator(t, memoryProvider{
		snapshot: &memorySnapshot{document: document},
	})

	_, err := coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("Build error = %v, want ErrSourceMismatch", err)
	}
}

func TestCoordinatorRejectsContentThatDoesNotMatchManifestDigest(t *testing.T) {
	document := sourceTestDocument(t)
	document.Fleet.Data.Vehicles[0].FixedCostCents++
	coordinator := newTestCoordinator(t, memoryProvider{
		snapshot: &memorySnapshot{document: document},
	})

	_, err := coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if !errors.Is(err, ErrSourceMismatch) ||
		!strings.Contains(err.Error(), "content digest") {
		t.Fatalf("Build error = %v, want content digest mismatch", err)
	}
}

func TestCoordinatorRejectsCrossTenantManifest(t *testing.T) {
	document := sourceTestDocument(t)
	request := sourceTestBuildRequest(document.Manifest)
	request.TenantID = "tenant-b"
	coordinator := newTestCoordinator(t, memoryProvider{
		snapshot: &memorySnapshot{document: document},
	})

	_, err := coordinator.Build(context.Background(), request)
	if !errors.Is(err, ErrManifestMismatch) {
		t.Fatalf("Build error = %v, want ErrManifestMismatch", err)
	}
}

func TestCoordinatorRejectsExpiredManifest(t *testing.T) {
	document := sourceTestDocument(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Provider: memoryProvider{snapshot: &memorySnapshot{document: document}},
		Limits:   DefaultLimits(),
		Clock: func() time.Time {
			return document.Manifest.ExpiresAt
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if !errors.Is(err, ErrManifestMismatch) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("Build error = %v, want expired manifest", err)
	}
}

func TestCoordinatorCancelsSlowReadersAndClosesSnapshot(t *testing.T) {
	document := sourceTestDocument(t)
	snapshot := &memorySnapshot{
		document:  document,
		delayKind: KindTravel,
		delay:     time.Minute,
	}
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Provider:     memoryProvider{snapshot: snapshot},
		ReadTimeout:  20 * time.Millisecond,
		CloseTimeout: time.Second,
		Limits:       DefaultLimits(),
		Clock: func() time.Time {
			return document.Manifest.IssuedAt.Add(time.Minute)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	_, err = coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Build error = %v, want context deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Build took %s after deadline", elapsed)
	}
	if got := snapshot.closeCount.Load(); got != 1 {
		t.Fatalf("snapshot close count = %d, want 1", got)
	}
}

func TestCoordinatorEnforcesSourceLimitsBeforeBuilding(t *testing.T) {
	document := sourceTestDocument(t)
	limits := DefaultLimits()
	limits.MaxVehicles = 1
	document.Fleet.Data.Vehicles = append(
		document.Fleet.Data.Vehicles,
		document.Fleet.Data.Vehicles[0],
	)
	document.Fleet.Data.Vehicles[1].ID = "vehicle-2"
	resealBlock(t, &document, KindFleet)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Provider: memoryProvider{snapshot: &memorySnapshot{document: document}},
		Limits:   limits,
		Clock: func() time.Time {
			return document.Manifest.IssuedAt.Add(time.Minute)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = coordinator.Build(
		context.Background(),
		sourceTestBuildRequest(document.Manifest),
	)
	if !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("Build error = %v, want ErrSourceTooLarge", err)
	}
}

func TestSealManifestRequiresEveryPinnedSource(t *testing.T) {
	document := sourceTestDocument(t)
	manifest := document.Manifest
	manifest.Digest = ""
	manifest.Revisions = manifest.Revisions[:len(manifest.Revisions)-1]

	_, err := SealManifest(manifest)
	if err == nil || !strings.Contains(err.Error(), "exactly 7") {
		t.Fatalf("SealManifest error = %v, want exact source count rejection", err)
	}
}

type memoryProvider struct {
	snapshot *memorySnapshot
}

func (provider memoryProvider) OpenSnapshot(
	ctx context.Context,
	tenantID domain.TenantID,
	ref SnapshotRef,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return provider.snapshot, nil
}

type memorySnapshot struct {
	document   JSONDocument
	delayKind  Kind
	delay      time.Duration
	closeCount atomic.Int64
}

func (snapshot *memorySnapshot) ReadManifest(ctx context.Context) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	return snapshot.document.Manifest, nil
}

func (snapshot *memorySnapshot) ReadOrders(
	ctx context.Context,
	revision Revision,
) (Orders, Stamp, error) {
	if err := snapshot.wait(ctx, KindOrders); err != nil {
		return Orders{}, Stamp{}, err
	}
	return snapshot.document.Orders.Data, snapshot.document.Orders.Stamp,
		snapshot.checkRevision(KindOrders, revision)
}

func (snapshot *memorySnapshot) ReadDepots(
	ctx context.Context,
	revision Revision,
) (Depots, Stamp, error) {
	if err := snapshot.wait(ctx, KindDepots); err != nil {
		return Depots{}, Stamp{}, err
	}
	return snapshot.document.Depots.Data, snapshot.document.Depots.Stamp,
		snapshot.checkRevision(KindDepots, revision)
}

func (snapshot *memorySnapshot) ReadFleet(
	ctx context.Context,
	revision Revision,
) (Fleet, Stamp, error) {
	if err := snapshot.wait(ctx, KindFleet); err != nil {
		return Fleet{}, Stamp{}, err
	}
	return snapshot.document.Fleet.Data, snapshot.document.Fleet.Stamp,
		snapshot.checkRevision(KindFleet, revision)
}

func (snapshot *memorySnapshot) ReadDrivers(
	ctx context.Context,
	revision Revision,
) (Drivers, Stamp, error) {
	if err := snapshot.wait(ctx, KindDrivers); err != nil {
		return Drivers{}, Stamp{}, err
	}
	return snapshot.document.Drivers.Data, snapshot.document.Drivers.Stamp,
		snapshot.checkRevision(KindDrivers, revision)
}

func (snapshot *memorySnapshot) ReadTravel(
	ctx context.Context,
	revision Revision,
) (Travel, Stamp, error) {
	if err := snapshot.wait(ctx, KindTravel); err != nil {
		return Travel{}, Stamp{}, err
	}
	return snapshot.document.Travel.Data, snapshot.document.Travel.Stamp,
		snapshot.checkRevision(KindTravel, revision)
}

func (snapshot *memorySnapshot) ReadChargers(
	ctx context.Context,
	revision Revision,
) (Chargers, Stamp, error) {
	if err := snapshot.wait(ctx, KindChargers); err != nil {
		return Chargers{}, Stamp{}, err
	}
	return snapshot.document.Chargers.Data, snapshot.document.Chargers.Stamp,
		snapshot.checkRevision(KindChargers, revision)
}

func (snapshot *memorySnapshot) ReadPolicy(
	ctx context.Context,
	revision Revision,
) (Policy, Stamp, error) {
	if err := snapshot.wait(ctx, KindPolicy); err != nil {
		return Policy{}, Stamp{}, err
	}
	return snapshot.document.Policy.Data, snapshot.document.Policy.Stamp,
		snapshot.checkRevision(KindPolicy, revision)
}

func (snapshot *memorySnapshot) Close(context.Context) error {
	snapshot.closeCount.Add(1)
	return nil
}

func (snapshot *memorySnapshot) wait(ctx context.Context, kind Kind) error {
	if kind != snapshot.delayKind || snapshot.delay == 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(snapshot.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (snapshot *memorySnapshot) checkRevision(kind Kind, got Revision) error {
	want := revisionFor(snapshot.document.Manifest, kind)
	if got != want {
		return errors.New("coordinator supplied the wrong revision")
	}
	return nil
}

func (snapshot *memorySnapshot) setFetchedAt(value time.Time) {
	snapshot.document.Orders.Stamp.FetchedAt = value
	snapshot.document.Depots.Stamp.FetchedAt = value
	snapshot.document.Fleet.Stamp.FetchedAt = value
	snapshot.document.Drivers.Stamp.FetchedAt = value
	snapshot.document.Travel.Stamp.FetchedAt = value
	snapshot.document.Chargers.Stamp.FetchedAt = value
	snapshot.document.Policy.Stamp.FetchedAt = value
}

func newTestCoordinator(t *testing.T, provider Provider) *Coordinator {
	t.Helper()
	document := sourceTestDocument(t)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Provider: provider,
		Limits:   DefaultLimits(),
		Clock: func() time.Time {
			return document.Manifest.IssuedAt.Add(time.Minute)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func sourceTestBuildRequest(manifest Manifest) BuildRequest {
	return BuildRequest{
		TenantID:  manifest.TenantID,
		ProblemID: "problem-1",
		Version:   1,
		SourceRef: manifest.Ref,
		Horizon: domain.TimeRange{
			Start: manifest.IssuedAt,
			End:   manifest.IssuedAt.Add(8 * time.Hour),
		},
		Commitments: domain.CommitmentSet{
			Executed:  []domain.ExecutedTaskCommitment{},
			Frozen:    []domain.FrozenTaskCommitment{},
			InTransit: []domain.InTransitCargoCommitment{},
			Soft:      []domain.SoftTaskCommitment{},
		},
	}
}

func sourceTestDocument(t *testing.T) JSONDocument {
	t.Helper()
	issuedAt := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	effectiveAt := issuedAt.Add(-time.Hour)
	document := JSONDocument{
		SchemaVersion: JSONSchemaVersion,
		Orders: Block[Orders]{Data: Orders{
			Locations: []domain.Location{},
			Requests:  []domain.TransportRequest{},
			Units:     []domain.FulfillmentUnit{},
			Cargo:     []domain.CargoItem{},
		}},
		Depots: Block[Depots]{Data: Depots{
			Locations: []domain.Location{{
				ID:   "depot-location-1",
				Name: "Depot 1",
				Kind: domain.LocationDepot,
			}},
			Depots: []domain.Depot{{
				ID:             "depot-1",
				LocationID:     "depot-location-1",
				Docks:          []domain.Dock{},
				AllowTripStart: true,
				AllowTripEnd:   true,
			}},
		}},
		Fleet: Block[Fleet]{Data: Fleet{
			Vehicles: []domain.Vehicle{{
				ID:          "vehicle-1",
				HomeDepotID: "depot-1",
				Availability: []domain.TimeRange{{
					Start: issuedAt,
					End:   issuedAt.Add(8 * time.Hour),
				}},
				Skills: domain.SkillSet{},
				Compartments: []domain.Compartment{{
					ID:          "compartment-1",
					Bounds:      domain.Cuboid{Size: domain.Box{Length: 4_000, Width: 2_000, Height: 2_000}},
					MaxPayloadG: 1_000_000,
				}},
				Doors:           []domain.Door{},
				Axles:           []domain.Axle{},
				MaxTrips:        1,
				MaxGrossWeightG: 2_000_000,
				TareWeightG:     500_000,
				Energy: domain.EnergySpec{
					Kind: domain.EnergyCombustion,
				},
			}},
		}},
		Drivers: Block[Drivers]{Data: Drivers{
			Drivers: []domain.Driver{{
				ID:            "driver-1",
				Skills:        domain.SkillSet{},
				Shift:         domain.TimeRange{Start: issuedAt, End: issuedAt.Add(8 * time.Hour)},
				StartLocation: "depot-location-1",
				EndLocations:  []domain.LocationID{"depot-location-1"},
			}},
		}},
		Travel: Block[Travel]{Data: Travel{
			Locations: []domain.Location{},
			Travel: domain.TravelMatrix{
				NodeIDs:        []domain.LocationID{"depot-location-1"},
				DistanceMeters: []int64{0},
				TravelSeconds:  []int64{0},
			},
			Energy: domain.EnergyMatrix{
				NodeIDs:  []domain.LocationID{"depot-location-1"},
				Profiles: []domain.EnergyProfileMatrix{},
			},
		}},
		Chargers: Block[Chargers]{Data: Chargers{
			Locations: []domain.Location{},
			Chargers:  []domain.ChargingStation{},
		}},
		Policy: Block[Policy]{Data: Policy{
			Policy: domain.PlanningPolicy{
				ID:                        "policy-1",
				Version:                   1,
				DefaultMinSupportPPM:      1_000_000,
				AllowedMixedCargoClasses:  [][]string{},
				RequiredOrderPenaltyCents: 1_000_000,
			},
		}},
	}
	document.Manifest = Manifest{
		SchemaVersion: ManifestSchemaVersion,
		TenantID:      "tenant-a",
		Ref:           "cut-1",
		IssuedAt:      issuedAt,
		ExpiresAt:     issuedAt.Add(24 * time.Hour),
	}
	for _, kind := range allKinds {
		digest := digestBlockData(t, document, kind)
		document.Manifest.Revisions = append(document.Manifest.Revisions, Revision{
			Kind:          kind,
			System:        string(kind) + "-system",
			Revision:      string(kind) + "-revision-1",
			EffectiveAt:   effectiveAt,
			ETag:          `"` + string(kind) + `-etag-1"`,
			ContentDigest: digest,
		})
	}
	var err error
	document.Manifest, err = SealManifest(document.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range allKinds {
		setBlockStamp(&document, kind, stampFor(
			document.Manifest,
			revisionFor(document.Manifest, kind),
			issuedAt,
		))
	}
	return document
}

func resealBlock(t *testing.T, document *JSONDocument, kind Kind) {
	t.Helper()
	for index := range document.Manifest.Revisions {
		if document.Manifest.Revisions[index].Kind == kind {
			document.Manifest.Revisions[index].ContentDigest =
				digestBlockData(t, *document, kind)
		}
	}
	document.Manifest.Digest = ""
	var err error
	document.Manifest, err = SealManifest(document.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	setBlockStamp(
		document,
		kind,
		stampFor(
			document.Manifest,
			revisionFor(document.Manifest, kind),
			document.Manifest.IssuedAt,
		),
	)
}

func digestBlockData(t *testing.T, document JSONDocument, kind Kind) domain.ArtifactDigest {
	t.Helper()
	var value any
	switch kind {
	case KindOrders:
		value = document.Orders.Data
	case KindDepots:
		value = document.Depots.Data
	case KindFleet:
		value = document.Fleet.Data
	case KindDrivers:
		value = document.Drivers.Data
	case KindTravel:
		value = document.Travel.Data
	case KindChargers:
		value = document.Chargers.Data
	case KindPolicy:
		value = document.Policy.Data
	default:
		t.Fatalf("unknown source kind %q", kind)
	}
	digest, err := domain.Digest(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func stampFor(manifest Manifest, revision Revision, fetchedAt time.Time) Stamp {
	return Stamp{
		TenantID:      manifest.TenantID,
		ManifestRef:   manifest.Ref,
		Kind:          revision.Kind,
		System:        revision.System,
		Revision:      revision.Revision,
		EffectiveAt:   revision.EffectiveAt,
		FetchedAt:     fetchedAt,
		ETag:          revision.ETag,
		EventOffset:   revision.EventOffset,
		ContentDigest: revision.ContentDigest,
	}
}

func setBlockStamp(document *JSONDocument, kind Kind, stamp Stamp) {
	switch kind {
	case KindOrders:
		document.Orders.Stamp = stamp
	case KindDepots:
		document.Depots.Stamp = stamp
	case KindFleet:
		document.Fleet.Stamp = stamp
	case KindDrivers:
		document.Drivers.Stamp = stamp
	case KindTravel:
		document.Travel.Stamp = stamp
	case KindChargers:
		document.Chargers.Stamp = stamp
	case KindPolicy:
		document.Policy.Stamp = stamp
	default:
		panic("unknown source kind " + string(kind))
	}
}
