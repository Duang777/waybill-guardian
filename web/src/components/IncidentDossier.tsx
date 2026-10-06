import {
  Check,
  Circle,
  Crosshair,
  LoaderCircle,
  Radio,
} from "lucide-react";
import type { Approval, RunStatus } from "../api";
import styles from "../app.module.css";
import {
  timelinePhases,
  visibleEvents,
  type EvidenceSelection,
  type TimelinePhase,
  type TimelineState,
} from "../timeline";

type RunStageBarProps = {
  timeline: TimelineState;
  runStatus: RunStatus | null;
  runID: string | null;
  connected: boolean;
};

export function RunStageBar({
  timeline,
  runStatus,
  runID,
  connected,
}: RunStageBarProps) {
  const phases = timelinePhases(visibleEvents(timeline));
  const activePhase = currentPhase(phases);

  return (
    <section
      className={styles.runStageBar}
      aria-labelledby="run-stage-title"
    >
      <div className={styles.runStageContext}>
        <div className={styles.runStageHeading}>
          <span className={styles.eyebrow}>Run stage / live</span>
          <span className={styles.agentConnection}>
            <Radio aria-hidden="true" size={12} />
            {connectionLabel({ connected, runID, runStatus })}
          </span>
        </div>
        <div
          className={styles.agentTaskLead}
          key={`${runID ?? "idle"}-${activePhase?.id ?? "waiting"}`}
        >
          <h2 id="run-stage-title">{taskHeading(runStatus, activePhase)}</h2>
          <p>{taskDescription(runStatus, activePhase)}</p>
        </div>
        <span className={styles.agentRunID}>
          {runID === null ? "尚未创建任务" : compactID(runID)}
        </span>
      </div>

      <ol
        className={`${styles.agentPlan} ${planProgressClass(phases)}`}
        aria-label="Agent 六阶段运行带"
      >
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
            <span className={styles.agentPlanLabel}>
              <small>{(index + 1).toString().padStart(2, "0")}</small>
              {phase.label}
            </span>
          </li>
        ))}
      </ol>
    </section>
  );
}

export function EvidenceLedger({
  approval,
  onEvidenceSelect,
}: {
  approval: Approval | null;
  onEvidenceSelect: (selection: EvidenceSelection) => void;
}) {
  const evidence = approval?.evidence ?? [];

  return (
    <aside
      className={styles.evidenceLedger}
      aria-labelledby="evidence-ledger-title"
    >
      <div className={styles.evidenceLedgerHeading}>
        <div>
          <span className={styles.eyebrow}>Verified findings</span>
          <h2 id="evidence-ledger-title">证据账本</h2>
        </div>
        <span className={styles.evidenceCount}>
          {evidence.length.toString().padStart(2, "0")}
        </span>
      </div>

      {evidence.length === 0 ? (
        <div className={styles.evidenceLedgerEmpty}>
          <span className={styles.evidenceLedgerRule} aria-hidden="true" />
          <strong>等待证据固化</strong>
          <p>Agent 核验运单、轨迹、司机与天气后，关键事实会登记在这里。</p>
        </div>
      ) : (
        <ol className={styles.evidenceLedgerList}>
          {evidence.map((item, index) => (
            <li key={evidenceKey(item)}>
              <span className={styles.evidenceOrdinal} aria-hidden="true">
                {(index + 1).toString().padStart(2, "0")}
              </span>
              <div>
                <span className={styles.evidenceLabel}>{item.label}</span>
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
                    <Crosshair aria-hidden="true" size={14} />
                  </button>
                ) : (
                  <strong>{item.value}</strong>
                )}
              </div>
              {"source" in item && (
                <span className={styles.evidenceSource}>
                  EVENT #{item.source.source_seq.toString().padStart(2, "0")}
                </span>
              )}
            </li>
          ))}
        </ol>
      )}
      <div className={styles.evidenceLedgerFooter}>
        <span>证据点击后同步定位地图与审计事件</span>
        <span>HASH VERIFIED</span>
      </div>
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

function planProgressClass(phases: readonly TimelinePhase[]): string {
  const reached = phases.filter((phase) => phase.status !== "upcoming").length;
  switch (reached) {
    case 0:
      return styles.agentPlanProgress0;
    case 1:
      return styles.agentPlanProgress1;
    case 2:
      return styles.agentPlanProgress2;
    case 3:
      return styles.agentPlanProgress3;
    case 4:
      return styles.agentPlanProgress4;
    case 5:
      return styles.agentPlanProgress5;
    case 6:
      return styles.agentPlanProgress6;
    default:
      return styles.agentPlanProgress0;
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
