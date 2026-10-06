import { describe, expect, it } from "vitest";
import { recordingSize } from "./recording-size.mjs";

describe("recordingSize", () => {
  it("defaults to the 1600x900 recording canvas", () => {
    expect(recordingSize(undefined)).toEqual({
      label: "1600x900",
      width: 1600,
      height: 900,
    });
  });

  it("supports the 1920x1080 recording canvas", () => {
    expect(recordingSize("1920x1080")).toEqual({
      label: "1920x1080",
      width: 1920,
      height: 1080,
    });
  });

  it("rejects unsupported dimensions", () => {
    expect(() => recordingSize("1280x720")).toThrow(
      "RECORD_RESOLUTION must be one of: 1600x900, 1920x1080",
    );
  });
});
