import { describe, expect, it } from "vitest";
import {
  waybillIdSchema,
  type AnomalyOverview,
  type HubOverview,
  type RouteOverview,
} from "../api";
import { buildSceneModel } from "./HubNetworkScene";

const hubs = [
  {
    hub_id: "HUB-A",
    name: "甲港",
    province: "甲省",
    city: "甲市",
    longitude: 110,
    latitude: 30,
    daily_capacity: 1_000,
    waybills: 100_000,
    in_flight: 100_000,
    anomalies: 2,
    handling: 0,
    closed: 0,
  },
  {
    hub_id: "HUB-B",
    name: "乙港",
    province: "乙省",
    city: "乙市",
    longitude: 120,
    latitude: 35,
    daily_capacity: 1_000,
    waybills: 100_000,
    in_flight: 100_000,
    anomalies: 2,
    handling: 0,
    closed: 0,
  },
] satisfies readonly HubOverview[];

const routes = [
  {
    route_id: "ROUTE-A-B",
    origin_hub_id: "HUB-A",
    destination_hub_id: "HUB-B",
    distance_km: 1_000,
    standard_hours: 16,
    waybills: 100_000,
    anomalies: 2,
    delay_heat: 1,
    max_risk: 90,
  },
] satisfies readonly RouteOverview[];

function anomaly(
  waybillID: "YD2026100001" | "YD2026100002",
  riskScore: number,
): AnomalyOverview {
  return {
    waybill_id: waybillIdSchema.parse(waybillID),
    origin: "甲市",
    destination: "乙市",
    origin_hub_id: "HUB-A",
    destination_hub_id: "HUB-B",
    route_id: "ROUTE-A-B",
    type: "delay",
    label: "延误",
    last_recorded_at: "2026-10-05T00:00:00Z",
    risk: {
      eta_delay: riskScore,
      road: riskScore,
      weather: riskScore,
    },
    risk_score: riskScore,
  };
}

describe("buildSceneModel", () => {
  it("caps flow markers for a high-volume route", () => {
    const model = buildSceneModel(hubs, routes, []);

    expect(model.markers).toHaveLength(4);
  });

  it("keeps the highest-risk anomaly for route drilldown", () => {
    const lowerRisk = anomaly("YD2026100001", 45);
    const higherRisk = anomaly("YD2026100002", 90);

    const model = buildSceneModel(hubs, routes, [
      higherRisk,
      lowerRisk,
    ]);

    expect(model.routes[0]?.anomaly?.waybill_id).toBe(higherRisk.waybill_id);
  });
});
