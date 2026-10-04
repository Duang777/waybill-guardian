package agent

import (
	"context"

	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

type CapabilityModelMiddleware struct {
	agents.NoopMiddleware
	activeWireNames map[string]struct{}
}

func NewCapabilityModelMiddleware(
	registry *guardtools.Registry,
) *CapabilityModelMiddleware {
	active := make(map[string]struct{})
	if registry != nil {
		for _, definition := range registry.ActiveDefinitions() {
			active[definition.WireName] = struct{}{}
		}
	}
	return &CapabilityModelMiddleware{activeWireNames: active}
}

func (m *CapabilityModelMiddleware) WrapModelCall(
	next agents.ModelCallFunc,
) agents.ModelCallFunc {
	return func(
		ctx context.Context,
		call *agents.ModelCall,
		request *responses.Request,
	) (*responses.Response, error) {
		if request == nil {
			return next(ctx, call, request)
		}
		filtered := *request
		filtered.Tools = make([]responses.ToolUnion, 0, len(request.Tools))
		for _, tool := range request.Tools {
			if tool.OfFunction == nil {
				continue
			}
			if _, ok := m.activeWireNames[tool.OfFunction.Name]; ok {
				filtered.Tools = append(filtered.Tools, tool)
			}
		}
		return next(ctx, call, &filtered)
	}
}
