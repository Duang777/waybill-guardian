import type {
  Overview,
  RouteOverview,
  RunStatus,
  WaybillID,
} from "./api";
import { anomalyTypeLabel } from "./overview-labels";
import {
  effectiveRunStatus,
  type RunProjection,
} from "./overview-run-projection";

type NonEmpty<T> = readonly [T, ...T[]];

type EmptyChartContent = {
  kind: "empty";
  title: string;
  detail: string;
};

type CountChartContent<T> =
  | EmptyChartContent
  | {
      kind: "ready";
      total: number;
      items: NonEmpty<T>;
    };

type RankingChartContent<T> =
  | EmptyChartContent
  | {
      kind: "ready";
      items: NonEmpty<T>;
    };

type SnapshotBasis =
  | {
      kind: "overview-snapshot";
      asOf: Overview["as_of"];
    }
  | {
      kind: "overview-snapshot-with-live-status";
      asOf: Overview["as_of"];
    };

export type CountSlice = {
  key: string;
  label: string;
  count: number;
  percentage: number;
};

export type DispositionKey =
  | "unassigned"
  | "investigating"
  | "awaiting_approval"
  | "executing"
  | "completed"
  | "rejected"
  | "needs_attention";

export type DispositionSlice = CountSlice & {
  key: DispositionKey;
};

export type RouteRiskRow = {
  routeID: RouteOverview["route_id"];
  label: string;
  anomalyRatePercentage: number;
  anomalies: number;
  waybills: number;
  maxRisk: number;
};

type ChartBase = {
  title: string;
  basis: SnapshotBasis;
};

export type AnomalyCompositionChart = ChartBase & {
  kind: "anomaly-composition";
  content: CountChartContent<CountSlice>;
};

export type DispositionCompositionChart = ChartBase & {
  kind: "disposition-composition";
  basis: Extract<
    SnapshotBasis,
    { kind: "overview-snapshot-with-live-status" }
  >;
  content: CountChartContent<DispositionSlice>;
};

export type RiskyRoutesChart = ChartBase & {
  kind: "risky-routes";
  content: RankingChartContent<RouteRiskRow>;
};

export type OverviewChartModel =
  | AnomalyCompositionChart
  | DispositionCompositionChart
  | RiskyRoutesChart;

type WithReadyContent<T extends OverviewChartModel> =
  T extends OverviewChartModel
    ? Omit<T, "content"> & {
        content: Extract<T["content"], { kind: "ready" }>;
      }
    : never;

export type ReadyOverviewChart = WithReadyContent<OverviewChartModel>;

export type OverviewChartsModel = {
  charts: readonly [
    AnomalyCompositionChart,
    DispositionCompositionChart,
    RiskyRoutesChart,
  ];
};

export type OverviewChartSource = Pick<
  Overview,
  "as_of" | "anomaly_distribution" | "anomalies" | "hubs" | "routes"
>;

export function buildOverviewChartModel({
  overview,
  runProjections,
}: {
  overview: OverviewChartSource;
  runProjections: ReadonlyMap<WaybillID, RunProjection>;
}): OverviewChartsModel {
  return {
    charts: [
      buildAnomalyComposition(overview),
      buildDispositionComposition(overview, runProjections),
      buildRiskyRoutes(overview),
    ],
  };
}

export function isReadyOverviewChart(
  chart: OverviewChartModel,
): chart is ReadyOverviewChart {
  return chart.content.kind === "ready";
}

const dispositionDefinitions = [
  { key: "unassigned", label: "待分派" },
  { key: "investigating", label: "分析中" },
  { key: "awaiting_approval", label: "待审批" },
  { key: "executing", label: "执行中" },
  { key: "completed", label: "已闭环" },
  { key: "rejected", label: "已驳回" },
  { key: "needs_attention", label: "需人工介入" },
] satisfies readonly { key: DispositionKey; label: string }[];

function buildAnomalyComposition(
  overview: OverviewChartSource,
): AnomalyCompositionChart {
  const grouped = new Map<string, number>();
  for (const item of overview.anomaly_distribution) {
    grouped.set(item.type, (grouped.get(item.type) ?? 0) + item.count);
  }
  const counts = [...grouped].map(([key, count]) => ({
    key,
    label: anomalyTypeLabel(key),
    count,
  }));
  counts.sort(
    (left, right) =>
      right.count - left.count || left.label.localeCompare(right.label, "zh-CN"),
  );
  const total = counts.reduce((sum, item) => sum + item.count, 0);
  const items = toNonEmpty(
    counts
      .filter((item) => item.count > 0)
      .map((item) => ({
        ...item,
        percentage: percentage(item.count, total),
      })),
  );

  return {
    kind: "anomaly-composition",
    title: "异常构成",
    basis: { kind: "overview-snapshot", asOf: overview.as_of },
    content:
      items === null
        ? {
            kind: "empty",
            title: "当前没有异常构成",
            detail: "经营快照中没有可统计的异常类型。",
          }
        : { kind: "ready", total, items },
  };
}

function buildDispositionComposition(
  overview: OverviewChartSource,
  runProjections: ReadonlyMap<WaybillID, RunProjection>,
): DispositionCompositionChart {
  const counts = new Map<DispositionKey, number>(
    dispositionDefinitions.map((item) => [item.key, 0]),
  );
  for (const anomaly of overview.anomalies) {
    const status = effectiveRunStatus({
      waybillID: anomaly.waybill_id,
      snapshotStatus: anomaly.run_status,
      projections: runProjections,
    });
    const key = dispositionKey(status);
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  const total = overview.anomalies.length;
  const items = toNonEmpty(
    dispositionDefinitions.flatMap((definition) => {
      const count = counts.get(definition.key) ?? 0;
      return count === 0
        ? []
        : [
            {
              ...definition,
              count,
              percentage: percentage(count, total),
            },
          ];
    }),
  );

  return {
    kind: "disposition-composition",
    title: "当前处置状态",
    basis: {
      kind: "overview-snapshot-with-live-status",
      asOf: overview.as_of,
    },
    content:
      items === null
        ? {
            kind: "empty",
            title: "当前没有处置任务",
            detail: "经营快照中没有异常运单。",
          }
        : { kind: "ready", total, items },
  };
}

function dispositionKey(status: RunStatus | undefined): DispositionKey {
  switch (status) {
    case undefined:
      return "unassigned";
    case "started":
    case "investigating":
      return "investigating";
    case "awaiting_approval":
      return "awaiting_approval";
    case "executing":
      return "executing";
    case "completed":
      return "completed";
    case "rejected":
      return "rejected";
    case "failed":
    case "review_required":
    case "manual_review":
      return "needs_attention";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function buildRiskyRoutes(
  overview: OverviewChartSource,
): RiskyRoutesChart {
  const hubs = new Map(
    overview.hubs.map((hub) => [
      hub.hub_id,
      hub.city.trim() === "" ? hub.name : hub.city,
    ]),
  );
  const rows = overview.routes
    .filter((route) => route.waybills > 0 && route.anomalies > 0)
    .map((route) => ({
      routeID: route.route_id,
      label: routeLabel(route, hubs),
      anomalyRatePercentage: route.delay_heat,
      anomalies: route.anomalies,
      waybills: route.waybills,
      maxRisk: route.max_risk,
    }));
  rows.sort(
    (left, right) =>
      right.anomalyRatePercentage - left.anomalyRatePercentage ||
      right.maxRisk - left.maxRisk ||
      right.anomalies - left.anomalies ||
      left.routeID.localeCompare(right.routeID),
  );
  const items = toNonEmpty(rows.slice(0, 5));

  return {
    kind: "risky-routes",
    title: "高异常占比线路",
    basis: { kind: "overview-snapshot", asOf: overview.as_of },
    content:
      items === null
        ? {
            kind: "empty",
            title: "当前没有异常线路",
            detail: "经营快照中的线路没有异常运单。",
          }
        : { kind: "ready", items },
  };
}

function routeLabel(
  route: RouteOverview,
  hubs: ReadonlyMap<string, string>,
): string {
  const origin = hubs.get(route.origin_hub_id) ?? route.origin_hub_id;
  const destination =
    hubs.get(route.destination_hub_id) ?? route.destination_hub_id;
  return `${origin} → ${destination}`;
}

function percentage(count: number, total: number): number {
  return total === 0 ? 0 : Math.round((count / total) * 1_000) / 10;
}

function toNonEmpty<T>(items: readonly T[]): NonEmpty<T> | null {
  const [head, ...tail] = items;
  return head === undefined ? null : [head, ...tail];
}
