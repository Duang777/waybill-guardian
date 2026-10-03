import { z } from "zod";

const runIdSchema = z.string().min(1).brand<"RunID">();
const approvalIdSchema = z.string().min(1).brand<"ApprovalID">();
const effectIdSchema = z.string().min(1).brand<"EffectID">();
const waybillIdSchema = z.string().regex(/^YD\d{10}$/).brand<"WaybillID">();

export type RunID = z.infer<typeof runIdSchema>;
export type ApprovalID = z.infer<typeof approvalIdSchema>;
export type WaybillID = z.infer<typeof waybillIdSchema>;

export const DEFAULT_WAYBILL_ID = waybillIdSchema.parse("YD2026101001");

const runStatusSchema = z.enum([
  "started",
  "investigating",
  "awaiting_approval",
  "executing",
  "completed",
  "rejected",
  "failed",
  "manual_review",
]);

export const runSchema = z
  .object({
    run_id: runIdSchema,
    incident_id: z.string().min(1),
    waybill_id: waybillIdSchema,
    status: runStatusSchema,
    last_seq: z.number().int().nonnegative(),
  })
  .strict();

export type Run = z.infer<typeof runSchema>;
export type RunStatus = z.infer<typeof runStatusSchema>;

export const runSummarySchema = runSchema
  .extend({
    updated_at: z.string().min(1),
  })
  .strict();

export type RunSummary = z.infer<typeof runSummarySchema>;

const carrierSchema = z
  .object({
    carrier_id: z.string().min(1),
    name: z.string().min(1),
    eta_hours: z.number().int().nonnegative(),
    reliability_pct: z.number().min(0).max(100),
  })
  .strict();

const waybillSchema = z
  .object({
    waybill_id: waybillIdSchema,
    origin: z.string().min(1),
    destination: z.string().min(1),
    cargo: z.string().min(1),
    carrier_id: z.string().min(1),
    driver_id: z.string().min(1),
    status: z.string().min(1),
    sla_hours: z.number().int().positive(),
    shipper_phone: z.string().min(1),
    candidate_carriers: z.array(carrierSchema),
  })
  .strict();

export const trackPointSchema = z
  .object({
    label: z.string().min(1),
    recorded_at: z.string().min(1),
    longitude: z.number(),
    latitude: z.number(),
    speed_kph: z.number().int().nonnegative(),
    stop_hours: z.number().nonnegative().optional(),
    anomaly: z.boolean(),
  })
  .strict();

export type TrackPoint = z.infer<typeof trackPointSchema>;

const driverSchema = z
  .object({
    driver_id: z.string().min(1),
    name: z.string().min(1),
    phone: z.string().min(1),
    plate: z.string().min(1),
    continuous_drive_hours: z.number().nonnegative(),
    fatigue_alert: z.boolean(),
  })
  .strict();

const weatherSchema = z
  .object({
    segment: z.string().min(1),
    condition: z.string().min(1),
    alert_level: z.string().min(1),
  })
  .strict();

const riskSchema = z
  .object({
    eta_delay: z.number().min(0).max(100),
    road: z.number().min(0).max(100),
    weather: z.number().min(0).max(100),
  })
  .strict();

export const waybillViewSchema = z
  .object({
    waybill: waybillSchema,
    tracking: z.array(trackPointSchema),
    driver: driverSchema,
    weather: z.array(weatherSchema),
    risk: riskSchema,
  })
  .strict();

export type WaybillView = z.infer<typeof waybillViewSchema>;

export const approvalStatusSchema = z.enum([
  "pending",
  "confirmed",
  "reconciliation_required",
  "executed",
  "partially_failed",
  "failed",
  "rejected",
  "expired",
]);

const approvalItemBaseSchema = z
  .object({
    call_id: z.string().min(1),
    action: z.string().min(1),
    wire_name: z.string().min(1),
    params: z.record(z.string(), z.unknown()),
    arguments_hash: z.string().min(1),
    idempotency_key: z.string().min(1),
  })
  .strict();

const approvalItemSchema = z.union([
  approvalItemBaseSchema,
  approvalItemBaseSchema.extend({ effect_id: effectIdSchema }).strict(),
  approvalItemBaseSchema
    .extend({
      identity_version: z.literal("effect-v1"),
      effect_id: effectIdSchema,
    })
    .strict(),
]);

const evidenceSchema = z
  .object({
    label: z.string().min(1),
    value: z.string().min(1),
  })
  .strict();

export const approvalSchema = z
  .object({
    id: approvalIdSchema,
    run_id: runIdSchema,
    sdk_run_id: z.string().min(1),
    waybill_id: waybillIdSchema,
    plan_version: z.number().int().positive(),
    items: z.array(approvalItemSchema).min(1),
    reason: z.string(),
    evidence: z.array(evidenceSchema),
    status: approvalStatusSchema,
    requested_at: z.string().min(1),
    expires_at: z.string().min(1),
    decided_by: z.string().optional(),
    decided_at: z.string().optional(),
    reject_reason: z.string().optional(),
  })
  .strict();

export type Approval = z.infer<typeof approvalSchema>;
export type ApprovalStatus = z.infer<typeof approvalStatusSchema>;

export const auditEventTypeSchema = z.enum([
  "run_started",
  "tool_call",
  "tool_result",
  "attribution",
  "approval_requested",
  "approval_decided",
  "approval_executed",
  "approval_execution_failed",
  "approval_reconciliation_required",
  "write_started",
  "write_executed",
  "write_failed",
  "write_unknown",
  "write_reconciliation_started",
  "write_reconciled",
  "duplicate_suppressed",
  "run_completed",
  "run_rejected",
  "run_failed",
  "note",
]);

export type AuditEventType = z.infer<typeof auditEventTypeSchema>;

export const auditEventSchema = z
  .object({
    schema_version: z.number().int().positive(),
    event_id: z.string().min(1),
    seq: z.number().int().positive(),
    ts: z.string().min(1),
    run_id: runIdSchema,
    actor: z.enum(["agent", "human", "system"]),
    type: auditEventTypeSchema,
    payload: z.unknown(),
    prev_hash: z.string().length(64),
    hash: z.string().length(64),
  })
  .strict();

export type AuditEvent = z.infer<typeof auditEventSchema>;

const pendingApprovalSummarySchema = z
  .object({
    id: approvalIdSchema,
    run_id: runIdSchema,
    waybill_id: waybillIdSchema,
    plan_version: z.number().int().positive(),
    requested_at: z.string().min(1),
    expires_at: z.string().min(1),
  })
  .strict();

export type PendingApprovalSummary = z.infer<typeof pendingApprovalSummarySchema>;

const replayableRunSnapshotSchema = z
  .object({
    run: runSummarySchema.extend({
      status: runStatusSchema.exclude(["manual_review"]),
    }),
    events: z.array(auditEventSchema).min(1),
  })
  .strict()
  .superRefine((snapshot, context) => {
    snapshot.events.forEach((event, index) => {
      if (event.run_id !== snapshot.run.run_id) {
        context.addIssue({
          code: "custom",
          message: "snapshot event belongs to another run",
          path: ["events", index, "run_id"],
        });
      }
      if (event.seq !== index + 1) {
        context.addIssue({
          code: "custom",
          message: "snapshot event sequence is not contiguous",
          path: ["events", index, "seq"],
        });
      }
    });
    if (snapshot.events.at(-1)?.seq !== snapshot.run.last_seq) {
      context.addIssue({
        code: "custom",
        message: "snapshot cursor does not match event tail",
        path: ["run", "last_seq"],
      });
    }
  });

const quarantinedRunSnapshotSchema = z
  .object({
    run: runSummarySchema.extend({
      status: z.literal("manual_review"),
    }),
    events: z.array(auditEventSchema).length(0),
  })
  .strict();

export const runSnapshotSchema = z.union([
  replayableRunSnapshotSchema,
  quarantinedRunSnapshotSchema,
]);

export type RunSnapshot = z.infer<typeof runSnapshotSchema>;

const activeRunsSchema = z.object({ runs: z.array(runSummarySchema) }).strict();
const pendingApprovalsSchema = z
  .object({ approvals: z.array(pendingApprovalSummarySchema) })
  .strict();

const problemSchema = z
  .object({
    error: z
      .object({
        code: z.string(),
        message: z.string(),
      })
      .strict(),
  })
  .strict();

export class APIError extends Error {
  readonly code: string;
  readonly status: number;

  constructor(message: string, code: string, status: number) {
    super(message);
    this.name = "APIError";
    this.code = code;
    this.status = status;
  }
}

async function request<T>(
  path: string,
  schema: z.ZodType<T>,
  init?: RequestInit,
): Promise<T> {
  const response = await fetch(path, init);
  const raw: unknown = await response.json();
  if (!response.ok) {
    const problem = problemSchema.safeParse(raw);
    if (problem.success) {
      throw new APIError(
        problem.data.error.message,
        problem.data.error.code,
        response.status,
      );
    }
    throw new APIError(`请求失败，HTTP ${response.status}`, "invalid_response", response.status);
  }
  return schema.parse(raw);
}

export function getWaybill(id: WaybillID): Promise<WaybillView> {
  return request(`/api/waybills/${encodeURIComponent(id)}`, waybillViewSchema);
}

export function triggerDemo(): Promise<Run> {
  return request("/api/demo/trigger", runSchema, { method: "POST" });
}

export async function listActiveRuns(signal?: AbortSignal): Promise<RunSummary[]> {
  const response = await request("/api/runs?status=active", activeRunsSchema, { signal });
  return response.runs;
}

export async function listPendingApprovals(
  signal?: AbortSignal,
): Promise<PendingApprovalSummary[]> {
  const response = await request("/api/approvals?status=pending", pendingApprovalsSchema, {
    signal,
  });
  return response.approvals;
}

export function getRunSnapshot(runID: RunID, signal?: AbortSignal): Promise<RunSnapshot> {
  return request(`/api/runs/${encodeURIComponent(runID)}`, runSnapshotSchema, { signal });
}

export function confirmApproval(id: ApprovalID): Promise<Approval> {
  return request(`/api/approvals/${encodeURIComponent(id)}/confirm`, approvalSchema, {
    method: "POST",
  });
}

export function rejectApproval(id: ApprovalID, reason: string): Promise<Approval> {
  return request(`/api/approvals/${encodeURIComponent(id)}/reject`, approvalSchema, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ reason }),
  });
}

type TimelineHandlers = {
  onEvent: (event: AuditEvent) => void;
  onConnectionChange: (connected: boolean) => void;
  onError: (message: string) => void;
};

export function openTimeline(
  runID: RunID,
  after: number,
  handlers: TimelineHandlers,
): () => void {
  const query = new URLSearchParams({ after: String(after) });
  const source = new EventSource(
    `/api/runs/${encodeURIComponent(runID)}/timeline?${query.toString()}`,
  );
  const receive = (message: Event): void => {
    if (!(message instanceof MessageEvent) || typeof message.data !== "string") {
      return;
    }
    try {
      const raw: unknown = JSON.parse(message.data);
      const event = auditEventSchema.parse(raw);
      handlers.onEvent(event);
      handlers.onConnectionChange(true);
    } catch (error) {
      handlers.onError(error instanceof Error ? error.message : "时间线事件无法解析");
    }
  };
  for (const type of auditEventTypeSchema.options) {
    source.addEventListener(type, receive);
  }
  source.onopen = () => handlers.onConnectionChange(true);
  source.onerror = () => {
    handlers.onConnectionChange(false);
  };
  return () => source.close();
}
