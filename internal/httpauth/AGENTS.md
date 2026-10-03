# internal/httpauth · AGENTS.md

## 职责

该包验证 HTTP 身份，并把 JWT claims 转成不可伪造的 `Principal`。它还集中维护租户、角色和
运单范围策略。`guardian`、存储和 platform 不依赖该包。

## 模式

- `local` 忽略请求凭据，生成固定主体 `local-demo-reviewer`。server 必须同时限制 loopback
  listener 和 Host。
- `jwt` 只接受一个 `Authorization: Bearer` header。验证器只接受 RS256，并校验 issuer、
  audience、`iat`、`nbf`、`exp`、subject、tenant、角色和运单范围。

## 授权

- `viewer` 允许读取。
- `dispatcher` 允许启动 run。
- `operator` 允许确认或驳回审批。
- `event_producer` 允许提交事件。该角色要求 1 到 16 个无重复的 `event_sources`，以及 1 到
  8 个无重复的 `event_types`。事件入口同时检查运单范围。
- Principal 的 tenant 必须等于进程配置的 `TENANT_ID`。
- token 必须设置 `waybill_all=true`，或提供非空 `waybill_ids`。两者不能同时使用。
- local Principal 只允许 `urn:waybill-guardian:local-producer` 和 `internal/events` 定义的
  两种事件类型。

`Principal` 的字段保持私有。HTTP middleware 通过 `Authenticate` 把 Principal 写入请求
context，handler 使用 `PrincipalFrom` 读取。JWT Principal 的 `CredentialDeadline` 包含
验证时使用的 `exp + leeway`。审批审计主体只取 `Principal.Subject()`。

## 约束

- 不接受 query token。
- 不把 token 或具体校验错误返回给调用者。
- 未认证返回 `401`。租户、角色或运单权限不足返回 `403`。
- 列表接口必须按 `Grant.Allows` 过滤。
- 事件入口必须同时使用 `Grant.AllowsEvent` 和 `Grant.Allows`。
