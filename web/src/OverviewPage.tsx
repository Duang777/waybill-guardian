import {
  Bot,
  CheckCheck,
  ChevronRight,
  Clock3,
  FileText,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  APIError,
  getKPIs,
  getOverview,
  listActiveRuns,
  openTimeline,
  operatingKPIKeys,
  startBatch,
  type AuditEvent,
  type KPIReport,
  type KPIMetric,
  type OperatingKPIKey,
  type Overview,
  type Run,
  type RunID,
  type RunStatus,
  type WaybillID,
} from "./api";
import {
  HaloBadge,
  HaloSegmented,
  RollingNumber,
  TextureButton,
  TextureLink,
  type HaloBadgeTone,
} from "./components/cult";
import { HubNetwork } from "./components/HubNetwork";
import { PanelErrorBoundary } from "./components/PanelErrorBoundary";
import {
  SkeletonBlock,
  StateFeedback,
} from "./components/StateFeedback";
import { OverviewOperatingCharts } from "./charts/OverviewOperatingCharts";
import styles from "./overview.module.css";
import { anomalyTypeLabel } from "./overview-labels";
import {
  advanceRunProjection,
  effectiveRunStatus,
  mergeRunProjections,
  type RunProjection,
} from "./overview-run-projection";
import { overviewWorkbenchHref } from "./workbench-route";
import {
  requestIssueCopy,
  toRequestIssue,
  type RequestIssue,
} from "./request-issue";

type OverviewResource =
  | { kind: "loading" }
  | { kind: "ready"; overview: Overview; kpis: KPIReport }
  | { kind: "error"; issue: RequestIssue };

type BatchState =
  | { kind: "idle" }
  | { kind: "starting" }
  | { kind: "result"; message: string; failed: number };

type QueueView = "all" | "unassigned" | "active";

type RunStream = {
  runID: RunID;
  close: () => void;
};

const activeRunRefreshMilliseconds = 2_000;

type KPIContext = {
  anomalyCount: number;
  hasClosedRunSample: boolean;
};

type KPIPresentation =
  | { kind: "value"; value: number }
  | { kind: "empty"; value: 0; detail: string }
  | { kind: "unavailable"; detail: string };

const queueViews = [
  { value: "all", label: "全部" },
  { value: "unassigned", label: "待分派" },
  { value: "active", label: "处置中" },
] satisfies readonly { value: QueueView; label: string }[];

export function OverviewPage() {
  const [resource, setResource] = useState<OverviewResource>({ kind: "loading" });
  const [selected, setSelected] = useState<ReadonlySet<WaybillID>>(new Set());
  const [runProjections, setRunProjections] = useState<
    ReadonlyMap<WaybillID, RunProjection>
  >(new Map());
  const [batch, setBatch] = useState<BatchState>({ kind: "idle" });
  const [queueView, setQueueView] = useState<QueueView>("all");
  const request = useRef<AbortController | null>(null);
  const activeRequest = useRef<AbortController | null>(null);
  const streams = useRef<Map<WaybillID, RunStream>>(new Map());

  const watchRun = useCallback((run: Run) => {
    const currentStream = streams.current.get(run.waybill_id);
    if (currentStream?.runID === run.run_id) {
      if (isStreamTerminal(run.status)) {
        currentStream.close();
        streams.current.delete(run.waybill_id);
      }
      return;
    }
    currentStream?.close();
    streams.current.delete(run.waybill_id);
    if (isStreamTerminal(run.status)) {
      return;
    }

    const close = openTimeline(run.run_id, run.last_seq, {
      onEvent: (event) => {
        const status = statusFromEvent(event);
        setRunProjections((current) => {
          const existing = current.get(run.waybill_id) ?? {
            runID: run.run_id,
            status: run.status,
            lastSeq: run.last_seq,
          };
          const advanced = advanceRunProjection(existing, {
            runID: event.run_id,
            seq: event.seq,
            status,
          });
          if (advanced === existing && current.has(run.waybill_id)) {
            return current;
          }
          const next = new Map(current);
          next.set(run.waybill_id, advanced);
          return next;
        });
        if (status !== null && isStreamTerminal(status)) {
          streams.current.delete(run.waybill_id);
        }
      },
      onConnectionChange: () => undefined,
      onError: () => undefined,
    });
    streams.current.set(run.waybill_id, { runID: run.run_id, close });
  }, []);

  const syncRuns = useCallback(
    (runs: readonly Run[]) => {
      setRunProjections((current) => mergeRunProjections(current, runs));
      for (const run of runs) {
        watchRun(run);
      }
    },
    [watchRun],
  );

  const load = useCallback(async () => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setResource({ kind: "loading" });
    try {
      const [overview, kpis, activeRuns] = await Promise.all([
        getOverview(controller.signal),
        getKPIs("24h", controller.signal),
        listActiveRuns(controller.signal),
      ]);
      if (!controller.signal.aborted) {
        syncRuns(activeRuns);
        setResource({ kind: "ready", overview, kpis });
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        setResource({ kind: "error", issue: toRequestIssue(error) });
      }
    }
  }, [syncRuns]);

  const refreshActiveRuns = useCallback(async () => {
    if (activeRequest.current !== null) {
      return;
    }
    const controller = new AbortController();
    activeRequest.current = controller;
    try {
      const activeRuns = await listActiveRuns(controller.signal);
      if (!controller.signal.aborted) {
        syncRuns(activeRuns);
      }
    } catch {
      // The next interval retries without replacing the last known projection.
    } finally {
      if (activeRequest.current === controller) {
        activeRequest.current = null;
      }
    }
  }, [syncRuns]);

  useEffect(() => {
    void load();
    const interval = window.setInterval(
      () => void refreshActiveRuns(),
      activeRunRefreshMilliseconds,
    );
    return () => {
      window.clearInterval(interval);
      request.current?.abort();
      activeRequest.current?.abort();
      for (const stream of streams.current.values()) {
        stream.close();
      }
      streams.current.clear();
    };
  }, [load, refreshActiveRuns]);

  const anomalies =
    resource.kind === "ready" ? resource.overview.anomalies : [];
  const queueEntries = useMemo(
    () =>
      anomalies.map((item, index) => {
        const projection = runProjections.get(item.waybill_id);
        return {
          item,
          rank: index + 1,
          status: effectiveRunStatus({
            waybillID: item.waybill_id,
            snapshotStatus: item.run_status,
            projections: runProjections,
          }),
          href: overviewWorkbenchHref(
            item.waybill_id,
            projection?.runID,
            item.run_id,
          ),
        };
      }),
    [anomalies, runProjections],
  );
  const queueCounts = useMemo(
    () => ({
      all: queueEntries.length,
      unassigned: queueEntries.filter((entry) => entry.status === undefined).length,
      active: queueEntries.filter((entry) => isActive(entry.status)).length,
    }),
    [queueEntries],
  );
  const visibleQueueEntries = useMemo(
    () =>
      queueEntries.filter((entry) => {
        switch (queueView) {
          case "all":
            return true;
          case "unassigned":
            return entry.status === undefined;
          case "active":
            return isActive(entry.status);
          default: {
            const exhaustive: never = queueView;
            return exhaustive;
          }
        }
      }),
    [queueEntries, queueView],
  );

  const toggleSelection = (waybillID: WaybillID) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(waybillID)) {
        next.delete(waybillID);
      } else {
        next.add(waybillID);
      }
      return next;
    });
  };

  const selectTopFive = () => {
    setSelected(
      new Set(
        visibleQueueEntries
          .filter((entry) => !isActive(entry.status))
          .slice(0, 5)
          .map((entry) => entry.item.waybill_id),
      ),
    );
  };

  const startSelected = async () => {
    if (selected.size === 0 || batch.kind === "starting") {
      return;
    }
    const activeWaybillIDs = new Set(
      queueEntries
        .filter((entry) => isActive(entry.status))
        .map((entry) => entry.item.waybill_id),
    );
    const requestedWaybillIDs = [...selected].filter(
      (waybillID) => !activeWaybillIDs.has(waybillID),
    );
    if (requestedWaybillIDs.length === 0) {
      setSelected(new Set());
      setBatch({
        kind: "result",
        failed: 0,
        message: "所选运单已在处置中",
      });
      return;
    }
    setBatch({ kind: "starting" });
    try {
      const result = await startBatch(requestedWaybillIDs);
      const accepted = result.results.filter(
        (
          item,
        ): item is typeof item & { run: NonNullable<typeof item.run> } =>
          item.run !== undefined,
      );
      syncRuns(accepted.map((item) => item.run));
      setSelected(new Set());
      const failed = result.requested - result.accepted;
      setBatch({
        kind: "result",
        failed,
        message:
          failed === 0
            ? `${result.accepted} 个独立处置任务已启动`
            : `${result.accepted} 个已启动，${failed} 个未启动`,
      });
    } catch (error) {
      setBatch({
        kind: "result",
        failed: requestedWaybillIDs.length,
        message: errorMessage(error),
      });
    }
  };

  return (
    <>
      <a className={styles.skipLink} href="#overview-main">跳到经营总览</a>
      <div className={styles.shell}>
        <header className={styles.header}>
          <a className={styles.brand} href="/" aria-label="Waybill Guardian 经营总览">
            <span className={styles.brandMark} aria-hidden="true">WG</span>
            <span>
              <strong>Waybill Guardian</strong>
              <small>全国异常运力指挥</small>
            </span>
          </a>
          <div className={styles.headerStatus}>
            <HaloBadge
              className={styles.headerNetworkStatus}
              tone={networkStatusTone(resource)}
              live={
                resource.kind === "ready" &&
                resource.overview.network_available
              }
            >
              {networkStatusLabel(resource)}
            </HaloBadge>
            {resource.kind === "ready" && (
              <HaloBadge tone="neutral">
                {dataModeLabel(resource.overview.data_mode)}
              </HaloBadge>
            )}
          </div>
          <TextureButton
            type="button"
            variant="icon"
            size="icon"
            aria-label="刷新经营总览"
            title="刷新"
            disabled={resource.kind === "loading"}
            onClick={() => void load()}
          >
            <RefreshCw aria-hidden="true" size={17} />
          </TextureButton>
        </header>

        <main id="overview-main">
          <div className={styles.overviewHead}>
            <section className={styles.commandBand} aria-labelledby="overview-title">
              <div>
                <span className={styles.eyebrow}>全国经营 / 24 小时</span>
                <h1 id="overview-title">全国公路港异常态势</h1>
              </div>
              {resource.kind === "ready" && (
                <div className={styles.snapshot}>
                  <Clock3 aria-hidden="true" size={15} />
                  数据截至 {formatDate(resource.overview.as_of)}
                </div>
              )}
            </section>
            {resource.kind === "loading" ? (
              <OverviewKPISkeleton />
            ) : resource.kind === "ready" ? (
              <section className={styles.kpiStrip} aria-label="24 小时经营指标">
                <OverviewKPIBand
                  report={resource.kpis}
                  context={{
                    anomalyCount: resource.overview.totals.anomalies,
                    hasClosedRunSample: hasClosedRunSample(resource.kpis),
                  }}
                />
              </section>
            ) : null}
          </div>

          {resource.kind === "loading" && <LoadingOverview />}
          {resource.kind === "error" && (
            <StateFeedback
              className={styles.overviewState}
              tone="error"
              eyebrow="Overview error"
              title={requestIssueCopy(resource.issue).title}
              detail={requestIssueCopy(resource.issue).detail}
              action={
                <TextureButton
                  type="button"
                  variant="secondary"
                  onClick={() => void load()}
                >
                  <RefreshCw aria-hidden="true" size={16} />
                  重试
                </TextureButton>
              }
            />
          )}
          {resource.kind === "ready" && (
            <>
              <div className={styles.operationsGrid}>
                <section className={styles.mapPanel} aria-labelledby="map-heading">
                  <PanelErrorBoundary name="全国港网">
                    <HubNetwork
                      hubs={resource.overview.hubs}
                      routes={resource.overview.routes}
                      anomalies={resource.overview.anomalies}
                      runIDs={new Map(
                        [...runProjections].map(([waybillID, projection]) => [
                          waybillID,
                          projection.runID,
                        ]),
                      )}
                      dataMode={resource.overview.data_mode}
                      totals={resource.overview.totals}
                    />
                  </PanelErrorBoundary>
                </section>

                <OverviewOperatingCharts
                  overview={resource.overview}
                  runProjections={runProjections}
                />

                <section className={styles.queuePanel} aria-labelledby="queue-heading">
                  <div className={styles.queueHeader}>
                    <div>
                      <span className={styles.eyebrow}>风险队列</span>
                      <h2 id="queue-heading">异常处置队列</h2>
                    </div>
                    <span className={styles.sectionMeta}>{anomalies.length} 项</span>
                  </div>
                  <div className={styles.queueControls}>
                    <HaloSegmented
                      className={styles.queueSegments}
                      ariaLabel="筛选异常处置队列"
                      semantics="radio"
                      items={queueViews.map((view) => ({
                        value: view.value,
                        label: (
                          <>
                            {view.label}
                            <small aria-hidden="true">
                              <RollingNumber value={queueCounts[view.value]} />
                            </small>
                          </>
                        ),
                      }))}
                      value={queueView}
                      onValueChange={setQueueView}
                    />
                    <TextureButton
                      className={styles.selectButton}
                      type="button"
                      variant="minimal"
                      disabled={
                        !visibleQueueEntries.some(
                          (entry) => !isActive(entry.status),
                        )
                      }
                      onClick={selectTopFive}
                    >
                      <CheckCheck aria-hidden="true" size={15} />
                      选择前 5
                    </TextureButton>
                  </div>
                  <div className={styles.queueSummary}>
                    <span>
                      显示 {visibleQueueEntries.length} / {anomalies.length}
                      {selected.size > 0 ? ` · 已选 ${selected.size}` : ""}
                    </span>
                    <TextureButton
                      className={styles.batchButton}
                      type="button"
                      variant="primary"
                      disabled={selected.size === 0 || batch.kind === "starting"}
                      onClick={() => void startSelected()}
                    >
                      <Bot aria-hidden="true" size={16} />
                      {batch.kind === "starting"
                        ? "正在启动"
                        : `交给 Agent${selected.size > 0 ? ` · ${selected.size}` : ""}`}
                    </TextureButton>
                  </div>
                  {batch.kind === "result" && (
                    <div
                      className={batch.failed === 0 ? styles.batchSuccess : styles.batchWarning}
                      role="status"
                    >
                      {batch.message}
                    </div>
                  )}
                  <div className={styles.queueList}>
                    {visibleQueueEntries.length === 0 ? (
                      <StateFeedback
                        className={styles.queueEmptyState}
                        tone="empty"
                        eyebrow="Empty queue"
                        title={
                          anomalies.length === 0
                            ? "当前没有异常运单"
                            : "当前筛选没有任务"
                        }
                        detail={
                          anomalies.length === 0
                            ? "经营数据已加载完成，风险队列中没有待处置记录。"
                            : "切换筛选条件可查看其他处置状态。"
                        }
                        compact
                      />
                    ) : (
                      visibleQueueEntries.map(({ item, rank, status, href }) => (
                        <article className={styles.queueItem} key={item.waybill_id}>
                          <label className={styles.queueCheck}>
                            <input
                              type="checkbox"
                              checked={selected.has(item.waybill_id)}
                              disabled={
                                batch.kind === "starting" || isActive(status)
                              }
                              onChange={() => toggleSelection(item.waybill_id)}
                            />
                            <span className={styles.srOnly}>选择 {item.waybill_id}</span>
                          </label>
                          <span className={styles.queueRank}>
                            {String(rank).padStart(2, "0")}
                          </span>
                          <div className={styles.queueBody}>
                            <div className={styles.queueTitle}>
                              <a href={href}>
                                {item.origin} → {item.destination}
                              </a>
                              <span className={styles.riskScore}>
                                <RollingNumber
                                  value={item.risk_score}
                                  label={`风险分 ${item.risk_score}`}
                                />
                              </span>
                            </div>
                            <div className={styles.queueMeta}>
                              <span>{item.waybill_id}</span>
                              <span>{anomalyTypeLabel(item.type)}</span>
                              <HaloBadge tone={statusTone(status)}>
                                {runStatusLabel(status)}
                              </HaloBadge>
                            </div>
                          </div>
                          <TextureLink
                            className={styles.drilldown}
                            href={href}
                            variant="icon"
                            size="icon"
                            aria-label={`查看运单 ${item.waybill_id}`}
                            title="查看运单"
                          >
                            <ChevronRight aria-hidden="true" size={17} />
                          </TextureLink>
                        </article>
                      ))
                    )}
                  </div>
                </section>

                <section className={styles.briefSection} aria-labelledby="brief-heading">
                  <div className={styles.sectionHeader}>
                    <div>
                      <span className={styles.eyebrow}>只读经营洞察</span>
                      <h2 id="brief-heading">经营简报</h2>
                    </div>
                    <div className={styles.briefMeta}>
                      <HaloBadge tone="success">
                        <ShieldCheck aria-hidden="true" size={15} />
                        只读聚合
                      </HaloBadge>
                      <HaloBadge
                        tone={
                          resource.overview.brief.mode === "model_read_only"
                            ? "info"
                            : "neutral"
                        }
                        title={briefFallbackDetail(resource.overview.brief)}
                      >
                        {resource.overview.brief.mode === "model_read_only" ? (
                          <Bot aria-hidden="true" size={15} />
                        ) : (
                          <FileText aria-hidden="true" size={15} />
                        )}
                        {briefSourceLabel(resource.overview.brief)}
                      </HaloBadge>
                    </div>
                  </div>
                  <div className={styles.briefGrid}>
                    {resource.overview.brief.items.map((item, index) => (
                      <article className={styles.briefItem} key={item.id}>
                        <span className={styles.briefIndex}>0{index + 1}</span>
                        <div>
                          <h3>{item.headline}</h3>
                          <p>{item.body}</p>
                          <div className={styles.citations}>
                            {item.evidence.map((evidence) => (
                              <span key={`${item.id}-${evidence.source}-${evidence.label}`}>
                                {evidence.label} · {evidence.value}
                              </span>
                            ))}
                          </div>
                        </div>
                      </article>
                    ))}
                  </div>
                </section>
              </div>
            </>
          )}
        </main>
      </div>
    </>
  );
}

export function OverviewKPIBand({
  report,
  context,
}: {
  report: KPIReport;
  context: KPIContext;
}) {
  const byKey = new Map(report.metrics.map((metric) => [metric.key, metric]));
  return operatingKPIKeys.map((key) => {
    const metric = byKey.get(key);
    return metric === undefined ? null : (
      <OverviewKPI
        key={key}
        metricKey={key}
        metric={metric}
        context={context}
      />
    );
  });
}

export function OverviewKPI({
  metricKey,
  metric,
  context,
}: {
  metricKey: OperatingKPIKey;
  metric: KPIMetric;
  context: KPIContext;
}) {
  const presentation = kpiPresentation(metricKey, metric, context);
  const detail =
    presentation.kind === "value" ? metric.formula : presentation.detail;
  const detailID = `kpi-${metricKey}-detail`;
  return (
    <article
      className={[
        styles.kpi,
        presentation.kind === "empty" ? styles.kpiEmpty : "",
      ]
        .filter(Boolean)
        .join(" ")}
      title={detail}
    >
      <div className={styles.kpiLabel}>
        <span>{metric.label}</span>
        <span>24H</span>
      </div>
      <strong
        className={
          presentation.kind === "unavailable"
            ? styles.kpiUnavailable
            : undefined
        }
        aria-describedby={
          presentation.kind === "value" ? undefined : detailID
        }
      >
        {presentation.kind === "unavailable" ? (
          "待接入"
        ) : (
          <RollingNumber
            value={presentation.value}
            precision={1}
            format={(value) => formatMetricValue(value, metric.unit)}
            label={`${metric.label} ${formatMetricValue(
              presentation.value,
              metric.unit,
            )}`}
          />
        )}
      </strong>
      <span
        className={`${styles.kpiFoot} ${
          presentation.kind === "value" ? "" : styles.kpiFootVisible
        }`}
        id={detailID}
      >
        {detail}
      </span>
    </article>
  );
}

function kpiPresentation(
  metricKey: OperatingKPIKey,
  metric: KPIMetric,
  context: KPIContext,
): KPIPresentation {
  if (metric.value === null) {
    return {
      kind: "unavailable",
      detail: metric.reason ?? "当前指标缺少计算依据",
    };
  }
  if (
    metricKey === "average_handling_minutes" ||
    metricKey === "approval_rate_pct"
  ) {
    return { kind: "value", value: metric.value };
  }
  if (metric.value !== 0 || context.hasClosedRunSample) {
    return { kind: "value", value: metric.value };
  }
  switch (metricKey) {
    case "time_recovered_hours":
    case "cost_impact_cny":
      return { kind: "empty", value: 0, detail: "暂无已审批执行" };
    case "labor_saved_hours":
      return { kind: "empty", value: 0, detail: "尚未完成自动取证" };
    case "anomaly_closure_rate_pct":
      return {
        kind: "empty",
        value: 0,
        detail:
          context.anomalyCount === 0
            ? "当前无异常运单"
            : `${context.anomalyCount} 条异常待闭环`,
      };
    default: {
      const exhaustive: never = metricKey;
      return exhaustive;
    }
  }
}

function hasClosedRunSample(report: KPIReport): boolean {
  return report.metrics.some(
    (metric) =>
      metric.key === "average_handling_minutes" &&
      metric.availability === "available",
  );
}

function briefSourceLabel(brief: Overview["brief"]): string {
  if (brief.mode === "model_read_only") {
    return `模型生成 · ${brief.source}`;
  }
  const fallback = briefFallbackLabel(brief.fallback_reason);
  return fallback === null ? "规则模板" : `规则模板 · ${fallback}`;
}

function briefFallbackLabel(
  reason: Extract<
    Overview["brief"],
    { mode: "deterministic_read_only" }
  >["fallback_reason"],
): string | null {
  switch (reason) {
    case undefined:
      return null;
    case "timeout":
      return "模型超时";
    case "provider_error":
      return "模型不可用";
    case "schema":
      return "格式无效";
    case "evidence":
      return "证据无效";
    default: {
      const exhaustive: never = reason;
      return exhaustive;
    }
  }
}

function briefFallbackDetail(brief: Overview["brief"]): string {
  if (brief.mode === "model_read_only") {
    return `模型：${brief.source}`;
  }
  switch (brief.fallback_reason) {
    case "timeout":
      return "模型生成超时，已使用规则模板";
    case "provider_error":
      return "模型服务不可用，已使用规则模板";
    case "schema":
      return "模型输出格式无效，已使用规则模板";
    case "evidence":
      return "模型引用证据无效，已使用规则模板";
    default:
      return "规则模板";
  }
}

function networkStatusTone(resource: OverviewResource): HaloBadgeTone {
  switch (resource.kind) {
    case "loading":
      return "neutral";
    case "error":
      return "danger";
    case "ready":
      return resource.overview.network_available ? "success" : "warning";
    default: {
      const exhaustive: never = resource;
      return exhaustive;
    }
  }
}

function networkStatusLabel(resource: OverviewResource): string {
  switch (resource.kind) {
    case "loading":
      return "数据连接中";
    case "error":
      return "数据不可用";
    case "ready":
      return resource.overview.network_available ? "全国网络在线" : "网络数据不完整";
    default: {
      const exhaustive: never = resource;
      return exhaustive;
    }
  }
}

function dataModeLabel(mode: Overview["data_mode"]): string {
  switch (mode) {
    case "simulated":
      return "仿真数据";
    case "fixture":
      return "文件数据";
    case "external":
      return "外部数据";
    default: {
      const exhaustive: never = mode;
      return exhaustive;
    }
  }
}

function OverviewKPISkeleton() {
  return (
    <section
      className={styles.kpiStrip}
      aria-label="正在加载 24 小时经营指标"
      aria-busy="true"
    >
      {Array.from({ length: 6 }, (_, index) => (
        <article className={styles.kpiSkeleton} key={`kpi-skeleton-${index}`}>
          <SkeletonBlock className={styles.kpiSkeletonLabel} />
          <SkeletonBlock className={styles.kpiSkeletonValue} />
          <SkeletonBlock className={styles.kpiSkeletonDetail} />
        </article>
      ))}
    </section>
  );
}

function LoadingOverview() {
  return (
    <div
      className={styles.loadingOverview}
      aria-label="正在汇总全国经营数据"
      aria-live="polite"
      aria-busy="true"
    >
      <div className={styles.operationsGrid}>
        <section className={styles.mapPanel}>
          <div className={styles.overviewMapSkeleton}>
            <SkeletonBlock className={styles.mapSkeletonTitle} />
            <SkeletonBlock className={styles.mapSkeletonNetwork} />
            <SkeletonBlock className={styles.mapSkeletonLegend} />
          </div>
        </section>
        <section
          className={styles.overviewChartsSkeleton}
          aria-label="正在加载经营图表"
        >
          {Array.from({ length: 3 }, (_, index) => (
            <article key={`chart-skeleton-${index}`}>
              <div>
                <SkeletonBlock className={styles.chartSkeletonEyebrow} />
                <SkeletonBlock className={styles.chartSkeletonTitle} />
              </div>
              <SkeletonBlock className={styles.chartSkeletonPlot} />
            </article>
          ))}
        </section>
        <section className={styles.queuePanel}>
          <div className={styles.queueSkeletonHeader}>
            <SkeletonBlock className={styles.mapSkeletonTitle} />
            <SkeletonBlock className={styles.queueSkeletonControl} />
          </div>
          <div className={styles.queueSkeletonList}>
            {Array.from({ length: 5 }, (_, index) => (
              <SkeletonBlock
                className={styles.queueSkeletonItem}
                key={`queue-skeleton-${index}`}
              />
            ))}
          </div>
        </section>
        <section className={styles.briefSection}>
          <div className={styles.briefSkeletonHeader}>
            <SkeletonBlock className={styles.mapSkeletonTitle} />
            <SkeletonBlock className={styles.queueSkeletonControl} />
          </div>
          <div className={styles.briefGrid}>
            {Array.from({ length: 3 }, (_, index) => (
              <article className={styles.briefSkeletonItem} key={`brief-skeleton-${index}`}>
                <SkeletonBlock className={styles.briefSkeletonIndex} />
                <div>
                  <SkeletonBlock className={styles.briefSkeletonTitle} />
                  <SkeletonBlock className={styles.briefSkeletonLine} />
                  <SkeletonBlock className={styles.briefSkeletonLineShort} />
                </div>
              </article>
            ))}
          </div>
        </section>
      </div>
    </div>
  );
}

function statusFromEvent(event: AuditEvent): RunStatus | null {
  switch (event.type) {
    case "approval_requested":
      return "awaiting_approval";
    case "approval_decided":
    case "approval_reconciliation_required":
      return "executing";
    case "run_completed":
      return "completed";
    case "run_rejected":
      return "rejected";
    case "run_failed":
    case "approval_execution_failed":
      return "failed";
    case "run_review_required":
      return "review_required";
    case "model_call_started":
    case "model_call_finished":
    case "tool_call":
    case "tool_result":
    case "proposal_prepared":
    case "attribution":
      return "investigating";
    default:
      return null;
  }
}

function isStreamTerminal(status: RunStatus): boolean {
  return (
    status === "completed" ||
    status === "rejected" ||
    status === "failed" ||
    status === "review_required" ||
    status === "manual_review"
  );
}

function isActive(status: RunStatus | undefined): boolean {
  return (
    status === "started" ||
    status === "investigating" ||
    status === "awaiting_approval" ||
    status === "executing" ||
    status === "review_required" ||
    status === "manual_review"
  );
}

function runStatusLabel(status: RunStatus | undefined): string {
  switch (status) {
    case "started":
    case "investigating":
      return "分析中";
    case "awaiting_approval":
      return "待审批";
    case "executing":
      return "执行中";
    case "completed":
      return "已闭环";
    case "rejected":
      return "已驳回";
    case "failed":
      return "需介入";
    case "review_required":
      return "提案待复核";
    case "manual_review":
      return "人工复核";
    default:
      return "待分派";
  }
}

function statusTone(status: RunStatus | undefined): HaloBadgeTone {
  if (status === "completed") {
    return "success";
  }
  if (
    status === "started" ||
    status === "investigating" ||
    status === "awaiting_approval" ||
    status === "executing"
  ) {
    return "warning";
  }
  if (
    status === "failed" ||
    status === "review_required" ||
    status === "manual_review"
  ) {
    return "danger";
  }
  return "neutral";
}

function formatMetricValue(value: number, unit: string): string {
  if (unit === "元") {
    return new Intl.NumberFormat("zh-CN", {
      style: "currency",
      currency: "CNY",
      maximumFractionDigits: 0,
    }).format(value);
  }
  return `${new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 1,
  }).format(value)}${unit}`;
}

function formatDate(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

function errorMessage(error: unknown): string {
  if (error instanceof APIError || error instanceof Error) {
    return error.message;
  }
  return "请求未完成，请检查服务状态";
}
