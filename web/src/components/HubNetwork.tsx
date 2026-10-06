import {
  ArrowLeft,
  ChevronRight,
  Crosshair,
  Map as MapIcon,
  Navigation,
  RotateCcw,
  Siren,
} from "lucide-react";
import {
  Component,
  lazy,
  Suspense,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ErrorInfo,
  type ReactNode,
} from "react";
import type {
  AnomalyOverview,
  HubOverview,
  Overview,
  RouteOverview,
  WaybillID,
} from "../api";
import type {
  FacilityCameraPreset,
  FacilitySceneSelection,
  SceneStats,
} from "./HubNetworkScene";
import styles from "../overview.module.css";
import { NetworkMap } from "./NetworkMap";
import {
  buildFacilityLayouts,
  type FacilityLayout,
  type FacilityTransportRouteKind,
  type FacilityVehicleState,
} from "./facilityLayout";

const HubNetworkScene = lazy(() => import("./HubNetworkScene"));

type HubNetworkProps = {
  hubs: readonly HubOverview[];
  routes: readonly RouteOverview[];
  anomalies: readonly AnomalyOverview[];
  dataMode: Overview["data_mode"];
  totals: Overview["totals"];
};

type SceneFailureBoundaryProps = {
  children: ReactNode;
  onFailure: () => void;
};

type SceneFailureBoundaryState = {
  failed: boolean;
};

export function HubNetwork({
  hubs,
  routes,
  anomalies,
  dataMode,
  totals,
}: HubNetworkProps) {
  const webGLAvailable = useMemo(supportsWebGL, []);
  const reducedMotion = useReducedMotion();
  const pageVisible = usePageVisibility();
  const [sceneFailed, setSceneFailed] = useState(false);
  const [sceneReady, setSceneReady] = useState(false);
  const [drawCalls, setDrawCalls] = useState<number | null>(null);
  const [sceneStats, setSceneStats] = useState<SceneStats | null>(null);
  const [selectedHubID, setSelectedHubID] = useState<string | null>(null);
  const [cameraPreset, setCameraPreset] =
    useState<FacilityCameraPreset>("overview");
  const [facilitySelection, setFacilitySelection] =
    useState<FacilitySceneSelection | null>(null);

  const hubByID = useMemo(
    () => new Map(hubs.map((hub) => [hub.hub_id, hub])),
    [hubs],
  );
  const facilityLayouts = useMemo(
    () => buildFacilityLayouts(hubs, routes),
    [hubs, routes],
  );
  const topRisk = anomalies[0];
  const topRiskHub = topRisk === undefined
    ? undefined
    : hubForAnomaly(topRisk, hubByID);
  const previousTopRisk = useRef(topRisk?.waybill_id);
  const selectedHub =
    selectedHubID === null ? undefined : hubByID.get(selectedHubID);
  const selectedLayout =
    selectedHubID === null ? undefined : facilityLayouts.get(selectedHubID);
  const selectedAnomaly = selectedHub === undefined
    ? undefined
    : anomalyForHub(selectedHub, anomalies);
  const selectionContent =
    selectedLayout === undefined || facilitySelection === null
      ? undefined
      : describeFacilitySelection(
          selectedLayout,
          facilitySelection,
          selectedAnomaly,
        );
  const useFallback = !webGLAvailable || sceneFailed;

  const selectHub = useCallback((hubID: string | null) => {
    setSelectedHubID(hubID);
    setCameraPreset("overview");
    setFacilitySelection(null);
  }, []);

  const focusTopRisk = useCallback(() => {
    if (topRiskHub !== undefined) {
      setSelectedHubID(topRiskHub.hub_id);
      setCameraPreset("risk");
      setFacilitySelection({ kind: "alert", id: "facility-alert" });
    }
  }, [topRiskHub]);

  const resetView = useCallback(() => {
    selectHub(null);
  }, [selectHub]);

  const selectFacilityObject = useCallback(
    (selection: FacilitySceneSelection) => {
      setFacilitySelection(selection);
      setCameraPreset(selection.kind === "vehicle" ? "follow" : "risk");
    },
    [],
  );

  const selectCameraPreset = useCallback(
    (preset: FacilityCameraPreset) => {
      setCameraPreset(preset);
      if (selectedLayout === undefined || preset === "overview") {
        setFacilitySelection(null);
        return;
      }
      if (preset === "follow" && facilitySelection?.kind !== "vehicle") {
        const vehicle =
          selectedLayout.vehicles.find((item) => item.state === "alert") ??
          selectedLayout.vehicles.find((item) => item.state === "moving") ??
          selectedLayout.vehicles[0];
        setFacilitySelection(
          vehicle === undefined ? null : { kind: "vehicle", id: vehicle.id },
        );
      }
      if (
        preset === "risk" &&
        facilitySelection?.kind !== "route" &&
        facilitySelection?.kind !== "alert"
      ) {
        const segment = selectedLayout.transportSegments.find(
          (item) => item.status === "risk",
        );
        setFacilitySelection(
          segment === undefined
            ? { kind: "alert", id: "facility-alert" }
            : { kind: "route", id: segment.id },
        );
      }
    },
    [facilitySelection, selectedLayout],
  );

  const useSceneFallback = useCallback(() => {
    setSceneFailed(true);
  }, []);

  const openWaybill = useCallback((waybillID: WaybillID) => {
    window.location.assign(`/waybills/${encodeURIComponent(waybillID)}`);
  }, []);

  useEffect(() => {
    const nextTopRisk = topRisk?.waybill_id;
    if (
      previousTopRisk.current !== undefined &&
      nextTopRisk !== undefined &&
      previousTopRisk.current !== nextTopRisk &&
      topRiskHub !== undefined
    ) {
      setSelectedHubID(topRiskHub.hub_id);
      setCameraPreset("risk");
      setFacilitySelection({ kind: "alert", id: "facility-alert" });
    }
    previousTopRisk.current = nextTopRisk;
  }, [topRisk?.waybill_id, topRiskHub]);

  return (
    <div
      className={styles.networkStage}
      role="region"
      aria-label={`全国公路港网络，${hubs.length} 个港口，${routes.length} 条线路`}
      data-network-renderer={useFallback ? "svg" : "webgl"}
      data-scene-mode={
        useFallback
          ? selectedHub === undefined
            ? "network"
            : "facility"
          : sceneStats?.mode ?? "loading"
      }
      data-scene-ready={sceneReady ? "true" : "false"}
      data-draw-calls={drawCalls ?? ""}
      data-scene-hubs={sceneStats?.hubs ?? ""}
      data-scene-routes={sceneStats?.routes ?? ""}
      data-scene-markers={sceneStats?.markers ?? ""}
      data-scene-facility-parts={sceneStats?.facilityParts ?? ""}
      data-scene-detail-parts={sceneStats?.detailParts ?? ""}
      data-scene-dock-bays={sceneStats?.dockBays ?? ""}
      data-scene-storage-slots={sceneStats?.storageSlots ?? ""}
      data-scene-occupied-slots={sceneStats?.occupiedSlots ?? ""}
      data-scene-warehouse-count={sceneStats?.warehouseCount ?? ""}
      data-scene-transport-routes={sceneStats?.transportRoutes ?? ""}
      data-scene-transport-segments={sceneStats?.transportSegments ?? ""}
      data-scene-vehicles={sceneStats?.vehicles ?? ""}
      data-scene-moving-vehicles={sceneStats?.movingVehicles ?? ""}
      data-scene-loading-vehicles={sceneStats?.loadingVehicles ?? ""}
      data-scene-alert-vehicles={sceneStats?.alertVehicles ?? ""}
      data-scene-layout={sceneStats?.layoutKind ?? ""}
      data-scene-layout-label={sceneStats?.layoutLabel ?? ""}
      data-scene-layout-signature={sceneStats?.layoutSignature ?? ""}
      data-scene-selected-hub={selectedHubID ?? ""}
      data-camera-preset={cameraPreset}
      data-scene-selected-object={
        facilitySelection === null
          ? ""
          : `${facilitySelection.kind}:${facilitySelection.id}`
      }
      data-scene-archetypes={
        sceneStats === null ? "" : JSON.stringify(sceneStats.archetypes)
      }
    >
      {useFallback ? (
        <NetworkMap
          hubs={hubs}
          routes={routes}
          anomalies={anomalies}
          selectedHubID={selectedHubID}
          reducedMotion={reducedMotion}
          paused={!pageVisible}
          facilitySelection={facilitySelection}
          onSelectHub={(hubID) => selectHub(hubID)}
          onSelectFacilityObject={selectFacilityObject}
        />
      ) : (
        <SceneFailureBoundary onFailure={useSceneFallback}>
          <Suspense fallback={<SceneLoading />}>
            <HubNetworkScene
              hubs={hubs}
              routes={routes}
              anomalies={anomalies}
              selectedHubID={selectedHubID}
              cameraPreset={cameraPreset}
              facilitySelection={facilitySelection}
              reducedMotion={reducedMotion}
              paused={!pageVisible}
              onSelectHub={(hubID) => selectHub(hubID)}
              onSelectFacilityObject={selectFacilityObject}
              onSelectWaybill={openWaybill}
              onFailure={useSceneFallback}
              onReady={() => setSceneReady(true)}
              onDrawCalls={setDrawCalls}
              onStats={setSceneStats}
            />
          </Suspense>
        </SceneFailureBoundary>
      )}

      <div className={styles.sceneHud} data-scene-hud>
        <div className={styles.sceneHudHeading}>
          <div className={styles.sceneBreadcrumb}>
            <span>全国港网</span>
            <ChevronRight aria-hidden="true" size={12} />
            <h2 id="map-heading">
              {selectedHub === undefined ? "实时态势" : selectedHub.name}
            </h2>
          </div>
          <div className={styles.sceneHudBadges}>
            {selectedHub !== undefined && (
              <span
                className={styles.sceneSelectedPill}
                data-scene-selection-pill
              >
                {selectedAnomaly?.waybill_id ?? selectedHub.hub_id}
              </span>
            )}
            <span className={styles.sceneDataBadge}>
              {dataModeLabel(dataMode)}
            </span>
          </div>
        </div>
        <dl className={styles.sceneMetrics} aria-label="网络运行摘要">
          <div>
            <dt>公路港</dt>
            <dd>{hubs.length}</dd>
          </div>
          <div>
            <dt>在途</dt>
            <dd>{totals.in_flight}</dd>
          </div>
          <div className={styles.sceneMetricSignal}>
            <dt>异常</dt>
            <dd>{totals.anomalies}</dd>
          </div>
          <div>
            <dt>处置中</dt>
            <dd>{totals.handling}</dd>
          </div>
        </dl>
      </div>

      {!useFallback && (
        <>
          {selectedHub === undefined && (
            <div className={styles.sceneRegions} aria-hidden="true">
              <span>西北</span>
              <span>华中</span>
              <span>东部沿海</span>
              <span>西南</span>
              <span>华南</span>
            </div>
          )}
          <div
            className={`${styles.sceneLegend} ${
              selectedHub === undefined ? "" : styles.sceneLegendFacility
            }`}
            aria-hidden="true"
          >
            {selectedHub === undefined ? (
              <>
                <span><i className={styles.sceneHubKey} />公路港</span>
                <span><i className={styles.sceneFlowKey} />运输流</span>
                <span><i className={styles.sceneRiskKey} />风险</span>
              </>
            ) : (
              <>
                <span><i className={styles.sceneLocalRouteKey} />场内链路</span>
                <span><i className={styles.sceneVehicleKey} />作业车辆</span>
                <span><i className={styles.sceneWarehouseKey} />仓库</span>
                <span><i className={styles.sceneDockKey} />月台</span>
                <span><i className={styles.sceneCargoKey} />货位</span>
                <span><i className={styles.sceneRiskKey} />告警塔</span>
              </>
            )}
          </div>
        </>
      )}

      <div className={styles.sceneCaption} aria-label="地图说明">
        <span>抽象港网</span>
        <span aria-hidden="true">·</span>
        <span>非测绘底图</span>
      </div>

      <div className={styles.sceneControls} data-scene-controls>
        {selectedHub !== undefined && (
          <div
            className={styles.sceneCameraPresets}
            role="group"
            aria-label="镜头预设"
          >
            <button
              type="button"
              className={
                cameraPreset === "overview" ? styles.scenePresetActive : ""
              }
              aria-pressed={cameraPreset === "overview"}
              onClick={() => selectCameraPreset("overview")}
            >
              <MapIcon aria-hidden="true" size={14} />
              <span>总览</span>
            </button>
            <button
              type="button"
              className={
                cameraPreset === "follow" ? styles.scenePresetActive : ""
              }
              aria-pressed={cameraPreset === "follow"}
              onClick={() => selectCameraPreset("follow")}
            >
              <Navigation aria-hidden="true" size={14} />
              <span>跟随</span>
            </button>
            <button
              type="button"
              className={
                cameraPreset === "risk" ? styles.scenePresetActive : ""
              }
              aria-pressed={cameraPreset === "risk"}
              onClick={() => selectCameraPreset("risk")}
            >
              <Siren aria-hidden="true" size={14} />
              <span>异常</span>
            </button>
          </div>
        )}
        <select
          value={selectedHubID ?? ""}
          aria-label="选择公路港"
          onChange={(event) => {
            selectHub(event.target.value || null);
          }}
        >
          <option value="">
            {selectedHub === undefined ? "选择港口" : "返回全国港网"}
          </option>
          {hubs.map((hub) => (
            <option key={hub.hub_id} value={hub.hub_id}>
              {hub.name}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={focusTopRisk}
          disabled={topRiskHub === undefined}
          aria-label="聚焦最高风险"
          title="聚焦最高风险"
        >
          <Crosshair aria-hidden="true" size={17} />
        </button>
        <button
          type="button"
          onClick={resetView}
          disabled={selectedHubID === null}
          aria-label={
            selectedHubID === null ? "复位网络视角" : "返回全国视角"
          }
          title={selectedHubID === null ? "复位视角" : "返回全国视角"}
        >
          {selectedHubID === null ? (
            <RotateCcw aria-hidden="true" size={17} />
          ) : (
            <ArrowLeft aria-hidden="true" size={17} />
          )}
        </button>
      </div>
      {selectedHub !== undefined && (
        <div
          className={`${styles.sceneSelection} ${
            sceneStats?.mode === "facility"
              ? styles.sceneSelectionFacility
              : ""
          }`}
          role="status"
        >
          <span>{selectedHub.province} / {selectedHub.city}</span>
          <strong>{selectionContent?.title ?? selectedHub.name}</strong>
          <dl>
            {(selectionContent?.metrics ?? [
              { label: "在途", value: String(selectedHub.in_flight) },
              { label: "异常", value: String(selectedHub.anomalies) },
              {
                label: "日容量",
                value: selectedHub.daily_capacity.toLocaleString("zh-CN"),
              },
            ]).map((metric) => (
              <div key={metric.label}>
                <dt>{metric.label}</dt>
                <dd>{metric.value}</dd>
              </div>
            ))}
          </dl>
          {sceneStats?.mode === "facility" && (
            <p className={styles.sceneFacilityMeta}>
              <span className={styles.sceneFacilityMetaPrimary}>
                {sceneStats.layoutLabel} · {sceneStats.warehouseCount} 仓
              </span>
              <span className={styles.sceneFacilityMetaResources}>
                月台 {sceneStats.dockBays} · 货位{" "}
                {sceneStats.occupiedSlots}/{sceneStats.storageSlots}
              </span>
              <span className={styles.sceneFacilityMetaFlow}>
                链路 {sceneStats.transportRoutes} · 车辆 {sceneStats.vehicles}
                {sceneStats.loadingVehicles > 0
                  ? ` · 装卸 ${sceneStats.loadingVehicles}`
                  : ""}
              </span>
              <span className={styles.sceneFacilityMetaAlert}>
                {sceneStats.alertVehicles > 0
                  ? `异常车 ${sceneStats.alertVehicles}`
                  : selectedHub.anomalies > 0
                    ? `告警 ${selectedHub.anomalies}`
                    : "告警正常"}
              </span>
            </p>
          )}
          {selectedAnomaly !== undefined && (
            <>
              <p className={styles.sceneEvidence}>
                <span>{selectedAnomaly.waybill_id}</span>
                <span>{selectedAnomaly.label || selectedAnomaly.type}</span>
                <span>风险 {selectedAnomaly.risk_score}</span>
                <span>{runStatusLabel(selectedAnomaly.run_status)}</span>
              </p>
              <a
                href={`/waybills/${encodeURIComponent(selectedAnomaly.waybill_id)}`}
                aria-label={`下钻 ${selectedHub.name} 的高风险运单 ${selectedAnomaly.waybill_id}`}
              >
                运单、司机与证据
              </a>
            </>
          )}
        </div>
      )}
    </div>
  );
}

class SceneFailureBoundary extends Component<
  SceneFailureBoundaryProps,
  SceneFailureBoundaryState
> {
  state: SceneFailureBoundaryState = { failed: false };

  static getDerivedStateFromError(): SceneFailureBoundaryState {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error("3D network scene failed", error, info.componentStack);
    this.props.onFailure();
  }

  render() {
    return this.state.failed ? null : this.props.children;
  }
}

function SceneLoading() {
  return (
    <div className={styles.sceneLoading} aria-live="polite">
      <span />
      正在建立三维港网
    </div>
  );
}

function hubForAnomaly(
  anomaly: AnomalyOverview,
  hubByID: ReadonlyMap<string, HubOverview>,
): HubOverview | undefined {
  const destination =
    anomaly.destination_hub_id === undefined
      ? undefined
      : hubByID.get(anomaly.destination_hub_id);
  if (destination !== undefined) {
    return destination;
  }
  return anomaly.origin_hub_id === undefined
    ? undefined
    : hubByID.get(anomaly.origin_hub_id);
}

function anomalyForHub(
  hub: HubOverview,
  anomalies: readonly AnomalyOverview[],
): AnomalyOverview | undefined {
  if (hub.focus_waybill_id !== undefined) {
    const focused = anomalies.find(
      (item) => item.waybill_id === hub.focus_waybill_id,
    );
    if (focused !== undefined) {
      return focused;
    }
  }
  return anomalies.find(
    (item) =>
      item.destination_hub_id === hub.hub_id ||
      item.origin_hub_id === hub.hub_id,
  );
}

type FacilitySelectionContent = {
  title: string;
  metrics: readonly { label: string; value: string }[];
};

function describeFacilitySelection(
  layout: FacilityLayout,
  selection: FacilitySceneSelection,
  anomaly: AnomalyOverview | undefined,
): FacilitySelectionContent | undefined {
  switch (selection.kind) {
    case "vehicle": {
      const vehicle = layout.vehicles.find((item) => item.id === selection.id);
      if (vehicle === undefined) {
        return undefined;
      }
      return {
        title: `作业车辆 ${vehicle.id}`,
        metrics: [
          { label: "状态", value: vehicleStateLabel(vehicle.state) },
          { label: "任务", value: vehicle.state === "alert" ? "异常" : "执行中" },
          { label: "链路", value: "场内环线" },
        ],
      };
    }
    case "route": {
      const index = layout.transportSegments.findIndex(
        (segment) => segment.id === selection.id,
      );
      const segment = layout.transportSegments[index];
      if (segment === undefined) {
        return undefined;
      }
      return {
        title: "场内运输链路",
        metrics: [
          {
            label: "状态",
            value: segment.status === "risk" ? "高风险" : "正常",
          },
          {
            label: "业务",
            value: segment.routeKinds.map(routeKindLabel).join("/"),
          },
          {
            label: "路段",
            value: `${index + 1}/${layout.transportSegments.length}`,
          },
        ],
      };
    }
    case "alert":
      return {
        title: anomaly?.label || "园区异常节点",
        metrics: [
          { label: "风险", value: String(anomaly?.risk_score ?? "待核验") },
          { label: "异常", value: anomaly?.type ?? "园区告警" },
          { label: "处置", value: runStatusLabel(anomaly?.run_status) },
        ],
      };
    default: {
      const exhaustive: never = selection;
      return exhaustive;
    }
  }
}

function routeKindLabel(kind: FacilityTransportRouteKind): string {
  switch (kind) {
    case "gate-to-dock":
      return "进场";
    case "dock-to-yard":
      return "转运";
    case "yard-to-gate":
      return "出场";
    default: {
      const exhaustive: never = kind;
      return exhaustive;
    }
  }
}

function vehicleStateLabel(state: FacilityVehicleState): string {
  switch (state) {
    case "moving":
      return "行驶";
    case "loading":
      return "装卸";
    case "alert":
      return "异常";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

function runStatusLabel(status: AnomalyOverview["run_status"]): string {
  switch (status) {
    case "awaiting_approval":
      return "待审批";
    case "executing":
      return "执行中";
    case "completed":
      return "已完成";
    case "rejected":
      return "已拒绝";
    case "failed":
      return "执行失败";
    case "review_required":
    case "manual_review":
      return "人工复核";
    case "started":
    case "investigating":
      return "调查中";
    case undefined:
      return "待处置";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function dataModeLabel(mode: Overview["data_mode"]): string {
  switch (mode) {
    case "simulated":
      return "仿真数据";
    case "fixture":
      return "内置样例";
    case "external":
      return "业务数据";
    default: {
      const exhaustive: never = mode;
      return exhaustive;
    }
  }
}

function supportsWebGL(): boolean {
  try {
    const canvas = document.createElement("canvas");
    const options: WebGLContextAttributes = {
      failIfMajorPerformanceCaveat: true,
    };
    const gl = canvas.getContext("webgl2", {
      ...options,
      alpha: false,
      antialias: true,
      powerPreference: "high-performance",
    });
    if (gl === null) {
      return false;
    }
    gl.getExtension("WEBGL_lose_context")?.loseContext();
    return true;
  } catch {
    return false;
  }
}

function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() =>
    window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );

  useEffect(() => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReduced(query.matches);
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  return reduced;
}

function usePageVisibility(): boolean {
  const [visible, setVisible] = useState(() => !document.hidden);

  useEffect(() => {
    const update = () => setVisible(!document.hidden);
    document.addEventListener("visibilitychange", update);
    return () => document.removeEventListener("visibilitychange", update);
  }, []);

  return visible;
}
