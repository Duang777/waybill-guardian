package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/jackc/pgx/v5"
)

var ErrEffectLeaseHeld = errors.New("effect lease is held by another worker")

type effectWorkKind uint8

const (
	effectWorkMutate effectWorkKind = iota + 1
	effectWorkReconcile
	effectWorkReplay
)

type effectClaim struct {
	effectID       domain.EffectID
	fence          int64
	attempt        int
	reconciliation int
	kind           effectWorkKind
}

type effectWork struct {
	kind   effectWorkKind
	claim  effectClaim
	result json.RawMessage
}

type effectRow struct {
	status          string
	callID          string
	action          domain.Action
	argumentsHash   string
	identityVersion idempotency.IdentityVersion
	key             domain.IdempotencyKey
	attempt         int
	reconciliation  int
	result          json.RawMessage
	leaseOwner      *string
	leaseActive     bool
	fence           int64
	approvalStatus  approval.Status
}

func (r *Repository) Execute(
	ctx context.Context,
	command idempotency.Command,
	fn func(context.Context) (json.RawMessage, error),
) (idempotency.Result, error) {
	if err := validateEffectCommand(command); err != nil {
		return idempotency.Result{}, err
	}
	work, err := r.acquireEffect(ctx, command, true)
	if err != nil {
		return idempotency.Result{}, err
	}
	switch work.kind {
	case effectWorkReplay:
		return idempotency.Result{
			Value:     append(json.RawMessage(nil), work.result...),
			Duplicate: true,
		}, nil
	case effectWorkReconcile:
		return r.reconcileClaim(ctx, command, work.claim)
	case effectWorkMutate:
	default:
		return idempotency.Result{}, fmt.Errorf("unknown PostgreSQL effect work")
	}

	value, callErr := fn(ctx)
	disposition := platform.EffectDispositionOf(callErr)
	if callErr == nil {
		disposition = platform.EffectSucceeded
	}
	if err := r.completeEffect(ctx, command, work.claim, disposition, value, callErr); err != nil {
		return idempotency.Result{}, err
	}
	switch disposition {
	case platform.EffectSucceeded:
		return idempotency.Result{Value: append(json.RawMessage(nil), value...)}, nil
	case platform.EffectRetryableFailed:
		return idempotency.Result{}, callErr
	case platform.EffectPermanentFailed:
		return idempotency.Result{}, errors.Join(callErr, idempotency.ErrPermanentFailure)
	default:
		result, reconcileErr := r.Reconcile(ctx, command)
		if reconcileErr != nil {
			return idempotency.Result{}, errors.Join(callErr, reconcileErr)
		}
		return result, nil
	}
}

func (r *Repository) Reconcile(
	ctx context.Context,
	command idempotency.Command,
) (idempotency.Result, error) {
	if err := validateEffectCommand(command); err != nil {
		return idempotency.Result{}, err
	}
	work, err := r.acquireEffect(ctx, command, false)
	if err != nil {
		return idempotency.Result{}, err
	}
	switch work.kind {
	case effectWorkReplay:
		return idempotency.Result{
			Value:     append(json.RawMessage(nil), work.result...),
			Duplicate: true,
		}, nil
	case effectWorkReconcile:
		return r.reconcileClaim(ctx, command, work.claim)
	default:
		return idempotency.Result{}, idempotency.ErrReconciliationPending
	}
}

func (r *Repository) Lookup(command idempotency.Command) (idempotency.State, bool) {
	if validateEffectCommand(command) != nil || r.checkOpen() != nil {
		return "", false
	}
	var row effectRow
	var result []byte
	err := r.db.pool.QueryRow(context.Background(), `
		SELECT status, call_id, action, arguments_hash, identity_version,
		       idempotency_key, attempt, reconciliation_attempt, response_canonical
		FROM waybill.effects
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
	`, r.tenantID, command.RunID, command.Identity.EffectID).Scan(
		&row.status,
		&row.callID,
		&row.action,
		&row.argumentsHash,
		&row.identityVersion,
		&row.key,
		&row.attempt,
		&row.reconciliation,
		&result,
	)
	if err != nil || !row.matches(command) {
		return "", false
	}
	state, ok := databaseEffectState(row.status)
	return state, ok
}

func (r *Repository) acquireEffect(
	ctx context.Context,
	command idempotency.Command,
	allowMutation bool,
) (effectWork, error) {
	tx, err := r.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return effectWork{}, fmt.Errorf("begin PostgreSQL effect claim: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, _, err := r.lockRunAndValidateClaim(
		ctx,
		tx,
		command.RunID,
		audit.EventWriteStarted,
	); err != nil {
		return effectWork{}, err
	}
	row, err := r.lockEffect(ctx, tx, command)
	if err != nil {
		return effectWork{}, err
	}
	if !row.matches(command) {
		return effectWork{}, idempotency.ErrKeyConflict
	}
	if row.approvalStatus != approval.StatusConfirmed &&
		row.approvalStatus != approval.StatusReconciliationRequired &&
		row.approvalStatus != approval.StatusExecuted {
		return effectWork{}, approval.ErrApprovalNotGranted
	}

	switch row.status {
	case "succeeded":
		_, inserted, err := r.appendTransition(ctx, tx, command.RunID, audit.Draft{
			EventID: "duplicate:" + string(command.Identity.EffectID) + ":" + command.CallID,
			Actor:   audit.ActorSystem,
			Type:    audit.EventDuplicateSuppressed,
			Payload: map[string]any{
				"identity_version": command.Identity.Version,
				"effect_id":        command.Identity.EffectID,
				"idempotency_key":  command.Identity.Key,
				"action":           command.Identity.Action,
				"call_id":          command.CallID,
			},
		})
		if err != nil {
			return effectWork{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return effectWork{}, fmt.Errorf("commit PostgreSQL effect replay: %w", err)
		}
		if inserted {
			r.notify(command.RunID)
		}
		return effectWork{
			kind:   effectWorkReplay,
			result: append(json.RawMessage(nil), row.result...),
		}, nil
	case "permanent_failed":
		return effectWork{}, idempotency.ErrPermanentFailure
	case "manual_review":
		return effectWork{}, idempotency.ErrManualReview
	case "dispatching", "reconciling":
		if row.leaseActive {
			return effectWork{}, ErrEffectLeaseHeld
		}
	}

	kind := effectWorkReconcile
	if row.status == "prepared" || row.status == "retryable_failed" {
		if !allowMutation {
			if row.status == "retryable_failed" {
				return effectWork{}, idempotency.ErrRetryableFailure
			}
			return effectWork{}, idempotency.ErrReconciliationPending
		}
		kind = effectWorkMutate
	}
	attempt := row.attempt
	reconciliation := row.reconciliation
	eventType := audit.EventWriteReconciliationStarted
	eventState := idempotency.StateStarted
	if kind == effectWorkMutate {
		attempt++
	} else {
		reconciliation++
	}
	var fence int64
	var deadline time.Time
	status := "reconciling"
	if kind == effectWorkMutate {
		status = "dispatching"
	}
	if err := tx.QueryRow(ctx, `
		UPDATE waybill.effects
		SET status = $4,
		    attempt = $5,
		    reconciliation_attempt = $6,
		    lease_owner = $7,
		    lease_deadline = clock_timestamp() + make_interval(secs => $8),
		    fencing_token = fencing_token + 1,
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
		RETURNING fencing_token, lease_deadline
	`, r.tenantID, command.RunID, command.Identity.EffectID, status, attempt,
		reconciliation, r.workerID, r.leaseTTL.Seconds()).Scan(&fence, &deadline); err != nil {
		return effectWork{}, fmt.Errorf("claim PostgreSQL effect: %w", err)
	}
	payload := writeProjection{
		Key:             command.Identity.Key,
		CallID:          command.CallID,
		Action:          command.Identity.Action,
		ArgumentsHash:   command.Identity.ArgumentsHash,
		IdentityVersion: command.Identity.Version,
		EffectID:        command.Identity.EffectID,
		Attempt:         attempt,
		Reconciliation:  reconciliation,
	}
	eventID := fmt.Sprintf(
		"write:%s:reconciliation:%d:%s",
		command.Identity.EffectID,
		reconciliation,
		eventState,
	)
	if kind == effectWorkMutate {
		eventType = audit.EventWriteStarted
		eventID = fmt.Sprintf(
			"write:%s:attempt:%d:%s",
			command.Identity.EffectID,
			attempt,
			idempotency.StateStarted,
		)
	}
	_, inserted, err := r.appendTransition(ctx, tx, command.RunID, audit.Draft{
		EventID: eventID,
		Actor:   audit.ActorSystem,
		Type:    eventType,
		Payload: payload,
	})
	if err != nil {
		return effectWork{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return effectWork{}, fmt.Errorf("commit PostgreSQL effect claim: %w", err)
	}
	if inserted {
		r.notify(command.RunID)
	}
	return effectWork{
		kind: kind,
		claim: effectClaim{
			effectID:       command.Identity.EffectID,
			fence:          fence,
			attempt:        attempt,
			reconciliation: reconciliation,
			kind:           kind,
		},
	}, nil
}

func (r *Repository) reconcileClaim(
	ctx context.Context,
	command idempotency.Command,
	claim effectClaim,
) (idempotency.Result, error) {
	if r.effectLookup == nil {
		return idempotency.Result{}, errors.Join(
			idempotency.ErrReconciliationPending,
			idempotency.ErrReconciliationDisabled,
		)
	}
	effect, lookupErr := r.effectLookup(ctx, command)
	if effect.Disposition == "" {
		effect.Disposition = platform.EffectDispositionOf(lookupErr)
	}
	if effect.Disposition == platform.EffectSucceeded && len(effect.Response) == 0 {
		lookupErr = errors.Join(lookupErr, errors.New("effect lookup returned success without a response"))
		effect.Disposition = platform.EffectPermanentFailed
	}
	if err := r.completeEffect(
		ctx,
		command,
		claim,
		effect.Disposition,
		effect.Response,
		lookupErr,
	); err != nil {
		return idempotency.Result{}, err
	}
	switch effect.Disposition {
	case platform.EffectSucceeded:
		return idempotency.Result{
			Value:     append(json.RawMessage(nil), effect.Response...),
			Duplicate: true,
		}, nil
	case platform.EffectRetryableFailed:
		return idempotency.Result{}, errors.Join(lookupErr, idempotency.ErrRetryableFailure)
	case platform.EffectPermanentFailed:
		return idempotency.Result{}, errors.Join(lookupErr, idempotency.ErrManualReview)
	default:
		return idempotency.Result{}, errors.Join(lookupErr, idempotency.ErrReconciliationPending)
	}
}

func (r *Repository) completeEffect(
	ctx context.Context,
	command idempotency.Command,
	claim effectClaim,
	disposition platform.EffectDisposition,
	result json.RawMessage,
	callErr error,
) error {
	tx, err := r.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin PostgreSQL effect completion: %w", err)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, _, err := r.lockRunAndValidateClaim(
		ctx,
		tx,
		command.RunID,
		audit.EventWriteExecuted,
	); err != nil {
		return err
	}
	var currentOwner *string
	var currentFence int64
	var leaseActive bool
	if err := tx.QueryRow(ctx, `
		SELECT lease_owner, fencing_token,
		       COALESCE(lease_deadline > clock_timestamp(), false)
		FROM waybill.effects
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
		FOR UPDATE
	`, r.tenantID, command.RunID, claim.effectID).Scan(
		&currentOwner,
		&currentFence,
		&leaseActive,
	); err != nil {
		return fmt.Errorf("lock PostgreSQL effect completion: %w", err)
	}
	if currentOwner == nil ||
		*currentOwner != r.workerID ||
		currentFence != claim.fence ||
		!leaseActive {
		return ErrStaleRunClaim
	}

	state := idempotency.StateSucceeded
	eventType := audit.EventWriteExecuted
	if claim.kind == effectWorkReconcile {
		eventType = audit.EventWriteReconciled
		switch disposition {
		case platform.EffectSucceeded:
			state = idempotency.StateSucceeded
		case platform.EffectRetryableFailed:
			state = idempotency.StateRetryableFailed
		case platform.EffectPermanentFailed:
			state = idempotency.StateManualReview
		default:
			state = idempotency.StateUnknown
		}
	} else {
		switch disposition {
		case platform.EffectSucceeded:
			state = idempotency.StateSucceeded
		case platform.EffectRetryableFailed:
			state = idempotency.StateRetryableFailed
			eventType = audit.EventWriteFailed
		case platform.EffectPermanentFailed:
			state = idempotency.StatePermanentFailed
			eventType = audit.EventWriteFailed
		default:
			state = idempotency.StateUnknown
			eventType = audit.EventWriteUnknown
		}
	}
	payload := writeProjection{
		Key:             command.Identity.Key,
		CallID:          command.CallID,
		Action:          command.Identity.Action,
		ArgumentsHash:   command.Identity.ArgumentsHash,
		IdentityVersion: command.Identity.Version,
		EffectID:        command.Identity.EffectID,
		Attempt:         claim.attempt,
		Disposition:     disposition,
		Result:          append(json.RawMessage(nil), result...),
		Reconciliation:  claim.reconciliation,
	}
	if callErr != nil {
		payload.Error = callErr.Error()
	}
	eventID := fmt.Sprintf(
		"write:%s:attempt:%d:%s",
		command.Identity.EffectID,
		claim.attempt,
		state,
	)
	if claim.kind == effectWorkReconcile {
		eventID = fmt.Sprintf(
			"write:%s:reconciliation:%d:%s",
			command.Identity.EffectID,
			claim.reconciliation,
			state,
		)
	}
	_, inserted, err := r.appendTransition(ctx, tx, command.RunID, audit.Draft{
		EventID: eventID,
		Actor:   audit.ActorSystem,
		Type:    eventType,
		Payload: payload,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit PostgreSQL effect completion: %w", err)
	}
	if inserted {
		r.notify(command.RunID)
	}
	return nil
}

func (r *Repository) lockEffect(
	ctx context.Context,
	tx pgx.Tx,
	command idempotency.Command,
) (effectRow, error) {
	var row effectRow
	var result []byte
	err := tx.QueryRow(ctx, `
		SELECT e.status, e.call_id, e.action, e.arguments_hash, e.identity_version,
		       e.idempotency_key, e.attempt, e.reconciliation_attempt, e.response_canonical,
		       e.lease_owner, COALESCE(e.lease_deadline > clock_timestamp(), false),
		       e.fencing_token, a.status
		FROM waybill.effects e
		JOIN waybill.approval_effects ae
		  ON ae.tenant_id = e.tenant_id AND ae.effect_id = e.effect_id
		JOIN waybill.approvals a
		  ON a.tenant_id = ae.tenant_id AND a.approval_id = ae.approval_id
		WHERE e.tenant_id = $1 AND e.run_id = $2 AND e.effect_id = $3
		FOR UPDATE OF e
	`, r.tenantID, command.RunID, command.Identity.EffectID).Scan(
		&row.status,
		&row.callID,
		&row.action,
		&row.argumentsHash,
		&row.identityVersion,
		&row.key,
		&row.attempt,
		&row.reconciliation,
		&result,
		&row.leaseOwner,
		&row.leaseActive,
		&row.fence,
		&row.approvalStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return effectRow{}, approval.ErrApprovalNotGranted
	}
	if err != nil {
		return effectRow{}, fmt.Errorf("lock PostgreSQL effect: %w", err)
	}
	row.result = append(json.RawMessage(nil), result...)
	return row, nil
}

func (row effectRow) matches(command idempotency.Command) bool {
	return row.callID == command.CallID &&
		row.action == command.Identity.Action &&
		row.argumentsHash == command.Identity.ArgumentsHash &&
		row.identityVersion == command.Identity.Version &&
		row.key == command.Identity.Key
}

func validateEffectCommand(command idempotency.Command) error {
	if command.RunID == "" || command.CallID == "" {
		return fmt.Errorf("run id and call id are required")
	}
	return command.Identity.Validate()
}

func databaseEffectState(status string) (idempotency.State, bool) {
	switch status {
	case "dispatching":
		return idempotency.StateStarted, true
	case "succeeded":
		return idempotency.StateSucceeded, true
	case "retryable_failed":
		return idempotency.StateRetryableFailed, true
	case "permanent_failed":
		return idempotency.StatePermanentFailed, true
	case "unknown":
		return idempotency.StateUnknown, true
	case "reconciling":
		return idempotency.StateReconciling, true
	case "manual_review":
		return idempotency.StateManualReview, true
	default:
		return "", false
	}
}

var _ idempotency.Executor = (*Repository)(nil)
