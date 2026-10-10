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

func (store *DeliveryStore) PublishRevision(
	ctx context.Context,
	claim deliveryservice.RunClaim,
	command deliveryservice.PublishRevisionTx,
) (deliverydomain.PlanRevision, error) {
	if command.RunStatus != deliverydomain.RunSucceeded &&
		command.RunStatus != deliverydomain.RunCandidateRejected {
		return deliverydomain.PlanRevision{}, deliveryservice.ErrConflict
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliverydomain.PlanRevision{}, err
	}
	defer tx.Rollback(context.Background())
	var currentStatus deliverydomain.RunStatus
	if err := tx.QueryRow(ctx, `
		SELECT status
		FROM waybill.delivery_runs
		WHERE tenant_id = $1
		  AND run_id = $2
		  AND lease_owner = $3
		  AND fencing_token = $4
		FOR UPDATE
	`, claim.Run.TenantID, claim.Run.ID, claim.WorkerID,
		claim.FencingToken).Scan(&currentStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deliverydomain.PlanRevision{}, deliveryservice.ErrLeaseLost
		}
		return deliverydomain.PlanRevision{}, err
	}
	if currentStatus != deliverydomain.RunValidating {
		return deliverydomain.PlanRevision{}, deliveryservice.ErrConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_plans (
			tenant_id,
			plan_id,
			problem_id,
			active_version,
			created_at,
			updated_at
		) VALUES ($1, $2, $3, 0, $4, $4)
	`, command.Plan.TenantID, command.Plan.ID, command.Plan.ProblemID,
		command.Plan.CreatedAt); err != nil {
		return deliverydomain.PlanRevision{}, fmt.Errorf("insert delivery plan: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_plan_revisions (
			tenant_id,
			revision_id,
			plan_id,
			base_revision_id,
			run_id,
			problem_digest,
			policy_digest,
			commitment_digest,
			plan_artifact_digest,
			plan_digest,
			validation_artifact_digest,
			validation_report_digest,
			effect_set_artifact_digest,
			effect_set_digest,
			status,
			version,
			created_at,
			updated_at
		) VALUES (
			$1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9,
			$10, $11, $12, NULLIF($13, ''), NULLIF($14, ''),
			$15, $16, $17, $18
		)
	`, command.Revision.TenantID, command.Revision.ID, command.Revision.PlanID,
		command.Revision.BaseRevisionID, command.Revision.RunID,
		command.Revision.ProblemDigest, command.Revision.PolicyDigest,
		command.Revision.CommitmentDigest, command.Revision.PlanArtifactDigest,
		command.Revision.PlanDigest, command.Revision.ValidationArtifact,
		command.Revision.ValidationReportDigest, command.Revision.EffectSetArtifact,
		command.Revision.EffectSetDigest, command.Revision.Status,
		command.Revision.Version, command.Revision.CreatedAt,
		command.Revision.UpdatedAt); err != nil {
		return deliverydomain.PlanRevision{}, fmt.Errorf("insert delivery plan revision: %w", err)
	}
	artifactRefs := []struct {
		digest deliverydomain.ArtifactDigest
		role   string
	}{
		{command.Revision.PlanArtifactDigest, "plan"},
		{command.Revision.ValidationArtifact, "validation_report"},
	}
	if command.Revision.EffectSetArtifact != "" {
		artifactRefs = append(artifactRefs, struct {
			digest deliverydomain.ArtifactDigest
			role   string
		}{command.Revision.EffectSetArtifact, "effect_set"})
	}
	for _, ref := range artifactRefs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO waybill.delivery_artifact_refs (
				tenant_id,
				digest,
				owner_type,
				owner_id,
				role,
				created_at
			) VALUES ($1, $2, 'plan_revision', $3, $4, $5)
		`, command.Revision.TenantID, ref.digest, command.Revision.ID,
			ref.role, command.Now); err != nil {
			return deliverydomain.PlanRevision{}, err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_runs
		SET status = $5,
		    version = version + 1,
		    result_revision_id = $6,
		    lease_owner = NULL,
		    lease_deadline = NULL,
		    updated_at = $7,
		    closed_at = $7
		WHERE tenant_id = $1
		  AND run_id = $2
		  AND lease_owner = $3
		  AND fencing_token = $4
		  AND status = 'validating'
	`, claim.Run.TenantID, claim.Run.ID, claim.WorkerID, claim.FencingToken,
		command.RunStatus, command.Revision.ID, command.Now)
	if err != nil {
		return deliverydomain.PlanRevision{}, err
	}
	if tag.RowsAffected() != 1 {
		return deliverydomain.PlanRevision{}, deliveryservice.ErrLeaseLost
	}
	revisionEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		command.Revision.TenantID,
		deliveryservice.AggregateRevision,
		string(command.Revision.ID),
		deliveryservice.EventRevisionPublished,
		command.Actor.Subject,
		command.Revision,
		command.Now,
	)
	if err != nil {
		return deliverydomain.PlanRevision{}, err
	}
	runEvent, err := appendDeliveryEvent(
		ctx,
		tx,
		claim.Run.TenantID,
		deliveryservice.AggregateRun,
		string(claim.Run.ID),
		deliveryservice.EventRevisionPublished,
		command.Actor.Subject,
		map[string]any{
			"revision_id": command.Revision.ID,
			"status":      command.RunStatus,
		},
		command.Now,
	)
	if err != nil {
		return deliverydomain.PlanRevision{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.PlanRevision{}, err
	}
	store.notifyDelivery(revisionEvent)
	store.notifyDelivery(runEvent)
	return command.Revision, nil
}

func (store *DeliveryStore) FinishRun(
	ctx context.Context,
	claim deliveryservice.RunClaim,
	command deliveryservice.FinishRunTx,
) error {
	if !command.Status.Terminal() ||
		command.Status == deliverydomain.RunSucceeded ||
		command.Status == deliverydomain.RunCandidateRejected {
		return deliveryservice.ErrConflict
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	tag, err := tx.Exec(ctx, `
		UPDATE waybill.delivery_runs
		SET status = $5,
		    version = version + 1,
		    failure_code = NULLIF($6, ''),
		    lease_owner = NULL,
		    lease_deadline = NULL,
		    updated_at = $7,
		    closed_at = $7
		WHERE tenant_id = $1
		  AND run_id = $2
		  AND lease_owner = $3
		  AND fencing_token = $4
		  AND status IN ('solving', 'validating')
	`, claim.Run.TenantID, claim.Run.ID, claim.WorkerID, claim.FencingToken,
		command.Status, command.FailureCode, command.Now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrLeaseLost
	}
	event, err := appendDeliveryEvent(
		ctx,
		tx,
		claim.Run.TenantID,
		deliveryservice.AggregateRun,
		string(claim.Run.ID),
		deliveryservice.EventRunFailed,
		command.Actor.Subject,
		map[string]any{
			"status":       command.Status,
			"failure_code": command.FailureCode,
		},
		command.Now,
	)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	store.notifyDelivery(event)
	return nil
}

func (store *DeliveryStore) RequestRunCancellation(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	runID deliverydomain.OptimizationRunID,
	expectedVersion uint64,
	actor deliveryservice.Actor,
	now time.Time,
) (deliverydomain.OptimizationRun, error) {
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliverydomain.OptimizationRun{}, err
	}
	defer tx.Rollback(context.Background())
	current, err := scanDeliveryRun(tx.QueryRow(ctx, deliveryRunSelect+`
		WHERE tenant_id = $1
		  AND run_id = $2
		FOR UPDATE
	`, tenantID, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.OptimizationRun{}, deliveryservice.ErrNotFound
	}
	if err != nil {
		return deliverydomain.OptimizationRun{}, err
	}
	if current.Version != expectedVersion || current.Status.Terminal() {
		return deliverydomain.OptimizationRun{}, deliveryservice.ErrConflict
	}
	targetStatus := current.Status
	var closedAt *time.Time
	if current.Status == deliverydomain.RunQueued {
		targetStatus = deliverydomain.RunCancelled
		closedAt = &now
	}
	updated, err := scanDeliveryRun(tx.QueryRow(ctx, `
		UPDATE waybill.delivery_runs
		SET status = $3,
		    version = version + 1,
		    cancel_requested_at = $4,
		    lease_owner = CASE WHEN $3 = 'cancelled' THEN NULL ELSE lease_owner END,
		    lease_deadline = CASE WHEN $3 = 'cancelled' THEN NULL ELSE lease_deadline END,
		    updated_at = $4,
		    closed_at = $5
		WHERE tenant_id = $1
		  AND run_id = $2
		RETURNING tenant_id,
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
	`, tenantID, runID, targetStatus, now, closedAt))
	if err != nil {
		return deliverydomain.OptimizationRun{}, err
	}
	event, err := appendDeliveryEvent(
		ctx,
		tx,
		tenantID,
		deliveryservice.AggregateRun,
		string(runID),
		deliveryservice.EventRunCancelRequested,
		actor.Subject,
		map[string]any{"status": targetStatus},
		now,
	)
	if err != nil {
		return deliverydomain.OptimizationRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.OptimizationRun{}, err
	}
	store.notifyDelivery(event)
	return updated, nil
}
