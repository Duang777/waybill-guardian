//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliverysource "github.com/Duang777/waybill-guardian/internal/delivery/source"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestDeliverySourcePostgresImportAndReadSnapshot(t *testing.T) {
	db := openIntegrationDB(t)
	document := deliverySourceDocument(t, deliverydomain.TenantID("tenant-"+uuid.NewString()))

	if err := deliverysource.ImportPostgresSnapshot(t.Context(), db.pool, document); err != nil {
		t.Fatal(err)
	}
	if err := deliverysource.ImportPostgresSnapshot(t.Context(), db.pool, document); err != nil {
		t.Fatalf("idempotent import: %v", err)
	}
	provider, err := deliverysource.NewPostgresProvider(db.pool)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := deliverysource.NewCoordinator(deliverysource.CoordinatorConfig{
		Provider: provider,
		Limits:   deliverysource.DefaultLimits(),
		Clock: func() time.Time {
			return document.Manifest.IssuedAt.Add(time.Minute)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Build(t.Context(), deliverysource.BuildRequest{
		TenantID:  document.Manifest.TenantID,
		ProblemID: "problem-1",
		Version:   1,
		SourceRef: document.Manifest.Ref,
		Horizon: deliverydomain.TimeRange{
			Start: document.Manifest.IssuedAt,
			End:   document.Manifest.IssuedAt.Add(8 * time.Hour),
		},
		Commitments: deliverydomain.CommitmentSet{
			Executed:  []deliverydomain.ExecutedTaskCommitment{},
			Frozen:    []deliverydomain.FrozenTaskCommitment{},
			InTransit: []deliverydomain.InTransitCargoCommitment{},
			Soft:      []deliverydomain.SoftTaskCommitment{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Problem.ProblemDigest == "" ||
		result.Manifest.Digest != document.Manifest.Digest {
		t.Fatalf("unexpected build result: %+v", result)
	}
	if _, err := provider.OpenSnapshot(
		t.Context(),
		"other-tenant",
		document.Manifest.Ref,
	); !errors.Is(err, deliverysource.ErrNotFound) {
		t.Fatalf("cross-tenant OpenSnapshot error = %v, want ErrNotFound", err)
	}

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.delivery_source_snapshots
		SET imported_at = clock_timestamp()
		WHERE tenant_id = $1
		  AND snapshot_ref = $2
	`, document.Manifest.TenantID, document.Manifest.Ref); err == nil {
		t.Fatal("immutable delivery source row accepted an update")
	}
}

func TestDeliverySourcePostgresTransactionOptions(t *testing.T) {
	db := openIntegrationDB(t)
	tx, err := db.pool.BeginTx(t.Context(), pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())

	var isolation string
	var readOnly bool
	if err := tx.QueryRow(t.Context(), `
		SELECT current_setting('transaction_isolation'),
		       current_setting('transaction_read_only')::boolean
	`).Scan(&isolation, &readOnly); err != nil {
		t.Fatal(err)
	}
	if isolation != "repeatable read" || !readOnly {
		t.Fatalf("transaction settings = %q/%t, want repeatable read/true",
			isolation, readOnly)
	}
	if _, err := tx.Exec(t.Context(), `
		INSERT INTO waybill.delivery_source_snapshots (
			tenant_id,
			snapshot_ref,
			schema_version,
			manifest,
			orders_source,
			depots_source,
			fleet_source,
			drivers_source,
			travel_source,
			chargers_source,
			policy_source
		) VALUES (
			'readonly-test', 'cut', 'delivery.source-json.v1',
			'{}', '{}', '{}', '{}', '{}', '{}', '{}', '{}'
		)
	`); err == nil {
		t.Fatal("read-only delivery source transaction accepted a write")
	}
}

func deliverySourceDocument(
	t *testing.T,
	tenantID deliverydomain.TenantID,
) deliverysource.JSONDocument {
	t.Helper()
	issuedAt := time.Date(2026, time.October, 10, 8, 0, 0, 0, time.UTC)
	document := deliverysource.JSONDocument{
		SchemaVersion: deliverysource.JSONSchemaVersion,
		Orders: deliverysource.Block[deliverysource.Orders]{
			Data: deliverysource.Orders{
				Locations: []deliverydomain.Location{},
				Requests:  []deliverydomain.TransportRequest{},
				Units:     []deliverydomain.FulfillmentUnit{},
				Cargo:     []deliverydomain.CargoItem{},
			},
		},
		Depots: deliverysource.Block[deliverysource.Depots]{
			Data: deliverysource.Depots{
				Locations: []deliverydomain.Location{{
					ID:   "depot-location-1",
					Name: "Depot 1",
					Kind: deliverydomain.LocationDepot,
				}},
				Depots: []deliverydomain.Depot{{
					ID:             "depot-1",
					LocationID:     "depot-location-1",
					Docks:          []deliverydomain.Dock{},
					AllowTripStart: true,
					AllowTripEnd:   true,
				}},
			},
		},
		Fleet: deliverysource.Block[deliverysource.Fleet]{
			Data: deliverysource.Fleet{
				Vehicles: []deliverydomain.Vehicle{{
					ID:           "vehicle-1",
					HomeDepotID:  "depot-1",
					Availability: []deliverydomain.TimeRange{{Start: issuedAt, End: issuedAt.Add(8 * time.Hour)}},
					Skills:       deliverydomain.SkillSet{},
					Compartments: []deliverydomain.Compartment{{
						ID:          "compartment-1",
						Bounds:      deliverydomain.Cuboid{Size: deliverydomain.Box{Length: 4_000, Width: 2_000, Height: 2_000}},
						MaxPayloadG: 1_000_000,
					}},
					Doors:           []deliverydomain.Door{},
					Axles:           []deliverydomain.Axle{},
					MaxTrips:        1,
					MaxGrossWeightG: 2_000_000,
					TareWeightG:     500_000,
					Energy: deliverydomain.EnergySpec{
						Kind: deliverydomain.EnergyCombustion,
					},
				}},
			},
		},
		Drivers: deliverysource.Block[deliverysource.Drivers]{
			Data: deliverysource.Drivers{
				Drivers: []deliverydomain.Driver{{
					ID:            "driver-1",
					Skills:        deliverydomain.SkillSet{},
					Shift:         deliverydomain.TimeRange{Start: issuedAt, End: issuedAt.Add(8 * time.Hour)},
					StartLocation: "depot-location-1",
					EndLocations:  []deliverydomain.LocationID{"depot-location-1"},
				}},
			},
		},
		Travel: deliverysource.Block[deliverysource.Travel]{
			Data: deliverysource.Travel{
				Locations: []deliverydomain.Location{},
				Travel: deliverydomain.TravelMatrix{
					NodeIDs:        []deliverydomain.LocationID{"depot-location-1"},
					DistanceMeters: []int64{0},
					TravelSeconds:  []int64{0},
				},
				Energy: deliverydomain.EnergyMatrix{
					NodeIDs:  []deliverydomain.LocationID{"depot-location-1"},
					Profiles: []deliverydomain.EnergyProfileMatrix{},
				},
			},
		},
		Chargers: deliverysource.Block[deliverysource.Chargers]{
			Data: deliverysource.Chargers{
				Locations: []deliverydomain.Location{},
				Chargers:  []deliverydomain.ChargingStation{},
			},
		},
		Policy: deliverysource.Block[deliverysource.Policy]{
			Data: deliverysource.Policy{
				Policy: deliverydomain.PlanningPolicy{
					ID:                        "policy-1",
					Version:                   1,
					DefaultMinSupportPPM:      1_000_000,
					RequiredOrderPenaltyCents: 1_000_000,
					AllowedMixedCargoClasses:  [][]string{},
				},
			},
		},
	}
	document.Manifest = deliverysource.Manifest{
		SchemaVersion: deliverysource.ManifestSchemaVersion,
		TenantID:      tenantID,
		Ref:           deliverysource.SnapshotRef("cut-" + uuid.NewString()),
		IssuedAt:      issuedAt,
		ExpiresAt:     issuedAt.Add(24 * time.Hour),
	}
	kinds := []deliverysource.Kind{
		deliverysource.KindOrders,
		deliverysource.KindDepots,
		deliverysource.KindFleet,
		deliverysource.KindDrivers,
		deliverysource.KindTravel,
		deliverysource.KindChargers,
		deliverysource.KindPolicy,
	}
	for _, kind := range kinds {
		digest, err := deliverydomain.Digest(deliverySourceData(document, kind))
		if err != nil {
			t.Fatal(err)
		}
		document.Manifest.Revisions = append(
			document.Manifest.Revisions,
			deliverysource.Revision{
				Kind:          kind,
				System:        string(kind) + "-system",
				Revision:      string(kind) + "-revision-1",
				EffectiveAt:   issuedAt.Add(-time.Hour),
				ETag:          `"` + string(kind) + `-etag-1"`,
				ContentDigest: digest,
			},
		)
	}
	var err error
	document.Manifest, err = deliverysource.SealManifest(document.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range document.Manifest.Revisions {
		stamp := deliverysource.Stamp{
			TenantID:      document.Manifest.TenantID,
			ManifestRef:   document.Manifest.Ref,
			Kind:          revision.Kind,
			System:        revision.System,
			Revision:      revision.Revision,
			EffectiveAt:   revision.EffectiveAt,
			FetchedAt:     document.Manifest.IssuedAt,
			ETag:          revision.ETag,
			EventOffset:   revision.EventOffset,
			ContentDigest: revision.ContentDigest,
		}
		setDeliverySourceStamp(&document, revision.Kind, stamp)
	}
	return document
}

func deliverySourceData(
	document deliverysource.JSONDocument,
	kind deliverysource.Kind,
) any {
	switch kind {
	case deliverysource.KindOrders:
		return document.Orders.Data
	case deliverysource.KindDepots:
		return document.Depots.Data
	case deliverysource.KindFleet:
		return document.Fleet.Data
	case deliverysource.KindDrivers:
		return document.Drivers.Data
	case deliverysource.KindTravel:
		return document.Travel.Data
	case deliverysource.KindChargers:
		return document.Chargers.Data
	case deliverysource.KindPolicy:
		return document.Policy.Data
	default:
		panic("unknown delivery source kind " + string(kind))
	}
}

func setDeliverySourceStamp(
	document *deliverysource.JSONDocument,
	kind deliverysource.Kind,
	stamp deliverysource.Stamp,
) {
	switch kind {
	case deliverysource.KindOrders:
		document.Orders.Stamp = stamp
	case deliverysource.KindDepots:
		document.Depots.Stamp = stamp
	case deliverysource.KindFleet:
		document.Fleet.Stamp = stamp
	case deliverysource.KindDrivers:
		document.Drivers.Stamp = stamp
	case deliverysource.KindTravel:
		document.Travel.Stamp = stamp
	case deliverysource.KindChargers:
		document.Chargers.Stamp = stamp
	case deliverysource.KindPolicy:
		document.Policy.Stamp = stamp
	default:
		panic("unknown delivery source kind " + string(kind))
	}
}
