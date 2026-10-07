import type {
  PendingApprovalSummary,
  RunID,
  RunSummary,
  WaybillID,
} from "./api";

export type RecoverySource<T> =
  | { kind: "ready"; data: readonly T[] }
  | { kind: "error"; message: string };

type RecoveryCandidates = {
  pendingApprovals: RecoverySource<PendingApprovalSummary>;
  activeRuns: RecoverySource<RunSummary>;
};

export type RecoveryDecision =
  | { kind: "recover"; runID: RunID; waybillID: WaybillID }
  | { kind: "select_waybill" }
  | { kind: "blocked"; message: string };

export function decideRecovery({
  pendingApprovals,
  activeRuns,
}: RecoveryCandidates): RecoveryDecision {
  const candidate =
    (pendingApprovals.kind === "ready"
      ? pendingApprovals.data[0]
      : undefined) ??
    (activeRuns.kind === "ready" ? activeRuns.data[0] : undefined);
  if (candidate !== undefined) {
    return {
      kind: "recover",
      runID: candidate.run_id,
      waybillID: candidate.waybill_id,
    };
  }

  const failures = [pendingApprovals, activeRuns]
    .filter((source): source is Extract<typeof source, { kind: "error" }> =>
      source.kind === "error",
    )
    .map((source) => source.message);
  if (failures.length > 0) {
    return { kind: "blocked", message: failures.join("；") };
  }
  return { kind: "select_waybill" };
}
