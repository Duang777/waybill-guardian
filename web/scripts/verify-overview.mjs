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
  const page = await browser.newPage({ viewport: { width: 1920, height: 1080 } });
  let batchPayload = null;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/runs:batch")) {
      batchPayload = request.postDataJSON();
    }
  });

  const navigationStarted = performance.now();
  await page.goto(webURL, { waitUntil: "networkidle" });
  await page
    .getByRole("heading", { name: "全国公路港异常态势", exact: true })
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
  await page.getByText("规则模板", { exact: true }).waitFor();
  const networkMap = page.locator('[data-network-renderer="webgl"]');
  await networkMap.locator("canvas").waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-network-renderer="webgl"]')
        ?.getAttribute("data-scene-ready") === "true",
  );
  await page.waitForFunction(() => {
    const stage = document.querySelector('[data-network-renderer="webgl"]');
    return Number(stage?.getAttribute("data-scene-hubs")) > 0;
  });
  const networkCanvas = networkMap.locator("canvas");
  const drawCalls = Number(await networkMap.getAttribute("data-draw-calls"));
  assert(
    Number.isFinite(drawCalls) && drawCalls > 0 && drawCalls <= 50,
    `3D network rendered with ${drawCalls} draw calls`,
  );
  const pixelProbe = await probeCanvasPixels(networkCanvas);
  assert(
    pixelProbe !== null && pixelProbe.colors >= 3,
    `3D network canvas is blank: ${JSON.stringify(pixelProbe)}`,
  );
  const overviewResponse = await page.request.get(`${backendURL}/api/overview`);
  assert(
    overviewResponse.ok(),
    `overview verification request returned ${overviewResponse.status()}`,
  );
  const overviewBody = await overviewResponse.json();
  const sceneCounts = {
    hubs: Number(await networkMap.getAttribute("data-scene-hubs")),
    routes: Number(await networkMap.getAttribute("data-scene-routes")),
    markers: Number(await networkMap.getAttribute("data-scene-markers")),
  };
  const expectedSceneCounts = {
    hubs: overviewBody.hubs.length,
    routes: overviewBody.routes.length,
    markers: overviewBody.routes.reduce(
      (count, route) => count + Math.min(route.waybills, 4),
      0,
    ),
  };
  assert(
    JSON.stringify(sceneCounts) === JSON.stringify(expectedSceneCounts),
    `3D scene counts ${JSON.stringify(sceneCounts)}, want ${JSON.stringify(expectedSceneCounts)}`,
  );
  const firstHub = overviewBody.hubs[0];
  const hubPicker = page.getByLabel("选择公路港", { exact: true });
  await hubPicker.selectOption(firstHub.hub_id);
  await page
    .locator('[role="status"]')
    .getByText(firstHub.name, { exact: true })
    .waitFor();
  await hubPicker.selectOption("");
  const mapBounds = await networkMap.boundingBox();
  assert(
    mapBounds !== null && mapBounds.width > 600 && mapBounds.height > 400,
    "desktop network map is blank or incorrectly framed",
  );
  const mapPanelBounds = await page
    .locator('[aria-labelledby="map-heading"]')
    .boundingBox();
  const queueBounds = await page
    .locator('[aria-labelledby="queue-heading"]')
    .boundingBox();
  assert(
    mapPanelBounds !== null &&
      queueBounds !== null &&
      Math.abs(mapPanelBounds.height - queueBounds.height) < 2,
    "desktop risk queue stretched the workspace below the 3D scene",
  );
  const riskQueue = page.locator('[aria-labelledby="queue-heading"]');
  const queueItems = riskQueue.locator("article");
  const initialQueueSize = await queueItems.count();
  assert(initialQueueSize > 5, "risk queue did not render enough anomalies");
  await riskQueue.getByRole("radio", { name: "处置中", exact: true }).check();
  await riskQueue.getByText("当前视图暂无任务", { exact: true }).waitFor();
  assert(
    (await queueItems.count()) === 0,
    "active queue view rendered unassigned anomalies",
  );
  await riskQueue.getByRole("radio", { name: "全部", exact: true }).check();
  assert(
    (await queueItems.count()) === initialQueueSize,
    "all queue view did not restore every anomaly",
  );
  const firstFlowFrame = await networkCanvas.screenshot();
  await page.waitForTimeout(700);
  const secondFlowFrame = await networkCanvas.screenshot();
  assert(
    !firstFlowFrame.equals(secondFlowFrame),
    "shipment markers did not move between animation frames",
  );
  const measuredFPS = await measureSceneFPS(networkCanvas, 90);
  assert(
    measuredFPS >= 50,
    `3D network measured ${measuredFPS.toFixed(1)} FPS, want at least 50`,
  );
  await page.getByRole("button", { name: "聚焦最高风险", exact: true }).click();
  await page.waitForTimeout(1_600);
  const mapDrilldown = page.locator(
    '[role="status"] a[aria-label^="下钻 "]',
  );
  await mapDrilldown.waitFor();
  assert(!(await hasHorizontalOverflow(page)), "desktop overview overflowed horizontally");
  await page.screenshot({
    path: join(artifactDir, "overview-desktop-1920.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1600, height: 900 });
  assert(!(await hasHorizontalOverflow(page)), "1600px overview overflowed horizontally");
  await page.screenshot({
    path: join(artifactDir, "overview-desktop-1600.png"),
  });
  await page.setViewportSize({ width: 1280, height: 900 });
  assert(!(await hasHorizontalOverflow(page)), "1280px overview overflowed horizontally");
  const clippedKPIText = await clippedText(
    page,
    'section[aria-label="24 小时经营指标"] article > span:last-child',
  );
  assert(
    clippedKPIText.length === 0,
    `1280px KPI text was clipped: ${clippedKPIText.join(", ")}`,
  );
  await page.screenshot({
    path: join(artifactDir, "overview-desktop.png"),
  });

  await verifyBriefProvenance(browser, webURL);
  await verifyReducedMotion(browser, webURL);
  await verifyWebGLContextLoss(browser, webURL);
  await verifyWebGLFallback(webURL);

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
  await riskQueue.getByRole("radio", { name: "处置中", exact: true }).check();
  assert(
    (await queueItems.count()) === acceptedRuns.length,
    "active queue view did not isolate the five running incidents",
  );
  assert(
    await riskQueue.getByRole("button", { name: "选择前 5", exact: true }).isDisabled(),
    "active queue view allowed selecting runs already in progress",
  );
  assert(
    (await riskQueue.locator('input[type="checkbox"]:not(:disabled)').count()) === 0,
    "active queue view allowed restarting an in-progress waybill",
  );
  await riskQueue.getByRole("radio", { name: "全部", exact: true }).check();

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
  const undersized = await undersizedControls(page);
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
  await page.getByRole("heading", { name: "运输轨迹证据", exact: true }).waitFor();
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
        hubs: sceneCounts.hubs,
        routes: sceneCounts.routes,
        markers: sceneCounts.markers,
        renderer: "webgl",
        draw_calls: drawCalls,
        sampled_canvas_colors: pixelProbe.colors,
        measured_fps: Math.round(measuredFPS),
        reduced_motion: "static",
        webgl_fallback: "svg",
        queue_views: 3,
        primary_kpis: 4,
        batch_runs: 5,
        independent_approvals: 5,
        approvals_left_pending: 4,
        map_drilldown_waybill: drilldownWaybillID,
        responsive_widths: [1920, 1600, 1280, 375, 320],
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

async function verifyBriefProvenance(browser, webURL) {
  const cases = [
    {
      name: "model",
      brief: {
        mode: "model_read_only",
        source: "doubao-pro-32k-long-model-name",
      },
      label: "模型生成 · doubao-pro-32k-long-model-name",
    },
    {
      name: "fallback",
      brief: {
        mode: "deterministic_read_only",
        source: "rules",
        fallback_reason: "timeout",
      },
      label: "规则模板 · 模型超时",
    },
    {
      name: "model-rules",
      brief: {
        mode: "model_read_only",
        source: "rules",
      },
      label: "模型生成 · rules",
    },
  ];
  for (const testCase of cases) {
    const page = await browser.newPage({ viewport: { width: 320, height: 812 } });
    try {
      await page.route("**/api/overview", async (route) => {
        const response = await route.fetch();
        const body = await response.json();
        body.brief = {
          ...body.brief,
          ...testCase.brief,
        };
        await route.fulfill({ response, json: body });
      });
      await page.goto(webURL, { waitUntil: "networkidle" });
      await page.getByText(testCase.label, { exact: true }).waitFor();
      assert(
        !(await hasHorizontalOverflow(page)),
        `320px ${testCase.name} provenance overflowed horizontally`,
      );
    } finally {
      await page.close();
    }
  }
}

async function probeCanvasPixels(canvas) {
  return canvas.evaluate(async (element) => {
    const gl =
      element.getContext("webgl2") ??
      element.getContext("webgl");
    if (gl === null) {
      return null;
    }
    await new Promise((resolve) => requestAnimationFrame(resolve));
    const samples = [];
    const pixel = new Uint8Array(4);
    for (let y = 1; y < 8; y += 1) {
      for (let x = 1; x < 8; x += 1) {
        gl.readPixels(
          Math.floor((gl.drawingBufferWidth * x) / 8),
          Math.floor((gl.drawingBufferHeight * y) / 8),
          1,
          1,
          gl.RGBA,
          gl.UNSIGNED_BYTE,
          pixel,
        );
        samples.push([...pixel].join(","));
      }
    }
    return {
      width: gl.drawingBufferWidth,
      height: gl.drawingBufferHeight,
      colors: new Set(samples).size,
    };
  });
}

async function measureSceneFPS(canvas, frames) {
  return canvas.evaluate(
    (element, frameCount) =>
      new Promise((resolve) => {
        const firstFrame = Number(element.dataset.sceneFrame);
        const startedAt = performance.now();
        const tick = () => {
          const currentFrame = Number(element.dataset.sceneFrame);
          const renderedFrames = currentFrame - firstFrame;
          const elapsed = performance.now() - startedAt;
          if (renderedFrames >= frameCount) {
            resolve((renderedFrames * 1_000) / elapsed);
            return;
          }
          if (elapsed >= 5_000) {
            resolve((renderedFrames * 1_000) / elapsed);
            return;
          }
          requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      }),
    frames,
  );
}

async function verifyReducedMotion(browser, webURL) {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 900 },
    reducedMotion: "reduce",
  });
  try {
    await page.goto(webURL, { waitUntil: "networkidle" });
    const stage = page.locator('[data-network-renderer="webgl"]');
    await page.waitForFunction(
      () =>
        document
          .querySelector('[data-network-renderer="webgl"]')
          ?.getAttribute("data-scene-ready") === "true",
    );
    const canvas = stage.locator("canvas");
    const firstFrame = await canvas.screenshot();
    await page.waitForTimeout(700);
    const secondFrame = await canvas.screenshot();
    assert(
      firstFrame.equals(secondFrame),
      "reduced-motion network continued animating",
    );
  } finally {
    await page.close();
  }
}

async function verifyWebGLContextLoss(browser, webURL) {
  const page = await browser.newPage({
    viewport: { width: 1280, height: 900 },
  });
  try {
    await page.goto(webURL, { waitUntil: "networkidle" });
    const stage = page.locator('[data-network-renderer="webgl"]');
    await page.waitForFunction(
      () =>
        document
          .querySelector('[data-network-renderer="webgl"]')
          ?.getAttribute("data-scene-ready") === "true",
    );
    const lost = await stage.locator("canvas").evaluate((element) => {
      const gl = element.getContext("webgl2");
      const extension = gl?.getExtension("WEBGL_lose_context");
      if (extension === null || extension === undefined) {
        return false;
      }
      extension.loseContext();
      return true;
    });
    assert(lost, "browser did not expose WEBGL_lose_context");
    const fallback = page.locator('[data-network-renderer="svg"]');
    await fallback.waitFor();
    assert(
      (await fallback.locator("canvas").count()) === 0,
      "context-lost page kept the failed WebGL canvas",
    );
    assert(
      (await fallback.getByRole("img", {
        name: /全国公路港异常网络/,
      }).locator("circle").count()) >= 72,
      "context-lost page did not render the SVG fallback",
    );
  } finally {
    await page.close();
  }
}

async function verifyWebGLFallback(webURL) {
  const fallbackBrowser = await chromium.launch({
    executablePath: await findChrome(),
    headless: true,
    args: ["--disable-webgl"],
  });
  try {
    const page = await fallbackBrowser.newPage({
      viewport: { width: 1280, height: 900 },
    });
    await page.goto(webURL, { waitUntil: "networkidle" });
    const fallback = page.locator('[data-network-renderer="svg"]');
    await fallback.waitFor();
    const networkMap = fallback.getByRole("img", {
      name: /全国公路港异常网络/,
    });
    assert(
      (await networkMap.locator("circle").count()) >= 72,
      "SVG fallback did not render all hub markers",
    );
    assert(
      (await networkMap.locator("line").count()) === 72,
      "SVG fallback did not render all routes",
    );
    assert(
      (await fallback.locator("canvas").count()) === 0,
      "WebGL-disabled page still rendered a canvas",
    );
  } finally {
    await fallbackBrowser.close();
  }
}

async function undersizedControls(page) {
  return page
    .locator('button, select, input[type="checkbox"], input[type="radio"]')
    .evaluateAll((controls) =>
      controls
        .filter((control) => {
          const target =
            control instanceof HTMLInputElement
              ? control.closest("label") ?? control
              : control;
          const bounds = target.getBoundingClientRect();
          return bounds.width < 40 || bounds.height < 40;
        })
        .map(
          (control) =>
            control.getAttribute("aria-label") ??
            control.textContent?.trim() ??
            control.getAttribute("type") ??
            "control",
        ),
    );
}

async function clippedText(page, selector) {
  return page.locator(selector).evaluateAll((elements) =>
    elements
      .filter(
        (element) =>
          element.scrollWidth > element.clientWidth ||
          element.scrollHeight > element.clientHeight,
      )
      .map((element) => element.textContent?.trim() ?? selector),
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
