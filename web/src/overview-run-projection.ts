import type { Run, RunID, RunStatus, WaybillID } from "./api";

export type RunProjection = {
  runID: RunID;
  status: RunStatus;
  lastSeq: number;
};

type ProjectableRun = Pick<
  Run,
  "run_id" | "waybill_id" | "status" | "last_seq"
>;

type ProjectionUpdate = {
  runID: RunID;
  seq: number;
  status: RunStatus | null;
};

export function mergeRunProjections(
  current: ReadonlyMap<WaybillID, RunProjection>,
  runs: readonly ProjectableRun[],
): ReadonlyMap<WaybillID, RunProjection> {
  let next: Map<WaybillID, RunProjection> | undefined;
  for (const run of runs) {
    const existing = (next ?? current).get(run.waybill_id);
    if (
      existing?.runID === run.run_id &&
      existing.lastSeq >= run.last_seq
    ) {
      continue;
    }
    next ??= new Map(current);
    next.set(run.waybill_id, {
      runID: run.run_id,
      status: run.status,
      lastSeq: run.last_seq,
    });
  }
  return next ?? current;
}

export function advanceRunProjection(
  current: RunProjection,
  update: ProjectionUpdate,
): RunProjection {
  if (current.runID !== update.runID || current.lastSeq >= update.seq) {
    return current;
  }
  return {
    runID: current.runID,
    status: update.status ?? current.status,
    lastSeq: update.seq,
  };
}

export function effectiveRunStatus({
  waybillID,
  snapshotStatus,
  projections,
}: {
  waybillID: WaybillID;
  snapshotStatus: RunStatus | undefined;
  projections: ReadonlyMap<WaybillID, RunProjection>;
}): RunStatus | undefined {
  return projections.get(waybillID)?.status ?? snapshotStatus;
}
