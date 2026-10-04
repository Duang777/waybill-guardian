package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/proposal"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

const (
	modelCandidateInitial = "initial"
	modelCandidateRepair  = "repair"
)

type InferenceDescriptor struct {
	Mode     string `json:"mode"`
	APIStyle string `json:"api_style,omitempty"`
	Model    string `json:"model,omitempty"`
}

type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type modelCallStarted struct {
	CallID        string `json:"call_id"`
	Mode          string `json:"mode"`
	Model         string `json:"model,omitempty"`
	APIStyle      string `json:"api_style,omitempty"`
	LoopIteration int    `json:"loop_iteration"`
	Candidate     string `json:"candidate"`
}

type modelCallFinished struct {
	CallID        string      `json:"call_id"`
	Mode          string      `json:"mode"`
	Model         string      `json:"model,omitempty"`
	APIStyle      string      `json:"api_style,omitempty"`
	LoopIteration int         `json:"loop_iteration"`
	Candidate     string      `json:"candidate"`
	LatencyMS     int64       `json:"latency_ms"`
	Usage         *TokenUsage `json:"usage,omitempty"`
	Outcome       string      `json:"outcome"`
	IssueCodes    []string    `json:"issue_codes,omitempty"`
}

type ProposalBoundary struct {
	agents.NoopMiddleware
	journal   audit.Journal
	compiler  *proposal.Compiler
	registry  *guardtools.Registry
	inference InferenceDescriptor
	clock     func() time.Time

	mu       sync.Mutex
	accepted map[string]proposal.Accepted
}

func NewProposalBoundary(
	journal audit.Journal,
	registry *guardtools.Registry,
	inference InferenceDescriptor,
	clock func() time.Time,
) (*ProposalBoundary, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	compiler, err := proposal.NewCompiler(journal)
	if err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	return &ProposalBoundary{
		journal:   journal,
		compiler:  compiler,
		registry:  registry,
		inference: inference,
		clock:     clock,
		accepted:  make(map[string]proposal.Accepted),
	}, nil
}

func (m *ProposalBoundary) WrapModelCall(next agents.ModelCallFunc) agents.ModelCallFunc {
	return func(
		ctx context.Context,
		call *agents.ModelCall,
		request *responses.Request,
	) (*responses.Response, error) {
		ctx = agents.WithModelStreamTransform(
			ctx,
			func(context.Context, *responses.ResponseChunk) (*responses.ResponseChunk, error) {
				return nil, nil
			},
		)
		response, invocation, err := m.invoke(ctx, next, call, request, modelCandidateInitial)
		if err != nil {
			return nil, err
		}
		if !m.hasWriteCall(response) {
			if err := m.finish(ctx, invocation, response, "succeeded"); err != nil {
				return nil, err
			}
			return response, nil
		}
		accepted, err := m.compileResponse(ctx, call, response)
		if err == nil {
			if finishErr := m.finish(ctx, invocation, response, "accepted"); finishErr != nil {
				return nil, finishErr
			}
			m.remember(call.RunID, accepted)
			return response, nil
		}
		if finishErr := m.finish(
			ctx,
			invocation,
			response,
			"validation_failed",
			string(proposal.CodeOf(err)),
		); finishErr != nil {
			return nil, finishErr
		}

		repairRequest, repairErr := proposalRepairRequest(request, response, err)
		if repairErr != nil {
			return nil, repairErr
		}
		repaired, repairInvocation, repairCallErr := m.invoke(
			ctx,
			next,
			call,
			repairRequest,
			modelCandidateRepair,
		)
		if repairCallErr != nil {
			return nil, repairCallErr
		}
		if !m.hasWriteCall(repaired) {
			repairErr := errors.New("repair response did not contain a write call")
			if finishErr := m.finish(
				ctx,
				repairInvocation,
				repaired,
				"validation_failed",
				string(proposal.CodeOf(repairErr)),
			); finishErr != nil {
				return nil, finishErr
			}
			return nil, errors.Join(
				proposal.ErrReviewRequired,
				repairErr,
			)
		}
		accepted, err = m.compileResponse(ctx, call, repaired)
		if err != nil {
			if finishErr := m.finish(
				ctx,
				repairInvocation,
				repaired,
				"validation_failed",
				string(proposal.CodeOf(err)),
			); finishErr != nil {
				return nil, finishErr
			}
			return nil, errors.Join(proposal.ErrReviewRequired, err)
		}
		if finishErr := m.finish(ctx, repairInvocation, repaired, "accepted"); finishErr != nil {
			return nil, finishErr
		}
		m.remember(call.RunID, accepted)
		return repaired, nil
	}
}

func (m *ProposalBoundary) Accepted(sdkRunID string) (proposal.Accepted, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	accepted, ok := m.accepted[sdkRunID]
	if ok {
		delete(m.accepted, sdkRunID)
	}
	return accepted, ok
}

type modelInvocation struct {
	runID     domain.RunID
	callID    string
	started   time.Time
	candidate string
	call      *agents.ModelCall
}

func (m *ProposalBoundary) invoke(
	ctx context.Context,
	next agents.ModelCallFunc,
	call *agents.ModelCall,
	request *responses.Request,
	candidate string,
) (*responses.Response, modelInvocation, error) {
	runID, err := modelRunID(call)
	if err != nil {
		return nil, modelInvocation{}, err
	}
	callID := fmt.Sprintf(
		"model:%s:%d:%s",
		call.RunID,
		call.LoopIteration,
		candidate,
	)
	started := m.clock()
	if _, err := m.journal.Append(ctx, runID, audit.Draft{
		EventID: callID + ":started",
		Actor:   audit.ActorAgent,
		Type:    audit.EventModelCallStarted,
		Payload: modelCallStarted{
			CallID:        callID,
			Mode:          m.inference.Mode,
			Model:         m.inference.Model,
			APIStyle:      m.inference.APIStyle,
			LoopIteration: call.LoopIteration,
			Candidate:     candidate,
		},
	}); err != nil {
		return nil, modelInvocation{}, err
	}

	invocation := modelInvocation{
		runID:     runID,
		callID:    callID,
		started:   started,
		candidate: candidate,
		call:      call,
	}
	response, callErr := next(ctx, call, request)
	if callErr == nil {
		return response, invocation, nil
	}
	if auditErr := m.finish(ctx, invocation, response, "failed"); auditErr != nil {
		return nil, modelInvocation{}, errors.Join(callErr, auditErr)
	}
	return nil, modelInvocation{}, callErr
}

func (m *ProposalBoundary) finish(
	ctx context.Context,
	invocation modelInvocation,
	response *responses.Response,
	outcome string,
	issueCodes ...string,
) error {
	finished := modelCallFinished{
		CallID:        invocation.callID,
		Mode:          m.inference.Mode,
		Model:         m.inference.Model,
		APIStyle:      m.inference.APIStyle,
		LoopIteration: invocation.call.LoopIteration,
		Candidate:     invocation.candidate,
		LatencyMS:     max(m.clock().Sub(invocation.started).Milliseconds(), 0),
		Outcome:       outcome,
		IssueCodes:    issueCodes,
	}
	if response != nil && response.Usage != nil {
		finished.Usage = &TokenUsage{
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
			TotalTokens:  response.Usage.TotalTokens,
		}
	}
	_, err := m.journal.Append(ctx, invocation.runID, audit.Draft{
		EventID: invocation.callID + ":finished",
		Actor:   audit.ActorSystem,
		Type:    audit.EventModelCallFinished,
		Payload: finished,
	})
	return err
}

func (m *ProposalBoundary) compileResponse(
	ctx context.Context,
	call *agents.ModelCall,
	response *responses.Response,
) (proposal.Accepted, error) {
	runID, err := modelRunID(call)
	if err != nil {
		return proposal.Accepted{}, err
	}
	text, err := singleAssistantText(response)
	if err != nil {
		return proposal.Accepted{}, err
	}
	accepted, err := m.compiler.Compile(ctx, runID, []byte(text))
	if err != nil {
		return proposal.Accepted{}, err
	}
	if err := m.validateWriteAlignment(response, accepted); err != nil {
		return proposal.Accepted{}, err
	}
	return accepted, nil
}

func (m *ProposalBoundary) hasWriteCall(response *responses.Response) bool {
	if response == nil {
		return false
	}
	for _, item := range response.Output {
		if item.OfFunctionCall == nil {
			continue
		}
		definition, ok := m.registry.ActiveByWireName(item.OfFunctionCall.Name)
		if ok && definition.Access == guardtools.AccessWrite {
			return true
		}
	}
	return false
}

func (m *ProposalBoundary) validateWriteAlignment(
	response *responses.Response,
	accepted proposal.Accepted,
) error {
	var reassignCarrier string
	var carrierIDs []string
	for _, item := range response.Output {
		if item.OfFunctionCall == nil {
			continue
		}
		definition, ok := m.registry.ActiveByWireName(item.OfFunctionCall.Name)
		if !ok || definition.Access != guardtools.AccessWrite {
			continue
		}
		write, err := m.registry.ParseActiveWrite(
			item.OfFunctionCall.Name,
			json.RawMessage(item.OfFunctionCall.Arguments),
		)
		if err != nil {
			return err
		}
		var carrierID string
		switch write.Action {
		case domain.ActionReassign:
			var input guardtools.ReassignInput
			if err := json.Unmarshal(write.Arguments, &input); err != nil {
				return err
			}
			if reassignCarrier != "" {
				return errors.New("proposal contains more than one reassign call")
			}
			reassignCarrier = input.CarrierID
			carrierID = input.CarrierID
		case domain.ActionSendSMS:
			var input guardtools.SendSMSInput
			if err := json.Unmarshal(write.Arguments, &input); err != nil {
				return err
			}
			carrierID = input.CarrierID
		}
		if carrierID != "" {
			carrierIDs = append(carrierIDs, carrierID)
		}
	}
	for _, carrierID := range carrierIDs {
		if reassignCarrier != "" && carrierID != reassignCarrier {
			return fmt.Errorf(
				"write call carrier %q does not match reassign carrier %q",
				carrierID,
				reassignCarrier,
			)
		}
	}
	if reassignCarrier == "" {
		return nil
	}
	if len(accepted.Alternatives) == 0 ||
		accepted.Alternatives[0].CarrierID != reassignCarrier {
		return fmt.Errorf(
			"selected carrier %q must be the first proposal alternative",
			reassignCarrier,
		)
	}
	return nil
}

func proposalRepairRequest(
	request *responses.Request,
	response *responses.Response,
	validationErr error,
) (*responses.Request, error) {
	if request == nil {
		return nil, errors.New("cannot repair a nil model request")
	}
	failedText, err := singleAssistantText(response)
	if err != nil {
		failedText = "<missing proposal JSON>"
	}
	repair := *request
	messages := append(
		responses.InputMessageList(nil),
		request.Input.OfInputMessageList...,
	)
	if request.Input.OfString != nil {
		messages = append(messages, responses.UserMessage(*request.Input.OfString))
	}
	repairPrompt := fmt.Sprintf(
		"上一份提案未通过校验，错误码为 %s。请修正后重新输出完整的 proposal.v1 JSON，"+
			"并在同一响应中重新提交原写工具调用。不要使用 Markdown 代码块，不要解释错误。"+
			"\n\n未通过的提案：\n%s",
		proposal.CodeOf(validationErr),
		failedText,
	)
	messages = append(messages, responses.UserMessage(repairPrompt))
	repair.Input = responses.InputUnion{OfInputMessageList: messages}
	return &repair, nil
}

func singleAssistantText(response *responses.Response) (string, error) {
	if response == nil {
		return "", errors.New("model returned no response")
	}
	var texts []string
	for _, item := range response.Output {
		if item.OfOutputMessage == nil || item.OfOutputMessage.Content == nil {
			continue
		}
		for _, content := range *item.OfOutputMessage.Content {
			if content.OfOutputText != nil {
				texts = append(texts, content.OfOutputText.Text)
			}
		}
	}
	if len(texts) != 1 {
		return "", fmt.Errorf("write response must contain exactly one proposal text, got %d", len(texts))
	}
	return texts[0], nil
}

func modelRunID(call *agents.ModelCall) (domain.RunID, error) {
	if call == nil {
		return "", errors.New("model call is missing")
	}
	value, _ := call.RunContext["run_id"].(string)
	if value == "" {
		return "", errors.New("run_id is missing from model context")
	}
	return domain.RunID(value), nil
}

func (m *ProposalBoundary) remember(sdkRunID string, accepted proposal.Accepted) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.accepted[sdkRunID] = accepted
}
