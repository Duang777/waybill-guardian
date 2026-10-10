import { Check } from "lucide-react";
import { forwardRef, type InputHTMLAttributes } from "react";
import styles from "./ui.module.css";

export type CheckboxProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "checked" | "onChange" | "type"
> & {
  checked: boolean;
  onCheckedChange: (checked: boolean) => void;
};

export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(
  function Checkbox(
    { checked, className, onCheckedChange, ...props },
    ref,
  ) {
    return (
      <span
        className={[styles.checkboxRoot, className].filter(Boolean).join(" ")}
        data-disabled={props.disabled || undefined}
        data-state={checked ? "checked" : "unchecked"}
      >
        <input
          {...props}
          className={styles.checkboxInput}
          type="checkbox"
          checked={checked}
          data-slot="checkbox"
          data-state={checked ? "checked" : "unchecked"}
          onChange={(event) => onCheckedChange(event.currentTarget.checked)}
          ref={ref}
        />
        <span className={styles.checkboxVisual} aria-hidden="true">
          <Check aria-hidden="true" size={13} strokeWidth={2.5} />
        </span>
      </span>
    );
  },
);
