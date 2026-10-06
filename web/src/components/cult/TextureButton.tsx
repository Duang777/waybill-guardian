import {
  forwardRef,
  type AnchorHTMLAttributes,
  type ButtonHTMLAttributes,
  type ReactNode,
} from "react";
import styles from "./cult.module.css";

export type TextureButtonVariant =
  | "primary"
  | "secondary"
  | "destructive"
  | "minimal"
  | "icon";

export type TextureButtonSize = "sm" | "default" | "lg" | "icon";

export type TextureButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  children: ReactNode;
  variant?: TextureButtonVariant;
  size?: TextureButtonSize;
};

export type TextureLinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & {
  children: ReactNode;
  variant?: TextureButtonVariant;
  size?: TextureButtonSize;
};

const variantClasses = {
  primary: styles.buttonPrimary,
  secondary: styles.buttonSecondary,
  destructive: styles.buttonDestructive,
  minimal: styles.buttonMinimal,
  icon: styles.buttonIcon,
} satisfies Record<TextureButtonVariant, string>;

const sizeClasses = {
  sm: styles.buttonSmall,
  default: styles.buttonDefault,
  lg: styles.buttonLarge,
  icon: styles.buttonIcon,
} satisfies Record<TextureButtonSize, string>;

export const TextureButton = forwardRef<
  HTMLButtonElement,
  TextureButtonProps
>(function TextureButton(
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
        styles.textureButton,
        variantClasses[variant],
        sizeClasses[size],
        className,
      ]
        .filter(Boolean)
        .join(" ")}
      data-slot="texture-button"
      ref={ref}
    >
      <span className={styles.textureButtonInner}>{children}</span>
    </button>
  );
});

export const TextureLink = forwardRef<HTMLAnchorElement, TextureLinkProps>(
  function TextureLink(
    {
      children,
      className,
      variant = "minimal",
      size = "default",
      ...props
    },
    ref,
  ) {
    return (
      <a
        {...props}
        className={[
          styles.textureButton,
          variantClasses[variant],
          sizeClasses[size],
          className,
        ]
          .filter(Boolean)
          .join(" ")}
        data-slot="texture-button"
        ref={ref}
      >
        <span className={styles.textureButtonInner}>{children}</span>
      </a>
    );
  },
);
