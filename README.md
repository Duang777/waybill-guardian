# waybill-guardian 运单守护

异常运单处置 Agent。「AI 重构产业 · 架构师大赛」AI+物流赛道参赛项目。

延误、破损或丢件发生后，Agent 查询运单、轨迹、司机和天气，形成归因和处置方案。
所有 TMS 和通知写操作都先进入人工审批。系统在确认后执行写操作，并把全过程写入可校验、
可回放的审计日志。

## 当前能力

- 七个工具与 [`contract.yaml`](./contract.yaml) 对齐。四个只读工具自动执行，三个写工具强制审批。
- hastekit v0.0.24 负责 Agent loop、typed tools、文件历史和 HITL pause/resume。
- 服务端根据业务参数生成稳定 `effect_id` 和幂等键。同一 effect 并发执行十次时，platform 只收到一次调用。
- 每个 run 使用一份 append-only JSONL。事件包含连续序号、前序哈希和当前哈希。
- SSE 支持 `Last-Event-ID` 续传。前端按 `(run_id, seq)` 去重。
- 审批支持确认、驳回和超时。首选运力被驳回后，Agent 会提交第二个候选方案。
- 当前无认证版本只监听 loopback，并用进程锁阻止两个实例共享同一个数据目录。
- 默认演示不需要模型密钥或高德密钥。

## 快速开始

准备 Go 1.25.3 或更高版本、Node.js 22.12 或更高版本，以及 npm。

```bash
git clone https://github.com/Duang777/waybill-guardian.git
cd waybill-guardian
./scripts/demo.sh
```

脚本首次运行时执行 `npm ci`，然后启动 API 和前端。打开
<http://127.0.0.1:5173>，点击 **启动演示**。按 `Ctrl+C` 会同时停止两个进程。

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
| `DEMO_STEP_DELAY` | `220ms` | 确定性模型每一步的演示延迟 |
| `PLATFORM` | `mock` | `mock` 可用；`real` 会在 adapter 未实现时拒绝启动 |
| `STORAGE` | `jsonl` | `jsonl` 用于离线演示；`postgres` 当前只完成 schema 基础 |
| `DATABASE_URL` | 空 | `STORAGE=postgres` 时必填，不应写入日志或仓库 |
| `PG_MAX_CONNS` | `8` | PostgreSQL 连接池最大连接数 |
| `PG_MIN_CONNS` | `0` | PostgreSQL 连接池最小连接数 |
| `PG_STARTUP_TIMEOUT` | `30s` | PostgreSQL 连接、检查和迁移的总超时 |
| `AGENT_MODE` | `demo` | `demo` 使用确定性模型；`online` 调用外部模型 |

例如，使用其他端口和临时数据目录：

```bash
BACKEND_PORT=18080 WEB_PORT=15173 DATA_DIR=/tmp/waybill-demo ./scripts/demo.sh
```

`BACKEND_HOST` 必须是 loopback IP 字面量。直接运行 `go run ./cmd/server` 时，`HTTP_ADDR`
默认是 `127.0.0.1:8080`，空 host、主机名、通配地址和非 loopback 地址都会被拒绝。服务也会
拒绝 Host 不是 loopback IP 的请求。当前版本没有用户认证，审批审计主体固定为
`local-demo-reviewer`，不能部署为远程共享服务。

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
```

使用 Docker 启动临时 PostgreSQL 17，并验证迁移和数据库约束：

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
```

`verify:e2e` 启动隔离的后端和前端，连续确认三次演示，再验证一次驳回路径。脚本还检查移动端
横向溢出、按钮尺寸和截图。

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
`internal/audit` 是业务事实源，hastekit file history 保存模型消息和 pending tool calls。

本地运行时将 `DATA_DIR` 和 hastekit history 目录权限设为 `0700`，数据文件设为 `0600`。
它对 `DATA_DIR` 的目录文件描述符持有独占锁，第二个使用同一目录的进程会拒绝启动。对于
“外部平台成功，但本地成功事件还未写入”的窗口，系统不会自动重试未知结果。真实 adapter
必须按幂等键查询或重试，否则不能启用真实写模式。

- 架构、恢复矩阵和取舍：[`docs/RFC-001.md`](./docs/RFC-001.md)
- 真实平台接入与生产处置链路：[`docs/RFC-002.md`](./docs/RFC-002.md)
- hastekit 源码研究：[`docs/research/hastekit-v0.0.24.md`](./docs/research/hastekit-v0.0.24.md)
- 三分钟演示讲稿：[`docs/demo-script.md`](./docs/demo-script.md)
- 模块职责索引：[`AGENTS.md`](./AGENTS.md)
- 第三方来源声明：[`THIRD_PARTY_NOTICES.md`](./THIRD_PARTY_NOTICES.md)
