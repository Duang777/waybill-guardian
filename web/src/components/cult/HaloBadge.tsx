import type { ComponentPropsWithoutRef, ReactNode } from "react";
import styles from "./cult.module.css";

export type HaloBadgeTone =
  | "neutral"
  | "info"
  | "success"
  | "warning"
  | "danger";

export type HaloBadgeProps = Omit<
  ComponentPropsWithoutRef<"span">,
  "children"
> & {
  children: ReactNode;
  tone?: HaloBadgeTone;
  live?: boolean;
  tabularNums?: boolean;
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
  ...props
}: HaloBadgeProps) {
  return (
    <span
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
    >
      <span className={styles.haloBadgeBody}>
        {live && (
          <span className={styles.badgeLiveDot} aria-hidden="true" />
        )}
        <span>{children}</span>
      </span>
    </span>
  );
}
