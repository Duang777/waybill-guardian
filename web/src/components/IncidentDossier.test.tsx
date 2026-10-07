import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { approvalSchema } from "../api";
import { initialTimelineState } from "../timeline";
import { EvidenceLedger, RunStageBar } from "./IncidentDossier";

describe("incident dossier", () => {
  it("separates the run stage from the linked evidence ledger", () => {
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
          label: "司机连续驾驶9小时",
          value: "9",
          source: {
            tool_call_id: "call-driver",
            field_path: "/continuous_drive_hours",
            source_seq: 13,
          },
        },
        {
          label: "司机连续驾驶9小时",
          value: "true",
          source: {
            tool_call_id: "call-driver",
            field_path: "/fatigue_alert",
            source_seq: 13,
          },
        },
        {
          label: "绵阳北服务区出现异常停留",
          value: "绵阳北服务区",
          source: {
            tool_call_id: "call-tracking",
            field_path: "/points/4/label",
            source_seq: 9,
          },
        },
        {
          label: "绵阳北服务区出现异常停留",
          value: "true",
          source: {
            tool_call_id: "call-tracking",
            field_path: "/points/4/anomaly",
            source_seq: 9,
          },
        },
        {
          label: "绵阳北服务区出现异常停留",
          value: "6",
          source: {
            tool_call_id: "call-tracking",
            field_path: "/points/4/stop_hours",
            source_seq: 9,
          },
        },
        {
          label: "沪陕高速存在天气预警",
          value: "小雨",
          source: {
            tool_call_id: "call-weather",
            field_path: "/segments/0/condition",
            source_seq: 17,
          },
        },
        {
          label: "沪陕高速存在天气预警",
          value: "low",
          source: {
            tool_call_id: "call-weather",
            field_path: "/segments/0/alert_level",
            source_seq: 17,
          },
        },
      ],
      status: "pending",
      requested_at: "2026-10-10T01:15:00Z",
      expires_at: "2099-10-10T01:25:00Z",
    });

    const stageMarkup = renderToStaticMarkup(
      <RunStageBar
        timeline={initialTimelineState}
        runStatus="awaiting_approval"
        runID="run-1"
        connection="online"
      />,
    );
    const replayMarkup = renderToStaticMarkup(
      <RunStageBar
        timeline={{
          ...initialTimelineState,
          playback: { kind: "paused", cursor: 0 },
        }}
        runStatus="awaiting_approval"
        runID="run-1"
        connection="online"
      />,
    );
    const evidenceMarkup = renderToStaticMarkup(
      <EvidenceLedger
        approval={approval}
        onEvidenceSelect={() => undefined}
      />,
    );

    expect(stageMarkup).toContain('aria-label="Agent 六阶段运行带"');
    expect(stageMarkup).toContain("感知");
    expect(stageMarkup).toContain("闭环");
    expect(stageMarkup).toContain("等待人工审批");
    expect(replayMarkup).toContain("Run stage / replay");
    expect(replayMarkup).toContain("回放：等待事件");
    expect(replayMarkup).toContain("SSE 在线 · 回放快照");
    expect(evidenceMarkup).toContain("证据账本");
    expect(evidenceMarkup).toContain("07 条引用");
    expect(evidenceMarkup).toContain("驾驶员状态");
    expect(evidenceMarkup).toContain("轨迹异常");
    expect(evidenceMarkup).toContain("天气影响");
    expect(evidenceMarkup).toContain("连续驾驶");
    expect(evidenceMarkup).toContain("9 小时");
    expect(evidenceMarkup).toContain("疲劳预警");
    expect(evidenceMarkup).toContain("已触发");
    expect(evidenceMarkup).toContain("异常停留");
    expect(evidenceMarkup).toContain("6 小时");
    expect(evidenceMarkup).toContain("天气状况");
    expect(evidenceMarkup).toContain("预警级别");
    expect(evidenceMarkup).toContain(">低<");
    expect(evidenceMarkup).toContain(
      "定位证据：绵阳北服务区出现异常停留，异常停留：6 小时，审计事件 9",
    );
    expect(evidenceMarkup).toContain("EVENT #09");
    expect(evidenceMarkup).toContain("EVENT #13");
    expect(evidenceMarkup).toContain("EVENT #17");
    expect(evidenceMarkup).not.toContain(">true<");
    expect(evidenceMarkup).not.toContain(">low<");
    expect(evidenceMarkup).not.toContain("归因证据链");
  });
});
