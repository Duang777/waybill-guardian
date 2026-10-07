import { lazy, Suspense, useMemo } from "react";
import type { Overview, WaybillID } from "../api";
import { PanelErrorBoundary } from "../components/PanelErrorBoundary";
import {
  SkeletonBlock,
  StateFeedback,
} from "../components/StateFeedback";
import {
  buildOverviewChartModel,
  isReadyOverviewChart,
  type OverviewChartModel,
  type ReadyOverviewChart,
} from "../overview-chart-model";
import type { RunProjection } from "../overview-run-projection";
import styles from "./overview-charts.module.css";

const OverviewEChart = lazy(() => import("./OverviewEChart"));

export function OverviewOperatingCharts({
  overview,
  runProjections,
}: {
  overview: Overview;
  runProjections: ReadonlyMap<WaybillID, RunProjection>;
}) {
  const model = useMemo(
    () => buildOverviewChartModel({ overview, runProjections }),
    [overview, runProjections],
  );

  return (
    <section
      className={styles.section}
      aria-labelledby="operating-charts-heading"
      data-overview-charts
    >
      <h2 className={styles.srOnly} id="operating-charts-heading">
        当前经营结构
      </h2>
      <div className={styles.grid}>
        {model.charts.map((chart) => (
          <ChartPanel chart={chart} key={chart.kind} />
        ))}
      </div>
    </section>
  );
}

function ChartPanel({ chart }: { chart: OverviewChartModel }) {
  const detail = chartDetail(chart);
  return (
    <figure
      className={styles.panel}
      data-operating-chart={chart.kind}
    >
      <figcaption
        className={[
          styles.caption,
          detail === null ? styles.captionCompact : "",
        ]
          .filter(Boolean)
          .join(" ")}
      >
        <div>
          <span className={styles.eyebrow}>{basisLabel(chart)}</span>
          <h3>{chart.title}</h3>
        </div>
        {detail !== null && <p>{detail}</p>}
      </figcaption>
      <ChartPanelContent chart={chart} />
    </figure>
  );
}

function ChartPanelContent({ chart }: { chart: OverviewChartModel }) {
  if (chart.content.kind === "empty") {
    return (
      <StateFeedback
        className={styles.empty}
        tone="empty"
        eyebrow="No data"
        title={chart.content.title}
        detail={chart.content.detail}
        compact
      />
    );
  }
  if (!isReadyOverviewChart(chart)) {
    return null;
  }
  return (
    <div className={styles.body}>
      <div className={styles.viewport}>
        <PanelErrorBoundary name={chart.title}>
          <Suspense fallback={<ChartSkeleton />}>
            <OverviewEChart chart={chart} />
          </Suspense>
        </PanelErrorBoundary>
      </div>
      <ChartFacts chart={chart} />
    </div>
  );
}

function ChartFacts({ chart }: { chart: ReadyOverviewChart }) {
  switch (chart.kind) {
    case "anomaly-composition":
    case "disposition-composition":
      return (
        <ul className={styles.factList} aria-label={`${chart.title}明细`}>
          {chart.content.items.map((item, index) => (
            <li key={item.key}>
              <i
                aria-hidden="true"
                data-chart-color={String((index % 7) + 1)}
                data-disposition-key={
                  chart.kind === "disposition-composition"
                    ? item.key
                    : undefined
                }
              />
              <span>{item.label}</span>
              <strong>{item.count}</strong>
              <small>{formatPercentage(item.percentage)}</small>
            </li>
          ))}
        </ul>
      );
    case "risky-routes":
      return (
        <ol className={styles.routeList} aria-label="高异常占比线路明细">
          {chart.content.items.map((item) => (
            <li key={item.routeID}>
              <span>{item.label}</span>
              <strong>{item.anomalyRatePercentage}%</strong>
              <small>
                {item.anomalies}/{item.waybills} 单 · 最高风险 {item.maxRisk}
              </small>
            </li>
          ))}
        </ol>
      );
    default: {
      const exhaustive: never = chart;
      return exhaustive;
    }
  }
}

function ChartSkeleton() {
  return (
    <div
      className={styles.chartSkeleton}
      aria-label="正在加载经营图表"
      aria-busy="true"
    >
      <SkeletonBlock className={styles.skeletonPlot} />
    </div>
  );
}

function basisLabel(chart: OverviewChartModel): string {
  const time = formatDate(chart.basis.asOf);
  return chart.basis.kind === "overview-snapshot-with-live-status"
    ? `${time} 范围 · 状态实时`
    : `${time} 快照`;
}

function chartDetail(chart: OverviewChartModel): string | null {
  switch (chart.kind) {
    case "anomaly-composition":
    case "risky-routes":
      return null;
    case "disposition-composition":
      return "异常范围来自快照，处置状态含本页实时更新";
    default: {
      const exhaustive: never = chart;
      return exhaustive;
    }
  }
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

function formatPercentage(value: number): string {
  return `${new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 1,
  }).format(value)}%`;
}
