import { readFile, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const write = process.argv.includes("--write");
const webDir = fileURLToPath(new URL("..", import.meta.url));
const files = ["src/app.module.css", "src/overview.module.css"];

const spacingTokens = new Map([
  [1, "--space-0-25"],
  [2, "--space-0-5"],
  [3, "--space-0-75"],
  [4, "--space-1"],
  [5, "--space-1-25"],
  [6, "--space-1-5"],
  [7, "--space-1-75"],
  [8, "--space-2"],
  [9, "--space-2-25"],
  [10, "--space-2-5"],
  [11, "--space-2-75"],
  [12, "--space-3"],
  [13, "--space-3-25"],
  [14, "--space-3-5"],
  [15, "--space-3-75"],
  [16, "--space-4"],
  [18, "--space-4-5"],
  [20, "--space-5"],
  [22, "--space-5-5"],
  [24, "--space-6"],
  [28, "--space-7"],
  [32, "--space-8"],
  [40, "--space-10"],
]);

const durationTokens = new Map([
  [120, "--duration-press"],
  [140, "--duration-focus"],
  [160, "--duration-state"],
  [240, "--duration-reveal"],
]);

for (const relativePath of files) {
  const path = `${webDir}/${relativePath}`;
  const before = await readFile(path, "utf8");
  if (!before.includes("oklch(")) {
    console.log(`${relativePath}: already tokenized`);
    continue;
  }
  const after = before.replace(
    /(^|\n)(\s*)([-\w]+)\s*:\s*([^;{}]+);/g,
    (declaration, lineStart, indentation, property, rawValue) => {
      const value = migrateDeclaration(property, rawValue.trim());
      return `${lineStart}${indentation}${property}: ${value};`;
    },
  );
  if (after === before) {
    console.log(`${relativePath}: already tokenized`);
    continue;
  }
  if (!write) {
    console.error(`${relativePath}: token migration required`);
    process.exitCode = 1;
    continue;
  }
  await writeFile(path, after);
  console.log(`${relativePath}: migrated`);
}

function migrateDeclaration(property, input) {
  let value = input;
  if (/^(gap|row-gap|column-gap|padding|padding-.+|margin|margin-.+)$/.test(property)) {
    value = value.replace(/(\d+)px/g, (_, pixels) => {
      const token = spacingTokens.get(Number(pixels));
      if (token === undefined) {
        throw new Error(`missing spacing token for ${pixels}px`);
      }
      return `var(${token})`;
    });
  }
  if (property === "border-radius") {
    value = value
      .replaceAll("3px", "var(--radius-xs)")
      .replaceAll("4px", "var(--radius-sm)")
      .replaceAll("6px", "var(--radius-md)")
      .replaceAll("8px", "var(--radius-lg)")
      .replaceAll("50%", "var(--radius-full)");
  }
  if (property === "font-family") {
    if (value.startsWith("var(")) {
      return value;
    }
    value = value.includes("Mono") || value.includes("monospace")
      ? "var(--font-family-mono)"
      : "var(--font-family-sans)";
  }
  if (property === "transition") {
    for (const [milliseconds, token] of durationTokens) {
      value = value.replaceAll(`${milliseconds}ms`, `var(${token})`);
    }
    value = value.replaceAll(
      "cubic-bezier(0.16, 1, 0.3, 1)",
      "var(--ease-out)",
    );
  }
  if (property === "box-shadow") {
    return shadowToken(value);
  }
  return value.replace(/oklch\(([^)]+)\)/g, (raw, channels) =>
    colorToken({ property, raw, channels }),
  );
}

function shadowToken(value) {
  if (!value.includes("oklch(")) {
    return value;
  }
  if (value.includes("0 0 0 1px")) {
    return "var(--shadow-panel)";
  }
  if (value.includes("0 0 0 3px") && hueOf(value) > 110) {
    return "var(--glow-success)";
  }
  if (value.includes("0 0 0 5px")) {
    return "var(--glow-primary)";
  }
  if (value.includes("0 4px")) {
    return "var(--shadow-primary)";
  }
  return "var(--shadow-elevated)";
}

function colorToken({ property, raw, channels }) {
  const parsed = parseChannels(channels);
  if (property === "color") {
    return `var(${textToken(parsed)})`;
  }
  if (property === "background" || property === "background-color") {
    return `var(${backgroundToken(parsed)})`;
  }
  if (property === "fill") {
    return `var(${mapFillToken(parsed)})`;
  }
  if (property === "stroke") {
    return `var(${mapStrokeToken(parsed)})`;
  }
  if (property === "accent-color" || property === "outline") {
    return "var(--focus-ring)";
  }
  if (property === "scrollbar-color") {
    return raw === "oklch(0.78 0.012 95)"
      ? "var(--scrollbar)"
      : raw;
  }
  if (property.startsWith("border")) {
    return `var(${borderToken(parsed)})`;
  }
  if (property === "background-image") {
    return "var(--map-grid)";
  }
  return raw;
}

function parseChannels(channels) {
  const [color, alpha] = channels.split("/").map((part) => part.trim());
  const [lightness, chroma, hue = "0"] = color.split(/\s+/);
  return {
    lightness: Number(lightness),
    chroma: Number(chroma),
    hue: Number(hue),
    alpha: alpha === undefined ? 1 : Number(alpha),
  };
}

function textToken(color) {
  const state = stateFamily(color);
  if (state !== null) {
    return `--${state}-text`;
  }
  if (color.lightness < 0.36 || color.lightness > 0.82) {
    return "--foreground-strong";
  }
  if (color.lightness < 0.48) {
    return "--foreground";
  }
  if (color.lightness < 0.63) {
    return "--muted-foreground";
  }
  return "--subtle-foreground";
}

function backgroundToken(color) {
  const state = stateFamily(color);
  if (state !== null) {
    return color.lightness > 0.84 ? `--${state}-wash` : `--${state}`;
  }
  if (color.lightness >= 0.985) {
    return "--surface-2";
  }
  if (color.lightness >= 0.955) {
    return "--surface-1";
  }
  if (color.lightness >= 0.8) {
    return "--surface-3";
  }
  if (color.lightness < 0.24) {
    return "--surface-0";
  }
  return "--surface-3";
}

function borderToken(color) {
  const state = stateFamily(color);
  if (state !== null) {
    return `--${state}-border`;
  }
  return color.lightness > 0.84 ? "--border-subtle" : "--border";
}

function mapFillToken(color) {
  if (color.chroma < 0.01) {
    return color.lightness > 0.9 ? "--map-canvas" : "--map-route";
  }
  if (color.hue >= 120 && color.hue < 190 && color.lightness > 0.8) {
    return "--map-region";
  }
  const state = stateFamily(color);
  if (state === "danger" || state === "primary") {
    return "--map-hub-anomaly";
  }
  return "--map-hub";
}

function mapStrokeToken(color) {
  if (color.chroma < 0.015) {
    return color.lightness > 0.8 ? "--map-marker-border" : "--map-route";
  }
  if (color.hue >= 120 && color.hue < 190) {
    return "--map-region-border";
  }
  if (color.hue >= 180 && color.hue < 260) {
    return color.alpha < 1 ? "--map-route-muted" : "--map-route";
  }
  return "--map-route-anomaly";
}

function stateFamily(color) {
  if (color.chroma < 0.055) {
    return null;
  }
  if (color.hue >= 115 && color.hue < 180) {
    return "success";
  }
  if (color.hue >= 180 && color.hue < 260) {
    return "info";
  }
  if (color.hue < 45) {
    return "danger";
  }
  if (color.hue < 115) {
    return "primary";
  }
  return null;
}

function hueOf(value) {
  const match = /oklch\(\S+\s+\S+\s+(\S+)/.exec(value);
  return match === null ? 0 : Number(match[1]);
}
