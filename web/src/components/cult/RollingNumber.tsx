import {
  motion,
  useReducedMotion,
  useSpring,
  useTransform,
} from "motion/react";
import { useEffect } from "react";
import styles from "./cult.module.css";

export type RollingNumberProps = {
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

export function RollingNumber({
  value,
  className,
  mass = 0.8,
  stiffness = 75,
  damping = 15,
  precision = 0,
  format = defaultFormatter,
  label,
}: RollingNumberProps) {
  const reduceMotion = useReducedMotion() ?? false;
  const spring = useSpring(value, { mass, stiffness, damping });
  const display = useTransform(spring, (current) =>
    format(Number(current.toFixed(precision))),
  );

  useEffect(() => {
    spring.set(value);
  }, [spring, value]);

  const classes = [styles.rollingNumber, className].filter(Boolean).join(" ");
  if (reduceMotion) {
    return (
      <span className={classes} aria-label={label}>
        {format(value)}
      </span>
    );
  }

  return (
    <motion.span className={classes} aria-label={label}>
      {display}
    </motion.span>
  );
}
