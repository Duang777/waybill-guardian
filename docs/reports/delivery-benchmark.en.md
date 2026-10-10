# City delivery benchmark evidence

> `go run ./scripts/delivery-benchmark report` generates this file. The authoritative data is [`delivery-benchmark.v1.json`](./delivery-benchmark.v1.json).

## Runtime environment

- Generated at: `2026-10-10T15:12:27Z`
- Source revision: `63de038c601a086463f594da7ed2f2ff5730c562`
- Benchmark tool SHA-256: `34b840140eb2bfcbe00bdf2a14b0f4fd17f808db9144be4c797d8a35ab4ce593`
- Go: `go1.25.3`; platform: `darwin/arm64`
- Operating system: `Darwin 25.5.0`
- CPU: `Apple M5 Pro`; memory: `51539607552` bytes
- Replays per adapter and dataset: `20`
- Evaluation budget: `100000`; timeout per run: `120` seconds

## Fixed datasets

| Dataset | Seed | Requests | Tasks | Cargo | Vehicles | Canonical SHA-256 | Problem digest |
|---|---:|---:|---:|---:|---:|---|---|
| `synthetic-s` | 101001 | 8 | 16 | 8 | 8 | `a13d90eaf06ee57c` | `de23f1f4c93c5271` |
| `synthetic-m` | 101002 | 32 | 64 | 32 | 32 | `9fbd2f3b8aac4ad5` | `807875f530cef022` |
| `synthetic-l` | 101003 | 128 | 256 | 128 | 128 | `e1a9b73857312efa` | `33fc3a0fac36c4bb` |

## Adapter status

| Adapter | Status | Required for publication | Detail |
|---|---|---:|---|
| `reference-baseline` | `passed` | No | - |
| `builtin` | `blocked` | Yes | command environment variable DELIVERY_BENCHMARK_BUILTIN_COMMAND is not set |
| `vroom` | `blocked` | No | command environment variable DELIVERY_BENCHMARK_VROOM_COMMAND is not set |
| `or-tools` | `blocked` | No | command environment variable DELIVERY_BENCHMARK_ORTOOLS_COMMAND is not set |
| `pyvrp` | `blocked` | No | command environment variable DELIVERY_BENCHMARK_PYVRP_COMMAND is not set |

## Measured results

| Adapter | Dataset | Feasible replays | Stable digest | Hard violations | Vehicles | Distance (m) | Cost (cent) | On-time rate (ppm) | Solve p50 (ms) | Validate p50 (ms) | Validate p95 (ms) |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `reference-baseline` | `synthetic-s` | 20/20 | Yes | 0 | 8 | 783836 | 165928 | 1000000 | 0.621 | 1.929 | 2.029 |
| `reference-baseline` | `synthetic-m` | 20/20 | Yes | 0 | 32 | 3011236 | 644389 | 1000000 | 2.719 | 7.198 | 7.605 |
| `reference-baseline` | `synthetic-l` | 20/20 | Yes | 0 | 128 | 11890258 | 2553485 | 1000000 | 10.386 | 33.959 | 34.627 |

## Publication gate

This report is not production solver release evidence. These required adapters have not passed: `builtin`.

The report covers measured behavior on the fixed datasets and common capability profile. It does not prove global optimality or certify physical loading safety.
