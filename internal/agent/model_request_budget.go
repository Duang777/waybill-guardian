package agent

import (
	"context"
	"errors"
	"time"

	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

type ModelRequestBudget struct {
	agents.NoopMiddleware
	timeout         time.Duration
	maxOutputTokens int
}

func NewModelRequestBudget(
	timeout time.Duration,
	maxOutputTokens int,
) *ModelRequestBudget {
	return &ModelRequestBudget{
		timeout:         timeout,
		maxOutputTokens: maxOutputTokens,
	}
}

func (m *ModelRequestBudget) WrapModelCall(
	next agents.ModelCallFunc,
) agents.ModelCallFunc {
	return func(
		ctx context.Context,
		call *agents.ModelCall,
		request *responses.Request,
	) (*responses.Response, error) {
		if request == nil {
			return nil, errors.New("model request is missing")
		}
		requestCtx, cancel := context.WithTimeout(ctx, m.timeout)
		defer cancel()

		bounded := *request
		if bounded.MaxOutputTokens == nil ||
			*bounded.MaxOutputTokens > m.maxOutputTokens {
			maxOutputTokens := m.maxOutputTokens
			bounded.MaxOutputTokens = &maxOutputTokens
		}
		return next(requestCtx, call, &bounded)
	}
}
