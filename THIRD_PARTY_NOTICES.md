# THIRD_PARTY_NOTICES.md

本仓库在开发过程中参考 / 引入的第三方项目。**思路借鉴 ≠ 代码复制**：
除特别注明外，本仓库未直接复制下列项目的源代码；所有业务代码均为本项目独立编写。

## 作为依赖引入

- **hastekit/agent-sdk-go**（https://github.com/hastekit/agent-sdk-go）
  - License：Apache-2.0
  - 用途：Go Agent harness —— agent 主循环、function tool / MCP 集成、provider 抽象、
    retry / fallback middleware、human-in-the-loop、流式事件。
  - 引入方式：Go module 依赖，`go.mod` 中钉死 tag / commit。
  - 合规：Apache-2.0 允许商用与修改，保留其 LICENSE 与 NOTICE 即可。

## 思路借鉴（未复制代码）

- **jattiphrswan/logistics-tracker**（https://github.com/jattiphrswan/logistics-tracker）
  - 借鉴点：`backend/simulator.js` 的轨迹模拟思路、`MapView.jsx` 地图组件思路、
    `AlertFeed.jsx` 改造为审计时间线的思路、`PlaybackPanel.jsx` 回放思路。
- **09karankr/port-logistics-intelligence**（https://github.com/09karankr/port-logistics-intelligence）
  - 借鉴点：前端风险评分三段式（ETA 延误 / 拥堵 / 天气）展示思路。
- **dominicfinn/open_tms**（https://github.com/dominicfinn/open_tms）
  - 借鉴点：shipment / operational issue 领域模型与状态设计的思路。

## 登记规则

- 后续若直接复制任一第三方文件（整文件或大段），必须在此按「项目 — 文件路径 — 用途」
  逐条登记，并保留原项目 License 要求的信息。
- 提交前（见根 AGENTS.md 检查清单）复核本文件与实际引入情况一致。
