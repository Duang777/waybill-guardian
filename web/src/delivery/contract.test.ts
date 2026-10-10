import { describe, expect, it } from "vitest";
import { deliveryWorkspaceSchema } from "./contract";
import {
  deliveryWorkspaceFixture,
  deliveryWorkspaceStateFixture,
} from "./test-fixture";

describe("deliveryWorkspaceSchema", () => {
  it("parses a complete versioned workspace", () => {
    const workspace = deliveryWorkspaceFixture();

    expect(workspace.schema_version).toBe("delivery.workspace.v1");
    expect(workspace.plan.schema_version).toBe("delivery.plan.v1");
    expect(workspace.validation.schema_version).toBe(
      "delivery.validation.v1",
    );
    expect(workspace.last_event_seq).toBe(4);
    expect(workspace.comparison.kind).toBe("available");
  });

  it("rejects fields outside the versioned contract", () => {
    const raw = {
      ...deliveryWorkspaceFixture(),
      browser_computed_route: true,
    };

    expect(deliveryWorkspaceSchema.safeParse(raw).success).toBe(false);
  });

  it("rejects a validation report for another plan", () => {
    const raw = structuredClone(deliveryWorkspaceFixture());
    raw.validation.plan_digest = "0".repeat(64);

    const parsed = deliveryWorkspaceSchema.safeParse(raw);

    expect(parsed.success).toBe(false);
    if (!parsed.success) {
      expect(parsed.error.issues.map((issue) => issue.message)).toContain(
        "validation report does not belong to this plan",
      );
    }
  });

  it("rejects execution before approval confirmation", () => {
    const raw = structuredClone(deliveryWorkspaceFixture());
    raw.execution = {
      kind: "running",
      execution_id: "EXECUTION-01",
      started_at: "2026-10-10T07:43:00Z",
      updated_at: "2026-10-10T07:43:10Z",
      effects: [
        {
          effect_id: "EFFECT-01",
          action: "tms.bind_dispatch_plan",
          target: "PLAN-HZ-1010",
          summary: "绑定配送计划",
          state: "dispatching",
        },
      ],
    };

    const parsed = deliveryWorkspaceSchema.safeParse(raw);

    expect(parsed.success).toBe(false);
    if (!parsed.success) {
      expect(parsed.error.issues.map((issue) => issue.message)).toContain(
        "execution requires a confirmed approval",
      );
    }
  });

  it("rejects a broken audit sequence", () => {
    const raw = structuredClone(deliveryWorkspaceFixture());
    const event = raw.audit[2];
    if (event === undefined) {
      throw new Error("fixture requires a third audit event");
    }
    event.seq = 9;

    expect(deliveryWorkspaceSchema.safeParse(raw).success).toBe(false);
  });

  it("rejects a snapshot cursor that does not match the audit tail", () => {
    const raw = structuredClone(deliveryWorkspaceFixture());
    raw.last_event_seq = 3;

    const parsed = deliveryWorkspaceSchema.safeParse(raw);

    expect(parsed.success).toBe(false);
    if (!parsed.success) {
      expect(parsed.error.issues.map((issue) => issue.message)).toContain(
        "workspace event cursor must match the audit tail",
      );
    }
  });

  it("requires reconciliation entries for every unknown effect", () => {
    const raw = structuredClone(
      deliveryWorkspaceStateFixture("reconciliation"),
    );
    if (raw.execution.kind !== "reconciliation_required") {
      throw new Error("fixture requires reconciliation");
    }
    raw.execution.reconciliation = [];

    expect(deliveryWorkspaceSchema.safeParse(raw).success).toBe(false);
  });

  it("requires partial execution to preserve both outcomes", () => {
    const raw = structuredClone(deliveryWorkspaceStateFixture("partial"));
    if (raw.execution.kind !== "partial") {
      throw new Error("fixture requires partial execution");
    }
    raw.execution.effects.forEach((effect) => {
      effect.state = "succeeded";
    });

    const parsed = deliveryWorkspaceSchema.safeParse(raw);

    expect(parsed.success).toBe(false);
    if (!parsed.success) {
      expect(parsed.error.issues.map((issue) => issue.message)).toContain(
        "partial execution requires succeeded and failed effects",
      );
    }
  });

  it("rejects orphaned route and loading references", () => {
    const raw = structuredClone(deliveryWorkspaceFixture());
    const duty = raw.plan.duties[0];
    const trip = duty?.trips[0];
    const stop = trip?.stops[0];
    const stage = trip?.load_stages[0];
    const placement = stage?.placements[0];
    if (trip === undefined || stop === undefined || placement === undefined) {
      throw new Error("fixture requires a trip and placement");
    }
    stop.location_id = "LOC-UNKNOWN";
    placement.cargo_id = "CARGO-UNKNOWN";

    const parsed = deliveryWorkspaceSchema.safeParse(raw);

    expect(parsed.success).toBe(false);
    if (!parsed.success) {
      expect(parsed.error.issues.map((issue) => issue.message)).toEqual(
        expect.arrayContaining([
          "plan stop references an unknown location",
          "placement references unknown cargo",
        ]),
      );
    }
  });
});
