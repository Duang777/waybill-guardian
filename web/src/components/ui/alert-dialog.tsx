import * as AlertDialogPrimitive from "@radix-ui/react-alert-dialog";
import {
  forwardRef,
  type ComponentPropsWithoutRef,
  type ComponentRef,
  type HTMLAttributes,
} from "react";
import styles from "./ui.module.css";

export const AlertDialog = AlertDialogPrimitive.Root;
export const AlertDialogTrigger = AlertDialogPrimitive.Trigger;
export const AlertDialogCancel = AlertDialogPrimitive.Cancel;

export const AlertDialogOverlay = forwardRef<
  ComponentRef<typeof AlertDialogPrimitive.Overlay>,
  ComponentPropsWithoutRef<typeof AlertDialogPrimitive.Overlay>
>(function AlertDialogOverlay({ className, ...props }, ref) {
  return (
    <AlertDialogPrimitive.Overlay
      {...props}
      className={[styles.dialogOverlay, className].filter(Boolean).join(" ")}
      data-slot="alert-dialog-overlay"
      ref={ref}
    />
  );
});

export const AlertDialogContent = forwardRef<
  ComponentRef<typeof AlertDialogPrimitive.Content>,
  ComponentPropsWithoutRef<typeof AlertDialogPrimitive.Content>
>(function AlertDialogContent({ className, ...props }, ref) {
  return (
    <AlertDialogPrimitive.Portal>
      <AlertDialogOverlay />
      <AlertDialogPrimitive.Content
        {...props}
        className={[styles.dialogContent, className].filter(Boolean).join(" ")}
        data-slot="alert-dialog-content"
        ref={ref}
      />
    </AlertDialogPrimitive.Portal>
  );
});

export const AlertDialogHeader = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement>
>(function AlertDialogHeader({ className, ...props }, ref) {
  return (
    <div
      {...props}
      className={[styles.dialogHeader, className].filter(Boolean).join(" ")}
      ref={ref}
    />
  );
});

export const AlertDialogFooter = forwardRef<
  HTMLDivElement,
  HTMLAttributes<HTMLDivElement>
>(function AlertDialogFooter({ className, ...props }, ref) {
  return (
    <div
      {...props}
      className={[styles.dialogFooter, className].filter(Boolean).join(" ")}
      ref={ref}
    />
  );
});

export const AlertDialogTitle = forwardRef<
  ComponentRef<typeof AlertDialogPrimitive.Title>,
  ComponentPropsWithoutRef<typeof AlertDialogPrimitive.Title>
>(function AlertDialogTitle({ className, ...props }, ref) {
  return (
    <AlertDialogPrimitive.Title
      {...props}
      className={[styles.dialogTitle, className].filter(Boolean).join(" ")}
      ref={ref}
    />
  );
});

export const AlertDialogDescription = forwardRef<
  ComponentRef<typeof AlertDialogPrimitive.Description>,
  ComponentPropsWithoutRef<typeof AlertDialogPrimitive.Description>
>(function AlertDialogDescription({ className, ...props }, ref) {
  return (
    <AlertDialogPrimitive.Description
      {...props}
      className={[styles.dialogDescription, className]
        .filter(Boolean)
        .join(" ")}
      ref={ref}
    />
  );
});
