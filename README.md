# waybill-guardian 运单守护

异常运单自治处置 Agent —— 「AI 重构产业 · 架构师大赛」AI+物流赛道参赛项目。

延误 / 破损 / 丢件发生后，Agent 自动拉取运单、轨迹、司机、天气做归因，
生成改派 / 赔付 / 通知方案；**所有写操作必须经过人工确认**，确认后回写 TMS，全程审计可回放。

- 架构与设计决策：见 [AGENTS.md](./AGENTS.md)
- 工具契约：见 [contract.yaml](./contract.yaml)
- 第三方来源声明：见 [THIRD_PARTY_NOTICES.md](./THIRD_PARTY_NOTICES.md)

## 快速开始（施工中）

```bash
# 后端
go run ./cmd/server

# 前端
cd web && npm install && npm run dev
```

演示剧本：杭州 → 成都运单 YD2026101001 延误处置（详见 AGENTS.md）。
