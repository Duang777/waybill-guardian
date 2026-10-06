import { describe, expect, it } from "vitest";
import {
  approvalSchema,
  auditEventSchema,
  runSnapshotSchema,
  type AuditEventType,
} from "./api";
import {
  evidenceFocusTarget,
  initialTimelineState,
  inferenceMode,
  latestApproval,
  latestProposal,
  playbackCursor,
  proposalForApproval,
  runStatus,
  timelinePhases,
  timelineReducer,
  visibleEvents,
} from "./timeline";

const hash = "0".repeat(64);

function event(seq: number, type: AuditEventType, payload: unknown) {
  return auditEventSchema.parse({
    schema_version: 1,
    event_id: `event-${seq}`,
    seq,
    ts: "2026-10-10T01:15:00Z",
    run_id: "run-1",
    actor: "system",
    type,
    payload,
    prev_hash: hash,
    hash,
  });
}

function preparedProposalPayload({
  approvalID,
  planVersion,
  digest,
  carrierID,
}: {
  approvalID: string;
  planVersion: number;
  digest: string;
  carrierID: string;
}) {
  return {
    proposal_id: `proposal:${approvalID}`,
    approval_id: approvalID,
    sdk_run_id: `sdk-${planVersion}`,
    plan_version: planVersion,
    proposal: {
      schema_version: "proposal.v1",
      summary: "司机疲劳与异常停留造成延误风险。",
      confidence_bps: 8600,
      attribution: [
        {
          factor: "司机连续驾驶时间过长",
          confidence_bps: 9100,
          evidence: [
            {
              tool_call_id: "call-driver",
              field_path: "/continuous_drive_hours",
              value: 9,
              display_value: "9",
              source_event_id: "tool:call-driver:result",
              source_seq: 1,
              source_hash: hash,
            },
          ],
        },
      ],
      alternatives: [
        {
          carrier_id: carrierID,
          reason: "时效和历史履约率更优",
        },
      ],
      expected_impact: {
        eta_saved_min: {
          availability: "unavailable",
          reason: "当前证据没有改派后的到达时间",
        },
        cost_delta_cny: {
          availability: "unavailable",
          reason: "当前证据没有成本字段",
        },
      },
      digest,
    },
    writes: [
      {
        call_id: "call-write",
        action: "tms.reassign",
        wire_name: "tms_reassign",
        params: {
          waybill_id: "YD2026101001",
          carrier_id: carrierID,
        },
        arguments_hash: "arguments-hash",
        identity_version: "effect-v1",
        effect_id: "1fd92e48-c2d3-5654-b9f9-a507a2348354",
        idempotency_key: "key-1",
      },
    ],
    writes_digest: "b".repeat(64),
    requested_at: "2026-10-10T01:15:00Z",
    expires_at: "2026-10-10T01:25:00Z",
  };
}

describe("timelineReducer", () => {
  it("accepts approval execution failure events", () => {
    const parsed = auditEventSchema.safeParse({
      schema_version: 1,
      event_id: "event-partial-failure",
      seq: 1,
      ts: "2026-10-10T01:15:00Z",
      run_id: "run-1",
      actor: "system",
      type: "approval_execution_failed",
      payload: {
        approval_id: "APR-1",
        status: "partially_failed",
        failed_at: "2026-10-10T01:16:01Z",
        items: [
          {
            call_id: "call-1",
            action: "tms.reassign",
            idempotency_key: "key-1",
            status: "failed",
          },
        ],
      },
      prev_hash: hash,
      hash,
    });

    expect(parsed.success).toBe(true);
  });

  it("deduplicates replayed events by sequence", () => {
    const first = event(1, "run_started", { status: "started" });
    const once = timelineReducer(initialTimelineState, {
      type: "event_received",
      event: first,
    });
    const twice = timelineReducer(once, { type: "event_received", event: first });

    expect(twice.events).toEqual([first]);
  });

  it("keeps incoming events hidden while paused and replays to the live edge", () => {
    const first = timelineReducer(initialTimelineState, {
      type: "event_received",
      event: event(1, "run_started", {}),
    });
    const paused = timelineReducer(first, { type: "toggle_playback" });
    const withNewEvent = timelineReducer(paused, {
      type: "event_received",
      event: event(2, "tool_call", { action: "tms.get_waybill" }),
    });

    expect(visibleEvents(withNewEvent)).toHaveLength(1);
    const playing = timelineReducer(withNewEvent, { type: "toggle_playback" });
    const finished = timelineReducer(playing, { type: "tick" });
    expect(visibleEvents(finished)).toHaveLength(2);
    expect(playbackCursor(finished)).toBe(2);
    expect(finished.playback.kind).toBe("paused");
  });

  it("hydrates one complete snapshot and returns to the live edge", () => {
    const events = [
      event(1, "run_started", {}),
      event(2, "tool_call", { action: "tms.get_waybill" }),
    ];
    const hydrated = timelineReducer(initialTimelineState, { type: "hydrate", events });

    expect(hydrated.events).toEqual(events);
    expect(hydrated.playback).toEqual({ kind: "live" });
  });

  it("focuses an evidence source and reveals its event", () => {
    const events = [
      event(1, "run_started", {}),
      event(2, "tool_result", { action: "tms.get_driver" }),
      event(3, "proposal_prepared", {}),
    ];
    const hydrated = timelineReducer(initialTimelineState, { type: "hydrate", events });
    const focused = timelineReducer(hydrated, { type: "focus_event", seq: 2 });

    expect(focused.focusedSeq).toBe(2);
    expect(focused.playback).toEqual({ kind: "paused", cursor: 2 });
    expect(visibleEvents(focused)).toEqual(events.slice(0, 2));
  });

  it("rejects events from another run after hydration", () => {
    const first = event(1, "run_started", {});
    const hydrated = timelineReducer(initialTimelineState, {
      type: "hydrate",
      events: [first],
    });
    const foreign = auditEventSchema.parse({
      ...event(2, "tool_call", { action: "tms.get_waybill" }),
      event_id: "foreign-event",
      run_id: "run-2",
    });

    const next = timelineReducer(hydrated, { type: "event_received", event: foreign });

    expect(next.events).toEqual([first]);
  });
});

describe("timeline presentation selectors", () => {
  it("groups the audit trail into six business phases and merges a tool call with its result", () => {
    const events = [
      event(1, "run_started", {}),
      event(2, "tool_call", {
        call_id: "call-tracking",
        action: "tms.get_tracking",
        wire_name: "tms_get_tracking",
        arguments: { waybill_id: "YD2026101001" },
      }),
      event(3, "tool_result", {
        call_id: "call-tracking",
        action: "tms.get_tracking",
        wire_name: "tms_get_tracking",
        result: { points: [] },
      }),
      event(4, "attribution", { summary: "异常停留导致延误风险" }),
      event(5, "proposal_prepared", {}),
      event(6, "approval_requested", {}),
      event(7, "approval_decided", {}),
      event(8, "write_started", { action: "tms.reassign" }),
      event(9, "write_executed", { action: "tms.reassign" }),
      event(10, "approval_executed", {}),
      event(11, "run_completed", {}),
    ];

    const phases = timelinePhases(events);

    expect(phases.map((phase) => phase.id)).toEqual([
      "perception",
      "attribution",
      "proposal",
      "approval",
      "execution",
      "closure",
    ]);
    expect(phases.map((phase) => phase.status)).toEqual([
      "complete",
      "complete",
      "complete",
      "complete",
      "complete",
      "complete",
    ]);
    expect(phases[0]?.steps).toHaveLength(2);
    expect(phases[0]?.steps[1]).toMatchObject({
      label: "读取轨迹",
      eventSeqs: [2, 3],
    });
  });

  it("marks approval as current while execution and closure are still upcoming", () => {
    const phases = timelinePhases([
      event(1, "run_started", {}),
      event(2, "attribution", {}),
      event(3, "proposal_prepared", {}),
      event(4, "approval_requested", {}),
    ]);

    expect(phases.map((phase) => phase.status)).toEqual([
      "complete",
      "complete",
      "complete",
      "current",
      "upcoming",
      "upcoming",
    ]);
  });

  it("maps tracking evidence to the matching route point and keeps driver evidence on the timeline", () => {
    const tracking = [
      {
        label: "杭州公路港",
        recorded_at: "2026-10-10T01:00:00Z",
        longitude: 120.1551,
        latitude: 30.2741,
        speed_kph: 68,
        anomaly: false,
      },
      {
        label: "绵阳北服务区",
        recorded_at: "2026-10-10T09:00:00Z",
        longitude: 104.6796,
        latitude: 31.4675,
        speed_kph: 0,
        stop_hours: 6,
        anomaly: true,
        anomaly_type: "delay",
      },
    ];
    const events = [
      event(1, "tool_result", {
        call_id: "call-tracking",
        action: "tms.get_tracking",
        wire_name: "tms_get_tracking",
        result: { points: tracking },
      }),
      event(2, "tool_result", {
        call_id: "call-driver",
        action: "tms.get_driver",
        wire_name: "tms_get_driver",
        result: { continuous_drive_hours: 9 },
      }),
    ];

    expect(
      evidenceFocusTarget(events, tracking, {
        sourceSeq: 1,
        fieldPath: "/points/1/stop_hours",
      }),
    ).toEqual({ kind: "route_point", seq: 1, pointIndex: 1 });
    expect(
      evidenceFocusTarget(events, tracking, {
        sourceSeq: 2,
        fieldPath: "/continuous_drive_hours",
      }),
    ).toEqual({ kind: "timeline_event", seq: 2 });
  });
});

describe("model proposal projection", () => {
  it("reads inference mode and the latest accepted proposal", () => {
    const started = event(1, "run_started", {
      incident_id: "incident-1",
      waybill_id: "YD2026101001",
      status: "started",
      inference: {
        mode: "online",
        api_style: "chat_completions",
        model: "deepseek-chat",
      },
    });
    const prepared = event(
      2,
      "proposal_prepared",
      preparedProposalPayload({
        approvalID: "APR-1",
        planVersion: 1,
        digest: "a".repeat(64),
        carrierID: "CARRIER-SW-42",
      }),
    );

    expect(inferenceMode([started])).toEqual({
      kind: "online",
      apiStyle: "chat_completions",
      model: "deepseek-chat",
    });
    expect(latestProposal([started, prepared])?.confidence_bps).toBe(8600);
    expect(runStatus([started, prepared])).toBe("investigating");
  });

  it("projects a rejected model proposal to review_required", () => {
    expect(
      runStatus([
        event(1, "run_started", {}),
        event(2, "run_review_required", { status: "review_required" }),
      ]),
    ).toBe("review_required");
  });

  it("pairs an approval with its referenced proposal instead of the newest proposal", () => {
    const firstPrepared = event(
      1,
      "proposal_prepared",
      preparedProposalPayload({
        approvalID: "APR-1",
        planVersion: 1,
        digest: "a".repeat(64),
        carrierID: "CARRIER-SW-42",
      }),
    );
    const firstApproval = event(2, "approval_requested", {
      id: "APR-1",
      run_id: "run-1",
      sdk_run_id: "sdk-1",
      waybill_id: "YD2026101001",
      plan_version: 1,
      items: [
        {
          call_id: "call-write",
          action: "tms.reassign",
          wire_name: "tms_reassign",
          params: {
            waybill_id: "YD2026101001",
            carrier_id: "CARRIER-SW-42",
          },
          arguments_hash: "arguments-hash",
          identity_version: "effect-v1",
          effect_id: "1fd92e48-c2d3-5654-b9f9-a507a2348354",
          idempotency_key: "key-1",
        },
      ],
      reason: "降低延误风险",
      evidence: [],
      proposal_ref: {
        proposal_id: "proposal:APR-1",
        event_id: "event-1",
        digest: "a".repeat(64),
      },
      status: "pending",
      requested_at: "2026-10-10T01:15:00Z",
      expires_at: "2026-10-10T01:25:00Z",
    });
    const secondPrepared = event(
      3,
      "proposal_prepared",
      preparedProposalPayload({
        approvalID: "APR-2",
        planVersion: 2,
        digest: "c".repeat(64),
        carrierID: "CARRIER-SW-19",
      }),
    );
    const events = [firstPrepared, firstApproval, secondPrepared];
    const approval = latestApproval(events);

    expect(latestProposal(events)?.alternatives[0]?.carrier_id).toBe("CARRIER-SW-19");
    expect(proposalForApproval(events, approval)?.alternatives[0]?.carrier_id).toBe(
      "CARRIER-SW-42",
    );
  });
});

describe("latestApproval", () => {
  it("accepts current effect identity and rejects partial identity", () => {
    const item = {
      call_id: "call-1",
      action: "notify.send_sms",
      wire_name: "notify_send_sms",
      params: {
        waybill_id: "YD2026101001",
        recipient: "shipper",
        carrier_id: "CARRIER-SW-42",
      },
      arguments_hash: "arguments-hash",
      identity_version: "effect-v1",
      effect_id: "1fd92e48-c2d3-5654-b9f9-a507a2348354",
      idempotency_key: "key-1",
    };
    const approval = {
      id: "APR-1",
      run_id: "run-1",
      sdk_run_id: "sdk-1",
      waybill_id: "YD2026101001",
      plan_version: 1,
      items: [item],
      reason: "降低延误风险",
      evidence: [],
      status: "pending",
      requested_at: "2026-10-10T01:15:00Z",
      expires_at: "2026-10-10T01:25:00Z",
    };

    expect(approvalSchema.safeParse(approval).success).toBe(true);
    const { identity_version: transitionalVersion, ...transitionalItem } = item;
    expect(transitionalVersion).toBe("effect-v1");
    expect(
      approvalSchema.safeParse({
        ...approval,
        items: [transitionalItem],
      }).success,
    ).toBe(true);
    expect(
      approvalSchema.safeParse({
        ...approval,
        items: [{ ...item, effect_id: undefined }],
      }).success,
    ).toBe(false);
  });

  it("projects requested, decided, and executed events", () => {
    const requested = event(1, "approval_requested", {
      id: "APR-1",
      run_id: "run-1",
      sdk_run_id: "sdk-1",
      waybill_id: "YD2026101001",
      plan_version: 1,
      items: [
        {
          call_id: "call-1",
          action: "tms.reassign",
          wire_name: "tms_reassign",
          params: {
            waybill_id: "YD2026101001",
            carrier_id: "CARRIER-SW-42",
            idempotency_key: "key-1",
          },
          arguments_hash: "arguments-hash",
          idempotency_key: "key-1",
        },
      ],
      reason: "降低延误风险",
      evidence: [{ label: "连续驾驶", value: "9 小时" }],
      status: "pending",
      requested_at: "2026-10-10T01:15:00Z",
      expires_at: "2026-10-10T01:25:00Z",
    });
    const decided = event(2, "approval_decided", {
      approval_id: "APR-1",
      status: "confirmed",
      decided_by: "reviewer",
      decided_at: "2026-10-10T01:16:00Z",
    });
    const executed = event(3, "approval_executed", {
      approval_id: "APR-1",
      executed_at: "2026-10-10T01:16:01Z",
    });
    const reconciliationRequired = event(3, "approval_reconciliation_required", {
      approval_id: "APR-1",
      checked_at: "2026-10-10T01:16:01Z",
      items: [
        {
          call_id: "call-1",
          action: "tms.reassign",
          effect_id: "1fd92e48-c2d3-5654-b9f9-a507a2348354",
          idempotency_key: "key-1",
          status: "unknown",
        },
      ],
    });
    const executionFailed = event(3, "approval_execution_failed", {
      approval_id: "APR-1",
      status: "partially_failed",
      failed_at: "2026-10-10T01:16:01Z",
      items: [
        {
          call_id: "call-1",
          action: "tms.reassign",
          idempotency_key: "key-1",
          status: "succeeded",
        },
        {
          call_id: "call-2",
          action: "notify.send_sms",
          idempotency_key: "key-2",
          status: "failed",
        },
      ],
    });

    expect(latestApproval([requested])?.status).toBe("pending");
    expect(latestApproval([requested, decided])?.status).toBe("confirmed");
    expect(latestApproval([requested, decided, executed])?.status).toBe("executed");
    expect(latestApproval([requested, decided, reconciliationRequired])?.status).toBe(
      "reconciliation_required",
    );
    expect(runStatus([requested, decided, reconciliationRequired])).toBe("executing");
    expect(latestApproval([requested, decided, executionFailed])?.status).toBe(
      "partially_failed",
    );
    expect(runStatus([requested, decided, executionFailed])).toBe("failed");
  });
});

describe("runSnapshotSchema", () => {
  it("accepts a contiguous event prefix and rejects a cursor mismatch", () => {
    const first = event(1, "run_started", {});
    const snapshot = {
      run: {
        run_id: "run-1",
        incident_id: "incident-1",
        waybill_id: "YD2026101001",
        status: "investigating",
        last_seq: 1,
        updated_at: "2026-10-10T01:15:00Z",
      },
      events: [first],
    };

    expect(runSnapshotSchema.safeParse(snapshot).success).toBe(true);
    expect(
      runSnapshotSchema.safeParse({
        ...snapshot,
        run: { ...snapshot.run, last_seq: 2 },
      }).success,
    ).toBe(false);
  });

  it("allows an empty timeline only for quarantined runs", () => {
    const run = {
      run_id: "run-quarantined",
      incident_id: "incident-quarantined",
      waybill_id: "YD2026101001",
      status: "manual_review",
      last_seq: 2,
      updated_at: "2026-10-10T01:15:00Z",
    };

    expect(runSnapshotSchema.safeParse({ run, events: [] }).success).toBe(true);
    expect(
      runSnapshotSchema.safeParse({
        run: { ...run, status: "investigating" },
        events: [],
      }).success,
    ).toBe(false);
  });
});

describe("playback projections", () => {
  it("derives approval and run status from the visible event prefix", () => {
    const events = [
      event(1, "run_started", {}),
      event(2, "approval_requested", {
        id: "APR-playback",
        run_id: "run-1",
        sdk_run_id: "sdk-1",
        waybill_id: "YD2026101001",
        plan_version: 1,
        items: [
          {
            call_id: "call-1",
            action: "tms.reassign",
            wire_name: "tms_reassign",
            params: {
              waybill_id: "YD2026101001",
              carrier_id: "CARRIER-SW-42",
            },
            arguments_hash: "arguments-hash",
            idempotency_key: "key-1",
          },
        ],
        reason: "降低延误风险",
        evidence: [],
        status: "pending",
        requested_at: "2026-10-10T01:15:00Z",
        expires_at: "2026-10-10T01:25:00Z",
      }),
      event(3, "approval_decided", {
        approval_id: "APR-playback",
        status: "confirmed",
        decided_by: "reviewer",
        decided_at: "2026-10-10T01:16:00Z",
      }),
      event(4, "run_completed", { status: "completed" }),
    ];
    const hydrated = timelineReducer(initialTimelineState, { type: "hydrate", events });
    const beforeApproval = timelineReducer(hydrated, { type: "seek", cursor: 1 });
    const awaitingApproval = timelineReducer(hydrated, { type: "seek", cursor: 2 });

    expect(latestApproval(visibleEvents(beforeApproval))).toBeNull();
    expect(runStatus(visibleEvents(beforeApproval))).toBe("investigating");
    expect(latestApproval(visibleEvents(awaitingApproval))?.status).toBe("pending");
    expect(runStatus(visibleEvents(awaitingApproval))).toBe("awaiting_approval");
    expect(runStatus(visibleEvents(hydrated))).toBe("completed");
  });
});
