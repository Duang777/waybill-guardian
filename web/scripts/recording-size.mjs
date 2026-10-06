const recordingSizes = new Map([
  ["1600x900", { width: 1600, height: 900 }],
  ["1920x1080", { width: 1920, height: 1080 }],
]);

export function recordingSize(value) {
  const label = value?.trim() || "1600x900";
  const size = recordingSizes.get(label);
  if (size === undefined) {
    throw new Error(
      `RECORD_RESOLUTION must be one of: ${[...recordingSizes.keys()].join(", ")}`,
    );
  }
  return { label, ...size };
}
