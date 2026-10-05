import { describe, expect, it } from "vitest";
import type { HubOverview, RouteOverview } from "../api";
import { buildFacilityLayouts } from "./facilityLayout";

function hub({
  id,
  capacity,
  inFlight,
  anomalies,
}: {
  id: string;
  capacity: number;
  inFlight: number;
  anomalies: number;
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
    handling: 0,
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
});
