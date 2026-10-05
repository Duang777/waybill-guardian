import { useMemo, type CSSProperties } from "react";
import type {
  AnomalyOverview,
  HubOverview,
  RouteOverview,
} from "../api";
import styles from "../overview.module.css";
import {
  buildFacilityLayouts,
  type FacilityElementKind,
  type FacilityLayout,
} from "./facilityLayout";

type NetworkMapProps = {
  hubs: readonly HubOverview[];
  routes: readonly RouteOverview[];
  anomalies: readonly AnomalyOverview[];
  selectedHubID: string | null;
  onSelectHub: (hubID: string) => void;
};

const bounds = {
  minLongitude: 73,
  maxLongitude: 135,
  minLatitude: 18,
  maxLatitude: 54,
};

export function NetworkMap({
  hubs,
  routes,
  anomalies,
  selectedHubID,
  onSelectHub,
}: NetworkMapProps) {
  const hubByID = new Map(hubs.map((hub) => [hub.hub_id, hub]));
  const facilityLayouts = useMemo(
    () => buildFacilityLayouts(hubs, routes),
    [hubs, routes],
  );
  const selectedHub =
    selectedHubID === null ? undefined : hubByID.get(selectedHubID);
  const selectedLayout =
    selectedHubID === null ? undefined : facilityLayouts.get(selectedHubID);

  if (selectedHub !== undefined && selectedLayout !== undefined) {
    return <FacilityDetailMap hub={selectedHub} layout={selectedLayout} />;
  }

  return (
    <div className={styles.networkMapFrame} data-fallback-mode="network">
      <svg
        className={styles.networkMap}
        viewBox="0 0 1000 560"
        role="img"
        aria-labelledby="network-map-title network-map-description"
        preserveAspectRatio="xMidYMid meet"
      >
        <title id="network-map-title">全国公路港异常网络</title>
        <desc id="network-map-description">
          展示公路港节点、运输线路与 {anomalies.length} 个异常，
          橙红色线路代表异常集中。
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
            return (
              <g
                key={hub.hub_id}
                role="button"
                tabIndex={0}
                aria-label={`查看 ${hub.name} 园区`}
                onClick={() => onSelectHub(hub.hub_id)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    onSelectHub(hub.hub_id);
                  }
                }}
              >
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
              </g>
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

function FacilityDetailMap({
  hub,
  layout,
}: {
  hub: HubOverview;
  layout: FacilityLayout;
}) {
  const scale = Math.min(
    760 / layout.campusWidth,
    400 / layout.campusDepth,
  );
  return (
    <div
      className={styles.networkMapFrame}
      data-fallback-mode="facility"
      data-fallback-layout={layout.kind}
      data-fallback-signature={layout.signature}
    >
      <svg
        className={styles.networkMap}
        viewBox="0 0 1000 560"
        role="img"
        aria-labelledby="facility-map-title facility-map-description"
        preserveAspectRatio="xMidYMid meet"
      >
        <title id="facility-map-title">{hub.name}园区详情</title>
        <desc id="facility-map-description">
          {layout.label}，展示该园区的仓库、月台、货位、场内道路和告警塔。
        </desc>
        <rect width="1000" height="560" className={styles.mapCanvas} />
        {layout.elements.map((element, index) => {
          const centerX = 500 + element.x * scale;
          const centerY = 280 + element.z * scale;
          const width = Math.max(element.width * scale, 2);
          const height = Math.max(element.depth * scale, 2);
          return (
          <rect
              key={`${element.kind}-${index}`}
              x={centerX - width / 2}
              y={centerY - height / 2}
              width={width}
              height={height}
              rx={element.kind === "beacon" ? width / 2 : 0}
              className={fallbackElementClass(element.kind, hub)}
              transform={
                element.rotationY === 0
                  ? undefined
                  : `rotate(${element.rotationY * (180 / Math.PI)} ${centerX} ${centerY})`
              }
          />
          );
        })}
      </svg>
      <div className={styles.mapLegend} aria-hidden="true">
        <span><i className={styles.sceneWarehouseKey} />仓库</span>
        <span><i className={styles.sceneDockKey} />月台</span>
        <span><i className={styles.sceneCargoKey} />货位</span>
        <span><i className={styles.sceneRiskKey} />告警塔</span>
      </div>
    </div>
  );
}

function fallbackElementClass(
  kind: FacilityElementKind,
  hub: HubOverview,
): string {
  switch (kind) {
    case "ground":
      return styles.facilityCampus;
    case "perimeter":
      return styles.facilityPerimeter;
    case "road":
      return styles.facilityRoad;
    case "apron":
      return styles.facilityApron;
    case "warehouse":
      return styles.facilityWarehouse;
    case "roof":
      return styles.facilityRoof;
    case "dock":
      return styles.facilityDock;
    case "slot":
      return styles.facilitySlot;
    case "cargo":
      return styles.facilityCargo;
    case "marking":
      return styles.facilityRoadMark;
    case "gatehouse":
      return styles.facilityGatehouse;
    case "tower":
      return styles.facilityTower;
    case "beacon":
      return hub.anomalies > 0
        ? styles.facilityAlarm
        : styles.facilityNormal;
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
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
