package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

const migrationLockID int64 = 0x57415942494c4c

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	Version  int64
	Name     string
	SQL      string
	Checksum string
}

func (db *DB) migrate(ctx context.Context) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	connection, err := db.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		return fmt.Errorf("lock PostgreSQL migrations: %w", err)
	}
	defer func() {
		_, _ = connection.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockID)
	}()

	if _, err := connection.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS waybill;
		CREATE TABLE IF NOT EXISTS waybill.schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			checksum text NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`, pgx.QueryExecModeSimpleProtocol); err != nil {
		return fmt.Errorf("bootstrap PostgreSQL migrations: %w", err)
	}
	rows, err := connection.Query(ctx, `
		SELECT version, name, checksum
		FROM waybill.schema_migrations
		ORDER BY version
	`)
	if err != nil {
		return fmt.Errorf("read PostgreSQL migrations: %w", err)
	}
	applied := make(map[int64]migration)
	for rows.Next() {
		var value migration
		if err := rows.Scan(&value.Version, &value.Name, &value.Checksum); err != nil {
			rows.Close()
			return fmt.Errorf("scan PostgreSQL migration: %w", err)
		}
		applied[value.Version] = value
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read PostgreSQL migrations: %w", err)
	}
	rows.Close()

	known := make(map[int64]migration, len(migrations))
	for _, value := range migrations {
		known[value.Version] = value
	}
	for version, value := range applied {
		expected, ok := known[version]
		if !ok {
			return fmt.Errorf("database has unknown migration version %d", version)
		}
		if value.Name != expected.Name || value.Checksum != expected.Checksum {
			return fmt.Errorf("migration %d checksum does not match embedded schema", version)
		}
	}
	for _, value := range migrations {
		if _, ok := applied[value.Version]; ok {
			continue
		}
		tx, err := connection.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", value.Version, err)
		}
		if _, err := tx.Exec(ctx, value.SQL, pgx.QueryExecModeSimpleProtocol); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %d: %w", value.Version, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO waybill.schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)
		`, value.Version, value.Name, value.Checksum); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %d: %w", value.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %d: %w", value.Version, err)
		}
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	result := make([]migration, 0, len(entries))
	versions := make(map[int64]struct{}, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 || len(parts[0]) != 6 {
			return nil, fmt.Errorf("invalid migration name %q", entry.Name())
		}
		version, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		if _, exists := versions[version]; exists {
			return nil, fmt.Errorf("duplicate migration version %d", version)
		}
		versions[version] = struct{}{}
		raw, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(raw)
		result = append(result, migration{
			Version:  version,
			Name:     entry.Name(),
			SQL:      string(raw),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Version < result[j].Version
	})
	if len(result) == 0 {
		return nil, fmt.Errorf("no embedded PostgreSQL migrations")
	}
	for index, value := range result {
		if value.Version != int64(index+1) {
			return nil, fmt.Errorf("migration versions must be contiguous from 1")
		}
	}
	return result, nil
}
