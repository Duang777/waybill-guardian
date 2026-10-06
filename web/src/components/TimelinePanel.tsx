import {
  Bot,
  Check,
  ChevronDown,
  ChevronUp,
  CircleDot,
  Cpu,
  History,
  Pause,
  Play,
  Radio,
  Route,
  UserRound,
} from "lucide-react";
import { useEffect, useRef, useState, type Dispatch } from "react";
import styles from "../app.module.css";
import {
  inferenceMode,
  playbackCursor,
  timelinePhases,
  visibleEvents,
  type InferenceMode,
  type TimelineAction,
  type TimelinePhase,
  type TimelinePhaseID,
  type TimelineState,
  type TimelineStep,
} from "../timeline";

type TimelinePanelProps = {
  state: TimelineState;
  showHeader?: boolean;
};

export function AuditDrawer({
  state,
  dispatch,
  open,
  onOpenChange,
}: {
  state: TimelineState;
  dispatch: Dispatch<TimelineAction>;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const mode = inferenceMode(state.events);
  return (
    <section
      className={`${styles.auditDrawer} ${
        open ? styles.auditDrawerOpen : ""
      }`}
      aria-labelledby="audit-drawer-title"
    >
      <button
        className={styles.auditDrawerToggle}
        type="button"
        aria-expanded={open}
        aria-controls="audit-drawer-content"
        onClick={() => onOpenChange(!open)}
      >
        <span className={styles.auditDrawerTitle}>
          <History aria-hidden="true" size={16} />
          <span>
            <strong id="audit-drawer-title">完整审计记录</strong>
            <small>按事件序列校验与回放</small>
          </span>
        </span>
        <span className={styles.auditDrawerMeta}>
          <span className={`${styles.inferenceBadge} ${modeClassName(mode)}`}>
            {modeLabel(mode)}
          </span>
          <span className={styles.eventCount}>
            {state.events.length.toString().padStart(2, "0")} events
          </span>
          <ChevronUp
            className={`${styles.auditDrawerChevron} ${
              open ? styles.auditDrawerChevronOpen : ""
            }`}
            aria-hidden="true"
            size={17}
          />
        </span>
      </button>
      <div
        className={styles.auditDrawerViewport}
        id="audit-drawer-content"
        aria-hidden={!open}
        inert={!open}
      >
        <div className={styles.auditDrawerContent}>
          <div className={styles.playbackBand}>
            <div className={styles.playbackLabel}>
              <Route aria-hidden="true" size={15} />
              <span>轨迹回放</span>
            </div>
            <PlaybackControls state={state} dispatch={dispatch} />
          </div>
          <TimelinePanel state={state} showHeader={false} />
        </div>
      </div>
    </section>
  );
}

export function TimelinePanel({
  state,
  showHeader = true,
}: TimelinePanelProps) {
  const mode = inferenceMode(state.events);
  return (
    <section
      className={`${styles.timelinePanel} ${
        showHeader ? "" : styles.timelinePanelInDrawer
      }`}
      aria-labelledby={showHeader ? "timeline-title" : undefined}
      aria-label={showHeader ? undefined : "Agent 审计事件"}
    >
      {showHeader && (
        <div className={styles.timelineHeader}>
          <div>
            <span className={styles.eyebrow}>Append-only audit</span>
            <h2 id="timeline-title">Agent 审计时间线</h2>
          </div>
          <div className={styles.timelineMeta}>
            <span className={`${styles.inferenceBadge} ${modeClassName(mode)}`}>
              {modeLabel(mode)}
            </span>
            <span className={styles.eventCount}>
              {state.events.length.toString().padStart(2, "0")} events
            </span>
          </div>
        </div>
      )}
      <TimelineFeed state={state} />
    </section>
  );
}

type TimelineFeedProps = {
  state: TimelineState;
};

export function PlaybackControls({
  state,
  dispatch,
}: {
  state: TimelineState;
  dispatch: Dispatch<TimelineAction>;
}) {
  const cursor = playbackCursor(state);
  const isPlaying = state.playback.kind === "playing";
  const isLive = state.playback.kind === "live";
  return (
    <div className={styles.playbackControls}>
      <button
        className={styles.iconButton}
        type="button"
        aria-label={isPlaying ? "暂停回放" : "播放回放"}
        title={isPlaying ? "暂停回放" : "播放回放"}
        disabled={state.events.length === 0}
        onClick={() => dispatch({ type: "toggle_playback" })}
      >
        {isPlaying ? <Pause aria-hidden="true" size={16} /> : <Play aria-hidden="true" size={16} />}
      </button>
      <input
        className={styles.playbackRange}
        type="range"
        min={0}
        max={Math.max(state.events.length, 1)}
        value={cursor}
        aria-label="审计回放进度"
        disabled={state.events.length === 0}
        onChange={(event) =>
          dispatch({ type: "seek", cursor: Number(event.target.value) })
        }
      />
      <span className={styles.playbackPosition}>
        {cursor}/{state.events.length}
      </span>
      <button
        className={`${styles.liveButton} ${isLive ? styles.liveButtonActive : ""}`}
        type="button"
        disabled={isLive}
        onClick={() => dispatch({ type: "go_live" })}
      >
        <Radio aria-hidden="true" size={14} />
        实时
      </button>
    </div>
  );
}

function TimelineFeed({ state }: TimelineFeedProps) {
  const events = visibleEvents(state);
  const phases = timelinePhases(events);
  const feed = useRef<HTMLDivElement>(null);
  const [expandedPhases, setExpandedPhases] = useState<
    readonly TimelinePhaseID[]
  >([]);
  const focusedPhase = phaseContaining(phases, state.focusedSeq);
  const currentPhase =
    phases.find((phase) => phase.status === "current" && phase.steps.length > 0)
      ?.id ?? null;

  useEffect(() => {
    if (state.runID === null || currentPhase === null) {
      setExpandedPhases([]);
      return;
    }
    setExpandedPhases([currentPhase]);
  }, [currentPhase, state.runID]);

  useEffect(() => {
    if (state.focusedSeq === null) {
      return;
    }
    const target = Array.from(
      feed.current?.querySelectorAll<HTMLElement>("[data-event-seqs]") ?? [],
    ).find((candidate) =>
      candidate.dataset.eventSeqs
        ?.split(",")
        .includes(state.focusedSeq?.toString() ?? ""),
    );
    target?.scrollIntoView({ block: "center", behavior: "smooth" });
    target?.focus({ preventScroll: true });
  }, [focusedPhase, state.focusedSeq]);

  if (events.length === 0) {
    return (
      <div className={styles.timelineEmpty}>
        <History aria-hidden="true" size={20} />
        <strong>尚无审计事件</strong>
        <span>处置启动后，工具调用、审批和平台写入会按序出现。</span>
      </div>
    );
  }

  return (
    <div className={styles.timelineFlow} ref={feed}>
      <ol className={styles.phaseStepper} aria-label="处置阶段">
        {phases.map((phase, index) => (
          <li
            className={`${styles.phaseStep} ${
              styles[`phaseStep${capitalizeStatus(phase.status)}`]
            }`}
            aria-current={phase.status === "current" ? "step" : undefined}
            key={phase.id}
          >
            <span className={styles.phaseStepIndex} aria-hidden="true">
              {phase.status === "complete" ? (
                <Check size={12} />
              ) : (
                (index + 1).toString().padStart(2, "0")
              )}
            </span>
            <span>{phase.label}</span>
          </li>
        ))}
      </ol>

      <div className={styles.timelinePhaseList}>
        {phases.map((phase, index) => {
          const expanded =
            phase.steps.length > 0 &&
            (focusedPhase === phase.id ||
              expandedPhases.includes(phase.id));
          const phaseTone = strongestTone(phase.steps);
          return (
            <section
              className={`${styles.timelinePhase} ${
                styles[`timelinePhase${capitalizeStatus(phase.status)}`]
              }`}
              key={phase.id}
            >
              <button
                className={styles.timelinePhaseToggle}
                type="button"
                aria-expanded={expanded}
                aria-controls={`timeline-phase-${phase.id}`}
                disabled={phase.steps.length === 0}
                onClick={() =>
                  setExpandedPhases((current) =>
                    current.includes(phase.id)
                      ? current.filter((id) => id !== phase.id)
                      : [...current, phase.id],
                  )
                }
              >
                <span className={styles.timelinePhaseIndex}>
                  {(index + 1).toString().padStart(2, "0")}
                </span>
                <span
                  className={`${styles.timelinePhaseSignal} ${
                    styles[`eventNode${capitalize(phaseTone)}`]
                  }`}
                  aria-hidden="true"
                >
                  <CircleDot size={12} />
                </span>
                <span className={styles.timelinePhaseCopy}>
                  <strong>{phase.label}</strong>
                  <span>{phase.description}</span>
                </span>
                <span className={styles.timelinePhaseMeta}>
                  <span>{phaseStatusLabel(phase)}</span>
                  {phase.steps.length > 0 && (
                    <ChevronDown
                      className={expanded ? styles.timelineChevronOpen : ""}
                      aria-hidden="true"
                      size={15}
                    />
                  )}
                </span>
              </button>
              {expanded && (
                <ol
                  className={styles.timelineStepList}
                  id={`timeline-phase-${phase.id}`}
                >
                  {phase.steps.map((step) => (
                    <TimelineStepRow
                      focusedSeq={state.focusedSeq}
                      step={step}
                      key={step.key}
                    />
                  ))}
                </ol>
              )}
            </section>
          );
        })}
      </div>
    </div>
  );
}

function TimelineStepRow({
  focusedSeq,
  step,
}: {
  focusedSeq: number | null;
  step: TimelineStep;
}) {
  const focused =
    focusedSeq !== null && step.eventSeqs.includes(focusedSeq);
  return (
    <li
      className={`${styles.timelineEvent} ${
        focused ? styles.timelineEventFocused : ""
      }`}
      data-event-seqs={step.eventSeqs.join(",")}
      tabIndex={focused ? -1 : undefined}
    >
      <span className={styles.sequence}>{formatSequence(step.eventSeqs)}</span>
      <span
        className={`${styles.eventNode} ${
          styles[`eventNode${capitalize(step.tone)}`]
        }`}
      >
        <CircleDot aria-hidden="true" size={12} />
      </span>
      <div className={styles.eventContent}>
        <div className={styles.eventTitle}>
          <strong>{step.label}</strong>
          <time dateTime={step.timestamp}>{formatTime(step.timestamp)}</time>
        </div>
        <p>{step.detail}</p>
        <span className={styles.eventActor}>
          {actorIcon(step.actor)}
          {actorLabel(step.actor)}
        </span>
      </div>
    </li>
  );
}

function phaseContaining(
  phases: readonly TimelinePhase[],
  seq: number | null,
): TimelinePhaseID | null {
  if (seq === null) {
    return null;
  }
  return (
    phases.find((phase) =>
      phase.steps.some((step) => step.eventSeqs.includes(seq)),
    )?.id ?? null
  );
}

function phaseStatusLabel(phase: TimelinePhase): string {
  switch (phase.status) {
    case "complete":
      return `${phase.steps.length} 步已完成`;
    case "current":
      return `${phase.steps.length} 步进行中`;
    case "upcoming":
      return "等待前序阶段";
    default: {
      const exhaustive: never = phase.status;
      return exhaustive;
    }
  }
}

function strongestTone(
  steps: readonly TimelineStep[],
): TimelineStep["tone"] {
  if (steps.some((step) => step.tone === "danger")) {
    return "danger";
  }
  if (steps.some((step) => step.tone === "warning")) {
    return "warning";
  }
  if (steps.some((step) => step.tone === "success")) {
    return "success";
  }
  return "neutral";
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

function formatSequence(sequences: readonly number[]): string {
  const first = sequences[0];
  const last = sequences.at(-1);
  if (first === undefined || last === undefined) {
    return "--";
  }
  const start = first.toString().padStart(2, "0");
  const end = last.toString().padStart(2, "0");
  return first === last ? start : `${start}-${end}`;
}

function modeClassName(mode: InferenceMode): string {
  switch (mode.kind) {
    case "online":
      return styles.inferenceBadgeOnline;
    case "offline":
      return styles.inferenceBadgeOffline;
    case "legacy":
      return styles.inferenceBadgeLegacy;
    default: {
      const exhaustive: never = mode;
      return exhaustive;
    }
  }
}

function modeLabel(mode: InferenceMode): string {
  switch (mode.kind) {
    case "online":
      return mode.model === null ? "在线推理" : `在线推理 · ${mode.model}`;
    case "offline":
      return "离线回放模式";
    case "legacy":
      return "历史运行";
    default: {
      const exhaustive: never = mode;
      return exhaustive;
    }
  }
}

function actorIcon(actor: "agent" | "human" | "system") {
  switch (actor) {
    case "agent":
      return <Bot aria-hidden="true" size={12} />;
    case "human":
      return <UserRound aria-hidden="true" size={12} />;
    case "system":
      return <Cpu aria-hidden="true" size={12} />;
    default: {
      const exhaustive: never = actor;
      return exhaustive;
    }
  }
}

function actorLabel(actor: "agent" | "human" | "system"): string {
  switch (actor) {
    case "agent":
      return "Agent";
    case "human":
      return "人工";
    case "system":
      return "系统";
    default: {
      const exhaustive: never = actor;
      return exhaustive;
    }
  }
}

function capitalize(
  value: TimelineStep["tone"],
): "Neutral" | "Warning" | "Success" | "Danger" {
  switch (value) {
    case "neutral":
      return "Neutral";
    case "warning":
      return "Warning";
    case "success":
      return "Success";
    case "danger":
      return "Danger";
    default: {
      const exhaustive: never = value;
      return exhaustive;
    }
  }
}

function formatTime(value: string): string {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
    timeZone: "Asia/Shanghai",
  }).format(new Date(value));
}
