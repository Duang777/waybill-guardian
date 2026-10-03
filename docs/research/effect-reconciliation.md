# 外部写入超时后的幂等与对账语义

> 研究日期：2026-10-03
> 范围：请求已经发出、客户端未收到确定结果时，外部副作用的生产语义

## 结论摘要

1. **超时不是失败，而是结果未知。** 请求可能未到达，也可能已提交但响应丢失。AWS 直接把
   “网络超时后不清楚实例是否已创建”作为幂等 API 的核心问题；Stripe 也要求把 `500` 结果视为
   不确定，因为它仍可能产生用户可见副作用。
   来源：[AWS Builders' Library](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/)，
   [Stripe advanced error handling](https://docs.stripe.com/error-low-level#server-errors)。
2. **幂等键标识一次业务意图，不标识一次网络尝试。** 同一意图的所有重试必须使用同一个 key
   和相同参数；修改参数或发起新的业务意图必须使用新 key。Stripe 和 EC2 都会拒绝同 key、不同
   参数的请求。
   来源：[Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests)，
   [EC2 idempotency](https://docs.aws.amazon.com/ec2/latest/devguide/ec2-api-idempotency.html)。
3. **幂等不等于结果已知，也不等于最终成功。** Stripe 会缓存首次执行的状态码和响应体，包括
   `500`；同 key 重放可能只会再次返回原 `500`。因此生产系统仍需要状态查询、事件通知或人工
   对账。
   来源：[Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests)，
   [Stripe advanced error handling](https://docs.stripe.com/error-low-level#server-errors)。
4. **安全重试取决于下游契约和有效期。** Stripe 的 key 至少保留 24 小时，清理后复用会被当作
   新请求；不同 AWS API 也有各自的作用域和 TTL。超过保证窗口后，不得盲目重放写请求。
   来源：[Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests)，
   [EC2 idempotency](https://docs.aws.amazon.com/ec2/latest/devguide/ec2-api-idempotency.html)。

## 1. 幂等键的生产契约

推荐把每个外部 effect 持久化为一条业务意图，并在首次发送前保存：

```text
effect_id
provider + operation + tenant/scope
idempotency_key
canonical_request_hash
key_created_at + key_expires_at
status + attempt
external_reference
last_response_digest + next_reconcile_at
```

key 应由服务端生成并保持稳定。不要只从请求参数推导 key：相同参数可能代表两次真实意图，
AWS 因此优先采用调用方提供的唯一 request ID；服务端应把“记录 token”和“执行 mutation”做成
原子、持久操作。
来源：[AWS Builders' Library: Reducing client complexity](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/#reducing-client-complexity)。

每次发送前校验 `provider + operation + scope + canonical_request_hash`。同 key、不同请求必须
停止并报警，不能覆盖旧记录。Stripe 会比较原请求参数，EC2 返回
`IdempotentParameterMismatch`。
来源：[Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests)，
[EC2 idempotency](https://docs.aws.amazon.com/ec2/latest/devguide/ec2-api-idempotency.html#client-tokens)。

下游的语义可能是“缓存首次原始响应”，也可能是“返回语义等价的当前状态”。AWS 明确区分
语义等价与字节完全相同，EC2 重放可返回更新后的资源状态；本地 adapter 必须声明采用哪一种。
来源：[AWS Builders' Library: Retries and semantic equivalence](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/#retries-and-semantic-equivalence)，
[EC2 idempotency](https://docs.aws.amazon.com/ec2/latest/devguide/ec2-api-idempotency.html)。

## 2. 结果分类

分类依据必须是可验证的 provider 证据，而不是异常类型的名字。

| 观察 | effect 状态 | 后续动作 |
|---|---|---|
| adapter 能证明请求未离开本进程或未被下游接受 | `retryable_failed` | 同 key、同参数重试 |
| 收到并校验了成功响应 | `succeeded` | 保存 external reference 和响应摘要 |
| 收到明确的参数、权限或业务拒绝，且 provider 保证未执行 | `permanent_failed` | 不重试；修正后作为新意图、新 key |
| 发送后超时、连接重置、响应无法解析、进程在落本地结果前崩溃 | `unknown` | 进入对账，不改 key |
| generic `5xx` | 默认 `unknown` | 仅按该 provider 的明确契约处理 |
| 查询确认成功或终态拒绝 | `succeeded` / `permanent_failed` | 落终态证据 |
| 查询明确证明从未执行，且仍在 key 有效期内 | `retryable_failed` | 同 key、同参数重试 |
| 查询暂时未找到，或查询面存在最终一致性 | 保持 `unknown` | 退避后继续查询 |
| key 已过期且仍无权威结论 | `manual_review` | 禁止自动写重放 |

超时与连接错误之所以进入 `unknown`，是因为客户端不知道服务端是否收到请求；Stripe 对网络错误
的建议是使用相同 key 和相同参数重试，直到取得确定响应。
来源：[Stripe advanced error handling](https://docs.stripe.com/error-low-level#network-errors)。

不能把所有 `5xx` 统一归为“确定未执行”。Stripe 明确说明 `500` 可能已经产生副作用，并建议
结合 webhook 和本地业务标识做交叉核对。
来源：[Stripe advanced error handling](https://docs.stripe.com/error-low-level#server-errors)。

状态查询的“未找到”也不必然等于“未执行”。例如 EC2 的读 API 是最终一致的，刚完成的 mutation
可能尚未出现在查询结果中；查询必须遵守 provider 公布的一致性窗口。
来源：[EC2 DescribeInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeInstances.html)。

## 3. 对账流程

```text
prepared -> dispatching -> succeeded
                       |-> retryable_failed -> dispatching
                       |-> permanent_failed
                       `-> unknown -> reconciling -> succeeded
                                                |-> permanent_failed
                                                |-> retryable_failed
                                                `-> manual_review
```

1. `unknown` 后冻结原始 key 和参数，不创建“补偿性新 key”。
   来源：[Stripe advanced error handling](https://docs.stripe.com/error-low-level#network-errors)。
2. 优先调用按 idempotency key 查询的状态接口；否则按 external reference、业务对象上的
   本地标识或 provider webhook 对账。AWS 展示了按 `client-token` 查询已创建资源，Stripe
   建议把本地标识写入对象 metadata 后与 webhook 交叉核对。
   来源：[EC2 DescribeInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeInstances.html)，
   [Stripe advanced error handling](https://docs.stripe.com/error-low-level#server-errors)。
3. 查询结果必须同时核对租户、operation、目标对象和请求哈希，避免把其他流程创建的相似资源
   误认成本 effect。AWS 建议把 client request ID 记录到资源和审计日志中，以区分资源来源。
   来源：[AWS Builders' Library: Reducing client complexity](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/#reducing-client-complexity)。
4. 若 provider 只支持同 key 重放，只有在其契约保证重放不会再次执行、且 key 仍有效时，重放
   才可兼作对账。Stripe 的首次 `500` 会被缓存，因此这种 provider 还需要额外状态通道。
   来源：[Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests)，
   [Stripe advanced error handling](https://docs.stripe.com/error-low-level#server-errors)。
5. 达到查询期限、超过 key TTL、provider 无法证明“未执行”，或证据互相冲突时转人工处理；
   不能把 `unknown` 降级成普通失败。
   来源：[Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests)，
   [AWS Builders' Library: Retrying and side effects](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/#retrying-and-side-effects)。

## 4. 安全重试规则

- 自动重试只适用于重复执行不会造成非预期状态变化的请求。Google AIP-194 要求不要自动重试
  会产生非预期状态变化的请求；事务失败应从完整事务边界重试，而不是只重试其中一个 RPC。
  来源：[Google AIP-194](https://google.aip.dev/194)。
- 对具有明确幂等契约的 unary 请求，`UNAVAILABLE` 可重试；`DEADLINE_EXCEEDED` 表示调用方
  deadline 必须被遵守，不应在同一自动重试循环里继续。之后由应用层把写 effect 标记为
  `unknown` 并对账。
  来源：[Google AIP-194: Retryable codes](https://google.aip.dev/194#retryable-codes)，
  [Non-retryable codes](https://google.aip.dev/194#non-retryable-codes)。
- `UNKNOWN` 不应自动重试；`ABORTED` 应在更高层重试整个事务。
  来源：[Google AIP-194: Generally non-retryable codes](https://google.aip.dev/194#generally-non-retryable-codes)。
- 重试使用 capped exponential backoff、jitter 和总尝试/总时长预算，并只在调用链的一层负责，
  避免多层重试相乘和故障时放大下游负载。
  来源：[AWS Builders' Library: Timeouts, retries, and backoff with jitter](https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/)。
- 相同意图始终使用原 key 和完全相同的 canonical request。参数变化表示新意图；旧 effect
  先进入明确终态，再以新 key 发起。
- 自动重试预算必须短于下游 key 的有效期。Stripe 清理至少 24 小时前的 key 后会把复用视为
  新请求，因此过期 key 只能先查询或人工处理。
  来源：[Stripe idempotent requests](https://docs.stripe.com/api/idempotent_requests)。

## 5. Durable workflow 的边界

Durable workflow 不会把外部写入变成 exactly-once。Temporal 说明：Activity 可能已成功执行，
但 worker 在上报前崩溃，随后 Activity 会再次执行；幂等键由 Activity 调用的外部服务执行去重，
而不是由 Temporal 本身执行。稳定 key 可由 workflow run ID 与 activity ID 组合，使其在
Activity 重试间不变。
来源：[Temporal Activity Definition: Idempotency](https://docs.temporal.io/activity-definition#idempotency)。

因此，无论使用数据库 worker 还是 Temporal，外部 mutation 仍需相同的下游幂等、结果分类和
对账契约；workflow runtime 只负责持久调度与重试。

## 6. Adapter 上线门槛

真实写 adapter 必须逐项明确并通过 contract test：

- key 的作用域、最大长度、保留时长，以及过期后行为；
- 同 key、同参数是否不重复执行，并返回原结果还是当前语义等价结果；
- 同 key、不同参数是否返回可识别冲突；
- 哪些响应能证明成功、永久失败或确定未执行；
- 超时、断连、不可解析响应和每类 `5xx` 的分类；
- 是否支持按 key 查询，或是否有可关联的 webhook / external reference；
- 查询一致性窗口、终态集合和“未找到”的准确含义；
- 并发同 key 请求、响应丢失、成功后本地崩溃、key 临界过期的行为。

若缺少稳定幂等键，或在未知结果后既不能查询也不能安全重放，adapter 不具备无人值守生产写入
条件，只能用于只读、sandbox 或人工执行。
来源：[AWS Builders' Library](https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/)，
[Google AIP-194](https://google.aip.dev/194)。
