import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError } from "../api";
import {
  confirmDeliveryApproval,
  getDeliveryWorkspace,
  rejectDeliveryApproval,
} from "./api";
import { planRevisionIdSchema } from "./contract";
import { deliveryWorkspaceFixture } from "./test-fixture";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("delivery API", () => {
  it("loads and parses a versioned plan workspace", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(JSON.stringify(deliveryWorkspaceFixture()), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const workspace = await getDeliveryWorkspace(
      planRevisionIdSchema.parse("REV-HZ-1010-07"),
    );

    expect(workspace.plan.revision_id).toBe("REV-HZ-1010-07");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/delivery/plan-revisions/REV-HZ-1010-07/workspace",
      { signal: undefined },
    );
  });

  it("rejects an incompatible workspace version at the boundary", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(
          JSON.stringify({ schema_version: "delivery.workspace.v2" }),
          { status: 200 },
        ),
      ),
    );

    await expect(
      getDeliveryWorkspace(
        planRevisionIdSchema.parse("REV-HZ-1010-07"),
      ),
    ).rejects.toMatchObject<Partial<APIError>>({
      code: "invalid_contract",
    });
  });

  it("rejects a workspace for another plan revision", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn<typeof fetch>().mockResolvedValue(
        new Response(JSON.stringify(deliveryWorkspaceFixture()), {
          status: 200,
        }),
      ),
    );

    await expect(
      getDeliveryWorkspace(
        planRevisionIdSchema.parse("REV-HZ-1010-08"),
      ),
    ).rejects.toMatchObject<Partial<APIError>>({
      code: "invalid_contract",
      message: "调度服务返回了其他计划修订的数据",
    });
  });

  it("submits confirm and reject decisions without authority fields", async () => {
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(
      new Response(null, { status: 204 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await confirmDeliveryApproval("APPROVAL-01");
    await rejectDeliveryApproval("APPROVAL-02", "司机班次已变化");

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "/api/delivery/approvals/APPROVAL-01/confirm",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
        signal: undefined,
      },
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/delivery/approvals/APPROVAL-02/reject",
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ reason: "司机班次已变化" }),
        signal: undefined,
      },
    );
  });
});
