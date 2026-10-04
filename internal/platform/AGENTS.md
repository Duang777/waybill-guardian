# internal/platform · AGENTS.md

## 职责

本目录定义 TMS、天气读接口、运单目录和 transport-neutral 写 runtime。
`internal/tools` 只依赖这些接口，不依赖文件格式或具体外部 SDK。

## 接口

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
    Network NetworkCatalog
}
```

写操作通过 `WriteRuntime` 的 capability、bind、dispatch、lookup 和 recovery 契约执行，
不会与读 adapter 混合。所有跨边界方法接受 `context.Context` 和请求对象。

## 数据源

`internal/platform/filestore` 将 JSON/CSV v1 完整校验后发布为不可变 `ReadSet`。`mock`
加载内嵌 JSON，`file` 加载 `DATA_FILE`。读取方法返回副本，运行期间不热更新。
v1 可选网络扩展包含 Hub、Vehicle、Route 和运单引用；出现任一网络实体时，三类实体与引用
必须完整。`cmd/datagenerate` 可复现生成 72 港、200 运单的非官方仿真数据。

`tools.FixtureWriteRuntime` 为 mock/file 模式提供确定性 effect 结果，并使用自己的互斥锁保护
幂等结果 map。它不修改读快照、不调用外部系统，也不发送真实短信。

`PLATFORM` 默认为 `mock`。`PLATFORM=file` 要求数据文件、local auth 和 JSONL storage。
`PLATFORM=real` 未完成真实 adapter 时返回 `ErrNotImplemented`，不会静默回退。

## 接入真实平台

真实读 adapter 必须实现 `ReadSet`，真实写 adapter 必须实现 `WriteRuntime`，并把项目生成的
幂等键原样传给下游。若下游不支持幂等写入，adapter 必须能按 key 查询已有结果。完成实现和
恢复测试之前，不得让 `PLATFORM=real` 启动。

## 验证

测试覆盖 JSON/CSV 等价、校验错误、defensive copy、并发读取、候选承运商校验、
三类写操作去重、稳定结果 ID 和 `PLATFORM=real` fail-fast。
