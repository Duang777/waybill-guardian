# 城市配送生产验收报告

> 此文件由 `go run ./scripts/delivery-acceptance report` 生成。权威数据位于 [`delivery-acceptance.v1.json`](./delivery-acceptance.v1.json)。

## 执行环境

- 生成时间：`2026-10-10T15:59:02Z`
- 源码修订：`f921411b2698384c3d9519b6340b102f6556f221`
- 验收工具 SHA-256：`14a992d888c20afb8f67d7bca198a853ffee202b5805c2e11f91e6f6b91e119e`
- 被测源码 SHA-256：`5dd760ddfa162276e3409d515ba4610afc97c6f7dc3fc7d1246453a19a07abe1`
- Go：`go1.26.9`，平台：`darwin/arm64`
- 操作系统：`Darwin 25.5.0`
- CPU：`Apple M5 Pro`，内存：`51539607552` 字节

## 探针状态

| 探针 | 状态 | 发布必需 | 说明 |
|---|---|---:|---|
| `artifact-resilience` | `passed` | 是 | - |
| `browser-cargo` | `passed` | 是 | - |
| `capacity-matrix` | `blocked` | 是 | command environment variable DELIVERY_ACCEPTANCE_CAPACITY_COMMAND is not set |
| `query-sse-capacity` | `blocked` | 是 | command environment variable DELIVERY_ACCEPTANCE_CONCURRENCY_COMMAND is not set |
| `failure-recovery` | `blocked` | 是 | command environment variable DELIVERY_ACCEPTANCE_RECOVERY_COMMAND is not set |
| `authorization` | `blocked` | 是 | command environment variable DELIVERY_ACCEPTANCE_AUTHORIZATION_COMMAND is not set |
| `observability` | `blocked` | 是 | command environment variable DELIVERY_ACCEPTANCE_OBSERVABILITY_COMMAND is not set |
| `supply-chain` | `passed` | 是 | - |

## 容量与恢复数据

| 探针 | 场景 | 样本 | 任务/车/仓/每车货物 | 查询/SSE | p50 | p95 | p99 | 峰值内存 | 可行率 | 质量差距 | 恢复时间 |
|---|---|---:|---|---|---:|---:|---:|---:|---:|---:|---:|
| `artifact-resilience` | `artifact-put` | 30 | - | - | 13.691 ms | 18.556 ms | 19.175 ms | 3.27 MiB | 1000000 ppm | 0 ppm | - |
| `artifact-resilience` | `artifact-open` | 30 | - | - | 1.437 ms | 1.887 ms | 2.114 ms | 3.27 MiB | 1000000 ppm | 0 ppm | - |
| `artifact-resilience` | `artifact-verify` | 30 | - | - | 1.371 ms | 1.769 ms | 2.088 ms | 3.27 MiB | 1000000 ppm | 0 ppm | - |
| `artifact-resilience` | `artifact-restore` | 1 | - | - | 300.702 ms | 300.702 ms | 300.702 ms | 3.27 MiB | 1000000 ppm | 0 ppm | 300.702 ms |

## 检查结果

| 探针 | 检查 | 结果 | 证据 |
|---|---|---:|---|
| `artifact-resilience` | `tenant_isolation` | 通过 | a foreign tenant could not open the digest |
| `artifact-resilience` | `digest_integrity` | 通过 | all stored digests reopened and verified |
| `artifact-resilience` | `corruption_detected` | 通过 | tampered envelope returned ErrIntegrity |
| `artifact-resilience` | `backup_restore` | 通过 | source and restored trees match; recovery_ns=300701542 |
| `artifact-resilience` | `size_limit` | 通过 | oversize artifact returned ErrTooLarge |
| `artifact-resilience` | `cancellation` | 通过 | cancelled context stopped publication |
| `artifact-resilience` | `concurrent_put` | 通过 | concurrent writers converged on one digest |
| `artifact-resilience` | `bounded_heap` | 通过 | peak_heap_bytes=3431896 limit=134217728 |
| `artifact-resilience` | `bounded_goroutines` | 通过 | baseline=1 peak=33 final=1 |
| `artifact-resilience` | `private_permissions` | 通过 | artifact files and directories deny group and other access |
| `browser-cargo` | `cargo_count` | 通过 | verify:delivery completed its 300-item assertion |
| `browser-cargo` | `instanced_rendering` | 通过 | verify:delivery completed its InstancedMesh assertion |
| `browser-cargo` | `nonblank_canvas` | 通过 | colors=12 chromatic_pixels=13 |
| `browser-cargo` | `state_matrix` | 通过 | states=20 expected=20 |
| `browser-cargo` | `remount_release` | 通过 | samples=21 remount_cycles=20 |
| `supply-chain` | `go_vulnerabilities` | 通过 | govulncheck reported no reachable vulnerability |
| `supply-chain` | `web_vulnerabilities` | 通过 | npm audit reported no high or critical production vulnerability |
| `supply-chain` | `licenses` | 通过 | committed Go and web license manifests are current |
| `supply-chain` | `container_contents` | 通过 | runtime image contains licenses and excludes source and environment files |
| `supply-chain` | `container_vulnerabilities` | 通过 | Trivy reported no fixable high or critical image vulnerability |
| `supply-chain` | `cyclonedx_sbom` | 通过 | Syft generated a nonempty CycloneDX image SBOM |

## 发布门禁

当前报告不能作为生产发布准入证据，`publication_ready=false`。

- 缺少执行入口的探针：`authorization`、`capacity-matrix`、`failure-recovery`、`observability`、`query-sse-capacity`

`blocked` 表示仓库中没有对应生产入口或本次未提供命令。报告不会用测试替身补写容量、故障恢复或越权结论。
