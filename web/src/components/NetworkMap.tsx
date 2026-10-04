import type { CSSProperties } from "react";
import type {
  AnomalyOverview,
  HubOverview,
  RouteOverview,
} from "../api";
import styles from "../overview.module.css";

type NetworkMapProps = {
  hubs: readonly HubOverview[];
  routes: readonly RouteOverview[];
  anomalies: readonly AnomalyOverview[];
};

const bounds = {
  minLongitude: 73,
  maxLongitude: 135,
  minLatitude: 18,
  maxLatitude: 54,
};

export function NetworkMap({ hubs, routes, anomalies }: NetworkMapProps) {
  const hubByID = new Map(hubs.map((hub) => [hub.hub_id, hub]));
  const anomalyByWaybill = new Map(
    anomalies.map((item) => [item.waybill_id, item]),
  );

  return (
    <div className={styles.networkMapFrame}>
      <svg
        className={styles.networkMap}
        viewBox="0 0 1000 560"
        role="img"
        aria-labelledby="network-map-title network-map-description"
        preserveAspectRatio="xMidYMid meet"
      >
        <title id="network-map-title">全国公路港异常网络</title>
        <desc id="network-map-description">
          展示公路港节点、运输线路与异常热度，橙红色线路代表异常集中。
        </desc>
        <defs>
          <pattern
            id="network-grid"
            width="80"
            height="80"
            patternUnits="userSpaceOnUse"
          >
            <path d="M 80 0 L 0 0 0 80" className={styles.mapGridLine} />
          </pattern>
        </defs>
        <rect width="1000" height="560" className={styles.mapCanvas} />
        <rect width="1000" height="560" fill="url(#network-grid)" />
        <path
          className={styles.mapOutline}
          d="M128 180L214 88L356 64L466 105L560 76L684 114L818 168L878 255L832 344L738 376L674 462L566 490L476 430L366 464L268 406L154 368L92 276Z"
        />
        <g className={styles.routeLayer}>
          {routes.map((route) => {
            const origin = hubByID.get(route.origin_hub_id);
            const destination = hubByID.get(route.destination_hub_id);
            if (origin === undefined || destination === undefined) {
              return null;
            }
            const from = project(origin);
            const to = project(destination);
            return (
              <line
                key={route.route_id}
                x1={from.x}
                y1={from.y}
                x2={to.x}
                y2={to.y}
                className={
                  route.anomalies > 0
                    ? styles.routeAnomaly
                    : styles.routeNormal
                }
                style={{ "--route-heat": route.delay_heat / 100 } as CSSProperties}
              >
                <title>
                  {route.route_id} · {route.anomalies} 单异常 · 风险{" "}
                  {route.max_risk}
                </title>
              </line>
            );
          })}
        </g>
        <g className={styles.hubLayer}>
          {hubs.map((hub) => {
            const point = project(hub);
            const focus =
              hub.focus_waybill_id === undefined
                ? undefined
                : anomalyByWaybill.get(hub.focus_waybill_id);
            const marker = (
              <>
                <circle
                  cx={point.x}
                  cy={point.y}
                  r={hub.anomalies > 0 ? 7 : 4}
                  className={
                    hub.anomalies > 0 ? styles.hubAnomaly : styles.hubNormal
                  }
                />
                {hub.anomalies >= 3 && (
                  <circle
                    cx={point.x}
                    cy={point.y}
                    r={12}
                    className={styles.hubPulse}
                  />
                )}
                <title>
                  {hub.name} · {hub.waybills} 单 · {hub.anomalies} 单异常
                </title>
              </>
            );
            if (focus === undefined) {
              return <g key={hub.hub_id}>{marker}</g>;
            }
            return (
              <a
                key={hub.hub_id}
                href={`/waybills/${encodeURIComponent(focus.waybill_id)}`}
                aria-label={`下钻 ${hub.name} 的高风险运单 ${focus.waybill_id}`}
              >
                {marker}
              </a>
            );
          })}
        </g>
      </svg>
      <div className={styles.mapLegend} aria-hidden="true">
        <span><i className={styles.legendHub} />公路港</span>
        <span><i className={styles.legendAnomaly} />异常节点</span>
        <span><i className={styles.legendRoute} />异常线路</span>
      </div>
    </div>
  );
}

function project(hub: HubOverview): { x: number; y: number } {
  const x =
    70 +
    ((hub.longitude - bounds.minLongitude) /
      (bounds.maxLongitude - bounds.minLongitude)) *
      860;
  const y =
    510 -
    ((hub.latitude - bounds.minLatitude) /
      (bounds.maxLatitude - bounds.minLatitude)) *
      450;
  return { x, y };
}
