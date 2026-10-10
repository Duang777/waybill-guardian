package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/execution"
)

type EffectWorkerConfig struct {
	TenantID     domain.TenantID
	WorkerID     string
	BatchSize    int
	LeaseTTL     time.Duration
	PollInterval time.Duration
	RetryBase    time.Duration
	RetryMax     time.Duration
}

type EffectWorker struct {
	application *Application
	config      EffectWorkerConfig
}

type effectObservation struct {
	SchemaVersion          string                 `json:"schema_version"`
	EffectID               domain.EffectID        `json:"effect_id"`
	Operation              domain.EffectOperation `json:"operation"`
	Disposition            execution.Disposition  `json:"disposition"`
	ExternalRef            string                 `json:"external_ref,omitempty"`
	ProviderResponse       json.RawMessage        `json:"provider_response,omitempty"`
	ProviderResponseDigest domain.ArtifactDigest  `json:"provider_response_digest,omitempty"`
	ErrorCode              string                 `json:"error_code,omitempty"`
	ObservedAt             time.Time              `json:"observed_at"`
}

func (application *Application) EffectWorker(
	config EffectWorkerConfig,
) (*EffectWorker, error) {
	if application.effectRegistry == nil {
		return nil, fmt.Errorf("delivery effect registry is not configured")
	}
	if config.TenantID == "" || strings.TrimSpace(config.WorkerID) == "" {
		return nil, fmt.Errorf("delivery effect worker tenant and ID are required")
	}
	if config.BatchSize == 0 {
		config.BatchSize = 8
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 30 * time.Second
	}
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.RetryBase == 0 {
		config.RetryBase = time.Second
	}
	if config.RetryMax == 0 {
		config.RetryMax = time.Minute
	}
	if config.BatchSize < 1 ||
		config.BatchSize > 100 ||
		config.LeaseTTL <= 0 ||
		config.PollInterval <= 0 ||
		config.RetryBase <= 0 ||
		config.RetryMax < config.RetryBase {
		return nil, fmt.Errorf("delivery effect worker configuration is invalid")
	}
	return &EffectWorker{application: application, config: config}, nil
}

func (worker *EffectWorker) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		didWork, err := worker.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			return err
		}
		delay := worker.config.PollInterval
		if didWork {
			delay = 0
		}
		timer.Reset(delay)
	}
}

func (worker *EffectWorker) RunOnce(ctx context.Context) (bool, error) {
	if err := worker.application.checkOpen(); err != nil {
		return false, err
	}
	now := worker.application.clock().UTC()
	expired, err := worker.application.store.ExpireApprovals(ctx, ExpireApprovals{
		TenantID: worker.config.TenantID,
		Limit:    worker.config.BatchSize,
		Now:      now,
	})
	if err != nil {
		return false, err
	}
	claims, err := worker.application.store.ClaimEffects(ctx, ClaimEffects{
		TenantID: worker.config.TenantID,
		WorkerID: worker.config.WorkerID,
		Limit:    worker.config.BatchSize,
		LeaseTTL: worker.config.LeaseTTL,
		Now:      now,
	})
	if err != nil {
		return expired > 0, err
	}
	var failures []error
	for _, claim := range claims {
		if err := worker.process(ctx, claim); err != nil && ctx.Err() == nil {
			failures = append(
				failures,
				fmt.Errorf("process effect %s: %w", claim.Effect.ID, err),
			)
		}
	}
	if len(claims) == 0 {
		recoveryErr := worker.recoverReadyExecutions(ctx)
		return expired > 0, recoveryErr
	}
	return true, errors.Join(failures...)
}

func (worker *EffectWorker) process(ctx context.Context, claim EffectClaim) error {
	binding, err := execution.DecodeBinding(claim.Effect.AdapterBinding)
	if err != nil {
		return worker.completeManualReview(ctx, claim, "binding_invalid")
	}
	if err := execution.VerifyBinding(claim.Effect, binding); err != nil {
		return worker.completeManualReview(ctx, claim, "binding_mismatch")
	}
	adapter, err := worker.application.effectRegistry.Adapter(
		claim.Effect.Action,
		claim.Effect.AdapterID,
		claim.Effect.ContractVersion,
	)
	if err != nil {
		return worker.completeManualReview(ctx, claim, "adapter_binding_changed")
	}
	if claim.Operation == domain.EffectOperationLookup &&
		claim.Effect.DispatchStartedAt == nil {
		return worker.completeManualReview(ctx, claim, "dispatch_time_missing")
	}

	callCtx, cancelCall := context.WithCancel(ctx)
	renewDone := make(chan error, 1)
	go worker.renewLease(callCtx, cancelCall, claim, renewDone)
	var result execution.Result
	switch claim.Operation {
	case domain.EffectOperationDispatch:
		result, err = adapter.Dispatch(callCtx, binding)
	case domain.EffectOperationLookup:
		result, err = adapter.Lookup(callCtx, binding, *claim.Effect.DispatchStartedAt)
	default:
		err = fmt.Errorf("unsupported effect operation %q", claim.Operation)
	}
	cancelCall()
	renewErr := <-renewDone
	if renewErr != nil {
		return renewErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	now := worker.application.clock().UTC()
	if err != nil {
		result = execution.Result{
			Disposition: execution.DispositionUnknown,
			ErrorCode:   "adapter_error",
			ObservedAt:  now,
		}
	}
	if result.ObservedAt.IsZero() {
		result.ObservedAt = now
	}
	responseArtifact, err := worker.storeObservation(ctx, claim, result)
	if err != nil {
		return err
	}
	completion := worker.completion(claim, result, responseArtifact, now)
	completed, err := worker.application.store.CompleteEffect(ctx, claim, completion)
	if err != nil {
		return err
	}
	if completed.ActivationReady {
		return worker.activateExecution(ctx, claim.Effect.ExecutionID)
	}
	return nil
}

func (worker *EffectWorker) renewLease(
	ctx context.Context,
	cancel context.CancelFunc,
	claim EffectClaim,
	done chan<- error,
) {
	interval := worker.config.LeaseTTL / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-ticker.C:
			deadline := worker.application.clock().UTC().Add(worker.config.LeaseTTL)
			if err := worker.application.store.RenewEffectLease(
				ctx,
				claim,
				deadline,
			); err != nil {
				cancel()
				done <- err
				return
			}
		}
	}
}

func (worker *EffectWorker) storeObservation(
	ctx context.Context,
	claim EffectClaim,
	result execution.Result,
) (domain.ArtifactDigest, error) {
	response := json.RawMessage(nil)
	if len(result.Response) > 0 && json.Valid(result.Response) {
		response = append(json.RawMessage(nil), result.Response...)
	}
	value, err := artifact.New(
		artifact.KindEffect,
		domain.EffectResultSchemaVersion,
		effectObservation{
			SchemaVersion:          domain.EffectResultSchemaVersion,
			EffectID:               claim.Effect.ID,
			Operation:              claim.Operation,
			Disposition:            result.Disposition,
			ExternalRef:            result.ExternalRef,
			ProviderResponse:       response,
			ProviderResponseDigest: result.ResponseDigest,
			ErrorCode:              result.ErrorCode,
			ObservedAt:             result.ObservedAt.UTC(),
		},
	)
	if err != nil {
		return "", err
	}
	ref, err := worker.application.artifacts.Put(ctx, claim.Effect.TenantID, value)
	if err != nil {
		return "", err
	}
	if err := worker.application.artifacts.Verify(
		ctx,
		claim.Effect.TenantID,
		ref.Digest,
	); err != nil {
		return "", err
	}
	return ref.Digest, nil
}

func (worker *EffectWorker) completion(
	claim EffectClaim,
	result execution.Result,
	responseDigest domain.ArtifactDigest,
	now time.Time,
) CompleteEffectTx {
	value := CompleteEffectTx{
		NextOperation:  claim.Operation,
		ExternalRef:    result.ExternalRef,
		ResponseDigest: responseDigest,
		ErrorCode:      result.ErrorCode,
		ObservedAt:     now,
	}
	switch result.Disposition {
	case execution.DispositionSucceeded:
		value.Status = domain.EffectSucceeded
	case execution.DispositionRetryableFailed:
		value.Status = domain.EffectRetryWait
		value.NextOperation = claim.Operation
		value.RetryAt = worker.retryAt(claim.Effect.Attempt, result.RetryAfter, now)
	case execution.DispositionPermanentFailed:
		value.Status = domain.EffectPermanentFailed
	case execution.DispositionUnknown, execution.DispositionPending:
		value.Status = domain.EffectUnknown
		value.NextOperation = domain.EffectOperationLookup
		value.RetryAt = worker.retryAt(claim.Effect.Attempt, result.RetryAfter, now)
	case execution.DispositionAuthoritativeAbsent:
		if now.Before(claim.Effect.KeyExpiresAt) {
			value.Status = domain.EffectRetryWait
			value.NextOperation = domain.EffectOperationDispatch
			value.RetryAt = worker.retryAt(claim.Effect.Attempt, result.RetryAfter, now)
		} else {
			value.Status = domain.EffectManualReview
			value.NextOperation = domain.EffectOperationLookup
			value.ErrorCode = "idempotency_key_expired"
		}
	default:
		value.Status = domain.EffectUnknown
		value.NextOperation = domain.EffectOperationLookup
		value.ErrorCode = "unsupported_adapter_result"
		value.RetryAt = worker.retryAt(claim.Effect.Attempt, 0, now)
	}
	return value
}

func (worker *EffectWorker) retryAt(
	attempt uint32,
	requested time.Duration,
	now time.Time,
) *time.Time {
	delay := worker.config.RetryBase
	for index := uint32(1); index < attempt && delay < worker.config.RetryMax; index++ {
		if delay > worker.config.RetryMax/2 {
			delay = worker.config.RetryMax
			break
		}
		delay *= 2
	}
	if requested > delay {
		delay = requested
	}
	if delay > worker.config.RetryMax {
		delay = worker.config.RetryMax
	}
	value := now.Add(delay)
	return &value
}

func (worker *EffectWorker) completeManualReview(
	ctx context.Context,
	claim EffectClaim,
	code string,
) error {
	now := worker.application.clock().UTC()
	result, err := worker.storeObservation(ctx, claim, execution.Result{
		Disposition: execution.DispositionPermanentFailed,
		ErrorCode:   code,
		ObservedAt:  now,
	})
	if err != nil {
		return err
	}
	_, err = worker.application.store.CompleteEffect(ctx, claim, CompleteEffectTx{
		Status:         domain.EffectManualReview,
		NextOperation:  domain.EffectOperationLookup,
		ResponseDigest: result,
		ErrorCode:      code,
		ObservedAt:     now,
	})
	return err
}

func (worker *EffectWorker) activateExecution(
	ctx context.Context,
	executionID domain.ExecutionID,
) error {
	return worker.application.activateReadyExecution(
		ctx,
		worker.config.TenantID,
		executionID,
	)
}

func (application *Application) activateReadyExecution(
	ctx context.Context,
	tenantID domain.TenantID,
	executionID domain.ExecutionID,
) error {
	value, _, err := application.store.GetExecution(ctx, tenantID, executionID)
	if err != nil {
		return err
	}
	approval, err := application.store.GetApproval(
		ctx,
		value.TenantID,
		value.ApprovalID,
	)
	if err != nil {
		return err
	}
	err = application.store.ActivateRevision(ctx, ActivateRevisionTx{
		TenantID:      value.TenantID,
		ExecutionID:   value.ID,
		PlanID:        value.PlanID,
		RevisionID:    value.RevisionID,
		ActiveVersion: approval.Binding.ActiveVersion,
		Now:           application.clock().UTC(),
	})
	if !errors.Is(err, ErrApprovalStale) {
		return err
	}
	staleExecution, _, getErr := application.store.GetExecution(
		ctx,
		tenantID,
		executionID,
	)
	if getErr != nil {
		return errors.Join(err, getErr)
	}
	staleApproval, getErr := application.store.GetApproval(
		ctx,
		tenantID,
		staleExecution.ApprovalID,
	)
	if getErr != nil {
		return errors.Join(err, getErr)
	}
	if staleExecution.Status != domain.ExecutionPartiallyApplied ||
		staleApproval.Status != domain.ApprovalStale {
		return err
	}
	return nil
}

func (worker *EffectWorker) recoverReadyExecutions(ctx context.Context) error {
	items, err := worker.application.store.ScanRecovery(ctx, RecoveryScan{
		TenantID: worker.config.TenantID,
		Limit:    worker.config.BatchSize,
		Now:      worker.application.clock().UTC(),
	})
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range items {
		if item.Kind != "execution_ready" {
			continue
		}
		if err := worker.activateExecution(
			ctx,
			domain.ExecutionID(item.ResourceID),
		); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
