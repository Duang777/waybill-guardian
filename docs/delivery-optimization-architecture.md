# 城市配送智能配载与调度架构

- 状态：实施基线
- 日期：2026-10-10
- 适用范围：`waybill-guardian` 城市配送规划、校验、审批、执行和动态重优化
- 研究依据：[`city-delivery-optimization.md`](./research/city-delivery-optimization.md)

## 1. 决策

系统新增独立的 `delivery` 业务域。现有 `guardian` 继续负责单运单异常调查和处置。
两个业务域通过版本化 CloudEvents 和公开应用命令协作，不共享业务聚合、状态机或数据表。

这个边界解决两个不同问题：

- `guardian` 以一张运单为一致性边界，负责调查、归因和少量写操作。
- `delivery` 以一个规划问题和活动配送计划为一致性边界，负责多仓、多订单、多车辆、
  多司机、路线、能源和三维装载。

不得给 `domain.RunContext` 增加可选的配送字段，也不得把 `waybill_id` 改成通用 subject。
现有异常处置的审批、幂等和审计语义保持不变。

## 2. 验收谓词

当以下条件全部满足时，城市配送能力才算完成：

1. 版本化问题可以表达多仓、多趟、取送对、可拆和不可拆订单、异构车辆、技能、温区、
   混装、硬软时间窗、司机工时、休息法规、EV 电量和充电。
2. 每个候选同时包含车辆分配、路线、时刻表、司机任务、能量计划和逐阶段三维装载。
3. 独立 Validator 从问题和计划原始字段重算全部硬约束。Validator 不导入 solver 或 packer。
4. 同一问题摘要、策略、求解器版本和评估预算产生相同计划摘要。
5. 动态事件生成后继问题和后继计划。已执行活动、硬承诺和冻结窗口不被改写。
6. 只有绑定有效校验报告的计划才能进入人工审批。
7. 审批绑定问题、策略、计划、校验报告和 effect 集的摘要。任一摘要变化都会使审批失效。
8. 确认后，外部写入使用服务端生成的稳定 effect 身份。重复请求、并发确认和进程恢复不会
   产生重复业务结果。
9. 进程在快照、求解、校验、审批、effect 和对账阶段退出后，恢复流程收敛到与无故障运行
   相同的业务状态。
10. 浏览器中的路线、指标、三维坐标、约束和执行状态逐字段来自后端 artifact。
11. PostgreSQL、artifact、SSE 和 HTTP 查询都执行租户隔离。跨租户 ID 和游标不能访问资源。
12. 现有异常运单功能、API、审计、恢复和前端路由继续通过原有测试。

## 3. 所有权边界

采用四个业务所有权边界。它们不是四个部署服务。

### 3.1 Problem

`Problem` 保存规划事实和约束。每个版本是不可变快照。

一个问题版本包含：

- 规划时域、仓库、位置和装卸月台。
- 配送请求、取送任务、履约单元和货物。
- 车辆、车厢、车门、车轴和重心包络。
- 司机班次、技能和法规。
- 充电站、连接器、充电曲线和能耗模型。
- 距离、时间和能量矩阵。
- 版本化规划策略。
- 来源版本、事实时间和读取水位。
- 已执行事实、硬承诺和软稳定性约束。

事实变化创建 `ProblemVersion + 1`。求解期间不能重新读取实时数据。

### 3.2 OptimizationRun

`OptimizationRun` 负责一次可恢复计算。它绑定一个问题摘要和一个求解配置摘要。

Run 不拥有审批或外部 effect。它只产出以下不可变 artifact：

- 规则基线。
- 求解器候选。
- 完整计划。
- 独立校验报告。
- 求解证据和性能统计。

同一问题可以有多个 Run。不同 solver 或预算可以并行生成候选。

### 3.3 DispatchPlan

`DispatchPlan` 是长期业务身份。它保存不可变 `PlanRevision`，并通过 CAS 维护唯一活动版本。

计划修订绑定：

- 父修订。
- 问题摘要。
- 承诺摘要。
- 策略摘要。
- 求解器和配置摘要。
- 计划 artifact。
- 校验报告 artifact。
- 相对活动版本的变化报告。

修改 ETA、路线、货物坐标或说明都生成新修订。系统不得原地修改已审批内容。

### 3.4 DispatchExecution

`DispatchExecution` 负责计划审批、effect 规划、外部写入、对账和活动版本切换。

它不假设 TMS、WMS、司机应用和通知平台参加同一数据库事务。可靠性来自：

- 本地事务。
- 不可变 effect 预览。
- 稳定幂等键。
- 外部结果查询。
- effect ledger。
- reconciliation。
- 人工复核终态。

## 4. 总体结构

```text
订单 / 仓 / 车 / 司机 / 路网 / 充电 / 策略
                    |
                    v
            Problem snapshot
       normalize -> freeze -> digest
                    |
                    v
             OptimizationRun
        baseline -> solve -> validate
            |          |          |
            |          |          +---- Independent Validator
            |          +--------------- builtin / VROOM / OR-Tools
            +-------------------------- artifact store
                    |
                    v
              DispatchPlan
       immutable revision + active CAS
                    |
                    v
            DispatchExecution
       approval -> effects -> reconcile
                    |
         +----------+-----------+
         v          v           v
        TMS        WMS       通知 / 司机应用

guardian incident domain
        |                     ^
        +---- CloudEvents ----+
```

建议代码结构：

```text
internal/delivery/
  domain/       问题、计划、状态和强类型值对象
  validate/     独立全量校验器
  solve/        内置求解器、3D 候选和外部 adapter
  service/      命令、查询、状态机和恢复协调
  artifact/     canonical codec 和内容寻址接口
  execution/    审批、effect、对账和 adapter 端口

internal/storage/postgres/
  delivery_*.go
  migrations/000008_delivery.sql

cmd/server/
  delivery_http.go

cmd/delivery-worker/
cmd/delivery-validator/

web/src/delivery/
```

模块数量以知识所有权为依据，不按执行阶段继续拆小包。`snapshot`、`baseline` 和 `coordinator`
属于 `service`，除非实现证明它们隐藏了独立复杂度。

## 5. 领域数据

### 5.1 身份和整数单位

```go
type TenantID string
type ProblemID string
type OptimizationRunID string
type PlanID string
type PlanRevisionID string
type ExecutionID string
type ApprovalID string
type EffectID string
type ArtifactDigest string

type DepotID string
type LocationID string
type RequestID string
type TaskID string
type FulfillmentUnitID string
type CargoID string
type VehicleID string
type DriverID string
type ChargerID string
```

长度使用毫米，重量使用克，金额使用分，比例使用 ppm，能量使用瓦时，功率使用瓦，
时间使用 UTC 时间戳或整数秒。求解和摘要路径不使用浮点业务值。

### 5.2 ProblemSnapshot

```go
type ProblemSnapshot struct {
	SchemaVersion string
	TenantID      TenantID
	ProblemID     ProblemID
	Version       uint64
	Horizon       TimeRange

	Locations        []Location
	Depots           []Depot
	Requests         []TransportRequest
	Units            []FulfillmentUnit
	Cargo            []CargoItem
	Vehicles         []Vehicle
	Drivers          []Driver
	Chargers         []ChargingStation
	Travel           TravelMatrix
	Energy           EnergyMatrix
	Policy           PlanningPolicy
	Commitments      CommitmentSet
	SourceRefs       []SourceRef

	ProblemDigest    ArtifactDigest
	PolicyDigest     ArtifactDigest
	CommitmentDigest ArtifactDigest
	CreatedAt        time.Time
}
```

`TransportRequest` 使用显式任务图表达取送和服务：

```go
type TransportRequest struct {
	ID             RequestID
	Priority       int32
	Tasks          []ServiceTask
	UnitIDs        []FulfillmentUnitID
	Split          SplitPolicy
	RequiredSkills SkillSet
}

type ServiceTask struct {
	ID              TaskID
	Kind            TaskKind
	LocationID      LocationID
	HardWindows     []TimeRange
	SoftWindows     []SoftTimeWindow
	ServiceSeconds  int64
	PredecessorIDs  []TaskID
	UnitIDs         []FulfillmentUnitID
	RequiredSkills  SkillSet
}
```

可拆订单只允许在稳定 `FulfillmentUnit` 边界拆分。不可拆订单的履约单元组成一个原子组。
solver 不得临时按重量比例生成无法追踪的货物。

### 5.3 车辆、司机和能源

```go
type Vehicle struct {
	ID               VehicleID
	HomeDepotID      DepotID
	Availability     []TimeRange
	Skills           SkillSet
	Compartments     []Compartment
	Doors            []Door
	Axles            []Axle
	CGEnvelope       CGEnvelope
	MaxTrips         uint16
	FixedCostCents   int64
	DistanceCostCPKM int64
	WorkCostCPH      int64
	Energy           EnergySpec
}

type Driver struct {
	ID             DriverID
	Skills         SkillSet
	Shift          TimeRange
	StartLocation  LocationID
	EndLocations   []LocationID
	Regulation     DriverRegulation
}

type DriverRegulation struct {
	MaxContinuousDriveSeconds int64
	RequiredBreakSeconds      int64
	MaxDutySeconds            int64
	MaxDriveSeconds           int64
	MinRestBetweenDutySeconds int64
}
```

EV 车辆包含电池容量、初始电量、保底电量、连接器和充电曲线引用。Validator 对每个路段重算
SOC，并全局检查充电站时窗和容量。

### 5.4 计划

```go
type Plan struct {
	SchemaVersion    string
	PlanID           PlanID
	RevisionID       PlanRevisionID
	ProblemDigest    ArtifactDigest
	PolicyDigest     ArtifactDigest
	CommitmentDigest ArtifactDigest
	Solver           SolverIdentity
	ConfigDigest     ArtifactDigest
	Duties           []VehicleDuty
	Unassigned       []UnassignedUnit
	Objective        ObjectiveVector
	Metrics          PlanMetrics
	PlanDigest       ArtifactDigest
}

type VehicleDuty struct {
	VehicleID VehicleID
	DriverIDs []DriverID
	Trips     []Trip
}

type Trip struct {
	ID           string
	StartDepotID DepotID
	EndDepotID   DepotID
	Stops        []Stop
	Schedule     []DutySegment
	Energy       []EnergyLeg
	LoadStages   []LoadStage
}
```

`VehicleDuty -> Trips` 表达同一车辆的多趟运行。车辆位置、司机位置和时间必须在相邻 trip 之间
连续。

### 5.5 三维装载

```go
type Placement struct {
	CargoID       CargoID
	CompartmentID string
	PositionMM    Point3
	SizeMM        Box
	Orientation   Orientation
	LoadAtTaskID  TaskID
	UnloadAtTaskID TaskID
	DoorID        string
}

type LoadStage struct {
	AfterStopIndex uint32
	Placements     []Placement
	AxleLoadsG     []int64
	CenterOfMassMM Point3
}
```

每个取货或卸货后的状态都要独立合法。只校验发车时布局不够。

## 6. 独立 Validator

`internal/delivery/validate` 只依赖 `domain` 和无业务判断的整数几何函数。CI 禁止它导入
`solve`、第三方 packer 或外部 adapter。

Validator 固定执行以下规则族：

| 前缀 | 规则 |
|---|---|
| `V0xx` | schema、摘要和引用完整性 |
| `V1xx` | 订单覆盖、拆分守恒和未分配原因 |
| `V2xx` | 取送先后、同车、同趟和最大在途时间 |
| `V3xx` | 仓、车辆、司机、技能、温区和混装 |
| `V4xx` | 路线连续性、多趟位置连续性和矩阵索引 |
| `V5xx` | 硬软时间窗、服务、等待、班次、工时和休息 |
| `V6xx` | EV 能量、保底电量、充电兼容、时窗和站点容量 |
| `V7xx` | 车厢边界、障碍物、朝向和不重叠 |
| `V8xx` | 支撑面积、支撑图、累计承重和易碎 |
| `V9xx` | 多门提取走廊、逐站卸货和 rehandle |
| `V10xx` | 车辆总重、舱室载重、轴载和重心包络 |
| `V11xx` | 已执行事实、硬承诺、冻结窗口和软稳定性差异 |
| `V12xx` | 目标值、成本和业务指标重算 |

每个 violation 包含稳定 code、严重度、对象引用、期望值、实际值、路段和站次。自由文本不参与
程序判断。

```go
type ValidationReport struct {
	SchemaVersion    string
	ProblemDigest    ArtifactDigest
	PolicyDigest     ArtifactDigest
	CommitmentDigest ArtifactDigest
	PlanDigest       ArtifactDigest
	Validator        ValidatorIdentity
	Valid            bool
	Violations       []Violation
	Metrics          PlanMetrics
	ReportDigest     ArtifactDigest
	CreatedAt        time.Time
}
```

只有 `Valid=true` 且硬违规为零的报告可以申请审批。Validator 不修补非法计划。

## 7. 求解器

### 7.1 深接口

```go
type Solver interface {
	Identity() SolverIdentity
	Capabilities(context.Context) (Capabilities, error)
	Solve(
		context.Context,
		ProblemSnapshot,
		SolveConfig,
		ProgressSink,
	) (SolveResult, error)
}
```

调用方只调用一次 `Solve`。路线构造、司机排班、充电、三维装载和局部搜索都隐藏在接口后。

### 7.2 内置确定性 solver

内置 solver 是完整能力的基线：

1. 根据仓、技能、温区、车型和司机资格建立候选索引。
2. 应用已执行事实和硬承诺。
3. 用稳定 regret insertion 构建多仓、多趟和取送路线。
4. 插入司机休息和 EV 充电。
5. 用极点法生成三维装载候选。
6. 运行 relocate、swap、2-opt、cross-exchange、trip split/merge、换仓、换车、换司机、
   休息移动、充电替换和 placement move。
7. 路线和装载失败产生 no-good cut，回到联合搜索。
8. 按字典序目标保存候选。
9. 把最终候选交给独立 Validator。

正常终止使用固定评估预算。同一输入和预算的候选归并顺序固定。wall-clock deadline 只保护资源；
触发时返回 `aborted`，不能把当时的偶然 incumbent 标记为可重放终态。

### 7.3 外部 adapter

VROOM 和 OR-Tools adapter 是路线种子 provider，不是可执行计划的权威。

每个 adapter 必须：

- 暴露版本化 capability。
- 对不能无损表达的硬约束 fail closed。
- 固化请求、响应、后端版本和摘要。
- 解析为内部 `RouteSeed`，不泄漏外部 DTO。
- 让内置 completion engine 补齐法规、能源和三维装载。
- 把完整结果交给同一个独立 Validator。

远端 job ID 持久化。worker 恢复后继续查询原 job，不能重复提交。

## 8. 动态重优化

高频遥测先归一化为有业务意义的 `OperationalFact`。GPS 每个点不直接创建问题版本。

支持的事实包括：

- 新订单、取消订单和服务完成。
- 车辆、司机或充电站不可用。
- 预计到达偏差超阈值。
- 路网矩阵发生实质变化。
- SOC 低于计划包络。
- `guardian` 已完成的改派或限制。

每次重优化基于活动计划修订和事实水位创建后继问题。

承诺分三层：

1. 已执行活动、在途货物和正在服务的站点不可修改。
2. 冻结窗口内已经通知的车辆、司机、顺序和 ETA 容差是硬约束。
3. 冻结窗口外允许改变，但换车、换司机、改序、ETA 漂移和重新装载计入稳定性成本。

若现实变化使硬承诺已经不可满足，Run 进入 `manual_review` 并输出最小冲突集。solver 不能静默
放宽承诺。

## 9. 审批和执行

审批绑定以下摘要：

```text
tenant
plan_id
revision_id
base_revision_id
problem_digest
policy_digest
commitment_digest
plan_digest
validation_report_digest
effect_set_digest
```

审批前，`execution.Planner` 生成不可变 effect 预览。典型 effect 包括：

- 创建路线计划。
- 分配车辆和司机。
- 发布逐站任务。
- 发布仓内装载指令。
- 更新客户 ETA。
- 激活计划修订。

effect 身份由服务端从 revision、action、业务目标和参数摘要生成。浏览器、模型和请求 header
不能覆盖 `effect_id` 或幂等键。

外部结果未知时先查询，不盲目重发。部分成功进入 `reconciliation_required`，并按 effect
逐项对账。全部必要 effect 成功后，系统用 CAS 切换活动计划修订。

## 10. 状态机

### 10.1 OptimizationRun

```text
requested -> snapshotted -> queued -> solving -> validating -> succeeded
                                   |          |          |
                                   |          |          +-> candidate_rejected
                                   |          +------------> exhausted
                                   +-----------------------> aborted
任意非终态 -> failed | cancelled | manual_review
```

`exhausted` 表示固定预算内没有找到可审批候选，不表示数学不可行。

### 10.2 PlanRevision

```text
candidate -> validated -> awaiting_approval -> approved -> applying -> active
                         |                   |             |
                         +-> rejected        +-> stale     +-> reconciliation_required
                         +-> expired                       +-> partially_applied
active -> superseded | completed
```

计划正文进入 `candidate` 后不可变。状态变化只更新投影，不修改 artifact。

### 10.3 DispatchExecution

```text
prepared -> executing -> committed
               |
               +-> retry_wait -> executing
               +-> reconciliation_required -> executing
               +-> manual_review
```

## 11. Artifact

问题、矩阵、计划、校验报告、三维装载、求解证据和 effect 请求响应进入内容寻址存储。

```go
type Store interface {
	Put(context.Context, TenantID, Artifact) (ArtifactRef, error)
	Open(context.Context, TenantID, ArtifactDigest) (io.ReadCloser, Metadata, error)
	Verify(context.Context, TenantID, ArtifactDigest) error
}
```

规则：

- canonical bytes 使用版本化 schema。
- 数组只有在业务无序时才稳定排序。
- 路线和装载步骤等有序数组保持原顺序。
- 写入采用 `put-if-absent`。
- 数据库只引用已完整写入并校验的 artifact。
- 读取时重新计算摘要。
- 审批引用的 artifact 在审计保留期内不得删除。
- 跨租户即使摘要相同也必须重新授权。

本地实现使用受权限保护的文件目录。生产实现使用对象存储和 PostgreSQL metadata。

## 12. PostgreSQL

delivery 使用独立 schema 或 `delivery_` 前缀，不写入 `waybill.runs`、`waybill.approvals`、
`waybill.effects` 或 `waybill.audit_events`。

主要表：

```text
delivery.problems
delivery.problem_versions
delivery.optimization_runs
delivery.solver_attempts
delivery.plans
delivery.plan_revisions
delivery.validation_records
delivery.approvals
delivery.executions
delivery.effects
delivery.operational_facts
delivery.audit_events
delivery.inbox
delivery.outbox
delivery.artifacts
```

每张表包含 `tenant_id`。所有业务唯一约束都包含 tenant。

关键事务：

- 创建问题版本、审计事件和 outbox。
- claim Run 并增加 fencing token。
- 保存阶段 checkpoint 和事件。
- 确认审批、创建 execution 和 effects。
- 完成 effect 并推进 execution。
- 全部 effect 成功后 CAS 激活计划修订。

solver、对象存储和外部 HTTP 不在数据库长事务内。

## 13. 恢复和并发

- Run 和 effect worker 使用数据库时间、租约续期和 fencing token。
- 旧 worker 的 checkpoint 或完成写必须因 fencing token 过期而影响零行。
- artifact 先完整写入，再提交数据库引用。
- 外部 solver 响应存在时，恢复流程复用原响应。
- 内置 solver 从稳定评估边界恢复，或用相同输入和预算确定性重跑。
- 取消是持久化意图，不只依赖 `context.CancelFunc`。
- 审批过期由持久化扫描推进，不依赖单进程 timer。
- SSE 从数据库 replay，再在同一水位切换 live 通知。
- 审计哈希或 artifact 摘要错误会隔离对应资源并进入 `manual_review`。

## 14. 租户和授权

`tenant_id` 只来自验证后的身份。请求路径和 body 中的 tenant 只能用于一致性检查。

权限至少包括：

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
```

repository 查询必须显式接收 tenant。对象下载和 SSE 建立前再次检查 tenant 和资源范围。
生产数据库使用 RLS 作为第二道防线，但不能代替应用授权。

策略可以要求计划创建者不能审批自己的计划。紧急 override 需要单独权限、非空原因和审计事件，
但仍不能绕过硬约束 Validator。

## 15. HTTP 和 SSE

新增独立资源：

```text
POST /api/v1/delivery/problems
GET  /api/v1/delivery/problems/{id}
POST /api/v1/delivery/optimization-runs
GET  /api/v1/delivery/optimization-runs/{id}
GET  /api/v1/delivery/optimization-runs/{id}/events
POST /api/v1/delivery/plans/{id}/reoptimizations
GET  /api/v1/delivery/plans/{id}
GET  /api/v1/delivery/plans/{id}/revisions/{revision_id}
POST /api/v1/delivery/revisions/{revision_id}/approval-requests
POST /api/v1/delivery/approvals/{id}/decisions
GET  /api/v1/delivery/executions/{id}
GET  /api/v1/delivery/artifacts/{digest}
```

命令要求 `Idempotency-Key`。可变聚合使用 `If-Match` 或 `expected_version`。
SSE 使用 Run 内单调 seq 和 `Last-Event-ID`。大型矩阵和三维坐标不进入 SSE。

## 16. 前端

新增独立调度台：

```text
/delivery
/delivery/problems/:id
/delivery/runs/:id
/delivery/plans/:id
/delivery/plans/:id/load
/delivery/approvals/:id
/delivery/executions/:id
```

页面包含：

- 问题来源、版本、水位和数据质量。
- 求解阶段、候选、基线和失败分类。
- 多仓、多趟路线地图。
- 司机 duty、服务、等待、休息和充电时间轴。
- SOC、载重、轴载和重心曲线。
- 逐站三维装卸和多门提取走廊。
- Validator 规则和对象定位。
- 活动计划、冻结区域和重优化差异。
- 审批摘要、effect 预览和执行对账。
- 审计事件和 artifact 引用。

前端只渲染权威数据。WebGL 不可用时使用确定性 SVG 正交视图和表格，审批仍然可用。

## 17. 与 guardian 的协作

Guardian 到 delivery：

```text
guardian.waybill.disposition.executed.v1
guardian.vehicle.unavailable.v1
guardian.driver.restricted.v1
```

Delivery 到 guardian 或外围系统：

```text
delivery.plan.activated.v1
delivery.assignment.created.v1
delivery.route.execution_deviated.v1
delivery.order.at_risk.v1
```

双方通过 inbox 去重和 anti-corruption mapper 转换。任何一方都不能导入另一方的内部聚合类型。

## 18. 可观测性和容量

指标只使用低基数标签：

```text
delivery_optimization_runs_total{status,solver}
delivery_optimization_duration_seconds{stage,solver}
delivery_candidates_total{result}
delivery_validation_violations_total{code}
delivery_adapter_requests_total{provider,result}
delivery_effects_total{action,status}
delivery_reoptimizations_total{trigger,result}
delivery_artifact_integrity_failures_total{kind}
delivery_worker_lease_lost_total{kind}
```

tenant、订单、车辆、Run 和摘要只进入受控日志或 trace。

容量验收至少覆盖：

- 100、500 和 2,000 个配送任务。
- 10、50 和 200 辆车辆。
- 1、5 和 20 个仓。
- 每车 300 件货物。
- 100 个并发 Run 查询和 500 个 SSE 连接。
- 对象存储延迟、数据库 failover、solver 超时和 effect 结果未知。

报告记录 p50、p95、p99、峰值内存、候选质量、可行率和恢复时间。容量超限必须返回稳定错误，
不能触发无界 goroutine 或内存增长。

## 19. 实施顺序

每个阶段都保持现有异常处置可运行。

1. 冻结问题、计划、校验报告和摘要协议。
2. 建立 golden corpus、性质测试、fuzz 和独立 Validator。
3. 实现 artifact store、delivery PostgreSQL schema、审计和租约。
4. 实现完整规则基线和内置确定性 solver。
5. 实现联合 3D packing、轴载、重心和逐站可达性。
6. 实现 Run、Plan 和 Execution 服务、恢复和 SSE。
7. 实现计划审批、effect、TMS/WMS adapter 和对账。
8. 实现动态事件、冻结承诺和重优化。
9. 实现 VROOM 和 OR-Tools HTTP adapter 及 capability conformance。
10. 实现完整前端和浏览器验收。
11. 完成 PostgreSQL、并发、故障注入、容量、安全和恢复验证。
12. 更新中英文 README、接口参考、运维手册和基准证据。

## 20. 架构综合记录

架构探索比较了三种完整方案：

- 候选一采用独立 dispatch 域、Plan 和 Revision 分离。
- 候选二采用 `OptimizationRun`、`PlanRevision` 和 `DispatchExecution` 三个核心聚合。
- 候选三增加 PlanningSession、ProblemRevision、SolveAttempt、Approval 和 Commit 五类聚合。

本设计以候选二为基础。候选一贡献活动计划 CAS、artifact 和跨域事件边界。候选三贡献事实水位、
持久化取消、fencing、承诺层级和完整三维规则。

没有采用候选三的细粒度包结构。问题版本和求解 attempt 是核心实体，但不需要为每个实体建立
一个应用包。实现先使用 `domain`、`validate`、`solve`、`service`、`artifact` 和 `execution`
六个知识模块。只有真实复杂度证明当前接口过浅时才继续拆分。

## 21. 取舍

- 接受 delivery 拥有独立审批和审计投影，以换取正确的业务边界。
- 接受 solver 和 Validator 重复计算，以换取独立准入证据。
- 接受不可变 revision 增加存储量，以换取审批绑定、重放和动态差异。
- 接受内置 solver 不证明全局最优，以换取完整约束、确定性和无外部依赖的生产基线。
- 接受外部 adapter 只生成路线种子，以防止能力缺口被误报为完整计划。
- 接受 Saga 和 reconciliation，以符合多个企业系统不能参加同一事务的事实。
- 接受保守的正交箱体和静态车辆模型。复杂材料、绑扎、侧倾和机械路径需要专业系统证明，
  本系统不会把可视化结果声明为工程安全认证。
