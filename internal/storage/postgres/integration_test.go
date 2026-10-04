//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	eventmodel "github.com/Duang777/waybill-guardian/internal/events"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	outboxmodel "github.com/Duang777/waybill-guardian/internal/outbox"
	"github.com/Duang777/waybill-guardian/internal/outboxhttp"
	"github.com/Duang777/waybill-guardian/internal/platform"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/google/uuid"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
	"github.com/jackc/pgx/v5"
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
	if health.SchemaVersion != 6 {
		t.Fatalf("schema version = %d, want 6", health.SchemaVersion)
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
	if tableCount != 14 {
		t.Fatalf("business table count = %d, want 14", tableCount)
	}
	var migrationCount int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM waybill.schema_migrations`,
	).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 6 {
		t.Fatalf("migration rows = %d, want 6", migrationCount)
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

func TestIngestEventConcurrentDuplicates(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	first := newIntegrationRepository(t, db, tenantID, "worker-1")
	second := newIntegrationRepository(t, db, tenantID, "worker-2")
	defer first.Close()
	defer second.Close()
	submission := decodeIntegrationDetection(t, "event-duplicate", "incident-duplicate", 1, 360)

	const workers = 10
	start := make(chan struct{})
	results := make(chan eventmodel.Result, workers)
	errs := make(chan error, workers)
	for index := 0; index < workers; index++ {
		repository := first
		if index%2 == 1 {
			repository = second
		}
		go func() {
			<-start
			result, err := repository.IngestEvent(t.Context(), submission)
			results <- result
			errs <- err
		}()
	}
	close(start)

	replayed := 0
	var want eventmodel.Result
	for index := 0; index < workers; index++ {
		if err := <-errs; err != nil {
			t.Fatalf("IngestEvent: %v", err)
		}
		result := <-results
		if index == 0 {
			want = result
		} else if result.EventID != want.EventID ||
			result.IncidentID != want.IncidentID ||
			result.IncidentVersion != want.IncidentVersion ||
			result.Disposition != want.Disposition {
			t.Fatalf("result %d = %+v, want %+v", index, result, want)
		}
		if result.Replayed {
			replayed++
		}
	}
	if replayed != workers-1 {
		t.Fatalf("replayed results = %d, want %d", replayed, workers-1)
	}
	if want.IncidentVersion != 1 || want.Disposition != eventmodel.DispositionApplied {
		t.Fatalf("first result = %+v", want)
	}

	var inboxCount, incidentCount, outboxCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM waybill.inbox_events
			 WHERE tenant_id = $1 AND event_id = 'event-duplicate'),
			(SELECT count(*) FROM waybill.incidents
			 WHERE tenant_id = $1 AND source_incident_key = 'incident-duplicate'),
			(SELECT count(*) FROM waybill.outbox_events
			 WHERE tenant_id = $1 AND aggregate_type = 'incident')
	`, tenantID).Scan(&inboxCount, &incidentCount, &outboxCount); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 1 || incidentCount != 1 || outboxCount != 1 {
		t.Fatalf(
			"durable counts = inbox:%d incident:%d outbox:%d",
			inboxCount,
			incidentCount,
			outboxCount,
		)
	}
}

func TestIngestEventReplaysStoredResultAndRejectsCollision(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	original := decodeIntegrationDetection(t, "event-replay", "incident-replay", 1, 360)
	first, err := repository.IngestEvent(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.IngestEvent(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed {
		t.Fatal("duplicate event did not report replay")
	}
	firstJSON, err := first.ResponseJSON()
	if err != nil {
		t.Fatal(err)
	}
	replayedJSON, err := replayed.ResponseJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, replayedJSON) {
		t.Fatalf("replayed result = %s, want %s", replayedJSON, firstJSON)
	}

	collision := decodeIntegrationDetection(t, "event-replay", "incident-replay", 1, 180)
	if _, err := repository.IngestEvent(t.Context(), collision); !errors.Is(
		err,
		eventmodel.ErrEventIdentityConflict,
	) {
		t.Fatalf("identity collision error = %v", err)
	}
}

func TestIngestEventRejectsIncidentWaybillRebinding(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	original := decodeIntegrationDetection(
		t,
		"event-original-waybill",
		"incident-bound-waybill",
		1,
		360,
	)
	first, err := repository.IngestEvent(t.Context(), original)
	if err != nil {
		t.Fatal(err)
	}
	initialHash, initialState, initialVersion := readIncidentProjection(
		t,
		db,
		tenantID,
		"incident-bound-waybill",
	)

	rebound := decodeIntegrationDetectionForWaybill(
		t,
		"event-rebound-waybill",
		"incident-bound-waybill",
		2,
		180,
		"YD2026101002",
	)
	if _, err := repository.IngestEvent(t.Context(), rebound); !errors.Is(
		err,
		eventmodel.ErrIncidentIdentityConflict,
	) {
		t.Fatalf("IngestEvent error = %v, want ErrIncidentIdentityConflict", err)
	}

	currentHash, currentState, currentVersion := readIncidentProjection(
		t,
		db,
		tenantID,
		"incident-bound-waybill",
	)
	if currentHash != initialHash ||
		currentState != initialState ||
		currentVersion != initialVersion {
		t.Fatalf(
			"incident changed after rejected rebinding: before=%s/%s/%d after=%s/%s/%d",
			initialHash,
			initialState,
			initialVersion,
			currentHash,
			currentState,
			currentVersion,
		)
	}

	var (
		storedWaybill domain.WaybillID
		inboxCount    int
		incidentCount int
		outboxCount   int
	)
	if err := db.pool.QueryRow(t.Context(), `
		SELECT
			(SELECT waybill_id FROM waybill.incidents
			 WHERE tenant_id = $1 AND incident_id = $2),
			(SELECT count(*) FROM waybill.inbox_events
			 WHERE tenant_id = $1 AND event_id = 'event-rebound-waybill'),
			(SELECT count(*) FROM waybill.incidents
			 WHERE tenant_id = $1 AND source_incident_key = 'incident-bound-waybill'),
			(SELECT count(*) FROM waybill.outbox_events
			 WHERE tenant_id = $1 AND aggregate_type = 'incident')
	`, tenantID, first.IncidentID).Scan(
		&storedWaybill,
		&inboxCount,
		&incidentCount,
		&outboxCount,
	); err != nil {
		t.Fatal(err)
	}
	if storedWaybill != "YD2026101001" ||
		inboxCount != 0 ||
		incidentCount != 1 ||
		outboxCount != 1 {
		t.Fatalf(
			"durable state after rejected rebinding = waybill:%q inbox:%d incident:%d outbox:%d",
			storedWaybill,
			inboxCount,
			incidentCount,
			outboxCount,
		)
	}
}

func TestIngestEventCorrectionBeforeTargetConverges(t *testing.T) {
	db := openIntegrationDB(t)
	firstTenant := "tenant-" + uuid.NewString()
	secondTenant := "tenant-" + uuid.NewString()
	first := newIntegrationRepository(t, db, firstTenant, "worker-1")
	second := newIntegrationRepository(t, db, secondTenant, "worker-2")
	defer first.Close()
	defer second.Close()

	target := decodeIntegrationDetection(t, "event-target", "incident-correction", 1, 360)
	correction := decodeIntegrationCorrection(
		t,
		"event-correction",
		"incident-correction",
		2,
		"event-target",
		180,
	)
	pending, err := first.IngestEvent(t.Context(), correction)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Disposition != eventmodel.DispositionCorrectionPending {
		t.Fatalf("correction-first result = %+v", pending)
	}
	if _, err := first.IngestEvent(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := second.IngestEvent(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := second.IngestEvent(t.Context(), correction); err != nil {
		t.Fatal(err)
	}

	firstHash, firstState, firstVersion := readIncidentProjection(
		t,
		db,
		firstTenant,
		"incident-correction",
	)
	secondHash, secondState, secondVersion := readIncidentProjection(
		t,
		db,
		secondTenant,
		"incident-correction",
	)
	if firstHash != secondHash ||
		firstState != string(eventmodel.TransportActive) ||
		secondState != string(eventmodel.TransportActive) ||
		firstVersion != secondVersion {
		t.Fatalf(
			"projections differ: first=%s/%s/%d second=%s/%s/%d",
			firstHash,
			firstState,
			firstVersion,
			secondHash,
			secondState,
			secondVersion,
		)
	}
}

func TestIngestEventReprojectsCorrectionWhenTargetBelongsToAnotherEpisode(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	correction := decodeIntegrationCorrection(
		t,
		"event-correction-other",
		"incident-a",
		2,
		"event-target-other",
		180,
	)
	if _, err := repository.IngestEvent(t.Context(), correction); err != nil {
		t.Fatal(err)
	}
	target := decodeIntegrationDetection(t, "event-target-other", "incident-b", 1, 360)
	if _, err := repository.IngestEvent(t.Context(), target); err != nil {
		t.Fatal(err)
	}

	_, state, _ := readIncidentProjection(t, db, tenantID, "incident-a")
	if state != string(eventmodel.TransportConflicted) {
		t.Fatalf("correction incident state = %q, want conflicted", state)
	}
	_, state, _ = readIncidentProjection(t, db, tenantID, "incident-b")
	if state != string(eventmodel.TransportActive) {
		t.Fatalf("target incident state = %q, want active", state)
	}
}

func TestIngestEventStaleEvidenceDoesNotAdvanceIncident(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	newer := decodeIntegrationDetection(t, "event-newer", "incident-stale", 2, 180)
	if _, err := repository.IngestEvent(t.Context(), newer); err != nil {
		t.Fatal(err)
	}
	older := decodeIntegrationDetection(t, "event-older", "incident-stale", 1, 360)
	result, err := repository.IngestEvent(t.Context(), older)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != eventmodel.DispositionStale || result.IncidentVersion != 1 {
		t.Fatalf("stale result = %+v", result)
	}
	var outboxCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM waybill.outbox_events
		WHERE tenant_id = $1 AND aggregate_type = 'incident'
	`, tenantID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if outboxCount != 1 {
		t.Fatalf("incident outbox count = %d, want 1", outboxCount)
	}
}

func TestIngestEventOutboxConflictRollsBackEverything(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()
	submission := decodeIntegrationDetection(t, "event-rollback", "incident-rollback", 1, 360)
	id := incidentID(tenantID, submission.Source(), submission.Record().IncidentKey)
	outboxID := incidentOutboxID(tenantID, id, 1)
	if _, err := db.pool.Exec(t.Context(), `
		INSERT INTO waybill.outbox_events (
			tenant_id, source, event_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, subject, payload, event_time,
			data_schema, payload_canonical
		) VALUES (
			$1, $2, $3, 'incident', 'conflicting-incident',
			99, 'test', 'incident/conflict', '{}'::jsonb, clock_timestamp(),
			'urn:waybill-guardian:schema:incident-snapshot:v1',
			convert_to('{}', 'UTF8')
		)
	`, tenantID, incidentOutboxSource, outboxID); err != nil {
		t.Fatal(err)
	}

	if _, err := repository.IngestEvent(t.Context(), submission); !errors.Is(
		err,
		ErrUniqueConflict,
	) {
		t.Fatalf("IngestEvent error = %v, want ErrUniqueConflict", err)
	}
	var inboxCount, incidentCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM waybill.inbox_events
			 WHERE tenant_id = $1 AND event_id = 'event-rollback'),
			(SELECT count(*) FROM waybill.incidents
			 WHERE tenant_id = $1 AND source_incident_key = 'incident-rollback')
	`, tenantID).Scan(&inboxCount, &incidentCount); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 0 || incidentCount != 0 {
		t.Fatalf("rolled back counts = inbox:%d incident:%d", inboxCount, incidentCount)
	}
}

func TestHistoryGovernanceMigrationQuarantinesLegacyActiveRun(t *testing.T) {
	db := openIntegrationDB(t)
	resetDatabaseToMigration(t, db, 3)
	tenantID := "legacy-" + uuid.NewString()
	if _, err := db.pool.Exec(t.Context(), `
		INSERT INTO waybill.incidents (
			tenant_id, incident_id, source, source_incident_key,
			waybill_id, kind, status
		) VALUES ($1, 'incident-legacy', 'test', 'legacy-key',
		          'YD2026101001', 'delay', 'awaiting_approval');
		INSERT INTO waybill.runs (
			tenant_id, run_id, incident_id, waybill_id, status,
			sdk_run_id, checkpoint_version
		) VALUES ($1, 'run-legacy', 'incident-legacy', 'YD2026101001',
		          'awaiting_approval', 'sdk-run-legacy', 1);
		INSERT INTO waybill.agent_checkpoints (
			tenant_id, namespace, thread_id, sdk_run_id, checkpoint_version,
			conversation_id, ciphertext, payload_hash, encryption_key_id,
			group_id, metadata
		) VALUES (
			$1, 'waybill-demo', 'run-legacy', 'sdk-run-legacy', 1,
			'conversation-legacy', decode('01', 'hex'), repeat('0', 64),
			'legacy-key', 'default', '{"phone":"13800138000"}'::jsonb
		);
		INSERT INTO waybill.agent_summaries (
			tenant_id, namespace, thread_id, summary_id, payload, payload_hash
		) VALUES (
			$1, 'waybill-demo', 'run-legacy', 'summary-legacy',
			decode('01', 'hex'), repeat('0', 64)
		);
		INSERT INTO waybill.incidents (
			tenant_id, incident_id, source, source_incident_key,
			waybill_id, kind, status
		) VALUES ($1, 'incident-terminal', 'test', 'terminal-key',
		          'YD2026101001', 'delay', 'resolved');
		INSERT INTO waybill.runs (
			tenant_id, run_id, incident_id, waybill_id, status,
			sdk_run_id, checkpoint_version, closed_at
		) VALUES ($1, 'run-terminal', 'incident-terminal', 'YD2026101001',
		          'completed', 'sdk-run-terminal', 1, clock_timestamp());
		INSERT INTO waybill.agent_checkpoints (
			tenant_id, namespace, thread_id, sdk_run_id, checkpoint_version,
			conversation_id, ciphertext, payload_hash, encryption_key_id,
			group_id, metadata
		) VALUES (
			$1, 'waybill-demo', 'run-terminal', 'sdk-run-terminal', 1,
			'conversation-terminal', decode('01', 'hex'), repeat('0', 64),
			'legacy-key', 'default', '{}'::jsonb
		)
	`, pgx.QueryExecModeSimpleProtocol, tenantID); err != nil {
		t.Fatal(err)
	}

	if err := db.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var (
		runStatus      string
		incidentStatus string
		sdkRunID       *string
		checkpoint     int64
		closedAt       *time.Time
	)
	if err := db.pool.QueryRow(t.Context(), `
		SELECT run.status, incident.status, run.sdk_run_id,
		       run.checkpoint_version, run.closed_at
		FROM waybill.runs run
		JOIN waybill.incidents incident
		  ON incident.tenant_id = run.tenant_id
		 AND incident.incident_id = run.incident_id
		WHERE run.tenant_id = $1 AND run.run_id = 'run-legacy'
	`, tenantID).Scan(
		&runStatus,
		&incidentStatus,
		&sdkRunID,
		&checkpoint,
		&closedAt,
	); err != nil {
		t.Fatal(err)
	}
	if runStatus != "manual_review" ||
		incidentStatus != "manual_review" ||
		sdkRunID != nil ||
		checkpoint != 0 ||
		closedAt == nil {
		t.Fatalf(
			"migrated run = status:%s incident:%s sdk:%v checkpoint:%d closed:%v",
			runStatus,
			incidentStatus,
			sdkRunID,
			checkpoint,
			closedAt,
		)
	}
	var quarantineCount, checkpointCount, summaryCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM waybill.run_quarantines
			  WHERE tenant_id = $1 AND run_id = 'run-legacy'),
			(SELECT count(*) FROM waybill.agent_checkpoints WHERE tenant_id = $1),
			(SELECT count(*) FROM waybill.agent_summaries WHERE tenant_id = $1)
	`, tenantID).Scan(&quarantineCount, &checkpointCount, &summaryCount); err != nil {
		t.Fatal(err)
	}
	if quarantineCount != 1 || checkpointCount != 0 || summaryCount != 0 {
		t.Fatalf(
			"migration rows = quarantine:%d checkpoints:%d summaries:%d",
			quarantineCount,
			checkpointCount,
			summaryCount,
		)
	}
	var metadataColumns, privacyColumns int
	var terminalSDKRunID *string
	var terminalCheckpoint int64
	if err := db.pool.QueryRow(t.Context(), `
		SELECT sdk_run_id, checkpoint_version
		FROM waybill.runs
		WHERE tenant_id = $1 AND run_id = 'run-terminal'
	`, tenantID).Scan(&terminalSDKRunID, &terminalCheckpoint); err != nil {
		t.Fatal(err)
	}
	if terminalSDKRunID != nil || terminalCheckpoint != 0 {
		t.Fatalf(
			"terminal run history pointer = sdk:%v checkpoint:%d, want nil and 0",
			terminalSDKRunID,
			terminalCheckpoint,
		)
	}
	if err := db.pool.QueryRow(t.Context(), `
		SELECT
			count(*) FILTER (WHERE column_name = 'metadata'),
			count(*) FILTER (WHERE column_name = 'privacy_schema_version')
		FROM information_schema.columns
		WHERE table_schema = 'waybill'
		  AND table_name IN ('agent_checkpoints', 'agent_summaries')
	`).Scan(&metadataColumns, &privacyColumns); err != nil {
		t.Fatal(err)
	}
	if metadataColumns != 0 || privacyColumns != 2 {
		t.Fatalf(
			"history columns = metadata:%d privacy_schema_version:%d",
			metadataColumns,
			privacyColumns,
		)
	}
}

func TestEventIngestionMigrationPreservesHistoricalOutbox(t *testing.T) {
	db := openIntegrationDB(t)
	resetDatabaseToMigration(t, db, 4)
	tenantID := "legacy-outbox-" + uuid.NewString()
	if _, err := db.pool.Exec(t.Context(), `
		INSERT INTO waybill.outbox_events (
			tenant_id, source, event_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, subject, payload
		) VALUES (
			$1, 'urn:waybill-guardian', 'legacy-event', 'run', 'legacy-run',
			1, 'com.waybill.audit.run_started.v1', 'run/legacy-run',
			'{"run_id":"legacy-run"}'::jsonb
		)
	`, tenantID); err != nil {
		t.Fatal(err)
	}

	if err := db.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var (
		status           string
		eventTime        time.Time
		dataSchema       string
		payloadCanonical []byte
	)
	if err := db.pool.QueryRow(t.Context(), `
		SELECT status, event_time, data_schema, payload_canonical
		FROM waybill.outbox_events
		WHERE tenant_id = $1 AND event_id = 'legacy-event'
	`, tenantID).Scan(
		&status,
		&eventTime,
		&dataSchema,
		&payloadCanonical,
	); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("historical outbox status = %q, want pending", status)
	}
	if eventTime.IsZero() ||
		dataSchema != "urn:waybill-guardian:schema:run-audit:v1" ||
		len(payloadCanonical) == 0 {
		t.Fatalf(
			"historical outbox metadata = time:%v schema:%q payload:%q",
			eventTime,
			dataSchema,
			payloadCanonical,
		)
	}

	var definition string
	if err := db.pool.QueryRow(t.Context(), `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE connamespace = 'waybill'::regnamespace
		  AND conname = 'inbox_events_outbox_fk'
	`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(definition, "MATCH FULL") {
		t.Fatalf("nullable inbox outbox foreign key uses MATCH FULL: %s", definition)
	}
}

func TestRepositoryTransactionRollsBackProjectionAuditAndOutbox(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, runID)
	claim, err := repository.ClaimRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	claimedCtx := context.WithValue(t.Context(), runClaimContextKey{}, claim)
	eventID := "force-outbox-conflict"
	if _, err := db.pool.Exec(t.Context(), `
		INSERT INTO waybill.outbox_events (
			tenant_id, source, event_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, payload, event_time,
			data_schema, payload_canonical
		) VALUES (
			$1, 'urn:waybill-guardian', $2, 'run', $3, 2, 'test', '{}'::jsonb,
			clock_timestamp(), 'urn:waybill-guardian:schema:run-audit:v1',
			convert_to('{}', 'UTF8')
		)
	`, tenantID, string(runID)+":"+eventID, runID); err != nil {
		t.Fatal(err)
	}

	_, err = repository.Append(claimedCtx, runID, audit.Draft{
		EventID: eventID,
		Actor:   audit.ActorAgent,
		Type:    audit.EventToolCall,
		Payload: map[string]any{"call_id": "call-1"},
	})
	if !errors.Is(err, ErrUniqueConflict) {
		t.Fatalf("Append error = %v, want ErrUniqueConflict", err)
	}
	var status string
	var lastSeq int64
	if err := db.pool.QueryRow(t.Context(), `
		SELECT status, last_audit_seq
		FROM waybill.runs
		WHERE tenant_id = $1 AND run_id = $2
	`, tenantID, runID).Scan(&status, &lastSeq); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.RunStarted) || lastSeq != 1 {
		t.Fatalf("run after rollback = status %q seq %d", status, lastSeq)
	}
	var auditCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM waybill.audit_events
		WHERE tenant_id = $1 AND run_id = $2
	`, tenantID, runID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count after rollback = %d, want 1", auditCount)
	}
}

func TestRunLeaseFencesStaleWorker(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	first := newIntegrationRepository(t, db, tenantID, "worker-1")
	second := newIntegrationRepository(t, db, tenantID, "worker-2")
	defer first.Close()
	defer second.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, first, runID)
	firstClaim, err := first.ClaimRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.ClaimRun(t.Context(), runID); !errors.Is(err, ErrRunLeaseHeld) {
		t.Fatalf("concurrent ClaimRun error = %v, want ErrRunLeaseHeld", err)
	}
	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.runs
		SET lease_deadline = clock_timestamp() - interval '1 second'
		WHERE tenant_id = $1 AND run_id = $2
	`, tenantID, runID); err != nil {
		t.Fatal(err)
	}
	secondClaim, err := second.ClaimRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if secondClaim.fence <= firstClaim.fence {
		t.Fatalf("second fence = %d, first fence = %d", secondClaim.fence, firstClaim.fence)
	}
	firstCtx := context.WithValue(t.Context(), runClaimContextKey{}, firstClaim)
	if _, err := first.Append(firstCtx, runID, toolCallDraft("stale")); !errors.Is(err, ErrStaleRunClaim) {
		t.Fatalf("stale Append error = %v, want ErrStaleRunClaim", err)
	}
	secondCtx := context.WithValue(t.Context(), runClaimContextKey{}, secondClaim)
	event, err := second.Append(secondCtx, runID, toolCallDraft("current"))
	if err != nil {
		t.Fatal(err)
	}
	if event.Seq != 2 {
		t.Fatalf("current event seq = %d, want 2", event.Seq)
	}
}

func TestRunLeaseReleaseIsConcurrentSafe(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, runID)
	_, release, err := repository.AcquireRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for range callers {
		go func() {
			defer wait.Done()
			results <- release()
		}()
	}
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent release: %v", err)
		}
	}

	var owner *string
	if err := db.pool.QueryRow(t.Context(), `
		SELECT lease_owner
		FROM waybill.runs
		WHERE tenant_id = $1 AND run_id = $2
	`, tenantID, runID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		t.Fatalf("lease owner after concurrent release = %q", *owner)
	}
}

func TestOutboxClaimRenewCompleteAndReclaimAreFenced(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	first := newIntegrationRepository(t, db, tenantID, "worker-1")
	second := newIntegrationRepository(t, db, tenantID, "worker-2")
	defer first.Close()
	defer second.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, first, runID)
	runCtx, release, err := first.AcquireRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Append(runCtx, runID, toolCallDraft("outbox-next")); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	claims, err := first.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("first outbox claims = %d, want 1", len(claims))
	}
	firstClaim := claims[0]
	firstStoredClaim := firstClaim.(*outboxClaim)
	if event := firstClaim.Event(); event.AggregateVersion != 1 || event.Attempt != 1 {
		t.Fatalf("first outbox event = %+v", event)
	}
	claims, err = second.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("concurrent outbox claims = %d, want 0", len(claims))
	}

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.outbox_events
		SET lease_deadline = clock_timestamp() - interval '1 second'
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
	`, tenantID, firstStoredClaim.source, firstStoredClaim.eventID); err != nil {
		t.Fatal(err)
	}
	claims, err = second.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("reclaimed outbox events = %d, want 1", len(claims))
	}
	secondClaim := claims[0]
	secondStoredClaim := secondClaim.(*outboxClaim)
	if event := secondClaim.Event(); event.AggregateVersion != 1 || event.Attempt != 2 {
		t.Fatalf("reclaimed outbox event = %+v", event)
	}
	if secondStoredClaim.fence <= firstStoredClaim.fence {
		t.Fatalf(
			"reclaimed fence = %d, first fence = %d",
			secondStoredClaim.fence,
			firstStoredClaim.fence,
		)
	}
	if err := second.RenewOutbox(t.Context(), secondClaim); err != nil {
		t.Fatalf("renew current outbox claim: %v", err)
	}
	if err := first.RenewOutbox(t.Context(), firstClaim); !errors.Is(err, ErrStaleOutboxClaim) {
		t.Fatalf("stale outbox renewal = %v, want ErrStaleOutboxClaim", err)
	}
	if err := first.CompleteOutbox(t.Context(), firstClaim, outboxmodel.Completion{
		Disposition: outboxmodel.Published,
	}); !errors.Is(err, ErrStaleOutboxClaim) {
		t.Fatalf("stale outbox completion = %v, want ErrStaleOutboxClaim", err)
	}
	if err := second.CompleteOutbox(t.Context(), secondClaim, outboxmodel.Completion{
		Disposition: outboxmodel.Published,
	}); err != nil {
		t.Fatal(err)
	}

	claims, err = first.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Event().AggregateVersion != 2 {
		t.Fatalf("next outbox claim = %+v", claims)
	}
	nextClaim := claims[0]
	nextStoredClaim := nextClaim.(*outboxClaim)
	if err := first.CompleteOutbox(t.Context(), nextClaim, outboxmodel.Completion{
		Disposition: outboxmodel.RetryableFailed,
		RetryAfter:  time.Hour,
		ErrorCode:   "broker_unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	claims, err = second.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Fatalf("outbox retry claimed before availability: %+v", claims)
	}
	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.outbox_events
		SET available_at = clock_timestamp() - interval '1 second'
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
	`, tenantID, nextStoredClaim.source, nextStoredClaim.eventID); err != nil {
		t.Fatal(err)
	}
	claims, err = second.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Event().Attempt != 2 {
		t.Fatalf("retryable outbox claims = %+v", claims)
	}
	if err := second.CompleteOutbox(t.Context(), claims[0], outboxmodel.Completion{
		Disposition: outboxmodel.Published,
	}); err != nil {
		t.Fatal(err)
	}

	var status string
	var publishedAt *time.Time
	var leaseOwner *string
	if err := db.pool.QueryRow(t.Context(), `
		SELECT status, published_at, lease_owner
		FROM waybill.outbox_events
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
	`, tenantID, nextStoredClaim.source, nextStoredClaim.eventID).Scan(
		&status,
		&publishedAt,
		&leaseOwner,
	); err != nil {
		t.Fatal(err)
	}
	if status != string(outboxmodel.Published) || publishedAt == nil || leaseOwner != nil {
		t.Fatalf("completed outbox state = status %q published %v owner %v",
			status, publishedAt, leaseOwner)
	}
}

func TestOutboxPermanentFailureRequiresAuditedExactRequeue(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, runID)
	runCtx, release, err := repository.AcquireRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(runCtx, runID, toolCallDraft("blocked-next")); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}

	claims, err := repository.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 {
		t.Fatalf("initial claims = %d, want 1", len(claims))
	}
	first := claims[0]
	event := first.Event()
	if event.Time.IsZero() ||
		event.DataContentType != "application/json" ||
		event.DataSchema != "urn:waybill-guardian:schema:run-audit:v1" ||
		len(event.Data) == 0 {
		t.Fatalf("claimed event metadata = %+v", event)
	}
	if err := repository.CompleteOutbox(t.Context(), first, outboxmodel.Completion{
		Disposition: outboxmodel.PermanentFailed,
		ErrorCode:   "downstream_rejected",
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := repository.OutboxStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.PermanentFailed != 1 ||
		stats.OldestUnpublishedAt == nil {
		t.Fatalf("outbox stats after permanent failure = %+v", stats)
	}
	blocked, err := repository.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 0 {
		t.Fatalf("claims behind permanent failure = %+v, want none", blocked)
	}
	if err := repository.RequeueOutbox(t.Context(), outboxmodel.RequeueRequest{
		Source:  event.Source,
		EventID: "missing",
		Actor:   "operator@example.com",
		Reason:  "downstream contract repaired",
	}); !errors.Is(err, ErrOutboxNotRequeueable) {
		t.Fatalf("requeue wrong event error = %v, want ErrOutboxNotRequeueable", err)
	}
	if err := repository.RequeueOutbox(t.Context(), outboxmodel.RequeueRequest{
		Source:  event.Source,
		EventID: event.ID,
		Actor:   "operator@example.com",
		Reason:  "downstream contract repaired",
	}); err != nil {
		t.Fatal(err)
	}

	var (
		status       string
		requeueCount int
		requeuedBy   string
		reason       string
	)
	if err := db.pool.QueryRow(t.Context(), `
		SELECT status, requeue_count, last_requeued_by, last_requeue_reason
		FROM waybill.outbox_events
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
	`, tenantID, event.Source, event.ID).Scan(
		&status,
		&requeueCount,
		&requeuedBy,
		&reason,
	); err != nil {
		t.Fatal(err)
	}
	if status != "pending" ||
		requeueCount != 1 ||
		requeuedBy != "operator@example.com" ||
		reason != "downstream contract repaired" {
		t.Fatalf(
			"requeue audit = status:%q count:%d actor:%q reason:%q",
			status,
			requeueCount,
			requeuedBy,
			reason,
		)
	}

	retried, err := repository.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(retried) != 1 ||
		retried[0].Event().ID != event.ID ||
		retried[0].Event().Attempt != 2 {
		t.Fatalf("requeued claims = %+v", retried)
	}
	if err := repository.CompleteOutbox(t.Context(), retried[0], outboxmodel.Completion{
		Disposition: outboxmodel.PermanentFailed,
		ErrorCode:   "still_rejected",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.RequeueOutbox(t.Context(), outboxmodel.RequeueRequest{
		Source:  event.Source,
		EventID: event.ID,
		Actor:   "second-operator@example.com",
		Reason:  "receiver allowlist updated",
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.pool.Query(t.Context(), `
		SELECT requeue_no, failure_code, actor, reason
		FROM waybill.outbox_requeues
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
		ORDER BY requeue_no
	`, tenantID, event.Source, event.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type requeueAudit struct {
		number      int
		failureCode string
		actor       string
		reason      string
	}
	var audits []requeueAudit
	for rows.Next() {
		var audit requeueAudit
		if err := rows.Scan(
			&audit.number,
			&audit.failureCode,
			&audit.actor,
			&audit.reason,
		); err != nil {
			t.Fatal(err)
		}
		audits = append(audits, audit)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 2 ||
		audits[0] != (requeueAudit{
			number:      1,
			failureCode: "downstream_rejected",
			actor:       "operator@example.com",
			reason:      "downstream contract repaired",
		}) ||
		audits[1] != (requeueAudit{
			number:      2,
			failureCode: "still_rejected",
			actor:       "second-operator@example.com",
			reason:      "receiver allowlist updated",
		}) {
		t.Fatalf("requeue audits = %+v", audits)
	}
	finalClaims, err := repository.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(finalClaims) != 1 || finalClaims[0].Event().Attempt != 3 {
		t.Fatalf("second requeue claims = %+v", finalClaims)
	}
}

func TestOutboxRedeliveryKeepsIdentityAndConsumerAppliesOnce(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository, err := NewRepository(db, RepositoryConfig{
		TenantID:       tenantID,
		WorkerID:       "worker-1",
		LeaseTTL:       5 * time.Second,
		OutboxLeaseTTL: 150 * time.Millisecond,
		PollInterval:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, runID)

	consumerConnection, err := db.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer consumerConnection.Release()
	if _, err := consumerConnection.Exec(t.Context(), `
		CREATE TEMP TABLE test_consumer_inbox (
			source text NOT NULL,
			event_id text NOT NULL,
			PRIMARY KEY (source, event_id)
		);
		CREATE TEMP TABLE test_consumer_state (
			singleton boolean PRIMARY KEY DEFAULT true,
			transitions integer NOT NULL
		);
		INSERT INTO test_consumer_state (transitions) VALUES (0);
	`); err != nil {
		t.Fatal(err)
	}

	var consumerMu sync.Mutex
	requestBodies := make([][]byte, 0, 2)
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer consumer-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			http.Error(w, "read body", http.StatusInternalServerError)
			return
		}
		var envelope struct {
			Source string `json:"source"`
			ID     string `json:"id"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			http.Error(w, "decode body", http.StatusBadRequest)
			return
		}
		tx, beginErr := consumerConnection.Begin(r.Context())
		if beginErr != nil {
			http.Error(w, "begin", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback(context.Background())
		tag, insertErr := tx.Exec(r.Context(), `
			INSERT INTO test_consumer_inbox (source, event_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, envelope.Source, envelope.ID)
		if insertErr != nil {
			http.Error(w, "insert", http.StatusInternalServerError)
			return
		}
		if tag.RowsAffected() == 1 {
			if _, updateErr := tx.Exec(r.Context(), `
				UPDATE test_consumer_state
				SET transitions = transitions + 1
				WHERE singleton = true
			`); updateErr != nil {
				http.Error(w, "update", http.StatusInternalServerError)
				return
			}
		}
		if commitErr := tx.Commit(r.Context()); commitErr != nil {
			http.Error(w, "commit", http.StatusInternalServerError)
			return
		}
		consumerMu.Lock()
		requestBodies = append(requestBodies, append([]byte(nil), body...))
		consumerMu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer consumer.Close()

	publisher, err := outboxhttp.New(outboxhttp.Config{
		URL:     consumer.URL,
		Token:   "consumer-token",
		Timeout: time.Second,
		Client:  consumer.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &failFirstCompletionStore{Store: repository}
	dispatcher, err := outboxmodel.NewDispatcher(outboxmodel.DispatcherConfig{
		Store:         store,
		Publisher:     publisher,
		BatchSize:     1,
		Concurrency:   1,
		PollInterval:  10 * time.Millisecond,
		LeaseTTL:      150 * time.Millisecond,
		StatsInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	dispatchDone := make(chan error, 1)
	go func() {
		dispatchDone <- dispatcher.Run(ctx)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		var status string
		if err := db.pool.QueryRow(t.Context(), `
			SELECT status
			FROM waybill.outbox_events
			WHERE tenant_id = $1 AND aggregate_type = 'run' AND aggregate_version = 1
		`, tenantID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "published" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox event was not published after the lost completion")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-dispatchDone; err != nil {
		t.Fatal(err)
	}

	var inboxCount, transitions int
	if err := consumerConnection.QueryRow(t.Context(), `
		SELECT
			(SELECT count(*) FROM test_consumer_inbox),
			(SELECT transitions FROM test_consumer_state WHERE singleton = true)
	`).Scan(&inboxCount, &transitions); err != nil {
		t.Fatal(err)
	}
	consumerMu.Lock()
	defer consumerMu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("consumer request count = %d, want 2", len(requestBodies))
	}
	if !bytes.Equal(requestBodies[0], requestBodies[1]) {
		t.Fatalf("redelivered event changed:\n%s\n%s", requestBodies[0], requestBodies[1])
	}
	if inboxCount != 1 || transitions != 1 {
		t.Fatalf("consumer state = inbox:%d transitions:%d, want 1 and 1", inboxCount, transitions)
	}
}

type failFirstCompletionStore struct {
	outboxmodel.Store
	mu     sync.Mutex
	failed bool
}

func (s *failFirstCompletionStore) CompleteOutbox(
	ctx context.Context,
	claim outboxmodel.Claim,
	result outboxmodel.Completion,
) error {
	s.mu.Lock()
	if !s.failed {
		s.failed = true
		s.mu.Unlock()
		return errors.New("simulated completion loss")
	}
	s.mu.Unlock()
	return s.Store.CompleteOutbox(ctx, claim, result)
}

func TestPrepareRecoveryQuarantinesOnlyDamagedRun(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()

	damagedRunID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, damagedRunID)
	runCtx, release, err := repository.AcquireRun(t.Context(), damagedRunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(runCtx, damagedRunID, toolCallDraft("damaged")); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	healthyRunID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, healthyRunID)

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.audit_events
		SET payload_canonical = convert_to('{"call_id":"tampered"}', 'UTF8')
		WHERE tenant_id = $1 AND run_id = $2 AND seq = 2
	`, tenantID, damagedRunID); err != nil {
		t.Fatal(err)
	}
	if err := repository.PrepareRecovery(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := repository.PrepareRecovery(t.Context()); err != nil {
		t.Fatalf("repeated recovery preparation: %v", err)
	}

	damaged, err := repository.RunProjection(t.Context(), damagedRunID)
	if err != nil {
		t.Fatal(err)
	}
	healthy, err := repository.RunProjection(t.Context(), healthyRunID)
	if err != nil {
		t.Fatal(err)
	}
	if damaged.Status != domain.RunManualReview {
		t.Fatalf("damaged run status = %q, want manual_review", damaged.Status)
	}
	if healthy.Status != domain.RunStarted {
		t.Fatalf("healthy run status = %q, want started", healthy.Status)
	}
	if _, err := repository.Replay(t.Context(), damagedRunID, 0); !errors.Is(err, ErrRunQuarantined) {
		t.Fatalf("damaged run replay = %v, want ErrRunQuarantined", err)
	}
	if _, err := repository.ClaimRun(t.Context(), damagedRunID); !errors.Is(err, ErrRunQuarantined) {
		t.Fatalf("damaged run claim = %v, want ErrRunQuarantined", err)
	}
	if err := repository.Verify(damagedRunID); err == nil {
		t.Fatal("damaged audit chain passed verification")
	}
	events, err := repository.AllEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RunID != healthyRunID {
		t.Fatalf("recoverable audit events = %+v", events)
	}
	healthyEvents, err := repository.Replay(t.Context(), healthyRunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(healthyEvents) != 1 {
		t.Fatalf("healthy run events = %d, want 1", len(healthyEvents))
	}
	outboxClaims, err := repository.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(outboxClaims) != 1 ||
		outboxClaims[0].Event().AggregateID != string(healthyRunID) {
		t.Fatalf("outbox claims after quarantine = %+v", outboxClaims)
	}

	var quarantineCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM waybill.run_quarantines
		WHERE tenant_id = $1 AND run_id = $2
	`, tenantID, damagedRunID).Scan(&quarantineCount); err != nil {
		t.Fatal(err)
	}
	if quarantineCount != 1 {
		t.Fatalf("quarantine rows = %d, want 1", quarantineCount)
	}

	history, err := NewConversationPersistence(context.Background(), db, HistoryConfig{
		TenantID: tenantID,
		KeyID:    "test-key",
		Key:      bytes.Repeat([]byte{0x24}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	clients, _, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := guardian.OpenDurable(guardian.DurableConfig{
		Config:      guardian.Config{Clients: clients},
		Journal:     repository,
		Effects:     repository,
		History:     history,
		Coordinator: repository,
	})
	if err != nil {
		t.Fatalf("open guardian with quarantined run: %v", err)
	}
	defer service.Close()
	if err := service.Recover(t.Context()); err != nil {
		t.Fatalf("recover healthy runs with quarantined peer: %v", err)
	}
	snapshot, err := service.Snapshot(t.Context(), damagedRunID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != domain.RunManualReview || len(snapshot.Events) != 0 {
		t.Fatalf("quarantined run snapshot = %+v", snapshot)
	}
	activeRuns, err := service.ListActiveRuns(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(activeRuns) != 1 ||
		activeRuns[0].RunID != damagedRunID ||
		activeRuns[0].Status != domain.RunManualReview {
		t.Fatalf("active runs after isolated recovery = %+v", activeRuns)
	}
}

func TestTimelineObservesAnotherRepositoryInstance(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	first := newIntegrationRepository(t, db, tenantID, "worker-1")
	second := newIntegrationRepository(t, db, tenantID, "worker-2")
	defer first.Close()
	defer second.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, first, runID)
	streamCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	subscription, err := first.Subscribe(streamCtx, runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	claim, err := second.ClaimRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	claimedCtx := context.WithValue(t.Context(), runClaimContextKey{}, claim)
	if _, err := second.Append(claimedCtx, runID, toolCallDraft("remote")); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-subscription.Events():
		if event.EventID != "tool:remote:call" || event.Seq != 2 {
			t.Fatalf("timeline event = %+v", event)
		}
	case <-streamCtx.Done():
		t.Fatal("timed out waiting for cross-instance timeline event")
	}
}

func TestRepositoryValidatesRecoveryCoverage(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	clients, _, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := guardtools.NewFixtureWriteRuntime(clients)
	if err != nil {
		t.Fatal(err)
	}
	repository := newIntegrationRepository(t, db, tenantID, "worker-coverage", runtime)
	defer repository.Close()

	if err := repository.ValidateRecoveryCoverage(t.Context()); err != nil {
		t.Fatal(err)
	}

	legacyRunID := domain.RunID(uuid.NewString())
	_, legacyCommand, _ := prepareApprovedReassignEffect(
		t,
		repository,
		legacyRunID,
		"CARRIER-SW-42",
	)
	if err := repository.ValidateRecoveryCoverage(t.Context()); err != nil {
		t.Fatalf("supported prepared effect blocked recovery coverage: %v", err)
	}
	restricted := newIntegrationRepository(
		t,
		db,
		tenantID,
		"worker-restricted-coverage",
		&advertisedWriteRuntime{
			WriteRuntime: runtime,
			actions:      []domain.Action{domain.ActionCreateClaim},
		},
	)
	defer restricted.Close()
	if err := restricted.ValidateRecoveryCoverage(t.Context()); !errors.Is(
		err,
		ErrRecoveryCoverageMissing,
	) {
		t.Fatalf("unsupported prepared effect recovery coverage error = %v", err)
	}
	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET status = 'permanent_failed'
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, legacyCommand.Identity.EffectID); err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateRecoveryCoverage(t.Context()); err != nil {
		t.Fatalf("terminal legacy effect blocked recovery coverage: %v", err)
	}

	boundRunID := domain.RunID(uuid.NewString())
	_, boundCommand, boundEffect := prepareApprovedReassignEffect(
		t,
		repository,
		boundRunID,
		"CARRIER-SW-77",
	)
	binding, err := runtime.Bind(
		boundEffect.Request(),
		boundCommand.Identity.Key,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET status = 'unknown',
		    binding_schema_version = $3,
		    adapter_id = $4,
		    provider_contract_version = $5,
		    provider_operation = $6,
		    provider_scope_digest = $7,
		    provider_request_hash = $8,
		    key_created_at = $9,
		    key_expires_at = $10,
		    lookup_consistency_window_ms = $11,
		    dispatch_started_at = clock_timestamp(),
		    retry_after = clock_timestamp()
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, boundCommand.Identity.EffectID, binding.SchemaVersion,
		binding.AdapterID, binding.ContractVersion, binding.ProviderOperation,
		binding.ProviderScopeDigest, binding.ProviderRequestHash,
		binding.KeyCreatedAt, binding.KeyExpiresAt,
		binding.LookupConsistencyWindow.Milliseconds()); err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateRecoveryCoverage(t.Context()); err != nil {
		t.Fatalf("supported recovery binding was rejected: %v", err)
	}

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET provider_contract_version = 'unsupported-v2'
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, boundCommand.Identity.EffectID); err != nil {
		t.Fatal(err)
	}
	if err := repository.ValidateRecoveryCoverage(t.Context()); !errors.Is(
		err,
		ErrRecoveryCoverageMissing,
	) {
		t.Fatalf("unsupported recovery coverage error = %v", err)
	}
}

func TestRepositoryListsOnlyDueEffectRecoveries(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	clients, _, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := guardtools.NewFixtureWriteRuntime(clients)
	if err != nil {
		t.Fatal(err)
	}
	repository := newIntegrationRepository(t, db, tenantID, "worker-due", runtime)
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	_, command, _ := prepareApprovedReassignEffect(
		t,
		repository,
		runID,
		"CARRIER-SW-42",
	)
	assertDue := func(want int) {
		t.Helper()
		commands, err := repository.DueRecoveries(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(commands) != want {
			t.Fatalf("due recoveries = %+v, want %d", commands, want)
		}
		if want == 1 && commands[0] != command {
			t.Fatalf("due recovery = %+v, want %+v", commands[0], command)
		}
	}
	assertDue(1)

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET status = 'dispatching',
		    lease_owner = 'live-worker',
		    lease_deadline = clock_timestamp() + interval '1 hour'
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID); err != nil {
		t.Fatal(err)
	}
	assertDue(0)

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET lease_deadline = clock_timestamp() - interval '1 second'
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID); err != nil {
		t.Fatal(err)
	}
	assertDue(1)

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET status = 'unknown',
		    retry_after = clock_timestamp() + interval '1 hour',
		    lease_owner = NULL,
		    lease_deadline = NULL
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID); err != nil {
		t.Fatal(err)
	}
	assertDue(0)

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET retry_after = clock_timestamp() - interval '1 second'
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID); err != nil {
		t.Fatal(err)
	}
	assertDue(1)

	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET status = 'succeeded', retry_after = NULL
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID); err != nil {
		t.Fatal(err)
	}
	assertDue(0)
}

func TestRepositoryExecutesAndReplaysApprovedEffect(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	clients, mock, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	writeRuntime, err := guardtools.NewFixtureWriteRuntime(clients)
	if err != nil {
		t.Fatal(err)
	}
	repository := newIntegrationRepository(t, db, tenantID, "worker-1", writeRuntime)
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, runID)
	claim, err := repository.ClaimRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	claimedCtx := context.WithValue(t.Context(), runClaimContextKey{}, claim)
	arguments := json.RawMessage(`{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`)
	identity, err := idempotency.Derive(idempotency.DerivationInput{
		RunContext: domain.RunContext{
			RunID:       runID,
			IncidentID:  domain.IncidentID("incident-" + string(runID)),
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionReassign,
		Target:    "waybill/YD2026101001/carrier/CARRIER-SW-42",
		Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	item := approval.Item{
		CallID:          "call-write-1",
		Action:          domain.ActionReassign,
		WireName:        "tms_reassign",
		Params:          arguments,
		ArgumentsHash:   identity.ArgumentsHash,
		IdentityVersion: identity.Version,
		EffectID:        identity.EffectID,
		IdempotencyKey:  identity.Key,
	}
	approvalID := approval.IDFor(runID, []string{item.CallID})
	now := time.Now().UTC()
	if _, err := repository.Append(claimedCtx, runID, audit.Draft{
		EventID: "approval:" + string(approvalID) + ":requested",
		Actor:   audit.ActorAgent,
		Type:    audit.EventApprovalRequested,
		Payload: approval.Approval{
			ID:          approvalID,
			RunID:       runID,
			SDKRunID:    "sdk-run-1",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
			Items:       []approval.Item{item},
			Reason:      "test",
			Evidence:    []approval.Evidence{{Label: "delay", Value: "6h"}},
			Status:      approval.StatusPending,
			RequestedAt: now,
			ExpiresAt:   now.Add(time.Minute),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(claimedCtx, runID, audit.Draft{
		EventID: "approval:" + string(approvalID) + ":confirmed",
		Actor:   audit.ActorHuman,
		Type:    audit.EventApprovalDecided,
		Payload: map[string]any{
			"approval_id": approvalID,
			"status":      approval.StatusConfirmed,
			"decided_by":  "reviewer",
			"decided_at":  now,
		},
	}); err != nil {
		t.Fatal(err)
	}

	command := idempotency.Command{RunID: runID, CallID: item.CallID, Identity: identity}
	effect, err := idempotency.AuthorizeEffect(command, platform.EffectRequest{
		Action:        item.Action,
		Arguments:     item.Params,
		ArgumentsHash: item.ArgumentsHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.Execute(claimedCtx, effect)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Execute(claimedCtx, effect)
	if err != nil {
		t.Fatal(err)
	}
	if mock.WriteCount(domain.ActionReassign) != 1 ||
		first.Duplicate ||
		!second.Duplicate ||
		string(first.Value) != string(second.Value) {
		t.Fatalf(
			"effect replay: calls=%d first=%+v second=%+v",
			mock.WriteCount(domain.ActionReassign),
			first,
			second,
		)
	}
	state, ok := repository.Status(command)
	if !ok || state != idempotency.StateSucceeded {
		t.Fatalf("effect state = %q, %v", state, ok)
	}
	var (
		bindingVersion int
		adapterID      string
		requestHash    string
		dispatchAt     time.Time
		responseDigest string
	)
	if err := db.pool.QueryRow(t.Context(), `
		SELECT binding_schema_version, adapter_id, provider_request_hash,
		       dispatch_started_at, response_digest
		FROM waybill.effects
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, identity.EffectID).Scan(
		&bindingVersion,
		&adapterID,
		&requestHash,
		&dispatchAt,
		&responseDigest,
	); err != nil {
		t.Fatal(err)
	}
	if bindingVersion != 1 ||
		adapterID != guardtools.FixtureRuntimeAdapterID ||
		len(requestHash) != 64 ||
		dispatchAt.IsZero() ||
		len(responseDigest) != 64 {
		t.Fatalf(
			"stored effect metadata = version:%d adapter:%q request:%q dispatch:%v response:%q",
			bindingVersion,
			adapterID,
			requestHash,
			dispatchAt,
			responseDigest,
		)
	}
	if err := repository.Verify(runID); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryPersistsBindingBeforeDispatchAndRenewsEffectLease(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	clients, _, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := guardtools.NewFixtureWriteRuntime(clients)
	if err != nil {
		t.Fatal(err)
	}
	blocking := &blockingWriteRuntime{
		WriteRuntime: fixture,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	repository, err := NewRepository(db, RepositoryConfig{
		TenantID:       tenantID,
		WorkerID:       "worker-blocking",
		LeaseTTL:       5 * time.Second,
		EffectLeaseTTL: 150 * time.Millisecond,
		OutboxLeaseTTL: 5 * time.Second,
		PollInterval:   10 * time.Millisecond,
		WriteRuntime:   blocking,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	claimedCtx, command, effect := prepareApprovedReassignEffect(
		t,
		repository,
		runID,
		"CARRIER-SW-42",
	)
	result := make(chan error, 1)
	go func() {
		_, executeErr := repository.Execute(claimedCtx, effect)
		result <- executeErr
	}()
	select {
	case <-blocking.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch was not entered")
	}

	var (
		status          string
		bindingVersion  int
		adapterID       string
		firstLeaseUntil time.Time
	)
	if err := db.pool.QueryRow(t.Context(), `
		SELECT status, binding_schema_version, adapter_id, lease_deadline
		FROM waybill.effects
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID).Scan(
		&status,
		&bindingVersion,
		&adapterID,
		&firstLeaseUntil,
	); err != nil {
		t.Fatal(err)
	}
	if status != "dispatching" ||
		bindingVersion != 1 ||
		adapterID != guardtools.FixtureRuntimeAdapterID {
		t.Fatalf(
			"pre-dispatch row = status:%q version:%d adapter:%q",
			status,
			bindingVersion,
			adapterID,
		)
	}

	time.Sleep(250 * time.Millisecond)
	var renewedLeaseUntil time.Time
	if err := db.pool.QueryRow(t.Context(), `
		SELECT lease_deadline
		FROM waybill.effects
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID).Scan(&renewedLeaseUntil); err != nil {
		t.Fatal(err)
	}
	if !renewedLeaseUntil.After(firstLeaseUntil) {
		t.Fatalf(
			"effect lease was not renewed: first=%v renewed=%v",
			firstLeaseUntil,
			renewedLeaseUntil,
		)
	}
	close(blocking.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRecoversExpiredDispatchByLookupWithoutMutation(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	clients, _, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := guardtools.NewFixtureWriteRuntime(clients)
	if err != nil {
		t.Fatal(err)
	}
	recoveryRuntime := &recoveryWriteRuntime{WriteRuntime: fixture}
	repository := newIntegrationRepository(
		t,
		db,
		tenantID,
		"worker-recovery",
		recoveryRuntime,
	)
	defer repository.Close()

	runID := domain.RunID(uuid.NewString())
	claimedCtx, command, effect := prepareApprovedReassignEffect(
		t,
		repository,
		runID,
		"CARRIER-SW-42",
	)
	binding, err := recoveryRuntime.Bind(
		effect.Request(),
		command.Identity.Key,
		time.Now().UTC().Add(-time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	dispatchStartedAt := time.Now().UTC().Add(-30 * time.Second)
	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET status = 'dispatching',
		    attempt = 1,
		    binding_schema_version = $4,
		    adapter_id = $5,
		    provider_contract_version = $6,
		    provider_operation = $7,
		    provider_scope_digest = $8,
		    provider_request_hash = $9,
		    key_created_at = $10,
		    key_expires_at = $11,
		    lookup_consistency_window_ms = $12,
		    dispatch_started_at = $13,
		    lease_owner = 'dead-worker',
		    lease_deadline = clock_timestamp() + interval '1 second',
		    fencing_token = 1
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
	`, tenantID, runID, command.Identity.EffectID, binding.SchemaVersion,
		binding.AdapterID, binding.ContractVersion, binding.ProviderOperation,
		binding.ProviderScopeDigest, binding.ProviderRequestHash,
		binding.KeyCreatedAt, binding.KeyExpiresAt,
		binding.LookupConsistencyWindow.Milliseconds(), dispatchStartedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(claimedCtx, runID, audit.Draft{
		EventID: "write:" + string(command.Identity.EffectID) + ":attempt:1:started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteStarted,
		Payload: writeProjection{
			Key:             command.Identity.Key,
			CallID:          command.CallID,
			Action:          command.Identity.Action,
			ArgumentsHash:   command.Identity.ArgumentsHash,
			IdentityVersion: command.Identity.Version,
			EffectID:        command.Identity.EffectID,
			Attempt:         1,
			State:           idempotency.StateStarted,
			Binding:         &binding,
			DispatchStarted: &dispatchStartedAt,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `
		UPDATE waybill.effects
		SET lease_deadline = clock_timestamp() - interval '1 second'
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID); err != nil {
		t.Fatal(err)
	}

	outcome, err := repository.Recover(claimedCtx, command)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Decision != idempotency.RecoveryResolved ||
		outcome.State != idempotency.StateSucceeded ||
		string(outcome.Result.Value) != `{"carrier_id":"CARRIER-SW-42","order_id":"RA-recovered","status":"accepted","waybill_id":"YD2026101001"}` {
		t.Fatalf("recovery outcome = %+v", outcome)
	}
	if recoveryRuntime.dispatchCalls != 0 || recoveryRuntime.lookupCalls != 1 {
		t.Fatalf(
			"runtime calls = dispatch:%d lookup:%d, want 0 and 1",
			recoveryRuntime.dispatchCalls,
			recoveryRuntime.lookupCalls,
		)
	}
	var (
		status            string
		externalRequestID string
		responseDigest    string
		lastLookupAt      time.Time
	)
	if err := db.pool.QueryRow(t.Context(), `
		SELECT status, external_request_id, response_digest, last_lookup_at
		FROM waybill.effects
		WHERE tenant_id = $1 AND effect_id = $2
	`, tenantID, command.Identity.EffectID).Scan(
		&status,
		&externalRequestID,
		&responseDigest,
		&lastLookupAt,
	); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" ||
		externalRequestID != "request-recovered" ||
		len(responseDigest) != 64 ||
		lastLookupAt.IsZero() {
		t.Fatalf(
			"recovered metadata = status:%q request:%q digest:%q lookup:%v",
			status,
			externalRequestID,
			responseDigest,
			lastLookupAt,
		)
	}
}

func TestConversationPersistenceSupportsIncrementalContinuation(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-1")
	defer repository.Close()
	runID := domain.RunID(uuid.NewString())
	appendStarted(t, repository, runID)
	leaseCtx, release, err := repository.AcquireRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = release()
	}()

	key := bytes.Repeat([]byte{0x42}, 32)
	persistence, err := NewConversationPersistence(context.Background(), db, HistoryConfig{
		TenantID: tenantID,
		KeyID:    "test-key",
		Key:      key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.SaveMessages(
		leaseCtx,
		"waybill-demo",
		history.DefaultGroupID,
		"sdk-run-1",
		"",
		string(runID),
		"conversation-1",
		[]history.Message{{ID: "message-1"}},
		map[string]any{"state": "running"},
	); err != nil {
		t.Fatal(err)
	}
	if err := persistence.SaveMessages(
		leaseCtx,
		"waybill-demo",
		history.DefaultGroupID,
		"sdk-run-1",
		"",
		string(runID),
		"conversation-1",
		[]history.Message{{ID: "message-2"}},
		map[string]any{"state": "paused"},
	); err != nil {
		t.Fatal(err)
	}
	if err := persistence.SaveMessages(
		leaseCtx,
		"waybill-demo",
		history.DefaultGroupID,
		"sdk-run-2",
		"sdk-run-1",
		string(runID),
		"conversation-1",
		[]history.Message{{ID: "message-3"}},
		map[string]any{"state": "completed"},
	); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewConversationPersistence(context.Background(), db, HistoryConfig{
		TenantID: tenantID,
		KeyID:    "test-key",
		Key:      key,
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.LoadMessages(
		t.Context(),
		"waybill-demo",
		string(runID),
		"sdk-run-2",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 2 ||
		len(loaded[0].Messages) != 2 ||
		len(loaded[1].Messages) != 1 ||
		loaded[0].Meta["state"] != "paused" ||
		loaded[1].Meta["state"] != "completed" {
		t.Fatalf("loaded checkpoints = %+v", loaded)
	}
	var ciphertext []byte
	if err := db.pool.QueryRow(t.Context(), `
		SELECT ciphertext
		FROM waybill.agent_checkpoints
		WHERE tenant_id = $1 AND namespace = 'waybill-demo' AND sdk_run_id = 'sdk-run-1'
	`, tenantID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("message-1")) ||
		bytes.Contains(ciphertext, []byte("paused")) {
		t.Fatal("checkpoint ciphertext contains plaintext history")
	}
}

func TestConversationPersistencePrunesOnlyExpiredTerminalHistory(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	repository := newIntegrationRepository(t, db, tenantID, "worker-retention")
	defer repository.Close()
	now := time.Now().UTC()
	key := bytes.Repeat([]byte{0x52}, 32)
	persistence, err := NewConversationPersistence(t.Context(), db, HistoryConfig{
		TenantID:  tenantID,
		KeyID:     "test-key",
		Key:       key,
		Clock:     func() time.Time { return now },
		Retention: 365 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	type fixture struct {
		runID    domain.RunID
		status   domain.RunStatus
		closedAt *time.Time
	}
	expiredAt := now.Add(-2 * time.Hour)
	recentAt := now.Add(-30 * time.Minute)
	fixtures := []fixture{
		{runID: domain.RunID("active-" + uuid.NewString()), status: domain.RunStarted},
		{runID: domain.RunID("expired-" + uuid.NewString()), status: domain.RunFailed, closedAt: &expiredAt},
		{runID: domain.RunID("recent-" + uuid.NewString()), status: domain.RunFailed, closedAt: &recentAt},
	}
	for _, value := range fixtures {
		appendStarted(t, repository, value.runID)
		leaseCtx, release, err := repository.AcquireRun(t.Context(), value.runID)
		if err != nil {
			t.Fatal(err)
		}
		sdkRunID := "sdk-" + string(value.runID)
		if err := persistence.SaveMessages(
			leaseCtx,
			"waybill-demo",
			history.DefaultGroupID,
			sdkRunID,
			"",
			string(value.runID),
			"conversation-"+string(value.runID),
			[]history.Message{{ID: "message-" + string(value.runID)}},
			map[string]any{"history_schema_version": 1},
		); err != nil {
			t.Fatal(err)
		}
		if err := persistence.SaveSummary(leaseCtx, "waybill-demo", history.Summary{
			ID:       "summary-" + string(value.runID),
			ThreadID: string(value.runID),
			Meta:     map[string]any{"history_schema_version": 1},
		}); err != nil {
			t.Fatal(err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
		if value.closedAt != nil {
			if _, err := db.pool.Exec(t.Context(), `
				UPDATE waybill.runs
				SET status = $3, closed_at = $4, updated_at = $4
				WHERE tenant_id = $1 AND run_id = $2
			`, tenantID, value.runID, value.status, value.closedAt); err != nil {
				t.Fatal(err)
			}
		}
	}

	if _, err := NewConversationPersistence(t.Context(), db, HistoryConfig{
		TenantID:  tenantID,
		KeyID:     "test-key",
		Key:       key,
		Clock:     func() time.Time { return now },
		Retention: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	for _, value := range fixtures {
		var (
			checkpoints       int
			summaries         int
			sdkRunID          *string
			checkpointVersion int64
		)
		if err := db.pool.QueryRow(t.Context(), `
			SELECT
				(SELECT count(*) FROM waybill.agent_checkpoints
				  WHERE tenant_id = $1 AND thread_id = $2),
				(SELECT count(*) FROM waybill.agent_summaries
				  WHERE tenant_id = $1 AND thread_id = $2),
				(SELECT sdk_run_id FROM waybill.runs
				  WHERE tenant_id = $1 AND run_id = $2),
				(SELECT checkpoint_version FROM waybill.runs
				  WHERE tenant_id = $1 AND run_id = $2)
		`, tenantID, value.runID).Scan(
			&checkpoints,
			&summaries,
			&sdkRunID,
			&checkpointVersion,
		); err != nil {
			t.Fatal(err)
		}
		want := 1
		if value.runID == fixtures[1].runID {
			want = 0
		}
		if checkpoints != want || summaries != want {
			t.Fatalf(
				"history for %s = checkpoints:%d summaries:%d, want %d each",
				value.runID,
				checkpoints,
				summaries,
				want,
			)
		}
		if want == 0 && (sdkRunID != nil || checkpointVersion != 0) {
			t.Fatalf(
				"expired history pointer for %s = sdk:%v checkpoint:%d",
				value.runID,
				sdkRunID,
				checkpointVersion,
			)
		}
		if want == 1 && (sdkRunID == nil || checkpointVersion != 1) {
			t.Fatalf(
				"retained history pointer for %s = sdk:%v checkpoint:%d",
				value.runID,
				sdkRunID,
				checkpointVersion,
			)
		}
	}
	var auditCount int
	if err := db.pool.QueryRow(t.Context(), `
		SELECT count(*)
		FROM waybill.audit_events
		WHERE tenant_id = $1 AND run_id = $2
	`, tenantID, fixtures[1].runID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("expired run audit events = %d, want 1", auditCount)
	}
}

func TestGuardianResumesPostgresHistoryAcrossInstances(t *testing.T) {
	db := openIntegrationDB(t)
	tenantID := "tenant-" + uuid.NewString()
	key := bytes.Repeat([]byte{0x62}, 32)
	clients, mock, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	writeRuntime, err := guardtools.NewFixtureWriteRuntime(clients)
	if err != nil {
		t.Fatal(err)
	}
	openService := func(workerID string) *guardian.Service {
		repository := newIntegrationRepository(t, db, tenantID, workerID, writeRuntime)
		persistence, err := NewConversationPersistence(t.Context(), db, HistoryConfig{
			TenantID: tenantID,
			KeyID:    "test-key",
			Key:      key,
		})
		if err != nil {
			t.Fatal(err)
		}
		service, err := guardian.OpenDurable(guardian.DurableConfig{
			Config:      guardian.Config{Clients: clients},
			Journal:     repository,
			Effects:     repository,
			History:     persistence,
			Coordinator: repository,
		})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}

	first := openService("worker-first")
	run, err := first.StartDemo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var pending approval.Approval
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending, err = first.CurrentApproval(run.RunID)
		if err == nil {
			break
		}
		if !errors.Is(err, approval.ErrNotFound) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending.ID == "" {
		t.Fatal("timed out waiting for PostgreSQL-backed approval")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second := openService("worker-second")
	defer second.Close()
	decided, err := second.Decide(t.Context(), pending.ID, guardian.DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "integration-reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decided.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q, want executed", decided.Status)
	}
	completed, err := second.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.RunCompleted {
		t.Fatalf("run status = %q, want completed", completed.Status)
	}
	if mock.WriteCount(domain.ActionReassign) != 1 ||
		mock.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf(
			"writes = reassign:%d sms:%d",
			mock.WriteCount(domain.ActionReassign),
			mock.WriteCount(domain.ActionSendSMS),
		)
	}
}

func openIntegrationDB(t *testing.T) *DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	db, err := Open(ctx, Config{DatabaseURL: databaseURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

func resetDatabaseToMigration(t *testing.T, db *DB, targetVersion int64) {
	t.Helper()
	if _, err := db.pool.Exec(t.Context(), `
		DROP SCHEMA IF EXISTS waybill CASCADE;
		CREATE SCHEMA waybill;
		CREATE TABLE waybill.schema_migrations (
			version bigint PRIMARY KEY,
			name text NOT NULL,
			checksum text NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`, pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version > targetVersion {
			break
		}
		tx, err := db.pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(
			t.Context(),
			migration.SQL,
			pgx.QueryExecModeSimpleProtocol,
		); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO waybill.schema_migrations (version, name, checksum)
			VALUES ($1, $2, $3)
		`, migration.Version, migration.Name, migration.Checksum); err != nil {
			_ = tx.Rollback(t.Context())
			t.Fatal(err)
		}
		if err := tx.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := db.pool.Exec(context.Background(), `DROP SCHEMA IF EXISTS waybill CASCADE`); err != nil {
			t.Errorf("drop integration schema: %v", err)
			return
		}
		if err := db.migrate(context.Background()); err != nil {
			t.Errorf("restore integration schema: %v", err)
		}
	})
}

func decodeIntegrationDetection(
	t *testing.T,
	eventID string,
	incidentKey string,
	version int64,
	stopMinutes int,
) eventmodel.Submission {
	t.Helper()
	return decodeIntegrationDetectionForWaybill(
		t,
		eventID,
		incidentKey,
		version,
		stopMinutes,
		"YD2026101001",
	)
}

func decodeIntegrationDetectionForWaybill(
	t *testing.T,
	eventID string,
	incidentKey string,
	version int64,
	stopMinutes int,
	waybillID string,
) eventmodel.Submission {
	t.Helper()
	body := fmt.Sprintf(
		`{"specversion":"1.0","id":%q,"source":"urn:tms:integration","type":"com.waybill.tracking.delay.detected.v1","subject":%q,"time":"2026-10-10T12:30:00Z","datacontenttype":"application/json","dataschema":"urn:waybill-guardian:schema:delay-detected:v1","data":{"waybill_id":%q,"incident_key":%q,"source_version":%d,"event_time":"2026-10-10T12:28:31Z","record_time":"2026-10-10T12:30:00Z","location":{"code":"MY-N-SERVICE","name":"Mianyang North Service Area"},"business_step":"transporting","reason_code":"stop_duration_exceeded","observations":{"stop_minutes":%d}}}`,
		eventID,
		"waybill/"+waybillID,
		waybillID,
		incidentKey,
		version,
		stopMinutes,
	)
	return decodeIntegrationSubmission(t, body)
}

func decodeIntegrationCorrection(
	t *testing.T,
	eventID string,
	incidentKey string,
	version int64,
	targetID string,
	stopMinutes int,
) eventmodel.Submission {
	t.Helper()
	body := fmt.Sprintf(
		`{"specversion":"1.0","id":%q,"source":"urn:tms:integration","type":"com.waybill.tracking.delay.corrected.v1","subject":"waybill/YD2026101001","time":"2026-10-10T12:45:00Z","datacontenttype":"application/json","dataschema":"urn:waybill-guardian:schema:delay-corrected:v1","data":{"waybill_id":"YD2026101001","incident_key":%q,"source_version":%d,"event_time":"2026-10-10T12:28:31Z","record_time":"2026-10-10T12:45:00Z","corrects":{"source":"urn:tms:integration","id":%q},"operation":"replace","replacement":{"location":{"code":"MY-S-SERVICE","name":"Mianyang South Service Area"},"business_step":"transporting","reason_code":"stop_duration_exceeded","observations":{"stop_minutes":%d}},"reason":"integration correction"}}`,
		eventID,
		incidentKey,
		version,
		targetID,
		stopMinutes,
	)
	return decodeIntegrationSubmission(t, body)
}

func decodeIntegrationSubmission(t *testing.T, body string) eventmodel.Submission {
	t.Helper()
	submission, err := eventmodel.DecodeStructured(eventmodel.DecodeRequest{
		ContentType: "application/cloudevents+json",
		Body:        strings.NewReader(body),
		Now:         time.Date(2026, 10, 10, 13, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return submission
}

func readIncidentProjection(
	t *testing.T,
	db *DB,
	tenantID string,
	incidentKey string,
) (hash string, state string, version int64) {
	t.Helper()
	if err := db.pool.QueryRow(t.Context(), `
		SELECT transport_hash, transport_state, version
		FROM waybill.incidents
		WHERE tenant_id = $1
		  AND source = 'urn:tms:integration'
		  AND source_incident_key = $2
	`, tenantID, incidentKey).Scan(&hash, &state, &version); err != nil {
		t.Fatal(err)
	}
	return hash, state, version
}

func newIntegrationRepository(
	t *testing.T,
	db *DB,
	tenantID string,
	workerID string,
	runtimes ...platform.WriteRuntime,
) *Repository {
	t.Helper()
	var writeRuntime platform.WriteRuntime
	if len(runtimes) > 0 {
		writeRuntime = runtimes[0]
	}
	repository, err := NewRepository(db, RepositoryConfig{
		TenantID:       tenantID,
		WorkerID:       workerID,
		LeaseTTL:       5 * time.Second,
		EffectLeaseTTL: 5 * time.Second,
		OutboxLeaseTTL: 5 * time.Second,
		PollInterval:   10 * time.Millisecond,
		WriteRuntime:   writeRuntime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

type blockingWriteRuntime struct {
	platform.WriteRuntime
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type advertisedWriteRuntime struct {
	platform.WriteRuntime
	actions []domain.Action
}

func (r *advertisedWriteRuntime) AdvertisedActions() []domain.Action {
	return append([]domain.Action(nil), r.actions...)
}

func (r *blockingWriteRuntime) Dispatch(
	ctx context.Context,
	binding platform.EffectBinding,
	request platform.EffectRequest,
	key domain.IdempotencyKey,
) platform.DispatchResult {
	r.once.Do(func() {
		close(r.entered)
	})
	select {
	case <-ctx.Done():
		return platform.DispatchResult{
			Disposition: platform.EffectUnknown,
			ErrorCode:   "dispatch_canceled",
		}
	case <-r.release:
		return r.WriteRuntime.Dispatch(ctx, binding, request, key)
	}
}

type recoveryWriteRuntime struct {
	platform.WriteRuntime
	dispatchCalls int
	lookupCalls   int
}

func (r *recoveryWriteRuntime) Dispatch(
	context.Context,
	platform.EffectBinding,
	platform.EffectRequest,
	domain.IdempotencyKey,
) platform.DispatchResult {
	r.dispatchCalls++
	return platform.DispatchResult{
		Disposition: platform.EffectPermanentFailed,
		ErrorCode:   "unexpected_dispatch",
	}
}

func (r *recoveryWriteRuntime) Lookup(
	context.Context,
	platform.EffectBinding,
	domain.IdempotencyKey,
) platform.LookupResult {
	r.lookupCalls++
	return platform.LookupResult{
		Disposition: platform.LookupApplied,
		Response: json.RawMessage(
			`{"order_id":"RA-recovered","waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42","status":"accepted"}`,
		),
		ExternalRef:       "RA-recovered",
		ExternalRequestID: "request-recovered",
		ResponseDigest:    strings.Repeat("a", 64),
	}
}

func prepareApprovedReassignEffect(
	t *testing.T,
	repository *Repository,
	runID domain.RunID,
	carrierID domain.CarrierID,
) (context.Context, idempotency.Command, idempotency.AuthorizedEffect) {
	t.Helper()
	appendStarted(t, repository, runID)
	claim, err := repository.ClaimRun(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	claimedCtx := context.WithValue(t.Context(), runClaimContextKey{}, claim)
	arguments, err := json.Marshal(map[string]string{
		"waybill_id": "YD2026101001",
		"carrier_id": string(carrierID),
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := idempotency.Derive(idempotency.DerivationInput{
		RunContext: domain.RunContext{
			RunID:       runID,
			IncidentID:  domain.IncidentID("incident-" + string(runID)),
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionReassign,
		Target:    "waybill/YD2026101001/carrier/" + string(carrierID),
		Arguments: arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	item := approval.Item{
		CallID:          "call-" + string(runID),
		Action:          domain.ActionReassign,
		WireName:        "tms_reassign",
		Params:          arguments,
		ArgumentsHash:   identity.ArgumentsHash,
		IdentityVersion: identity.Version,
		EffectID:        identity.EffectID,
		IdempotencyKey:  identity.Key,
	}
	approvalID := approval.IDFor(runID, []string{item.CallID})
	now := time.Now().UTC()
	if _, err := repository.Append(claimedCtx, runID, audit.Draft{
		EventID: "approval:" + string(approvalID) + ":requested",
		Actor:   audit.ActorAgent,
		Type:    audit.EventApprovalRequested,
		Payload: approval.Approval{
			ID:          approvalID,
			RunID:       runID,
			SDKRunID:    "sdk-" + string(runID),
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
			Items:       []approval.Item{item},
			Reason:      "test",
			Evidence:    []approval.Evidence{{Label: "delay", Value: "6h"}},
			Status:      approval.StatusPending,
			RequestedAt: now,
			ExpiresAt:   now.Add(time.Minute),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(claimedCtx, runID, audit.Draft{
		EventID: "approval:" + string(approvalID) + ":confirmed",
		Actor:   audit.ActorHuman,
		Type:    audit.EventApprovalDecided,
		Payload: map[string]any{
			"approval_id": approvalID,
			"status":      approval.StatusConfirmed,
			"decided_by":  "reviewer",
			"decided_at":  now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	command := idempotency.Command{
		RunID:    runID,
		CallID:   item.CallID,
		Identity: identity,
	}
	effect, err := idempotency.AuthorizeEffect(command, platform.EffectRequest{
		Action:        item.Action,
		Arguments:     item.Params,
		ArgumentsHash: item.ArgumentsHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	return claimedCtx, command, effect
}

func appendStarted(t *testing.T, repository *Repository, runID domain.RunID) {
	t.Helper()
	if _, err := repository.Append(t.Context(), runID, audit.Draft{
		EventID: "run:" + string(runID) + ":started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventRunStarted,
		Payload: map[string]any{
			"incident_id": "incident-" + string(runID),
			"waybill_id":  "YD2026101001",
			"status":      domain.RunStarted,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func toolCallDraft(id string) audit.Draft {
	return audit.Draft{
		EventID: "tool:" + id + ":call",
		Actor:   audit.ActorAgent,
		Type:    audit.EventToolCall,
		Payload: map[string]any{
			"call_id":   id,
			"action":    domain.ActionGetWaybill,
			"wire_name": "tms_get_waybill",
			"arguments": map[string]any{"waybill_id": "YD2026101001"},
		},
	}
}
