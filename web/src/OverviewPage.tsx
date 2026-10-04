import {
  AlertTriangle,
  Bot,
  CheckCheck,
  ChevronRight,
  CircleGauge,
  Clock3,
  RefreshCw,
  Route,
  ShieldCheck,
} from "lucide-react";
import type { ReactNode } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  APIError,
  getKPIs,
  getOverview,
  openTimeline,
  startBatch,
  type AuditEvent,
  type KPIReport,
  type KPIMetric,
  type Overview,
  type RunStatus,
  type WaybillID,
} from "./api";
import { HubNetwork } from "./components/HubNetwork";
import styles from "./overview.module.css";

type OverviewResource =
  | { kind: "loading" }
  | { kind: "ready"; overview: Overview; kpis: KPIReport }
  | { kind: "error"; message: string };

type BatchState =
  | { kind: "idle" }
  | { kind: "starting" }
  | { kind: "result"; message: string; failed: number };

const primaryKPIKeys = [
  "time_recovered_hours",
  "cost_impact_cny",
  "labor_saved_hours",
  "anomaly_closure_rate_pct",
] as const;

export function OverviewPage() {
  const [resource, setResource] = useState<OverviewResource>({ kind: "loading" });
  const [selected, setSelected] = useState<ReadonlySet<WaybillID>>(new Set());
  const [runStatuses, setRunStatuses] = useState<
    ReadonlyMap<WaybillID, RunStatus>
  >(new Map());
  const [batch, setBatch] = useState<BatchState>({ kind: "idle" });
  const request = useRef<AbortController | null>(null);
  const streams = useRef<Map<WaybillID, () => void>>(new Map());

  const load = useCallback(async () => {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setResource({ kind: "loading" });
    try {
      const [overview, kpis] = await Promise.all([
        getOverview(controller.signal),
        getKPIs("24h", controller.signal),
      ]);
      if (!controller.signal.aborted) {
        setResource({ kind: "ready", overview, kpis });
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        setResource({ kind: "error", message: errorMessage(error) });
      }
    }
  }, []);

  useEffect(() => {
    void load();
    return () => {
      request.current?.abort();
      for (const close of streams.current.values()) {
        close();
      }
      streams.current.clear();
    };
  }, [load]);

  const anomalies =
    resource.kind === "ready" ? resource.overview.anomalies : [];
  const primaryKPIs = useMemo(() => {
    if (resource.kind !== "ready") {
      return [];
    }
    const byKey = new Map(resource.kpis.metrics.map((metric) => [metric.key, metric]));
    return primaryKPIKeys.flatMap((key) => {
      const metric = byKey.get(key);
      return metric === undefined ? [] : [metric];
    });
  }, [resource]);

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
    setSelected(new Set(anomalies.slice(0, 5).map((item) => item.waybill_id)));
  };

  const startSelected = async () => {
    if (selected.size === 0 || batch.kind === "starting") {
      return;
    }
    setBatch({ kind: "starting" });
    try {
      const result = await startBatch([...selected]);
      const accepted = result.results.filter(
        (
          item,
        ): item is typeof item & { run: NonNullable<typeof item.run> } =>
          item.run !== undefined,
      );
      setRunStatuses((current) => {
        const next = new Map(current);
        for (const item of accepted) {
          next.set(item.waybill_id, item.run.status);
        }
        return next;
      });
      for (const item of accepted) {
        streams.current.get(item.waybill_id)?.();
        let close: () => void = () => {};
        close = openTimeline(item.run.run_id, item.run.last_seq, {
          onEvent: (event) => {
            const status = statusFromEvent(event);
            if (status !== null) {
              setRunStatuses((current) => {
                const next = new Map(current);
                next.set(item.waybill_id, status);
                return next;
              });
            }
            if (status !== null && isTerminal(status)) {
              close();
              streams.current.delete(item.waybill_id);
            }
          },
          onConnectionChange: () => undefined,
          onError: () => undefined,
        });
        streams.current.set(item.waybill_id, close);
      }
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
        failed: selected.size,
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
          <div className={styles.headerContext}>
            <span className={styles.liveDot} aria-hidden="true" />
            <span>全国网络</span>
            {resource.kind === "ready" && (
              <span className={styles.dataBadge}>
                {resource.overview.data_mode === "simulated" ? "仿真数据" : "业务数据"}
              </span>
            )}
          </div>
          <button
            className={styles.iconButton}
            type="button"
            aria-label="刷新经营总览"
            title="刷新"
            disabled={resource.kind === "loading"}
            onClick={() => void load()}
          >
            <RefreshCw aria-hidden="true" size={17} />
          </button>
        </header>

        <main id="overview-main">
          <section className={styles.commandBand} aria-labelledby="overview-title">
            <div>
              <span className={styles.eyebrow}>Executive control tower</span>
              <h1 id="overview-title">全国公路港异常总览</h1>
            </div>
            {resource.kind === "ready" && (
              <div className={styles.snapshot}>
                <Clock3 aria-hidden="true" size={15} />
                数据截至 {formatDate(resource.overview.as_of)}
              </div>
            )}
          </section>

          {resource.kind === "loading" && <LoadingOverview />}
          {resource.kind === "error" && (
            <section className={styles.errorState} role="alert">
              <AlertTriangle aria-hidden="true" size={20} />
              <div>
                <strong>经营数据暂不可用</strong>
                <span>{resource.message}</span>
              </div>
              <button type="button" onClick={() => void load()}>
                <RefreshCw aria-hidden="true" size={16} />
                重试
              </button>
            </section>
          )}
          {resource.kind === "ready" && (
            <>
              <section className={styles.kpiStrip} aria-label="24 小时经营指标">
                {primaryKPIs.map((metric) => (
                  <KPI key={metric.key} metric={metric} />
                ))}
              </section>

              <section className={styles.operationalStrip} aria-label="网络运行摘要">
                <OperationalFact
                  icon={<Route aria-hidden="true" size={17} />}
                  value={resource.overview.hubs.length}
                  label="覆盖公路港"
                />
                <OperationalFact
                  icon={<CircleGauge aria-hidden="true" size={17} />}
                  value={resource.overview.totals.in_flight}
                  label="在途运单"
                />
                <OperationalFact
                  icon={<AlertTriangle aria-hidden="true" size={17} />}
                  value={resource.overview.totals.anomalies}
                  label="异常运单"
                  signal
                />
                <OperationalFact
                  icon={<Bot aria-hidden="true" size={17} />}
                  value={resource.overview.totals.handling}
                  label="Agent 处置中"
                />
              </section>

              <div className={styles.workspace}>
                <section className={styles.mapPanel} aria-labelledby="map-heading">
                  <div className={styles.sectionHeader}>
                    <div>
                      <span className={styles.eyebrow}>Network pulse</span>
                      <h2 id="map-heading">公路港网络态势</h2>
                    </div>
                    <span className={styles.sectionMeta}>
                      {resource.overview.routes.length} 条线路
                    </span>
                  </div>
                  <HubNetwork
                    hubs={resource.overview.hubs}
                    routes={resource.overview.routes}
                    anomalies={resource.overview.anomalies}
                  />
                </section>

                <section className={styles.queuePanel} aria-labelledby="queue-heading">
                  <div className={styles.queueHeader}>
                    <div>
                      <span className={styles.eyebrow}>Risk queue</span>
                      <h2 id="queue-heading">异常处置队列</h2>
                    </div>
                    <button
                      className={styles.selectButton}
                      type="button"
                      disabled={anomalies.length === 0}
                      onClick={selectTopFive}
                    >
                      <CheckCheck aria-hidden="true" size={15} />
                      选择前 5
                    </button>
                  </div>
                  <div className={styles.queueSummary}>
                    <span>{selected.size} 项已选择</span>
                    <button
                      className={styles.batchButton}
                      type="button"
                      disabled={selected.size === 0 || batch.kind === "starting"}
                      onClick={() => void startSelected()}
                    >
                      <Bot aria-hidden="true" size={16} />
                      {batch.kind === "starting"
                        ? "正在启动"
                        : `交给 Agent${selected.size > 0 ? ` · ${selected.size}` : ""}`}
                    </button>
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
                    {anomalies.map((item, index) => {
                      const status = runStatuses.get(item.waybill_id) ?? item.run_status;
                      return (
                        <article className={styles.queueItem} key={item.waybill_id}>
                          <label className={styles.queueCheck}>
                            <input
                              type="checkbox"
                              checked={selected.has(item.waybill_id)}
                              disabled={batch.kind === "starting"}
                              onChange={() => toggleSelection(item.waybill_id)}
                            />
                            <span className={styles.srOnly}>选择 {item.waybill_id}</span>
                          </label>
                          <span className={styles.queueRank}>{String(index + 1).padStart(2, "0")}</span>
                          <div className={styles.queueBody}>
                            <div className={styles.queueTitle}>
                              <a href={`/waybills/${encodeURIComponent(item.waybill_id)}`}>
                                {item.origin} → {item.destination}
                              </a>
                              <span className={styles.riskScore}>{item.risk_score}</span>
                            </div>
                            <div className={styles.queueMeta}>
                              <span>{item.waybill_id}</span>
                              <span>{anomalyLabel(item.type)}</span>
                              <span className={statusClass(status, styles)}>
                                {runStatusLabel(status)}
                              </span>
                            </div>
                          </div>
                          <a
                            className={styles.drilldown}
                            href={`/waybills/${encodeURIComponent(item.waybill_id)}`}
                            aria-label={`查看运单 ${item.waybill_id}`}
                            title="查看运单"
                          >
                            <ChevronRight aria-hidden="true" size={17} />
                          </a>
                        </article>
                      );
                    })}
                  </div>
                </section>
              </div>

              <section className={styles.briefSection} aria-labelledby="brief-heading">
                <div className={styles.sectionHeader}>
                  <div>
                    <span className={styles.eyebrow}>Read-only intelligence</span>
                    <h2 id="brief-heading">经营简报</h2>
                  </div>
                  <span className={styles.readOnly}>
                    <ShieldCheck aria-hidden="true" size={15} />
                    只读聚合
                  </span>
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
            </>
          )}
        </main>
      </div>
    </>
  );
}

function KPI({ metric }: { metric: KPIMetric }) {
  return (
    <article className={styles.kpi}>
      <div className={styles.kpiLabel}>
        <span>{metric.label}</span>
        <span>24H</span>
      </div>
      <strong className={metric.value === null ? styles.kpiUnavailable : undefined}>
        {metric.value === null ? "待接入" : formatMetric(metric)}
      </strong>
      <span className={styles.kpiFoot}>
        {metric.value === null ? metric.reason : metric.formula}
      </span>
    </article>
  );
}

function OperationalFact({
  icon,
  value,
  label,
  signal = false,
}: {
  icon: ReactNode;
  value: number;
  label: string;
  signal?: boolean;
}) {
  return (
    <div className={signal ? styles.operationalSignal : styles.operationalFact}>
      {icon}
      <strong>{value}</strong>
      <span>{label}</span>
    </div>
  );
}

function LoadingOverview() {
  return (
    <div className={styles.loadingState} aria-live="polite">
      <span className={styles.loadingLine} />
      <span className={styles.loadingLine} />
      <span className={styles.loadingLine} />
      <span>正在汇总全国网络</span>
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

function isTerminal(status: RunStatus): boolean {
  return (
    status === "completed" ||
    status === "rejected" ||
    status === "failed" ||
    status === "review_required"
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

function statusClass(
  status: RunStatus | undefined,
  classNames: typeof styles,
): string {
  if (status === "completed") {
    return classNames.statusComplete;
  }
  if (
    status === "started" ||
    status === "investigating" ||
    status === "awaiting_approval" ||
    status === "executing"
  ) {
    return classNames.statusActive;
  }
  if (
    status === "failed" ||
    status === "review_required" ||
    status === "manual_review"
  ) {
    return classNames.statusDanger;
  }
  return classNames.statusIdle;
}

function anomalyLabel(value: string): string {
  const labels: Record<string, string> = {
    delay: "时效延误",
    damage: "货损",
    fatigue: "疲劳驾驶",
    loss: "货物丢失",
    weather: "天气影响",
  };
  return labels[value] ?? value;
}

function formatMetric(metric: KPIMetric): string {
  if (metric.value === null) {
    return "待接入";
  }
  if (metric.unit === "元") {
    return new Intl.NumberFormat("zh-CN", {
      style: "currency",
      currency: "CNY",
      maximumFractionDigits: 0,
    }).format(metric.value);
  }
  return `${new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 1,
  }).format(metric.value)}${metric.unit}`;
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
