# 文件数据源与任意运单设计

## 实现状态

本设计已落实到 `internal/platform/filestore`、`cmd/dataimport`、`POST /api/runs` 和前端运单目录。
`web/scripts/verify-file-data.mjs` 使用不同城市的双运单 CSV 验证选择、竞态抑制、审批和写入。
`scripts/check-production-fixture-literals.sh` 阻止已知演示值重新进入生产 Go/TypeScript 源码。

## 问题

Issue #60 要让服务从 JSON 或 CSV 加载运单数据，并对文件中的任意运单执行完整处置流程。
当前代码同时存在四个固定点：

1. `openPlatformRuntime` 的读数据始终来自内嵌 fixture。
2. `StartDemo` 和 `/api/demo/trigger` 固定选择一张运单。
3. `ScenarioModel`、归因事件和审批证据包含固定司机、路线、承运商和异常文案。
4. 前端没有运单目录和选择状态，并显示固定路线与异常标签。

只替换数据文件不能解决这些问题。设计必须同时处理导入、运行身份、模型证据和前端选择，
并保留审批、幂等、审计、授权、隐私和恢复约束。

## 调用方用法

### 启动文件模式

`DATA_FILE` 接受一个规范 JSON 文件或一个长表 CSV 文件。服务启动时读取一次，运行期间不热
更新。

```bash
PLATFORM=file \
DATA_FILE=./official-v1.csv \
AUTH_MODE=local \
STORAGE=jsonl \
go run ./cmd/server
```

文件模式只用于本地回环演示。写操作继续使用内存 effect runtime，并经过现有审批和幂等层。

### 校验同一文件

```bash
go run ./cmd/dataimport validate --data ./official-v1.csv
```

CLI 和服务端都调用 `filestore.Load`。CLI 校验成功意味着同一文件可以通过服务启动校验。

成功输出只包含数据集标识和数量：

```text
valid dataset=contest-v1 format=csv waybills=200 anomalies=17 hubs=72 vehicles=200 routes=72
```

失败输出包含文件内位置，不输出手机号、车牌或绝对路径：

```text
row 18 recorded_at: must be later than the previous point for waybill "YD2026101042"
header: missing required column "driver_id"
row 9 longitude: must be between -180 and 180
```

仓库内的非官方仿真数据由固定规则生成：

```bash
go run ./cmd/datagenerate \
  --output ./data/simulated/waybills-v1.json \
  --waybills 200
```

默认结果包含 72 个公路港、72 条线路、200 台车辆、200 张运单和
`delay`、`damage`、`loss`、`weather`、`fatigue` 五类异常。生成结果仍必须通过
`cmd/dataimport validate`，不能绕过运行时校验。

### 选择和启动运单

```http
GET /api/waybills
```

```json
{
  "waybills": [
    {
      "waybill_id": "YD2026101042",
      "origin": "宁波",
      "destination": "西安",
      "status": "delayed",
      "has_anomaly": true,
      "anomaly_label": "襄阳服务区",
      "last_recorded_at": "2026-10-11T13:06:00Z"
    }
  ]
}
```

```http
POST /api/runs
Content-Type: application/json

{"waybill_id":"YD2026101042"}
```

返回的 `waybill_id` 是后续详情、审批、SSE 和回放的权威值。前端不能继续信任请求发出前的选择
状态。

## 形态

### 不可变读快照

`internal/platform` 将读能力从写能力中拆开。文件 adapter 不实现改派、赔付或通知。

```go
type TMSReader interface {
	GetWaybill(context.Context, GetWaybillRequest) (Waybill, error)
	GetTracking(context.Context, GetTrackingRequest) ([]TrackPoint, error)
	GetDriver(context.Context, GetDriverRequest) (Driver, error)
}

type WeatherReader interface {
	GetRoadWeather(context.Context, GetRoadWeatherRequest) ([]RoadWeather, error)
}

type WaybillCatalog interface {
	ListWaybills(context.Context) ([]WaybillSummary, error)
}

type ReadSet struct {
	TMS     TMSReader
	Weather WeatherReader
	Catalog WaybillCatalog
}
```

`filestore.Load` 将两种外部格式转换为同一个私有 draft，完成全部校验后一次性发布快照：

```go
type Loaded struct {
	Reads  platform.ReadSet
	Source SourceDescriptor
	Stats  Stats
}

func Load(path string) (Loaded, error)
func LoadEmbeddedJSON(name string, raw []byte) (Loaded, error)
```

快照预先建立以下索引：

```text
waybill_id -> Waybill
waybill_id -> ordered TrackPoint[]
driver_id  -> Driver
route key  -> ordered RoadWeather[]
sorted WaybillSummary[]
```

构造完成后不再写入这些 map 和 slice。所有返回 slice 的方法都返回副本，因此读取不需要锁。
替换文件必须重启服务，活动 run 不会在执行中切换事实源。

### 独立写 runtime

现有 `platform.WriteRuntime` 保持不变。文件模式和 mock 模式使用
`tools.FixtureWriteRuntime`，它持有自己的 effect 结果 map，并通过 `ReadSet` 校验运单、
候选承运商和通知接收方。

```go
type FixtureWriteRuntime struct {
	reads platform.ReadSet

	mu            sync.Mutex
	reassignments map[domain.IdempotencyKey]platform.ReassignOrder
	claims        map[domain.IdempotencyKey]platform.ClaimOrder
	messages      map[domain.IdempotencyKey]platform.SMSReceipt
}
```

锁只保护 effect 结果。读取不可变快照时不持锁。相同幂等键仍返回第一次结果，稳定响应 ID 仍由
服务端生成的幂等键派生。

### 私有导入模型

JSON 和 CSV 的 wire 类型只存在于 `internal/platform/filestore`，不得出现在 platform、
Guardian 或 HTTP 公共类型中。

规范 JSON v1 使用数组保留重复 ID，便于校验：

```json
{
  "schema_version": "v1",
  "dataset_id": "contest-v1",
  "hubs": [],
  "vehicles": [],
  "routes": [],
  "waybills": [],
  "drivers": [],
  "waybill_candidates": [],
  "tracking": [],
  "weather": []
}
```

CSV v1 是一个长表文件。列顺序可以变化，但列集合必须精确匹配模板：

```text
schema_version,dataset_id,record_type,waybill_id,origin,destination,cargo,
current_carrier_id,driver_id,status,sla_hours,shipper_phone,priority,carrier_id,
carrier_name,eta_hours,reliability_pct,tracking_sequence,label,recorded_at,
longitude,latitude,speed_kph,stop_hours,anomaly,driver_name,driver_phone,
driver_plate,continuous_drive_hours,fatigue_alert,weather_sequence,segment,
condition,alert_level,origin_hub_id,destination_hub_id,route_id,vehicle_id,
hub_id,hub_name,province,city,daily_capacity,vehicle_plate,vehicle_type,
load_capacity_tons,distance_km,standard_hours,anomaly_type
```

`record_type` 取以下值：

- `dataset`：声明一次 `schema_version` 和 `dataset_id`。
- `hub`：公路港主数据，包含省市、坐标和日作业能力。
- `vehicle`：车辆主数据，车牌只保存脱敏值。
- `route`：公路港之间的线路、里程和标准时效。
- `waybill`：运单主数据。
- `driver`：司机主数据。
- `candidate`：运单与候选承运商关系，包含优先级、ETA 和可靠性。
- `tracking`：轨迹点，包含显式序号。
- `weather`：按起讫地关联的天气路段，包含显式序号。

每种记录的字段矩阵如下。`record_type` 在每行必填；`stop_hours` 和 `anomaly_type`
按轨迹状态选填。网络扩展列可以不出现在旧 CSV header 中；一旦出现网络实体，
`origin_hub_id`、`destination_hub_id`、`route_id` 和 `vehicle_id` 必须完整。表中未列出的
字段必须为空。

| record_type | 数量 | 必填字段 |
|---|---:|---|
| `dataset` | 恰好 1 行 | `schema_version`, `dataset_id` |
| `hub` | 每个 `hub_id` 1 行 | `hub_id`, `hub_name`, `province`, `city`, `longitude`, `latitude`, `daily_capacity` |
| `vehicle` | 每个 `vehicle_id` 1 行 | `vehicle_id`, `vehicle_plate`, `vehicle_type`, `load_capacity_tons` |
| `route` | 每个 `route_id` 1 行 | `route_id`, `origin_hub_id`, `destination_hub_id`, `distance_km`, `standard_hours` |
| `waybill` | 每个 `waybill_id` 1 行 | 原字段；网络扩展启用后另需 `origin_hub_id`, `destination_hub_id`, `route_id`, `vehicle_id` |
| `driver` | 每个 `driver_id` 1 行 | `driver_id`, `driver_name`, `driver_phone`, `driver_plate`, `continuous_drive_hours`, `fatigue_alert` |
| `candidate` | 每个运单至少 1 行 | `waybill_id`, `priority`, `carrier_id`, `carrier_name`, `eta_hours`, `reliability_pct` |
| `tracking` | 每个运单至少 1 行 | `waybill_id`, `tracking_sequence`, `label`, `recorded_at`, `longitude`, `latitude`, `speed_kph`, `anomaly` |
| `weather` | 每条路线至少 1 行 | `origin`, `destination`, `weather_sequence`, `segment`, `condition`, `alert_level` |

解析器拒绝无关列中的非空值。ID 不做隐式 trim；首尾空白直接报错。`driver_id`、
`carrier_id` 采用 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`。候选 ETA 和可靠性属于
`waybill_id + carrier_id` 关系，不属于全局承运商。

首版不加入 mapping DSL。官方字段到规范 v1 的转换放在一次性导入脚本或人工映射步骤中。
在官方字段尚未发布时实现通用映射语言，会把未知规则变成长期运行时接口。

### 校验

语法解析和语义校验是 `filestore` 的私有阶段。调用方只能获得完整快照或错误，不能获得部分
数据。

JSON 校验：

- 拒绝未知字段、重复顶层值和尾随值。
- 限制文件大小。
- 要求 `schema_version` 为 `v1`。

CSV 校验：

- 拒绝缺失、未知和重复列。
- 拒绝变长记录、错误引号和未知 `record_type`。
- 拒绝不属于当前记录类型的非空单元格。
- 限制文件大小和总行数。

语义校验：

- 运单 ID 保持现有 `YD` 加十位数字的契约。
- 实体和关系 ID 唯一，引用必须存在。
- 每张运单必须有司机、候选承运商、轨迹和对应路线天气。
- 经度范围为 `[-180, 180]`，纬度范围为 `[-90, 90]`，数值必须有限。
- 时间必须为 RFC3339。每张运单的轨迹序号连续，时间严格递增。
- SLA、ETA、速度、停留和连续驾驶时长不能为负，可靠性在 `[0, 100]`。
- 候选优先级从 1 连续递增。
- `dataset_id` 必须匹配 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`。

校验最多收集 100 个问题，并按位置、字段和错误码稳定排序。服务端启动失败和 CLI 使用同一个
`ValidationError` 文本。

### 来源标识

运行审计继续记录 `platform_profile` 和 `read_source`。文件来源使用以下格式：

```text
file:csv:v1:<dataset_id>:<raw_sha256_prefix>
```

该值不包含绝对路径、联系方式、车牌或坐标。digest 固定一次进程使用的精确输入。
`Load` 只在错误中使用 `filepath.Base(path)`；底层打开和 stat 错误包装为稳定错误码，不直接
透传含绝对路径的 `PathError`。

### 任意运单 run

Guardian 删除生产代码中的 `DemoWaybillID`，并用以下方法替代 `StartDemo`：

```go
func (s *Service) StartRun(
	ctx context.Context,
	waybillID domain.WaybillID,
) (RunView, error)
```

执行顺序：

1. 检查 service 生命周期和运单 ID 格式。
2. 调用 `GetWaybill` 证明运单存在。
3. 生成 run 和 incident ID。
4. 追加带目标运单的 `run_started`。
5. 启动使用相同运单 ID 的 Agent。

不存在的运单不会产生审计前缀、history thread 或后台 goroutine。incident ID 使用通用前缀，
不假设异常一定是延误。

Guardian 将 platform summary 映射为自己的 API DTO。platform 类型不带 HTTP JSON tag：

```go
type WaybillCatalogItem struct {
	WaybillID     domain.WaybillID `json:"waybill_id"`
	Origin        string           `json:"origin"`
	Destination   string           `json:"destination"`
	Status        string           `json:"status"`
	HasAnomaly    bool             `json:"has_anomaly"`
	AnomalyLabel  string           `json:"anomaly_label,omitempty"`
	LastRecordedAt time.Time       `json:"last_recorded_at"`
}
```

`Service.ListWaybills` 先执行 service 生命周期检查和 `ctx.Err()` 检查，再把 platform summary
映射为新的 DTO 并返回副本。快照中的目录固定按 `waybill_id` 升序排列；兼容 alias 在授权过滤
后选择该顺序中的第一张异常运单。

详情接口同样不直接嵌入 platform 类型。Guardian 定义并映射 `WaybillDTO`、`TrackPointDTO`、
`DriverDTO` 和 `RoadWeatherDTO`，在该层完成 RFC3339 格式化、手机号与车牌遮罩和风险派生。
platform 领域类型不携带 JSON tag。

快照的 `GetWaybill` 必须复制 `CandidateCarriers`，`GetTracking`、`GetRoadWeather` 和
`ListWaybills` 必须复制返回 slice。调用方修改任何返回值都不能影响后续读取。

### 数据驱动的离线 Agent

`ScenarioModel` 继续从持久化 conversation 判断下一步，但每一步解析前一个工具结果：

1. 用 run 的运单 ID 查询运单。
2. 从运单结果读取 `driver_id`、起讫地和候选承运商。
3. 用同一运单 ID 查询轨迹。
4. 用返回的司机 ID 查询司机。
5. 用返回的起讫地构造天气查询。
6. 从实际候选列表选择第一个未尝试承运商。
7. 从实际疲劳、异常停留和天气字段生成文案。

模型解析器按 `call_id` 关联工具调用和结果，并严格解码模型安全 DTO。司机、路线或候选关系与
目标运单不一致时，run 失败，不改变冻结的工具参数契约。

Agent 工具链新增读取绑定 middleware。它从可信 `ToolCall.RunContext` 读取目标运单：

- `get_waybill` 和 `get_tracking` 的 `waybill_id` 必须等于 run 运单。
- `get_driver` 的 `driver_id` 必须等于 run 运单当前引用的司机。
- `get_road_weather` 的 route 必须等于 run 运单起讫地派生的 route。

middleware 在调用 typed handler 前完成这些检查，因此不修改冻结的工具参数。直接供 HTTP
详情使用的 platform 读取不经过 Agent middleware。

Guardian 在暂停时从同一个不可变 read source 派生结构化归因和审批证据。纯函数输入是运单、
轨迹、司机和天气领域值，输出是摘要、原因、证据和风险评分。首版不解析 assistant 自由文本，
也不为 Issue #59 提前增加证据引用协议。

### HTTP 与授权

新增：

```text
GET  /api/waybills
POST /api/runs
```

`GET /api/waybills`：

1. 要求 `read` capability。
2. 从 Guardian 获取稳定排序的目录。
3. 使用 `grant.Allows` 过滤后返回。

`POST /api/runs`：

1. 要求 `run:create` capability。
2. 要求 JSON content type，并严格解码 `{"waybill_id":"..."}`。
3. 校验运单 ID。
4. 先执行 `grant.Require(waybill_id)`，再查询是否存在，避免越权探测。
5. 调用 `StartRun` 并返回 202。

`POST /api/demo/trigger` 保留为空请求体兼容别名。handler 先按授权范围过滤目录，再选择第一张
含异常轨迹的运单，然后调用相同的 `StartRun`。没有可用异常运单时返回 404。

### 前端状态

前端新增严格解析的运单目录和一个运单选择控件：

```ts
type CatalogResource =
  | { kind: "loading" }
  | { kind: "empty" }
  | { kind: "ready"; items: readonly WaybillCatalogItem[] }
  | { kind: "error"; message: string };
```

启动顺序保持现有优先级：

1. 优先恢复待审批 run。
2. 其次恢复活动 run。
3. 没有 run 时加载目录，选择第一项并读取详情。
4. 空目录显示空状态并禁用启动。

启动所选运单后，前端使用响应中的 `run_id` 和 `waybill_id` 替换本地选择。切换目录或 run 时
继续使用 generation counter 和 `AbortSignal`，旧请求不得覆盖新状态。

本地地图的起讫标签来自运单，异常图例来自异常轨迹点。加载和空状态不显示 fixture 文案。
目录或详情加载失败时，页面清空旧运单视图，显示可重试错误，并保持启动按钮禁用。现有测试继续
覆盖 SSE 游标续传和 `(run_id, seq)` 去重，新增测试证明目录切换不改变这些行为。

## 模块图

```text
cmd/dataimport
  `-- internal/platform/filestore.Load

cmd/server
  +-- mock -> embedded JSON -> immutable snapshot
  +-- file -> DATA_FILE -> immutable snapshot
  `-- real -> embedded snapshot + tmssandbox WriteRuntime
             |
             v
internal/guardian
  +-- catalog/detail/start use cases
  +-- attribution and risk derivation
  `-- existing run/approval/recovery lifecycle
             |
             +--> internal/tools read handlers -> immutable snapshot
             |
             `--> approval/idempotency -> WriteRuntime
                                           +-- fixture memory effects
                                           `-- tmssandbox
```

## 兼容行为

- `PLATFORM=mock` 继续使用内嵌数据、local auth、JSONL 和七个工具。
- `PLATFORM=file` 要求 `DATA_FILE`、local auth 和 JSONL，保留回环监听和 Host 检查。
- `PLATFORM=real` 继续使用内嵌读快照、PostgreSQL、JWT、sandbox 改派和恢复覆盖检查。
- `/api/demo/trigger`、详情、审批、run snapshot 和 SSE 响应保持兼容。
- 运单 ID 语法保持不变。
- 不实现热更新、通用 mapping DSL、数据库导入或真实只读 TMS。
- v1 网络扩展保持向后兼容：旧文件可不含网络列；带网络数据的文件必须提供完整实体和引用。

## 实施切片

### 切片 1：数据契约

- 实现 JSON/CSV v1、共享校验器、不可变快照和来源标识。
- 实现 `cmd/dataimport validate`。
- 迁移内嵌 fixture 到 v1，并提供 `data/templates` 文件。
- 验证 JSON/CSV 等价、错误位置、引用、坐标、时间顺序和并发读取。
- 验证所有返回值的深复制和目录排序。

切片结束时提交并推送。服务行为尚不切换。

### 切片 2：运行时和 API

- 将工具、Guardian 和 fixture writer 迁移到读写拆分接口。
- 增加 file mode、目录 API 和任意运单 run API。
- 让 ScenarioModel、归因、审批证据和风险从数据派生。
- 更新 HTTP、授权、恢复和 PostgreSQL 测试。

切片结束时运行 Go race、vet 和 build，再提交并推送。

### 切片 3：前端和文档

- 增加目录选择、空状态和任意运单启动。
- 删除固定路线、运单和异常标签。
- 更新 README、AGENTS、RFC 和 demo 脚本。
- 使用一份不同城市、司机和承运商的 CSV 完成浏览器 E2E。
- 增加生产代码 fixture 字面量扫描，只允许 fixture、testdata、测试、文档和生成物。

切片结束时运行前端测试、build、audit 和 E2E，再执行 `./scripts/test-postgres.sh`、
`go test -race ./...`、`go vet ./...`、`go build ./...` 和 history governance 检查，最后
提交并推送。

## 综合决策

Candidate 1 是基线，因为它完整表达了不可变快照、候选关系、读写隔离和校验边界。

吸收 Candidate 2：

- Guardian/API 使用独立目录 DTO，platform 类型不携带 wire tag。
- 前端使用显式 loading、empty、ready 和 error 状态。
- 路线索引使用私有复合 key，不拼接可能冲突的字符串。

吸收 Candidate 3：

- CSV 改为单个长表文件，满足 `DATA_FILE=...csv`。
- 服务在创建审计和 history 文件前完成数据加载。
- 兼容 alias 在授权过滤后选择异常运单。

拒绝：

- 不使用全局 carrier ETA/可靠性，因为这些属性属于运单候选关系。
- 不把包含联系方式和坐标的整张 `WaybillCase` 暴露给每个工具。
- 不修改司机和天气工具参数，避免破坏冻结契约和已有 history。
- 不增加新的 audit evidence 投影，现有 read source 足以完成本 issue 的确定性归因。

## 取舍

- 接受重启才能换数据，换取每个进程内不可变且可定位的数据版本。
- 接受固定 CSV v1，换取可读校验和 CLI/服务端一致性。
- 接受启动时加载整个有界数据集，换取原子校验和低延迟读取。
- 接受迁移内部读写接口，换取文件 adapter 不伪装成可写 TMS。
- 接受首版只支持现有运单 ID 语法，避免同时迁移公开身份契约。

## 设计复核

- `filestore.Load` 隐藏格式识别、解析、校验、关联、排序、索引和 defensive copy，不是浅模块。
- wire 记录不离开 `filestore`，platform 记录不带 HTTP tag，不泄漏存储决策。
- parse 和 validate 是同一模块的私有实现，不向调用方暴露时间阶段。
- HTTP 增加协议和授权策略，Guardian 增加生命周期、DTO 映射、遮罩和审计顺序，不是透传层。
- 快照没有共享 writer，effect runtime 只锁自己的结果 map。
- 热路径最多为 HTTP 到 Guardian 到快照，或 tool 到快照。

实现按上述三个切片落地；每个切片都在独立提交中保留可重复验证的边界。
