# web · AGENTS.md

## 职责

演示前端。目标：**3 分钟内让评委看懂"异常 → 归因 → 审批 → 回写"全过程**。
技术：React + 高德 JS API（国内瓦片稳定；高德 key 放 `.env`，不提交）。

## 页面结构

1. **异常运单地图**（高德）：轨迹线 + 异常点（绵阳停留段）高亮；点击轨迹点显示时间/速度。
2. **Agent 审计时间线**：按 `audit` 事件流渲染；支持回放（播放 / 暂停 / 进度条）。
3. **人工审批卡片**：展示处置方案 + 归因证据链 + 确认 / 驳回按钮；确认后卡片变为"已执行"。
4. **风险评分条**：ETA 延误 / 路况 / 天气三段式 0-100 分（装饰性，演示第一印象）。

## 设计决策

- **地图用高德**：演示在国内，Leaflet + CARTO 瓦片可能慢；高德 JS API 稳定。
  （`MapView.jsx` 的 Leaflet 实现思路可借鉴，API 调用按高德重写。）
- **时间线是审计的投影**：不维护独立状态，直接消费 `GET /api/runs/:id/timeline` SSE；
  断线用 `Last-Event-ID` 续传（`cmd/server` 配合）。
- **审批卡片信息密度优先**：证据链摘要必须在一屏内看完，评委没耐心翻页。

## 参考与借鉴（思路，未复制代码）

- `jattiphrswan/logistics-tracker`：
  - `backend/simulator.js` → 轨迹模拟与定时推送的**思路**（本仓库用 Go mock + SSE 实现）
  - `MapView.jsx` → 地图组件拆分**思路**
  - `AlertFeed.jsx` → 改造为审计时间线的**思路**
  - `PlaybackPanel.jsx` → 回放控制条的**思路**
- `09karankr/port-logistics-intelligence` → 风险评分三段式展示的**思路**

## TODO

- [ ] 高德 JS API 接入（key 进 `.env.local`，`.gitignore` 已覆盖）
- [ ] 四个页面组件骨架 + mock 数据先跑通静态展示
- [ ] SSE 接入：时间线实时追加、审批卡片实时弹出
- [ ] 回放控制条（播放 / 暂停 / 进度）
