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
  const selectedHub =
    selectedHubID === null ? undefined : hubByID.get(selectedHubID);

  if (selectedHub !== undefined) {
    return <FacilityDetailMap hub={selectedHub} hubs={hubs} />;
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
  hubs,
}: {
  hub: HubOverview;
  hubs: readonly HubOverview[];
}) {
  const detail = buildFallbackFacilityDetail(hub, hubs);
  return (
    <div className={styles.networkMapFrame} data-fallback-mode="facility">
      <svg
        className={styles.networkMap}
        viewBox="0 0 1000 560"
        role="img"
        aria-labelledby="facility-map-title facility-map-description"
        preserveAspectRatio="xMidYMid meet"
      >
        <title id="facility-map-title">{hub.name}园区详情</title>
        <desc id="facility-map-description">
          展示该园区的仓库、月台、货位、场内道路和告警塔。
        </desc>
        <rect width="1000" height="560" className={styles.mapCanvas} />
        <rect
          x="84"
          y="68"
          width="832"
          height="424"
          className={styles.facilityCampus}
        />
        <rect
          x="720"
          y="100"
          width="118"
          height="360"
          className={styles.facilityRoad}
        />
        {Array.from({ length: 6 }, (_, index) => (
          <rect
            key={`road-${index}`}
            x="774"
            y={126 + index * 54}
            width="10"
            height="28"
            className={styles.facilityRoadMark}
          />
        ))}
        <rect
          x="250"
          y="155"
          width={detail.warehouseWidth}
          height="152"
          className={styles.facilityWarehouse}
        />
        {detail.warehouseCount > 1 && (
          <rect
            x="250"
            y="322"
            width={detail.warehouseWidth * 0.72}
            height="80"
            className={styles.facilityWarehouseSecondary}
          />
        )}
        <rect
          x="218"
          y="294"
          width={detail.warehouseWidth + 64}
          height="78"
          className={styles.facilityApron}
        />
        {Array.from({ length: detail.dockBays }, (_, index) => {
          const x = distributedPosition(
            258,
            242 + detail.warehouseWidth,
            index,
            detail.dockBays,
          );
          return (
            <g key={`dock-${index}`}>
              <rect
                x={x}
                y="282"
                width="20"
                height="24"
                className={styles.facilityDock}
              />
              <line
                x1={x + 10}
                y1="316"
                x2={x + 10}
                y2="360"
                className={styles.facilityBayLine}
              />
            </g>
          );
        })}
        {Array.from({ length: detail.storageSlots }, (_, index) => {
          const column = index % 4;
          const row = Math.floor(index / 4);
          const x = 112 + column * 30;
          const y = 126 + row * 42;
          return (
            <g key={`slot-${index}`}>
              <rect
                x={x}
                y={y}
                width="22"
                height="30"
                className={styles.facilitySlot}
              />
              {index < detail.occupiedSlots && (
                <rect
                  x={x + 3}
                  y={y + 4}
                  width="16"
                  height="22"
                  className={styles.facilityCargo}
                />
              )}
            </g>
          );
        })}
        <rect
          x="852"
          y={360 - detail.towerHeight}
          width="12"
          height={detail.towerHeight}
          className={styles.facilityTower}
        />
        <circle
          cx="858"
          cy={348 - detail.towerHeight}
          r="16"
          className={
            hub.anomalies > 0
              ? styles.facilityAlarm
              : styles.facilityNormal
          }
        />
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

function buildFallbackFacilityDetail(
  hub: HubOverview,
  hubs: readonly HubOverview[],
) {
  const capacities = hubs
    .map((item) => item.daily_capacity)
    .sort((left, right) => left - right);
  const rank = capacities.findIndex(
    (capacity) => capacity >= hub.daily_capacity,
  );
  const quartile = Math.min(
    3,
    Math.floor((Math.max(rank, 0) * 4) / Math.max(capacities.length, 1)),
  );
  const maxInFlight = Math.max(...hubs.map((item) => item.in_flight), 1);
  const dockBays = 4 + quartile * 2;
  const storageSlots = 6 + quartile * 2;
  const occupiedSlots =
    hub.in_flight === 0
      ? 0
      : Math.max(
          1,
          Math.ceil((hub.in_flight / maxInFlight) * storageSlots),
        );

  return {
    dockBays,
    storageSlots,
    occupiedSlots,
    towerHeight: 58 + Math.min(hub.anomalies, 8) * 8,
    warehouseCount: 1 + Math.floor(quartile / 2),
    warehouseWidth: 280 + quartile * 42,
  };
}

function distributedPosition(
  start: number,
  end: number,
  index: number,
  count: number,
): number {
  if (count <= 1) {
    return (start + end) / 2;
  }
  return start + ((end - start) * index) / (count - 1);
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
