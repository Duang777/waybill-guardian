//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMigrationsAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	const workers = 2
	databases := make([]*DB, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer wait.Done()
			databases[index], errs[index] = Open(ctx, Config{DatabaseURL: databaseURL})
		}(index)
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Open worker %d: %v", index, err)
		}
		defer databases[index].Close()
	}
	db := databases[0]

	health, err := db.Check(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if health.SchemaVersion != 1 {
		t.Fatalf("schema version = %d, want 1", health.SchemaVersion)
	}
	var tableCount int
	if err := db.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = 'waybill'
		  AND table_name <> 'schema_migrations'
	`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 11 {
		t.Fatalf("business table count = %d, want 11", tableCount)
	}
	var migrationCount int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM waybill.schema_migrations`,
	).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 1 {
		t.Fatalf("migration rows = %d, want 1", migrationCount)
	}

	insert := func() error {
		_, err := db.pool.Exec(ctx, `
			INSERT INTO waybill.inbox_events (
				tenant_id, source, event_id, event_type, payload, payload_hash
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		`, "tenant-test", "urn:test", "event-1", "test.event.v1", `{}`, strings.Repeat("0", 64))
		return MapError(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			results <- insert()
		}()
	}
	close(start)

	successes := 0
	conflicts := 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrUniqueConflict) {
			t.Fatalf("concurrent inbox insert = %v, want ErrUniqueConflict", err)
		}
		var conflict *UniqueConflictError
		if !errors.As(err, &conflict) || conflict.Constraint != "inbox_events_pk" {
			t.Fatalf("concurrent inbox conflict = %+v", conflict)
		}
		conflicts++
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent inbox inserts: successes = %d, conflicts = %d", successes, conflicts)
	}
}
