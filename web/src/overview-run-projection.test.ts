import { describe, expect, it } from "vitest";
import {
  advanceRunProjection,
  effectiveRunStatus,
  mergeRunProjections,
  type RunProjection,
} from "./overview-run-projection";
import { runIdSchema, runSchema, waybillIdSchema } from "./api";

const waybillID = waybillIdSchema.parse("YD2026101001");

function projection({
  runID,
  status,
  lastSeq,
}: {
  runID: string;
  status: RunProjection["status"];
  lastSeq: number;
}): RunProjection {
  return {
    runID: runIdSchema.parse(runID),
    status,
    lastSeq,
  };
}

describe("overview run projections", () => {
  it("preserves the projection map for an empty run summary", () => {
    const current = new Map([
      [
        waybillID,
        projection({
          runID: "run-current",
          status: "awaiting_approval",
          lastSeq: 8,
        }),
      ],
    ]);

    expect(mergeRunProjections(current, [])).toBe(current);
  });

  it("preserves the projection map when every run summary is stale", () => {
    const current = new Map([
      [
        waybillID,
        projection({
          runID: "run-current",
          status: "awaiting_approval",
          lastSeq: 8,
        }),
      ],
    ]);
    const stale = runSchema.parse({
      run_id: "run-current",
      incident_id: "incident-current",
      waybill_id: waybillID,
      status: "investigating",
      last_seq: 7,
    });

    expect(mergeRunProjections(current, [stale])).toBe(current);
  });

  it("discovers a new external run and ignores an older summary", () => {
    const previous = projection({
      runID: "run-previous",
      status: "completed",
      lastSeq: 12,
    });
    const external = runSchema.parse({
      run_id: "run-external",
      incident_id: "incident-external",
      waybill_id: waybillID,
      status: "awaiting_approval",
      last_seq: 8,
    });
    const discovered = mergeRunProjections(
      new Map([[waybillID, previous]]),
      [external],
    );

    expect(discovered.get(waybillID)).toEqual({
      runID: external.run_id,
      status: "awaiting_approval",
      lastSeq: 8,
    });

    const stale = runSchema.parse({
      ...external,
      status: "investigating",
      last_seq: 6,
    });
    const reconciled = mergeRunProjections(discovered, [stale]);

    expect(reconciled.get(waybillID)).toEqual({
      runID: external.run_id,
      status: "awaiting_approval",
      lastSeq: 8,
    });
  });

  it("advances only the matching run with a newer event sequence", () => {
    const current = projection({
      runID: "run-current",
      status: "awaiting_approval",
      lastSeq: 8,
    });

    expect(
      advanceRunProjection(current, {
        runID: runIdSchema.parse("run-current"),
        seq: 9,
        status: "completed",
      }),
    ).toEqual({
      runID: current.runID,
      status: "completed",
      lastSeq: 9,
    });
    expect(
      advanceRunProjection(current, {
        runID: runIdSchema.parse("run-current"),
        seq: 7,
        status: "investigating",
      }),
    ).toEqual(current);
    expect(
      advanceRunProjection(current, {
        runID: runIdSchema.parse("run-other"),
        seq: 20,
        status: "completed",
      }),
    ).toEqual(current);
  });

  it("prefers a live projection over the overview snapshot", () => {
    const current = projection({
      runID: "run-current",
      status: "awaiting_approval",
      lastSeq: 8,
    });

    expect(
      effectiveRunStatus({
        waybillID,
        snapshotStatus: "investigating",
        projections: new Map([[waybillID, current]]),
      }),
    ).toBe("awaiting_approval");
    expect(
      effectiveRunStatus({
        waybillID,
        snapshotStatus: "investigating",
        projections: new Map(),
      }),
    ).toBe("investigating");
  });
});
