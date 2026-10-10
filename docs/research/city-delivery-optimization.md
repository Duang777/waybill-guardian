# 城市配送智能配载与调度技术研究

- 状态：研究结论
- 研究日期：2026-10-10
- 适用范围：waybill-guardian 新增城市配送智能配载与调度能力
- 依赖基线：[`RFC-001`](../RFC-001.md)、[`RFC-002`](../RFC-002.md)

## 1. 结论摘要

### 1.1 推荐方案

在现有异常运单处置系统旁新增 `delivery optimization` 业务域，不替换现有
Agent、人工审批、幂等、审计、SSE 和总览链路。48 小时版本采用 Go 原生路线搜索、受控的
三维装载依赖和项目自有 Validator：

1. 服务端把订单、货物、车辆、时间窗、距离和时间矩阵归一化为不可变问题快照。
2. 项目内的确定性路线求解器完成车辆分配、顺序插入、relocate、swap 和 2-opt。
3. `gopackx v0.3.0` 只负责生成三维放置候选，项目代码补充配送顺序和卸货阻挡约束。
4. 每次路线变更只重新计算受影响车辆的时间表和装载方案。
5. 项目自有 Validator 不调用路线求解器或 `gopackx` 的可行性函数，从原始字段重算全部硬约束。
6. 只有零硬约束违规、输入未过期的计划才能进入人工审批。
7. 审批通过后，复用现有 effect 身份、幂等执行、审计和恢复机制写入 TMS。

`gopackx` 是 2026 年发布的新项目，虽然提供 MIT 许可证、整数几何、确定性结果、超时返回当前
候选、支撑率、易碎和累计承重，但采用量很低。接入前设置两小时技术门禁：固定 tag 和 commit，
运行项目的边界、性质、并发和超时测试。任何一项失败就切换到仓库内的最小极点放置器，不降低
Validator 或卸货顺序规则。[R22]

路线和装载不能割裂。Gendreau 等人的联合问题研究采用外层路线搜索、内层装载子问题的结构，
证明这种分解适合组合车辆路径与三维装载约束；极点法则为三维候选放置位置提供了可实现的
启发式基础。[R1][R2]

PyVRP v0.14.0 作为离线质量基准，不进入在线运行路径。它支持固定 seed、运行时上限、异构车队、
固定成本、多维容量、时间窗和服务时长，适合检查项目内路线求解器的质量差距。[R10] VROOM
v1.15.0 是赛后首选的 permissive 许可证路线 adapter。Nextroute 当前使用 BSL 1.1，cuOpt 要求
NVIDIA GPU 和 CUDA，二者都不作为初赛默认依赖。[R11][R23][R24]

### 1.2 核心边界

| 参与者 | 可以做什么 | 不可以做什么 |
|---|---|---|
| Agent | 理解业务目标、选择已注册场景、请求求解、解释候选、汇总风险和证据 | 构造坐标、声明计划可行、修改硬约束、生成执行身份、代替人工批准 |
| Solver | 在固定输入、版本、种子和预算下生成候选计划及目标值 | 执行外部写入、批准计划、把自身检查作为最终合规证明 |
| Validator | 独立重算路线、时间窗、容量、几何、承重和卸货顺序约束 | 优化方案、修补非法计划、信任 Solver 的中间结论 |
| Guardian | 持久化状态、校验版本、协调求解、审批、effect、恢复和审计 | 让 HTTP handler 或前端成为业务状态权威 |
| 调度员 | 比较人工基线与候选、检查三维装载、批准或驳回 | 绕过 Validator 批准非法或过期计划 |

NIST AI RMF 强调需要清楚区分人机角色和决策责任。这里把模型限制为解释和编排，把约束判定、
审批和外部写入留给确定性组件和人员。[R19]

### 1.3 48 小时范围

首版应支持：

- 单仓发车、同仓返回。
- 异构车辆、车辆数量上限、载重、车厢尺寸、车辆技能。
- 配送订单、服务时长、硬时间窗、货物尺寸、重量、允许旋转、易碎和最大承重。
- 后门卸货的严格分区顺序。
- 车辆固定成本、里程成本和工时成本。
- 人工规则基线、优化候选、独立校验、审批执行和完整回放。
- 地图路线、车辆指标、三维装载和逐站卸货演示。

首版不承诺：

- 全局最优性证明。
- 动态交通下的连续在线重优化。
- 多仓、中途取货、司机排班法规和充电路径。
- 任意形状货物、托盘重组、真实材料受力仿真和车辆动态稳定性认证。
- 仅凭三维画面直接生成可用于生产装车的安全指令。

## 2. 问题定义

### 2.1 联合优化问题

输入包含配送订单集合、货物集合、可用车辆集合、仓库、冻结的距离和行驶时间矩阵，以及版本化
策略。输出同时回答三个问题：

1. 哪些订单由哪辆车配送。
2. 每辆车以什么顺序访问客户，何时到达和服务。
3. 每件货物以什么朝向放在车厢的哪个坐标，如何按配送顺序卸货。

VRPTW 要求每个客户在时间窗内开始服务，路线不能超过车辆容量，并在车辆数量限制下优化路线。
DIMACS 将时间窗定义为硬约束，并使用独立 Controller 检查可行性，这支持本方案将 Validator
设为求解器外的准入门禁。[R4] OR-Tools 官方文档也把时间、载荷等累积量建模为
dimension，但它没有提供三维装载可行性。[R6][R7]

### 2.2 硬约束

任何一项违反都使候选不可审批：

| 类别 | 首版规则 |
|---|---|
| 订单完整性 | 每个必送订单恰好出现一次；同一订单的货物首版不拆车 |
| 车辆资格 | 车辆处于可用状态，技能、区域和货物类型兼容 |
| 路线闭合 | 从指定仓库出发并返回；访问序列、矩阵索引和订单一致 |
| 时间窗 | 根据冻结时间矩阵、等待时间和服务时长逐站前推，服务开始时间不得晚于窗口结束 |
| 工时 | 路线结束时间不超过车辆班次结束时间 |
| 重量 | 车上货物总重不超过车辆最大载重 |
| 边界 | 每件货物旋转后完全位于车厢内部 |
| 朝向 | 只使用货物声明允许的正交旋转 |
| 不重叠 | 任意两件货物的开区间体积不得相交；接触面允许相等 |
| 支撑 | 非落地货物的底面支撑比例达到策略阈值 |
| 承重 | 压在某件货物上的总重量不超过其最大承重 |
| 易碎 | `fragile_top_only` 货物上方不得放置其他货物 |
| 卸货顺序 | 首版使用后门严格分区，先送货物必须比后送货物更靠近车门 |
| 输入绑定 | 计划、校验报告、审批绑定同一个问题和策略摘要 |

首版把车门定义在 `x=0` 平面，`x` 轴沿车厢深度向内。若订单 A 比订单 B 先配送，则 A 的
装载分区最大 `x` 不得大于 B 的装载分区最小 `x`。该规则比真实侧向通道更保守，但易于解释、
独立校验和演示。赛后可升级为基于提取走廊的可达性检查。

### 2.3 软目标

对通过硬约束检查的计划使用字典序目标，不把不同量纲粗暴相加：

```text
objective = (
  unassigned_required_orders,
  hard_violation_count,
  vehicles_used,
  total_cost_cents,
  total_distance_meters,
  total_wait_seconds,
  negative_min_vehicle_volume_utilization_ppm
)
```

前两项必须为零才可审批。剩余项按顺序比较，确保求解器不会为了少跑里程而漏单，也不会为了提高
装载率而增加车辆。若业务要改变优先级，必须发布新的 `policy_version`，不能由 Agent 临时调整。

## 3. 总体架构

```text
异常处置入口 ------------------------------+
                                            |
配送订单 / 车辆 / 路网矩阵                  |
        |                                   |
        v                                   v
  Snapshot Builder                    existing guardian
  - schema validate                   - incident handling
  - normalize units                   - Agent pause/resume
  - freeze matrices                   - approval/effect
  - canonical digest                  - audit/SSE/recovery
        |
        +---------------------------+
                                    |
                                    v
                         Optimization Coordinator
                         - solve lease/state machine
                         - baseline and solver calls
                         - artifact references
                         - event emission
                            |             |
                            v             v
                    Deterministic       Independent
                    Solver              Validator
                    - routing           - recompute all
                    - 3D packing          hard constraints
                    - local search      - violation report
                            |             |
                            +------+------+
                                   |
                                   v
                          Candidate Comparison
                          - baseline delta
                          - map and 3D views
                          - evidence summary
                                   |
                                   v
                         existing approval/effect
                         - version-bound decision
                         - idempotent TMS writes
                         - audit and reconciliation
```

### 3.1 模块边界

建议新增以下包，名称可在实现阶段按仓库惯例调整：

```text
internal/optimization/
  domain/       问题、计划、目标值和版本类型
  snapshot/     归一化、schema 校验、矩阵冻结和摘要
  baseline/     人工规则基线
  routing/      Go 原生路线构造、局部搜索和路线校验
  packing/      gopackx adapter、配送顺序约束和装载候选
  validate/     不依赖 solver/packing 的独立校验器
  coordinator/  状态机、恢复、事件和 artifact 引用
  metrics/      固定标签指标
```

`internal/guardian` 仍是 HTTP 层的业务入口。它可以组合异常处置和配送优化用例，但审批、
effect、幂等和审计只保留一套实现。现有异常运单页面、总览投影和 SSE 事件不改变语义。

### 3.2 可替换求解器接口

```go
type Solver interface {
	Name() string
	Version() string
	Solve(ctx context.Context, problem ProblemSnapshot, cfg SolveConfig) (SolveResult, error)
}

type Packer interface {
	Name() string
	Version() string
	Pack(ctx context.Context, request PackingRequest) (PackingResult, error)
}

type Validator interface {
	Name() string
	Version() string
	Validate(ctx context.Context, problem ProblemSnapshot, plan Plan) ValidationReport
}
```

接口以版本化值对象为边界，不暴露求解器内部节点、缓存或随机数生成器。将来接入 OR-Tools、
PyVRP、VROOM 或其他装载器时，只新增 adapter，不改变审批和执行契约。

## 4. 版本化数据契约

### 4.1 通用规则

所有进入摘要的数据必须满足：

- 长度使用整数毫米，重量使用整数克，时间使用 UTC 时间戳或整数秒。
- 金额使用整数分，比例使用整数 ppm。
- 数组按稳定 ID 排序，枚举值显式版本化。
- 不使用 `NaN`、无穷值或依赖平台舍入的浮点数。
- JSON Schema 使用 2020-12 dialect 做边界校验。[R16]
- 摘要使用 RFC 8785 JCS 规范化后计算 SHA-256。[R17]
- 所有参与 JCS 数值都限制在 JSON/ECMAScript 安全整数范围内。

### 4.2 问题快照

```go
type ProblemSnapshot struct {
	SchemaVersion   string
	ProblemID       string
	ProblemVersion  uint64
	CreatedAt       time.Time
	Depot           Depot
	Orders          []DeliveryOrder
	Cargo           []CargoItem
	Vehicles        []Vehicle
	DistanceMatrixM []int64
	TravelMatrixS   []int64
	Policy          OptimizationPolicy
	SourceRefs      []SourceRef
	ProblemDigest   string
}
```

核心对象至少包含：

```go
type DeliveryOrder struct {
	OrderID         string
	LocationID      string
	TimeWindowStart time.Time
	TimeWindowEnd   time.Time
	ServiceSeconds  int64
	RequiredSkills  []string
	Priority        int
	CargoIDs        []string
}

type CargoItem struct {
	CargoID           string
	OrderID           string
	LengthMM          int64
	WidthMM           int64
	HeightMM          int64
	WeightG           int64
	AllowedRotations  []Rotation
	FragileTopOnly    bool
	MaxTopLoadG       int64
	MinSupportPPM     int64
}

type Vehicle struct {
	VehicleID        string
	LengthMM         int64
	WidthMM          int64
	HeightMM         int64
	MaxPayloadG      int64
	AvailableFrom    time.Time
	AvailableUntil   time.Time
	Skills           []string
	FixedCostCents   int64
	DistanceCostCPKM int64
	DriveCostCPH     int64
}
```

距离和时间矩阵是问题事实的一部分。求解期间不得重新请求地图服务，否则同一问题版本会因实时路况
变化得到无法复现的结果。新路况必须创建 `problem_version + 1`。

`ProblemDigest` 对清空自身摘要字段后的规范化快照计算。矩阵必须同时记录节点顺序，不能只保存
扁平数值数组。

### 4.3 求解配置

```go
type SolveConfig struct {
	SchemaVersion      string
	SolverVersion      string
	PolicyVersion      string
	Seed               uint64
	MaxMoveEvaluations uint64
	MaxPackAttempts    uint64
	MaxCandidates      uint32
	HardDeadline       time.Duration
}
```

Coordinator 先对不含 `Seed` 的求解器版本、策略和预算字段计算 `base_config_hash`，再从
`problem_digest + base_config_hash` 派生 `Seed`，最后计算包含 Seed 的完整配置摘要。正常终止
依赖固定的评估次数，不能依赖机器快慢决定搜索轮数。`HardDeadline` 只用于资源保护，触发时
返回 `aborted`，不能把当时偶然找到的计划伪装成可复现终态。

### 4.4 计划输出

```go
type Plan struct {
	SchemaVersion    string
	PlanID           string
	ProblemDigest    string
	PolicyVersion    string
	SolverName       string
	SolverVersion    string
	SolverConfigHash string
	Routes           []VehicleRoute
	Unassigned       []UnassignedOrder
	Objective        ObjectiveVector
	PlanDigest       string
}

type VehicleRoute struct {
	VehicleID       string
	Stops           []RouteStop
	Placements      []Placement
	DistanceMeters  int64
	DriveSeconds    int64
	VolumeUsedPPM   int64
	PayloadUsedPPM  int64
}

type Placement struct {
	CargoID      string
	XMM          int64
	YMM          int64
	ZMM          int64
	LengthMM     int64
	WidthMM      int64
	HeightMM     int64
	Rotation     Rotation
	DeliveryRank uint32
}
```

`PlanDigest` 对去除 `PlanID` 和自身摘要字段后的完整规范化计划计算，再用
`UUIDv5(plan_namespace, plan_digest)` 生成 `PlanID`。求解时间、进程 ID、日志文本和非确定性
统计不得进入摘要。

### 4.5 校验报告

```go
type ValidationReport struct {
	SchemaVersion    string
	ProblemDigest    string
	PlanDigest       string
	ValidatorName    string
	ValidatorVersion string
	Valid            bool
	Violations       []Violation
	Metrics          PlanMetrics
	ReportDigest     string
}
```

每个 `Violation` 包含稳定 `code`、对象引用、期望值和实际值，不使用只有人能理解的自由文本作为
程序判断依据。审批必须绑定 `ReportDigest`，且 `Valid=true`。

### 4.6 Artifact 与审计分离

问题快照、计划和校验报告可能较大，应按摘要写入不可变 artifact store。审计事件只保存：

```text
artifact_type
schema_version
digest
storage_ref
size_bytes
created_at
```

审计仍保存业务状态变化和哈希链，不把大矩阵或全部三维坐标复制进每个事件。读取 artifact 时必须
重新计算摘要，摘要不匹配则进入 `manual_review`。

## 5. 确定性算法管线

### 5.1 总流程

```text
原始订单和运力
  -> schema 与业务字段校验
  -> 单位归一化和稳定排序
  -> 冻结距离/时间矩阵
  -> 生成问题摘要
  -> 生成人工规则基线
  -> 构造初始可行路线
  -> 为每条路线做极点式三维装载
  -> relocate / swap / 2-opt 局部搜索
  -> 只重算受影响车辆的时间表和装载
  -> 保留字典序更优且装载可行的候选
  -> 独立 Validator 全量复核
  -> 与人工基线比较
  -> 生成审批候选
```

### 5.2 人工规则基线

基线不是随意编造的差方案，而是一个可复现的调度规则：

1. 按区域、时间窗结束时间、优先级、订单 ID 稳定排序。
2. 对每个订单按当前路线增量距离选择车辆。
3. 车辆必须通过技能、时间窗、重量和三维装载检查。
4. 容量使用率超过策略软阈值后优先开启下一辆车。
5. 同分时按车辆 ID 和订单 ID 选择。

基线也必须经过同一个独立 Validator。页面可以同时展示：

- `rule_baseline`：上述算法生成的规则基线。
- `uploaded_human_plan`：调度员实际方案，可选，同样必须版本化和校验。

这一区分避免把一个刻意弱化的程序基线冒充真实人工水平。

### 5.3 初始路线

首版使用带约束的 Clarke-Wright savings 或顺序插入法生成初始路线。Clarke-Wright 方法从单客户
路线开始，根据合并节省逐步构造车辆路线，原论文可作为实现依据。[R3]

每次插入按以下顺序检查：

1. 路线时间窗和工时。
2. 车辆技能与总重量。
3. 该车辆订单集合的三维装载。
4. 字典序目标变化。

重量和体积只适合快速剪枝，不能替代三维可装载性判断。

### 5.4 三维装载

装载器通过 `Packer` 接口调用固定版本的 `gopackx` 极点引擎，项目代码控制输入排序、允许旋转、
车辆分区和预算：

1. 按配送顺序分区，再按底面积、重量、高度和 cargo ID 稳定排序。
2. 初始候选点为每个分区的左下后角。
3. 放置货物后，从其正向边界生成新的极点。
4. 删除越界、被占用或被其他候选支配的点。
5. 枚举允许旋转，按空间增量、支撑率、重心高度和坐标稳定排序。
6. 每次放置立即检查边界、不重叠、支撑、承重、易碎和分区顺序。

极点概念适用于三维装箱，并允许加入固定位置等额外约束。[R2] `gopackx` 只生成候选。项目代码
仍要检查装载分区、卸货扫掠和后续支撑关系，并把候选交给独立 Validator。[R22]

卸货不能简化为一次逆序排序。对每个停靠点依次移除当前订单货物，并检查：

- 当前货物沿车门方向的直线扫掠体不与后续订单货物相交。
- 当前货物上方没有后续订单货物。
- 当前货物不是后续订单货物的必要支撑。
- 移除当前货物后，剩余货物仍满足支撑率、承重和易碎规则。

首版只有后门，车门位于 `x=0`。多门、叉车转弯和机械臂路径规划不在 48 小时范围内。

### 5.5 联合局部搜索

固定预算内按稳定顺序评估：

- `relocate(order, routeA, routeB, position)`
- `swap(orderA, orderB)`
- `two_opt(route, i, j)`
- `intra_route_relocate(order, position)`

移动涉及哪些车辆，就只重算这些车辆的路线时间和装载。只有全部受影响车辆都可行，并且目标向量
严格改善时才接受。候选摘要相同则按规范化 plan body 的字节序打破平局。

装载失败需要返回稳定原因码，例如：

```text
vehicle_payload_exceeded
item_no_allowed_rotation
no_extreme_point_fit
support_ratio_below_minimum
top_load_exceeded
fragile_item_blocked
unload_partition_conflict
```

Solver 可以用这些原因剪枝，但最终审批只信任 Validator 的报告。

### 5.6 备选候选

首版最多保留三类经过校验的候选：

- 最少车辆。
- 最低总成本。
- 最均衡装载。

它们必须来自同一问题和策略版本。页面明确显示目标差异，不让 Agent 自行给候选冠以 `最优`。

## 6. 独立约束校验

### 6.1 独立性的含义

Validator 与 Solver 可以共享 `domain` 类型和整数几何基础类型，但不得：

- 调用 Solver 的 `CanInsert`、packing cache 或可行性函数。
- 信任计划内的利用率、到达时间、总里程或目标值。
- 在发现错误后自动移动货物或调整路线。
- 因为计划来自受信求解器而跳过字段校验。

Validator 从问题快照和计划原始字段重新计算：

1. ID 完整性、重复和归属。
2. 路线距离、时间、等待和服务开始时间。
3. 车辆资格、重量和工时。
4. 旋转后尺寸与车厢边界。
5. 三维轴对齐盒体相交。
6. 底面支撑面积、上方重量和易碎规则。
7. 后门分区及卸货顺序。
8. 全部指标和目标向量。

### 6.2 几何判定

两个轴对齐长方体只有在三个轴的开区间都相交时才重叠：

```text
overlap =
  ax0 < bx1 && bx0 < ax1 &&
  ay0 < by1 && by0 < ay1 &&
  az0 < bz1 && bz0 < az1
```

使用整数毫米避免浮点误差。支撑面积由货物底面与所有直接接触支撑面的矩形并集计算，不能简单把
重叠面积相加，否则支撑区域相互重叠时会重复计数。

### 6.3 审批门禁

进入审批必须同时满足：

```text
report.valid == true
report.problem_digest == plan.problem_digest
report.plan_digest == plan.plan_digest
report.validator_version in allowed_versions
plan.policy_version == current_required_policy_version
problem is not stale
```

Validator 失败时保存报告和失败事件，但不得创建可确认审批。

## 7. 求解事件与状态机

### 7.1 状态机

```text
draft
  -> snapshotted
  -> baselining
  -> solving
  -> validating
  -> awaiting_approval
  -> executing
  -> completed

baselining / solving / validating -> infeasible
                               |-> failed
                               `-> aborted
awaiting_approval -> rejected
                  |-> expired
                  `-> superseded
executing -> partially_failed
          |-> reconciliation_required
          `-> failed
```

`infeasible` 表示确定性管线在给定预算和首版约束下没有找到可审批方案，不等于数学上证明不存在
任何可行解。只有穷举或精确求解器给出证明时才能使用 `proven_infeasible`。

### 7.2 内部审计事件

建议追加以下 typed events：

```text
optimization_snapshot_created
optimization_baseline_completed
optimization_solve_started
optimization_candidate_improved
optimization_solve_completed
optimization_validation_completed
optimization_approval_requested
optimization_approval_decided
optimization_execution_started
optimization_effect_result
optimization_run_completed
optimization_run_failed
optimization_run_superseded
```

`candidate_improved` 只记录 incumbent 改善，不记录每次移动。事件包含候选序号、目标值和 artifact
摘要，避免审计日志被搜索过程淹没。SSE 直接复用现有 replay 加 live 通道。

### 7.3 对外事件

需要通过 outbox 发布时，使用 CloudEvents 1.0.2 信封；`source + id` 唯一，业务 payload 自行
版本化。[R18] 示例类型：

```text
com.waybill.optimization.solve.completed.v1
com.waybill.optimization.approval.requested.v1
com.waybill.optimization.dispatch.completed.v1
```

内部审计事件和对外 CloudEvent 不要求一一对应。对外事件只在业务事务提交后由 outbox 发布。

## 8. Agent、审批与执行

### 8.1 Agent 工具

建议为 Agent 暴露窄工具，而不是让模型提交完整计划：

```text
dispatch.get_problem_summary       只读
dispatch.request_optimization      只读业务计算
dispatch.get_candidate             只读
dispatch.explain_candidate         只读确定性指标
dispatch.commit_plan               写，强制人工审批
```

`request_optimization` 只接受场景 ID 和已注册策略，不接受模型生成的坐标、距离矩阵或任意权重。
`explain_candidate` 返回服务端计算的基线差异和违规状态，模型负责转成自然语言。

### 8.2 审批绑定

审批项至少绑定：

```text
problem_digest
plan_digest
validation_report_digest
baseline_plan_digest
policy_version
solver_name + solver_version + solver_config_hash
validator_name + validator_version
artifact_refs
effect_ids
expires_at
```

确认时重新读取当前订单、车辆和矩阵版本。任一权威输入变化则把审批标记为 `superseded`，创建新
问题版本重新求解，不允许确认旧方案后静默套用新数据。

### 8.3 执行 effect

审批通过后生成：

- 一个计划级 `dispatch.commit_plan` 意图。
- 每条车辆路线一个 `tms.create_or_update_route` effect。
- 每辆车一份版本化装载清单 effect。
- 必要的司机和客户通知 effect。

`effect_id` 从审批绑定的 plan digest、action、target 和 canonical arguments 派生。重试使用同一
幂等键和同一参数。部分成功时进入现有 `partially_failed` 或
`reconciliation_required` 路径，不能把整个计划简单标记为未执行。

执行前 guard 再检查审批状态、参数摘要和计划状态。Agent history、浏览器参数和请求 header
不能提供 effect ID、幂等键或审批人身份。

### 8.4 重优化

车辆故障、临时订单或路况变化产生新 `ProblemSnapshot`。新计划不得覆盖旧计划：

```text
problem v1 -> plan A -> approved -> executing
problem v2 -> plan B -> awaiting_approval
```

已经执行的停靠点和车上实际货物在 v2 中作为 pinned facts。新审批明确列出相对已执行计划的变更。

## 9. 恢复语义

### 9.1 求解恢复

48 小时版本不序列化复杂搜索栈。持久化：

- 问题、配置和摘要。
- 当前业务状态。
- 基线、已完成候选和校验报告 artifact。
- 固定 seed 和评估预算。

进程在 `solving` 中退出后，恢复器以相同输入重新运行确定性求解。稳定事件 ID 由
`optimization_run_id + event_type + candidate_sequence + digest` 派生，重复结果不会造成两个
逻辑事件。

### 9.2 各阶段恢复

| 崩溃位置 | 恢复动作 |
|---|---|
| 快照写入前 | 从权威输入重新创建新快照 |
| 快照已写、求解未完成 | 校验 artifact 摘要后，以同配置重跑 |
| 计划已写、校验未写 | 运行独立 Validator |
| 校验已写、审批未写 | 幂等创建绑定同一 digest 的审批 |
| 审批待处理 | 复用现有审批恢复和过期机制 |
| 已确认、effect 未准备 | 在事务边界幂等补写 effect |
| effect 已发送、结果未知 | 按原幂等键对账，禁止换 key 重发 |
| 全部 effect 成功、终态未写 | 根据 effect 投影补写完成事件 |

artifact 丢失、摘要不匹配、版本不可执行或外部结果无法确定时进入 `manual_review`，其他 run
继续恢复。

## 10. 指标与可观测性

### 10.1 业务指标定义

同一问题版本下比较人工基线和优化计划：

| 指标 | 定义 |
|---|---|
| 车辆数 | `len(routes with at least one order)` |
| 总里程 | 按冻结距离矩阵累加所有路线边 |
| 总成本 | 车辆固定成本 + 里程成本 + 驾驶工时成本 |
| 准时率 | 在窗口结束前开始服务的订单数 / 已分配订单数 |
| 体积装载率 | 货物体积和 / 已用车辆车厢体积和 |
| 重量装载率 | 货物重量和 / 已用车辆载重和 |
| 最低单车装载率 | 已用车辆中最低的体积装载率 |
| 等待时间 | 早到后等待窗口开启的总秒数 |
| 求解耗时 | 从 solve started 到 completed 的单调时钟耗时 |
| 校验耗时 | Validator 全量检查耗时 |

求解耗时用于性能观测，不进入 plan digest 或确定性目标。

成本舍入也属于策略契约。首版按车辆分别计算
`ceil(distance_meters * distance_cost_cpkm / 1000)` 和
`ceil(drive_seconds * drive_cost_cph / 3600)`，再与固定成本求和；Solver 和 Validator 必须使用
分别实现但相同版本的公式。

### 10.2 Prometheus

遵循 Prometheus 对在线服务、分阶段处理和库的请求数、错误数、延迟及在途数量建议。[R20]
建议指标：

```text
optimization_runs_total{solver,status,size_bucket}
optimization_runs_in_progress{solver}
optimization_solve_duration_seconds{solver,status,size_bucket}
optimization_validation_duration_seconds{validator,status,size_bucket}
optimization_move_evaluations_total{solver,move_type}
optimization_candidates_total{solver,outcome}
optimization_validation_violations_total{validator,code}
optimization_approval_total{decision,stale}
optimization_effect_total{action,status}
optimization_recovery_total{stage,outcome}
```

标签不能包含 `problem_id`、`plan_id`、订单 ID、车辆 ID 或客户信息。基线和候选的详细业务指标
放在计划投影和审计中，不作为高基数 Prometheus 标签。

### 10.3 Trace 与日志

建议 span：

```text
optimization.build_snapshot
optimization.generate_baseline
optimization.solve
optimization.pack_route
optimization.validate
optimization.await_approval
optimization.execute_effect
```

OpenTelemetry GenAI agent 和 framework span 语义目前标记为 Development，因此可参考
`invoke_agent`、`plan` 和 `execute_tool` 命名，但稳定业务字段保留在项目命名空间。[R21]
模型 prompt、货物明细和客户地址默认不写入 trace。

结构化日志记录 run ID、solve ID、版本、摘要前缀、阶段、耗时和低基数错误码。审计记录谁批准了
什么，trace 和日志记录系统如何执行，两者不能互相替代。

### 10.4 演示验收目标

以下是需要实测的目标，不是当前已取得的结果：

- 固定演示数据集包含 1 个仓库、30 个门店、8 至 12 辆车和 300 个 SKU 行项目。
- 指定参考机器上，规则基线在 1 秒内完成，优化和全量校验在 10 秒内完成。
- 同一输入连续运行 20 次，plan digest 完全一致。
- 优化计划硬约束违规为零，必送订单准时率为 100%。
- 优化目标按字典序不差于有效基线。
- 演示数据目标为至少少用 1 辆车，或总里程降低 10%，同时保持准时率。

最终文档必须记录参考机器、数据摘要、预算和实测分布，不能只展示单次最好结果。

## 11. 前端方案

### 11.1 信息架构

保留现有总览和异常运单处置，新增 **智能配载** 入口：

1. **任务输入**：订单数、货物数、车辆数、时间窗风险和数据版本。
2. **基线对比**：人工规则基线与候选的车辆数、里程、成本、准时率、装载率和求解耗时。
3. **路线地图**：按车辆着色，显示停靠顺序、时间窗、预计到达和风险。
4. **三维车厢**：显示货物坐标、朝向、订单、配送站和支撑状态。
5. **审批卡片**：展示版本、Validator 状态、指标差异、影响范围和执行动作。
6. **审计时间线**：通过现有 SSE 回放快照、基线、求解、校验、审批和执行事件。

### 11.2 三维交互

路线地图继续使用现有 MapLibre 能力，车厢局部坐标单独使用 R3F。两者由同一个离散
`stepIndex` 驱动，拖回任意步骤都必须得到相同状态。不要把 WGS84 地理坐标和毫米级车厢坐标
放在一个 Three.js 场景中。

现有 `HubNetworkScene.tsx` 的相机、缩放和降级经验可复用于车厢视图，但业务语义需要重建：

- 货物按配送站着色，不按随机颜色。
- 支持车辆切换、等距、俯视、车尾、左侧相机预设和复位。
- 提供隐藏车顶、隐藏近侧壁和恢复车厢操作，默认不使用全车厢半透明叠加。
- 点击货物显示尺寸、重量、旋转、支撑率和配送站。
- **逐站卸货** 隐藏当前站已卸货物，验证后续货物是否可见。
- **装车顺序** 按逆配送顺序播放。
- 易碎、低支撑和承重接近阈值使用明确图例。
- WebGL 不可用时展示确定性的顶视和侧视 SVG，不阻断审批信息。

同类型箱体复用 geometry 和 material，超过 100 件后使用 `InstancedMesh`。实例拾取必须把
`instanceId` 映射回稳定 `cargo_id`。静止时采用 `frameloop="demand"`，只在回放、相机变化或
业务状态变化时重绘。R3F 官方性能指南明确推荐按需渲染、资源复用和实例化。[R29]

移动端使用 **路线** 和 **装载** 两个标签页，不同时运行两个 WebGL 画布。所有操作都提供 DOM 按钮，
货物和站点有可键盘访问的列表，列表选择与画面选择双向同步。`prefers-reduced-motion` 下禁用
自动播放、轨迹拖尾和相机缓动。R3F 的 Canvas fallback 可承载 WebGL 初始化失败后的替代视图。
[R30]

页面必须区分 `算法布局` 与 `已批准执行`。Validator 未通过、数据过期或计划未批准时，三维
画面显示明显状态，不使用容易误解为已下发的文案。

### 11.3 审批页面

审批人首先看到：

- `0 项硬约束违规` 或阻断原因。
- 与基线相比的绝对值和百分比变化。
- 使用车辆和未分配订单。
- 输入、策略、求解器、Validator 和计划版本。
- 将写入 TMS 的 effect 清单。

自然语言解释位于确定性指标之后。模型解释与指标冲突时，以服务端指标和 Validator 为准。

## 12. 候选技术、许可证与 48 小时风险

核验日期为 2026-10-10。版本和许可证以候选官方仓库、官方发布页和仓库 LICENSE 为准。

| 候选 | 核验版本 | 许可证 | 能力与缺口 | 48 小时判断 |
|---|---:|---|---|---|
| 项目内 Go 路线求解器 | 项目版本化 | Apache-2.0 | 能按本题约束实现插入和局部搜索，并在移动评估中调用装载子问题 | **P0 选择**。无跨语言运行时，范围可控 |
| gopackx | v0.3.0 | MIT | Go 原生，整数几何、极点/LAFF/VNS、旋转、支撑率、易碎、累计承重、`context` 和确定性结果；无路线和 LIFO | **P0 有条件选择**。必须先通过两小时技术门禁，Validator 不复用其判定 |
| PyVRP | v0.14.0 | MIT | 支持固定 seed、时限、时间窗、服务时长、异构车队、固定成本和多维容量；无三维装载 | **离线基准**。不进入默认运行路径 |
| VROOM | v1.15.0 | BSD-2-Clause | JSON 输入输出，支持 CVRP、VRPTW、异构车队、多维容量、技能、固定成本和自定义矩阵；无三维装载和公开 seed | **赛后首个路线 adapter**。可用单线程和固定输入做重放验收 |
| Google OR-Tools | v9.15 | Apache-2.0 | 路由约束表达成熟，官方支持 C++、Python、Java、C#；无官方 Go binding，无三维装载 | 赛后用于需要自定义路线约束的场景 |
| xflp | v0.7.7 | MIT | Java 21，极点加 GRASP，支持 LIFO、承重、堆叠组、装卸地点和双轴轴载；输出三维放置 | 能力最贴题，但再引入 JVM 会拖慢初赛交付，作为外部对照 |
| skjolber/3d-bin-container-packing | 4.2.x | Apache-2.0 | Java 17 源码目标，LAFF、Plain、Brute Force、障碍物和可视化；没有内建 LIFO、易碎和累计承重 | 放弃作为默认依赖。补约束和 JVM 接入的成本高 |
| felicze/3l-cvrp | 2025 论文仓库 | MIT | 精确 branch-and-cut 加 OR-Tools CP-SAT，覆盖旋转、支撑、易碎和 LIFO；依赖 Gurobi，官方说明是研究代码 | 只用于算法和数据参考，不能进入 48 小时运行时 |
| SolutionValidator | 2022 最后提交 | GPL-3.0 | 独立检查 2L/3L-CVRP、LIFO、易碎、支撑、承重、轴载和静态稳定 | 可在隔离研究环境作外部 oracle，不嵌入 Apache-2.0 运行时 |
| Nextroute | v1.12.7 | BSL 1.1 | Go 原生，约束和未规划原因解释较强 | 不采用。当前许可证不是开源许可证，产品化需要重新授权 |
| NVIDIA cuOpt | v26.08 | Apache-2.0 | GPU 路由求解器，支持 Python 和 Server API | 不采用。要求 Linux、NVIDIA GPU、CUDA 12 或 13 |
| gedex/bp3d | 2024 最后提交 | MIT | Go 三维装箱，接口简单；使用 `float64`，缺少卸货顺序、支撑和承重契约 | 不直接依赖。只作最小算法对照 |
| enzoruiz/3dbinpacking | v1.1.2 | MIT | Python 三维装箱，支持箱体、货物、最大重量和小数精度；缺少卸货顺序、支撑、易碎和累计承重契约 | 不采用。约束不足，且会增加 Python 运行时 |
| binpack3d | v0.1.1 | MIT | TypeScript，旋转、易碎、支撑和可视化友好；项目创建于 2026-06，采用量很低 | 只适合前端原型，不作为服务端可行性裁判 |

证据：

- OR-Tools 官方安装页只列 C++、Python、Java 和 C#，路由文档展示 VRPTW 与 dimension。
  [R6][R7][R8][R9]
- PyVRP 官方能力清单包含时间窗、服务时长、异构车队和多维容量，但没有三维装载。
  [R10]
- VROOM 官方 README 列出 CVRP、VRPTW、MDHVRPTW、技能、工时和自定义矩阵。
  [R11]
- Timefold 官方 FAQ 给出 Apache-2.0、Java 21 基线和人工 pinning 方式。[R12]
- 装箱和联合求解候选的语言、约束、许可证与依赖见各自官方 README 和 LICENSE。
  [R13][R14][R15][R22][R25][R26][R27][R28]

### 12.1 推荐的赛后替换路径

赛后先接入 VROOM adapter。若出现 VROOM 无法表达的路线约束，再评估 OR-Tools：

```text
VROOM or OR-Tools route master
  -> route/order-set candidate
  -> custom Go packing subproblem
  -> infeasible conflict feedback
  -> next route candidate
  -> independent Go Validator
```

VROOM 的 JSON 契约和自定义矩阵能降低 adapter 成本。OR-Tools 的约束表达和长期维护性更强，
但需要 C++、Python 或 Java sidecar。两者都不能直接解决三维联合问题，也不能替代项目自有
Validator。

### 12.2 明确放弃的方案

| 方案 | 放弃原因 |
|---|---|
| 让 LLM 直接输出路线和三维坐标 | 无法提供确定性、完整约束证明和稳定重放；错误可能具有物理风险 |
| 路线优化后再做一次装箱，失败就人工处理 | 路线选择没有感知可装载性，容易得到距离好但无法装车的方案 |
| 只按总体积和总重量判断可装载 | 不能发现尺寸、旋转、重叠、支撑、承重和卸货顺序问题 |
| 48 小时内接入完整 OR-Tools/PyVRP/VROOM 服务 | 仍需三维子问题、adapter、部署和恢复，风险高于 Go 原生受限实现 |
| 使用 bp3d 直接作为生产可行性证明 | 数据类型和约束集合不满足本题，且缺少独立 Validator |
| 把 gopackx 的返回结果直接视为可审批 | 它不处理路线和卸货可达性，且新项目风险需要项目自有 Validator 隔离 |
| 用 Nextroute 作为默认路线库 | BSL 1.1 不是开源许可证，竞赛后的生产使用边界不符合默认依赖要求 |
| 用 CP-SAT/MILP 建完整路线加三维精确模型 | 建模、对称性处理、求解时限和不可行性解释均超出 48 小时 |
| 复用 Solver 的可行性函数做 Validator | 同一实现错误会同时污染生成和验收，失去独立门禁价值 |

## 13. 验证计划

### 13.1 契约测试

- JSON Schema 接受合法 v1，拒绝未知字段、非法枚举、负尺寸和越界整数。
- 同一语义的不同 JSON 字段顺序得到同一 digest。
- 任意业务字段变化都会改变对应 artifact digest。
- plan、report 和 approval 的 digest 链无法错配。
- v1 reader 明确拒绝不支持的主版本，不做猜测性兼容。

### 13.2 Validator 单元与性质测试

至少覆盖：

- 货物刚好贴边合法，越界 1 mm 非法。
- 盒体只接触表面合法，重叠 1 mm 非法。
- 非允许旋转、重复货物、漏单和跨车重复非法。
- 时间窗边界相等合法，晚 1 秒非法。
- 超重 1 g、超工时 1 秒非法。
- 支撑率边界、重复支撑面积、悬空、承重和易碎规则。
- 先送货物被放在后送分区深处。
- 篡改 Solver 报告指标不影响 Validator 重算结果。

对每个合法 golden plan 自动生成单点变异，例如移动坐标、交换订单、修改到达时间或删除货物，
要求 Validator 捕获预期错误码。几何核心增加 Go fuzz test。

### 13.3 Solver 测试

- 同一 problem/config 连续运行得到相同 plan digest。
- 订单和车辆输入顺序变化不影响结果。
- 固定预算下评估次数一致。
- `relocate`、`swap`、`2-opt` 只接受严格改善且可行的移动。
- 受影响车辆增量重算结果与全量重算一致。
- Solver 生成的每个可审批候选都通过独立 Validator。
- 找不到方案时返回稳定原因，不把预算耗尽写成数学不可行。

### 13.4 基准数据

采用学术基准和赛事数据两条轨道：

- 路线层按需下载 Solomon VRPTW 和 Gehring-Homberger 实例，记录源 URL、下载日期和 SHA-256，
  不把来源条款不清楚的整套数据重新分发。DIMACS 官方说明涵盖 Solomon 100 客户和
  Homberger-Gehring 200 至 1000 客户实例。[R4][R5]
- 三维层使用 OR-Library `thpack` 数据和项目自有边界 fixture。OR-Library 明确允许按其 MIT
  条款使用和再分发，但 `thpack` 不包含易碎、支撑、承重和配送顺序，不能用它证明这些约束。
  [R31]
- 联合层保留 Gendreau 3L-CVRP 的 30 客户实例 `E031-09h` 作为学术锚点，只通过来源脚本下载。
  它的公开解可以用于核对问题定义，不能替项目自己的时间窗、成本和 300 个 SKU 数据背书。
  [R1][R25]
- 赛事数据 `city30-v1` 完全由仓库内固定 seed 生成器创建，包含 1 个仓库、30 个门店、300 个
  SKU 行项目、异构车辆、冻结矩阵、硬时间窗和全部装载属性。生成器保存版本、seed、字段来源、
  单位、缩放、舍入规则和最终摘要。

`city30-v1` 不能继承任何源基准的最优值。README 只写 `独立校验可行`、`相对规则基线改善`
或 `固定预算下当前最好结果`，不写全局最优。

### 13.5 集成与恢复测试

- snapshot -> solve -> validate -> approval -> effect -> completed 全链路。
- Validator 失败不能创建可确认审批。
- 审批等待期间输入版本变化，原审批变为 `superseded`。
- 相同确认重复提交只执行一次 effect。
- 在表 9.2 的每个阶段注入退出，重启后收敛到相同业务状态。
- 外部请求超时进入 `reconciliation_required`，不生成新幂等键。
- SSE 使用 `Last-Event-ID` 回放后无缺失、无逻辑重复。
- 异常运单处置和现有总览回归测试保持通过。

### 13.6 前端验收

- 地图路线、站序和指标与 plan artifact 一致。
- 三维盒体坐标与 Validator 输入一致。
- 逐站卸货顺序和后门方向正确。
- 路线回放支持上一步、下一步、播放、暂停、拖动和 0.5x/1x/2x，回退后货物恢复原坐标。
- 箱体违规同时显示颜色、文字原因和稳定错误码，不能只依赖颜色。
- WebGL 降级视图仍能完成候选比较和审批。
- 键盘操作、颜色图例和状态文本不只依赖颜色表达。
- 审批页面显示所有绑定版本和实际 effect。
- 300 件货物场景下连续旋转 10 秒无控制台错误，连续挂载和卸载 20 次无 WebGL context、
  监听器或动画循环泄漏。

## 14. 48 小时实施拆分

### 14.1 时间安排

| 时间 | 交付 |
|---|---|
| 0 至 4 小时 | 冻结 v1 问题、计划、校验报告 schema；完成演示数据和目标指标 |
| 4 至 12 小时 | 先实现独立 Validator、边界用例和变异测试 |
| 8 至 18 小时 | 并行实现人工规则基线、极点装载和初始路线 |
| 18 至 28 小时 | 实现 relocate/swap/2-opt、受影响车辆重算和固定预算 |
| 24 至 34 小时 | 接入 coordinator、事件、artifact、审批、effect 和恢复 |
| 28 至 40 小时 | 新增基线对比、路线地图、三维车厢和降级视图 |
| 40 至 46 小时 | 全链路、重启、幂等、SSE、性能和现有功能回归 |
| 46 至 48 小时 | 固化演示数据与摘要，录制证据，更新许可证和限制说明 |

### 14.2 并行工作流

为降低串行阻塞，可拆为四条边界清楚的工作流：

1. **契约与 Validator**：拥有 domain、schema 和 validate。
2. **Solver**：只依赖已冻结 domain，交付候选和 golden tests。
3. **Guardian 集成**：拥有状态机、artifact、事件、审批和 effect。
4. **Web**：消费固定 API fixture，待后端联调后切换真实响应。

任何 schema 变化由契约工作流统一合入，其他工作流不能各自新增同义字段。

### 14.3 子 Issue 边界

执行跟踪：

- Epic：[城市配送智能配载与调度闭环 #127](https://github.com/Duang777/waybill-guardian/issues/127)
- D1：[数据契约、赛事数据与独立 Validator #128](https://github.com/Duang777/waybill-guardian/issues/128)
- D2：[确定性 VRPTW 路线搜索与规则基线 #129](https://github.com/Duang777/waybill-guardian/issues/129)
- D3：[三维装载候选与逐站卸货约束 #130](https://github.com/Duang777/waybill-guardian/issues/130)
- D4：[Optimization Coordinator、artifact 与恢复 #131](https://github.com/Duang777/waybill-guardian/issues/131)
- D5：[Agent、版本绑定审批与幂等 TMS effect #132](https://github.com/Duang777/waybill-guardian/issues/132)
- D6：[智能配载工作台与三维装卸回放 #133](https://github.com/Duang777/waybill-guardian/issues/133)
- D7：[基准证据、双语 README 与双流程演示 #134](https://github.com/Duang777/waybill-guardian/issues/134)

| 工作包 | 主要范围 | 依赖 | 独立验收 |
|---|---|---|---|
| D1 契约、生成器与 Validator | `delivery_dataset.v1`、`delivery_plan.v1`、`validation_report.v1`、`city30-v1`、摘要和独立 Validator | 无 | schema、摘要、边界、变异和 fuzz 测试 |
| D2 路线求解与规则基线 | 顺序插入、relocate、swap、2-opt、固定预算、PyVRP 对照 | D1 | 同输入 20 次摘要一致，指标不差于有效基线 |
| D3 三维装载与卸货校验 | gopackx 技术门禁、Packer adapter、配送分区、卸货扫掠、支撑和承重 | D1 | 300 件货物、超时、回退、故障 fixture |
| D4 Optimization Coordinator | 状态机、artifact、事件、恢复、指标、HTTP 读写接口 | D1、D2、D3 | 阶段崩溃注入后收敛到相同状态 |
| D5 Agent、审批和 TMS effect | typed tools、证据绑定、`dispatch.commit_plan`、幂等和 freshness check | D4 | 未确认零写入，并发确认只写一次 |
| D6 智能配载工作台 | 基线对比、路线地图、车厢 3D、步骤回放、审批、SVG 降级 | D4 API fixture，可与 D5 并行 | 300 箱性能、键盘、降级和浏览器验收 |
| D7 基准、README 与演示 | 学术下载器、实测报告、中英文文档、讲稿、截图和视频 | D1 至 D6 | 链接、许可证、数据来源和双流程演示 |

每个子 Issue 都必须保留现有异常处置入口并运行现有 Go、Web、PostgreSQL、恢复和浏览器测试。

### 14.4 降级顺序

若时间不足，按以下顺序缩小范围：

1. 保留单仓、硬时间窗、异构车辆和三维几何。
2. 保留 Validator、审批、幂等、审计和恢复。
3. 将搜索移动缩减为 insertion + relocate。
4. 将候选数缩减为一个。
5. 将三维视图缩减为顶视、侧视和透视三个固定视角。

不能删除独立 Validator、版本绑定或审批来换取更多算法花样。

## 15. 主要风险与控制

| 风险 | 后果 | 控制 |
|---|---|---|
| 三维可行性误判 | 页面展示无法实际装载的方案 | 独立整数 Validator，非法计划禁止审批 |
| 搜索时间失控 | 演示超时或结果不稳定 | 固定评估预算、稳定 seed、硬 deadline 只中止 |
| 路网数据变化 | 结果不可复现、审批过期 | 冻结矩阵，新数据生成新 problem version |
| 目标函数被钻空子 | 漏单换取低成本 | 字典序目标，漏单和硬违规优先级最高 |
| 装载物理模型过简 | 实际运输安全风险 | 明确首版边界，人工审批，不宣称工程安全认证 |
| 计划批准后车辆变化 | 执行旧方案 | 确认时 freshness check，变化即 superseded |
| 外部写入结果未知 | 重复派车或通知 | 稳定 effect ID、同 key 对账、禁止换 key 重试 |
| Agent 幻觉 | 错误解释或非法参数 | 模型只调用窄工具，所有事实由服务端计算 |
| 3D 页面掩盖错误 | 视觉正确但数据错误 | UI 读取已校验 artifact，显示 digest 和 Validator 状态 |
| 新模块破坏现有主线 | 异常处置和总览回归 | 新业务域隔离，保留现有契约并运行全量回归 |
| 第三方许可证遗漏 | 提交合规风险 | 采用前核验 LICENSE，登记直接依赖与版本 |

## 16. 最终决策

48 小时版本选择项目内 Go 路线搜索、固定版本的 `gopackx` 候选装载器和项目自有 Validator。
这个组合同时满足：

- 可以原生复用现有 Guardian、审批、幂等、审计、SSE 和恢复能力。
- 可以把题目要求的路线和三维装载放在同一搜索闭环。
- 可以用固定预算、整数运算和稳定排序得到可回放结果。
- `gopackx` 隔离在 `Packer` 接口后，技术门禁失败时可以替换，不影响计划和审批契约。
- 独立 Validator 不复用第三方可行性判断，不让候选生成器自证。
- 不引入 Python、C++ 或 JVM 的新部署和故障边界。

赛后先评估 VROOM 路线 adapter，需要更复杂约束时再评估 OR-Tools。无论替换何种求解器或
装载器，版本化 I/O 和独立 Validator 都保留。Agent 不获得约束裁决权，人工审批也不能绕过
可行性门禁。

## 参考资料

以下仅列论文、标准、官方文档、官方仓库及维护者资料。

- **[R1] 论文**：Michel Gendreau, Manuel Iori, Gilbert Laporte, Silvano Martello,
  "A Tabu Search Algorithm for a Routing and Container Loading Problem,"
  *Transportation Science* 40(3), 2006.
  <https://doi.org/10.1287/trsc.1050.0145>
- **[R2] 论文**：Teodor Gabriel Crainic, Guido Perboli, Roberto Tadei,
  "Extreme Point-Based Heuristics for Three-Dimensional Bin Packing,"
  *INFORMS Journal on Computing* 20(3), 2008.
  <https://doi.org/10.1287/ijoc.1070.0250>
- **[R3] 论文**：G. Clarke, J. W. Wright,
  "Scheduling of Vehicles from a Central Depot to a Number of Delivery Points,"
  *Operations Research* 12(4), 1964.
  <https://doi.org/10.1287/opre.12.4.568>
- **[R4] 官方挑战文档**：DIMACS Implementation Challenge,
  "Vehicle Routing Problem with Time Windows."
  <https://dimacs.rutgers.edu/programs/implementation-challenges/vehicle-routing/implementation-challenge-vehicle-routing/vrp-with-time-windows>
- **[R5] 维护机构资料**：SINTEF，Solomon benchmark。
  <https://www.sintef.no/projectweb/top/vrptw/solomon-benchmark/>
- **[R6] 官方文档**：Google OR-Tools，"Vehicle Routing Problem with Time Windows."
  <https://developers.google.com/optimization/routing/vrptw>
- **[R7] 官方文档**：Google OR-Tools，"Dimensions."
  <https://developers.google.com/optimization/routing/dimensions>
- **[R8] 官方文档**：Google OR-Tools，"Install OR-Tools."
  <https://developers.google.com/optimization/install>
- **[R9] 官方仓库**：Google OR-Tools 仓库、v9.15 发布页和 Apache-2.0 许可证。
  <https://github.com/google/or-tools>
  <https://github.com/google/or-tools/releases/tag/v9.15>
  <https://raw.githubusercontent.com/google/or-tools/v9.15/LICENSE>
- **[R10] 官方文档与仓库**：PyVRP 文档、仓库、v0.14.0 发布页和 MIT 许可证。
  <https://pyvrp.org/>
  <https://github.com/PyVRP/PyVRP>
  <https://github.com/PyVRP/PyVRP/releases/tag/v0.14.0>
  <https://raw.githubusercontent.com/PyVRP/PyVRP/v0.14.0/LICENSE.md>
- **[R11] 官方仓库**：VROOM Project 仓库、v1.15.0 发布页和 BSD-2-Clause 许可证。
  <https://github.com/VROOM-Project/vroom>
  <https://github.com/VROOM-Project/vroom/releases/tag/v1.15.0>
  <https://raw.githubusercontent.com/VROOM-Project/vroom/v1.15.0/LICENSE>
- **[R12] 官方文档与仓库**：Timefold Solver FAQ、仓库、v2.7.1 发布页和
  Apache-2.0 许可证。
  <https://docs.timefold.ai/timefold-solver/latest/frequently-asked-questions>
  <https://github.com/TimefoldAI/timefold-solver>
  <https://github.com/TimefoldAI/timefold-solver/releases/tag/v2.7.1>
  <https://raw.githubusercontent.com/TimefoldAI/timefold-solver/v2.7.1/LICENSE.txt>
- **[R13] 官方仓库**：gedex/bp3d，README 和 MIT 许可证。
  <https://github.com/gedex/bp3d>
- **[R14] 官方仓库**：enzoruiz/3dbinpacking，README 和 MIT 许可证。
  <https://github.com/enzoruiz/3dbinpacking>
- **[R15] 官方仓库与维护者说明**：skjolber/3d-bin-container-packing，
  README 和 Apache-2.0 许可证。
  <https://github.com/skjolber/3d-bin-container-packing>
- **[R16] 官方规范**：JSON Schema 2020-12。
  <https://json-schema.org/specification>
- **[R17] 标准**：RFC 8785，JSON Canonicalization Scheme。
  <https://www.rfc-editor.org/rfc/rfc8785>
- **[R18] 官方规范**：CloudEvents Specification v1.0.2。
  <https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md>
- **[R19] 官方框架**：NIST AI RMF 1.0，Appendix C，
  "AI Risk Management and Human-AI Interaction."
  <https://airc.nist.gov/airmf-resources/airmf/appendices/app-c-ai-risk-management-and-human-ai-interaction/>
- **[R20] 官方文档**：Prometheus，"Instrumentation."
  <https://prometheus.io/docs/practices/instrumentation/>
- **[R21] 官方规范**：OpenTelemetry Semantic Conventions,
  "GenAI agent and framework spans."
  <https://opentelemetry.io/docs/specs/semconv/gen-ai/gen-ai-agent-spans/>
- **[R22] 官方仓库**：GoPackX v0.3.0，README、发布页和 MIT 许可证。
  <https://github.com/jcoruiz/gopackx>
  <https://github.com/jcoruiz/gopackx/releases/tag/v0.3.0>
- **[R23] 官方仓库**：Nextroute，BSL 1.1 许可证。
  <https://github.com/nextmv-io/nextroute>
  <https://github.com/nextmv-io/nextroute/blob/develop/LICENSE>
- **[R24] 官方仓库**：NVIDIA cuOpt，系统要求、API 和 Apache-2.0 许可证。
  <https://github.com/NVIDIA/cuopt>
- **[R25] 论文作者仓库**：Tamke 等人，3L-CVRP branch-and-cut、实例和解。
  <https://github.com/felicze/3l-cvrp>
- **[R26] 官方仓库**：xflp v0.7.7，约束能力和 MIT 许可证。
  <https://github.com/hschneid/xflp>
- **[R27] 论文作者仓库**：SolutionValidator，约束范围和 GPL-3.0 许可证。
  <https://github.com/CorinnaKrebs/SolutionValidator>
- **[R28] 官方仓库**：binpack3d v0.1.1，README 和 MIT 许可证。
  <https://github.com/wxul/binpack3d>
- **[R29] 官方文档**：React Three Fiber，"Scaling performance."
  <https://r3f.docs.pmnd.rs/advanced/scaling-performance>
- **[R30] 官方文档**：React Three Fiber，"Canvas."
  <https://r3f.docs.pmnd.rs/api/canvas>
- **[R31] 官方资料**：OR-Library `thpack` 数据说明和使用条款。
  <https://people.brunel.ac.uk/~mastjjb/jeb/orlib/thpackinfo.html>
  <https://people.brunel.ac.uk/~mastjjb/jeb/orlib/legal.html>
