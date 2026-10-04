package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/proposal"
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

	waybillWire, err := m.activeWireName(domain.ActionGetWaybill)
	if err != nil {
		return nil, err
	}
	if !state.hasResult(waybillWire) {
		return toolCallResponse(
			runContext,
			waybillWire,
			guardtools.GetWaybillInput{WaybillID: string(runContext.WaybillID)},
		), nil
	}
	var waybill guardtools.GetWaybillOutput
	if err := state.latestResult(waybillWire, &waybill); err != nil {
		return nil, err
	}
	if waybill.WaybillID != runContext.WaybillID {
		return nil, fmt.Errorf("waybill evidence does not match the run target")
	}

	trackingWire, err := m.activeWireName(domain.ActionGetTracking)
	if err != nil {
		return nil, err
	}
	if !state.hasResult(trackingWire) {
		return toolCallResponse(
			runContext,
			trackingWire,
			guardtools.GetTrackingInput{WaybillID: string(runContext.WaybillID)},
		), nil
	}
	var tracking guardtools.GetTrackingOutput
	if err := state.latestResult(trackingWire, &tracking); err != nil {
		return nil, err
	}

	driverWire, err := m.activeWireName(domain.ActionGetDriver)
	if err != nil {
		return nil, err
	}
	if !state.hasResult(driverWire) {
		return toolCallResponse(
			runContext,
			driverWire,
			guardtools.GetDriverInput{DriverID: string(waybill.DriverID)},
		), nil
	}
	var driver guardtools.GetDriverOutput
	if err := state.latestResult(driverWire, &driver); err != nil {
		return nil, err
	}
	if driver.DriverID != waybill.DriverID {
		return nil, fmt.Errorf("driver evidence does not match the waybill")
	}

	weatherWire, err := m.activeWireName(domain.ActionGetRoadWeather)
	if err != nil {
		return nil, err
	}
	route := waybill.Origin + "-" + waybill.Destination
	if !state.hasResult(weatherWire) {
		return toolCallResponse(
			runContext,
			weatherWire,
			guardtools.GetRoadWeatherInput{Route: route},
		), nil
	}
	var weather guardtools.GetRoadWeatherOutput
	if err := state.latestResult(weatherWire, &weather); err != nil {
		return nil, err
	}
	if len(waybill.CandidateCarriers) == 0 {
		return nil, fmt.Errorf("waybill has no candidate carriers")
	}

	reassignWire, ok := m.registry.ActiveWireName(domain.ActionReassign)
	if !ok {
		return nil, fmt.Errorf("tool capability %q is unavailable", domain.ActionReassign)
	}
	smsWire, smsActive := m.registry.ActiveWireName(domain.ActionSendSMS)
	reassignCalls := state.calls[reassignWire]
	if reassignCalls == 0 {
		carrier := waybill.CandidateCarriers[0]
		return m.proposal(
			runContext,
			runContext.PlanVersion,
			carrier,
			proposalSummary(carrier, tracking, driver, weather),
			waybill,
			tracking,
			driver,
			weather,
			state,
		)
	}
	if state.latestDeclined(reassignWire) {
		if reassignCalls < len(waybill.CandidateCarriers) {
			carrier := waybill.CandidateCarriers[reassignCalls]
			return m.proposal(
				runContext,
				runContext.PlanVersion,
				carrier,
				fmt.Sprintf("上一改派方案已驳回，改用%s作为备选运力。", carrier.Name),
				waybill,
				tracking,
				driver,
				weather,
				state,
			)
		}
		return textResponse("候选改派方案均被驳回，本次处置结束并转人工跟进。"), nil
	}
	if state.hasSuccessfulResult(reassignWire) {
		if !smsActive {
			return textResponse("改派已完成，处置过程已写入审计时间线。"), nil
		}
		if state.successfulResultCount(smsWire) >= 2 {
			return textResponse("改派已完成，货主和司机通知已发送，处置过程已写入审计时间线。"), nil
		}
	}
	return textResponse("写操作未全部成功，本次处置转人工检查。"), nil
}

func (m *ScenarioModel) activeWireName(action domain.Action) (string, error) {
	wireName, ok := m.registry.ActiveWireName(action)
	if !ok {
		return "", fmt.Errorf("tool capability %q is unavailable", action)
	}
	return wireName, nil
}

func toolCallResponse(
	runContext domain.RunContext,
	wireName string,
	arguments any,
) *responses.Response {
	return &responses.Response{
		Output: []responses.OutputMessageUnion{
			toolCall(runContext.RunID, wireName, runContext.PlanVersion, arguments),
		},
	}
}

func proposalSummary(
	carrier guardtools.CarrierEvidence,
	tracking guardtools.GetTrackingOutput,
	driver guardtools.GetDriverOutput,
	weather guardtools.GetRoadWeatherOutput,
) string {
	evidence := make([]string, 0, 3)
	if driver.FatigueAlert {
		evidence = append(
			evidence,
			fmt.Sprintf("司机连续驾驶%s小时并触发疲劳预警", formatNumber(driver.ContinuousDriveHrs)),
		)
	}
	for _, point := range tracking.Points {
		if !point.Anomaly {
			continue
		}
		value := point.Label
		if point.StopHours > 0 {
			value += fmt.Sprintf("停留%s小时", formatNumber(point.StopHours))
		}
		evidence = append(evidence, value)
	}
	for _, segment := range weather.Segments {
		if strings.EqualFold(segment.AlertLevel, "none") {
			continue
		}
		evidence = append(
			evidence,
			fmt.Sprintf("%s预警级别%s", segment.Segment, segment.AlertLevel),
		)
	}
	prefix := "已完成运单、轨迹、司机和天气核验"
	if len(evidence) > 0 {
		prefix = strings.Join(evidence, "，")
	}
	return fmt.Sprintf("%s，建议改派%s。", prefix, carrier.Name)
}

func formatNumber(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

func (m *ScenarioModel) proposal(
	runContext domain.RunContext,
	planVersion int,
	carrier guardtools.CarrierEvidence,
	summary string,
	waybill guardtools.GetWaybillOutput,
	tracking guardtools.GetTrackingOutput,
	driver guardtools.GetDriverOutput,
	weather guardtools.GetRoadWeatherOutput,
	state conversationState,
) (*responses.Response, error) {
	reassignWire, _ := m.registry.ActiveWireName(domain.ActionReassign)
	reassign := guardtools.ReassignInput{
		WaybillID: string(runContext.WaybillID),
		CarrierID: string(carrier.ID),
	}
	draft, err := offlineProposal(
		summary,
		carrier,
		waybill,
		tracking,
		driver,
		weather,
		state,
		m.registry,
	)
	if err != nil {
		return nil, err
	}
	rawProposal, err := json.Marshal(draft)
	if err != nil {
		return nil, fmt.Errorf("marshal offline proposal: %w", err)
	}
	output := []responses.OutputMessageUnion{
		assistantText(string(rawProposal)),
		toolCall(runContext.RunID, reassignWire, planVersion, reassign),
	}
	smsWire, smsActive := m.registry.ActiveWireName(domain.ActionSendSMS)
	if !smsActive {
		return &responses.Response{Output: output}, nil
	}
	shipperSMS := guardtools.SendSMSInput{
		WaybillID: string(runContext.WaybillID),
		Recipient: guardtools.RecipientShipper,
		CarrierID: string(carrier.ID),
	}
	driverSMS := guardtools.SendSMSInput{
		WaybillID: string(runContext.WaybillID),
		Recipient: guardtools.RecipientDriver,
		CarrierID: string(carrier.ID),
	}
	output = append(output,
		toolCall(runContext.RunID, smsWire, planVersion, shipperSMS),
		toolCall(runContext.RunID, smsWire, planVersion, driverSMS),
	)
	return &responses.Response{Output: output}, nil
}

func offlineProposal(
	summary string,
	selected guardtools.CarrierEvidence,
	waybill guardtools.GetWaybillOutput,
	tracking guardtools.GetTrackingOutput,
	driver guardtools.GetDriverOutput,
	weather guardtools.GetRoadWeatherOutput,
	state conversationState,
	registry *guardtools.Registry,
) (proposal.Draft, error) {
	driverWire, _ := registry.ActiveWireName(domain.ActionGetDriver)
	trackingWire, _ := registry.ActiveWireName(domain.ActionGetTracking)
	weatherWire, _ := registry.ActiveWireName(domain.ActionGetRoadWeather)
	waybillWire, _ := registry.ActiveWireName(domain.ActionGetWaybill)
	attribution := make([]proposal.AttributionDraft, 0, 3)
	if driver.ContinuousDriveHrs > 0 || driver.FatigueAlert {
		attribution = append(attribution, proposal.AttributionDraft{
			Factor:        fmt.Sprintf("司机连续驾驶%s小时", formatNumber(driver.ContinuousDriveHrs)),
			ConfidenceBPS: 9100,
			EvidenceRefs: []proposal.EvidenceRef{
				evidenceRef(
					state.latestCallID(driverWire),
					"/continuous_drive_hours",
					driver.ContinuousDriveHrs,
				),
				evidenceRef(state.latestCallID(driverWire), "/fatigue_alert", driver.FatigueAlert),
			},
		})
	}
	for index, point := range tracking.Points {
		if !point.Anomaly {
			continue
		}
		refs := []proposal.EvidenceRef{
			evidenceRef(
				state.latestCallID(trackingWire),
				fmt.Sprintf("/points/%d/label", index),
				point.Label,
			),
			evidenceRef(
				state.latestCallID(trackingWire),
				fmt.Sprintf("/points/%d/anomaly", index),
				point.Anomaly,
			),
		}
		if point.StopHours > 0 {
			refs = append(refs, evidenceRef(
				state.latestCallID(trackingWire),
				fmt.Sprintf("/points/%d/stop_hours", index),
				point.StopHours,
			))
		}
		attribution = append(attribution, proposal.AttributionDraft{
			Factor:        point.Label + "出现异常停留",
			ConfidenceBPS: 8800,
			EvidenceRefs:  refs,
		})
		break
	}
	for index, segment := range weather.Segments {
		if strings.EqualFold(segment.AlertLevel, "none") {
			continue
		}
		attribution = append(attribution, proposal.AttributionDraft{
			Factor:        segment.Segment + "存在天气预警",
			ConfidenceBPS: 7600,
			EvidenceRefs: []proposal.EvidenceRef{
				evidenceRef(
					state.latestCallID(weatherWire),
					fmt.Sprintf("/segments/%d/condition", index),
					segment.Condition,
				),
				evidenceRef(
					state.latestCallID(weatherWire),
					fmt.Sprintf("/segments/%d/alert_level", index),
					segment.AlertLevel,
				),
			},
		})
		break
	}
	if len(attribution) == 0 {
		attribution = append(attribution, proposal.AttributionDraft{
			Factor:        "运单当前仍处于运输状态",
			ConfidenceBPS: 6000,
			EvidenceRefs: []proposal.EvidenceRef{
				evidenceRef(state.latestCallID(waybillWire), "/status", waybill.Status),
			},
		})
	}

	alternatives := make([]proposal.Alternative, 0, min(len(waybill.CandidateCarriers), 8))
	alternatives = append(alternatives, carrierAlternative(selected))
	for _, carrier := range waybill.CandidateCarriers {
		if carrier.ID == selected.ID || len(alternatives) == 8 {
			continue
		}
		alternatives = append(alternatives, carrierAlternative(carrier))
	}
	return proposal.Draft{
		SchemaVersion: proposal.SchemaVersion,
		Summary:       summary,
		ConfidenceBPS: 8600,
		Attribution:   attribution,
		Alternatives:  alternatives,
		ExpectedImpact: proposal.ExpectedImpactDraft{
			ETASavedMin: proposal.ImpactMetricDraft{
				Availability: proposal.AvailabilityUnavailable,
				Reason:       "当前证据没有改派后的到达时间",
			},
			CostDeltaCNY: proposal.ImpactMetricDraft{
				Availability: proposal.AvailabilityUnavailable,
				Reason:       "当前证据没有成本字段",
			},
		},
	}, nil
}

func evidenceRef(callID string, fieldPath string, value any) proposal.EvidenceRef {
	raw, _ := json.Marshal(value)
	return proposal.EvidenceRef{
		ToolCallID: callID,
		FieldPath:  proposal.JSONPointer(fieldPath),
		Quoted:     raw,
	}
}

func carrierAlternative(carrier guardtools.CarrierEvidence) proposal.Alternative {
	return proposal.Alternative{
		CarrierID: string(carrier.ID),
		Reason: fmt.Sprintf(
			"预计时效%d小时，历史履约率%s%%",
			carrier.ETAHours,
			formatNumber(carrier.ReliabilityPct),
		),
	}
}

type conversationResult struct {
	callID string
	output string
}

type conversationState struct {
	calls   map[string]int
	results map[string][]conversationResult
}

func inspectConversation(messages responses.InputMessageList) conversationState {
	state := conversationState{
		calls:   make(map[string]int),
		results: make(map[string][]conversationResult),
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
			state.results[name] = append(state.results[name], conversationResult{
				callID: output.CallID,
				output: *output.Output.OfString,
			})
		}
	}
	return state
}

func (s conversationState) hasResult(name string) bool {
	return len(s.results[name]) > 0
}

func (s conversationState) latestResult(name string, destination any) error {
	values := s.results[name]
	if len(values) == 0 {
		return fmt.Errorf("tool %q has no result", name)
	}
	if err := json.Unmarshal([]byte(values[len(values)-1].output), destination); err != nil {
		return fmt.Errorf("decode tool %q result: %w", name, err)
	}
	return nil
}

func (s conversationState) latestCallID(name string) string {
	values := s.results[name]
	if len(values) == 0 {
		return ""
	}
	return values[len(values)-1].callID
}

func (s conversationState) latestDeclined(name string) bool {
	values := s.results[name]
	return len(values) > 0 && strings.Contains(values[len(values)-1].output, "declined")
}

func (s conversationState) hasSuccessfulResult(name string) bool {
	values := s.results[name]
	if len(values) == 0 {
		return false
	}
	latest := values[len(values)-1].output
	return !strings.Contains(latest, "declined") && !strings.Contains(latest, "failed")
}

func (s conversationState) successfulResultCount(name string) int {
	count := 0
	for _, result := range s.results[name] {
		if !strings.Contains(result.output, "declined") &&
			!strings.Contains(result.output, "failed") {
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
	return strings.ToLower(
		base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:8]),
	)
}

var _ agents.LLM = (*ScenarioModel)(nil)
