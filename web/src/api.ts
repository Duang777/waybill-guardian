import { z } from "zod";

const runIdSchema = z.string().min(1).brand<"RunID">();
const approvalIdSchema = z.string().min(1).brand<"ApprovalID">();
const effectIdSchema = z.string().min(1).brand<"EffectID">();
export const waybillIdSchema = z.string().regex(/^YD\d{10}$/).brand<"WaybillID">();

export type RunID = z.infer<typeof runIdSchema>;
export type ApprovalID = z.infer<typeof approvalIdSchema>;
export type WaybillID = z.infer<typeof waybillIdSchema>;

const runStatusSchema = z.enum([
  "started",
  "investigating",
  "awaiting_approval",
  "executing",
  "completed",
  "rejected",
  "failed",
  "review_required",
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

const waybillCatalogItemSchema = z
  .object({
    waybill_id: waybillIdSchema,
    origin: z.string().min(1),
    destination: z.string().min(1),
    origin_hub_id: z.string().min(1).optional(),
    destination_hub_id: z.string().min(1).optional(),
    route_id: z.string().min(1).optional(),
    status: z.string().min(1),
    has_anomaly: z.boolean(),
    anomaly_label: z.string().min(1).optional(),
    anomaly_type: z.string().min(1).optional(),
    last_recorded_at: z.string().min(1),
  })
  .strict();

export type WaybillCatalogItem = z.infer<typeof waybillCatalogItemSchema>;

const riskSchema = z
  .object({
    eta_delay: z.number().min(0).max(100),
    road: z.number().min(0).max(100),
    weather: z.number().min(0).max(100),
  })
  .strict();

const hubOverviewSchema = z
  .object({
    hub_id: z.string().min(1),
    name: z.string().min(1),
    province: z.string().min(1),
    city: z.string().min(1),
    longitude: z.number(),
    latitude: z.number(),
    daily_capacity: z.number().int().nonnegative(),
    waybills: z.number().int().nonnegative(),
    in_flight: z.number().int().nonnegative(),
    anomalies: z.number().int().nonnegative(),
    handling: z.number().int().nonnegative(),
    closed: z.number().int().nonnegative(),
    focus_waybill_id: waybillIdSchema.optional(),
  })
  .strict();

export type HubOverview = z.infer<typeof hubOverviewSchema>;

const routeOverviewSchema = z
  .object({
    route_id: z.string().min(1),
    origin_hub_id: z.string().min(1),
    destination_hub_id: z.string().min(1),
    distance_km: z.number().int().nonnegative(),
    standard_hours: z.number().int().nonnegative(),
    waybills: z.number().int().nonnegative(),
    anomalies: z.number().int().nonnegative(),
    delay_heat: z.number().int().min(0).max(100),
    max_risk: z.number().int().min(0).max(100),
  })
  .strict();

export type RouteOverview = z.infer<typeof routeOverviewSchema>;

const anomalyOverviewSchema = z
  .object({
    waybill_id: waybillIdSchema,
    origin: z.string().min(1),
    destination: z.string().min(1),
    origin_hub_id: z.string().min(1).optional(),
    destination_hub_id: z.string().min(1).optional(),
    route_id: z.string().min(1).optional(),
    type: z.string().min(1),
    label: z.string(),
    last_recorded_at: z.string().min(1),
    risk: riskSchema,
    risk_score: z.number().int().min(0).max(100),
    run_id: runIdSchema.optional(),
    run_status: runStatusSchema.optional(),
  })
  .strict();

export type AnomalyOverview = z.infer<typeof anomalyOverviewSchema>;

const executiveBriefItemSchema = z
  .object({
    id: z.string().min(1),
    headline: z.string().min(1),
    body: z.string().min(1),
    evidence: z
      .array(
        z
          .object({
            label: z.string().min(1),
            value: z.string().min(1),
            source: z.string().min(1),
          })
          .strict(),
      )
      .min(1),
  })
  .strict();

const briefFallbackReasonSchema = z.enum([
  "timeout",
  "provider_error",
  "schema",
  "evidence",
]);

const executiveBriefSchema = z.discriminatedUnion("mode", [
  z
    .object({
      mode: z.literal("deterministic_read_only"),
      source: z.literal("rules"),
      fallback_reason: briefFallbackReasonSchema.optional(),
      items: z.array(executiveBriefItemSchema).length(3),
    })
    .strict(),
  z
    .object({
      mode: z.literal("model_read_only"),
      source: z.string().min(1).refine((source) => source !== "rules"),
      items: z.array(executiveBriefItemSchema).length(3),
    })
    .strict(),
]);

export const overviewSchema = z
  .object({
    as_of: z.string().min(1),
    data_mode: z.enum(["simulated", "fixture", "external"]),
    network_available: z.boolean(),
    totals: z
      .object({
        waybills: z.number().int().nonnegative(),
        anomalies: z.number().int().nonnegative(),
        in_flight: z.number().int().nonnegative(),
        handling: z.number().int().nonnegative(),
        closed: z.number().int().nonnegative(),
      })
      .strict(),
    hubs: z.array(hubOverviewSchema),
    routes: z.array(routeOverviewSchema),
    anomalies: z.array(anomalyOverviewSchema),
    anomaly_distribution: z.array(
      z
        .object({
          type: z.string().min(1),
          count: z.number().int().nonnegative(),
        })
        .strict(),
    ),
    brief: executiveBriefSchema,
  })
  .strict();

export type Overview = z.infer<typeof overviewSchema>;

const kpiMetricSchema = z
  .object({
    key: z.string().min(1),
    label: z.string().min(1),
    value: z.number().nullable(),
    unit: z.string(),
    availability: z.enum(["available", "unavailable"]),
    formula: z.string().min(1),
    reason: z.string().min(1).optional(),
  })
  .strict();

export type KPIMetric = z.infer<typeof kpiMetricSchema>;

export const kpiReportSchema = z
  .object({
    window: z.string().min(1),
    as_of: z.string().min(1),
    assumptions: z
      .object({
        evidence_step_minutes: z.number().positive(),
      })
      .strict(),
    metrics: z.array(kpiMetricSchema).min(4),
  })
  .strict();

export type KPIReport = z.infer<typeof kpiReportSchema>;

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
    origin_hub_id: z.string().min(1).optional(),
    destination_hub_id: z.string().min(1).optional(),
    route_id: z.string().min(1).optional(),
    vehicle_id: z.string().min(1).optional(),
    cargo: z.string().min(1),
    carrier_id: z.string().min(1),
    driver_id: z.string().min(1),
    status: z.string().min(1),
    sla_hours: z.number().int().nonnegative(),
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
    anomaly_type: z.string().min(1).optional(),
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

const legacyEvidenceSchema = z
  .object({
    label: z.string().min(1),
    value: z.string().min(1),
  })
  .strict();

const sourcedEvidenceSchema = legacyEvidenceSchema
  .extend({
    source: z
      .object({
        tool_call_id: z.string().min(1),
        field_path: z.string().min(1),
        source_seq: z.number().int().positive(),
      })
      .strict(),
  })
  .strict();

const evidenceSchema = z.union([legacyEvidenceSchema, sourcedEvidenceSchema]);

const proposalRefSchema = z
  .object({
    proposal_id: z.string().min(1),
    event_id: z.string().min(1),
    digest: z.string().regex(/^[0-9a-f]{64}$/),
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
    proposal_ref: proposalRefSchema.optional(),
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

const proposalCitationSchema = z
  .object({
    tool_call_id: z.string().min(1),
    field_path: z.string().min(1),
    value: z.unknown(),
    display_value: z.string(),
    source_event_id: z.string().min(1),
    source_seq: z.number().int().positive(),
    source_hash: z.string().length(64),
  })
  .strict();

const proposalImpactSchema = z
  .object({
    availability: z.literal("unavailable"),
    reason: z.string().min(1),
  })
  .strict();

export const proposalSchema = z
  .object({
    schema_version: z.literal("proposal.v1"),
    summary: z.string().min(1),
    confidence_bps: z.number().int().min(1).max(10_000),
    attribution: z
      .array(
        z
          .object({
            factor: z.string().min(1),
            confidence_bps: z.number().int().min(1).max(10_000),
            evidence: z.array(proposalCitationSchema).min(1),
          })
          .strict(),
      )
      .min(1),
    alternatives: z.array(
      z
        .object({
          carrier_id: z.string().min(1),
          reason: z.string().min(1),
        })
        .strict(),
    ),
    expected_impact: z
      .object({
        eta_saved_min: proposalImpactSchema,
        cost_delta_cny: proposalImpactSchema,
      })
      .strict(),
    digest: z.string().regex(/^[0-9a-f]{64}$/),
  })
  .strict();

export type Proposal = z.infer<typeof proposalSchema>;

export const preparedProposalSchema = z
  .object({
    proposal_id: z.string().min(1),
    approval_id: approvalIdSchema,
    sdk_run_id: z.string().min(1),
    plan_version: z.number().int().positive(),
    proposal: proposalSchema,
    writes: z.array(approvalItemSchema).min(1),
    writes_digest: z.string().regex(/^[0-9a-f]{64}$/),
    requested_at: z.string().min(1).optional(),
    expires_at: z.string().min(1),
  })
  .strict();

export type PreparedProposal = z.infer<typeof preparedProposalSchema>;

export const runStartedPayloadSchema = z
  .object({
    incident_id: z.string().min(1),
    waybill_id: waybillIdSchema,
    status: z.literal("started"),
    platform_profile: z.string().optional(),
    read_source: z.string().optional(),
    inference: z
      .object({
        mode: z.enum(["offline", "online"]),
        api_style: z.string().optional(),
        model: z.string().optional(),
      })
      .strict()
      .optional(),
  })
  .strict();

export const auditEventTypeSchema = z.enum([
  "run_started",
  "model_call_started",
  "model_call_finished",
  "tool_call",
  "tool_result",
  "proposal_prepared",
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
  "run_review_required",
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

export const pendingApprovalSummarySchema = z
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
const waybillCatalogSchema = z
  .object({ waybills: z.array(waybillCatalogItemSchema) })
  .strict();
const pendingApprovalsSchema = z
  .object({ approvals: z.array(pendingApprovalSummarySchema) })
  .strict();
const batchRunResultSchema = z
  .object({
    waybill_id: waybillIdSchema,
    run: runSchema.optional(),
    error: z
      .object({
        code: z.string().min(1),
        message: z.string().min(1),
      })
      .strict()
      .optional(),
  })
  .strict()
  .refine((value) => (value.run === undefined) !== (value.error === undefined), {
    message: "batch item must contain exactly one of run or error",
  });

export const batchRunResponseSchema = z
  .object({
    requested: z.number().int().positive(),
    accepted: z.number().int().nonnegative(),
    results: z.array(batchRunResultSchema).min(1),
  })
  .strict()
  .refine((value) => value.requested === value.results.length, {
    message: "batch result count does not match requested count",
  });

export type BatchRunResponse = z.infer<typeof batchRunResponseSchema>;

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

export function getWaybill(id: WaybillID, signal?: AbortSignal): Promise<WaybillView> {
  return request(`/api/waybills/${encodeURIComponent(id)}`, waybillViewSchema, { signal });
}

export async function listWaybills(signal?: AbortSignal): Promise<WaybillCatalogItem[]> {
  const response = await request("/api/waybills", waybillCatalogSchema, { signal });
  return response.waybills;
}

export function getOverview(signal?: AbortSignal): Promise<Overview> {
  return request("/api/overview", overviewSchema, { signal });
}

export function getKPIs(
  window = "24h",
  signal?: AbortSignal,
): Promise<KPIReport> {
  const query = new URLSearchParams({ window });
  return request(`/api/kpis?${query.toString()}`, kpiReportSchema, { signal });
}

export function startRun(waybillID: WaybillID, signal?: AbortSignal): Promise<Run> {
  return request("/api/runs", runSchema, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ waybill_id: waybillID }),
    signal,
  });
}

export function startBatch(
  waybillIDs: readonly WaybillID[],
  signal?: AbortSignal,
): Promise<BatchRunResponse> {
  return request("/api/runs:batch", batchRunResponseSchema, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ waybill_ids: waybillIDs }),
    signal,
  });
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
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({}),
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
