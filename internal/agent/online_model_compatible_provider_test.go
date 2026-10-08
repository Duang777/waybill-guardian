package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform"
	"github.com/Duang777/waybill-guardian/internal/proposal"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
)

const compatibleTerminalText = "全部已批准操作已执行完成。"

type compatibleModelServer struct {
	mu            sync.Mutex
	apiStyle      string
	requests      map[domain.WaybillID]int
	resultCallIDs map[domain.WaybillID]map[string]struct{}
}

func newCompatibleModelServer(apiStyle string) *compatibleModelServer {
	return &compatibleModelServer{
		apiStyle:      apiStyle,
		requests:      make(map[domain.WaybillID]int),
		resultCallIDs: make(map[domain.WaybillID]map[string]struct{}),
	}
}

func (s *compatibleModelServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		http.Error(w, "missing test authorization", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var state compatibleConversation
	switch s.apiStyle {
	case APIStyleResponses:
		if r.URL.Path != "/v1/responses" {
			http.Error(w, "responses API used the wrong endpoint", http.StatusBadRequest)
			return
		}
		state, err = decodeResponsesCompatibleRequest(body)
	case APIStyleChatCompletions:
		if r.URL.Path != "/v1/chat/completions" {
			http.Error(w, "chat completions API used the wrong endpoint", http.StatusBadRequest)
			return
		}
		state, err = decodeChatCompatibleRequest(body)
	default:
		err = fmt.Errorf("unsupported API style %q", s.apiStyle)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	output, err := nextCompatibleOutput(state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.requests[state.waybillID]++
	seen := make(map[string]struct{}, len(state.resultsByCallID))
	for callID := range state.resultsByCallID {
		seen[callID] = struct{}{}
	}
	s.resultCallIDs[state.waybillID] = seen
	s.mu.Unlock()

	switch s.apiStyle {
	case APIStyleResponses:
		writeResponsesStream(w, output)
	case APIStyleChatCompletions:
		writeChatCompletionsStream(w, output)
	}
}

func (s *compatibleModelServer) requestCount(waybillID domain.WaybillID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[waybillID]
}

func (s *compatibleModelServer) sawResults(
	waybillID domain.WaybillID,
	callIDs []string,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.resultCallIDs[waybillID]
	for _, callID := range callIDs {
		if _, ok := seen[callID]; !ok {
			return false
		}
	}
	return true
}

type compatibleConversation struct {
	waybillID       domain.WaybillID
	callOrder       []string
	callsByID       map[string]compatibleObservedCall
	resultsByCallID map[string]json.RawMessage
}

type compatibleObservedCall struct {
	name      string
	arguments json.RawMessage
}

func decodeResponsesCompatibleRequest(body []byte) (compatibleConversation, error) {
	return decodeCompatibleRequestItems(body, "input", "messages")
}

func decodeChatCompatibleRequest(body []byte) (compatibleConversation, error) {
	return decodeCompatibleRequestItems(body, "messages", "input")
}

func decodeCompatibleRequestItems(
	body []byte,
	itemsField string,
	rejectedField string,
) (compatibleConversation, error) {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return compatibleConversation{}, fmt.Errorf("decode compatible request: %w", err)
	}
	if _, exists := request[rejectedField]; exists {
		return compatibleConversation{}, fmt.Errorf(
			"compatible request unexpectedly contains %q",
			rejectedField,
		)
	}
	rawItems, ok := request[itemsField]
	if !ok {
		return compatibleConversation{}, fmt.Errorf(
			"compatible request has no %q",
			itemsField,
		)
	}
	var items []any
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return compatibleConversation{}, fmt.Errorf(
			"decode compatible request %s: %w",
			itemsField,
			err,
		)
	}
	state := compatibleConversation{
		callsByID:       make(map[string]compatibleObservedCall),
		resultsByCallID: make(map[string]json.RawMessage),
	}
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if message["role"] == "user" && state.waybillID == "" {
			state.waybillID = domain.WaybillID(findWaybillID(message["content"]))
		}
		if message["type"] == "function_call" {
			if err := state.addCall(
				stringValue(message["call_id"]),
				stringValue(message["name"]),
				message["arguments"],
			); err != nil {
				return compatibleConversation{}, err
			}
		}
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				function, _ := call["function"].(map[string]any)
				if err := state.addCall(
					stringValue(call["id"]),
					stringValue(function["name"]),
					function["arguments"],
				); err != nil {
					return compatibleConversation{}, err
				}
			}
		}
	}
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var callID string
		var output any
		switch {
		case message["type"] == "function_call_output":
			callID = stringValue(message["call_id"])
			output = message["output"]
		case message["role"] == "tool":
			callID = stringValue(message["tool_call_id"])
			output = message["content"]
		}
		if callID == "" {
			continue
		}
		if _, ok := state.callsByID[callID]; !ok {
			return compatibleConversation{}, fmt.Errorf(
				"tool result references unknown call_id %q",
				callID,
			)
		}
		if _, duplicate := state.resultsByCallID[callID]; duplicate {
			return compatibleConversation{}, fmt.Errorf(
				"duplicate tool result for call_id %q",
				callID,
			)
		}
		raw, err := compatibleOutputJSON(output)
		if err != nil {
			return compatibleConversation{}, err
		}
		if !json.Valid(raw) {
			return compatibleConversation{}, fmt.Errorf(
				"tool result for call_id %q is not JSON",
				callID,
			)
		}
		state.resultsByCallID[callID] = raw
	}
	if state.waybillID == "" {
		return compatibleConversation{}, fmt.Errorf("user message has no waybill ID")
	}
	return state, nil
}

func (s *compatibleConversation) addCall(id, name string, arguments any) error {
	if id == "" || name == "" {
		return fmt.Errorf("compatible tool call has no call_id or name")
	}
	if _, duplicate := s.callsByID[id]; duplicate {
		return fmt.Errorf("duplicate compatible tool call %q", id)
	}
	raw, err := compatibleOutputJSON(arguments)
	if err != nil {
		return err
	}
	if !json.Valid(raw) {
		return fmt.Errorf("tool call %q arguments are not JSON", id)
	}
	s.callOrder = append(s.callOrder, id)
	s.callsByID[id] = compatibleObservedCall{
		name:      name,
		arguments: raw,
	}
	return nil
}

var waybillIDPattern = regexp.MustCompile(`YD[0-9]+`)

func findWaybillID(value any) string {
	switch typed := value.(type) {
	case string:
		return waybillIDPattern.FindString(typed)
	case []any:
		for _, item := range typed {
			if result := findWaybillID(item); result != "" {
				return result
			}
		}
	case map[string]any:
		for _, item := range typed {
			if result := findWaybillID(item); result != "" {
				return result
			}
		}
	}
	return ""
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func compatibleOutputJSON(value any) (json.RawMessage, error) {
	if text, ok := value.(string); ok {
		return json.RawMessage(text), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode compatible tool value: %w", err)
	}
	return raw, nil
}

type compatibleOutput struct {
	text  string
	calls []compatibleToolCall
}

type compatibleToolCall struct {
	id        string
	name      string
	arguments any
}

func nextCompatibleOutput(state compatibleConversation) (compatibleOutput, error) {
	waybillRaw, ok, err := state.readResult("tms_get_waybill")
	if err != nil {
		return compatibleOutput{}, err
	}
	if !ok {
		return state.nextRead(
			"tms_get_waybill",
			guardtools.GetWaybillInput{WaybillID: string(state.waybillID)},
		)
	}
	var waybill guardtools.GetWaybillOutput
	if err := json.Unmarshal(waybillRaw, &waybill); err != nil {
		return compatibleOutput{}, fmt.Errorf("decode waybill result: %w", err)
	}

	trackingRaw, ok, err := state.readResult("tms_get_tracking")
	if err != nil {
		return compatibleOutput{}, err
	}
	if !ok {
		return state.nextRead(
			"tms_get_tracking",
			guardtools.GetTrackingInput{WaybillID: string(state.waybillID)},
		)
	}
	driverRaw, ok, err := state.readResult("tms_get_driver")
	if err != nil {
		return compatibleOutput{}, err
	}
	if !ok {
		return state.nextRead(
			"tms_get_driver",
			guardtools.GetDriverInput{DriverID: string(waybill.DriverID)},
		)
	}
	_, ok, err = state.readResult("ext_get_road_weather")
	if err != nil {
		return compatibleOutput{}, err
	}
	if !ok {
		return state.nextRead(
			"ext_get_road_weather",
			guardtools.GetRoadWeatherInput{
				Route: waybill.Origin + "-" + waybill.Destination,
			},
		)
	}

	var driver guardtools.GetDriverOutput
	if err := json.Unmarshal(driverRaw, &driver); err != nil {
		return compatibleOutput{}, fmt.Errorf("decode driver result: %w", err)
	}
	var tracking guardtools.GetTrackingOutput
	if err := json.Unmarshal(trackingRaw, &tracking); err != nil {
		return compatibleOutput{}, fmt.Errorf("decode tracking result: %w", err)
	}
	decision, err := compatibleDecisionOutput(state.waybillID, waybill, driver, tracking)
	if err != nil {
		return compatibleOutput{}, err
	}
	return state.finishOrPropose(decision)
}

func (s compatibleConversation) readResult(
	name string,
) (json.RawMessage, bool, error) {
	callID := compatibleCallID(s.waybillID, name)
	call, called := s.callsByID[callID]
	result, completed := s.resultsByCallID[callID]
	if called && call.name != name {
		return nil, false, fmt.Errorf(
			"read call %q has name %q, want %q",
			callID,
			call.name,
			name,
		)
	}
	if completed && !called {
		return nil, false, fmt.Errorf("read result %q has no call", callID)
	}
	if called && !completed {
		return nil, false, fmt.Errorf("read call %q has no result", callID)
	}
	return result, completed, nil
}

func (s compatibleConversation) nextRead(
	name string,
	arguments any,
) (compatibleOutput, error) {
	callID := compatibleCallID(s.waybillID, name)
	if _, exists := s.callsByID[callID]; exists {
		return compatibleOutput{}, fmt.Errorf("read call %q is incomplete", callID)
	}
	for observedID := range s.callsByID {
		if !isCompatibleReadCall(s.waybillID, observedID) {
			return compatibleOutput{}, fmt.Errorf(
				"unexpected call %q before evidence collection completed",
				observedID,
			)
		}
	}
	return compatibleReadCall(s.waybillID, name, arguments), nil
}

func (s compatibleConversation) finishOrPropose(
	decision compatibleOutput,
) (compatibleOutput, error) {
	expectedWrites := make(map[string]compatibleToolCall, len(decision.calls))
	for _, call := range decision.calls {
		expectedWrites[call.id] = call
	}
	observedWrites := 0
	for callID := range s.callsByID {
		if isCompatibleReadCall(s.waybillID, callID) {
			continue
		}
		observedWrites++
		if _, ok := expectedWrites[callID]; !ok {
			return compatibleOutput{}, fmt.Errorf("unexpected write call %q", callID)
		}
	}
	if observedWrites == 0 {
		return decision, nil
	}
	if observedWrites != len(expectedWrites) {
		return compatibleOutput{}, fmt.Errorf(
			"observed %d write calls, want %d",
			observedWrites,
			len(expectedWrites),
		)
	}
	for callID, expected := range expectedWrites {
		observed, ok := s.callsByID[callID]
		if !ok {
			return compatibleOutput{}, fmt.Errorf("missing write call %q", callID)
		}
		if observed.name != expected.name {
			return compatibleOutput{}, fmt.Errorf(
				"write call %q has name %q, want %q",
				callID,
				observed.name,
				expected.name,
			)
		}
		expectedArguments, err := json.Marshal(expected.arguments)
		if err != nil {
			return compatibleOutput{}, err
		}
		if !sameCompatibleJSON(observed.arguments, expectedArguments) {
			return compatibleOutput{}, fmt.Errorf(
				"write call %q arguments = %s, want %s",
				callID,
				observed.arguments,
				expectedArguments,
			)
		}
		result, ok := s.resultsByCallID[callID]
		if !ok {
			return compatibleOutput{}, fmt.Errorf("write call %q has no result", callID)
		}
		var payload map[string]any
		if err := json.Unmarshal(result, &payload); err != nil {
			return compatibleOutput{}, fmt.Errorf(
				"decode write result %q: %w",
				callID,
				err,
			)
		}
		if errorText := stringValue(payload["error"]); errorText != "" {
			return compatibleOutput{}, fmt.Errorf(
				"write call %q failed: %s",
				callID,
				errorText,
			)
		}
	}
	return compatibleOutput{text: compatibleTerminalText}, nil
}

func isCompatibleReadCall(waybillID domain.WaybillID, callID string) bool {
	for _, name := range []string{
		"tms_get_waybill",
		"tms_get_tracking",
		"tms_get_driver",
		"ext_get_road_weather",
	} {
		if callID == compatibleCallID(waybillID, name) {
			return true
		}
	}
	return false
}

func sameCompatibleJSON(left, right json.RawMessage) bool {
	var compactLeft, compactRight bytes.Buffer
	if json.Compact(&compactLeft, left) != nil ||
		json.Compact(&compactRight, right) != nil {
		return false
	}
	return bytes.Equal(compactLeft.Bytes(), compactRight.Bytes())
}

func compatibleDecisionOutput(
	waybillID domain.WaybillID,
	waybill guardtools.GetWaybillOutput,
	driver guardtools.GetDriverOutput,
	tracking guardtools.GetTrackingOutput,
) (compatibleOutput, error) {
	if len(waybill.CandidateCarriers) == 0 {
		return compatibleOutput{}, fmt.Errorf("waybill %s has no candidate carriers", waybillID)
	}
	selected := waybill.CandidateCarriers[0]
	alternatives := make([]proposal.Alternative, 0, len(waybill.CandidateCarriers))
	for _, carrier := range waybill.CandidateCarriers {
		alternatives = append(alternatives, carrierAlternative(carrier))
	}
	draft := proposal.Draft{
		SchemaVersion: proposal.SchemaVersion,
		Summary:       fmt.Sprintf("已核验运单证据，建议改派%s。", selected.Name),
		ConfidenceBPS: 8500,
		Attribution: []proposal.AttributionDraft{{
			Factor:        "司机连续驾驶时长需要处置",
			ConfidenceBPS: 8400,
			EvidenceRefs: []proposal.EvidenceRef{
				evidenceRef(
					compatibleCallID(waybillID, "tms_get_driver"),
					"/continuous_drive_hours",
					driver.ContinuousDriveHrs,
				),
			},
		}},
		Alternatives: alternatives,
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
	}
	rawProposal, err := json.Marshal(draft)
	if err != nil {
		return compatibleOutput{}, err
	}
	calls := []compatibleToolCall{
		compatibleWriteCall(waybillID, "tms_reassign", guardtools.ReassignInput{
			WaybillID: string(waybillID),
			CarrierID: string(selected.ID),
		}),
	}
	seenClaims := make(map[string]bool)
	for _, point := range tracking.Points {
		if !point.Anomaly ||
			(point.AnomalyType != "damage" && point.AnomalyType != "loss") ||
			seenClaims[point.AnomalyType] {
			continue
		}
		seenClaims[point.AnomalyType] = true
		calls = append(calls, compatibleWriteCall(
			waybillID,
			"tms_create_claim_"+point.AnomalyType,
			guardtools.CreateClaimInput{
				WaybillID: string(waybillID),
				ClaimType: point.AnomalyType,
			},
		))
	}
	calls = append(
		calls,
		compatibleWriteCall(waybillID, "notify_send_sms", guardtools.SendSMSInput{
			WaybillID: string(waybillID),
			Recipient: guardtools.RecipientShipper,
			CarrierID: string(selected.ID),
		}),
		compatibleWriteCall(waybillID, "notify_send_sms_driver", guardtools.SendSMSInput{
			WaybillID: string(waybillID),
			Recipient: guardtools.RecipientDriver,
			CarrierID: string(selected.ID),
		}),
	)
	return compatibleOutput{
		text:  string(rawProposal),
		calls: calls,
	}, nil
}

func compatibleReadCall(
	waybillID domain.WaybillID,
	name string,
	arguments any,
) compatibleOutput {
	return compatibleOutput{calls: []compatibleToolCall{{
		id:        compatibleCallID(waybillID, name),
		name:      name,
		arguments: arguments,
	}}}
}

func compatibleWriteCall(
	waybillID domain.WaybillID,
	idSuffix string,
	arguments any,
) compatibleToolCall {
	name := idSuffix
	if idSuffix == "notify_send_sms_driver" {
		name = "notify_send_sms"
	}
	if strings.HasPrefix(idSuffix, "tms_create_claim_") {
		name = "tms_create_claim"
	}
	return compatibleToolCall{
		id:        compatibleCallID(waybillID, idSuffix),
		name:      name,
		arguments: arguments,
	}
}

func compatibleCallID(waybillID domain.WaybillID, suffix string) string {
	return "call-" + strings.ToLower(string(waybillID)) + "-" + suffix
}

func writeResponsesStream(w http.ResponseWriter, output compatibleOutput) {
	items := make([]any, 0, len(output.calls)+1)
	if output.text != "" {
		items = append(items, map[string]any{
			"id":   "msg-compatible",
			"type": "message",
			"role": "assistant",
			"content": []any{map[string]any{
				"type":        "output_text",
				"text":        output.text,
				"annotations": []any{},
			}},
		})
	}
	for _, call := range output.calls {
		arguments, _ := json.Marshal(call.arguments)
		items = append(items, map[string]any{
			"id":        "fc-" + call.id,
			"type":      "function_call",
			"call_id":   call.id,
			"name":      call.name,
			"arguments": string(arguments),
		})
	}
	event := map[string]any{
		"type":            "response.completed",
		"sequence_number": 0,
		"response": map[string]any{
			"id":     "resp-compatible",
			"object": "response",
			"status": "completed",
			"model":  "compatible-model",
			"output": items,
			"usage": map[string]any{
				"input_tokens":  40,
				"output_tokens": 10,
				"total_tokens":  50,
			},
		},
	}
	raw, _ := json.Marshal(event)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}

func writeChatCompletionsStream(w http.ResponseWriter, output compatibleOutput) {
	delta := map[string]any{"role": "assistant"}
	if output.text != "" {
		delta["content"] = output.text
	}
	if len(output.calls) > 0 {
		calls := make([]any, 0, len(output.calls))
		for index, call := range output.calls {
			arguments, _ := json.Marshal(call.arguments)
			calls = append(calls, map[string]any{
				"index": index,
				"id":    call.id,
				"type":  "function",
				"function": map[string]any{
					"name":      call.name,
					"arguments": string(arguments),
				},
			})
		}
		delta["tool_calls"] = calls
	}
	finishReason := "stop"
	if len(output.calls) > 0 {
		finishReason = "tool_calls"
	}
	chunk := map[string]any{
		"id":      "chat-compatible",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   "compatible-model",
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
	usage := map[string]any{
		"id":      "chat-compatible",
		"object":  "chat.completion.chunk",
		"created": 1,
		"model":   "compatible-model",
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     40,
			"completion_tokens": 10,
			"total_tokens":      50,
		},
	}
	rawChunk, _ := json.Marshal(chunk)
	rawUsage, _ := json.Marshal(usage)
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n", rawChunk, rawUsage)
}

type observedDispatch struct {
	binding platform.EffectBinding
	request platform.EffectRequest
	key     domain.IdempotencyKey
	result  platform.DispatchResult
}

type observedWriteRuntime struct {
	next platform.WriteRuntime

	mu         sync.Mutex
	dispatches []observedDispatch
}

func newObservedWriteRuntime(next platform.WriteRuntime) *observedWriteRuntime {
	return &observedWriteRuntime{next: next}
}

func (r *observedWriteRuntime) AdvertisedActions() []domain.Action {
	return r.next.AdvertisedActions()
}

func (r *observedWriteRuntime) Bind(
	request platform.EffectRequest,
	key domain.IdempotencyKey,
	createdAt time.Time,
) (platform.EffectBinding, error) {
	return r.next.Bind(request, key, createdAt)
}

func (r *observedWriteRuntime) Dispatch(
	ctx context.Context,
	binding platform.EffectBinding,
	request platform.EffectRequest,
	key domain.IdempotencyKey,
) platform.DispatchResult {
	result := r.next.Dispatch(ctx, binding, request, key)
	r.mu.Lock()
	r.dispatches = append(r.dispatches, observedDispatch{
		binding: binding,
		request: platform.EffectRequest{
			Action:        request.Action,
			Arguments:     append(json.RawMessage(nil), request.Arguments...),
			ArgumentsHash: request.ArgumentsHash,
		},
		key: key,
		result: platform.DispatchResult{
			Disposition:       result.Disposition,
			Response:          append(json.RawMessage(nil), result.Response...),
			ExternalRef:       result.ExternalRef,
			ExternalRequestID: result.ExternalRequestID,
			ResponseDigest:    result.ResponseDigest,
			ErrorCode:         result.ErrorCode,
			RetryAfter:        result.RetryAfter,
		},
	})
	r.mu.Unlock()
	return result
}

func (r *observedWriteRuntime) Lookup(
	ctx context.Context,
	binding platform.EffectBinding,
	key domain.IdempotencyKey,
) platform.LookupResult {
	return r.next.Lookup(ctx, binding, key)
}

func (r *observedWriteRuntime) SupportsRecovery(binding platform.EffectBinding) bool {
	return r.next.SupportsRecovery(binding)
}

func (r *observedWriteRuntime) snapshot() []observedDispatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]observedDispatch, len(r.dispatches))
	copy(result, r.dispatches)
	for index := range result {
		result[index].request.Arguments = append(
			json.RawMessage(nil),
			result[index].request.Arguments...,
		)
		result[index].result.Response = append(
			json.RawMessage(nil),
			result[index].result.Response...,
		)
	}
	return result
}
