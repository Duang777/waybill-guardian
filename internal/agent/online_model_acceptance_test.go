package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/platform/filestore"
	"github.com/Duang777/waybill-guardian/internal/proposal"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
)

func TestOnlineCompatibleAPIsPauseDelayDamageAndLossWaybills(t *testing.T) {
	loaded, err := filestore.Load("../../data/simulated/waybills-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		waybillID domain.WaybillID
		writes    int
	}{
		{waybillID: "YD2026100001", writes: 3},
		{waybillID: "YD2026100007", writes: 4},
		{waybillID: "YD2026100013", writes: 4},
	}

	for _, apiStyle := range []string{APIStyleResponses, APIStyleChatCompletions} {
		t.Run(apiStyle, func(t *testing.T) {
			fake := newCompatibleModelServer()
			server := httptest.NewServer(fake)
			defer server.Close()

			journal, err := audit.Open(t.TempDir()+"/audit", time.Now)
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			handlers, err := guardtools.NewHandlers(loaded.Reads)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := guardtools.NewRegistry(handlers)
			if err != nil {
				t.Fatal(err)
			}
			engine, err := NewEngine(
				t.TempDir()+"/history",
				registry,
				[]agents.Middleware{
					NewAuditMiddleware(journal),
					NewReadBindingMiddleware(loaded.Reads),
				},
				0,
				ModelConfig{
					Mode:     ModeOnline,
					APIStyle: apiStyle,
					BaseURL:  server.URL + "/v1",
					APIKey:   "test-key",
					Model:    "compatible-model",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()

			for index, test := range cases {
				waybillID := test.waybillID
				runID := domain.RunID(fmt.Sprintf("run-%s-%d", apiStyle, index))
				outcome, err := engine.Start(t.Context(), domain.RunContext{
					RunID:       runID,
					IncidentID:  domain.IncidentID("incident-" + string(waybillID)),
					WaybillID:   waybillID,
					PlanVersion: 1,
				})
				if err != nil {
					t.Fatalf("start %s: %v", waybillID, err)
				}
				if outcome.Status != agentstate.RunStatusPaused {
					t.Fatalf("%s status = %q, want paused", waybillID, outcome.Status)
				}
				if outcome.Proposal == nil || outcome.Proposal.Digest == "" {
					t.Fatalf("%s proposal = %+v", waybillID, outcome.Proposal)
				}
				if len(outcome.Interrupts) != test.writes {
					t.Fatalf(
						"%s interrupts = %d, want %d",
						waybillID,
						len(outcome.Interrupts),
						test.writes,
					)
				}
				for _, interrupt := range outcome.Interrupts {
					var arguments struct {
						WaybillID string `json:"waybill_id"`
					}
					if err := json.Unmarshal(interrupt.Arguments, &arguments); err != nil {
						t.Fatal(err)
					}
					if arguments.WaybillID != string(waybillID) {
						t.Fatalf(
							"%s write %s targets %q",
							waybillID,
							interrupt.WireName,
							arguments.WaybillID,
						)
					}
				}
				assertOnlineAcceptanceAudit(t, journal, runID)
				if requests := fake.requestCount(waybillID); requests != 5 {
					t.Fatalf("%s model requests = %d, want 5", waybillID, requests)
				}
			}
		})
	}
}

func assertOnlineAcceptanceAudit(
	t *testing.T,
	journal audit.Journal,
	runID domain.RunID,
) {
	t.Helper()
	events, err := journal.Replay(t.Context(), runID, 0)
	if err != nil {
		t.Fatal(err)
	}
	readResults := make(map[domain.Action]bool)
	modelCalls := 0
	for _, event := range events {
		switch event.Type {
		case audit.EventToolResult:
			var payload struct {
				Action domain.Action `json:"action"`
				Error  string        `json:"error"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error == "" {
				readResults[payload.Action] = true
			}
		case audit.EventModelCallFinished:
			var payload modelCallFinished
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Mode != ModeOnline || payload.Model != "compatible-model" {
				t.Fatalf("model audit = %+v", payload)
			}
			if payload.Usage == nil || payload.Usage.TotalTokens != 50 {
				t.Fatalf("model usage = %+v", payload.Usage)
			}
			modelCalls++
		}
	}
	for _, action := range []domain.Action{
		domain.ActionGetWaybill,
		domain.ActionGetTracking,
		domain.ActionGetDriver,
		domain.ActionGetRoadWeather,
	} {
		if !readResults[action] {
			t.Fatalf("run %s has no successful %s result", runID, action)
		}
	}
	if modelCalls != 5 {
		t.Fatalf("run %s model audit events = %d, want 5", runID, modelCalls)
	}
}

type compatibleModelServer struct {
	mu       sync.Mutex
	requests map[domain.WaybillID]int
}

func newCompatibleModelServer() *compatibleModelServer {
	return &compatibleModelServer{requests: make(map[domain.WaybillID]int)}
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
	state, err := decodeCompatibleRequest(body)
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
	s.mu.Unlock()

	switch r.URL.Path {
	case "/v1/responses":
		writeResponsesStream(w, output)
	case "/v1/chat/completions":
		writeChatCompletionsStream(w, output)
	default:
		http.NotFound(w, r)
	}
}

func (s *compatibleModelServer) requestCount(waybillID domain.WaybillID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[waybillID]
}

type compatibleRequestState struct {
	waybillID domain.WaybillID
	callIDs   map[string]string
	results   map[string]json.RawMessage
}

func decodeCompatibleRequest(body []byte) (compatibleRequestState, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return compatibleRequestState{}, fmt.Errorf("decode compatible request: %w", err)
	}
	items, ok := request["input"].([]any)
	if !ok {
		items, ok = request["messages"].([]any)
	}
	if !ok {
		return compatibleRequestState{}, fmt.Errorf("compatible request has no messages")
	}
	state := compatibleRequestState{
		callIDs: make(map[string]string),
		results: make(map[string]json.RawMessage),
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
			state.callIDs[stringValue(message["name"])] = stringValue(message["call_id"])
		}
		if calls, ok := message["tool_calls"].([]any); ok {
			for _, rawCall := range calls {
				call, _ := rawCall.(map[string]any)
				function, _ := call["function"].(map[string]any)
				state.callIDs[stringValue(function["name"])] = stringValue(call["id"])
			}
		}
	}
	callNames := make(map[string]string, len(state.callIDs))
	for name, callID := range state.callIDs {
		callNames[callID] = name
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
		name := callNames[callID]
		if name == "" {
			continue
		}
		raw, err := compatibleOutputJSON(output)
		if err != nil {
			return compatibleRequestState{}, err
		}
		state.results[name] = raw
	}
	if state.waybillID == "" {
		return compatibleRequestState{}, fmt.Errorf("user message has no waybill ID")
	}
	return state, nil
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
		return nil, fmt.Errorf("encode compatible tool output: %w", err)
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

func nextCompatibleOutput(state compatibleRequestState) (compatibleOutput, error) {
	if _, ok := state.results["tms_get_waybill"]; !ok {
		return compatibleReadCall(
			state.waybillID,
			"tms_get_waybill",
			guardtools.GetWaybillInput{WaybillID: string(state.waybillID)},
		), nil
	}
	var waybill guardtools.GetWaybillOutput
	if err := json.Unmarshal(state.results["tms_get_waybill"], &waybill); err != nil {
		return compatibleOutput{}, fmt.Errorf("decode waybill result: %w", err)
	}
	if _, ok := state.results["tms_get_tracking"]; !ok {
		return compatibleReadCall(
			state.waybillID,
			"tms_get_tracking",
			guardtools.GetTrackingInput{WaybillID: string(state.waybillID)},
		), nil
	}
	if _, ok := state.results["tms_get_driver"]; !ok {
		return compatibleReadCall(
			state.waybillID,
			"tms_get_driver",
			guardtools.GetDriverInput{DriverID: string(waybill.DriverID)},
		), nil
	}
	if _, ok := state.results["ext_get_road_weather"]; !ok {
		return compatibleReadCall(
			state.waybillID,
			"ext_get_road_weather",
			guardtools.GetRoadWeatherInput{Route: waybill.Origin + "-" + waybill.Destination},
		), nil
	}
	if len(waybill.CandidateCarriers) == 0 {
		return compatibleOutput{}, fmt.Errorf("waybill %s has no candidate carriers", state.waybillID)
	}
	var driver guardtools.GetDriverOutput
	if err := json.Unmarshal(state.results["tms_get_driver"], &driver); err != nil {
		return compatibleOutput{}, fmt.Errorf("decode driver result: %w", err)
	}
	var tracking guardtools.GetTrackingOutput
	if err := json.Unmarshal(state.results["tms_get_tracking"], &tracking); err != nil {
		return compatibleOutput{}, fmt.Errorf("decode tracking result: %w", err)
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
					state.callIDs["tms_get_driver"],
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
		compatibleWriteCall(state.waybillID, "tms_reassign", guardtools.ReassignInput{
			WaybillID: string(state.waybillID),
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
			state.waybillID,
			"tms_create_claim_"+point.AnomalyType,
			guardtools.CreateClaimInput{
				WaybillID: string(state.waybillID),
				ClaimType: point.AnomalyType,
			},
		))
	}
	calls = append(
		calls,
		compatibleWriteCall(state.waybillID, "notify_send_sms", guardtools.SendSMSInput{
			WaybillID: string(state.waybillID),
			Recipient: guardtools.RecipientShipper,
			CarrierID: string(selected.ID),
		}),
		compatibleWriteCall(state.waybillID, "notify_send_sms_driver", guardtools.SendSMSInput{
			WaybillID: string(state.waybillID),
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
