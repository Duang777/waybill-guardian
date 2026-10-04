package agent

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Duang777/waybill-guardian/internal/audit"
	"github.com/Duang777/waybill-guardian/internal/domain"
	"github.com/Duang777/waybill-guardian/internal/proposal"
	guardtools "github.com/Duang777/waybill-guardian/internal/tools"
	"github.com/google/uuid"
	hastekit "github.com/hastekit/agent-sdk-go"
	"github.com/hastekit/agent-sdk-go/pkg/agents"
	"github.com/hastekit/agent-sdk-go/pkg/agents/agentstate"
	"github.com/hastekit/agent-sdk-go/pkg/agents/history"
	agentmiddleware "github.com/hastekit/agent-sdk-go/pkg/agents/middleware"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm"
	"github.com/hastekit/agent-sdk-go/pkg/gateway/llm/responses"
)

const Namespace = "waybill-demo"

var ErrEngineClosed = errors.New("agent engine is closed")

const (
	ModeOffline = "offline"
	ModeDemo    = "demo"
	ModeOnline  = "online"

	APIStyleResponses       = "responses"
	APIStyleChatCompletions = "chat_completions"

	DefaultLLMRequestTimeout  = 45 * time.Second
	DefaultLLMMaxOutputTokens = 4096
	MaxLLMMaxOutputTokens     = 32768
	maxAgentLoops             = 20
)

//go:embed prompts/system.md
var SystemPrompt string

type ModelConfig struct {
	Mode            string
	APIStyle        string
	BaseURL         string
	APIKey          string
	Model           string
	RequestTimeout  time.Duration
	MaxOutputTokens int
}

type Interrupt struct {
	CallID    string
	WireName  string
	Action    domain.Action
	Arguments json.RawMessage
}

type Outcome struct {
	SDKRunID   string
	Status     agentstate.RunStatus
	Interrupts []Interrupt
	Text       string
	Chunks     []string
	Proposal   *proposal.Accepted
}

type Engine struct {
	agent     *agents.Agent
	registry  *guardtools.Registry
	history   *history.CommonConversationManager
	tools     *toolCallTracker
	proposals *ProposalBoundary
	inference InferenceDescriptor

	mu        sync.Mutex
	handles   map[*agents.AgentHandle]struct{}
	closing   bool
	closeDone chan struct{}
	closeErr  error
}

type toolCallTracker struct {
	agents.NoopMiddleware

	mu      sync.Mutex
	wg      sync.WaitGroup
	closing bool
}

func (t *toolCallTracker) WrapToolCall(next agents.ToolCallFunc) agents.ToolCallFunc {
	return func(
		ctx context.Context,
		tool *agents.BaseTool,
		call *agents.ToolCall,
	) (*agents.ToolCallResponse, error) {
		if err := t.begin(); err != nil {
			return nil, err
		}
		defer t.wg.Done()
		return next(ctx, tool, call)
	}
}

func (t *toolCallTracker) begin() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return ErrEngineClosed
	}
	t.wg.Add(1)
	return nil
}

func (t *toolCallTracker) closeAndWait() {
	t.mu.Lock()
	t.closing = true
	t.mu.Unlock()
	t.wg.Wait()
}

func NewEngine(
	historyDir string,
	registry *guardtools.Registry,
	middlewares []agents.Middleware,
	stepDelay time.Duration,
	modelConfig ModelConfig,
	historyPolicies ...HistoryPolicy,
) (*Engine, error) {
	inference, onlineModel, err := prepareModel(modelConfig)
	if err != nil {
		return nil, err
	}
	if registry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	if len(historyPolicies) > 1 {
		return nil, fmt.Errorf("at most one history policy is supported")
	}
	var historyPolicy HistoryPolicy
	if len(historyPolicies) == 1 {
		historyPolicy = historyPolicies[0]
	}
	fileHistory, err := openSecureHistory(historyDir, historyPolicy)
	if err != nil {
		return nil, fmt.Errorf("open hastekit history: %w", err)
	}
	boundary, err := proposalBoundaryFor(middlewares, registry, inference)
	if err != nil {
		_ = fileHistory.Close()
		return nil, err
	}
	if inference.Mode == ModeOnline && boundary == nil {
		_ = fileHistory.Close()
		return nil, fmt.Errorf("online mode requires audit middleware")
	}
	return newEngine(
		fileHistory,
		registry,
		middlewares,
		stepDelay,
		inference,
		onlineModel,
		boundary,
	), nil
}

func NewEngineWithPersistence(
	persistence history.ConversationPersistenceAdapter,
	registry *guardtools.Registry,
	middlewares []agents.Middleware,
	stepDelay time.Duration,
	modelConfig ModelConfig,
) (*Engine, error) {
	if persistence == nil {
		return nil, fmt.Errorf("conversation persistence is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	inference, onlineModel, err := prepareModel(modelConfig)
	if err != nil {
		return nil, err
	}
	manager := history.NewConversationManager(guardHistoryPersistence(persistence))
	boundary, err := proposalBoundaryFor(middlewares, registry, inference)
	if err != nil {
		return nil, err
	}
	if inference.Mode == ModeOnline && boundary == nil {
		_ = manager.Close()
		return nil, fmt.Errorf("online mode requires audit middleware")
	}
	return newEngine(
		manager,
		registry,
		middlewares,
		stepDelay,
		inference,
		onlineModel,
		boundary,
	), nil
}

func prepareModel(modelConfig ModelConfig) (InferenceDescriptor, llm.Provider, error) {
	mode, err := normalizeModelMode(modelConfig.Mode)
	if err != nil {
		return InferenceDescriptor{}, nil, err
	}
	if mode == ModeOnline {
		requestTimeout, maxOutputTokens, limitErr := normalizeModelLimits(modelConfig)
		if limitErr != nil {
			return InferenceDescriptor{}, nil, limitErr
		}
		onlineModel, modelErr := newOnlineModel(modelConfig)
		apiStyle := strings.ToLower(strings.TrimSpace(modelConfig.APIStyle))
		if apiStyle == "" {
			apiStyle = APIStyleResponses
		}
		return InferenceDescriptor{
			Mode:            mode,
			APIStyle:        apiStyle,
			Model:           strings.TrimSpace(modelConfig.Model),
			RequestTimeout:  requestTimeout,
			MaxOutputTokens: maxOutputTokens,
		}, onlineModel, modelErr
	}
	return InferenceDescriptor{Mode: mode}, nil, nil
}

func normalizeModelLimits(config ModelConfig) (time.Duration, int, error) {
	requestTimeout := config.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = DefaultLLMRequestTimeout
	}
	if requestTimeout < 0 {
		return 0, 0, fmt.Errorf("LLM_REQUEST_TIMEOUT must be a positive duration")
	}
	maxOutputTokens := config.MaxOutputTokens
	if maxOutputTokens == 0 {
		maxOutputTokens = DefaultLLMMaxOutputTokens
	}
	if maxOutputTokens < 1 || maxOutputTokens > MaxLLMMaxOutputTokens {
		return 0, 0, fmt.Errorf(
			"LLM_MAX_OUTPUT_TOKENS must be between 1 and %d",
			MaxLLMMaxOutputTokens,
		)
	}
	return requestTimeout, maxOutputTokens, nil
}

func normalizeModelMode(value string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(value)); mode {
	case "", ModeDemo, ModeOffline:
		return ModeOffline, nil
	case ModeOnline:
		return ModeOnline, nil
	default:
		return "", fmt.Errorf("AGENT_MODE must be offline, demo, or online")
	}
}

func newEngine(
	conversationHistory *history.CommonConversationManager,
	registry *guardtools.Registry,
	middlewares []agents.Middleware,
	stepDelay time.Duration,
	inference InferenceDescriptor,
	onlineModel llm.Provider,
	proposalBoundary *ProposalBoundary,
) *Engine {
	toolCalls := &toolCallTracker{}
	middlewares = append([]agents.Middleware{toolCalls}, middlewares...)
	maxLoops := maxAgentLoops
	options := &agents.AgentOptions{
		Name:        "waybill-guardian",
		Instruction: hastekit.NewPrompt(SystemPrompt),
		History:     conversationHistory,
		Tools:       registry.Tools(),
		MaxLoops:    &maxLoops,
	}
	var sdkAgent *agents.Agent
	if inference.Mode == ModeOnline {
		middlewares = append(
			middlewares,
			NewModelRequestBudget(inference.RequestTimeout, inference.MaxOutputTokens),
		)
	}
	if proposalBoundary != nil {
		middlewares = append(middlewares, proposalBoundary)
	}
	if inference.Mode == ModeOnline {
		options.LLM = onlineModel
		middlewares = append(
			middlewares,
			agentmiddleware.NewRetry(agentmiddleware.RetryConfig{MaxAttempts: 3}),
		)
	}
	middlewares = append(middlewares, NewCapabilityModelMiddleware(registry))
	options.Middlewares = protectModelBoundary(middlewares)
	if inference.Mode == ModeOnline {
		sdkAgent = agents.NewAgent(options)
	} else {
		sdkAgent = agents.NewAgent(options).WithLLM(NewScenarioModel(registry, stepDelay))
	}
	return &Engine{
		agent:     sdkAgent,
		registry:  registry,
		history:   conversationHistory,
		tools:     toolCalls,
		proposals: proposalBoundary,
		inference: inference,
		handles:   make(map[*agents.AgentHandle]struct{}),
		closeDone: make(chan struct{}),
	}
}

func proposalBoundaryFor(
	middlewares []agents.Middleware,
	registry *guardtools.Registry,
	inference InferenceDescriptor,
) (*ProposalBoundary, error) {
	for _, middleware := range middlewares {
		source, ok := middleware.(interface{ Journal() audit.Journal })
		if !ok {
			continue
		}
		return NewProposalBoundary(source.Journal(), registry, inference, time.Now)
	}
	return nil, nil
}

func protectModelBoundary(middlewares []agents.Middleware) []agents.Middleware {
	protected := make([]agents.Middleware, 0, len(middlewares)+2)
	protected = append(protected, historyGuard{})
	protected = append(protected, middlewares...)
	return append(protected, historyGuard{})
}

func newOnlineModel(config ModelConfig) (llm.Provider, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	apiKey := strings.TrimSpace(config.APIKey)
	model := strings.TrimSpace(config.Model)
	apiStyle := strings.ToLower(strings.TrimSpace(config.APIStyle))
	if apiStyle == "" {
		apiStyle = APIStyleResponses
	}
	if baseURL == "" || apiKey == "" || model == "" {
		return nil, fmt.Errorf("LLM_BASE_URL, LLM_API_KEY, and LLM_MODEL are required in online mode")
	}
	parsed, err := url.ParseRequestURI(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("LLM_BASE_URL must be an absolute HTTP(S) API root")
	}
	lowerPath := strings.ToLower(parsed.EscapedPath())
	if strings.HasSuffix(baseURL, "/") ||
		strings.HasSuffix(lowerPath, "/responses") ||
		strings.HasSuffix(lowerPath, "/chat/completions") {
		return nil, fmt.Errorf("LLM_BASE_URL must not include a trailing slash or endpoint path")
	}

	provider := hastekit.ProviderOpenAI
	modelPrefix := "OpenAI/"
	switch apiStyle {
	case APIStyleResponses:
	case APIStyleChatCompletions:
		// The SDK's DeepSeek provider is its generic openaicompat bridge and
		// accepts an arbitrary OpenAI-compatible API root.
		provider = hastekit.ProviderDeepSeek
		modelPrefix = "DeepSeek/"
	default:
		return nil, fmt.Errorf("LLM_API_STYLE must be responses or chat_completions")
	}
	client := hastekit.NewLLMClient([]hastekit.ProviderConfig{{
		ProviderName: provider,
		BaseURL:      baseURL,
		ApiKeys: []*hastekit.APIKeyConfig{{
			Name:   "primary",
			APIKey: apiKey,
		}},
	}})
	return client.Model(modelPrefix + model), nil
}

func (e *Engine) Start(ctx context.Context, runContext domain.RunContext) (Outcome, error) {
	return e.execute(ctx, &agents.AgentInput{
		Namespace: Namespace,
		ThreadID:  string(runContext.RunID),
		SessionID: string(runContext.RunID),
		Message: history.Message{Messages: []responses.InputMessageUnion{
			responses.UserMessage(fmt.Sprintf("处置异常运单 %s", runContext.WaybillID)),
		}},
		RunContext: contextMap(runContext),
	})
}

func (e *Engine) Resume(
	ctx context.Context,
	runContext domain.RunContext,
	previousSDKRunID string,
	interrupts []Interrupt,
	approved bool,
) (Outcome, error) {
	action := responses.InterruptActionReject
	if approved {
		action = responses.InterruptActionApprove
	}
	resolutions := make([]responses.InterruptResolution, 0, len(interrupts))
	for _, interrupt := range interrupts {
		resolutions = append(resolutions, responses.InterruptResolution{
			CallID: interrupt.CallID,
			Action: action,
		})
	}
	return e.execute(ctx, &agents.AgentInput{
		Namespace:     Namespace,
		ThreadID:      string(runContext.RunID),
		SessionID:     string(runContext.RunID),
		PreviousRunID: previousSDKRunID,
		Message: history.Message{
			ID: uuid.NewString(),
			Messages: []responses.InputMessageUnion{{
				OfFunctionCallInterruptResolution: &responses.FunctionCallInterruptResolutionMessage{
					ID:          uuid.NewString(),
					Resolutions: resolutions,
				},
			}},
		},
		RunContext: contextMap(runContext),
	})
}

func (e *Engine) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closing {
		done := e.closeDone
		e.mu.Unlock()
		<-done
		return e.closeErr
	}
	e.closing = true
	handles := make([]*agents.AgentHandle, 0, len(e.handles))
	for handle := range e.handles {
		handles = append(handles, handle)
	}
	e.mu.Unlock()

	var closeErrors []error
	for _, handle := range handles {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := handle.Stop(stopCtx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("stop agent run %q: %w", handle.StreamID, err))
		}
		cancel()
	}
	for _, handle := range handles {
		_, _ = handle.Wait()
	}
	e.tools.closeAndWait()
	closeErrors = append(closeErrors, e.history.Close())
	closeErr := errors.Join(closeErrors...)

	e.mu.Lock()
	e.closeErr = closeErr
	close(e.closeDone)
	e.mu.Unlock()
	return closeErr
}

func (e *Engine) execute(ctx context.Context, input *agents.AgentInput) (Outcome, error) {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return Outcome{}, ErrEngineClosed
	}
	handle, err := e.agent.Execute(ctx, input)
	if err != nil {
		e.mu.Unlock()
		return Outcome{}, err
	}
	e.handles[handle] = struct{}{}
	e.mu.Unlock()

	stopDone := make(chan error, 1)
	stopWatch := context.AfterFunc(ctx, func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopDone <- handle.Stop(stopCtx)
	})
	var chunks []string
	for chunk := range handle.Chunks {
		if chunk != nil {
			chunks = append(chunks, chunk.ChunkType())
		}
	}
	var stopErr error
	if !stopWatch() {
		stopErr = <-stopDone
	}
	result, waitErr := handle.Wait()
	e.mu.Lock()
	delete(e.handles, handle)
	closing := e.closing
	e.mu.Unlock()
	if closing {
		return Outcome{}, errors.Join(ErrEngineClosed, stopErr, waitErr)
	}
	if ctx.Err() != nil || stopErr != nil || waitErr != nil {
		return Outcome{}, errors.Join(ctx.Err(), stopErr, waitErr)
	}
	outcome := Outcome{
		SDKRunID: result.RunID,
		Status:   result.Status,
		Text:     result.Text(),
		Chunks:   chunks,
	}
	for _, value := range result.Interrupts {
		definition, ok := e.registry.ByWireName(value.FunctionCallMessage.Name)
		if !ok {
			return Outcome{}, fmt.Errorf("paused on unknown tool %q", value.FunctionCallMessage.Name)
		}
		outcome.Interrupts = append(outcome.Interrupts, Interrupt{
			CallID:    value.FunctionCallMessage.CallID,
			WireName:  value.FunctionCallMessage.Name,
			Action:    definition.Action,
			Arguments: json.RawMessage(value.FunctionCallMessage.Arguments),
		})
	}
	if len(outcome.Interrupts) > 0 && e.proposals != nil {
		accepted, ok := e.proposals.Accepted(result.RunID)
		if !ok {
			return Outcome{}, fmt.Errorf("paused agent run has no accepted proposal")
		}
		outcome.Proposal = &accepted
	}
	return outcome, nil
}

func (e *Engine) Inference() InferenceDescriptor {
	return e.inference
}

func contextMap(value domain.RunContext) map[string]any {
	return map[string]any{
		"run_id":       string(value.RunID),
		"incident_id":  string(value.IncidentID),
		"waybill_id":   string(value.WaybillID),
		"plan_version": value.PlanVersion,
	}
}
