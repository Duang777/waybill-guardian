import { APIError } from "./api";

export type RequestIssue =
  | { kind: "contract"; detail: string }
  | { kind: "forbidden"; detail: string }
  | { kind: "timeout"; detail: string }
  | { kind: "unavailable"; detail: string }
  | { kind: "unknown"; detail: string };

export type RequestIssueCopy = {
  title: string;
  detail: string;
};

export function toRequestIssue(error: unknown): RequestIssue {
  if (error instanceof APIError) {
    if (error.status === 401 || error.status === 403) {
      return { kind: "forbidden", detail: error.message };
    }
    if (
      error.status === 408 ||
      error.status === 504 ||
      error.code.includes("timeout") ||
      error.code.includes("deadline")
    ) {
      return { kind: "timeout", detail: error.message };
    }
    if (error.status >= 500) {
      return { kind: "unavailable", detail: error.message };
    }
    if (
      error.code === "invalid_json" ||
      error.code === "invalid_contract" ||
      error.code === "invalid_response"
    ) {
      return { kind: "contract", detail: error.message };
    }
    return { kind: "unknown", detail: error.message };
  }
  if (
    error instanceof DOMException &&
    (error.name === "TimeoutError" || error.name === "AbortError")
  ) {
    return { kind: "timeout", detail: "请求在完成前已超时" };
  }
  if (error instanceof TypeError) {
    return { kind: "unavailable", detail: "无法连接到服务" };
  }
  if (error instanceof Error) {
    return { kind: "unknown", detail: error.message };
  }
  return { kind: "unknown", detail: "请求未完成，请检查服务状态" };
}

export function requestIssueFromMessage(
  detail: string,
  kind: RequestIssue["kind"] = "unknown",
): RequestIssue {
  return { kind, detail };
}

export function requestIssueCopy(issue: RequestIssue): RequestIssueCopy {
  switch (issue.kind) {
    case "contract":
      return {
        title: "响应格式错误",
        detail: issue.detail,
      };
    case "forbidden":
      return {
        title: "没有访问权限",
        detail: issue.detail,
      };
    case "timeout":
      return {
        title: "请求处理超时",
        detail: issue.detail,
      };
    case "unavailable":
      return {
        title: "服务暂不可用",
        detail: issue.detail,
      };
    case "unknown":
      return {
        title: "请求未完成",
        detail: issue.detail,
      };
    default: {
      const exhaustive: never = issue;
      return exhaustive;
    }
  }
}
