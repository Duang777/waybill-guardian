import { Toaster as SonnerToaster, type ToasterProps } from "sonner";
import styles from "./ui.module.css";

export function Toaster(props: ToasterProps) {
  return (
    <SonnerToaster
      theme="light"
      position="bottom-right"
      visibleToasts={3}
      closeButton
      containerAriaLabel="操作通知"
      mobileOffset={12}
      offset={18}
      toastOptions={{
        unstyled: true,
        classNames: {
          toast: styles.toast,
          content: styles.toastContent,
          title: styles.toastTitle,
          description: styles.toastDescription,
          icon: styles.toastIcon,
          closeButton: styles.toastClose,
          success: styles.toastSuccess,
          warning: styles.toastWarning,
          error: styles.toastError,
          info: styles.toastInfo,
        },
        closeButtonAriaLabel: "关闭通知",
      }}
      {...props}
    />
  );
}
