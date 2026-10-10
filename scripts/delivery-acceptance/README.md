# Delivery production acceptance reference

`delivery-acceptance` generates the production acceptance evidence for the Delivery domain.
The tool records `passed`, `blocked`, or `failed` for each probe. It never converts a missing
production entry point into a pass.

## Commands

Run every embedded and configured probe:

```bash
./scripts/delivery-acceptance/run.sh
```

Skip Chrome and record `browser-cargo` as `blocked`:

```bash
./scripts/delivery-acceptance/run.sh --skip-browser
```

Require every publication probe to pass:

```bash
./scripts/delivery-acceptance/run.sh --require-publication
```

Check the committed JSON report, source digests, and generated Markdown:

```bash
./scripts/delivery-acceptance/check.sh
```

The authoritative report is
[`docs/reports/delivery-acceptance.v1.json`](../../docs/reports/delivery-acceptance.v1.json).
The Markdown reports contain no independent values.

## Embedded probes

`artifact-resilience` runs against `artifact.FileStore`. It measures 30 write, open, and verify
operations. The probe also checks:

- Cross-tenant digest isolation.
- Corruption detection.
- Backup and restore tree digests.
- The artifact size limit and context cancellation.
- Thirty-two concurrent writers for one digest.
- Heap and goroutine bounds.
- Private file and directory permissions.

`browser-cargo` runs `npm run verify:delivery`. It requires:

- 300 cargo placements rendered with `InstancedMesh`.
- A nonblank WebGL canvas.
- 20 desktop and mobile states.
- 20 unmount and remount cycles without retained canvases, documents, DOM nodes, or listeners.

## Command probes

Production probes use environment variables that contain JSON string arrays. The runner passes
arguments directly to `exec.CommandContext`. It does not invoke a shell.

| Probe | Environment variable |
|---|---|
| `capacity-matrix` | `DELIVERY_ACCEPTANCE_CAPACITY_COMMAND` |
| `query-sse-capacity` | `DELIVERY_ACCEPTANCE_CONCURRENCY_COMMAND` |
| `failure-recovery` | `DELIVERY_ACCEPTANCE_RECOVERY_COMMAND` |
| `authorization` | `DELIVERY_ACCEPTANCE_AUTHORIZATION_COMMAND` |
| `observability` | `DELIVERY_ACCEPTANCE_OBSERVABILITY_COMMAND` |
| `supply-chain` | `DELIVERY_ACCEPTANCE_SUPPLY_CHAIN_COMMAND` |

Each command must contain the `{result}` placeholder. The runner also expands `{config}`, `{root}`,
and `{probe}`.

```bash
export DELIVERY_ACCEPTANCE_CAPACITY_COMMAND='[
  "./bin/delivery-capacity-acceptance",
  "--config", "{config}",
  "--result", "{result}",
  "--probe", "{probe}"
]'
```

The repository includes a production supply-chain probe:

```bash
export DELIVERY_ACCEPTANCE_SUPPLY_CHAIN_COMMAND='[
  "./scripts/delivery-acceptance/supply-chain-probe.sh",
  "--result", "{result}"
]'
```

The probe runs `govulncheck`, `npm audit`, the license manifest check, the runtime image content
check, a Syft CycloneDX image scan, and a Trivy image vulnerability scan. The script pins
`golang.org/x/vuln` to `v1.8.0`, Syft to `v1.54.1`, and Trivy to `v0.75.0`.

The command must create a strict `delivery.acceptance.command-result.v1` JSON document at
`{result}`. The runner rejects unknown fields, duplicate checks, undeclared measurements, missing
dimensions, invalid percentile order, and a failed required check.

```json
{
  "schema_version": "delivery.acceptance.command-result.v1",
  "probe_id": "query-sse-capacity",
  "checks": [
    {
      "id": "stable_capacity_error",
      "passed": true,
      "detail": "the 501st SSE connection returned DELIVERY_SSE_CAPACITY"
    }
  ],
  "measurements": [
    {
      "id": "queries-100-sse-500",
      "sample_count": 20,
      "query_concurrency": 100,
      "sse_connections": 500,
      "duration_ns": {
        "minimum": 1000000,
        "p50": 2000000,
        "p95": 4000000,
        "p99": 5000000,
        "maximum": 6000000
      },
      "peak_memory_bytes": 67108864,
      "feasibility_ppm": 1000000
    }
  ],
  "artifacts": [
    {
      "name": "query-sse-run.json",
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "bytes": 4096
    }
  ]
}
```

The example omits the other checks required by `config.json`. A real result must contain exactly
the configured checks and measurements.

## Capacity dimensions

`config.json` fixes the capacity matrix:

| Scenario | Tasks | Vehicles | Depots | Cargo per vehicle | Minimum samples |
|---|---:|---:|---:|---:|---:|
| `tasks-100` | 100 | 10 | 1 | 300 | 20 |
| `tasks-500` | 500 | 50 | 5 | 300 | 20 |
| `tasks-2000` | 2,000 | 200 | 20 | 300 | 20 |

The HTTP capacity probe fixes 100 concurrent workspace queries and 500 SSE connections. Recovery
measurements cover PostgreSQL failover, artifact restore, and effect reconciliation.

## Report binding

The report stores three hashes:

- `tool_sha256` covers the Go, JSON, and shell files in `scripts/delivery-acceptance`.
- `config_sha256` covers the exact configuration bytes.
- `subject_sha256` covers the artifact store, Delivery browser files, dependency manifests,
  license manifests, `Dockerfile`, and CI workflow.

`check.sh` fails when any hash changes. Run `run.sh` again after an intentional change.

`publication_gate.publication_ready` is `true` only when every required probe passes. A missing
command produces `blocked`. A command failure or an invalid result produces `failed`.
