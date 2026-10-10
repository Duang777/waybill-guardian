# 城市配送平台实现设计

本文固定 Issue #135、#131、#132 的实现边界。它补充
[`delivery-optimization-architecture.md`](./delivery-optimization-architecture.md)，只描述本轮代码形状、
公开接口和原子事务，不重复算法细节。

## 1. 调用方视角

HTTP、Delivery Agent 和 worker 都通过同一个 `service.Platform` 进入业务，但使用隔离接口。

```go
type Platform interface {
	Commands() Commands
	Queries() Queries
	EventStreams() EventStreams
	RunWorker(WorkerConfig) RunWorker
	EffectWorker(WorkerConfig) EffectWorker
	Recover(context.Context) error
}
```

创建问题只提交服务端已登记的数据源和快照引用。请求不能携带订单、车辆、矩阵、路线、坐标、
约束、effect ID 或幂等执行键。

```go
created, err := platform.Commands().CreateProblem(ctx, service.CreateProblem{
	TenantID:       trustedTenant,
	Actor:          trustedActor,
	IdempotencyKey: requestKey,
	ProblemID:      "problem-west-20261010",
	SourceProfile:  "enterprise-prod-v1",
	SnapshotRef:    "cut-20261010T080000Z",
	Horizon:        horizon,
})

run, err := platform.Commands().RequestOptimization(ctx, service.RequestOptimization{
	TenantID:       trustedTenant,
	Actor:          trustedActor,
	IdempotencyKey: runKey,
	ProblemID:      created.ProblemID,
	ProblemVersion: created.Version,
	SolverProfile:  "production-default-v1",
})
```

worker 不接触 repository 或 lease 细节。

```go
didWork, err := platform.RunWorker(config).RunOnce(ctx)
err = platform.EffectWorker(config).Run(ctx)
```

审批确认只接受审批身份、预期版本和人工决定。确认事务创建唯一 execution 和已经预览过的
effect 集，不执行外部 HTTP。

```go
decision, err := platform.Commands().DecideApproval(ctx, service.DecideApproval{
	TenantID:        trustedTenant,
	Actor:           trustedActor,
	IdempotencyKey:  decisionKey,
	ApprovalID:      approvalID,
	ExpectedVersion: expectedVersion,
	Decision:        service.Confirm,
})
```

## 2. 模块所有权

```text
internal/delivery/
  domain/       Problem、Run、PlanRevision、Execution 状态和值对象
  source/       七类 Reader、权威 manifest、快照协调和 adapter
  service/      Commands、Queries、worker、状态机和恢复协调
  execution/    effect 预览、身份、adapter 端口和 lookup-first 对账
  agent/        六个 Delivery 工具、prompt 和独立 history namespace
  artifact/     现有内容寻址 Store
  validate/     现有独立 Validator

internal/storage/postgres/
  delivery_*.go
  migrations/000008_delivery_core.sql
  migrations/000009_delivery_events.sql
  migrations/000010_delivery_execution.sql

cmd/server/
  delivery_http.go
  delivery_sse.go
```

约束：

- `delivery` 不导入 guardian 聚合、审批、幂等或工具类型。
- `validate` 继续只依赖 `delivery/domain`。
- handler、Agent tool 和 worker 不直接编排 SQL 事务。
- solver、artifact I/O 和外部 HTTP 不在 PostgreSQL 事务内执行。

## 3. 权威数据快照

七类来源必须由一个不可变 manifest 固定。只比较抓取时间差不能证明来源处于同一业务水位。

```go
type Manifest struct {
	SchemaVersion string
	TenantID      domain.TenantID
	Ref           SnapshotRef
	IssuedAt      time.Time
	ExpiresAt     time.Time
	Revisions     []Revision
	Digest        domain.ArtifactDigest
}

type Revision struct {
	Kind          Kind
	System        string
	Revision      string
	EffectiveAt   time.Time
	ETag          string
	EventOffset   string
	ContentDigest domain.ArtifactDigest
}

type Stamp struct {
	TenantID      domain.TenantID
	ManifestRef   SnapshotRef
	Kind          Kind
	System        string
	Revision      string
	EffectiveAt   time.Time
	FetchedAt     time.Time
	ETag          string
	EventOffset   string
	ContentDigest domain.ArtifactDigest
}
```

Reader 固定为：

- `OrderReader`
- `DepotReader`
- `FleetReader`
- `DriverReader`
- `TravelMatrixReader`
- `ChargerReader`
- `PolicyReader`

快照协调器执行：

1. 读取并校验 manifest。
2. 在共享 deadline 下并发读取七个精确 revision。
3. 逐项核对 tenant、manifest、kind、revision、effective time、ETag/offset 和内容摘要。
4. 检查规模、整数单位、UTC、引用和矩阵维度。
5. 使用 `Manifest.IssuedAt` 设置 `ProblemSnapshot.CreatedAt`。
6. 调用现有 `service.BuildProblemSnapshot`，不复制 canonical 或 digest 逻辑。
7. 写入并复验 problem artifact。
8. 用一个短事务提交 problem version、审计、outbox 和幂等结果。

同一 manifest 连续构建二十次必须得到相同 problem digest。任何来源失败、混合水位或缺少固定
revision 都 fail closed，不回退到 fixture 或未声明的缓存。

adapter：

- PostgreSQL：一个只读 `REPEATABLE READ` 快照覆盖七类读取。
- JSON：顶层 manifest 绑定七个数据段摘要，严格解码并拒绝符号链接和超限内容。
- Enterprise HTTP：固定 HTTPS origin、认证、deadline、响应体上限、禁用重定向、SSRF 防护、
  严格 schema 和只读重试。

## 4. 聚合和状态

四个聚合保持独立：

- `Problem`：稳定身份和不可变 `ProblemVersion`。
- `OptimizationRun`：绑定 problem/config digest，拥有 checkpoint、取消意图和终止分类。
- `DispatchPlan`：拥有不可变 `PlanRevision` 和唯一 active revision CAS。
- `DispatchExecution`：拥有审批、effect ledger、对账和激活结果。

`PlanRevision` 绑定：

```go
type PlanRevision struct {
	ID                     domain.PlanRevisionID
	PlanID                 domain.PlanID
	BaseRevisionID         domain.PlanRevisionID
	ProblemDigest          domain.ArtifactDigest
	PolicyDigest           domain.ArtifactDigest
	CommitmentDigest       domain.ArtifactDigest
	PlanArtifactDigest     domain.ArtifactDigest
	PlanDigest             domain.ArtifactDigest
	ValidationArtifact     domain.ArtifactDigest
	ValidationReportDigest domain.ArtifactDigest
	EffectSetArtifact      domain.ArtifactDigest
	EffectSetDigest        domain.ArtifactDigest
	Status                 domain.RevisionStatus
	Version                uint64
}
```

计划正文、报告和 effect preview 进入 `candidate` 后不可变。状态变化只更新投影。

## 5. 操作型持久化端口

禁止暴露通用 `Transact(func(UnitOfWork))`。Store 的每个方法拥有完整原子不变量：

```go
type Store interface {
	CommitProblem(context.Context, CommitProblemTx) (ProblemVersion, Replay, error)
	CreateRun(context.Context, CreateRunTx) (OptimizationRun, Replay, error)
	SaveRunCheckpoint(context.Context, RunClaim, SaveCheckpointTx) error
	PublishRevision(context.Context, RunClaim, PublishRevisionTx) (PlanRevision, error)
	PrepareApproval(context.Context, PrepareApprovalTx) (Approval, Replay, error)
	DecideAndCreateExecution(
		context.Context,
		DecideExecutionTx,
	) (DispatchExecution, Replay, error)
	CompleteEffect(context.Context, EffectClaim, CompleteEffectTx) error
	ActivateRevision(context.Context, ActivateRevisionTx) error

	ClaimRuns(context.Context, ClaimRuns) ([]RunClaim, error)
	ClaimEffects(context.Context, ClaimEffects) ([]EffectClaim, error)
	Replay(context.Context, StreamCursor) ([]Event, error)
	Subscribe(context.Context, StreamCursor) (*Subscription, error)
	ScanRecovery(context.Context, RecoveryScan) ([]RecoveryItem, error)
}
```

每个写方法内部统一完成：

- 锁和 CAS/fencing 判断。
- projection 更新。
- hash-chain audit append。
- ordered outbox insert。
- 命令幂等结果保存。

旧 lease worker 必须影响零行。artifact 先完整发布再写数据库引用，允许产生可回收孤儿 artifact，
不允许数据库指向半写对象。

## 6. 审批和 effect

审批绑定以下不可变事实：

```go
type ApprovalBinding struct {
	TenantID               domain.TenantID
	PlanID                 domain.PlanID
	RevisionID             domain.PlanRevisionID
	BaseRevisionID         domain.PlanRevisionID
	ActiveVersion          uint64
	ProblemDigest          domain.ArtifactDigest
	PolicyDigest           domain.ArtifactDigest
	CommitmentDigest       domain.ArtifactDigest
	PlanDigest             domain.ArtifactDigest
	ValidationReportDigest domain.ArtifactDigest
	EffectSetDigest        domain.ArtifactDigest
}
```

effect 身份只由服务端从 tenant、revision、action、target 和 canonical parameters digest 派生。
浏览器、模型和 header 不能覆盖 effect ID 或 key。

adapter capability 和持久化 binding 必须包含：

- `SameRequestReplays`
- `MismatchRejected`
- `LookupByKey`
- `KeyRetention`
- `LookupConsistencyWindow`
- `SupportsRecovery`

必要 effect 缺少任一恢复保证时，审批创建即失败。

执行规则：

1. 首次 dispatch 前持久化 adapter binding、request digest、key 和 `dispatching` intent。
2. 外部调用在事务外执行。
3. 超时、EOF、响应损坏、发送后取消或 lease 丢失均记为 `unknown`。
4. `dispatching`、`unknown`、`reconciling` 恢复时只能 lookup。
5. 只有 provider 在一致性窗口后证明 `authoritative_absent`，且原 key 未过期时，才允许同 key、
   同请求重发。
6. required effect 全部成功后执行本地 revision activation CAS。

revision activation 不是外部 effect，不进入 effect set。审批通过 `BaseRevisionID`、
`ActiveVersion` 和 effect-set digest 绑定它的前提。

## 7. HTTP、SSE 和授权

HTTP 路由使用 `/api/v1/delivery/...`，继续经过现有 CORS、认证、credential deadline 和 shutdown
context。命令要求 `Idempotency-Key`，可变资源要求 `If-Match`。

新增权限：

```text
delivery.read
delivery.problem.write
delivery.optimize
delivery.approve
delivery.execute
delivery.reoptimize
delivery.audit.read
delivery.artifact.read
delivery.policy.admin
delivery.override
```

tenant 只来自已验证 Principal。repository 查询继续显式包含 tenant。跨 tenant ID、cursor、
approval 和 artifact digest 返回不可枚举的拒绝结果。

artifact digest 不是 bearer capability。下载前必须证明该 digest 可从同 tenant 且已授权的
problem、run、revision、approval 或 execution 到达。

SSE：

1. 先注册 PostgreSQL notification interest。
2. 再读取 stream head `H` 和 `(cursor,H]` backlog。
3. 后续查询 `seq > H`；notification 只负责唤醒，数据库行是权威事实。
4. SSE `id` 使用 aggregate-local 持久化 seq。
5. 大型矩阵、计划、三维坐标和 provider payload 不进入 SSE。

## 8. Delivery Agent

只注册：

```text
delivery.get_problem_summary
delivery.request_optimization
delivery.get_candidate
delivery.compare_revisions
delivery.explain_validation
delivery.request_plan_approval
```

工具输入只有资源 ID、登记过的 profile 和解释文本。tenant、actor、digest、路线、坐标、硬约束、
目标权重、effect、幂等键和审批决定都不能由模型提交。

复用 hastekit runtime 基础设施，但使用独立 registry、prompt、history namespace 和业务状态。
不把第二套完整 runtime 作为 #132 的前置条件。

## 9. 验证顺序

1. source manifest、七类 Reader、三类 adapter contract tests。
2. 相同 manifest 二十次 digest 一致和混合水位 fail-closed。
3. PostgreSQL migration、tenant FK、命令幂等、lease/fence 和事务原子性。
4. run checkpoint、artifact 损坏隔离和进程恢复。
5. revision/report/effect-set digest 绑定。
6. 二十次并发确认只创建一个 execution/effect 集。
7. TMS/WMS 超时后只 lookup，不换 key、不盲目重发。
8. SSE replay/live 无缺失和逻辑重复。
9. Agent schema 拒绝权威字段。
10. `go test ./...`、PostgreSQL 集成测试、race、边界检查和 guardian 回归。

## 10. 综合决策

架构探索比较了三种形状：

- 单一大 `Application` 和完整 authority manifest。
- 角色隔离的 `Commands`、`Queries`、workers 和不可变聚合投影。
- artifact 证据中心配细粒度生命周期包。

最终以第二种为基础。它提供最短的 handler/tool/worker 调用链。第一种贡献精确 manifest、
adapter 恢复契约、SSE 水位顺序、artifact 可达性和本地 activation CAS。第三种贡献操作型事务
端口，替换通用 UnitOfWork。

拒绝通用事务回调，因为它允许服务层漏写 audit/outbox 或改变锁顺序。拒绝仅凭时间偏差推断
一致水位。拒绝把本地 activation 放进外部 effect set。拒绝在本阶段强制引入 RLS；当前必须
完成 tenant-qualified 主外键、repository scope、artifact 可达性和跨租户测试，RLS 需要独立
连接角色与 `SET LOCAL` 设计。

## 11. 取舍

- 接受 Delivery 独立表、审批、审计和 effect 状态，以保持 guardian 行为稳定。
- 接受 artifact-first 产生孤儿对象，以避免数据库引用半写内容。
- 接受重复 Validator 和 artifact 校验，以获得独立准入证据。
- 接受 provider lookup 延迟，以避免重复 TMS/WMS 写入。
- 接受 enterprise source 必须提供 manifest、数据库快照或等价公共水位；无法证明一致时拒绝冻结。

## 12. 首个实现单元

先实现 source manifest、七类 Reader、快照协调器和 adapter conformance suite。该单元完成后，
`CreateProblem` 可以从权威来源生成可重复、可校验的 `ProblemSnapshot`，并为 PostgreSQL
`CommitProblem` 提供稳定输入。
