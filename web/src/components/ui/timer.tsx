import { Clock3 } from "lucide-react";
import {
  forwardRef,
  type ComponentType,
  type HTMLAttributes,
} from "react";
import styles from "./ui.module.css";

export type TimerRootProps = HTMLAttributes<HTMLDivElement>;

export const TimerRoot = forwardRef<HTMLDivElement, TimerRootProps>(
  function TimerRoot({ children, className, ...props }, ref) {
    return (
      <div
        {...props}
        className={[styles.timerRoot, className].filter(Boolean).join(" ")}
        data-slot="timer"
        role="timer"
        aria-live="polite"
        aria-atomic="true"
        ref={ref}
      >
        {children}
      </div>
    );
  },
);

export type TimerIconProps = HTMLAttributes<HTMLSpanElement> & {
  icon?: ComponentType<{
    "aria-hidden"?: boolean | "true" | "false";
    size?: number | string;
  }>;
};

export const TimerIcon = forwardRef<HTMLSpanElement, TimerIconProps>(
  function TimerIcon(
    { icon: Icon = Clock3, className, ...props },
    ref,
  ) {
    return (
      <span
        {...props}
        className={[styles.timerIcon, className].filter(Boolean).join(" ")}
        ref={ref}
      >
        <Icon aria-hidden="true" size={13} />
      </span>
    );
  },
);

export type TimerDisplayProps = HTMLAttributes<HTMLSpanElement> & {
  time: string;
  label?: string;
};

export const TimerDisplay = forwardRef<HTMLSpanElement, TimerDisplayProps>(
  function TimerDisplay({ time, label, className, ...props }, ref) {
    return (
      <span
        {...props}
        className={[styles.timerDisplay, className].filter(Boolean).join(" ")}
        aria-label={label ?? `剩余时间：${time}`}
        ref={ref}
      >
        {time}
      </span>
    );
  },
);
