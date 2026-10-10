package postgres

import (
	"context"
	"errors"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

const deliveryApprovalSelect = `
	SELECT tenant_id,
	       approval_id,
	       plan_id,
	       revision_id,
	       COALESCE(base_revision_id, ''),
	       active_version,
	       problem_digest,
	       policy_digest,
	       commitment_digest,
	       plan_digest,
	       validation_report_digest,
	       effect_set_artifact_digest,
	       effect_set_digest,
	       status,
	       version,
	       requested_by,
	       plan_created_by,
	       reason,
	       requested_at,
	       expires_at,
	       COALESCE(decided_by, ''),
	       decided_at,
	       COALESCE(reject_reason, ''),
	       COALESCE(override_reason, ''),
	       COALESCE(execution_id, '')
	FROM waybill.delivery_approvals
`

func scanDeliveryApproval(scanner deliveryScanner) (deliverydomain.PlanApproval, error) {
	var value deliverydomain.PlanApproval
	value.Binding = deliverydomain.ApprovalBinding{}
	err := scanner.Scan(
		&value.TenantID,
		&value.ID,
		&value.Binding.PlanID,
		&value.Binding.RevisionID,
		&value.Binding.BaseRevisionID,
		&value.Binding.ActiveVersion,
		&value.Binding.ProblemDigest,
		&value.Binding.PolicyDigest,
		&value.Binding.CommitmentDigest,
		&value.Binding.PlanDigest,
		&value.Binding.ValidationReportDigest,
		&value.EffectSetArtifact,
		&value.Binding.EffectSetDigest,
		&value.Status,
		&value.Version,
		&value.RequestedBy,
		&value.PlanCreatedBy,
		&value.Reason,
		&value.RequestedAt,
		&value.ExpiresAt,
		&value.DecidedBy,
		&value.DecidedAt,
		&value.RejectReason,
		&value.OverrideReason,
		&value.ExecutionID,
	)
	value.Binding.TenantID = value.TenantID
	return value, err
}

const deliveryExecutionSelect = `
	SELECT tenant_id,
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
	FROM waybill.delivery_executions
`

func scanDeliveryExecution(scanner deliveryScanner) (deliverydomain.DispatchExecution, error) {
	var value deliverydomain.DispatchExecution
	err := scanner.Scan(
		&value.TenantID,
		&value.ID,
		&value.ApprovalID,
		&value.PlanID,
		&value.RevisionID,
		&value.EffectSetDigest,
		&value.Status,
		&value.Version,
		&value.CreatedAt,
		&value.UpdatedAt,
		&value.CompletedAt,
	)
	return value, err
}

const deliveryEffectSelect = `
	SELECT tenant_id,
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
	FROM waybill.delivery_effects
`

func scanDeliveryEffect(scanner deliveryScanner) (deliverydomain.EffectRecord, error) {
	var value deliverydomain.EffectRecord
	err := scanner.Scan(
		&value.TenantID,
		&value.ID,
		&value.ExecutionID,
		&value.RevisionID,
		&value.Ordinal,
		&value.Action,
		&value.Target,
		&value.Parameters,
		&value.ParametersDigest,
		&value.Required,
		&value.AdapterID,
		&value.ContractVersion,
		&value.AdapterBinding,
		&value.AdapterBindingDigest,
		&value.IdempotencyKey,
		&value.RequestDigest,
		&value.KeyCreatedAt,
		&value.KeyExpiresAt,
		&value.LookupConsistencyWindowSeconds,
		&value.Status,
		&value.NextOperation,
		&value.Attempt,
		&value.ExternalRef,
		&value.ResponseDigest,
		&value.ErrorCode,
		&value.RetryAt,
		&value.DispatchStartedAt,
		&value.LastLookupAt,
		&value.LeaseOwner,
		&value.LeaseDeadline,
		&value.FencingToken,
		&value.UpdatedAt,
	)
	return value, err
}

func (store *DeliveryStore) PrepareApproval(
	ctx context.Context,
	command deliveryservice.PrepareApprovalTx,
) (deliverydomain.PlanApproval, deliveryservice.Replay, error) {
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	defer tx.Rollback(context.Background())
	replayed, found, err := beginDeliveryCommand(
		ctx,
		tx,
		command.Approval.TenantID,
		"request_plan_approval",
		command.IdempotencyKey,
		command.RequestDigest,
	)
	if err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	if found {
		value, err := decodeDeliveryReplay[deliverydomain.PlanApproval](replayed)
		if err != nil {
			return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
		}
		return value, deliveryservice.Replay{Replayed: true}, nil
	}
	var (
		revisionStatus         deliverydomain.RevisionStatus
		revisionVersion        uint64
		planID                 deliverydomain.PlanID
		problemDigest          deliverydomain.ArtifactDigest
		policyDigest           deliverydomain.ArtifactDigest
		commitmentDigest       deliverydomain.ArtifactDigest
		planDigest             deliverydomain.ArtifactDigest
		validationReportDigest deliverydomain.ArtifactDigest
	)
	if err := tx.QueryRow(ctx, `
		SELECT status,
		       version,
		       plan_id,
		       problem_digest,
		       policy_digest,
		       commitment_digest,
		       plan_digest,
		       validation_report_digest
		FROM waybill.delivery_plan_revisions
		WHERE tenant_id = $1
		  AND revision_id = $2
		FOR UPDATE
	`, command.Approval.TenantID, command.Approval.Binding.RevisionID).Scan(
		&revisionStatus,
		&revisionVersion,
		&planID,
		&problemDigest,
		&policyDigest,
		&commitmentDigest,
		&planDigest,
		&validationReportDigest,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deliverydomain.PlanApproval{}, deliveryservice.Replay{},
				deliveryservice.ErrNotFound
		}
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	if revisionStatus != deliverydomain.RevisionValidated ||
		revisionVersion != command.ExpectedRevisionVersion ||
		planID != command.Approval.Binding.PlanID ||
		problemDigest != command.Approval.Binding.ProblemDigest ||
		policyDigest != command.Approval.Binding.PolicyDigest ||
		commitmentDigest != command.Approval.Binding.CommitmentDigest ||
		planDigest != command.Approval.Binding.PlanDigest ||
		validationReportDigest != command.Approval.Binding.ValidationReportDigest {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
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
	`, command.Approval.TenantID, planID).Scan(
		&activeRevision,
		&activeVersion,
	); err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	if activeRevision != command.Approval.Binding.BaseRevisionID ||
		activeVersion != command.Approval.Binding.ActiveVersion {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	approval := command.Approval
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_approvals (
			tenant_id, approval_id, plan_id, revision_id, base_revision_id,
			active_version, problem_digest, policy_digest, commitment_digest,
			plan_digest, validation_report_digest, effect_set_artifact_digest,
			effect_set_digest, status, version, requested_by, plan_created_by,
			reason, requested_at, expires_at
		) VALUES (
			$1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20
		)
	`, approval.TenantID, approval.ID, approval.Binding.PlanID,
		approval.Binding.RevisionID, approval.Binding.BaseRevisionID,
		approval.Binding.ActiveVersion, approval.Binding.ProblemDigest,
		approval.Binding.PolicyDigest, approval.Binding.CommitmentDigest,
		approval.Binding.PlanDigest, approval.Binding.ValidationReportDigest,
		approval.EffectSetArtifact, approval.Binding.EffectSetDigest,
		approval.Status, approval.Version, approval.RequestedBy, approval.PlanCreatedBy,
		approval.Reason, approval.RequestedAt, approval.ExpiresAt); err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_plan_revisions
		SET status = 'awaiting_approval',
		    version = version + 1,
		    effect_set_artifact_digest = $3,
		    effect_set_digest = $4,
		    updated_at = $5
		WHERE tenant_id = $1
		  AND revision_id = $2
	`, approval.TenantID, approval.Binding.RevisionID,
		approval.EffectSetArtifact, approval.Binding.EffectSetDigest,
		approval.RequestedAt); err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_artifact_refs (
			tenant_id, digest, owner_type, owner_id, role, created_at
		) VALUES ($1, $2, 'plan_revision', $3, 'effect_set', $4)
	`, approval.TenantID, approval.EffectSetArtifact,
		approval.Binding.RevisionID, approval.RequestedAt); err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	event, err := appendDeliveryEvent(
		ctx, tx, approval.TenantID, deliveryservice.AggregateRevision,
		string(approval.Binding.RevisionID), deliveryservice.EventApprovalRequested,
		command.Actor.Subject, approval, approval.RequestedAt,
	)
	if err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	if err := completeDeliveryCommand(
		ctx, tx, approval.TenantID, "request_plan_approval",
		command.IdempotencyKey, "approval", string(approval.ID), approval,
		approval.RequestedAt,
	); err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.PlanApproval{}, deliveryservice.Replay{}, err
	}
	store.notifyDelivery(event)
	return approval, deliveryservice.Replay{}, nil
}

func (store *DeliveryStore) DecideAndCreateExecution(
	ctx context.Context,
	command deliveryservice.DecideExecutionTx,
) (deliverydomain.DispatchExecution, deliveryservice.Replay, error) {
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	defer tx.Rollback(context.Background())
	replayed, found, err := beginDeliveryCommand(
		ctx, tx, command.TenantID, "decide_approval",
		command.IdempotencyKey, command.RequestDigest,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if command.Decision == deliveryservice.RejectApproval {
		approval, getErr := store.getApprovalForDecision(
			ctx, tx, command.TenantID, command.ApprovalID,
		)
		if getErr != nil {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, getErr
		}
		if found {
			if err := tx.Commit(ctx); err != nil {
				return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
			}
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{Replayed: true}, nil
		}
		if approval.TenantID == "" {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
				deliveryservice.ErrNotFound
		}
		return store.rejectApproval(ctx, tx, command, approval)
	}
	if found {
		value, err := decodeDeliveryReplay[deliverydomain.DispatchExecution](replayed)
		if err != nil {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
		}
		return value, deliveryservice.Replay{Replayed: true}, nil
	}
	approval, err := store.getApprovalForDecision(
		ctx, tx, command.TenantID, command.ApprovalID,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if approval.TenantID != command.Execution.TenantID ||
		approval.Version != command.ExpectedVersion ||
		approval.Status != deliverydomain.ApprovalPending ||
		command.Execution.ApprovalID != approval.ID ||
		command.Execution.PlanID != approval.Binding.PlanID ||
		command.Execution.RevisionID != approval.Binding.RevisionID ||
		approval.Binding.EffectSetDigest != command.Execution.EffectSetDigest {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	if !command.Now.Before(approval.ExpiresAt) {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrApprovalExpired
	}
	if approval.PlanCreatedBy == command.Actor.Subject && command.OverrideReason == "" {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrSeparationOfDuties
	}
	if err := store.verifyApprovalStillCurrent(
		ctx,
		tx,
		approval,
		command.ExpectedActiveRevisionID,
		command.ExpectedActiveVersion,
	); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	execution := command.Execution
	for _, effect := range command.Effects {
		if effect.TenantID != execution.TenantID ||
			effect.ExecutionID != execution.ID ||
			effect.RevisionID != execution.RevisionID ||
			effect.Status != deliverydomain.EffectPrepared ||
			effect.NextOperation != deliverydomain.EffectOperationDispatch {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
				deliveryservice.ErrConflict
		}
	}
	if err := reserveExecution(ctx, tx, command, approval); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_executions (
			tenant_id, execution_id, approval_id, plan_id, revision_id,
			effect_set_digest, status, version, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
	`, execution.TenantID, execution.ID, execution.ApprovalID,
		execution.PlanID, execution.RevisionID, execution.EffectSetDigest,
		execution.Status, execution.Version, execution.CreatedAt); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	for _, effect := range command.Effects {
		if _, err := tx.Exec(ctx, `
			INSERT INTO waybill.delivery_effects (
				tenant_id, effect_id, execution_id, revision_id, ordinal,
				action, target, parameters, parameters_digest, required,
				adapter_id, contract_version, adapter_binding,
				adapter_binding_digest, idempotency_key, request_digest,
				key_created_at, key_expires_at, lookup_consistency_window_seconds,
				status, next_operation, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10,
				$11, $12, $13::jsonb, $14, $15, $16, $17, $18, $19,
				$20, $21, $22
			)
		`, effect.TenantID, effect.ID, effect.ExecutionID, effect.RevisionID,
			effect.Ordinal, effect.Action, effect.Target, []byte(effect.Parameters),
			effect.ParametersDigest, effect.Required, effect.AdapterID,
			effect.ContractVersion, []byte(effect.AdapterBinding),
			effect.AdapterBindingDigest, effect.IdempotencyKey, effect.RequestDigest,
			effect.KeyCreatedAt, effect.KeyExpiresAt,
			effect.LookupConsistencyWindowSeconds, effect.Status,
			effect.NextOperation, effect.UpdatedAt); err != nil {
			return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_approvals
		SET status = 'confirmed',
		    version = version + 1,
		    decided_by = $3,
		    decided_at = $4,
		    execution_id = $5,
		    override_reason = NULLIF($6, '')
		WHERE tenant_id = $1 AND approval_id = $2
	`, approval.TenantID, approval.ID, command.Actor.Subject, command.Now,
		execution.ID, command.OverrideReason); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_plan_revisions
		SET status = 'approved',
		    version = version + 1,
		    updated_at = $3
		WHERE tenant_id = $1 AND revision_id = $2
	`, approval.TenantID, approval.Binding.RevisionID, command.Now); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	approvalEvent, err := appendDeliveryEvent(
		ctx, tx, execution.TenantID, deliveryservice.AggregateRevision,
		string(execution.RevisionID), deliveryservice.EventApprovalDecided,
		command.Actor.Subject, map[string]any{
			"approval_id":     approval.ID,
			"decision":        "confirm",
			"execution_id":    execution.ID,
			"override_reason": command.OverrideReason,
		}, command.Now,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	executionEvent, err := appendDeliveryEvent(
		ctx, tx, execution.TenantID, deliveryservice.AggregateExecution,
		string(execution.ID), deliveryservice.EventExecutionCreated,
		command.Actor.Subject, execution, command.Now,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if err := completeDeliveryCommand(
		ctx, tx, execution.TenantID, "decide_approval",
		command.IdempotencyKey, "execution", string(execution.ID), execution,
		command.Now,
	); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	store.notifyDelivery(approvalEvent)
	store.notifyDelivery(executionEvent)
	return execution, deliveryservice.Replay{}, nil
}

func (store *DeliveryStore) getApprovalForDecision(
	ctx context.Context,
	tx pgx.Tx,
	tenantID deliverydomain.TenantID,
	approvalID deliverydomain.ApprovalID,
) (deliverydomain.PlanApproval, error) {
	value, err := scanDeliveryApproval(tx.QueryRow(ctx, deliveryApprovalSelect+`
		WHERE tenant_id = $1
		  AND approval_id = $2
		FOR UPDATE
	`, tenantID, approvalID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.PlanApproval{}, deliveryservice.ErrNotFound
	}
	return value, err
}

func (store *DeliveryStore) verifyApprovalStillCurrent(
	ctx context.Context,
	tx pgx.Tx,
	approval deliverydomain.PlanApproval,
	expectedActiveRevisionID deliverydomain.PlanRevisionID,
	expectedActiveVersion uint64,
) error {
	if expectedActiveRevisionID != approval.Binding.BaseRevisionID ||
		expectedActiveVersion != approval.Binding.ActiveVersion {
		return deliveryservice.ErrApprovalStale
	}
	var (
		activeRevision          deliverydomain.PlanRevisionID
		activeVersion           uint64
		revisionStatus          deliverydomain.RevisionStatus
		problemDigest           deliverydomain.ArtifactDigest
		policyDigest            deliverydomain.ArtifactDigest
		commitmentDigest        deliverydomain.ArtifactDigest
		planDigest              deliverydomain.ArtifactDigest
		validationReportDigest  deliverydomain.ArtifactDigest
		effectSetArtifactDigest deliverydomain.ArtifactDigest
		effectSetDigest         deliverydomain.ArtifactDigest
	)
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(plan.active_revision_id, ''),
		       plan.active_version,
		       revision.status,
		       revision.problem_digest,
		       revision.policy_digest,
		       revision.commitment_digest,
		       revision.plan_digest,
		       revision.validation_report_digest,
		       COALESCE(revision.effect_set_artifact_digest, ''),
		       COALESCE(revision.effect_set_digest, '')
		FROM waybill.delivery_plans plan
		JOIN waybill.delivery_plan_revisions revision
		  ON revision.tenant_id = plan.tenant_id
		 AND revision.revision_id = $3
		WHERE plan.tenant_id = $1 AND plan.plan_id = $2
		FOR UPDATE OF plan, revision
	`, approval.TenantID, approval.Binding.PlanID,
		approval.Binding.RevisionID).Scan(
		&activeRevision,
		&activeVersion,
		&revisionStatus,
		&problemDigest,
		&policyDigest,
		&commitmentDigest,
		&planDigest,
		&validationReportDigest,
		&effectSetArtifactDigest,
		&effectSetDigest,
	); err != nil {
		return err
	}
	if activeRevision != expectedActiveRevisionID ||
		activeVersion != expectedActiveVersion ||
		revisionStatus != deliverydomain.RevisionAwaitingApproval ||
		problemDigest != approval.Binding.ProblemDigest ||
		policyDigest != approval.Binding.PolicyDigest ||
		commitmentDigest != approval.Binding.CommitmentDigest ||
		planDigest != approval.Binding.PlanDigest ||
		validationReportDigest != approval.Binding.ValidationReportDigest ||
		effectSetArtifactDigest != approval.EffectSetArtifact ||
		effectSetDigest != approval.Binding.EffectSetDigest {
		return deliveryservice.ErrApprovalStale
	}
	return nil
}

func (store *DeliveryStore) rejectApproval(
	ctx context.Context,
	tx pgx.Tx,
	command deliveryservice.DecideExecutionTx,
	approval deliverydomain.PlanApproval,
) (deliverydomain.DispatchExecution, deliveryservice.Replay, error) {
	if approval.Version != command.ExpectedVersion ||
		approval.Status != deliverydomain.ApprovalPending {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrConflict
	}
	if !command.Now.Before(approval.ExpiresAt) {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{},
			deliveryservice.ErrApprovalExpired
	}
	if err := store.verifyApprovalStillCurrent(
		ctx,
		tx,
		approval,
		command.ExpectedActiveRevisionID,
		command.ExpectedActiveVersion,
	); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_approvals
		SET status = 'rejected',
		    version = version + 1,
		    decided_by = $3,
		    decided_at = $4,
		    reject_reason = $5
		WHERE tenant_id = $1 AND approval_id = $2
	`, approval.TenantID, approval.ID, command.Actor.Subject, command.Now,
		command.RejectReason); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_plan_revisions
		SET status = 'rejected', version = version + 1, updated_at = $3
		WHERE tenant_id = $1 AND revision_id = $2
	`, approval.TenantID, approval.Binding.RevisionID, command.Now); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	result := deliverydomain.DispatchExecution{}
	if err := completeDeliveryCommand(
		ctx, tx, approval.TenantID, "decide_approval",
		command.IdempotencyKey, "approval", string(approval.ID), result, command.Now,
	); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	event, err := appendDeliveryEvent(
		ctx, tx, approval.TenantID, deliveryservice.AggregateRevision,
		string(approval.Binding.RevisionID), deliveryservice.EventApprovalDecided,
		command.Actor.Subject, map[string]any{
			"approval_id": approval.ID,
			"decision":    "reject",
			"reason":      command.RejectReason,
		}, command.Now,
	)
	if err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.DispatchExecution{}, deliveryservice.Replay{}, err
	}
	store.notifyDelivery(event)
	return result, deliveryservice.Replay{}, nil
}
