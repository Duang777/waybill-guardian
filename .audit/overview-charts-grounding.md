# Overview operating charts grounding

## Existing flow

`OverviewPage` loads `/api/overview`, `/api/kpis?window=24h`, and active runs
concurrently. The overview and KPI responses remain snapshots until a page refresh.
Active run polling and SSE only advance each anomaly's browser-side run projection.

The page already renders six operating KPIs, a network and risk-queue workspace, and
a three-item executive brief. At wide desktop sizes, those sections share a fixed
single-screen height budget.

## Supported chart meanings

- Anomaly composition uses `overview.anomaly_distribution`.
- Disposition composition uses each anomaly's effective current status:
  `runProjections.get(waybillID)?.status ?? anomaly.run_status`.
- Risky-route ranking uses `routes[].delay_heat`, `anomalies`, `waybills`, and
  `max_risk`, with hub metadata for readable labels.

The API has no time buckets, previous snapshots, milestone history, or manual
baseline series. The UI must not render trend lines, comparison arrows, or a
historical process funnel from these fields. `delay_heat` is an anomaly percentage,
not measured delay duration.

## Integration constraints

- Keep every ECharts runtime import under a `React.lazy()` boundary.
- Use `echarts/core` with only the rendered chart types, components, and
  `CanvasRenderer`.
- Keep the ECharts chunk separate from the entry, Three.js, and MapLibre chunks.
- Reuse the lower-page height budget instead of appending an unchanged fixed-height
  chart row.
- Keep the executive brief and its source badges visible.
- Keep each chart independently useful if another chart fails.
- Reserve final chart geometry while the lazy chunk loads to avoid layout shift.
- Disable ECharts animation for `prefers-reduced-motion: reduce`.
- Update charts with `setOption` on the existing instance and dispose only on
  unmount.

## Verification targets

- Pure tests cover grouping, labels, zero-data states, route ordering, and status
  projection.
- Browser verification checks three rendered canvases, nonblank pixels, stable
  instances across live status updates, 1560x800 plus existing responsive widths,
  reduced motion, and horizontal overflow.
- Production build verification enforces a gzip budget below 350 KB for the lazy
  chart chunk.
- Dependency manifests and third-party notices record ECharts and its NOTICE file.

## Relevant files

- `web/src/OverviewPage.tsx`
- `web/src/overview.module.css`
- `web/src/api.ts`
- `web/src/overview-run-projection.ts`
- `web/scripts/verify-overview.mjs`
- `internal/guardian/overview.go`
- `THIRD_PARTY_NOTICES.md`
