import { describe, expect, it } from "vitest";
import {
  runIdSchema,
  waybillIdSchema,
  type AnomalyOverview,
  type HubOverview,
  type RouteOverview,
  type RunStatus,
} from "./api";
import {
  buildOverviewChartModel,
  type OverviewChartSource,
} from "./overview-chart-model";
import type { RunProjection } from "./overview-run-projection";

const asOf = "2026-10-12T04:24:00Z";

describe("overview chart model", () => {
  it("merges anomaly types and preserves unknown business labels", () => {
    const model = buildOverviewChartModel({
      overview: source({
        anomaly_distribution: [
          { type: "delay", count: 2 },
          { type: "customs", count: 1 },
          { type: "delay", count: 3 },
          { type: "damage", count: 0 },
        ],
      }),
      runProjections: new Map(),
    });

    expect(model.charts[0].content).toEqual({
      kind: "ready",
      total: 6,
      items: [
        {
          key: "delay",
          label: "时效延误",
          count: 5,
          percentage: 83.3,
        },
        {
          key: "customs",
          label: "customs",
          count: 1,
          percentage: 16.7,
        },
      ],
    });
  });

  it("groups every run status and applies the live projection first", () => {
    const statuses: readonly (RunStatus | undefined)[] = [
      undefined,
      "started",
      "awaiting_approval",
      "executing",
      "completed",
      "rejected",
      "failed",
      "review_required",
      "manual_review",
    ];
    const anomalies = statuses.map((status, index) =>
      anomaly(index + 1, status),
    );
    const projectedWaybill = anomalies[1].waybill_id;
    const projection: RunProjection = {
      runID: runIdSchema.parse("run-projected"),
      status: "completed",
      lastSeq: 9,
    };
    const model = buildOverviewChartModel({
      overview: source({ anomalies }),
      runProjections: new Map([[projectedWaybill, projection]]),
    });

    expect(model.charts[1].content).toEqual({
      kind: "ready",
      total: 9,
      items: [
        {
          key: "unassigned",
          label: "待分派",
          count: 1,
          percentage: 11.1,
        },
        {
          key: "awaiting_approval",
          label: "待审批",
          count: 1,
          percentage: 11.1,
        },
        {
          key: "executing",
          label: "执行中",
          count: 1,
          percentage: 11.1,
        },
        {
          key: "completed",
          label: "已闭环",
          count: 2,
          percentage: 22.2,
        },
        {
          key: "rejected",
          label: "已驳回",
          count: 1,
          percentage: 11.1,
        },
        {
          key: "needs_attention",
          label: "需人工介入",
          count: 3,
          percentage: 33.3,
        },
      ],
    });
  });

  it("ranks five routes by anomaly rate and risk with readable labels", () => {
    const hubs = [
      hub("hub-a", "杭州"),
      hub("hub-b", "成都"),
      hub("hub-c", "武汉"),
    ];
    const routes = [
      route("route-f", "hub-a", "hub-b", 30, 99, 4, 13),
      route("route-b", "hub-a", "hub-b", 80, 88, 8, 10),
      route("route-a", "hub-a", "hub-c", 80, 88, 9, 12),
      route("route-c", "hub-a", "missing-hub", 80, 90, 5, 8),
      route("route-d", "hub-b", "hub-c", 70, 95, 6, 9),
      route("route-e", "hub-c", "hub-a", 60, 91, 3, 5),
      route("route-empty", "hub-a", "hub-b", 0, 0, 0, 12),
    ];
    const model = buildOverviewChartModel({
      overview: source({ hubs, routes }),
      runProjections: new Map(),
    });

    expect(model.charts[2].content).toEqual({
      kind: "ready",
      items: [
        {
          routeID: "route-c",
          label: "杭州 → missing-hub",
          anomalyRatePercentage: 80,
          anomalies: 5,
          waybills: 8,
          maxRisk: 90,
        },
        {
          routeID: "route-a",
          label: "杭州 → 武汉",
          anomalyRatePercentage: 80,
          anomalies: 9,
          waybills: 12,
          maxRisk: 88,
        },
        {
          routeID: "route-b",
          label: "杭州 → 成都",
          anomalyRatePercentage: 80,
          anomalies: 8,
          waybills: 10,
          maxRisk: 88,
        },
        {
          routeID: "route-d",
          label: "成都 → 武汉",
          anomalyRatePercentage: 70,
          anomalies: 6,
          waybills: 9,
          maxRisk: 95,
        },
        {
          routeID: "route-e",
          label: "武汉 → 杭州",
          anomalyRatePercentage: 60,
          anomalies: 3,
          waybills: 5,
          maxRisk: 91,
        },
      ],
    });
  });

  it("returns explicit empty variants instead of zero-valued series", () => {
    const model = buildOverviewChartModel({
      overview: source(),
      runProjections: new Map(),
    });

    expect(model.charts.map((chart) => chart.content.kind)).toEqual([
      "empty",
      "empty",
      "empty",
    ]);
  });
});

function source(
  overrides: Partial<OverviewChartSource> = {},
): OverviewChartSource {
  return {
    as_of: asOf,
    anomaly_distribution: [],
    anomalies: [],
    hubs: [],
    routes: [],
    ...overrides,
  };
}

function anomaly(
  index: number,
  status: RunStatus | undefined,
): AnomalyOverview {
  const base = {
    waybill_id: waybillIdSchema.parse(
      `YD20261010${String(index).padStart(2, "0")}`,
    ),
    origin: "杭州",
    destination: "成都",
    type: "delay",
    label: "延误",
    last_recorded_at: asOf,
    risk: { eta_delay: 80, road: 40, weather: 20 },
    risk_score: 56,
  };
  return status === undefined ? base : { ...base, run_status: status };
}

function hub(id: string, city: string): HubOverview {
  return {
    hub_id: id,
    name: `${city}公路港`,
    province: city,
    city,
    longitude: 0,
    latitude: 0,
    daily_capacity: 100,
    waybills: 10,
    in_flight: 8,
    anomalies: 2,
    handling: 1,
    closed: 1,
  };
}

function route(
  id: string,
  originHubID: string,
  destinationHubID: string,
  anomalyRatePercentage: number,
  maxRisk: number,
  anomalies: number,
  waybills: number,
): RouteOverview {
  return {
    route_id: id,
    origin_hub_id: originHubID,
    destination_hub_id: destinationHubID,
    distance_km: 1_000,
    standard_hours: 16,
    waybills,
    anomalies,
    delay_heat: anomalyRatePercentage,
    max_risk: maxRisk,
  };
}
