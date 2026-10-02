# internal/platform · AGENTS.md

## 职责

TMS 接口的 **seam（接缝层）**：定义 Go interface，mock 实现与未来真实平台 adapter
实现同一接口。现在用 mock 跑通 demo，真实接口就绪后只需新增一个 adapter 实现，
agent 与 tools 层零改动。

## 接口定义（TODO 落代码）

```go
type TMSClient interface {
    GetWaybill(waybillID string) (*Waybill, error)
    GetTracking(waybillID string) ([]TrackPoint, error)
    GetDriver(driverID string) (*Driver, error)
    Reassign(waybillID, carrierID, idempotencyKey string) (*ReassignOrder, error)
    CreateClaim(waybillID, claimType, idempotencyKey string) (*ClaimOrder, error)
}
```

- `MockTMS`：确定性演示数据（杭州→成都剧本），`internal/tools/testdata/demo.json` 驱动。
- `RealTMSAdapter`（TODO）：赛事平台真实接口就绪后实现；与 Mock 实现同一 interface，
  通过配置开关切换。
- 天气工具 `ext.get_road_weather`：独立 interface（`WeatherClient`），mock 返回沿途天气；
  真实版接天气 API。

## 设计决策

- **interface 隔离变化**：agent 永远只依赖 `TMSClient`，不知道 mock 还是真实。
  这是"赛前无接口也能全速开发"的关键。
- **mock 数据与剧本绑定**：mock 不是通用 faker，而是精确服务于演示剧本的数据集；
  通用 faker 留到打磨期（10-17~23）再补。
- 写操作的真实副作用（真实改派）只在 `RealTMSAdapter` 里发生；mock 的写操作只记日志，
  演示安全。

## 参考方案

- `dominicfinn/open_tms` 的 shipment / carrier / operational issue 领域划分**思路**
 （本仓库的 struct 独立设计，未复制代码）。

## TODO

- [ ] `TMSClient` / `WeatherClient` interface 落代码
- [ ] `MockTMS` 实现 + demo.json 数据集
- [ ] 配置开关 `PLATFORM=mock|real`（env）
- [ ] 真实接口就绪后：`RealTMSAdapter` 实现（新文件，不动旧代码）
