package source

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostgresProviderUsesReadOnlyRepeatableReadSnapshot(t *testing.T) {
	document := sourceTestDocument(t)
	tx := &fakePostgresTx{row: postgresDocumentRow(t, document)}
	var (
		gotOptions pgx.TxOptions
		beginCalls atomic.Int64
	)
	provider := &PostgresProvider{
		begin: func(
			ctx context.Context,
			options pgx.TxOptions,
		) (postgresTx, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			beginCalls.Add(1)
			gotOptions = options
			return tx, nil
		},
	}
	snapshot, err := provider.OpenSnapshot(
		context.Background(),
		document.Manifest.TenantID,
		document.Manifest.Ref,
	)
	if err != nil {
		t.Fatal(err)
	}
	if gotOptions.IsoLevel != pgx.RepeatableRead ||
		gotOptions.AccessMode != pgx.ReadOnly {
		t.Fatalf("transaction options = %+v", gotOptions)
	}
	if beginCalls.Load() != 1 {
		t.Fatalf("begin calls = %d, want 1", beginCalls.Load())
	}
	if len(tx.args) != 2 ||
		tx.args[0] != document.Manifest.TenantID ||
		tx.args[1] != document.Manifest.Ref {
		t.Fatalf("query args = %#v", tx.args)
	}
	if !strings.Contains(tx.query, "tenant_id = $1") ||
		!strings.Contains(tx.query, "snapshot_ref = $2") {
		t.Fatalf("query does not bind tenant and snapshot ref: %s", tx.query)
	}

	fleet, _, err := snapshot.ReadFleet(
		context.Background(),
		revisionFor(document.Manifest, KindFleet),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet.Vehicles) != 1 || fleet.Vehicles[0].ID != "vehicle-1" {
		t.Fatalf("fleet = %+v", fleet)
	}
	if err := snapshot.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tx.commitCount.Load() != 1 || tx.rollbackCount.Load() != 0 {
		t.Fatalf("commit/rollback = %d/%d, want 1/0",
			tx.commitCount.Load(), tx.rollbackCount.Load())
	}
}

func TestPostgresProviderRollsBackMissingOrInvalidRows(t *testing.T) {
	document := sourceTestDocument(t)
	tests := []struct {
		name string
		row  pgx.Row
		want error
	}{
		{
			name: "missing",
			row:  fakePostgresRow{err: pgx.ErrNoRows},
			want: ErrNotFound,
		},
		{
			name: "tampered content",
			row: func() pgx.Row {
				document.Fleet.Data.Vehicles[0].FixedCostCents++
				return postgresDocumentRow(t, document)
			}(),
			want: ErrSourceMismatch,
		},
		{
			name: "malformed JSON",
			row: func() pgx.Row {
				row := postgresDocumentRow(t, sourceTestDocument(t))
				row.values[1] = []byte(`{"unknown":true}`)
				return row
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakePostgresTx{row: test.row}
			provider := &PostgresProvider{
				begin: func(
					context.Context,
					pgx.TxOptions,
				) (postgresTx, error) {
					return tx, nil
				},
			}
			_, err := provider.OpenSnapshot(
				context.Background(),
				document.Manifest.TenantID,
				document.Manifest.Ref,
			)
			if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("OpenSnapshot error = %v, want %v", err, test.want)
				}
			} else if err == nil {
				t.Fatal("OpenSnapshot accepted malformed PostgreSQL JSON")
			}
			if tx.rollbackCount.Load() != 1 || tx.commitCount.Load() != 0 {
				t.Fatalf("commit/rollback = %d/%d, want 0/1",
					tx.commitCount.Load(), tx.rollbackCount.Load())
			}
		})
	}
}

func TestPostgresSnapshotRollsBackWhenCommitFails(t *testing.T) {
	document := sourceTestDocument(t)
	tx := &fakePostgresTx{
		row:       postgresDocumentRow(t, document),
		commitErr: errors.New("commit failed"),
	}
	provider := &PostgresProvider{
		begin: func(context.Context, pgx.TxOptions) (postgresTx, error) {
			return tx, nil
		},
	}
	snapshot, err := provider.OpenSnapshot(
		context.Background(),
		document.Manifest.TenantID,
		document.Manifest.Ref,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(context.Background()); err == nil {
		t.Fatal("Close succeeded after commit failure")
	}
	if tx.commitCount.Load() != 1 || tx.rollbackCount.Load() != 1 {
		t.Fatalf("commit/rollback = %d/%d, want 1/1",
			tx.commitCount.Load(), tx.rollbackCount.Load())
	}
}

type fakePostgresTx struct {
	row           pgx.Row
	query         string
	args          []any
	commitErr     error
	commitCount   atomic.Int64
	rollbackCount atomic.Int64
}

func (tx *fakePostgresTx) QueryRow(
	ctx context.Context,
	query string,
	args ...any,
) pgx.Row {
	if err := ctx.Err(); err != nil {
		return fakePostgresRow{err: err}
	}
	tx.query = query
	tx.args = args
	return tx.row
}

func (tx *fakePostgresTx) Commit(context.Context) error {
	tx.commitCount.Add(1)
	return tx.commitErr
}

func (tx *fakePostgresTx) Rollback(context.Context) error {
	tx.rollbackCount.Add(1)
	return nil
}

type fakePostgresRow struct {
	values []any
	err    error
}

func (row fakePostgresRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != len(row.values) {
		return errors.New("unexpected PostgreSQL scan destination count")
	}
	for index, destination := range destinations {
		switch target := destination.(type) {
		case *string:
			value, ok := row.values[index].(string)
			if !ok {
				return errors.New("unexpected PostgreSQL string value")
			}
			*target = value
		case *[]byte:
			value, ok := row.values[index].([]byte)
			if !ok {
				return errors.New("unexpected PostgreSQL JSON value")
			}
			*target = append((*target)[:0], value...)
		default:
			return errors.New("unexpected PostgreSQL scan destination")
		}
	}
	return nil
}

func postgresDocumentRow(t *testing.T, document JSONDocument) fakePostgresRow {
	t.Helper()
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
	row := fakePostgresRow{
		values: make([]any, 0, len(values)+1),
	}
	row.values = append(row.values, document.SchemaVersion)
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		row.values = append(row.values, raw)
	}
	return row
}

var _ postgresTx = (*fakePostgresTx)(nil)
var _ pgx.Row = fakePostgresRow{}
