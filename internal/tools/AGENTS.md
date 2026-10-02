# internal/tools · AGENTS.md

## 职责

`contract.yaml` 中 7 个工具的 **mock 实现** + 确定性演示数据集。
agent 只依赖 `internal/platform` 的接口，本目录是"工具外壳"（参数校验 → 调 platform →
结果整形），未来真实 adapter 上线时外壳不动。

## 工具清单

| 工具 | 读/写 | mock 数据来源 |
|---|---|---|
| `tms.get_waybill` | 读 | 演示运单 YD2026101001（杭州→成都，时效 72h） |
| `tms.get_tracking` | 读 | 轨迹点序列：杭州出发 → 绵阳段停留 6h（异常点）→ … |
| `tms.get_driver` | 读 | 司机：连续驾驶 9h，疲劳预警 |
| `ext.get_road_weather` | 读 | 成绵高速段：晴，无预警（用于排除天气因素，体现归因严谨） |
| `tms.reassign` | 写 | mock：返回改派单号；需审批 + 幂等键 |
| `tms.create_claim` | 写 | mock：返回赔付单号；需审批 + 幂等键 |
| `notify.send_sms` | 写 | mock：只记日志不真发；需审批 |

## 设计决策

- **确定性数据**：同一输入永远返回同一输出，demo 可排练、评委可复现；随机性只允许出现在
  "候选运力二选一"的排序展示层，不影响主干剧本。
- **归因证据链完整**：天气工具返回"无预警"是有意设计 —— 演示时 agent 能说出"已排除天气因素"，
  这是"归因严谨"的答辩素材。
- 参数校验在外壳做（waybill_id 格式、carrier_id 非空），业务校验在 platform 做。

## 参考方案

- `dominicfinn/open_tms` 的 shipment / operational issue 字段与状态设计**思路**
 （本仓库 schema 独立编写，未复制代码）。

## TODO

- [ ] 7 个工具的 Go struct（入参 / 出参）定义
- [ ] 演示数据集 JSON 落盘（`internal/tools/testdata/demo.json`）
- [ ] 每个工具的单测：参数校验 + 确定性断言
