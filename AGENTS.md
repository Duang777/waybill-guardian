# waybill-guardian · AGENTS.md

> 异常运单处置 Agent。「AI 重构产业 · 架构师大赛」AI+物流赛道参赛项目。

## 一句话

延误、破损或丢件发生后，Agent 查询运单、轨迹、司机和天气，形成归因和处置方案。
所有写操作必须经过人工审批。确认后，系统回写 TMS，并记录可校验、可回放的审计事件。

## 关键时间线

- 报名截止：2026-10-09
- 48h 初赛：2026-10-10 ~ 10-12（提交：demo 视频 + 本仓库）
- TOP9 评审：10-13 ~ 10-16；打磨：10-17 ~ 10-23；决赛：10-24

## 架构

```
web/  (React + 高德 JS API 或本地轨迹视图)
 ├─ 异常运单地图和轨迹点
 ├─ Agent 审计时间线与回放
 └─ 人工审批卡片
        │ HTTP + SSE
cmd/server ── internal/guardian
                 ├─ agent:       hastekit Agent loop、pause/resume、模型 retry
                 ├─ tools:       7 个契约工具
                 ├─ approval:    pending → confirmed / rejected / expired
                 ├─ idempotency: 稳定 effect 身份、并发合并、结果回放
                 ├─ audit:       append-only JSONL、哈希链、SSE replay/live
                 └─ platform:    mock clients 和未实现的 real adapter
```

## 工具契约（contract.yaml）

读（可自动执行）：`tms.get_waybill` / `tms.get_tracking` / `tms.get_driver` / `ext.get_road_weather`

写（强制人工确认 + 幂等键）：`tms.reassign` / `tms.create_claim` / `notify.send_sms`

## 核心设计决策

| 决策 | 选择 | 依据 |
|---|---|---|
| Agent runtime | hastekit/agent-sdk-go v0.0.24 | 使用 typed tools、HITL pause/resume 和流式事件。统一 guard 限制模型与 history 数据，终态 history 默认保留 7 天 |
| 人工审批 | SDK pause/resume 加持久化审批投影 | 写操作先暂停，人工决定落盘后才恢复同一个 thread。超时默认拒绝 |
| 幂等 | 服务端从业务参数生成 `effect_id` 和 key | 模型不接触执行身份。同类写操作按目标和参数独立去重 |
| 审计 | 每个 run 一份 append-only JSONL | `seq`、`prev_hash` 和 `hash` 支持完整性校验、回放和 SSE 续传 |
| 数据层 | fixture 驱动的 mock clients | 默认演示可离线重复运行。`PLATFORM=real` 在 adapter 未实现时拒绝启动 |
| 前端地图 | 高德 JS API 加本地降级视图 | 未配置 key 或 SDK 加载失败时，其他演示功能仍可使用 |

## 目录地图

- `cmd/server`：HTTP 和 SSE 入口。
- `internal/guardian`：用例协调、run 锁、超时和启动恢复。
- `internal/agent`：hastekit 组装、七个工具注册、HITL 和模型配置。
- `internal/tools`：契约工具的 typed wrapper 和确定性数据集。
- `internal/approval`：审批状态机和事件投影。
- `internal/audit`：append-only JSONL、哈希校验、回放和订阅。
- `internal/idempotency`：幂等键生成、校验、并发合并和结果回放。
- `internal/platform`：TMS、天气和通知接口，以及 mock 实现。
- `web`：React 运营控制台。

## 参考项目（诚实标注，详见 THIRD_PARTY_NOTICES.md）

思路借鉴，本仓库未复制这些项目的源文件：

- `hastekit/agent-sdk-go`：作为 Go module 依赖引入，许可证为 Apache-2.0。
- `jattiphrswan/logistics-tracker`：轨迹模拟、地图组件、时间线和回放交互。
- `09karankr/port-logistics-intelligence`：风险评分展示和 append-only audit。
- `dominicfinn/open_tms`：shipment 和 operational issue 领域划分。

## 演示剧本（初赛视频用）

杭州到成都运单 `YD2026101001` 在绵阳段停留超时。Agent 查询轨迹、司机和天气后，
归因到疲劳驾驶和服务区长时间停留。Agent 生成改派与通知方案，人工确认后回写 mock TMS，
再通过审计时间线回放全过程。逐字稿见 [`docs/demo-script.md`](./docs/demo-script.md)。

## 提交前检查清单

- [x] 仓库已公开。
- [x] `go.mod` 将 hastekit 固定为 `v0.0.24`。
- [x] `THIRD_PARTY_NOTICES.md` 已登记直接依赖和参考项目。
- [x] 本仓库没有复制参考项目的源文件。
- [x] `./scripts/demo.sh` 可启动完整演示。
- [x] README 和三分钟讲稿使用当前页面流程。
- [x] `npm run record:demo` 可生成带中文字幕的演示录像。
- [ ] 审看最终视频、补充正式配音，并在赛事平台提交视频和公开仓库链接。
