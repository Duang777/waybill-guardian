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

<p align="center">异常运单处置 Agent。先取证，再请人确认。</p>

<p align="center">
  <a href="https://go.dev/dl/"><img alt="Go 1.25.3" src="https://img.shields.io/badge/Go-1.25.3-00ADD8?logo=go&logoColor=white"></a>
  <a href="https://react.dev/"><img alt="React 19.3.0" src="https://img.shields.io/badge/React-19.3.0-087EA4?logo=react&logoColor=white"></a>
  <a href="https://nodejs.org/"><img alt="Node.js 22.12 或更高版本" src="https://img.shields.io/badge/Node.js-%3E%3D22.12-339933?logo=nodedotjs&logoColor=white"></a>
  <a href="https://github.com/Duang777/waybill-guardian/issues"><img alt="GitHub issues" src="https://img.shields.io/github/issues/Duang777/waybill-guardian"></a>
  <a href="https://github.com/Duang777/waybill-guardian/issues/62"><img alt="许可证尚未选定" src="https://img.shields.io/badge/license-pending-lightgrey"></a>
</p>

传化集团与动势科技的「AI 重构产业架构师大赛」，赛道是 AI+物流。本仓库是参赛项目。标志是原创图形，没有使用主办方商标。

## 给评委

延误、破损或丢件发生后，waybill-guardian 读取运单、轨迹、司机和天气，整理归因，并给出改派、赔付或短信方案。写操作在人工确认前不会执行。确认之后，服务按幂等键回写，并把全过程写入可以校验、可以回放的审计日志。

调度员少做的是在几个系统之间来回查证据。人仍然决定是否改派、是否赔付、是否发通知。复核时按审计序号重放。

仓库里没有时效挽回、成本或人力节省的测量数字。页面上的三项风险分是常量，ETA 延误 86、路况 34、天气 8，写在 [`internal/guardian/service.go`](internal/guardian/service.go)。它们不是算出来的指标。

**默认演示不是在线推理。** `AGENT_MODE=demo` 使用 [`internal/agent/scenario_model.go`](internal/agent/scenario_model.go) 里的 `ScenarioModel`。它按固定顺序调用工具。司机编号、路线、承运商和归因句子写在代码里。把演示改成真实模型推理，见 [issue 59](https://github.com/Duang777/waybill-guardian/issues/59)。

`AGENT_MODE=online` 可以调用 OpenAI Responses API，或 OpenAI 兼容的 Chat Completions API。这是已经接上的调用路径。issue 59 要求的是默认演示走真实推理、结构化证据引用，以及国产模型验收。这些还没有做。

数据只有内置的一张运单 `YD2026101001`，文件是 [`internal/tools/testdata/demo.json`](internal/tools/testdata/demo.json)。没有文件导入，也不能对任意运单启动。见 [issue 60](https://github.com/Duang777/waybill-guardian/issues/60)。

页面是这一张运单的工作台。没有全国公路港总览。见 [issue 61](https://github.com/Duang777/waybill-guardian/issues/61)。当前界面也还不是黑橙指挥中心，那是 [issue 77](https://github.com/Duang777/waybill-guardian/issues/77)。

<p align="center">
  <img alt="桌面宽度下，脚本演示停在人工审批。方案是改派到川行快运，并通知货主和司机。地图是本地轨迹。" src="docs/assets/console-approval.png" width="840">
</p>

<p align="center">
  <img alt="手机宽度下，同一次脚本演示在确认后显示处置完成。" src="docs/assets/console-completed-mobile.png" width="280">
</p>

上面两张图来自 `npm run verify:e2e`，没有配置高德 key，所以地图显示本地轨迹。审批卡上的归因句子来自 `ScenarioModel`，不是当次模型推理。

## 架构

```mermaid
flowchart TD
  incident["异常运单"]
  agent["hastekit Agent"]
  readtools["四个只读工具"]
  fixture["内置样例 demo.json"]
  approval["人工审批"]
  writetools["写工具"]
  platformbox["Mock 或改派沙箱"]
  auditlog["审计日志"]

  incident --> agent
  agent --> readtools
  readtools --> fixture
  readtools --> agent
  agent --> approval
  approval --> writetools
  writetools --> platformbox
  agent --> auditlog
  writetools --> auditlog
```

`cmd/server` 提供 HTTP 和 SSE。`internal/guardian` 串起 Agent、审批、幂等和恢复。Agent 运行时是 hastekit `agent-sdk-go` v0.0.24。

四个只读工具是 `tms.get_waybill`、`tms.get_tracking`、`tms.get_driver`、`ext.get_road_weather`。它们自动执行。三个写工具是 `tms.reassign`、`tms.create_claim`、`notify.send_sms`。它们在执行前暂停，等人工决定。契约在 [`contract.yaml`](contract.yaml)。

默认 `PLATFORM=mock` 时，读和写都走内置样例。短信不会真正发出。`PLATFORM=real` 必须同时使用 PostgreSQL 和 JWT。读路径仍是 `fixture-v1`。写路径只有 `tms.reassign`，发到 HTTP 沙箱 `tms-reassign-sandbox-v1`。这个 profile 不注册赔付和短信。它用来验证网络写入和对账，不是生产 TMS，也不是官方数据导入。

审计默认是每个 run 一份 append-only JSONL，带 `seq`、`prev_hash` 和 `hash`。`STORAGE=postgres` 时，业务投影、审计和 outbox 在同一事务里提交。时间线用 SSE，客户端可以用 `Last-Event-ID` 续传。

审批从 `pending` 开始。确认后是 `confirmed`，驳回是 `rejected`，超时是 `expired`。写操作全部成功后是 `executed`。部分成功是 `partially_failed`，全部失败是 `failed`。结果还不能确定时是 `reconciliation_required`。

## 能力

状态只描述当前 `main` 上的代码。

| 状态 | 含义 |
|---|---|
| 已交付 | 默认分支可以运行，范围写在说明里 |
| 进行中 | 有一部分代码，issue 的验收标准还没达到 |
| 计划中 | 默认分支没有这项能力 |

| 能力 | 状态 | 说明 |
|---|---|---|
| 四个只读工具，三个写工具，人工审批 | 已交付 | 与 [`contract.yaml`](contract.yaml) 对齐。mock 下三个写操作都要审批。 |
| 服务端幂等键 | 已交付 | 模型不提交 `effect_id` 或幂等键。同一键的 10 个并发调用只会进入 platform 一次，见 [`idempotency_test.go`](internal/idempotency/idempotency_test.go)。 |
| JSONL 审计、哈希链、SSE 回放 | 已交付 | 前端按 run 和 `seq` 去重。 |
| PostgreSQL 存储 | 已交付 | 保存 run、审批、effect、审计、outbox，以及 AES-256-GCM 加密的 Agent history。 |
| 本地身份和 JWT | 已交付 | `AUTH_MODE=local` 只监听 loopback。`jwt` 校验 RS256、issuer、audience、时效、租户、角色和运单范围。 |
| 高德地图或本地轨迹 | 已交付 | 没有 key，或 SDK 加载失败时，页面改用本地坐标。 |
| 改派 HTTP 沙箱 | 已交付 | 仅 `tms.reassign`。读仍是内置样例。不是生产 TMS。 |
| 在线模型调用 | 进行中 | 可以配置 OpenAI 兼容 API。默认演示仍是脚本。验收标准见 [issue 59](https://github.com/Duang777/waybill-guardian/issues/59)。 |
| 文件导入和任意运单 | 计划中 | [issue 60](https://github.com/Duang777/waybill-guardian/issues/60) |
| 公路港总览 | 计划中 | [issue 61](https://github.com/Duang777/waybill-guardian/issues/61) |
| 许可证文件 | 计划中 | [issue 62](https://github.com/Duang777/waybill-guardian/issues/62) |
| 非 GET 请求的 CSRF 检查 | 计划中 | [issue 64](https://github.com/Duang777/waybill-guardian/issues/64) |
| Docker Compose | 计划中 | [issue 65](https://github.com/Duang777/waybill-guardian/issues/65) |
| GitHub Actions | 计划中 | [issue 67](https://github.com/Duang777/waybill-guardian/issues/67)。仓库里还没有 workflow，所以本页没有 CI 徽章。 |
| 指挥中心视觉 | 计划中 | [issue 77](https://github.com/Duang777/waybill-guardian/issues/77)，包含 issue 69 到 76。 |

## 演示

讲稿在 [`docs/demo-script.md`](docs/demo-script.md)。启动：

```bash
./scripts/demo.sh
```

打开 <http://127.0.0.1:5173>，点击 **启动演示**。

1. Agent 查询杭州到成都运单 `YD2026101001`。
2. 时间线记下运单、轨迹、司机和天气四次工具调用。
3. 脚本根据连续驾驶 9 小时、绵阳北服务区停留 6 小时和晴天数据写出归因，并暂停在审批卡。方案是改派到川行快运，再通知货主和司机。这三个写操作此时还没有执行。
4. 点击 **确认并执行**。时间线出现平台写入，运单状态变为处置完成。
5. 用回放控件从第一条审计事件再看一遍，然后点 **实时** 回到末尾。
6. 如果驳回首选承运商，脚本会改提蜀道联运。`npm run verify:e2e` 覆盖了确认三次和驳回一次。

带中文字幕、没有音轨的录像：

```bash
cd web
npm run record:demo
```

输出在 `web/artifacts/waybill-guardian-demo.mp4`，1600×900。这个目录被 git 忽略。正式配音还不在仓库里。可用 `RECORD_OUTPUT`、`RECORD_BACKEND_PORT` 和 `RECORD_WEB_PORT` 改输出路径和端口。

## 快速开始

需要 Go 1.25.3 或更高版本，以及 Node.js 22.12 或更高版本。本次核对使用 Go 1.25.3 和 Node.js 22.14.0。`./scripts/demo.sh` 能把 API 和前端拉起来，`npm run verify:e2e` 已通过。

```bash
git clone https://github.com/Duang777/waybill-guardian.git
cd waybill-guardian
./scripts/demo.sh
```

脚本在缺少 `web/node_modules/.bin/vite` 时先执行 `npm ci`，然后编译 API 并启动前端。就绪后打印 `Waybill Guardian is ready`。`Ctrl+C` 会停掉两个进程。

换端口和数据目录：

```bash
BACKEND_PORT=18080 WEB_PORT=15173 DATA_DIR=/tmp/waybill-demo ./scripts/demo.sh
```

检查命令：

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
./scripts/check-history-governance.sh
```

`./scripts/test-postgres.sh` 用 Docker 启动临时 PostgreSQL 17，检查迁移、事务、租约和加密 history。

```bash
cd web
npm ci
npm test
npm run build
npm run verify:e2e
```

## 配置

`./scripts/demo.sh` 会设置下面四项，其余变量从当前 shell 继承。直接运行 `go run ./cmd/server` 时，监听地址用 `HTTP_ADDR`，默认 `127.0.0.1:8080`。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `BACKEND_HOST` | `127.0.0.1` | API 监听地址，只给 `demo.sh` 使用 |
| `BACKEND_PORT` | `8080` | API 端口，只给 `demo.sh` 使用 |
| `WEB_HOST` | `127.0.0.1` | 前端监听地址，只给 `demo.sh` 使用 |
| `WEB_PORT` | `5173` | 前端端口，只给 `demo.sh` 使用 |
| `HTTP_ADDR` | `127.0.0.1:8080` | 服务监听地址。local 模式必须是 loopback IP |
| `DATA_DIR` | `data` | JSONL 审计和 hastekit history 目录 |
| `AGENT_MODE` | `demo` | `demo` 使用 `ScenarioModel`。`online` 调用外部模型 |
| `PLATFORM` | `mock` | `mock` 使用内置样例。`real` 使用改派沙箱，见下文 |
| `STORAGE` | `jsonl` | `jsonl` 或 `postgres` |
| `AUTH_MODE` | `local` | `local` 或 `jwt` |
| `APPROVAL_TTL` | `10m` | 审批有效期，Go duration。非法值会退回默认值 |
| `HISTORY_RETENTION` | `168h` | 已结束 Agent history 的保留期，必须为正数 |
| `DEMO_STEP_DELAY` | `220ms` | 脚本模型每一步的等待 |
| `TENANT_ID` | `local-demo` | JWT 模式必须显式设置 |
| `INSTANCE_ID` | 随机 UUID | PostgreSQL 租约里的 worker 身份 |

### 在线模型

```bash
AGENT_MODE=online \
LLM_API_STYLE=responses \
LLM_BASE_URL=https://api.openai.com/v1 \
LLM_API_KEY=replace-me \
LLM_MODEL=gpt-5-mini \
./scripts/demo.sh
```

`LLM_API_STYLE` 可以是 `responses` 或 `chat_completions`，默认 `responses`。`LLM_BASE_URL` 必须是 API 根路径，不能以 `/` 结尾，也不能带上 `/responses` 或 `/chat/completions`。在线模式只配置一个 provider，没有 provider fallback。一次模型调用最多尝试三次，包含第一次。

这仍不是 issue 59 里的默认真实推理演示。

### 平台

`PLATFORM=mock` 不访问外部系统。

`PLATFORM=real` 还要求：

| 变量 | 要求 |
|---|---|
| `STORAGE` | `postgres` |
| `AUTH_MODE` | `jwt` |
| `REAL_PLATFORM_PROFILE` | `tms-reassign-sandbox-v1` |
| `REAL_READ_SOURCE` | `fixture-v1` |
| `TMS_SANDBOX_BASE_URL` | 沙箱根地址 |
| `TMS_SANDBOX_TOKEN` | Bearer token |
| `TMS_SANDBOX_ACCOUNT` | 必须等于 `TENANT_ID` |

相关超时有 `PLATFORM_REQUEST_TIMEOUT`（默认 `3s`）、`PLATFORM_STARTUP_TIMEOUT`（默认 `5s`）、`EFFECT_RECONCILE_HORIZON`（默认 `24h`）、`EFFECT_RECONCILE_POLL_INTERVAL`（默认 `1s`）和 `PLATFORM_MAX_LOOKUP_CONSISTENCY_WINDOW`（默认 `30s`）。

### 存储、认证和 outbox

`STORAGE=postgres` 时必须提供 `DATABASE_URL` 和 `CHECKPOINT_ENCRYPTION_KEY`。后者是 Base64 编码的 32 字节 AES-256 key。`CHECKPOINT_KEY_ID` 默认 `local-v1`。

连接池：`PG_MAX_CONNS` 默认 8，`PG_MIN_CONNS` 默认 0，`PG_STARTUP_TIMEOUT` 默认 `30s`。`RUN_LEASE_TTL` 默认 `30s`，`EFFECT_LEASE_TTL` 默认 `15s`。

`AUTH_MODE=jwt` 还要 `AUTH_JWT_ISSUER`、`AUTH_JWT_AUDIENCE` 和 `AUTH_JWT_PUBLIC_KEY_FILE`。公钥是 PEM 编码的 RSA 公钥。JWT 需要 `sub`、`tenant_id`、`roles`，以及 `waybill_all=true` 或非空 `waybill_ids`。角色可以是 `viewer`、`dispatcher`、`operator`、`event_producer`。服务不终止 TLS。非 loopback 部署要放在 HTTPS 入口后面。

`POST /v1/events` 只在 PostgreSQL 模式注册。outbox dispatcher 默认关闭。`OUTBOX_ENABLED=true` 时必须提供 `OUTBOX_URL` 和 `OUTBOX_TOKEN`。除 loopback 测试地址外，`OUTBOX_URL` 必须是 HTTPS。`METRICS_ADDR` 例如 `127.0.0.1:9090`，在独立端口的 `/metrics` 暴露 Prometheus 指标。这两项都要求 PostgreSQL。

| 变量 | 默认值 |
|---|---|
| `OUTBOX_BATCH_SIZE` | `10`，最大 100 |
| `OUTBOX_CONCURRENCY` | `4`，最大 100 |
| `OUTBOX_POLL_INTERVAL` | `250ms` |
| `OUTBOX_LEASE_TTL` | `30s` |
| `OUTBOX_STATS_INTERVAL` | `15s` |
| `OUTBOX_HTTP_TIMEOUT` | `10s` |

### 前端

把高德配置写进 `web/.env.local`。该文件已被 git 忽略。

```dotenv
VITE_AMAP_KEY=replace-me
VITE_AMAP_SECURITY_JS_CODE=replace-me
```

`VITE_API_TARGET` 是 Vite 开发代理的 API 地址，默认 `http://127.0.0.1:8080`。`demo.sh` 会把它设成当前 API。

### 删除 Agent history

先停掉服务。删除本地 history：

```bash
rm -rf "${DATA_DIR:-data}/hastekit"
```

PostgreSQL 按租户删除 history，并清掉已结束 run 的 checkpoint 指针：

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

不要清理仍在运行的 run。删除 history 不会删除 `waybill.audit_events`。

## 安全与合规

写工具带有 `RequiresApproval`。hastekit 在工具执行前把 run 停在 `await_approval`。人工决定先写入审计，服务再用同一个 thread 恢复。确认、驳回和超时争用同一把 run 锁，所以只会落下一种决定。超时按驳回恢复 Agent。默认有效期是 10 分钟。

模型只提交业务参数。服务端生成 `effect_id` 和幂等键。middleware 在恢复执行时核对 `call_id`、参数哈希和 `effect_id`。同一幂等键的并发调用会合并。测试里 10 个并发调用只进入 platform 一次。已经失败的调用可以按新的 attempt 重试。如果外部系统已经成功，但本地成功事件还没写上，状态是 indeterminate，服务不会自动重试。真实 adapter 必须能按同一个键重试，或按键查询结果。

审计事件追加写入。`seq`、`prev_hash` 和 `hash` 用来检查这条链有没有被改过。SSE 先按游标回放，再推 live 事件。

读工具返回给模型的字段不包括电话、车牌和精确坐标。短信工具只接收运单、接收方角色和承运商。电话和模板在审批之后由服务端解析。history guard 拒绝把电话、车牌、精确坐标和短信供应商参数写进模型 history。运营接口里的电话和车牌会打码。地图接口仍会返回轨迹坐标，供控制台画线。

`AUTH_MODE=local` 只接受 loopback IP，并拒绝 Host 不是 loopback IP 的请求。审批主体固定为 `local-demo-reviewer`。客户端送来的 `Authorization` 和 `X-Actor` 不决定身份。

非 GET 接口还没有 Origin 或 Sec-Fetch-Site 检查。见 [issue 64](https://github.com/Duang777/waybill-guardian/issues/64)。

## 开源声明

直接依赖、npm 包和参考项目写在 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

hastekit `agent-sdk-go` v0.0.24 以 Go module 引入，许可证是 Apache-2.0。本仓库没有复制它的源码。

下面三个项目只用来比较交互和领域划分，没有源文件进入本仓库：

- [jattiphrswan/logistics-tracker](https://github.com/jattiphrswan/logistics-tracker)
- [09karankr/port-logistics-intelligence](https://github.com/09karankr/port-logistics-intelligence)
- [dominicfinn/open_tms](https://github.com/dominicfinn/open_tms)

## 路线图

| 议题 | 内容 |
|---|---|
| [59](https://github.com/Duang777/waybill-guardian/issues/59) | 演示改为真实模型推理，补结构化证据引用 |
| [60](https://github.com/Duang777/waybill-guardian/issues/60) | 文件导入，以及对任意运单启动 |
| [61](https://github.com/Duang777/waybill-guardian/issues/61) | 多公路港总览和可核验的 KPI |
| [62](https://github.com/Duang777/waybill-guardian/issues/62) | 选定并加入 LICENSE |
| [64](https://github.com/Duang777/waybill-guardian/issues/64) | 非 GET 请求的 CSRF 检查 |
| [65](https://github.com/Duang777/waybill-guardian/issues/65) | Docker Compose |
| [67](https://github.com/Duang777/waybill-guardian/issues/67) | GitHub Actions |
| [68](https://github.com/Duang777/waybill-guardian/issues/68) | 评审向 README。本页补了架构图、边界和演示入口。量化指标、官方数据步骤和配音视频仍未完成 |
| [77](https://github.com/Duang777/waybill-guardian/issues/77) | 前端视觉，含 issue 69 到 76 |

生产化处置链路见 [issue 44](https://github.com/Duang777/waybill-guardian/issues/44)。

社交预览图在 [`docs/assets/social-preview.png`](docs/assets/social-preview.png)，尺寸 1280×640。GitHub 仓库设置里的 Social preview 需要单独上传，这个文件不会自动变成那张图。

## 许可证

本仓库还没有 `LICENSE` 文件，也还没有选定开源许可证。见 [issue 62](https://github.com/Duang777/waybill-guardian/issues/62)。在那个文件合入之前，不要把本仓库当成已经按某个 OSI 许可证授权。

第三方组件的许可证见 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

## 延伸阅读

- 架构和恢复：[docs/RFC-001.md](docs/RFC-001.md)
- 真实平台接入：[docs/RFC-002.md](docs/RFC-002.md)
- 改派沙箱：[docs/real-write-adapter-design.md](docs/real-write-adapter-design.md)
- Agent history：[docs/history-governance.md](docs/history-governance.md)
- 模块索引：[AGENTS.md](AGENTS.md)
