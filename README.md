# waybill-guardian 运单守护

异常运单处置 Agent。「AI 重构产业 · 架构师大赛」AI+物流赛道参赛项目。

延误、破损或丢件发生后，Agent 查询运单、轨迹、司机和天气，形成归因和处置方案。
所有 TMS 和通知写操作都先进入人工审批。系统在确认后执行写操作，并把全过程写入可校验、
可回放的审计日志。

## 当前能力

- 七个工具与 [`contract.yaml`](./contract.yaml) 对齐。四个只读工具自动执行，三个写工具强制审批。
- hastekit v0.0.24 负责 Agent loop、typed tools 和 HITL pause/resume；history 可使用本地文件或 PostgreSQL。
- 模型和 Agent history 只接收业务证据白名单，不保存手机号、车牌、精确坐标或短信模板参数。
- 服务端根据业务参数生成稳定 `effect_id` 和幂等键。同一 effect 并发执行十次时，platform 只收到一次调用。
- 默认每个 run 使用一份 append-only JSONL。PostgreSQL 模式在同一事务提交业务投影、审计和 outbox。
- SSE 支持 `Last-Event-ID` 续传。前端按 `(run_id, seq)` 去重。
- 审批支持确认、驳回和超时。首选运力被驳回后，Agent 会提交第二个候选方案。
- `PLATFORM=file` 在启动时严格加载 JSON/CSV v1，页面可选择并处置文件中的任意运单。
- 首页按公路港和线路聚合异常运单。风险队列支持一次启动 5 个独立 run，每个 run 保留自己的审批和 SSE。
- 当前无认证版本只监听 loopback，并用进程锁阻止两个实例共享同一个数据目录。
- 默认演示不需要模型密钥或高德密钥。

## 快速开始

准备 Go 1.25.3 或更高版本、Node.js 22.12 或更高版本，以及 npm。

```bash
git clone https://github.com/Duang777/waybill-guardian.git
cd waybill-guardian
./scripts/demo.sh
```

脚本首次运行时执行 `npm ci`，然后加载仓库中的 72 港仿真数据。打开
<http://127.0.0.1:5173> 查看经营总览。选择异常运单后点击 **交给 Agent**，或下钻到
`/waybills/:id` 处理单张运单。按 `Ctrl+C` 会同时停止两个进程。

演示流程如下：

1. Agent 查询杭州到成都运单 `YD2026101001`。
2. 时间线显示运单、轨迹、司机和天气工具调用。
3. Agent 根据连续驾驶 9 小时、绵阳北服务区停留 6 小时和晴天数据完成归因。
4. 审批卡片显示改派、货主通知和司机通知三个写操作。
5. 点击 **确认并执行**。页面显示三次平台写入和处置完成事件。
6. 点击回放按钮，从第一条审计事件重新播放时间线。

高德地图未配置时，页面使用内置坐标轨迹。此降级不影响完整流程。

## 启动选项

脚本接受以下环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `BACKEND_HOST` | `127.0.0.1` | API 监听地址 |
| `BACKEND_PORT` | `8080` | API 端口 |
| `WEB_HOST` | `127.0.0.1` | 前端监听地址 |
| `WEB_PORT` | `5173` | 前端端口 |
| `DATA_DIR` | `./data` | 审计日志和 hastekit history 目录 |
| `APPROVAL_TTL` | `10m` | 审批有效期，使用 Go duration 格式 |
| `HISTORY_RETENTION` | `168h` | 已结束 Agent history 的保留期，必须为正数 |
| `DEMO_STEP_DELAY` | `220ms` | 确定性模型每一步的演示延迟 |
| `PLATFORM` | `file` | `mock` 使用内嵌数据；`file` 加载 JSON/CSV；`real` 未实现时拒绝启动 |
| `DATA_FILE` | `./data/simulated/waybills-v1.json` | `PLATFORM=file` 时必填的 JSON/CSV v1 文件 |
| `MAX_CONCURRENT_RUNS` | `8` | 同时执行调查阶段的 run 数量，范围 1 到 64 |
| `EVIDENCE_STEP_MINUTES` | `8` | 人工完成一次证据采集的估算分钟数，必须为正数 |
| `STORAGE` | `jsonl` | `jsonl` 用于离线演示；`postgres` 使用事务仓储 |
| `AUTH_MODE` | `local` | `local` 使用本机演示身份；`jwt` 验证 Bearer JWT |
| `AUTH_JWT_ISSUER` | 空 | `AUTH_MODE=jwt` 时必填，必须精确匹配 JWT `iss` |
| `AUTH_JWT_AUDIENCE` | 空 | `AUTH_MODE=jwt` 时必填，必须包含在 JWT `aud` |
| `AUTH_JWT_PUBLIC_KEY_FILE` | 空 | `AUTH_MODE=jwt` 时必填，PEM 编码的 RSA 公钥 |
| `DATABASE_URL` | 空 | `STORAGE=postgres` 时必填，不应写入日志或仓库 |
| `PG_MAX_CONNS` | `8` | PostgreSQL 连接池最大连接数 |
| `PG_MIN_CONNS` | `0` | PostgreSQL 连接池最小连接数 |
| `PG_STARTUP_TIMEOUT` | `30s` | PostgreSQL 连接、检查和迁移的总超时 |
| `TENANT_ID` | `local-demo` | HTTP 授权和 PostgreSQL 数据的租户边界；JWT 模式必须显式设置 |
| `INSTANCE_ID` | 随机 UUID | PostgreSQL run、effect 和 outbox 租约的 worker 身份 |
| `RUN_LEASE_TTL` | `30s` | PostgreSQL run 和 effect 租约时长 |
| `OUTBOX_ENABLED` | `false` | 在 PostgreSQL 模式显式启动 outbox dispatcher |
| `OUTBOX_URL` | 空 | dispatcher 的 CloudEvents HTTP endpoint |
| `OUTBOX_TOKEN` | 空 | dispatcher 启动时必填的 Bearer token |
| `OUTBOX_BATCH_SIZE` | `10` | 每次 claim 的最大事件数，范围 1 到 100 |
| `OUTBOX_CONCURRENCY` | `4` | 同时发布的最大事件数，范围 1 到 100 |
| `OUTBOX_POLL_INTERVAL` | `250ms` | 无可发布事件或 claim 失败后的轮询间隔 |
| `OUTBOX_LEASE_TTL` | `30s` | outbox claim 租约时长 |
| `OUTBOX_STATS_INTERVAL` | `15s` | outbox 指标快照刷新间隔 |
| `OUTBOX_HTTP_TIMEOUT` | `10s` | 单次下游发布超时 |
| `METRICS_ADDR` | 空 | 独立 Prometheus listener；例如 `127.0.0.1:9090` |
| `CHECKPOINT_KEY_ID` | `local-v1` | checkpoint 加密密钥版本 |
| `CHECKPOINT_ENCRYPTION_KEY` | 空 | PostgreSQL 模式必填，Base64 编码的 32 字节 AES-256 key |
| `AGENT_MODE` | `demo` | `demo` 使用确定性模型；`online` 调用外部模型 |

例如，使用其他端口和临时数据目录：

```bash
BACKEND_PORT=18080 WEB_PORT=15173 DATA_DIR=/tmp/waybill-demo ./scripts/demo.sh
```

### 使用 JSON/CSV 数据

仓库提供等价的 v1 模板：
[`data/templates/waybills-v1.json`](./data/templates/waybills-v1.json) 和
[`data/templates/waybills-v1.csv`](./data/templates/waybills-v1.csv)。先用与服务端相同的
loader 校验文件：

```bash
env -u GOROOT go run ./cmd/dataimport validate --data ./official-v1.csv
```

校验成功后启动文件模式：

```bash
PLATFORM=file \
DATA_FILE=./official-v1.csv \
DATA_DIR=/tmp/waybill-file-demo \
./scripts/demo.sh
```

服务启动时一次性加载完整文件，任何语法、引用、坐标或时间顺序错误都会在监听端口前失败。
运行期间不会热更新数据；替换文件后需要重启。格式、字段矩阵与校验规则见
[`docs/file-data-source-design.md`](./docs/file-data-source-design.md)。

仓库还提供固定生成规则的非官方仿真数据，包含 72 个公路港、72 条线路、200 台车辆、
200 张运单和 5 类异常：

```bash
env -u GOROOT go run ./cmd/datagenerate \
  --output ./data/simulated/waybills-v1.json \
  --waybills 200

env -u GOROOT go run ./cmd/dataimport validate \
  --data ./data/simulated/waybills-v1.json
```

`hubs`、`vehicles`、`routes` 以及运单上的港口、线路、车辆引用是 v1 的可选网络扩展。
一旦文件提供任一网络实体，校验器会要求三类实体和全部引用同时完整，避免聚合视图读取到
半套拓扑。仿真数据只用于产品演示和容量验证，不代表真实经营数据。

### 经营总览和 KPI

`GET /api/overview` 返回授权范围内的港口、线路、异常队列和三条经营简报。服务先按
`waybill_id` 授权范围过滤，再计算所有总数和比例。简报只读取聚合结果，不调用写工具。

`POST /api/runs:batch` 接受最多 20 个 `waybill_id`。服务为每个运单调用一次 `StartRun`，
并返回逐项成功或失败结果。`MAX_CONCURRENT_RUNS` 限制同时执行的调查任务。每个已接受的
run 使用独立的 `run_id`、审批记录和 SSE 时间线。

`GET /api/kpis?window=24h` 使用以下口径。窗口结束时间取授权范围内最新异常运单的
`last_recorded_at`。如果审计事件更新，则使用较新的审计时间。

| KPI | 公式 | 数据不足时的结果 |
|---|---|---|
| 时效挽回 | `sum(不处置预测 ETA - 处置后 ETA)` | 缺少两个 ETA 字段时返回 `unavailable` |
| 成本影响 | `sum(避免违约金 - 改派差价 - 处置成本)` | 缺少价格和成本字段时返回 `unavailable` |
| 人力节省 | `成功自动证据采集步数 * EVIDENCE_STEP_MINUTES / 60` | 没有采集事件时返回 `0` 小时 |
| 异常闭环率 | `已完成或已驳回处置的异常运单数 / 窗口内异常运单数 * 100%` | 没有异常运单时返回 `0%` |
| 平均处置时长 | `sum(终态时间 - 启动时间) / 窗口内闭环 run 数` | 没有闭环 run 时返回 `unavailable` |
| 人工审批通过率 | `人工确认数 / 人工决定数 * 100%` | 没有人工决定时返回 `unavailable` |

时效挽回和成本影响不会用仿真假设补值。接入正式数据后，adapter 必须提供计算公式所需的
基线、结果和成本字段。

`AUTH_MODE=local` 时，`BACKEND_HOST` 必须是 loopback IP 字面量。直接运行
`go run ./cmd/server` 时，`HTTP_ADDR` 默认是 `127.0.0.1:8080`。local 模式拒绝空 host、
主机名、通配地址、非 loopback 地址，以及 Host 不是 loopback IP 的请求。审批审计主体固定为
`local-demo-reviewer`。

`AUTH_MODE=jwt` 验证 RS256 签名、issuer、audience、`iat`、`nbf` 和 `exp`。JWT 还必须包含
`sub`、`tenant_id`、`roles`，以及 `waybill_all=true` 或非空 `waybill_ids`。可用角色为
`viewer`、`dispatcher`、`operator` 和 `event_producer`。`event_producer` token 还必须包含
非空且无重复的 `event_sources` 和 `event_types`。服务忽略
`X-Actor`，并把已验证的 `sub` 写入 `decided_by`。请求 context 的 deadline 不晚于 JWT
有效期，因此时间线 SSE 会在凭据到期时断开。JWT 模式可以监听显式非 loopback IP，但服务
本身不终止 TLS。远程部署必须放在 HTTPS 入口后。`PLATFORM=real` 要求
`STORAGE=postgres` 和 `AUTH_MODE=jwt`，真实 adapter 尚未实现，因此仍会拒绝启动。

`STORAGE=postgres` 注册 `POST /v1/events`。JSONL 模式不注册该路由。dispatcher 默认关闭，
因为首次启用会按 aggregate 顺序发布已有的 pending 事件。除 loopback 测试地址外，
`OUTBOX_URL` 必须使用 HTTPS。设置 `METRICS_ADDR` 后，独立 listener 在 `/metrics` 暴露
固定标签的入站和 outbox 指标。

### 使用在线模型

在线模式支持 OpenAI Responses API 和 OpenAI-compatible Chat Completions API：

```bash
AGENT_MODE=online \
LLM_API_STYLE=responses \
LLM_BASE_URL=https://api.openai.com/v1 \
LLM_API_KEY=replace-me \
LLM_MODEL=gpt-5-mini \
./scripts/demo.sh
```

将 `LLM_API_STYLE` 改为 `chat_completions` 可接兼容服务。`LLM_BASE_URL` 必须是 API 根路径，
不能以 `/` 结尾，也不能包含 `/responses` 或 `/chat/completions`。在线模式只配置一个
provider，模型调用最多尝试三次，不启用 provider fallback。

### 使用高德地图

在 `web/.env.local` 中配置浏览器端密钥。该文件已被 `.gitignore` 排除。

```dotenv
VITE_AMAP_KEY=replace-me
VITE_AMAP_SECURITY_JS_CODE=replace-me
```

## 验证

运行后端检查：

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
./scripts/check-production-fixture-literals.sh
./scripts/check-history-governance.sh
```

使用 Docker 启动临时 PostgreSQL 17，验证迁移、事务、租约、加密 history 和浏览器完整流程：

```bash
./scripts/test-postgres.sh
```

运行前端检查和浏览器端完整流程：

```bash
cd web
npm ci
npm test
npm run build
npm audit
npm run verify:e2e
npm run verify:file-e2e
npm run verify:overview
```

`verify:e2e` 启动隔离的后端和前端，连续确认三次演示，再验证一次驳回路径。脚本还检查移动端
横向溢出、按钮尺寸和截图。`verify:file-e2e` 使用双运单 CSV 验证目录切换、旧响应抑制、
动态路线与异常标签，以及所选运单的完整审批执行。
`verify:overview` 使用 72 港仿真数据检查 4 项主 KPI、港网 SVG、5 单批量触发、SSE 状态、
单运单下钻，以及 1280、375 和 320 像素宽度。

## 录制演示

安装 Chrome、ffmpeg 和 ffprobe 后运行：

```bash
cd web
npm run record:demo
```

脚本启动隔离的前后端，依次录制确认、审计回放和驳回路径，再生成
`web/artifacts/waybill-guardian-demo.mp4`。视频为 1600×900、无音轨，并带中文讲解字幕。
正式配音可按 [`docs/demo-script.md`](./docs/demo-script.md) 录制。

使用 `RECORD_OUTPUT=/absolute/path/demo.mp4` 可修改输出路径。端口冲突时，通过
`RECORD_BACKEND_PORT` 和 `RECORD_WEB_PORT` 指定其他端口。

## 架构与边界

`cmd/server` 只处理 HTTP 和 SSE。`internal/guardian` 协调 Agent、审批、幂等和恢复。
默认模式由 `internal/audit` 的 JSONL 保存业务事实，hastekit file history 保存模型消息和
pending tool calls。PostgreSQL 模式不打开这些业务文件，数据库是 run、审批、effect、审计、
outbox 和 Agent history 的唯一事实源。

本地运行时将 `DATA_DIR` 和 hastekit history 目录权限设为 `0700`，数据文件设为 `0600`。
它对 `DATA_DIR` 的目录文件描述符持有独占锁，第二个使用同一目录的进程会拒绝启动。对于
“外部平台成功，但本地成功事件还未写入”的窗口，系统不会自动重试未知结果。真实 adapter
必须按幂等键查询或重试，否则不能启用真实写模式。

- Agent history 使用 `history_schema_version=1`。本地文件用 sidecar 标记状态；PostgreSQL
  使用 `privacy_schema_version=1`，并用 AES-256-GCM 加密 payload。
- 服务启动时删除超过 `HISTORY_RETENTION` 的已结束 history。运行中的 history 和独立审计
  事件不受该清理影响。
- [`scripts/check-history-governance.sh`](./scripts/check-history-governance.sh) 扫描本地
  history。设置 `DATABASE_URL` 后，脚本也检查 PostgreSQL history 的明文列。

- 架构、恢复矩阵和取舍：[`docs/RFC-001.md`](./docs/RFC-001.md)
- 真实平台接入与生产处置链路：[`docs/RFC-002.md`](./docs/RFC-002.md)
- Agent history 隐私与保留策略：[`docs/history-governance.md`](./docs/history-governance.md)
- 文件数据源与任意运单：[`docs/file-data-source-design.md`](./docs/file-data-source-design.md)
- hastekit 源码研究：[`docs/research/hastekit-v0.0.24.md`](./docs/research/hastekit-v0.0.24.md)
- 三分钟演示讲稿：[`docs/demo-script.md`](./docs/demo-script.md)
- 模块职责索引：[`AGENTS.md`](./AGENTS.md)
- 第三方来源声明：[`THIRD_PARTY_NOTICES.md`](./THIRD_PARTY_NOTICES.md)

### 删除 Agent history

停止服务后，删除本地 history：

```bash
rm -rf "${DATA_DIR:-data}/hastekit"
```

PostgreSQL 模式按租户删除 history，并清理已结束 run 的 checkpoint 指针：

```sql
BEGIN;
DELETE FROM waybill.agent_summaries WHERE tenant_id = :'tenant_id';
DELETE FROM waybill.agent_checkpoints WHERE tenant_id = :'tenant_id';
UPDATE waybill.runs
SET sdk_run_id = NULL, checkpoint_version = 0
WHERE tenant_id = :'tenant_id'
  AND status IN ('completed', 'rejected', 'failed', 'manual_review');
COMMIT;
```

不要清理运行中的 PostgreSQL run。删除 history 不会删除 `audit_events`。
