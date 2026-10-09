import { forwardRef, type TextareaHTMLAttributes } from "react";
import styles from "./ui.module.css";

export type TextareaProps =
  TextareaHTMLAttributes<HTMLTextAreaElement> & {
    invalid?: boolean;
  };

export const Textarea = forwardRef<HTMLTextAreaElement, TextareaProps>(
  function Textarea(
    { className, invalid = false, ...props },
    ref,
  ) {
    return (
      <textarea
        {...props}
        className={[styles.textarea, className].filter(Boolean).join(" ")}
        data-slot="textarea"
        aria-invalid={invalid || undefined}
        ref={ref}
      />
    );
  },
);
