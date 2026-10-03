package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Duang777/waybill-guardian/internal/outbox"
)

var (
	ErrStaleOutboxClaim     = errors.New("outbox claim is stale")
	ErrOutboxNotRequeueable = errors.New("outbox event is not permanently failed")
)

var _ outbox.Store = (*Repository)(nil)

type outboxClaim struct {
	event      outbox.Event
	repository *Repository
	source     string
	eventID    string
	owner      string
	fence      int64
}

func (c *outboxClaim) Event() outbox.Event {
	if c == nil {
		return outbox.Event{}
	}
	return c.event.Clone()
}

func (r *Repository) ClaimOutbox(ctx context.Context, limit int) ([]outbox.Claim, error) {
	if err := r.checkOpen(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		return nil, fmt.Errorf("outbox claim limit must be between 1 and 100")
	}
	rows, err := r.db.pool.Query(ctx, `
		WITH candidates AS MATERIALIZED (
			SELECT candidate.source, candidate.event_id
			FROM waybill.outbox_events candidate
			WHERE candidate.tenant_id = $1
			  AND candidate.available_at <= clock_timestamp()
			  AND (
			      candidate.status IN ('pending', 'retryable_failed')
			      OR (
			          candidate.status = 'publishing'
			          AND candidate.lease_deadline <= clock_timestamp()
			      )
			  )
			  AND (
			      candidate.aggregate_type <> 'run'
			      OR NOT EXISTS (
			          SELECT 1
			          FROM waybill.run_quarantines quarantine
			          WHERE quarantine.tenant_id = candidate.tenant_id
			            AND quarantine.run_id = candidate.aggregate_id
			      )
			  )
			  AND NOT EXISTS (
			      SELECT 1
			      FROM waybill.outbox_events predecessor
			      WHERE predecessor.tenant_id = candidate.tenant_id
			        AND predecessor.aggregate_type = candidate.aggregate_type
			        AND predecessor.aggregate_id = candidate.aggregate_id
			        AND predecessor.aggregate_version < candidate.aggregate_version
			        AND predecessor.status <> 'published'
			  )
			ORDER BY candidate.created_at, candidate.source, candidate.event_id
			FOR UPDATE OF candidate SKIP LOCKED
			LIMIT $3
		),
		claimed AS (
			UPDATE waybill.outbox_events event
			SET status = 'publishing',
			    attempt = event.attempt + 1,
			    lease_owner = $2,
			    lease_deadline = clock_timestamp() + make_interval(secs => $4),
			    fencing_token = event.fencing_token + 1,
			    last_error_code = NULL
			FROM candidates
			WHERE event.tenant_id = $1
			  AND event.source = candidates.source
			  AND event.event_id = candidates.event_id
			RETURNING event.source, event.event_id, event.aggregate_type,
			          event.aggregate_id, event.aggregate_version, event.event_type,
			          COALESCE(event.subject, '') AS subject,
			          event.event_time, event.data_content_type, event.data_schema,
			          event.payload_canonical, event.attempt, event.created_at,
			          event.fencing_token
		)
		SELECT source, event_id, aggregate_type, aggregate_id, aggregate_version,
		       event_type, subject, event_time, data_content_type, data_schema,
		       payload_canonical, attempt, created_at, fencing_token
		FROM claimed
		ORDER BY created_at, source, event_id
	`, r.tenantID, r.workerID, limit, r.outboxLeaseTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim PostgreSQL outbox events: %w", err)
	}
	defer rows.Close()

	claims := make([]outbox.Claim, 0, limit)
	for rows.Next() {
		var claim outboxClaim
		var payload []byte
		claim.repository = r
		claim.owner = r.workerID
		if err := rows.Scan(
			&claim.event.Source,
			&claim.event.ID,
			&claim.event.AggregateType,
			&claim.event.AggregateID,
			&claim.event.AggregateVersion,
			&claim.event.Type,
			&claim.event.Subject,
			&claim.event.Time,
			&claim.event.DataContentType,
			&claim.event.DataSchema,
			&payload,
			&claim.event.Attempt,
			&claim.event.CreatedAt,
			&claim.fence,
		); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL outbox claim: %w", err)
		}
		claim.event.Data = append(json.RawMessage(nil), payload...)
		claim.event.Time = claim.event.Time.UTC()
		claim.event.CreatedAt = claim.event.CreatedAt.UTC()
		claim.source = claim.event.Source
		claim.eventID = claim.event.ID
		claims = append(claims, &claim)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read PostgreSQL outbox claims: %w", err)
	}
	return claims, nil
}

func (r *Repository) RenewOutbox(ctx context.Context, claim outbox.Claim) error {
	if err := r.validateOutboxClaim(claim); err != nil {
		return err
	}
	storedClaim := claim.(*outboxClaim)
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE waybill.outbox_events
		SET lease_deadline = clock_timestamp() + make_interval(secs => $6)
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
		  AND status = 'publishing'
		  AND lease_owner = $4 AND fencing_token = $5
		  AND lease_deadline > clock_timestamp()
	`, r.tenantID, storedClaim.source, storedClaim.eventID, storedClaim.owner,
		storedClaim.fence, r.outboxLeaseTTL.Seconds())
	if err != nil {
		return fmt.Errorf("renew PostgreSQL outbox lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutboxClaim
	}
	return nil
}

func (r *Repository) CompleteOutbox(
	ctx context.Context,
	claim outbox.Claim,
	result outbox.Completion,
) error {
	if err := r.validateOutboxClaim(claim); err != nil {
		return err
	}
	if err := result.Validate(); err != nil {
		return err
	}
	storedClaim := claim.(*outboxClaim)
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE waybill.outbox_events
		SET status = $6,
		    available_at = CASE
		        WHEN $6 = 'retryable_failed'
		        THEN clock_timestamp() + make_interval(secs => $7)
		        ELSE available_at
		    END,
		    published_at = CASE
		        WHEN $6 = 'published' THEN clock_timestamp()
		        ELSE NULL
		    END,
		    last_error_code = NULLIF($8, ''),
		    lease_owner = NULL,
		    lease_deadline = NULL
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
		  AND status = 'publishing'
		  AND lease_owner = $4 AND fencing_token = $5
		  AND lease_deadline > clock_timestamp()
	`, r.tenantID, storedClaim.source, storedClaim.eventID, storedClaim.owner,
		storedClaim.fence,
		result.Disposition, result.RetryAfter.Seconds(), result.ErrorCode)
	if err != nil {
		return fmt.Errorf("complete PostgreSQL outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutboxClaim
	}
	return nil
}

func (r *Repository) OutboxStats(ctx context.Context) (outbox.Stats, error) {
	if err := r.checkOpen(); err != nil {
		return outbox.Stats{}, err
	}
	var stats outbox.Stats
	if err := r.db.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'publishing'),
			count(*) FILTER (WHERE status = 'published'),
			count(*) FILTER (WHERE status = 'retryable_failed'),
			count(*) FILTER (WHERE status = 'permanent_failed'),
			min(created_at) FILTER (WHERE status <> 'published')
		FROM waybill.outbox_events
		WHERE tenant_id = $1
	`, r.tenantID).Scan(
		&stats.Pending,
		&stats.Publishing,
		&stats.Published,
		&stats.RetryableFailed,
		&stats.PermanentFailed,
		&stats.OldestUnpublishedAt,
	); err != nil {
		return outbox.Stats{}, fmt.Errorf("read PostgreSQL outbox stats: %w", err)
	}
	if stats.OldestUnpublishedAt != nil {
		oldest := stats.OldestUnpublishedAt.UTC()
		stats.OldestUnpublishedAt = &oldest
	}
	return stats, nil
}

func (r *Repository) RequeueOutbox(
	ctx context.Context,
	request outbox.RequeueRequest,
) error {
	if err := r.checkOpen(); err != nil {
		return err
	}
	if err := request.Validate(); err != nil {
		return err
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE waybill.outbox_events
		SET status = 'pending',
		    available_at = clock_timestamp(),
		    last_error_code = NULL,
		    lease_owner = NULL,
		    lease_deadline = NULL,
		    requeue_count = requeue_count + 1,
		    last_requeued_at = clock_timestamp(),
		    last_requeued_by = $4,
		    last_requeue_reason = $5
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
		  AND status = 'permanent_failed'
	`, r.tenantID, strings.TrimSpace(request.Source), strings.TrimSpace(request.EventID),
		strings.TrimSpace(request.Actor), strings.TrimSpace(request.Reason))
	if err != nil {
		return fmt.Errorf("requeue PostgreSQL outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrOutboxNotRequeueable
	}
	return nil
}

func (r *Repository) validateOutboxClaim(claim outbox.Claim) error {
	if err := r.checkOpen(); err != nil {
		return err
	}
	storedClaim, ok := claim.(*outboxClaim)
	if !ok ||
		storedClaim == nil ||
		storedClaim.repository != r ||
		storedClaim.source == "" ||
		storedClaim.eventID == "" ||
		storedClaim.owner != r.workerID ||
		storedClaim.fence <= 0 {
		return ErrStaleOutboxClaim
	}
	return nil
}
