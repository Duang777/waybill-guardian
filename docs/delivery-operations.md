# 城市配送运维手册

- 状态：已提交组件可执行，服务端集成待完成
- 日期：2026-10-10
- 配套参考：[`delivery-api-reference.md`](./delivery-api-reference.md)、
  [`delivery-optimization-architecture.md`](./delivery-optimization-architecture.md)

本手册区分已经提交的能力与尚未接入 `cmd/server` 的能力。基准报告决定 solver
质量门禁，生产验收报告决定容量、安全、高可用和灾备门禁。两个报告的
`publication_gate.publication_ready` 都必须为 `true`。

## 当前运行边界

| 能力 | 状态 | 验证入口 |
|---|---|---|
| Problem、Plan、Validation schema 和 canonical digest | 已提交 | `go test ./internal/delivery/domain ./internal/delivery/service` |
| 独立 Validator | 已提交 | `go test ./internal/delivery/validate` |
| 内容寻址文件 artifact store | 已提交 | `go test ./internal/delivery/artifact` |
| Delivery 调度台与浏览器契约 | 已提交 | `cd web && npm test && npm run verify:delivery` |
| 固定数据、reference baseline、20 次重放和报告生成 | 已提交 | `./scripts/delivery-benchmark/run.sh` |
| Artifact 隔离、损坏和备份恢复验收 | 已提交 | `./scripts/delivery-acceptance/run.sh --skip-browser` |
| 300 件浏览器回放和资源释放验收 | 已提交 | `./scripts/delivery-acceptance/run.sh` |
| 漏洞、许可证、镜像内容和 CycloneDX SBOM 门禁 | 已提交 | GitHub Actions `docker` 和 `go` jobs |
| 生产 solver 命令入口 | 未提交 | 报告中的 `builtin` 为 `blocked` |
| Delivery HTTP handler、SSE publisher 和审批写入 | 未提交 | `cmd/server` 中没有对应路由 |
| Delivery PostgreSQL schema、worker 和租约 | 未提交 | 现有 migration 不包含 `delivery_*` 表 |
| VROOM、OR-Tools 和 PyVRP adapter | 未提交 | 对应报告项为 `blocked` |

## 在全新环境检查仓库

安装 Go 1.26.9 或更高版本，以及 Node.js 22.12 或更高版本。

```bash
git clone https://github.com/Duang777/waybill-guardian.git
cd waybill-guardian

go test ./...
go vet ./...

cd web
npm ci
npm test
npm run build
cd ..

./scripts/delivery-benchmark/check.sh
./scripts/delivery-acceptance/check.sh
```

基准检查会重建固定数据摘要并检查文档。生产验收检查会核对工具、配置和被测源码摘要，
再比较权威 JSON 生成的中英文报告。

## 运行当前应用

使用 Docker 启动当前异常处置应用：

```bash
docker compose up --build
```

检查健康状态：

```bash
curl --fail --silent http://127.0.0.1:8080/healthz
```

当前镜像不提供 Delivery HTTP API。不要把浏览器 fixture 测试解释为容器端到端验收。
Delivery handler 合入后，发布检查还需要读取一个真实 workspace snapshot，建立 SSE，
执行一次审批，并验证 artifact 摘要。

## 运行 Delivery 基准

生成当前可用证据：

```bash
./scripts/delivery-benchmark/run.sh
```

只有在生产 solver 命令已配置时才使用发布门禁：

```bash
export DELIVERY_BENCHMARK_BUILTIN_COMMAND='[
  "./bin/delivery-solver",
  "solve",
  "--problem", "{problem}",
  "--plan", "{plan}",
  "--budget", "{budget}"
]'

./scripts/delivery-benchmark/run.sh --require-publication
```

命令返回成功后，再检查以下字段：

```bash
jq '.publication_gate' docs/reports/delivery-benchmark.v1.json
jq '.adapters[] | {id, status, reason}' docs/reports/delivery-benchmark.v1.json
```

`publication_ready` 必须是 `true`。所有发布计划必须满足 `all_valid=true`、
`digest_stable=true` 和 `hard_violation_count=0`。

## 运行生产验收

运行当前仓库可执行的 artifact 和浏览器探针：

```bash
./scripts/delivery-acceptance/run.sh
jq '.publication_gate' docs/reports/delivery-acceptance.v1.json
```

生产探针通过 JSON argv 环境变量接入。每个命令必须写出严格的
`delivery.acceptance.command-result.v1`。完整字段和示例见
[`scripts/delivery-acceptance/README.md`](../scripts/delivery-acceptance/README.md)。

| 探针 | 环境变量 |
|---|---|
| 100、500、2,000 任务容量矩阵 | `DELIVERY_ACCEPTANCE_CAPACITY_COMMAND` |
| 100 查询和 500 SSE 连接 | `DELIVERY_ACCEPTANCE_CONCURRENCY_COMMAND` |
| PostgreSQL、artifact、solver、lease 和 effect 恢复 | `DELIVERY_ACCEPTANCE_RECOVERY_COMMAND` |
| tenant、digest、cursor、审批和 effect 越权 | `DELIVERY_ACCEPTANCE_AUTHORIZATION_COMMAND` |
| 低基数指标、日志、trace、SLO 和告警 | `DELIVERY_ACCEPTANCE_OBSERVABILITY_COMMAND` |
| 漏洞、许可证、镜像内容和 SBOM | `DELIVERY_ACCEPTANCE_SUPPLY_CHAIN_COMMAND` |

仓库提供供应链探针：

```bash
export DELIVERY_ACCEPTANCE_SUPPLY_CHAIN_COMMAND='[
  "./scripts/delivery-acceptance/supply-chain-probe.sh",
  "--result", "{result}"
]'
```

配置完全部命令后运行发布门禁：

```bash
./scripts/delivery-acceptance/run.sh --require-publication
```

命令缺失时，探针状态是 `blocked`。命令失败或证据字段不满足配置时，探针状态是 `failed`。

## 扩容

当前 reference baseline 只用于验证数据、摘要和 Validator，不代表生产容量。扩容前按
[`delivery-optimization-architecture.md`](./delivery-optimization-architecture.md#18-可观测性和容量)
运行 `scripts/delivery-acceptance/config.json` 固定的 100、500 和 2,000 任务矩阵。

生产服务需要分别限制：

- 同时运行的 OptimizationRun 数量。
- 每个 solver 的 CPU、内存、评估预算和 wall-clock 保护时间。
- 单租户和全局 SSE 连接数。
- workspace snapshot 与 artifact 的最大字节数。
- adapter 并发、请求超时和重试次数。
- worker claim 数量和租约续期间隔。

达到限制时返回稳定的容量错误。不要启动无界 goroutine，也不要在 HTTP handler 中同步等待
长时间求解。

## 升级

1. 运行 `./scripts/delivery-benchmark/check.sh` 和
   `./scripts/delivery-acceptance/check.sh --require-publication`。
2. 备份数据库和 artifact 根目录。
3. 在预发布环境运行数据库 migration。
4. 启动一个新版本实例，但先不要停止旧 worker。
5. 检查新实例健康状态和 schema version。
6. 停止旧 worker 获取新 claim。
7. 等待旧租约到期，并确认 fencing token 拒绝旧 worker 写入。
8. 切换 HTTP 流量。
9. 建立 SSE，确认游标可以从切换前序号继续。
10. 再次运行两个 `--require-publication` 门禁。

schema 字段语义变化必须发布新版本。不要原地改变 `delivery.problem.v1`、
`delivery.plan.v1` 或 `delivery.workspace.v1` 的既有字段含义。

## 备份 artifact

`artifact.FileStore` 接收调用方提供的根目录。当前 `cmd/server` 还没有对应环境变量，因此以下
步骤适用于完成服务端接入后的根目录。

停止会写入 artifact 的 worker，或使用文件系统一致性快照。然后运行：

```bash
test -n "${DELIVERY_ARTIFACT_ROOT:?set DELIVERY_ARTIFACT_ROOT}"
test -d "$DELIVERY_ARTIFACT_ROOT"

backup_dir="$(dirname "$DELIVERY_ARTIFACT_ROOT")"
backup_name="$(basename "$DELIVERY_ARTIFACT_ROOT")"
tar -C "$backup_dir" -cf delivery-artifacts.tar "$backup_name"
shasum -a 256 delivery-artifacts.tar > delivery-artifacts.tar.sha256
```

备份必须保留文件权限。artifact 文件和租户目录使用 `0600` 与 `0700`。

## 恢复并校验 artifact

恢复到空目录，不要覆盖正在使用的 store：

```bash
shasum -a 256 -c delivery-artifacts.tar.sha256
mkdir -p /restore/target
tar -C /restore/target -xf delivery-artifacts.tar
```

恢复后必须通过 `artifact.Store.Verify` 遍历数据库引用的每个摘要。文件存在不足以证明恢复
成功。`Verify` 会重新计算 envelope 摘要、检查 canonical JSON 和 document schema。

如果任一摘要失败：

1. 隔离对应 problem、run、revision 或 execution。
2. 禁止审批和 effect 执行。
3. 从另一份已校验备份恢复。
4. 写入完整性失败审计事件。
5. 在人工确认前保持 `manual_review`。

当前仓库没有遍历 Delivery 数据库引用的恢复命令，因为 Delivery PostgreSQL schema 尚未提交。
在该命令出现前，服务器级备份恢复验收保持 `blocked`。

## 对账

外部 effect 返回超时、连接重置、非法响应或结果不匹配时，执行状态进入
`reconciliation_required`。每个 unknown effect 必须有且只有一个 reconciliation item。

对账顺序：

1. 按服务端保存的幂等键摘要查询外部平台。
2. 如果平台确认成功，保存平台回执并把 effect 标记为 `succeeded`。
3. 如果平台确认未执行，按 adapter 契约决定是否重试。
4. 如果平台无法确定结果，保持 unknown 并推进 `next_check_at`。
5. 达到重试或时间上限后进入 `manual_review`。

不要提供“盲目重试”按钮。审批确认的 effect 集和 execution effect 集必须完全相同。

## 告警

Delivery 指标尚未接入 Prometheus。接入后至少创建以下告警：

| 条件 | 建议级别 |
|---|---|
| `delivery_artifact_integrity_failures_total` 增长 | P0 |
| 发布计划出现硬约束 violation | P0 |
| `reconciliation_required` 超过业务时限 | P1 |
| worker 反复失去租约 | P1 |
| solver 超时率持续升高 | P1 |
| SSE 重连率或快照读取错误率持续升高 | P2 |
| adapter 429 或 5xx 比例升高 | P2 |

tenant、订单、车辆、run 和 digest 不进入 Prometheus label。它们只进入受控日志或 trace。

## 故障排查

| 现象 | 检查 | 处理 |
|---|---|---|
| `dataset manifest is stale` | generator version、seed 或规则是否变化 | 审核变化后运行 `generate --write`，不要只改摘要 |
| adapter 为 `blocked` | 对应 `DELIVERY_BENCHMARK_*_COMMAND` 是否设置 | 配置 JSON 字符串数组并重跑 |
| adapter 为 `failed` | solver 是否写入严格 `delivery.plan.v1` | 在临时环境复现命令，修复输出或 solver |
| acceptance probe 为 `blocked` | 对应 `DELIVERY_ACCEPTANCE_*_COMMAND` 是否设置 | 配置 JSON argv 并重跑 |
| acceptance probe 为 `failed` | 命令退出状态和 result JSON 是否符合配置 | 修复探针或产品能力，不要手工改报告 |
| plan digest 不一致 | 是否使用 wall-clock、无序 map 或并行归并 | 改用固定预算和稳定归并顺序 |
| Validator 返回 `V0xx` | schema 或摘要绑定错误 | 重新 canonicalize 并绑定问题、策略、承诺和计划摘要 |
| Validator 返回 `V7xx` 至 `V10xx` | 三维坐标、支撑、提取、轴载或重心错误 | 修复 plan，不要让 Validator 修改计划 |
| workspace 显示契约错误 | HTTP 2xx 内容不符合 Zod schema | 对照 `web/src/delivery/contract.ts` 修复生产者 |
| 审批按钮锁定 | SSE 未进入 `online` | 恢复事件连接并从最后游标续传 |
| artifact 打不开 | tenant、摘要、权限或文件完整性错误 | 调用 `Verify`，隔离损坏资源 |

故障修复后，重新运行单元测试、Delivery 浏览器验收、基准检查和受影响的恢复场景。
