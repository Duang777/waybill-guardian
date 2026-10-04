import { afterEach, describe, expect, it, vi } from "vitest";
import {
  approvalSchema,
  confirmApproval,
  getKPIs,
  listWaybills,
  startBatch,
  startRun,
  waybillIdSchema,
  waybillViewSchema,
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

  it("confirms an approval with an explicit empty JSON object", async () => {
    const approval = approvalSchema.parse({
      id: "approval-1",
      run_id: "run-1",
      sdk_run_id: "sdk-run-1",
      waybill_id: "YD2026101042",
      plan_version: 1,
      items: [
        {
          call_id: "call-1",
          action: "tms.reassign",
          wire_name: "tms.reassign",
          params: {},
          arguments_hash: "arguments-hash",
          idempotency_key: "idempotency-key",
        },
      ],
      reason: "driver fatigue",
      evidence: [],
      status: "executed",
      requested_at: "2026-10-11T13:06:00Z",
      expires_at: "2026-10-11T13:16:00Z",
      decided_by: "local-demo-reviewer",
      decided_at: "2026-10-11T13:07:00Z",
    });
    let requestInit: RequestInit | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
        requestInit = init;
        return Promise.resolve(jsonResponse(approval, 202));
      }),
    );

    await confirmApproval(approval.id);

    expect(requestInit?.method).toBe("POST");
    expect(requestInit?.headers).toEqual({ "Content-Type": "application/json" });
    expect(requestInit?.body).toBe("{}");
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

  it("accepts the zero-hour SLA allowed by the file contract", () => {
    const parsed = waybillViewSchema.parse({
      waybill: {
        waybill_id: "YD2026101042",
        origin: "宁波",
        destination: "西安",
        cargo: "工业传感器",
        carrier_id: "CARRIER-EAST-08",
        driver_id: "DRIVER-42",
        status: "delayed",
        sla_hours: 0,
        shipper_phone: "138****2468",
        candidate_carriers: [{
          carrier_id: "CARRIER-NW-11",
          name: "秦岭货运",
          eta_hours: 10,
          reliability_pct: 97,
        }],
      },
      tracking: [{
        label: "宁波集散中心",
        recorded_at: "2026-10-11T01:00:00Z",
        longitude: 121.55,
        latitude: 29.87,
        speed_kph: 0,
        anomaly: false,
      }],
      driver: {
        driver_id: "DRIVER-42",
        name: "李师傅",
        phone: "139****5678",
        plate: "浙B***42",
        continuous_drive_hours: 0,
        fatigue_alert: false,
      },
      weather: [{
        segment: "宁波-西安",
        condition: "晴",
        alert_level: "none",
      }],
      risk: { eta_delay: 0, road: 0, weather: 0 },
    });

    expect(parsed.waybill.sla_hours).toBe(0);
  });
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
