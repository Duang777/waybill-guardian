package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/jackc/pgx/v5"
)

var (
	ErrRunLeaseHeld  = errors.New("run lease is held by another worker")
	ErrStaleRunClaim = errors.New("run claim is stale")
)

type runClaim struct {
	repository *Repository
	runID      domain.RunID
	owner      string
	fence      int64
	deadline   time.Time
}

type runClaimContextKey struct{}

func (r *Repository) ClaimRun(ctx context.Context, runID domain.RunID) (*runClaim, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	var claim runClaim
	claim.repository = r
	claim.runID = runID
	claim.owner = r.workerID
	err := r.db.pool.QueryRow(ctx, `
		UPDATE waybill.runs
		SET lease_owner = $3,
		    lease_deadline = clock_timestamp() + make_interval(secs => $4),
		    fencing_token = fencing_token + 1,
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2
		  AND (lease_deadline IS NULL OR lease_deadline <= clock_timestamp())
		RETURNING fencing_token, lease_deadline
	`, r.tenantID, runID, r.workerID, r.leaseTTL.Seconds()).Scan(
		&claim.fence,
		&claim.deadline,
	)
	if err == nil {
		claim.deadline = claim.deadline.UTC()
		return &claim, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("claim PostgreSQL run %q: %w", runID, err)
	}
	var exists bool
	if err := r.db.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM waybill.runs WHERE tenant_id = $1 AND run_id = $2
		)
	`, r.tenantID, runID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("inspect PostgreSQL run lease: %w", err)
	}
	if !exists {
		return nil, audit.ErrRunNotFound
	}
	return nil, ErrRunLeaseHeld
}

func (r *Repository) RenewRun(ctx context.Context, claim *runClaim) error {
	if err := r.validateClaimOwner(claim); err != nil {
		return err
	}
	var deadline time.Time
	err := r.db.pool.QueryRow(ctx, `
		UPDATE waybill.runs
		SET lease_deadline = clock_timestamp() + make_interval(secs => $5),
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2
		  AND lease_owner = $3 AND fencing_token = $4
		  AND lease_deadline > clock_timestamp()
		RETURNING lease_deadline
	`, r.tenantID, claim.runID, claim.owner, claim.fence, r.leaseTTL.Seconds()).Scan(&deadline)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrStaleRunClaim
	}
	if err != nil {
		return fmt.Errorf("renew PostgreSQL run lease: %w", err)
	}
	claim.deadline = deadline.UTC()
	return nil
}

func (r *Repository) ReleaseRun(ctx context.Context, claim *runClaim) error {
	if err := r.validateClaimOwner(claim); err != nil {
		return err
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE waybill.runs
		SET lease_owner = NULL, lease_deadline = NULL, updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2
		  AND lease_owner = $3 AND fencing_token = $4
	`, r.tenantID, claim.runID, claim.owner, claim.fence)
	if err != nil {
		return fmt.Errorf("release PostgreSQL run lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleRunClaim
	}
	return nil
}

func (r *Repository) AcquireRun(
	ctx context.Context,
	runID domain.RunID,
) (context.Context, func() error, error) {
	claim, err := r.ClaimRun(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	leaseCtx = context.WithValue(leaseCtx, runClaimContextKey{}, claim)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(r.leaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(context.Background(), r.leaseTTL/3)
				err := r.RenewRun(renewCtx, claim)
				renewCancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	var released bool
	var releaseErr error
	return leaseCtx, func() error {
		if released {
			return releaseErr
		}
		released = true
		close(done)
		cancel()
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), r.leaseTTL/3)
		defer releaseCancel()
		releaseErr = r.ReleaseRun(releaseCtx, claim)
		return releaseErr
	}, nil
}

func (r *Repository) validateClaimOwner(claim *runClaim) error {
	if claim == nil || claim.repository != r || claim.runID == "" ||
		claim.owner != r.workerID || claim.fence <= 0 {
		return ErrStaleRunClaim
	}
	return nil
}

func claimFromContext(ctx context.Context) *runClaim {
	claim, _ := ctx.Value(runClaimContextKey{}).(*runClaim)
	return claim
}
