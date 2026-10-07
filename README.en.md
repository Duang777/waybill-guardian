<p align="center">
  English · <a href="README.md">简体中文</a>
</p>

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-dark.svg">
    <img alt="waybill guardian logo. A shield with a folded waybill corner and a three-stop route." src="docs/assets/logo-light.svg" width="420">
  </picture>
</p>

<h1 align="center">waybill-guardian</h1>

<p align="center">An agent for abnormal waybills. It gathers evidence, then waits for a person.</p>

<p align="center">
  <a href="https://go.dev/dl/"><img alt="Go 1.25.3" src="https://img.shields.io/badge/Go-1.25.3-00ADD8?logo=go&logoColor=white"></a>
  <a href="https://react.dev/"><img alt="React 19.3.0" src="https://img.shields.io/badge/React-19.3.0-087EA4?logo=react&logoColor=white"></a>
  <a href="https://nodejs.org/"><img alt="Node.js 22.12 or newer" src="https://img.shields.io/badge/Node.js-%3E%3D22.12-339933?logo=nodedotjs&logoColor=white"></a>
  <a href="https://github.com/Duang777/waybill-guardian/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/Duang777/waybill-guardian/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/Duang777/waybill-guardian/issues"><img alt="GitHub issues" src="https://img.shields.io/github/issues/Duang777/waybill-guardian"></a>
  <a href="LICENSE"><img alt="Apache 2.0 license" src="https://img.shields.io/badge/license-Apache--2.0-blue"></a>
</p>

This repository is an entry in the 传化集团 and 动势科技 architect contest, on the AI + logistics track. The logo is original. It does not use the organizers' marks.

## For judges

After a delay, damage, or loss, waybill-guardian reads the waybill, tracking, driver, and weather data. It then attributes the incident and proposes reassignment, a claim, or a notification. The system does not run a write action before a person confirms it. After confirmation, the server writes under an idempotency key and appends the full process to a verifiable, replayable audit log.

<p align="center">
  <a href="https://github.com/Duang777/waybill-guardian/releases/download/demo-v1.0.0/waybill-guardian-demo-1920-zh.mp4"><strong>Watch the 60-second narrated demo</strong></a>
  ·
  <a href="docs/demo-script.md">Read the script and shot list</a>
</p>

<p align="center">
  <img alt="Nationwide highway-port operating overview at 1920 pixels wide. The page shows six KPIs, the 72-port network, the anomaly queue, three operating charts, and an operating brief." src="docs/assets/overview-console.png" width="960">
</p>

### Disposition boundaries

| Phase | System behavior | Human boundary | Verifiable record |
|---|---|---|---|
| Investigation | Calls four read-only tools | No manual lookup across four systems | Tool arguments, result summaries, and evidence references |
| Proposal | Returns structured attribution, alternatives, and write arguments | A reviewer checks the impact and alternatives | Inference mode, latency, token use, and proposal version |
| Execution | Generates the `effect_id` and idempotency key on the server | A reviewer confirms, rejects, or lets the approval expire | Human decision, platform receipt, and retry state |
| Review | Streams live events over SSE and replays the audit from a cursor | A reviewer inspects events by sequence number | `seq`, `prev_hash`, and `hash` |

### Verifiable value model

This repository does not present simulated output as a production result. The overview shows only facts that the API can prove, using these formulas:

| Metric | Assumption or fact | Result |
|---|---|---|
| Estimated labor saved for one complete investigation | Four evidence steps at the default `EVIDENCE_STEP_MINUTES=8` | `4 × 8 / 60 = 0.53` person-hours |
| Evidence workload for 72 anomalous waybills | One anomalous waybill per highway port and all four steps succeed | `72 × 4 × 8 / 60 = 38.4` person-hours |
| Realized time recovery and cost impact | Includes only the latest run with a confirmed and executed approval and a complete `impact` record | Returns `unavailable` when data is incomplete instead of filling a default |

The first two rows estimate manual evidence-collection effort from configuration. They do not measure agent runtime or claim a realized production result. Approval, disposition, and review still require a person. See [Operating overview and KPIs](#operating-overview-and-kpis) for every formula.

## Architecture

```mermaid
flowchart TD
  browser["Operating overview and waybill workbench"]
  guardian["HTTP + guardian use-case coordination"]
  agent["hastekit Agent"]
  readtools["Four read tools"]
  sources["mock / JSON / CSV / external reads"]
  approval["Persistent human approval"]
  effects["Effect identity and idempotent execution"]
  writetools["Three write tools"]
  platform["In-memory runtime / TMS adapter"]
  audit["Append-only audit and hash chain"]

  browser -->|Start or decide| guardian
  guardian -->|Start or resume| agent
  agent --> readtools
  readtools --> sources
  sources --> readtools
  readtools --> agent
  agent -->|Pause with a proposal| approval
  guardian -->|Persist the human decision| approval
  approval -->|Decision persisted| guardian
  agent -->|Approved write calls| effects
  effects --> writetools
  writetools --> platform
  guardian --> audit
  approval --> audit
  effects --> audit
  audit -->|SSE replay + live| browser
```

```mermaid
sequenceDiagram
  participant O as Reviewer
  participant W as Web
  participant G as guardian
  participant A as Agent
  participant P as Platform
  participant D as Audit

  O->>W: Start disposition
  W->>G: POST run
  G->>A: Investigate and propose
  A-->>G: Proposal and write calls
  G->>D: Record approval batch and pause
  G-->>W: pending
  O->>W: Confirm or reject
  W->>G: Submit human decision
  G->>D: Persist the decision first
  alt Confirmed
    G->>A: Resume the same thread
    A->>G: Execute write tools
    G->>P: Write idempotently by effect identity
    P-->>G: Return platform receipt
    G->>D: Record execution result
  else Rejected or expired
    G->>A: Resume with the reason
    A-->>G: Propose an alternative or finish
  end
  D-->>W: SSE replay and live events
```

`cmd/server` serves HTTP and SSE. `internal/guardian` coordinates the agent, approval, idempotency, and recovery. The agent runtime is hastekit `agent-sdk-go` v0.0.24. [`contract.yaml`](contract.yaml) defines the tool contract.

The four read tools run automatically. The three write tools pause before execution and wait for a human decision. Approval starts at `pending`, then moves to `confirmed`, `rejected`, or `expired`. Execution can finish as `executed`, `partially_failed`, `failed`, or `reconciliation_required`.

The default audit stores one append-only JSONL file per run. With `STORAGE=postgres`, the business projection, audit, and outbox commit in one transaction. The browser uses `Last-Event-ID` to resume SSE from its last event.

### Replace the simulated data with official data

1. Map waybills, tracking, drivers, weather, and optional network entities to [`data/templates/waybills-v1.json`](data/templates/waybills-v1.json) or [`data/templates/waybills-v1.csv`](data/templates/waybills-v1.csv).
2. Run `go run ./cmd/dataimport validate --data <file-path>`. The validator checks references, coordinates, and timestamp order before the service starts.
3. Start the read-data demo with `PLATFORM=file DATA_FILE=<file-path> AGENT_MODE=offline ./scripts/demo.sh`. Production writes require an `internal/platform` adapter. The current `PLATFORM=real` profile is a reassignment HTTP sandbox, not a production TMS.

See [`docs/file-data-source-design.md`](docs/file-data-source-design.md) for fields and errors. See [`docs/RFC-002.md`](docs/RFC-002.md) for the production platform boundary.

<p align="center">
  <img alt="Desktop width. The embedded fixture is waiting for approval. The waybill selector shows Hangzhou to Chengdu, YD2026101001, and the proposal reassigns to 川行快运. The map is the local track. Display scores are ETA 83, road 75, and weather 0." src="docs/assets/console-approval.png" width="840">
</p>

<p align="center">
  <img alt="Phone width. The same scripted demo shows 处置完成 after confirmation, and the button reads 重新处置." src="docs/assets/console-completed-mobile.png" width="280">
</p>

The overview screenshot comes from `AGENT_MODE=offline npm run verify:overview`. The workbench
screenshots come from `AGENT_MODE=offline npm run verify:e2e`. With no Amap key, the page uses the
local track without administrative boundaries.

## Features

The status describes the code in this repository.

| Status | Meaning |
|---|---|
| Shipped | It runs on the default branch, within the scope in the notes |
| In progress | Some code exists, and the issue acceptance criteria are not met |
| Planned | The default branch does not have this |

| Feature | Status | Notes |
|---|---|---|
| Four read tools, three write tools, human approval | Shipped | Matches [`contract.yaml`](contract.yaml). Under mock, all three writes require approval. |
| Server-generated idempotency key | Shipped | The model does not submit `effect_id` or the key. Ten concurrent calls for one key reach the platform once. See [`idempotency_test.go`](internal/idempotency/idempotency_test.go). |
| JSONL audit, hash chain, SSE replay | Shipped | The UI deduplicates by run and `seq`. |
| PostgreSQL storage | Shipped | Stores runs, approvals, effects, audit, outbox, and AES-256-GCM agent history. |
| Local identity and JWT | Shipped | `AUTH_MODE=local` listens on loopback by default; the container also checks the Host and TCP peer. `jwt` checks RS256, issuer, audience, time, tenant, role, and waybill scope. |
| Amap or a local track | Shipped | With no key, or if the SDK fails to load, the page draws local coordinates. |
| Reassign HTTP sandbox | Shipped | `tms.reassign` only. Reads stay on the fixture. Not a production TMS. |
| Online model calls | In progress | Formal demo entry points default to online inference. Compatible fake tests cover both APIs and three waybills. Live domestic-provider acceptance still needs a deployment credential. See [issue 59](https://github.com/Duang777/waybill-guardian/issues/59). |
| File import | Shipped | `PLATFORM=file` loads JSON or CSV v1 at startup, and the page can select a waybill from the file. Writes stay on the in-memory fixture runtime. See closed [issue 60](https://github.com/Duang777/waybill-guardian/issues/60) and [`docs/file-data-source-design.md`](docs/file-data-source-design.md). |
| Highway-port overview | Shipped | The home page shows the 72-port network, KPIs, three operating charts, the anomaly queue, operating briefs, and batch start with per-run approval. See [issue 61](https://github.com/Duang777/waybill-guardian/issues/61) and [issue 72](https://github.com/Duang777/waybill-guardian/issues/72). |
| Apache-2.0 and dependency manifests | Shipped | The root has `LICENSE`. Transitive dependency manifests are in [`docs/licenses/`](docs/licenses/). |
| CSRF checks on non-GET requests | Shipped | Go's `CrossOriginProtection` checks `Sec-Fetch-Site` and `Origin`. JSON write endpoints check `Content-Type`. See closed [issue 64](https://github.com/Duang777/waybill-guardian/issues/64). |
| Docker Compose | Shipped | One container serves the frontend and API, with an optional PostgreSQL 17 profile. |
| GitHub Actions | Shipped | Pull requests and `main` run Go, Web, PostgreSQL, license, and image checks. |
| White industrial network and evidence linking | Shipped | The contest scope covers the nationwide port network, evidence navigation, recording layouts, and mobile. See closed [issue 77](https://github.com/Duang777/waybill-guardian/issues/77). |

## Demo

The script is [`docs/demo-script.md`](docs/demo-script.md). The formal demo calls an online model by default:

```bash
export LLM_API_STYLE=chat_completions
export LLM_BASE_URL=https://api.deepseek.com
export LLM_API_KEY=replace-me
export LLM_MODEL=deepseek-v4-flash
./scripts/demo.sh
```

Without model credentials, start offline replay explicitly:

```bash
AGENT_MODE=offline ./scripts/demo.sh
```

Open <http://127.0.0.1:5173> for the operating overview. Select anomalous waybills and click **交给 Agent**, or open `/waybills/:id` for one waybill. The workbench still offers **启动处置**. `POST /api/demo/trigger` starts the embedded waybill.

1. The agent reads waybill `YD2026101001`, Hangzhou to Chengdu.
2. The timeline records waybill, tracking, driver, and weather calls.
3. The online model returns a structured attribution, alternatives, and evidence references from the tool results. In the embedded fixture the driver has driven 9 hours with a fatigue alert, 绵阳北服务区 is a 6 hour stop, and the weather alert is `none`. The write calls have not run yet.
4. Click **确认并执行**. The timeline shows the platform writes, and the waybill status becomes completed.
5. Use the replay control to watch from the first audit event, then click **实时** to return to the live end.
6. If the first carrier is rejected, the agent continues from the human decision. The offline `ScenarioModel` proposes 蜀道联运, and `npm run verify:e2e` covers three confirmations and one rejection.

The published
[1920 by 1080 narrated version](https://github.com/Duang777/waybill-guardian/releases/download/demo-v1.0.0/waybill-guardian-demo-1920-zh.mp4)
has on-screen Chinese captions and a system-generated Chinese voice track. The
[voiceover script](docs/demo-script.md#60-秒配音稿) uses no cloned voice.

To generate the subtitled source recording without an audio track:

```bash
cd web
npm run record:demo
```

The recording script inherits the model settings above and defaults to `online`. To record without credentials, run `AGENT_MODE=offline npm run record:demo`. The file is `web/artifacts/waybill-guardian-demo.mp4`, at 1600 by 900 by default. For a full-HD recording, run:

```bash
RECORD_RESOLUTION=1920x1080 npm run record:demo
```

`RECORD_RESOLUTION` accepts only `1600x900` and `1920x1080`. The output directory is
gitignored. `RECORD_OUTPUT`, `RECORD_BACKEND_PORT`, and `RECORD_WEB_PORT` change the output path
and ports.

## Quick start

### Docker

With Docker installed, one command builds the image and serves the frontend and API at
<http://127.0.0.1:8080>:

```bash
git clone https://github.com/Duang777/waybill-guardian.git
cd waybill-guardian
AGENT_MODE=offline docker compose up --build
```

Compose publishes the application only on host loopback. The `app-data` volume stores the audit and
agent history. Use `docker compose down` to stop it, or `docker compose down --volumes` to delete the
demo data too.

To use the optional PostgreSQL 17 profile:

```bash
cp .env.example .env
docker compose up --build
```

The copied `.env` keeps the `prod` profile and PostgreSQL storage enabled on later starts. Its
database password and checkpoint key are local demo values. Replace them for a real deployment, use
`AUTH_MODE=jwt`, and run behind HTTPS. If the PostgreSQL password changes, URL-encode it in
`DATABASE_URL`.

### Local toolchain

You need Go 1.25.3 or newer, Node.js 22.12 or newer, and npm:

```bash
./scripts/demo.sh
```

If `web/node_modules/.bin/vite` is missing, the script runs `npm ci`, then builds the API and starts the web console. When both are up it prints `Waybill Guardian is ready`. `Ctrl+C` stops both processes.

Use another port and data directory:

```bash
BACKEND_PORT=18080 WEB_PORT=15173 DATA_DIR=/tmp/waybill-demo ./scripts/demo.sh
```

Checks:

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
./scripts/check-production-fixture-literals.sh
./scripts/check-history-governance.sh
./scripts/licenses.sh
```

`scripts/licenses.sh` pins its scanners and verifies that the committed Go and Web production
dependency manifests match the lock files. Run `./scripts/licenses.sh --write` after a dependency
change.

`./scripts/test-postgres.sh` starts a temporary PostgreSQL 17 with Docker and checks migrations,
transactions, leases, encrypted history, and the browser workflow.

```bash
./scripts/test-postgres.sh
```

```bash
cd web
npm ci
npm test
npm run build
npm audit --omit=dev --audit-level=high
npm run verify:e2e
npm run verify:file-e2e
npm run verify:overview
```

GitHub Actions runs the Go race suite, recovery stability loop, PostgreSQL integration, Web,
license, and image checks on pull requests and `main`. Scheduled and manual runs also execute both
browser E2E suites and upload `web/artifacts/*.png`.

## Configuration

`./scripts/demo.sh` reads the first four variables below and derives `ALLOWED_ORIGINS` from the web URL by default. Every other variable is inherited from the shell. `go run ./cmd/server` listens on `HTTP_ADDR`, default `127.0.0.1:8080`.

| Variable | Default | Meaning |
|---|---|---|
| `BACKEND_HOST` | `127.0.0.1` | API host, used by `demo.sh` |
| `BACKEND_PORT` | `8080` | API port, used by `demo.sh` |
| `WEB_HOST` | `127.0.0.1` | Web host, used by `demo.sh` |
| `WEB_PORT` | `5173` | Web port, used by `demo.sh` |
| `HTTP_ADDR` | `127.0.0.1:8080` | Server listen address. Local mode requires a loopback IP |
| `ALLOWED_ORIGINS` | empty | Comma-separated trusted browser origins in exact `scheme://host[:port]` form. Configure only for cross-origin deployments |
| `WEB_STATIC_DIR` | empty | Frontend build directory served by Go. The container uses `/app/web` |
| `ALLOW_NON_LOOPBACK_LOCAL` | `false` | Allow local mode to listen on a non-loopback IP, only for a container published on host loopback |
| `LOCAL_TRUSTED_REMOTE` | empty | Required with the previous option. The TCP peer must match this IP, hostname, or `container-gateway` |
| `DATA_DIR` | `data` | JSONL audit and hastekit history directory |
| `AGENT_MODE` | Entry-specific | `demo.sh`, recording, and containers default to `online`; the server alone defaults to `offline`. `demo` is an alias for `offline` |
| `LLM_API_STYLE` | `responses` | `responses` or `chat_completions` in online mode |
| `LLM_BASE_URL` | empty | Required online. The model API root without a concrete endpoint |
| `LLM_API_KEY` | empty | Required online. Read only from the process environment |
| `LLM_MODEL` | empty | Required online. A current model ID from the provider |
| `LLM_REQUEST_TIMEOUT` | `45s` | Total deadline for one logical model call, including repair and provider retries. Must be a positive Go duration |
| `LLM_MAX_OUTPUT_TOKENS` | `4096` | Maximum output tokens per provider request, from 1 through 32768 |
| `PLATFORM` | `mock` | `mock` uses the fixture. `file` loads `DATA_FILE`. `real` uses the reassign sandbox |
| `DATA_FILE` | empty | Required for `PLATFORM=file`. One JSON or CSV v1 file |
| `MAX_CONCURRENT_RUNS` | `8` | Maximum concurrent investigation runs, from 1 through 64 |
| `EVIDENCE_STEP_MINUTES` | `8` | Estimated human minutes per evidence collection step. Must be positive |
| `STORAGE` | `jsonl` | `jsonl` or `postgres` |
| `AUTH_MODE` | `local` | `local` or `jwt` |
| `APPROVAL_TTL` | `10m` | Approval lifetime, Go duration. An invalid value falls back to the default |
| `HISTORY_RETENTION` | `168h` | Retention for finished agent history. Must be positive |
| `DEMO_STEP_DELAY` | `220ms` | Pause between scripted model steps |
| `TENANT_ID` | `local-demo` | Required explicitly in JWT mode |
| `INSTANCE_ID` | random UUID | Worker identity on PostgreSQL leases |

### File data

The v1 templates in the repository are [`data/templates/waybills-v1.json`](data/templates/waybills-v1.json) and [`data/templates/waybills-v1.csv`](data/templates/waybills-v1.csv). Validate them with the same loader the server uses:

```bash
go run ./cmd/dataimport validate --data ./data/templates/waybills-v1.csv
go run ./cmd/dataimport validate --data ./data/templates/waybills-v1.json
```

The commands print `valid dataset=template-v1 format=csv waybills=1 anomalies=1` and `valid dataset=template-v1 format=json waybills=1 anomalies=1`. Replace the path with your own v1 file. This repository does not contain `official-v1.csv`.

Start file mode after validation:

```bash
PLATFORM=file \
DATA_FILE=./data/templates/waybills-v1.csv \
DATA_DIR=/tmp/waybill-file-demo \
AGENT_MODE=offline \
./scripts/demo.sh
```

Compose mounts the repository's `data/` directory read-only at `/app/data`. Run the template in the
container with:

```bash
COMPOSE_PROFILES= STORAGE=jsonl PLATFORM=file AGENT_MODE=offline \
DATA_FILE=/app/data/templates/waybills-v1.json \
docker compose up --build
```

`PLATFORM=file` also requires `STORAGE=jsonl` and `AUTH_MODE=local`. The server loads the whole file once at startup. A syntax, reference, coordinate, or time-order error fails before the process listens. It does not reload the file while running. Replacing the file requires a restart. Fields and checks are in [`docs/file-data-source-design.md`](docs/file-data-source-design.md). The waybill selector lists the waybills in the file.

The repository also contains reproducible simulated data with 72 highway ports, 72 routes, 200
vehicles, 200 waybills, and five anomaly types:

```bash
env -u GOROOT go run ./cmd/datagenerate \
  --output ./data/simulated/waybills-v1.json \
  --waybills 200

env -u GOROOT go run ./cmd/dataimport validate \
  --data ./data/simulated/waybills-v1.json
```

`hubs`, `vehicles`, `routes`, and the corresponding waybill references are optional v1 network
extensions. If a file contains any network entity, the validator requires all three entity groups
and every reference. The simulated data is for product demonstrations and capacity checks. It is
not operational data.

### Operating overview and KPIs

`GET /api/overview` returns authorized highway ports, routes, the anomaly queue, and three cited
operating briefs. The service filters the waybill scope before calculating totals and ratios.
`AGENT_MODE=offline` returns the deterministic brief, and `demo` is an alias. `AGENT_MODE=online`
uses an independent model call without tools or history. The model receives aggregate counts
without waybill, route, port, person, or vehicle identities. It can cite only evidence IDs supplied
by the server, which rebuilds the displayed references. If the model call, parsing, privacy check,
or citation check fails, the endpoint returns the deterministic brief with HTTP 200.

The page builds the anomaly-composition and high-anomaly-route charts from the same overview
snapshot. SSE projections update the current disposition chart. The page does not show a trend,
period comparison, or process funnel without historical buckets, a previous snapshot, or a manual
baseline.

`POST /api/runs:batch` accepts up to 20 `waybill_id` values and returns an independent result for
each one. `MAX_CONCURRENT_RUNS` limits concurrent investigations. Every accepted run has its own
`run_id`, approval record, and SSE timeline.

`GET /api/kpis?window=24h` uses these formulas. The window ends at the latest anomalous waybill
`last_recorded_at` in the authorized scope, or at a later audit event time.

| KPI | Formula | Result when data is missing |
|---|---|---|
| Time recovered | `sum(no_action_eta_hours - post_action_eta_hours)` | `unavailable` when any anomalous waybill in the window lacks a complete impact record |
| Cost impact | `sum(avoided_penalty_cents - reassign_delta_cents - handling_cost_cents) / 100` | `unavailable` when any anomalous waybill in the window lacks a complete impact record |
| Labor saved | `successful evidence steps * EVIDENCE_STEP_MINUTES / 60` | `0` hours without evidence events |
| Anomaly closure rate | `completed or rejected anomaly runs / anomalous waybills * 100%` | `0%` without anomalous waybills |
| Average handling time | `sum(terminal time - start time) / closed runs` | `unavailable` without closed runs |
| Approval rate | `confirmed decisions / human decisions * 100%` | `unavailable` without decisions |

A JSON waybill can include one complete `impact` object. CSV input can include the five fields with
the same names. All five values must appear together. ETA values use hours, and monetary values use
integer cents. The service sums cents before converting the result to yuan. The simulated data
provides reproducible values for anomalous waybills. A production adapter must provide the same
facts, and the service does not fill incomplete records with averages or defaults. Both KPIs are
available with a zero value when the window contains no anomalies.

### Online model

This example uses the DeepSeek Chat Completions API:

```bash
AGENT_MODE=online \
LLM_API_STYLE=chat_completions \
LLM_BASE_URL=https://api.deepseek.com \
LLM_API_KEY=replace-me \
LLM_MODEL=deepseek-v4-flash \
./scripts/demo.sh
```

The following domestic providers expose compatible Chat Completions APIs. Model IDs, interface support, and prices can change. Check the linked provider documentation before running the demo.

| Provider | `LLM_API_STYLE` | `LLM_BASE_URL` | Example `LLM_MODEL` | Official resources |
|---|---|---|---|---|
| Alibaba Cloud Model Studio Qwen | `chat_completions` | `https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | `qwen-plus` | [Setup](https://help.aliyun.com/zh/model-studio/qwen-api-via-openai-chat-completions) · [Pricing](https://help.aliyun.com/zh/model-studio/model-pricing) |
| DeepSeek | `chat_completions` | `https://api.deepseek.com` | `deepseek-v4-flash` | [Setup](https://api-docs.deepseek.com/) · [Pricing](https://api-docs.deepseek.com/quick_start/pricing/) |
| Volcengine Ark Doubao | `chat_completions` | `https://ark.cn-beijing.volces.com/api/v3` | `doubao-seed-1-6-251015` | [Setup](https://www.volcengine.com/docs/82379/1399008) · [Pricing](https://www.volcengine.com/docs/82379/1544106) |
| Kimi | `chat_completions` | `https://api.moonshot.cn/v1` | `kimi-k3` | [Setup](https://platform.kimi.com/docs/get-api-key) · [Pricing](https://platform.kimi.com/docs/pricing/chat) |
| Zhipu GLM | `chat_completions` | `https://open.bigmodel.cn/api/paas/v4` | `glm-5.3` | [Setup](https://docs.bigmodel.cn/cn/guide/develop/openai/introduction) · [Pricing](https://docs.bigmodel.cn/cn/guide/start/pricing) |

`LLM_API_STYLE` defaults to `responses`. `LLM_BASE_URL` must be the API root. It must not end in `/`, and it must not include `/responses` or `/chat/completions`. Online mode configures one provider and does not enable provider fallback. Each provider request can return at most 4096 tokens. One logical model call has a 45-second deadline; within it, the initial request and one structural repair can each make at most three attempts. The server also limits assistant proposal text to 32 KiB and includes at most 4 KiB of failed text in a repair request. Read tools return allowlisted fields with bounded text and collection sizes. The model must treat all text in those results as untrusted business data, not instructions. The audit records token usage and latency for every logical model call.

Model calls incur provider charges and send redacted waybill evidence to the selected provider. Before a deployment calls a provider, the operator must review its current model IDs, prices, data rules, and service terms. [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) lists the terms pages. The automated suite tests protocol compatibility, not a live provider.

The online operating brief does not reuse the handling agent or change the handling inference scope in issue 59.

### Platform

`PLATFORM=mock` does not call an external system.

`PLATFORM=real` also requires:

| Variable | Required value |
|---|---|
| `STORAGE` | `postgres` |
| `AUTH_MODE` | `jwt` |
| `REAL_PLATFORM_PROFILE` | `tms-reassign-sandbox-v1` |
| `REAL_READ_SOURCE` | `fixture-v1` |
| `TMS_SANDBOX_BASE_URL` | Sandbox root URL |
| `TMS_SANDBOX_TOKEN` | Bearer token |
| `TMS_SANDBOX_ACCOUNT` | Must equal `TENANT_ID` |

Related timeouts are `PLATFORM_REQUEST_TIMEOUT` (default `3s`), `PLATFORM_STARTUP_TIMEOUT` (default `5s`), `EFFECT_RECONCILE_HORIZON` (default `24h`), `EFFECT_RECONCILE_POLL_INTERVAL` (default `1s`), and `PLATFORM_MAX_LOOKUP_CONSISTENCY_WINDOW` (default `30s`).

### Storage, auth, and outbox

`STORAGE=postgres` requires `DATABASE_URL` and `CHECKPOINT_ENCRYPTION_KEY`. The key is a Base64 encoding of 32 bytes for AES-256. `CHECKPOINT_KEY_ID` defaults to `local-v1`.

Pool settings: `PG_MAX_CONNS` defaults to 8, `PG_MIN_CONNS` defaults to 0, `PG_STARTUP_TIMEOUT` defaults to `30s`. `RUN_LEASE_TTL` defaults to `30s`. `EFFECT_LEASE_TTL` defaults to `15s`.

`AUTH_MODE=jwt` also needs `AUTH_JWT_ISSUER`, `AUTH_JWT_AUDIENCE`, and `AUTH_JWT_PUBLIC_KEY_FILE`. The public key is a PEM-encoded RSA key. The JWT needs `sub`, `tenant_id`, `roles`, and either `waybill_all=true` or a non-empty `waybill_ids`. Roles are `viewer`, `dispatcher`, `operator`, and `event_producer`. The server does not terminate TLS. A non-loopback deployment needs an HTTPS front door.

`POST /v1/events` is registered only in PostgreSQL mode. The outbox dispatcher is off by default. `OUTBOX_ENABLED=true` requires `OUTBOX_URL` and `OUTBOX_TOKEN`. Except for a loopback test address, `OUTBOX_URL` must be HTTPS. `METRICS_ADDR`, for example `127.0.0.1:9090`, exposes Prometheus metrics at `/metrics` on a separate listener. Both require PostgreSQL.

| Variable | Default |
|---|---|
| `OUTBOX_BATCH_SIZE` | `10`, maximum 100 |
| `OUTBOX_CONCURRENCY` | `4`, maximum 100 |
| `OUTBOX_POLL_INTERVAL` | `250ms` |
| `OUTBOX_LEASE_TTL` | `30s` |
| `OUTBOX_STATS_INTERVAL` | `15s` |
| `OUTBOX_HTTP_TIMEOUT` | `10s` |

### Web

Put Amap settings in `web/.env.local`. Git ignores that file.

```dotenv
VITE_AMAP_KEY=replace-me
VITE_AMAP_SECURITY_JS_CODE=replace-me
```

`VITE_API_TARGET` is the API address for the Vite dev proxy. The default is `http://127.0.0.1:8080`. `demo.sh` sets it to the API it started.

### Delete agent history

Stop the server first. Delete local history:

```bash
rm -rf "${DATA_DIR:-data}/hastekit"
```

PostgreSQL deletion is per tenant. It also clears checkpoint pointers on finished runs:

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

Do not clear a run that is still active. Deleting history does not delete `waybill.audit_events`.

## Safety and compliance

Write tools set `RequiresApproval`. hastekit pauses the run at `await_approval` before the tool runs. The human decision is written to the audit first. The server then resumes the same thread. Confirm, reject, and expiry share one run lock, so only one decision is stored. Expiry resumes the agent as a rejection. The default lifetime is 10 minutes.

The model submits business arguments only. The server generates `effect_id` and the idempotency key. On resume, middleware checks `call_id`, the argument hash, and `effect_id`. Concurrent calls for the same key are merged. In the test, 10 concurrent calls reach the platform once. A recorded failure can be retried as a new attempt. If the external system succeeded and the local success event was not written, the state is indeterminate and the server does not retry automatically. A real adapter must retry safely with the same key, or look the key up.

Audit events are append-only. `seq`, `prev_hash`, and `hash` let a reader check that the chain was not edited. SSE replays from the cursor, then streams live events.

The read-tool payloads sent to the model omit phone numbers, license plates, and exact coordinates. The SMS tool receives the waybill, the recipient role, and the carrier. The server resolves the phone number and template after approval. The history guard rejects phone numbers, license plates, exact coordinates, and SMS provider parameters in model history. The operator API masks phone numbers and license plates. The map API still returns track coordinates so the console can draw the route.

`AUTH_MODE=local` listens on loopback by default and rejects a request whose Host is not a loopback
IP. The container listens on all interfaces with `ALLOW_NON_LOOPBACK_LOCAL=true`, validates the TCP
peer with `LOCAL_TRUSTED_REMOTE=container-gateway`, and publishes the Compose port only on host
loopback. The approval subject is fixed as `local-demo-reviewer`. A client-supplied `Authorization`
header or `X-Actor` header does not choose the identity.

Go's standard `CrossOriginProtection` checks `Sec-Fetch-Site` and `Origin` on unsafe API methods.
Same-origin requests, exact origins in `ALLOWED_ORIGINS`, and CLI or service requests without browser
origin headers are allowed. Other cross-site browser requests receive 403. Approval confirmation,
rejection, and the demo trigger also require `Content-Type: application/json`; requests without
parameters send `{}`.

## Open source

Direct dependencies, npm packages, and reference projects are listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

hastekit `agent-sdk-go` v0.0.24 is a Go module dependency under Apache-2.0. This repository does not copy its source.

These three projects were used to compare interaction and domain splits. None of their source files are in this repository:

- [jattiphrswan/logistics-tracker](https://github.com/jattiphrswan/logistics-tracker)
- [09karankr/port-logistics-intelligence](https://github.com/09karankr/port-logistics-intelligence)
- [dominicfinn/open_tms](https://github.com/dominicfinn/open_tms)

## Roadmap

| Issue | Topic |
|---|---|
| [59](https://github.com/Duang777/waybill-guardian/issues/59) | Run the demo on a real model, with structured evidence references |
| [60](https://github.com/Duang777/waybill-guardian/issues/60) | Closed. Load JSON or CSV v1 at startup and select a waybill on the page |
| [61](https://github.com/Duang777/waybill-guardian/issues/61) | Complete. Highway-port overview, KPIs, anomaly queue, and batch start |
| [62](https://github.com/Duang777/waybill-guardian/issues/62) | Complete. Apache-2.0, transitive dependency manifests, and `.mailmap` |
| [64](https://github.com/Duang777/waybill-guardian/issues/64) | Complete. Cross-origin checks for non-GET requests and JSON `Content-Type` validation |
| [65](https://github.com/Duang777/waybill-guardian/issues/65) | Complete. Single-container image and Docker Compose |
| [67](https://github.com/Duang777/waybill-guardian/issues/67) | Complete. GitHub Actions |
| [68](https://github.com/Duang777/waybill-guardian/issues/68) | Repository work complete. The README has the judge entry, architecture flow, value model, data replacement steps, and a 60-second narrated demo |
| [77](https://github.com/Duang777/waybill-guardian/issues/77) | Contest scope complete. The port network, evidence linking, recording layouts, and mobile are shipped. Issues 70, 72, 74, and 76 remain independent P2 work |

The production handling path is [issue 44](https://github.com/Duang777/waybill-guardian/issues/44).

The social preview image is [`docs/assets/social-preview.png`](docs/assets/social-preview.png), at 1280 by 640. GitHub's repository Social preview is a separate upload. This file does not become that image by itself.

## License

This project uses the [Apache License 2.0](LICENSE). Transitive Go and Web dependency manifests are
in [`docs/licenses/`](docs/licenses/). External model and map service terms are recorded in
[`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

The repository does not rewrite existing Git history. `.mailmap` makes local commands such as
`git shortlog` display old company-email commits under the maintainer's personal identity. It does
not change commit objects or SHAs. Future commits use a personal or GitHub noreply address.

## Further reading

- Architecture and recovery: [docs/RFC-001.md](docs/RFC-001.md)
- Real platform adapter: [docs/RFC-002.md](docs/RFC-002.md)
- Reassign sandbox: [docs/real-write-adapter-design.md](docs/real-write-adapter-design.md)
- File data source: [docs/file-data-source-design.md](docs/file-data-source-design.md)
- Agent history: [docs/history-governance.md](docs/history-governance.md)
- Module index: [AGENTS.md](AGENTS.md)
