import type {
  RunID,
  RunStatus,
  TimelineConnectionState,
} from "./api";

export type WorkbenchConnectionState =
  | "idle"
  | "connecting"
  | "online"
  | "reconnecting"
  | "offline"
  | "sealed";

export function workbenchConnectionState({
  runID,
  runStatus,
  timelineConnection,
  browserOnline,
}: {
  runID: RunID | null;
  runStatus: RunStatus | null;
  timelineConnection: TimelineConnectionState;
  browserOnline: boolean;
}): WorkbenchConnectionState {
  if (runID === null) {
    return "idle";
  }
  if (runStatus !== null && isTerminalRunStatus(runStatus)) {
    return "sealed";
  }
  if (!browserOnline) {
    return "offline";
  }
  switch (timelineConnection) {
    case "online":
      return "online";
    case "reconnecting":
      return "reconnecting";
    case "connecting":
    case "closed":
      return "connecting";
    default: {
      const exhaustive: never = timelineConnection;
      return exhaustive;
    }
  }
}

export function connectionLabel(state: WorkbenchConnectionState): string {
  switch (state) {
    case "idle":
      return "未启动";
    case "connecting":
      return "SSE 连接中";
    case "online":
      return "SSE 在线";
    case "reconnecting":
      return "SSE 重连中";
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

export function decisionUnavailableReason(
  state: WorkbenchConnectionState,
): string | null {
  switch (state) {
    case "online":
      return null;
    case "offline":
      return "浏览器处于离线状态，恢复联网后才能提交审批。";
    case "reconnecting":
    case "connecting":
      return "实时审计连接尚未恢复，系统已暂时锁定审批操作。";
    case "sealed":
      return "当前运行已结束，不能再提交审批。";
    case "idle":
      return "处置任务尚未启动。";
    default: {
      const exhaustive: never = state;
      return exhaustive;
    }
  }
}

function isTerminalRunStatus(status: RunStatus): boolean {
  return (
    status === "completed" ||
    status === "rejected" ||
    status === "failed" ||
    status === "review_required" ||
    status === "manual_review"
  );
}
