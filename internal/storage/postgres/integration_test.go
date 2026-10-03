//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	if health.SchemaVersion != 5 {
		t.Fatalf("schema version = %d, want 5", health.SchemaVersion)
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
	if tableCount != 13 {
		t.Fatalf("business table count = %d, want 13", tableCount)
	}
	var migrationCount int
	if err := db.pool.QueryRow(ctx,
		`SELECT count(*) FROM waybill.schema_migrations`,
	).Scan(&migrationCount); err != nil {
		t.Fatal(err)
	}
	if migrationCount != 5 {
		t.Fatalf("migration rows = %d, want 5", migrationCount)
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

func TestRepositoryExecutesAndReplaysApprovedEffect(t *testing.T) {
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
	arguments := json.RawMessage(`{"waybill_id":"YD2026101001","carrier_id":"carrier-b"}`)
	identity, err := idempotency.Derive(idempotency.DerivationInput{
		RunContext: domain.RunContext{
			RunID:       runID,
			IncidentID:  domain.IncidentID("incident-" + string(runID)),
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		},
		Action:    domain.ActionReassign,
		Target:    "waybill/YD2026101001/carrier/carrier-b",
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
	calls := 0
	expected := json.RawMessage(`{"order_id":"order-1","status":"accepted"}`)
	first, err := repository.Execute(claimedCtx, command, func(context.Context) (json.RawMessage, error) {
		calls++
		return expected, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Execute(claimedCtx, command, func(context.Context) (json.RawMessage, error) {
		calls++
		return nil, errors.New("duplicate mutation")
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || first.Duplicate || !second.Duplicate ||
		string(first.Value) != string(expected) ||
		string(second.Value) != string(expected) {
		t.Fatalf("effect replay: calls=%d first=%+v second=%+v", calls, first, second)
	}
	state, ok := repository.Lookup(command)
	if !ok || state != idempotency.StateSucceeded {
		t.Fatalf("effect state = %q, %v", state, ok)
	}
	if err := repository.Verify(runID); err != nil {
		t.Fatal(err)
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
	openService := func(workerID string) *guardian.Service {
		repository := newIntegrationRepository(t, db, tenantID, workerID)
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
	body := fmt.Sprintf(
		`{"specversion":"1.0","id":%q,"source":"urn:tms:integration","type":"com.waybill.tracking.delay.detected.v1","subject":"waybill/YD2026101001","time":"2026-10-10T12:30:00Z","datacontenttype":"application/json","dataschema":"urn:waybill-guardian:schema:delay-detected:v1","data":{"waybill_id":"YD2026101001","incident_key":%q,"source_version":%d,"event_time":"2026-10-10T12:28:31Z","record_time":"2026-10-10T12:30:00Z","location":{"code":"MY-N-SERVICE","name":"Mianyang North Service Area"},"business_step":"transporting","reason_code":"stop_duration_exceeded","observations":{"stop_minutes":%d}}}`,
		eventID,
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
) *Repository {
	t.Helper()
	repository, err := NewRepository(db, RepositoryConfig{
		TenantID:       tenantID,
		WorkerID:       workerID,
		LeaseTTL:       5 * time.Second,
		OutboxLeaseTTL: 5 * time.Second,
		PollInterval:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return repository
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
