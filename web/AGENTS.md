# web · AGENTS.md

## 职责

React 运营控制台。目标是在三分钟内显示异常、归因、审批、平台写入和审计回放。
页面不持有业务权威状态，所有 run 和审批状态都从 HTTP 响应及 SSE 事件投影。

## 页面结构

1. 经营总览 `/`：显示 4 项主 KPI、全国港网、风险队列和带统计引用的只读经营简报。
2. 批量处置：从风险队列选择运单后调用 `/api/runs:batch`，再按 run 打开 SSE。
3. 单运单工作台 `/waybills/:id`：显示运单摘要、轨迹、审计时间线和人工审批卡片。

## 设计决策

- 高德 key 存在时加载高德 JS API。key 缺失或 SDK 加载失败时，`RouteMap` 显示本地坐标轨迹，
  其他功能继续工作。
- `timelineReducer` 是页面状态的唯一入口。它按 `seq` 排序并忽略重复事件。
- 浏览器原生 `EventSource` 负责重连和 `Last-Event-ID`。服务端按该游标补齐事件。
- 目录、运单详情和 run 分别建模。启动后以响应中的 `waybill_id` 为权威值。
- 切换运单或 run 会中止旧请求并递增选择代次，旧响应不能覆盖新页面。
- 启动恢复优先选择待审批 run，其次选择其他活跃 run；页面恢复不会隐式创建 run。
- 审批操作进行时禁用按钮。驳回必须先填写原因。
- API 响应通过 Zod 在边界解析。结构不符合契约时，页面显示错误。
- 经营简报只展示 `/api/overview` 返回的聚合事实和引用，前端不直接调用模型，也不触发写操作。
- 页面在 375 和 320 像素宽度下没有横向溢出，交互控件至少为 40 像素。

## 配置

- `VITE_API_TARGET`：Vite 开发代理的 API 地址，默认 `http://127.0.0.1:8080`。
- `VITE_AMAP_KEY`：可选的高德浏览器端 key。
- `VITE_AMAP_SECURITY_JS_CODE`：使用新版高德安全配置时填写。

将高德配置写入 `web/.env.local`。该文件不提交。

## 验证

`npm test` 覆盖 API 边界、恢复优先级、时间线去重、审批投影、run 状态和回放 reducer。
`npm run verify:e2e`
使用 Chrome 连续确认三次演示，并验证一次驳回路径。脚本保存 desktop 和 mobile 截图，
检查横向溢出、跳转链接和按钮尺寸。

`npm run verify:file-e2e` 使用双运单 CSV 启动 `PLATFORM=file`，验证目录切换、旧响应抑制、
动态路线和异常标签，以及所选运单的完整审批执行。

`npm run verify:overview` 使用 72 港仿真数据验证 KPI、港网 SVG、5 个独立 run 与审批、
单独确认不影响其余 4 个审批、地图下钻、轨迹异常点和三档响应式布局。

`npm run record:demo` 启动隔离服务，录制确认、回放和驳回路径，再用 ffmpeg 生成带中文字幕的
1600×900 MP4。录制文件写入已忽略的 `web/artifacts/`。

参考项目只用于交互思路，本仓库没有复制其源码。登记见根目录
`THIRD_PARTY_NOTICES.md`。
