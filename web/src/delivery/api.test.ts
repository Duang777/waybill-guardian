import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError } from "../api";
import {
  confirmDeliveryApproval,
  getDeliveryWorkspace,
  openDeliveryWorkspaceEvents,
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

  it("opens the revision stream after the snapshot cursor and deduplicates replay", () => {
    let source: FakeEventSource | undefined;
    class StubEventSource extends FakeEventSource {
      constructor(url: string | URL) {
        super(url);
        source = this;
      }
    }
    vi.stubGlobal("EventSource", StubEventSource);
    const onEvent = vi.fn();
    const onConnectionChange = vi.fn();
    const revisionID = planRevisionIdSchema.parse("REV-HZ-1010-07");

    const close = openDeliveryWorkspaceEvents(revisionID, 4, {
      onEvent,
      onConnectionChange,
      onError: vi.fn(),
    });
    source?.onopen?.(new Event("open"));
    source?.emit("approval_changed", streamEvent(5), "5");
    source?.emit("approval_changed", streamEvent(5), "5");
    source?.emit("execution_changed", streamEvent(6), "6");

    expect(source?.url).toBe(
      "/api/delivery/plan-revisions/REV-HZ-1010-07/events?after=4",
    );
    expect(onEvent).toHaveBeenCalledTimes(2);
    expect(onEvent.mock.calls.map(([event]) => event.seq)).toEqual([5, 6]);
    expect(onConnectionChange).toHaveBeenNthCalledWith(1, "connecting");
    expect(onConnectionChange).toHaveBeenCalledWith("online");

    close();
    expect(source?.closed).toBe(true);
    expect(onConnectionChange).toHaveBeenLastCalledWith("closed");
  });

  it("rejects stream events for another revision or mismatched SSE id", () => {
    let source: FakeEventSource | undefined;
    class StubEventSource extends FakeEventSource {
      constructor(url: string | URL) {
        super(url);
        source = this;
      }
    }
    vi.stubGlobal("EventSource", StubEventSource);
    const onEvent = vi.fn();
    const onError = vi.fn();

    openDeliveryWorkspaceEvents(
      planRevisionIdSchema.parse("REV-HZ-1010-07"),
      4,
      {
        onEvent,
        onConnectionChange: vi.fn(),
        onError,
      },
    );
    source?.emit(
      "approval_changed",
      {
        ...streamEvent(5),
        revision_id: "REV-HZ-1010-08",
      },
      "5",
    );
    source?.emit("approval_changed", streamEvent(5), "19");

    expect(onEvent).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledTimes(2);
  });
});

function streamEvent(seq: number) {
  return {
    schema_version: "delivery.workspace-event.v1",
    event_id: `DELIVERY-EVENT-${seq}`,
    seq,
    revision_id: "REV-HZ-1010-07",
    workspace_version: seq,
    occurred_at: "2026-10-10T07:45:00Z",
    kind: seq === 5 ? "approval_changed" : "execution_changed",
  };
}

class FakeEventSource {
  readonly listeners = new Map<string, EventListener[]>();
  readonly url: string;
  closed = false;
  onopen: ((event: Event) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;

  constructor(url: string | URL) {
    this.url = String(url);
  }

  addEventListener(type: string, listener: EventListener): void {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  emit(type: string, payload: unknown, lastEventID: string): void {
    const event = new MessageEvent(type, {
      data: JSON.stringify(payload),
      lastEventId: lastEventID,
    });
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event);
    }
  }

  close(): void {
    this.closed = true;
  }
}
