import { readFile } from "node:fs/promises";
import { gzipSync } from "node:zlib";
import { join } from "node:path";
import { webDir } from "./demo-runtime.mjs";

const manifestPath = join(webDir, "dist", ".vite", "manifest.json");
const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
const entryKey = "index.html";
const chartKey = "src/charts/OverviewEChart.tsx";
const sceneKey = "src/components/HubNetworkScene.tsx";
const mapKey = "node_modules/maplibre-gl/dist/maplibre-gl.mjs";
const budgetBytes = 350 * 1_024;
const entryBaselineGzipBytes = 187_686;
const entryGrowthBudgetBytes = 25 * 1_024;

const entry = requireChunk(entryKey);
const chart = requireChunk(chartKey);
const scene = requireChunk(sceneKey);
const map = requireChunk(mapKey);

assert(entry.isEntry === true, "Vite manifest does not mark index.html as entry");
assert(
  chart.isDynamicEntry === true &&
    entry.dynamicImports?.includes(chartKey) === true,
  "ECharts runtime is not isolated behind the overview dynamic import",
);
assert(
  new Set([entry.file, chart.file, scene.file, map.file]).size === 4,
  "ECharts shares an output chunk with the entry, Three.js, or MapLibre",
);

const chartFiles = collectDynamicFiles(chartKey);
let gzipBytes = 0;
let rawBytes = 0;
for (const file of chartFiles) {
  const source = await readFile(join(webDir, "dist", file));
  rawBytes += source.byteLength;
  gzipBytes += gzipSync(source).byteLength;
}

const entrySource = await readFile(join(webDir, "dist", entry.file));
const entryText = entrySource.toString("utf8");
const entryGzipBytes = gzipSync(entrySource).byteLength;
assert(
  !entryText.includes("经营图表渲染失败") &&
    !entryText.includes("wg-paper"),
  "ECharts runtime markers leaked into the initial entry chunk",
);
assert(
  gzipBytes < budgetBytes,
  `chart graph gzip ${gzipBytes} bytes exceeds ${budgetBytes} bytes`,
);
assert(
  entryGzipBytes < entryBaselineGzipBytes + entryGrowthBudgetBytes,
  `entry gzip ${entryGzipBytes} bytes exceeds motion growth budget ` +
    `${entryBaselineGzipBytes + entryGrowthBudgetBytes} bytes`,
);

console.log(
  `bundle verified: entry gzip=${entryGzipBytes}; ` +
    `charts=${chartFiles.join(", ")} raw=${rawBytes} gzip=${gzipBytes}`,
);

function requireChunk(key) {
  const chunk = manifest[key];
  assert(chunk !== undefined, `Vite manifest is missing ${key}`);
  return chunk;
}

function collectDynamicFiles(rootKey) {
  const files = [];
  const visited = new Set();
  const visit = (key) => {
    if (visited.has(key)) {
      return;
    }
    visited.add(key);
    const chunk = requireChunk(key);
    files.push(chunk.file);
    for (const dependency of chunk.dynamicImports ?? []) {
      visit(dependency);
    }
  };
  visit(rootKey);
  return files;
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
