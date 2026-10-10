package postgres

import (
	"context"
	"errors"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

func (store *DeliveryStore) GetProblem(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	problemID deliverydomain.ProblemID,
	version uint64,
) (deliverydomain.ProblemVersion, error) {
	value, err := scanDeliveryProblem(store.db.pool.QueryRow(ctx, deliveryProblemSelect+`
		WHERE tenant_id = $1
		  AND problem_id = $2
		  AND version = $3
	`, tenantID, problemID, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.ProblemVersion{}, deliveryservice.ErrNotFound
	}
	return value, err
}

func (store *DeliveryStore) GetRun(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	runID deliverydomain.OptimizationRunID,
) (deliverydomain.OptimizationRun, error) {
	value, err := scanDeliveryRun(store.db.pool.QueryRow(ctx, deliveryRunSelect+`
		WHERE tenant_id = $1
		  AND run_id = $2
	`, tenantID, runID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.OptimizationRun{}, deliveryservice.ErrNotFound
	}
	return value, err
}

func (store *DeliveryStore) GetPlan(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	planID deliverydomain.PlanID,
) (deliverydomain.DispatchPlan, error) {
	value, err := scanDeliveryPlan(store.db.pool.QueryRow(ctx, deliveryPlanSelect+`
		WHERE tenant_id = $1
		  AND plan_id = $2
	`, tenantID, planID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.DispatchPlan{}, deliveryservice.ErrNotFound
	}
	return value, err
}

func (store *DeliveryStore) GetRevision(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	revisionID deliverydomain.PlanRevisionID,
) (deliverydomain.PlanRevision, error) {
	value, err := scanDeliveryRevision(store.db.pool.QueryRow(ctx, deliveryRevisionSelect+`
		WHERE tenant_id = $1
		  AND revision_id = $2
	`, tenantID, revisionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return deliverydomain.PlanRevision{}, deliveryservice.ErrNotFound
	}
	return value, err
}

func (store *DeliveryStore) ArtifactReachable(
	ctx context.Context,
	tenantID deliverydomain.TenantID,
	digest deliverydomain.ArtifactDigest,
) (bool, error) {
	var reachable bool
	if err := store.db.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM waybill.delivery_artifact_refs
			WHERE tenant_id = $1
			  AND digest = $2
		)
	`, tenantID, digest).Scan(&reachable); err != nil {
		return false, err
	}
	return reachable, nil
}

func (store *DeliveryStore) ScanRecovery(
	ctx context.Context,
	request deliveryservice.RecoveryScan,
) ([]deliveryservice.RecoveryItem, error) {
	if request.Limit <= 0 {
		return nil, deliveryservice.ErrConflict
	}
	rows, err := store.db.pool.Query(ctx, `
		SELECT tenant_id, kind, resource_id, status
		FROM (
			SELECT 'run'::text AS kind,
			       run_id AS resource_id,
			       status,
			       updated_at,
			       tenant_id
			FROM waybill.delivery_runs
			WHERE status IN ('queued', 'solving', 'validating')
			  AND (lease_deadline IS NULL OR lease_deadline <= $1)
			  AND ($3 = '' OR tenant_id = $3)

			UNION ALL

			SELECT 'execution_ready'::text AS kind,
			       execution.execution_id AS resource_id,
			       execution.status,
			       execution.updated_at,
			       execution.tenant_id
			FROM waybill.delivery_executions execution
			JOIN waybill.delivery_execution_reservations reservation
			  ON reservation.tenant_id = execution.tenant_id
			 AND reservation.execution_id = execution.execution_id
			WHERE execution.status IN (
			    'prepared',
			    'executing',
			    'reconciliation_required'
			)
			  AND reservation.status IN (
			      'reserved',
			      'reconciliation_required'
			  )
			  AND ($3 = '' OR execution.tenant_id = $3)
			  AND NOT EXISTS (
			      SELECT 1
			      FROM waybill.delivery_effects effect
			      WHERE effect.tenant_id = execution.tenant_id
			        AND effect.execution_id = execution.execution_id
			        AND effect.required
			        AND effect.status <> 'succeeded'
			  )
		) recovery
		ORDER BY updated_at, tenant_id, kind, resource_id
		LIMIT $2
	`, request.Now, request.Limit, request.TenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []deliveryservice.RecoveryItem
	for rows.Next() {
		var item deliveryservice.RecoveryItem
		if err := rows.Scan(
			&item.TenantID,
			&item.Kind,
			&item.ResourceID,
			&item.Status,
		); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
