import { describe, expect, it } from "vitest";
import type { HubOverview, RouteOverview } from "../api";
import {
  buildFacilityLayouts,
  sampleFacilityRoute,
} from "./facilityLayout";

function hub({
  id,
  capacity,
  inFlight,
  anomalies,
  handling = 0,
}: {
  id: string;
  capacity: number;
  inFlight: number;
  anomalies: number;
  handling?: number;
}): HubOverview {
  return {
    hub_id: id,
    name: `${id} 公路港`,
    province: "测试省",
    city: "测试市",
    longitude: 100 + capacity / 1_000,
    latitude: 25 + capacity / 2_000,
    daily_capacity: capacity,
    waybills: Math.max(inFlight, anomalies, 1),
    in_flight: inFlight,
    anomalies,
    handling,
    closed: 0,
  };
}

function route(
  hubID: string,
  distance: number,
): RouteOverview {
  return {
    route_id: `ROUTE-${hubID}`,
    origin_hub_id: hubID,
    destination_hub_id: `OUT-${hubID}`,
    distance_km: distance,
    standard_hours: Math.ceil(distance / 70),
    waybills: 1,
    anomalies: 0,
    delay_heat: 0,
    max_risk: 0,
  };
}

const hubs = [
  hub({ id: "COURTYARD", capacity: 1_000, inFlight: 0, anomalies: 0 }),
  hub({ id: "GATEWAY", capacity: 5_000, inFlight: 0, anomalies: 0 }),
  hub({ id: "LINEAR", capacity: 2_500, inFlight: 0, anomalies: 0 }),
  hub({ id: "CROSS", capacity: 2_000, inFlight: 10, anomalies: 0 }),
  hub({ id: "SPLIT", capacity: 3_000, inFlight: 1, anomalies: 10 }),
] satisfies readonly HubOverview[];

const routes = [
  route("COURTYARD", 500),
  route("GATEWAY", 600),
  route("LINEAR", 5_000),
  route("CROSS", 700),
  route("SPLIT", 800),
] satisfies readonly RouteOverview[];

describe("buildFacilityLayouts", () => {
  it("builds all five topologies from operating signals", () => {
    const layouts = buildFacilityLayouts(hubs, routes);

    expect(
      Object.fromEntries(
        hubs.map((item) => [item.hub_id, layouts.get(item.hub_id)?.kind]),
      ),
    ).toEqual({
      COURTYARD: "courtyard",
      GATEWAY: "gateway",
      LINEAR: "linear",
      CROSS: "cross-dock",
      SPLIT: "split-yard",
    });
  });

  it("gives every hub a distinct stable geometry signature", () => {
    const forward = buildFacilityLayouts(hubs, routes);
    const reversed = buildFacilityLayouts(
      [...hubs].reverse(),
      [...routes].reverse(),
    );
    const forwardSignatures = hubs.map(
      (item) => forward.get(item.hub_id)?.signature,
    );

    expect(new Set(forwardSignatures).size).toBe(hubs.length);
    expect(
      Object.fromEntries(
        hubs.map((item) => [
          item.hub_id,
          forward.get(item.hub_id)?.signature,
        ]),
      ),
    ).toEqual(
      Object.fromEntries(
        hubs.map((item) => [
          item.hub_id,
          reversed.get(item.hub_id)?.signature,
        ]),
      ),
    );
  });

  it("connects the gate, dock, yard, and exit with stable local routes", () => {
    const layouts = buildFacilityLayouts(hubs, routes);

    for (const layout of layouts.values()) {
      expect(layout.transportRoutes.map((item) => item.kind)).toEqual([
        "gate-to-dock",
        "dock-to-yard",
        "yard-to-gate",
      ]);
      const [inbound, transfer, outbound] = layout.transportRoutes;
      expect(inbound).toBeDefined();
      expect(transfer).toBeDefined();
      expect(outbound).toBeDefined();
      if (
        inbound === undefined ||
        transfer === undefined ||
        outbound === undefined
      ) {
        continue;
      }
      expect(inbound.points.at(-1)).toEqual(transfer.points[0]);
      expect(transfer.points.at(-1)).toEqual(outbound.points[0]);
      expect(outbound.points.at(-1)).toEqual(inbound.points[0]);
      expect(layout.vehicles.length).toBeGreaterThan(0);
      expect(layout.vehicles.length).toBeLessThanOrEqual(6);
    }
  });

  it("derives vehicle states and route risk from operating data", () => {
    const activeHub = hub({
      id: "ACTIVE",
      capacity: 4_000,
      inFlight: 12,
      anomalies: 1,
      handling: 2,
    });
    const activeRoute = {
      ...route("ACTIVE", 1_200),
      anomalies: 2,
      delay_heat: 70,
      max_risk: 82,
    } satisfies RouteOverview;
    const layout = buildFacilityLayouts(
      [activeHub],
      [activeRoute],
    ).get(activeHub.hub_id);

    expect(layout?.transportRoutes.map((item) => item.status)).toEqual([
      "risk",
      "risk",
      "active",
    ]);
    expect(layout?.vehicles.map((vehicle) => vehicle.state)).toEqual([
      "alert",
      "loading",
      "loading",
      "moving",
      "moving",
      "moving",
    ]);
  });

  it("samples a vehicle position deterministically along the route", () => {
    const layout = buildFacilityLayouts(hubs, routes).get("CROSS");
    const route = layout?.transportRoutes[0];
    expect(route).toBeDefined();
    if (route === undefined) {
      return;
    }

    expect(sampleFacilityRoute(route, 0)).toEqual(
      sampleFacilityRoute(route, 1),
    );
    expect(sampleFacilityRoute(route, 0.35)).toEqual(
      sampleFacilityRoute(route, 0.35),
    );
    expect(sampleFacilityRoute(route, 0.35)).not.toEqual(
      sampleFacilityRoute(route, 0),
    );
  });
});
