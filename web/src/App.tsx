import { AlertOctagon, ArrowLeft, Play, RotateCw, Route, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import styles from "./app.module.css";
import {
  APIError,
  confirmApproval,
  getRunSnapshot,
  getWaybill,
  listActiveRuns,
  listPendingApprovals,
  listWaybills,
  openTimeline,
  rejectApproval,
  startRun,
  type RunID,
  type RunSummary,
  type WaybillCatalogItem,
  type WaybillID,
  type WaybillView,
  waybillIdSchema,
} from "./api";
import { ApprovalPanel } from "./components/ApprovalPanel";
import { RouteMap } from "./components/RouteMap";
import { SummaryStrip } from "./components/SummaryStrip";
import { PlaybackControls, TimelinePanel } from "./components/TimelinePanel";
import {
  decideRecovery,
  type RecoverySource,
} from "./recovery";
import {
  initialTimelineState,
  latestApproval,
  runStatus,
  timelineReducer,
  visibleEvents,
} from "./timeline";
import type { WaybillResource } from "./waybill-resource";
import { OverviewPage } from "./OverviewPage";

type CatalogResource =
  | { kind: "loading" }
  | { kind: "empty" }
  | { kind: "ready"; data: readonly WaybillCatalogItem[] }
  | { kind: "error"; message: string };

type SelectionTarget =
  | { kind: "waybill"; waybillID: WaybillID }
  | { kind: "run"; runID: RunID }
  | { kind: "known_run"; runID: RunID; waybillID: WaybillID };

type WaybillSelection =
  | { kind: "empty" }
  | { kind: "loading"; target: SelectionTarget }
  | { kind: "ready"; waybillID: WaybillID; data: WaybillView }
  | { kind: "error"; target: SelectionTarget; message: string };

type PendingAction = "bootstrap" | "trigger" | "confirm" | "reject" | null;

type SelectionRequest = {
  generation: number;
  controller: AbortController;
};

export default function App() {
  const match = /^\/waybills\/([^/]+)\/?$/.exec(window.location.pathname);
  if (match === null) {
    return <OverviewPage />;
  }
  const parsed = waybillIdSchema.safeParse(decodeURIComponent(match[1] ?? ""));
  return <WaybillWorkbench initialWaybillID={parsed.success ? parsed.data : null} />;
}

function WaybillWorkbench({
  initialWaybillID,
}: {
  initialWaybillID: WaybillID | null;
}) {
  const [catalog, setCatalog] = useState<CatalogResource>({ kind: "loading" });
  const [selection, setSelection] = useState<WaybillSelection>({ kind: "empty" });
  const [run, setRun] = useState<RunSummary | null>(null);
  const [timelineAfter, setTimelineAfter] = useState<number | null>(null);
  const [timeline, dispatch] = useReducer(timelineReducer, initialTimelineState);
  const [connected, setConnected] = useState(false);
  const [pendingAction, setPendingAction] = useState<PendingAction>("bootstrap");
  const [message, setMessage] = useState<string | null>(null);
  const [recoveryError, setRecoveryError] = useState<string | null>(null);
  const selectionGeneration = useRef(0);
  const selectionRequest = useRef<AbortController | null>(null);
  const catalogRequest = useRef<AbortController | null>(null);
  const bootstrapRequest = useRef<AbortController | null>(null);
  const catalogGeneration = useRef(0);
  const closeTimeline = useRef<(() => void) | null>(null);

  const beginSelection = useCallback((): SelectionRequest => {
    selectionRequest.current?.abort();
    closeTimeline.current?.();
    closeTimeline.current = null;
    const controller = new AbortController();
    const generation = selectionGeneration.current + 1;
    selectionGeneration.current = generation;
    selectionRequest.current = controller;
    setConnected(false);
    setTimelineAfter(null);
    dispatch({ type: "reset" });
    return { generation, controller };
  }, []);

  const loadCatalog = useCallback(async (): Promise<
    readonly WaybillCatalogItem[] | null
  > => {
    catalogRequest.current?.abort();
    const controller = new AbortController();
    const generation = catalogGeneration.current + 1;
    catalogGeneration.current = generation;
    catalogRequest.current = controller;
    setCatalog({ kind: "loading" });
    try {
      const waybills = await listWaybills(controller.signal);
      if (catalogGeneration.current !== generation) {
        return null;
      }
      setCatalog(
        waybills.length === 0
          ? { kind: "empty" }
          : { kind: "ready", data: waybills },
      );
      return waybills;
    } catch (error) {
      if (isAbortError(error) || catalogGeneration.current !== generation) {
        return null;
      }
      setCatalog({ kind: "error", message: errorMessage(error) });
      return null;
    }
  }, []);

  const hydrateRun = useCallback(
    async (
      runID: RunID,
      request: SelectionRequest,
      expectedWaybillID?: WaybillID,
    ): Promise<void> => {
      let target: SelectionTarget =
        expectedWaybillID === undefined
          ? { kind: "run", runID }
          : { kind: "known_run", runID, waybillID: expectedWaybillID };
      try {
        const snapshot = await getRunSnapshot(runID, request.controller.signal);
        if (selectionGeneration.current !== request.generation) {
          return;
        }
        if (
          expectedWaybillID !== undefined &&
          snapshot.run.waybill_id !== expectedWaybillID
        ) {
          throw new Error("启动响应与运行快照的运单不一致");
        }
        const waybillID = expectedWaybillID ?? snapshot.run.waybill_id;
        target = { kind: "known_run", runID, waybillID };
        setRun(snapshot.run);
        setSelection({ kind: "loading", target });
        dispatch({ type: "hydrate", events: snapshot.events });
        setTimelineAfter(snapshot.run.last_seq);

        const data = await getWaybill(waybillID, request.controller.signal);
        if (selectionGeneration.current === request.generation) {
          setSelection({ kind: "ready", waybillID, data });
        }
      } catch (error) {
        if (
          isAbortError(error) ||
          selectionGeneration.current !== request.generation
        ) {
          return;
        }
        const detail = errorMessage(error);
        setSelection({ kind: "error", target, message: detail });
      }
    },
    [],
  );

  const selectRun = useCallback(
    async ({
      runID,
      expectedWaybillID,
    }: {
      runID: RunID;
      expectedWaybillID?: WaybillID;
    }): Promise<void> => {
      const request = beginSelection();
      setRun(null);
      setMessage(null);
      setSelection({
        kind: "loading",
        target:
          expectedWaybillID === undefined
            ? { kind: "run", runID }
            : { kind: "known_run", runID, waybillID: expectedWaybillID },
      });
      await hydrateRun(runID, request, expectedWaybillID);
    },
    [beginSelection, hydrateRun],
  );

  const loadWaybill = useCallback(
    async (waybillID: WaybillID): Promise<void> => {
      const request = beginSelection();
      setRun(null);
      const target: SelectionTarget = { kind: "waybill", waybillID };
      setSelection({ kind: "loading", target });
      setMessage(null);
      try {
        const data = await getWaybill(waybillID, request.controller.signal);
        if (selectionGeneration.current === request.generation) {
          setSelection({ kind: "ready", waybillID, data });
        }
      } catch (error) {
        if (
          isAbortError(error) ||
          selectionGeneration.current !== request.generation
        ) {
          return;
        }
        setSelection({
          kind: "error",
          target,
          message: errorMessage(error),
        });
      }
    },
    [beginSelection],
  );

  const bootstrap = useCallback(async (): Promise<void> => {
    bootstrapRequest.current?.abort();
    const controller = new AbortController();
    bootstrapRequest.current = controller;
    setPendingAction("bootstrap");
    setRecoveryError(null);
    setMessage(null);
    const catalogPromise = loadCatalog();
    try {
      const [activeRuns, pendingApprovals] = await Promise.allSettled([
        listActiveRuns(controller.signal),
        listPendingApprovals(controller.signal),
      ]);
      if (controller.signal.aborted) {
        return;
      }
      const decision = decideRecovery({
        activeRuns: scopedRecoverySource(activeRuns, initialWaybillID),
        pendingApprovals: scopedRecoverySource(
          pendingApprovals,
          initialWaybillID,
        ),
      });
      if (decision.kind === "recover") {
        await selectRun({ runID: decision.runID });
        return;
      }
      if (decision.kind === "blocked") {
        setRecoveryError(decision.message);
        return;
      }
      const waybills = await catalogPromise;
      const targetWaybillID = initialWaybillID ?? waybills?.[0]?.waybill_id;
      if (!controller.signal.aborted && targetWaybillID !== undefined) {
        await loadWaybill(targetWaybillID);
      }
    } finally {
      if (bootstrapRequest.current === controller && !controller.signal.aborted) {
        setPendingAction(null);
      }
    }
  }, [initialWaybillID, loadCatalog, loadWaybill, selectRun]);

  useEffect(() => {
    void bootstrap();
    return () => {
      bootstrapRequest.current?.abort();
      catalogRequest.current?.abort();
      selectionRequest.current?.abort();
      catalogGeneration.current += 1;
      selectionGeneration.current += 1;
    };
  }, [bootstrap]);

  useEffect(() => {
    if (run === null || timelineAfter === null || run.status === "manual_review") {
      return;
    }
    const generation = selectionGeneration.current;
    setConnected(false);
    const close = openTimeline(run.run_id, timelineAfter, {
      onEvent: (event) => {
        if (selectionGeneration.current === generation && event.run_id === run.run_id) {
          dispatch({ type: "event_received", event });
        }
      },
      onConnectionChange: (value) => {
        if (selectionGeneration.current === generation) {
          setConnected(value);
        }
      },
      onError: (value) => {
        if (selectionGeneration.current === generation) {
          setMessage(value);
        }
      },
    });
    closeTimeline.current = close;
    return () => {
      close();
      if (closeTimeline.current === close) {
        closeTimeline.current = null;
      }
    };
  }, [run, timelineAfter]);

  useEffect(() => {
    if (timeline.playback.kind !== "playing") {
      return;
    }
    const timer = window.setInterval(() => dispatch({ type: "tick" }), 650);
    return () => window.clearInterval(timer);
  }, [timeline.playback.kind]);

  const projectedEvents = useMemo(() => visibleEvents(timeline), [timeline]);
  const currentApproval = useMemo(() => latestApproval(projectedEvents), [projectedEvents]);
  const currentStatus =
    runStatus(projectedEvents) ??
    (timeline.playback.kind === "live" ? (run?.status ?? null) : null);
  const view = selection.kind === "ready" ? selection.data : null;
  const waybillResource = toWaybillResource(selection);
  const selectedWaybillID =
    waybillResource.kind === "empty" ? null : waybillResource.waybillID;
  useEffect(() => {
    if (selectedWaybillID === null) {
      return;
    }
    const path = `/waybills/${encodeURIComponent(selectedWaybillID)}`;
    if (window.location.pathname !== path) {
      window.history.replaceState(null, "", path);
    }
  }, [selectedWaybillID]);
  const anomaly = view?.tracking.find((point) => point.anomaly) ?? null;
  const resourceError =
    recoveryError ??
    (selection.kind === "error"
      ? selection.message
      : catalog.kind === "error"
        ? catalog.message
        : null);
  const displayedError = message ?? resourceError;

  const startSelectedRun = async () => {
    if (selection.kind !== "ready") {
      return;
    }
    const currentSelection = selection;
    const request = beginSelection();
    setRun(null);
    setPendingAction("trigger");
    setMessage(null);
    try {
      const started = await startRun(
        currentSelection.waybillID,
        request.controller.signal,
      );
      if (selectionGeneration.current !== request.generation) {
        return;
      }
      setSelection({
        kind: "loading",
        target: {
          kind: "known_run",
          runID: started.run_id,
          waybillID: started.waybill_id,
        },
      });
      await hydrateRun(
        started.run_id,
        request,
        started.waybill_id,
      );
    } catch (error) {
      if (
        !isAbortError(error) &&
        selectionGeneration.current === request.generation
      ) {
        setSelection(currentSelection);
        setMessage(errorMessage(error));
      }
    } finally {
      if (selectionGeneration.current === request.generation) {
        setPendingAction(null);
      }
    }
  };

  const retryResource = async () => {
    setMessage(null);
    if (recoveryError !== null) {
      await bootstrap();
      return;
    }
    if (selection.kind === "error") {
      switch (selection.target.kind) {
        case "waybill":
          await loadWaybill(selection.target.waybillID);
          return;
        case "run":
          await selectRun({ runID: selection.target.runID });
          return;
        case "known_run":
          await selectRun({
            runID: selection.target.runID,
            expectedWaybillID: selection.target.waybillID,
          });
          return;
        default: {
          const exhaustive: never = selection.target;
          return exhaustive;
        }
      }
    }
    if (catalog.kind === "error") {
      const waybills = await loadCatalog();
      if (selection.kind === "empty" && waybills?.[0] !== undefined) {
        await loadWaybill(waybills[0].waybill_id);
      }
    }
  };

  const selectWaybill = (value: string) => {
    if (catalog.kind !== "ready") {
      return;
    }
    const item = catalog.data.find((candidate) => candidate.waybill_id === value);
    if (item !== undefined && item.waybill_id !== selectedWaybillID) {
      void loadWaybill(item.waybill_id);
    }
  };

  const confirm = async () => {
    if (currentApproval === null) {
      return;
    }
    setPendingAction("confirm");
    setMessage(null);
    try {
      await confirmApproval(currentApproval.id);
    } catch (error) {
      setMessage(errorMessage(error));
    } finally {
      setPendingAction(null);
    }
  };

  const reject = async (reason: string) => {
    if (currentApproval === null) {
      return;
    }
    setPendingAction("reject");
    setMessage(null);
    try {
      await rejectApproval(currentApproval.id, reason);
    } catch (error) {
      setMessage(errorMessage(error));
      throw error;
    } finally {
      setPendingAction(null);
    }
  };

  return (
    <>
      <a className={styles.skipLink} href="#main-content">
        跳到主要内容
      </a>
      <div className={styles.appShell}>
        <header className={styles.topbar}>
          <div className={styles.brand}>
            <a
              className={styles.backButton}
              href="/"
              aria-label="返回全国经营总览"
              title="返回总览"
            >
              <ArrowLeft aria-hidden="true" size={17} />
            </a>
            <span className={styles.brandMark} aria-hidden="true">
              WG
            </span>
            <div>
              <strong>Waybill Guardian</strong>
              <span>异常运单处置控制台</span>
            </div>
          </div>
          <label className={styles.waybillPicker}>
            <span>WAYBILL</span>
            <select
              value={selectedWaybillID ?? ""}
              disabled={
                catalog.kind !== "ready" ||
                pendingAction !== null ||
                recoveryError !== null
              }
              aria-label="选择异常运单"
              onChange={(event) => selectWaybill(event.currentTarget.value)}
            >
              {catalog.kind === "loading" && <option value="">正在读取运单目录</option>}
              {catalog.kind === "empty" && <option value="">暂无可处置运单</option>}
              {catalog.kind === "error" && <option value="">运单目录不可用</option>}
              {catalog.kind === "ready" &&
                catalog.data.map((item) => (
                  <option value={item.waybill_id} key={item.waybill_id}>
                    {item.origin} → {item.destination} · {item.waybill_id}
                  </option>
                ))}
            </select>
          </label>
          <div className={styles.runContext}>
            <span className={styles.runLabel}>ACTIVE RUN</span>
            <span className={styles.mono}>
              {run === null ? "NOT STARTED" : compactID(run.run_id)}
            </span>
          </div>
          <button
            className={styles.triggerButton}
            type="button"
            disabled={
              pendingAction !== null ||
              recoveryError !== null ||
              selection.kind !== "ready"
            }
            onClick={() => void startSelectedRun()}
          >
            {run === null ? (
              <Play aria-hidden="true" size={17} />
            ) : (
              <RotateCw aria-hidden="true" size={17} />
            )}
            {pendingAction === "trigger"
              ? "正在启动"
              : pendingAction === "bootstrap"
                ? "正在恢复"
              : run === null
                ? "启动处置"
                : "重新处置"}
          </button>
        </header>

        <main id="main-content" className={styles.main}>
          <SummaryStrip
            resource={waybillResource}
            status={currentStatus}
            connected={connected}
          />

          {displayedError !== null && (
            <div className={styles.errorBanner} role="alert">
              <AlertOctagon aria-hidden="true" size={17} />
              <span>{displayedError}</span>
              <div className={styles.errorActions}>
                {resourceError !== null && message === null && (
                  <button
                    className={styles.retryButton}
                    type="button"
                    onClick={() => void retryResource()}
                  >
                    <RotateCw aria-hidden="true" size={15} />
                    重试
                  </button>
                )}
                {message !== null && (
                  <button
                    className={styles.dismissButton}
                    type="button"
                    aria-label="关闭错误提示"
                    title="关闭"
                    onClick={() => setMessage(null)}
                  >
                    <X aria-hidden="true" size={16} />
                  </button>
                )}
              </div>
            </div>
          )}

          <div className={styles.workspace}>
            <div className={styles.primaryColumn}>
              <section className={styles.mapPanel} aria-labelledby="route-map-title">
                <div className={styles.panelTitleRow}>
                  <div>
                    <span className={styles.eyebrow}>Live route evidence</span>
                    <h2 id="route-map-title">异常轨迹</h2>
                  </div>
                  <span className={styles.anomalyLegend}>
                    <span aria-hidden="true" />
                    {anomaly === null
                      ? selection.kind === "error"
                        ? "异常轨迹加载失败"
                        : selectedWaybillID === null
                        ? "暂无异常轨迹"
                        : selection.kind === "loading"
                          ? "等待异常轨迹"
                          : "未发现异常轨迹"
                      : anomaly.stop_hours === undefined
                        ? anomaly.label
                        : `${anomaly.label}停留 ${formatHours(anomaly.stop_hours)} 小时`}
                  </span>
                </div>
                <RouteMap
                  points={view?.tracking ?? []}
                  origin={view?.waybill.origin ?? null}
                  destination={view?.waybill.destination ?? null}
                  resourceKind={waybillResource.kind}
                />
              </section>

              <div className={styles.playbackBand}>
                <div className={styles.playbackLabel}>
                  <Route aria-hidden="true" size={15} />
                  <span>事故回放</span>
                </div>
                <PlaybackControls state={timeline} dispatch={dispatch} />
              </div>

              <TimelinePanel state={timeline} />
            </div>

            <ApprovalPanel
              approval={currentApproval}
              view={view}
              busy={pendingAction === "confirm" || pendingAction === "reject"}
              onConfirm={confirm}
              onReject={reject}
            />
          </div>
        </main>
      </div>
    </>
  );
}

function errorMessage(error: unknown): string {
  if (error instanceof APIError) {
    return error.message;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return "请求未完成，请检查服务状态";
}

function recoverySource<T>(
  result: PromiseSettledResult<T[]>,
): RecoverySource<T> {
  return result.status === "fulfilled"
    ? { kind: "ready", data: result.value }
    : { kind: "error", message: errorMessage(result.reason) };
}

function scopedRecoverySource<T extends { waybill_id: WaybillID }>(
  result: PromiseSettledResult<T[]>,
  waybillID: WaybillID | null,
): RecoverySource<T> {
  const source = recoverySource(result);
  if (source.kind !== "ready" || waybillID === null) {
    return source;
  }
  return {
    kind: "ready",
    data: source.data.filter((item) => item.waybill_id === waybillID),
  };
}

function toWaybillResource(selection: WaybillSelection): WaybillResource {
  switch (selection.kind) {
    case "empty":
      return selection;
    case "loading":
      return {
        kind: "loading",
        waybillID: targetWaybillID(selection.target),
      };
    case "error":
      return {
        kind: "error",
        waybillID: targetWaybillID(selection.target),
      };
    case "ready":
      return {
        kind: "ready",
        waybillID: selection.waybillID,
        view: selection.data,
      };
    default: {
      const exhaustive: never = selection;
      return exhaustive;
    }
  }
}

function targetWaybillID(target: SelectionTarget): WaybillID | null {
  switch (target.kind) {
    case "waybill":
    case "known_run":
      return target.waybillID;
    case "run":
      return null;
    default: {
      const exhaustive: never = target;
      return exhaustive;
    }
  }
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function compactID(value: string): string {
  return value.length > 18 ? `${value.slice(0, 8)}...${value.slice(-6)}` : value;
}

function formatHours(value: number): string {
  return new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 1,
  }).format(value);
}
