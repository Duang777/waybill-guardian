package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestModelBriefGeneratorUsesStatelessReadOnlyRequest(t *testing.T) {
	model := &briefModelStub{
		responses: []*responses.Response{agents.ModelCallText(validBriefJSON())},
	}
	generator := newModelBriefGenerator(model, time.Second)
	input := sampleBriefInput()

	result, err := generator.Generate(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(result.Items))
	}
	if model.calls != 1 || len(model.requests) != 1 {
		t.Fatalf("model calls = %d, requests = %d", model.calls, len(model.requests))
	}
	request := model.requests[0]
	if len(request.Tools) != 0 {
		t.Fatalf("tools = %d, want 0", len(request.Tools))
	}
	if request.Store == nil || *request.Store {
		t.Fatalf("store = %v, want false", request.Store)
	}
	if request.Input.OfString == nil {
		t.Fatal("brief request input is not a single stateless string")
	}
	for _, forbidden := range []string{"YD2026101001", "ROUTE-", "HUB-", "longitude", "latitude"} {
		if strings.Contains(*request.Input.OfString, forbidden) {
			t.Fatalf("request input contains %q: %s", forbidden, *request.Input.OfString)
		}
	}
}

func TestModelBriefGeneratorRejectsInvalidOrUnsafeOutput(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantReason BriefFailureReason
	}{
		{
			name:       "wrong item count",
			text:       `{"items":[{"headline":"一","body":"二","evidence_ids":["fleet_scope"]}]}`,
			wantReason: BriefFailureSchema,
		},
		{
			name: "unknown evidence",
			text: strings.Replace(
				validBriefJSON(),
				`"anomaly_mix"`,
				`"waybill_identity"`,
				1,
			),
			wantReason: BriefFailureEvidence,
		},
		{
			name: "unsafe prose",
			text: strings.Replace(
				validBriefJSON(),
				`"建议复盘共性"`,
				`"联系 13800138000"`,
				1,
			),
			wantReason: BriefFailureSchema,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := &briefModelStub{
				responses: []*responses.Response{agents.ModelCallText(test.text)},
			}
			generator := newModelBriefGenerator(model, time.Second)
			_, err := generator.Generate(t.Context(), sampleBriefInput())
			if err == nil {
				t.Fatal("Generate accepted invalid output")
			}
			if got := BriefFailureOf(err); got != test.wantReason {
				t.Fatalf("failure reason = %q, want %q", got, test.wantReason)
			}
		})
	}
}

func TestModelBriefGeneratorRetriesProviderErrorsWithFreshAttemptDeadlines(t *testing.T) {
	model := &briefModelStub{
		errors: []error{
			errors.New("temporary one"),
			errors.New("temporary two"),
		},
		responses: []*responses.Response{agents.ModelCallText(validBriefJSON())},
		delays:    []time.Duration{25 * time.Millisecond, 25 * time.Millisecond},
	}
	generator := newModelBriefGenerator(model, 40*time.Millisecond)

	if _, err := generator.Generate(t.Context(), sampleBriefInput()); err != nil {
		t.Fatal(err)
	}
	if model.calls != maxBriefAttempts {
		t.Fatalf("model calls = %d, want %d", model.calls, maxBriefAttempts)
	}
	if len(model.deadlines) != maxBriefAttempts {
		t.Fatalf("deadlines = %d, want %d", len(model.deadlines), maxBriefAttempts)
	}
	for index := 1; index < len(model.deadlines); index++ {
		if !model.deadlines[index].After(model.deadlines[index-1]) {
			t.Fatalf("attempt deadlines did not reset: %v", model.deadlines)
		}
	}
}

func TestModelBriefGeneratorClassifiesAttemptTimeout(t *testing.T) {
	model := &briefModelStub{
		responses: []*responses.Response{agents.ModelCallText(validBriefJSON())},
		delays:    []time.Duration{100 * time.Millisecond},
	}
	generator := newModelBriefGenerator(model, 20*time.Millisecond)

	_, err := generator.Generate(t.Context(), sampleBriefInput())
	if got := BriefFailureOf(err); got != BriefFailureTimeout {
		t.Fatalf("failure reason = %q, want %q", got, BriefFailureTimeout)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
}

func TestNewBriefGeneratorOnlyEnablesOnlineMode(t *testing.T) {
	for _, mode := range []string{"", ModeDemo, ModeOffline} {
		generator, err := NewBriefGenerator(ModelConfig{Mode: mode})
		if err != nil || generator != nil {
			t.Fatalf("%q generator = %#v, error = %v", mode, generator, err)
		}
	}
	if _, err := NewBriefGenerator(ModelConfig{Mode: ModeOnline}); err == nil {
		t.Fatal("online brief generator accepted incomplete provider config")
	}
	if _, err := NewBriefGenerator(ModelConfig{Mode: "invalid"}); err == nil {
		t.Fatal("brief generator accepted invalid mode")
	}
}

type briefModelStub struct {
	calls     int
	errors    []error
	responses []*responses.Response
	requests  []*responses.Request
	deadlines []time.Time
	delays    []time.Duration
}

func (s *briefModelStub) NewResponses(
	ctx context.Context,
	request *responses.Request,
) (*responses.Response, error) {
	s.calls++
	s.requests = append(s.requests, request)
	if deadline, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, deadline)
	}
	if len(s.delays) > 0 {
		delay := s.delays[0]
		s.delays = s.delays[1:]
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if len(s.errors) > 0 {
		err := s.errors[0]
		s.errors = s.errors[1:]
		return nil, err
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func sampleBriefInput() BriefInput {
	return BriefInput{
		TotalWaybills:         200,
		TotalAnomalies:        67,
		TopAnomalyType:        "delay",
		TopAnomalyCount:       14,
		TopAnomalySharePct:    21,
		HottestRouteAnomalies: 3,
		HottestRouteHeatPct:   100,
		HottestRouteMaxRisk:   82,
		BusiestHubAnomalies:   4,
		BusiestHubHandling:    2,
	}
}

func validBriefJSON() string {
	return `{"items":[` +
		`{"headline":"治理高频异常","body":"建议复盘共性","evidence_ids":["anomaly_mix"]},` +
		`{"headline":"聚焦线路风险","body":"建议前置预案","evidence_ids":["route_hotspot"]},` +
		`{"headline":"调配节点资源","body":"建议平衡负荷","evidence_ids":["hub_pressure","fleet_scope"]}` +
		`]}`
}
