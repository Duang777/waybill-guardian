package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
)

type AuditMiddleware struct {
	agents.NoopMiddleware
	journal audit.Journal
}

func NewAuditMiddleware(journal audit.Journal) *AuditMiddleware {
	return &AuditMiddleware{journal: journal}
}

func (m *AuditMiddleware) WrapToolCall(next agents.ToolCallFunc) agents.ToolCallFunc {
	return func(ctx context.Context, tool *agents.BaseTool, call *agents.ToolCall) (*agents.ToolCallResponse, error) {
		runID, err := runIDFromCall(call)
		if err != nil {
			return nil, err
		}
		action, _ := toolAction(tool)
		var arguments any
		if err := json.Unmarshal([]byte(call.Arguments), &arguments); err != nil {
			return nil, fmt.Errorf("decode tool arguments for audit: %w", err)
		}
		if _, err := m.journal.Append(ctx, runID, audit.Draft{
			EventID: "tool:" + call.CallID + ":call",
			Actor:   audit.ActorAgent,
			Type:    audit.EventToolCall,
			Payload: map[string]any{
				"call_id":   call.CallID,
				"action":    action,
				"wire_name": call.Name,
				"arguments": arguments,
			},
		}); err != nil {
			return nil, err
		}

		result, callErr := next(ctx, tool, call)
		payload := map[string]any{
			"call_id":   call.CallID,
			"action":    action,
			"wire_name": call.Name,
		}
		if callErr != nil {
			payload["error"] = callErr.Error()
		} else if result != nil && result.FunctionCallOutputMessage != nil &&
			result.Output.OfString != nil {
			var output any
			if err := json.Unmarshal([]byte(*result.Output.OfString), &output); err != nil {
				output = *result.Output.OfString
			}
			payload["result"] = output
		}
		if _, err := m.journal.Append(ctx, runID, audit.Draft{
			EventID: "tool:" + call.CallID + ":result",
			Actor:   audit.ActorSystem,
			Type:    audit.EventToolResult,
			Payload: payload,
		}); err != nil {
			return nil, err
		}
		return result, callErr
	}
}

type WriteEffectMiddleware struct {
	agents.NoopMiddleware
	approvals *approval.Store
	effects   *idempotency.Store
	registry  *guardtools.Registry
}

func NewWriteEffectMiddleware(
	approvals *approval.Store,
	effects *idempotency.Store,
	registry *guardtools.Registry,
) *WriteEffectMiddleware {
	return &WriteEffectMiddleware{
		approvals: approvals,
		effects:   effects,
		registry:  registry,
	}
}

func (m *WriteEffectMiddleware) WrapToolCall(next agents.ToolCallFunc) agents.ToolCallFunc {
	return func(ctx context.Context, tool *agents.BaseTool, call *agents.ToolCall) (*agents.ToolCallResponse, error) {
		if !isWrite(tool) {
			return next(ctx, tool, call)
		}
		runContext, err := parseRunContext(call.RunContext)
		if err != nil {
			return nil, err
		}
		action, err := toolAction(tool)
		if err != nil {
			return nil, err
		}
		write, err := m.registry.ParseWrite(call.Name, json.RawMessage(call.Arguments))
		if err != nil {
			return nil, err
		}
		if write.Action != action {
			return nil, fmt.Errorf("tool action %q does not match registered action %q", action, write.Action)
		}
		authorization, err := m.approvals.Authorize(approval.AuthorizationRequest{
			RunID:                 runContext.RunID,
			CallID:                call.CallID,
			Action:                action,
			WireName:              write.WireName,
			BusinessArgumentsHash: write.ArgumentsHash,
			LegacyArgumentsHash:   write.LegacyFullHash,
			LegacyKey:             write.LegacyKey,
		})
		if err != nil {
			return nil, err
		}
		identity, err := authorization.Item.Identity()
		if err != nil {
			return nil, err
		}
		var expected idempotency.Identity
		switch identity.Version {
		case idempotency.IdentityEffectV1:
			expected, err = idempotency.Derive(idempotency.DerivationInput{
				RunContext: runContext,
				Action:     action,
				Target:     write.Target,
				Arguments:  write.Arguments,
			})
		case idempotency.IdentityEffectV0:
			expected, err = idempotency.EffectV0Identity(runContext, action, write.ArgumentsHash)
		default:
			expected = identity
		}
		if err != nil {
			return nil, err
		}
		if expected != identity {
			return nil, idempotency.ErrInvalidIdentity
		}
		command := idempotency.Command{
			RunID:    runContext.RunID,
			CallID:   call.CallID,
			Identity: identity,
		}
		if authorization.LegacyAmbiguous {
			state, ok := m.effects.Lookup(command)
			if !ok || state != idempotency.StateSucceeded {
				return nil, approval.ErrAmbiguousLegacyApproval
			}
		}
		executionCall := canonicalToolCall(call, write.Arguments)
		result, err := m.effects.Execute(ctx, command, func(ctx context.Context) (json.RawMessage, error) {
			executionContext, err := idempotency.WithExecution(ctx, identity)
			if err != nil {
				return nil, err
			}
			response, err := next(executionContext, tool, executionCall)
			if err != nil {
				return nil, err
			}
			if response == nil || response.FunctionCallOutputMessage == nil ||
				response.Output.OfString == nil {
				return nil, fmt.Errorf("write tool %q returned no string result", action)
			}
			return json.RawMessage(*response.Output.OfString), nil
		})
		if err != nil {
			return nil, err
		}
		return agents.ToolCallResult(call, string(result.Value)), nil
	}
}

func isWrite(tool *agents.BaseTool) bool {
	if tool == nil || tool.Meta == nil {
		return false
	}
	access, _ := tool.Meta[guardtools.MetaAccess].(string)
	return access == guardtools.AccessWrite
}

func toolAction(tool *agents.BaseTool) (domain.Action, error) {
	if tool == nil || tool.Meta == nil {
		return "", fmt.Errorf("tool metadata is missing")
	}
	name, _ := tool.Meta[guardtools.MetaContractName].(string)
	if name == "" {
		return "", fmt.Errorf("tool contract name is missing")
	}
	return domain.Action(name), nil
}

func runIDFromCall(call *agents.ToolCall) (domain.RunID, error) {
	value, _ := call.RunContext["run_id"].(string)
	if value == "" {
		return "", fmt.Errorf("run_id is missing from tool context")
	}
	return domain.RunID(value), nil
}

func canonicalToolCall(call *agents.ToolCall, arguments json.RawMessage) *agents.ToolCall {
	result := *call
	functionCall := *call.FunctionCallMessage
	functionCall.Arguments = string(arguments)
	result.FunctionCallMessage = &functionCall
	return &result
}
