import { Compass, MapPin } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { hasAMapKey, loadAMap } from "../amap";
import type { TrackPoint } from "../api";
import styles from "../app.module.css";
import type { WaybillResource } from "../waybill-resource";

type RouteMapProps = {
  points: readonly TrackPoint[];
  origin: string | null;
  destination: string | null;
  resourceKind: WaybillResource["kind"];
};

type MapMode =
  | { kind: "loading" }
  | { kind: "amap" }
  | { kind: "fallback"; reason: string };

type ProjectedPoint = TrackPoint & {
  x: number;
  y: number;
};

type Coordinate = [longitude: number, latitude: number];

export function RouteMap({
  points,
  origin,
  destination,
  resourceKind,
}: RouteMapProps) {
  const mapElement = useRef<HTMLDivElement>(null);
  const [selectedIndex, setSelectedIndex] = useState(0);
  const [mode, setMode] = useState<MapMode>(
    hasAMapKey()
      ? { kind: "loading" }
      : { kind: "fallback", reason: "未配置高德 key，已切换本地轨迹视图" },
  );
  const selected = points[selectedIndex] ?? null;
  const projected = useMemo(() => projectPoints(points), [points]);

  useEffect(() => {
    const anomaly = points.findIndex((point) => point.anomaly);
    setSelectedIndex(anomaly >= 0 ? anomaly : 0);
  }, [points]);

  useEffect(() => {
    if (!hasAMapKey() || mapElement.current === null || points.length === 0) {
      return;
    }
    let disposed = false;
    let map: AMap.Map | null = null;
    setMode({ kind: "loading" });
    void loadAMap()
      .then(() => {
        if (disposed || mapElement.current === null) {
          return;
        }
        map = new AMap.Map(mapElement.current, {
          viewMode: "2D",
          zoom: 5,
          center: [points[0].longitude, points[0].latitude],
          mapStyle: "amap://styles/whitesmoke",
        });
        const path = points.map(coordinate);
        const route = new AMap.Polyline({
          path,
          strokeColor: "#31383d",
          strokeWeight: 5,
          strokeOpacity: 0.88,
          lineJoin: "round",
          lineCap: "round",
          showDir: true,
        });
        const markers = points.map((point, index) => {
          const marker = new AMap.CircleMarker({
            center: [point.longitude, point.latitude],
            radius: point.anomaly ? 10 : 6,
            strokeColor: point.anomaly ? "#ae6814" : "#ffffff",
            strokeWeight: point.anomaly ? 4 : 2,
            fillColor: point.anomaly ? "#dc8b24" : "#31383d",
            fillOpacity: 1,
            zIndex: point.anomaly ? 30 : 20,
          });
          marker.on("click", () => setSelectedIndex(index));
          return marker;
        });
        map.add([route, ...markers]);
        map.setFitView([route, ...markers], false, [56, 56, 56, 56], 12);
        setMode({ kind: "amap" });
      })
      .catch((error: unknown) => {
        if (!disposed) {
          setMode({
            kind: "fallback",
            reason:
              error instanceof Error
                ? `高德地图加载失败：${error.message}`
                : "高德地图加载失败，已切换本地轨迹视图",
          });
        }
      });
    return () => {
      disposed = true;
      map?.destroy();
    };
  }, [points]);

  if (points.length === 0) {
    return (
      <div className={styles.mapEmpty}>
        <MapPin aria-hidden="true" size={20} />
        <span>{emptyMapLabel(resourceKind)}</span>
      </div>
    );
  }

  return (
    <div className={styles.mapStage}>
      <div className={styles.mapViewport}>
        <div
          ref={mapElement}
          className={`${styles.amapCanvas} ${mode.kind === "amap" ? styles.mapVisible : ""}`}
          aria-hidden={mode.kind !== "amap"}
        />
        {mode.kind !== "amap" && (
          <FallbackMap
            points={projected}
            origin={origin}
            destination={destination}
            selectedIndex={selectedIndex}
            onSelect={setSelectedIndex}
          />
        )}
        {mode.kind === "loading" && (
          <div className={styles.mapLoading}>正在连接高德地图</div>
        )}
        {mode.kind === "fallback" && (
          <div className={styles.mapNotice} title={mode.reason}>
            <Compass aria-hidden="true" size={14} />
            <span>本地轨迹视图</span>
          </div>
        )}
        <div className={styles.mapCoordinates} aria-hidden="true">
          <span>N 31.23°</span>
          <span>E 121.47°</span>
        </div>
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
  const route = points.map((point) => `${point.x},${point.y}`).join(" ");
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
        <polyline
          className={styles.routeCorridor}
          points={route}
          vectorEffect="non-scaling-stroke"
        />
        <polyline
          className={styles.routePath}
          points={route}
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
