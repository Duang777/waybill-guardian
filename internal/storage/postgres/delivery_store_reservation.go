package postgres

import (
	"context"
	"errors"
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
