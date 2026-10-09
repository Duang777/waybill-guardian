import {
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import styles from "./ui.module.css";

export type SegmentedControlItem<Value extends string> = {
  value: Value;
  label: ReactNode;
  disabled?: boolean;
};

export type SegmentedControlProps<Value extends string> = {
  items: readonly SegmentedControlItem<Value>[];
  value?: Value;
  defaultValue?: Value;
  onValueChange?: (value: Value) => void;
  className?: string;
  disabled?: boolean;
  ariaLabel: string;
  semantics?: "buttons" | "radio";
};

type SegmentedStyle = CSSProperties & {
  "--segment-count": number;
  "--segment-index": number;
};

export function SegmentedControl<Value extends string>({
  items,
  value,
  defaultValue,
  onValueChange,
  className,
  disabled = false,
  ariaLabel,
  semantics = "buttons",
}: SegmentedControlProps<Value>) {
  const radioName = useId();
  const [internalValue, setInternalValue] = useState<Value | undefined>(
    defaultValue ?? items[0]?.value,
  );
  const selected = value ?? internalValue;
  const selectedIndex = Math.max(
    items.findIndex((item) => item.value === selected),
    0,
  );
  const refs = useRef(new Map<Value, HTMLButtonElement>());
  const style = useMemo<SegmentedStyle>(
    () => ({
      "--segment-count": Math.max(items.length, 1),
      "--segment-index": selectedIndex,
    }),
    [items.length, selectedIndex],
  );

  const select = (next: Value) => {
    if (value === undefined) {
      setInternalValue(next);
    }
    if (next !== selected) {
      onValueChange?.(next);
    }
  };

  const moveFocus = (
    event: KeyboardEvent<HTMLButtonElement>,
    currentIndex: number,
  ) => {
    const direction =
      event.key === "ArrowRight" || event.key === "ArrowDown"
        ? 1
        : event.key === "ArrowLeft" || event.key === "ArrowUp"
          ? -1
          : 0;
    if (direction === 0 && event.key !== "Home" && event.key !== "End") {
      return;
    }
    event.preventDefault();
    const enabled = items.filter((item) => !item.disabled);
    if (enabled.length === 0) {
      return;
    }
    const current = items[currentIndex];
    const enabledIndex = enabled.findIndex(
      (item) => item.value === current?.value,
    );
    const next =
      event.key === "Home"
        ? enabled[0]
        : event.key === "End"
          ? enabled.at(-1)
          : enabled[
              (enabledIndex + direction + enabled.length) % enabled.length
            ];
    if (next !== undefined) {
      select(next.value);
      refs.current.get(next.value)?.focus();
    }
  };

  return (
    <div
      className={[styles.segmentedControl, className]
        .filter(Boolean)
        .join(" ")}
      style={style}
      role={semantics === "radio" ? "radiogroup" : "group"}
      aria-label={ariaLabel}
      data-slot="segmented-control"
    >
      <span className={styles.segmentThumb} aria-hidden="true" />
      {items.map((item, index) =>
        semantics === "radio" ? (
          <label
            className={styles.segmentItem}
            data-selected={selected === item.value}
            key={item.value}
          >
            <input
              className={styles.segmentRadio}
              type="radio"
              name={radioName}
              value={item.value}
              aria-label={
                typeof item.label === "string" ? item.label : undefined
              }
              checked={selected === item.value}
              disabled={disabled || item.disabled}
              onChange={() => select(item.value)}
            />
            <span className={styles.segmentContent}>{item.label}</span>
          </label>
        ) : (
          <button
            className={styles.segmentItem}
            type="button"
            key={item.value}
            ref={(node) => {
              if (node === null) {
                refs.current.delete(item.value);
              } else {
                refs.current.set(item.value, node);
              }
            }}
            aria-pressed={selected === item.value}
            disabled={disabled || item.disabled}
            tabIndex={selected === item.value ? 0 : -1}
            onClick={() => select(item.value)}
            onKeyDown={(event) => moveFocus(event, index)}
          >
            {item.label}
          </button>
        ),
      )}
    </div>
  );
}
