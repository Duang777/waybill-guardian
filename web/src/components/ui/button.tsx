import {
  forwardRef,
  type AnchorHTMLAttributes,
  type ButtonHTMLAttributes,
  type ReactNode,
} from "react";
import styles from "./ui.module.css";

export type ButtonVariant =
  | "primary"
  | "secondary"
  | "destructive"
  | "ghost"
  | "icon";

export type ButtonSize = "sm" | "default" | "lg" | "icon";

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  children: ReactNode;
  variant?: ButtonVariant;
  size?: ButtonSize;
};

export type ButtonLinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & {
  children: ReactNode;
  variant?: ButtonVariant;
  size?: ButtonSize;
};

const variantClasses = {
  primary: styles.buttonPrimary,
  secondary: styles.buttonSecondary,
  destructive: styles.buttonDestructive,
  ghost: styles.buttonGhost,
  icon: styles.buttonIconVariant,
} satisfies Record<ButtonVariant, string>;

const sizeClasses = {
  sm: styles.buttonSmall,
  default: styles.buttonDefault,
  lg: styles.buttonLarge,
  icon: styles.buttonIconSize,
} satisfies Record<ButtonSize, string>;

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  function Button(
    {
      children,
      className,
      variant = "primary",
      size = "default",
      ...props
    },
    ref,
  ) {
    return (
      <button
        {...props}
        className={[
          styles.button,
          variantClasses[variant],
          sizeClasses[size],
          className,
        ]
          .filter(Boolean)
          .join(" ")}
        data-slot="button"
        ref={ref}
      >
        {children}
      </button>
    );
  },
);

export const ButtonLink = forwardRef<HTMLAnchorElement, ButtonLinkProps>(
  function ButtonLink(
    {
      children,
      className,
      variant = "ghost",
      size = "default",
      ...props
    },
    ref,
  ) {
    return (
      <a
        {...props}
        className={[
          styles.button,
          variantClasses[variant],
          sizeClasses[size],
          className,
        ]
          .filter(Boolean)
          .join(" ")}
        data-slot="button"
        ref={ref}
      >
        {children}
      </a>
    );
  },
);
