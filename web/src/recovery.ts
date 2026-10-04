import type { PendingApprovalSummary, RunID, RunSummary } from "./api";

type RecoveryCandidates = {
  pendingApprovals: readonly PendingApprovalSummary[];
  activeRuns: readonly RunSummary[];
};

export function preferredRecoveryRun({
  pendingApprovals,
  activeRuns,
}: RecoveryCandidates): RunID | null {
  return pendingApprovals[0]?.run_id ?? activeRuns[0]?.run_id ?? null;
}
