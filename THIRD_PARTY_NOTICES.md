# 第三方声明

本仓库通过 Go Modules 和 npm 使用第三方软件。锁文件记录完整的传递依赖和校验值。
除下文明确列出的包管理器依赖外，本仓库没有复制参考项目的源文件。

## Go 直接依赖

| 项目 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| [hastekit/agent-sdk-go](https://github.com/hastekit/agent-sdk-go) | `v0.0.24` | Apache-2.0 | Agent loop、typed tools、file history、HITL pause/resume、provider 和模型 retry |
| [go-yaml/yaml](https://github.com/go-yaml/yaml) | `v3.0.1` | MIT 和 Apache-2.0 双许可 | 在测试中解析 `contract.yaml` |
| [google/uuid](https://github.com/google/uuid) | `v1.6.0` | BSD-3-Clause | 生成 run ID 和 SDK resolution message ID |
| [jackc/pgx](https://github.com/jackc/pgx) | `v5.11.0` | MIT | PostgreSQL 连接池、协议和迁移执行 |

本项目没有复制 hastekit 源码。`go.mod` 固定版本，`go.sum` 记录模块校验值。hastekit 提供
provider fallback middleware，但当前实现没有配置 fallback。

## Web 运行时依赖

| npm 包 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| `react`、`react-dom` | `19.3.0` | MIT | 页面和状态渲染 |
| `@amap/amap-jsapi-loader` | `1.0.1` | MIT | 按需加载高德 JS API |
| `lucide-react` | `1.50.0` | ISC | 界面图标 |
| `zod` | `4.6.5` | MIT | 浏览器端 API 边界校验 |

## Web 构建和测试依赖

| npm 包 | 版本 | 许可证 |
|---|---|---|
| `@amap/amap-jsapi-types` | `0.0.15` | MIT |
| `@types/node` | `24.10.0` | MIT |
| `@types/react`、`@types/react-dom` | `19.3.0` | MIT |
| `@vitejs/plugin-react` | `6.1.1` | MIT |
| `vite` | `8.3.2` | MIT |
| `vitest` | `5.0.3` | MIT |
| `playwright-core` | `1.63.0` | Apache-2.0 |
| `typescript` | `7.0.2` | Apache-2.0 |

`web/package-lock.json` 固定直接依赖和传递依赖。各 npm 包自带许可证文件。

## 参考项目

以下项目只用于研究和设计比较，没有源文件或代码片段进入本仓库：

- [jattiphrswan/logistics-tracker](https://github.com/jattiphrswan/logistics-tracker)：
  轨迹模拟、地图组件、告警时间线和回放交互。
- [09karankr/port-logistics-intelligence](https://github.com/09karankr/port-logistics-intelligence)：
  风险评分展示和 append-only audit。
- [dominicfinn/open_tms](https://github.com/dominicfinn/open_tms)：
  shipment、carrier 和 operational issue 领域划分。

若以后复制第三方文件或代码片段，必须在此登记项目、来源路径、目标路径、用途和许可证要求。
