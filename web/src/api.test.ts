import { afterEach, describe, expect, it, vi } from "vitest";
import {
  getKPIs,
  listWaybills,
  startBatch,
  startRun,
  waybillIdSchema,
} from "./api";

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

  it("parses KPI availability instead of coercing missing values to zero", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          jsonResponse({
            window: "24h0m0s",
            as_of: "2026-10-12T04:24:00Z",
            assumptions: { evidence_step_minutes: 8 },
            metrics: [
              {
                key: "time_recovered_hours",
                label: "时效挽回",
                value: null,
                unit: "小时",
                availability: "unavailable",
                formula: "baseline - actual",
                reason: "missing baseline",
              },
              {
                key: "cost_impact_cny",
                label: "成本影响",
                value: null,
                unit: "元",
                availability: "unavailable",
                formula: "benefit - cost",
                reason: "missing cost fields",
              },
              {
                key: "labor_saved_hours",
                label: "人力节省",
                value: 2,
                unit: "小时",
                availability: "available",
                formula: "steps * minutes / 60",
              },
              {
                key: "anomaly_closure_rate_pct",
                label: "异常闭环率",
                value: 40,
                unit: "%",
                availability: "available",
                formula: "closed / anomalies",
              },
            ],
          }),
        ),
      ),
    );

    const report = await getKPIs();

    expect(report.metrics[0]?.value).toBeNull();
    expect(report.metrics[2]?.value).toBe(2);
  });

  it("posts batch IDs and parses independent run outcomes", async () => {
    const ids = [
      waybillIdSchema.parse("YD2026101041"),
      waybillIdSchema.parse("YD2026101042"),
    ];
    let requestInit: RequestInit | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
        requestInit = init;
        return Promise.resolve(
          jsonResponse(
            {
              requested: 2,
              accepted: 1,
              results: [
                {
                  waybill_id: ids[0],
                  run: {
                    run_id: "run-1",
                    incident_id: "incident-1",
                    waybill_id: ids[0],
                    status: "started",
                    last_seq: 1,
                  },
                },
                {
                  waybill_id: ids[1],
                  error: {
                    code: "run_capacity_reached",
                    message: "capacity reached",
                  },
                },
              ],
            },
            202,
          ),
        );
      }),
    );

    const response = await startBatch(ids);

    expect(requestInit?.body).toBe(JSON.stringify({ waybill_ids: ids }));
    expect(response.accepted).toBe(1);
    expect(response.results[1]?.error?.code).toBe("run_capacity_reached");
  });
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
