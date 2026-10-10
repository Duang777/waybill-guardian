package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	deliveryexecution "github.com/Duang777/waybill-guardian/internal/delivery/execution"
)

const maxExecutionArtifactBytes = 128 << 20

func (application *Application) RequestPlanApproval(
	ctx context.Context,
	request RequestPlanApproval,
) (domain.PlanApproval, Replay, error) {
	if err := application.checkOpen(); err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	if application.effectPlanner == nil || application.effectRegistry == nil {
		return domain.PlanApproval{}, Replay{}, fmt.Errorf("delivery execution is not configured")
	}
	if request.TenantID == "" ||
		request.Actor.Subject == "" ||
		request.IdempotencyKey == "" ||
		request.RevisionID == "" ||
		request.ExpectedVersion == 0 ||
		request.TTL <= 0 ||
		request.TTL > 24*time.Hour ||
		strings.TrimSpace(request.Reason) == "" ||
		len(strings.TrimSpace(request.Reason)) > 1_000 {
		return domain.PlanApproval{}, Replay{}, fmt.Errorf("approval request is incomplete")
	}
	revision, err := application.store.GetRevision(ctx, request.TenantID, request.RevisionID)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	if revision.Version != request.ExpectedVersion ||
		revision.Status != domain.RevisionValidated {
		return domain.PlanApproval{}, Replay{}, ErrConflict
	}
	planRecord, err := application.store.GetPlan(ctx, request.TenantID, revision.PlanID)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	run, err := application.store.GetRun(ctx, request.TenantID, revision.RunID)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	if run.ResultRevisionID != revision.ID ||
		run.RequestedBy == "" ||
		run.RequestedBy == "system:unknown" {
		return domain.PlanApproval{}, Replay{}, ErrConflict
	}
	plan, err := application.loadPlan(ctx, request.TenantID, revision)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	report, err := application.loadValidation(ctx, request.TenantID, revision)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	if !report.Valid ||
		report.PlanDigest != revision.PlanDigest ||
		report.ReportDigest != revision.ValidationReportDigest ||
		report.ProblemDigest != revision.ProblemDigest ||
		report.PolicyDigest != revision.PolicyDigest ||
		report.CommitmentDigest != revision.CommitmentDigest {
		return domain.PlanApproval{}, Replay{}, ErrConflict
	}
	effectSet, err := application.effectPlanner.BuildEffectSet(
		request.TenantID,
		revision.ID,
		plan,
	)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	if err := deliveryexecution.VerifyEffectSet(effectSet); err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	effectArtifact, err := artifact.New(
		artifact.KindEffect,
		domain.EffectSetSchemaVersion,
		effectSet,
	)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	effectRef, err := application.artifacts.Put(ctx, request.TenantID, effectArtifact)
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	if err := application.artifacts.Verify(ctx, request.TenantID, effectRef.Digest); err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	now := application.clock().UTC()
	approval := domain.PlanApproval{
		TenantID: request.TenantID,
		ID:       domain.ApprovalID(application.newID("approval")),
		Binding: domain.ApprovalBinding{
			TenantID:               request.TenantID,
			PlanID:                 revision.PlanID,
			RevisionID:             revision.ID,
			BaseRevisionID:         revision.BaseRevisionID,
			ActiveVersion:          planRecord.ActiveVersion,
			ProblemDigest:          revision.ProblemDigest,
			PolicyDigest:           revision.PolicyDigest,
			CommitmentDigest:       revision.CommitmentDigest,
			PlanDigest:             revision.PlanDigest,
			ValidationReportDigest: revision.ValidationReportDigest,
			EffectSetDigest:        effectSet.Digest,
		},
		EffectSetArtifact: effectRef.Digest,
		Status:            domain.ApprovalPending,
		Version:           1,
		RequestedBy:       request.Actor.Subject,
		PlanCreatedBy:     run.RequestedBy,
		Reason:            strings.TrimSpace(request.Reason),
		RequestedAt:       now,
		ExpiresAt:         now.Add(request.TTL),
	}
	requestDigest, err := domain.Digest(struct {
		Operation       string
		TenantID        domain.TenantID
		RevisionID      domain.PlanRevisionID
		RevisionVersion uint64
		EffectSetDigest domain.ArtifactDigest
		TTLSeconds      int64
		Reason          string
	}{
		Operation:       "delivery.request_plan_approval.v1",
		TenantID:        request.TenantID,
		RevisionID:      request.RevisionID,
		RevisionVersion: request.ExpectedVersion,
		EffectSetDigest: effectSet.Digest,
		TTLSeconds:      int64(request.TTL / time.Second),
		Reason:          approval.Reason,
	})
	if err != nil {
		return domain.PlanApproval{}, Replay{}, err
	}
	return application.store.PrepareApproval(ctx, PrepareApprovalTx{
		IdempotencyKey:          request.IdempotencyKey,
		RequestDigest:           requestDigest,
		ExpectedRevisionVersion: request.ExpectedVersion,
		Actor:                   request.Actor,
		Approval:                approval,
	})
}

func (application *Application) DecideApproval(
	ctx context.Context,
	request DecideApproval,
) (domain.DispatchExecution, Replay, error) {
	if err := application.checkOpen(); err != nil {
		return domain.DispatchExecution{}, Replay{}, err
	}
	if request.TenantID == "" ||
		request.Actor.Subject == "" ||
		request.IdempotencyKey == "" ||
		request.ApprovalID == "" ||
		request.ExpectedVersion == 0 ||
		(request.Decision != ConfirmApproval && request.Decision != RejectApproval) {
		return domain.DispatchExecution{}, Replay{}, fmt.Errorf("approval decision is incomplete")
	}
	if request.Decision == RejectApproval && strings.TrimSpace(request.RejectReason) == "" {
		return domain.DispatchExecution{}, Replay{}, fmt.Errorf("reject reason is required")
	}
	if request.Decision == RejectApproval && strings.TrimSpace(request.OverrideReason) != "" {
		return domain.DispatchExecution{}, Replay{}, fmt.Errorf("reject decision cannot use override")
	}
	approval, err := application.store.GetApproval(
		ctx,
		request.TenantID,
		request.ApprovalID,
	)
	if err != nil {
		return domain.DispatchExecution{}, Replay{}, err
	}
	if approval.Version != request.ExpectedVersion ||
		approval.Status != domain.ApprovalPending {
		return domain.DispatchExecution{}, Replay{}, ErrConflict
	}
	now := application.clock().UTC()
	if !now.Before(approval.ExpiresAt) {
		return domain.DispatchExecution{}, Replay{}, ErrApprovalExpired
	}
	overrideReason := strings.TrimSpace(request.OverrideReason)
	if request.Decision == ConfirmApproval &&
		request.Actor.Subject == approval.PlanCreatedBy &&
		overrideReason == "" {
		return domain.DispatchExecution{}, Replay{}, ErrSeparationOfDuties
	}
	effectSet, err := application.loadEffectSet(ctx, approval)
	if err != nil {
		return domain.DispatchExecution{}, Replay{}, err
	}
	execution := domain.DispatchExecution{}
	var effects []domain.EffectRecord
	if request.Decision == ConfirmApproval {
		execution = domain.DispatchExecution{
			TenantID:        request.TenantID,
			ID:              domain.ExecutionID(application.newID("execution")),
			ApprovalID:      approval.ID,
			PlanID:          approval.Binding.PlanID,
			RevisionID:      approval.Binding.RevisionID,
			EffectSetDigest: effectSet.Digest,
			Status:          domain.ExecutionPrepared,
			Version:         1,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		effects = make([]domain.EffectRecord, 0, len(effectSet.Effects))
		for _, preview := range effectSet.Effects {
			key := "delivery-effect-v1:" + string(preview.ID)
			binding, bindErr := application.effectRegistry.Bind(preview, key, now)
			if bindErr != nil {
				return domain.DispatchExecution{}, Replay{}, bindErr
			}
			bindingRaw, bindErr := domain.CanonicalJSON(binding)
			if bindErr != nil {
				return domain.DispatchExecution{}, Replay{}, bindErr
			}
			bindingDigest, bindErr := domain.DigestCanonicalJSON(bindingRaw)
			if bindErr != nil {
				return domain.DispatchExecution{}, Replay{}, bindErr
			}
			effects = append(effects, domain.EffectRecord{
				TenantID:                       request.TenantID,
				ID:                             preview.ID,
				ExecutionID:                    execution.ID,
				RevisionID:                     execution.RevisionID,
				Ordinal:                        preview.Ordinal,
				Action:                         preview.Action,
				Target:                         preview.Target,
				Parameters:                     append(json.RawMessage(nil), preview.Parameters...),
				ParametersDigest:               preview.ParametersDigest,
				Required:                       preview.Required,
				AdapterID:                      preview.AdapterID,
				ContractVersion:                preview.ContractVersion,
				AdapterBinding:                 json.RawMessage(bindingRaw),
				AdapterBindingDigest:           bindingDigest,
				IdempotencyKey:                 key,
				RequestDigest:                  binding.RequestDigest,
				KeyCreatedAt:                   binding.CreatedAt,
				KeyExpiresAt:                   binding.ExpiresAt,
				LookupConsistencyWindowSeconds: preview.LookupConsistencyWindowSeconds,
				Status:                         domain.EffectPrepared,
				NextOperation:                  domain.EffectOperationDispatch,
				UpdatedAt:                      now,
			})
		}
	}
	requestDigest, err := domain.Digest(struct {
		Operation                string
		TenantID                 domain.TenantID
		ApprovalID               domain.ApprovalID
		ApprovalVersion          uint64
		ExpectedActiveRevisionID domain.PlanRevisionID
		ExpectedActiveVersion    uint64
		Decision                 ApprovalDecision
		RejectReason             string
		OverrideReason           string
	}{
		Operation:                "delivery.decide_approval.v2",
		TenantID:                 request.TenantID,
		ApprovalID:               request.ApprovalID,
		ApprovalVersion:          request.ExpectedVersion,
		ExpectedActiveRevisionID: approval.Binding.BaseRevisionID,
		ExpectedActiveVersion:    approval.Binding.ActiveVersion,
		Decision:                 request.Decision,
		RejectReason:             strings.TrimSpace(request.RejectReason),
		OverrideReason:           overrideReason,
	})
	if err != nil {
		return domain.DispatchExecution{}, Replay{}, err
	}
	return application.store.DecideAndCreateExecution(ctx, DecideExecutionTx{
		TenantID:                 request.TenantID,
		IdempotencyKey:           request.IdempotencyKey,
		RequestDigest:            requestDigest,
		Actor:                    request.Actor,
		ApprovalID:               request.ApprovalID,
		ExpectedVersion:          request.ExpectedVersion,
		ExpectedActiveRevisionID: approval.Binding.BaseRevisionID,
		ExpectedActiveVersion:    approval.Binding.ActiveVersion,
		Decision:                 request.Decision,
		RejectReason:             strings.TrimSpace(request.RejectReason),
		OverrideReason:           overrideReason,
		Execution:                execution,
		Effects:                  effects,
		Now:                      now,
	})
}

func (application *Application) GetApproval(
	ctx context.Context,
	tenantID domain.TenantID,
	approvalID domain.ApprovalID,
) (domain.PlanApproval, error) {
	if err := application.checkOpen(); err != nil {
		return domain.PlanApproval{}, err
	}
	return application.store.GetApproval(ctx, tenantID, approvalID)
}

func (application *Application) GetExecution(
	ctx context.Context,
	tenantID domain.TenantID,
	executionID domain.ExecutionID,
) (domain.DispatchExecution, []domain.EffectRecord, error) {
	if err := application.checkOpen(); err != nil {
		return domain.DispatchExecution{}, nil, err
	}
	return application.store.GetExecution(ctx, tenantID, executionID)
}

func (application *Application) loadPlan(
	ctx context.Context,
	tenantID domain.TenantID,
	revision domain.PlanRevision,
) (domain.Plan, error) {
	raw, err := application.readArtifact(
		ctx,
		tenantID,
		revision.PlanArtifactDigest,
		artifact.KindPlan,
		domain.PlanSchemaVersion,
	)
	if err != nil {
		return domain.Plan{}, err
	}
	var plan domain.Plan
	if err := decodeStrictArtifact(raw, &plan); err != nil {
		return domain.Plan{}, err
	}
	digest, err := domain.ComputePlanDigest(plan)
	if err != nil ||
		digest != plan.PlanDigest ||
		plan.PlanDigest != revision.PlanDigest ||
		plan.RevisionID != revision.ID {
		return domain.Plan{}, ErrConflict
	}
	return plan, nil
}

func (application *Application) loadValidation(
	ctx context.Context,
	tenantID domain.TenantID,
	revision domain.PlanRevision,
) (domain.ValidationReport, error) {
	raw, err := application.readArtifact(
		ctx,
		tenantID,
		revision.ValidationArtifact,
		artifact.KindValidationReport,
		domain.ValidationSchemaVersion,
	)
	if err != nil {
		return domain.ValidationReport{}, err
	}
	var report domain.ValidationReport
	if err := decodeStrictArtifact(raw, &report); err != nil {
		return domain.ValidationReport{}, err
	}
	digest, err := domain.ComputeReportDigest(report)
	if err != nil || digest != report.ReportDigest {
		return domain.ValidationReport{}, ErrConflict
	}
	return report, nil
}

func (application *Application) loadEffectSet(
	ctx context.Context,
	approval domain.PlanApproval,
) (domain.EffectSet, error) {
	raw, err := application.readArtifact(
		ctx,
		approval.TenantID,
		approval.EffectSetArtifact,
		artifact.KindEffect,
		domain.EffectSetSchemaVersion,
	)
	if err != nil {
		return domain.EffectSet{}, err
	}
	var effectSet domain.EffectSet
	if err := decodeStrictArtifact(raw, &effectSet); err != nil {
		return domain.EffectSet{}, err
	}
	if err := deliveryexecution.VerifyEffectSet(effectSet); err != nil {
		return domain.EffectSet{}, err
	}
	if effectSet.Digest != approval.Binding.EffectSetDigest ||
		effectSet.RevisionID != approval.Binding.RevisionID ||
		effectSet.TenantID != approval.TenantID {
		return domain.EffectSet{}, ErrConflict
	}
	return effectSet, nil
}

func (application *Application) readArtifact(
	ctx context.Context,
	tenantID domain.TenantID,
	digest domain.ArtifactDigest,
	kind artifact.Kind,
	schema string,
) ([]byte, error) {
	reader, metadata, err := application.artifacts.Open(ctx, tenantID, digest)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	if metadata.Kind != kind || metadata.SchemaVersion != schema {
		return nil, ErrConflict
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxExecutionArtifactBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxExecutionArtifactBytes {
		return nil, artifact.ErrTooLarge
	}
	return raw, nil
}

func decodeStrictArtifact(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("artifact has trailing JSON")
	}
	return nil
}
