import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright-core";
import {
  availablePort,
  findChrome,
  repoDir,
  startProcess,
  stopProcesses,
  waitForHTTP,
  webDir,
} from "./demo-runtime.mjs";

const artifactDir = join(webDir, "artifacts");
const dataDir = await mkdtemp(join(tmpdir(), "waybill-guardian-overview-"));
const dataFile = join(repoDir, "data", "simulated", "waybills-v1.json");
const backendPort = await availablePort(process.env.OVERVIEW_BACKEND_PORT);
const webPort = await availablePort(process.env.OVERVIEW_WEB_PORT);
const backendURL = `http://127.0.0.1:${backendPort}`;
const webURL = `http://127.0.0.1:${webPort}`;
const processes = [];
let browser = null;

try {
  await mkdir(artifactDir, { recursive: true });
  const goEnvironment = { ...process.env };
  delete goEnvironment.GOROOT;
  const backendProcess = startProcess("go", ["run", "./cmd/server"], {
    cwd: repoDir,
    env: {
      ...goEnvironment,
      AGENT_MODE: "offline",
      AUTH_MODE: "local",
      DATA_DIR: dataDir,
      DATA_FILE: dataFile,
      DEMO_STEP_DELAY: "40ms",
      HTTP_ADDR: `127.0.0.1:${backendPort}`,
      MAX_CONCURRENT_RUNS: "8",
      PLATFORM: "file",
      STORAGE: "jsonl",
    },
  });
  processes.push(backendProcess);
  await waitForHTTP(
    `${backendURL}/healthz`,
    backendProcess,
    processes,
    /waybill guardian listening/,
  );

  const webProcess = startProcess(
    "npm",
    [
      "run",
      "dev",
      "--",
      "--host",
      "127.0.0.1",
      "--port",
      String(webPort),
      "--strictPort",
    ],
    {
      cwd: webDir,
      env: {
        ...process.env,
        VITE_API_TARGET: backendURL,
      },
    },
  );
  processes.push(webProcess);
  await waitForHTTP(webURL, webProcess, processes, /Local:/);

  browser = await chromium.launch({
    executablePath: await findChrome(),
    headless: true,
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  let batchPayload = null;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/runs:batch")) {
      batchPayload = request.postDataJSON();
    }
  });

  const navigationStarted = performance.now();
  await page.goto(webURL, { waitUntil: "networkidle" });
  await page
    .getByRole("heading", { name: "全国公路港异常总览", exact: true })
    .waitFor();
  const visibleInMilliseconds = performance.now() - navigationStarted;
  assert(
    visibleInMilliseconds < 3_000,
    `overview became visible after ${Math.round(visibleInMilliseconds)}ms`,
  );
  assert(
    (await page.locator('section[aria-label="24 小时经营指标"] article').count()) === 4,
    "overview did not render four primary KPIs",
  );
  const networkMap = page.getByRole("img", {
    name: /全国公路港异常网络/,
  });
  assert(
    (await networkMap.locator("circle").count()) >= 72,
    "network map did not render all hub markers",
  );
  assert(
    (await networkMap.locator("line").count()) === 72,
    "network map did not render all routes",
  );
  const mapBounds = await networkMap.boundingBox();
  assert(
    mapBounds !== null && mapBounds.width > 600 && mapBounds.height > 400,
    "desktop network map is blank or incorrectly framed",
  );
  assert(!(await hasHorizontalOverflow(page)), "desktop overview overflowed horizontally");
  await page.screenshot({
    path: join(artifactDir, "overview-desktop.png"),
    fullPage: true,
  });

  await page.getByRole("button", { name: "选择前 5", exact: true }).click();
  assert(
    (await page.locator('input[type="checkbox"]:checked').count()) === 5,
    "top-five selection did not select five anomalies",
  );
  await page.getByRole("button", { name: "交给 Agent · 5", exact: true }).click();
  await page.getByText("5 个独立处置任务已启动", { exact: true }).waitFor();
  assert(
    Array.isArray(batchPayload?.waybill_ids) && batchPayload.waybill_ids.length === 5,
    "batch request did not contain five independent waybill IDs",
  );
  await page.getByText("待审批", { exact: true }).first().waitFor();

  const firstDrilldown = page.locator('a[aria-label^="查看运单"]').first();
  const href = await firstDrilldown.getAttribute("href");
  assert(
    typeof href === "string" && /^\/waybills\/YD\d{10}$/.test(href),
    `invalid drilldown URL: ${href}`,
  );

  await page.setViewportSize({ width: 375, height: 812 });
  assert(!(await hasHorizontalOverflow(page)), "375px overview overflowed horizontally");
  await page.screenshot({
    path: join(artifactDir, "overview-mobile-375.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 320, height: 812 });
  assert(!(await hasHorizontalOverflow(page)), "320px overview overflowed horizontally");
  const undersized = await undersizedButtons(page);
  assert(
    undersized.length === 0,
    `320px controls smaller than 40px: ${undersized.join(", ")}`,
  );
  await page.screenshot({
    path: join(artifactDir, "overview-mobile-320.png"),
  });

  await firstDrilldown.click();
  await page.waitForURL(/\/waybills\/YD\d{10}$/);
  await page.getByRole("heading", { name: "异常轨迹", exact: true }).waitFor();
  assert(
    await page.getByRole("link", { name: "返回全国经营总览", exact: true }).isVisible(),
    "workbench does not provide a return path to the overview",
  );

  console.log(
    JSON.stringify(
      {
        first_visible_ms: Math.round(visibleInMilliseconds),
        hubs: 72,
        routes: 72,
        primary_kpis: 4,
        batch_runs: 5,
        responsive_widths: [1280, 375, 320],
      },
      null,
      2,
    ),
  );
} finally {
  await browser?.close();
  stopProcesses(processes);
  await rm(dataDir, { recursive: true, force: true });
}

async function hasHorizontalOverflow(page) {
  return page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
  );
}

async function undersizedButtons(page) {
  return page.locator("button").evaluateAll((buttons) =>
    buttons
      .filter((button) => {
        const bounds = button.getBoundingClientRect();
        return bounds.width < 40 || bounds.height < 40;
      })
      .map((button) => button.getAttribute("aria-label") ?? button.textContent?.trim() ?? "button"),
  );
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
