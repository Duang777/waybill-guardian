import { describe, expect, it } from "vitest";
import {
  buildDeliverySteps,
  buildRoutePlot,
  findStepIndex,
  formatCurrency,
  formatDistance,
  formatPercentFromPPM,
  resolveTripContext,
  selectionForStep,
} from "./model";
import { deliveryWorkspaceFixture } from "./test-fixture";

describe("delivery workspace model", () => {
  it("builds one ordered step for every planned stop", () => {
    const steps = buildDeliverySteps(deliveryWorkspaceFixture());

    expect(steps).toHaveLength(4);
    expect(steps.map((step) => step.location.name)).toEqual([
      "杭州传化公路港",
      "萧山产业园",
      "绍兴柯桥仓",
      "余杭制造基地",
    ]);
    expect(steps.map((step) => step.loadStage?.placements.length)).toEqual([
      8, 6, 4, 0,
    ]);
  });

  it("maps a selected step back to a stable plan position", () => {
    const workspace = deliveryWorkspaceFixture();
    const steps = buildDeliverySteps(workspace);
    const selected = steps[2];
    if (selected === undefined) {
      throw new Error("fixture requires a third route step");
    }
    const selection = selectionForStep(selected);

    expect(findStepIndex(steps, selection)).toBe(2);
    expect(resolveTripContext(workspace, selection)?.trip.id).toBe(
      "TRIP-HZ-01",
    );
  });

  it("normalizes route coordinates without changing route order", () => {
    const steps = buildDeliverySteps(deliveryWorkspaceFixture());
    const first = steps[0];
    if (first === undefined) {
      throw new Error("fixture requires at least one route step");
    }
    const points = buildRoutePlot(steps, selectionForStep(first));

    expect(points.map((point) => point.sequence)).toEqual([1, 2, 3, 4]);
    expect(points.filter((point) => point.selected)).toHaveLength(1);
    expect(
      points.every(
        (point) =>
          point.xPercent >= 8 &&
          point.xPercent <= 92 &&
          point.yPercent >= 8 &&
          point.yPercent <= 92,
      ),
    ).toBe(true);
  });

  it("formats authoritative metrics without recomputing them", () => {
    expect(formatDistance(86_420)).toBe("86.4 km");
    expect(formatCurrency(74_360)).toBe("¥744");
    expect(formatPercentFromPPM(1_000_000)).toBe("100.0%");
  });
});
