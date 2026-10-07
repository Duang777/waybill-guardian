import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import type { TrackPoint } from "../api";
import {
  buildRouteLineFeature,
  buildRoutePointCollection,
  buildSmoothRoutePath,
  routeMapProvider,
  routePointStatus,
  RouteMap,
} from "./RouteMap";

vi.mock("../amap", () => ({
  hasAMapKey: () => false,
  loadAMap: () => Promise.reject(new Error("not used in local map tests")),
}));

const points = [
  {
    label: "沈阳公路港",
    recorded_at: "2026-10-10T09:06:00Z",
    longitude: 123.4315,
    latitude: 41.8057,
    speed_kph: 0,
    anomaly: false,
  },
  {
    label: "干线轨迹点 1",
    recorded_at: "2026-10-10T13:06:00Z",
    longitude: 122.1,
    latitude: 39.7,
    speed_kph: 66,
    anomaly: false,
  },
  {
    label: "沈阳至南昌中途节点",
    recorded_at: "2026-10-10T17:06:00Z",
    longitude: 119.3,
    latitude: 35.2,
    speed_kph: 0,
    stop_hours: 4,
    anomaly: true,
    anomaly_type: "loss",
  },
  {
    label: "干线轨迹点 3",
    recorded_at: "2026-10-10T21:06:00Z",
    longitude: 117.7,
    latitude: 31.9,
    speed_kph: 64,
    anomaly: false,
  },
  {
    label: "南昌公路港",
    recorded_at: "2026-10-11T01:06:00Z",
    longitude: 115.8582,
    latitude: 28.6829,
    speed_kph: 0,
    anomaly: false,
  },
] satisfies readonly TrackPoint[];

describe("RouteMap", () => {
  it("renders a smooth local route and the selected point coordinates", () => {
    const markup = renderToStaticMarkup(
      <RouteMap
        points={points}
        origin="沈阳"
        destination="南昌"
        resourceKind="ready"
        focusRequest={null}
      />,
    );

    expect(markup).toContain("<path");
    expect(markup).not.toContain("<polyline");
    expect(markup).toContain("N 35.2000°");
    expect(markup).toContain("E 119.3000°");
    expect(markup).toContain("本地轨迹示意");
    expect(markup).not.toContain("正在加载矢量路网");
    expect(markup).not.toContain("N 31.23°");
    expect(markup).not.toContain("E 121.47°");
  });

  it("labels points after an anomaly as unconfirmed instead of normal traffic", () => {
    expect(routePointStatus(points, 3)).toBe("异常后待确认");
    expect(routePointStatus(points, 1)).toBe("正常通行");
  });

  it("uses the local view unless AMap or OpenFreeMap is explicit", () => {
    expect(routeMapProvider(false, undefined)).toBeNull();
    expect(routeMapProvider(false, "")).toBeNull();
    expect(routeMapProvider(false, "openfreemap")).toBe("vector");
    expect(routeMapProvider(true, undefined)).toBe("amap");
  });
});

describe("vector route data", () => {
  it("preserves longitude-latitude order in the route line", () => {
    const feature = buildRouteLineFeature(points);

    expect(feature.geometry.coordinates).toEqual(
      points.map((point) => [point.longitude, point.latitude]),
    );
  });

  it("marks one selected point without losing anomaly metadata", () => {
    const collection = buildRoutePointCollection(points, 1);

    expect(
      collection.features.filter((feature) => feature.properties.selected),
    ).toHaveLength(1);
    expect(collection.features[1]?.properties).toEqual({
      index: 1,
      anomaly: false,
      selected: true,
    });
    expect(collection.features[2]?.properties.anomaly).toBe(true);
  });
});

describe("buildSmoothRoutePath", () => {
  it("is stable and interpolates a multi-point route", () => {
    const route = [
      { x: 8, y: 14 },
      { x: 28, y: 42 },
      { x: 54, y: 34 },
      { x: 92, y: 72 },
    ];

    const first = buildSmoothRoutePath(route);

    expect(first).toBe(buildSmoothRoutePath(route));
    expect(first).toBe(
      "M 8 14 C 11.333 18.667 20.333 38.667 28 42 " +
        "C 35.667 45.333 43.333 29 54 34 " +
        "C 64.667 39 85.667 65.667 92 72",
    );
  });

  it("handles empty, single-point, and two-point routes", () => {
    expect(buildSmoothRoutePath([])).toBe("");
    expect(buildSmoothRoutePath([{ x: 8, y: 14 }])).toBe("M 8 14");
    expect(
      buildSmoothRoutePath([
        { x: 8, y: 14 },
        { x: 92, y: 72 },
      ]),
    ).toBe("M 8 14 L 92 72");
  });
});
