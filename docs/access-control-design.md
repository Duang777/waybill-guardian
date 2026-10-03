# HTTP 身份与对象访问设计

## 问题

HTTP API 当前只受 loopback 限制。生产入口需要验证调用者身份，并按租户、角色和运单范围
授权 waybill、run 和 approval。run 与 approval 的 URL ID 不包含运单归属，因此授权前必须从
现有投影解析 `waybill_id`。默认 mock demo 仍需匿名可用，`PLATFORM=real` 缺少认证时必须在
数据库或 adapter 初始化前退出。

## 调用者用法

默认 demo 不新增凭据：

```bash
go run ./cmd/server
```

它使用 `AUTH_MODE=local`，只允许 loopback listener 和 Host。服务端生成
`local-demo-reviewer` 身份，请求中的 `Authorization` 和 `X-Actor` 都不能改变该身份。

JWT 模式使用固定 RSA 公钥：

```bash
AUTH_MODE=jwt \
AUTH_JWT_ISSUER=https://identity.example \
AUTH_JWT_AUDIENCE=waybill-guardian \
AUTH_JWT_PUBLIC_KEY_FILE=/run/secrets/waybill-jwt.pem \
TENANT_ID=tenant-a \
HTTP_ADDR=0.0.0.0:8080 \
go run ./cmd/server
```

token 必须包含 `sub`、`tenant_id`、`roles`、`waybill_all` 或 `waybill_ids`，以及标准
`iss`、`aud`、`iat` 和 `exp`。JWT 模式只接受 `Authorization: Bearer <token>`，不接受
query token。

handler 的调用顺序如下：

```go
// GET /api/runs/{id}/timeline
run, err := a.service.GetRun(domain.RunID(r.PathValue("id")))
grant, err := a.access.Grant(principalFromContext(r.Context()), httpauth.Read)
err = grant.Require(run.WaybillID)
subscription, err := a.service.Timeline(r.Context(), run.RunID, after)
```

```go
// POST /api/approvals/{id}/confirm
current, err := a.service.GetApproval(domain.ApprovalID(r.PathValue("id")))
principal, err := principalFromContext(r.Context())
grant, err := a.access.Grant(principal, httpauth.DecideApproval)
err = grant.Require(current.WaybillID)
decided, err := a.service.Decide(r.Context(), current.ID, guardian.DecisionRequest{
	Kind:      approval.DecisionConfirm,
	DecidedBy: principal.Subject(),
})
```

```go
// GET /api/runs
grant, err := a.access.Grant(principalFromContext(r.Context()), httpauth.Read)
runs, err := a.service.ListActiveRuns(r.Context())
runs = slices.DeleteFunc(runs, func(run guardian.RunSummary) bool {
	return !grant.Allows(run.WaybillID)
})
```

## 形状

`internal/httpauth` 同时拥有 HTTP 凭据解析、已验证 Principal、角色映射和运单 scope。
JWT wire claims 保持私有，不进入 handler、guardian 或存储接口。

```go
package httpauth

type Mode string

const (
	ModeLocal Mode = "local"
	ModeJWT   Mode = "jwt"
)

type TenantID string
type Subject string
type Role string
type Capability string

const (
	RoleViewer     Role = "viewer"
	RoleDispatcher Role = "dispatcher"
	RoleOperator   Role = "operator"

	Read           Capability = "read"
	StartRun       Capability = "run:create"
	DecideApproval Capability = "approval:decide"
)

type Config struct {
	Mode     Mode
	TenantID TenantID
	JWT      *JWTConfig
}

type JWTConfig struct {
	Issuer       string
	Audience     string
	PublicKeyPEM []byte
	Clock        func() time.Time
	Leeway       time.Duration
}

type Principal struct {
	subject  Subject
	tenantID TenantID
	roles    map[Role]struct{}
	scope    waybillScope
}

type Boundary struct {
	mode     Mode
	tenantID TenantID
	verifier tokenVerifier
	local    Principal
}

type Grant struct {
	capability Capability
	scope      waybillScope
}

func New(Config) (*Boundary, error)
func (b *Boundary) Authenticate(*http.Request) (*http.Request, error)
func (b *Boundary) Grant(Principal, Capability) (Grant, error)
func PrincipalFrom(context.Context) (Principal, error)
func (p Principal) Subject() string
func (p Principal) CredentialDeadline() (time.Time, bool)
func (g Grant) Require(domain.WaybillID) error
func (g Grant) Allows(domain.WaybillID) bool
func (g Grant) AllowsEvent(source, eventType string) bool
```

`Boundary` 只接受 RS256。它验证签名、精确 issuer、audience、subject、tenant、`iat`、`exp`
和存在时的 `nbf`。未知 role、非法运单 ID、空 scope，以及同时设置 `waybill_all=true` 和
`waybill_ids` 都使 token 无效。角色到 capability 的映射只存在一份：

- `viewer` 授予 `read`。
- `dispatcher` 授予 `run:create`。
- `operator` 授予 `approval:decide`。
- `event_producer` 授予 `event:ingest`。

`Boundary.Grant` 先要求 Principal tenant 等于进程 `TENANT_ID`，再检查角色。
`Grant.Require` 和 `Grant.Allows` 隐藏 wildcard 与 ID 集合表示。`Grant.AllowsEvent` 对
source 和 type 做精确匹配。`event_producer` JWT 必须包含非空、无重复且有数量上限的
`event_sources` 和 `event_types`。local Principal 拥有四个角色和全部运单范围，但事件
source 固定为 `urn:waybill-guardian:local-producer`。JWT Principal 还保存经验证的凭据截止
时间；HTTP middleware 将它转成请求 context deadline，长时间线在 token 到期后停止。

```text
cmd/server/main.go       配置、fail-fast、依赖组装
cmd/server/http.go       路由、资源解析、problem 映射
       |
       +--> internal/httpauth
       |      JWT/local、Principal、Grant、tenant/role/waybill policy
       |
       +--> internal/guardian
              不依赖 HTTP、JWT、Principal 或 Role
```

`GET /healthz` 保持匿名。其他路由统一经过认证中间件：

| 路由 | capability | 运单来源 |
|---|---|---|
| `POST /api/demo/trigger` | `run:create` | `guardian.DemoWaybillID` |
| `GET /api/runs` | `read` | 每个 `RunSummary.WaybillID` |
| run detail、timeline | `read` | `GetRun(id).WaybillID` |
| `GET /api/approvals` | `read` | 每个摘要的 `WaybillID` |
| approval confirm、reject | `approval:decide` | `GetApproval(id).WaybillID` |
| `GET /api/waybills/{id}` | `read` | 已校验的 path ID |
| `POST /v1/events` | `event:ingest` | 已校验的 source、type 和 `data.waybill_id` |

缺失或无效身份返回 `401 unauthenticated` 和 `WWW-Authenticate: Bearer`。已认证但租户、
角色或运单范围不符返回 `403 forbidden`。列表静默过滤无权对象并保持空数组为 `[]`。

启动先解析 mode、监听地址和存储配置，再校验 runtime mode 和构造 `Boundary`，最后才打开
PostgreSQL 和 platform adapter。`PLATFORM=real` 要求 `STORAGE=postgres` 与
`AUTH_MODE=jwt`。local 模式继续强制 loopback；JWT 模式允许显式非 loopback IP。

## 综合决定

采用 HTTP `Boundary + Grant` 和现有对象投影。三个候选中，该方案以最小接口隐藏 JWT、租户、
角色和运单 scope，同时不把 Principal 传入后台恢复、审批过期和 Agent 执行路径。

从另一候选吸收 `PrincipalFrom` 的显式错误，防止中间件漏挂后使用零值身份；从第三个候选吸收
独立 `waybill_all` claim，避免用 `"*"` 伪装运单 ID。拒绝 `AuthorizedService` 门面，因为它
需要复制七个 guardian 方法，并把 HTTP 身份类型带入业务层。

## 接受的取舍

- 接受单一 RSA 公钥和重启轮换，以换取无网络依赖且能在数据库连接前验证配置。
- 接受 run detail 的一次归属读取加一次 snapshot 读取，以避免授权前加载审计事件。
- 接受进程单租户，由配置表达资源 tenant，以复用现有 repository 过滤且不改 schema。
- 接受 JWT 模式暂不支持原生 `EventSource`，避免 token 进入 URL；local demo 不受影响。

## 开放问题与风险

- 正式身份源是否直接签发可信的 `roles` 和运单 scope，还是需要权限服务换取？
- 生产浏览器时间线应改用带 header 的流式 fetch，还是由同源网关提供受保护会话？
- 接入多公钥轮换时，需要用 JWKS 和 `kid` 替换静态 RSA 公钥。

## 下一实现步骤

先实现并单测 `internal/httpauth`，再接入启动配置和 HTTP 授权矩阵。
