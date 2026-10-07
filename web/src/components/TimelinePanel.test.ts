import { describe, expect, it } from "vitest";
import { auditEventSchema, type AuditEvent } from "../api";
import {
  initialTimelineState,
  timelineReducer,
  type TimelineState,
} from "../timeline";
import { motionDuration, motionEase } from "../motion/tokens";
import { appendedLiveSequence } from "./TimelinePanel";

describe("timeline motion feedback", () => {
  it("animates only an event appended to the live edge of the same run", () => {
    const first = receive(initialTimelineState, event("run-1", 1));
    const second = receive(first, event("run-1", 2));

    expect(appendedLiveSequence(first, second)).toBe(2);

    const paused = timelineReducer(first, { type: "toggle_playback" });
    const receivedWhilePaused = receive(paused, event("run-1", 2));
    expect(appendedLiveSequence(paused, receivedWhilePaused)).toBeNull();

    const liveAgain = timelineReducer(receivedWhilePaused, { type: "go_live" });
    expect(appendedLiveSequence(receivedWhilePaused, liveAgain)).toBeNull();

    const otherRun = timelineReducer(initialTimelineState, {
      type: "hydrate",
      events: [event("run-2", 1), event("run-2", 2)],
    });
    expect(appendedLiveSequence(first, otherRun)).toBeNull();
  });

  it("keeps the shared feedback timings within the interaction budget", () => {
    expect(motionDuration).toEqual({
      press: 0.12,
      event: 0.18,
      status: 0.24,
    });
    expect(motionEase).toEqual([0.16, 1, 0.3, 1]);
    expect(Math.max(...Object.values(motionDuration))).toBeLessThanOrEqual(0.3);
  });
});

function receive(state: TimelineState, auditEvent: AuditEvent): TimelineState {
  return timelineReducer(state, {
    type: "event_received",
    event: auditEvent,
  });
}

function event(runID: string, seq: number): AuditEvent {
  return auditEventSchema.parse({
    schema_version: 1,
    event_id: `${runID}:${seq}`,
    seq,
    ts: "2026-10-10T01:15:00Z",
    run_id: runID,
    actor: "system",
    type: seq === 1 ? "run_started" : "proposal_prepared",
    payload: {},
    prev_hash: "0".repeat(64),
    hash: "1".repeat(64),
  });
}
