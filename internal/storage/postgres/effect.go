package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/jackc/pgx/v5"
)

var (
	ErrEffectLeaseHeld   = errors.New("effect lease is held by another worker")
	ErrStaleEffectClaim  = errors.New("effect claim is stale")
	ErrEffectRuntimeDown = errors.New("effect write runtime is not configured")
)

type effectWorkKind uint8

const (
	effectWorkDispatch effectWorkKind = iota + 1
	effectWorkLookup
	effectWorkReplay
)

type effectClaim struct {
	runID          domain.RunID
	effectID       domain.EffectID
	fence          int64
	attempt        int
	reconciliation int
	kind           effectWorkKind
}

type effectWork struct {
	kind             effectWorkKind
	claim            effectClaim
	binding          platform.EffectBinding
	result           json.RawMessage
	localFailureCode string
}

type effectRow struct {
	status                  string
	callID                  string
	action                  domain.Action
	argumentsHash           string
	identityVersion         idempotency.IdentityVersion
	key                     domain.IdempotencyKey
	attempt                 int
	reconciliation          int
	result                  json.RawMessage
	leaseOwner              *string
	leaseActive             bool
	fence                   int64
	approvalStatus          approval.Status
	retryAfter              *time.Time
	bindingSchemaVersion    int
	adapterID               *string
	contractVersion         *string
	providerOperation       *string
	providerScopeDigest     *string
	providerRequestHash     *string
	keyCreatedAt            *time.Time
	keyExpiresAt            *time.Time
	lookupConsistencyWindow *int64
}

type effectCompletion struct {
	state               idempotency.State
	result              json.RawMessage
	dispatchDisposition platform.EffectDisposition
	lookupDisposition   platform.LookupDisposition
	externalRef         string
	externalRequestID   string
	responseDigest      string
	errorCode           string
	errorClass          string
	retryAt             *time.Time
	lastLookupAt        *time.Time
}

func (r *Repository) Execute(
	ctx context.Context,
	effect idempotency.AuthorizedEffect,
) (idempotency.Result, error) {
	command := effect.Command()
	if err := validateEffectCommand(command); err != nil {
		return idempotency.Result{}, err
	}
	if r.writeRuntime == nil {
		return idempotency.Result{}, ErrEffectRuntimeDown
	}
	var work effectWork
	for {
		var err error
		work, err = r.acquireDispatch(ctx, effect)
		if !errors.Is(err, idempotency.ErrReconciliationPending) &&
			!errors.Is(err, ErrEffectLeaseHeld) {
			if err != nil {
				return idempotency.Result{}, err
			}
			break
		}
		outcome, recoverErr := r.Recover(ctx, command)
		if recoverErr != nil {
			return idempotency.Result{}, recoverErr
		}
		if outcome.Decision == idempotency.RecoveryReadyToResume {
			continue
		}
		return resultFromRecovery(outcome, nil)
	}
	switch work.kind {
	case effectWorkReplay:
		return idempotency.Result{
			Value:     append(json.RawMessage(nil), work.result...),
			Duplicate: true,
		}, nil
	case effectWorkLookup:
		outcome, recoverErr := r.recoverClaim(ctx, command, work)
		return resultFromRecovery(outcome, recoverErr)
	case effectWorkDispatch:
	default:
		return idempotency.Result{}, fmt.Errorf("unknown PostgreSQL effect work")
	}

	dispatched := r.dispatchWithLease(ctx, work.claim, func(callCtx context.Context) platform.DispatchResult {
		return r.writeRuntime.Dispatch(
			callCtx,
			work.binding,
			effect.Request(),
			command.Identity.Key,
		)
	})
	dispatched = normalizeDispatchResult(dispatched)
	completion := dispatchCompletion(r.clock().UTC(), dispatched)
	completionCtx, cancel := r.effectCompletionContext(ctx)
	defer cancel()
	if err := r.completeEffect(completionCtx, command, work.claim, completion); err != nil {
		return idempotency.Result{}, err
	}
	switch completion.state {
	case idempotency.StateSucceeded:
		return idempotency.Result{
			Value: append(json.RawMessage(nil), completion.result...),
		}, nil
	case idempotency.StateRetryableFailed:
		return idempotency.Result{}, effectResultError(
			idempotency.ErrRetryableFailure,
			completion.errorCode,
		)
	case idempotency.StatePermanentFailed:
		return idempotency.Result{}, effectResultError(
			idempotency.ErrPermanentFailure,
			completion.errorCode,
		)
	}
	if ctx.Err() != nil {
		return idempotency.Result{}, errors.Join(ctx.Err(), idempotency.ErrReconciliationPending)
	}
	outcome, recoverErr := r.Recover(ctx, command)
	return resultFromRecovery(outcome, recoverErr)
}

func (r *Repository) Recover(
	ctx context.Context,
	command idempotency.Command,
) (idempotency.RecoveryOutcome, error) {
	if err := validateEffectCommand(command); err != nil {
		return idempotency.RecoveryOutcome{}, err
	}
	work, outcome, err := r.acquireRecovery(ctx, command)
	if err != nil || work == nil {
		return outcome, err
	}
	return r.recoverClaim(ctx, command, *work)
}

func (r *Repository) Status(command idempotency.Command) (idempotency.State, bool) {
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

func (r *Repository) acquireDispatch(
	ctx context.Context,
	effect idempotency.AuthorizedEffect,
) (effectWork, error) {
	command := effect.Command()
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
	if err := validateEffectApproval(row.approvalStatus); err != nil {
		return effectWork{}, err
	}

	switch row.status {
	case "succeeded":
		return r.replayEffect(ctx, tx, command, row.result)
	case "permanent_failed":
		return effectWork{}, idempotency.ErrPermanentFailure
	case "manual_review":
		return effectWork{}, idempotency.ErrManualReview
	case "dispatching", "reconciling":
		if row.leaseActive {
			return effectWork{}, ErrEffectLeaseHeld
		}
		return effectWork{}, idempotency.ErrReconciliationPending
	case "unknown":
		return effectWork{}, idempotency.ErrReconciliationPending
	case "retryable_failed":
		if row.retryAfter != nil && r.clock().UTC().Before(row.retryAfter.UTC()) {
			return effectWork{}, idempotency.ErrRetryableFailure
		}
	default:
		if row.status != "prepared" {
			return effectWork{}, fmt.Errorf("unknown PostgreSQL effect status %q", row.status)
		}
	}

	binding, ok := row.binding()
	if !ok {
		if row.status != "prepared" {
			return effectWork{}, idempotency.ErrManualReview
		}
		binding, err = r.writeRuntime.Bind(
			effect.Request(),
			command.Identity.Key,
			r.clock().UTC(),
		)
		if err != nil {
			return effectWork{}, fmt.Errorf("bind PostgreSQL effect: %w", err)
		}
	}
	if binding.Action != command.Identity.Action ||
		!r.writeRuntime.SupportsRecovery(binding) {
		return effectWork{}, idempotency.ErrManualReview
	}
	if !r.clock().UTC().Before(binding.KeyExpiresAt) {
		return effectWork{}, idempotency.ErrManualReview
	}

	attempt := row.attempt + 1
	var fence int64
	var deadline time.Time
	startedAt := r.clock().UTC()
	if err := tx.QueryRow(ctx, `
		UPDATE waybill.effects
		SET status = 'dispatching',
		    attempt = $4,
		    binding_schema_version = $5,
		    adapter_id = $6,
		    provider_contract_version = $7,
		    provider_operation = $8,
		    provider_scope_digest = $9,
		    provider_request_hash = $10,
		    key_created_at = $11,
		    key_expires_at = $12,
		    lookup_consistency_window_ms = $13,
		    dispatch_started_at = $14,
		    retry_after = NULL,
		    last_error_code = NULL,
		    last_error_class = NULL,
		    lease_owner = $15,
		    lease_deadline = clock_timestamp() + make_interval(secs => $16),
		    fencing_token = fencing_token + 1,
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
		RETURNING fencing_token, lease_deadline
	`, r.tenantID, command.RunID, command.Identity.EffectID, attempt,
		binding.SchemaVersion, binding.AdapterID, binding.ContractVersion,
		binding.ProviderOperation, binding.ProviderScopeDigest,
		binding.ProviderRequestHash, binding.KeyCreatedAt, binding.KeyExpiresAt,
		binding.LookupConsistencyWindow.Milliseconds(), startedAt, r.workerID,
		r.effectLeaseTTL.Seconds()).Scan(&fence, &deadline); err != nil {
		return effectWork{}, fmt.Errorf("claim PostgreSQL effect dispatch: %w", err)
	}
	payload := writeProjection{
		Key:             command.Identity.Key,
		CallID:          command.CallID,
		Action:          command.Identity.Action,
		ArgumentsHash:   command.Identity.ArgumentsHash,
		IdentityVersion: command.Identity.Version,
		EffectID:        command.Identity.EffectID,
		Attempt:         attempt,
		State:           idempotency.StateStarted,
		Binding:         &binding,
		DispatchStarted: &startedAt,
	}
	_, inserted, err := r.appendTransition(ctx, tx, command.RunID, audit.Draft{
		EventID: fmt.Sprintf(
			"write:%s:attempt:%d:%s",
			command.Identity.EffectID,
			attempt,
			idempotency.StateStarted,
		),
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteStarted,
		Payload: payload,
	})
	if err != nil {
		return effectWork{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return effectWork{}, fmt.Errorf("commit PostgreSQL effect dispatch claim: %w", err)
	}
	if inserted {
		r.notify(command.RunID)
	}
	return effectWork{
		kind:    effectWorkDispatch,
		binding: binding,
		claim: effectClaim{
			runID:          command.RunID,
			effectID:       command.Identity.EffectID,
			fence:          fence,
			attempt:        attempt,
			reconciliation: row.reconciliation,
			kind:           effectWorkDispatch,
		},
	}, nil
}

func (r *Repository) acquireRecovery(
	ctx context.Context,
	command idempotency.Command,
) (*effectWork, idempotency.RecoveryOutcome, error) {
	tx, err := r.db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, idempotency.RecoveryOutcome{}, fmt.Errorf(
			"begin PostgreSQL effect recovery: %w",
			err,
		)
	}
	defer func() {
		_ = tx.Rollback(context.Background())
	}()
	if _, _, err := r.lockRunAndValidateClaim(
		ctx,
		tx,
		command.RunID,
		audit.EventWriteReconciliationStarted,
	); err != nil {
		return nil, idempotency.RecoveryOutcome{}, err
	}
	row, err := r.lockEffect(ctx, tx, command)
	if err != nil {
		return nil, idempotency.RecoveryOutcome{}, err
	}
	if !row.matches(command) {
		return nil, idempotency.RecoveryOutcome{}, idempotency.ErrKeyConflict
	}
	if err := validateEffectApproval(row.approvalStatus); err != nil {
		return nil, idempotency.RecoveryOutcome{}, err
	}

	switch row.status {
	case "succeeded":
		return nil, idempotency.RecoveryOutcome{
			Decision: idempotency.RecoveryResolved,
			Result: idempotency.Result{
				Value:     append(json.RawMessage(nil), row.result...),
				Duplicate: true,
			},
			State: idempotency.StateSucceeded,
		}, nil
	case "prepared":
		return nil, idempotency.RecoveryOutcome{
			Decision: idempotency.RecoveryReadyToResume,
			State:    idempotency.StateRetryableFailed,
		}, nil
	case "permanent_failed":
		return nil, idempotency.RecoveryOutcome{
			Decision: idempotency.RecoveryPermanentFailure,
			State:    idempotency.StatePermanentFailed,
		}, nil
	case "manual_review":
		return nil, idempotency.RecoveryOutcome{
			Decision: idempotency.RecoveryManualReview,
			State:    idempotency.StateManualReview,
		}, nil
	case "dispatching", "reconciling":
		if row.leaseActive {
			return nil, idempotency.RecoveryOutcome{
				Decision: idempotency.RecoveryBusy,
				State:    databaseEffectStateOrUnknown(row.status),
			}, nil
		}
	case "retryable_failed":
		if row.retryAfter != nil && r.clock().UTC().Before(row.retryAfter.UTC()) {
			return nil, idempotency.RecoveryOutcome{
				Decision: idempotency.RecoveryPending,
				State:    idempotency.StateRetryableFailed,
				RetryAt:  row.retryAfter.UTC(),
			}, nil
		}
	case "unknown":
		if row.retryAfter != nil && r.clock().UTC().Before(row.retryAfter.UTC()) {
			return nil, idempotency.RecoveryOutcome{
				Decision: idempotency.RecoveryPending,
				State:    idempotency.StateUnknown,
				RetryAt:  row.retryAfter.UTC(),
			}, nil
		}
	default:
		return nil, idempotency.RecoveryOutcome{}, fmt.Errorf(
			"unknown PostgreSQL effect status %q",
			row.status,
		)
	}

	binding, bindingOK := row.binding()
	localFailureCode := ""
	switch {
	case !bindingOK:
		localFailureCode = "recovery_binding_unavailable"
	case r.writeRuntime == nil:
		localFailureCode = "recovery_runtime_unavailable"
	case !r.writeRuntime.SupportsRecovery(binding):
		localFailureCode = "recovery_binding_unavailable"
	case !r.clock().UTC().Before(binding.KeyExpiresAt):
		localFailureCode = "key_expired"
	case row.status == "retryable_failed":
		return nil, idempotency.RecoveryOutcome{
			Decision: idempotency.RecoveryReadyToResume,
			State:    idempotency.StateRetryableFailed,
		}, nil
	}

	reconciliation := row.reconciliation + 1
	var fence int64
	var deadline time.Time
	if err := tx.QueryRow(ctx, `
		UPDATE waybill.effects
		SET status = 'reconciling',
		    reconciliation_attempt = $4,
		    lease_owner = $5,
		    lease_deadline = clock_timestamp() + make_interval(secs => $6),
		    fencing_token = fencing_token + 1,
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
		RETURNING fencing_token, lease_deadline
	`, r.tenantID, command.RunID, command.Identity.EffectID, reconciliation,
		r.workerID, r.effectLeaseTTL.Seconds()).Scan(&fence, &deadline); err != nil {
		return nil, idempotency.RecoveryOutcome{}, fmt.Errorf(
			"claim PostgreSQL effect recovery: %w",
			err,
		)
	}
	payload := writeProjection{
		Key:             command.Identity.Key,
		CallID:          command.CallID,
		Action:          command.Identity.Action,
		ArgumentsHash:   command.Identity.ArgumentsHash,
		IdentityVersion: command.Identity.Version,
		EffectID:        command.Identity.EffectID,
		Attempt:         row.attempt,
		State:           idempotency.StateReconciling,
		Reconciliation:  reconciliation,
	}
	_, inserted, err := r.appendTransition(ctx, tx, command.RunID, audit.Draft{
		EventID: fmt.Sprintf(
			"write:%s:reconciliation:%d:%s",
			command.Identity.EffectID,
			reconciliation,
			idempotency.StateStarted,
		),
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteReconciliationStarted,
		Payload: payload,
	})
	if err != nil {
		return nil, idempotency.RecoveryOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, idempotency.RecoveryOutcome{}, fmt.Errorf(
			"commit PostgreSQL effect recovery claim: %w",
			err,
		)
	}
	if inserted {
		r.notify(command.RunID)
	}
	return &effectWork{
		kind:             effectWorkLookup,
		binding:          binding,
		localFailureCode: localFailureCode,
		claim: effectClaim{
			runID:          command.RunID,
			effectID:       command.Identity.EffectID,
			fence:          fence,
			attempt:        row.attempt,
			reconciliation: reconciliation,
			kind:           effectWorkLookup,
		},
	}, idempotency.RecoveryOutcome{}, nil
}

func (r *Repository) recoverClaim(
	ctx context.Context,
	command idempotency.Command,
	work effectWork,
) (idempotency.RecoveryOutcome, error) {
	var lookedUp platform.LookupResult
	var lastLookupAt *time.Time
	if work.localFailureCode != "" {
		lookedUp = platform.LookupResult{
			Disposition: platform.LookupConflict,
			ErrorCode:   work.localFailureCode,
		}
	} else {
		now := r.clock().UTC()
		lastLookupAt = &now
		lookedUp = r.lookupWithLease(ctx, work.claim, func(callCtx context.Context) platform.LookupResult {
			return r.writeRuntime.Lookup(callCtx, work.binding, command.Identity.Key)
		})
	}
	lookedUp = normalizeLookupResult(lookedUp)
	completion, decision := lookupCompletion(
		r.clock().UTC(),
		r.pollInterval,
		lookedUp,
		lastLookupAt,
	)
	completionCtx, cancel := r.effectCompletionContext(ctx)
	defer cancel()
	if err := r.completeEffect(completionCtx, command, work.claim, completion); err != nil {
		return idempotency.RecoveryOutcome{}, err
	}
	outcome := idempotency.RecoveryOutcome{
		Decision: decision,
		State:    completion.state,
	}
	if completion.retryAt != nil {
		outcome.RetryAt = completion.retryAt.UTC()
	}
	if decision == idempotency.RecoveryResolved {
		outcome.Result = idempotency.Result{
			Value:     append(json.RawMessage(nil), completion.result...),
			Duplicate: true,
		}
	}
	return outcome, nil
}

func (r *Repository) replayEffect(
	ctx context.Context,
	tx pgx.Tx,
	command idempotency.Command,
	result json.RawMessage,
) (effectWork, error) {
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
		result: append(json.RawMessage(nil), result...),
	}, nil
}

func (r *Repository) completeEffect(
	ctx context.Context,
	command idempotency.Command,
	claim effectClaim,
	completion effectCompletion,
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
		return ErrStaleEffectClaim
	}

	eventType := audit.EventWriteExecuted
	if claim.kind == effectWorkLookup {
		eventType = audit.EventWriteReconciled
	} else {
		switch completion.state {
		case idempotency.StateRetryableFailed, idempotency.StatePermanentFailed:
			eventType = audit.EventWriteFailed
		case idempotency.StateUnknown:
			eventType = audit.EventWriteUnknown
		}
	}
	payload := writeProjection{
		Key:               command.Identity.Key,
		CallID:            command.CallID,
		Action:            command.Identity.Action,
		ArgumentsHash:     command.Identity.ArgumentsHash,
		IdentityVersion:   command.Identity.Version,
		EffectID:          command.Identity.EffectID,
		Attempt:           claim.attempt,
		State:             completion.state,
		Disposition:       completion.dispatchDisposition,
		LookupDisposition: completion.lookupDisposition,
		Result:            append(json.RawMessage(nil), completion.result...),
		ErrorCode:         completion.errorCode,
		ErrorClass:        completion.errorClass,
		ExternalRef:       completion.externalRef,
		ExternalRequestID: completion.externalRequestID,
		ResponseDigest:    completion.responseDigest,
		RetryAt:           completion.retryAt,
		LastLookupAt:      completion.lastLookupAt,
		Reconciliation:    claim.reconciliation,
	}
	eventID := fmt.Sprintf(
		"write:%s:attempt:%d:%s",
		command.Identity.EffectID,
		claim.attempt,
		completion.state,
	)
	if claim.kind == effectWorkLookup {
		eventID = fmt.Sprintf(
			"write:%s:reconciliation:%d:%s",
			command.Identity.EffectID,
			claim.reconciliation,
			completion.state,
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
		       e.fencing_token, a.status, e.retry_after,
		       e.binding_schema_version, e.adapter_id, e.provider_contract_version,
		       e.provider_operation, e.provider_scope_digest, e.provider_request_hash,
		       e.key_created_at, e.key_expires_at, e.lookup_consistency_window_ms
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
		&row.retryAfter,
		&row.bindingSchemaVersion,
		&row.adapterID,
		&row.contractVersion,
		&row.providerOperation,
		&row.providerScopeDigest,
		&row.providerRequestHash,
		&row.keyCreatedAt,
		&row.keyExpiresAt,
		&row.lookupConsistencyWindow,
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

func (row effectRow) binding() (platform.EffectBinding, bool) {
	if row.bindingSchemaVersion != 1 ||
		row.adapterID == nil ||
		row.contractVersion == nil ||
		row.providerOperation == nil ||
		row.providerScopeDigest == nil ||
		row.providerRequestHash == nil ||
		row.keyCreatedAt == nil ||
		row.keyExpiresAt == nil ||
		row.lookupConsistencyWindow == nil {
		return platform.EffectBinding{}, false
	}
	return platform.EffectBinding{
		SchemaVersion:           row.bindingSchemaVersion,
		Action:                  row.action,
		AdapterID:               *row.adapterID,
		ContractVersion:         *row.contractVersion,
		ProviderOperation:       *row.providerOperation,
		ProviderScopeDigest:     *row.providerScopeDigest,
		ProviderRequestHash:     *row.providerRequestHash,
		KeyCreatedAt:            row.keyCreatedAt.UTC(),
		KeyExpiresAt:            row.keyExpiresAt.UTC(),
		LookupConsistencyWindow: time.Duration(*row.lookupConsistencyWindow) * time.Millisecond,
	}, true
}

func (row effectRow) matches(command idempotency.Command) bool {
	return row.callID == command.CallID &&
		row.action == command.Identity.Action &&
		row.argumentsHash == command.Identity.ArgumentsHash &&
		row.identityVersion == command.Identity.Version &&
		row.key == command.Identity.Key
}

func (r *Repository) dispatchWithLease(
	ctx context.Context,
	claim effectClaim,
	call func(context.Context) platform.DispatchResult,
) platform.DispatchResult {
	leaseCtx, stop := r.maintainEffectLease(ctx, claim)
	result := call(leaseCtx)
	canceled := leaseCtx.Err() != nil
	renewErr := stop()
	if canceled || renewErr != nil {
		return platform.DispatchResult{
			Disposition: platform.EffectUnknown,
			ErrorCode:   "effect_lease_lost",
		}
	}
	return result
}

func (r *Repository) lookupWithLease(
	ctx context.Context,
	claim effectClaim,
	call func(context.Context) platform.LookupResult,
) platform.LookupResult {
	leaseCtx, stop := r.maintainEffectLease(ctx, claim)
	result := call(leaseCtx)
	canceled := leaseCtx.Err() != nil
	renewErr := stop()
	if canceled || renewErr != nil {
		return platform.LookupResult{
			Disposition: platform.LookupPending,
			ErrorCode:   "effect_lease_lost",
		}
	}
	return result
}

func (r *Repository) maintainEffectLease(
	ctx context.Context,
	claim effectClaim,
) (context.Context, func() error) {
	leaseCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	renewInterval := r.effectLeaseTTL / 3
	if renewInterval <= 0 {
		renewInterval = time.Nanosecond
	}
	var once sync.Once
	var renewErr error
	var renewMu sync.Mutex
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		ticker := time.NewTicker(renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				renewCtx, renewCancel := context.WithTimeout(
					context.Background(),
					renewInterval,
				)
				err := r.renewEffectLease(renewCtx, claim)
				renewCancel()
				if err != nil {
					renewMu.Lock()
					renewErr = err
					renewMu.Unlock()
					cancel()
					return
				}
			}
		}
	}()
	stop := func() error {
		once.Do(func() {
			close(done)
			cancel()
		})
		wait.Wait()
		renewMu.Lock()
		defer renewMu.Unlock()
		return renewErr
	}
	return leaseCtx, stop
}

func (r *Repository) renewEffectLease(ctx context.Context, claim effectClaim) error {
	tag, err := r.db.pool.Exec(ctx, `
		UPDATE waybill.effects
		SET lease_deadline = clock_timestamp() + make_interval(secs => $6),
		    updated_at = clock_timestamp()
		WHERE tenant_id = $1 AND run_id = $2 AND effect_id = $3
		  AND lease_owner = $4 AND fencing_token = $5
		  AND lease_deadline > clock_timestamp()
		  AND status IN ('dispatching', 'reconciling')
	`, r.tenantID, claim.runID, claim.effectID, r.workerID, claim.fence,
		r.effectLeaseTTL.Seconds())
	if err != nil {
		return fmt.Errorf("renew PostgreSQL effect lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrStaleEffectClaim
	}
	return nil
}

func (r *Repository) effectCompletionContext(
	ctx context.Context,
) (context.Context, context.CancelFunc) {
	timeout := r.effectLeaseTTL / 3
	if timeout <= 0 {
		timeout = time.Second
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

func dispatchCompletion(
	now time.Time,
	result platform.DispatchResult,
) effectCompletion {
	state := mutationState(result.Disposition)
	var retryAt *time.Time
	if result.RetryAfter > 0 {
		value := now.Add(result.RetryAfter).UTC()
		retryAt = &value
	} else if state == idempotency.StateUnknown {
		value := now.UTC()
		retryAt = &value
	}
	return effectCompletion{
		state:               state,
		result:              append(json.RawMessage(nil), result.Response...),
		dispatchDisposition: result.Disposition,
		externalRef:         result.ExternalRef,
		externalRequestID:   result.ExternalRequestID,
		responseDigest:      result.ResponseDigest,
		errorCode:           result.ErrorCode,
		errorClass:          dispatchErrorClass(result.Disposition),
		retryAt:             retryAt,
	}
}

func lookupCompletion(
	now time.Time,
	pollInterval time.Duration,
	result platform.LookupResult,
	lastLookupAt *time.Time,
) (effectCompletion, idempotency.RecoveryDecision) {
	state, decision := lookupState(result.Disposition)
	var retryAt *time.Time
	if state == idempotency.StateUnknown {
		delay := result.RetryAfter
		if delay <= 0 {
			delay = pollInterval
		}
		if delay <= 0 {
			delay = time.Second
		}
		value := now.Add(delay).UTC()
		retryAt = &value
	}
	return effectCompletion{
		state:             state,
		result:            append(json.RawMessage(nil), result.Response...),
		lookupDisposition: result.Disposition,
		externalRef:       result.ExternalRef,
		externalRequestID: result.ExternalRequestID,
		responseDigest:    result.ResponseDigest,
		errorCode:         result.ErrorCode,
		errorClass:        lookupErrorClass(result.Disposition),
		retryAt:           retryAt,
		lastLookupAt:      lastLookupAt,
	}, decision
}

func normalizeDispatchResult(result platform.DispatchResult) platform.DispatchResult {
	switch result.Disposition {
	case platform.EffectSucceeded:
		canonical, err := canonicalEffectResponse(result.Response)
		if err != nil {
			result.Disposition = platform.EffectUnknown
			result.Response = nil
			result.ErrorCode = "invalid_response"
		} else {
			result.Response = canonical
		}
	case platform.EffectRetryableFailed, platform.EffectPermanentFailed, platform.EffectUnknown:
	default:
		result.Disposition = platform.EffectUnknown
		result.ErrorCode = "invalid_disposition"
	}
	return result
}

func normalizeLookupResult(result platform.LookupResult) platform.LookupResult {
	switch result.Disposition {
	case platform.LookupApplied:
		canonical, err := canonicalEffectResponse(result.Response)
		if err != nil {
			result.Disposition = platform.LookupConflict
			result.Response = nil
			result.ErrorCode = "invalid_response"
		} else {
			result.Response = canonical
		}
	case platform.LookupRejected, platform.LookupAbsent, platform.LookupPending,
		platform.LookupConflict:
	default:
		result.Disposition = platform.LookupPending
		result.ErrorCode = "invalid_disposition"
	}
	return result
}

func canonicalEffectResponse(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("effect response is empty")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func mutationState(disposition platform.EffectDisposition) idempotency.State {
	switch disposition {
	case platform.EffectSucceeded:
		return idempotency.StateSucceeded
	case platform.EffectRetryableFailed:
		return idempotency.StateRetryableFailed
	case platform.EffectPermanentFailed:
		return idempotency.StatePermanentFailed
	default:
		return idempotency.StateUnknown
	}
}

func lookupState(
	disposition platform.LookupDisposition,
) (idempotency.State, idempotency.RecoveryDecision) {
	switch disposition {
	case platform.LookupApplied:
		return idempotency.StateSucceeded, idempotency.RecoveryResolved
	case platform.LookupRejected:
		return idempotency.StatePermanentFailed, idempotency.RecoveryPermanentFailure
	case platform.LookupAbsent:
		return idempotency.StateRetryableFailed, idempotency.RecoveryReadyToResume
	case platform.LookupConflict:
		return idempotency.StateManualReview, idempotency.RecoveryManualReview
	default:
		return idempotency.StateUnknown, idempotency.RecoveryPending
	}
}

func dispatchErrorClass(disposition platform.EffectDisposition) string {
	switch disposition {
	case platform.EffectRetryableFailed:
		return "dispatch_retryable"
	case platform.EffectPermanentFailed:
		return "dispatch_permanent"
	case platform.EffectUnknown:
		return "dispatch_unknown"
	default:
		return ""
	}
}

func lookupErrorClass(disposition platform.LookupDisposition) string {
	switch disposition {
	case platform.LookupRejected:
		return "lookup_rejected"
	case platform.LookupAbsent:
		return "lookup_absent"
	case platform.LookupPending:
		return "lookup_pending"
	case platform.LookupConflict:
		return "lookup_conflict"
	default:
		return ""
	}
}

func resultFromRecovery(
	outcome idempotency.RecoveryOutcome,
	err error,
) (idempotency.Result, error) {
	if err != nil {
		return idempotency.Result{}, err
	}
	switch outcome.Decision {
	case idempotency.RecoveryResolved:
		return outcome.Result, nil
	case idempotency.RecoveryReadyToResume:
		return idempotency.Result{}, idempotency.ErrRetryableFailure
	case idempotency.RecoveryPermanentFailure:
		return idempotency.Result{}, idempotency.ErrPermanentFailure
	case idempotency.RecoveryManualReview:
		return idempotency.Result{}, idempotency.ErrManualReview
	default:
		return idempotency.Result{}, idempotency.ErrReconciliationPending
	}
}

func effectResultError(sentinel error, code string) error {
	if code == "" {
		return sentinel
	}
	return errors.Join(sentinel, errors.New(code))
}

func validateEffectApproval(status approval.Status) error {
	if status != approval.StatusConfirmed &&
		status != approval.StatusReconciliationRequired &&
		status != approval.StatusExecuted {
		return approval.ErrApprovalNotGranted
	}
	return nil
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

func databaseEffectStateOrUnknown(status string) idempotency.State {
	state, ok := databaseEffectState(status)
	if !ok {
		return idempotency.StateUnknown
	}
	return state
}

var _ idempotency.Executor = (*Repository)(nil)
