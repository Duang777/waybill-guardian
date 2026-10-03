package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/tools"
)

func TestDemoConfirmCompletesExactlyOnce(t *testing.T) {
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Clients: clients, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartDemo(context.Background())
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
	if mock.WriteCount(domain.ActionReassign) != 1 || mock.WriteCount(domain.ActionSendSMS) != 1 {
		t.Fatalf("writes = reassign:%d sms:%d", mock.WriteCount(domain.ActionReassign), mock.WriteCount(domain.ActionSendSMS))
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
	if mock.WriteCount(domain.ActionReassign) != 1 || mock.WriteCount(domain.ActionSendSMS) != 1 {
		t.Fatal("repeated confirmation executed writes again")
	}
}

func TestConcurrentConfirmResumesRunOnce(t *testing.T) {
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Clients: clients, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartDemo(context.Background())
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
	if mock.WriteCount(domain.ActionReassign) != 1 || mock.WriteCount(domain.ActionSendSMS) != 1 {
		t.Fatalf("writes = reassign:%d sms:%d", mock.WriteCount(domain.ActionReassign), mock.WriteCount(domain.ActionSendSMS))
	}
}

func TestRejectedReassignProducesAlternativePlan(t *testing.T) {
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Clients: clients, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartDemo(context.Background())
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
	if mock.WriteCount(domain.ActionReassign) != 0 {
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
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{DataDir: t.TempDir(), Clients: clients})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	run, err := service.StartDemo(context.Background())
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
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	service, err := Open(Config{
		DataDir:     t.TempDir(),
		Clients:     clients,
		ApprovalTTL: 40 * time.Millisecond,
		StepDelay:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	run, err := service.StartDemo(context.Background())
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
	if mock.WriteCount(domain.ActionReassign) != 0 || mock.WriteCount(domain.ActionSendSMS) != 0 {
		t.Fatal("expired approvals executed writes")
	}
}

func TestRecoverReplaysConfirmedApproval(t *testing.T) {
	dataDir := t.TempDir()
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{DataDir: dataDir, Clients: clients, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartDemo(context.Background())
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

	reopened, err := Open(Config{DataDir: dataDir, Clients: clients, StepDelay: 0})
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
	if mock.WriteCount(domain.ActionReassign) != 1 || mock.WriteCount(domain.ActionSendSMS) != 1 {
		t.Fatalf("writes = reassign:%d sms:%d", mock.WriteCount(domain.ActionReassign), mock.WriteCount(domain.ActionSendSMS))
	}
}

func TestRecoverReschedulesPendingApprovalExpiry(t *testing.T) {
	dataDir := t.TempDir()
	clients, _, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{
		DataDir:     dataDir,
		Clients:     clients,
		ApprovalTTL: 500 * time.Millisecond,
		StepDelay:   0,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartDemo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pending := waitForApproval(t, first, run.RunID)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(Config{
		DataDir:     dataDir,
		Clients:     clients,
		ApprovalTTL: 500 * time.Millisecond,
		StepDelay:   0,
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
	clients, mock, err := tools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	first, err := Open(Config{DataDir: dataDir, Clients: clients, StepDelay: 0})
	if err != nil {
		t.Fatal(err)
	}
	run, err := first.StartDemo(context.Background())
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

	reopened, err := Open(Config{DataDir: dataDir, Clients: clients, StepDelay: 0})
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
	if mock.WriteCount(domain.ActionReassign) != 1 || mock.WriteCount(domain.ActionSendSMS) != 1 {
		t.Fatal("recovery repeated already successful effects")
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
	t.Fatalf("timed out waiting for approval; run=%+v", run)
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
	t.Fatalf("timed out waiting for run status %q; run=%+v", want, run)
	return RunView{}
}
