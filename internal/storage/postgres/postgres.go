package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const minimumServerVersion = 170000

var (
	ErrDatabaseURLRequired = errors.New("DATABASE_URL is required for PostgreSQL storage")
	ErrReadOnlyDatabase    = errors.New("PostgreSQL must be writable")
	ErrUnsupportedVersion  = errors.New("PostgreSQL 17 or newer is required")
)

type Config struct {
	DatabaseURL    string
	MaxConns       int32
	MinConns       int32
	StartupTimeout time.Duration
}

type Health struct {
	ServerVersion int
	SchemaVersion int64
}

type DB struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, config Config) (*DB, error) {
	if config.DatabaseURL == "" {
		return nil, ErrDatabaseURLRequired
	}
	if config.MaxConns == 0 {
		config.MaxConns = 8
	}
	if config.StartupTimeout == 0 {
		config.StartupTimeout = 30 * time.Second
	}
	if config.MaxConns < 1 || config.MinConns < 0 || config.MinConns > config.MaxConns {
		return nil, fmt.Errorf("invalid PostgreSQL pool limits")
	}
	if config.StartupTimeout <= 0 {
		return nil, fmt.Errorf("PostgreSQL startup timeout must be positive")
	}

	poolConfig, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration")
	}
	poolConfig.MaxConns = config.MaxConns
	poolConfig.MinConns = config.MinConns
	poolConfig.ConnConfig.ConnectTimeout = config.StartupTimeout
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "waybill-guardian"
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"

	startupCtx, cancel := context.WithTimeout(ctx, config.StartupTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(startupCtx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL pool: %w", err)
	}
	db := &DB{pool: pool}
	if err := db.checkServer(startupCtx); err != nil {
		db.Close()
		return nil, err
	}
	if err := db.migrate(startupCtx); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Check(startupCtx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (db *DB) Check(ctx context.Context) (Health, error) {
	var health Health
	var readOnly bool
	err := db.pool.QueryRow(ctx, `
		SELECT current_setting('server_version_num')::integer,
		       current_setting('transaction_read_only')::boolean
	`).Scan(&health.ServerVersion, &readOnly)
	if err != nil {
		return Health{}, fmt.Errorf("check PostgreSQL server: %w", err)
	}
	if health.ServerVersion < minimumServerVersion {
		return Health{}, ErrUnsupportedVersion
	}
	if readOnly {
		return Health{}, ErrReadOnlyDatabase
	}
	if err := db.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM waybill.schema_migrations`,
	).Scan(&health.SchemaVersion); err != nil {
		return Health{}, fmt.Errorf("check PostgreSQL schema version: %w", err)
	}
	return health, nil
}

func (db *DB) Close() {
	if db != nil && db.pool != nil {
		db.pool.Close()
	}
}

func (db *DB) checkServer(ctx context.Context) error {
	if err := db.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping PostgreSQL: %w", err)
	}
	var version int
	var readOnly bool
	if err := db.pool.QueryRow(ctx, `
		SELECT current_setting('server_version_num')::integer,
		       current_setting('transaction_read_only')::boolean
	`).Scan(&version, &readOnly); err != nil {
		return fmt.Errorf("inspect PostgreSQL server: %w", err)
	}
	if version < minimumServerVersion {
		return ErrUnsupportedVersion
	}
	if readOnly {
		return ErrReadOnlyDatabase
	}
	return nil
}
