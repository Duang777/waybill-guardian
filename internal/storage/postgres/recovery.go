package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/jackc/pgx/v5"
)

var ErrRunQuarantined = errors.New("run is quarantined")

type RunQuarantinedError struct {
	RunID  domain.RunID
	Reason string
}

func (e *RunQuarantinedError) Error() string {
	return fmt.Sprintf("%s: %s: %s", ErrRunQuarantined, e.RunID, e.Reason)
}

func (e *RunQuarantinedError) Unwrap() error {
	return ErrRunQuarantined
}

type auditHead struct {
	runID    domain.RunID
	lastSeq  audit.Seq
	lastHash *string
}

func (r *Repository) PrepareRecovery(ctx context.Context) error {
	if err := r.checkOpen(); err != nil {
		return err
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT run.run_id
		FROM waybill.runs run
		WHERE run.tenant_id = $1
		ORDER BY run.run_id
	`, r.tenantID)
	if err != nil {
		return fmt.Errorf("list PostgreSQL runs for recovery: %w", err)
	}
	var runIDs []domain.RunID
	for rows.Next() {
		var runID domain.RunID
		if err := rows.Scan(&runID); err != nil {
			rows.Close()
			return fmt.Errorf("scan PostgreSQL recovery run: %w", err)
		}
		runIDs = append(runIDs, runID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read PostgreSQL recovery runs: %w", err)
	}
	rows.Close()

	for _, runID := range runIDs {
		if err := r.prepareRunRecovery(ctx, runID); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) prepareRunRecovery(ctx context.Context, runID domain.RunID) error {
	tx, err := r.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin PostgreSQL run verification: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()

	var head auditHead
	var incidentID domain.IncidentID
	var quarantined bool
	head.runID = runID
	if err := tx.QueryRow(ctx, `
		SELECT run.incident_id, run.last_audit_seq, run.last_audit_hash,
		       EXISTS (
		           SELECT 1
		           FROM waybill.run_quarantines quarantine
		           WHERE quarantine.tenant_id = run.tenant_id
		             AND quarantine.run_id = run.run_id
		       )
		FROM waybill.runs run
		WHERE run.tenant_id = $1 AND run.run_id = $2
		FOR UPDATE OF run
	`, r.tenantID, runID).Scan(
		&incidentID,
		&head.lastSeq,
		&head.lastHash,
		&quarantined,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return audit.ErrRunNotFound
		}
		return fmt.Errorf("lock PostgreSQL run for verification: %w", err)
	}
	if quarantined {
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("finish PostgreSQL run verification: %w", err)
		}
		return nil
	}

	rows, err := tx.Query(ctx, auditSelect+`
		WHERE tenant_id = $1 AND run_id = $2
		ORDER BY seq
	`, r.tenantID, runID)
	if err != nil {
		return fmt.Errorf("read PostgreSQL audit events for run %q: %w", runID, err)
	}
	events, err := scanEvents(rows)
	if err != nil {
		return err
	}
	if err := verifyAuditHead(head, events); err != nil {
		if quarantineErr := r.quarantineRun(
			ctx,
			tx,
			runID,
			incidentID,
			events,
			err,
		); quarantineErr != nil {
			return quarantineErr
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL run verification: %w", err)
	}
	return nil
}

func (r *Repository) RunProjection(
	ctx context.Context,
	runID domain.RunID,
) (audit.RunProjection, error) {
	if err := r.checkOpen(); err != nil {
		return audit.RunProjection{}, err
	}
	row := r.db.pool.QueryRow(ctx, `
		SELECT run_id, incident_id, waybill_id, status, last_audit_seq, updated_at
		FROM waybill.runs
		WHERE tenant_id = $1 AND run_id = $2
	`, r.tenantID, runID)
	projection, err := scanRunProjection(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return audit.RunProjection{}, audit.ErrRunNotFound
	}
	if err != nil {
		return audit.RunProjection{}, fmt.Errorf("read PostgreSQL run projection: %w", err)
	}
	return projection, nil
}

func (r *Repository) RunProjections(ctx context.Context) ([]audit.RunProjection, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	rows, err := r.db.pool.Query(ctx, `
		SELECT run_id, incident_id, waybill_id, status, last_audit_seq, updated_at
		FROM waybill.runs
		WHERE tenant_id = $1
		ORDER BY run_id
	`, r.tenantID)
	if err != nil {
		return nil, fmt.Errorf("list PostgreSQL run projections: %w", err)
	}
	defer rows.Close()

	var projections []audit.RunProjection
	for rows.Next() {
		projection, err := scanRunProjection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan PostgreSQL run projection: %w", err)
		}
		projections = append(projections, projection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read PostgreSQL run projections: %w", err)
	}
	return projections, nil
}

func (r *Repository) replayUnchecked(
	ctx context.Context,
	runID domain.RunID,
	after audit.Seq,
) ([]audit.Event, error) {
	rows, err := r.db.pool.Query(ctx, auditSelect+`
		WHERE tenant_id = $1 AND run_id = $2 AND seq > $3
		ORDER BY seq
	`, r.tenantID, runID, after)
	if err != nil {
		return nil, fmt.Errorf("read PostgreSQL audit events for run %q: %w", runID, err)
	}
	return scanEvents(rows)
}

func verifyAuditHead(head auditHead, events []audit.Event) error {
	if err := audit.VerifyEvents(head.runID, events); err != nil {
		return err
	}
	if len(events) == 0 {
		return fmt.Errorf("audit chain is empty")
	}
	last := events[len(events)-1]
	if last.Seq != head.lastSeq {
		return fmt.Errorf("audit head seq is %d, event tail is %d", head.lastSeq, last.Seq)
	}
	if head.lastHash == nil {
		return fmt.Errorf("audit head hash is missing")
	}
	if last.Hash != *head.lastHash {
		return fmt.Errorf("audit head hash does not match event tail")
	}
	return nil
}

func (r *Repository) quarantineRun(
	ctx context.Context,
	tx pgx.Tx,
	runID domain.RunID,
	incidentID domain.IncidentID,
	events []audit.Event,
	cause error,
) error {
	var observedSeq audit.Seq
	var observedHash *string
	if len(events) > 0 {
		last := events[len(events)-1]
		observedSeq = last.Seq
		observedHash = &last.Hash
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.run_quarantines (
			tenant_id, run_id, reason, detail_code, observed_seq, observed_hash
		) VALUES ($1, $2, $3, 'audit_integrity_invalid', $4, $5)
		ON CONFLICT (tenant_id, run_id) DO NOTHING
	`, r.tenantID, runID, cause.Error(), observedSeq, observedHash); err != nil {
		return fmt.Errorf("record PostgreSQL run quarantine: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.runs
		SET status = 'manual_review',
		    lease_owner = NULL,
		    lease_deadline = NULL,
		    updated_at = clock_timestamp(),
		    closed_at = COALESCE(closed_at, clock_timestamp())
		WHERE tenant_id = $1 AND run_id = $2
	`, r.tenantID, runID); err != nil {
		return fmt.Errorf("quarantine PostgreSQL run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.incidents
		SET status = 'manual_review', updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND incident_id = $2
	`, r.tenantID, incidentID); err != nil {
		return fmt.Errorf("quarantine PostgreSQL incident: %w", err)
	}
	return nil
}

func scanRunProjection(row rowScanner) (audit.RunProjection, error) {
	var projection audit.RunProjection
	err := row.Scan(
		&projection.RunID,
		&projection.IncidentID,
		&projection.WaybillID,
		&projection.Status,
		&projection.LastSeq,
		&projection.UpdatedAt,
	)
	projection.UpdatedAt = projection.UpdatedAt.UTC()
	return projection, err
}

var _ audit.RecoveryJournal = (*Repository)(nil)
