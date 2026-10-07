import { describe, expect, it } from "vitest";
import {
  pendingApprovalSummarySchema,
  runSummarySchema,
} from "./api";
import { decideRecovery } from "./recovery";

const activeRun = runSummarySchema.parse({
  run_id: "run-active",
  incident_id: "incident-active",
  waybill_id: "YD2026101041",
  status: "investigating",
  last_seq: 3,
  updated_at: "2026-10-11T09:30:00Z",
});

const pendingApproval = pendingApprovalSummarySchema.parse({
  id: "approval-pending",
  run_id: "run-pending",
  waybill_id: "YD2026101042",
  plan_version: 1,
  requested_at: "2026-10-11T09:31:00Z",
  expires_at: "2026-10-11T09:41:00Z",
});

describe("decideRecovery", () => {
  it("restores a pending approval before another active run", () => {
    expect(
      decideRecovery({
        pendingApprovals: { kind: "ready", data: [pendingApproval] },
        activeRuns: { kind: "ready", data: [activeRun] },
      }),
    ).toEqual({
      kind: "recover",
      runID: pendingApproval.run_id,
      waybillID: pendingApproval.waybill_id,
    });
  });

  it("uses an active run when the pending-approval query fails", () => {
    expect(
      decideRecovery({
        pendingApprovals: { kind: "error", message: "approvals unavailable" },
        activeRuns: { kind: "ready", data: [activeRun] },
      }),
    ).toEqual({
      kind: "recover",
      runID: activeRun.run_id,
      waybillID: activeRun.waybill_id,
    });
  });

  it("blocks a new selection when a failed query could hide a run", () => {
    expect(
      decideRecovery({
        pendingApprovals: { kind: "ready", data: [] },
        activeRuns: { kind: "error", message: "runs unavailable" },
      }),
    ).toEqual({ kind: "blocked", message: "runs unavailable" });
  });

  it("selects a waybill only after both recovery sources are empty", () => {
    expect(
      decideRecovery({
        pendingApprovals: { kind: "ready", data: [] },
        activeRuns: { kind: "ready", data: [] },
      }),
    ).toEqual({ kind: "select_waybill" });
  });
});
