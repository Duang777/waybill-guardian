import { describe, expect, it } from "vitest";
import { runIdSchema, waybillIdSchema } from "./api";
import {
  formatWorkbenchHref,
  overviewWorkbenchHref,
  parseWorkbenchRoute,
} from "./workbench-route";

describe("workbench route", () => {
  it("parses and formats an exact run target", () => {
    const route = parseWorkbenchRoute(
      "/waybills/YD2026101042",
      "?run=run-terminal-1",
    );

    expect(route).toEqual({
      kind: "run",
      waybillID: "YD2026101042",
      runID: "run-terminal-1",
    });
    if (route.kind === "run") {
      expect(formatWorkbenchHref(route)).toBe(
        "/waybills/YD2026101042?run=run-terminal-1",
      );
    }
  });

  it("keeps a waybill-only target when no run is present", () => {
    const route = parseWorkbenchRoute("/waybills/YD2026101042", "");

    expect(route).toEqual({
      kind: "waybill",
      waybillID: "YD2026101042",
    });
  });

  it("parses a delivery plan revision target", () => {
    expect(
      parseWorkbenchRoute(
        "/delivery/plans/REV-HZ-1010-07",
        "",
      ),
    ).toEqual({
      kind: "delivery-plan",
      revisionID: "REV-HZ-1010-07",
    });
  });

  it.each([
    ["invalid waybill", "/waybills/not-a-waybill", ""],
    ["empty run", "/waybills/YD2026101042", "?run="],
    ["duplicate run", "/waybills/YD2026101042", "?run=one&run=two"],
    ["invalid encoding", "/waybills/%E0%A4%A", ""],
  ])("rejects %s", (_name, pathname, search) => {
    expect(parseWorkbenchRoute(pathname, search)).toEqual({ kind: "invalid" });
  });

  it("routes non-workbench paths to the overview", () => {
    expect(parseWorkbenchRoute("/", "")).toEqual({ kind: "overview" });
    expect(parseWorkbenchRoute("/not-a-route", "")).toEqual({ kind: "invalid" });
  });

  it("prefers a newer local run projection over a stale overview snapshot", () => {
    const waybillID = waybillIdSchema.parse("YD2026101042");
    const projectedRunID = runIdSchema.parse("run-new-active");
    const snapshotRunID = runIdSchema.parse("run-old-terminal");
    expect(
      overviewWorkbenchHref(
        waybillID,
        projectedRunID,
        snapshotRunID,
      ),
    ).toBe("/waybills/YD2026101042?run=run-new-active");
    expect(
      overviewWorkbenchHref(waybillID, undefined, snapshotRunID),
    ).toBe("/waybills/YD2026101042?run=run-old-terminal");
  });
});
