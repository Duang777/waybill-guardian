import { AlertOctagon, Play, RotateCw, Route, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import styles from "./app.module.css";
import {
  APIError,
  DEFAULT_WAYBILL_ID,
  confirmApproval,
  getRunSnapshot,
  getWaybill,
  listActiveRuns,
  listPendingApprovals,
  openTimeline,
  rejectApproval,
  triggerDemo,
  type RunID,
  type RunSummary,
  type WaybillView,
} from "./api";
import { ApprovalPanel } from "./components/ApprovalPanel";
import { RouteMap } from "./components/RouteMap";
import { SummaryStrip } from "./components/SummaryStrip";
import { PlaybackControls, TimelinePanel } from "./components/TimelinePanel";
import {
  initialTimelineState,
  latestApproval,
  runStatus,
  timelineReducer,
  visibleEvents,
} from "./timeline";

type Resource<T> =
  | { kind: "loading" }
  | { kind: "ready"; data: T }
  | { kind: "error"; message: string };

type PendingAction = "bootstrap" | "trigger" | "confirm" | "reject" | null;

export default function App() {
  const [waybill, setWaybill] = useState<Resource<WaybillView>>({ kind: "loading" });
  const [run, setRun] = useState<RunSummary | null>(null);
  const [timelineAfter, setTimelineAfter] = useState<number | null>(null);
  const [timeline, dispatch] = useReducer(timelineReducer, initialTimelineState);
  const [connected, setConnected] = useState(false);
  const [pendingAction, setPendingAction] = useState<PendingAction>("bootstrap");
  const [message, setMessage] = useState<string | null>(null);
  const selectionGeneration = useRef(0);
  const closeTimeline = useRef<(() => void) | null>(null);

  const selectRun = useCallback(async (runID: RunID, signal?: AbortSignal) => {
    closeTimeline.current?.();
    closeTimeline.current = null;
    const generation = selectionGeneration.current + 1;
    selectionGeneration.current = generation;
    setConnected(false);
    setTimelineAfter(null);
    dispatch({ type: "reset" });

    const snapshot = await getRunSnapshot(runID, signal);
    if (selectionGeneration.current !== generation) {
      return;
    }
    setRun(snapshot.run);
    dispatch({ type: "hydrate", events: snapshot.events });
    setTimelineAfter(snapshot.run.last_seq);

    const data = await getWaybill(snapshot.run.waybill_id);
    if (selectionGeneration.current === generation) {
      setWaybill({ kind: "ready", data });
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void Promise.all([
      listActiveRuns(controller.signal),
      listPendingApprovals(controller.signal),
    ])
      .then(async ([runs, approvals]) => {
        const selectedRunID = approvals[0]?.run_id ?? runs[0]?.run_id;
        if (selectedRunID !== undefined) {
          await selectRun(selectedRunID, controller.signal);
          return;
        }
        const data = await getWaybill(DEFAULT_WAYBILL_ID);
        if (!controller.signal.aborted) {
          setWaybill({ kind: "ready", data });
        }
      })
      .catch((error: unknown) => {
        if (!isAbortError(error)) {
          setMessage(errorMessage(error));
          setWaybill({ kind: "error", message: errorMessage(error) });
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setPendingAction(null);
        }
      });
    return () => {
      controller.abort();
      selectionGeneration.current += 1;
    };
  }, [selectRun]);

  useEffect(() => {
    if (run === null || timelineAfter === null) {
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
  const view = waybill.kind === "ready" ? waybill.data : null;

  const startDemo = async () => {
    closeTimeline.current?.();
    closeTimeline.current = null;
    selectionGeneration.current += 1;
    setRun(null);
    setTimelineAfter(null);
    setConnected(false);
    dispatch({ type: "reset" });
    setPendingAction("trigger");
    setMessage(null);
    try {
      const started = await triggerDemo();
      await selectRun(started.run_id);
    } catch (error) {
      setMessage(errorMessage(error));
    } finally {
      setPendingAction(null);
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
          <div className={styles.runContext}>
            <span className={styles.runLabel}>ACTIVE RUN</span>
            <span className={styles.mono}>
              {run === null ? "NOT STARTED" : compactID(run.run_id)}
            </span>
          </div>
          <button
            className={styles.triggerButton}
            type="button"
            disabled={pendingAction !== null}
            onClick={() => void startDemo()}
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
                ? "启动演示"
                : "重新演示"}
          </button>
        </header>

        <main id="main-content" className={styles.main}>
          <SummaryStrip view={view} status={currentStatus} connected={connected} />

          {message !== null && (
            <div className={styles.errorBanner} role="alert">
              <AlertOctagon aria-hidden="true" size={17} />
              <span>{message}</span>
              <button
                type="button"
                aria-label="关闭错误提示"
                title="关闭"
                onClick={() => setMessage(null)}
              >
                <X aria-hidden="true" size={16} />
              </button>
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
                    绵阳停留 6 小时
                  </span>
                </div>
                <RouteMap points={view?.tracking ?? []} />
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
