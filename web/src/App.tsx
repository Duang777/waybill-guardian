import {
  AlertOctagon,
  ArrowLeft,
  Play,
  Radio,
  RotateCw,
  WifiOff,
  X,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { toast } from "sonner";
import styles from "./app.module.css";
import {
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
  type TimelineConnectionState,
  type WaybillCatalogItem,
  type WaybillID,
  type WaybillView,
} from "./api";
import {
  RouteMap,
  type RoutePointFocusRequest,
} from "./components/RouteMap";
import {
  EvidenceLedger,
  RunStageBar,
} from "./components/IncidentDossier";
import {
  ApprovalPanel,
  type ApprovalDecisionState,
} from "./components/ApprovalPanel";
import { Badge, type BadgeTone } from "./components/ui/badge";
import { Button, ButtonLink } from "./components/ui/button";
import { PanelErrorBoundary } from "./components/PanelErrorBoundary";
import {
  SkeletonBlock,
  StateFeedback,
} from "./components/StateFeedback";
import { SummaryStrip } from "./components/SummaryStrip";
import { AuditDrawer } from "./components/TimelinePanel";
import {
  decideRecovery,
  type RecoverySource,
} from "./recovery";
import {
  evidenceFocusTarget,
  inferenceMode,
  initialTimelineState,
  latestApproval,
  proposalForApproval,
  runStatus,
  timelineReducer,
  visibleEvents,
  type EvidenceSelection,
} from "./timeline";
import type { WaybillResource } from "./waybill-resource";
import {
  requestIssueCopy,
  requestIssueFromMessage,
  toRequestIssue,
  type RequestIssue,
} from "./request-issue";
import { OverviewPage } from "./OverviewPage";
import {
  formatWorkbenchHref,
  parseWorkbenchRoute,
  type WorkbenchTarget,
} from "./workbench-route";
import {
  connectionLabel,
  decisionUnavailableReason,
  workbenchConnectionState,
  type WorkbenchConnectionState,
} from "./workbench-connection";

type CatalogResource =
  | { kind: "loading" }
  | { kind: "empty" }
  | { kind: "ready"; data: readonly WaybillCatalogItem[] }
  | { kind: "error"; issue: RequestIssue };

type WaybillSelection =
  | { kind: "empty" }
  | { kind: "loading"; target: WorkbenchTarget }
  | { kind: "ready"; waybillID: WaybillID; data: WaybillView }
  | { kind: "error"; target: WorkbenchTarget; issue: RequestIssue };

type PendingAction = "bootstrap" | "trigger" | "confirm" | "reject" | null;

type DecisionFeedback = {
  approvalID: string;
  action: "confirm" | "reject";
};

type RejectFailure = {
  approvalID: string;
  issue: RequestIssue;
};

type SelectionRequest = {
  generation: number;
  controller: AbortController;
};

export default function App() {
  const route = parseWorkbenchRoute(
    window.location.pathname,
    window.location.search,
  );
  if (route.kind === "overview") {
    return <OverviewPage />;
  }
  if (route.kind === "invalid") {
    return <InvalidRoutePage />;
  }
  return <WaybillWorkbench initialTarget={route} />;
}

export function InvalidRoutePage() {
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
        </header>
        <main id="main-content" className={styles.routeError}>
          <AlertOctagon aria-hidden="true" size={28} />
          <span className={styles.eyebrow}>Invalid route</span>
          <h1>无法打开运单工作台</h1>
          <p>URL 中的运单或任务标识无效。</p>
          <ButtonLink href="/" variant="primary">
            <ArrowLeft aria-hidden="true" size={16} />
            返回全国经营总览
          </ButtonLink>
        </main>
      </div>
    </>
  );
}

function WaybillWorkbench({
  initialTarget,
}: {
  initialTarget: WorkbenchTarget;
}) {
  const [catalog, setCatalog] = useState<CatalogResource>({ kind: "loading" });
  const [selection, setSelection] = useState<WaybillSelection>({
    kind: "loading",
    target: initialTarget,
  });
  const [run, setRun] = useState<RunSummary | null>(null);
  const [timelineAfter, setTimelineAfter] = useState<number | null>(null);
  const [timeline, dispatch] = useReducer(timelineReducer, initialTimelineState);
  const [timelineConnection, setTimelineConnection] =
    useState<TimelineConnectionState>("closed");
  const [browserOnline, setBrowserOnline] = useState(() => navigator.onLine);
  const [pendingAction, setPendingAction] = useState<PendingAction>("bootstrap");
  const [decisionFeedback, setDecisionFeedback] =
    useState<DecisionFeedback | null>(null);
  const [rejectFailure, setRejectFailure] = useState<RejectFailure | null>(null);
  const [message, setMessage] = useState<RequestIssue | null>(null);
  const [recoveryError, setRecoveryError] = useState<RequestIssue | null>(null);
  const [routePointFocus, setRoutePointFocus] =
    useState<RoutePointFocusRequest | null>(null);
  const [auditOpen, setAuditOpen] = useState(false);
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
    setTimelineConnection("closed");
    setTimelineAfter(null);
    setRoutePointFocus(null);
    setAuditOpen(false);
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
      setCatalog({ kind: "error", issue: toRequestIssue(error) });
      return null;
    }
  }, []);

  const hydrateRun = useCallback(
    async (
      runID: RunID,
      request: SelectionRequest,
      waybillID: WaybillID,
    ): Promise<void> => {
      const target: WorkbenchTarget = { kind: "run", runID, waybillID };
      try {
        const snapshot = await getRunSnapshot(runID, request.controller.signal);
        if (selectionGeneration.current !== request.generation) {
          return;
        }
        if (snapshot.run.run_id !== runID || snapshot.run.waybill_id !== waybillID) {
          throw new Error("运行快照与目标身份不一致");
        }
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
        setSelection({ kind: "error", target, issue: toRequestIssue(error) });
      }
    },
    [],
  );

  const selectRun = useCallback(
    async ({
      runID,
      waybillID,
    }: {
      runID: RunID;
      waybillID: WaybillID;
    }): Promise<void> => {
      const request = beginSelection();
      setRun(null);
      setMessage(null);
      setSelection({
        kind: "loading",
        target: { kind: "run", runID, waybillID },
      });
      await hydrateRun(runID, request, waybillID);
    },
    [beginSelection, hydrateRun],
  );

  const loadWaybill = useCallback(
    async (waybillID: WaybillID): Promise<void> => {
      const request = beginSelection();
      setRun(null);
      const target: WorkbenchTarget = { kind: "waybill", waybillID };
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
          issue: toRequestIssue(error),
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
      if (initialTarget.kind === "run") {
        await selectRun({
          runID: initialTarget.runID,
          waybillID: initialTarget.waybillID,
        });
        return;
      }
      const [activeRuns, pendingApprovals] = await Promise.allSettled([
        listActiveRuns(controller.signal),
        listPendingApprovals(controller.signal),
      ]);
      if (controller.signal.aborted) {
        return;
      }
      const initialWaybillID = initialTarget.waybillID;
      const decision = decideRecovery({
        activeRuns: scopedRecoverySource(activeRuns, initialWaybillID),
        pendingApprovals: scopedRecoverySource(
          pendingApprovals,
          initialWaybillID,
        ),
      });
      if (decision.kind === "recover") {
        await selectRun({
          runID: decision.runID,
          waybillID: decision.waybillID,
        });
        return;
      }
      if (decision.kind === "blocked") {
        setRecoveryError(
          requestIssueFromMessage(decision.message, "unavailable"),
        );
        return;
      }
      const waybills = await catalogPromise;
      if (waybills?.length === 0) {
        setSelection({ kind: "empty" });
        return;
      }
      const targetWaybillID = initialWaybillID ?? waybills?.[0]?.waybill_id;
      if (!controller.signal.aborted && targetWaybillID !== undefined) {
        await loadWaybill(targetWaybillID);
      }
    } finally {
      if (bootstrapRequest.current === controller && !controller.signal.aborted) {
        setPendingAction(null);
      }
    }
  }, [initialTarget, loadCatalog, loadWaybill, selectRun]);

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
    const updateBrowserConnection = () => setBrowserOnline(navigator.onLine);
    window.addEventListener("online", updateBrowserConnection);
    window.addEventListener("offline", updateBrowserConnection);
    return () => {
      window.removeEventListener("online", updateBrowserConnection);
      window.removeEventListener("offline", updateBrowserConnection);
    };
  }, []);

  useEffect(() => {
    if (
      run === null ||
      timelineAfter === null ||
      isTerminalRunStatus(run.status)
    ) {
      return;
    }
    const generation = selectionGeneration.current;
    setTimelineConnection("connecting");
    const close = openTimeline(run.run_id, timelineAfter, {
      onEvent: (event) => {
        if (selectionGeneration.current === generation && event.run_id === run.run_id) {
          dispatch({ type: "event_received", event });
        }
      },
      onConnectionChange: (value) => {
        if (selectionGeneration.current === generation) {
          setTimelineConnection(value);
        }
      },
      onError: (value) => {
        if (selectionGeneration.current === generation) {
          setMessage(requestIssueFromMessage(value, "contract"));
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
  const workbenchEvents =
    timeline.focusedSeq === null ? projectedEvents : timeline.events;
  const currentApproval = useMemo(
    () => latestApproval(workbenchEvents),
    [workbenchEvents],
  );
  const currentApprovalID = currentApproval?.id ?? null;
  const currentProposal = useMemo(
    () => proposalForApproval(workbenchEvents, currentApproval),
    [currentApproval, workbenchEvents],
  );
  const currentStatus =
    runStatus(workbenchEvents) ??
    (timeline.playback.kind === "live" || timeline.focusedSeq !== null
      ? (run?.status ?? null)
      : null);
  const connection = workbenchConnectionState({
    runID: run?.run_id ?? null,
    runStatus: currentStatus,
    timelineConnection,
    browserOnline,
  });
  const currentInferenceMode = inferenceMode(timeline.events);
  const approvalDecisionState = decisionState({
    pendingAction,
    connection,
  });
  const view = selection.kind === "ready" ? selection.data : null;
  const waybillResource = toWaybillResource(selection);
  const selectedWaybillID =
    waybillResource.kind === "empty" ? null : waybillResource.waybillID;
  useEffect(() => {
    if (
      decisionFeedback === null ||
      currentApproval === null ||
      currentApproval.id !== decisionFeedback.approvalID ||
      currentApproval.status === "pending"
    ) {
      return;
    }
    const timer = window.setTimeout(() => {
      setDecisionFeedback((current) =>
        current?.approvalID === decisionFeedback.approvalID ? null : current,
      );
    }, 320);
    return () => window.clearTimeout(timer);
  }, [currentApproval, decisionFeedback]);

  useEffect(() => {
    if (selectedWaybillID === null) {
      return;
    }
    const target =
      selection.kind === "loading" || selection.kind === "error"
        ? selection.target
        : run !== null && run.waybill_id === selectedWaybillID
          ? {
              kind: "run" as const,
              runID: run.run_id,
              waybillID: selectedWaybillID,
            }
          : { kind: "waybill" as const, waybillID: selectedWaybillID };
    const href = formatWorkbenchHref(target);
    if (`${window.location.pathname}${window.location.search}` !== href) {
      window.history.replaceState(null, "", href);
    }
  }, [run, selectedWaybillID, selection]);
  const anomaly = view?.tracking.find((point) => point.anomaly) ?? null;
  const blockingIssue =
    recoveryError ??
    (selection.kind === "error"
      ? selection.issue
      : catalog.kind === "error" && selection.kind !== "ready"
        ? catalog.issue
        : null);
  const bannerIssue =
    message ??
    (catalog.kind === "error" && selection.kind === "ready"
      ? catalog.issue
      : null);
  const bannerCopy = bannerIssue === null ? null : requestIssueCopy(bannerIssue);
  const rejectError =
    rejectFailure !== null &&
    rejectFailure.approvalID === currentApprovalID
      ? requestIssueCopy(rejectFailure.issue)
      : null;

  useEffect(() => {
    setRejectFailure((current) =>
      current !== null && current.approvalID !== currentApprovalID
        ? null
        : current,
    );
  }, [currentApprovalID]);

  const selectEvidence = (selection: EvidenceSelection) => {
    const target = evidenceFocusTarget(
      timeline.events,
      view?.tracking ?? [],
      selection,
    );
    if (target === null) {
      return;
    }
    setAuditOpen(true);
    dispatch({ type: "focus_event", seq: target.seq });
    if (target.kind === "route_point") {
      setRoutePointFocus({
        sourceSeq: target.seq,
        pointIndex: target.pointIndex,
      });
    }
  };

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
          kind: "run",
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
        setMessage(toRequestIssue(error));
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
          await selectRun({
            runID: selection.target.runID,
            waybillID: selection.target.waybillID,
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
    setDecisionFeedback({
      approvalID: currentApproval.id,
      action: "confirm",
    });
    setRejectFailure(null);
    setPendingAction("confirm");
    setMessage(null);
    try {
      const decided = await confirmApproval(currentApproval.id);
      await selectRun({
        runID: decided.run_id,
        waybillID: decided.waybill_id,
      });
      toast.success("审批已通过", {
        description: "写操作已恢复执行，结果会继续写入审计时间线。",
      });
    } catch (error) {
      setDecisionFeedback((current) =>
        current?.approvalID === currentApproval.id ? null : current,
      );
      setMessage(toRequestIssue(error));
    } finally {
      setPendingAction(null);
    }
  };

  const reject = async (reason: string) => {
    if (currentApproval === null) {
      return;
    }
    setDecisionFeedback({
      approvalID: currentApproval.id,
      action: "reject",
    });
    setRejectFailure(null);
    setPendingAction("reject");
    setMessage(null);
    try {
      const decided = await rejectApproval(currentApproval.id, reason);
      await selectRun({
        runID: decided.run_id,
        waybillID: decided.waybill_id,
      });
      toast.success("驳回决定已提交", {
        description: "驳回原因已记录，Agent 将继续处理。",
      });
    } catch (error) {
      const issue = toRequestIssue(error);
      setDecisionFeedback((current) =>
        current?.approvalID === currentApproval.id ? null : current,
      );
      setRejectFailure({
        approvalID: currentApproval.id,
        issue,
      });
      setMessage(issue);
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
            <ButtonLink
              className={styles.backButton}
              href="/"
              variant="icon"
              size="icon"
              aria-label="返回全国经营总览"
              title="返回总览"
            >
              <ArrowLeft aria-hidden="true" size={17} />
            </ButtonLink>
            <span className={styles.brandMark} aria-hidden="true">
              WG
            </span>
            <div>
              <strong>Waybill Guardian</strong>
              <span>异常运单处置控制台</span>
            </div>
          </div>
          <label className={styles.waybillPicker}>
            <span>运单</span>
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
            <div>
              <span className={styles.runLabel}>当前任务</span>
              <span className={styles.mono}>
                {run === null ? "NOT STARTED" : compactID(run.run_id)}
              </span>
            </div>
            <div className={styles.runSignals}>
              <Badge
                tone={connectionTone(connection)}
                live={connection === "online"}
              >
                {connection === "offline" ? (
                  <WifiOff aria-hidden="true" size={12} />
                ) : (
                  <Radio aria-hidden="true" size={12} />
                )}
                {connectionLabel(connection)}
              </Badge>
              {run !== null && (
                <Badge tone={inferenceTone(currentInferenceMode)}>
                  {inferenceLabel(currentInferenceMode)}
                </Badge>
              )}
            </div>
          </div>
          <Button
            className={styles.triggerButton}
            type="button"
            variant="primary"
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
          </Button>
        </header>

        <main id="main-content" className={styles.main}>
          <SummaryStrip
            resource={waybillResource}
            status={currentStatus}
            connected={connection === "online"}
          />

          {bannerCopy !== null && (
            <div className={styles.errorBanner} role="alert">
              <AlertOctagon aria-hidden="true" size={17} />
              <span className={styles.errorCopy}>
                <strong>{bannerCopy.title}</strong>
                <span>{bannerCopy.detail}</span>
              </span>
              <div className={styles.errorActions}>
                {catalog.kind === "error" && message === null && (
                  <Button
                    type="button"
                    variant="secondary"
                    size="sm"
                    onClick={() => void retryResource()}
                  >
                    <RotateCw aria-hidden="true" size={15} />
                    重试
                  </Button>
                )}
                {message !== null && (
                  <Button
                    type="button"
                    variant="icon"
                    size="icon"
                    aria-label="关闭错误提示"
                    title="关闭"
                    onClick={() => setMessage(null)}
                  >
                    <X aria-hidden="true" size={16} />
                  </Button>
                )}
              </div>
            </div>
          )}

          {shouldShowConnectionNotice(connection) && (
            <div className={styles.connectionNotice} role="status">
              <WifiOff aria-hidden="true" size={16} />
              <span>
                <strong>{connectionLabel(connection)}</strong>
                审计流会自动恢复，连接恢复前审批操作保持锁定。
              </span>
            </div>
          )}

          {blockingIssue !== null ? (
            <StateFeedback
              className={styles.workbenchState}
              tone="error"
              eyebrow="Workbench error"
              title={requestIssueCopy(blockingIssue).title}
              detail={requestIssueCopy(blockingIssue).detail}
              action={
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => void retryResource()}
                >
                  <RotateCw aria-hidden="true" size={16} />
                  重试
                </Button>
              }
            />
          ) : waybillResource.kind === "loading" ? (
            <WorkbenchSkeleton />
          ) : waybillResource.kind === "empty" ? (
            <StateFeedback
              className={styles.workbenchState}
              tone="empty"
              eyebrow="Empty catalog"
              title="当前没有异常运单"
              detail="运单目录已加载完成，没有可进入处置工作台的异常记录。"
              action={
                <ButtonLink href="/" variant="primary">
                  <ArrowLeft aria-hidden="true" size={16} />
                  返回经营总览
                </ButtonLink>
              }
            />
          ) : waybillResource.kind === "ready" ? (
            <div className={styles.dossierWorkbench}>
              <RunStageBar
                timeline={timeline}
                runStatus={currentStatus}
                runID={run?.run_id ?? null}
                connection={connection}
              />

              <div
                className={styles.evidenceWorkspace}
                aria-label="空间证据工作区"
              >
                <section className={styles.mapPanel} aria-labelledby="route-map-title">
                  <div className={styles.panelTitleRow}>
                    <div>
                      <span className={styles.eyebrow}>
                        {timeline.playback.kind === "live"
                          ? "Spatial evidence / live"
                          : "Spatial evidence / replay"}
                      </span>
                      <h2 id="route-map-title">运输轨迹证据</h2>
                    </div>
                    <span className={styles.anomalyLegend}>
                      <span aria-hidden="true" />
                      {anomaly === null
                        ? "未发现异常轨迹"
                        : anomaly.stop_hours === undefined
                          ? "检测到异常节点"
                          : `异常停留 ${formatHours(anomaly.stop_hours)} 小时`}
                    </span>
                  </div>
                  <PanelErrorBoundary name="运输轨迹">
                    <RouteMap
                      points={waybillResource.view.tracking}
                      origin={waybillResource.view.waybill.origin}
                      destination={waybillResource.view.waybill.destination}
                      resourceKind="ready"
                      focusRequest={routePointFocus}
                    />
                  </PanelErrorBoundary>
                </section>

                <PanelErrorBoundary name="证据账本">
                  <EvidenceLedger
                    approval={currentApproval}
                    onEvidenceSelect={selectEvidence}
                  />
                </PanelErrorBoundary>
              </div>

              <div className={styles.decisionDock} aria-label="人工决策区">
                <PanelErrorBoundary name="人工决策闸">
                  <ApprovalPanel
                    approval={currentApproval}
                    proposal={currentProposal}
                    runStatus={currentStatus}
                    view={waybillResource.view}
                    decisionState={approvalDecisionState}
                    rejectError={rejectError}
                    transitionIntent={
                      decisionFeedback !== null &&
                      decisionFeedback.approvalID === currentApproval?.id
                        ? decisionFeedback.action
                        : null
                    }
                    embedded
                    showEvidence={false}
                    onEvidenceSelect={selectEvidence}
                    onConfirm={confirm}
                    onReject={reject}
                  />
                </PanelErrorBoundary>
              </div>

              <PanelErrorBoundary name="审计记录">
                <AuditDrawer
                  state={timeline}
                  dispatch={dispatch}
                  open={auditOpen}
                  onOpenChange={setAuditOpen}
                />
              </PanelErrorBoundary>
            </div>
          ) : null}
        </main>
      </div>
    </>
  );
}

function WorkbenchSkeleton() {
  return (
    <div
      className={`${styles.dossierWorkbench} ${styles.workbenchSkeleton}`}
      aria-label="正在加载运单工作台"
      aria-busy="true"
    >
      <section className={styles.skeletonStage}>
        <div>
          <SkeletonBlock className={styles.skeletonShort} />
          <SkeletonBlock className={styles.skeletonHeading} />
        </div>
        <div className={styles.skeletonSteps}>
          {Array.from({ length: 6 }, (_, index) => (
            <SkeletonBlock
              className={styles.skeletonStep}
              key={`stage-skeleton-${index}`}
            />
          ))}
        </div>
      </section>
      <div className={styles.evidenceWorkspace}>
        <section className={styles.mapPanel}>
          <div className={styles.skeletonPanelHeading}>
            <SkeletonBlock className={styles.skeletonHeading} />
            <SkeletonBlock className={styles.skeletonShort} />
          </div>
          <div className={styles.skeletonMap}>
            <SkeletonBlock className={styles.skeletonRoute} />
          </div>
        </section>
        <aside className={styles.skeletonLedger}>
          <SkeletonBlock className={styles.skeletonHeading} />
          <SkeletonBlock />
          <SkeletonBlock />
          <SkeletonBlock />
        </aside>
      </div>
      <div className={styles.decisionDock}>
        <div className={styles.skeletonDecision}>
          <SkeletonBlock className={styles.skeletonDecisionLabel} />
          <SkeletonBlock className={styles.skeletonDecisionBody} />
          <SkeletonBlock className={styles.skeletonDecisionAction} />
        </div>
      </div>
      <SkeletonBlock className={styles.skeletonAudit} />
    </div>
  );
}

function decisionState({
  pendingAction,
  connection,
}: {
  pendingAction: PendingAction;
  connection: WorkbenchConnectionState;
}): ApprovalDecisionState {
  if (pendingAction === "confirm" || pendingAction === "reject") {
    return { kind: "busy" };
  }
  const reason = decisionUnavailableReason(connection);
  return reason === null
    ? { kind: "ready" }
    : { kind: "unavailable", reason };
}

function shouldShowConnectionNotice(
  connection: WorkbenchConnectionState,
): boolean {
  return connection === "reconnecting" || connection === "offline";
}

function connectionTone(
  connection: WorkbenchConnectionState,
): BadgeTone {
  switch (connection) {
    case "online":
      return "success";
    case "connecting":
      return "info";
    case "reconnecting":
    case "offline":
      return "warning";
    case "idle":
    case "sealed":
      return "neutral";
    default: {
      const exhaustive: never = connection;
      return exhaustive;
    }
  }
}

function inferenceLabel(mode: ReturnType<typeof inferenceMode>): string {
  switch (mode.kind) {
    case "online":
      return mode.model === null ? "在线模型" : `在线模型 · ${mode.model}`;
    case "offline":
      return "离线规则";
    case "legacy":
      return "历史运行";
    default: {
      const exhaustive: never = mode;
      return exhaustive;
    }
  }
}

function inferenceTone(
  mode: ReturnType<typeof inferenceMode>,
): BadgeTone {
  switch (mode.kind) {
    case "online":
      return "info";
    case "offline":
    case "legacy":
      return "neutral";
    default: {
      const exhaustive: never = mode;
      return exhaustive;
    }
  }
}

function recoverySource<T>(
  result: PromiseSettledResult<T[]>,
): RecoverySource<T> {
  return result.status === "fulfilled"
    ? { kind: "ready", data: result.value }
    : { kind: "error", message: toRequestIssue(result.reason).detail };
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

function targetWaybillID(target: WorkbenchTarget): WaybillID {
  return target.waybillID;
}

function isTerminalRunStatus(status: RunSummary["status"]): boolean {
  return (
    status === "completed" ||
    status === "rejected" ||
    status === "failed" ||
    status === "review_required" ||
    status === "manual_review"
  );
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
