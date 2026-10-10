package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
	"github.com/Duang777/waybill-guardian/internal/delivery/service"
)

const maxValidationViolations = 50

type HandlersConfig struct {
	Platform       service.Platform
	TenantID       domain.TenantID
	Actor          service.Actor
	SolverProfiles []string
	ApprovalTTL    time.Duration
}

type Handlers struct {
	commands       service.Commands
	queries        service.Queries
	tenantID       domain.TenantID
	actor          service.Actor
	solverProfiles map[string]struct{}
	approvalTTL    time.Duration
}

func NewHandlers(config HandlersConfig) (*Handlers, error) {
	if config.Platform == nil ||
		config.TenantID == "" ||
		strings.TrimSpace(config.Actor.Subject) == "" ||
		len(config.SolverProfiles) == 0 ||
		config.ApprovalTTL <= 0 ||
		config.ApprovalTTL > 24*time.Hour {
		return nil, fmt.Errorf("delivery agent handler configuration is incomplete")
	}
	profiles := make(map[string]struct{}, len(config.SolverProfiles))
	for _, profile := range config.SolverProfiles {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			return nil, fmt.Errorf("delivery agent solver profile is invalid")
		}
		if _, exists := profiles[profile]; exists {
			return nil, fmt.Errorf("delivery agent solver profile %q is repeated", profile)
		}
		profiles[profile] = struct{}{}
	}
	return &Handlers{
		commands:       config.Platform.Commands(),
		queries:        config.Platform.Queries(),
		tenantID:       config.TenantID,
		actor:          config.Actor,
		solverProfiles: profiles,
		approvalTTL:    config.ApprovalTTL,
	}, nil
}

func (handlers *Handlers) GetProblemSummary(
	ctx context.Context,
	input GetProblemSummaryInput,
) (ProblemSummary, error) {
	if input.ProblemID == "" || input.Version == 0 {
		return ProblemSummary{}, fmt.Errorf("problem_id and version are required")
	}
	version, err := handlers.queries.GetProblem(
		ctx,
		handlers.tenantID,
		input.ProblemID,
		input.Version,
	)
	if err != nil {
		return ProblemSummary{}, err
	}
	var problem domain.ProblemSnapshot
	if err := handlers.readArtifact(
		ctx,
		version.ProblemArtifact,
		artifact.KindProblem,
		domain.ProblemSchemaVersion,
		&problem,
	); err != nil {
		return ProblemSummary{}, err
	}
	rebuilt, err := service.BuildProblemSnapshot(problem)
	if err != nil ||
		rebuilt.ProblemDigest != version.ProblemDigest ||
		rebuilt.PolicyDigest != version.PolicyDigest ||
		rebuilt.CommitmentDigest != version.CommitmentDigest {
		return ProblemSummary{}, service.ErrConflict
	}
	return ProblemSummary{
		ProblemID:        version.ProblemID,
		Version:          version.Version,
		ProblemDigest:    version.ProblemDigest,
		PolicyDigest:     version.PolicyDigest,
		CommitmentDigest: version.CommitmentDigest,
		Horizon:          rebuilt.Horizon,
		Locations:        len(rebuilt.Locations),
		Depots:           len(rebuilt.Depots),
		Requests:         len(rebuilt.Requests),
		Units:            len(rebuilt.Units),
		Cargo:            len(rebuilt.Cargo),
		Vehicles:         len(rebuilt.Vehicles),
		Drivers:          len(rebuilt.Drivers),
		Chargers:         len(rebuilt.Chargers),
	}, nil
}

func (handlers *Handlers) RequestOptimization(
	ctx context.Context,
	input RequestOptimizationInput,
) (OptimizationRequested, error) {
	profile := strings.TrimSpace(input.SolverProfile)
	if input.ProblemID == "" || input.ProblemVersion == 0 {
		return OptimizationRequested{}, fmt.Errorf("problem_id and problem_version are required")
	}
	if _, exists := handlers.solverProfiles[profile]; !exists {
		return OptimizationRequested{}, fmt.Errorf("solver_profile is not registered")
	}
	key, err := handlers.commandKey(ctx, "request_optimization", input)
	if err != nil {
		return OptimizationRequested{}, err
	}
	run, replay, err := handlers.commands.RequestOptimization(
		ctx,
		service.RequestOptimization{
			TenantID:       handlers.tenantID,
			Actor:          handlers.actor,
			IdempotencyKey: key,
			ProblemID:      input.ProblemID,
			ProblemVersion: input.ProblemVersion,
			SolverProfile:  profile,
		},
	)
	if err != nil {
		return OptimizationRequested{}, err
	}
	return OptimizationRequested{
		RunID:          run.ID,
		ProblemID:      run.ProblemID,
		ProblemVersion: run.ProblemVersion,
		Status:         run.Status,
		Version:        run.Version,
		Replayed:       replay.Replayed,
	}, nil
}

func (handlers *Handlers) GetCandidate(
	ctx context.Context,
	input GetCandidateInput,
) (CandidateSummary, error) {
	if input.RunID == "" {
		return CandidateSummary{}, fmt.Errorf("run_id is required")
	}
	run, err := handlers.queries.GetRun(ctx, handlers.tenantID, input.RunID)
	if err != nil {
		return CandidateSummary{}, err
	}
	if run.ResultRevisionID == "" {
		return CandidateSummary{}, service.ErrConflict
	}
	revision, plan, report, err := handlers.loadRevision(
		ctx,
		run.ResultRevisionID,
	)
	if err != nil {
		return CandidateSummary{}, err
	}
	return CandidateSummary{
		RunID:                  run.ID,
		RevisionID:             revision.ID,
		PlanID:                 revision.PlanID,
		RevisionStatus:         revision.Status,
		PlanDigest:             plan.PlanDigest,
		ValidationReportDigest: report.ReportDigest,
		Valid:                  report.Valid,
		ViolationCount:         len(report.Violations),
		Objective:              plan.Objective,
		Metrics:                report.Metrics,
	}, nil
}

func (handlers *Handlers) CompareRevisions(
	ctx context.Context,
	input CompareRevisionsInput,
) (RevisionComparison, error) {
	if input.BaseRevisionID == "" ||
		input.CandidateRevisionID == "" ||
		input.BaseRevisionID == input.CandidateRevisionID {
		return RevisionComparison{}, fmt.Errorf("two distinct revision IDs are required")
	}
	baseRevision, basePlan, baseReport, err := handlers.loadRevision(
		ctx,
		input.BaseRevisionID,
	)
	if err != nil {
		return RevisionComparison{}, err
	}
	candidateRevision, candidatePlan, candidateReport, err := handlers.loadRevision(
		ctx,
		input.CandidateRevisionID,
	)
	if err != nil {
		return RevisionComparison{}, err
	}
	if baseRevision.PlanID != candidateRevision.PlanID {
		return RevisionComparison{}, service.ErrConflict
	}
	return RevisionComparison{
		BaseRevisionID:      baseRevision.ID,
		CandidateRevisionID: candidateRevision.ID,
		BasePlanDigest:      basePlan.PlanDigest,
		CandidatePlanDigest: candidatePlan.PlanDigest,
		BaseMetrics:         baseReport.Metrics,
		CandidateMetrics:    candidateReport.Metrics,
		Delta:               metricDelta(baseReport.Metrics, candidateReport.Metrics),
	}, nil
}

func (handlers *Handlers) ExplainValidation(
	ctx context.Context,
	input ExplainValidationInput,
) (ValidationExplanation, error) {
	if input.RevisionID == "" {
		return ValidationExplanation{}, fmt.Errorf("revision_id is required")
	}
	_, _, report, err := handlers.loadRevision(ctx, input.RevisionID)
	if err != nil {
		return ValidationExplanation{}, err
	}
	count := min(len(report.Violations), maxValidationViolations)
	violations := append([]domain.Violation(nil), report.Violations[:count]...)
	return ValidationExplanation{
		RevisionID:     input.RevisionID,
		Valid:          report.Valid,
		ViolationCount: len(report.Violations),
		Violations:     violations,
		Metrics:        report.Metrics,
		Validator:      report.Validator,
		ReportDigest:   report.ReportDigest,
		Truncated:      count != len(report.Violations),
	}, nil
}

func (handlers *Handlers) RequestPlanApproval(
	ctx context.Context,
	input RequestPlanApprovalInput,
) (ApprovalRequested, error) {
	reason := strings.TrimSpace(input.Reason)
	if input.RevisionID == "" || reason == "" || len(reason) > 1_000 {
		return ApprovalRequested{}, fmt.Errorf("revision_id and a short reason are required")
	}
	revision, err := handlers.queries.GetRevision(
		ctx,
		handlers.tenantID,
		input.RevisionID,
	)
	if err != nil {
		return ApprovalRequested{}, err
	}
	key, err := handlers.commandKey(ctx, "request_plan_approval", input)
	if err != nil {
		return ApprovalRequested{}, err
	}
	approval, replay, err := handlers.commands.RequestPlanApproval(
		ctx,
		service.RequestPlanApproval{
			TenantID:        handlers.tenantID,
			Actor:           handlers.actor,
			IdempotencyKey:  key,
			RevisionID:      revision.ID,
			ExpectedVersion: revision.Version,
			TTL:             handlers.approvalTTL,
			Reason:          reason,
		},
	)
	if err != nil {
		return ApprovalRequested{}, err
	}
	return ApprovalRequested{
		ApprovalID:      approval.ID,
		RevisionID:      approval.Binding.RevisionID,
		Status:          approval.Status,
		Version:         approval.Version,
		ExpiresAt:       approval.ExpiresAt,
		EffectSetDigest: approval.Binding.EffectSetDigest,
		Replayed:        replay.Replayed,
	}, nil
}
