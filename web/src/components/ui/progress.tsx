import * as ProgressPrimitive from "@radix-ui/react-progress";
import type { CSSProperties, ReactNode } from "react";
import { AnimatedNumber } from "./animated-number";
import styles from "./ui.module.css";

export type ProgressTone = "high" | "medium" | "low" | "info";

export type ProgressProps = {
  value: number | null;
  label?: ReactNode;
  ariaLabel?: string;
  min?: number;
  max?: number;
  showValue?: boolean;
  tone?: ProgressTone;
  className?: string;
  formatValue?: (value: number) => string;
};

const toneClasses = {
  high: styles.progressHigh,
  medium: styles.progressMedium,
  low: styles.progressLow,
  info: styles.progressInfo,
} satisfies Record<ProgressTone, string>;

const defaultValueFormatter = (value: number): string =>
  new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 0,
  }).format(value);

export function Progress({
  value,
  label,
  ariaLabel,
  min = 0,
  max = 100,
  showValue = false,
  tone = "info",
  className,
  formatValue = defaultValueFormatter,
}: ProgressProps) {
  const span = max - min;
  const normalized =
    value === null || !Number.isFinite(value) || span <= 0
      ? 0
      : Math.min(1, Math.max(0, (value - min) / span));
  const radixValue =
    value === null || span <= 0
      ? null
      : Math.min(span, Math.max(0, value - min));
  const indicatorStyle = {
    "--progress-offset": `${(1 - normalized) * -100}%`,
  } as CSSProperties;

  return (
    <ProgressPrimitive.Root
      className={[styles.progress, toneClasses[tone], className]
        .filter(Boolean)
        .join(" ")}
      data-slot="progress"
      value={radixValue}
      max={span > 0 ? span : 100}
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
              <AnimatedNumber
                value={value}
                format={formatValue}
                label={formatValue(value)}
              />
            </strong>
          )}
        </div>
      )}
      <div className={styles.progressRim} aria-hidden="true">
        <div className={styles.progressTrack}>
          <ProgressPrimitive.Indicator
            className={styles.progressIndicator}
            style={indicatorStyle}
          />
        </div>
      </div>
    </ProgressPrimitive.Root>
  );
}
