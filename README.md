<p align="center">
  <a href="README.en.md">English</a> · 简体中文
</p>

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
    <img alt="waybill guardian 标志。盾形里有运单折角和一条三站路线。" src="docs/assets/logo-light.svg" width="420">
  </picture>
</p>

<h1 align="center">waybill-guardian</h1>

<p align="center">证据驱动的异常运单处置 Agent。自动调查，人工决策，幂等执行，全程审计。</p>

<p align="center">
  <a href="https://go.dev/dl/"><img alt="Go 1.26.9" src="https://img.shields.io/badge/Go-1.26.9-00ADD8?logo=go&logoColor=white"></a>
  <a href="https://react.dev/"><img alt="React 19.3.0" src="https://img.shields.io/badge/React-19.3.0-087EA4?logo=react&logoColor=white"></a>
  <a href="https://nodejs.org/"><img alt="Node.js 22.12 或更高版本" src="https://img.shields.io/badge/Node.js-%3E%3D22.12-339933?logo=nodedotjs&logoColor=white"></a>
  <a href="https://github.com/Duang777/waybill-guardian/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Duang777/waybill-guardian/actions/workflows/ci.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="Apache 2.0 license" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

传化集团与动势科技「AI 重构产业架构师大赛」AI+物流赛道参赛项目。

## 给评委

延误、破损或丢件发生后，waybill-guardian 自动汇集运单、轨迹、司机和天气证据，计算风险，形成可引用证据的归因结果，并提出改派、理赔或通知方案。所有写操作都先进入人工审批。确认后，服务端生成稳定 effect 身份和幂等键，执行平台写入，并把全过程记录为可校验、可续传、可回放的审计事件。

<p align="center">
  <a href="https://github.com/Duang777/waybill-guardian/releases/download/demo-v1.0.0/waybill-guardian-demo-1920-zh.mp4"><strong>观看 60 秒中文配音演示</strong></a>
  ·
  <a href="docs/demo-script.md">查看三分钟讲稿</a>
  ·
  <a href="contract.yaml">查看工具契约</a>
</p>

<p align="center">
  <img alt="全国公路港经营总览。页面显示六项 KPI、72 港网络、异常队列、三张经营图表和经营简报。" src="docs/assets/overview-console.png" width="960">
</p>

### 一条完整的处置闭环

| 阶段 | 系统动作 | 关键约束 | 可核验证据 |
|---|---|---|---|
| 发现 | 接收异常事件或由运营人员选择异常运单 | 授权范围先于聚合和处置 | CloudEvents、运单范围、事件哈希 |
| 调查 | 调用运单、轨迹、司机、天气四个只读工具 | 每次读取必须绑定当前运单、司机和线路 | 工具参数、结果摘要、证据字段 |
| 归因 | 计算时效、路况和天气风险，生成结构化归因 | 每个归因必须引用已审计的工具字段 | 置信度、JSON Pointer、事件序号和哈希 |
| 提案 | 生成改派、理赔和通知候选动作 | 候选承运商必须来自本次运单证据 | 方案摘要、备选方案、写操作清单 |
| 决策 | `pending` 审批进入确认、驳回或过期 | 人工决定先持久化，再恢复同一 Agent thread | 审批人、原因、时间和方案版本 |
| 执行 | 按稳定 effect 身份调用平台 adapter | 同一业务 effect 并发合并，成功结果直接回放 | effect、attempt、平台回执和响应摘要 |
| 恢复 | 启动时重建 run、审批和 effect 状态 | 未知外部结果先查询，不盲目重复写入 | lookup 结果、恢复决定和人工复核状态 |
| 复核 | SSE 推送实时事件，并从游标续传 | 客户端按 run 和 `seq` 去重 | `seq`、`prev_hash`、`hash` |

### 与普通 Agent 的差异

waybill-guardian 把模型放在受约束的业务流程中。模型负责理解证据和组织方案，服务端负责身份、授权、审批、写入、恢复和审计。模型不能自行生成 effect 身份，不能引用不存在的证据，也不能绕过人工审批直接调用写工具。

## 核心算法

### 1. 多源证据融合与风险评分

服务按固定顺序读取运单、轨迹、司机和天气，并生成三个 0 至 100 的风险分量。确定性评分先完成异常排序，模型再解释证据和组织方案。

```text
ETA 风险 =
  clamp(
    未妥投基础分 20
    + 异常轨迹点数量 × 15
    + 异常停留总小时 × 8,
    0,
    100
  )

道路风险 =
  clamp(
    连续驾驶小时 × 5
    + 疲劳预警 30,
    0,
    100
  )

天气风险 =
  无预警 0
  蓝色或黄色 30
  橙色 60
  红色或严重预警 90

综合风险 = ETA 风险 × 50% + 道路风险 × 30% + 天气风险 × 20%
```

总览按综合风险降序排列异常运单；同分时按最近记录时间排序。评分实现位于 [`internal/guardian/assessment.go`](internal/guardian/assessment.go) 和 [`internal/guardian/overview.go`](internal/guardian/overview.go)。

### 2. 证据约束的 Agent 推理

Agent 使用 hastekit `agent-sdk-go` v0.0.24。运行时通过中间件收紧模型边界：

- 工具能力过滤器只把当前部署启用的工具暴露给模型。
- 读取绑定校验器强制运单、司机和线路与当前 run 一致，阻止跨运单取证。
- 请求预算把一次逻辑模型调用限制在 45 秒内，并把单次输出限制为 4096 token。
- Agent loop 最多执行 20 轮，provider 请求最多尝试三次。
- 模型调用记录模式、模型名、时延、token 用量、结果和问题代码。
- 工具输出只包含白名单字段，并限制文本、数组和历史大小。

这组约束位于 [`internal/agent`](internal/agent)。模型提供商可以使用 Responses 或 Chat Completions 兼容接口。

### 3. 可编译的结构化提案

模型输出先经过 `proposal.v1` 编译器，再进入审批。编译器执行以下检查：

1. JSON 必须通过严格解析，不能包含未知字段、重复键或尾随值。
2. 每个归因项必须包含置信度和 1 至 8 个证据引用。
3. 每个引用使用 RFC 6901 JSON Pointer 指向本次 run 的工具结果。
4. 引用值必须与审计事件中的标量值逐字节一致。
5. 引用保存来源事件 ID、`seq` 和 `hash`，审批时可以回到原始证据。
6. 四个只读工具必须全部成功，提案才能通过。
7. 候选承运商必须存在于最新运单证据中。
8. 当前工具不能证明的收益字段必须明确标记为 `unavailable`，不能由模型估算。

首版提案未通过时，系统把问题代码和受限长度的失败片段交给模型修复一次。修复后仍不合格，run 进入人工复核。实现位于 [`internal/proposal`](internal/proposal) 和 [`internal/agent/proposal_middleware.go`](internal/agent/proposal_middleware.go)。

### 4. 持久化人工审批状态机

写工具带有 `RequiresApproval`。Agent 到达写调用时暂停，服务端把业务参数、证据、候选方案和到期时间组成审批批次。

```text
pending ──确认──> confirmed ──全部成功──> executed
   │                   ├──部分失败──> partially_failed
   │                   └──结果未知──> reconciliation_required
   ├──驳回──> rejected
   └──超时──> expired
```

确认、驳回和过期争用同一把 run 锁，因此只能落下一种决定。超时按拒绝恢复 Agent，不会默认放行。驳回原因会回到同一个 thread，Agent 可以提交下一版候选方案。

### 5. 稳定 effect 身份与并发幂等

模型只提交业务参数。服务端按业务语义生成写操作身份：

1. 对工具参数做规范化 JSON 编码，并计算 SHA-256。
2. 用 `run_id`、`incident_id`、`waybill_id` 和 `plan_version` 生成方案 UUID。
3. 用 action、target 和参数哈希生成方案项摘要。
4. 用方案 UUID 和方案项摘要生成稳定 `effect_id`。
5. 从 `effect_id` 派生 256 位幂等键。

同一幂等键的并发调用会等待同一执行结果。成功结果直接回放，键与业务参数冲突时拒绝执行。测试中的 10 个并发调用只进入 platform 一次，见 [`internal/idempotency/idempotency_test.go`](internal/idempotency/idempotency_test.go)。

当外部系统可能已经成功，但本地尚未写入成功事件时，effect 进入 `unknown`。恢复器先按幂等键查询外部结果，再决定完成、重试、等待或转人工复核。

### 6. 追加式审计与哈希链

每个 run 都有一条只追加事件流。事件包含：

- 单调递增的 `seq`
- 稳定 `event_id`
- `actor` 和事件类型
- 脱敏后的 payload
- `prev_hash`
- 当前事件 `hash`

新事件的哈希覆盖规范化事件内容和前序哈希。启动时，存储会校验序号、前序哈希和当前哈希。JSONL 写入使用单写者锁、文件 `fsync` 和目录 `fsync`。PostgreSQL 模式把业务投影、审计事件和 outbox 写入同一事务。

SSE 先发送 `Last-Event-ID` 之后的历史事件，再切换到 live 订阅。浏览器断线后可以从最后序号继续，不需要重新执行 Agent。

### 7. CloudEvents 归并与乱序修正

`POST /v1/events` 接收 CloudEvents 1.0 structured JSON。入口执行严格 schema、UTF-8、时间、source URI、subject、版本和 body 大小校验，并对等价 JSON 生成稳定哈希。

事件 reducer 与输入顺序无关。它按 `source_version` 归并检测事件和修正事件，支持 replace 与 retract，并识别以下状态：

- `active`
- `retracted`
- `pending_correction`
- `conflicted`

同版本不同内容、跨 incident 修正、缺失修正目标和非法版本关系不会被静默覆盖。系统保留冲突引用，并把结果送入人工复核。Outbox 使用租约、续租、有界并发和封顶指数退避投递下游 CloudEvents。

### 8. 可核验经营指标

经营总览先按 JWT 授权的运单范围过滤，再计算指标。服务不使用默认均值补齐缺失事实。

| KPI | 计算口径 |
|---|---|
| 已实现时效挽回 | 只统计同一审批先由人工确认、再由系统执行成功、且 run 已完成的运单 |
| 已实现成本影响 | `避免违约金 - 改派差价 - 处置成本`，金额先按整数分求和 |
| 人力节省 | `成功自动证据采集步数 × EVIDENCE_STEP_MINUTES ÷ 60` |
| 异常闭环率 | `已完成或已驳回的异常运单数 ÷ 窗口内异常运单数` |
| 平均处置时长 | `终态时间 - 启动时间` 的窗口均值 |
| 人工审批通过率 | `人工确认数 ÷ 人工决定数` |

数据不足时，指标返回 `unavailable` 和原因。经营简报只接收不含运单、人员、车辆和线路身份的聚合计数，引用由服务端重建。

## 城市配送智能配载与调度

仓库新增独立的 `delivery` 业务域，不把多订单规划状态塞入单运单处置聚合。当前已提交的基础能力包括：

- `delivery.problem.v1`、`delivery.plan.v1` 和 `delivery.validation.v1` 强类型契约。
- 固定事实快照、canonical JSON，以及 problem、policy、commitment、plan 和 report 摘要绑定。
- 独立 Validator 的 13 个规则族，覆盖订单守恒、取送、资源、路线、时间窗、司机法规、能源、
  三维边界、支撑、卸货可达性、轴载、重心、承诺和指标重算。
- 内容寻址 artifact store，读取时重新校验摘要和 canonical envelope。
- 城市配送调度台，联动路线、司机时间轴、SOC 和逐站三维装卸，并展示审批、effect 和对账状态。
- 固定 seed 的基准工具，可接入内置 solver、VROOM、OR-Tools 和 PyVRP 命令。

最新权威报告使用 8、32 和 128 个请求的数据集，对应 16、64 和 256 个任务。确定性 reference
baseline 在每个数据集上重放 20 次，全部计划的硬约束违规为 0，plan digest 和 validation
report digest 均保持一致。完整运行记录、机器信息、耗时和发布门禁见
[`delivery-benchmark.v1.json`](docs/reports/delivery-benchmark.v1.json)，可读表格由该 JSON
生成：[`城市配送基准证据`](docs/reports/delivery-benchmark.md)。

```bash
./scripts/delivery-benchmark/run.sh
./scripts/delivery-benchmark/check.sh
```

reference baseline 用于验证数据、摘要、Validator 和报告链路，不代表生产 solver 质量，不证明
全局最优，也不构成物理装载安全认证。生产 solver 的证据只有在报告
`publication_gate.publication_ready=true` 时才可发布。

生产验收工具固定 100、500 和 2,000 个任务的容量矩阵，并覆盖每车 300 件货物、
100 个并发查询、500 个 SSE 连接、故障恢复、跨租户授权和供应链检查。权威状态见
[`delivery-acceptance.v1.json`](docs/reports/delivery-acceptance.v1.json)，可读报告见
[`城市配送生产验收报告`](docs/reports/delivery-acceptance.md)。缺少生产入口的探针保持
`blocked`，不会计入通过。

```bash
./scripts/delivery-acceptance/run.sh
./scripts/delivery-acceptance/check.sh
```

## 系统架构

```mermaid
flowchart TD
  browser["React 经营总览与单运单工作台"]
  api["HTTP API + SSE"]
  guardian["guardian 用例协调与恢复"]
  agent["hastekit Agent loop"]
  proposal["proposal.v1 证据编译器"]
  approval["持久化审批状态机"]
  identity["effect 身份与幂等执行"]
  reads["运单 / 轨迹 / 司机 / 天气"]
  sources["TMS / JSON / CSV / 事件流"]
  writes["改派 / 理赔 / 通知 adapter"]
  audit["append-only 审计与哈希链"]
  storage["JSONL 或 PostgreSQL 17"]
  outbox["CloudEvents outbox"]

  browser --> api
  api --> guardian
  guardian --> agent
  agent --> reads
  reads --> sources
  agent --> proposal
  proposal --> approval
  approval --> guardian
  guardian --> identity
  identity --> writes
  guardian --> audit
  proposal --> audit
  approval --> audit
  identity --> audit
  audit --> storage
  storage -->|replay + live| api
  storage --> outbox
```

```mermaid
sequenceDiagram
  participant O as 运营人员
  participant W as Web
  participant G as Guardian
  participant A as Agent
  participant P as Proposal Compiler
  participant H as Approval
  participant E as Effect Executor
  participant T as Platform
  participant D as Audit

  O->>W: 启动异常处置
  W->>G: POST run
  G->>A: 创建 run 与调查上下文
  A->>T: 查询四类运营证据
  T-->>A: 返回受限字段
  A->>P: 提交归因和候选动作
  P->>D: 解析并绑定证据引用
  P-->>H: 创建审批批次
  H-->>W: pending
  O->>H: 确认或驳回
  H->>D: 先持久化人工决定
  alt 确认
    H->>A: 恢复同一 thread
    A->>E: 提交已批准写调用
    E->>E: 派生 effect_id 与幂等键
    E->>T: 执行或查询已有结果
    T-->>E: 平台回执
    E->>D: 写入执行结果
  else 驳回或过期
    H->>A: 携带原因恢复
    A->>P: 提交下一版方案或结束
  end
  D-->>W: SSE 续传和实时事件
```

## 产品能力

| 能力域 | 已实现能力 |
|---|---|
| 全国经营总览 | 72 港网络、线路热度、车辆状态、六项 KPI、三张经营图表、经营简报 |
| 异常队列 | 风险排序、状态筛选、批量选择、单次最多启动 20 张运单 |
| 单运单工作台 | 六阶段运行带、地图轨迹、异常点、证据账本、审批卡片、审计抽屉 |
| Agent 调查 | 四个只读工具、调用审计、工具绑定、能力过滤、模型预算和重试 |
| 处置动作 | TMS 改派、破损或丢件理赔、货主或司机通知 |
| 人工决策 | 确认、驳回、原因回传、到期拒绝、第二候选方案 |
| 可靠执行 | 服务端 effect 身份、并发合并、结果回放、失败分类和结果核对 |
| 运行恢复 | 审批恢复、方案 checkpoint 恢复、effect 恢复、历史保留策略 |
| 审计复核 | 哈希链校验、SSE 游标续传、事件回放、敏感字段脱敏 |
| 数据接入 | JSON 或 CSV v1、CloudEvents 1.0、TMS 和通知 adapter 边界 |
| 身份授权 | local 受限访问、JWT RS256、tenant、role 和 waybill scope |
| 持久化 | JSONL、PostgreSQL 17、AES-256-GCM Agent history、事务 outbox |
| 可观测性 | 固定标签 Prometheus 指标、模型时延和 token、outbox 状态统计 |
| 前端体验 | 白色工业控制台、桌面和移动端、自适应图表、高德地图接入 |
| 城市配送基础 | 强类型规划契约、独立 Validator、artifact store、20 次确定性重放和调度台 |

<p align="center">
  <img alt="桌面工作台停在人工审批。页面显示运行阶段、地图、证据账本、候选方案和待执行动作。" src="docs/assets/console-approval.png" width="840">
</p>

<p align="center">
  <img alt="移动端工作台显示处置完成。" src="docs/assets/console-completed-mobile.png" width="280">
</p>

## 可靠性证据

| 场景 | 系统行为 | 自动化证据 |
|---|---|---|
| 同一 effect 10 个并发请求 | platform 只执行一次，其余调用等待或回放结果 | `TestConcurrentExecuteRunsEffectOnce` |
| 两个审批决定并发到达 | run 锁和状态机只接受一个决定 | `TestConcurrentConfirmResumesRunOnce`、`TestDecisionConflict` |
| 服务在审批后重启 | 重建审批并恢复同一个 Agent thread | `TestRecoverReplaysConfirmedApproval` |
| 提案已落盘但审批未创建 | 从 proposal checkpoint 物化审批，不再次调用模型 | `TestRecoverUsesPreparedProposalWithoutCallingModelAgain` |
| 外部写入结果未知 | 查询外部结果，不重复 dispatch | `TestRecoverReconcilesStartedEffectWithoutStoppingService` |
| 单个 run 审计损坏 | 隔离损坏 run，其余 run 和 outbox 继续恢复 | `TestPrepareRecoveryQuarantinesOnlyDamagedRun` |
| SSE 断线重连 | 从 `Last-Event-ID` 之后回放，再接 live | `TestHTTPDemoFlowAndSSECursor` |
| 乱序修正事件 | reducer 与输入顺序无关并最终收敛 | `TestReduceCorrectionBeforeTargetConverges` |
| 模型引用错误证据 | proposal 编译失败并进入修复或人工复核 | `internal/proposal/proposal_test.go` |
| Agent history 含敏感字段 | 保存和加载边界直接拒绝 | `internal/agent/history_guard_test.go` |

GitHub Actions 对 pull request 和 `main` 运行 Go、race、Web、PostgreSQL 17、恢复稳定性、许可证和容器检查。

## 演示

正式演示使用在线模型。配置任一兼容 Responses 或 Chat Completions 的模型服务：

```bash
export LLM_API_STYLE=chat_completions
export LLM_BASE_URL=https://api.deepseek.com
export LLM_API_KEY=replace-me
export LLM_MODEL=deepseek-v4-flash
./scripts/demo.sh
```

打开 <http://127.0.0.1:5173>。

1. 在经营总览选择异常运单，点击 **交给 Agent**。
2. Agent 查询运单、轨迹、司机和天气，页面实时显示六阶段进度。
3. 证据账本展示连续驾驶、异常停留和天气预警。每条归因可以定位到工具结果。
4. 审批卡片展示候选承运商、排序依据和待执行动作。此时平台写入尚未发生。
5. 点击 **确认并执行**。服务端恢复同一个 thread，并按 effect 身份执行写操作。
6. 展开 **完整审计记录**，从第一条事件回放，再点击 **实时** 回到末尾。
7. 重新处置并驳回首选方案。Agent 读取驳回原因后提交第二候选方案。

已发布的 [1920×1080 中文配音演示](https://github.com/Duang777/waybill-guardian/releases/download/demo-v1.0.0/waybill-guardian-demo-1920-zh.mp4)包含中文字幕和系统合成音轨。配音稿见 [`docs/demo-script.md`](docs/demo-script.md#60-秒配音稿)。

生成演示录像：

```bash
cd web
npm run record:demo
```

生成 1920×1080 版本：

```bash
RECORD_RESOLUTION=1920x1080 npm run record:demo
```

## 快速开始

### 本地工具链

需要 Go 1.26.9 或更高版本，以及 Node.js 22.12 或更高版本。

```bash
git clone https://github.com/Duang777/waybill-guardian.git
cd waybill-guardian

export LLM_API_STYLE=chat_completions
export LLM_BASE_URL=https://api.deepseek.com
export LLM_API_KEY=replace-me
export LLM_MODEL=deepseek-v4-flash

./scripts/demo.sh
```

脚本安装前端依赖、编译 Go 服务，并启动 API 与 Web。就绪后会打印访问地址。按 `Ctrl+C` 同时停止两个进程。

### Docker

```bash
git clone https://github.com/Duang777/waybill-guardian.git
cd waybill-guardian

LLM_API_STYLE=chat_completions \
LLM_BASE_URL=https://api.deepseek.com \
LLM_API_KEY=replace-me \
LLM_MODEL=deepseek-v4-flash \
docker compose up --build
```

打开 <http://127.0.0.1:8080>。Compose 只把应用端口发布到宿主机 loopback，容器使用只读根文件系统，并删除全部 Linux capabilities。

启用 PostgreSQL 17：

```bash
COMPOSE_PROFILES=prod \
STORAGE=postgres \
DATABASE_URL='postgres://waybill:waybill@postgres:5432/waybill?sslmode=disable' \
CHECKPOINT_ENCRYPTION_KEY='MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=' \
LLM_API_STYLE=chat_completions \
LLM_BASE_URL=https://api.deepseek.com \
LLM_API_KEY=replace-me \
LLM_MODEL=deepseek-v4-flash \
docker compose up --build
```

示例数据库口令和 checkpoint key 只用于本机启动。部署时必须替换，并在 HTTPS 入口后使用 `AUTH_MODE=jwt`。

## 数据与平台接入

### JSON 和 CSV 数据

数据接口支持 JSON 或 CSV v1。字段模板位于：

- [`data/templates/waybills-v1.json`](data/templates/waybills-v1.json)
- [`data/templates/waybills-v1.csv`](data/templates/waybills-v1.csv)

服务启动前严格检查引用、坐标、时间顺序和网络拓扑：

```bash
go run ./cmd/dataimport validate --data ./data/templates/waybills-v1.json
go run ./cmd/dataimport validate --data ./data/templates/waybills-v1.csv
```

使用业务数据启动：

```bash
PLATFORM=file \
DATA_FILE=/absolute/path/to/waybills-v1.json \
AGENT_MODE=online \
LLM_API_STYLE=chat_completions \
LLM_BASE_URL=https://api.deepseek.com \
LLM_API_KEY=replace-me \
LLM_MODEL=deepseek-v4-flash \
./scripts/demo.sh
```

数据格式和校验规则见 [`docs/file-data-source-design.md`](docs/file-data-source-design.md)。

### 企业平台 adapter

`internal/platform` 定义读取、写入、结果查询和恢复契约。Agent、审批、幂等和审计只依赖这些接口。企业接入时替换 adapter 和凭据，不需要修改 Agent loop。

仓库提供 HTTP 改派 adapter，并为理赔和通知保留同一 effect 执行协议。平台接入设计见 [`docs/RFC-002.md`](docs/RFC-002.md) 和 [`docs/real-write-adapter-design.md`](docs/real-write-adapter-design.md)。

### CloudEvents

PostgreSQL 模式注册 `POST /v1/events`。启用 outbox 后，服务使用带 Bearer 认证的 HTTPS publisher 发送 structured CloudEvents。

```text
Content-Type: application/cloudevents+json
specversion: 1.0
```

当前事件 profile 支持延误发现和延误修正。事件 schema、规范哈希和 reducer 位于 [`internal/events`](internal/events)。

## 主要配置

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LLM_API_STYLE` | `responses` | `responses` 或 `chat_completions` |
| `LLM_BASE_URL` | 空 | 模型 API 根路径 |
| `LLM_API_KEY` | 空 | 只从运行环境读取 |
| `LLM_MODEL` | 空 | 提供商当前可用的模型 ID |
| `LLM_REQUEST_TIMEOUT` | `45s` | 一次逻辑模型调用的总时限 |
| `LLM_MAX_OUTPUT_TOKENS` | `4096` | 单次 provider 请求的输出上限 |
| `BRIEF_TIMEOUT` | `8s` | 经营简报调用时限 |
| `MAX_CONCURRENT_RUNS` | `8` | 同时调查的 run 数量，范围 1 至 64 |
| `APPROVAL_TTL` | `10m` | 审批有效期 |
| `HISTORY_RETENTION` | `168h` | 已结束 Agent history 的保留期 |
| `EVIDENCE_STEP_MINUTES` | `8` | 人工完成一次证据采集的估算分钟数 |
| `STORAGE` | `jsonl` | `jsonl` 或 `postgres` |
| `AUTH_MODE` | `local` | `local` 或 `jwt` |
| `DATA_FILE` | 空 | JSON 或 CSV v1 文件路径 |
| `OUTBOX_ENABLED` | `false` | 启用 CloudEvents dispatcher |
| `METRICS_ADDR` | 空 | Prometheus 指标监听地址 |

国产模型兼容配置示例：

| 提供商 | API 风格 | `LLM_BASE_URL` | 模型示例 |
|---|---|---|---|
| 阿里云百炼千问 | Chat Completions | `https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | `qwen-plus` |
| DeepSeek | Chat Completions | `https://api.deepseek.com` | `deepseek-v4-flash` |
| 火山方舟豆包 | Chat Completions | `https://ark.cn-beijing.volces.com/api/v3` | `doubao-seed-1-6-251015` |
| Kimi | Chat Completions | `https://api.moonshot.cn/v1` | `kimi-k3` |
| 智谱 GLM | Chat Completions | `https://open.bigmodel.cn/api/paas/v4` | `glm-5.3` |

模型 ID、价格和数据处理规则由提供商维护。部署前请核对对应服务条款。相关入口登记在 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

## 安全与合规

- 写工具必须经过人工审批，模型无法关闭该要求。
- JWT 校验 RS256、issuer、audience、时效、tenant、role 和 waybill scope。
- 非安全 HTTP 方法使用 Go `CrossOriginProtection` 校验来源。
- JSON 写接口校验 `Content-Type` 并拒绝未知字段。
- 读工具不向模型返回电话、车牌和精确坐标。
- Agent history guard 拒绝电话、车牌、精确坐标和通知供应商参数。
- 运营接口对电话和车牌做脱敏。
- PostgreSQL 中的 Agent history 使用 AES-256-GCM 加密。
- 外部 outbox 地址除 loopback 测试地址外必须使用 HTTPS。
- 容器使用只读根文件系统、`no-new-privileges` 和空 capability 集合。

模型调用会把受限后的运营证据发送给部署方选择的提供商。部署方需要按企业规则配置模型、数据处理协议、密钥托管和网络出口。

## 验证

后端检查：

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
./scripts/check-history-governance.sh
./scripts/delivery-benchmark/check.sh
./scripts/delivery-acceptance/check.sh
./scripts/licenses.sh
```

PostgreSQL 17 集成检查：

```bash
./scripts/test-postgres.sh
```

前端和浏览器流程：

```bash
cd web
npm ci
npm test
npm run build
npm run verify:e2e
npm run verify:file-e2e
npm run verify:overview
```

浏览器验收覆盖启动、取证、审批、改派、通知、驳回、第二候选方案、审计回放、文件数据、经营总览和响应式布局。

## 仓库地图

| 路径 | 职责 |
|---|---|
| `cmd/server` | HTTP、SSE、认证、事件入口和进程生命周期 |
| `internal/guardian` | run 协调、风险评分、批量启动、恢复和 KPI |
| `internal/agent` | hastekit 组装、工具边界、模型预算和提案修复 |
| `internal/proposal` | `proposal.v1` 严格解析、证据引用和摘要 |
| `internal/approval` | 审批状态机和授权 |
| `internal/idempotency` | effect 身份、并发合并、重试和结果核对 |
| `internal/audit` | 追加式事件、哈希链、回放和订阅 |
| `internal/events` | CloudEvents profile、规范哈希和 incident reducer |
| `internal/outbox` | 有界 dispatcher、租约续期和重试 |
| `internal/storage/postgres` | 事务存储、run 租约、effect 租约和加密 history |
| `internal/platform` | 企业 TMS、天气、理赔和通知 adapter 边界 |
| `internal/tools` | 七个 typed tools 和字段白名单 |
| `internal/delivery` | 城市配送领域契约、快照、独立 Validator 和 artifact store |
| `scripts/delivery-benchmark` | 固定数据、solver 采集协议、20 次重放和报告生成 |
| `scripts/delivery-acceptance` | 容量、安全、高可用、灾备和供应链验收 |
| `web` | React 经营总览、工作台、地图、图表和审计时间线 |

## 开源声明

项目使用 [Apache License 2.0](LICENSE)。直接依赖、传递依赖和参考项目写在 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)，Go 和 Web 许可清单位于 [`docs/licenses/`](docs/licenses/)。

hastekit `agent-sdk-go` v0.0.24 以 Go module 引入。本仓库没有复制其源码。

以下项目用于比较交互和领域划分，没有源文件进入本仓库：

- [jattiphrswan/logistics-tracker](https://github.com/jattiphrswan/logistics-tracker)
- [09karankr/port-logistics-intelligence](https://github.com/09karankr/port-logistics-intelligence)
- [dominicfinn/open_tms](https://github.com/dominicfinn/open_tms)

## 延伸阅读

- [架构和恢复](docs/RFC-001.md)
- [企业平台接入](docs/RFC-002.md)
- [HTTP 写 adapter](docs/real-write-adapter-design.md)
- [文件数据源](docs/file-data-source-design.md)
- [Agent history 治理](docs/history-governance.md)
- [城市配送架构](docs/delivery-optimization-architecture.md)
- [城市配送接口参考](docs/delivery-api-reference.md)
- [城市配送运维手册](docs/delivery-operations.md)
- [城市配送基准证据](docs/reports/delivery-benchmark.md)
- [城市配送生产验收报告](docs/reports/delivery-acceptance.md)
- [演示脚本](docs/demo-script.md)
- [模块索引](AGENTS.md)
