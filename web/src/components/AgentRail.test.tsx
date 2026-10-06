import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { approvalSchema } from "../api";
import { initialTimelineState } from "../timeline";
import { AgentRail } from "./AgentRail";

describe("AgentRail", () => {
  it("keeps the plan, linked evidence, and approval in one task rail", () => {
    const approval = approvalSchema.parse({
      id: "APR-pending",
      run_id: "run-1",
      sdk_run_id: "sdk-1",
      waybill_id: "YD2026101001",
      plan_version: 1,
      items: [
        {
          call_id: "call-reassign",
          action: "tms.reassign",
          wire_name: "tms_reassign",
          params: {
            waybill_id: "YD2026101001",
            carrier_id: "CARRIER-SW-42",
          },
          arguments_hash: "arguments-hash",
          idempotency_key: "key-1",
        },
      ],
      reason: "司机连续驾驶时间过长",
      evidence: [
        {
          label: "异常停留",
          value: "6 小时",
          source: {
            tool_call_id: "call-tracking",
            field_path: "/points/4/stop_hours",
            source_seq: 9,
          },
        },
      ],
      status: "pending",
      requested_at: "2026-10-10T01:15:00Z",
      expires_at: "2099-10-10T01:25:00Z",
    });

    const markup = renderToStaticMarkup(
      <AgentRail
        timeline={initialTimelineState}
        approval={approval}
        proposal={null}
        runStatus="awaiting_approval"
        runID="run-1"
        connected
        view={null}
        busy={false}
        onEvidenceSelect={() => undefined}
        onConfirm={() => Promise.resolve()}
        onReject={() => Promise.resolve()}
      />,
    );

    expect(markup).toContain('aria-label="Agent 处置栏"');
    expect(markup).toContain('aria-label="Agent 处置计划"');
    expect(markup).toContain("感知");
    expect(markup).toContain("闭环");
    expect(markup).toContain("等待人工审批");
    expect(markup).toContain("证据摘要");
    expect(markup).toContain("定位证据：异常停留，审计事件 9");
    expect(markup).not.toContain("归因证据链");
  });
});
