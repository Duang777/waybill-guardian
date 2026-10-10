package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

func (store *DeliveryStore) GetApproval(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	approvalID deliverydomain.ApprovalID,
) (deliverydomain.PlanApproval, error) {
	value, err := scanDeliveryApproval(store.db.pool.QueryRow(ctx, deliveryApprovalSelect+`
		WHERE tenant_id = $1 AND approval_id = $2
	`, tenantID, approvalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.PlanApproval{}, deliveryservice.ErrNotFound
	}
	return value, err
}

func (store *DeliveryStore) GetExecution(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	executionID deliverydomain.ExecutionID,
) (deliverydomain.DispatchExecution, []deliverydomain.EffectRecord, error) {
	execution, err := scanDeliveryExecution(store.db.pool.QueryRow(
		ctx,
		deliveryExecutionSelect+`
			WHERE tenant_id = $1 AND execution_id = $2
		`,
		tenantID,
		executionID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.DispatchExecution{}, nil, deliveryservice.ErrNotFound
	}
	if err != nil {
		return deliverydomain.DispatchExecution{}, nil, err
	}
	rows, err := store.db.pool.Query(ctx, deliveryEffectSelect+`
		WHERE tenant_id = $1 AND execution_id = $2
		ORDER BY ordinal
	`, tenantID, executionID)
	if err != nil {
		return deliverydomain.DispatchExecution{}, nil, err
	}
	defer rows.Close()
	effects := make([]deliverydomain.EffectRecord, 0)
	for rows.Next() {
		effect, scanErr := scanDeliveryEffect(rows)
		if scanErr != nil {
			return deliverydomain.DispatchExecution{}, nil, scanErr
		}
		effects = append(effects, effect)
	}
	return execution, effects, rows.Err()
}

func (store *DeliveryStore) ClaimEffects(
	ctx context.Context,
	request deliveryservice.ClaimEffects,
) ([]deliveryservice.EffectClaim, error) {
	if request.TenantID == "" ||
		request.WorkerID == "" ||
		request.Limit <= 0 ||
		request.LeaseTTL <= 0 ||
		request.Now.IsZero() {
		return nil, fmt.Errorf("invalid delivery effect claim")
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	expiredEvents, err := store.expireEffectKeys(ctx, tx, request)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT effect.tenant_id,
		       effect.effect_id,
		       CASE
		           WHEN effect.status = 'prepared' THEN 'dispatch'
		           WHEN effect.status IN ('dispatching', 'unknown', 'reconciling')
		               THEN 'lookup'
		           ELSE effect.next_operation
		       END
		FROM waybill.delivery_effects effect
		JOIN waybill.delivery_executions execution
		  ON execution.tenant_id = effect.tenant_id
		 AND execution.execution_id = effect.execution_id
		JOIN waybill.delivery_execution_reservations reservation
		  ON reservation.tenant_id = execution.tenant_id
		 AND reservation.execution_id = execution.execution_id
		JOIN waybill.delivery_plans plan
		  ON plan.tenant_id = reservation.tenant_id
		 AND plan.plan_id = reservation.plan_id
		WHERE effect.tenant_id = $1
		  AND effect.status IN (
		      'prepared',
		      'dispatching',
		      'unknown',
		      'reconciling',
		      'retry_wait'
		  )
		  AND effect.key_expires_at > $2
		  AND (effect.retry_at IS NULL OR effect.retry_at <= $2)
		  AND (effect.lease_deadline IS NULL OR effect.lease_deadline <= $2)
		  AND (
		      (
		          reservation.status = 'reserved'
		          AND execution.status IN (
		              'prepared',
		              'executing',
		              'reconciliation_required'
		          )
		          AND COALESCE(plan.active_revision_id, '') =
		              COALESCE(reservation.base_revision_id, '')
		          AND plan.active_version = reservation.active_version
		      )
		      OR
		      (
		          reservation.status = 'reconciliation_required'
		          AND execution.status IN (
		              'reconciliation_required',
		              'manual_review',
		              'partially_applied'
		          )
		          AND (
		              effect.status IN ('dispatching', 'unknown', 'reconciling')
		              OR effect.next_operation = 'lookup'
		          )
		      )
		      OR
		      (
		          reservation.status = 'committed'
		          AND execution.status = 'committed'
		          AND NOT effect.required
		          AND plan.active_revision_id = reservation.revision_id
		          AND plan.active_version = reservation.active_version + 1
		      )
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM waybill.delivery_effects earlier
		      WHERE earlier.tenant_id = effect.tenant_id
		        AND earlier.execution_id = effect.execution_id
		        AND earlier.ordinal < effect.ordinal
		        AND earlier.required
		        AND earlier.status <> 'succeeded'
		  )
		ORDER BY effect.updated_at, effect.execution_id, effect.ordinal
		FOR UPDATE OF effect, execution, reservation, plan SKIP LOCKED
		LIMIT $3
	`, request.TenantID, request.Now, request.Limit)
	if err != nil {
		return nil, err
	}
	type effectCandidate struct {
		tenantID  deliverydomain.TenantID
		effectID  deliverydomain.EffectID
		operation deliverydomain.EffectOperation
	}
	candidates := make([]effectCandidate, 0, request.Limit)
	for rows.Next() {
		var candidate effectCandidate
		if scanErr := rows.Scan(
			&candidate.tenantID,
			&candidate.effectID,
			&candidate.operation,
		); scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	claims := make([]deliveryservice.EffectClaim, 0, len(candidates))
	events := append([]deliveryservice.Event(nil), expiredEvents...)
	for _, candidate := range candidates {
		operation := candidate.operation
		if operation != deliverydomain.EffectOperationDispatch &&
			operation != deliverydomain.EffectOperationLookup {
			return nil, fmt.Errorf(
				"effect %q has invalid next operation",
				candidate.effectID,
			)
		}
		targetStatus := deliverydomain.EffectDispatching
		if operation == deliverydomain.EffectOperationLookup {
			targetStatus = deliverydomain.EffectReconciling
		}
		deadline := request.Now.Add(request.LeaseTTL)
		updated, err := scanDeliveryEffect(tx.QueryRow(ctx, `
			UPDATE waybill.delivery_effects
			SET status = $4,
			    next_operation = $5,
			    attempt = attempt + 1,
			    retry_at = NULL,
			    dispatch_started_at = CASE
			        WHEN $5 = 'dispatch' THEN COALESCE(dispatch_started_at, $6)
			        ELSE dispatch_started_at
			    END,
			    last_lookup_at = CASE
			        WHEN $5 = 'lookup' THEN $6
			        ELSE last_lookup_at
			    END,
			    lease_owner = $3,
			    lease_deadline = $7,
			    fencing_token = fencing_token + 1,
			    updated_at = $6
			WHERE tenant_id = $1
			  AND effect_id = $2
			  AND (lease_deadline IS NULL OR lease_deadline <= $6)
			RETURNING tenant_id,
			          effect_id,
			          execution_id,
			          revision_id,
			          ordinal,
			          action,
			          target,
			          parameters,
			          parameters_digest,
			          required,
			          adapter_id,
			          contract_version,
			          adapter_binding,
			          adapter_binding_digest,
			          idempotency_key,
			          request_digest,
			          key_created_at,
			          key_expires_at,
			          lookup_consistency_window_seconds,
			          status,
			          next_operation,
			          attempt,
			          COALESCE(external_ref, ''),
			          COALESCE(response_digest, ''),
			          COALESCE(error_code, ''),
			          retry_at,
			          dispatch_started_at,
			          last_lookup_at,
			          COALESCE(lease_owner, ''),
			          lease_deadline,
			          fencing_token,
			          updated_at
		`, candidate.tenantID, candidate.effectID, request.WorkerID, targetStatus,
			operation, request.Now, deadline))
		if err != nil {
			return nil, err
		}
		if operation == deliverydomain.EffectOperationLookup && updated.Required {
			if _, err := tx.Exec(ctx, `
				UPDATE waybill.delivery_execution_reservations
				SET status = 'reconciliation_required',
				    updated_at = $3
				WHERE tenant_id = $1
				  AND execution_id = $2
				  AND status = 'reserved'
			`, updated.TenantID, updated.ExecutionID, request.Now); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE waybill.delivery_executions
			SET status = CASE
			        WHEN $4 = 'lookup' AND $5 THEN 'reconciliation_required'
			        WHEN status = 'prepared' THEN 'executing'
			        ELSE status
			    END,
			    version = version + 1,
			    updated_at = $3
			WHERE tenant_id = $1
			  AND execution_id = $2
			  AND status IN ('prepared', 'executing', 'reconciliation_required')
		`, updated.TenantID, updated.ExecutionID, request.Now,
			operation, updated.Required); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE waybill.delivery_plan_revisions
			SET status = CASE
			        WHEN $4 = 'lookup' AND $5 THEN 'reconciliation_required'
			        WHEN status = 'approved' THEN 'applying'
			        ELSE status
			    END,
			    version = version + 1,
			    updated_at = $3
			WHERE tenant_id = $1
			  AND revision_id = $2
			  AND status IN ('approved', 'applying', 'reconciliation_required')
		`, updated.TenantID, updated.RevisionID, request.Now,
			operation, updated.Required); err != nil {
			return nil, err
		}
		eventPayload := map[string]any{
			"effect_id":      updated.ID,
			"operation":      operation,
			"attempt":        updated.Attempt,
			"fencing_token":  updated.FencingToken,
			"lease_deadline": deadline,
		}
		executionEvent, err := appendDeliveryEvent(
			ctx,
			tx,
			updated.TenantID,
			deliveryservice.AggregateExecution,
			string(updated.ExecutionID),
			deliveryservice.EventEffectClaimed,
			"system:delivery-effect-worker",
			eventPayload,
			request.Now,
		)
		if err != nil {
			return nil, err
		}
		revisionEvent, err := appendDeliveryEvent(
			ctx,
			tx,
			updated.TenantID,
			deliveryservice.AggregateRevision,
			string(updated.RevisionID),
			deliveryservice.EventEffectClaimed,
			"system:delivery-effect-worker",
			eventPayload,
			request.Now,
		)
		if err != nil {
			return nil, err
		}
		claims = append(claims, deliveryservice.EffectClaim{
			Effect:       updated,
			Operation:    operation,
			WorkerID:     request.WorkerID,
			FencingToken: updated.FencingToken,
		})
		events = append(events, executionEvent, revisionEvent)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	for _, event := range events {
		store.notifyDelivery(event)
	}
	return claims, nil
}

func (store *DeliveryStore) expireEffectKeys(
	ctx context.Context,
	tx pgx.Tx,
	request deliveryservice.ClaimEffects,
) ([]deliveryservice.Event, error) {
	rows, err := tx.Query(ctx, deliveryEffectSelect+`
		WHERE tenant_id = $1
		  AND status IN (
		      'prepared',
		      'dispatching',
		      'unknown',
		      'reconciling',
		      'retry_wait'
		  )
		  AND key_expires_at <= $2
		  AND (lease_deadline IS NULL OR lease_deadline <= $2)
		  AND EXISTS (
		      SELECT 1
		      FROM waybill.delivery_execution_reservations reservation
		      WHERE reservation.tenant_id = delivery_effects.tenant_id
		        AND reservation.execution_id = delivery_effects.execution_id
		        AND reservation.status <> 'released'
		  )
		ORDER BY key_expires_at, execution_id, ordinal
		FOR UPDATE SKIP LOCKED
		LIMIT $3
	`, request.TenantID, request.Now, request.Limit)
	if err != nil {
		return nil, err
	}
	var expired []deliverydomain.EffectRecord
	for rows.Next() {
		effect, scanErr := scanDeliveryEffect(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		expired = append(expired, effect)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	events := make([]deliveryservice.Event, 0, len(expired))
	for _, effect := range expired {
		if _, err := tx.Exec(ctx, `
			UPDATE waybill.delivery_effects
			SET status = 'manual_review',
			    error_code = 'idempotency_key_expired',
			    retry_at = NULL,
			    lease_owner = NULL,
			    lease_deadline = NULL,
			    updated_at = $3
			WHERE tenant_id = $1 AND effect_id = $2
		`, effect.TenantID, effect.ID, request.Now); err != nil {
			return nil, err
		}
		if effect.Required {
			if err := store.markExecutionFailed(
				ctx,
				tx,
				effect.TenantID,
				effect.ExecutionID,
				effect.RevisionID,
				request.Now,
			); err != nil {
				return nil, err
			}
		}
		eventPayload := map[string]any{
			"effect_id":  effect.ID,
			"status":     deliverydomain.EffectManualReview,
			"error_code": "idempotency_key_expired",
		}
		executionEvent, err := appendDeliveryEvent(
			ctx,
			tx,
			effect.TenantID,
			deliveryservice.AggregateExecution,
			string(effect.ExecutionID),
			deliveryservice.EventEffectCompleted,
			"system:delivery-effect-worker",
			eventPayload,
			request.Now,
		)
		if err != nil {
			return nil, err
		}
		revisionEvent, err := appendDeliveryEvent(
			ctx,
			tx,
			effect.TenantID,
			deliveryservice.AggregateRevision,
			string(effect.RevisionID),
			deliveryservice.EventEffectCompleted,
			"system:delivery-effect-worker",
			eventPayload,
			request.Now,
		)
		if err != nil {
			return nil, err
		}
		events = append(events, executionEvent, revisionEvent)
	}
	return events, nil
}

func (store *DeliveryStore) RenewEffectLease(
	ctx context.Context,
	claim deliveryservice.EffectClaim,
	deadline time.Time,
) error {
	tag, err := store.db.pool.Exec(ctx, `
		UPDATE waybill.delivery_effects
		SET lease_deadline = $5,
		    updated_at = $6
		WHERE tenant_id = $1
		  AND effect_id = $2
		  AND lease_owner = $3
		  AND fencing_token = $4
		  AND status IN ('dispatching', 'reconciling')
		  AND EXISTS (
		      SELECT 1
		      FROM waybill.delivery_execution_reservations reservation
		      WHERE reservation.tenant_id = delivery_effects.tenant_id
		        AND reservation.execution_id = delivery_effects.execution_id
		        AND reservation.status <> 'released'
		  )
	`, claim.Effect.TenantID, claim.Effect.ID, claim.WorkerID,
		claim.FencingToken, deadline, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrLeaseLost
	}
	return nil
}
