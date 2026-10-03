import { describe, expect, it } from "vitest";
import { auditEventSchema, type AuditEventType } from "./api";
import {
  initialTimelineState,
  latestApproval,
  playbackCursor,
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
});

describe("latestApproval", () => {
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

    expect(latestApproval([requested])?.status).toBe("pending");
    expect(latestApproval([requested, decided])?.status).toBe("confirmed");
    expect(latestApproval([requested, decided, executed])?.status).toBe("executed");
  });
});
