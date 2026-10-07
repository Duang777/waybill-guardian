import { Inbox, TriangleAlert, WifiOff } from "lucide-react";
import type { ReactNode } from "react";
import styles from "./state-feedback.module.css";

type StateFeedbackTone = "empty" | "error" | "offline";

type StateFeedbackProps = {
  tone: StateFeedbackTone;
  eyebrow: string;
  title: string;
  detail: string;
  action?: ReactNode;
  compact?: boolean;
  className?: string;
};

export function StateFeedback({
  tone,
  eyebrow,
  title,
  detail,
  action,
  compact = false,
  className,
}: StateFeedbackProps) {
  const classes = [
    styles.state,
    styles[`state${capitalize(tone)}`],
    compact ? styles.stateCompact : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <section
      className={classes}
      role={tone === "error" ? "alert" : "status"}
      aria-live={tone === "error" ? "assertive" : "polite"}
    >
      <span className={styles.stateIcon} aria-hidden="true">
        <StateIcon tone={tone} />
      </span>
      <div className={styles.stateCopy}>
        <span className={styles.stateEyebrow}>{eyebrow}</span>
        <h2>{title}</h2>
        <p>{detail}</p>
      </div>
      {action !== undefined && (
        <div className={styles.stateAction}>{action}</div>
      )}
    </section>
  );
}

export function SkeletonBlock({
  className,
}: {
  className?: string;
}) {
  return (
    <span
      className={[styles.skeleton, className ?? ""].filter(Boolean).join(" ")}
      aria-hidden="true"
    />
  );
}

function StateIcon({ tone }: { tone: StateFeedbackTone }) {
  switch (tone) {
    case "empty":
      return <Inbox size={24} />;
    case "error":
      return <TriangleAlert size={24} />;
    case "offline":
      return <WifiOff size={24} />;
    default: {
      const exhaustive: never = tone;
      return exhaustive;
    }
  }
}

function capitalize(value: StateFeedbackTone): Capitalize<StateFeedbackTone> {
  switch (value) {
    case "empty":
      return "Empty";
    case "error":
      return "Error";
    case "offline":
      return "Offline";
    default: {
      const exhaustive: never = value;
      return exhaustive;
    }
  }
}
