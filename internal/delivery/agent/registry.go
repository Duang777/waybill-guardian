package agent

import (
	"context"
	"encoding/json"
	"fmt"

	hastekit "github.com/hastekit/agent-sdk-go"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

const (
	ContractGetProblemSummary   = "delivery.get_problem_summary"
	ContractRequestOptimization = "delivery.request_optimization"
	ContractGetCandidate        = "delivery.get_candidate"
	ContractCompareRevisions    = "delivery.compare_revisions"
	ContractExplainValidation   = "delivery.explain_validation"
	ContractRequestApproval     = "delivery.request_plan_approval"

	metaContractName = "contract_name"
	metaAccess       = "access"
	accessRead       = "read"
	accessCommand    = "command"
)

type Definition struct {
	ContractName string
	WireName     string
	Access       string
	Tool         agents.Tool
}

type Registry struct {
	definitions []Definition
	byWireName  map[string]Definition
}

type callIdentityKey struct{}

type callIdentity struct {
	ThreadID string
	CallID   string
}

func NewRegistry(handlers *Handlers) (*Registry, error) {
	if handlers == nil {
		return nil, fmt.Errorf("delivery agent handlers are required")
	}
	definitions := []Definition{
		readDefinition(
			ContractGetProblemSummary,
			"delivery_get_problem_summary",
			"Read a compact summary of an existing delivery problem",
			handlers.GetProblemSummary,
		),
		commandDefinition(
			ContractRequestOptimization,
			"delivery_request_optimization",
			"Request optimization with an existing problem and registered solver profile",
			handlers.RequestOptimization,
		),
		readDefinition(
			ContractGetCandidate,
			"delivery_get_candidate",
			"Read the validated candidate produced by an optimization run",
			handlers.GetCandidate,
		),
		readDefinition(
			ContractCompareRevisions,
			"delivery_compare_revisions",
			"Compare server-computed metrics for two revisions of one plan",
			handlers.CompareRevisions,
		),
		readDefinition(
			ContractExplainValidation,
			"delivery_explain_validation",
			"Read validator findings for an existing plan revision",
			handlers.ExplainValidation,
		),
		commandDefinition(
			ContractRequestApproval,
			"delivery_request_plan_approval",
			"Create a human approval request for an existing validated revision",
			handlers.RequestPlanApproval,
		),
	}
	byWireName := make(map[string]Definition, len(definitions))
	byContract := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, exists := byWireName[definition.WireName]; exists {
			return nil, fmt.Errorf("duplicate delivery tool wire name %q", definition.WireName)
		}
		if _, exists := byContract[definition.ContractName]; exists {
			return nil, fmt.Errorf("duplicate delivery tool contract %q", definition.ContractName)
		}
		byWireName[definition.WireName] = definition
		byContract[definition.ContractName] = struct{}{}
	}
	return &Registry{definitions: definitions, byWireName: byWireName}, nil
}

func (registry *Registry) Definitions() []Definition {
	return append([]Definition(nil), registry.definitions...)
}

func (registry *Registry) Tools() []agents.Tool {
	result := make([]agents.Tool, 0, len(registry.definitions))
	for _, definition := range registry.definitions {
		result = append(result, definition.Tool)
	}
	return result
}

func (registry *Registry) ByWireName(name string) (Definition, bool) {
	value, ok := registry.byWireName[name]
	return value, ok
}

func readDefinition[I any, O any](
	contractName string,
	wireName string,
	description string,
	handler func(context.Context, I) (O, error),
) Definition {
	return Definition{
		ContractName: contractName,
		WireName:     wireName,
		Access:       accessRead,
		Tool: newStrictTool(
			wireName,
			description,
			contractName,
			accessRead,
			true,
			handler,
		),
	}
}

func commandDefinition[I any, O any](
	contractName string,
	wireName string,
	description string,
	handler func(context.Context, I) (O, error),
) Definition {
	return Definition{
		ContractName: contractName,
		WireName:     wireName,
		Access:       accessCommand,
		Tool: newStrictTool(
			wireName,
			description,
			contractName,
			accessCommand,
			false,
			handler,
		),
	}
}

type strictTool[I any, O any] struct {
	descriptor *agents.BaseTool
	handler    func(context.Context, I) (O, error)
}

func newStrictTool[I any, O any](
	wireName string,
	description string,
	contractName string,
	access string,
	readOnly bool,
	handler func(context.Context, I) (O, error),
) *strictTool[I, O] {
	strict := true
	destructive := false
	idempotent := true
	openWorld := false
	return &strictTool[I, O]{
		descriptor: &agents.BaseTool{
			ToolUnion: responses.ToolUnion{
				OfFunction: &responses.FunctionTool{
					Name:        wireName,
					Description: &description,
					Parameters:  hastekit.NewOutputSchema(*new(I)),
					Strict:      &strict,
				},
			},
			Annotations: &agents.ToolAnnotations{
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
				IdempotentHint:  &idempotent,
				OpenWorldHint:   &openWorld,
			},
			Meta: map[string]any{
				metaContractName: contractName,
				metaAccess:       access,
			},
		},
		handler: handler,
	}
}

func (tool *strictTool[I, O]) GetToolDescriptor() *agents.BaseTool {
	return tool.descriptor
}

func (tool *strictTool[I, O]) Execute(
	ctx context.Context,
	call *agents.ToolCall,
) (*agents.ToolCallResponse, error) {
	if call == nil || call.FunctionCallMessage == nil {
		return nil, fmt.Errorf("delivery tool call is required")
	}
	var input I
	if err := decodeStrictInput(call.Arguments, &input); err != nil {
		return nil, fmt.Errorf("decode delivery tool input: %w", err)
	}
	ctx = context.WithValue(ctx, callIdentityKey{}, callIdentity{
		ThreadID: call.ThreadID,
		CallID:   call.CallID,
	})
	output, err := tool.handler(ctx, input)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return nil, err
	}
	return agents.ToolCallResult(call, string(raw)), nil
}
