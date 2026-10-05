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

项目通过 `historyGuard` 检查模型请求、模型响应、持久化消息、metadata 和 summary。guard
拒绝手机号、车牌、精确坐标和短信供应商参数，并为 metadata 写入
`history_schema_version=1`。两个 Engine 构造器固定安装同一个 guard。

`secureHistoryPersistence` 包装 SDK file persistence。history 目录权限为 `0700`，JSONL 和
sidecar 文件权限为 `0600`。每个 conversation 的 sidecar 保存 schema、thread 和终态时间。
启动时删除无当前 sidecar 的旧 history，并只按 `HISTORY_RETENTION` 删除过期终态 history。
PostgreSQL adapter 使用 AES-256-GCM 加密 payload，并通过 `privacy_schema_version=1` 拒绝
未知格式。

`Engine` 跟踪所有活动的 `AgentHandle`，并通过 middleware 单独跟踪实际工具调用。调用方取消
context 或关闭 Engine 时，Engine 先发送 SDK stop，再等待 run 和已启动工具全部结束，最后
关闭 history。即使 SDK 在取消宽限期后放弃等待一个工具，服务也不会提前关闭审计资源。

## 工具注册映射

| 契约工具 | 执行策略 |
|---|---|
| `tms.get_waybill` / `tms.get_tracking` / `tms.get_driver` / `ext.get_road_weather` | 自动执行（只读） |
| `tms.reassign` / `tms.create_claim` / `notify.send_sms` | 走 approval 人审闸；middleware 校验服务端生成的 effect 身份 |

契约名包含点，模型 wire name 使用下划线。每个 hastekit tool 的 metadata 保存原契约名和
读写类型。

## middleware 职责

`guardian.Open` 安装业务 middleware：

1. `AuditMiddleware` 记录工具调用和结果，并在写盘前脱敏。
2. `WriteEffectMiddleware` 严格解析业务参数，校验 confirmed 审批中的 `call_id`、参数哈希和
   `effect_id`，再合并并发调用或回放首次成功结果。middleware 通过 context 把持久化 key
   交给 typed handler。

hastekit 自己根据 `RequiresApproval` 在首次写调用前暂停。项目 middleware 在恢复执行时再次
校验业务审批和幂等约束。Engine 在这些 middleware 外层固定安装 `historyGuard`。

## system prompt

`SystemPrompt` 要求 Agent 先读取四类证据，再提出写操作。模型只提交业务参数，不生成
`effect_id` 或 `idempotency_key`。首选运力被拒绝后，Agent 使用第二个候选运力。

## 模型配置

`AGENT_MODE=offline` 使用 `ScenarioModel`。它根据持久化 conversation 中的工具结果决定下一步，
所以重启后不依赖内存 step counter。该模式不需要 API key。`AGENT_MODE=demo` 是兼容别名。
直接运行 `cmd/server` 默认使用该模式；`scripts/demo.sh`、录像脚本和容器默认使用 `online`。

`AGENT_MODE=online` 需要：

- `LLM_BASE_URL`：绝对 HTTP(S) API 根路径，不得以 `/` 结尾，也不得包含具体 endpoint。
- `LLM_API_KEY`：provider 密钥。
- `LLM_MODEL`：模型名。
- `LLM_API_STYLE`：`responses` 或 `chat_completions`，默认 `responses`。
- `LLM_REQUEST_TIMEOUT`：一次逻辑模型调用的总时限，默认 45 秒，覆盖结构修复和 provider retry。
- `LLM_MAX_OUTPUT_TOKENS`：单次 provider 请求的输出上限，默认 4096，最大 32768。

在线模式安装 hastekit model retry，最多尝试三次。当前只配置一个 provider，因此不启用
provider fallback。`ModelRequestBudget` 位于 `ProposalBoundary` 外层，首次请求、一次结构修复
和各自的 provider retry 共用一个 deadline。提案文本最多 32 KiB，修复请求最多回灌 4 KiB
失败文本。读工具限制文本和数组大小；system prompt 要求模型把工具文本视为不可信业务数据。
测试覆盖两个 API style、鉴权 header、模型名、配置校验、503 重试和上述边界。
协议级 fake provider 还会让三张不同异常运单完成四类证据读取，并停在三个写操作的审批前。
该测试不等同于真实模型服务验收。

经营简报使用独立的 `BriefGenerator` 直接调用同一 provider。它只接收授权范围内的聚合计数，
不创建 Agent、thread 或 session，不加载或保存 history，不注册工具，并固定 `Store=false`。
请求和响应都经过同一隐私校验；三条建议必须引用服务端提供的语义 evidence ID，展示标签和值
由 `internal/guardian` 重建。`BRIEF_TIMEOUT` 默认是 8 秒，每次 provider 尝试分别计时；
失败时总览保留确定性简报并返回稳定的 `fallback_reason`。
