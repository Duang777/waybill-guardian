# internal/approval · AGENTS.md

## 职责

人工审批状态机和事件投影。改派、赔付和通知在执行前都必须有 confirmed 审批。

## 状态机

```
pending → confirmed → executed
   │          ├────→ partially_failed
   │          └────→ failed
   ├────→ rejected
   └────→ expired
```

`rejected` 需要非空原因。`expired` 由系统产生，并按 reject resolution 恢复 Agent。
批次中部分 effect 成功时进入 `partially_failed`，全部失败时进入 `failed`，两者都需要人工补偿。
审批有效期通过 `APPROVAL_TTL` 配置，默认 10 分钟。

## 审批单

一次 hastekit pause 中的所有写调用组成一个不可变审批批次。主要字段包括：

- `id`、`run_id`、`sdk_run_id`、`waybill_id` 和 `plan_version`。
- `items[]`：`call_id`、action、wire name、完整参数、参数哈希和幂等键。
- `reason` 和 `evidence`。
- `status`、`requested_at`、`expires_at`、`decided_by`、`decided_at` 和 `reject_reason`。

审批 ID 从 `run_id` 和排序后的 `call_id` 派生。重复创建同一个批次时，store 返回已有审批。

## 持久化和并发

审批 store 不维护独立文件。它把 `approval_requested`、`approval_decided` 和
`approval_executed` 追加到 `internal/audit` 的 run JSONL，并在启动时重建内存投影。

`guardian.Service` 按 run 串行决定和恢复。相同决定重复提交时返回当前状态，相反决定返回
`ErrDecisionConflict`。确认、驳回和过期定时器竞争同一个 run 锁，因此只会有一种决定落盘。

## 与 hastekit 的关系

hastekit v0.0.24 会在 `RequiresApproval` 工具执行前持久化 pending calls 并暂停 run。
人工决定先写入业务日志，`guardian` 再用同一个 namespace、thread 和 SDK run ID 提交
approve 或 reject resolution。

确认后，`ApprovalGuard` 还会比较 `call_id` 和参数哈希。只有完整匹配审批批次的写调用才能
进入幂等 middleware。所有 effect 成功后，状态才变为 `executed`；否则
`approval_execution_failed` 事件记录每个 effect 的结果，run 进入 `failed`。

## 验证

测试覆盖合法迁移、非法跳转、重复决定、驳回原因、超时、参数绑定、恢复和已成功 effect 对账。
