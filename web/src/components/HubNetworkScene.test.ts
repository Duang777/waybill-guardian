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

function facilityHub({
  hubID,
  capacity,
  inFlight,
  anomalies: anomalyCount,
}: {
  hubID: string;
  capacity: number;
  inFlight: number;
  anomalies: number;
}): HubOverview {
  return {
    hub_id: hubID,
    name: `${hubID} 公路港`,
    province: "测试省",
    city: "测试市",
    longitude: 110 + capacity / 1_000,
    latitude: 30 + capacity / 2_000,
    daily_capacity: capacity,
    waybills: Math.max(inFlight, anomalyCount),
    in_flight: inFlight,
    anomalies: anomalyCount,
    handling: 0,
    closed: 0,
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

  it("builds four facility archetypes from capacity quartiles", () => {
    const model = buildSceneModel(
      [
        facilityHub({
          hubID: "HUB-LOCAL",
          capacity: 1_000,
          inFlight: 0,
          anomalies: 0,
        }),
        facilityHub({
          hubID: "HUB-REGIONAL",
          capacity: 2_000,
          inFlight: 1,
          anomalies: 1,
        }),
        facilityHub({
          hubID: "HUB-GATEWAY",
          capacity: 3_000,
          inFlight: 2,
          anomalies: 2,
        }),
        facilityHub({
          hubID: "HUB-YARD",
          capacity: 4_000,
          inFlight: 4,
          anomalies: 4,
        }),
      ],
      [],
      [],
    );

    expect(model.hubs.map((hub) => hub.archetype)).toEqual([
      "local-depot",
      "regional-cross-dock",
      "gateway-campus",
      "yard-terminal",
    ]);
    expect(model.hubs.map((hub) => hub.cargoSlots)).toEqual([0, 1, 2, 3]);
    expect(model.hubs[0]?.scale).toBeLessThan(model.hubs[3]?.scale ?? 0);
    expect(model.hubs[0]?.signalHeight).toBe(0);
    expect(model.hubs[3]?.signalHeight).toBeGreaterThan(0);
    expect(new Set(model.facilityParts.map((part) => part.kind))).toEqual(
      new Set(["pad", "structure", "roof", "cargo", "signal"]),
    );
  });

  it("keeps facility classification stable when hub input order changes", () => {
    const facilityHubs = [
      facilityHub({
        hubID: "HUB-LOCAL",
        capacity: 1_000,
        inFlight: 0,
        anomalies: 0,
      }),
      facilityHub({
        hubID: "HUB-REGIONAL",
        capacity: 2_000,
        inFlight: 1,
        anomalies: 1,
      }),
      facilityHub({
        hubID: "HUB-GATEWAY",
        capacity: 3_000,
        inFlight: 2,
        anomalies: 2,
      }),
      facilityHub({
        hubID: "HUB-YARD",
        capacity: 4_000,
        inFlight: 4,
        anomalies: 4,
      }),
    ] satisfies readonly HubOverview[];

    const forward = buildSceneModel(facilityHubs, [], []);
    const reversed = buildSceneModel([...facilityHubs].reverse(), [], []);
    const summarize = (
      model: ReturnType<typeof buildSceneModel>,
    ) =>
      Object.fromEntries(
        model.hubs.map((hub) => [
          hub.hub.hub_id,
          {
            archetype: hub.archetype,
            scale: hub.scale,
            cargoSlots: hub.cargoSlots,
            signalHeight: hub.signalHeight,
            rotationY: hub.rotationY,
          },
        ]),
      );

    expect(summarize(reversed)).toEqual(summarize(forward));
  });
});
