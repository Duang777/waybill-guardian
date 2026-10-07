import { describe, expect, it } from "vitest";
import { runIdSchema } from "./api";
import {
  decisionUnavailableReason,
  workbenchConnectionState,
} from "./workbench-connection";

const runID = runIdSchema.parse("run-connection");

describe("workbench connection state", () => {
  it("keeps terminal runs sealed after the stream closes", () => {
    expect(
      workbenchConnectionState({
        runID,
        runStatus: "completed",
        timelineConnection: "closed",
        browserOnline: true,
      }),
    ).toBe("sealed");
  });

  it("distinguishes browser offline from EventSource reconnecting", () => {
    expect(
      workbenchConnectionState({
        runID,
        runStatus: "awaiting_approval",
        timelineConnection: "reconnecting",
        browserOnline: false,
      }),
    ).toBe("offline");
    expect(
      workbenchConnectionState({
        runID,
        runStatus: "awaiting_approval",
        timelineConnection: "reconnecting",
        browserOnline: true,
      }),
    ).toBe("reconnecting");
  });

  it("allows approval only while the audit stream is online", () => {
    expect(decisionUnavailableReason("online")).toBeNull();
    expect(decisionUnavailableReason("reconnecting")).toContain("锁定审批");
    expect(decisionUnavailableReason("offline")).toContain("恢复联网");
  });
});
