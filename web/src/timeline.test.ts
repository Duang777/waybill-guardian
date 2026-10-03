import { describe, expect, it } from "vitest";
import {
  approvalSchema,
  auditEventSchema,
  runSnapshotSchema,
  type AuditEventType,
} from "./api";
import {
  initialTimelineState,
  latestApproval,
  playbackCursor,
  runStatus,
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

describe("latestApproval", () => {
  it("accepts current effect identity and rejects partial identity", () => {
    const item = {
      call_id: "call-1",
      action: "notify.send_sms",
      wire_name: "notify_send_sms",
      params: {
        phone: "138****1234",
        template_id: "delay",
        params: {},
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
