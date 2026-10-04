# 在线模型与证据引用设计

## 问题

在线模型已经能调用 Responses API 或 OpenAI-compatible Chat Completions API，
但当前演示默认使用 `ScenarioModel`。Agent 暂停后，`guardian.Service` 还会重新读取平台数据，
再用固定规则生成归因和审批证据。因此，审批卡展示的内容不是模型对当次工具结果的结构化判断，
也不能定位到对应的审计字段。

本设计让模型提案进入审批，同时保留以下约束：

- `contract.yaml` 中的七个业务工具保持不变。
- 写操作继续使用 hastekit pause/resume、人工审批和服务端 effect 身份。
- 模型只能引用同一 run 中已审计的成功读工具结果。
- 旧 JSONL、旧 PostgreSQL 记录和旧 `{label,value}` 证据继续可读。
- 离线模式继续用于 CI 和无凭据演示，并在界面中明确标识。

## 调用方式

正式演示显式配置在线模型：

```bash
AGENT_MODE=online \
LLM_API_STYLE=chat_completions \
LLM_BASE_URL=https://api.example.com/v1 \
LLM_API_KEY="$LLM_API_KEY" \
LLM_MODEL=model-name \
LLM_REQUEST_TIMEOUT=45s \
LLM_MAX_OUTPUT_TOKENS=4096 \
./scripts/demo.sh
```

无模型凭据时显式使用离线模式：

```bash
AGENT_MODE=offline ./scripts/demo.sh
```

`AGENT_MODE=demo` 作为 `offline` 的兼容别名保留。服务把归一化后的模式写入
`run_started`。前端只显示审计中记录的模式，不根据当前进程配置猜测。

`guardian.Service` 只接收类型化结果：

```go
type Outcome interface {
	isOutcome()
}

type Completed struct {
	SDKRunID string
	Text     string
}

type Paused struct {
	SDKRunID  string
	Proposal  proposal.Accepted
	Interrupts []Interrupt
}
```

`Paused.Proposal` 已通过结构校验和证据解析。`guardian.Service` 不解析模型 JSON，
也不重新读取平台来生成审批归因。

## 提案协议

模型在发出写工具调用的同一响应中返回一个 JSON 对象：

```json
{
  "schema_version": "proposal.v1",
  "summary": "司机疲劳预警和异常停留共同造成延误风险。",
  "confidence_bps": 8600,
  "attribution": [
    {
      "factor": "司机连续驾驶时间过长",
      "confidence_bps": 9100,
      "evidence_refs": [
        {
          "tool_call_id": "call_driver",
          "field_path": "/continuous_drive_hours",
          "quoted_value": 9
        }
      ]
    }
  ],
  "alternatives": [
    {
      "carrier_id": "CARRIER-1",
      "reason": "时效和历史履约率更优"
    }
  ],
  "expected_impact": {
    "eta_saved_min": {
      "availability": "unavailable",
      "reason": "当前证据没有改派后的到达时间"
    },
    "cost_delta_cny": {
      "availability": "unavailable",
      "reason": "当前证据没有成本字段"
    }
  }
}
```

当前工具结果没有成本数据，也没有改派后的权威到达时间。模型必须把对应指标标为
`unavailable`。只有后续工具契约提供可引用字段后，服务端才允许 `available`。

模型继续通过现有写工具提交执行参数。归因、备选方案和影响估算不会进入写工具参数，
因此不会改变参数哈希、effect ID 或幂等键。

## 模型边界

`internal/agent` 增加 `ProposalBoundary`。该 middleware 包装每次模型调用：

1. 使用 `agents.WithModelStreamTransform` 抑制 provider chunk。
2. 调用现有 provider retry。
3. 记录一次逻辑模型调用的时延和 token 用量。
4. 读工具响应直接返回。
5. 写工具响应必须包含一个合法提案。
6. 首次校验失败时，用原请求、无工具调用的失败提案文本和受限错误码构造一次修复请求。
7. 第二次失败时返回 `proposal.ErrReviewRequired`。
8. 校验成功后，把提案规范化，再交给 hastekit 保存 history 和 pending writes。

抑制 provider chunk 是必要条件。hastekit v0.0.24 在首个 chunk 发布后把调用标记为 committed，
此后 middleware 不能用修复结果替换响应。产品进度仍由业务审计 SSE 提供。

修复请求不能带入第一次响应的 function-call items。否则 Chat Completions provider 会看到
没有对应 tool result 的 assistant tool calls，并拒绝请求。

middleware 顺序如下：

```text
historyGuard
  ModelRequestBudget
    ProposalBoundary
      Retry
        CapabilityModelMiddleware
          historyGuard
            provider
```

`ModelRequestBudget` 在 `ProposalBoundary` 外层。首次调用、一次结构修复和各自的 provider
retry 共用默认 45 秒的 deadline。它复制请求并把每次 provider 输出限制为默认 4096 token。
`ProposalBoundary` 在 `Retry` 外层，因此结构修复不会被当成 provider 重试。assistant 文本
最多拼接 32 KiB，修复请求最多带入 4 KiB 失败文本。

## 证据账本

`AuditMiddleware` 负责产生模型和业务共同使用的证据：

1. 读工具返回类型化结果。
2. handler 限制文本字段和数组数量，拒绝超长外部 ID。
3. middleware 应用现有脱敏规则并生成 canonical JSON。
4. middleware 追加 `tool_result`。
5. middleware 从已提交事件中取回 `result`。
6. middleware 把相同 JSON 返回给模型。

因此，模型看到的值和哈希链中的值来自同一份字节。
system prompt 把所有工具文本定义为不可信业务数据，禁止模型执行字段中的命令或角色声明。

`internal/proposal.Compiler` 只接受以下引用：

- `tool_call_id` 属于当前 run。
- 对应事件是成功的读工具 `tool_result`。
- 同一 run 中该 `call_id` 唯一。
- `field_path` 是合法的 RFC 6901 JSON Pointer。
- 路径位于事件的 `result` 下，并解析为标量。
- `quoted_value` 的 canonical JSON 与审计值相同。

编译后的引用由服务端补充来源：

```go
type Citation struct {
	ToolCallID     string          `json:"tool_call_id"`
	FieldPath     JSONPointer     `json:"field_path"`
	Value         json.RawMessage `json:"value"`
	DisplayValue  string          `json:"display_value"`
	SourceEventID string          `json:"source_event_id"`
	SourceSeq     audit.Seq       `json:"source_seq"`
	SourceHash    string          `json:"source_hash"`
}
```

前端使用 `DisplayValue`。前端不重新解析完整工具结果，也不展示未引用字段。

## 持久化和审批

hastekit 先保存 paused checkpoint。随后 Guardian 生成服务端 effect 身份，并追加
`proposal_prepared`。该事件包含：

- 已接受的提案和摘要。
- 已解析的证据引用。
- SDK run ID 和 plan version。
- 规范化后的写调用集合及其摘要。
- 稳定 proposal digest。

Guardian 随后创建 `approval_requested`。审批记录的 `Items` 仍是唯一可执行内容。
新审批通过 `proposal_ref` 关联解释信息；旧审批继续使用 `reason` 和 `{label,value}`。

如果相同 `proposal_prepared` event ID 已存在，调用方必须比较 proposal digest、SDK run ID、
plan version 和写调用集合。内容不同表示 checkpoint 冲突，系统不能把旧事件当成成功重放。

PostgreSQL 只投影 proposal 和引用。审计日志仍是权威数据。

## 恢复

恢复过程不会重新调用模型生成已经接受的提案。

| 崩溃位置 | 恢复行为 |
|---|---|
| `proposal_prepared` 之前 | 进入 `review_required`，不重新生成提案 |
| `proposal_prepared` 之后、`approval_requested` 之前 | 从 checkpoint 重建同一审批 |
| `approval_requested` 之后 | 使用现有审批恢复逻辑 |
| 第二次提案校验失败 | 进入 `review_required`，不创建审批 |

`review_required` 是独立终态。`manual_review` 继续只表示审计或恢复隔离。前端可以查看
`review_required` 的完整可信时间线。

旧 run 没有 inference descriptor 或 proposal checkpoint 时，继续按旧恢复规则处理。

## 模型审计

每个逻辑模型调用产生 `model_call_started` 和 `model_call_finished`：

```go
type ModelCallFinished struct {
	CallID        string      `json:"call_id"`
	Mode          string      `json:"mode"`
	Model         string      `json:"model,omitempty"`
	APIStyle      string      `json:"api_style,omitempty"`
	LoopIteration int         `json:"loop_iteration"`
	Candidate     string      `json:"candidate"`
	LatencyMS     int64       `json:"latency_ms"`
	Usage         *TokenUsage `json:"usage,omitempty"`
	Outcome       string      `json:"outcome"`
	IssueCodes    []string    `json:"issue_codes,omitempty"`
}
```

审计不记录 prompt、响应正文、API key、base URL、header 或 provider 参数。provider 未返回
usage 时省略该字段，不能把未知用量记成零。

## 前端

审批证据保留兼容联合类型：

```ts
type Evidence =
  | { label: string; value: string }
  | {
      label: string;
      value: string;
      source: {
        tool_call_id: string;
        field_path: string;
        source_seq: number;
      };
    };
```

带来源的证据显示为按钮。按钮跳到 `tool_result` 的时间线行，并显示字段路径和值。旧证据继续
显示纯文本。时间线标题显示“在线推理”“离线回放”或“历史运行，模式未记录”。

## 模块

| 路径 | 职责 |
|---|---|
| `internal/proposal/` | 严格解码、引用解析、影响字段规则、digest 和 checkpoint |
| `internal/agent/proposal_middleware.go` | Hastekit response 适配、一次修复和流抑制 |
| `internal/agent/model_audit_middleware.go` | 模型 started/finished 审计 |
| `internal/agent/engine.go` | 类型化 outcome 和模式描述 |
| `internal/agent/middleware.go` | 让模型读取已审计的 canonical 结果 |
| `internal/guardian/service.go` | 创建 checkpoint 和审批，处理 `review_required` |
| `internal/approval/approval.go` | 兼容旧证据和新 `proposal_ref` |
| `internal/storage/postgres/` | 新事件和状态的可重建投影 |
| `web/src/api.ts` | 旧证据和新证据的 Zod 联合类型 |
| `web/src/components/ApprovalPanel.tsx` | 提案、备选方案和引用按钮 |
| `web/src/components/TimelinePanel.tsx` | 审计事件锚点和引用字段显示 |

## 取舍

- 接受不展示 provider token 流，以换取可替换的一次结构修复。
- 接受在早期崩溃窗口转人工，以避免重新生成不同提案。
- 接受新增 proposal checkpoint，以便在暂停后确定性重建审批。
- 接受标量引用限制，以保证值比较、界面展示和隐私检查可确定执行。
- 接受 `review_required` 数据库迁移，以保持 `manual_review` 的隔离语义。

不采用额外的 `guardian_submit_proposal` 工具。该工具会增加第二次 pause/resume、动态工具阶段和
定向 resolution。也不把提案字段加入写工具参数。

## 验证

实现必须证明：

- 七个工具名称和三个写参数 schema 没有变化。
- 三张不同运单通过同一 fake compatible server 进入审批。
- Responses 和 Chat Completions 两条路径都通过。
- 首次非法提案只修复一次，第二次失败进入 `review_required`。
- 跨 run、写工具、失败结果、重复 call ID、缺失路径、非标量和值不一致的引用全部拒绝。
- 模型审计包含模式、模型、API style、loop、时延和可选 usage，且不含敏感请求内容。
- `proposal_prepared` 后重启能创建同一审批。
- 旧 JSONL 和旧 PostgreSQL 记录原样回放。
- 前端引用定位到准确事件，并在 375px 和 1600x900 下可用。

## 下一步

先实现 `internal/proposal` 的严格解码和审计引用解析，再接入模型 middleware。
