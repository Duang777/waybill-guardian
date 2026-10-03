package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrStaleOutboxClaim = errors.New("outbox claim is stale")

type OutboxDisposition string

const (
	OutboxPublished       OutboxDisposition = "published"
	OutboxRetryableFailed OutboxDisposition = "retryable_failed"
	OutboxPermanentFailed OutboxDisposition = "permanent_failed"
)

type OutboxEvent struct {
	Source           string
	ID               string
	AggregateType    string
	AggregateID      string
	AggregateVersion int64
	Type             string
	Subject          string
	Payload          json.RawMessage
	Attempt          int
	CreatedAt        time.Time
}

type OutboxClaim struct {
	event      OutboxEvent
	repository *Repository
	source     string
	eventID    string
	owner      string
	fence      int64
}

func (c *OutboxClaim) Event() OutboxEvent {
	if c == nil {
		return OutboxEvent{}
	}
	event := c.event
	event.Payload = append(json.RawMessage(nil), c.event.Payload...)
	return event
}

type OutboxResult struct {
	Disposition OutboxDisposition
	RetryAfter  time.Duration
	ErrorCode   string
}

func (r *Repository) ClaimOutbox(ctx context.Context, limit int) ([]*OutboxClaim, error) {
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
			          event.payload, event.attempt,
			          event.created_at, event.fencing_token
		)
		SELECT source, event_id, aggregate_type, aggregate_id, aggregate_version,
		       event_type, subject, payload, attempt, created_at, fencing_token
		FROM claimed
		ORDER BY created_at, source, event_id
	`, r.tenantID, r.workerID, limit, r.leaseTTL.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim PostgreSQL outbox events: %w", err)
	}
	defer rows.Close()

	claims := make([]*OutboxClaim, 0, limit)
	for rows.Next() {
		var claim OutboxClaim
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
			&payload,
			&claim.event.Attempt,
			&claim.event.CreatedAt,
			&claim.fence,
		); err != nil {
			return nil, fmt.Errorf("scan PostgreSQL outbox claim: %w", err)
		}
		claim.event.Payload = append(json.RawMessage(nil), payload...)
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

func (r *Repository) RenewOutbox(ctx context.Context, claim *OutboxClaim) error {
	if err := r.validateOutboxClaim(claim); err != nil {
		return err
	}
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE waybill.outbox_events
		SET lease_deadline = clock_timestamp() + make_interval(secs => $6)
		WHERE tenant_id = $1 AND source = $2 AND event_id = $3
		  AND status = 'publishing'
		  AND lease_owner = $4 AND fencing_token = $5
		  AND lease_deadline > clock_timestamp()
	`, r.tenantID, claim.source, claim.eventID, claim.owner, claim.fence,
		r.leaseTTL.Seconds())
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
	claim *OutboxClaim,
	result OutboxResult,
) error {
	if err := r.validateOutboxClaim(claim); err != nil {
		return err
	}
	if err := validateOutboxResult(result); err != nil {
		return err
	}
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
	`, r.tenantID, claim.source, claim.eventID, claim.owner, claim.fence,
		result.Disposition, result.RetryAfter.Seconds(), result.ErrorCode)
	if err != nil {
		return fmt.Errorf("complete PostgreSQL outbox event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleOutboxClaim
	}
	return nil
}

func (r *Repository) validateOutboxClaim(claim *OutboxClaim) error {
	if err := r.checkOpen(); err != nil {
		return err
	}
	if claim == nil ||
		claim.repository != r ||
		claim.source == "" ||
		claim.eventID == "" ||
		claim.owner != r.workerID ||
		claim.fence <= 0 {
		return ErrStaleOutboxClaim
	}
	return nil
}

func validateOutboxResult(result OutboxResult) error {
	if result.RetryAfter < 0 {
		return fmt.Errorf("outbox retry delay cannot be negative")
	}
	switch result.Disposition {
	case OutboxPublished:
		if result.RetryAfter != 0 || result.ErrorCode != "" {
			return fmt.Errorf("published outbox result cannot include retry or error fields")
		}
	case OutboxRetryableFailed:
		if result.ErrorCode == "" {
			return fmt.Errorf("retryable outbox failure requires an error code")
		}
	case OutboxPermanentFailed:
		if result.RetryAfter != 0 || result.ErrorCode == "" {
			return fmt.Errorf("permanent outbox failure requires only an error code")
		}
	default:
		return fmt.Errorf("unsupported outbox disposition %q", result.Disposition)
	}
	return nil
}
