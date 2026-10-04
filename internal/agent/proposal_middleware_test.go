package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/proposal"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestProposalBoundaryRepairsOnceAndAuditsMetadata(t *testing.T) {
	store, registry, call := proposalBoundaryFixture(t, "run-boundary")
	var tick atomic.Int64
	boundary, err := NewProposalBoundary(
		store,
		registry,
		InferenceDescriptor{
			Mode:     ModeOnline,
			APIStyle: APIStyleChatCompletions,
			Model:    "model-secret-name",
		},
		func() time.Time {
			base := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
			return base.Add(time.Duration(tick.Add(10)) * time.Millisecond)
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	var calls int
	var repairedRequest *responses.Request
	wrapped := boundary.WrapModelCall(func(
		_ context.Context,
		_ *agents.ModelCall,
		request *responses.Request,
	) (*responses.Response, error) {
		calls++
		if calls == 1 {
			return proposalResponse(t, registry, "not-json"), nil
		}
		repairedRequest = request
		response := proposalResponse(t, registry, validBoundaryProposal(t))
		response.Usage = &responses.Usage{
			InputTokens:  120,
			OutputTokens: 80,
			TotalTokens:  200,
		}
		return response, nil
	})

	response, err := wrapped(
		t.Context(),
		call,
		&responses.Request{
			Input: responses.InputUnion{
				OfInputMessageList: []responses.InputMessageUnion{
					responses.UserMessage("investigate"),
				},
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
	text, err := assistantResponseText(response)
	if err != nil {
		t.Fatal(err)
	}
	if text == "not-json" {
		t.Fatal("boundary returned the invalid first response")
	}
	if repairedRequest == nil {
		t.Fatal("repair request was not captured")
	}
	repairJSON, err := json.Marshal(repairedRequest.Input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(repairJSON), "invalid_json") ||
		!strings.Contains(string(repairJSON), "not-json") {
		t.Fatalf("repair request = %s", repairJSON)
	}
	if strings.Contains(string(repairJSON), "call-write-from-invalid-response") {
		t.Fatalf("repair request retained failed function calls: %s", repairJSON)
	}

	accepted, ok := boundary.Accepted(call.RunID)
	if !ok || accepted.Digest == "" {
		t.Fatalf("accepted proposal = %+v, ok = %v", accepted, ok)
	}
	if _, exists := boundary.Accepted(call.RunID); exists {
		t.Fatal("accepted proposal was not consumed")
	}

	events, err := store.Replay(t.Context(), domain.RunID("run-boundary"), 0)
	if err != nil {
		t.Fatal(err)
	}
	var started, finished int
	var outcomes []string
	for _, event := range events {
		switch event.Type {
		case audit.EventModelCallStarted:
			started++
		case audit.EventModelCallFinished:
			finished++
			var payload modelCallFinished
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			outcomes = append(outcomes, payload.Outcome)
			if strings.Contains(string(event.Payload), "not-json") ||
				strings.Contains(string(event.Payload), "api-key") ||
				strings.Contains(string(event.Payload), "base_url") {
				t.Fatalf("telemetry leaked request or response content: %s", event.Payload)
			}
			if payload.Candidate == modelCandidateRepair {
				if payload.Usage == nil || payload.Usage.TotalTokens != 200 {
					t.Fatalf("repair usage = %+v", payload.Usage)
				}
			}
		}
	}
	if started != 2 || finished != 2 {
		t.Fatalf("model events = started:%d finished:%d", started, finished)
	}
	if strings.Join(outcomes, ",") != "validation_failed,accepted" {
		t.Fatalf("model outcomes = %v", outcomes)
	}
}

func TestAssistantResponseTextJoinsOutputBlocksInOrder(t *testing.T) {
	message := assistantText(`{"schema_`)
	*message.OfOutputMessage.Content = append(
		*message.OfOutputMessage.Content,
		responses.OutputContentUnion{
			OfOutputText: &responses.OutputTextContent{Text: `version":"proposal.v1"}`},
		},
	)

	text, err := assistantResponseText(&responses.Response{
		Output: []responses.OutputMessageUnion{message},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != `{"schema_version":"proposal.v1"}` {
		t.Fatalf("joined text = %q", text)
	}
}

func TestProposalBoundaryRejectsSMSCarrierOutsidePreferredAlternative(t *testing.T) {
	store, registry, _ := proposalBoundaryFixture(t, "run-sms-alignment")
	boundary, err := NewProposalBoundary(
		store,
		registry,
		InferenceDescriptor{Mode: ModeOffline},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	sendSMSWire, ok := registry.ActiveWireName(domain.ActionSendSMS)
	if !ok {
		t.Fatal("send SMS tool is not active")
	}
	response := &responses.Response{
		Output: []responses.OutputMessageUnion{{
			OfFunctionCall: &responses.FunctionCallMessage{
				ID:        "fc-sms",
				CallID:    "call-sms",
				Name:      sendSMSWire,
				Arguments: `{"waybill_id":"YD2026101001","recipient":"shipper","carrier_id":"CARRIER-OTHER"}`,
			},
		}},
	}
	accepted := proposal.Accepted{
		Alternatives: []proposal.Alternative{{
			CarrierID: "CARRIER-SW-42",
			Reason:    "preferred",
		}},
	}

	err = boundary.validateWriteAlignment(response, accepted)
	if err == nil ||
		!strings.Contains(err.Error(), `must match first proposal alternative "CARRIER-SW-42"`) {
		t.Fatalf("validateWriteAlignment error = %v", err)
	}
}

func TestProposalBoundaryStopsAfterOneFailedRepair(t *testing.T) {
	store, registry, call := proposalBoundaryFixture(t, "run-review")
	boundary, err := NewProposalBoundary(
		store,
		registry,
		InferenceDescriptor{Mode: ModeOffline},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	wrapped := boundary.WrapModelCall(func(
		context.Context,
		*agents.ModelCall,
		*responses.Request,
	) (*responses.Response, error) {
		calls++
		return proposalResponse(t, registry, `{"schema_version":"proposal.v1"}`), nil
	})

	_, err = wrapped(t.Context(), call, &responses.Request{})
	if !errors.Is(err, proposal.ErrReviewRequired) {
		t.Fatalf("boundary error = %v, want ErrReviewRequired", err)
	}
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
	if _, ok := boundary.Accepted(call.RunID); ok {
		t.Fatal("invalid proposal was accepted")
	}
}

func TestProposalBoundaryAllowsReadOnlyResponseWithoutProposal(t *testing.T) {
	store, registry, call := proposalBoundaryFixture(t, "run-read-only")
	boundary, err := NewProposalBoundary(
		store,
		registry,
		InferenceDescriptor{Mode: ModeOffline},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	wrapped := boundary.WrapModelCall(func(
		context.Context,
		*agents.ModelCall,
		*responses.Request,
	) (*responses.Response, error) {
		calls++
		return toolCallResponse(
			domain.RunContext{RunID: "run-read-only", PlanVersion: 1},
			"tms_get_driver",
			guardtools.GetDriverInput{DriverID: "DRV-1"},
		), nil
	})
	if _, err := wrapped(t.Context(), call, &responses.Request{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("model calls = %d, want 1", calls)
	}
}

func proposalBoundaryFixture(
	t *testing.T,
	runID domain.RunID,
) (*audit.Store, *guardtools.Registry, *agents.ModelCall) {
	t.Helper()
	store, err := audit.Open(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	clients, _, err := guardtools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := guardtools.NewHandlers(clients)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := guardtools.NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	appendBoundaryToolResult(t, store, runID, "call-waybill", domain.ActionGetWaybill, map[string]any{
		"waybill_id": "YD2026101001",
		"candidate_carriers": []any{
			map[string]any{"carrier_id": "CARRIER-SW-42", "name": "西南速运"},
		},
	})
	appendBoundaryToolResult(t, store, runID, "call-tracking", domain.ActionGetTracking, map[string]any{
		"points": []any{
			map[string]any{"label": "杭州", "anomaly": false},
			map[string]any{"label": "绵阳北服务区", "anomaly": true},
		},
	})
	appendBoundaryToolResult(t, store, runID, "call-driver", domain.ActionGetDriver, map[string]any{
		"driver_id":              "DRV-0286",
		"continuous_drive_hours": 9,
		"fatigue_alert":          true,
	})
	appendBoundaryToolResult(t, store, runID, "call-weather", domain.ActionGetRoadWeather, map[string]any{
		"segments": []any{
			map[string]any{"segment": "绵阳-成都", "condition": "小雨"},
		},
	})
	return store, registry, &agents.ModelCall{
		RunID:         "sdk-" + string(runID),
		LoopIteration: 4,
		RunContext:    map[string]any{"run_id": string(runID)},
	}
}

func appendBoundaryToolResult(
	t *testing.T,
	store *audit.Store,
	runID domain.RunID,
	callID string,
	action domain.Action,
	result any,
) {
	t.Helper()
	if _, err := store.Append(t.Context(), runID, audit.Draft{
		EventID: "tool:" + callID + ":result",
		Actor:   audit.ActorSystem,
		Type:    audit.EventToolResult,
		Payload: map[string]any{
			"call_id": callID,
			"action":  action,
			"result":  result,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func validBoundaryProposal(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(proposal.Draft{
		SchemaVersion: proposal.SchemaVersion,
		Summary:       "司机连续驾驶时间过长，建议改派。",
		ConfidenceBPS: 9000,
		Attribution: []proposal.AttributionDraft{{
			Factor:        "司机疲劳风险",
			ConfidenceBPS: 9200,
			EvidenceRefs: []proposal.EvidenceRef{
				{
					ToolCallID: "call-driver",
					FieldPath:  "/continuous_drive_hours",
					Quoted:     json.RawMessage("9"),
				},
				{
					ToolCallID: "call-driver",
					FieldPath:  "/fatigue_alert",
					Quoted:     json.RawMessage("true"),
				},
			},
		}},
		Alternatives: []proposal.Alternative{{
			CarrierID: "CARRIER-SW-42",
			Reason:    "候选运力中预计时效更优",
		}},
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
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func proposalResponse(
	t *testing.T,
	registry *guardtools.Registry,
	text string,
) *responses.Response {
	t.Helper()
	reassignWire, ok := registry.ActiveWireName(domain.ActionReassign)
	if !ok {
		t.Fatal("reassign tool is not active")
	}
	return &responses.Response{
		Output: []responses.OutputMessageUnion{
			assistantText(text),
			{
				OfFunctionCall: &responses.FunctionCallMessage{
					ID:        "fc-write-from-invalid-response",
					CallID:    "call-write-from-invalid-response",
					Name:      reassignWire,
					Arguments: `{"waybill_id":"YD2026101001","carrier_id":"CARRIER-SW-42"}`,
				},
			},
		},
	}
}
