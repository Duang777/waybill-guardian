package agent

import (
	"context"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestReadBindingMiddlewareRejectsCrossWaybillEvidence(t *testing.T) {
	reads, _, err := guardtools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	middleware := NewReadBindingMiddleware(reads)
	tests := []struct {
		name      string
		action    domain.Action
		arguments string
	}{
		{
			name:      "waybill",
			action:    domain.ActionGetWaybill,
			arguments: `{"waybill_id":"YD2026101002"}`,
		},
		{
			name:      "tracking",
			action:    domain.ActionGetTracking,
			arguments: `{"waybill_id":"YD2026101002"}`,
		},
		{
			name:      "driver",
			action:    domain.ActionGetDriver,
			arguments: `{"driver_id":"DRIVER-OTHER"}`,
		},
		{
			name:      "weather",
			action:    domain.ActionGetRoadWeather,
			arguments: `{"route":"宁波-西安"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nextCalled := false
			execute := middleware.WrapToolCall(func(
				context.Context,
				*agents.BaseTool,
				*agents.ToolCall,
			) (*agents.ToolCallResponse, error) {
				nextCalled = true
				return nil, nil
			})
			tool := &agents.BaseTool{Meta: map[string]any{
				guardtools.MetaAccess:       guardtools.AccessRead,
				guardtools.MetaContractName: string(test.action),
			}}
			call := &agents.ToolCall{
				FunctionCallMessage: &responses.FunctionCallMessage{
					CallID:    "call-read-binding",
					Name:      "read",
					Arguments: test.arguments,
				},
				RunContext: contextMap(domain.RunContext{
					RunID:      "run-read-binding",
					IncidentID: "incident-read-binding",
					WaybillID:  "YD2026101001",
				}),
			}
			if _, err := execute(t.Context(), tool, call); err == nil {
				t.Fatal("cross-waybill read was accepted")
			}
			if nextCalled {
				t.Fatal("cross-waybill read reached the typed handler")
			}
		})
	}
}

func TestReadBindingMiddlewareAllowsBoundRead(t *testing.T) {
	reads, _, err := guardtools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	nextCalled := false
	execute := NewReadBindingMiddleware(reads).WrapToolCall(func(
		_ context.Context,
		_ *agents.BaseTool,
		call *agents.ToolCall,
	) (*agents.ToolCallResponse, error) {
		nextCalled = true
		return agents.ToolCallResult(call, `{}`), nil
	})
	tool := &agents.BaseTool{Meta: map[string]any{
		guardtools.MetaAccess:       guardtools.AccessRead,
		guardtools.MetaContractName: string(domain.ActionGetDriver),
	}}
	call := &agents.ToolCall{
		FunctionCallMessage: &responses.FunctionCallMessage{
			CallID:    "call-bound-read",
			Name:      "tms_get_driver",
			Arguments: `{"driver_id":"DRV-0286"}`,
		},
		RunContext: contextMap(domain.RunContext{
			RunID:      "run-bound-read",
			IncidentID: "incident-bound-read",
			WaybillID:  "YD2026101001",
		}),
	}
	if _, err := execute(t.Context(), tool, call); err != nil {
		t.Fatal(err)
	}
	if !nextCalled {
		t.Fatal("bound read did not reach the typed handler")
	}
}
