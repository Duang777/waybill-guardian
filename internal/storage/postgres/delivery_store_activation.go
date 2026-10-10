package postgres

import (
	"context"
	"errors"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

func (store *DeliveryStore) ActivateRevision(
	ctx context.Context,
	command deliveryservice.ActivateRevisionTx,
) error {
	if command.TenantID == "" ||
		command.ExecutionID == "" ||
		command.PlanID == "" ||
		command.RevisionID == "" ||
		command.Now.IsZero() {
		return deliveryservice.ErrConflict
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	reservation, err := getExecutionReservationForUpdate(
		ctx,
		tx,
		command.TenantID,
		command.ExecutionID,
	)
	if err != nil {
		return err
	}
	execution, err := scanDeliveryExecution(tx.QueryRow(ctx, deliveryExecutionSelect+`
		WHERE tenant_id = $1 AND execution_id = $2
		FOR UPDATE
	`, command.TenantID, command.ExecutionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliveryservice.ErrNotFound
	}
	if err != nil {
		return err
	}
	if execution.PlanID != command.PlanID || execution.RevisionID != command.RevisionID {
		return deliveryservice.ErrConflict
	}
	approval, err := scanDeliveryApproval(tx.QueryRow(ctx, deliveryApprovalSelect+`
		WHERE tenant_id = $1 AND approval_id = $2
		FOR UPDATE
	`, command.TenantID, execution.ApprovalID))
	if err != nil {
		return err
	}
	if approval.Status != deliverydomain.ApprovalConfirmed ||
		approval.ExecutionID != execution.ID ||
		approval.Binding.PlanID != command.PlanID ||
		approval.Binding.RevisionID != command.RevisionID ||
		approval.Binding.ActiveVersion != command.ActiveVersion ||
		approval.Binding.EffectSetDigest != execution.EffectSetDigest ||
		reservation.ApprovalID != approval.ID ||
		reservation.PlanID != command.PlanID ||
		reservation.RevisionID != command.RevisionID ||
		reservation.BaseRevisionID != approval.Binding.BaseRevisionID ||
		reservation.ActiveVersion != command.ActiveVersion ||
		reservation.EffectSetDigest != execution.EffectSetDigest {
		return deliveryservice.ErrConflict
	}
	var revisionStatus deliverydomain.RevisionStatus
	if err := tx.QueryRow(ctx, `
		SELECT status
		FROM waybill.delivery_plan_revisions
		WHERE tenant_id = $1 AND revision_id = $2
		FOR UPDATE
	`, command.TenantID, command.RevisionID).Scan(&revisionStatus); err != nil {
		return err
	}
	if execution.Status == deliverydomain.ExecutionCommitted {
		if revisionStatus != deliverydomain.RevisionActive ||
			reservation.Status != executionReservationCommitted {
			return deliveryservice.ErrConflict
		}
	} else if revisionStatus != deliverydomain.RevisionApproved &&
		revisionStatus != deliverydomain.RevisionApplying &&
		revisionStatus != deliverydomain.RevisionReconciliationRequired {
		return deliveryservice.ErrConflict
	} else if reservation.Status != executionReservationReserved {
		return deliveryservice.ErrConflict
	}
	var (
		activeRevision deliverydomain.PlanRevisionID
		activeVersion  uint64
	)
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(active_revision_id, ''), active_version
		FROM waybill.delivery_plans
		WHERE tenant_id = $1 AND plan_id = $2
		FOR UPDATE
	`, command.TenantID, command.PlanID).Scan(
		&activeRevision,
		&activeVersion,
	); err != nil {
		return err
	}
	if execution.Status == deliverydomain.ExecutionCommitted {
		if activeRevision != command.RevisionID ||
			activeVersion != command.ActiveVersion+1 {
			return deliveryservice.ErrConflict
		}
		return tx.Commit(ctx)
	}
	if activeVersion != command.ActiveVersion {
		return store.finishStaleActivation(
			ctx,
			tx,
			execution,
			approval,
			reservation,
			activeRevision,
			activeVersion,
			command,
		)
	}
	if activeRevision != approval.Binding.BaseRevisionID {
		return store.finishStaleActivation(
			ctx,
			tx,
			execution,
			approval,
			reservation,
			activeRevision,
			activeVersion,
			command,
		)
	}
	var requiredOutstanding int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM waybill.delivery_effects
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND required
		  AND status <> 'succeeded'
	`, command.TenantID, command.ExecutionID).Scan(&requiredOutstanding); err != nil {
		return err
	}
	if requiredOutstanding != 0 {
		return deliveryservice.ErrConflict
	}
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_plans
		SET active_revision_id = $3,
		    active_version = active_version + 1,
		    updated_at = $5
		WHERE tenant_id = $1
		  AND plan_id = $2
		  AND active_version = $4
	`, command.TenantID, command.PlanID, command.RevisionID,
		command.ActiveVersion, command.Now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return store.finishStaleActivation(
			ctx,
			tx,
			execution,
			approval,
			reservation,
			activeRevision,
			activeVersion,
			command,
		)
	}
	var supersededEvent deliveryservice.Event
	if activeRevision != "" && activeRevision != command.RevisionID {
		if _, err := tx.Exec(ctx, `
			UPDATE waybill.delivery_plan_revisions
			SET status = 'superseded',
			    version = version + 1,
			    updated_at = $3
			WHERE tenant_id = $1 AND revision_id = $2
		`, command.TenantID, activeRevision, command.Now); err != nil {
			return err
		}
		supersededEvent, err = appendDeliveryEvent(
			ctx,
			tx,
			command.TenantID,
			deliveryservice.AggregateRevision,
			string(activeRevision),
			deliveryservice.EventRevisionSuperseded,
			"system:delivery-effect-worker",
			map[string]any{
				"plan_id":              command.PlanID,
				"superseded_by":        command.RevisionID,
				"activation_execution": command.ExecutionID,
			},
			command.Now,
		)
		if err != nil {
			return err
		}
	}
	tag, err = tx.Exec(ctx, `
		UPDATE waybill.delivery_plan_revisions
		SET status = 'active',
		    version = version + 1,
		    updated_at = $3
		WHERE tenant_id = $1
		  AND revision_id = $2
		  AND status IN ('approved', 'applying', 'reconciliation_required')
	`, command.TenantID, command.RevisionID, command.Now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrConflict
	}
	tag, err = tx.Exec(ctx, `
		UPDATE waybill.delivery_executions
		SET status = 'committed',
		    version = version + 1,
		    updated_at = $3,
		    completed_at = $3
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status IN ('prepared', 'executing', 'reconciliation_required')
	`, command.TenantID, command.ExecutionID, command.Now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrConflict
	}
	tag, err = tx.Exec(ctx, `
		UPDATE waybill.delivery_execution_reservations
		SET status = 'committed',
		    updated_at = $3,
		    completed_at = $3
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status = 'reserved'
	`, command.TenantID, command.ExecutionID, command.Now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrConflict
	}
	planEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.TenantID,
		deliveryservice.AggregatePlan,
		string(command.PlanID),
		deliveryservice.EventRevisionActivated,
		"system:delivery-effect-worker",
		map[string]any{
			"execution_id":   command.ExecutionID,
			"revision_id":    command.RevisionID,
			"active_version": command.ActiveVersion + 1,
		},
		command.Now,
	)
	if err != nil {
		return err
	}
	executionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.TenantID,
		deliveryservice.AggregateExecution,
		string(command.ExecutionID),
		deliveryservice.EventRevisionActivated,
		"system:delivery-effect-worker",
		map[string]any{
			"plan_id":        command.PlanID,
			"revision_id":    command.RevisionID,
			"active_version": command.ActiveVersion + 1,
		},
		command.Now,
	)
	if err != nil {
		return err
	}
	revisionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.TenantID,
		deliveryservice.AggregateRevision,
		string(command.RevisionID),
		deliveryservice.EventRevisionActivated,
		"system:delivery-effect-worker",
		map[string]any{
			"plan_id":        command.PlanID,
			"execution_id":   command.ExecutionID,
			"active_version": command.ActiveVersion + 1,
		},
		command.Now,
	)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	store.notifyDelivery(planEvent)
	store.notifyDelivery(executionEvent)
	store.notifyDelivery(revisionEvent)
	if supersededEvent.EventID != "" {
		store.notifyDelivery(supersededEvent)
	}
	return nil
}

func (store *DeliveryStore) finishStaleActivation(
	ctx context.Context,
	tx pgx.Tx,
	execution deliverydomain.DispatchExecution,
	approval deliverydomain.PlanApproval,
	reservation executionReservation,
	activeRevision deliverydomain.PlanRevisionID,
	activeVersion uint64,
	command deliveryservice.ActivateRevisionTx,
) error {
	blockedPreparedEffectIDs, err := terminalizePreparedEffects(
		ctx,
		tx,
		command.TenantID,
		execution.ID,
		"blocked_by_stale_activation",
		command.Now,
	)
	if err != nil {
		return err
	}
	hasDispatchIntent, err := executionHasDispatchIntent(
		ctx,
		tx,
		command.TenantID,
		execution.ID,
	)
	if err != nil {
		return err
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
		  AND status = $5
	`, command.TenantID, execution.ID, reservationStatus, command.Now,
		reservation.Status); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_approvals
		SET status = 'stale',
		    version = version + 1
		WHERE tenant_id = $1
		  AND approval_id = $2
		  AND status = 'confirmed'
	`, command.TenantID, approval.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_executions
		SET status = 'partially_applied',
		    version = version + 1,
		    updated_at = $3
		WHERE tenant_id = $1
		  AND execution_id = $2
		  AND status <> 'committed'
	`, command.TenantID, execution.ID, command.Now); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_plan_revisions
		SET status = 'partially_applied',
		    version = version + 1,
		    updated_at = $3
		WHERE tenant_id = $1
		  AND revision_id = $2
		  AND status <> 'active'
	`, command.TenantID, execution.RevisionID, command.Now); err != nil {
		return err
	}
	eventPayload := map[string]any{
		"plan_id":                     execution.PlanID,
		"revision_id":                 execution.RevisionID,
		"expected_base_revision":      approval.Binding.BaseRevisionID,
		"current_active_revision":     activeRevision,
		"expected_active_version":     approval.Binding.ActiveVersion,
		"current_active_version":      activeVersion,
		"reason":                      "approval_base_changed",
		"blocked_prepared":            len(blockedPreparedEffectIDs),
		"blocked_prepared_effect_ids": blockedPreparedEffectIDs,
	}
	if len(blockedPreparedEffectIDs) > 0 {
		eventPayload["blocked_prepared_error_code"] = "blocked_by_stale_activation"
	}
	executionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.TenantID,
		deliveryservice.AggregateExecution,
		string(execution.ID),
		deliveryservice.EventActivationFailed,
		"system:delivery-effect-worker",
		eventPayload,
		command.Now,
	)
	if err != nil {
		return err
	}
	revisionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.TenantID,
		deliveryservice.AggregateRevision,
		string(execution.RevisionID),
		deliveryservice.EventActivationFailed,
		"system:delivery-effect-worker",
		eventPayload,
		command.Now,
	)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	store.notifyDelivery(executionEvent)
	store.notifyDelivery(revisionEvent)
	return deliveryservice.ErrApprovalStale
}

func (store *DeliveryStore) ExpireApprovals(
	ctx context.Context,
	request deliveryservice.ExpireApprovals,
) (int, error) {
	if request.Limit <= 0 || request.Now.IsZero() {
		return 0, deliveryservice.ErrConflict
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background())
	query := deliveryApprovalSelect + `
		WHERE status = 'pending'
		  AND (
		      expires_at <= $1
		      OR EXISTS (
		          SELECT 1
		          FROM waybill.delivery_plans plan
		          WHERE plan.tenant_id = delivery_approvals.tenant_id
		            AND plan.plan_id = delivery_approvals.plan_id
		            AND (
		                COALESCE(plan.active_revision_id, '') <>
		                    COALESCE(delivery_approvals.base_revision_id, '')
		                OR plan.active_version <>
		                    delivery_approvals.active_version
		            )
		      )
		      OR EXISTS (
		          SELECT 1
		          FROM waybill.delivery_plan_revisions revision
		          WHERE revision.tenant_id = delivery_approvals.tenant_id
		            AND revision.revision_id = delivery_approvals.revision_id
		            AND (
		                revision.status <> 'awaiting_approval'
		                OR revision.problem_digest <>
		                    delivery_approvals.problem_digest
		                OR revision.policy_digest <>
		                    delivery_approvals.policy_digest
		                OR revision.commitment_digest <>
		                    delivery_approvals.commitment_digest
		                OR revision.plan_digest <>
		                    delivery_approvals.plan_digest
		                OR revision.validation_report_digest <>
		                    delivery_approvals.validation_report_digest
		                OR COALESCE(revision.effect_set_artifact_digest, '') <>
		                    delivery_approvals.effect_set_artifact_digest
		                OR COALESCE(revision.effect_set_digest, '') <>
		                    delivery_approvals.effect_set_digest
		            )
		      )
		  )
	`
	args := []any{request.Now, request.Limit}
	if request.TenantID != "" {
		query += ` AND delivery_approvals.tenant_id = $3`
		args = append(args, request.TenantID)
	}
	query += `
		ORDER BY expires_at, tenant_id, approval_id
		FOR UPDATE SKIP LOCKED
		LIMIT $2
	`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	var approvals []deliverydomain.PlanApproval
	for rows.Next() {
		approval, scanErr := scanDeliveryApproval(rows)
		if scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		approvals = append(approvals, approval)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	events := make([]deliveryservice.Event, 0, len(approvals))
	for _, approval := range approvals {
		status := deliverydomain.ApprovalStale
		revisionStatus := deliverydomain.RevisionStale
		if !request.Now.Before(approval.ExpiresAt) {
			status = deliverydomain.ApprovalExpired
			revisionStatus = deliverydomain.RevisionExpired
		}
		if _, err := tx.Exec(ctx, `
			UPDATE waybill.delivery_approvals
			SET status = $3,
			    version = version + 1
			WHERE tenant_id = $1
			  AND approval_id = $2
			  AND status = 'pending'
		`, approval.TenantID, approval.ID, status); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE waybill.delivery_plan_revisions
			SET status = $3,
			    version = version + 1,
			    updated_at = $4
			WHERE tenant_id = $1
			  AND revision_id = $2
			  AND status = 'awaiting_approval'
		`, approval.TenantID, approval.Binding.RevisionID,
			revisionStatus, request.Now); err != nil {
			return 0, err
		}
		event, err := appendDeliveryEvent(
			ctx,
			tx,
			approval.TenantID,
			deliveryservice.AggregateRevision,
			string(approval.Binding.RevisionID),
			deliveryservice.EventApprovalDecided,
			"system:delivery-recovery",
			map[string]any{
				"approval_id": approval.ID,
				"decision":    status,
			},
			request.Now,
		)
		if err != nil {
			return 0, err
		}
		events = append(events, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	for _, event := range events {
		store.notifyDelivery(event)
	}
	return len(approvals), nil
}
