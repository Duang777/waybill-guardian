import type { ComponentPropsWithoutRef, ReactNode } from "react";
import styles from "./ui.module.css";

export type BadgeTone =
  | "neutral"
  | "info"
  | "success"
  | "warning"
  | "danger";

export type BadgeProps = Omit<
  ComponentPropsWithoutRef<"span">,
  "children"
> & {
  children: ReactNode;
  tone?: BadgeTone;
  live?: boolean;
  tabularNums?: boolean;
};

const toneClasses = {
  neutral: styles.badgeNeutral,
  info: styles.badgeInfo,
  success: styles.badgeSuccess,
  warning: styles.badgeWarning,
  danger: styles.badgeDanger,
} satisfies Record<BadgeTone, string>;

export function Badge({
  children,
  className,
  tone = "neutral",
  live = false,
  tabularNums = false,
  ...props
}: BadgeProps) {
  return (
    <span
      {...props}
      className={[
        styles.badge,
        toneClasses[tone],
        tabularNums ? styles.badgeTabular : "",
        className,
      ]
        .filter(Boolean)
        .join(" ")}
      data-slot="badge"
    >
      {live && <span className={styles.badgeLiveDot} aria-hidden="true" />}
      <span>{children}</span>
    </span>
  );
}
