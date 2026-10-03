package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/jackc/pgx/v5"
)

var ErrRepositoryClosed = errors.New("PostgreSQL repository is closed")

type RepositoryConfig struct {
	TenantID       string
	WorkerID       string
	LeaseTTL       time.Duration
	OutboxLeaseTTL time.Duration
	PollInterval   time.Duration
	Clock          func() time.Time
	EffectLookup   idempotency.LookupFunc
}

type Repository struct {
	db             *DB
	tenantID       string
	workerID       string
	leaseTTL       time.Duration
	outboxLeaseTTL time.Duration
	pollInterval   time.Duration
	clock          func() time.Time
	effectLookup   idempotency.LookupFunc

	mu            sync.Mutex
	closed        bool
	nextSubID     uint64
	subscriptions map[uint64]context.CancelFunc
}

func NewRepository(db *DB, config RepositoryConfig) (*Repository, error) {
	if db == nil || db.pool == nil {
		return nil, fmt.Errorf("PostgreSQL database is required")
	}
	if config.TenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}
	if config.WorkerID == "" {
		return nil, fmt.Errorf("worker ID is required")
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 30 * time.Second
	}
	if config.OutboxLeaseTTL <= 0 {
		config.OutboxLeaseTTL = 30 * time.Second
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Repository{
		db:             db,
		tenantID:       config.TenantID,
		workerID:       config.WorkerID,
		leaseTTL:       config.LeaseTTL,
		outboxLeaseTTL: config.OutboxLeaseTTL,
		pollInterval:   config.PollInterval,
		clock:          config.Clock,
		effectLookup:   config.EffectLookup,
		subscriptions:  make(map[uint64]context.CancelFunc),
	}, nil
}

func (r *Repository) Append(
	ctx context.Context,
	runID domain.RunID,
	draft audit.Draft,
) (audit.Event, error) {
	if err := r.checkOpen(); err != nil {
		return audit.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return audit.Event{}, err
	}
	tx, err := r.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return audit.Event{}, fmt.Errorf("begin PostgreSQL audit transition: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	event, inserted, err := r.appendTransition(ctx, tx, runID, draft)
	if err != nil {
		return audit.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return audit.Event{}, fmt.Errorf("commit PostgreSQL audit transition: %w", err)
	}
	if inserted {
		r.notify(runID)
	}
	return event, nil
}

func (r *Repository) appendTransition(
	ctx context.Context,
	tx pgx.Tx,
	runID domain.RunID,
	draft audit.Draft,
) (audit.Event, bool, error) {
	if draft.Type == audit.EventRunStarted {
		if err := r.ensureRun(ctx, tx, runID, draft); err != nil {
			return audit.Event{}, false, err
		}
	}

	lastSeq, lastHash, err := r.lockRunAndValidateClaim(ctx, tx, runID, draft.Type)
	if err != nil {
		return audit.Event{}, false, err
	}
	existing, found, err := r.eventByID(ctx, tx, runID, draft.EventID)
	if err != nil {
		return audit.Event{}, false, err
	}
	if found {
		return existing, false, nil
	}
	if draft.Type == audit.EventRunStarted && lastSeq != 0 {
		return audit.Event{}, false, fmt.Errorf("run %q is already started", runID)
	}

	prevHash := audit.GenesisHash
	if lastHash != nil {
		prevHash = *lastHash
	}
	event, err := audit.BuildEvent(
		runID,
		audit.Seq(lastSeq+1),
		prevHash,
		r.clock().UTC(),
		draft,
	)
	if err != nil {
		return audit.Event{}, false, err
	}
	if err := r.applyProjection(ctx, tx, event); err != nil {
		return audit.Event{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.audit_events (
			tenant_id, run_id, seq, event_id, schema_version, occurred_at,
			actor_type, event_type, payload, payload_canonical, prev_hash, hash
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12)
	`, r.tenantID, event.RunID, event.Seq, event.EventID, event.SchemaVersion, event.TS,
		event.Actor, event.Type, []byte(event.Payload), []byte(event.Payload),
		event.PrevHash, event.Hash); err != nil {
		return audit.Event{}, false, fmt.Errorf("insert PostgreSQL audit event: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.runs
		SET last_audit_seq = $3, last_audit_hash = $4, updated_at = $5
		WHERE tenant_id = $1 AND run_id = $2
	`, r.tenantID, runID, event.Seq, event.Hash, event.TS); err != nil {
		return audit.Event{}, false, fmt.Errorf("advance PostgreSQL audit head: %w", err)
	}
	if err := r.insertOutbox(ctx, tx, event); err != nil {
		return audit.Event{}, false, err
	}
	return event, true, nil
}

func (r *Repository) lockRunAndValidateClaim(
	ctx context.Context,
	tx pgx.Tx,
	runID domain.RunID,
	eventType audit.EventType,
) (int64, *string, error) {
	var lastSeq int64
	var lastHash *string
	var leaseOwner *string
	var fence int64
	var leaseActive bool
	var status domain.RunStatus
	if err := tx.QueryRow(ctx, `
		SELECT last_audit_seq, last_audit_hash, lease_owner, fencing_token, status,
		       COALESCE(lease_deadline > clock_timestamp(), false)
		FROM waybill.runs
		WHERE tenant_id = $1 AND run_id = $2
		FOR UPDATE
	`, r.tenantID, runID).Scan(
		&lastSeq,
		&lastHash,
		&leaseOwner,
		&fence,
		&status,
		&leaseActive,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, audit.ErrRunNotFound
		}
		return 0, nil, fmt.Errorf("lock PostgreSQL run %q: %w", runID, err)
	}
	if status == domain.RunManualReview {
		return 0, nil, ErrRunQuarantined
	}
	if eventType != audit.EventRunStarted {
		claim := claimFromContext(ctx)
		if claim == nil ||
			claim.repository != r ||
			claim.runID != runID ||
			leaseOwner == nil ||
			*leaseOwner != claim.owner ||
			fence != claim.fence ||
			!leaseActive {
			return 0, nil, ErrStaleRunClaim
		}
	}
	return lastSeq, lastHash, nil
}

func (r *Repository) notify(runID domain.RunID) {
	_, _ = r.db.pool.Exec(
		context.Background(),
		`SELECT pg_notify('waybill_audit', $1)`,
		r.tenantID+":"+string(runID),
	)
}

func (r *Repository) Replay(
	ctx context.Context,
	runID domain.RunID,
	after audit.Seq,
) ([]audit.Event, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	var lastSeq int64
	var quarantineReason *string
	if err := r.db.pool.QueryRow(ctx, `
		SELECT run.last_audit_seq, quarantine.reason
		FROM waybill.runs run
		LEFT JOIN waybill.run_quarantines quarantine
		  ON quarantine.tenant_id = run.tenant_id
		 AND quarantine.run_id = run.run_id
		WHERE run.tenant_id = $1 AND run.run_id = $2
	`, r.tenantID, runID).Scan(&lastSeq, &quarantineReason); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, audit.ErrRunNotFound
		}
		return nil, fmt.Errorf("read PostgreSQL audit head: %w", err)
	}
	if quarantineReason != nil {
		return nil, &RunQuarantinedError{RunID: runID, Reason: *quarantineReason}
	}
	if uint64(after) > uint64(lastSeq) {
		return nil, audit.ErrCursorAhead
	}
	return r.replayUnchecked(ctx, runID, after)
}

func (r *Repository) Subscribe(
	ctx context.Context,
	runID domain.RunID,
	after audit.Seq,
) (*audit.Subscription, error) {
	backlog, err := r.Replay(ctx, runID, after)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		return nil, ErrRepositoryClosed
	}
	subscriptionID := r.nextSubID
	r.nextSubID++
	r.subscriptions[subscriptionID] = cancel
	r.mu.Unlock()

	size := len(backlog) + 64
	if size < 64 {
		size = 64
	}
	events := make(chan audit.Event, size)
	go r.pollTimeline(streamCtx, subscriptionID, runID, after, backlog, events)
	return audit.NewSubscription(events, cancel), nil
}

func (r *Repository) AllEvents(ctx context.Context) ([]audit.Event, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	rows, err := r.db.pool.Query(ctx, auditSelect+`
		WHERE tenant_id = $1
		  AND NOT EXISTS (
		      SELECT 1
		      FROM waybill.run_quarantines quarantine
		      WHERE quarantine.tenant_id = audit_events.tenant_id
		        AND quarantine.run_id = audit_events.run_id
		  )
		ORDER BY run_id, seq
	`, r.tenantID)
	if err != nil {
		return nil, fmt.Errorf("read PostgreSQL audit events: %w", err)
	}
	return scanEvents(rows)
}

func (r *Repository) Verify(runID domain.RunID) error {
	ctx := context.Background()
	var head auditHead
	head.runID = runID
	if err := r.db.pool.QueryRow(ctx, `
		SELECT last_audit_seq, last_audit_hash
		FROM waybill.runs
		WHERE tenant_id = $1 AND run_id = $2
	`, r.tenantID, runID).Scan(&head.lastSeq, &head.lastHash); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit.ErrRunNotFound
		}
		return fmt.Errorf("read PostgreSQL audit head: %w", err)
	}
	events, err := r.replayUnchecked(ctx, runID, 0)
	if err != nil {
		return err
	}
	return verifyAuditHead(head, events)
}

func (r *Repository) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	cancels := make([]context.CancelFunc, 0, len(r.subscriptions))
	for _, cancel := range r.subscriptions {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return nil
}

func (r *Repository) pollTimeline(
	ctx context.Context,
	subscriptionID uint64,
	runID domain.RunID,
	after audit.Seq,
	backlog []audit.Event,
	events chan<- audit.Event,
) {
	defer close(events)
	defer func() {
		r.mu.Lock()
		delete(r.subscriptions, subscriptionID)
		r.mu.Unlock()
	}()
	cursor := after
	send := func(values []audit.Event) bool {
		for _, event := range values {
			select {
			case events <- event:
				cursor = event.Seq
			case <-ctx.Done():
				return false
			}
		}
		return true
	}
	if !send(backlog) {
		return
	}
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			values, err := r.Replay(ctx, runID, cursor)
			if err != nil || !send(values) {
				return
			}
		}
	}
}

func (r *Repository) eventByID(
	ctx context.Context,
	tx pgx.Tx,
	runID domain.RunID,
	eventID string,
) (audit.Event, bool, error) {
	row := tx.QueryRow(ctx, auditSelect+`
		WHERE tenant_id = $1 AND run_id = $2 AND event_id = $3
	`, r.tenantID, runID, eventID)
	event, err := scanEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return audit.Event{}, false, nil
	}
	if err != nil {
		return audit.Event{}, false, fmt.Errorf("read duplicate PostgreSQL audit event: %w", err)
	}
	return event, true, nil
}

func (r *Repository) checkOpen() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRepositoryClosed
	}
	return nil
}

const auditSelect = `
	SELECT schema_version, event_id, seq, occurred_at, run_id, actor_type,
	       event_type, payload_canonical, prev_hash, hash
	FROM waybill.audit_events
`

type rowScanner interface {
	Scan(...any) error
}

func scanEvent(row rowScanner) (audit.Event, error) {
	var event audit.Event
	var payload []byte
	err := row.Scan(
		&event.SchemaVersion,
		&event.EventID,
		&event.Seq,
		&event.TS,
		&event.RunID,
		&event.Actor,
		&event.Type,
		&payload,
		&event.PrevHash,
		&event.Hash,
	)
	event.Payload = append(json.RawMessage(nil), payload...)
	event.TS = event.TS.UTC()
	return event, err
}

func scanEvents(rows pgx.Rows) ([]audit.Event, error) {
	defer rows.Close()
	var events []audit.Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan PostgreSQL audit event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read PostgreSQL audit events: %w", err)
	}
	return events, nil
}

var _ audit.Journal = (*Repository)(nil)
