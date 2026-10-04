import type { WaybillID, WaybillView } from "./api";

export type WaybillResource =
  | { kind: "empty" }
  | { kind: "loading"; waybillID: WaybillID | null }
  | { kind: "error"; waybillID: WaybillID | null }
  | { kind: "ready"; waybillID: WaybillID; view: WaybillView };
