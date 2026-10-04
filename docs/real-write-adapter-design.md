# 真实写入 adapter 与对账设计

## 问题

Issue #40 要接入一个支持幂等查询的外部写 adapter，并证明网络超时、进程退出和多 worker
不会重复产生副作用。当前代码已经具备审批绑定、稳定 effect identity、PostgreSQL effect 状态机、
lease 和 fencing，但仍有四个缺口：

1. `PLATFORM=real` 直接返回 `ErrNotImplemented`。
2. 工具 registry 无条件暴露三个写操作，无法表达部分能力。
3. PostgreSQL 将 `dispatching` 映射为 `started`，Guardian 只主动对账 `unknown`，过期的
   `dispatching` 可能无法恢复。
4. `ExternalRef`、`RetryAfter` 和 provider 证据没有随状态转换持久化。

首个实现选择 operation-scoped 的 `tms.reassign` HTTP sandbox。四个读工具继续使用显式标记的
fixture。`tms.create_claim` 和 `notify.send_sms` 不注册。该 profile 用于验证真实网络写入和
崩溃恢复，不宣称已经接入生产 TMS。

## 调用方用法

### 启动配置

```dotenv
PLATFORM=real
REAL_PLATFORM_PROFILE=tms-reassign-sandbox-v1
REAL_READ_SOURCE=fixture-v1

TMS_SANDBOX_BASE_URL=https://sandbox.example.test
TMS_SANDBOX_TOKEN=secret
TMS_SANDBOX_ACCOUNT=tenant-a
PLATFORM_REQUEST_TIMEOUT=3s
PLATFORM_STARTUP_TIMEOUT=5s

EFFECT_LEASE_TTL=15s
EFFECT_RECONCILE_HORIZON=24h
EFFECT_RECONCILE_POLL_INTERVAL=1s

STORAGE=postgres
AUTH_MODE=jwt
```

真实模式没有隐式 read source，也不回退到 mock。启动日志和审计记录必须明确标记
`fixture-v1` reads 与 `tms-reassign-sandbox-v1` write。

启动顺序：

```text
解析模式与配置
  -> 打开并迁移 PostgreSQL
  -> 构造 hardened HTTP client
  -> 获取并校验 provider capability manifest
  -> 打开 effect repository
  -> 校验所有非终态 effect 的恢复 binding
  -> 构造 capability-filtered tool registry
  -> 执行一次恢复扫描
  -> 开始监听并启动周期对账
```

### 模型可见能力

首个真实 profile 只暴露：

```text
tms.get_waybill
tms.get_tracking
tms.get_driver
ext.get_road_weather
tms.reassign
```

`tms.create_claim` 和 `notify.send_sms` 不出现在模型请求、审批入口或 active registry 中。
构造未知写调用必须在创建 approval 或 effect 前返回 `capability_unavailable`。

### 已审批写入

middleware 仍是唯一写入入口。它完成参数解析、审批授权和 effect identity 校验，然后把
经过校验的 `AuthorizedEffect` 交给 executor。模型和 handler 都不能提供 idempotency key。

```go
write := registry.ParseWrite(call.Name, json.RawMessage(call.Arguments))
authorization := approvals.Authorize(write.AuthorizationRequest(runContext, call.CallID))
identity := authorization.Item.Identity()

effect, err := idempotency.AuthorizeEffect(idempotency.Command{
	RunID:    runContext.RunID,
	CallID:   call.CallID,
	Identity: identity,
}, platform.EffectRequest{
	Action:        write.Action,
	Arguments:     write.Arguments,
	ArgumentsHash: write.ArgumentsHash,
})
result, err := effects.Execute(ctx, effect)
```

`AuthorizeEffect` 是唯一构造入口。它要求 request action 与 `Identity.Action` 相等，request hash
与 `Identity.ArgumentsHash` 相等，并复制 canonical arguments。executor 不接受可独立组合的
`Command + EffectRequest`。

executor 在调用外部系统前持久化 provider binding、request hash、key 窗口和
`dispatching` 状态。adapter 必须逐字节转发服务端生成的 key。

### 崩溃恢复

Guardian 不再读取粗粒度 state 后自行决定是否对账。它对每个非终态 approval item 调用：

```go
outcome, err := effects.Recover(ctx, command)
```

repository 在同一事务中决定：

- `Resolved`：查询或已有记录得到成功结果。
- `Busy`：另一个 worker 持有有效 lease。
- `ReadyToResume`：provider 已权威证明未执行，可用原 key 恢复原审批调用。
- `Pending`：查询尚不权威，等待持久化的 `retry_after`。
- `PermanentFailure`：provider 已证明终态拒绝且无副作用。
- `ManualReview`：key 已过期、证据冲突或恢复 binding 不可用。

`Recover` 不发送 mutation。

## 形态

### 模块图

```text
cmd/server
  +-- real profile config
  +-- platform.Open
  +-- ValidateRecoveryCoverage
  `-- capability-filtered registry
          |
          v
internal/agent.WriteEffectMiddleware
          |
          v
internal/idempotency.Executor
          |
          v
internal/storage/postgres
  +-- effect state and binding
  +-- run/effect lease
  +-- fencing and audit
  `-- repository-owned Recover
          |
          v
internal/platform.Runtime
          |
          v
internal/platform/tmssandbox
  +-- private HTTP DTOs
  +-- manifest validation
  +-- dispatch classification
  `-- lookup classification
```

### 核心类型

```go
type WriteCapability struct {
	Action                   domain.Action
	AdapterID                string
	ContractVersion          string
	Environment              string
	KeyScope                 string
	KeyRetention             time.Duration
	LookupConsistencyWindow  time.Duration
	SameRequestReplays       bool
	MismatchedRequestRejects bool
	LookupByKey              bool
}

type EffectRequest struct {
	Action        domain.Action
	Arguments     json.RawMessage
	ArgumentsHash string
}

type AuthorizedEffect struct {
	command Command
	request platform.EffectRequest
}

func AuthorizeEffect(Command, platform.EffectRequest) (AuthorizedEffect, error)

type EffectBinding struct {
	SchemaVersion           int
	Action                  domain.Action
	AdapterID               string
	ContractVersion         string
	ProviderOperation       string
	ProviderScopeDigest     string
	ProviderRequestHash     string
	KeyCreatedAt            time.Time
	KeyExpiresAt            time.Time
	LookupConsistencyWindow time.Duration
}

type DispatchResult struct {
	Disposition      EffectDisposition
	Response         json.RawMessage
	ExternalRef      string
	ExternalRequestID string
	ResponseDigest   string
	ErrorCode        string
	RetryAfter       time.Duration
}

type LookupDisposition string

const (
	LookupApplied  LookupDisposition = "applied"
	LookupRejected LookupDisposition = "rejected"
	LookupAbsent   LookupDisposition = "authoritative_absent"
	LookupPending  LookupDisposition = "pending"
	LookupConflict LookupDisposition = "conflict"
)

type LookupResult struct {
	Disposition      LookupDisposition
	Response         json.RawMessage
	ExternalRef      string
	ExternalRequestID string
	ResponseDigest   string
	ErrorCode        string
	RetryAfter       time.Duration
}

type WriteRuntime interface {
	AdvertisedActions() []domain.Action
	Bind(EffectRequest, domain.IdempotencyKey, time.Time) (EffectBinding, error)
	Dispatch(context.Context, EffectBinding, EffectRequest, domain.IdempotencyKey) DispatchResult
	Lookup(context.Context, EffectBinding, domain.IdempotencyKey) LookupResult
	SupportsRecovery(EffectBinding) bool
}

type Executor interface {
	Execute(context.Context, AuthorizedEffect) (Result, error)
	Recover(context.Context, Command) (RecoveryOutcome, error)
	Status(Command) (State, bool)
}

type RecoveryDecision string

const (
	RecoveryResolved         RecoveryDecision = "resolved"
	RecoveryBusy             RecoveryDecision = "busy"
	RecoveryReadyToResume    RecoveryDecision = "ready_to_resume"
	RecoveryPending          RecoveryDecision = "pending"
	RecoveryPermanentFailure RecoveryDecision = "permanent_failure"
	RecoveryManualReview     RecoveryDecision = "manual_review"
)

type RecoveryOutcome struct {
	Decision RecoveryDecision
	Result   Result
	State    State
	RetryAt  time.Time
}
```

`DispatchResult` 与 `LookupResult` 必须分型。普通 lookup `404` 不能直接表示 mutation 可重试。
provider wire DTO、HTTP status、token 和原始错误 body 只存在于 `tmssandbox` 包。
`Status` 只用于投影和展示，不能参与恢复控制流。

### 工具 catalog 与 active registry

工具能力拆成三个概念：

- `ContractCatalog` 保存七个冻结的 schema 和 canonical parser，用于历史校验。
- `ExecutionRegistry` 保留恢复历史 pending call 所需的 SDK tool definition。
- `ActiveRegistry` 是新审批的 allowlist，只包含当前 reads 和 advertised writes。

hastekit 的 `AgentOptions.Tools` 使用 execution registry，以便恢复历史 pending call。新增
`CapabilityModelMiddleware.WrapModelCall`，在每次模型请求前复制
`responses.Request.Tools`，只保留 active registry 中的 schema。这样 execution-only tool 不会
暴露给模型。审批创建也只使用 active registry；crafted call 即使能命中 SDK tool definition，
没有既存审批和持久化 binding 仍会在 middleware 中被拒绝。

非终态历史 effect 按持久化的 adapter ID 和 contract version 加载 recovery binding。该 binding
只用于恢复，不重新进入 active registry。缺失精确恢复 binding 时，启动 fail closed。

middleware 直接调用 executor。写 handler 只保留 SDK tool 的类型入口，绕过 middleware 时返回
`write middleware required`，不产生外部调用。

### 首个 provider contract

```text
GET  /.well-known/waybill-capabilities
POST /v1/reassignments
GET  /v1/effects/{url-escaped-idempotency-key}
```

mutation headers：

```text
Authorization: Bearer <token>
Idempotency-Key: <原始 key>
Waybill-Request-SHA256: <provider request hash>
```

manifest 必须证明：

- provider、environment、contract version 与本地 profile 一致；
- `tms.reassign` 支持同 key 同请求回放；
- 同 key 异请求返回可识别冲突；
- key scope 至少是 tenant + operation；
- 支持按原 key 权威查询；
- key retention 大于 reconciliation horizon、consistency window 和 request timeout 的总预算；
- lookup consistency window 不超过本地允许上限。

远端声明只能验证本地已编译能力，不能动态开启未知 operation。

### HTTP 分类

mutation：

| 观察 | 结果 |
|---|---|
| 本地校验失败，尚未创建请求 | permanent 或 retryable 的本地错误 |
| DNS、dial、TLS 失败，且能证明 request 未发送 | `retryable_failed` |
| timeout、reset、EOF、取消，且 request 可能已发送 | `unknown` |
| 合法 `2xx`，operation/hash/receipt 一致 | `succeeded` |
| 不可解析或被截断的 `2xx` | `unknown` |
| 同 key 异 request hash | `permanent_failed`，进入人工处理 |
| provider 明确保证未提交的业务拒绝 | `permanent_failed` |
| `408`、`429`、任意 `5xx` 或未知状态 | 默认 `unknown` |

lookup：

| 观察 | 结果 |
|---|---|
| matching applied record | `LookupApplied` |
| provider 证明终态拒绝且无副作用 | `LookupRejected`，映射 `permanent_failed` |
| consistency window 内未找到 | `LookupPending` |
| window 后权威证明未执行，且 key 未过期 | `LookupAbsent` |
| operation/hash/external ref 冲突 | `LookupConflict`，映射 `manual_review` |
| key 过期后仍未找到 | repository 映射 `manual_review` |
| timeout、`429`、`5xx`、无效响应 | `LookupPending` |

状态机不读取 `ErrorCode` 决定转移，只使用穷尽的 lookup disposition 与持久化 key 窗口。

mutation 使用独立的 single-attempt HTTP path。Go `http.Transport` 会把带
`Idempotency-Key` 且 body 可重放的 POST 视为幂等请求并在复用连接失败时自动重试，因此 mutation
request 必须使用不可重放 body，显式令 `GetBody=nil`，并由 adapter 的 RoundTripper 拒绝任何
可重放 mutation request。lookup GET 可以使用普通 transport 重试策略。contract test 必须在
复用连接上模拟 provider 提交后断连，断言只出现一次 POST，后续请求只有 GET。

effect state machine 是 mutation retry 的唯一决策者。

### 持久化与迁移

新增 migration，复用现有 `external_ref` 与 `retry_after`，并增加：

```text
binding_schema_version
adapter_id
provider_contract_version
provider_operation
provider_scope_digest
provider_request_hash
key_created_at
key_expires_at
dispatch_started_at
last_lookup_at
external_request_id
response_digest
last_error_class
```

`binding_schema_version=1` 时 binding 字段全非空；version 0 时全空。已终态 version-0 行保持可读。
真实模式遇到非终态 version-0 行时拒绝启动。

`retry_after` 是下次查询时间的唯一数据库事实源。所有 provider metadata、effect 状态和审计事件
在同一事务提交。token、provider account、原始响应 body 和个人信息不进入 effect 或审计。

### 状态与 lease

```text
prepared -> dispatching -> succeeded
                       |-> retryable_failed
                       |-> permanent_failed
                       `-> unknown

expired dispatching/reconciling
unknown when retry_after is due
  -> reconciling -> succeeded
                 |-> retryable_failed
                 |-> unknown
                 `-> manual_review

retryable_failed -> dispatching with same key and same request
```

每次 dispatch 和 lookup claim 都按 `EFFECT_LEASE_TTL/3` 续租。HTTP timeout 必须短于剩余 lease
预算。续租失败时取消外部 context，但取消不能证明 provider 未执行，因此结果仍是 `unknown`。

外部 context 已取消后，worker 使用 `context.WithoutCancel` 派生短数据库 deadline，尝试持久化
`unknown`。提交仍校验当前 fencing token，旧 worker 不能覆盖新 owner 的结果。

周期对账只扫描到期 effect，不在事务或 lease 下 sleep。它不负责 UI、告警、容量门禁或通用任务
队列。

## 验证

### Adapter contract tests

使用严格 `httptest.Server` 覆盖：

- capability manifest 的 provider、version、scope、retention、lookup 和 consistency 校验；
- 原 key 逐字节转发；
- 同 key 同请求回放、同 key 异请求冲突；
- provider commit 后 client timeout，后续只发 GET；
- reset、EOF、截断 `2xx`、重复响应、`429`、`5xx` 和无效 JSON；
- 复用连接在 provider commit 后断开时，Go transport 不自动重发 POST；
- consistency window 前后的 not-found 语义；
- redirect 禁用、HTTPS 默认要求、测试仅允许 loopback HTTP；
- error 与审计不含 token、account 或原始 provider body。

### PostgreSQL integration tests

- binding 和 provider metadata 在网络调用前持久化；
- 过期 `dispatching` 与 `reconciling` 只返回 lookup work；
- live lease 返回 busy；
- 慢 HTTP 期间续租，第二 worker 不能 claim；
- 两个 repository 竞争同一 stale effect 时只有一个 fence 可提交；
- 旧 worker completion 被拒绝；
- unknown 不能 mutation，权威 absent 后才能以原 key 重试；
- key 到期进入 manual review；
- `ExternalRef`、绝对 `retry_after`、request ID 和 response digest 可在重开 repository 后读取；
- advertised actions 与 recovery bindings 分离；
- execution registry 保留历史工具，但模型 request 只包含 active schemas；
- 缺失历史 adapter version 时启动失败。

### 进程故障测试

父测试持有 PostgreSQL 和 HTTP sandbox，启动真实 server 子进程，在确定 barrier 处 `SIGKILL`：

1. `dispatching` 已提交但 HTTP 尚未发送；
2. provider 已提交但响应尚未返回；
3. adapter 已收到成功但本地 completion 尚未提交；
4. lookup 已成功但本地 completion 尚未提交；
5. 两个实例同时恢复同一 stale effect。

每个用例同时断言 POST attempts、provider accepted mutations、GET 次数、effect 状态、attempt、
reconciliation count、approval 状态和审计链。provider accepted mutation 最终必须为 1。

## 综合决定

候选 2 是基线。它选择 `tms.reassign`，避免把短信联系人、模板和加密快照并入 Issue #40；它还用
`DispatchResult` 与 `LookupResult` 的类型差异约束查询语义，并把 stale 状态判断收进
repository-owned `Recover`。

从候选 1 合并：

- model-visible actions 与 recovery-only bindings 分离；
- 外部 context 取消后使用短独立 deadline 记录 `unknown`；
- 恢复只使用持久化 binding、request hash 和原 key，禁止按当前默认 provider 重算。

从候选 3 合并：

- `binding_schema_version` 兼容迁移和 all-or-none 约束；
- 复用 `external_ref` 与 `retry_after`；
- adapter contract 默认使用 `httptest.Server`，只有进程终止场景启动子进程；
- HTTPS、redirect 和 base URL 加固。

未采用候选 1 的 notification sandbox、`ContactResolver`、模板治理或跨 approval/effect 复制
`PreparedEffect`。未采用候选 3 的 callback executor 和重复
`DispatchWrite -> Runtime.Dispatch` 路由。

## 接受的取舍

- 接受 fixture reads，以换取不吞并 Issue #36 的真实读取范围。
- 接受仅开放 `tms.reassign`，以换取可证明的完整故障语义。
- 接受 capability endpoint 故障会阻止启动，以换取不在保证未知时接收真实写。
- 接受非终态 legacy binding 阻止真实模式启动，以换取不猜测 provider。
- 接受最小周期对账循环，以换取 unknown 在 consistency window 后自动收敛。
- 接受 lease renewal 和 migration 复杂度，以换取多 worker 与重启安全。

## 替代方案

- **统一七操作 gateway**：首轮需要为未实现操作制造假协议，扩大范围且不增加核心安全证明。
- **notification-only sandbox**：当前会引入联系人解析、模板治理和个人信息快照，超过 Issue #40。
- **注册全部工具并在运行时返回 unsupported**：会让模型和审批暴露不可执行能力。
- **unknown 后直接同 key 重放 mutation**：违反 Issue #40 只按 key 查询的验收条件。
- **只把 stale `dispatching` 映射为 `unknown`**：Guardian 仍需理解 lease 和数据库竞态，接口过浅。

## 风险与待确认

- sandbox 必须实际保证声明的 key retention，并在自身重启后保留幂等记录。
- provider credentials 必须按 tenant 或 account 隔离，避免不同 tenant 共享 key scope。
- capability revision 首版采用精确匹配，兼容范围留待后续需求。
- 非终态历史 binding 的人工迁移不在 Issue #40 范围内。
- OpenTelemetry、运营队列和容量门禁属于 Issue #43，不在本实现中加入。

## 下一实现步骤

先定义 capability、binding、dispatch/lookup result 和 capability-filtered registry，并写失败的
startup 与 adapter contract tests。边界固定后再修改 PostgreSQL effect executor 和 migration。
