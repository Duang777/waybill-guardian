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
	journal *audit.Store
}

func NewAuditMiddleware(journal *audit.Store) *AuditMiddleware {
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

type ApprovalGuard struct {
	agents.NoopMiddleware
	approvals *approval.Store
}

func NewApprovalGuard(approvals *approval.Store) *ApprovalGuard {
	return &ApprovalGuard{approvals: approvals}
}

func (m *ApprovalGuard) WrapToolCall(next agents.ToolCallFunc) agents.ToolCallFunc {
	return func(ctx context.Context, tool *agents.BaseTool, call *agents.ToolCall) (*agents.ToolCallResponse, error) {
		if !isWrite(tool) {
			return next(ctx, tool, call)
		}
		runID, err := runIDFromCall(call)
		if err != nil {
			return nil, err
		}
		hash, err := idempotency.ArgumentsHash(call.Arguments)
		if err != nil {
			return nil, err
		}
		if !m.approvals.Allows(runID, call.CallID, hash) {
			return nil, approval.ErrApprovalNotGranted
		}
		return next(ctx, tool, call)
	}
}

type IdempotencyMiddleware struct {
	agents.NoopMiddleware
	store *idempotency.Store
}

func NewIdempotencyMiddleware(store *idempotency.Store) *IdempotencyMiddleware {
	return &IdempotencyMiddleware{store: store}
}

func (m *IdempotencyMiddleware) WrapToolCall(next agents.ToolCallFunc) agents.ToolCallFunc {
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
		key, err := keyFromArguments(call.Arguments)
		if err != nil {
			return nil, err
		}
		window := fmt.Sprintf("%s/plan-%d", runContext.IncidentID, runContext.PlanVersion)
		expected := idempotency.Generate(action, runContext.WaybillID, window)
		if key != expected {
			return nil, fmt.Errorf("%w: got %q", idempotency.ErrMissingKey, key)
		}
		hash, err := idempotency.ArgumentsHash(call.Arguments)
		if err != nil {
			return nil, err
		}
		result, err := m.store.Execute(ctx, idempotency.Command{
			RunID:         runContext.RunID,
			CallID:        call.CallID,
			Action:        action,
			Key:           key,
			ArgumentsHash: hash,
		}, func(ctx context.Context) (json.RawMessage, error) {
			response, err := next(ctx, tool, call)
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

func keyFromArguments(raw string) (domain.IdempotencyKey, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return "", fmt.Errorf("decode write arguments: %w", err)
	}
	value := values["idempotency_key"]
	if len(value) == 0 {
		return "", idempotency.ErrMissingKey
	}
	var key string
	if err := json.Unmarshal(value, &key); err != nil || key == "" {
		return "", idempotency.ErrMissingKey
	}
	return domain.IdempotencyKey(key), nil
}
