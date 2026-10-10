package postgres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	deliverydomain "github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryservice "github.com/Duang777/waybill-guardian/internal/delivery/service"
	"github.com/jackc/pgx/v5"
)

func (store *DeliveryStore) CommitProblem(
	ctx context.Context,
	command deliveryservice.CommitProblemTx,
) (deliverydomain.ProblemVersion, deliveryservice.Replay, error) {
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	defer tx.Rollback(context.Background())
	replayed, found, err := beginDeliveryCommand(
		ctx,
		tx,
		command.Problem.TenantID,
		"create_problem",
		command.IdempotencyKey,
		command.RequestDigest,
	)
	if err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	if found {
		value, err := decodeDeliveryReplay[deliverydomain.ProblemVersion](replayed)
		if err != nil {
			return deliverydomain.ProblemVersion{}, deliveryservice.Replay{},
				fmt.Errorf("decode problem replay: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
		}
		return value, deliveryservice.Replay{Replayed: true}, nil
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_problems (
			tenant_id,
			problem_id,
			latest_version,
			created_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (tenant_id, problem_id) DO NOTHING
	`, command.Problem.TenantID, command.Problem.ProblemID,
		command.Problem.Version, command.Problem.CreatedAt)
	if err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	if tag.RowsAffected() == 1 {
		if command.Problem.Version != 1 {
			return deliverydomain.ProblemVersion{}, deliveryservice.Replay{},
				deliveryservice.ErrConflict
		}
	} else {
		var latest uint64
		if err := tx.QueryRow(ctx, `
			SELECT latest_version
			FROM waybill.delivery_problems
			WHERE tenant_id = $1
			  AND problem_id = $2
			FOR UPDATE
		`, command.Problem.TenantID, command.Problem.ProblemID).Scan(&latest); err != nil {
			return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
		}
		if command.Problem.Version != latest+1 {
			return deliverydomain.ProblemVersion{}, deliveryservice.Replay{},
				deliveryservice.ErrConflict
		}
		if _, err := tx.Exec(ctx, `
			UPDATE waybill.delivery_problems
			SET latest_version = $3,
			    updated_at = $4
			WHERE tenant_id = $1
			  AND problem_id = $2
		`, command.Problem.TenantID, command.Problem.ProblemID,
			command.Problem.Version, command.Problem.CreatedAt); err != nil {
			return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_problem_versions (
			tenant_id,
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
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
		)
	`, command.Problem.TenantID, command.Problem.ProblemID, command.Problem.Version,
		command.Problem.ProblemDigest, command.Problem.PolicyDigest,
		command.Problem.CommitmentDigest, command.Problem.ManifestDigest,
		command.Problem.ProblemArtifact, command.Problem.SourceProfile,
		command.Problem.SourceRef, command.Problem.CreatedAt); err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	ownerID := string(command.Problem.ProblemID) + ":" +
		strconv.FormatUint(command.Problem.Version, 10)
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_artifact_refs (
			tenant_id,
			digest,
			owner_type,
			owner_id,
			role,
			created_at
		) VALUES ($1, $2, 'problem_version', $3, 'problem', $4)
	`, command.Problem.TenantID, command.Problem.ProblemArtifact, ownerID,
		command.Problem.CreatedAt); err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	event, err := appendDeliveryEvent(
		ctx,
		tx,
		command.Problem.TenantID,
		deliveryservice.AggregateProblem,
		string(command.Problem.ProblemID),
		deliveryservice.EventProblemCreated,
		command.Actor.Subject,
		command.Problem,
		command.Problem.CreatedAt,
	)
	if err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	if err := completeDeliveryCommand(
		ctx,
		tx,
		command.Problem.TenantID,
		"create_problem",
		command.IdempotencyKey,
		"problem_version",
		ownerID,
		command.Problem,
		command.Problem.CreatedAt,
	); err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.ProblemVersion{}, deliveryservice.Replay{}, err
	}
	store.notifyDelivery(event)
	return command.Problem, deliveryservice.Replay{}, nil
}

func (store *DeliveryStore) CreateRun(
	ctx context.Context,
	command deliveryservice.CreateRunTx,
) (deliverydomain.OptimizationRun, deliveryservice.Replay, error) {
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return deliverydomain.OptimizationRun{}, deliveryservice.Replay{}, err
	}
	defer tx.Rollback(context.Background())
	replayed, found, err := beginDeliveryCommand(
		ctx,
		tx,
		command.Run.TenantID,
		"request_optimization",
		command.IdempotencyKey,
		command.RequestDigest,
	)
	if err != nil {
		return deliverydomain.OptimizationRun{}, deliveryservice.Replay{}, err
	}
	if found {
		value, err := decodeDeliveryReplay[deliverydomain.OptimizationRun](replayed)
		if err != nil {
			return deliverydomain.OptimizationRun{}, deliveryservice.Replay{},
				fmt.Errorf("decode run replay: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return deliverydomain.OptimizationRun{}, deliveryservice.Replay{}, err
		}
		return value, deliveryservice.Replay{Replayed: true}, nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.delivery_runs (
			tenant_id,
			run_id,
			problem_id,
			problem_version,
			problem_digest,
			solver_profile,
			config_digest,
			requested_by,
			status,
			version,
			created_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
	`, command.Run.TenantID, command.Run.ID, command.Run.ProblemID,
		command.Run.ProblemVersion, command.Run.ProblemDigest,
		command.Run.SolverProfile, command.Run.ConfigDigest, command.Run.RequestedBy,
		command.Run.Status, command.Run.Version, command.Run.CreatedAt); err != nil {
		return deliverydomain.OptimizationRun{}, deliveryservice.Replay{}, err
	}
	event, err := appendDeliveryEvent(
		ctx,
		tx,
		command.Run.TenantID,
		deliveryservice.AggregateRun,
		string(command.Run.ID),
		deliveryservice.EventRunRequested,
		command.Actor.Subject,
		command.Run,
		command.Run.CreatedAt,
	)
	if err != nil {
		return deliverydomain.OptimizationRun{}, deliveryservice.Replay{}, err
	}
	if err := completeDeliveryCommand(
		ctx,
		tx,
		command.Run.TenantID,
		"request_optimization",
		command.IdempotencyKey,
		"optimization_run",
		string(command.Run.ID),
		command.Run,
		command.Run.CreatedAt,
	); err != nil {
		return deliverydomain.OptimizationRun{}, deliveryservice.Replay{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return deliverydomain.OptimizationRun{}, deliveryservice.Replay{}, err
	}
	store.notifyDelivery(event)
	return command.Run, deliveryservice.Replay{}, nil
}

func (store *DeliveryStore) ClaimRuns(
	ctx context.Context,
	request deliveryservice.ClaimRuns,
) ([]deliveryservice.RunClaim, error) {
	if request.TenantID == "" ||
		request.Limit <= 0 ||
		request.LeaseTTL <= 0 ||
		request.WorkerID == "" {
		return nil, fmt.Errorf("invalid delivery run claim")
	}
	tx, err := store.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	rows, err := tx.Query(ctx, deliveryRunSelect+`
		WHERE tenant_id = $1
		  AND status IN ('queued', 'solving', 'validating')
		  AND cancel_requested_at IS NULL
		  AND (lease_deadline IS NULL OR lease_deadline <= $2)
		ORDER BY created_at, tenant_id, run_id
		FOR UPDATE SKIP LOCKED
		LIMIT $3
	`, request.TenantID, request.Now, request.Limit)
	if err != nil {
		return nil, err
	}
	var runs []deliverydomain.OptimizationRun
	for rows.Next() {
		run, scanErr := scanDeliveryRun(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	claims := make([]deliveryservice.RunClaim, 0, len(runs))
	events := make([]deliveryservice.Event, 0, len(runs))
	for _, run := range runs {
		targetStatus := run.Status
		if targetStatus == deliverydomain.RunQueued {
			targetStatus = deliverydomain.RunSolving
		}
		deadline := request.Now.Add(request.LeaseTTL)
		updated, err := scanDeliveryRun(tx.QueryRow(ctx, `
			UPDATE waybill.delivery_runs
			SET status = $3,
			    version = version + 1,
			    lease_owner = $4,
			    lease_deadline = $5,
			    fencing_token = fencing_token + 1,
			    updated_at = $6
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
		`, run.TenantID, run.ID, targetStatus, request.WorkerID, deadline, request.Now))
		if err != nil {
			return nil, err
		}
		event, err := appendDeliveryEvent(
			ctx,
			tx,
			run.TenantID,
			deliveryservice.AggregateRun,
			string(run.ID),
			deliveryservice.EventRunClaimed,
			"system:delivery-run-worker",
			map[string]any{
				"worker_id":      request.WorkerID,
				"fencing_token":  updated.FencingToken,
				"lease_deadline": deadline,
			},
			request.Now,
		)
		if err != nil {
			return nil, err
		}
		claims = append(claims, deliveryservice.RunClaim{
			Run:          updated,
			WorkerID:     request.WorkerID,
			FencingToken: updated.FencingToken,
		})
		events = append(events, event)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	for _, event := range events {
		store.notifyDelivery(event)
	}
	return claims, nil
}

func (store *DeliveryStore) RenewRunLease(
	ctx context.Context,
	claim deliveryservice.RunClaim,
	deadline time.Time,
) error {
	tag, err := store.db.pool.Exec(ctx, `
		UPDATE waybill.delivery_runs
		SET lease_deadline = $5,
		    updated_at = $6
		WHERE tenant_id = $1
		  AND run_id = $2
		  AND lease_owner = $3
		  AND fencing_token = $4
		  AND status IN ('solving', 'validating')
	`, claim.Run.TenantID, claim.Run.ID, claim.WorkerID, claim.FencingToken,
		deadline, time.Now().UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrLeaseLost
	}
	return nil
}

func (store *DeliveryStore) SaveRunCheckpoint(
	ctx context.Context,
	claim deliveryservice.RunClaim,
	command deliveryservice.SaveCheckpointTx,
) error {
	if command.Status != deliverydomain.RunSolving &&
		command.Status != deliverydomain.RunValidating {
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
		    checkpoint_digest = COALESCE(NULLIF($6, ''), checkpoint_digest),
		    updated_at = $7
		WHERE tenant_id = $1
		  AND run_id = $2
		  AND lease_owner = $3
		  AND fencing_token = $4
		  AND (
		      (status = 'solving' AND $5 IN ('solving', 'validating'))
		      OR
		      (status = 'validating' AND $5 = 'validating')
		  )
	`, claim.Run.TenantID, claim.Run.ID, claim.WorkerID, claim.FencingToken,
		command.Status, command.CheckpointDigest, command.Now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return deliveryservice.ErrLeaseLost
	}
	if command.CheckpointDigest != "" {
		if _, err := tx.Exec(ctx, `
			INSERT INTO waybill.delivery_artifact_refs (
				tenant_id,
				digest,
				owner_type,
				owner_id,
				role,
				created_at
			) VALUES ($1, $2, 'run', $3, 'checkpoint', $4)
			ON CONFLICT DO NOTHING
		`, claim.Run.TenantID, command.CheckpointDigest, claim.Run.ID,
			command.Now); err != nil {
			return err
		}
	}
	event, err := appendDeliveryEvent(
		ctx,
		tx,
		claim.Run.TenantID,
		deliveryservice.AggregateRun,
		string(claim.Run.ID),
		deliveryservice.EventRunCheckpointed,
		"system:delivery-run-worker",
		map[string]any{
			"status":            command.Status,
			"checkpoint_digest": command.CheckpointDigest,
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

func deliveryTerminalStatus(status deliverydomain.RunStatus) bool {
	return status.Terminal()
}
