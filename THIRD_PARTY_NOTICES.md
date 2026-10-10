# 第三方声明

本仓库通过 Go Modules 和 npm 使用第三方软件。锁文件记录完整的传递依赖和校验值。
除下文明确列出的包管理器依赖外，本仓库没有复制参考项目的源文件。

本项目本身采用 [Apache License 2.0](./LICENSE)。传递依赖清单见
[`docs/licenses/go.csv`](./docs/licenses/go.csv) 和
[`docs/licenses/web.csv`](./docs/licenses/web.csv)。两份清单由
[`scripts/licenses.sh`](./scripts/licenses.sh) 根据 `go.sum` 和
`web/package-lock.json` 生成，不代替各依赖随包发布的许可证原文。容器镜像另在
`/app/third_party_licenses` 保存实际打包依赖的许可证和 NOTICE 原文。

## Go 直接依赖

| 项目 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| [golang-jwt/jwt](https://github.com/golang-jwt/jwt) | `v5.3.1` | MIT | 验证 JWT 签名、issuer、audience 和时效声明 |
| [hastekit/agent-sdk-go](https://github.com/hastekit/agent-sdk-go) | `v0.0.24` | Apache-2.0 | Agent loop、typed tools、file history、HITL pause/resume、provider 和模型 retry |
| [go-yaml/yaml](https://github.com/go-yaml/yaml) | `v3.0.1` | MIT 和 Apache-2.0 双许可 | 在测试中解析 `contract.yaml` |
| [google/uuid](https://github.com/google/uuid) | `v1.6.0` | BSD-3-Clause | 生成 run ID 和 SDK resolution message ID |
| [jackc/pgx](https://github.com/jackc/pgx) | `v5.11.0` | MIT | PostgreSQL 连接池、协议和迁移执行 |
| [prometheus/client_golang](https://github.com/prometheus/client_golang) | `v1.23.2` | Apache-2.0 | 暴露固定标签的入站事件和 outbox 指标 |
| [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) | `v0.47.0` | BSD-3-Clause | 本地数据目录文件锁 |

本项目没有复制 hastekit 源码。`go.mod` 固定版本，`go.sum` 记录模块校验值。hastekit 提供
provider fallback middleware，但当前实现没有配置 fallback。

## Web 运行时依赖

| npm 包 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| `react`、`react-dom` | `19.3.0` | MIT | 页面和状态渲染 |
| `echarts` | `6.1.0` | Apache-2.0 | 按需渲染经营异常构成、当前处置状态和高异常占比线路图表 |
| `three` | `0.186.1` | MIT | 全国公路港三维场景、合批几何和 WebGL 渲染 |
| `@react-three/fiber` | `9.8.1` | MIT | Three.js 的 React renderer 和场景生命周期 |
| `@amap/amap-jsapi-loader` | `1.0.1` | MIT | 按需加载高德 JS API |
| `maplibre-gl` | `6.12.0` | BSD-3-Clause | 在未配置高德 key 时渲染可交互的矢量地图、轨迹和点位 |
| `lucide-react` | `1.50.0` | ISC | 界面图标 |
| `motion` | `14.0.0` | MIT | 数字、进度、实时事件、审批状态和轨迹选点反馈 |
| `zod` | `4.6.5` | MIT | 浏览器端 API 边界校验 |
| `@radix-ui/react-alert-dialog` | `1.1.24` | MIT | 驳回方案弹层的焦点锁定、Escape 关闭和焦点归还 |
| `@radix-ui/react-progress` | `1.1.17` | MIT | 风险进度条的无障碍语义 |
| `@tanstack/react-table` | `8.21.3` | MIT | 异常处置队列的行模型和排序 |
| `sonner` | `2.0.8` | MIT | 批量启动和审批结果的瞬时通知 |

ECharts 的 NOTICE 原文保存在
[`docs/licenses/echarts-NOTICE.txt`](./docs/licenses/echarts-NOTICE.txt)。

## Web 构建和测试依赖

| npm 包 | 版本 | 许可证 |
|---|---|---|
| `@amap/amap-jsapi-types` | `0.0.15` | MIT |
| `@types/geojson` | `7946.0.16` | MIT |
| `@types/node` | `24.10.0` | MIT |
| `@types/react`、`@types/react-dom` | `19.3.0` | MIT |
| `@types/three` | `0.186.0` | MIT |
| `@vitejs/plugin-react` | `6.1.1` | MIT |
| `vite` | `8.3.2` | MIT |
| `vitest` | `5.0.3` | MIT |
| `playwright-core` | `1.63.0` | Apache-2.0 |
| `typescript` | `7.0.2` | Apache-2.0 |

`web/package-lock.json` 固定直接依赖和传递依赖。各 npm 包自带许可证文件。

## CI 和验收工具

下列工具只在 CI 或 `scripts/delivery-acceptance/supply-chain-probe.sh` 中运行，不进入应用
运行时镜像：

| 工具 | 版本 | 许可证 | 用途 |
|---|---|---|---|
| [golang.org/x/vuln](https://github.com/golang/vuln) | `v1.8.0` | BSD-3-Clause | 扫描 Go 调用图中的已知漏洞 |
| [Anchore Syft](https://github.com/anchore/syft) | `v1.54.1` | Apache-2.0 | 从最终容器镜像生成 CycloneDX SBOM |
| [Aqua Trivy](https://github.com/aquasecurity/trivy) | `v0.75.0` | Apache-2.0 | 扫描最终容器镜像中的高危和严重漏洞 |

GitHub Actions 对 `anchore/sbom-action` 和 `aquasecurity/trivy-action` 使用固定 commit。CI
上传 CycloneDX JSON 作为构建证据，但应用不会读取该文件。

## 改编组件

`web/src/components/ui/` 中的按钮、徽标、进度、分段控件、数字和计时器改编自
[nolly-studio/cult-ui](https://github.com/nolly-studio/cult-ui) 的公开 registry：
`texture-button`、`halo-badge`、`halo-progress`、`halo-segmented`、
`rolling-number` 和 `timer`。同目录的基础组件组织和 AlertDialog 组合参考
[shadcn/ui](https://github.com/shadcn-ui/ui) 的项目内源码模式。本仓库没有引入 Tailwind，
而是用 CSS Modules 适配白色工业视觉，并只按实际用例接入 Radix Primitives。Cult UI 与
shadcn/ui 均采用 MIT 许可证；许可证原文分别保存在
[`web/src/components/ui/CULT-UI-LICENSE`](./web/src/components/ui/CULT-UI-LICENSE) 和
[`web/src/components/ui/SHADCN-UI-LICENSE`](./web/src/components/ui/SHADCN-UI-LICENSE)。

## 外部服务

正式演示入口默认使用在线模型，但仓库不捆绑模型服务，也不保存 API key。CI 和无凭据演示
使用 `AGENT_MODE=offline`。部署方选择在线服务后，需要自行取得账号、密钥和许可，并遵守
调用时有效的价格、数据规则和服务条款。只有部署方实际配置的服务会收到脱敏后的运单证据。

| 可选服务 | 接口用途 | 官方计费 | 官方条款 |
|---|---|---|---|
| 阿里云百炼千问 | OpenAI-compatible Chat Completions | [模型调用价格](https://help.aliyun.com/zh/model-studio/model-pricing) | [阿里云百炼服务协议](https://terms.alicdn.com/legal-agreement/terms/common_platform_service/20230728213935489/20230728213935489.html) |
| DeepSeek | OpenAI-compatible Chat Completions | [模型与价格](https://api-docs.deepseek.com/zh-cn/quick_start/pricing/) | [开放平台服务条款](https://cdn.deepseek.com/policies/zh-CN/deepseek-open-platform-terms-of-service.html) |
| 火山方舟豆包 | OpenAI-compatible Chat Completions | [模型服务价格](https://www.volcengine.com/docs/82379/1544106) | [火山方舟专用条款](https://www.volcengine.com/docs/82379/1104498) |
| Kimi | OpenAI-compatible Chat Completions | [模型推理价格](https://platform.kimi.com/docs/pricing/chat) | [Kimi 开放平台服务协议](https://platform.kimi.com/docs/agreement/modeluse) |
| 智谱 GLM | OpenAI-compatible Chat Completions | [API 定价](https://docs.bigmodel.cn/cn/guide/start/pricing) | [大模型开放平台服务协议](https://docs.bigmodel.cn/cn/terms/service-agreement) |
| OpenAI | Responses API 或 Chat Completions | [API 定价](https://openai.com/api/pricing/) | [Service Terms](https://openai.com/policies/service-terms/) |

上表是配置入口，不表示仓库已用每个服务完成真实调用验收。模型名、价格和条款可能变化，
部署方必须在运行前复核官方页面。

- 在线模型：`AGENT_MODE=online` 支持 OpenAI Responses API 和 OpenAI-compatible Chat
  Completions API。
- 地图和底图：配置 `VITE_AMAP_KEY` 后，浏览器加载高德地图 JS API。地图、底图和接口数据
  不随仓库再分发，使用时适用
  [高德地图开放平台服务协议](https://lbs.amap.com/pages/terms/)。
- 默认矢量底图：未配置高德 key 时，MapLibre 从
  [OpenFreeMap](https://openfreemap.org/) 加载 Positron 样式和瓦片，底层道路与地名数据
  来自 [OpenStreetMap](https://www.openstreetmap.org/copyright)。页面显示两者署名；
  在线样式、瓦片和数据不随仓库再分发。

## 城市配送基准数据与求解器

[`scripts/delivery-benchmark`](./scripts/delivery-benchmark) 当前只使用仓库自有的固定 seed
合成数据。生成器版本、seed、字段来源、单位、舍入规则和 SHA-256 记录在
[`manifest.json`](./data/simulated/delivery-benchmark-v1/manifest.json)。该数据不包含第三方
订单、地图、客户或车辆记录。

基准配置为 VROOM、OR-Tools 和 PyVRP 预留外部命令入口，但本仓库没有把这些工具加入
`go.mod`、`package-lock.json` 或容器镜像。未配置命令时，权威报告把 adapter 标记为
`blocked`，不生成质量结果。部署方接入外部工具时，需要登记实际版本、分发方式和许可证，
并重新生成 Go、Web 和容器许可证清单。

Solomon、Gehring-Homberger、OR-Library 和 3L-CVRP 实例目前没有进入仓库、容器或已发布
基准报告。引入外部实例前，必须记录下载 URL、下载日期、原始文件 SHA-256、适用条款和
内部转换摘要。未完成这些字段的数据不能进入发布证据。

## 参考项目

以下项目只用于研究和设计比较，没有源文件或代码片段进入本仓库：

- [jattiphrswan/logistics-tracker](https://github.com/jattiphrswan/logistics-tracker)：
  轨迹模拟、地图组件、告警时间线和回放交互。
- [09karankr/port-logistics-intelligence](https://github.com/09karankr/port-logistics-intelligence)：
  风险评分展示和 append-only audit。
- [dominicfinn/open_tms](https://github.com/dominicfinn/open_tms)：
  shipment、carrier 和 operational issue 领域划分。
- WareTrack 概念视频：只借鉴“等轴测运营沙盘”和“对象即数据”的视觉语言。原作者未发布
  代码或许可证，本仓库没有使用其代码、模型、纹理或其他素材。原状态页不接受自动链接检查，
  因此第三方声明不保留不可验证的 URL。
- [BoardUI](https://github.com/BoardUI/boardui)：
  参考其 MIT 许可公开源码中的浅灰分组底、白色内部工作面板、轻量接触阴影和分段控件
  层级。本仓库使用 CSS Modules 独立实现，没有复制 BoardUI 组件、模板或素材。
- [Awwwards](https://www.awwwards.com/)：
  只参考其获奖站点常见的编辑排版、全宽主视觉和细线分区方法。本仓库没有复制页面源码、
  商标、图片、字体或其他素材。

若以后复制第三方文件或代码片段，必须在此登记项目、来源路径、目标路径、用途和许可证要求。
