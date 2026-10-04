package agent

import (
	"context"
	"testing"

	"github.com/Duang777/waybill-guardian/internal/domain"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestCapabilityModelMiddlewareFiltersExecutionOnlyTools(t *testing.T) {
	registry := partialRegistry(t)
	request := &responses.Request{}
	for _, definition := range registry.Definitions() {
		request.Tools = append(
			request.Tools,
			definition.Tool.GetToolDescriptor().ToolUnion,
		)
	}
	request.Tools = append(request.Tools, responses.ToolUnion{
		OfImageGeneration: &responses.ImageGenerationTool{},
	})
	originalCount := len(request.Tools)

	var gotRequest *responses.Request
	wrapped := NewCapabilityModelMiddleware(registry).WrapModelCall(func(
		_ context.Context,
		_ *agents.ModelCall,
		filtered *responses.Request,
	) (*responses.Response, error) {
		gotRequest = filtered
		return agents.ModelCallText("done"), nil
	})
	if _, err := wrapped(context.Background(), &agents.ModelCall{}, request); err != nil {
		t.Fatal(err)
	}
	if len(request.Tools) != originalCount {
		t.Fatalf("middleware mutated original tools: got %d, want %d", len(request.Tools), originalCount)
	}
	if gotRequest == request {
		t.Fatal("middleware passed the mutable request to the next model call")
	}

	want := []string{
		"tms_get_waybill",
		"tms_get_tracking",
		"tms_get_driver",
		"ext_get_road_weather",
		"tms_reassign",
	}
	if len(gotRequest.Tools) != len(want) {
		t.Fatalf("filtered tools = %d, want %d", len(gotRequest.Tools), len(want))
	}
	for index, tool := range gotRequest.Tools {
		if tool.OfFunction == nil || tool.OfFunction.Name != want[index] {
			t.Fatalf("tool %d = %+v, want %q", index, tool, want[index])
		}
	}
}

func partialRegistry(t *testing.T) *guardtools.Registry {
	t.Helper()
	clients, _, err := guardtools.NewDemoClients()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := guardtools.NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := guardtools.NewRegistryForActions(handlers, []domain.Action{
		domain.ActionGetWaybill,
		domain.ActionGetTracking,
		domain.ActionGetDriver,
		domain.ActionGetRoadWeather,
		domain.ActionReassign,
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
