package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

type postgresTx interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type beginPostgresSnapshot func(
	context.Context,
	pgx.TxOptions,
) (postgresTx, error)

type PostgresProvider struct {
	begin beginPostgresSnapshot
}

func NewPostgresProvider(pool *pgxpool.Pool) (*PostgresProvider, error) {
	if pool == nil {
		return nil, fmt.Errorf("delivery source PostgreSQL pool is required")
	}
	return &PostgresProvider{
		begin: func(ctx context.Context, options pgx.TxOptions) (postgresTx, error) {
			return pool.BeginTx(ctx, options)
		},
	}, nil
}

func (provider *PostgresProvider) OpenSnapshot(
	ctx context.Context,
	tenantID domain.TenantID,
	ref SnapshotRef,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tx, err := provider.begin(ctx, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, fmt.Errorf("begin delivery source PostgreSQL snapshot: %w", err)
	}
	rollback := func() {
		_ = tx.Rollback(context.Background())
	}

	var (
		schemaVersion string
		manifestRaw   []byte
		ordersRaw     []byte
		depotsRaw     []byte
		fleetRaw      []byte
		driversRaw    []byte
		travelRaw     []byte
		chargersRaw   []byte
		policyRaw     []byte
	)
	err = tx.QueryRow(ctx, `
		SELECT schema_version,
		       manifest,
		       orders_source,
		       depots_source,
		       fleet_source,
		       drivers_source,
		       travel_source,
		       chargers_source,
		       policy_source
		FROM waybill.delivery_source_snapshots
		WHERE tenant_id = $1
		  AND snapshot_ref = $2
	`, tenantID, ref).Scan(
		&schemaVersion,
		&manifestRaw,
		&ordersRaw,
		&depotsRaw,
		&fleetRaw,
		&driversRaw,
		&travelRaw,
		&chargersRaw,
		&policyRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		rollback()
		return nil, ErrNotFound
	}
	if err != nil {
		rollback()
		return nil, fmt.Errorf("read delivery source PostgreSQL snapshot: %w", err)
	}

	document, err := decodePostgresDocument(
		schemaVersion,
		manifestRaw,
		ordersRaw,
		depotsRaw,
		fleetRaw,
		driversRaw,
		travelRaw,
		chargersRaw,
		policyRaw,
	)
	if err != nil {
		rollback()
		return nil, err
	}
	if document.Manifest.TenantID != tenantID || document.Manifest.Ref != ref {
		rollback()
		return nil, fmt.Errorf("%w: PostgreSQL row does not match tenant/ref",
			ErrManifestMismatch)
	}
	return &postgresSnapshot{
		jsonSnapshot: jsonSnapshot{document: document},
		tx:           tx,
	}, nil
}

type postgresSnapshot struct {
	jsonSnapshot
	tx     postgresTx
	closed atomic.Bool
}

func (snapshot *postgresSnapshot) Close(ctx context.Context) error {
	if !snapshot.closed.CompareAndSwap(false, true) {
		return nil
	}
	if err := snapshot.tx.Commit(ctx); err != nil {
		_ = snapshot.tx.Rollback(context.Background())
		return fmt.Errorf("commit delivery source PostgreSQL snapshot: %w", err)
	}
	return nil
}

func ImportPostgresSnapshot(
	ctx context.Context,
	pool *pgxpool.Pool,
	document JSONDocument,
) error {
	if pool == nil {
		return fmt.Errorf("delivery source PostgreSQL pool is required")
	}
	if err := validateJSONDocument(document); err != nil {
		return err
	}
	values := []any{
		document.Manifest,
		document.Orders,
		document.Depots,
		document.Fleet,
		document.Drivers,
		document.Travel,
		document.Chargers,
		document.Policy,
	}
	raw := make([][]byte, len(values))
	for index, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("encode delivery source PostgreSQL import: %w", err)
		}
		raw[index] = encoded
	}
	tag, err := pool.Exec(ctx, `
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
			$1, $2, $3,
			$4::jsonb, $5::jsonb, $6::jsonb, $7::jsonb,
			$8::jsonb, $9::jsonb, $10::jsonb, $11::jsonb
		)
		ON CONFLICT (tenant_id, snapshot_ref) DO NOTHING
	`,
		document.Manifest.TenantID,
		document.Manifest.Ref,
		document.SchemaVersion,
		raw[0],
		raw[1],
		raw[2],
		raw[3],
		raw[4],
		raw[5],
		raw[6],
		raw[7],
	)
	if err != nil {
		return fmt.Errorf("import delivery source PostgreSQL snapshot: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var existing Manifest
	var existingRaw []byte
	if err := pool.QueryRow(ctx, `
		SELECT manifest
		FROM waybill.delivery_source_snapshots
		WHERE tenant_id = $1
		  AND snapshot_ref = $2
	`, document.Manifest.TenantID, document.Manifest.Ref).Scan(&existingRaw); err != nil {
		return fmt.Errorf("verify existing delivery source PostgreSQL snapshot: %w", err)
	}
	if err := decodeStrictJSON(existingRaw, &existing); err != nil {
		return fmt.Errorf("decode existing delivery source PostgreSQL manifest: %w", err)
	}
	if existing.Digest != document.Manifest.Digest {
		return fmt.Errorf("%w: snapshot ref already contains a different manifest",
			ErrManifestMismatch)
	}
	return nil
}

func decodePostgresDocument(
	schemaVersion string,
	manifestRaw []byte,
	ordersRaw []byte,
	depotsRaw []byte,
	fleetRaw []byte,
	driversRaw []byte,
	travelRaw []byte,
	chargersRaw []byte,
	policyRaw []byte,
) (JSONDocument, error) {
	document := JSONDocument{SchemaVersion: schemaVersion}
	values := []struct {
		name        string
		raw         []byte
		destination any
	}{
		{"manifest", manifestRaw, &document.Manifest},
		{"orders", ordersRaw, &document.Orders},
		{"depots", depotsRaw, &document.Depots},
		{"fleet", fleetRaw, &document.Fleet},
		{"drivers", driversRaw, &document.Drivers},
		{"travel", travelRaw, &document.Travel},
		{"chargers", chargersRaw, &document.Chargers},
		{"policy", policyRaw, &document.Policy},
	}
	for _, value := range values {
		if err := decodeStrictJSON(value.raw, value.destination); err != nil {
			return JSONDocument{}, fmt.Errorf(
				"decode delivery source PostgreSQL %s: %w",
				value.name,
				err,
			)
		}
	}
	if err := validateJSONDocument(document); err != nil {
		return JSONDocument{}, err
	}
	return document, nil
}
