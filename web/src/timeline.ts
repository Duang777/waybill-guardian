import { z } from "zod";
import {
  approvalSchema,
  approvalStatusSchema,
  type Approval,
  type AuditEvent,
  type AuditEventType,
  type RunStatus,
} from "./api";

export type Playback =
  | { kind: "live" }
  | { kind: "paused"; cursor: number }
  | { kind: "playing"; cursor: number };

export type TimelineState = {
  events: readonly AuditEvent[];
  playback: Playback;
};

export type TimelineAction =
  | { type: "reset" }
  | { type: "event_received"; event: AuditEvent }
  | { type: "toggle_playback" }
  | { type: "seek"; cursor: number }
  | { type: "go_live" }
  | { type: "tick" };

export const initialTimelineState: TimelineState = {
  events: [],
  playback: { kind: "live" },
};

export function timelineReducer(
  state: TimelineState,
  action: TimelineAction,
): TimelineState {
  switch (action.type) {
    case "reset":
      return initialTimelineState;
    case "event_received": {
      if (state.events.some((event) => event.seq === action.event.seq)) {
        return state;
      }
      const events = [...state.events, action.event].sort((left, right) => left.seq - right.seq);
      return { ...state, events };
    }
    case "toggle_playback":
      if (state.playback.kind === "live") {
        return {
          ...state,
          playback: { kind: "paused", cursor: state.events.length },
        };
      }
      if (state.playback.kind === "playing") {
        return {
          ...state,
          playback: { kind: "paused", cursor: state.playback.cursor },
        };
      }
      return {
        ...state,
        playback: {
          kind: "playing",
          cursor:
            state.playback.cursor >= state.events.length ? 0 : state.playback.cursor,
        },
      };
    case "seek":
      return {
        ...state,
        playback: {
          kind: "paused",
          cursor: clamp(action.cursor, 0, state.events.length),
        },
      };
    case "go_live":
      return { ...state, playback: { kind: "live" } };
    case "tick":
      if (state.playback.kind !== "playing") {
        return state;
      }
      if (state.playback.cursor + 1 >= state.events.length) {
        return {
          ...state,
          playback: { kind: "paused", cursor: state.events.length },
        };
      }
      return {
        ...state,
        playback: { kind: "playing", cursor: state.playback.cursor + 1 },
      };
    default: {
      const exhaustive: never = action;
      return exhaustive;
    }
  }
}

export function visibleEvents(state: TimelineState): readonly AuditEvent[] {
  if (state.playback.kind === "live") {
    return state.events;
  }
  return state.events.slice(0, state.playback.cursor);
}

export function playbackCursor(state: TimelineState): number {
  return state.playback.kind === "live" ? state.events.length : state.playback.cursor;
}

const approvalDecisionSchema = z
  .object({
    approval_id: z.string().min(1),
    status: approvalStatusSchema,
    decided_by: z.string(),
    decided_at: z.string(),
    reject_reason: z.string().optional(),
  })
  .strict();

const approvalExecutedSchema = z
  .object({
    approval_id: z.string().min(1),
    executed_at: z.string(),
  })
  .strict();

const approvalExecutionItemBaseSchema = z
  .object({
    call_id: z.string().min(1),
    action: z.string().min(1),
    idempotency_key: z.string().min(1),
    status: z.enum([
      "succeeded",
      "failed",
      "started",
      "indeterminate",
      "retryable_failed",
      "permanent_failed",
      "unknown",
      "reconciling",
      "manual_review",
      "missing",
    ]),
  })
  .strict();

const approvalExecutionItemSchema = z.union([
  approvalExecutionItemBaseSchema,
  approvalExecutionItemBaseSchema.extend({ effect_id: z.string().min(1) }).strict(),
]);

const approvalExecutionFailedSchema = z
  .object({
    approval_id: z.string().min(1),
    status: z.enum(["partially_failed", "failed"]),
    failed_at: z.string(),
    items: z.array(approvalExecutionItemSchema),
  })
  .strict();

const approvalReconciliationSchema = z
  .object({
    approval_id: z.string().min(1),
    checked_at: z.string(),
    items: z.array(approvalExecutionItemSchema),
  })
  .strict();

export function latestApproval(events: readonly AuditEvent[]): Approval | null {
  let current: Approval | null = null;
  for (const event of events) {
    if (event.type === "approval_requested") {
      const parsed = approvalSchema.safeParse(event.payload);
      if (parsed.success) {
        current = parsed.data;
      }
      continue;
    }
    if (event.type === "approval_decided" && current !== null) {
      const parsed = approvalDecisionSchema.safeParse(event.payload);
      if (parsed.success && parsed.data.approval_id === current.id) {
        current = {
          ...current,
          status: parsed.data.status,
          decided_by: parsed.data.decided_by,
          decided_at: parsed.data.decided_at,
          reject_reason: parsed.data.reject_reason,
        };
      }
      continue;
    }
    if (event.type === "approval_executed" && current !== null) {
      const parsed = approvalExecutedSchema.safeParse(event.payload);
      if (parsed.success && parsed.data.approval_id === current.id) {
        current = { ...current, status: "executed" };
      }
      continue;
    }
    if (event.type === "approval_execution_failed" && current !== null) {
      const parsed = approvalExecutionFailedSchema.safeParse(event.payload);
      if (parsed.success && parsed.data.approval_id === current.id) {
        current = { ...current, status: parsed.data.status };
      }
      continue;
    }
    if (event.type === "approval_reconciliation_required" && current !== null) {
      const parsed = approvalReconciliationSchema.safeParse(event.payload);
      if (parsed.success && parsed.data.approval_id === current.id) {
        current = { ...current, status: "reconciliation_required" };
      }
    }
  }
  return current;
}

export function runStatus(events: readonly AuditEvent[]): RunStatus | null {
  let status: RunStatus | null = null;
  for (const event of events) {
    switch (event.type) {
      case "run_started":
        status = "investigating";
        break;
      case "approval_requested":
        status = "awaiting_approval";
        break;
      case "approval_decided":
      case "approval_reconciliation_required":
      case "write_started":
      case "write_executed":
      case "write_unknown":
      case "write_reconciliation_started":
      case "write_reconciled":
        status = "executing";
        break;
      case "run_completed":
        status = "completed";
        break;
      case "run_rejected":
        status = "rejected";
        break;
      case "run_failed":
      case "write_failed":
      case "approval_execution_failed":
        status = "failed";
        break;
      case "tool_call":
      case "tool_result":
      case "attribution":
      case "approval_executed":
      case "duplicate_suppressed":
      case "note":
        break;
      default: {
        const exhaustive: never = event.type;
        return exhaustive;
      }
    }
  }
  return status;
}

export type EventPresentation = {
  label: string;
  detail: string;
  tone: "neutral" | "warning" | "success" | "danger";
};

const eventLabels = {
  run_started: "异常处置已启动",
  tool_call: "Agent 调用工具",
  tool_result: "工具返回证据",
  attribution: "归因完成",
  approval_requested: "等待人工审批",
  approval_decided: "审批决定已记录",
  approval_executed: "审批动作已执行",
  approval_execution_failed: "审批动作部分失败",
  approval_reconciliation_required: "审批动作等待对账",
  write_started: "开始写回平台",
  write_executed: "平台写入成功",
  write_failed: "平台写入失败",
  write_unknown: "平台写入结果未知",
  write_reconciliation_started: "开始查询平台结果",
  write_reconciled: "平台结果已对账",
  duplicate_suppressed: "重复写入已拦截",
  run_completed: "处置完成",
  run_rejected: "处置转人工跟进",
  run_failed: "处置失败",
  note: "系统备注",
} satisfies Record<AuditEventType, string>;

const recordSchema = z.record(z.string(), z.unknown());

export function presentEvent(event: AuditEvent): EventPresentation {
  return {
    label: eventLabels[event.type],
    detail: eventDetail(event),
    tone: eventTone(event.type),
  };
}

function eventDetail(event: AuditEvent): string {
  const payload = recordSchema.safeParse(event.payload);
  if (!payload.success) {
    return `事件 ${event.event_id}`;
  }
  for (const key of ["summary", "action", "status", "wire_name"]) {
    const value = payload.data[key];
    if (typeof value === "string" && value.length > 0) {
      return value;
    }
  }
  if (event.type === "approval_requested") {
    const approval = approvalSchema.safeParse(event.payload);
    if (approval.success) {
      return `方案 v${approval.data.plan_version} · ${approval.data.items.length} 项写操作`;
    }
  }
  return `事件 ${event.event_id}`;
}

function eventTone(type: AuditEventType): EventPresentation["tone"] {
  switch (type) {
    case "approval_requested":
    case "attribution":
    case "approval_reconciliation_required":
    case "write_unknown":
    case "write_reconciliation_started":
      return "warning";
    case "approval_executed":
    case "write_executed":
    case "write_reconciled":
    case "run_completed":
    case "duplicate_suppressed":
      return "success";
    case "write_failed":
    case "approval_execution_failed":
    case "run_failed":
    case "run_rejected":
      return "danger";
    case "run_started":
    case "tool_call":
    case "tool_result":
    case "approval_decided":
    case "write_started":
    case "note":
      return "neutral";
    default: {
      const exhaustive: never = type;
      return exhaustive;
    }
  }
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(Math.max(value, minimum), maximum);
}
