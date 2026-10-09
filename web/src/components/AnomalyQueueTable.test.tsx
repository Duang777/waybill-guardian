import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { waybillIdSchema } from "../api";
import { AnomalyQueueTable, type AnomalyQueueRow } from "./AnomalyQueueTable";

describe("AnomalyQueueTable", () => {
  it("renders stable row identity and accessible sorting controls", () => {
    const waybillID = waybillIdSchema.parse("YD2026101001");
    const rows: readonly AnomalyQueueRow[] = [
      {
        waybillID,
        origin: "杭州",
        destination: "成都",
        anomalyLabel: "停留超时",
        rank: 1,
        riskScore: 91,
        statusLabel: "待分派",
        statusTone: "warning",
        href: `/waybills/${waybillID}`,
        selectable: true,
      },
    ];
    const markup = renderToStaticMarkup(
      <AnomalyQueueTable
        rows={rows}
        selected={new Set([waybillID])}
        selectionDisabled={false}
        onToggleSelection={() => undefined}
      />,
    );

    expect(markup).toContain('aria-label="异常处置队列数据"');
    expect(markup).toContain(`data-waybill-id="${waybillID}"`);
    expect(markup).toContain('aria-sort="none"');
    expect(markup).toContain("运单 / 线路");
    expect(markup).toContain("风险");
    expect(markup).toContain("checked");
    expect(markup).toContain(`href="/waybills/${waybillID}"`);
  });
});
