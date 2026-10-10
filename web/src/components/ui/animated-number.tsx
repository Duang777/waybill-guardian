import {
  m,
  useReducedMotion,
  useSpring,
  useTransform,
} from "motion/react";
import { useEffect } from "react";
import styles from "./ui.module.css";

export type AnimatedNumberProps = {
  value: number;
  className?: string;
  mass?: number;
  stiffness?: number;
  damping?: number;
  precision?: number;
  format?: (value: number) => string;
  label?: string;
};

const defaultFormatter = (value: number): string =>
  value.toLocaleString("zh-CN");

export function AnimatedNumber({
  value,
  className,
  mass = 0.8,
  stiffness = 75,
  damping = 15,
  precision = 0,
  format = defaultFormatter,
  label,
}: AnimatedNumberProps) {
  const reduceMotion = useReducedMotion() ?? false;
  const spring = useSpring(value, { mass, stiffness, damping });
  const display = useTransform(spring, (current) =>
    format(Number(current.toFixed(precision))),
  );

  useEffect(() => {
    spring.set(value);
  }, [spring, value]);

  const classes = [styles.animatedNumber, className]
    .filter(Boolean)
    .join(" ");
  if (reduceMotion) {
    return (
      <span className={classes} aria-label={label}>
        {format(value)}
      </span>
    );
  }

  return (
    <m.span className={classes} aria-label={label}>
      {display}
    </m.span>
  );
}
