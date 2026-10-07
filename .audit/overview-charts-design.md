# Overview operating charts design

## Problem

The overview already owns a validated current snapshot and live run projections.
It does not own historical buckets, previous snapshots, milestone history, or a
manual baseline. The chart layer must show only current composition and ranking,
stay outside the initial JavaScript entry, preserve the executive brief, and fit
the wide-screen presentation canvas.

## Usage

`OverviewPage` passes the state it already owns to one facade:

```tsx
<OverviewOperatingCharts
  overview={resource.overview}
  runProjections={runProjections}
/>
```

Pure tests call the model builder directly:

```ts
const model = buildOverviewChartModel({ overview, runProjections });
expect(model.charts[1].content).toEqual(expectedDisposition);
```

The page does not import ECharts, construct options, manage chart instances, or
coordinate chart-specific loading and error states.

## Shape

### Domain model

```ts
type ChartContent<T> =
  | { kind: "empty"; title: string; detail: string }
  | { kind: "ready"; total: number; items: readonly T[] };

type OverviewChartModel =
  | AnomalyCompositionChart
  | DispositionCompositionChart
  | RiskyRoutesChart;

type OverviewChartsModel = {
  charts: readonly [
    AnomalyCompositionChart,
    DispositionCompositionChart,
    RiskyRoutesChart,
  ];
};

function buildOverviewChartModel(input: {
  overview: Overview;
  runProjections: ReadonlyMap<WaybillID, RunProjection>;
}): OverviewChartsModel;
```

The fixed tuple makes the three approved meanings explicit. Empty and ready
states cannot coexist. ECharts option types do not cross this boundary.

The views are:

1. Anomaly composition from `anomaly_distribution`.
2. Current disposition composition from each anomaly's effective current status.
3. Five risky routes ordered by anomaly percentage, maximum risk, anomaly count,
   and route ID.

The UI never labels current status as a funnel and never labels `delay_heat` as
delay duration.

### Shared policies

`overview-run-projection.ts` owns the rule that a live projection wins over the
snapshot run status. `overview-labels.ts` owns anomaly labels used by both the
queue and charts. The chart model owns grouping, percentages, route joins,
ordering, freshness copy, and empty states.

### Rendering boundary

`OverviewOperatingCharts.tsx` renders headings, freshness text, visible exact
values, fixed plot geometry, empty states, and one error boundary plus `Suspense`
per chart.

`OverviewEChart.tsx` is the only runtime path to ECharts. It registers only pie,
bar, grid, tooltip, and CanvasRenderer modules. It initializes one instance per
panel, updates it with `setOption`, observes its container, disables animation
for reduced motion, and disposes only on unmount.

Effect-time failures from `init`, `setOption`, or `ResizeObserver` are stored in
component state and thrown during the next render so the chart-local React error
boundary handles them.

The canvas is supplementary and hidden from assistive technology. Exact values
remain visible in compact HTML lists or a route table, so color and tooltips are
never the only carriers of meaning.

### Layout

At `min-width: 1560px` and `min-height: 800px`, the ready-state body becomes a
two-column, two-row operations grid:

```text
207 px header/command/KPI region + 593 px operations grid = 800 px

top row:    network map | risk queue
bottom row: three charts | executive brief
```

The bottom row is 240 px and the queue/brief column is 400 px. The chart side
uses three equal columns. Below that breakpoint, the document returns to normal
flow, with two chart columns on tablets and one on phones.

Loading, ready, empty, and error states reserve the same plot dimensions.

## Module map

- `web/src/overview-chart-model.ts`: pure business presentation model.
- `web/src/overview-labels.ts`: shared anomaly labels.
- `web/src/charts/OverviewOperatingCharts.tsx`: public facade and semantic HTML.
- `web/src/charts/OverviewEChart.tsx`: private lazy ECharts runtime.
- `web/src/charts/overview-charts.module.css`: chart-internal geometry.
- `web/src/OverviewPage.tsx`: data owner and page composition.
- `web/src/overview.module.css`: two-row operations grid and page breakpoints.
- `web/scripts/verify-overview.mjs`: rendered, responsive, update, and motion checks.
- `web/scripts/verify-chart-bundle.mjs`: lazy graph and gzip budget check.

## Synthesis decision

Candidate 2 is the base because it has the smallest page-facing interface and the
only explicit `1560x800` layout arithmetic that matches the existing map/queue
split. Candidate 1 contributes always-visible exact-value HTML. Candidate 3
contributes the effect-failure bridge into React error boundaries.

Rejected shapes include caller-built ECharts options, three public chart
components, one combined canvas, hidden charts behind tabs, and synthetic
historical data.

## Tradeoffs accepted

- We accept five visible routes in exchange for readable labels in a 244 px row.
- We accept a shorter wide-screen map in exchange for preserving one-screen
  comparison; browser verification must reject HUD overlap.
- We accept three ECharts instances in exchange for independent update and
  failure behavior.
- We accept no trends, deltas, funnel, or baseline comparison in exchange for
  truthful data semantics.
- We accept a local lifecycle adapter in exchange for explicit bundle contents
  and stable `setOption` updates.

## Open questions and risks

- Does the 376 px wide-screen map keep every HUD and control readable?
- Does the selected ECharts graph stay below 350 KB gzip?
- Do real route names fit five rows without hiding their distinct endpoints?

Browser and build verification resolve these questions. A failure changes the
layout or registered modules, not the data meaning.

## Next implementation step

Implement the pure model and tests before installing or importing ECharts.
