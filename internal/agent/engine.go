package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Duang777/waybill-guardian/internal/domain"
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

const (
	ModeDemo   = "demo"
	ModeOnline = "online"

	APIStyleResponses       = "responses"
	APIStyleChatCompletions = "chat_completions"
)

const SystemPrompt = `你是物流异常处置专家。先读取运单、轨迹、司机和天气，再形成证据链。
所有写操作必须携带系统可校验的 idempotency_key，并等待人工审批。一次只提出一个审批批次。
如果首个改派方案被驳回，使用第二候选运力提出替代方案。`

type ModelConfig struct {
	Mode     string
	APIStyle string
	BaseURL  string
	APIKey   string
	Model    string
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
}

type Engine struct {
	agent    *agents.Agent
	registry *guardtools.Registry
	history  *history.CommonConversationManager
}

func NewEngine(
	historyDir string,
	registry *guardtools.Registry,
	middlewares []agents.Middleware,
	stepDelay time.Duration,
	modelConfig ModelConfig,
) (*Engine, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry is required")
	}
	mode := strings.ToLower(strings.TrimSpace(modelConfig.Mode))
	if mode == "" {
		mode = ModeDemo
	}
	var onlineModel llm.Provider
	if mode == ModeOnline {
		var err error
		onlineModel, err = newOnlineModel(modelConfig)
		if err != nil {
			return nil, err
		}
	} else if mode != ModeDemo {
		return nil, fmt.Errorf("AGENT_MODE must be demo or online")
	}
	fileHistory, err := openSecureHistory(historyDir)
	if err != nil {
		return nil, fmt.Errorf("open hastekit history: %w", err)
	}
	maxLoops := 12
	options := &agents.AgentOptions{
		Name:        "waybill-guardian",
		Instruction: hastekit.NewPrompt(SystemPrompt),
		History:     fileHistory,
		Tools:       registry.Tools(),
		MaxLoops:    &maxLoops,
	}
	var sdkAgent *agents.Agent
	if mode == ModeOnline {
		options.LLM = onlineModel
		options.Middlewares = append(
			append([]agents.Middleware(nil), middlewares...),
			agentmiddleware.NewRetry(agentmiddleware.RetryConfig{MaxAttempts: 3}),
		)
		sdkAgent = agents.NewAgent(options)
	} else {
		options.Middlewares = middlewares
		sdkAgent = agents.NewAgent(options).WithLLM(NewScenarioModel(registry, stepDelay))
	}
	return &Engine{agent: sdkAgent, registry: registry, history: fileHistory}, nil
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
		Namespace:  Namespace,
		ThreadID:   string(runContext.RunID),
		SessionID:  string(runContext.RunID),
		Message:    history.Message{Messages: []responses.InputMessageUnion{responses.UserMessage("处置演示异常运单")}},
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
	return e.history.Close()
}

func (e *Engine) execute(ctx context.Context, input *agents.AgentInput) (Outcome, error) {
	handle, err := e.agent.Execute(ctx, input)
	if err != nil {
		return Outcome{}, err
	}
	var chunks []string
	for chunk := range handle.Chunks {
		if chunk != nil {
			chunks = append(chunks, chunk.ChunkType())
		}
	}
	result, err := handle.Wait(ctx)
	if err != nil {
		return Outcome{}, err
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
	return outcome, nil
}

func contextMap(value domain.RunContext) map[string]any {
	return map[string]any{
		"run_id":       string(value.RunID),
		"incident_id":  string(value.IncidentID),
		"waybill_id":   string(value.WaybillID),
		"plan_version": value.PlanVersion,
	}
}
