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
        busy={false}
        onEvidenceSelect={() => undefined}
        onConfirm={() => Promise.resolve()}
        onReject={() => Promise.resolve()}
      />,
    );

    expect(markup).toContain("提案需要人工复核");
    expect(markup).not.toContain("方案已驳回");
    expect(markup).not.toContain("APR-stale");
  });
});
