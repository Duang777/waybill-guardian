package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Duang777/waybill-guardian/internal/approval"
	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/idempotency"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
	"github.com/hastekit/agent-sdk-go/pkg/utils"
)

func TestScenarioAgentPausesThenExecutesApprovedWrites(t *testing.T) {
	dataDir := t.TempDir()
	journal, err := audit.Open(dataDir+"/audit", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	approvals, err := approval.NewStore(journal, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	clients, mock, err := guardtools.NewDemoRuntime()
	if err != nil {
		t.Fatal(err)
	}
	writeRuntime := mock
	idempotencyStore, err := idempotency.NewStore(journal, idempotency.StoreConfig{
		Runtime: writeRuntime,
	})
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
	middlewares := []agents.Middleware{
		NewAuditMiddleware(journal),
		NewWriteEffectMiddleware(approvals, idempotencyStore, registry),
	}
	engine, err := NewEngine(dataDir+"/history", registry, middlewares, 0, ModelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	runContext := domain.RunContext{
		RunID:       "run-agent",
		IncidentID:  "incident-agent",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	}
	outcome, err := engine.Start(context.Background(), runContext)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != agentstate.RunStatusPaused {
		t.Fatalf("status = %q, want paused; text=%q", outcome.Status, outcome.Text)
	}
	if len(outcome.Interrupts) != 3 {
		t.Fatalf("interrupts = %d, want 3", len(outcome.Interrupts))
	}
	if mock.WriteCount(domain.ActionReassign) != 0 || mock.WriteCount(domain.ActionSendSMS) != 0 {
		t.Fatal("write tools ran before approval")
	}

	items := make([]approval.Item, 0, len(outcome.Interrupts))
	callIDs := make([]string, 0, len(outcome.Interrupts))
	for _, interrupt := range outcome.Interrupts {
		var rawArguments map[string]json.RawMessage
		if err := json.Unmarshal(interrupt.Arguments, &rawArguments); err != nil {
			t.Fatal(err)
		}
		if rawArguments["effect_id"] != nil || rawArguments["idempotency_key"] != nil {
			t.Fatalf("model-facing arguments contain execution identity: %s", interrupt.Arguments)
		}
		write, err := registry.ParseWrite(interrupt.WireName, interrupt.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		identity, err := idempotency.Derive(idempotency.DerivationInput{
			RunContext: runContext,
			Action:     write.Action,
			Target:     write.Target,
			Arguments:  write.Arguments,
		})
		if err != nil {
			t.Fatal(err)
		}
		callIDs = append(callIDs, interrupt.CallID)
		items = append(items, approval.Item{
			CallID:          interrupt.CallID,
			Action:          write.Action,
			WireName:        write.WireName,
			Params:          write.Arguments,
			ArgumentsHash:   identity.ArgumentsHash,
			IdentityVersion: identity.Version,
			EffectID:        identity.EffectID,
			IdempotencyKey:  identity.Key,
		})
	}
	approvalID := approval.IDFor(runContext.RunID, callIDs)
	if _, err := approvals.Create(context.Background(), approval.Approval{
		ID:        approvalID,
		RunID:     runContext.RunID,
		SDKRunID:  outcome.SDKRunID,
		WaybillID: runContext.WaybillID,
		Items:     items,
		Reason:    "fatigue and extended stop",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := approvals.Decide(context.Background(), approvalID, approval.Decision{
		Kind:      approval.DecisionConfirm,
		DecidedBy: "reviewer",
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := engine.Resume(context.Background(), runContext, outcome.SDKRunID, outcome.Interrupts, true)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Status != agentstate.RunStatusCompleted {
		t.Fatalf("resumed status = %q, text=%q", resumed.Status, resumed.Text)
	}
	if mock.WriteCount(domain.ActionReassign) != 1 || mock.WriteCount(domain.ActionSendSMS) != 2 {
		t.Fatalf("writes = reassign:%d sms:%d", mock.WriteCount(domain.ActionReassign), mock.WriteCount(domain.ActionSendSMS))
	}
	if len(outcome.Chunks) == 0 || len(resumed.Chunks) == 0 {
		t.Fatal("expected streaming lifecycle chunks")
	}
}

func TestScenarioAgentOnlyProposesActiveWrites(t *testing.T) {
	registry := partialRegistry(t)
	engine, err := NewEngine(
		t.TempDir()+"/history",
		registry,
		nil,
		0,
		ModelConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	outcome, err := engine.Start(context.Background(), domain.RunContext{
		RunID:       "run-partial-capability",
		IncidentID:  "incident-partial-capability",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != agentstate.RunStatusPaused {
		t.Fatalf("status = %q, want paused", outcome.Status)
	}
	if len(outcome.Interrupts) != 1 {
		t.Fatalf("interrupts = %+v, want one reassign", outcome.Interrupts)
	}
	if outcome.Interrupts[0].Action != domain.ActionReassign ||
		outcome.Interrupts[0].WireName != "tms_reassign" {
		t.Fatalf("interrupt = %+v, want tms.reassign", outcome.Interrupts[0])
	}
}

func TestOnlineModelRoutesConfiguredAPIStyle(t *testing.T) {
	tests := []struct {
		name     string
		apiStyle string
		path     string
		response string
	}{
		{
			name:     "responses",
			apiStyle: APIStyleResponses,
			path:     "/v1/responses",
			response: `{"id":"resp-1","model":"model-1","output":[],"usage":{}}`,
		},
		{
			name:     "chat completions",
			apiStyle: APIStyleChatCompletions,
			path:     "/v1/chat/completions",
			response: `{"id":"chat-1","model":"model-1","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}],"usage":{}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var path, authorization, model string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				authorization = r.Header.Get("Authorization")
				var body struct {
					Model string `json:"model"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				model = body.Model
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()

			provider, err := newOnlineModel(ModelConfig{
				Mode:     ModeOnline,
				APIStyle: test.apiStyle,
				BaseURL:  server.URL + "/v1",
				APIKey:   "secret",
				Model:    "model-1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.NewResponses(context.Background(), &responses.Request{
				Input: responses.InputUnion{OfString: utils.Ptr("hello")},
			}); err != nil {
				t.Fatal(err)
			}
			if path != test.path {
				t.Fatalf("path = %q, want %q", path, test.path)
			}
			if authorization != "Bearer secret" {
				t.Fatalf("authorization = %q", authorization)
			}
			if model != "model-1" {
				t.Fatalf("model = %q", model)
			}
		})
	}
}

func TestOnlineModelRejectsIncompleteOrEndpointURL(t *testing.T) {
	tests := []ModelConfig{
		{Mode: ModeOnline, APIStyle: APIStyleResponses, BaseURL: "https://example.com/v1", Model: "model"},
		{Mode: ModeOnline, APIStyle: APIStyleResponses, BaseURL: "https://example.com/v1", APIKey: "key"},
		{Mode: ModeOnline, APIStyle: "other", BaseURL: "https://example.com/v1", APIKey: "key", Model: "model"},
		{Mode: ModeOnline, APIStyle: APIStyleResponses, BaseURL: "https://example.com/v1/", APIKey: "key", Model: "model"},
		{Mode: ModeOnline, APIStyle: APIStyleResponses, BaseURL: "https://example.com/v1/responses", APIKey: "key", Model: "model"},
	}
	for _, config := range tests {
		if _, err := newOnlineModel(config); err == nil {
			t.Fatalf("newOnlineModel(%+v) accepted invalid config", config)
		}
	}
}

func TestOnlineEngineRetriesProviderFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"retry"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w,
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"output\":[],\"usage\":{}}}\n\n")
	}))
	defer server.Close()

	dataDir := t.TempDir()
	journal, err := audit.Open(dataDir+"/audit", time.Now)
	if err != nil {
		t.Fatal(err)
	}
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
	engine, err := NewEngine(dataDir+"/history", registry, []agents.Middleware{
		NewAuditMiddleware(journal),
	}, 0, ModelConfig{
		Mode:     ModeOnline,
		APIStyle: APIStyleResponses,
		BaseURL:  server.URL,
		APIKey:   "secret",
		Model:    "model-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	outcome, err := engine.Start(context.Background(), domain.RunContext{
		RunID:       "run-online",
		IncidentID:  "incident-online",
		WaybillID:   "YD2026101001",
		PlanVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Status != agentstate.RunStatusCompleted {
		t.Fatalf("status = %q", outcome.Status)
	}
	if calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", calls.Load())
	}
}

func TestEngineCancellationStopsDetachedRun(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-releaseRequest:
		}
	}))
	defer server.Close()
	defer close(releaseRequest)

	dataDir := t.TempDir()
	journal, err := audit.Open(dataDir+"/audit", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
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
	engine, err := NewEngine(dataDir+"/history", registry, []agents.Middleware{
		NewAuditMiddleware(journal),
	}, 0, ModelConfig{
		Mode:     ModeOnline,
		APIStyle: APIStyleResponses,
		BaseURL:  server.URL,
		APIKey:   "secret",
		Model:    "model-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := engine.Start(ctx, domain.RunContext{
			RunID:       "run-cancel",
			IncidentID:  "incident-cancel",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		})
		done <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start error = %v, want context cancellation", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("detached run did not stop after context cancellation")
	}
}

func TestEngineCloseStopsDetachedRunBeforeClosingHistory(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requestStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-releaseRequest:
		}
	}))
	defer server.Close()
	defer close(releaseRequest)

	dataDir := t.TempDir()
	journal, err := audit.Open(dataDir+"/audit", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
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
	engine, err := NewEngine(dataDir+"/history", registry, []agents.Middleware{
		NewAuditMiddleware(journal),
	}, 0, ModelConfig{
		Mode:     ModeOnline,
		APIStyle: APIStyleResponses,
		BaseURL:  server.URL,
		APIKey:   "secret",
		Model:    "model-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	runDone := make(chan error, 1)
	go func() {
		_, err := engine.Start(context.Background(), domain.RunContext{
			RunID:       "run-close",
			IncidentID:  "incident-close",
			WaybillID:   "YD2026101001",
			PlanVersion: 1,
		})
		runDone <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("model request did not start")
	}

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- engine.Close()
	}()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Engine.Close did not wait for the active run to stop")
	}
	if err := <-runDone; !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Start error = %v, want ErrEngineClosed", err)
	}
}

func TestToolCallTrackerClosesAfterUnderlyingCalls(t *testing.T) {
	tracker := &toolCallTracker{}
	started := make(chan struct{})
	release := make(chan struct{})
	callDone := make(chan struct{})
	tracked := tracker.WrapToolCall(func(
		context.Context,
		*agents.BaseTool,
		*agents.ToolCall,
	) (*agents.ToolCallResponse, error) {
		close(started)
		<-release
		return nil, nil
	})
	go func() {
		defer close(callDone)
		_, _ = tracked(context.Background(), nil, nil)
	}()
	<-started

	waitDone := make(chan struct{})
	go func() {
		tracker.closeAndWait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("tracker returned before the tool call completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-callDone:
	case <-time.After(time.Second):
		t.Fatal("tool call did not complete")
	}
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("tracker did not observe tool completion")
	}

	var lateCalled atomic.Bool
	late := tracker.WrapToolCall(func(
		context.Context,
		*agents.BaseTool,
		*agents.ToolCall,
	) (*agents.ToolCallResponse, error) {
		lateCalled.Store(true)
		return nil, nil
	})
	if _, err := late(context.Background(), nil, nil); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("late tool call error = %v, want ErrEngineClosed", err)
	}
	if lateCalled.Load() {
		t.Fatal("late tool call reached the underlying handler")
	}
}
