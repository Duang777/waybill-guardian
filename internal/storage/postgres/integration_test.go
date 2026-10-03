//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/guardian"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/google/uuid"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
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
	if health.SchemaVersion != 3 {
		t.Fatalf("schema version = %d, want 3", health.SchemaVersion)
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
	if migrationCount != 3 {
		t.Fatalf("migration rows = %d, want 3", migrationCount)
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
			aggregate_version, event_type, payload
		) VALUES ($1, 'urn:waybill-guardian', $2, 'run', $3, 2, 'test', '{}'::jsonb)
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
	`, tenantID, firstClaim.source, firstClaim.eventID); err != nil {
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
	if event := secondClaim.Event(); event.AggregateVersion != 1 || event.Attempt != 2 {
		t.Fatalf("reclaimed outbox event = %+v", event)
	}
	if secondClaim.fence <= firstClaim.fence {
		t.Fatalf("reclaimed fence = %d, first fence = %d", secondClaim.fence, firstClaim.fence)
	}
	if err := second.RenewOutbox(t.Context(), secondClaim); err != nil {
		t.Fatalf("renew current outbox claim: %v", err)
	}
	if err := first.RenewOutbox(t.Context(), firstClaim); !errors.Is(err, ErrStaleOutboxClaim) {
		t.Fatalf("stale outbox renewal = %v, want ErrStaleOutboxClaim", err)
	}
	if err := first.CompleteOutbox(t.Context(), firstClaim, OutboxResult{
		Disposition: OutboxPublished,
	}); !errors.Is(err, ErrStaleOutboxClaim) {
		t.Fatalf("stale outbox completion = %v, want ErrStaleOutboxClaim", err)
	}
	if err := second.CompleteOutbox(t.Context(), secondClaim, OutboxResult{
		Disposition: OutboxPublished,
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
	if err := first.CompleteOutbox(t.Context(), nextClaim, OutboxResult{
		Disposition: OutboxRetryableFailed,
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
	`, tenantID, nextClaim.source, nextClaim.eventID); err != nil {
		t.Fatal(err)
	}
	claims, err = second.ClaimOutbox(t.Context(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || claims[0].Event().Attempt != 2 {
		t.Fatalf("retryable outbox claims = %+v", claims)
	}
	if err := second.CompleteOutbox(t.Context(), claims[0], OutboxResult{
		Disposition: OutboxPublished,
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
	`, tenantID, nextClaim.source, nextClaim.eventID).Scan(
		&status,
		&publishedAt,
		&leaseOwner,
	); err != nil {
		t.Fatal(err)
	}
	if status != string(OutboxPublished) || publishedAt == nil || leaseOwner != nil {
		t.Fatalf("completed outbox state = status %q published %v owner %v",
			status, publishedAt, leaseOwner)
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

	history, err := NewConversationPersistence(db, HistoryConfig{
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
	persistence, err := NewConversationPersistence(db, HistoryConfig{
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

	reopened, err := NewConversationPersistence(db, HistoryConfig{
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

func newIntegrationRepository(
	t *testing.T,
	db *DB,
	tenantID string,
	workerID string,
) *Repository {
	t.Helper()
	repository, err := NewRepository(db, RepositoryConfig{
		TenantID:     tenantID,
		WorkerID:     workerID,
		LeaseTTL:     5 * time.Second,
		PollInterval: 10 * time.Millisecond,
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
