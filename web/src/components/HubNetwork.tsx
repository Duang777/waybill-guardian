import { ArrowLeft, Crosshair, RotateCcw } from "lucide-react";
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
  RouteOverview,
  WaybillID,
} from "../api";
import type { SceneStats } from "./HubNetworkScene";
import styles from "../overview.module.css";
import { NetworkMap } from "./NetworkMap";

const HubNetworkScene = lazy(() => import("./HubNetworkScene"));

type HubNetworkProps = {
  hubs: readonly HubOverview[];
  routes: readonly RouteOverview[];
  anomalies: readonly AnomalyOverview[];
};

type SceneFailureBoundaryProps = {
  children: ReactNode;
  onFailure: () => void;
};

type SceneFailureBoundaryState = {
  failed: boolean;
};

export function HubNetwork({ hubs, routes, anomalies }: HubNetworkProps) {
  const webGLAvailable = useMemo(supportsWebGL, []);
  const reducedMotion = useReducedMotion();
  const pageVisible = usePageVisibility();
  const [sceneFailed, setSceneFailed] = useState(false);
  const [sceneReady, setSceneReady] = useState(false);
  const [drawCalls, setDrawCalls] = useState<number | null>(null);
  const [sceneStats, setSceneStats] = useState<SceneStats | null>(null);
  const [selectedHubID, setSelectedHubID] = useState<string | null>(null);

  const hubByID = useMemo(
    () => new Map(hubs.map((hub) => [hub.hub_id, hub])),
    [hubs],
  );
  const topRisk = anomalies[0];
  const topRiskHub = topRisk === undefined
    ? undefined
    : hubForAnomaly(topRisk, hubByID);
  const previousTopRisk = useRef(topRisk?.waybill_id);
  const selectedHub =
    selectedHubID === null ? undefined : hubByID.get(selectedHubID);
  const selectedAnomaly = selectedHub === undefined
    ? undefined
    : anomalyForHub(selectedHub, anomalies);
  const useFallback = !webGLAvailable || sceneFailed;

  const focusTopRisk = useCallback(() => {
    if (topRiskHub !== undefined) {
      setSelectedHubID(topRiskHub.hub_id);
    }
  }, [topRiskHub]);

  const resetView = useCallback(() => {
    setSelectedHubID(null);
  }, []);

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
      data-scene-layout={sceneStats?.layoutKind ?? ""}
      data-scene-layout-label={sceneStats?.layoutLabel ?? ""}
      data-scene-layout-signature={sceneStats?.layoutSignature ?? ""}
      data-scene-selected-hub={selectedHubID ?? ""}
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
          onSelectHub={setSelectedHubID}
        />
      ) : (
        <SceneFailureBoundary onFailure={useSceneFallback}>
          <Suspense fallback={<SceneLoading />}>
            <HubNetworkScene
              hubs={hubs}
              routes={routes}
              anomalies={anomalies}
              selectedHubID={selectedHubID}
              reducedMotion={reducedMotion}
              paused={!pageVisible}
              onSelectHub={setSelectedHubID}
              onSelectWaybill={openWaybill}
              onFailure={useSceneFallback}
              onReady={() => setSceneReady(true)}
              onDrawCalls={setDrawCalls}
              onStats={setSceneStats}
            />
          </Suspense>
        </SceneFailureBoundary>
      )}

      {!useFallback && (
        <>
          <div className={styles.sceneIdentity}>
            <span>
              {selectedHub === undefined
                ? "WG / LIVE NETWORK"
                : "WG / FACILITY VIEW"}
            </span>
            <strong>
              {selectedHub === undefined ? `${hubs.length} HUBS` : "园区详情"}
            </strong>
          </div>
          {selectedHub === undefined && (
            <div className={styles.sceneRegions} aria-hidden="true">
              <span>西北</span>
              <span>华中</span>
              <span>东部沿海</span>
              <span>西南</span>
              <span>华南</span>
            </div>
          )}
          <div className={styles.sceneLegend} aria-hidden="true">
            {selectedHub === undefined ? (
              <>
                <span><i className={styles.sceneHubKey} />公路港</span>
                <span><i className={styles.sceneFlowKey} />运输流</span>
                <span><i className={styles.sceneRiskKey} />风险</span>
              </>
            ) : (
              <>
                <span><i className={styles.sceneWarehouseKey} />仓库</span>
                <span><i className={styles.sceneDockKey} />月台</span>
                <span><i className={styles.sceneCargoKey} />货位</span>
                <span><i className={styles.sceneRiskKey} />告警塔</span>
              </>
            )}
          </div>
        </>
      )}

      <div className={styles.sceneControls}>
        <select
          value={selectedHubID ?? ""}
          aria-label="选择公路港"
          onChange={(event) => {
            setSelectedHubID(event.target.value || null);
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
        <div className={styles.sceneSelection} role="status">
          <span>{selectedHub.province} / {selectedHub.city}</span>
          <strong>{selectedHub.name}</strong>
          <dl>
            <div><dt>在途</dt><dd>{selectedHub.in_flight}</dd></div>
            <div><dt>异常</dt><dd>{selectedHub.anomalies}</dd></div>
            <div>
              <dt>日容量</dt>
              <dd>{selectedHub.daily_capacity.toLocaleString("zh-CN")}</dd>
            </div>
          </dl>
          {sceneStats?.mode === "facility" && (
            <p className={styles.sceneFacilityMeta}>
              <span>
                {sceneStats.layoutLabel} · {sceneStats.warehouseCount} 仓
              </span>
              <span>月台 {sceneStats.dockBays}</span>
              <span>
                货位 {sceneStats.occupiedSlots}/{sceneStats.storageSlots}
              </span>
              <span>
                {selectedHub.anomalies > 0
                  ? `告警 ${selectedHub.anomalies}`
                  : "告警正常"}
              </span>
            </p>
          )}
          {selectedAnomaly !== undefined && (
            <a
              href={`/waybills/${encodeURIComponent(selectedAnomaly.waybill_id)}`}
              aria-label={`下钻 ${selectedHub.name} 的高风险运单 ${selectedAnomaly.waybill_id}`}
            >
              查看 {selectedAnomaly.waybill_id}
            </a>
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
