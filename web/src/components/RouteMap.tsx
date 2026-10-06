import { Compass, MapPin } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type {
  Feature,
  FeatureCollection,
  LineString,
  Point,
} from "geojson";
import type {
  ErrorEvent as MapLibreErrorEvent,
  Map as MapLibreMap,
  MapLayerMouseEvent,
} from "maplibre-gl";
import { hasAMapKey, loadAMap } from "../amap";
import type { TrackPoint } from "../api";
import styles from "../app.module.css";
import type { WaybillResource } from "../waybill-resource";

const vectorMapStyleURL = "https://tiles.openfreemap.org/styles/positron";
const vectorMapTimeoutMS = 10_000;
const routeSourceID = "waybill-route";
const completedRouteSourceID = "waybill-route-completed";
const remainingRouteSourceID = "waybill-route-remaining";
const routePointSourceID = "waybill-route-points";
const routePointLayerID = "waybill-route-points-layer";

type RouteMapProps = {
  points: readonly TrackPoint[];
  origin: string | null;
  destination: string | null;
  resourceKind: WaybillResource["kind"];
};

type MapProvider = "amap" | "vector";

type MapMode =
  | { kind: "loading"; provider: MapProvider }
  | { kind: "amap" }
  | { kind: "vector" }
  | { kind: "fallback"; provider: MapProvider; reason: string };

type MapController =
  | {
      kind: "amap";
      destroy: () => void;
    }
  | {
      kind: "vector";
      destroy: () => void;
      select: (index: number) => void;
    };

type ProjectedPoint = TrackPoint & {
  x: number;
  y: number;
};

type Coordinate = [longitude: number, latitude: number];

type PointSelection = {
  points: readonly TrackPoint[];
  index: number;
};

type CanvasPoint = {
  x: number;
  y: number;
};

export function RouteMap({
  points,
  origin,
  destination,
  resourceKind,
}: RouteMapProps) {
  const mapElement = useRef<HTMLDivElement>(null);
  const mapController = useRef<MapController | null>(null);
  const preferredIndex = preferredPointIndex(points);
  const [selection, setSelection] = useState<PointSelection>(() => ({
    points,
    index: preferredIndex,
  }));
  const selectedIndex =
    selection.points === points && selection.index < points.length
      ? selection.index
      : preferredIndex;
  const [mode, setMode] = useState<MapMode>(
    hasAMapKey()
      ? { kind: "loading", provider: "amap" }
      : { kind: "loading", provider: "vector" },
  );
  const selected = points[selectedIndex] ?? null;
  const projected = useMemo(() => projectPoints(points), [points]);

  useEffect(() => {
    const element = mapElement.current;
    if (element === null || points.length === 0) {
      return;
    }
    const provider: MapProvider = hasAMapKey() ? "amap" : "vector";
    let disposed = false;
    let controller: MapController | null = null;
    mapController.current = null;
    setMode({ kind: "loading", provider });

    const initialize =
      provider === "amap"
        ? initializeAMap({
            element,
            points,
            onSelect: (index) => setSelection({ points, index }),
          })
        : initializeVectorMap({
            element,
            points,
            selectedIndex,
            onSelect: (index) => setSelection({ points, index }),
          });
    void initialize
      .then((nextController) => {
        if (disposed) {
          nextController.destroy();
          return;
        }
        controller = nextController;
        mapController.current = nextController;
        setMode({ kind: nextController.kind });
      })
      .catch((error: unknown) => {
        if (!disposed) {
          setMode({
            kind: "fallback",
            provider,
            reason:
              error instanceof Error
                ? `${mapProviderLabel(provider)}加载失败：${error.message}`
                : `${mapProviderLabel(provider)}加载失败，已切换本地轨迹视图`,
          });
        }
      });
    return () => {
      disposed = true;
      controller?.destroy();
      if (mapController.current === controller) {
        mapController.current = null;
      }
    };
  }, [points]);

  useEffect(() => {
    const controller = mapController.current;
    if (controller?.kind === "vector") {
      controller.select(selectedIndex);
    }
  }, [mode.kind, selectedIndex]);

  if (points.length === 0) {
    return (
      <div className={styles.mapEmpty}>
        <MapPin aria-hidden="true" size={20} />
        <span>{emptyMapLabel(resourceKind)}</span>
      </div>
    );
  }

  const interactiveMapVisible = mode.kind === "amap" || mode.kind === "vector";

  return (
    <div className={styles.mapStage}>
      <div className={styles.mapViewport}>
        <div
          ref={mapElement}
          className={`${styles.mapCanvas} ${interactiveMapVisible ? styles.mapVisible : ""}`}
          aria-hidden={!interactiveMapVisible}
        />
        {!interactiveMapVisible && (
          <FallbackMap
            points={projected}
            origin={origin}
            destination={destination}
            selectedIndex={selectedIndex}
            onSelect={(index) => setSelection({ points, index })}
          />
        )}
        {mode.kind === "loading" && (
          <div className={styles.mapLoading}>{mapLoadingLabel(mode.provider)}</div>
        )}
        {mode.kind === "vector" && (
          <div className={styles.mapNotice}>
            <Compass aria-hidden="true" size={14} />
            <span>矢量路网</span>
          </div>
        )}
        {mode.kind === "fallback" && (
          <div className={styles.mapNotice} title={mode.reason}>
            <Compass aria-hidden="true" size={14} />
            <span>本地测绘模式</span>
          </div>
        )}
        {selected !== null && (
          <div className={styles.mapCoordinates} aria-hidden="true">
            <span>{formatCoordinate(selected.latitude, "N", "S")}</span>
            <span>{formatCoordinate(selected.longitude, "E", "W")}</span>
          </div>
        )}
      </div>
      {selected !== null && (
        <div className={styles.pointInspector} aria-live="polite">
          <div className={styles.pointInspectorLead}>
            <span className={styles.pointIndex}>
              {(selectedIndex + 1).toString().padStart(2, "0")}
              <small>/{points.length.toString().padStart(2, "0")}</small>
            </span>
            <div>
              <span className={styles.eyebrow}>
                {selected.anomaly ? "风险轨迹点" : "轨迹点"}
              </span>
              <strong>{selected.label}</strong>
            </div>
          </div>
          <dl>
            <div>
              <dt>记录时间</dt>
              <dd>{formatTimestamp(selected.recorded_at)}</dd>
            </div>
            <div>
              <dt>车速</dt>
              <dd>{selected.speed_kph} km/h</dd>
            </div>
            <div>
              <dt>节点状态</dt>
              <dd>
                {selected.stop_hours === undefined
                  ? "正常通行"
                  : `停留 ${selected.stop_hours} 小时`}
              </dd>
            </div>
          </dl>
          <div className={styles.pointInspectorStatus}>
            <span
              className={selected.anomaly ? styles.riskSignal : styles.normalSignal}
              aria-hidden="true"
            />
            <span className={styles.eyebrow}>当前轨迹点</span>
          </div>
        </div>
      )}
    </div>
  );
}

type InitializeMapOptions = {
  element: HTMLDivElement;
  points: readonly TrackPoint[];
  onSelect: (index: number) => void;
};

type InitializeVectorMapOptions = InitializeMapOptions & {
  selectedIndex: number;
};

type RoutePointProperties = {
  index: number;
  anomaly: boolean;
  selected: boolean;
};

async function initializeAMap({
  element,
  points,
  onSelect,
}: InitializeMapOptions): Promise<MapController> {
  const firstPoint = points[0];
  if (firstPoint === undefined) {
    throw new Error("轨迹点为空");
  }
  await loadAMap();
  const map = new AMap.Map(element, {
    viewMode: "2D",
    zoom: 5,
    center: coordinate(firstPoint),
    mapStyle: "amap://styles/whitesmoke",
  });
  const route = new AMap.Polyline({
    path: points.map(coordinate),
    strokeColor: "#31383d",
    strokeWeight: 5,
    strokeOpacity: 0.88,
    lineJoin: "round",
    lineCap: "round",
    showDir: true,
  });
  const markers = points.map((point, index) => {
    const marker = new AMap.CircleMarker({
      center: coordinate(point),
      radius: point.anomaly ? 10 : 6,
      strokeColor: point.anomaly ? "#ae6814" : "#ffffff",
      strokeWeight: point.anomaly ? 4 : 2,
      fillColor: point.anomaly ? "#dc8b24" : "#31383d",
      fillOpacity: 1,
      zIndex: point.anomaly ? 30 : 20,
    });
    marker.on("click", () => onSelect(index));
    return marker;
  });
  map.add([route, ...markers]);
  map.setFitView([route, ...markers], false, [56, 56, 56, 56], 12);
  return {
    kind: "amap",
    destroy: () => map.destroy(),
  };
}

async function initializeVectorMap({
  element,
  points,
  selectedIndex,
  onSelect,
}: InitializeVectorMapOptions): Promise<MapController> {
  const firstPoint = points[0];
  if (firstPoint === undefined) {
    throw new Error("轨迹点为空");
  }
  const maplibre = await import("maplibre-gl");
  const map = new maplibre.Map({
    container: element,
    style: vectorMapStyleURL,
    center: coordinate(firstPoint),
    zoom: 4,
    attributionControl: false,
    dragRotate: false,
    maplibreLogo: false,
    pitchWithRotate: false,
  });

  try {
    map.addControl(
      new maplibre.AttributionControl({
        compact: element.clientWidth < 520,
        customAttribution:
          '<a href="https://openfreemap.org/" target="_blank" rel="noopener">OpenFreeMap</a> · ' +
          '<a href="https://www.openstreetmap.org/copyright" target="_blank" rel="noopener">© OpenStreetMap</a>',
      }),
      "bottom-left",
    );
    map.addControl(
      new maplibre.NavigationControl({
        showCompass: false,
        showZoom: true,
      }),
      "top-right",
    );
    await waitForMapEvent(map, "load");

    map.addSource(routeSourceID, {
      type: "geojson",
      data: buildRouteLineFeature(points),
    });
    map.addLayer({
      id: "waybill-route-casing",
      type: "line",
      source: routeSourceID,
      layout: {
        "line-cap": "round",
        "line-join": "round",
      },
      paint: {
        "line-color": "#d2d9d6",
        "line-opacity": 0.92,
        "line-width": 9,
      },
    });
    map.addLayer({
      id: "waybill-route-base",
      type: "line",
      source: routeSourceID,
      layout: {
        "line-cap": "round",
        "line-join": "round",
      },
      paint: {
        "line-color": "#66757a",
        "line-opacity": 0.9,
        "line-width": 3,
      },
    });

    const progressIndex = routeProgressIndex(points);
    const completedPoints = points.slice(0, progressIndex + 1);
    if (completedPoints.length >= 2) {
      map.addSource(completedRouteSourceID, {
        type: "geojson",
        data: buildRouteLineFeature(completedPoints),
      });
      map.addLayer({
        id: "waybill-route-completed",
        type: "line",
        source: completedRouteSourceID,
        layout: {
          "line-cap": "round",
          "line-join": "round",
        },
        paint: {
          "line-color": "#147c76",
          "line-opacity": 1,
          "line-width": 4,
        },
      });
    }
    const remainingPoints = points.slice(progressIndex);
    if (remainingPoints.length >= 2) {
      map.addSource(remainingRouteSourceID, {
        type: "geojson",
        data: buildRouteLineFeature(remainingPoints),
      });
      map.addLayer({
        id: "waybill-route-remaining",
        type: "line",
        source: remainingRouteSourceID,
        layout: {
          "line-cap": "round",
          "line-join": "round",
        },
        paint: {
          "line-color": "#69777a",
          "line-dasharray": [1.5, 2],
          "line-opacity": 0.9,
          "line-width": 3,
        },
      });
    }

    map.addSource(routePointSourceID, {
      type: "geojson",
      data: buildRoutePointCollection(points, selectedIndex),
    });
    map.addLayer({
      id: "waybill-route-selected-point",
      type: "circle",
      source: routePointSourceID,
      filter: ["==", ["get", "selected"], true],
      paint: {
        "circle-color": [
          "case",
          ["get", "anomaly"],
          "#bd4634",
          "#147c76",
        ],
        "circle-opacity": 0.16,
        "circle-radius": 15,
      },
    });
    map.addLayer({
      id: routePointLayerID,
      type: "circle",
      source: routePointSourceID,
      paint: {
        "circle-color": [
          "case",
          ["get", "anomaly"],
          "#bd4634",
          "#28373d",
        ],
        "circle-radius": ["case", ["get", "anomaly"], 8, 5],
        "circle-stroke-color": "#ffffff",
        "circle-stroke-width": ["case", ["get", "anomaly"], 3, 2],
      },
    });

    const pointSource = map.getSource(routePointSourceID);
    if (!(pointSource instanceof maplibre.GeoJSONSource)) {
      throw new Error("轨迹点图层初始化失败");
    }
    const handlePointClick = (event: MapLayerMouseEvent) => {
      const index = event.features?.[0]?.properties?.index;
      if (
        typeof index === "number" &&
        Number.isInteger(index) &&
        index >= 0 &&
        index < points.length
      ) {
        onSelect(index);
      }
    };
    map.on("click", routePointLayerID, handlePointClick);
    map.on("mouseenter", routePointLayerID, () => {
      map.getCanvas().style.cursor = "pointer";
    });
    map.on("mouseleave", routePointLayerID, () => {
      map.getCanvas().style.cursor = "";
    });

    const bounds = new maplibre.LngLatBounds(
      coordinate(firstPoint),
      coordinate(firstPoint),
    );
    for (const point of points.slice(1)) {
      bounds.extend(coordinate(point));
    }
    const compact = element.clientWidth < 520;
    map.fitBounds(bounds, {
      padding: compact
        ? { top: 52, right: 28, bottom: 38, left: 28 }
        : { top: 48, right: 54, bottom: 42, left: 54 },
      maxZoom: 7.2,
      duration: 0,
    });

    return {
      kind: "vector",
      select: (index) => {
        void pointSource.setData(buildRoutePointCollection(points, index));
      },
      destroy: () => map.remove(),
    };
  } catch (error) {
    map.remove();
    throw error;
  }
}

function waitForMapEvent(
  map: MapLibreMap,
  eventName: "load",
): Promise<void> {
  return new Promise((resolve, reject) => {
    const timeout = window.setTimeout(() => {
      cleanup();
      reject(new Error("连接底图服务超时"));
    }, vectorMapTimeoutMS);
    const handleReady = () => {
      cleanup();
      resolve();
    };
    const handleError = (event: MapLibreErrorEvent) => {
      cleanup();
      reject(new Error(event.error.message));
    };
    const cleanup = () => {
      window.clearTimeout(timeout);
      map.off(eventName, handleReady);
      map.off("error", handleError);
    };
    map.on(eventName, handleReady);
    map.on("error", handleError);
  });
}

export function buildRouteLineFeature(
  points: readonly TrackPoint[],
): Feature<LineString> {
  return {
    type: "Feature",
    properties: {},
    geometry: {
      type: "LineString",
      coordinates: points.map(coordinate),
    },
  };
}

export function buildRoutePointCollection(
  points: readonly TrackPoint[],
  selectedIndex: number,
): FeatureCollection<Point, RoutePointProperties> {
  return {
    type: "FeatureCollection",
    features: points.map((point, index) => ({
      type: "Feature",
      properties: {
        index,
        anomaly: point.anomaly,
        selected: index === selectedIndex,
      },
      geometry: {
        type: "Point",
        coordinates: coordinate(point),
      },
    })),
  };
}

function routeProgressIndex(points: readonly TrackPoint[]): number {
  const anomaly = points.findIndex((point) => point.anomaly);
  return anomaly >= 0 ? anomaly : Math.max(0, points.length - 1);
}

function mapProviderLabel(provider: MapProvider): string {
  return provider === "amap" ? "高德地图" : "矢量路网";
}

function mapLoadingLabel(provider: MapProvider): string {
  return provider === "amap" ? "正在连接高德地图" : "正在加载矢量路网";
}

function emptyMapLabel(kind: WaybillResource["kind"]): string {
  switch (kind) {
    case "empty":
      return "暂无运单轨迹";
    case "loading":
      return "正在读取运单轨迹";
    case "error":
      return "运单轨迹加载失败";
    case "ready":
      return "当前运单暂无轨迹";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

type FallbackMapProps = {
  points: readonly ProjectedPoint[];
  origin: string | null;
  destination: string | null;
  selectedIndex: number;
  onSelect: (index: number) => void;
};

function FallbackMap({
  points,
  origin,
  destination,
  selectedIndex,
  onSelect,
}: FallbackMapProps) {
  const route = buildSmoothRoutePath(points);
  const firstPoint = points[0];
  const lastPoint = points.at(-1);
  return (
    <div className={styles.localMap}>
      <div className={styles.mapGrid} aria-hidden="true" />
      <div className={styles.mapTopography} aria-hidden="true" />
      {origin !== null && firstPoint !== undefined && (
        <span
          className={`${styles.routeEndpointLabel} ${styles.routeOriginLabel}`}
          style={{ left: `${firstPoint.x}%`, top: `${firstPoint.y}%` }}
        >
          <small>起点</small>
          {origin}
        </span>
      )}
      {destination !== null && lastPoint !== undefined && (
        <span
          className={`${styles.routeEndpointLabel} ${styles.routeDestinationLabel}`}
          style={{ left: `${lastPoint.x}%`, top: `${lastPoint.y}%` }}
        >
          <small>终点</small>
          {destination}
        </span>
      )}
      <svg
        className={styles.routeLine}
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <path
          className={styles.routeCorridor}
          d={route}
          vectorEffect="non-scaling-stroke"
        />
        <path
          className={styles.routePath}
          d={route}
          vectorEffect="non-scaling-stroke"
        />
      </svg>
      {points.map((point, index) => (
        <button
          className={`${styles.routePoint} ${
            point.anomaly ? styles.routePointAnomaly : ""
          } ${selectedIndex === index ? styles.routePointSelected : ""}`}
          style={{ left: `${point.x}%`, top: `${point.y}%` }}
          type="button"
          aria-label={`查看${point.label}轨迹点`}
          title={`${point.label} · ${point.speed_kph} km/h`}
          key={`${point.label}-${point.recorded_at}`}
          onClick={() => onSelect(index)}
        >
          <span />
        </button>
      ))}
    </div>
  );
}

function coordinate(point: TrackPoint): Coordinate {
  return [point.longitude, point.latitude];
}

function preferredPointIndex(points: readonly TrackPoint[]): number {
  const anomaly = points.findIndex((point) => point.anomaly);
  return anomaly >= 0 ? anomaly : 0;
}

function projectPoints(points: readonly TrackPoint[]): readonly ProjectedPoint[] {
  if (points.length === 0) {
    return [];
  }
  const longitudes = points.map((point) => point.longitude);
  const latitudes = points.map((point) => point.latitude);
  const minimumLongitude = Math.min(...longitudes);
  const maximumLongitude = Math.max(...longitudes);
  const minimumLatitude = Math.min(...latitudes);
  const maximumLatitude = Math.max(...latitudes);
  const longitudeRange = maximumLongitude - minimumLongitude || 1;
  const latitudeRange = maximumLatitude - minimumLatitude || 1;
  return points.map((point) => ({
    ...point,
    x: 8 + ((point.longitude - minimumLongitude) / longitudeRange) * 84,
    y: 14 + ((maximumLatitude - point.latitude) / latitudeRange) * 58,
  }));
}

export function buildSmoothRoutePath(points: readonly CanvasPoint[]): string {
  const first = points[0];
  if (first === undefined) {
    return "";
  }
  const move = `M ${formatPathNumber(first.x)} ${formatPathNumber(first.y)}`;
  if (points.length === 1) {
    return move;
  }
  if (points.length === 2) {
    const last = points[1];
    return `${move} L ${formatPathNumber(last.x)} ${formatPathNumber(last.y)}`;
  }

  const commands = [move];
  for (let index = 0; index < points.length - 1; index++) {
    const previous = points[Math.max(0, index - 1)] ?? first;
    const current = points[index] ?? first;
    const next = points[index + 1] ?? current;
    const afterNext = points[Math.min(points.length - 1, index + 2)] ?? next;
    const firstControl = {
      x: current.x + (next.x - previous.x) / 6,
      y: current.y + (next.y - previous.y) / 6,
    };
    const secondControl = {
      x: next.x - (afterNext.x - current.x) / 6,
      y: next.y - (afterNext.y - current.y) / 6,
    };
    commands.push(
      `C ${formatPathNumber(firstControl.x)} ${formatPathNumber(firstControl.y)} ` +
        `${formatPathNumber(secondControl.x)} ${formatPathNumber(secondControl.y)} ` +
        `${formatPathNumber(next.x)} ${formatPathNumber(next.y)}`,
    );
  }
  return commands.join(" ");
}

function formatPathNumber(value: number): string {
  return Number(value.toFixed(3)).toString();
}

function formatCoordinate(
  value: number,
  positiveDirection: "N" | "E",
  negativeDirection: "S" | "W",
): string {
  const direction = value >= 0 ? positiveDirection : negativeDirection;
  return `${direction} ${Math.abs(value).toFixed(4)}°`;
}

function formatTimestamp(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
    timeZone: "Asia/Shanghai",
  }).format(new Date(value));
}
