import { BarChart, PieChart } from "echarts/charts";
import { GridComponent, TooltipComponent } from "echarts/components";
import {
  init,
  registerTheme,
  use,
  type EChartsCoreOption,
  type EChartsType,
} from "echarts/core";
import { CanvasRenderer } from "echarts/renderers";
import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import type {
  DispositionKey,
  ReadyOverviewChart,
} from "../overview-chart-model";
import {
  readOverviewChartTheme,
  type OverviewChartTheme,
} from "./overview-chart-theme";

use([
  BarChart,
  PieChart,
  GridComponent,
  TooltipComponent,
  CanvasRenderer,
]);

const themeName = "wg-paper";
let themeRegistered = false;
let chartInstanceSequence = 0;

export default function OverviewEChart({
  chart,
}: {
  chart: ReadyOverviewChart;
}) {
  const host = useRef<HTMLDivElement>(null);
  const instance = useRef<EChartsType | null>(null);
  const theme = useRef<OverviewChartTheme | null>(null);
  const [ready, setReady] = useState(false);
  const [failure, setFailure] = useState<Error | null>(null);
  const [instanceID] = useState(nextChartInstanceID);
  const reducedMotion = usePrefersReducedMotion();

  if (failure !== null) {
    throw failure;
  }

  useLayoutEffect(() => {
    const element = host.current;
    if (element === null) {
      return;
    }

    let created: EChartsType | null = null;
    try {
      const resolvedTheme = readOverviewChartTheme(element);
      registerPaperTheme(resolvedTheme);
      theme.current = resolvedTheme;
      created = init(element, themeName, { renderer: "canvas" });
      instance.current = created;
      setReady(true);
    } catch (error) {
      setFailure(toError(error));
    }

    return () => {
      created?.dispose();
      if (instance.current === created) {
        instance.current = null;
      }
      theme.current = null;
    };
  }, []);

  useEffect(() => {
    if (!ready || instance.current === null || theme.current === null) {
      return;
    }
    try {
      instance.current.setOption(
        optionFor(chart, theme.current, reducedMotion),
        {
          lazyUpdate: false,
          notMerge: false,
          replaceMerge: ["series"],
        },
      );
    } catch (error) {
      setFailure(toError(error));
    }
  }, [chart, ready, reducedMotion]);

  useEffect(() => {
    const element = host.current;
    if (!ready || element === null || instance.current === null) {
      return;
    }
    let active = true;
    const observer = new ResizeObserver(() => {
      if (!active) {
        return;
      }
      try {
        instance.current?.resize();
      } catch (error) {
        setFailure(toError(error));
      }
    });
    observer.observe(element);
    return () => {
      active = false;
      observer.disconnect();
    };
  }, [ready]);

  return (
    <div
      ref={host}
      aria-hidden="true"
      data-chart-instance={instanceID}
      data-chart-motion={reducedMotion ? "reduced" : "animated"}
      data-overview-chart={chart.kind}
    />
  );
}

function optionFor(
  chart: ReadyOverviewChart,
  theme: OverviewChartTheme,
  reducedMotion: boolean,
): EChartsCoreOption {
  const motion: Pick<
    EChartsCoreOption,
    | "animation"
    | "animationDuration"
    | "animationDurationUpdate"
    | "animationEasing"
    | "animationEasingUpdate"
  > = {
    animation: !reducedMotion,
    animationDuration: reducedMotion ? 0 : 260,
    animationDurationUpdate: reducedMotion ? 0 : 180,
    animationEasing: "cubicOut",
    animationEasingUpdate: "cubicOut",
  };
  switch (chart.kind) {
    case "anomaly-composition":
      return chart.content.items.length <= 4
        ? {
            ...motion,
            color: [...theme.palette],
            tooltip: { trigger: "item", confine: true },
            series: [
              {
                type: "pie",
                radius: ["48%", "76%"],
                center: ["50%", "50%"],
                avoidLabelOverlap: true,
                label: { show: false },
                emphasis: { scale: false },
                itemStyle: {
                  borderColor: theme.paper,
                  borderWidth: 2,
                },
                data: chart.content.items.map((item) => ({
                  name: item.label,
                  value: item.count,
                })),
              },
            ],
          }
        : horizontalCountBars(chart, theme, motion);
    case "disposition-composition":
      return {
        ...motion,
        color: [...theme.palette],
        grid: { left: 4, right: 4, top: 36, bottom: 36 },
        tooltip: { trigger: "axis", confine: true },
        xAxis: {
          type: "value",
          min: 0,
          max: chart.content.total,
          show: false,
        },
        yAxis: {
          type: "category",
          data: [""],
          show: false,
        },
        series: chart.content.items.map((item) => ({
          type: "bar",
          name: item.label,
          stack: "disposition",
          barWidth: 24,
          data: [item.count],
          emphasis: { disabled: true },
          itemStyle: {
            color: dispositionColor(theme, item.key),
          },
        })),
      };
    case "risky-routes":
      return {
        ...motion,
        color: [theme.palette[0]],
        grid: { left: 4, right: 4, top: 6, bottom: 14 },
        tooltip: { trigger: "axis", confine: true },
        xAxis: {
          type: "value",
          min: 0,
          max: 100,
          axisLabel: {
            color: theme.muted,
            fontSize: 8,
            formatter: "{value}%",
          },
          axisLine: { show: false },
          axisTick: { show: false },
          splitLine: {
            lineStyle: { color: theme.line, width: 1 },
          },
        },
        yAxis: {
          type: "category",
          inverse: true,
          data: chart.content.items.map((item) => item.label),
          axisLabel: { show: false },
          axisLine: { show: false },
          axisTick: { show: false },
        },
        series: [
          {
            type: "bar",
            name: "异常占比",
            barMaxWidth: 8,
            data: chart.content.items.map(
              (item) => item.anomalyRatePercentage,
            ),
            itemStyle: { color: chartColor(theme, 1) },
          },
          {
            type: "bar",
            name: "最高风险",
            barMaxWidth: 8,
            data: chart.content.items.map((item) => item.maxRisk),
            itemStyle: { color: theme.palette[0] },
          },
        ],
      };
    default: {
      const exhaustive: never = chart;
      return exhaustive;
    }
  }
}

function horizontalCountBars(
  chart: Extract<
    ReadyOverviewChart,
    { kind: "anomaly-composition" }
  >,
  theme: OverviewChartTheme,
  motion: Pick<
    EChartsCoreOption,
    | "animation"
    | "animationDuration"
    | "animationDurationUpdate"
    | "animationEasing"
    | "animationEasingUpdate"
  >,
): EChartsCoreOption {
  return {
    ...motion,
    grid: { left: 4, right: 4, top: 6, bottom: 14 },
    tooltip: { trigger: "axis", confine: true },
    xAxis: {
      type: "value",
      min: 0,
      axisLabel: { show: false },
      axisLine: { show: false },
      axisTick: { show: false },
      splitLine: { lineStyle: { color: theme.line, width: 1 } },
    },
    yAxis: {
      type: "category",
      inverse: true,
      data: chart.content.items.map((item) => item.label),
      axisLabel: { show: false },
      axisLine: { show: false },
      axisTick: { show: false },
    },
    series: [
      {
        type: "bar",
        barMaxWidth: 12,
        data: chart.content.items.map((item, index) => ({
          value: item.count,
          itemStyle: { color: chartColor(theme, index) },
        })),
      },
    ],
  };
}

function registerPaperTheme(theme: OverviewChartTheme): void {
  if (themeRegistered) {
    return;
  }
  registerTheme(themeName, {
    color: [...theme.palette],
    backgroundColor: "transparent",
    textStyle: {
      color: theme.ink,
      fontFamily:
        '"Avenir Next", "PingFang SC", "Noto Sans CJK SC", sans-serif',
    },
  });
  themeRegistered = true;
}

function chartColor(theme: OverviewChartTheme, index: number): string {
  return theme.palette[index % theme.palette.length] ?? theme.ink;
}

function dispositionColor(
  theme: OverviewChartTheme,
  key: DispositionKey,
): string {
  switch (key) {
    case "unassigned":
      return chartColor(theme, 6);
    case "investigating":
      return chartColor(theme, 1);
    case "awaiting_approval":
      return chartColor(theme, 2);
    case "executing":
      return chartColor(theme, 3);
    case "completed":
      return chartColor(theme, 4);
    case "rejected":
      return chartColor(theme, 5);
    case "needs_attention":
      return chartColor(theme, 0);
    default: {
      const exhaustive: never = key;
      return exhaustive;
    }
  }
}

function nextChartInstanceID(): string {
  chartInstanceSequence += 1;
  return `overview-chart-${chartInstanceSequence}`;
}

function usePrefersReducedMotion(): boolean {
  const [reducedMotion, setReducedMotion] = useState(() =>
    typeof window === "undefined"
      ? false
      : window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );

  useEffect(() => {
    const query = window.matchMedia("(prefers-reduced-motion: reduce)");
    const update = () => setReducedMotion(query.matches);
    query.addEventListener("change", update);
    return () => query.removeEventListener("change", update);
  }, []);

  return reducedMotion;
}

function toError(value: unknown): Error {
  return value instanceof Error
    ? value
    : new Error("经营图表渲染失败");
}
