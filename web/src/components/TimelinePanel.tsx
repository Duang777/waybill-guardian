import {
  Bot,
  CircleDot,
  Cpu,
  History,
  Pause,
  Play,
  Radio,
  UserRound,
} from "lucide-react";
import { useEffect, useRef, type Dispatch } from "react";
import styles from "../app.module.css";
import {
  inferenceMode,
  playbackCursor,
  presentEvent,
  visibleEvents,
  type InferenceMode,
  type TimelineAction,
  type TimelineState,
} from "../timeline";

type TimelinePanelProps = {
  state: TimelineState;
};

export function TimelinePanel({ state }: TimelinePanelProps) {
  const mode = inferenceMode(state.events);
  return (
    <section className={styles.timelinePanel} aria-labelledby="timeline-title">
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
  const feed = useRef<HTMLOListElement>(null);

  useEffect(() => {
    if (state.playback.kind === "live") {
      feed.current?.scrollTo({ top: feed.current.scrollHeight, behavior: "smooth" });
    }
  }, [events.length, state.playback.kind]);

  useEffect(() => {
    if (state.focusedSeq === null) {
      return;
    }
    const target = feed.current?.querySelector<HTMLElement>(
      `[data-event-seq="${state.focusedSeq}"]`,
    );
    target?.scrollIntoView({ block: "center", behavior: "smooth" });
    target?.focus({ preventScroll: true });
  }, [state.focusedSeq]);

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
    <ol className={styles.timelineList} ref={feed}>
      {events.map((event) => {
        const presentation = presentEvent(event);
        return (
          <li
            className={`${styles.timelineEvent} ${
              state.focusedSeq === event.seq ? styles.timelineEventFocused : ""
            }`}
            data-event-seq={event.seq}
            key={`${event.run_id}-${event.seq}`}
            tabIndex={-1}
          >
            <span className={styles.sequence}>{event.seq.toString().padStart(2, "0")}</span>
            <span
              className={`${styles.eventNode} ${styles[`eventNode${capitalize(presentation.tone)}`]}`}
            >
              <CircleDot aria-hidden="true" size={12} />
            </span>
            <div className={styles.eventContent}>
              <div className={styles.eventTitle}>
                <strong>{presentation.label}</strong>
                <time dateTime={event.ts}>{formatTime(event.ts)}</time>
              </div>
              <p>{presentation.detail}</p>
              <span className={styles.eventActor}>
                {actorIcon(event.actor)}
                {actorLabel(event.actor)}
              </span>
            </div>
          </li>
        );
      })}
    </ol>
  );
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
  value: ReturnType<typeof presentEvent>["tone"],
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
