# 城市配送接口参考

- 状态：已提交契约参考
- 日期：2026-10-10
- 权威实现：[`web/src/delivery/contract.ts`](../web/src/delivery/contract.ts)、
  [`web/src/delivery/api.ts`](../web/src/delivery/api.ts)、
  [`internal/delivery/artifact`](../internal/delivery/artifact)

本文只描述当前仓库已提交的浏览器契约、SSE 契约、artifact 契约和基准 adapter 协议。
`cmd/server` 尚未注册城市配送 HTTP handler，因此本文不把浏览器测试 fixture 解释为服务端实测。
服务端集成完成前，生产 HTTP 验收状态是 `blocked`。

## 浏览器 HTTP 契约

所有标识符都需要至少一个字符。`revisionID` 还必须匹配
`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`。摘要使用 64 位小写十六进制 SHA-256。

### 读取计划工作区

```text
GET /api/delivery/plan-revisions/{revisionID}/workspace
```

成功响应使用 `delivery.workspace.v1`。顶层字段如下：

| 字段 | 类型 | 约束 |
|---|---|---|
| `tenant_id` | string | 来自服务端授权上下文 |
| `optimization_run_id` | string | 当前求解 run |
| `run_state` | enum | `queued`、`solving`、`validating`、`candidate`、`awaiting_approval`、`executing`、`completed`、`failed` 或 `manual_review` |
| `attempt` | positive integer | 求解 attempt |
| `created_at`、`updated_at` | RFC 3339 timestamp | 必须包含时区 |
| `last_event_seq` | nonnegative integer | 必须等于 `audit` 尾序号 |
| `problem` | object | 位置、任务、货物、车辆、司机、来源和 `problem_digest` |
| `plan` | `delivery.plan.v1` projection | 路线、时刻、能量、逐站装载、目标值和指标 |
| `validation` | `delivery.validation.v1` | 独立 Validator 结果 |
| `decision` | tagged union | 审批未申请、待决、已确认、已驳回、已过期或已失效 |
| `execution` | tagged union | 未执行、执行中、部分完成、待对账、已完成、失败或人工复核 |
| `comparison` | tagged union | 与基准修订的结构化差异或 `unavailable` |
| `audit` | array | 从 1 开始且连续的审计摘要 |

浏览器还检查以下跨字段条件：

- URL 中的 `revisionID` 必须等于 `plan.revision_id`。
- problem、plan 和 validation 的 problem、policy、commitment、plan digest 必须相互绑定。
- `validation.valid=true` 时不能包含 violation。
- execution 开始前，decision 必须是 `confirmed`。
- execution effect 集必须等于审批确认的 effect 集。
- `completed` execution 激活的 revision 必须是当前 revision。
- `audit` 序号必须连续，`last_event_seq` 必须等于审计尾序号。
- 路线、司机、任务、位置、货物、舱室和车门引用必须存在。

### 确认审批

```text
POST /api/delivery/approvals/{approvalID}/confirm
Content-Type: application/json

{}
```

成功响应可以使用任意 2xx 状态，响应正文不参与浏览器状态更新。浏览器等待 SSE 后重新读取
工作区。

### 驳回审批

```text
POST /api/delivery/approvals/{approvalID}/reject
Content-Type: application/json

{"reason":"非空原因"}
```

浏览器在提交前要求非空原因。服务端仍需执行相同校验，不能依赖浏览器。

### 错误响应

非 2xx JSON 响应使用：

```json
{
  "error": {
    "code": "stable_machine_code",
    "message": "operator-readable message"
  }
}
```

当前浏览器定义三个本地错误码：

| code | 产生位置 | 含义 |
|---|---|---|
| `invalid_json` | 浏览器边界 | 2xx 响应不是 JSON |
| `invalid_response` | 浏览器边界 | 非 2xx 响应不符合错误 envelope |
| `invalid_contract` | 浏览器边界 | 2xx JSON 不符合 `delivery.workspace.v1` 或 revision 不匹配 |

服务端业务错误码尚未在 `cmd/server` 实现。服务端 owner 需要在 handler 合入时冻结 400、401、
403、404、409、412、413、422、429 和 503 的稳定 code。文档不会预填未实现 code。

## SSE 契约

```text
GET /api/delivery/plan-revisions/{revisionID}/events?after={lastEventSeq}
Accept: text/event-stream
```

每个事件使用 `delivery.workspace-event.v1`：

```json
{
  "schema_version": "delivery.workspace-event.v1",
  "event_id": "event-identifier",
  "seq": 42,
  "revision_id": "revision-identifier",
  "workspace_version": 8,
  "occurred_at": "2026-10-10T08:00:00Z",
  "kind": "execution_changed"
}
```

`kind` 只能是：

- `run_state_changed`
- `approval_changed`
- `execution_changed`
- `revision_stale`
- `audit_appended`

服务端把 `id` 设置为十进制 `seq`。浏览器要求 SSE `id` 与 JSON `seq` 相同，并忽略
`seq <= cursor` 的重复事件。事件只表示工作区版本发生变化。路线、矩阵和三维坐标继续从
workspace snapshot 获取。

浏览器以 snapshot 的 `last_event_seq` 建立连接。重连时再次传递最后已接收的序号。审批按钮
只在 SSE 为 `online` 时可用。

## Artifact 契约

[`internal/delivery/artifact`](../internal/delivery/artifact) 已实现内容寻址文件存储。

支持的 kind：

- `problem`
- `plan`
- `validation_report`
- `matrix`
- `load`
- `evidence`
- `effect`

artifact envelope 使用 `delivery.artifact.v1`：

```json
{
  "schema_version": "delivery.artifact.v1",
  "kind": "plan",
  "document_schema_version": "delivery.plan.v1",
  "document": {}
}
```

摘要覆盖 canonical envelope，不只覆盖 `document`。`Put` 使用 `put-if-absent`、临时文件、
文件 `fsync`、原子 rename 和目录 `fsync`。`Open` 每次重新计算摘要并拒绝非 canonical
内容。租户目录名由租户 ID 的域分离 SHA-256 派生，调用方仍必须先完成租户授权。

稳定错误：

| Go error | 条件 |
|---|---|
| `artifact.ErrNotFound` | 指定租户和摘要下没有 artifact |
| `artifact.ErrIntegrity` | 摘要、envelope、canonical JSON 或已存文件不一致 |
| `artifact.ErrTooLarge` | 写入内容超过 store 的配置上限 |

当前服务器没有 artifact 下载 handler。`GET` 路径、缓存 header 和 HTTP 错误映射要等服务端
集成后加入本参考。

## Benchmark adapter 协议

[`scripts/delivery-benchmark/config.json`](../scripts/delivery-benchmark/config.json) 为每个 command
adapter 指定一个环境变量。变量值是 JSON 字符串数组。runner 不通过 shell 执行命令。

命令参数支持以下占位符：

| 占位符 | 内容 |
|---|---|
| `{problem}` | canonical `delivery.problem.v1` 文件路径 |
| `{plan}` | solver 必须写入的 `delivery.plan.v1` 文件路径 |
| `{dataset}` | 固定数据集 ID |
| `{budget}` | 固定评估预算 |
| `{timeout_seconds}` | 单次进程超时 |

`{problem}` 和 `{plan}` 是必需占位符。runner 严格解析计划 JSON，调用独立 Validator，并要求
20 次计划摘要和校验报告摘要一致。完整命令见
[`scripts/delivery-benchmark/README.md`](../scripts/delivery-benchmark/README.md)。

## Production acceptance command 协议

[`scripts/delivery-acceptance/config.json`](../scripts/delivery-acceptance/config.json) 固定容量、
恢复、安全、可观测性和供应链检查。外部命令仍使用 JSON 字符串数组，runner 不通过 shell
执行命令。

命令必须包含 `{result}`。runner 还支持 `{config}`、`{root}` 和 `{probe}`。命令要在
`{result}` 写入严格的 `delivery.acceptance.command-result.v1`，其中包含：

- 与配置完全相同的 `probe_id`。
- 配置声明的全部检查及其证据。
- 配置声明的全部测量场景、p50、p95、p99、峰值内存、可行率、质量差距和恢复时间。
- 可选的证据文件名称、SHA-256 和字节数。

runner 拒绝未知字段、重复检查、额外场景、缺失维度、分位数倒序、样本不足和任一必需检查失败。
完整 schema 示例见
[`scripts/delivery-acceptance/README.md`](../scripts/delivery-acceptance/README.md)。

## 版本管理

以下 schema 已提交并受测试保护：

- `delivery.problem.v1`
- `delivery.plan.v1`
- `delivery.validation.v1`
- `delivery.artifact.v1`
- `delivery.workspace.v1`
- `delivery.workspace-event.v1`
- `delivery.benchmark.config.v1`
- `delivery.benchmark.manifest.v1`
- `delivery.benchmark.report.v1`
- `delivery.acceptance.config.v1`
- `delivery.acceptance.command-result.v1`
- `delivery.acceptance.report.v1`

字段语义变化需要新 schema version。新增可选字段也要先更新生产者、浏览器 Zod 契约、fixture、
契约测试和本文。
