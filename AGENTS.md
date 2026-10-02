# waybill-guardian · AGENTS.md

> 异常运单自治处置 Agent —— 「AI 重构产业 · 架构师大赛」AI+物流赛道参赛项目。

## 一句话

延误 / 破损 / 丢件发生后，Agent 自动拉取运单、轨迹、司机、天气做归因，
生成改派 / 赔付 / 通知方案；**所有写操作必须经过人工确认闸**，确认后回写 TMS，全程审计可回放。

## 关键时间线

- 报名截止：2026-10-09
- 48h 初赛：2026-10-10 ~ 10-12（提交：demo 视频 + 本仓库）
- TOP9 评审：10-13 ~ 10-16；打磨：10-17 ~ 10-23；决赛：10-24

## 架构

```
web/  (React + 高德 JS API)
 ├─ 异常运单地图 / 轨迹回放
 ├─ Agent 审计时间线
 └─ 人工审批卡片（确认改派 / 驳回赔付）
        │ HTTP + SSE
cmd/server ── internal/agent  (hastekit 接线)
                 ├─ tools:       7 个契约工具（查询自动执行，写操作走审批）
                 ├─ approval:    人审闸 pending → confirmed / rejected
                 ├─ idempotency: 写操作强制 idempotency_key（middleware 拦截缺键调用）
                 ├─ audit:       append-only 事件流，可回放
                 └─ platform:    mock TMS ⟷ 真实平台 adapter（同一 Go interface）
```

## 工具契约（contract.yaml）

读（可自动执行）：`tms.get_waybill` / `tms.get_tracking` / `tms.get_driver` / `ext.get_road_weather`

写（强制人工确认 + 幂等键）：`tms.reassign` / `tms.create_claim` / `notify.send_sms`

## 核心设计决策

| 决策 | 选择 | 依据 |
|---|---|---|
| Agent harness | hastekit/agent-sdk-go，go.mod 钉死版本 | Go 原生；自带 function calling / MCP、provider 抽象、retry / fallback middleware、HITL、流式事件。脏活 80% 现成，自研聚焦人审 / 幂等 / 审计 20% |
| 人审闸 | 同步审批对象 + 状态机（非事后审计） | 物流写操作不可逆（改派涉及真实运力）；评委必问"怎么防 Agent 改错"（Q2），这是核心答辩素材 |
| 幂等 | 写操作强制 `idempotency_key`，middleware 层拦截 | 重试 / fallback 场景下防止重复改派、重复赔付 |
| 审计 | append-only 事件流，可回放 | 支撑"事故复盘"演示；评委可回放异常处置全过程 |
| 数据层 | 先 mock TMS，与真实平台 adapter 实现同一契约 | 赛前无真实接口；切换时 agent 代码零改动 |
| 前端地图 | 高德 JS API | 演示在国内，瓦片稳定；Leaflet / CARTO 可能慢 |

## 目录地图

- `cmd/server` — HTTP + SSE 入口
- `internal/agent` — hastekit 组装：7 工具注册、middleware 链、HITL 接线
- `internal/tools` — 契约工具的 mock 实现（确定性演示数据）
- `internal/approval` — 人审闸状态机
- `internal/audit` — append-only 审计存储与回放
- `internal/idempotency` — 幂等键生成、校验、去重
- `internal/platform` — TMS 接口定义；mock 实现 + 未来真实 adapter
- `web` — 演示前端

## 参考项目（诚实标注，详见 THIRD_PARTY_NOTICES.md）

思路借鉴（本仓库未复制其源代码）：
- `hastekit/agent-sdk-go` — harness 本体，作为 Go module 依赖引入（Apache-2.0）
- `jattiphrswan/logistics-tracker` — 轨迹模拟 / 地图组件 / 告警信息流改审计时间线 / 回放面板的**思路**
- `09karankr/port-logistics-intelligence` — 风险评分三段式展示的**思路**
- `dominicfinn/open_tms` — shipment / operational issue 领域模型的**思路**

## 演示剧本（初赛视频用）

杭州 → 成都运单 YD2026101001：车辆在绵阳段停留超时 → Agent 拉轨迹 / 司机 / 天气 →
归因"司机疲劳驾驶 + 服务区长时间停留" → 生成改派方案（候选运力二选一）→ 人工确认 →
回写 TMS → 短信通知货主 → 审计时间线回放。

## 提交前检查清单

- [ ] 仓库转公开（当前 private）
- [ ] go.mod 中 hastekit 已钉死 tag / commit
- [ ] THIRD_PARTY_NOTICES.md 与实际引入情况一致
- [ ] 无参考项目整仓 / 整文件复制残留
- [ ] demo 剧本可一键跑通
- [ ] README 演示说明与视频一致
