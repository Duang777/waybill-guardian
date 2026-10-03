# internal/idempotency · AGENTS.md

## 职责

本目录防止改派、赔付和通知被重复执行。模型调用重试、服务恢复和人工重复点击都不能依赖
调用方自行去重，因此 Agent 的每个写调用都经过 `WriteEffectMiddleware`。

## key 生成和校验

```
proposal_id = UUIDv5(run_id, incident_id, waybill_id, plan_version)
proposal_item_id = SHA256(identity_version, action, target, arguments_hash)
effect_id = UUIDv5(proposal_id, proposal_item_id)
key = SHA256("waybill-effect-key-v1\0" + effect_id)
```

Agent 只提交业务参数。`guardian` 在创建审批前生成并保存 `effect_id` 和 key。middleware
从可信 `RunContext` 和严格解析的业务参数重算身份。handler 只从 execution context 读取 key。

参数绑定使用 canonical JSON 的 SHA-256。相同 key 携带不同 effect、action 或参数哈希时返回
`ErrKeyConflict`。旧审批保留原 key 和完整参数哈希；未完成且共享 key 的旧批次拒绝执行。

## 去重语义

状态如下：

```text
absent -> started -> succeeded
                  |-> failed
                  `-> indeterminate
```

- 首次调用先追加 `write_started`，再调用 platform。
- 相同 key 的并发调用等待第一次执行。成功后，等待者读取首次结果。
- 后续成功重复调用不再进入 platform，并追加 `duplicate_suppressed`。
- 已记录失败的调用允许重试。每次重试使用递增 attempt 和独立事件 ID。
- 启动恢复只看到 `write_started` 而没有结果时，状态为 `indeterminate`。系统不自动重试。

store 不维护独立 JSONL。它从 `internal/audit` 的事件重建 entry。成功结果包含在
`write_executed` 中，可在 hastekit 重放工具调用时直接返回。

## 外部系统边界

本地 `write_started` 与外部平台提交不在同一个事务中。如果外部平台已成功，而
`write_executed` 尚未落盘，系统无法判断结果。真实 adapter 必须支持同一幂等键的安全重试，
或支持按 key 查询结果。否则不能启用真实写模式。

## 验证

测试覆盖身份 golden vector、参数哈希、key 冲突、并发十次只执行一次、旧结果回放、
失败 attempt 和不完整身份拒绝。
