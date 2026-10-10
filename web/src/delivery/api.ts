import { z } from "zod";
import { APIError } from "../api";
import {
  deliveryWorkspaceSchema,
  type DeliveryWorkspace,
  type PlanRevisionID,
} from "./contract";

const problemSchema = z
  .object({
    error: z
      .object({
        code: z.string().min(1),
        message: z.string().min(1),
      })
      .strict(),
  })
  .strict();

export async function getDeliveryWorkspace(
  revisionID: PlanRevisionID,
  signal?: AbortSignal,
): Promise<DeliveryWorkspace> {
  const response = await fetch(
    `/api/delivery/plan-revisions/${encodeURIComponent(revisionID)}/workspace`,
    { signal },
  );
  const body = await response.text();
  let raw: unknown;
  try {
    raw = JSON.parse(body);
  } catch {
    throw new APIError(
      response.ok
        ? "调度服务返回的内容不是有效 JSON"
        : `调度请求失败，HTTP ${response.status}，且响应不是有效 JSON`,
      "invalid_json",
      response.status,
    );
  }

  if (!response.ok) {
    const problem = problemSchema.safeParse(raw);
    if (problem.success) {
      throw new APIError(
        problem.data.error.message,
        problem.data.error.code,
        response.status,
      );
    }
    throw new APIError(
      `调度请求失败，HTTP ${response.status}`,
      "invalid_response",
      response.status,
    );
  }

  const parsed = deliveryWorkspaceSchema.safeParse(raw);
  if (!parsed.success) {
    const issue = parsed.error.issues[0];
    const location =
      issue === undefined || issue.path.length === 0
        ? ""
        : `，字段 ${issue.path.join(".")}`;
    throw new APIError(
      `调度服务数据不符合 delivery.workspace.v1 契约${location}`,
      "invalid_contract",
      response.status,
    );
  }
  if (parsed.data.plan.revision_id !== revisionID) {
    throw new APIError(
      "调度服务返回了其他计划修订的数据",
      "invalid_contract",
      response.status,
    );
  }
  return parsed.data;
}

export function confirmDeliveryApproval(
  approvalID: string,
  signal?: AbortSignal,
): Promise<void> {
  return submitDeliveryDecision(
    approvalID,
    "confirm",
    {},
    signal,
  );
}

export function rejectDeliveryApproval(
  approvalID: string,
  reason: string,
  signal?: AbortSignal,
): Promise<void> {
  return submitDeliveryDecision(
    approvalID,
    "reject",
    { reason },
    signal,
  );
}

async function submitDeliveryDecision(
  approvalID: string,
  action: "confirm" | "reject",
  body: Readonly<Record<string, string>>,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch(
    `/api/delivery/approvals/${encodeURIComponent(approvalID)}/${action}`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
      signal,
    },
  );
  if (response.ok) {
    return;
  }
  const responseBody = await response.text();
  let raw: unknown;
  try {
    raw = JSON.parse(responseBody);
  } catch {
    throw new APIError(
      `审批请求失败，HTTP ${response.status}`,
      "invalid_response",
      response.status,
    );
  }
  const problem = problemSchema.safeParse(raw);
  if (problem.success) {
    throw new APIError(
      problem.data.error.message,
      problem.data.error.code,
      response.status,
    );
  }
  throw new APIError(
    `审批请求失败，HTTP ${response.status}`,
    "invalid_response",
    response.status,
  );
}
