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
  const batchResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/runs:batch"),
  );
  await page.getByRole("button", { name: "交给 Agent · 5", exact: true }).click();
  const batchResponse = await batchResponsePromise;
  assert(batchResponse.status() === 202, `batch request returned ${batchResponse.status()}`);
  const batchResponseBody = await batchResponse.json();
  await page.getByText("5 个独立处置任务已启动", { exact: true }).waitFor();
  assert(
    Array.isArray(batchPayload?.waybill_ids) && batchPayload.waybill_ids.length === 5,
    "batch request did not contain five independent waybill IDs",
  );
  assert(
    batchResponseBody.requested === 5 &&
      batchResponseBody.accepted === 5 &&
      Array.isArray(batchResponseBody.results) &&
      batchResponseBody.results.length === 5,
    `batch response did not accept five runs: ${JSON.stringify(batchResponseBody)}`,
  );
  const acceptedRuns = batchResponseBody.results.map((item) => {
    assert(item.error === undefined, `batch item failed: ${JSON.stringify(item)}`);
    assert(item.run?.waybill_id === item.waybill_id, "batch run changed its waybill pairing");
    return item.run;
  });
  assertUnique(
    acceptedRuns.map((run) => run.run_id),
    "batch run IDs",
  );
  assertUnique(
    acceptedRuns.map((run) => run.waybill_id),
    "batch waybill IDs",
  );
  assert(
    sameValues(
      acceptedRuns.map((run) => run.waybill_id),
      batchPayload.waybill_ids,
    ),
    "batch response did not preserve the five requested waybills",
  );
  for (const run of acceptedRuns) {
    await page
      .locator("article")
      .filter({ hasText: run.waybill_id })
      .getByText("待审批", { exact: true })
      .waitFor();
  }

  const runToWaybill = new Map(
    acceptedRuns.map((run) => [run.run_id, run.waybill_id]),
  );
  const pendingApprovals = await waitForPendingApprovals(page, backendURL, 5);
  assertUnique(
    pendingApprovals.map((approval) => approval.id),
    "approval IDs",
  );
  for (const approval of pendingApprovals) {
    assert(
      runToWaybill.get(approval.run_id) === approval.waybill_id,
      `approval changed run/waybill pairing: ${JSON.stringify(approval)}`,
    );
  }

  const confirmedApproval = pendingApprovals[0];
  const remainingApprovals = pendingApprovals.slice(1);
  const confirmResponse = await page.request.post(
    `${backendURL}/api/approvals/${encodeURIComponent(confirmedApproval.id)}/confirm`,
    { data: {} },
  );
  assert(
    confirmResponse.ok(),
    `approval confirmation returned ${confirmResponse.status()}`,
  );
  const confirmedBody = await confirmResponse.json();
  assert(
    confirmedBody.id === confirmedApproval.id &&
      confirmedBody.run_id === confirmedApproval.run_id &&
      confirmedBody.waybill_id === confirmedApproval.waybill_id &&
      confirmedBody.status === "executed",
    `confirmation changed approval identity: ${JSON.stringify(confirmedBody)}`,
  );
  const stillPending = await waitForPendingApprovals(page, backendURL, 4);
  assert(
    sameApprovalSet(stillPending, remainingApprovals),
    "confirming one approval changed one of the other four approvals",
  );
  await page
    .locator("article")
    .filter({ hasText: confirmedApproval.waybill_id })
    .getByText("已闭环", { exact: true })
    .waitFor();

  const mapDrilldown = page.locator('svg a[aria-label^="下钻 "]').first();
  const href = await mapDrilldown.getAttribute("href");
  assert(
    typeof href === "string" && /^\/waybills\/YD\d{10}$/.test(href),
    `invalid map drilldown URL: ${href}`,
  );
  const drilldownWaybillID = href.split("/").at(-1);
  const waybillResponse = await page.request.get(
    `${backendURL}/api/waybills/${encodeURIComponent(drilldownWaybillID)}`,
  );
  assert(waybillResponse.ok(), `drilldown waybill returned ${waybillResponse.status()}`);
  const waybillBody = await waybillResponse.json();
  const anomalyPoint = waybillBody.tracking?.find((point) => point.anomaly);
  assert(anomalyPoint !== undefined, "map drilldown waybill has no anomaly point");

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

  await mapDrilldown.click();
  await page.waitForURL(`${webURL}${href}`);
  assert(
    new URL(page.url()).pathname === href,
    `map drilldown opened ${page.url()}, want ${href}`,
  );
  await page.getByRole("heading", { name: "异常轨迹", exact: true }).waitFor();
  const routePoints = page.locator(
    'button[aria-label^="查看"][aria-label$="轨迹点"]',
  );
  await routePoints.first().waitFor();
  assert(
    (await routePoints.count()) >= 3,
    "workbench did not render route-point controls",
  );
  await page
    .getByRole("button", {
      name: `查看${anomalyPoint.label}轨迹点`,
      exact: true,
    })
    .waitFor();
  await page.getByText(anomalyPoint.label, { exact: true }).last().waitFor();
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
        independent_approvals: 5,
        approvals_left_pending: 4,
        map_drilldown_waybill: drilldownWaybillID,
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

async function waitForPendingApprovals(page, backendURL, expectedCount) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    const response = await page.request.get(
      `${backendURL}/api/approvals?status=pending`,
    );
    assert(response.ok(), `pending approvals returned ${response.status()}`);
    const body = await response.json();
    if (Array.isArray(body.approvals) && body.approvals.length === expectedCount) {
      return body.approvals;
    }
    await page.waitForTimeout(100);
  }
  throw new Error(`timed out waiting for ${expectedCount} pending approvals`);
}

function sameApprovalSet(actual, expected) {
  const expectedByID = new Map(
    expected.map((approval) => [
      approval.id,
      `${approval.run_id}\u0000${approval.waybill_id}`,
    ]),
  );
  return (
    actual.length === expected.length &&
    actual.every(
      (approval) =>
        expectedByID.get(approval.id) ===
        `${approval.run_id}\u0000${approval.waybill_id}`,
    )
  );
}

function sameValues(actual, expected) {
  return (
    actual.length === expected.length &&
    actual.every((value) => expected.includes(value))
  );
}

function assertUnique(values, label) {
  assert(new Set(values).size === values.length, `${label} are not unique`);
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
