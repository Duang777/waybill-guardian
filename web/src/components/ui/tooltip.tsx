import type { ReactElement, ReactNode } from "react";
import styles from "./ui.module.css";

export function Tooltip({
  children,
  content,
  side = "top",
}: {
  children: ReactElement;
  content: ReactNode;
  side?: "top" | "right" | "bottom" | "left";
}) {
  return (
    <span className={styles.tooltipRoot}>
      {children}
      <span
        className={styles.tooltipContent}
        data-side={side}
        role="tooltip"
      >
        {content}
        <span className={styles.tooltipArrow} aria-hidden="true" />
      </span>
    </span>
  );
}
