import { describe, expect, it } from "vitest";
import {
  pendingApprovalSummarySchema,
  runSummarySchema,
} from "./api";
import { preferredRecoveryRun } from "./recovery";

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

describe("preferredRecoveryRun", () => {
  it("restores a pending approval before another active run", () => {
    expect(
      preferredRecoveryRun({
        pendingApprovals: [pendingApproval],
        activeRuns: [activeRun],
      }),
    ).toBe(pendingApproval.run_id);
  });

  it("falls back to an active run and returns null when no run exists", () => {
    expect(
      preferredRecoveryRun({
        pendingApprovals: [],
        activeRuns: [activeRun],
      }),
    ).toBe(activeRun.run_id);
    expect(
      preferredRecoveryRun({
        pendingApprovals: [],
        activeRuns: [],
      }),
    ).toBeNull();
  });
});
