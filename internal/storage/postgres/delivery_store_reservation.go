package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

type executionReservationStatus string

const (
	executionReservationReserved               executionReservationStatus = "reserved"
	executionReservationReconciliationRequired executionReservationStatus = "reconciliation_required"
	executionReservationCommitted              executionReservationStatus = "committed"
	executionReservationReleased               executionReservationStatus = "released"
)

type executionReservation struct {
	TenantID        deliverydomain.TenantID
	ExecutionID     deliverydomain.ExecutionID
	ApprovalID      deliverydomain.ApprovalID
	PlanID          deliverydomain.PlanID
	RevisionID      deliverydomain.PlanRevisionID
	BaseRevisionID  deliverydomain.PlanRevisionID
	ActiveVersion   uint64
	EffectSetDigest deliverydomain.ArtifactDigest
	Status          executionReservationStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CompletedAt     *time.Time
}

const deliveryExecutionReservationSelect = `
	SELECT tenant_id,
	       execution_id,
	       approval_id,
	       plan_id,
	       revision_id,
	       COALESCE(base_revision_id, ''),
	       active_version,
	       effect_set_digest,
	       status,
	       created_at,
	       updated_at,
	       completed_at
	FROM waybill.delivery_execution_reservations
`

func scanExecutionReservation(
	scanner deliveryScanner,
) (executionReservation, error) {
	var value executionReservation
	err := scanner.Scan(
		&value.TenantID,
		&value.ExecutionID,
		&value.ApprovalID,
		&value.PlanID,
		&value.RevisionID,
		&value.BaseRevisionID,
		&value.ActiveVersion,
		&value.EffectSetDigest,
		&value.Status,
		&value.CreatedAt,
		&value.UpdatedAt,
		&value.CompletedAt,
	)
	return value, err
}

func getExecutionReservationForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	executionID deliverydomain.ExecutionID,
) (executionReservation, error) {
	value, err := scanExecutionReservation(tx.QueryRow(
		ctx,
		deliveryExecutionReservationSelect+`
			WHERE tenant_id = $1 AND execution_id = $2
			FOR UPDATE
		`,
		tenantID,
		executionID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return executionReservation{}, deliveryservice.ErrNotFound
	}
	return value, err
}

func reserveExecution(
	ctx context.Context,
	tx pgx.Tx,
	command deliveryservice.DecideExecutionTx,
	approval deliverydomain.PlanApproval,
) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_execution_reservations (
			tenant_id, execution_id, approval_id, plan_id, revision_id,
			base_revision_id, active_version, effect_set_digest, status,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, 'reserved', $9, $9
		)
		ON CONFLICT (tenant_id, plan_id)
			WHERE status IN ('reserved', 'reconciliation_required')
		DO NOTHING
	`, command.TenantID, command.Execution.ID, approval.ID,
		approval.Binding.PlanID, approval.Binding.RevisionID,
		command.ExpectedActiveRevisionID, command.ExpectedActiveVersion,
		approval.Binding.EffectSetDigest, command.Now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrConflict
	}
	return nil
}

func executionHasDispatchIntent(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	executionID deliverydomain.ExecutionID,
) (bool, error) {
	var started bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM waybill.delivery_effects
			WHERE tenant_id = $1
			  AND execution_id = $2
			  AND dispatch_started_at IS NOT NULL
		)
	`, tenantID, executionID).Scan(&started); err != nil {
		return false, err
	}
	return started, nil
}

func (store *DeliveryStore) ResolveExecutionReservation(
	ctx context.Context,
	command deliveryservice.ResolveExecutionReservationTx,
) (deliverydomain.DispatchExecution, deliveryservice.Replay, error) {
	if command.TenantID == "" ||
		command.IdempotencyKey == "" ||
		command.RequestDigest == "" ||
		command.Actor.Subject == "" ||
		command.ExecutionID == "" ||
		command.ExpectedVersion == 0 ||
		strings.TrimSpace(command.Reason) == "" ||
		len(command.Reason) > 1_000 ||
		len(command.CompensationReference) > 1_000 ||
		command.Now.IsZero() {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	defer tx.Rollback(context.Background())
	replayed, found, err := beginDeliveryCommand(
		ctx,
		tx,
		command.TenantID,
		"resolve_execution_reservation",
		command.IdempotencyKey,
		command.RequestDigest,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if found {
		value, decodeErr := decodeDeliveryReplay[deliverydomain.DispatchExecution](replayed)
		if decodeErr != nil {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, decodeErr
		}
		if err := tx.Commit(ctx); err != nil {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
		}
		return value, deliveryservice.Replay{Replayed: true}, nil
	}
	reservation, err := getExecutionReservationForUpdate(
		ctx,
		tx,
		command.TenantID,
		command.ExecutionID,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	execution, err := scanDeliveryExecution(tx.QueryRow(
		ctx,
		deliveryExecutionSelect+`
			WHERE tenant_id = $1 AND execution_id = $2
			FOR UPDATE
		`,
		command.TenantID,
		command.ExecutionID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrNotFound
	}
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if reservation.Status != executionReservationReconciliationRequired ||
		reservation.TenantID != execution.TenantID ||
		reservation.ExecutionID != execution.ID ||
		reservation.PlanID != execution.PlanID ||
		reservation.RevisionID != execution.RevisionID ||
		reservation.ApprovalID != execution.ApprovalID ||
		reservation.EffectSetDigest != execution.EffectSetDigest ||
		execution.Version != command.ExpectedVersion ||
		(execution.Status != deliverydomain.ExecutionPartiallyApplied &&
			execution.Status != deliverydomain.ExecutionManualReview) {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	var (
		effectCount                 int
		nonterminalCount            int
		succeededCount              int
		failedCount                 int
		dispatchedManualReviewCount int
	)
	if err := tx.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (
		           WHERE status NOT IN (
		               'succeeded',
		               'permanent_failed',
		               'manual_review'
		           )
		       ),
		       count(*) FILTER (WHERE status = 'succeeded'),
		       count(*) FILTER (
		           WHERE status IN ('permanent_failed', 'manual_review')
		       ),
		       count(*) FILTER (
		           WHERE status = 'manual_review'
		             AND dispatch_started_at IS NOT NULL
		       )
		FROM waybill.delivery_effects
		WHERE tenant_id = $1 AND execution_id = $2
	`, command.TenantID, command.ExecutionID).Scan(
		&effectCount,
		&nonterminalCount,
		&succeededCount,
		&failedCount,
		&dispatchedManualReviewCount,
	); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if effectCount == 0 ||
		nonterminalCount != 0 ||
		((succeededCount > 0 || dispatchedManualReviewCount > 0) &&
			strings.TrimSpace(command.CompensationReference) == "") {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_execution_reservations
		SET status = 'released',
		    updated_at = $3,
		    completed_at = $3
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status = 'reconciliation_required'
	`, command.TenantID, command.ExecutionID, command.Now)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if tag.RowsAffected() != 1 {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	execution, err = scanDeliveryExecution(tx.QueryRow(ctx, `
		UPDATE waybill.delivery_executions
		SET version = version + 1,
		    updated_at = $3
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND version = $4
		  AND status IN ('partially_applied', 'manual_review')
		RETURNING tenant_id,
		          execution_id,
		          approval_id,
		          plan_id,
		          revision_id,
		          effect_set_digest,
		          status,
		          version,
		          created_at,
		          updated_at,
		          completed_at
	`, command.TenantID, command.ExecutionID, command.Now, command.ExpectedVersion))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	eventPayload := map[string]any{
		"reason":                         command.Reason,
		"compensation_reference":         command.CompensationReference,
		"effect_count":                   effectCount,
		"succeeded_count":                succeededCount,
		"failed_count":                   failedCount,
		"dispatched_manual_review_count": dispatchedManualReviewCount,
		"reservation_status":             executionReservationReleased,
	}
	executionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.TenantID,
		deliveryservice.AggregateExecution,
		string(execution.ID),
		deliveryservice.EventExecutionReservationResolved,
		command.Actor.Subject,
		eventPayload,
		command.Now,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	revisionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.TenantID,
		deliveryservice.AggregateRevision,
		string(execution.RevisionID),
		deliveryservice.EventExecutionReservationResolved,
		command.Actor.Subject,
		eventPayload,
		command.Now,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if err := completeDeliveryCommand(
		ctx,
		tx,
		command.TenantID,
		"resolve_execution_reservation",
		command.IdempotencyKey,
		"execution",
		string(execution.ID),
		execution,
		command.Now,
	); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	store.notifyDelivery(executionEvent)
	store.notifyDelivery(revisionEvent)
	return execution, deliveryservice.Replay{}, nil
}
