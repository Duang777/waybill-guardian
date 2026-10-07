import {
  runIdSchema,
  waybillIdSchema,
  type RunID,
  type WaybillID,
} from "./api";

export type WorkbenchTarget =
  | { kind: "waybill"; waybillID: WaybillID }
  | { kind: "run"; waybillID: WaybillID; runID: RunID };

export type AppRoute =
  | WorkbenchTarget
  | { kind: "overview" }
  | { kind: "invalid" };

export function parseWorkbenchRoute(
  pathname: string,
  search: string,
): AppRoute {
  const match = /^\/waybills\/([^/]+)\/?$/.exec(pathname);
  if (match === null) {
    return { kind: "overview" };
  }
  let decodedWaybillID: string;
  try {
    decodedWaybillID = decodeURIComponent(match[1] ?? "");
  } catch {
    return { kind: "invalid" };
  }
  const waybill = waybillIdSchema.safeParse(decodedWaybillID);
  if (!waybill.success) {
    return { kind: "invalid" };
  }

  const runValues = new URLSearchParams(search).getAll("run");
  if (runValues.length === 0) {
    return { kind: "waybill", waybillID: waybill.data };
  }
  if (runValues.length !== 1) {
    return { kind: "invalid" };
  }
  const run = runIdSchema.safeParse(runValues[0]);
  if (!run.success) {
    return { kind: "invalid" };
  }
  return {
    kind: "run",
    waybillID: waybill.data,
    runID: run.data,
  };
}

export function formatWorkbenchHref(target: WorkbenchTarget): string {
  const path = `/waybills/${encodeURIComponent(target.waybillID)}`;
  if (target.kind === "waybill") {
    return path;
  }
  const query = new URLSearchParams({ run: target.runID });
  return `${path}?${query.toString()}`;
}

export function overviewWorkbenchHref(
  waybillID: WaybillID,
  projectedRunID?: RunID,
  snapshotRunID?: RunID,
): string {
  const runID = projectedRunID ?? snapshotRunID;
  return formatWorkbenchHref(
    runID === undefined
      ? { kind: "waybill", waybillID }
      : { kind: "run", waybillID, runID },
  );
}
