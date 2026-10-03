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
	if len(migrations) != 1 || migrations[0].Version != 1 {
		t.Fatalf("migrations = %+v", migrations)
	}
	if migrations[0].Name != "000001_initial.sql" || len(migrations[0].Checksum) != 64 {
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
