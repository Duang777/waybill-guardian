package postgres

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestLoadMigrations(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 10 ||
		migrations[0].Version != 1 ||
		migrations[1].Version != 2 ||
		migrations[2].Version != 3 ||
		migrations[3].Version != 4 ||
		migrations[4].Version != 5 ||
		migrations[5].Version != 6 ||
		migrations[6].Version != 7 ||
		migrations[7].Version != 8 ||
		migrations[8].Version != 9 ||
		migrations[9].Version != 10 {
		t.Fatalf("migrations = %+v", migrations)
	}
	if migrations[0].Name != "000001_initial.sql" ||
		migrations[1].Name != "000002_repository_runtime.sql" ||
		migrations[2].Name != "000003_outbox_reclaim.sql" ||
		migrations[3].Name != "000004_history_governance.sql" ||
		migrations[4].Name != "000005_event_ingestion.sql" ||
		migrations[5].Name != "000006_real_write_effects.sql" ||
		migrations[6].Name != "000007_model_proposals.sql" ||
		migrations[7].Name != "000008_delivery_source_snapshots.sql" ||
		migrations[8].Name != "000009_delivery_core.sql" ||
		migrations[9].Name != "000010_delivery_events.sql" ||
		len(migrations[0].Checksum) != 64 ||
		len(migrations[1].Checksum) != 64 ||
		len(migrations[2].Checksum) != 64 ||
		len(migrations[3].Checksum) != 64 ||
		len(migrations[4].Checksum) != 64 ||
		len(migrations[5].Checksum) != 64 ||
		len(migrations[6].Checksum) != 64 ||
		len(migrations[7].Checksum) != 64 ||
		len(migrations[8].Checksum) != 64 ||
		len(migrations[9].Checksum) != 64 {
		t.Fatalf("migration metadata = %+v", migrations[0])
	}
}

func TestOpenRequiresDatabaseURL(t *testing.T) {
	if _, err := Open(t.Context(), Config{}); !errors.Is(err, ErrDatabaseURLRequired) {
		t.Fatalf("Open error = %v, want ErrDatabaseURLRequired", err)
	}
}

func TestOpenRejectsInvalidPoolConfiguration(t *testing.T) {
	tests := []Config{
		{DatabaseURL: "postgres://localhost/waybill", MaxConns: -1},
		{DatabaseURL: "postgres://localhost/waybill", MaxConns: 1, MinConns: -1},
		{DatabaseURL: "postgres://localhost/waybill", MaxConns: 1, MinConns: 2},
		{DatabaseURL: "postgres://localhost/waybill", StartupTimeout: -1},
	}
	for _, config := range tests {
		if _, err := Open(t.Context(), config); err == nil {
			t.Fatalf("Open accepted invalid config %+v", config)
		}
	}
}

func TestMapErrorRecognizesUniqueViolation(t *testing.T) {
	cause := &pgconn.PgError{
		Code:           "23505",
		ConstraintName: "effects_idempotency_key_uq",
	}
	err := MapError(cause)
	if !errors.Is(err, ErrUniqueConflict) {
		t.Fatalf("mapped error = %v", err)
	}
	var conflict *UniqueConflictError
	if !errors.As(err, &conflict) || conflict.Constraint != cause.ConstraintName {
		t.Fatalf("conflict = %+v", conflict)
	}
	other := errors.New("network failure")
	if MapError(other) != other {
		t.Fatal("non-PostgreSQL error was replaced")
	}
}
