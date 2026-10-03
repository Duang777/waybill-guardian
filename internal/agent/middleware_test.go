package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestIdempotencyMiddlewareSeparatesSameActionEffects(t *testing.T) {
	journal, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, err := idempotency.NewStore(journal)
	if err != nil {
		t.Fatal(err)
	}
	middleware := NewIdempotencyMiddleware(store)
	tool := &agents.BaseTool{Meta: map[string]any{
		guardtools.MetaAccess:       guardtools.AccessWrite,
		guardtools.MetaContractName: string(domain.ActionSendSMS),
	}}
	calls := make(map[string]int)
	execute := middleware.WrapToolCall(func(
		_ context.Context,
		_ *agents.BaseTool,
		call *agents.ToolCall,
	) (*agents.ToolCallResponse, error) {
		var arguments struct {
			Phone string `json:"phone"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
			return nil, err
		}
		calls[arguments.Phone]++
		return agents.ToolCallResult(call, fmt.Sprintf(`{"phone":%q}`, arguments.Phone)), nil
	})

	runContext := map[string]any{
		"run_id":       "run-effects",
		"incident_id":  "incident-effects",
		"waybill_id":   "YD2026101001",
		"plan_version": 1,
	}
	shipper := testSMSCall("call-shipper", "13800001234", runContext)
	driver := testSMSCall("call-driver", "13900005678", runContext)
	retry := testSMSCall("call-shipper-retry", "13800001234", runContext)

	first, err := execute(context.Background(), tool, shipper)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(context.Background(), tool, driver); err != nil {
		t.Fatal(err)
	}
	replayed, err := execute(context.Background(), tool, retry)
	if err != nil {
		t.Fatal(err)
	}
	if calls["13800001234"] != 1 || calls["13900005678"] != 1 {
		t.Fatalf("effect calls = %#v, want one call per effect", calls)
	}
	if *replayed.Output.OfString != *first.Output.OfString {
		t.Fatalf("replayed output = %q, want %q", *replayed.Output.OfString, *first.Output.OfString)
	}
	events, err := journal.Replay(context.Background(), "run-effects", 0)
	if err != nil {
		t.Fatal(err)
	}
	effectIDs := make(map[domain.EffectID]struct{})
	keys := make(map[domain.IdempotencyKey]struct{})
	started := 0
	duplicates := 0
	for _, event := range events {
		switch event.Type {
		case audit.EventWriteStarted:
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
		case audit.EventDuplicateSuppressed:
			duplicates++
		}
	}
	if started != 2 || len(effectIDs) != 2 || len(keys) != 2 {
		t.Fatalf("started = %d, effect IDs = %d, keys = %d", started, len(effectIDs), len(keys))
	}
	if duplicates != 1 {
		t.Fatalf("duplicate events = %d, want 1", duplicates)
	}
}

func testSMSCall(
	callID string,
	phone string,
	runContext map[string]any,
) *agents.ToolCall {
	arguments, _ := json.Marshal(map[string]any{
		"phone":       phone,
		"template_id": "waybill_reassigned",
		"params": map[string]string{
			"waybill_id": "YD2026101001",
		},
	})
	return &agents.ToolCall{
		FunctionCallMessage: &responses.FunctionCallMessage{
			ID:        "fc-" + callID,
			CallID:    callID,
			Name:      "notify_send_sms",
			Arguments: string(arguments),
		},
		RunContext: runContext,
	}
}
