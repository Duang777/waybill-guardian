package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

func TestModelRequestBudgetBoundsOutputWithoutMutatingRequest(t *testing.T) {
	configuredLimit := 4096
	higherLimit := 9000
	request := &responses.Request{
		Parameters: responses.Parameters{MaxOutputTokens: &higherLimit},
	}
	var received *responses.Request
	wrapped := NewModelRequestBudget(time.Second, configuredLimit).WrapModelCall(
		func(
			_ context.Context,
			_ *agents.ModelCall,
			bounded *responses.Request,
		) (*responses.Response, error) {
			received = bounded
			return &responses.Response{}, nil
		},
	)

	if _, err := wrapped(t.Context(), &agents.ModelCall{}, request); err != nil {
		t.Fatal(err)
	}
	if received == request {
		t.Fatal("middleware passed the caller's request without copying it")
	}
	if received.MaxOutputTokens == nil || *received.MaxOutputTokens != configuredLimit {
		t.Fatalf("max output tokens = %v, want %d", received.MaxOutputTokens, configuredLimit)
	}
	if request.MaxOutputTokens == nil || *request.MaxOutputTokens != higherLimit {
		t.Fatalf("original max output tokens = %v, want %d", request.MaxOutputTokens, higherLimit)
	}
}

func TestModelRequestBudgetKeepsStricterCallerLimit(t *testing.T) {
	callerLimit := 500
	wrapped := NewModelRequestBudget(time.Second, 4096).WrapModelCall(
		func(
			_ context.Context,
			_ *agents.ModelCall,
			request *responses.Request,
		) (*responses.Response, error) {
			if request.MaxOutputTokens == nil || *request.MaxOutputTokens != callerLimit {
				t.Fatalf("max output tokens = %v, want %d", request.MaxOutputTokens, callerLimit)
			}
			return &responses.Response{}, nil
		},
	)

	if _, err := wrapped(
		t.Context(),
		&agents.ModelCall{},
		&responses.Request{
			Parameters: responses.Parameters{MaxOutputTokens: &callerLimit},
		},
	); err != nil {
		t.Fatal(err)
	}
}

func TestModelRequestBudgetCancelsTheWholeNestedCall(t *testing.T) {
	const timeout = 30 * time.Millisecond
	var firstDeadline time.Time
	wrapped := NewModelRequestBudget(timeout, 4096).WrapModelCall(
		func(
			ctx context.Context,
			_ *agents.ModelCall,
			_ *responses.Request,
		) (*responses.Response, error) {
			var ok bool
			firstDeadline, ok = ctx.Deadline()
			if !ok {
				t.Fatal("nested call has no deadline")
			}
			time.Sleep(10 * time.Millisecond)
			secondDeadline, ok := ctx.Deadline()
			if !ok || !secondDeadline.Equal(firstDeadline) {
				t.Fatalf(
					"nested calls did not share a deadline: first=%s second=%s",
					firstDeadline,
					secondDeadline,
				)
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)

	started := time.Now()
	_, err := wrapped(t.Context(), &agents.ModelCall{}, &responses.Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("model call error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("model call took %s after a %s deadline", elapsed, timeout)
	}
}
