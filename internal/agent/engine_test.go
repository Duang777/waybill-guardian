package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
)

func TestScenarioAgentPausesThenExecutesApprovedWrites(t *testing.T) {
	dataDir := t.TempDir()
	journal, err := audit.Open(dataDir+"/audit", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := approval.NewStore(journal, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	idempotencyStore, err := idempotency.NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	clients, mock, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := guardtools.NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := guardtools.NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	middlewares := []agents.Middleware{
		NewAuditMiddleware(journal),
		NewApprovalGuard(approvals),
		NewIdempotencyMiddleware(idempotencyStore),
	}
	engine, err := NewEngine(dataDir+"/history", registry, middlewares, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	runContext := domain.RunContext{
		RunID:       "run-agent",
		IncidentID:  "incident-agent",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	}
	outcome, err := engine.Start(context.Background(), runContext)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != agentstate.RunStatusPaused {
		t.Fatalf("status = %q, want paused; text=%q", outcome.Status, outcome.Text)
	}
	if len(outcome.Interrupts) != 2 {
		t.Fatalf("interrupts = %d, want 2", len(outcome.Interrupts))
	}
	if mock.WriteCount(domain.ActionReassign) != 0 || mock.WriteCount(domain.ActionSendSMS) != 0 {
		t.Fatal("write tools ran before approval")
	}

	items := make([]approval.Item, 0, len(outcome.Interrupts))
	callIDs := make([]string, 0, len(outcome.Interrupts))
	for _, interrupt := range outcome.Interrupts {
		hash, err := idempotency.ArgumentsHash(string(interrupt.Arguments))
		if err != nil {
			t.Fatal(err)
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(interrupt.Arguments, &values); err != nil {
			t.Fatal(err)
		}
		var key domain.IdempotencyKey
		if err := json.Unmarshal(values["idempotency_key"], &key); err != nil {
			t.Fatal(err)
		}
		callIDs = append(callIDs, interrupt.CallID)
		items = append(items, approval.Item{
			CallID:         interrupt.CallID,
			Action:         interrupt.Action,
			WireName:       interrupt.WireName,
			Params:         interrupt.Arguments,
			ArgumentsHash:  hash,
			IdempotencyKey: key,
		})
	}
	approvalID := approval.IDFor(runContext.RunID, callIDs)
	if _, err := approvals.Create(context.Background(), approval.Approval{
		ID:        approvalID,
		RunID:     runContext.RunID,
		SDKRunID:  outcome.SDKRunID,
		WaybillID: runContext.WaybillID,
		Items:     items,
		Reason:    "fatigue and extended stop",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Decide(context.Background(), approvalID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := engine.Resume(context.Background(), runContext, outcome.SDKRunID, outcome.Interrupts, true)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != agentstate.RunStatusCompleted {
		t.Fatalf("resumed status = %q, text=%q", resumed.Status, resumed.Text)
	}
	if mock.WriteCount(domain.ActionReassign) != 1 || mock.WriteCount(domain.ActionSendSMS) != 1 {
		t.Fatalf("writes = reassign:%d sms:%d", mock.WriteCount(domain.ActionReassign), mock.WriteCount(domain.ActionSendSMS))
	}
	if len(outcome.Chunks) == 0 || len(resumed.Chunks) == 0 {
		t.Fatal("expected streaming lifecycle chunks")
	}
}
