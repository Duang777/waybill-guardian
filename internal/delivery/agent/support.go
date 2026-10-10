package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
)

func (handlers *Handlers) loadRevision(
	ctx context.Context,
	revisionID domain.PlanRevisionID,
) (domain.PlanRevision, domain.Plan, domain.ValidationReport, error) {
	revision, err := handlers.queries.GetRevision(ctx, handlers.tenantID, revisionID)
	if err != nil {
		return domain.PlanRevision{}, domain.Plan{}, domain.ValidationReport{}, err
	}
	var plan domain.Plan
	if err := handlers.readArtifact(
		ctx,
		revision.PlanArtifactDigest,
		artifact.KindPlan,
		domain.PlanSchemaVersion,
		&plan,
	); err != nil {
		return domain.PlanRevision{}, domain.Plan{}, domain.ValidationReport{}, err
	}
	planDigest, err := domain.ComputePlanDigest(plan)
	if err != nil ||
		planDigest != plan.PlanDigest ||
		plan.PlanDigest != revision.PlanDigest ||
		plan.RevisionID != revision.ID {
		return domain.PlanRevision{}, domain.Plan{}, domain.ValidationReport{},
			service.ErrConflict
	}
	var report domain.ValidationReport
	if err := handlers.readArtifact(
		ctx,
		revision.ValidationArtifact,
		artifact.KindValidationReport,
		domain.ValidationSchemaVersion,
		&report,
	); err != nil {
		return domain.PlanRevision{}, domain.Plan{}, domain.ValidationReport{}, err
	}
	reportDigest, err := domain.ComputeReportDigest(report)
	if err != nil ||
		reportDigest != report.ReportDigest ||
		report.ReportDigest != revision.ValidationReportDigest ||
		report.PlanDigest != plan.PlanDigest ||
		report.ProblemDigest != revision.ProblemDigest ||
		report.PolicyDigest != revision.PolicyDigest ||
		report.CommitmentDigest != revision.CommitmentDigest {
		return domain.PlanRevision{}, domain.Plan{}, domain.ValidationReport{},
			service.ErrConflict
	}
	return revision, plan, report, nil
}

func (handlers *Handlers) readArtifact(
	ctx context.Context,
	digest domain.ArtifactDigest,
	kind artifact.Kind,
	schema string,
	destination any,
) error {
	reader, metadata, err := handlers.queries.OpenArtifact(
		ctx,
		handlers.tenantID,
		digest,
	)
	if err != nil {
		return err
	}
	defer reader.Close()
	if metadata.Kind != kind ||
		metadata.SchemaVersion != schema ||
		metadata.Digest != digest {
		return service.ErrConflict
	}
	decoder := json.NewDecoder(io.LimitReader(reader, 128<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("delivery agent artifact has a trailing value")
	}
	return nil
}

func (handlers *Handlers) commandKey(
	ctx context.Context,
	operation string,
	input any,
) (service.IdempotencyKey, error) {
	call, _ := ctx.Value(callIdentityKey{}).(callIdentity)
	digest, err := domain.Digest(struct {
		Version   string
		TenantID  domain.TenantID
		Actor     string
		Operation string
		ThreadID  string
		CallID    string
		Input     any
	}{
		Version:   "delivery.agent-command-key.v1",
		TenantID:  handlers.tenantID,
		Actor:     handlers.actor.Subject,
		Operation: operation,
		ThreadID:  call.ThreadID,
		CallID:    call.CallID,
		Input:     input,
	})
	if err != nil {
		return "", err
	}
	return service.IdempotencyKey("delivery-agent-v1:" + string(digest)), nil
}

func metricDelta(
	base domain.PlanMetrics,
	candidate domain.PlanMetrics,
) PlanMetricDelta {
	return PlanMetricDelta{
		VehiclesUsed:              int64(candidate.VehiclesUsed) - int64(base.VehiclesUsed),
		Trips:                     int64(candidate.Trips) - int64(base.Trips),
		Stops:                     int64(candidate.Stops) - int64(base.Stops),
		TotalDistanceMeters:       candidate.TotalDistanceMeters - base.TotalDistanceMeters,
		TotalDriveSeconds:         candidate.TotalDriveSeconds - base.TotalDriveSeconds,
		TotalCostCents:            candidate.TotalCostCents - base.TotalCostCents,
		OnTimeTasks:               int64(candidate.OnTimeTasks) - int64(base.OnTimeTasks),
		LateTasks:                 int64(candidate.LateTasks) - int64(base.LateTasks),
		OnTimeRatePPM:             candidate.OnTimeRatePPM - base.OnTimeRatePPM,
		MeanVolumeUtilizationPPM:  candidate.MeanVolumeUtilizationPPM - base.MeanVolumeUtilizationPPM,
		MeanPayloadUtilizationPPM: candidate.MeanPayloadUtilizationPPM - base.MeanPayloadUtilizationPPM,
		Rehandles:                 int64(candidate.Rehandles) - int64(base.Rehandles),
	}
}

func decodeStrictInput(raw string, destination any) error {
	canonical, err := domain.CanonicalizeJSON([]byte(raw))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("tool input has a trailing value")
	}
	return nil
}
