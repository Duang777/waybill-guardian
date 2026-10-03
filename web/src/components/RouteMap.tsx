import { AlertTriangle, MapPin } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { hasAMapKey, loadAMap } from "../amap";
import type { TrackPoint } from "../api";
import styles from "../app.module.css";

type RouteMapProps = {
  points: readonly TrackPoint[];
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

export function RouteMap({ points }: RouteMapProps) {
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
        <span>正在读取运单轨迹</span>
      </div>
    );
  }

  return (
    <div className={styles.mapStage}>
      <div
        ref={mapElement}
        className={`${styles.amapCanvas} ${mode.kind === "amap" ? styles.mapVisible : ""}`}
        aria-hidden={mode.kind !== "amap"}
      />
      {mode.kind !== "amap" && (
        <FallbackMap
          points={projected}
          selectedIndex={selectedIndex}
          onSelect={setSelectedIndex}
        />
      )}
      {mode.kind === "loading" && (
        <div className={styles.mapLoading}>正在连接高德地图</div>
      )}
      {mode.kind === "fallback" && (
        <div className={styles.mapNotice}>
          <AlertTriangle aria-hidden="true" size={14} />
          <span>{mode.reason}</span>
        </div>
      )}
      {selected !== null && (
        <div className={styles.pointInspector} aria-live="polite">
          <div>
            <span className={styles.eyebrow}>当前轨迹点</span>
            <strong>{selected.label}</strong>
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
              <dt>停留</dt>
              <dd>{selected.stop_hours === undefined ? "行驶中" : `${selected.stop_hours} 小时`}</dd>
            </div>
          </dl>
        </div>
      )}
    </div>
  );
}

type FallbackMapProps = {
  points: readonly ProjectedPoint[];
  selectedIndex: number;
  onSelect: (index: number) => void;
};

function FallbackMap({ points, selectedIndex, onSelect }: FallbackMapProps) {
  const route = points.map((point) => `${point.x},${point.y}`).join(" ");
  return (
    <div className={styles.localMap}>
      <div className={styles.mapGrid} aria-hidden="true" />
      <span className={styles.originLabel}>杭州</span>
      <span className={styles.destinationLabel}>成都</span>
      <svg
        className={styles.routeLine}
        viewBox="0 0 100 100"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <polyline points={route} vectorEffect="non-scaling-stroke" />
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
