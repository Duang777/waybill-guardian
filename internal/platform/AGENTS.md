# internal/platform · AGENTS.md

## 职责

本目录定义 TMS、天气和通知接口。`internal/tools` 只依赖这些接口，不依赖 mock 或具体外部
SDK。

## 接口

```go
type TMSClient interface {
    GetWaybill(context.Context, GetWaybillRequest) (Waybill, error)
    GetTracking(context.Context, GetTrackingRequest) ([]TrackPoint, error)
    GetDriver(context.Context, GetDriverRequest) (Driver, error)
    Reassign(context.Context, ReassignRequest) (ReassignOrder, error)
    CreateClaim(context.Context, CreateClaimRequest) (ClaimOrder, error)
}

type WeatherClient interface {
    GetRoadWeather(context.Context, GetRoadWeatherRequest) ([]RoadWeather, error)
}

type NotificationClient interface {
    SendSMS(context.Context, SendSMSRequest) (SMSReceipt, error)
}
```

所有方法接受 `context.Context` 和请求对象。请求对象避免长参数列表，并确保每个写请求都包含
`IdempotencyKey`。

## mock 实现

`Mock` 同时实现三个接口。数据来自嵌入二进制的
`internal/tools/testdata/demo.json`。相同查询返回相同数据，相同幂等键返回相同写入结果。
mock 不调用外部系统，也不发送真实短信。

`PLATFORM` 默认为 `mock`。`PLATFORM=real` 立即返回 `ErrNotImplemented`，避免配置错误时静默
使用 mock。`RealAdapter` 只提供接口占位实现，每个方法都返回该错误。

## 接入真实平台

真实 adapter 必须实现三个接口，并把项目生成的幂等键原样传给下游。若下游不支持幂等写入，
adapter 必须能按 key 查询已有结果。完成实现和恢复测试之前，不得让 `PLATFORM=real` 启动。

## 验证

测试覆盖确定性查询、候选承运商校验、三类写操作去重、稳定结果 ID 和
`PLATFORM=real` fail-fast。
