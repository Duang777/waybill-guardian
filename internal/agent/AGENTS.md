# internal/agent · AGENTS.md

## 职责

hastekit agent 的组装层：7 个契约工具注册、middleware 链编排、human-in-the-loop 接线、
system prompt。这是本项目"架构师"叙事的核心：**脏活（重试 / fallback / 多模型）用 SDK，
人审 / 幂等 / 审计三件套自己写**。

## 依赖（钉死版本）

```bash
go get github.com/hastekit/agent-sdk-go@v0.0.24
go mod tidy && go list -m github.com/hastekit/agent-sdk-go  # 确认解析到 v0.0.24
```

- 选用理由：Go 原生；function calling + MCP、provider 抽象、retry / provider fallback 中间件、
  streaming / cancellation、structured output、OTel tracing 一应俱全（2026-10-02 核实，Apache-2.0）。
- 风险：项目新（12 stars）、README 声明有 breaking changes → **必须钉死版本**，升级需手动回归。
- 待源码验证：HITL 是否支持持久化 pause/resume；若只是同步回调式批准，则用 `internal/approval`
  的状态机兜底（审批单落盘，agent run 挂起等待，前端确认后恢复）。

## 工具注册映射（contract.yaml → hastekit function tool）

| 契约工具 | 执行策略 |
|---|---|
| `tms.get_waybill` / `tms.get_tracking` / `tms.get_driver` / `ext.get_road_weather` | 自动执行（只读） |
| `tms.reassign` / `tms.create_claim` / `notify.send_sms` | 走 approval 人审闸；middleware 强制校验 `idempotency_key` |

## middleware 链（顺序即执行顺序）

1. `idempotency` — 写操作缺 `idempotency_key` 直接拦截拒绝（防 agent 漏传）
2. `approval` — 写操作挂起生成审批单，等待人工确认后放行
3. `audit` — 所有 tool call / 审批状态变更记为 append-only 事件
4. hastekit 自带 `retry` / `fallback` — 模型调用失败重试、多 provider 切换

## system prompt 设计要点（TODO 细化）

- 角色：物流异常处置专家，先归因后行动，禁止跳过归因直接改派。
- 工具使用纪律：一次只调一个写操作；写操作参数必须复述给用户确认（对应审批卡片）。
- 输出：归因结论（原因 + 证据链）→ 处置方案（改派 / 赔付 / 通知三选一或组合）→ 等待确认。

## 参考方案

- `hastekit/agent-sdk-go` README 的 middleware / HITL / streaming 示例（作为依赖使用，Apache-2.0）。

## TODO

- [ ] 通读 hastekit HITL 源码，确认 pause/resume 语义，结论记在这里
- [ ] system prompt 初版 + 演示剧本联调
- [ ] provider 配置：国内 OpenAI-compatible endpoint 的 base_url 接法验证
