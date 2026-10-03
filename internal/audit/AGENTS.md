# internal/audit · AGENTS.md

## 职责

本目录提供 append-only 审计日志、完整性校验、历史回放和 live 订阅。审批和幂等投影也从
这份日志重建，避免一项业务事实写入多个文件。

## 事件模型

- `schema_version`、稳定 `event_id`、单调递增 `seq`、`ts`、`run_id`。
- `actor`：`agent`、`human` 或 `system`。
- `type`：run、工具、归因、审批、写操作、去重和备注事件。
- `payload`：结构化数据。store 在序列化和计算哈希前递归脱敏。
- `prev_hash` 和 `hash`：SHA-256 哈希链。

首条事件的 `prev_hash` 是 64 个 `0`。`event_id` 已存在时，store 返回原事件，避免恢复过程
重复追加同一业务事实。

## 写入与恢复

每个 run 使用一份 `audit-<run_id>.jsonl`。一次 append 在 run 锁内完成：

1. 脱敏 payload。
2. 分配下一个 `seq` 并计算哈希。
3. 一次写入完整 JSON 和换行。
4. 调用 `fsync`。
5. 更新内存投影并通知订阅者。

启动时，store 校验序号、`prev_hash` 和 `hash`。最后一条不完整记录可以截断。中间坏行、
序号倒退或哈希不匹配会使启动失败。

`Open` 将数据目录权限收紧为 `0700`，并以 `0600` 打开 `.writer.lock`。进程使用非阻塞
exclusive flock 持有单 writer 所有权，直到 `Close`。第二个进程不能同时打开同一数据目录。
`Close` 同时关闭 live 订阅，所有后续读写返回 `ErrStoreClosed`。

`MaskPhone` 和 `MaskPlate` 处理 API 返回值。递归 payload 脱敏处理手机号、电话和车牌相关字段，
避免敏感原文进入日志和哈希输入。

## 回放与订阅

`Replay(runID, after)` 返回所有 `seq > after` 的事件。`Subscribe` 在同一 run 锁内复制 backlog
并注册 live subscriber，避免 replay 和 live 交接时漏事件。慢订阅者会被关闭，客户端使用
最后收到的 `seq` 重新连接。

## 验证

测试覆盖十条事件顺序、哈希链、脱敏、重复 event ID、损坏日志、尾部修复、游标错误和
replay/live 交接。
