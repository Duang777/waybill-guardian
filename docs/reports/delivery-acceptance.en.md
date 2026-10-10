# City delivery production acceptance report

> `go run ./scripts/delivery-acceptance report` generates this file. The authoritative data is [`delivery-acceptance.v1.json`](./delivery-acceptance.v1.json).

## Runtime environment

- Generated at: `2026-10-10T15:59:02Z`
- Source revision: `f921411b2698384c3d9519b6340b102f6556f221`
- Acceptance tool SHA-256: `14a992d888c20afb8f67d7bca198a853ffee202b5805c2e11f91e6f6b91e119e`
- Tested source SHA-256: `5dd760ddfa162276e3409d515ba4610afc97c6f7dc3fc7d1246453a19a07abe1`
- Go: `go1.26.9`; platform: `darwin/arm64`
- Operating system: `Darwin 25.5.0`
- CPU: `Apple M5 Pro`; memory: `51539607552` bytes

## Probe status

| Probe | Status | Required for publication | Detail |
|---|---|---:|---|
| `artifact-resilience` | `passed` | yes | - |
| `browser-cargo` | `passed` | yes | - |
| `capacity-matrix` | `blocked` | yes | command environment variable DELIVERY_ACCEPTANCE_CAPACITY_COMMAND is not set |
| `query-sse-capacity` | `blocked` | yes | command environment variable DELIVERY_ACCEPTANCE_CONCURRENCY_COMMAND is not set |
| `failure-recovery` | `blocked` | yes | command environment variable DELIVERY_ACCEPTANCE_RECOVERY_COMMAND is not set |
| `authorization` | `blocked` | yes | command environment variable DELIVERY_ACCEPTANCE_AUTHORIZATION_COMMAND is not set |
| `observability` | `blocked` | yes | command environment variable DELIVERY_ACCEPTANCE_OBSERVABILITY_COMMAND is not set |
| `supply-chain` | `passed` | yes | - |

## Capacity and recovery measurements

| Probe | Scenario | Samples | Tasks/vehicles/depots/cargo per vehicle | Queries/SSE | p50 | p95 | p99 | Peak memory | Feasibility | Quality gap | Recovery |
|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|---:|
| `artifact-resilience` | `artifact-put` | 30 | - | - | 13.691 ms | 18.556 ms | 19.175 ms | 3.27 MiB | 1000000 ppm | 0 ppm | - |
| `artifact-resilience` | `artifact-open` | 30 | - | - | 1.437 ms | 1.887 ms | 2.114 ms | 3.27 MiB | 1000000 ppm | 0 ppm | - |
| `artifact-resilience` | `artifact-verify` | 30 | - | - | 1.371 ms | 1.769 ms | 2.088 ms | 3.27 MiB | 1000000 ppm | 0 ppm | - |
| `artifact-resilience` | `artifact-restore` | 1 | - | - | 300.702 ms | 300.702 ms | 300.702 ms | 3.27 MiB | 1000000 ppm | 0 ppm | 300.702 ms |

## Check results

| Probe | Check | Result | Evidence |
|---|---|---:|---|
| `artifact-resilience` | `tenant_isolation` | pass | a foreign tenant could not open the digest |
| `artifact-resilience` | `digest_integrity` | pass | all stored digests reopened and verified |
| `artifact-resilience` | `corruption_detected` | pass | tampered envelope returned ErrIntegrity |
| `artifact-resilience` | `backup_restore` | pass | source and restored trees match; recovery_ns=300701542 |
| `artifact-resilience` | `size_limit` | pass | oversize artifact returned ErrTooLarge |
| `artifact-resilience` | `cancellation` | pass | cancelled context stopped publication |
| `artifact-resilience` | `concurrent_put` | pass | concurrent writers converged on one digest |
| `artifact-resilience` | `bounded_heap` | pass | peak_heap_bytes=3431896 limit=134217728 |
| `artifact-resilience` | `bounded_goroutines` | pass | baseline=1 peak=33 final=1 |
| `artifact-resilience` | `private_permissions` | pass | artifact files and directories deny group and other access |
| `browser-cargo` | `cargo_count` | pass | verify:delivery completed its 300-item assertion |
| `browser-cargo` | `instanced_rendering` | pass | verify:delivery completed its InstancedMesh assertion |
| `browser-cargo` | `nonblank_canvas` | pass | colors=12 chromatic_pixels=13 |
| `browser-cargo` | `state_matrix` | pass | states=20 expected=20 |
| `browser-cargo` | `remount_release` | pass | samples=21 remount_cycles=20 |
| `supply-chain` | `go_vulnerabilities` | pass | govulncheck reported no reachable vulnerability |
| `supply-chain` | `web_vulnerabilities` | pass | npm audit reported no high or critical production vulnerability |
| `supply-chain` | `licenses` | pass | committed Go and web license manifests are current |
| `supply-chain` | `container_contents` | pass | runtime image contains licenses and excludes source and environment files |
| `supply-chain` | `container_vulnerabilities` | pass | Trivy reported no fixable high or critical image vulnerability |
| `supply-chain` | `cyclonedx_sbom` | pass | Syft generated a nonempty CycloneDX image SBOM |

## Publication gate

This report does not admit a production release. `publication_ready=false`.

- Probes without an execution entry point: `authorization`、`capacity-matrix`、`failure-recovery`、`observability`、`query-sse-capacity`

`blocked` means that the repository has no corresponding production entry point or that this run did not provide its command. The report does not replace capacity, recovery, or authorization evidence with a test double.
