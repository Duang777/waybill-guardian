import type { DeliveryStreamConnectionState } from "./api";
import type { DeliveryWorkspace } from "./contract";

export type DeliveryConnectionState =
  | "idle"
  | "connecting"
  | "online"
  | "reconnecting"
  | "offline"
  | "sealed";

export function deliveryConnectionState({
  runState,
  transport,
  browserOnline,
}: {
  runState: DeliveryWorkspace["run_state"] | null;
  transport: DeliveryStreamConnectionState;
  browserOnline: boolean;
}): DeliveryConnectionState {
  if (runState === null) {
    return "idle";
  }
  if (
    runState === "completed" ||
    runState === "failed" ||
    runState === "manual_review"
  ) {
    return "sealed";
  }
  if (!browserOnline) {
    return "offline";
  }
  switch (transport) {
    case "online":
      return "online";
    case "reconnecting":
      return "reconnecting";
    case "connecting":
    case "closed":
      return "connecting";
    default: {
      const exhaustive: never = transport;
      return exhaustive;
    }
  }
}

export function deliveryConnectionLabel(
  state: DeliveryConnectionState,
): string {
  switch (state) {
    case "idle":
      return "实时流未启动";
    case "connecting":
      return "实时流连接中";
    case "online":
      return "实时流在线";
    case "reconnecting":
      return "实时流重连中";
    case "offline":
      return "浏览器离线";
    case "sealed":
      return "审计已固化";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

export function deliveryDecisionUnavailableReason(
  state: DeliveryConnectionState,
): string | null {
  switch (state) {
    case "online":
      return null;
    case "offline":
      return "浏览器处于离线状态，恢复联网后才能提交审批。";
    case "connecting":
    case "reconnecting":
      return "实时审计连接尚未恢复，系统已锁定审批操作。";
    case "sealed":
      return "当前运行已结束或进入人工复核，不能再提交审批。";
    case "idle":
      return "调度实时流尚未启动。";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}
