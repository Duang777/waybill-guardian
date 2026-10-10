package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/delivery/artifact"
	"github.com/Duang777/waybill-guardian/internal/delivery/domain"
)

const maxProblemArtifactBytes = 128 << 20

type WorkerConfig struct {
	TenantID     domain.TenantID
	WorkerID     string
	BatchSize    int
	LeaseTTL     time.Duration
	PollInterval time.Duration
}

type RunWorker struct {
	application *Application
	config      WorkerConfig
}

func (application *Application) RunWorker(config WorkerConfig) (*RunWorker, error) {
	if strings.TrimSpace(string(config.TenantID)) == "" ||
		strings.TrimSpace(config.WorkerID) == "" {
		return nil, fmt.Errorf("delivery run worker tenant and ID are required")
	}
	if config.BatchSize == 0 {
		config.BatchSize = 4
	}
	if config.LeaseTTL == 0 {
		config.LeaseTTL = 30 * time.Second
	}
	if config.PollInterval == 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.BatchSize < 1 ||
		config.BatchSize > 100 ||
		config.LeaseTTL <= 0 ||
		config.PollInterval <= 0 {
		return nil, fmt.Errorf("delivery run worker configuration is invalid")
	}
	return &RunWorker{application: application, config: config}, nil
}

func (worker *RunWorker) Run(ctx context.Context) error {
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

func (worker *RunWorker) RunOnce(ctx context.Context) (bool, error) {
	if err := worker.application.checkOpen(); err != nil {
		return false, err
	}
	claims, err := worker.application.store.ClaimRuns(ctx, ClaimRuns{
		TenantID: worker.config.TenantID,
		WorkerID: worker.config.WorkerID,
		Limit:    worker.config.BatchSize,
		LeaseTTL: worker.config.LeaseTTL,
		Now:      worker.application.clock().UTC(),
	})
	if err != nil {
		return false, err
	}
	if len(claims) == 0 {
		return false, nil
	}
	var failures []error
	for _, claim := range claims {
		if err := worker.process(ctx, claim); err != nil && ctx.Err() == nil {
			failures = append(failures, fmt.Errorf("process run %s: %w", claim.Run.ID, err))
		}
	}
	return true, errors.Join(failures...)
}

func (worker *RunWorker) process(ctx context.Context, claim RunClaim) error {
	problemVersion, err := worker.application.store.GetProblem(
		ctx,
		claim.Run.TenantID,
		claim.Run.ProblemID,
		claim.Run.ProblemVersion,
	)
	if err != nil {
		return worker.finishFailure(ctx, claim, domain.RunManualReview, "problem_missing")
	}
	if problemVersion.ProblemDigest != claim.Run.ProblemDigest {
		return worker.finishFailure(ctx, claim, domain.RunManualReview, "problem_digest_mismatch")
	}
	problem, err := worker.loadProblem(ctx, problemVersion)
	if err != nil {
		return worker.finishFailure(ctx, claim, domain.RunManualReview, "problem_artifact_invalid")
	}
	binding, exists := worker.application.solvers[claim.Run.SolverProfile]
	if !exists || binding.ConfigDigest != claim.Run.ConfigDigest {
		return worker.finishFailure(ctx, claim, domain.RunManualReview, "solver_profile_missing")
	}
	if err := worker.application.store.SaveRunCheckpoint(ctx, claim, SaveCheckpointTx{
		Status: domain.RunSolving,
		Now:    worker.application.clock().UTC(),
	}); err != nil {
		return err
	}

	solveCtx, cancelSolve := context.WithCancel(ctx)
	defer cancelSolve()
	renewDone := make(chan error, 1)
	go worker.renewLease(solveCtx, cancelSolve, claim, renewDone)
	progress := &runProgressSink{
		application: worker.application,
		claim:       claim,
	}
	planID := domain.PlanID("plan-" + string(claim.Run.ID))
	revisionID := domain.PlanRevisionID("revision-" + string(claim.Run.ID) + "-1")
	plan, solveErr := binding.Solver.Solve(
		solveCtx,
		problem,
		SolveRequest{
			RunID:      claim.Run.ID,
			PlanID:     planID,
			RevisionID: revisionID,
			Profile:    claim.Run.SolverProfile,
		},
		progress,
	)
	cancelSolve()
	renewErr := <-renewDone
	if renewErr != nil {
		return renewErr
	}
	if solveErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, getErr := worker.application.store.GetRun(
			ctx,
			claim.Run.TenantID,
			claim.Run.ID,
		)
		if getErr == nil && current.CancelRequestedAt != nil {
			return worker.finishFailure(ctx, claim, domain.RunCancelled, "cancelled")
		}
		switch {
		case errors.Is(solveErr, ErrSolverExhausted):
			return worker.finishFailure(ctx, claim, domain.RunExhausted, "solver_exhausted")
		case errors.Is(solveErr, ErrSolverAborted):
			return worker.finishFailure(ctx, claim, domain.RunAborted, "solver_aborted")
		default:
			return worker.finishFailure(ctx, claim, domain.RunFailed, "solver_failed")
		}
	}
	if err := worker.application.store.SaveRunCheckpoint(ctx, claim, SaveCheckpointTx{
		Status: domain.RunValidating,
		Now:    worker.application.clock().UTC(),
	}); err != nil {
		return err
	}
	plan.SchemaVersion = domain.PlanSchemaVersion
	plan.PlanID = planID
	plan.RevisionID = revisionID
	plan.ProblemDigest = problem.ProblemDigest
	plan.PolicyDigest = problem.PolicyDigest
	plan.CommitmentDigest = problem.CommitmentDigest
	plan.Solver = binding.Solver.Identity()
	plan.ConfigDigest = binding.ConfigDigest
	plan.PlanDigest = ""
	plan.PlanDigest, err = domain.ComputePlanDigest(plan)
	if err != nil {
		return worker.finishFailure(ctx, claim, domain.RunFailed, "plan_digest_failed")
	}
	now := worker.application.clock().UTC()
	report := worker.application.validator.Validate(problem, plan, now)
	if report.ReportDigest == "" {
		return worker.finishFailure(ctx, claim, domain.RunFailed, "validation_digest_missing")
	}
	planArtifact, err := artifact.New(artifact.KindPlan, domain.PlanSchemaVersion, plan)
	if err != nil {
		return err
	}
	planRef, err := worker.application.artifacts.Put(
		ctx,
		claim.Run.TenantID,
		planArtifact,
	)
	if err != nil {
		return err
	}
	if err := worker.application.artifacts.Verify(
		ctx,
		claim.Run.TenantID,
		planRef.Digest,
	); err != nil {
		return err
	}
	reportArtifact, err := artifact.New(
		artifact.KindValidationReport,
		domain.ValidationSchemaVersion,
		report,
	)
	if err != nil {
		return err
	}
	reportRef, err := worker.application.artifacts.Put(
		ctx,
		claim.Run.TenantID,
		reportArtifact,
	)
	if err != nil {
		return err
	}
	if err := worker.application.artifacts.Verify(
		ctx,
		claim.Run.TenantID,
		reportRef.Digest,
	); err != nil {
		return err
	}

	revisionStatus := domain.RevisionValidated
	runStatus := domain.RunSucceeded
	if !report.Valid {
		revisionStatus = domain.RevisionCandidate
		runStatus = domain.RunCandidateRejected
	}
	_, err = worker.application.store.PublishRevision(ctx, claim, PublishRevisionTx{
		Actor: Actor{Subject: "system:delivery-run-worker"},
		Plan: domain.DispatchPlan{
			TenantID:      claim.Run.TenantID,
			ID:            planID,
			ProblemID:     claim.Run.ProblemID,
			ActiveVersion: 0,
			CreatedAt:     now,
			UpdatedAt:     now,
		},
		Revision: domain.PlanRevision{
			TenantID:               claim.Run.TenantID,
			ID:                     revisionID,
			PlanID:                 planID,
			RunID:                  claim.Run.ID,
			ProblemDigest:          problem.ProblemDigest,
			PolicyDigest:           problem.PolicyDigest,
			CommitmentDigest:       problem.CommitmentDigest,
			PlanArtifactDigest:     planRef.Digest,
			PlanDigest:             plan.PlanDigest,
			ValidationArtifact:     reportRef.Digest,
			ValidationReportDigest: report.ReportDigest,
			Status:                 revisionStatus,
			Version:                1,
			CreatedAt:              now,
			UpdatedAt:              now,
		},
		RunStatus: runStatus,
		Now:       now,
	})
	return err
}

func (worker *RunWorker) renewLease(
	ctx context.Context,
	cancel context.CancelFunc,
	claim RunClaim,
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
			if err := worker.application.store.RenewRunLease(ctx, claim, deadline); err != nil {
				cancel()
				done <- err
				return
			}
			run, err := worker.application.store.GetRun(ctx, claim.Run.TenantID, claim.Run.ID)
			if err != nil {
				cancel()
				done <- err
				return
			}
			if run.CancelRequestedAt != nil {
				cancel()
			}
		}
	}
}

func (worker *RunWorker) loadProblem(
	ctx context.Context,
	version domain.ProblemVersion,
) (domain.ProblemSnapshot, error) {
	reader, metadata, err := worker.application.artifacts.Open(
		ctx,
		version.TenantID,
		version.ProblemArtifact,
	)
	if err != nil {
		return domain.ProblemSnapshot{}, err
	}
	defer reader.Close()
	if metadata.Kind != artifact.KindProblem ||
		metadata.SchemaVersion != domain.ProblemSchemaVersion {
		return domain.ProblemSnapshot{}, fmt.Errorf("problem artifact metadata mismatch")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, maxProblemArtifactBytes+1))
	if err != nil {
		return domain.ProblemSnapshot{}, err
	}
	if len(raw) > maxProblemArtifactBytes {
		return domain.ProblemSnapshot{}, artifact.ErrTooLarge
	}
	problem, err := decodeProblemArtifact(raw)
	if err != nil {
		return domain.ProblemSnapshot{}, err
	}
	if problem.ProblemDigest != version.ProblemDigest ||
		problem.PolicyDigest != version.PolicyDigest ||
		problem.CommitmentDigest != version.CommitmentDigest {
		return domain.ProblemSnapshot{}, fmt.Errorf("problem version binding mismatch")
	}
	return problem, nil
}

func (worker *RunWorker) finishFailure(
	ctx context.Context,
	claim RunClaim,
	status domain.RunStatus,
	code string,
) error {
	return worker.application.store.FinishRun(ctx, claim, FinishRunTx{
		Actor:       Actor{Subject: "system:delivery-run-worker"},
		Status:      status,
		FailureCode: code,
		Now:         worker.application.clock().UTC(),
	})
}

type runProgressSink struct {
	application *Application
	claim       RunClaim
}

func (sink *runProgressSink) Checkpoint(ctx context.Context, value any) error {
	checkpoint, err := artifact.New(
		artifact.KindEvidence,
		"delivery.solver-checkpoint.v1",
		value,
	)
	if err != nil {
		return err
	}
	ref, err := sink.application.artifacts.Put(ctx, sink.claim.Run.TenantID, checkpoint)
	if err != nil {
		return err
	}
	if err := sink.application.artifacts.Verify(
		ctx,
		sink.claim.Run.TenantID,
		ref.Digest,
	); err != nil {
		return err
	}
	return sink.application.store.SaveRunCheckpoint(ctx, sink.claim, SaveCheckpointTx{
		Status:           domain.RunSolving,
		CheckpointDigest: ref.Digest,
		Now:              sink.application.clock().UTC(),
	})
}
