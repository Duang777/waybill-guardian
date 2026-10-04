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
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestWriteEffectMiddlewareSeparatesSameActionEffects(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	approvals, err := approval.NewStore(journal, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	clients, mock, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	writeRuntime, err := guardtools.NewFixtureWriteRuntime(clients)
	if err != nil {
		t.Fatal(err)
	}
	effects, err := idempotency.NewStore(journal, idempotency.StoreConfig{
		Runtime: writeRuntime,
	})
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

	runContext := domain.RunContext{
		RunID:       "run-effects",
		IncidentID:  "incident-effects",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	}
	shipper := testSMSCall("call-shipper", guardtools.RecipientShipper, runContext)
	driver := testSMSCall("call-driver", guardtools.RecipientDriver, runContext)
	items := []approval.Item{
		materializeTestItem(t, registry, runContext, shipper),
		materializeTestItem(t, registry, runContext, driver),
	}
	approvalID := approval.IDFor(runContext.RunID, []string{shipper.CallID, driver.CallID})
	if _, err := approvals.Create(context.Background(), approval.Approval{
		ID:          approvalID,
		RunID:       runContext.RunID,
		SDKRunID:    "sdk-run-effects",
		WaybillID:   runContext.WaybillID,
		PlanVersion: runContext.PlanVersion,
		Items:       items,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Decide(context.Background(), approvalID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}

	middleware := NewWriteEffectMiddleware(approvals, effects, registry)
	tool := &agents.BaseTool{Meta: map[string]any{
		guardtools.MetaAccess:       guardtools.AccessWrite,
		guardtools.MetaContractName: string(domain.ActionSendSMS),
	}}
	nextCalls := 0
	execute := middleware.WrapToolCall(func(
		_ context.Context,
		_ *agents.BaseTool,
		call *agents.ToolCall,
	) (*agents.ToolCallResponse, error) {
		nextCalls++
		return agents.ToolCallResult(call, `{"unexpected":true}`), nil
	})

	first, err := execute(context.Background(), tool, shipper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(context.Background(), tool, driver); err != nil {
		t.Fatal(err)
	}
	replayed, err := execute(context.Background(), tool, shipper)
	if err != nil {
		t.Fatal(err)
	}
	if nextCalls != 0 {
		t.Fatalf("write handler calls = %d, want 0", nextCalls)
	}
	if mock.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf("SMS writes = %d, want 2", mock.WriteCount(domain.ActionSendSMS))
	}
	if *replayed.Output.OfString != *first.Output.OfString {
		t.Fatalf("replayed output = %q, want %q", *replayed.Output.OfString, *first.Output.OfString)
	}

	events, err := journal.Replay(context.Background(), runContext.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	effectIDs := make(map[domain.EffectID]struct{})
	keys := make(map[domain.IdempotencyKey]struct{})
	started := 0
	for _, event := range events {
		if event.Type != audit.EventWriteStarted {
			continue
		}
		started++
		var payload struct {
			EffectID domain.EffectID       `json:"effect_id"`
			Key      domain.IdempotencyKey `json:"idempotency_key"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		effectIDs[payload.EffectID] = struct{}{}
		keys[payload.Key] = struct{}{}
	}
	if started != 2 || len(effectIDs) != 2 || len(keys) != 2 {
		t.Fatalf("started = %d, effect IDs = %d, keys = %d", started, len(effectIDs), len(keys))
	}
}

func materializeTestItem(
	t *testing.T,
	registry *guardtools.Registry,
	runContext domain.RunContext,
	call *agents.ToolCall,
) approval.Item {
	t.Helper()
	write, err := registry.ParseWrite(call.Name, json.RawMessage(call.Arguments))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := idempotency.Derive(idempotency.DerivationInput{
		RunContext: runContext,
		Action:     write.Action,
		Target:     write.Target,
		Arguments:  write.Arguments,
	})
	if err != nil {
		t.Fatal(err)
	}
	return approval.Item{
		CallID:          call.CallID,
		Action:          write.Action,
		WireName:        write.WireName,
		Params:          write.Arguments,
		ArgumentsHash:   identity.ArgumentsHash,
		IdentityVersion: identity.Version,
		EffectID:        identity.EffectID,
		IdempotencyKey:  identity.Key,
	}
}

func testSMSCall(
	callID string,
	recipient guardtools.NotificationRecipient,
	runContext domain.RunContext,
) *agents.ToolCall {
	arguments, _ := json.Marshal(guardtools.SendSMSInput{
		WaybillID: "YD2026101001",
		Recipient: recipient,
		CarrierID: "CARRIER-SW-42",
	})
	return &agents.ToolCall{
		FunctionCallMessage: &responses.FunctionCallMessage{
			ID:        "fc-" + callID,
			CallID:    callID,
			Name:      "notify_send_sms",
			Arguments: string(arguments),
		},
		RunContext: contextMap(runContext),
	}
}
