package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/constants"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

type ScenarioModel struct {
	registry *guardtools.Registry
	delay    time.Duration
}

func NewScenarioModel(registry *guardtools.Registry, delay time.Duration) *ScenarioModel {
	return &ScenarioModel{registry: registry, delay: delay}
}

func (m *ScenarioModel) NewStreamingResponses(
	ctx context.Context,
	call *agents.ModelCall,
	request *responses.Request,
	_ func(*responses.ResponseChunk),
) (*responses.Response, error) {
	if m.delay > 0 {
		timer := time.NewTimer(m.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	state := inspectConversation(request.Input.OfInputMessageList)
	runContext, err := parseRunContext(call.RunContext)
	if err != nil {
		return nil, err
	}

	for _, step := range []struct {
		action domain.Action
		args   any
	}{
		{domain.ActionGetWaybill, guardtools.GetWaybillInput{WaybillID: string(runContext.WaybillID)}},
		{domain.ActionGetTracking, guardtools.GetTrackingInput{WaybillID: string(runContext.WaybillID)}},
		{domain.ActionGetDriver, guardtools.GetDriverInput{DriverID: "DRV-0286"}},
		{domain.ActionGetRoadWeather, guardtools.GetRoadWeatherInput{Route: "杭州-成都"}},
	} {
		wireName, ok := m.registry.WireName(step.action)
		if !ok {
			return nil, fmt.Errorf("tool %q is not registered", step.action)
		}
		if !state.hasResult(wireName) {
			return &responses.Response{
				Output: []responses.OutputMessageUnion{
					toolCall(runContext.RunID, wireName, 1, step.args),
				},
			}, nil
		}
	}

	reassignWire, _ := m.registry.WireName(domain.ActionReassign)
	smsWire, _ := m.registry.WireName(domain.ActionSendSMS)
	reassignCalls := state.calls[reassignWire]
	if reassignCalls == 0 {
		return m.proposal(runContext, 1, "CARRIER-SW-42",
			"司机连续驾驶 9 小时且绵阳北服务区停留 6 小时，天气无预警，建议改派川行快运。"), nil
	}
	if state.latestDeclined(reassignWire) {
		if reassignCalls == 1 {
			return m.proposal(runContext, 2, "CARRIER-SW-19",
				"首选改派已驳回，改用蜀道联运作为备选运力。"), nil
		}
		return textResponse("两个改派方案均被驳回，本次处置结束并转人工跟进。"), nil
	}
	if state.hasSuccessfulResult(reassignWire) && state.successfulResultCount(smsWire) >= 2 {
		return textResponse("改派已完成，货主和司机通知已发送，处置过程已写入审计时间线。"), nil
	}
	return textResponse("写操作未全部成功，本次处置转人工检查。"), nil
}

func (m *ScenarioModel) proposal(
	runContext domain.RunContext,
	planVersion int,
	carrierID string,
	summary string,
) *responses.Response {
	reassignWire, _ := m.registry.WireName(domain.ActionReassign)
	smsWire, _ := m.registry.WireName(domain.ActionSendSMS)
	reassign := guardtools.ReassignInput{
		WaybillID: string(runContext.WaybillID),
		CarrierID: carrierID,
	}
	shipperSMS := guardtools.SendSMSInput{
		Phone:      "13800001234",
		TemplateID: "waybill_reassigned",
		Params: map[string]string{
			"waybill_id": string(runContext.WaybillID),
			"carrier_id": carrierID,
		},
	}
	driverSMS := guardtools.SendSMSInput{
		Phone:      "13961234567",
		TemplateID: "waybill_reassigned_driver",
		Params: map[string]string{
			"waybill_id": string(runContext.WaybillID),
			"carrier_id": carrierID,
		},
	}
	return &responses.Response{Output: []responses.OutputMessageUnion{
		assistantText(summary),
		toolCall(runContext.RunID, reassignWire, planVersion, reassign),
		toolCall(runContext.RunID, smsWire, planVersion, shipperSMS),
		toolCall(runContext.RunID, smsWire, planVersion, driverSMS),
	}}
}

type conversationState struct {
	calls   map[string]int
	results map[string][]string
}

func inspectConversation(messages responses.InputMessageList) conversationState {
	state := conversationState{
		calls:   make(map[string]int),
		results: make(map[string][]string),
	}
	callNames := make(map[string]string)
	for _, message := range messages {
		if message.OfFunctionCall != nil {
			call := message.OfFunctionCall
			callNames[call.CallID] = call.Name
			state.calls[call.Name]++
			continue
		}
		if message.OfFunctionCallOutput == nil || message.OfFunctionCallOutput.Output.OfString == nil {
			continue
		}
		output := message.OfFunctionCallOutput
		if name := callNames[output.CallID]; name != "" {
			state.results[name] = append(state.results[name], *output.Output.OfString)
		}
	}
	return state
}

func (s conversationState) hasResult(name string) bool {
	return len(s.results[name]) > 0
}

func (s conversationState) latestDeclined(name string) bool {
	values := s.results[name]
	return len(values) > 0 && strings.Contains(values[len(values)-1], "declined")
}

func (s conversationState) hasSuccessfulResult(name string) bool {
	values := s.results[name]
	if len(values) == 0 {
		return false
	}
	latest := values[len(values)-1]
	return !strings.Contains(latest, "declined") && !strings.Contains(latest, "failed")
}

func (s conversationState) successfulResultCount(name string) int {
	count := 0
	for _, result := range s.results[name] {
		if !strings.Contains(result, "declined") && !strings.Contains(result, "failed") {
			count++
		}
	}
	return count
}

func parseRunContext(values map[string]any) (domain.RunContext, error) {
	runID, _ := values["run_id"].(string)
	incidentID, _ := values["incident_id"].(string)
	waybillID, _ := values["waybill_id"].(string)
	planVersion := intValue(values["plan_version"])
	if runID == "" || incidentID == "" || waybillID == "" {
		return domain.RunContext{}, fmt.Errorf("scenario model requires run_id, incident_id, and waybill_id")
	}
	if planVersion == 0 {
		planVersion = 1
	}
	return domain.RunContext{
		RunID:       domain.RunID(runID),
		IncidentID:  domain.IncidentID(incidentID),
		WaybillID:   domain.WaybillID(waybillID),
		PlanVersion: planVersion,
	}, nil
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	default:
		return 0
	}
}

func toolCall(runID domain.RunID, name string, version int, args any) responses.OutputMessageUnion {
	raw, _ := json.Marshal(args)
	callID := stableID(string(runID) + "|" + name + "|" + fmt.Sprint(version) + "|" + string(raw))
	return responses.OutputMessageUnion{OfFunctionCall: &responses.FunctionCallMessage{
		ID:        "fc_" + callID,
		CallID:    "call_" + callID,
		Name:      name,
		Arguments: string(raw),
	}}
}

func textResponse(text string) *responses.Response {
	return &responses.Response{Output: []responses.OutputMessageUnion{assistantText(text)}}
}

func assistantText(text string) responses.OutputMessageUnion {
	content := responses.OutputContent{
		{OfOutputText: &responses.OutputTextContent{Text: text}},
	}
	return responses.OutputMessageUnion{OfOutputMessage: &responses.OutputMessage{
		ID:      "msg_" + stableID(text),
		Role:    constants.RoleAssistant,
		Content: &content,
	}}
}

func stableID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

var _ agents.LLM = (*ScenarioModel)(nil)
