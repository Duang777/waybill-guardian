import { describe, expect, it } from "vitest";
import { APIError } from "./api";
import { requestIssueCopy, toRequestIssue } from "./request-issue";

describe("request issue presentation", () => {
  it.each([
    {
      error: new APIError("字段缺失", "invalid_contract", 200),
      kind: "contract",
      title: "响应格式错误",
    },
    {
      error: new APIError("forbidden", "forbidden", 403),
      kind: "forbidden",
      title: "没有访问权限",
    },
    {
      error: new APIError("deadline exceeded", "deadline_exceeded", 504),
      kind: "timeout",
      title: "请求处理超时",
    },
    {
      error: new APIError("upstream failed", "internal_error", 500),
      kind: "unavailable",
      title: "服务暂不可用",
    },
  ])("maps $kind failures to actionable copy", ({ error, kind, title }) => {
    const issue = toRequestIssue(error);

    expect(issue.kind).toBe(kind);
    expect(requestIssueCopy(issue).title).toBe(title);
  });

  it("treats a failed network connection as service unavailability", () => {
    const issue = toRequestIssue(new TypeError("Failed to fetch"));

    expect(issue).toEqual({
      kind: "unavailable",
      detail: "无法连接到服务",
    });
  });
});
