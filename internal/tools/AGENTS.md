# internal/tools · AGENTS.md

## 职责

本目录把 `contract.yaml` 中的七个工具定义成 Go 输入和输出类型，并注册为 hastekit tools。
handler 校验参数，调用 `internal/platform` 接口，再返回 typed result。确定性演示数据也放在
本目录。

## 工具清单

| 契约名 | wire name | 读写 | 演示行为 |
|---|---|---|---|
| `tms.get_waybill` | `tms_get_waybill` | 读 | 返回运单 `YD2026101001` |
| `tms.get_tracking` | `tms_get_tracking` | 读 | 返回含绵阳北 6 小时停留的轨迹 |
| `tms.get_driver` | `tms_get_driver` | 读 | 返回连续驾驶 9 小时和疲劳预警 |
| `ext.get_road_weather` | `ext_get_road_weather` | 读 | 返回晴天和无预警，用于排除天气因素 |
| `tms.reassign` | `tms_reassign` | 写 | 返回稳定改派单号 |
| `tms.create_claim` | `tms_create_claim` | 写 | 返回稳定赔付单号 |
| `notify.send_sms` | `notify_send_sms` | 写 | 返回 mock 回执，不发送短信 |

模型 wire name 使用下划线，以兼容函数名不接受点的 provider。tool metadata 保存原契约名和
`read` 或 `write` 类型。

## 注册规则

- 四个读工具设置 `ReadOnlyHint`，不需要审批。
- 三个写工具设置 `RequiresApproval` 和 `IdempotentHint`。
- 每个写工具 schema 都包含 `idempotency_key`。
- `waybill_id` 必须匹配 `YD` 加十位数字。
- handler 校验空字符串和缺失对象。业务约束由 platform 实现。

## 演示数据

`testdata/demo.json` 通过 `go:embed` 编入服务。它包含一张杭州到成都运单、五个轨迹点、
一名司机、沿途天气和两个候选承运商。数据不使用随机数，因此相同输入产生相同输出。

## 验证

`registry_test.go` 解析根目录 `contract.yaml`，比较七个工具的契约名、字段、读写类型和
审批标志。测试还覆盖参数校验、确定性结果和两个候选承运商。
