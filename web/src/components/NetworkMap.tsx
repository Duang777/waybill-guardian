import { useMemo, type CSSProperties } from "react";
import type {
  AnomalyOverview,
  HubOverview,
  RouteOverview,
} from "../api";
import type { FacilitySceneSelection } from "./HubNetworkScene";
import styles from "../overview.module.css";
import {
  buildFacilityLayouts,
  sampleFacilityVehiclePath,
  type FacilityElementKind,
  type FacilityLayout,
  type FacilityVehiclePath,
  type FacilityVehicleState,
} from "./facilityLayout";

type NetworkMapProps = {
  hubs: readonly HubOverview[];
  routes: readonly RouteOverview[];
  anomalies: readonly AnomalyOverview[];
  selectedHubID: string | null;
  facilitySelection: FacilitySceneSelection | null;
  reducedMotion: boolean;
  paused: boolean;
  onSelectHub: (hubID: string) => void;
  onSelectFacilityObject: (selection: FacilitySceneSelection) => void;
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
  facilitySelection,
  reducedMotion,
  paused,
  onSelectHub,
  onSelectFacilityObject,
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
    return (
      <FacilityDetailMap
        hub={selectedHub}
        layout={selectedLayout}
        selection={facilitySelection}
        reducedMotion={reducedMotion}
        paused={paused}
        onSelect={onSelectFacilityObject}
      />
    );
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
  selection,
  reducedMotion,
  paused,
  onSelect,
}: {
  hub: HubOverview;
  layout: FacilityLayout;
  selection: FacilitySceneSelection | null;
  reducedMotion: boolean;
  paused: boolean;
  onSelect: (selection: FacilitySceneSelection) => void;
}) {
  const scale = Math.min(
    650 / layout.campusWidth,
    380 / layout.campusDepth,
  );
  const centerX = 570;
  const centerY = 280;
  return (
    <div
      className={styles.networkMapFrame}
      data-fallback-mode="facility"
      data-fallback-layout={layout.kind}
      data-fallback-signature={layout.signature}
      data-fallback-routes={layout.transportRoutes.length}
      data-fallback-segments={layout.transportSegments.length}
      data-fallback-vehicles={layout.vehicles.length}
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
          {layout.label}，展示该园区的场内运输链、作业车辆、仓库、月台、
          货位和告警塔。
        </desc>
        <rect width="1000" height="560" className={styles.mapCanvas} />
        {layout.elements.map((element, index) => {
          const elementCenterX = centerX + element.x * scale;
          const elementCenterY = centerY + element.z * scale;
          const width = Math.max(element.width * scale, 2);
          const height = Math.max(element.depth * scale, 2);
          const selectable = element.kind === "beacon";
          return (
            <rect
              key={`${element.kind}-${index}`}
              x={elementCenterX - width / 2}
              y={elementCenterY - height / 2}
              width={width}
              height={height}
              rx={element.kind === "beacon" ? width / 2 : 0}
              className={fallbackElementClass(element.kind, hub)}
              role={selectable ? "button" : undefined}
              tabIndex={selectable ? 0 : undefined}
              aria-label={selectable ? "查看园区异常节点" : undefined}
              data-selected={
                selectable && selection?.kind === "alert" ? "true" : undefined
              }
              data-dimmed={
                selection !== null && !selectable ? "true" : undefined
              }
              onClick={
                selectable
                  ? () => onSelect({ kind: "alert", id: "facility-alert" })
                  : undefined
              }
              onKeyDown={
                selectable
                  ? (event) => {
                      if (event.key === "Enter" || event.key === " ") {
                        event.preventDefault();
                        onSelect({ kind: "alert", id: "facility-alert" });
                      }
                    }
                  : undefined
              }
              transform={
                element.rotationY === 0
                  ? undefined
                  : `rotate(${element.rotationY * (180 / Math.PI)} ${elementCenterX} ${elementCenterY})`
              }
            />
          );
        })}
        <g className={styles.facilityTransportLayer}>
          {layout.transportSegments.map((segment) => (
            <line
              key={segment.id}
              x1={centerX + segment.from[0] * scale}
              y1={centerY + segment.from[1] * scale}
              x2={centerX + segment.to[0] * scale}
              y2={centerY + segment.to[1] * scale}
              role="button"
              tabIndex={0}
              aria-label={`查看${segment.status === "risk" ? "高风险" : "正常"}场内路段`}
              data-facility-object={`route:${segment.id}`}
              data-selected={
                selection?.kind === "route" && selection.id === segment.id
                  ? "true"
                  : undefined
              }
              data-dimmed={
                selection !== null &&
                segment.status !== "risk" &&
                !(selection.kind === "route" && selection.id === segment.id)
                  ? "true"
                  : undefined
              }
              className={
                segment.status === "risk"
                  ? styles.facilityRouteRisk
                  : styles.facilityRouteActive
              }
              onClick={() => onSelect({ kind: "route", id: segment.id })}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  onSelect({ kind: "route", id: segment.id });
                }
              }}
            />
          ))}
          {layout.vehicles.map((vehicle) => {
            const duration = Math.max(6, 1 / Math.max(vehicle.speed, 0.01));
            const sample = sampleFacilityVehiclePath(
              layout.vehiclePath,
              vehicle.phase,
            );
            const transform =
              `translate(${centerX + sample.x * scale} ${
                centerY + sample.z * scale
              }) rotate(${90 - sample.rotationY * (180 / Math.PI)})`;
            return (
              <g
                key={vehicle.id}
                className={styles.facilityVehicle}
                transform={
                  reducedMotion || paused || vehicle.speed === 0
                    ? transform
                    : undefined
                }
                data-vehicle-state={vehicle.state}
                data-facility-object={`vehicle:${vehicle.id}`}
                data-selected={
                  selection?.kind === "vehicle" &&
                  selection.id === vehicle.id
                    ? "true"
                    : undefined
                }
                data-dimmed={
                  selection !== null &&
                  vehicle.state !== "alert" &&
                  !(selection.kind === "vehicle" &&
                    selection.id === vehicle.id)
                    ? "true"
                    : undefined
                }
                role="button"
                tabIndex={0}
                aria-label={`查看作业车辆 ${vehicle.id}`}
                onClick={() => onSelect({ kind: "vehicle", id: vehicle.id })}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") {
                    event.preventDefault();
                    onSelect({ kind: "vehicle", id: vehicle.id });
                  }
                }}
              >
                {!reducedMotion && !paused && vehicle.speed > 0 && (
                  <animateMotion
                    path={facilityVehiclePathData(
                      layout.vehiclePath,
                      centerX,
                      centerY,
                      scale,
                    )}
                    begin={`-${vehicle.phase * duration}s`}
                    dur={`${duration}s`}
                    repeatCount="indefinite"
                    rotate="auto"
                  />
                )}
                <rect
                  x="-11"
                  y="-6"
                  width="22"
                  height="12"
                  rx="2"
                  className={fallbackVehicleClass(vehicle.state)}
                />
                <rect
                  x="3"
                  y="-5"
                  width="7"
                  height="10"
                  rx="1"
                  className={styles.facilityVehicleCab}
                />
              </g>
            );
          })}
        </g>
      </svg>
      <div className={styles.mapLegend} aria-hidden="true">
        <span><i className={styles.sceneLocalRouteKey} />场内链路</span>
        <span><i className={styles.sceneVehicleKey} />作业车辆</span>
        <span><i className={styles.sceneWarehouseKey} />仓库</span>
        <span><i className={styles.sceneDockKey} />月台</span>
        <span><i className={styles.sceneCargoKey} />货位</span>
        <span><i className={styles.sceneRiskKey} />告警塔</span>
      </div>
    </div>
  );
}

function facilityVehiclePathData(
  path: FacilityVehiclePath,
  centerX: number,
  centerY: number,
  scale: number,
): string {
  const projected = path.points.map(([x, z]) => ({
    x: centerX + x * scale,
    y: centerY + z * scale,
  }));
  if (
    projected.length > 2 &&
    projected[0]?.x === projected.at(-1)?.x &&
    projected[0]?.y === projected.at(-1)?.y
  ) {
    projected.pop();
  }
  const rounded = projected.map((point, index) => {
    const previous = projected[(index - 1 + projected.length) % projected.length];
    const next = projected[(index + 1) % projected.length];
    if (previous === undefined || next === undefined) {
      return { point, entry: point, exit: point };
    }
    const radius = Math.min(
      12,
      Math.hypot(point.x - previous.x, point.y - previous.y) / 3,
      Math.hypot(next.x - point.x, next.y - point.y) / 3,
    );
    return {
      point,
      entry: moveToward(point, previous, radius),
      exit: moveToward(point, next, radius),
    };
  });
  const first = rounded[0];
  if (first === undefined) {
    return "";
  }
  const commands = [`M ${first.exit.x} ${first.exit.y}`];
  for (let index = 1; index < rounded.length; index += 1) {
    const corner = rounded[index];
    if (corner !== undefined) {
      commands.push(
        `L ${corner.entry.x} ${corner.entry.y}`,
        `Q ${corner.point.x} ${corner.point.y} ${corner.exit.x} ${corner.exit.y}`,
      );
    }
  }
  commands.push(
    `L ${first.entry.x} ${first.entry.y}`,
    `Q ${first.point.x} ${first.point.y} ${first.exit.x} ${first.exit.y}`,
    "Z",
  );
  return commands.join(" ");
}

function moveToward(
  from: { x: number; y: number },
  to: { x: number; y: number },
  distance: number,
): { x: number; y: number } {
  const length = Math.hypot(to.x - from.x, to.y - from.y);
  const ratio = length === 0 ? 0 : distance / length;
  return {
    x: from.x + (to.x - from.x) * ratio,
    y: from.y + (to.y - from.y) * ratio,
  };
}

function fallbackVehicleClass(state: FacilityVehicleState): string {
  switch (state) {
    case "moving":
      return styles.facilityVehicleMoving;
    case "loading":
      return styles.facilityVehicleLoading;
    case "alert":
      return styles.facilityVehicleAlert;
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
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
