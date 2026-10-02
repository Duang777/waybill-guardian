# cmd/server · AGENTS.md

## 职责

HTTP + SSE 服务入口。薄层：只做协议转换与事件推送，业务逻辑全部下沉到 `internal/`。

## 端点设计

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/demo/trigger` | 触发演示剧本（杭州→成都延误运单），启动一次 agent run |
| GET | `/api/runs/:id/timeline` | SSE：推送审计事件与审批请求 |
| POST | `/api/approvals/:id/confirm` | 人工确认（改派 / 赔付 / 通知） |
| POST | `/api/approvals/:id/reject` | 人工驳回（需填写原因，记入审计） |
| GET | `/api/waybills/:id` | 运单详情（供前端地图初渲染） |

## 设计决策

- **SSE 而非 WebSocket**：事件流单向（server→web），SSE 足够且断线重连简单；审批确认走普通 POST。
- **无状态**：审批状态在 `internal/approval`，审计在 `internal/audit`；server 重启不丢状态（TODO：approval/audit 落盘后）。
- 演示触发端点与真实异常接入端点分离：初赛先跑剧本，`platform` adapter 就绪后把真实异常 webhook 接到同一 run 启动逻辑。

## hastekit 接线点

- server 持有 `internal/agent` 构建的 agent 实例；`/api/demo/trigger` 创建 run 并返回 run_id。
- agent 的流式事件 → 转写为 `internal/audit` 事件 → SSE 推送。

## 参考方案

- `logistics-tracker` 的 `backend/simulator.js`：用"定时推送模拟位置"的思路做 SSE 事件推送（仅思路，未复制代码）。

## TODO

- [ ] 审批确认接口的幂等（前端重复点击只产生一次 confirm）
- [ ] SSE 断线续传（Last-Event-ID → audit 回放补齐）
