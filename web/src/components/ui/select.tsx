import { ChevronDown } from "lucide-react";
import styles from "./ui.module.css";

export type SelectOption = {
  value: string;
  label: string;
  disabled?: boolean;
};

export type SelectProps = {
  value: string | null;
  options: readonly SelectOption[];
  onValueChange: (value: string | null) => void;
  ariaLabel: string;
  placeholder: string;
  nullOptionLabel?: string;
  disabled?: boolean;
  className?: string;
};

export function Select({
  value,
  options,
  onValueChange,
  ariaLabel,
  placeholder,
  nullOptionLabel,
  disabled = false,
  className,
}: SelectProps) {
  return (
    <span
      className={[styles.selectRoot, className].filter(Boolean).join(" ")}
      data-disabled={disabled || undefined}
    >
      <select
        className={styles.selectTrigger}
        value={value ?? ""}
        disabled={disabled}
        aria-label={ariaLabel}
        data-slot="select-trigger"
        onChange={(event) =>
          onValueChange(
            event.currentTarget.value === ""
              ? null
              : event.currentTarget.value,
          )
        }
      >
        <option value="" disabled={nullOptionLabel === undefined}>
          {nullOptionLabel ?? placeholder}
        </option>
        {options.map((option) => (
          <option
            value={option.value}
            disabled={option.disabled}
            key={option.value}
          >
            {option.label}
          </option>
        ))}
      </select>
      <ChevronDown
        className={styles.selectIcon}
        aria-hidden="true"
        size={15}
      />
    </span>
  );
}
