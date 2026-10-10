# 城市配送基准证据

> 此文件由 `go run ./scripts/delivery-benchmark report` 生成。权威数据位于 [`delivery-benchmark.v1.json`](./delivery-benchmark.v1.json)。

## 执行环境

- 生成时间：`2026-10-10T15:12:27Z`
- 源码修订：`63de038c601a086463f594da7ed2f2ff5730c562`
- 基准工具 SHA-256：`34b840140eb2bfcbe00bdf2a14b0f4fd17f808db9144be4c797d8a35ab4ce593`
- Go：`go1.25.3`，平台：`darwin/arm64`
- 操作系统：`Darwin 25.5.0`
- CPU：`Apple M5 Pro`，内存：`51539607552` 字节
- 每个 adapter 和数据集重放：`20` 次
- 评估预算：`100000`，单次超时：`120` 秒

## 固定数据集

| 数据集 | seed | 请求 | 任务 | 货物 | 车辆 | canonical SHA-256 | problem digest |
|---|---:|---:|---:|---:|---:|---|---|
| `synthetic-s` | 101001 | 8 | 16 | 8 | 8 | `a13d90eaf06ee57c` | `de23f1f4c93c5271` |
| `synthetic-m` | 101002 | 32 | 64 | 32 | 32 | `9fbd2f3b8aac4ad5` | `807875f530cef022` |
| `synthetic-l` | 101003 | 128 | 256 | 128 | 128 | `e1a9b73857312efa` | `33fc3a0fac36c4bb` |

## Adapter 状态

| Adapter | 状态 | 发布必需 | 说明 |
|---|---|---:|---|
| `reference-baseline` | `passed` | 否 | - |
| `builtin` | `blocked` | 是 | command environment variable DELIVERY_BENCHMARK_BUILTIN_COMMAND is not set |
| `vroom` | `blocked` | 否 | command environment variable DELIVERY_BENCHMARK_VROOM_COMMAND is not set |
| `or-tools` | `blocked` | 否 | command environment variable DELIVERY_BENCHMARK_ORTOOLS_COMMAND is not set |
| `pyvrp` | `blocked` | 否 | command environment variable DELIVERY_BENCHMARK_PYVRP_COMMAND is not set |

## 实测结果

| Adapter | 数据集 | 可行率 | digest 一致 | 硬约束违规 | 车辆 | 总里程（米） | 总成本（分） | 准时率（ppm） | 求解 p50（毫秒） | 校验 p50（毫秒） | 校验 p95（毫秒） |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `reference-baseline` | `synthetic-s` | 20/20 | 是 | 0 | 8 | 783836 | 165928 | 1000000 | 0.621 | 1.929 | 2.029 |
| `reference-baseline` | `synthetic-m` | 20/20 | 是 | 0 | 32 | 3011236 | 644389 | 1000000 | 2.719 | 7.198 | 7.605 |
| `reference-baseline` | `synthetic-l` | 20/20 | 是 | 0 | 128 | 11890258 | 2553485 | 1000000 | 10.386 | 33.959 | 34.627 |

## 发布门禁

当前报告不能作为生产 solver 的发布质量证明。以下发布必需 adapter 尚未通过：`builtin`。

报告只陈述固定数据和共同能力范围内的实测结果。它不证明全局最优，也不构成物理装载安全认证。
