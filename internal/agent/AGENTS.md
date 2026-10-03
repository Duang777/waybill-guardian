# internal/agent · AGENTS.md

## 职责

本目录组装 hastekit Agent，注册七个契约工具，连接 middleware，处理 HITL pause/resume，
并配置确定性模型或在线模型。审批、幂等和业务审计由项目代码实现。

## 依赖（钉死版本）

```bash
go get github.com/hastekit/agent-sdk-go@v0.0.24
go mod tidy
go list -m github.com/hastekit/agent-sdk-go
```

当前版本为 `v0.0.24`，对应 commit `df1bdd4560bcff6dbffea29a6e3a263cb8c39d44`。
许可证为 Apache-2.0。该版本可能包含 breaking changes，因此升级必须重新执行后端和浏览器端验收。

## HITL 结论

源码确认 `WithNeedsApproval(true)` 会在工具执行前把 run 保存为 `await_approval`，并返回
`paused`。它不是同步阻塞回调。恢复时，调用方使用相同的 namespace 和 thread，传入
`PreviousRunID` 和 `FunctionCallInterruptResolutionMessage`。

本项目直接使用该 pause/resume 协议。hastekit file history 保存 pending tool calls，
`internal/approval` 把审批请求和决定写入业务 JSONL。`guardian.Recover` 负责在进程启动时
对账两份记录。完整源码证据见
[`docs/research/hastekit-v0.0.24.md`](../../docs/research/hastekit-v0.0.24.md)。

## 工具注册映射

| 契约工具 | 执行策略 |
|---|---|
| `tms.get_waybill` / `tms.get_tracking` / `tms.get_driver` / `ext.get_road_weather` | 自动执行（只读） |
| `tms.reassign` / `tms.create_claim` / `notify.send_sms` | 走 approval 人审闸；middleware 强制校验 `idempotency_key` |

契约名包含点，模型 wire name 使用下划线。每个 hastekit tool 的 metadata 保存原契约名和
读写类型。

## middleware 职责

`guardian.Open` 安装三个项目 middleware：

1. `AuditMiddleware` 记录工具调用和结果，并在写盘前脱敏。
2. `ApprovalGuard` 只允许与 confirmed 审批中 `call_id` 和参数哈希一致的写调用。
3. `IdempotencyMiddleware` 从可信 `RunContext` 重算 key，合并并发调用，并回放首次成功结果。

hastekit 自己根据 `RequiresApproval` 在首次写调用前暂停。项目 middleware 在恢复执行时再次
校验业务审批和幂等约束。

## system prompt

`SystemPrompt` 要求 Agent 先读取四类证据，再提出写操作。每个写操作必须携带
`idempotency_key`。首选运力被拒绝后，Agent 使用第二个候选运力。

## 模型配置

`AGENT_MODE=demo` 使用 `ScenarioModel`。它根据持久化 conversation 中的工具结果决定下一步，
所以重启后不依赖内存 step counter。该模式不需要 API key。

`AGENT_MODE=online` 需要：

- `LLM_BASE_URL`：绝对 HTTP(S) API 根路径，不得以 `/` 结尾，也不得包含具体 endpoint。
- `LLM_API_KEY`：provider 密钥。
- `LLM_MODEL`：模型名。
- `LLM_API_STYLE`：`responses` 或 `chat_completions`，默认 `responses`。

在线模式安装 hastekit model retry，最多尝试三次。当前只配置一个 provider，因此不启用
provider fallback。测试覆盖两个 API style、鉴权 header、模型名、配置校验和 503 重试。
