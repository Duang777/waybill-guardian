# internal/approval · AGENTS.md

## 职责

人工确认闸。**所有写操作（改派 / 赔付 / 通知）在真正执行前必须经过这里**，
这是本项目区别于"普通客服 Agent"的核心，也是评委必问 Q2（"怎么防 Agent 改错"）的正面回答。

## 状态机

```
pending → confirmed → executed
   ├────→ rejected（需填写驳回原因）
   └────→ expired（超时未处理，默认拒绝；超时时长可配，演示用 10 分钟）
```

## 审批单字段

- `id` / `run_id`（关联哪次 agent run）/ `waybill_id`
- `action`：reassign / create_claim / send_sms
- `params`：完整写操作参数（含 `idempotency_key`，审批通过后原样放行）
- `reason`：agent 生成的处置理由（展示给审批人）
- `evidence`：归因证据链摘要（轨迹异常点、司机状态、天气结论）
- `status` / `decided_by` / `decided_at` / `reject_reason`

## 设计决策

- **同步阻塞而非事后审计**：写操作先挂起，agent run 等待；确认后才真正调用 platform。
  事后审计拦不住已经发出的改派指令 —— 这句话就是 Q2 的答案。
- **审批单携带完整证据链**：审批人（演示时是评委）在卡片上能看到"为什么这么判"，
  而不是盲点确认。
- **默认拒绝**：超时未处理按拒绝计，避免"没人看就自动执行"的灾难。
- 与 hastekit HITL 的关系：若 SDK 的 HITL 支持持久化 pause/resume，直接对接；
  若只是同步回调，则本状态机为权威（审批单落盘，run 挂起等待，前端确认后恢复）。
  结论待源码验证后记到 `internal/agent/AGENTS.md`。

## TODO

- [ ] 审批单存储（初期内存 + JSONL 落盘，后续 SQLite）
- [ ] 与 agent middleware 的对接：挂起 / 恢复 / 驳回三条路径
- [ ] SSE 推送新审批单到前端（`cmd/server` 配合）
