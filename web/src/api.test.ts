import { afterEach, describe, expect, it, vi } from "vitest";
import { listWaybills, startRun, waybillIdSchema } from "./api";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("waybill API", () => {
  it("parses the catalog at the network boundary", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          jsonResponse({
            waybills: [
              {
                waybill_id: "YD2026101042",
                origin: "宁波",
                destination: "西安",
                status: "delayed",
                has_anomaly: true,
                anomaly_label: "襄阳服务区",
                last_recorded_at: "2026-10-11T13:06:00Z",
              },
            ],
          }),
        ),
      ),
    );

    const catalog = await listWaybills();

    expect(catalog).toHaveLength(1);
    expect(catalog[0]?.waybill_id).toBe("YD2026101042");
    expect(catalog[0]?.origin).toBe("宁波");
  });

  it("accepts an empty catalog after authorization filtering", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(jsonResponse({ waybills: [] }))),
    );

    await expect(listWaybills()).resolves.toEqual([]);
  });

  it("posts the selected waybill and trusts the run returned by the server", async () => {
    let requestInput: RequestInfo | URL | null = null;
    let requestInit: RequestInit | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        requestInput = input;
        requestInit = init;
        return Promise.resolve(
          jsonResponse(
            {
              run_id: "run-authoritative",
              incident_id: "incident-authoritative",
              waybill_id: "YD2026101042",
              status: "started",
              last_seq: 1,
            },
            202,
          ),
        );
      }),
    );

    const selected = waybillIdSchema.parse("YD2026101041");
    const run = await startRun(selected);

    expect(requestInput).toBe("/api/runs");
    expect(requestInit?.method).toBe("POST");
    expect(requestInit?.headers).toEqual({ "Content-Type": "application/json" });
    expect(requestInit?.body).toBe(JSON.stringify({ waybill_id: selected }));
    expect(run.waybill_id).toBe("YD2026101042");
  });
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
