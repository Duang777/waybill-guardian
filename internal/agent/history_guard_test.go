package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestHistoryGuardRejectsProhibitedData(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "field", value: map[string]any{"longitude": 120.1}},
		{name: "mobile number", value: map[string]any{"text": "contact 13800138000"}},
		{name: "license plate", value: map[string]any{"text": "vehicle 浙A12345"}},
		{name: "nested JSON string", value: map[string]any{"arguments": `{"template_id":"delay"}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateHistoryValue("test value", test.value)
			if !errors.Is(err, ErrUnsafeHistory) {
				t.Fatalf("error = %v, want ErrUnsafeHistory", err)
			}
		})
	}
}

func TestHistoryGuardChecksModelRequestAndResponse(t *testing.T) {
	guard := historyGuard{}
	called := false
	wrapped := guard.WrapModelCall(func(
		context.Context,
		*agents.ModelCall,
		*responses.Request,
	) (*responses.Response, error) {
		called = true
		return agents.ModelCallText("driver phone 13800138000"), nil
	})

	_, err := wrapped(
		context.Background(),
		&agents.ModelCall{},
		&responses.Request{Parameters: responses.Parameters{
			ExtraFields: map[string]any{"latitude": 30.2},
		}},
	)
	if !errors.Is(err, ErrUnsafeHistory) {
		t.Fatalf("request error = %v, want ErrUnsafeHistory", err)
	}
	if called {
		t.Fatal("unsafe request reached model")
	}

	_, err = wrapped(context.Background(), &agents.ModelCall{}, &responses.Request{})
	if !errors.Is(err, ErrUnsafeHistory) {
		t.Fatalf("response error = %v, want ErrUnsafeHistory", err)
	}
	if !called {
		t.Fatal("safe request did not reach model")
	}
}

func TestGuardedHistoryPersistenceStampsAndRequiresSchema(t *testing.T) {
	ctx := context.Background()
	inner := history.NewInMemoryConversationPersistence()
	guarded := guardHistoryPersistence(inner)
	meta := map[string]any{"state": "paused"}

	if err := guarded.SaveMessages(
		ctx,
		Namespace,
		history.DefaultGroupID,
		"run-current",
		"",
		"thread-current",
		"conversation-current",
		[]history.Message{{
			ID:       "message-current",
			Messages: []responses.InputMessageUnion{responses.UserMessage("safe")},
		}},
		meta,
	); err != nil {
		t.Fatal(err)
	}
	if _, exists := meta[historySchemaKey]; exists {
		t.Fatal("SaveMessages mutated caller metadata")
	}
	stored, err := inner.LoadMessages(ctx, Namespace, "thread-current", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := stored[0].Meta[historySchemaKey]; got != historySchemaVersion {
		t.Fatalf("stored schema version = %#v, want %d", got, historySchemaVersion)
	}
	if _, err := guarded.LoadMessages(ctx, Namespace, "thread-current", ""); err != nil {
		t.Fatalf("load current history: %v", err)
	}

	if err := inner.SaveMessages(
		ctx,
		Namespace,
		history.DefaultGroupID,
		"run-legacy",
		"",
		"thread-legacy",
		"conversation-legacy",
		nil,
		map[string]any{"state": "paused"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := guarded.LoadMessages(ctx, Namespace, "thread-legacy", ""); !errors.Is(err, ErrUnsafeHistory) {
		t.Fatalf("legacy load error = %v, want ErrUnsafeHistory", err)
	}
}

func TestGuardedHistoryPersistenceRejectsUnsafeWritesAndSummaries(t *testing.T) {
	ctx := context.Background()
	guarded := guardHistoryPersistence(history.NewInMemoryConversationPersistence())
	unsafeMessage := history.Message{
		ID:       "unsafe-message",
		Messages: []responses.InputMessageUnion{responses.UserMessage("call 13800138000")},
	}
	err := guarded.SaveMessages(
		ctx,
		Namespace,
		history.DefaultGroupID,
		"run-unsafe",
		"",
		"thread-unsafe",
		"conversation-unsafe",
		[]history.Message{unsafeMessage},
		nil,
	)
	if !errors.Is(err, ErrUnsafeHistory) {
		t.Fatalf("message error = %v, want ErrUnsafeHistory", err)
	}

	err = guarded.SaveSummary(ctx, Namespace, history.Summary{
		ID:             "summary-unsafe",
		ThreadID:       "thread-unsafe",
		SummaryMessage: unsafeMessage,
	})
	if !errors.Is(err, ErrUnsafeHistory) {
		t.Fatalf("summary error = %v, want ErrUnsafeHistory", err)
	}
}
