package postgres

import (
	"context"
	"slices"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

func (store *DeliveryStore) CompleteEffect(
	ctx context.Context,
	claim deliveryservice.EffectClaim,
	command deliveryservice.CompleteEffectTx,
) (deliveryservice.EffectCompletion, error) {
	if claim.Effect.TenantID == "" ||
		claim.Effect.ID == "" ||
		claim.WorkerID == "" ||
		claim.FencingToken == 0 ||
		command.ObservedAt.IsZero() {
		return deliveryservice.EffectCompletion{}, deliveryservice.ErrConflict
	}
	if err := validateEffectCompletion(command); err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	defer tx.Rollback(context.Background())
	reservation, err := getExecutionReservationForUpdate(
		ctx,
		tx,
		claim.Effect.TenantID,
		claim.Effect.ExecutionID,
	)
	if err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	if reservation.Status == executionReservationReleased {
		return deliveryservice.EffectCompletion{}, deliveryservice.ErrLeaseLost
	}
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_effects
		SET status = $5,
		    next_operation = $6,
		    external_ref = NULLIF($7, ''),
		    response_digest = NULLIF($8, ''),
		    error_code = NULLIF($9, ''),
		    retry_at = $10,
		    lease_owner = NULL,
		    lease_deadline = NULL,
		    updated_at = $11
		WHERE tenant_id = $1
		  AND effect_id = $2
		  AND lease_owner = $3
		  AND fencing_token = $4
		  AND status IN ('dispatching', 'reconciling')
	`, claim.Effect.TenantID, claim.Effect.ID, claim.WorkerID,
		claim.FencingToken, command.Status, command.NextOperation,
		command.ExternalRef, command.ResponseDigest, command.ErrorCode,
		command.RetryAt, command.ObservedAt)
	if err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.EffectCompletion{}, deliveryservice.ErrLeaseLost
	}
	if command.ResponseDigest != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO waybill.delivery_artifact_refs (
				tenant_id, digest, owner_type, owner_id, role, created_at
			) VALUES ($1, $2, 'execution', $3, $4, $5)
			ON CONFLICT DO NOTHING
		`, claim.Effect.TenantID, command.ResponseDigest,
			claim.Effect.ExecutionID, "effect_result:"+string(claim.Effect.ID),
			command.ObservedAt); err != nil {
			return deliveryservice.EffectCompletion{}, err
		}
	}
	var blockedPreparedEffectIDs []deliverydomain.EffectID
	if claim.Effect.Required &&
		(command.Status == deliverydomain.EffectPermanentFailed ||
			command.Status == deliverydomain.EffectManualReview) {
		blockedPreparedEffectIDs, err = terminalizePreparedEffects(
			ctx,
			tx,
			claim.Effect.TenantID,
			claim.Effect.ExecutionID,
			"blocked_by_required_failure",
			command.ObservedAt,
		)
		if err != nil {
			return deliveryservice.EffectCompletion{}, err
		}
	}
	var (
		requiredOutstanding int
		requiredFailed      int
		succeeded           int
		requiredLookup      int
	)
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (
		           WHERE required AND status <> 'succeeded'
		       ),
		       count(*) FILTER (
		           WHERE required
		             AND status IN ('permanent_failed', 'manual_review')
		       ),
		       count(*) FILTER (WHERE status = 'succeeded'),
		       count(*) FILTER (
		           WHERE required
		             AND (
		                 status IN ('unknown', 'reconciling')
		                 OR (status = 'retry_wait' AND next_operation = 'lookup')
		             )
		       )
		FROM waybill.delivery_effects
		WHERE tenant_id = $1 AND execution_id = $2
	`, claim.Effect.TenantID, claim.Effect.ExecutionID).Scan(
		&requiredOutstanding,
		&requiredFailed,
		&succeeded,
		&requiredLookup,
	); err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	executionStatus := deliverydomain.ExecutionExecuting
	revisionStatus := deliverydomain.RevisionApplying
	switch {
	case requiredFailed > 0 && succeeded > 0:
		executionStatus = deliverydomain.ExecutionPartiallyApplied
		revisionStatus = deliverydomain.RevisionPartiallyApplied
	case requiredFailed > 0:
		executionStatus = deliverydomain.ExecutionManualReview
		revisionStatus = deliverydomain.RevisionPartiallyApplied
	case requiredLookup > 0:
		executionStatus = deliverydomain.ExecutionReconciliationRequired
		revisionStatus = deliverydomain.RevisionReconciliationRequired
	}
	reservationStatus := executionReservationReserved
	if requiredFailed > 0 || requiredLookup > 0 {
		reservationStatus = executionReservationReconciliationRequired
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_execution_reservations
		SET status = $3,
		    updated_at = $4
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status IN ('reserved', 'reconciliation_required')
	`, claim.Effect.TenantID, claim.Effect.ExecutionID,
		reservationStatus, command.ObservedAt); err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_executions
		SET status = $3,
		    version = version + 1,
		    updated_at = $4
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status <> 'committed'
	`, claim.Effect.TenantID, claim.Effect.ExecutionID,
		executionStatus, command.ObservedAt); err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_plan_revisions
		SET status = $3,
		    version = version + 1,
		    updated_at = $4
		WHERE tenant_id = $1
		  AND revision_id = $2
		  AND status <> 'active'
	`, claim.Effect.TenantID, claim.Effect.RevisionID,
		revisionStatus, command.ObservedAt); err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	eventPayload := map[string]any{
		"effect_id":                   claim.Effect.ID,
		"operation":                   claim.Operation,
		"status":                      command.Status,
		"next_operation":              command.NextOperation,
		"external_ref":                command.ExternalRef,
		"response_digest":             command.ResponseDigest,
		"error_code":                  command.ErrorCode,
		"retry_at":                    command.RetryAt,
		"blocked_prepared":            len(blockedPreparedEffectIDs),
		"blocked_prepared_effect_ids": blockedPreparedEffectIDs,
	}
	if len(blockedPreparedEffectIDs) > 0 {
		eventPayload["blocked_prepared_error_code"] = "blocked_by_required_failure"
	}
	executionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		claim.Effect.TenantID,
		deliveryservice.AggregateExecution,
		string(claim.Effect.ExecutionID),
		deliveryservice.EventEffectCompleted,
		"system:delivery-effect-worker",
		eventPayload,
		command.ObservedAt,
	)
	if err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	revisionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		claim.Effect.TenantID,
		deliveryservice.AggregateRevision,
		string(claim.Effect.RevisionID),
		deliveryservice.EventEffectCompleted,
		"system:delivery-effect-worker",
		eventPayload,
		command.ObservedAt,
	)
	if err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliveryservice.EffectCompletion{}, err
	}
	store.notifyDelivery(executionEvent)
	store.notifyDelivery(revisionEvent)
	return deliveryservice.EffectCompletion{
		ExecutionID:     claim.Effect.ExecutionID,
		ActivationReady: requiredOutstanding == 0,
	}, nil
}

func validateEffectCompletion(command deliveryservice.CompleteEffectTx) error {
	switch command.Status {
	case deliverydomain.EffectSucceeded,
		deliverydomain.EffectPermanentFailed,
		deliverydomain.EffectManualReview:
		if command.RetryAt != nil {
			return deliveryservice.ErrConflict
		}
	case deliverydomain.EffectUnknown:
		if command.NextOperation != deliverydomain.EffectOperationLookup {
			return deliveryservice.ErrConflict
		}
	case deliverydomain.EffectRetryWait:
		if command.RetryAt == nil {
			return deliveryservice.ErrConflict
		}
	default:
		return deliveryservice.ErrConflict
	}
	if command.NextOperation != deliverydomain.EffectOperationDispatch &&
		command.NextOperation != deliverydomain.EffectOperationLookup {
		return deliveryservice.ErrConflict
	}
	return nil
}

func terminalizePreparedEffects(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	executionID deliverydomain.ExecutionID,
	errorCode string,
	now time.Time,
) ([]deliverydomain.EffectID, error) {
	rows, err := tx.Query(ctx, `
		UPDATE waybill.delivery_effects
		SET status = 'manual_review',
		    error_code = $3,
		    retry_at = NULL,
		    lease_owner = NULL,
		    lease_deadline = NULL,
		    updated_at = $4
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status = 'prepared'
		  AND dispatch_started_at IS NULL
		RETURNING effect_id
	`, tenantID, executionID, errorCode, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	effectIDs := make([]deliverydomain.EffectID, 0)
	for rows.Next() {
		var effectID deliverydomain.EffectID
		if err := rows.Scan(&effectID); err != nil {
			return nil, err
		}
		effectIDs = append(effectIDs, effectID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Sort(effectIDs)
	return effectIDs, nil
}

func (store *DeliveryStore) markExecutionFailed(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	executionID deliverydomain.ExecutionID,
	revisionID deliverydomain.PlanRevisionID,
	now time.Time,
) ([]deliverydomain.EffectID, error) {
	blockedPreparedEffectIDs, err := terminalizePreparedEffects(
		ctx,
		tx,
		tenantID,
		executionID,
		"blocked_by_required_failure",
		now,
	)
	if err != nil {
		return nil, err
	}
	var succeeded int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM waybill.delivery_effects
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status = 'succeeded'
	`, tenantID, executionID).Scan(&succeeded); err != nil {
		return nil, err
	}
	executionStatus := deliverydomain.ExecutionManualReview
	if succeeded > 0 {
		executionStatus = deliverydomain.ExecutionPartiallyApplied
	}
	hasDispatchIntent, err := executionHasDispatchIntent(
		ctx,
		tx,
		tenantID,
		executionID,
	)
	if err != nil {
		return nil, err
	}
	reservationStatus := executionReservationReleased
	if hasDispatchIntent {
		reservationStatus = executionReservationReconciliationRequired
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_execution_reservations
		SET status = $3::text,
		    updated_at = $4::timestamptz,
		    completed_at = CASE
		        WHEN $3::text = 'released' THEN $4::timestamptz
		        ELSE NULL
		    END
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status IN ('reserved', 'reconciliation_required')
	`, tenantID, executionID, reservationStatus, now); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_executions
		SET status = $3, version = version + 1, updated_at = $4
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status <> 'committed'
	`, tenantID, executionID, executionStatus, now); err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		UPDATE waybill.delivery_plan_revisions
		SET status = 'partially_applied',
		    version = version + 1,
		    updated_at = $3
		WHERE tenant_id = $1
		  AND revision_id = $2
		  AND status <> 'active'
	`, tenantID, revisionID, now)
	return blockedPreparedEffectIDs, err
}
