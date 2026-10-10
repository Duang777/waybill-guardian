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
	query := `
		SELECT run_id, status
		FROM waybill.delivery_runs
		WHERE status IN ('queued', 'solving', 'validating')
		  AND (lease_deadline IS NULL OR lease_deadline <= $1)
	`
	args := []any{request.Now, request.Limit}
	if request.TenantID != "" {
		query += ` AND tenant_id = $3`
		args = append(args, request.TenantID)
	}
	query += ` ORDER BY updated_at, tenant_id, run_id LIMIT $2`
	rows, err := store.db.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []deliveryservice.RecoveryItem
	for rows.Next() {
		var item deliveryservice.RecoveryItem
		item.Kind = "run"
		if err := rows.Scan(&item.ResourceID, &item.Status); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
