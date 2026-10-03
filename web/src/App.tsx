import { AlertOctagon, Play, RotateCw, Route, X } from "lucide-react";
import { useEffect, useMemo, useReducer, useState } from "react";
import styles from "./app.module.css";
import {
  APIError,
  DEFAULT_WAYBILL_ID,
  confirmApproval,
  getWaybill,
  openTimeline,
  rejectApproval,
  triggerDemo,
  type Run,
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
} from "./timeline";

type Resource<T> =
  | { kind: "loading" }
  | { kind: "ready"; data: T }
  | { kind: "error"; message: string };

type PendingAction = "trigger" | "confirm" | "reject" | null;

export default function App() {
  const [waybill, setWaybill] = useState<Resource<WaybillView>>({ kind: "loading" });
  const [run, setRun] = useState<Run | null>(null);
  const [timeline, dispatch] = useReducer(timelineReducer, initialTimelineState);
  const [connected, setConnected] = useState(false);
  const [pendingAction, setPendingAction] = useState<PendingAction>(null);
  const [message, setMessage] = useState<string | null>(null);

  useEffect(() => {
    let active = true;
    void getWaybill(DEFAULT_WAYBILL_ID)
      .then((data) => {
        if (active) {
          setWaybill({ kind: "ready", data });
        }
      })
      .catch((error: unknown) => {
        if (active) {
          setWaybill({ kind: "error", message: errorMessage(error) });
        }
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (run === null) {
      return;
    }
    setConnected(false);
    return openTimeline(run.run_id, {
      onEvent: (event) => dispatch({ type: "event_received", event }),
      onConnectionChange: setConnected,
      onError: setMessage,
    });
  }, [run]);

  useEffect(() => {
    if (timeline.playback.kind !== "playing") {
      return;
    }
    const timer = window.setInterval(() => dispatch({ type: "tick" }), 650);
    return () => window.clearInterval(timer);
  }, [timeline.playback.kind]);

  const currentApproval = useMemo(
    () => latestApproval(timeline.events),
    [timeline.events],
  );
  const currentStatus = runStatus(timeline.events) ?? run?.status ?? null;
  const view = waybill.kind === "ready" ? waybill.data : null;

  const startDemo = async () => {
    setPendingAction("trigger");
    setMessage(null);
    dispatch({ type: "reset" });
    try {
      const started = await triggerDemo();
      setRun(started);
      const data = await getWaybill(started.waybill_id);
      setWaybill({ kind: "ready", data });
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

function compactID(value: string): string {
  return value.length > 18 ? `${value.slice(0, 8)}...${value.slice(-6)}` : value;
}
