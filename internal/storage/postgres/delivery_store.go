package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const deliveryGenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

type DeliveryStore struct {
	db *DB

	mu            sync.Mutex
	nextSubID     uint64
	subscriptions map[uint64]*deliverySubscription
}

func NewDeliveryStore(db *DB) (*DeliveryStore, error) {
	if db == nil || db.pool == nil {
		return nil, fmt.Errorf("PostgreSQL database is required")
	}
	return &DeliveryStore{
		db:            db,
		subscriptions: make(map[uint64]*deliverySubscription),
	}, nil
}

type deliveryScanner interface {
	Scan(...any) error
}

func scanDeliveryProblem(scanner deliveryScanner) (deliverydomain.ProblemVersion, error) {
	var value deliverydomain.ProblemVersion
	err := scanner.Scan(
		&value.TenantID,
		&value.ProblemID,
		&value.Version,
		&value.ProblemDigest,
		&value.PolicyDigest,
		&value.CommitmentDigest,
		&value.ManifestDigest,
		&value.ProblemArtifact,
		&value.SourceProfile,
		&value.SourceRef,
		&value.CreatedAt,
	)
	return value, err
}

const deliveryProblemSelect = `
	SELECT tenant_id,
	       problem_id,
	       version,
	       problem_digest,
	       policy_digest,
	       commitment_digest,
	       manifest_digest,
	       problem_artifact_digest,
	       source_profile,
	       source_ref,
	       created_at
	FROM waybill.delivery_problem_versions
`

func scanDeliveryRun(scanner deliveryScanner) (deliverydomain.OptimizationRun, error) {
	var value deliverydomain.OptimizationRun
	err := scanner.Scan(
		&value.TenantID,
		&value.ID,
		&value.ProblemID,
		&value.ProblemVersion,
		&value.ProblemDigest,
		&value.SolverProfile,
		&value.ConfigDigest,
		&value.RequestedBy,
		&value.Status,
		&value.Version,
		&value.CancelRequestedAt,
		&value.CheckpointDigest,
		&value.ResultRevisionID,
		&value.FailureCode,
		&value.LeaseOwner,
		&value.LeaseDeadline,
		&value.FencingToken,
		&value.CreatedAt,
		&value.UpdatedAt,
		&value.ClosedAt,
	)
	return value, err
}

const deliveryRunSelect = `
	SELECT tenant_id,
	       run_id,
	       problem_id,
	       problem_version,
	       problem_digest,
	       solver_profile,
	       config_digest,
	       requested_by,
	       status,
	       version,
	       cancel_requested_at,
	       COALESCE(checkpoint_digest, ''),
	       COALESCE(result_revision_id, ''),
	       COALESCE(failure_code, ''),
	       COALESCE(lease_owner, ''),
	       lease_deadline,
	       fencing_token,
	       created_at,
	       updated_at,
	       closed_at
	FROM waybill.delivery_runs
`

func scanDeliveryPlan(scanner deliveryScanner) (deliverydomain.DispatchPlan, error) {
	var value deliverydomain.DispatchPlan
	err := scanner.Scan(
		&value.TenantID,
		&value.ID,
		&value.ProblemID,
		&value.ActiveRevision,
		&value.ActiveVersion,
		&value.CreatedAt,
		&value.UpdatedAt,
	)
	return value, err
}

const deliveryPlanSelect = `
	SELECT tenant_id,
	       plan_id,
	       problem_id,
	       COALESCE(active_revision_id, ''),
	       active_version,
	       created_at,
	       updated_at
	FROM waybill.delivery_plans
`

func scanDeliveryRevision(scanner deliveryScanner) (deliverydomain.PlanRevision, error) {
	var value deliverydomain.PlanRevision
	err := scanner.Scan(
		&value.TenantID,
		&value.ID,
		&value.PlanID,
		&value.BaseRevisionID,
		&value.RunID,
		&value.ProblemDigest,
		&value.PolicyDigest,
		&value.CommitmentDigest,
		&value.PlanArtifactDigest,
		&value.PlanDigest,
		&value.ValidationArtifact,
		&value.ValidationReportDigest,
		&value.EffectSetArtifact,
		&value.EffectSetDigest,
		&value.Status,
		&value.Version,
		&value.CreatedAt,
		&value.UpdatedAt,
	)
	return value, err
}

const deliveryRevisionSelect = `
	SELECT tenant_id,
	       revision_id,
	       plan_id,
	       COALESCE(base_revision_id, ''),
	       run_id,
	       problem_digest,
	       policy_digest,
	       commitment_digest,
	       plan_artifact_digest,
	       plan_digest,
	       validation_artifact_digest,
	       validation_report_digest,
	       COALESCE(effect_set_artifact_digest, ''),
	       COALESCE(effect_set_digest, ''),
	       status,
	       version,
	       created_at,
	       updated_at
	FROM waybill.delivery_plan_revisions
`

func beginDeliveryCommand(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	operation string,
	key deliveryservice.IdempotencyKey,
	requestDigest deliverydomain.ArtifactDigest,
) ([]byte, bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_command_results (
			tenant_id,
			operation,
			idempotency_key,
			request_digest
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, operation, idempotency_key) DO NOTHING
	`, tenantID, operation, key, requestDigest)
	if err != nil {
		return nil, false, fmt.Errorf("claim delivery idempotency key: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return nil, false, nil
	}
	var (
		storedDigest deliverydomain.ArtifactDigest
		response     []byte
	)
	if err := tx.QueryRow(ctx, `
		SELECT request_digest, response
		FROM waybill.delivery_command_results
		WHERE tenant_id = $1
		  AND operation = $2
		  AND idempotency_key = $3
		FOR UPDATE
	`, tenantID, operation, key).Scan(&storedDigest, &response); err != nil {
		return nil, false, fmt.Errorf("read delivery idempotency result: %w", err)
	}
	if storedDigest != requestDigest {
		return nil, false, deliveryservice.ErrIdempotencyConflict
	}
	if len(response) == 0 {
		return nil, false, fmt.Errorf("delivery idempotency result is incomplete")
	}
	return response, true, nil
}

func completeDeliveryCommand(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	operation string,
	key deliveryservice.IdempotencyKey,
	resourceType string,
	resourceID string,
	response any,
	completedAt time.Time,
) error {
	raw, err := deliverydomain.CanonicalJSON(response)
	if err != nil {
		return fmt.Errorf("canonicalize delivery command result: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_command_results
		SET resource_type = $4,
		    resource_id = $5,
		    response = $6::jsonb,
		    completed_at = $7
		WHERE tenant_id = $1
		  AND operation = $2
		  AND idempotency_key = $3
		  AND response IS NULL
	`, tenantID, operation, key, resourceType, resourceID, raw, completedAt)
	if err != nil {
		return fmt.Errorf("complete delivery idempotency result: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrConflict
	}
	return nil
}

func appendDeliveryEvent(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	aggregateType deliveryservice.AggregateType,
	aggregateID string,
	eventType deliveryservice.EventType,
	actor string,
	payload any,
	occurredAt time.Time,
) (deliveryservice.Event, error) {
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_event_heads (
			tenant_id,
			aggregate_type,
			aggregate_id,
			updated_at
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, aggregate_type, aggregate_id) DO NOTHING
	`, tenantID, aggregateType, aggregateID, occurredAt); err != nil {
		return deliveryservice.Event{}, fmt.Errorf("ensure delivery event head: %w", err)
	}
	var (
		lastSeq  uint64
		lastHash string
	)
	if err := tx.QueryRow(ctx, `
		SELECT last_seq, last_hash
		FROM waybill.delivery_event_heads
		WHERE tenant_id = $1
		  AND aggregate_type = $2
		  AND aggregate_id = $3
		FOR UPDATE
	`, tenantID, aggregateType, aggregateID).Scan(&lastSeq, &lastHash); err != nil {
		return deliveryservice.Event{}, fmt.Errorf("lock delivery event head: %w", err)
	}
	raw, err := deliverydomain.CanonicalJSON(payload)
	if err != nil {
		return deliveryservice.Event{}, fmt.Errorf("canonicalize delivery event: %w", err)
	}
	event := deliveryservice.Event{
		SchemaVersion: 1,
		TenantID:      tenantID,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Seq:           lastSeq + 1,
		EventID:       uuid.NewString(),
		Type:          eventType,
		Actor:         actor,
		OccurredAt:    occurredAt.UTC().Truncate(time.Microsecond),
		Payload:       json.RawMessage(raw),
		PrevHash:      lastHash,
	}
	event.Hash, err = hashDeliveryEvent(event)
	if err != nil {
		return deliveryservice.Event{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_events (
			tenant_id,
			aggregate_type,
			aggregate_id,
			seq,
			event_id,
			event_type,
			actor,
			occurred_at,
			payload,
			payload_canonical,
			prev_hash,
			hash
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9::jsonb, $10, $11, $12
		)
	`, event.TenantID, event.AggregateType, event.AggregateID, event.Seq,
		event.EventID, event.Type, event.Actor, event.OccurredAt, raw, raw,
		event.PrevHash, event.Hash); err != nil {
		return deliveryservice.Event{}, fmt.Errorf("insert delivery event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_outbox (
			tenant_id,
			event_id,
			aggregate_type,
			aggregate_id,
			aggregate_seq,
			event_type,
			payload,
			payload_canonical,
			available_at,
			created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $9
		)
	`, event.TenantID, event.EventID, event.AggregateType, event.AggregateID,
		event.Seq, event.Type, raw, raw, event.OccurredAt); err != nil {
		return deliveryservice.Event{}, fmt.Errorf("insert delivery outbox event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_event_heads
		SET last_seq = $4,
		    last_hash = $5,
		    updated_at = $6
		WHERE tenant_id = $1
		  AND aggregate_type = $2
		  AND aggregate_id = $3
	`, tenantID, aggregateType, aggregateID, event.Seq, event.Hash,
		event.OccurredAt); err != nil {
		return deliveryservice.Event{}, fmt.Errorf("advance delivery event head: %w", err)
	}
	return event, nil
}

func hashDeliveryEvent(event deliveryservice.Event) (string, error) {
	value := struct {
		SchemaVersion int
		TenantID      deliverydomain.TenantID
		AggregateType deliveryservice.AggregateType
		AggregateID   string
		Seq           uint64
		EventID       string
		Type          deliveryservice.EventType
		Actor         string
		OccurredAt    time.Time
		Payload       json.RawMessage
		PrevHash      string
	}{
		SchemaVersion: event.SchemaVersion,
		TenantID:      event.TenantID,
		AggregateType: event.AggregateType,
		AggregateID:   event.AggregateID,
		Seq:           event.Seq,
		EventID:       event.EventID,
		Type:          event.Type,
		Actor:         event.Actor,
		OccurredAt:    event.OccurredAt.UTC(),
		Payload:       event.Payload,
		PrevHash:      event.PrevHash,
	}
	digest, err := deliverydomain.Digest(value)
	return string(digest), err
}

func decodeDeliveryReplay[T any](raw []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return value, fmt.Errorf("delivery replay has trailing JSON")
	}
	return value, nil
}

func (store *DeliveryStore) notifyDelivery(event deliveryservice.Event) {
	key := deliveryStreamKey{
		tenantID:      event.TenantID,
		aggregateType: event.AggregateType,
		aggregateID:   event.AggregateID,
	}
	store.mu.Lock()
	for _, subscription := range store.subscriptions {
		if subscription.key != key {
			continue
		}
		select {
		case subscription.wake <- struct{}{}:
		default:
		}
	}
	store.mu.Unlock()
	_, _ = store.db.pool.Exec(
		context.Background(),
		`SELECT pg_notify('delivery_events', $1)`,
		string(event.TenantID)+"|"+string(event.AggregateType)+"|"+event.AggregateID,
	)
}
