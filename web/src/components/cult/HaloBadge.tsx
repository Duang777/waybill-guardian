import {
  motion,
  useReducedMotion,
  type HTMLMotionProps,
} from "motion/react";
import type { ReactNode } from "react";
import styles from "./cult.module.css";

export type HaloBadgeTone =
  | "neutral"
  | "info"
  | "success"
  | "warning"
  | "danger";

export type HaloBadgeProps = Omit<HTMLMotionProps<"span">, "children"> & {
  children: ReactNode;
  tone?: HaloBadgeTone;
  live?: boolean;
  tabularNums?: boolean;
  layout?: boolean;
};

const toneClasses = {
  neutral: styles.badgeNeutral,
  info: styles.badgeInfo,
  success: styles.badgeSuccess,
  warning: styles.badgeWarning,
  danger: styles.badgeDanger,
} satisfies Record<HaloBadgeTone, string>;

export function HaloBadge({
  children,
  className,
  tone = "neutral",
  live = false,
  tabularNums = false,
  layout = true,
  ...props
}: HaloBadgeProps) {
  const reduceMotion = useReducedMotion() ?? false;

  return (
    <motion.span
      {...props}
      className={[
        styles.haloBadge,
        toneClasses[tone],
        tabularNums ? styles.badgeTabular : "",
        className,
      ]
        .filter(Boolean)
        .join(" ")}
      data-slot="halo-badge"
      layout={layout && !reduceMotion}
      transition={{ type: "spring", bounce: 0, duration: 0.32 }}
    >
      <span className={styles.haloBadgeBody}>
        {live && (
          <span className={styles.badgeLiveDot} aria-hidden="true" />
        )}
        <span>{children}</span>
      </span>
    </motion.span>
  );
}
