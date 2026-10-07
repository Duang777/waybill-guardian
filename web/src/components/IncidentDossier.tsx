import {
  Check,
  Circle,
  Crosshair,
  Radio,
} from "lucide-react";
import type { Approval, RunStatus } from "../api";
import styles from "../app.module.css";
import {
  playbackCursor,
  timelinePhases,
  visibleEvents,
  type EvidenceSelection,
  type TimelinePhase,
  type TimelineState,
} from "../timeline";
import {
  connectionLabel,
  type WorkbenchConnectionState,
} from "../workbench-connection";
import { HaloBadge, RollingNumber } from "./cult";

type RunStageBarProps = {
  timeline: TimelineState;
  runStatus: RunStatus | null;
  runID: string | null;
  connection: WorkbenchConnectionState;
};

export function RunStageBar({
  timeline,
  runStatus,
  runID,
  connection,
}: RunStageBarProps) {
  const phases = timelinePhases(visibleEvents(timeline));
  const activePhase = currentPhase(phases);
  const replaying = timeline.playback.kind !== "live";

  return (
    <section
      className={styles.runStageBar}
      aria-labelledby="run-stage-title"
    >
      <div className={styles.runStageContext}>
        <div className={styles.runStageHeading}>
          <span className={styles.eyebrow}>
            {replaying ? "Run stage / replay" : "Run stage / live"}
          </span>
          <HaloBadge
            tone={connectionTone(connection, replaying)}
            live={!replaying && connection === "online"}
          >
            <Radio aria-hidden="true" size={12} />
            {runConnectionLabel(connection, replaying)}
          </HaloBadge>
        </div>
        <div
          className={styles.agentTaskLead}
          key={`${runID ?? "idle"}-${activePhase?.id ?? "waiting"}`}
        >
          <h2 id="run-stage-title">
            {replaying
              ? `回放：${activePhase?.label ?? "等待事件"}`
              : taskHeading(runStatus, activePhase)}
          </h2>
          <p>
            {replaying
              ? `正在查看事件 ${playbackCursor(timeline)} / ${timeline.events.length}`
              : taskDescription(runStatus, activePhase)}
          </p>
        </div>
      </div>

      <ol
        className={styles.agentPlan}
        aria-label="Agent 六阶段运行带"
      >
        {phases.map((phase, index) => (
          <li
            className={`${styles.agentPlanStep} ${
              styles[`agentPlanStep${capitalizeStatus(phase.status)}`]
            }`}
            aria-current={phase.status === "current" ? "step" : undefined}
            aria-label={`${phase.label}，${phaseStatusLabel(phase.status)}`}
            key={phase.id}
          >
            <span className={styles.agentPlanIcon} aria-hidden="true">
              {phaseIcon(phase)}
            </span>
            <span className={styles.agentPlanLabel}>
              <small>{(index + 1).toString().padStart(2, "0")}</small>
              <strong>{phase.label}</strong>
            </span>
            <span className={styles.agentPlanState}>
              {phaseStatusLabel(phase.status)}
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
  const groups = groupEvidence(evidence);

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
          <strong>
            <RollingNumber
              value={groups.length}
              format={twoDigits}
              label={`${groups.length} 条证据结论`}
            />
          </strong>
          <small>条结论 · {evidence.length.toString().padStart(2, "0")} 条引用</small>
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
          {groups.map((group, index) => (
            <li className={styles.evidenceGroup} key={group.label}>
              <div className={styles.evidenceGroupHeading}>
                <span className={styles.evidenceOrdinal} aria-hidden="true">
                  {(index + 1).toString().padStart(2, "0")}
                </span>
                <h3>{evidenceGroupTitle(group)}</h3>
                <HaloBadge tone="neutral" tabularNums>
                  {groupSourceLabel(group)}
                </HaloBadge>
              </div>
              <ul className={styles.evidenceFactList}>
                {group.items.map((item) => (
                  <li key={evidenceKey(item)}>
                    <span className={styles.evidenceFactLabel}>
                      {evidenceFactLabel(item)}
                    </span>
                    {"source" in item ? (
                      <button
                        className={styles.agentEvidenceLink}
                        type="button"
                        aria-label={`定位证据：${group.label}，${evidenceFactLabel(item)}：${evidenceDisplayValue(item)}，审计事件 ${item.source.source_seq}`}
                        title={`定位到审计事件 #${item.source.source_seq}`}
                        onClick={() =>
                          onEvidenceSelect({
                            sourceSeq: item.source.source_seq,
                            fieldPath: item.source.field_path,
                          })
                        }
                      >
                        <strong>{evidenceDisplayValue(item)}</strong>
                        <Crosshair aria-hidden="true" size={14} />
                      </button>
                    ) : (
                      <strong>{evidenceDisplayValue(item)}</strong>
                    )}
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ol>
      )}
    </aside>
  );
}

function twoDigits(value: number): string {
  return value.toString().padStart(2, "0");
}

type EvidenceItem = Approval["evidence"][number];

type EvidenceGroup = {
  label: string;
  items: EvidenceItem[];
};

function groupEvidence(items: readonly EvidenceItem[]): EvidenceGroup[] {
  const groups: EvidenceGroup[] = [];
  const groupsByLabel = new Map<string, EvidenceGroup>();

  for (const item of items) {
    const existing = groupsByLabel.get(item.label);
    if (existing !== undefined) {
      existing.items.push(item);
      continue;
    }

    const group = { label: item.label, items: [item] };
    groupsByLabel.set(item.label, group);
    groups.push(group);
  }

  return groups;
}

function evidenceFactLabel(item: EvidenceItem): string {
  if (!("source" in item)) {
    return "核验事实";
  }

  if (item.source.field_path.endsWith("/continuous_drive_hours")) {
    return "连续驾驶";
  }
  if (item.source.field_path.endsWith("/fatigue_alert")) {
    return "疲劳预警";
  }
  if (
    item.source.field_path.includes("/points/") &&
    item.source.field_path.endsWith("/stop_hours")
  ) {
    return "异常停留";
  }
  if (
    item.source.field_path.includes("/points/") &&
    item.source.field_path.endsWith("/anomaly")
  ) {
    return "异常标记";
  }
  if (
    item.source.field_path.includes("/points/") &&
    item.source.field_path.endsWith("/label")
  ) {
    return "异常位置";
  }
  if (item.source.field_path.endsWith("/condition")) {
    return "天气状况";
  }
  if (item.source.field_path.endsWith("/alert_level")) {
    return "预警级别";
  }
  return "核验事实";
}

function evidenceDisplayValue(item: EvidenceItem): string {
  const value = item.value.trim();
  if (!("source" in item)) {
    return value;
  }

  if (
    item.source.field_path.endsWith("/continuous_drive_hours") ||
    item.source.field_path.endsWith("/stop_hours")
  ) {
    return value.includes("小时") ? value : `${value} 小时`;
  }
  if (
    item.source.field_path.endsWith("/fatigue_alert") ||
    item.source.field_path.endsWith("/anomaly")
  ) {
    if (value.toLowerCase() === "true") {
      return "已触发";
    }
    if (value.toLowerCase() === "false") {
      return "未触发";
    }
  }
  if (item.source.field_path.endsWith("/alert_level")) {
    return weatherAlertLabel(value);
  }
  return value;
}

function weatherAlertLabel(value: string): string {
  switch (value.toLowerCase()) {
    case "none":
    case "no_alert":
      return "无";
    case "low":
      return "低";
    case "medium":
      return "中";
    case "high":
      return "高";
    default:
      return value;
  }
}

function evidenceGroupTitle(group: EvidenceGroup): string {
  const fieldPaths = group.items.flatMap((item) =>
    "source" in item ? [item.source.field_path] : [],
  );
  if (
    fieldPaths.some(
      (path) =>
        path.endsWith("/continuous_drive_hours") ||
        path.endsWith("/fatigue_alert"),
    )
  ) {
    return "驾驶员状态";
  }
  if (
    fieldPaths.some(
      (path) =>
        path.includes("/points/") &&
        (path.endsWith("/label") ||
          path.endsWith("/anomaly") ||
          path.endsWith("/stop_hours")),
    )
  ) {
    return "轨迹异常";
  }
  if (fieldPaths.some((path) => path.includes("/segments/"))) {
    return "天气影响";
  }
  return group.label;
}

function groupSourceLabel(group: EvidenceGroup): string {
  const sourceSequences: number[] = [];
  for (const item of group.items) {
    if (
      "source" in item &&
      !sourceSequences.includes(item.source.source_seq)
    ) {
      sourceSequences.push(item.source.source_seq);
    }
  }
  if (sourceSequences.length === 0) {
    return "已核验";
  }
  return sourceSequences
    .map((seq) => `EVENT #${seq.toString().padStart(2, "0")}`)
    .join(" / ");
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

function runConnectionLabel(
  connection: WorkbenchConnectionState,
  replaying: boolean,
): string {
  if (replaying) {
    return connection === "online"
      ? "SSE 在线 · 回放快照"
      : `回放快照 · ${connectionLabel(connection)}`;
  }
  return connectionLabel(connection);
}

function connectionTone(
  connection: WorkbenchConnectionState,
  replaying: boolean,
): "neutral" | "info" | "success" | "warning" {
  if (replaying) {
    return "neutral";
  }
  switch (connection) {
    case "online":
      return "success";
    case "connecting":
      return "info";
    case "reconnecting":
    case "offline":
      return "warning";
    case "idle":
    case "sealed":
      return "neutral";
    default: {
      const exhaustive: never = connection;
      return exhaustive;
    }
  }
}

function phaseIcon(phase: TimelinePhase) {
  switch (phase.status) {
    case "complete":
      return <Check size={12} />;
    case "current":
      return <Circle fill="currentColor" size={10} />;
    case "upcoming":
      return <Circle size={10} />;
    default: {
      const exhaustive: never = phase.status;
      return exhaustive;
    }
  }
}

function phaseStatusLabel(status: TimelinePhase["status"]): string {
  switch (status) {
    case "complete":
      return "已完成";
    case "current":
      return "当前";
    case "upcoming":
      return "待处理";
    default: {
      const exhaustive: never = status;
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
