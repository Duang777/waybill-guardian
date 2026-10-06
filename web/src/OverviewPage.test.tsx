import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { KPIMetric } from "./api";
import { OverviewKPI } from "./OverviewPage";

function metric({
  key,
  value,
  unit = "小时",
  reason,
}: {
  key: string;
  value: number | null;
  unit?: string;
  reason?: string;
}): KPIMetric {
  return {
    key,
    label: "测试指标",
    value,
    unit,
    availability: value === null ? "unavailable" : "available",
    formula: "测试公式",
    reason,
  };
}

describe("OverviewKPI", () => {
  it("explains a zero value when no run has closed", () => {
    const markup = renderToStaticMarkup(
      <OverviewKPI
        metricKey="time_recovered_hours"
        metric={metric({ key: "time_recovered_hours", value: 0 })}
        context={{ anomalyCount: 67, hasClosedRunSample: false }}
      />,
    );

    expect(markup).toContain("0小时");
    expect(markup).toContain("暂无已审批执行");
    expect(markup).toContain('aria-describedby="kpi-time_recovered_hours-detail"');
  });

  it("keeps a measured zero after a run has closed", () => {
    const markup = renderToStaticMarkup(
      <OverviewKPI
        metricKey="cost_impact_cny"
        metric={metric({ key: "cost_impact_cny", value: 0, unit: "元" })}
        context={{ anomalyCount: 67, hasClosedRunSample: true }}
      />,
    );

    expect(markup).toContain("¥0");
    expect(markup).not.toContain("暂无已审批执行");
    expect(markup).not.toContain("aria-describedby");
  });

  it("shows why an unavailable metric cannot be calculated", () => {
    const markup = renderToStaticMarkup(
      <OverviewKPI
        metricKey="labor_saved_hours"
        metric={metric({
          key: "labor_saved_hours",
          value: null,
          reason: "缺少取证记录",
        })}
        context={{ anomalyCount: 67, hasClosedRunSample: false }}
      />,
    );

    expect(markup).toContain("待接入");
    expect(markup).toContain("缺少取证记录");
    expect(markup).toContain('aria-describedby="kpi-labor_saved_hours-detail"');
  });

  it("names the pending anomaly population", () => {
    const markup = renderToStaticMarkup(
      <OverviewKPI
        metricKey="anomaly_closure_rate_pct"
        metric={metric({ key: "anomaly_closure_rate_pct", value: 0 })}
        context={{ anomalyCount: 67, hasClosedRunSample: false }}
      />,
    );

    expect(markup).toContain("67 条异常待闭环");
  });
});
