# cmd/server · AGENTS.md

## 职责

HTTP 和 SSE 服务入口。handler 只做请求校验、协议转换和错误映射。业务流程由
`internal/guardian.Service` 协调。

## 端点设计

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/healthz` | 健康检查，返回 200 |
| POST | `/api/demo/trigger` | 启动杭州到成都演示 run，返回 202 |
| GET | `/api/runs/:id/timeline` | 先回放再推送审计事件 |
| POST | `/api/approvals/:id/confirm` | 记录确认并恢复 run，返回 202 |
| POST | `/api/approvals/:id/reject` | 记录驳回原因并恢复 run，返回 202 |
| GET | `/api/waybills/:id` | 返回运单、轨迹、司机、天气和风险数据 |

## 设计决策

- 时间线是单向事件流，因此使用 SSE。审批决定使用普通 POST。
- SSE 帧的 `id` 等于持久化事件的 `seq`。客户端发送 `Last-Event-ID` 后，服务端回放所有
  `seq > cursor` 的事件，再继续推送 live 事件。
- 服务端每 15 秒发送无 ID 的 heartbeat。慢订阅者会断开，客户端随后按游标恢复。
- handler 不持有审批或审计状态。服务重启时，`guardian.Recover` 从 JSONL 和 hastekit
  file history 恢复可证明安全的状态。
- 请求返回后，已经接受的 run 使用服务生命周期 context 继续执行。浏览器断开不会取消 run。

## 请求约束

- confirm 请求体必须为空。重复 confirm 返回当前决定或执行结果，不会重复调用 platform。
- reject 请求体为 `{"reason":"..."}`，拒绝未知字段，且 `reason` 不能为空。
- 当前 local 模式没有用户认证，审批人固定为 `local-demo-reviewer`。客户端提供的 `X-Actor`
  不参与身份判断。
- 非数字 `Last-Event-ID` 返回 400。游标超过当前末尾返回 409。
- 错误响应统一为 `{"error":{"code":"...","message":"..."}}`。

## 启动配置

`main.go` 读取 `HTTP_ADDR`、`DATA_DIR`、`APPROVAL_TTL`、`DEMO_STEP_DELAY`、`PLATFORM` 和
Agent 模型变量。`HTTP_ADDR` 默认是 `127.0.0.1:8080`，只接受显式 loopback host。
`PLATFORM=real` 在真实 adapter 或生产鉴权未实现时返回启动错误。

## 验证

`http_test.go` 覆盖触发、确认、驳回、错误映射、重复决定、SSE 游标续传和
`PLATFORM=real` fail-fast。
