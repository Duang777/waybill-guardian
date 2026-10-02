# internal/idempotency · AGENTS.md

## 职责

写操作的幂等保障。物流写操作（改派、赔付）一旦重复执行就是真实资损，
而 agent 场景天然多重试（模型重试、fallback 切换、人工重复点击确认），
因此幂等不能靠"调用方自觉"，必须在 middleware 层强制。

## Key 生成规则

```
key = sha256(action + "|" + waybill_id + "|" + business_window)
```

- `business_window`：业务时间窗（如运单异常事件 id），同一异常的重复处置请求视为同一操作。
- agent 生成 key 并随工具参数传入；middleware 校验缺失则直接拒绝（fail fast，
  防 agent 漏传）。

## 去重语义

- 首次执行：执行写操作，存储 `key → result`。
- 重复请求（同 key）：**不重新执行**，直接返回首次结果，并在审计中记 `duplicate_suppressed` 事件。
- 存储：初期内存 map + 启动加载 JSONL；后续迁 SQLite（`UNIQUE(key)` 约束）。

## 设计决策

- **放在 middleware 而不是每个 tool 里**：防漏 —— 新增写工具时不可能忘记；
  校验逻辑与业务逻辑解耦。
- **key 由 agent 生成、middleware 强制校验**：既让 agent 感知幂等（prompt 里要求），
  又不信任 agent（缺键拦截）。
- 前端"确认"按钮的重复点击：走同一 key，后端去重，前端再加一次 disabled，双保险
 （`cmd/server` TODO 联动）。

## TODO

- [ ] middleware 实现：缺键拦截、同键去重、结果回放
- [ ] 并发测试：同一 key 并发 10 次只执行一次
- [ ] key 生成函数单测（同一异常 → 同 key；不同异常 → 不同 key）
