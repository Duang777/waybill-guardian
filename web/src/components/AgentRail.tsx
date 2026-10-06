import {
  Check,
  Circle,
  Crosshair,
  LoaderCircle,
  Radio,
} from "lucide-react";
import type {
  Approval,
  Proposal,
  RunStatus,
  WaybillView,
} from "../api";
import styles from "../app.module.css";
import {
  timelinePhases,
  visibleEvents,
  type EvidenceSelection,
  type TimelinePhase,
  type TimelineState,
} from "../timeline";
import { ApprovalPanel } from "./ApprovalPanel";

type AgentRailProps = {
  timeline: TimelineState;
  approval: Approval | null;
  proposal: Proposal | null;
  runStatus: RunStatus | null;
  runID: string | null;
  connected: boolean;
  view: WaybillView | null;
  busy: boolean;
  onEvidenceSelect: (selection: EvidenceSelection) => void;
  onConfirm: () => Promise<void>;
  onReject: (reason: string) => Promise<void>;
};

export function AgentRail({
  timeline,
  approval,
  proposal,
  runStatus,
  runID,
  connected,
  view,
  busy,
  onEvidenceSelect,
  onConfirm,
  onReject,
}: AgentRailProps) {
  const phases = timelinePhases(visibleEvents(timeline));
  const activePhase = currentPhase(phases);
  const evidence = approval?.evidence ?? [];

  return (
    <aside className={styles.agentRail} aria-label="Agent 处置栏">
      <section className={styles.agentTaskPanel} aria-labelledby="agent-task-title">
        <div className={styles.agentRailHeading}>
          <div>
            <span className={styles.eyebrow}>Agent task / plan</span>
            <span className={styles.agentRunID}>
              {runID === null ? "尚未创建任务" : compactID(runID)}
            </span>
          </div>
          <span className={styles.agentConnection}>
            <Radio aria-hidden="true" size={12} />
            {connectionLabel({ connected, runID, runStatus })}
          </span>
        </div>

        <div className={styles.agentTaskLead}>
          <h2 id="agent-task-title">{taskHeading(runStatus, activePhase)}</h2>
          <p>{taskDescription(runStatus, activePhase)}</p>
        </div>

        <ol className={styles.agentPlan} aria-label="Agent 处置计划">
          {phases.map((phase, index) => (
            <li
              className={`${styles.agentPlanStep} ${
                styles[`agentPlanStep${capitalizeStatus(phase.status)}`]
              }`}
              aria-current={phase.status === "current" ? "step" : undefined}
              key={phase.id}
            >
              <span className={styles.agentPlanIcon} aria-hidden="true">
                {phaseIcon(phase)}
              </span>
              <span>{phase.label}</span>
              <small>{(index + 1).toString().padStart(2, "0")}</small>
            </li>
          ))}
        </ol>
      </section>

      <section
        className={styles.agentEvidencePanel}
        aria-labelledby="agent-evidence-title"
      >
        <div className={styles.agentSectionHeading}>
          <span id="agent-evidence-title">证据摘要</span>
          <span>{evidence.length} 条</span>
        </div>
        {evidence.length === 0 ? (
          <p className={styles.agentEvidenceEmpty}>
            Agent 固化归因后，关键证据会出现在这里。
          </p>
        ) : (
          <dl className={styles.agentEvidenceList}>
            {evidence.map((item) => (
              <div key={evidenceKey(item)}>
                <dt>{item.label}</dt>
                <dd>
                  {"source" in item ? (
                    <button
                      className={styles.agentEvidenceLink}
                      type="button"
                      aria-label={`定位证据：${item.label}，审计事件 ${item.source.source_seq}`}
                      title={`定位到审计事件 #${item.source.source_seq}`}
                      onClick={() =>
                        onEvidenceSelect({
                          sourceSeq: item.source.source_seq,
                          fieldPath: item.source.field_path,
                        })
                      }
                    >
                      <span>{item.value}</span>
                      <Crosshair aria-hidden="true" size={13} />
                    </button>
                  ) : (
                    item.value
                  )}
                </dd>
              </div>
            ))}
          </dl>
        )}
      </section>

      <ApprovalPanel
        approval={approval}
        proposal={proposal}
        runStatus={runStatus}
        view={view}
        busy={busy}
        embedded
        showEvidence={false}
        onEvidenceSelect={onEvidenceSelect}
        onConfirm={onConfirm}
        onReject={onReject}
      />
    </aside>
  );
}

function currentPhase(
  phases: readonly TimelinePhase[],
): TimelinePhase | null {
  return (
    phases.find((phase) => phase.status === "current") ??
    phases.findLast((phase) => phase.status === "complete") ??
    null
  );
}

function taskHeading(
  status: RunStatus | null,
  phase: TimelinePhase | null,
): string {
  switch (status) {
    case null:
      return "等待启动处置";
    case "awaiting_approval":
      return "等待人工审批";
    case "review_required":
    case "manual_review":
      return "等待人工复核";
    case "completed":
      return "处置闭环完成";
    case "rejected":
      return "等待后续跟进";
    case "failed":
      return "处置任务中断";
    case "started":
    case "investigating":
    case "executing":
      return phase?.label ?? "正在处理";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function taskDescription(
  status: RunStatus | null,
  phase: TimelinePhase | null,
): string {
  switch (status) {
    case null:
      return "启动后，Agent 将按计划读取证据并生成处置方案。";
    case "awaiting_approval":
      return "写操作已暂停，等待审批人核对证据和影响。";
    case "review_required":
    case "manual_review":
      return "自动校验未通过，本次运行不会执行写操作。";
    case "completed":
      return "平台写入和审计记录已经完成。";
    case "rejected":
      return "审批决定已记录，Agent 将根据原因继续处理。";
    case "failed":
      return "运行已停止，请查看审计记录中的失败原因。";
    case "started":
    case "investigating":
    case "executing":
      return phase?.description ?? "正在读取最新运行状态。";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function connectionLabel({
  connected,
  runID,
  runStatus,
}: {
  connected: boolean;
  runID: string | null;
  runStatus: RunStatus | null;
}): string {
  if (connected) {
    return "SSE 在线";
  }
  if (runID === null) {
    return "未启动";
  }
  if (
    runStatus === "completed" ||
    runStatus === "rejected" ||
    runStatus === "failed" ||
    runStatus === "review_required" ||
    runStatus === "manual_review"
  ) {
    return "审计已固化";
  }
  return "正在连接";
}

function phaseIcon(phase: TimelinePhase) {
  switch (phase.status) {
    case "complete":
      return <Check size={12} />;
    case "current":
      return <LoaderCircle size={12} />;
    case "upcoming":
      return <Circle size={10} />;
    default: {
      const exhaustive: never = phase.status;
      return exhaustive;
    }
  }
}

function capitalizeStatus(
  status: TimelinePhase["status"],
): "Complete" | "Current" | "Upcoming" {
  switch (status) {
    case "complete":
      return "Complete";
    case "current":
      return "Current";
    case "upcoming":
      return "Upcoming";
    default: {
      const exhaustive: never = status;
      return exhaustive;
    }
  }
}

function evidenceKey(item: Approval["evidence"][number]): string {
  return "source" in item
    ? `${item.label}-${item.source.source_seq}-${item.source.field_path}`
    : `${item.label}-${item.value}`;
}

function compactID(value: string): string {
  return value.length > 18 ? `${value.slice(0, 8)}...${value.slice(-6)}` : value;
}
