# internal/audit · AGENTS.md

## 职责

append-only 审计事件流：记录每次 agent run 的完整轨迹，支持回放。
演示时的"审计时间线"和"事故复盘回放"都从这里取数。

## 事件模型

- `seq`（单调递增）/ `ts` / `run_id` / `actor`（agent / human / system）
- `type`：`tool_call` / `tool_result` / `attribution`（归因结论）/ `approval_requested` /
  `approval_decided` / `write_executed` / `note`
- `payload`：结构化数据（工具名、参数、关键结果摘要；**不记敏感字段**，如司机电话脱敏）
- `prev_hash`：前一事件的哈希，形成哈希链（防篡改叙事，决赛可讲；初赛可先留字段）

## 设计决策

- **只追加**：不提供 update / delete API；错误用补偿事件表达（如 `approval_decided=rejected`
  而不是删掉 `approval_requested`）。
- **存储**：初期 JSONL 文件（`data/audit-<run_id>.jsonl`），读写简单、天然 append-only；
  后续量大了再迁 SQLite，接口不变。
- **回放**：`Replay(run_id) → []Event`，前端时间线 + PlaybackPanel 按 `seq` 顺序播放；
  SSE 断线续传靠 `Last-Event-ID = seq` 补齐（`cmd/server` 配合）。

## 参考方案

- `09karankr/port-logistics-intelligence` 的 append-only audit 表设计**思路**
 （`audit/log.ts` + audit migration 的只追加思想；本仓库独立实现，未复制代码）。

## TODO

- [ ] Event struct 与 JSONL writer/reader
- [ ] 脱敏规则（电话、车牌的掩码函数）
- [ ] 回放 API + 单测（写入 10 事件 → 回放顺序与哈希链校验）
