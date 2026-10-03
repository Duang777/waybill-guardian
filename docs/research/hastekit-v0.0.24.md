# hastekit agent-sdk-go v0.0.24 技术核验

> 核验日期：2026-10-03
> 固定版本：`github.com/hastekit/agent-sdk-go v0.0.24`
> tag commit：`df1bdd4560bcff6dbffea29a6e3a263cb8c39d44`

## 结论摘要

| 问题 | 结论 |
|---|---|
| HITL / tool approval | **不是同步阻塞回调**。工具执行前，run 进入 `await_approval`，保存状态并返回 `paused`；之后用同一 namespace/thread 发起新的 `Execute`，携带 approve/reject resolution 恢复。 |
| 跨进程恢复 | 审批状态可由持久化 history 保存。文件 history 足以支持进程重启后恢复一个已经 paused 的 run；它不能据此视为多进程并发协调方案。正在运行的本地 detached run 不抗进程退出。生产级跨进程执行应使用 Temporal/Restate 和共享 broker。 |
| Tool API | 最小路径是 `hastekit.NewTool` + `AgentConfig.Tools`；SDK从输入类型生成 JSON Schema，并负责参数反序列化与结果序列化。 |
| Streaming | `Agent.Execute` 返回 `*AgentHandle`，事件从 `Chunks` 读取，最终结果用 `Wait`，停止用 `Stop`。事件覆盖文本、tool call 参数、reasoning、run 生命周期、tool progress 等。 |
| OpenAI-compatible | 原生 OpenAI provider 调用 `BaseURL + "/responses"`；只有 `/chat/completions` 的兼容服务应使用 `openaicompat.Client`，其调用 `BaseURL + "/chat/completions"`。默认鉴权是 Bearer API key。 |
| Retry / fallback | 两者都是真实 middleware，但**默认不自动安装**。retry 只重试模型/provider 调用，不重试 tool；fallback 只切 provider/model。流式请求都只允许在首个 chunk 交付前重试或切换。 |
| License / tag | Apache-2.0。`v0.0.24` 是指向上述 commit 的轻量 tag；存在 Git tag，但没有对应 GitHub Release。模块声明 Go `1.25.3`。 |

## 研究边界与一手资料

本结论只依据 v0.0.24 固定 tag 下的官方仓库源码、README、示例、GitHub tag API 和
pkg.go.dev，不依据博客或二手教程。

- 官方 tag：<https://github.com/hastekit/agent-sdk-go/tree/v0.0.24>
- GitHub tag API：<https://api.github.com/repos/hastekit/agent-sdk-go/git/ref/tags/v0.0.24>
- Go API 页面：<https://pkg.go.dev/github.com/hastekit/agent-sdk-go@v0.0.24>
- License：<https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/LICENSE>
- 模块版本：[`go.mod`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/go.mod#L1-L3)

## 1. HITL / tool approval

### 1.1 结论：持久化 pause/resume，不是同步 callback

一个 `RequiresApproval` 工具不会在 `Execute` 内等待某个同步批准函数：

1. 工具描述的 `RequiresApproval` 标志使调用进入待审批集合。
2. 状态机转入 `StepAwaitApproval`，并把 pending tool call 记录为
   `InterruptModeApproval`。
3. agent 先执行 `SaveMessages`，再发布 `run.paused`，最后返回
   `AgentOutput{Status: paused, Interrupts: ...}`。
4. 调用方稍后在同一 namespace/thread 上再次调用 `Execute`，提交
   `FunctionCallInterruptResolutionMessage`。
5. resolution 被转成 approve/reject 队列，状态机回到 `StepExecuteTools`，
   已批准工具才真正执行。

直接证据：

- 状态转移和 approval interrupt 持久化：
  [`RunState.TransitionToAwaitApproval` / `recordApprovalInterrupts`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/agentstate/agent_state.go#L220-L248)
- paused 分支先保存、发事件、再返回：
  [`Agent.ExecuteWithRun`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/agent.go#L1075-L1103)
- resolution 驱动状态回到执行工具：
  [`ConversationRunManager.ProcessIncomingMessage`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/history/conversation_manager.go#L762-L830)
- 官方完整示例先获得 paused result，再在同一 thread 发起第二次 `Execute`：
  [`examples/agents/6_human_in_the_loop/main.go`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/examples/agents/6_human_in_the_loop/main.go#L132-L186)
- resume 消息的数据结构：
  [`FunctionCallInterruptResolutionMessage`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/llm/responses/request.go#L310-L329)

最小恢复形态：

```go
if out.Status == agentstate.RunStatusPaused {
    resolution := responses.InputMessageUnion{
        OfFunctionCallInterruptResolution: &responses.FunctionCallInterruptResolutionMessage{
            ID: uuid.NewString(),
            Resolutions: []responses.InterruptResolution{{
                CallID: out.Interrupts[0].FunctionCallMessage.CallID,
                Action: responses.InterruptActionApprove,
            }},
        },
    }

    handle, err = agent.Execute(ctx, &agents.AgentInput{
        Namespace: namespace,
        ThreadID:  threadID, // 必须回到同一会话
        Message: history.Message{
            Messages: []responses.InputMessageUnion{resolution},
        },
    })
}
```

AG-UI 也采用第二次 POST 的恢复协议：`forwardedProps.command.resume.decisions[]`
携带 `{toolCallId, approved}`。见
[`README: Human-in-the-loop`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/README.md#L705-L711)
和
[`ApprovalsToMessage`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agui/run_input.go#L238-L255)。

### 1.2 能否跨进程恢复

需要区分三个场景。

**已暂停 run + 持久化 history：可以在进程重启后恢复。**

`RunState`（包括 current step、pending calls、interrupts、resolution bookkeeping）
被编码进 history metadata 的 `run_state`；下一次在同一 namespace/thread 上创建 run
时会加载该状态。官方 HITL 示例使用 `OpenFileHistory`，因此 paused 结果返回后，
即使原进程结束，新进程也可以从文件 history 加载状态并提交 resolution。

- `RunState.ToMeta`：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/agentstate/agent_state.go#L337-L399>
- history 保存 `RunState` metadata：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/history/conversation_manager.go#L600-L625>
- 新 run 加载并复用 paused run state：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/history/conversation_manager.go#L125-L205>
- 官方 file history 用法：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/README.md#L1325-L1367>

这里能证明“重启后读取已持久化暂停状态”，但不能证明内置文件 adapter 适合作为多个
服务进程同时写入的协调存储。本项目初赛单进程可用；不要把它描述成生产级分布式锁或队列。

**正在运行的 local detached run：不能跨进程存活。**

README 明确写明 local detached run 只在当前进程生命周期内存活。默认 history 和 broker
也是内存实现。见
[`README: Run and Execute`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/README.md#L110-L134)。

**生产级 fault-tolerant / 跨 worker：使用 durable runtime。**

官方支持 Temporal 和 Restate；跨进程 broker 必须使用 Redis 或其他 shared broker，
显式传入 memory broker 只适合同一进程共享实例。

- durable agent 配置：
  [`README: Durable Agents`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/README.md#L1396-L1435)
- Temporal 示例：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/examples/agents/9_temporal_agent/main.go>
- Restate 示例：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/examples/agents/8_restate_agent/main.go>

### 1.3 对 waybill 的含义

RFC 的“若仅同步回调则自建挂起机制”前提不成立：SDK 已有真正的 pause/resume。
但业务审批单仍应由 `internal/approval` 持有权威状态，因为 SDK interrupt 只表达一次
tool call 的批准或拒绝，不替代项目所需的审批人、过期时间、驳回原因、证据链和审计字段。

建议职责边界：

- hastekit：暂停 agent、保存 pending tool call、接收 resolution、恢复运行。
- `internal/approval`：审批单生命周期、权限、超时默认拒绝、审计。
- 接线层：审批单 `confirmed/rejected` 后生成对应 SDK resolution。

## 2. Tool / function 注册与调用

### 2.1 最小 API

根包提供泛型 `hastekit.NewTool`。输入结构体生成 function JSON Schema；SDK 在
`Execute` 中反序列化模型参数、调用 Go 函数并把输出序列化为 tool result。

- `NewTool`、options：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/function_tool.go#L71-L129>
- 参数和结果转换：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/function_tool.go#L202-L228>
- descriptor / schema / approval：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/function_tool.go#L230-L250>

```go
type ReassignInput struct {
    WaybillID     string `json:"waybill_id"`
    CarrierID     string `json:"carrier_id"`
    IdempotencyKey string `json:"idempotency_key"`
}

type ReassignOutput struct {
    OrderID string `json:"order_id"`
}

reassign := hastekit.NewTool(
    func(ctx context.Context, in ReassignInput) (ReassignOutput, error) {
        return tms.Reassign(ctx, in)
    },
    hastekit.WithName("tms.reassign"),
    hastekit.WithDescription("Reassign a waybill to another carrier"),
    hastekit.WithNeedsApproval(true),
    hastekit.WithIdempotent(true),
)

agent, err := hastekit.NewAgent(&hastekit.AgentConfig{
    Name:        "waybill-guardian",
    Instruction: hastekit.NewPrompt("先归因，后处置。"),
    LLM:         model,
    Tools:       []agents.Tool{reassign},
    History:     fileHistory,
})
```

注册后不直接“调用工具 API”；正常入口是 `agent.Run` 或 `agent.Execute`，由模型生成
function call，agent loop 查找并执行工具：

- `Agent.Run` / `Agent.Execute`：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/execution.go#L21-L65>
- agent tool 查找和执行：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/agent.go#L920-L987>

如需完全手写 schema，可实现 `agents.Tool`：提供 `Execute(context.Context,
*agents.ToolCall)` 和 `GetToolDescriptor() *agents.BaseTool`。官方手写示例见
[`GetUserTool` / `DeleteUserTool`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/examples/agents/6_human_in_the_loop/main.go#L20-L98)。

## 3. Streaming event API

### 3.1 Agent 层

`agent.Execute` 返回：

```go
type AgentHandle struct {
    StreamID string
    Chunks   <-chan *responses.ResponseChunk
}
```

主要操作：

- `for chunk := range handle.Chunks`：消费事件。
- `handle.Wait(ctx)`：等待最终 `AgentOutput`；不依赖消费事件。
- `handle.Stop(ctx)`：协作式停止 run。
- `handle.EnqueueMessage(...)`：向仍在运行的 run 追加消息；已经 paused 并返回的 run
  应在同一 thread 上重新 `Execute`。

源码：

- [`AgentHandle`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/handle.go#L12-L83)
- [`Agent.Execute`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/execution.go#L48-L65)

每个 handle 的待消费缓冲区是 256。消费者落后导致事件丢失时，最终结果仍可获得，
但 `Wait` 同时返回 `ErrStreamOverflow`：

- [`StreamBufferSize` / `ErrStreamOverflow`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/execution.go#L15-L19)
- [`Agent.subscribe`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/handle.go#L85-L112)

### 3.2 事件类型

统一事件是 `responses.ResponseChunk` tagged union，至少覆盖：

- provider 生命周期：`response.created/in_progress/completed`；
- assistant text delta/done；
- function call arguments delta/done；
- reasoning summary / reasoning text；
- agent 生命周期：`run.created/in_progress/paused/failed/completed`；
- function output、tool progress、background task、input message。

源码：

- [`ResponseChunk`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/llm/responses/response_chunks.go#L18-L98)
- [`ResponseChunk.ChunkType`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/llm/responses/response_chunks.go#L524-L613)
- paused event 的 pending interrupts：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/llm/responses/response_chunks.go#L756-L797>

`tool.progress` 是 best-effort 实时旁路，不进入 history，也不在 durable runtime replay；
不能把它作为审计事实源。

### 3.3 HTTP / SSE

根包 `hastekit.NewHTTPHandler(registry)` 把每个 chunk 写成：

```text
event: <chunk.ChunkType()>
data: <chunk JSON>
```

源码：
[`agent_server.go`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/agent_server.go#L20-L76)。
另有 AG-UI SSE handler，支持断线重连、cursor replay 和 HITL interrupt，见
[`README: AG-UI`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/README.md#L681-L711)。

Provider 低层接口也有
`NewStreamingResponses(ctx, *responses.Request) (chan *responses.ResponseChunk, error)`；
通常项目应消费 Agent 层 `handle.Chunks`，因为它额外包含 run/tool 生命周期事件。

## 4. OpenAI-compatible provider 配置

### 4.1 原生 OpenAI Responses API

根包配置结构实际是 gateway 类型的 alias：

```go
client := hastekit.NewLLMClient([]hastekit.ProviderConfig{{
    ProviderName:  hastekit.ProviderOpenAI,
    BaseURL:       "https://example.com/v1",
    CustomHeaders: map[string]string{"X-Tenant": "demo"},
    ApiKeys: []*hastekit.APIKeyConfig{{
        Name:   "primary",
        APIKey: os.Getenv("LLM_API_KEY"),
    }},
}})

model := client.Model("OpenAI/model-name")
```

- 配置字段：
  [`gateway.ProviderConfig` / `APIKeyConfig`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/types.go#L1-L24)
- root aliases：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/llm_client.go#L1-L15>

`ProviderOpenAI` 会构造 OpenAI client；该 client 默认 base URL 是
`https://api.openai.com/v1`，请求地址是 `BaseURL + "/responses"`，鉴权是
`Authorization: Bearer <key>`：

- provider 分派：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/providers.go#L20-L40>
- OpenAI client：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/providers/openai/client.go#L39-L89>

因此 `BaseURL` 必须是 API 根路径，通常应包含 `/v1`，且不要以 `/` 结尾；
不要把 `/responses` 自己写进 `BaseURL`。

### 4.2 只有 `/chat/completions` 的兼容服务

若国内服务仅兼容 Chat Completions，而不实现 OpenAI Responses API，应直接使用：

```go
import "github.com/hastekit/agent-sdk-go/pkg/gateway/providers/openaicompat"

model := openaicompat.NewClient(&openaicompat.ClientOptions{
    BaseURL: "https://example.com/v1",
    ApiKey:  os.Getenv("LLM_API_KEY"),
    Headers: map[string]string{"X-Tenant": "demo"},
})
```

该 client 在内部把 SDK Responses 请求转换成 Chat Completions，请求
`BaseURL + "/chat/completions"`，再转回统一 Responses 结果；默认仍是 Bearer，
也允许用 `Authorize` 自定义鉴权。

- `ClientOptions` 和默认鉴权：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/providers/openaicompat/client.go#L18-L71>
- 非流式和流式调用：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/providers/openaicompat/client.go#L74-L159>

SDK 内置 DeepSeek、Moonshot、ZAI provider 就是这种桥接方式，可通过根
`ProviderConfig.BaseURL` 覆盖 endpoint：

- DeepSeek：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/providers/deepseek/client.go>
- Moonshot：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/providers/moonshot/client.go>
- ZAI：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/providers/zai/client.go>

注意：把一个任意 `openaicompat.Client` 直接作为 `AgentConfig.LLM` 很适合单 provider，
但 config-store 驱动的 provider fallback 依赖 `NewLLMClient` 按 target
重新解析 provider/key。需要多 provider fallback 时，应优先使用 SDK 已注册的
provider 名称，或实现能按 target 路由的自定义 LLM 层。

## 5. Retry / fallback middleware

SDK 同时存在 gateway middleware 和 agent model-call middleware。前者包装 gateway
请求，后者可直接放进 `AgentConfig.Middlewares`，并能向 durable runtime 标记策略已经结束。
两层能力相似，不应同时对同一模型调用安装，否则重试次数会相乘。

### 5.1 Retry 的真实能力

默认参数：

- 最多 3 次尝试，**包含第一次请求**；
- 初始 backoff 500ms，倍数 2，最大 30s；
- full jitter 范围 `[delay/2, delay]`；
- provider 返回明确 `Retry-After` 时直接采用。

默认重试：

- HTTP `408/409/425/429/500/502/503/504`；
- timeout、connection reset/abort、broken pipe、unexpected EOF 等传输错误。

不重试：

- `context.Canceled` / `context.DeadlineExceeded`；
- 其他请求型 4xx。

流式调用只有在首个 chunk 交付前才可重试。首 chunk 发出后已经“提交”，不会拼接一个
新请求的后半段。

源码：

- 默认值和流式边界：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/middleware/retry.go#L18-L95>
- 错误分类：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/middleware/retry.go#L118-L170>
- backoff / `Retry-After`：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/middleware/retry.go#L258-L309>

Agent 版本只包装 model call，并在最终失败上设置 `PolicyFinished`，阻止 Temporal/Restate
把 middleware 已经耗尽的整条链再次重跑：
[`pkg/agents/middleware/retry.go`](https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/middleware/retry.go#L13-L76)。

**它不重试 tool。** tool 失败由 agent loop转成 tool output 反馈给模型。项目写工具的
幂等和业务级重试仍需自己设计。

### 5.2 Fallback 的真实能力

fallback 按配置顺序切换 `Provider/model`。默认除以下情况外都可 fallback：

- context canceled / deadline exceeded；
- HTTP 400、422。

这意味着 401/403、429、5xx、provider 不支持模型等默认都可以切下一个目标。
目标 API key 从 config store 解析；缺 key 的目标被跳过并合并错误。fallback 修改的是
provider/model，不改变 tools，也不执行 tool fallback。

流式调用同样只允许在首个 chunk 前切换。推荐顺序是 fallback 在外、retry 在内，
使每个 provider 都拥有完整 retry budget：

```go
gw.UseMiddleware(
    middleware.NewFallbackModels("Anthropic/model-b", "Gemini/model-c"),
    middleware.NewRetry(middleware.RetryConfig{}),
)
```

源码：

- 顺序、非默认安装、流式边界：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/middleware/fallback.go#L45-L65>
- 默认 fallback 分类：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/middleware/fallback.go#L118-L137>
- target/key 处理：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/middleware/fallback.go#L139-L165>
  和
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/gateway/middleware/fallback.go#L241-L265>
- agent middleware：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/pkg/agents/middleware/fallback.go#L13-L67>

README 和源码明确说明 middleware 是显式配置，client 不会自动安装 fallback。对 waybill
而言，应只在 agent model-call 层安装一次 `fallback(outer) -> retry(inner)`，并把写工具
的幂等约束与模型 retry 分开。

## 6. License、tag 与版本事实

- License：Apache License 2.0。官方文件：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/LICENSE>
- tag：`v0.0.24`。
- commit：`df1bdd4560bcff6dbffea29a6e3a263cb8c39d44`。
- tag 类型：轻量 tag。Git 对 `v0.0.24` 的对象类型解析为 `commit`，而不是 annotated
  tag object。
- GitHub tag API：
  <https://api.github.com/repos/hastekit/agent-sdk-go/git/ref/tags/v0.0.24>
- GitHub Release：`/releases/tags/v0.0.24` API 返回 404，因此应表述为“存在 Git tag，
  无对应 GitHub Release”，不能写成“官方 release 页面发布”。
- pkg.go.dev：
  <https://pkg.go.dev/github.com/hastekit/agent-sdk-go@v0.0.24>
- 上游 `go.mod` 声明 `go 1.25.3`：
  <https://github.com/hastekit/agent-sdk-go/blob/v0.0.24/go.mod#L1-L3>。
  本项目当前声明 `go 1.25`，版本线兼容，但实际构建机仍需能满足 Go toolchain 自动选择
  或直接安装相应 patch toolchain。

## 对当前 RFC 的修订建议

1. 将 “D2. 人审闸为同步阻塞” 改成“工具执行前的持久化暂停闸”。它是同步于副作用的
   pre-execution gate，但不是阻塞 goroutine 等 callback。
2. 删除 “若 SDK 仅同步回调” 这一分支；保留业务审批状态机，但定位为业务权威记录，
   通过 SDK resolution 驱动恢复。
3. 初赛单进程采用 `OpenFileHistory` 即可演示暂停后重启恢复；明确不承诺多副本并发。
4. 决赛或生产化再切 Temporal/Restate + Redis broker，避免自研 durable execution。
5. provider 联调先判断目标 endpoint 支持 `/responses` 还是只有
   `/chat/completions`，据此选择 OpenAI provider 或 `openaicompat`。
6. retry/fallback 只包模型调用一次；写工具继续由项目 middleware 强制
   `idempotency_key`，不要误以为 SDK model retry 能保障业务写幂等。
