package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentkit "github.com/Duang777/waybill-guardian/internal/agent"
	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

func TestDemoConfirmCompletesExactlyOnce(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)
	var smsEffects []domain.EffectID
	var smsKeys []domain.IdempotencyKey
	for _, item := range pending.Items {
		if item.Action == domain.ActionSendSMS {
			smsEffects = append(smsEffects, item.EffectID)
			smsKeys = append(smsKeys, item.IdempotencyKey)
		}
	}
	if len(smsEffects) != 2 {
		t.Fatalf("sms effects = %d, want 2", len(smsEffects))
	}
	if smsEffects[0] == smsEffects[1] || smsKeys[0] == smsKeys[1] {
		t.Fatalf("same-action effects collided: effects=%v keys=%v", smsEffects, smsKeys)
	}
	decided, err := service.Decide(context.Background(), pending.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "demo-reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decided.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q", decided.Status)
	}
	completed, err := service.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.RunCompleted {
		t.Fatalf("run status = %q", completed.Status)
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 || runtime.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf("writes = reassign:%d sms:%d", runtime.WriteCount(domain.ActionReassign), runtime.WriteCount(domain.ActionSendSMS))
	}

	repeated, err := service.Decide(context.Background(), pending.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "demo-reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Status != approval.StatusExecuted {
		t.Fatalf("repeated status = %q", repeated.Status)
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 || runtime.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatal("repeated confirmation executed writes again")
	}
}

func TestRunStartedAuditRecordsPlatformSources(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{
		DataDir:         t.TempDir(),
		Reads:           clients,
		WriteRuntime:    runtime,
		PlatformProfile: "tms-reassign-sandbox-v1",
		ReadSource:      "fixture-v1",
		StepDelay:       0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.Replay(context.Background(), run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Type != audit.EventRunStarted {
		t.Fatalf("first audit event = %+v", events)
	}
	var payload runStartedPayload
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Profile != "tms-reassign-sandbox-v1" ||
		payload.ReadSource != "fixture-v1" {
		t.Fatalf("run platform sources = %+v", payload)
	}
}

func TestDiscoveryAndSnapshotUseDurableEventPrefix(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)

	runs, err := service.ListActiveRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].RunID != run.RunID {
		t.Fatalf("active runs = %+v", runs)
	}
	approvals, err := service.ListPendingApprovals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 1 || approvals[0].ID != pending.ID {
		t.Fatalf("pending approvals = %+v", approvals)
	}
	snapshot, err := service.Snapshot(context.Background(), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) == 0 {
		t.Fatal("snapshot has no events")
	}
	last := snapshot.Events[len(snapshot.Events)-1].Seq
	if snapshot.Run.LastSeq != last {
		t.Fatalf("snapshot last seq = %d, event tail = %d", snapshot.Run.LastSeq, last)
	}
	if snapshot.Run.Status != domain.RunAwaitingApproval {
		t.Fatalf("snapshot status = %q, want awaiting_approval", snapshot.Run.Status)
	}
	if _, err := service.Decide(context.Background(), pending.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "discovery-test",
	}); err != nil {
		t.Fatal(err)
	}
	runs, err = service.ListActiveRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("completed run remained active: %+v", runs)
	}
	approvals, err = service.ListPendingApprovals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(approvals) != 0 {
		t.Fatalf("executed approval remained pending: %+v", approvals)
	}
}

func TestPartialWriteFailureDoesNotCompleteApprovalOrRun(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	failingRuntime := &failingActionRuntime{
		WriteRuntime: runtime,
		action:       domain.ActionSendSMS,
	}
	service, err := Open(Config{
		DataDir:      t.TempDir(),
		Reads:        clients,
		WriteRuntime: failingRuntime,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)
	decided, err := service.Decide(context.Background(), pending.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "demo-reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decided.Status != approval.Status("partially_failed") {
		t.Fatalf("approval status = %q, want partially_failed", decided.Status)
	}
	failed, err := service.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", failed.Status)
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 {
		t.Fatalf("reassign writes = %d, want 1", runtime.WriteCount(domain.ActionReassign))
	}
	if runtime.WriteCount(domain.ActionSendSMS) != 0 {
		t.Fatalf("sms writes = %d, want 0", runtime.WriteCount(domain.ActionSendSMS))
	}
	events, err := service.Replay(context.Background(), run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var executionFailed bool
	itemStatuses := make(map[domain.Action]approval.ExecutionStatus)
	for _, event := range events {
		if event.Type == audit.EventRunCompleted {
			t.Fatal("partial write failure emitted run_completed")
		}
		if event.Type == audit.EventApprovalExecutionFailed {
			executionFailed = true
			var payload struct {
				Status approval.Status          `json:"status"`
				Items  []approval.ItemExecution `json:"items"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Status != approval.StatusPartiallyFailed {
				t.Fatalf("execution failure status = %q", payload.Status)
			}
			for _, item := range payload.Items {
				itemStatuses[item.Action] = item.Status
			}
		}
	}
	if !executionFailed {
		t.Fatal("partial write failure did not emit approval_execution_failed")
	}
	if itemStatuses[domain.ActionReassign] != approval.ExecutionSucceeded {
		t.Fatalf("reassign status = %q", itemStatuses[domain.ActionReassign])
	}
	if itemStatuses[domain.ActionSendSMS] != approval.ExecutionPermanent {
		t.Fatalf("sms status = %q", itemStatuses[domain.ActionSendSMS])
	}
}

func TestConcurrentConfirmResumesRunOnce(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)

	start := make(chan struct{})
	results := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			decided, err := service.Decide(context.Background(), pending.ID, DecisionRequest{
				Kind:      approval.DecisionConfirm,
				DecidedBy: "demo-reviewer",
			})
			if err == nil && decided.Status != approval.StatusExecuted {
				err = errors.New("approval was not executed")
			}
			results <- err
		}()
	}
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 || runtime.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf("writes = reassign:%d sms:%d", runtime.WriteCount(domain.ActionReassign), runtime.WriteCount(domain.ActionSendSMS))
	}
}

func TestRejectedReassignProducesAlternativePlan(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	first := waitForApproval(t, service, run.RunID)
	if first.PlanVersion != 1 {
		t.Fatalf("first plan version = %d", first.PlanVersion)
	}
	if _, err := service.Decide(context.Background(), first.ID, DecisionRequest{
		Kind:         approval.DecisionReject,
		DecidedBy:    "demo-reviewer",
		RejectReason: "优先承运商无可用车辆",
	}); err != nil {
		t.Fatal(err)
	}
	second := waitForDifferentApproval(t, service, run.RunID, first.ID)
	if second.PlanVersion != 2 {
		t.Fatalf("second plan version = %d", second.PlanVersion)
	}
	var params map[string]any
	if err := json.Unmarshal(second.Items[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if params["carrier_id"] != "CARRIER-SW-19" {
		t.Fatalf("alternative carrier = %v", params["carrier_id"])
	}
	if runtime.WriteCount(domain.ActionReassign) != 0 {
		t.Fatal("rejected plan executed a write")
	}
	if _, err := service.Decide(context.Background(), second.ID, DecisionRequest{
		Kind:         approval.DecisionReject,
		DecidedBy:    "demo-reviewer",
		RejectReason: "全部候选均不可用",
	}); err != nil {
		t.Fatal(err)
	}
	finished, err := service.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != domain.RunRejected {
		t.Fatalf("run status = %q", finished.Status)
	}
}

func TestDecisionConflict(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Reads: clients, WriteRuntime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)
	if _, err := service.Decide(context.Background(), pending.ID, DecisionRequest{
		Kind:         approval.DecisionReject,
		DecidedBy:    "demo-reviewer",
		RejectReason: "no",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Decide(context.Background(), pending.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "demo-reviewer",
	}); !errors.Is(err, approval.ErrDecisionConflict) {
		t.Fatalf("conflicting decision error = %v", err)
	}
}

func TestApprovalExpiresAndResumesWithoutManualRecovery(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{
		DataDir:      t.TempDir(),
		Reads:        clients,
		WriteRuntime: runtime,
		ApprovalTTL:  40 * time.Millisecond,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, service, run.RunID, domain.RunRejected)

	values := service.approvals.List()
	if len(values) != 2 {
		t.Fatalf("approvals = %d, want 2", len(values))
	}
	for _, value := range values {
		if value.Status != approval.StatusExpired {
			t.Fatalf("approval %s status = %q", value.ID, value.Status)
		}
	}
	if runtime.WriteCount(domain.ActionReassign) != 0 || runtime.WriteCount(domain.ActionSendSMS) != 0 {
		t.Fatal("expired approvals executed writes")
	}
}

func TestLateConfirmExpiresAndResumesBeforeTimer(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	var clockNanos atomic.Int64
	clockNanos.Store(time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC).UnixNano())
	clock := func() time.Time {
		return time.Unix(0, clockNanos.Load()).UTC()
	}
	service, err := Open(Config{
		DataDir:      t.TempDir(),
		Reads:        clients,
		WriteRuntime: runtime,
		Clock:        clock,
		ApprovalTTL:  time.Hour,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	first := waitForApproval(t, service, run.RunID)
	clockNanos.Store(first.ExpiresAt.UnixNano())

	if _, err := service.Decide(context.Background(), first.ID, DecisionRequest{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "late-reviewer",
	}); !errors.Is(err, approval.ErrDecisionConflict) {
		t.Fatalf("late confirm error = %v, want ErrDecisionConflict", err)
	}
	expired, err := service.GetApproval(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != approval.StatusExpired {
		t.Fatalf("expired approval status = %q", expired.Status)
	}
	second := waitForDifferentApproval(t, service, run.RunID, first.ID)
	if second.PlanVersion != 2 {
		t.Fatalf("replacement plan version = %d, want 2", second.PlanVersion)
	}
	if runtime.WriteCount(domain.ActionReassign) != 0 || runtime.WriteCount(domain.ActionSendSMS) != 0 {
		t.Fatal("late confirmation executed writes")
	}
}

func TestRecoverReplaysConfirmedApproval(t *testing.T) {
	dataDir := t.TempDir()
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, first, run.RunID)
	if _, err := first.approvals.Decide(context.Background(), pending.ID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "recovery-test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}

	recovered, err := reopened.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q", recovered.Status)
	}
	waitForRunStatus(t, reopened, run.RunID, domain.RunCompleted)
	if runtime.WriteCount(domain.ActionReassign) != 1 || runtime.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf("writes = reassign:%d sms:%d", runtime.WriteCount(domain.ActionReassign), runtime.WriteCount(domain.ActionSendSMS))
	}
}

func TestRecoverUsesPreparedProposalWithoutCallingModelAgain(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	source, err := Open(Config{
		DataDir:      t.TempDir(),
		Reads:        clients,
		WriteRuntime: runtime,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := source.StartRun(t.Context(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	expected := waitForApproval(t, source, run.RunID)
	events, err := source.Replay(t.Context(), run.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name              string
		includeCheckpoint bool
	}{
		{name: "after checkpoint", includeCheckpoint: true},
		{name: "before checkpoint", includeCheckpoint: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			now := replayEventPrefix(
				t,
				dataDir,
				events,
				audit.EventProposalPrepared,
				test.includeCheckpoint,
			)
			reopened, err := Open(Config{
				DataDir:      dataDir,
				Reads:        clients,
				WriteRuntime: runtime,
				Clock:        func() time.Time { return now },
				StepDelay:    0,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			before, err := reopened.Replay(t.Context(), run.RunID, 0)
			if err != nil {
				t.Fatal(err)
			}
			modelCallsBefore := countEvents(before, audit.EventModelCallStarted)
			if err := reopened.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, err := reopened.Replay(t.Context(), run.RunID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := countEvents(after, audit.EventModelCallStarted); got != modelCallsBefore {
				t.Fatalf("model call count = %d, want %d", got, modelCallsBefore)
			}

			if !test.includeCheckpoint {
				recovered, err := reopened.GetRun(run.RunID)
				if err != nil {
					t.Fatal(err)
				}
				if recovered.Status != domain.RunReviewRequired {
					t.Fatalf("run status = %q, want review_required", recovered.Status)
				}
				if _, err := reopened.CurrentApproval(run.RunID); !errors.Is(err, approval.ErrNotFound) {
					t.Fatalf("CurrentApproval error = %v, want ErrNotFound", err)
				}
				return
			}

			recovered, err := reopened.CurrentApproval(run.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.ID != expected.ID ||
				recovered.SDKRunID != expected.SDKRunID ||
				recovered.ProposalRef == nil ||
				recovered.ProposalRef.Digest != expected.ProposalRef.Digest ||
				len(recovered.Items) != len(expected.Items) {
				t.Fatalf("recovered approval = %+v, want checkpoint identity %+v", recovered, expected)
			}
		})
	}
}

func TestRecoverRejectsChangedReadSource(t *testing.T) {
	dataDir := t.TempDir()
	reads, _, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime, err := tools.NewFixtureWriteRuntimeForSource(reads, "dataset-a")
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{
		DataDir:      dataDir,
		Reads:        reads,
		WriteRuntime: firstRuntime,
		ReadSource:   "dataset-a",
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartRun(t.Context(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, first, run.RunID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	secondRuntime, err := tools.NewFixtureWriteRuntimeForSource(reads, "dataset-b")
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(Config{
		DataDir:      dataDir,
		Reads:        reads,
		WriteRuntime: secondRuntime,
		ReadSource:   "dataset-b",
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(t.Context()); !errors.Is(
		err,
		ErrRecoveryReadSourceMismatch,
	) {
		t.Fatalf("Recover error = %v, want ErrRecoveryReadSourceMismatch", err)
	}
	recovered, err := reopened.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != approval.StatusPending {
		t.Fatalf("approval status = %q, want pending", recovered.Status)
	}
	if secondRuntime.WriteCount(domain.ActionReassign) != 0 ||
		secondRuntime.WriteCount(domain.ActionSendSMS) != 0 {
		t.Fatal("source mismatch executed writes")
	}
}

func TestEffectReconcilerResumesDuePreparedApproval(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, service, run.RunID)
	confirmed, err := service.approvals.Decide(context.Background(), pending.ID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "reconciler-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := confirmed.Items[0].Identity()
	if err != nil {
		t.Fatal(err)
	}
	service.effects = &scheduledRecoveryExecutor{
		Executor: service.effects,
		command: idempotency.Command{
			RunID:    confirmed.RunID,
			CallID:   confirmed.Items[0].CallID,
			Identity: identity,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.RunEffectReconciler(ctx, 5*time.Millisecond)
	}()
	waitForRunStatus(t, service, run.RunID, domain.RunCompleted)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("effect reconciler did not stop")
	}

	recovered, err := service.GetApproval(confirmed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q, want executed", recovered.Status)
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 ||
		runtime.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf(
			"writes = reassign:%d sms:%d",
			runtime.WriteCount(domain.ActionReassign),
			runtime.WriteCount(domain.ActionSendSMS),
		)
	}
}

func TestEffectReconcilerKeepsBusyAndPendingEffectsPaused(t *testing.T) {
	tests := []struct {
		name    string
		outcome idempotency.RecoveryOutcome
	}{
		{
			name: "busy",
			outcome: idempotency.RecoveryOutcome{
				Decision: idempotency.RecoveryBusy,
				State:    idempotency.StateStarted,
			},
		},
		{
			name: "pending",
			outcome: idempotency.RecoveryOutcome{
				Decision: idempotency.RecoveryPending,
				State:    idempotency.StateUnknown,
				RetryAt:  time.Now().UTC().Add(time.Minute),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clients, runtime, err := tools.NewDemoRuntime()
			if err != nil {
				t.Fatal(err)
			}
			service, err := Open(Config{
				DataDir:      t.TempDir(),
				Reads:        clients,
				WriteRuntime: runtime,
				StepDelay:    0,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()

			run, err := service.StartRun(context.Background(), "YD2026101001")
			if err != nil {
				t.Fatal(err)
			}
			pending := waitForApproval(t, service, run.RunID)
			confirmed, err := service.approvals.Decide(
				context.Background(),
				pending.ID,
				approval.Decision{
					Kind:      approval.DecisionConfirm,
					DecidedBy: "reconciler-test",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := confirmed.Items[0].Identity()
			if err != nil {
				t.Fatal(err)
			}
			service.effects = &fixedRecoveryExecutor{
				Executor: service.effects,
				command: idempotency.Command{
					RunID:    confirmed.RunID,
					CallID:   confirmed.Items[0].CallID,
					Identity: identity,
				},
				outcome: test.outcome,
			}

			if err := service.reconcileDueEffects(context.Background()); err != nil {
				t.Fatal(err)
			}
			recovered, err := service.GetApproval(confirmed.ID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != approval.StatusReconciliationRequired {
				t.Fatalf(
					"approval status = %q, want reconciliation_required",
					recovered.Status,
				)
			}
			if runtime.WriteCount(domain.ActionReassign) != 0 ||
				runtime.WriteCount(domain.ActionSendSMS) != 0 {
				t.Fatal("busy or pending recovery resumed mutations")
			}
		})
	}
}

func TestRecoverReschedulesPendingApprovalExpiry(t *testing.T) {
	dataDir := t.TempDir()
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{
		DataDir:      dataDir,
		Reads:        clients,
		WriteRuntime: runtime,
		ApprovalTTL:  500 * time.Millisecond,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, first, run.RunID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{
		DataDir:      dataDir,
		Reads:        clients,
		WriteRuntime: runtime,
		ApprovalTTL:  500 * time.Millisecond,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitForRunStatus(t, reopened, run.RunID, domain.RunRejected)
	recovered, err := reopened.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != approval.StatusExpired {
		t.Fatalf("approval status = %q", recovered.Status)
	}
}

func TestRecoverCompletesApprovalWithoutRepeatingSuccessfulEffects(t *testing.T) {
	dataDir := t.TempDir()
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, first, run.RunID)
	if _, err := first.approvals.Decide(context.Background(), pending.ID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "recovery-test",
	}); err != nil {
		t.Fatal(err)
	}
	outcome, err := first.engine.Resume(context.Background(), domain.RunContext{
		RunID:       run.RunID,
		IncidentID:  run.IncidentID,
		WaybillID:   run.WaybillID,
		PlanVersion: pending.PlanVersion,
	}, pending.SDKRunID, interruptsFromApproval(pending), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.handleOutcome(context.Background(), run, pending.PlanVersion, outcome, false); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q", recovered.Status)
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 || runtime.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatal("recovery repeated already successful effects")
	}
}

func TestRecoverReconcilesStartedEffectWithoutStoppingService(t *testing.T) {
	dataDir := t.TempDir()
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, first, run.RunID)
	confirmed, err := first.approvals.Decide(context.Background(), pending.ID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "recovery-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var reassign approval.Item
	for _, item := range confirmed.Items {
		if item.Action == domain.ActionReassign {
			reassign = item
			break
		}
	}
	if reassign.CallID == "" {
		t.Fatal("approval has no reassign effect")
	}
	var arguments struct {
		WaybillID string `json:"waybill_id"`
		CarrierID string `json:"carrier_id"`
	}
	if err := json.Unmarshal(reassign.Params, &arguments); err != nil {
		t.Fatal(err)
	}
	binding := fixtureBindingForItem(t, runtime, reassign)
	result := runtime.Dispatch(
		context.Background(),
		binding,
		platform.EffectRequest{
			Action:        reassign.Action,
			Arguments:     reassign.Params,
			ArgumentsHash: reassign.ArgumentsHash,
		},
		reassign.IdempotencyKey,
	)
	if result.Disposition != platform.EffectSucceeded {
		t.Fatalf("preexisting reassign dispatch = %+v", result)
	}
	dispatchStartedAt := time.Now().UTC()
	if _, err := first.journal.Append(context.Background(), run.RunID, audit.Draft{
		EventID: "write:" + string(reassign.IdempotencyKey) + ":started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteStarted,
		Payload: map[string]any{
			"idempotency_key":     reassign.IdempotencyKey,
			"effect_id":           reassign.EffectID,
			"identity_version":    reassign.IdentityVersion,
			"call_id":             reassign.CallID,
			"action":              reassign.Action,
			"arguments_hash":      reassign.ArgumentsHash,
			"attempt":             1,
			"binding":             binding,
			"dispatch_started_at": dispatchStartedAt,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatalf("Recover stopped service for one unresolved effect: %v", err)
	}
	recovered, err := reopened.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != approval.StatusExecuted {
		t.Fatalf("approval status = %q, want executed", recovered.Status)
	}
	if runtime.WriteCount(domain.ActionReassign) != 1 {
		t.Fatalf("reassign calls = %d, want 1", runtime.WriteCount(domain.ActionReassign))
	}
}

func TestRecoverLeavesUnknownStartedEffectPendingWithoutStoppingService(t *testing.T) {
	dataDir := t.TempDir()
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	unknownRuntime := &unknownLookupRuntime{WriteRuntime: runtime}
	first, err := Open(Config{
		DataDir:      dataDir,
		Reads:        clients,
		WriteRuntime: unknownRuntime,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartRun(context.Background(), "YD2026101001")
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, first, run.RunID)
	confirmed, err := first.approvals.Decide(context.Background(), pending.ID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "recovery-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	var item approval.Item
	for _, candidate := range confirmed.Items {
		if candidate.Action == domain.ActionReassign {
			item = candidate
			break
		}
	}
	if item.CallID == "" {
		t.Fatal("approval has no reassign effect")
	}
	binding := fixtureBindingForItem(t, unknownRuntime, item)
	dispatchStartedAt := time.Now().UTC()
	if _, err := first.journal.Append(context.Background(), run.RunID, audit.Draft{
		EventID: "write:" + string(item.IdempotencyKey) + ":started",
		Actor:   audit.ActorSystem,
		Type:    audit.EventWriteStarted,
		Payload: map[string]any{
			"idempotency_key":     item.IdempotencyKey,
			"effect_id":           item.EffectID,
			"identity_version":    item.IdentityVersion,
			"call_id":             item.CallID,
			"action":              item.Action,
			"arguments_hash":      item.ArgumentsHash,
			"attempt":             1,
			"binding":             binding,
			"dispatch_started_at": dispatchStartedAt,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{
		DataDir:      dataDir,
		Reads:        clients,
		WriteRuntime: unknownRuntime,
		StepDelay:    0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Recover(context.Background()); err != nil {
		t.Fatalf("Recover stopped service for a pending reconciliation: %v", err)
	}
	recovered, err := reopened.GetApproval(pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != approval.StatusReconciliationRequired {
		t.Fatalf("approval status = %q, want reconciliation_required", recovered.Status)
	}
	recoveredRun, err := reopened.GetRun(run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recoveredRun.Status == domain.RunFailed {
		t.Fatal("pending reconciliation marked the run failed")
	}
	if runtime.WriteCount(item.Action) != 0 {
		t.Fatalf("unknown effect was dispatched %d more times", runtime.WriteCount(item.Action))
	}
}

func TestOpenReleasesAuditLockAfterInitializationFailure(t *testing.T) {
	dataDir := t.TempDir()
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{
		DataDir:      dataDir,
		Reads:        clients,
		WriteRuntime: runtime,
		Model:        agentkit.ModelConfig{Mode: "invalid"},
	}); err == nil {
		t.Fatal("Open accepted invalid agent mode")
	}

	service, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime})
	if err != nil {
		t.Fatalf("Open after initialization failure: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsNegativeHistoryRetention(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(Config{
		DataDir:          t.TempDir(),
		Reads:            clients,
		WriteRuntime:     runtime,
		HistoryRetention: -time.Second,
	}); err == nil {
		t.Fatal("Open accepted negative history retention")
	}
}

func TestConcurrentCloseWaitsForResourceRelease(t *testing.T) {
	dataDir := t.TempDir()
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime})
	if err != nil {
		t.Fatal(err)
	}

	service.wg.Add(1)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- service.Close()
	}()
	deadline := time.Now().Add(time.Second)
	for {
		service.mu.Lock()
		closing := service.closed
		service.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			service.wg.Done()
			t.Fatal("first Close did not start")
		}
		time.Sleep(time.Millisecond)
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- service.Close()
	}()
	var secondErr error
	returnedEarly := false
	select {
	case secondErr = <-secondDone:
		returnedEarly = true
	case <-time.After(50 * time.Millisecond):
	}
	service.wg.Done()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if !returnedEarly {
		secondErr = <-secondDone
	}
	if secondErr != nil {
		t.Fatal(secondErr)
	}
	if returnedEarly {
		t.Fatal("concurrent Close returned before resource release")
	}

	reopened, err := Open(Config{DataDir: dataDir, Reads: clients, WriteRuntime: runtime})
	if err != nil {
		t.Fatalf("Open after concurrent Close: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRejectsNewOperations(t *testing.T) {
	clients, runtime, err := tools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Reads: clients, WriteRuntime: runtime})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := service.StartRun(context.Background(), "YD2026101001"); !errors.Is(err, ErrServiceClosed) {
		t.Fatalf("StartRun after Close error = %v, want ErrServiceClosed", err)
	}
	if err := service.Recover(context.Background()); !errors.Is(err, ErrServiceClosed) {
		t.Fatalf("Recover after Close error = %v, want ErrServiceClosed", err)
	}
}

func waitForApproval(t *testing.T, service *Service, runID domain.RunID) approval.Approval {
	t.Helper()
	return waitForDifferentApproval(t, service, runID, "")
}

func waitForDifferentApproval(
	t *testing.T,
	service *Service,
	runID domain.RunID,
	previous domain.ApprovalID,
) approval.Approval {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		value, err := service.CurrentApproval(runID)
		if err == nil && value.ID != previous {
			return value
		}
		if err != nil && !errors.Is(err, approval.ErrNotFound) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ := service.GetRun(runID)
	events, _ := service.Replay(context.Background(), runID, 0)
	encodedEvents, _ := json.Marshal(events)
	t.Fatalf("timed out waiting for approval; run=%+v events=%s", run, encodedEvents)
	return approval.Approval{}
}

func waitForRunStatus(t *testing.T, service *Service, runID domain.RunID, want domain.RunStatus) RunView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := service.GetRun(runID)
		if err == nil && run.Status == want {
			return run
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _ := service.GetRun(runID)
	events, _ := service.Replay(context.Background(), runID, 0)
	encodedEvents, _ := json.Marshal(events)
	t.Fatalf(
		"timed out waiting for run status %q; run=%+v events=%s",
		want,
		run,
		encodedEvents,
	)
	return RunView{}
}

func fixtureBindingForItem(
	t *testing.T,
	runtime platform.WriteRuntime,
	item approval.Item,
) platform.EffectBinding {
	t.Helper()
	binding, err := runtime.Bind(platform.EffectRequest{
		Action:        item.Action,
		Arguments:     item.Params,
		ArgumentsHash: item.ArgumentsHash,
	}, item.IdempotencyKey, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func replayEventPrefix(
	t *testing.T,
	dataDir string,
	events []audit.Event,
	stopType audit.EventType,
	includeStop bool,
) time.Time {
	t.Helper()
	now := events[0].TS
	store, err := audit.Open(dataDir, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range events {
		if source.Type == stopType && !includeStop {
			break
		}
		now = source.TS
		replayed, err := store.Append(t.Context(), source.RunID, audit.Draft{
			EventID: source.EventID,
			Actor:   source.Actor,
			Type:    source.Type,
			Payload: source.Payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		if replayed.Hash != source.Hash {
			t.Fatalf("replayed event %q hash = %q, want %q", source.EventID, replayed.Hash, source.Hash)
		}
		if source.Type == stopType {
			break
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return now
}

func countEvents(events []audit.Event, eventType audit.EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

type failingActionRuntime struct {
	platform.WriteRuntime
	action domain.Action
}

func (r *failingActionRuntime) Dispatch(
	ctx context.Context,
	binding platform.EffectBinding,
	request platform.EffectRequest,
	key domain.IdempotencyKey,
) platform.DispatchResult {
	if request.Action == r.action {
		return platform.DispatchResult{
			Disposition: platform.EffectPermanentFailed,
			ErrorCode:   "injected_failure",
		}
	}
	return r.WriteRuntime.Dispatch(ctx, binding, request, key)
}

type unknownLookupRuntime struct {
	platform.WriteRuntime
}

func (r *unknownLookupRuntime) Lookup(
	context.Context,
	platform.EffectBinding,
	domain.IdempotencyKey,
) platform.LookupResult {
	return platform.LookupResult{Disposition: platform.LookupPending}
}

type scheduledRecoveryExecutor struct {
	idempotency.Executor
	command idempotency.Command
}

func (e *scheduledRecoveryExecutor) DueRecoveries(
	context.Context,
) ([]idempotency.Command, error) {
	state, ok := e.Status(e.command)
	if ok {
		switch state {
		case idempotency.StateSucceeded,
			idempotency.StatePermanentFailed,
			idempotency.StateManualReview:
			return nil, nil
		}
	}
	return []idempotency.Command{e.command}, nil
}

type fixedRecoveryExecutor struct {
	idempotency.Executor
	command idempotency.Command
	outcome idempotency.RecoveryOutcome
}

func (e *fixedRecoveryExecutor) DueRecoveries(
	context.Context,
) ([]idempotency.Command, error) {
	return []idempotency.Command{e.command}, nil
}

func (e *fixedRecoveryExecutor) Recover(
	context.Context,
	idempotency.Command,
) (idempotency.RecoveryOutcome, error) {
	return e.outcome, nil
}

func (e *fixedRecoveryExecutor) Status(
	idempotency.Command,
) (idempotency.State, bool) {
	return e.outcome.State, true
}
