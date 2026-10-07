import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { approvalSchema } from "../api";
import { ApprovalPanel } from "./ApprovalPanel";

describe("ApprovalPanel", () => {
  it("shows review_required instead of a stale approval", () => {
    const approval = approvalSchema.parse({
      id: "APR-stale",
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
      reason: "旧方案",
      evidence: [],
      status: "rejected",
      requested_at: "2026-10-10T01:15:00Z",
      expires_at: "2026-10-10T01:25:00Z",
      decided_by: "reviewer",
      decided_at: "2026-10-10T01:16:00Z",
      reject_reason: "承运商不可用",
    });

    const markup = renderToStaticMarkup(
      <ApprovalPanel
        approval={approval}
        proposal={null}
        runStatus="review_required"
        view={null}
        decisionState={{ kind: "ready" }}
        onEvidenceSelect={() => undefined}
        onConfirm={() => Promise.resolve()}
        onReject={() => Promise.resolve()}
      />,
    );

    expect(markup).toContain("提案需要人工复核");
    expect(markup).not.toContain("方案已驳回");
    expect(markup).not.toContain("APR-stale");
  });

  it("names sourced evidence controls and announces the throttled approval deadline", () => {
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
      <ApprovalPanel
        approval={approval}
        proposal={null}
        runStatus="awaiting_approval"
        view={null}
        decisionState={{ kind: "ready" }}
        onEvidenceSelect={() => undefined}
        onConfirm={() => Promise.resolve()}
        onReject={() => Promise.resolve()}
      />,
    );

    expect(markup).toContain("定位证据：异常停留，审计事件 9");
    expect(markup).toContain('aria-live="polite"');
    expect(markup).toContain("剩余 ");

    const disconnectedMarkup = renderToStaticMarkup(
      <ApprovalPanel
        approval={approval}
        proposal={null}
        runStatus="awaiting_approval"
        view={null}
        decisionState={{
          kind: "unavailable",
          reason: "实时审计连接尚未恢复，系统已暂时锁定审批操作。",
        }}
        onEvidenceSelect={() => undefined}
        onConfirm={() => Promise.resolve()}
        onReject={() => Promise.resolve()}
      />,
    );

    expect(disconnectedMarkup).toContain("暂时锁定审批操作");
    expect(disconnectedMarkup.match(/disabled=""/g)).toHaveLength(2);
  });

  it("shows an expired approval as a terminal state without decision actions", () => {
    const approval = approvalSchema.parse({
      id: "APR-expired",
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
      evidence: [],
      status: "expired",
      requested_at: "2026-10-10T01:15:00Z",
      expires_at: "2026-10-10T01:25:00Z",
      decided_by: "system",
      decided_at: "2026-10-10T01:25:00Z",
      reject_reason: "approval expired",
    });

    const markup = renderToStaticMarkup(
      <ApprovalPanel
        approval={approval}
        proposal={null}
        runStatus="rejected"
        view={null}
        decisionState={{ kind: "ready" }}
        onEvidenceSelect={() => undefined}
        onConfirm={() => Promise.resolve()}
        onReject={() => Promise.resolve()}
      />,
    );

    expect(markup).toContain("方案已过期");
    expect(markup).toContain("已过期");
    expect(markup).not.toContain("确认并执行");
    expect(markup).not.toContain("驳回方案");
  });
});
