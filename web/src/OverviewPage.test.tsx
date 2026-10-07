import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { KPIMetric, KPIReport } from "./api";
import { OverviewKPI, OverviewKPIBand } from "./OverviewPage";

function metric({
  key,
  label = "测试指标",
  value,
  unit = "小时",
  reason,
}: {
  key: string;
  label?: string;
  value: number | null;
  unit?: string;
  reason?: string;
}): KPIMetric {
  return {
    key,
    label,
    value,
    unit,
    availability: value === null ? "unavailable" : "available",
    formula: "测试公式",
    reason,
  };
}

describe("OverviewKPI", () => {
  it("renders the six documented operating metrics", () => {
    const metrics = [
      metric({
        key: "time_recovered_hours",
        label: "已实现时效挽回",
        value: 0,
      }),
      metric({
        key: "cost_impact_cny",
        label: "已实现成本影响",
        value: 0,
        unit: "元",
      }),
      metric({ key: "labor_saved_hours", label: "人力节省", value: 0 }),
      metric({
        key: "anomaly_closure_rate_pct",
        label: "异常闭环率",
        value: 0,
        unit: "%",
      }),
      metric({
        key: "anomaly_rate_pct",
        label: "异常率",
        value: 33.5,
        unit: "%",
      }),
      metric({
        key: "average_handling_minutes",
        label: "平均处置时长",
        value: null,
        unit: "分钟",
        reason: "窗口内尚无闭环 run",
      }),
      metric({
        key: "approval_rate_pct",
        label: "人工审批通过率",
        value: null,
        unit: "%",
        reason: "窗口内尚无人工审批决定",
      }),
    ];
    const report: KPIReport = {
      window: "24h0m0s",
      as_of: "2026-10-12T04:24:00Z",
      assumptions: { evidence_step_minutes: 8 },
      metrics,
    };

    const markup = renderToStaticMarkup(
      <OverviewKPIBand
        report={report}
        context={{ anomalyCount: 67, hasClosedRunSample: false }}
      />,
    );

    expect(markup.match(/<article/g)).toHaveLength(6);
    expect(markup).toContain("平均处置时长");
    expect(markup).toContain("窗口内尚无闭环 run");
    expect(markup).toContain("人工审批通过率");
    expect(markup).toContain("窗口内尚无人工审批决定");
    expect(markup).not.toContain(">异常率<");
  });

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
