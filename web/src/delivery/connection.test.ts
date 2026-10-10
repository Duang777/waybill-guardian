import { describe, expect, it } from "vitest";
import {
  deliveryConnectionLabel,
  deliveryConnectionState,
  deliveryDecisionUnavailableReason,
} from "./connection";

describe("delivery connection state", () => {
  it("distinguishes browser offline from stream reconnecting", () => {
    expect(
      deliveryConnectionState({
        runState: "awaiting_approval",
        transport: "reconnecting",
        browserOnline: false,
      }),
    ).toBe("offline");
    expect(
      deliveryConnectionState({
        runState: "awaiting_approval",
        transport: "reconnecting",
        browserOnline: true,
      }),
    ).toBe("reconnecting");
  });

  it("seals terminal and manual-review runs", () => {
    expect(
      deliveryConnectionState({
        runState: "completed",
        transport: "closed",
        browserOnline: true,
      }),
    ).toBe("sealed");
    expect(
      deliveryConnectionState({
        runState: "manual_review",
        transport: "online",
        browserOnline: true,
      }),
    ).toBe("sealed");
    expect(deliveryConnectionLabel("sealed")).toBe("审计已固化");
  });

  it("allows approval only while the revision stream is online", () => {
    expect(deliveryDecisionUnavailableReason("online")).toBeNull();
    expect(deliveryDecisionUnavailableReason("reconnecting")).toContain(
      "锁定审批",
    );
    expect(deliveryDecisionUnavailableReason("offline")).toContain("恢复联网");
  });
});
