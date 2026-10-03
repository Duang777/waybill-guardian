# CloudEvents 入站与 outbox 发布设计

- 状态：已接受
- 对应 Issue：[#39](https://github.com/Duang777/waybill-guardian/issues/39)
- 依赖：[#16](https://github.com/Duang777/waybill-guardian/issues/16)、[#35](https://github.com/Duang777/waybill-guardian/issues/35)
- 设计基线：`docs/RFC-002.md`

## 目标

本设计实现一条可恢复的异常事件链路：

```text
认证并校验 CloudEvent
  -> inbox 去重
  -> 重算 incident 运输事实投影
  -> 同事务写入 outbox
  -> dispatcher 至少一次发布
```

数据库提交前不向下游发布。数据库提交后即使 HTTP 响应丢失，生产者也能用相同
`source + id` 重试并获得首次提交的结果。dispatcher 发布成功但标记失败时，会使用相同
CloudEvent 身份重发。消费者必须在自己的状态更新事务中按 `source + id` 去重。

默认 JSONL 演示不注册事件入口，也不启动 dispatcher 或指标监听器。

## HTTP 和身份边界

PostgreSQL 模式注册：

```text
POST /v1/events
Content-Type: application/cloudevents+json
```

入口沿用现有认证 middleware。JWT 必须包含：

- `tenant_id`，且等于进程配置的 `TENANT_ID`。
- `event_producer` 角色。
- 非空且无重复的 `event_sources`，最多 16 项。
- 非空且无重复的 `event_types`，最多 8 项。
- 现有 `waybill_all` 或 `waybill_ids` 运单范围。

来源和事件类型采用精确匹配，不支持通配符。租户只来自已验证 JWT 和绑定租户的
PostgreSQL repository，事件体不能选择租户。

本地认证仅允许来源 `urn:waybill-guardian:local-producer` 和本文定义的两种事件类型。

## CloudEvents profile

入口只接受 CloudEvents 1.0 structured JSON。请求体上限为 1 MiB。解析器拒绝：

- 无效 UTF-8、重复 JSON key、尾随 JSON 值和未知字段。
- CloudEvents 扩展字段和 `data_base64`。
- 除 `charset=utf-8` 外的 media type 参数。
- 不受支持的 `type + dataschema` 组合。

信封必须包含：

| 字段 | 规则 |
|---|---|
| `specversion` | 等于 `1.0` |
| `id` | 1 到 128 个 `[A-Za-z0-9._:-]` 字符 |
| `source` | 1 到 256 字节的绝对 URI，不含 fragment |
| `type` | 本文定义的两种事件之一 |
| `subject` | 等于 `waybill/{data.waybill_id}` |
| `time` | RFC3339Nano，归一化后等于 `data.record_time` |
| `datacontenttype` | 等于 `application/json` |
| `dataschema` | 与 `type` 精确匹配 |
| `data` | 对应 schema 的 JSON object |

`record_time` 最多比服务端当前时间晚五分钟。历史事件没有额外的最早时间限制。
`event_time` 不能晚于 `record_time`。两个时间字段都不参与业务排序。

### 延误事件

```text
type       = com.waybill.tracking.delay.detected.v1
dataschema = urn:waybill-guardian:schema:delay-detected:v1
```

```json
{
  "waybill_id": "YD2026101001",
  "incident_key": "delay-20261010-000184",
  "source_version": 41,
  "event_time": "2026-10-10T12:28:31Z",
  "record_time": "2026-10-10T12:30:00Z",
  "location": {
    "code": "MY-N-SERVICE",
    "name": "Mianyang North Service Area"
  },
  "business_step": "transporting",
  "reason_code": "stop_duration_exceeded",
  "observations": {
    "stop_minutes": 360
  }
}
```

`incident_key` 是来源稳定的异常编号，长度为 1 到 128 个 ASCII 字节。
`source_version` 是 1 到 `math.MaxInt64` 的整数。首版只接受
`business_step=transporting`、`reason_code=stop_duration_exceeded`，以及 1 到 10080
分钟的停留时长。

### 修正事件

```text
type       = com.waybill.tracking.delay.corrected.v1
dataschema = urn:waybill-guardian:schema:delay-corrected:v1
```

```json
{
  "waybill_id": "YD2026101001",
  "incident_key": "delay-20261010-000184",
  "source_version": 42,
  "event_time": "2026-10-10T12:28:31Z",
  "record_time": "2026-10-10T12:45:00Z",
  "corrects": {
    "source": "urn:tms:region-east",
    "id": "evt-20261010-000184"
  },
  "operation": "replace",
  "replacement": {
    "location": {
      "code": "MY-S-SERVICE",
      "name": "Mianyang South Service Area"
    },
    "business_step": "transporting",
    "reason_code": "stop_duration_exceeded",
    "observations": {
      "stop_minutes": 180
    }
  },
  "reason": "GPS point was assigned to the wrong service area"
}
```

`operation=replace` 必须提供完整 replacement，`operation=retract` 禁止提供
replacement。`reason` 必填，最长 512 个 UTF-8 字节。`corrects.source` 必须等于信封
source，修正事件不能引用自身。

## 事件身份和回放

inbox 身份为：

```text
(tenant_id, source, event_id)
```

解析器把经过验证的固定字段结构序列化为 canonical JSON。时间统一为 UTC
RFC3339Nano。`event_hash` 计算方式为：

```text
SHA-256("waybill-event-v1\0" + canonical_event)
```

JSON 空白、object key 顺序和等价时区表示不改变哈希。任何受支持的信封字段或 data
字段变化都会改变哈希。

首次提交在 inbox 中保存版本化结果 JSON 的 canonical bytes。精确重试返回相同 body，并加
`Idempotent-Replayed: true`。相同身份对应不同 `event_hash` 时返回
`409 event_identity_conflict`。旧版本 inbox 记录缺少完整事件哈希时返回
`409 event_identity_legacy`，系统不猜测它是否等价。

## incident 投影

`source_version` 是唯一业务顺序。`received_at`、信封 `time`、`event_time` 和
`record_time` 都不能决定新旧。

incident 身份由 tenant、source 和 `incident_key` 确定。ID 使用固定 namespace 的 UUIDv5。
同一来源和 incident key 不能绑定多个运单。

投影由该 episode 的全部不可变 inbox 记录重算：

1. 延误事件在自己的 `source_version` 形成候选事实。
2. 修正只能指向同 tenant、source、waybill 和 incident key 下较低版本的延误事件。
3. 同一目标的最高版本有效修正生效。replace 产生新候选，retract 移除该候选。
4. 剩余候选中 `source_version` 最大者是当前事实。没有候选时状态为 `retracted`。
5. 修正先于目标到达时状态为 `pending_correction`。目标以后到达会触发引用它的 episode
   重投影。
6. 同一 source version 出现不同语义记录时状态为 `conflicted`，并进入人工复核。
7. 无效目标、episode 身份冲突和版本冲突都保存稳定 review code。
8. 冲突证据只保存排序后的前 16 个事件引用和总数。

reducer 是纯函数。任意相同事件集合的输入排列必须产生相同投影和 fingerprint。

`incidents.status` 继续表示工作流状态。事件投影使用独立字段。已有 run 后发生事实变化时，
系统设置 `requires_reinvestigation=true`，但不修改 run，也不覆盖工作流状态。

## 入站事务

`postgres.Repository.IngestEvent` 在一个事务中完成：

1. 插入完整 inbox 记录，冲突时读取并回放已提交结果。
2. 对当前事件和 correction target 的身份按排序后的 advisory key 加锁。
3. 找出当前 episode，以及已有 correction 引用新事件所影响的其他 episode。
4. 按 incident ID 排序并锁定全部 incident。
5. 对每个 episode 读取完整事件集合并执行 reducer。
6. 投影变化时递增 incident version，并写一个完整 snapshot outbox 事件。
7. 把当前 inbox 记录更新为 processed，保存结果 canonical bytes。
8. 提交后返回。

inbox、incident 和 outbox 的任一步失败都会回滚。不同 incident 的多行锁始终按排序顺序获取。
提交结果不明确时，repository 用新连接读取 inbox。匹配的终态结果表示提交成功；没有记录时只
允许一次有界重试；无法判定时返回 503，由生产者使用相同身份重试。

## outbox

outbox 保存重建 structured CloudEvent 所需的全部字段，包括 source、id、type、subject、
time、dataschema 和 canonical data bytes。incident outbox ID 由 tenant、incident ID 和
incident version 计算，重试不生成新身份。

dispatcher：

- 使用现有 `FOR UPDATE SKIP LOCKED`、租约和 fencing token。
- 发布期间每 `leaseTTL / 3` 续租。
- 任意续租错误都会取消当前发布，旧 owner 不再完成记录。
- 2xx 表示成功。408、425、429、5xx、网络错误和超时可重试。
- 其他 3xx 和 4xx 是 permanent failure。
- retryable failure 使用确定性指数退避，最大五分钟。
- permanent failure 阻塞同 aggregate 的后续版本，直到带原因的人工 requeue。

发布成功但完成数据库更新失败时，租约到期后会重发同一事件。下游消费者通过 transactional
inbox 避免重复改变状态。

迁移不把历史 outbox 记录伪装成 published，也不删除它们。启用 dispatcher 会按现有 aggregate
顺序发布历史 `com.waybill.audit.*` 记录。部署者必须先确认下游支持这些事件。mock 模式默认
关闭 dispatcher。

## 指标

指标只使用固定枚举标签：

```text
waybill_event_ingest_requests_total{outcome}
waybill_outbox_publish_attempts_total{outcome}
waybill_outbox_events{status}
waybill_outbox_oldest_unpublished_age_seconds
waybill_outbox_store_errors_total{operation}
```

标签禁止包含 tenant、source、type、event ID、incident ID、worker ID、URL 和错误文本。
dispatcher 定期用一个 SQL 聚合刷新内存快照。Prometheus scrape 不查询数据库。最老事件年龄
根据快照中的时间持续增长，积压清空后归零。

## 验证

实现至少证明：

- 同一事件并发提交 10 次，只有一个 inbox、incident transition 和 outbox 事件。
- 提交后丢失响应，重试返回首次保存的结果。
- 乱序、correction 先到和相同版本冲突的最终投影与到达顺序无关。
- 跨 tenant、未授权 source/type/waybill 和非法 schema 在存储前失败。
- outbox 发布后标记前失败会重发，但 transactional consumer 只改变一次状态。
- 两个 dispatcher 不会同时持有一个有效 claim，过期 owner 不能完成记录。
- PostgreSQL 17 migration、race test、现有离线 E2E 全部通过。
