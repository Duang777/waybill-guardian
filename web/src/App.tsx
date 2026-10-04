import { AlertOctagon, Play, RotateCw, Route, X } from "lucide-react";
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
} from "./api";
import { ApprovalPanel } from "./components/ApprovalPanel";
import { RouteMap } from "./components/RouteMap";
import { SummaryStrip } from "./components/SummaryStrip";
import { PlaybackControls, TimelinePanel } from "./components/TimelinePanel";
import { preferredRecoveryRun } from "./recovery";
import {
  initialTimelineState,
  latestApproval,
  runStatus,
  timelineReducer,
  visibleEvents,
} from "./timeline";

type CatalogResource =
  | { kind: "loading" }
  | { kind: "empty" }
  | { kind: "ready"; data: readonly WaybillCatalogItem[] }
  | { kind: "error"; message: string };

type WaybillSelection =
  | { kind: "empty" }
  | { kind: "loading"; waybillID: WaybillID }
  | { kind: "ready"; waybillID: WaybillID; data: WaybillView }
  | { kind: "error"; waybillID: WaybillID; message: string };

type PendingAction = "bootstrap" | "trigger" | "confirm" | "reject" | null;

type SelectionRequest = {
  generation: number;
  controller: AbortController;
};

export default function App() {
  const [catalog, setCatalog] = useState<CatalogResource>({ kind: "loading" });
  const [selection, setSelection] = useState<WaybillSelection>({ kind: "empty" });
  const [run, setRun] = useState<RunSummary | null>(null);
  const [timelineAfter, setTimelineAfter] = useState<number | null>(null);
  const [timeline, dispatch] = useReducer(timelineReducer, initialTimelineState);
  const [connected, setConnected] = useState(false);
  const [pendingAction, setPendingAction] = useState<PendingAction>("bootstrap");
  const [message, setMessage] = useState<string | null>(null);
  const selectionGeneration = useRef(0);
  const selectionRequest = useRef<AbortController | null>(null);
  const catalogRequest = useRef<AbortController | null>(null);
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
      let waybillID = expectedWaybillID;
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
        waybillID = expectedWaybillID ?? snapshot.run.waybill_id;
        setRun(snapshot.run);
        setSelection({ kind: "loading", waybillID });
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
        if (waybillID === undefined) {
          setMessage(detail);
          setSelection({ kind: "empty" });
        } else {
          setSelection({ kind: "error", waybillID, message: detail });
        }
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
      if (expectedWaybillID !== undefined) {
        setSelection({ kind: "loading", waybillID: expectedWaybillID });
      }
      await hydrateRun(runID, request, expectedWaybillID);
    },
    [beginSelection, hydrateRun],
  );

  const loadWaybill = useCallback(
    async (waybillID: WaybillID): Promise<void> => {
      const request = beginSelection();
      setRun(null);
      setSelection({ kind: "loading", waybillID });
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
          waybillID,
          message: errorMessage(error),
        });
      }
    },
    [beginSelection],
  );

  useEffect(() => {
    const controller = new AbortController();
    void (async () => {
      const catalogPromise = loadCatalog();
      let recoveredRunID: RunID | null = null;
      try {
        const [activeRuns, pendingApprovals] = await Promise.all([
          listActiveRuns(controller.signal),
          listPendingApprovals(controller.signal),
        ]);
        recoveredRunID = preferredRecoveryRun({
          pendingApprovals,
          activeRuns,
        });
      } catch (error) {
        if (!isAbortError(error)) {
          setMessage(errorMessage(error));
        }
      }

      if (controller.signal.aborted) {
        return;
      }
      if (recoveredRunID !== null) {
        await selectRun({ runID: recoveredRunID });
      } else {
        const waybills = await catalogPromise;
        const firstWaybill = waybills?.[0];
        if (!controller.signal.aborted && firstWaybill !== undefined) {
          await loadWaybill(firstWaybill.waybill_id);
        }
      }
      if (!controller.signal.aborted) {
        setPendingAction(null);
      }
    })();

    return () => {
      controller.abort();
      catalogRequest.current?.abort();
      selectionRequest.current?.abort();
      catalogGeneration.current += 1;
      selectionGeneration.current += 1;
    };
  }, [loadCatalog, loadWaybill, selectRun]);

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
  const selectedWaybillID =
    selection.kind === "empty" ? null : selection.waybillID;
  const anomaly = view?.tracking.find((point) => point.anomaly) ?? null;
  const resourceError =
    selection.kind === "error"
      ? selection.message
      : catalog.kind === "error"
        ? catalog.message
        : null;
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
        waybillID: started.waybill_id,
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
    if (selection.kind === "error") {
      if (run !== null && run.waybill_id === selection.waybillID) {
        await selectRun({ runID: run.run_id });
      } else {
        await loadWaybill(selection.waybillID);
      }
      return;
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
              disabled={catalog.kind !== "ready" || pendingAction !== null}
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
            disabled={pendingAction !== null || selection.kind !== "ready"}
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
            view={view}
            waybillID={selectedWaybillID}
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
                      ? selectedWaybillID === null
                        ? "暂无异常轨迹"
                        : "等待异常轨迹"
                      : anomaly.stop_hours === undefined
                        ? anomaly.label
                        : `${anomaly.label}停留 ${formatHours(anomaly.stop_hours)} 小时`}
                  </span>
                </div>
                <RouteMap
                  points={view?.tracking ?? []}
                  origin={view?.waybill.origin ?? null}
                  destination={view?.waybill.destination ?? null}
                  hasSelection={selectedWaybillID !== null}
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
