package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type runStartedProjection struct {
	IncidentID domain.IncidentID `json:"incident_id"`
	WaybillID  domain.WaybillID  `json:"waybill_id"`
	Status     domain.RunStatus  `json:"status"`
}

type approvalDecisionProjection struct {
	ApprovalID   domain.ApprovalID `json:"approval_id"`
	Status       approval.Status   `json:"status"`
	DecidedBy    string            `json:"decided_by"`
	DecidedAt    json.RawMessage   `json:"decided_at"`
	RejectReason string            `json:"reject_reason"`
}

type approvalStatusProjection struct {
	ApprovalID domain.ApprovalID `json:"approval_id"`
	Status     approval.Status   `json:"status"`
}

type writeProjection struct {
	Key             domain.IdempotencyKey       `json:"idempotency_key"`
	CallID          string                      `json:"call_id"`
	Action          domain.Action               `json:"action"`
	ArgumentsHash   string                      `json:"arguments_hash"`
	IdentityVersion idempotency.IdentityVersion `json:"identity_version"`
	EffectID        domain.EffectID             `json:"effect_id"`
	Attempt         int                         `json:"attempt"`
	Disposition     platform.EffectDisposition  `json:"disposition"`
	Result          json.RawMessage             `json:"result"`
	Error           string                      `json:"error"`
	Reconciliation  int                         `json:"reconciliation"`
}

func (r *Repository) ensureRun(
	ctx context.Context,
	tx pgx.Tx,
	runID domain.RunID,
	draft audit.Draft,
) error {
	raw, err := audit.Redact(draft.Payload)
	if err != nil {
		return err
	}
	var payload runStartedProjection
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("decode run_started projection: %w", err)
	}
	if payload.IncidentID == "" || payload.WaybillID == "" || payload.Status != domain.RunStarted {
		return fmt.Errorf("run_started projection is incomplete")
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.incidents (
			tenant_id, incident_id, source, source_incident_key, waybill_id, kind, status
		) VALUES ($1, $2, 'demo', $3, $4, 'delay', 'open')
		ON CONFLICT (tenant_id, incident_id) DO NOTHING
	`, r.tenantID, payload.IncidentID, payload.IncidentID, payload.WaybillID); err != nil {
		return fmt.Errorf("insert PostgreSQL incident: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.runs (
			tenant_id, run_id, incident_id, waybill_id, status
		) VALUES ($1, $2, $3, $4, 'started')
		ON CONFLICT (tenant_id, run_id) DO NOTHING
	`, r.tenantID, runID, payload.IncidentID, payload.WaybillID); err != nil {
		return fmt.Errorf("insert PostgreSQL run: %w", MapError(err))
	}
	return nil
}

func (r *Repository) applyProjection(ctx context.Context, tx pgx.Tx, event audit.Event) error {
	switch event.Type {
	case audit.EventRunStarted:
		return nil
	case audit.EventToolCall, audit.EventToolResult, audit.EventAttribution:
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunInvestigating, false, event)
	case audit.EventApprovalRequested:
		return r.projectApprovalRequested(ctx, tx, event)
	case audit.EventApprovalDecided:
		if err := r.projectApprovalDecision(ctx, tx, event); err != nil {
			return err
		}
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunExecuting, false, event)
	case audit.EventApprovalExecuted:
		return r.setApprovalStatus(ctx, tx, event, approval.StatusExecuted)
	case audit.EventApprovalExecutionFailed:
		if err := r.setApprovalStatusFromPayload(ctx, tx, event); err != nil {
			return err
		}
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunFailed, true, event)
	case audit.EventApprovalReconciliationRequired:
		if err := r.setApprovalStatus(
			ctx,
			tx,
			event,
			approval.StatusReconciliationRequired,
		); err != nil {
			return err
		}
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunExecuting, false, event)
	case audit.EventWriteStarted:
		if err := r.projectWriteStarted(ctx, tx, event); err != nil {
			return err
		}
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunExecuting, false, event)
	case audit.EventWriteExecuted, audit.EventWriteFailed, audit.EventWriteUnknown,
		audit.EventWriteReconciliationStarted, audit.EventWriteReconciled:
		return r.projectWriteResult(ctx, tx, event)
	case audit.EventRunCompleted:
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunCompleted, true, event)
	case audit.EventRunRejected:
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunRejected, true, event)
	case audit.EventRunFailed:
		return r.setRunStatus(ctx, tx, event.RunID, domain.RunFailed, true, event)
	default:
		return nil
	}
}

func (r *Repository) projectApprovalRequested(
	ctx context.Context,
	tx pgx.Tx,
	event audit.Event,
) error {
	var value approval.Approval
	if err := json.Unmarshal(event.Payload, &value); err != nil {
		return fmt.Errorf("decode approval projection: %w", err)
	}
	if value.ID == "" || value.RunID != event.RunID || len(value.Items) == 0 {
		return fmt.Errorf("approval projection is incomplete")
	}
	var incidentID domain.IncidentID
	if err := tx.QueryRow(ctx, `
		SELECT incident_id
		FROM waybill.runs
		WHERE tenant_id = $1 AND run_id = $2
	`, r.tenantID, event.RunID).Scan(&incidentID); err != nil {
		return fmt.Errorf("read approval incident: %w", err)
	}
	items, err := json.Marshal(value.Items)
	if err != nil {
		return fmt.Errorf("marshal approval items: %w", err)
	}
	evidence, err := json.Marshal(value.Evidence)
	if err != nil {
		return fmt.Errorf("marshal approval evidence: %w", err)
	}
	itemsHash := sha256.Sum256(items)
	proposalID := "proposal:" + string(value.ID)
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.proposals (
			tenant_id, proposal_id, run_id, proposal_version, status, items,
			arguments_hash, model_version, prompt_version, tool_contract_version,
			policy_version, reason, created_at
		) VALUES (
			$1, $2, $3, $4, 'proposed', $5::jsonb,
			$6, 'runtime', 'waybill-demo-v1', 'contract-v1',
			'approval-v1', $7, $8
		)
	`, r.tenantID, proposalID, event.RunID, value.PlanVersion, items,
		hex.EncodeToString(itemsHash[:]), value.Reason, event.TS); err != nil {
		return fmt.Errorf("insert PostgreSQL proposal: %w", MapError(err))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.approvals (
			tenant_id, approval_id, incident_id, run_id, proposal_id, proposal_version,
			status, required_role, required_approvals, reason, evidence_refs,
			requested_at, expires_at, sdk_run_id, waybill_id, items, evidence
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			'pending', 'operator', 1, $7, $8::jsonb,
			$9, $10, $11, $12, $13::jsonb, $8::jsonb
		)
	`, r.tenantID, value.ID, incidentID, event.RunID, proposalID, value.PlanVersion,
		value.Reason, evidence, value.RequestedAt, value.ExpiresAt, value.SDKRunID,
		value.WaybillID, items); err != nil {
		return fmt.Errorf("insert PostgreSQL approval: %w", MapError(err))
	}
	for index, item := range value.Items {
		target := string(item.Action) + "/" + item.CallID
		if _, err := tx.Exec(ctx, `
			INSERT INTO waybill.effects (
				tenant_id, effect_id, run_id, proposal_id, identity_version,
				action, target, arguments, arguments_hash, idempotency_key,
				call_id, wire_name
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8::jsonb, $9, $10,
				$11, $12
			)
		`, r.tenantID, item.EffectID, event.RunID, proposalID, item.IdentityVersion,
			item.Action, target, []byte(item.Params), item.ArgumentsHash,
			item.IdempotencyKey, item.CallID, item.WireName); err != nil {
			return fmt.Errorf("insert PostgreSQL effect: %w", MapError(err))
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO waybill.approval_effects (
				tenant_id, approval_id, effect_id, ordinal, display_arguments,
				arguments_hash
			) VALUES ($1, $2, $3, $4, $5::jsonb, $6)
		`, r.tenantID, value.ID, item.EffectID, index, []byte(item.Params),
			item.ArgumentsHash); err != nil {
			return fmt.Errorf("bind PostgreSQL approval effect: %w", MapError(err))
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.runs
		SET status = 'awaiting_approval', sdk_run_id = $3, plan_version = $4,
		    closed_at = NULL
		WHERE tenant_id = $1 AND run_id = $2
	`, r.tenantID, event.RunID, value.SDKRunID, value.PlanVersion); err != nil {
		return fmt.Errorf("update PostgreSQL paused run: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.incidents
		SET status = 'awaiting_approval', updated_at = $3
		WHERE tenant_id = $1 AND incident_id = $2
	`, r.tenantID, incidentID, event.TS); err != nil {
		return fmt.Errorf("update PostgreSQL paused incident: %w", err)
	}
	return nil
}

func (r *Repository) projectApprovalDecision(
	ctx context.Context,
	tx pgx.Tx,
	event audit.Event,
) error {
	var payload struct {
		ApprovalID   domain.ApprovalID `json:"approval_id"`
		Status       approval.Status   `json:"status"`
		DecidedBy    string            `json:"decided_by"`
		DecidedAt    string            `json:"decided_at"`
		RejectReason string            `json:"reject_reason"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode approval decision projection: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE waybill.approvals
		SET status = $3, decided_by = $4, decided_at = $5::timestamptz,
		    reject_reason = NULLIF($6, ''), version = version + 1, updated_at = $7
		WHERE tenant_id = $1 AND approval_id = $2 AND status = 'pending'
	`, r.tenantID, payload.ApprovalID, payload.Status, payload.DecidedBy,
		payload.DecidedAt, payload.RejectReason, event.TS)
	if err != nil {
		return fmt.Errorf("update PostgreSQL approval decision: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("approval %q is not pending", payload.ApprovalID)
	}
	proposalStatus := "rejected"
	if payload.Status == approval.StatusConfirmed {
		proposalStatus = "approved"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.proposals p
		SET status = $3
		FROM waybill.approvals a
		WHERE a.tenant_id = $1 AND a.approval_id = $2
		  AND p.tenant_id = a.tenant_id AND p.proposal_id = a.proposal_id
	`, r.tenantID, payload.ApprovalID, proposalStatus); err != nil {
		return fmt.Errorf("update PostgreSQL proposal decision: %w", err)
	}
	return nil
}

func (r *Repository) setApprovalStatus(
	ctx context.Context,
	tx pgx.Tx,
	event audit.Event,
	status approval.Status,
) error {
	var payload struct {
		ApprovalID domain.ApprovalID `json:"approval_id"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode approval status projection: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE waybill.approvals
		SET status = $3, version = version + 1, updated_at = $4
		WHERE tenant_id = $1 AND approval_id = $2
	`, r.tenantID, payload.ApprovalID, status, event.TS)
	if err != nil {
		return fmt.Errorf("update PostgreSQL approval status: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("approval %q not found", payload.ApprovalID)
	}
	return nil
}

func (r *Repository) setApprovalStatusFromPayload(
	ctx context.Context,
	tx pgx.Tx,
	event audit.Event,
) error {
	var payload approvalStatusProjection
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode approval failure projection: %w", err)
	}
	return r.setApprovalStatus(ctx, tx, event, payload.Status)
}

func (r *Repository) projectWriteStarted(
	ctx context.Context,
	tx pgx.Tx,
	event audit.Event,
) error {
	var payload writeProjection
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode write start projection: %w", err)
	}
	command, err := tx.Exec(ctx, `
		UPDATE waybill.effects
		SET status = 'dispatching', attempt = $4, updated_at = $5
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
		  AND idempotency_key = $6 AND arguments_hash = $7
	`, r.tenantID, event.RunID, payload.EffectID, payload.Attempt, event.TS,
		payload.Key, payload.ArgumentsHash)
	if err != nil {
		return fmt.Errorf("start PostgreSQL effect: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("effect %q is not bound to the approved arguments", payload.EffectID)
	}
	return nil
}

func (r *Repository) projectWriteResult(
	ctx context.Context,
	tx pgx.Tx,
	event audit.Event,
) error {
	var payload writeProjection
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode write result projection: %w", err)
	}
	if event.Type == audit.EventWriteReconciliationStarted {
		command, err := tx.Exec(ctx, `
			UPDATE waybill.effects
			SET status = 'reconciling',
			    reconciliation_attempt = GREATEST(reconciliation_attempt, $4),
			    updated_at = $5
			WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
		`, r.tenantID, event.RunID, payload.EffectID, payload.Reconciliation, event.TS)
		if err != nil {
			return fmt.Errorf("start PostgreSQL effect reconciliation: %w", err)
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("effect %q not found", payload.EffectID)
		}
		return nil
	}
	status := ""
	switch event.Type {
	case audit.EventWriteExecuted:
		status = "succeeded"
	case audit.EventWriteFailed:
		switch payload.Disposition {
		case platform.EffectPermanentFailed:
			status = "permanent_failed"
		default:
			status = "retryable_failed"
		}
	case audit.EventWriteUnknown:
		status = "unknown"
	case audit.EventWriteReconciled:
		switch payload.Disposition {
		case platform.EffectSucceeded:
			status = "succeeded"
		case platform.EffectRetryableFailed:
			status = "retryable_failed"
		case platform.EffectPermanentFailed:
			status = "manual_review"
		default:
			status = "unknown"
		}
	}
	command, err := tx.Exec(ctx, `
		UPDATE waybill.effects
		SET status = $4,
		    response = CASE WHEN $5::bytea IS NULL THEN response ELSE convert_from($5, 'UTF8')::jsonb END,
		    response_canonical = CASE WHEN $5::bytea IS NULL THEN response_canonical ELSE $5 END,
		    last_error_code = NULLIF($6, ''),
		    reconciliation_attempt = GREATEST(reconciliation_attempt, $7),
		    lease_owner = NULL,
		    lease_deadline = NULL,
		    updated_at = $8
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
	`, r.tenantID, event.RunID, payload.EffectID, status, []byte(payload.Result),
		payload.Error, payload.Reconciliation, event.TS)
	if err != nil {
		return fmt.Errorf("complete PostgreSQL effect: %w", err)
	}
	if command.RowsAffected() != 1 {
		return fmt.Errorf("effect %q not found", payload.EffectID)
	}
	return nil
}

func (r *Repository) setRunStatus(
	ctx context.Context,
	tx pgx.Tx,
	runID domain.RunID,
	status domain.RunStatus,
	closed bool,
	event audit.Event,
) error {
	var command string
	if closed {
		command = `
			UPDATE waybill.runs
			SET status = $3, closed_at = $4
			WHERE tenant_id = $1 AND run_id = $2
		`
	} else {
		command = `
			UPDATE waybill.runs
			SET status = $3, closed_at = NULL
			WHERE tenant_id = $1 AND run_id = $2
		`
	}
	var tag pgconn.CommandTag
	var err error
	if closed {
		tag, err = tx.Exec(ctx, command, r.tenantID, runID, status, event.TS)
	} else {
		tag, err = tx.Exec(ctx, command, r.tenantID, runID, status)
	}
	if err != nil {
		return fmt.Errorf("update PostgreSQL run status: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return audit.ErrRunNotFound
	}
	incidentStatus := string(status)
	switch status {
	case domain.RunStarted:
		incidentStatus = "open"
	case domain.RunCompleted:
		incidentStatus = "resolved"
	case domain.RunRejected, domain.RunFailed:
		incidentStatus = "failed"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE waybill.incidents i
		SET status = $3, version = version + 1, updated_at = $4
		FROM waybill.runs r
		WHERE r.tenant_id = $1 AND r.run_id = $2
		  AND i.tenant_id = r.tenant_id AND i.incident_id = r.incident_id
	`, r.tenantID, runID, incidentStatus, event.TS); err != nil {
		return fmt.Errorf("update PostgreSQL incident status: %w", err)
	}
	return nil
}

func (r *Repository) insertOutbox(
	ctx context.Context,
	tx pgx.Tx,
	event audit.Event,
) error {
	payload, err := json.Marshal(map[string]any{
		"run_id":   event.RunID,
		"seq":      event.Seq,
		"event_id": event.EventID,
		"type":     event.Type,
		"payload":  json.RawMessage(event.Payload),
	})
	if err != nil {
		return fmt.Errorf("marshal PostgreSQL outbox event: %w", err)
	}
	eventID := string(event.RunID) + ":" + event.EventID
	if _, err := tx.Exec(ctx, `
		INSERT INTO waybill.outbox_events (
			tenant_id, source, event_id, aggregate_type, aggregate_id,
			aggregate_version, event_type, subject, payload
		) VALUES (
			$1, 'urn:waybill-guardian', $2, 'run', $3,
			$4, $5, $6, $7::jsonb
		)
	`, r.tenantID, eventID, event.RunID, event.Seq,
		"com.waybill.audit."+string(event.Type)+".v1",
		"run/"+string(event.RunID), payload); err != nil {
		return fmt.Errorf("insert PostgreSQL outbox event: %w", MapError(err))
	}
	return nil
}
