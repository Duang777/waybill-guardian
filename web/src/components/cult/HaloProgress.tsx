import { m, useReducedMotion } from "motion/react";
import type { ReactNode } from "react";
import { RollingNumber } from "./RollingNumber";
import styles from "./cult.module.css";

export type HaloProgressTone = "high" | "medium" | "low" | "info";

export type HaloProgressProps = {
  value: number | null;
  label?: ReactNode;
  ariaLabel?: string;
  min?: number;
  max?: number;
  showValue?: boolean;
  tone?: HaloProgressTone;
  className?: string;
  formatValue?: (value: number) => string;
};

const toneClasses = {
  high: styles.progressHigh,
  medium: styles.progressMedium,
  low: styles.progressLow,
  info: styles.progressInfo,
} satisfies Record<HaloProgressTone, string>;

const defaultValueFormatter = (value: number): string =>
  new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 0,
  }).format(value);

export function HaloProgress({
  value,
  label,
  ariaLabel,
  min = 0,
  max = 100,
  showValue = false,
  tone = "info",
  className,
  formatValue = defaultValueFormatter,
}: HaloProgressProps) {
  const reduceMotion = useReducedMotion() ?? false;
  const span = max - min;
  const progress =
    value === null || !Number.isFinite(value) || span <= 0
      ? 0
      : Math.min(1, Math.max(0, (value - min) / span));

  return (
    <div
      className={[styles.haloProgress, toneClasses[tone], className]
        .filter(Boolean)
        .join(" ")}
      data-slot="halo-progress"
      role="progressbar"
      aria-label={ariaLabel}
      aria-valuemin={min}
      aria-valuemax={max}
      aria-valuenow={value ?? undefined}
    >
      {(label !== undefined || showValue) && (
        <div className={styles.progressLabel}>
          <span>{label}</span>
          {showValue && value !== null && (
            <strong className={styles.progressValue}>
              <RollingNumber
                value={value}
                format={formatValue}
                label={`${formatValue(value)}`}
              />
            </strong>
          )}
        </div>
      )}
      <div className={styles.progressRim} aria-hidden="true">
        <div className={styles.progressTrack}>
          <m.span
            className={styles.progressIndicator}
            initial={false}
            animate={{ scaleX: progress }}
            transition={
              reduceMotion
                ? { duration: 0 }
                : {
                    type: "spring",
                    bounce: 0.12,
                    damping: 34,
                    stiffness: 420,
                  }
            }
          />
        </div>
      </div>
    </div>
  );
}
