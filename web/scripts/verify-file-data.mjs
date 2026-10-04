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
const dataDir = await mkdtemp(join(tmpdir(), "waybill-guardian-file-e2e-"));
const dataFile = join(webDir, "testdata", "waybills-v1.csv");
const backendPort = await availablePort(process.env.FILE_E2E_BACKEND_PORT);
const webPort = await availablePort(process.env.FILE_E2E_WEB_PORT);
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
      AUTH_MODE: "local",
      DATA_DIR: dataDir,
      DATA_FILE: dataFile,
      DEMO_STEP_DELAY: "25ms",
      HTTP_ADDR: `127.0.0.1:${backendPort}`,
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
  await page.addInitScript(() => {
    if (sessionStorage.getItem("waybill-stale-response-tested") === "true") {
      return;
    }
    const originalFetch = window.fetch.bind(window);
    let targetRequests = 0;
    window.__staleWaybillJSONReady = false;
    window.__staleWaybillJSONSettled = false;
    window.fetch = async (input, init) => {
      const response = await originalFetch(input, init);
      const url =
        typeof input === "string"
          ? input
          : input instanceof Request
            ? input.url
            : input.toString();
      if (!url.endsWith("/api/waybills/YD2026101042")) {
        return response;
      }
      targetRequests += 1;
      if (targetRequests !== 1) {
        return response;
      }
      const originalJSON = response.json.bind(response);
      response.json = async () => {
        const data = await originalJSON();
        window.__staleWaybillJSONReady = true;
        await new Promise((release) => {
          window.__releaseStaleWaybillJSON = release;
        });
        requestAnimationFrame(() => {
          requestAnimationFrame(() => {
            window.__staleWaybillJSONSettled = true;
          });
        });
        return data;
      };
      return response;
    };
  });
  const runRequests = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/runs")) {
      runRequests.push(request.postDataJSON());
    }
  });

  await page.goto(`${webURL}/waybills/YD2026101041`, { waitUntil: "networkidle" });
  const selector = page.getByLabel("选择异常运单");
  await page.getByText("南京 → 青岛", { exact: true }).waitFor();
  assert(
    (await selector.locator("option").count()) === 2,
    "file catalog did not expose both waybills",
  );

  await selector.selectOption("YD2026101042");
  await page.waitForFunction(() => window.__staleWaybillJSONReady === true);
  await selector.selectOption("YD2026101041");
  await page.getByText("医疗器械", { exact: true }).waitFor();
  await page.evaluate(() => window.__releaseStaleWaybillJSON());
  await page.waitForFunction(() => window.__staleWaybillJSONSettled === true);
  assert(
    (await selector.inputValue()) === "YD2026101041",
    "a stale detail response replaced the latest waybill selection",
  );
  assert(
    (await page.getByText("医疗器械", { exact: true }).count()) === 1,
    "the stale response replaced the latest waybill details",
  );
  await page.evaluate(() => {
    sessionStorage.setItem("waybill-stale-response-tested", "true");
  });

  await selector.selectOption("YD2026101042");
  await page.getByText("宁波 → 西安", { exact: true }).waitFor();
  await page.getByText("工业传感器", { exact: true }).waitFor();
  await page.getByText("襄阳服务区停留 4.5 小时", { exact: true }).waitFor();
  await page.getByText("宁波", { exact: true }).last().waitFor();
  await page.getByText("西安", { exact: true }).last().waitFor();

  let snapshotFailures = 0;
  const failFirstSnapshot = async (route) => {
    const request = route.request();
    const pathname = new URL(request.url()).pathname;
    if (
      request.method() === "GET" &&
      /^\/api\/runs\/[^/]+$/.test(pathname) &&
      snapshotFailures === 0
    ) {
      snapshotFailures += 1;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "snapshot_unavailable",
            message: "simulated snapshot failure",
          },
        }),
      });
      return;
    }
    await route.continue();
  };
  await page.route("**/api/runs/*", failFirstSnapshot);
  await page.getByRole("button", { name: "启动处置", exact: true }).click();
  await page.getByText("simulated snapshot failure", { exact: true }).waitFor();
  assert(runRequests.length === 1, `started ${runRequests.length} runs, want 1`);
  await page.getByRole("button", { name: "重试", exact: true }).click();
  await page.getByText("改派至秦岭货运", { exact: true }).waitFor();
  await page.getByText("待确认", { exact: true }).waitFor();
  await page.unroute("**/api/runs/*", failFirstSnapshot);
  assert(runRequests.length === 1, `started ${runRequests.length} runs, want 1`);
  assert(
    runRequests[0]?.waybill_id === "YD2026101042",
    "run request did not use the selected file waybill",
  );

  let pendingListFailures = 0;
  const failFirstPendingList = async (route) => {
    if (pendingListFailures === 0) {
      pendingListFailures += 1;
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({
          error: {
            code: "approvals_unavailable",
            message: "simulated approvals failure",
          },
        }),
      });
      return;
    }
    await route.continue();
  };
  await page.route("**/api/approvals?status=pending", failFirstPendingList);
  await page.reload({ waitUntil: "networkidle" });
  await page.getByText("改派至秦岭货运", { exact: true }).waitFor();
  await page.getByText("待确认", { exact: true }).waitFor();
  await page.unroute("**/api/approvals?status=pending", failFirstPendingList);
  assert(runRequests.length === 1, "partial recovery failure started a duplicate run");

  await page.getByRole("button", { name: "确认并执行", exact: true }).click();
  await page.getByText("方案已执行", { exact: true }).waitFor();
  await page
    .locator('[aria-label="运单状态摘要"]')
    .getByText("处置完成", { exact: true })
    .waitFor();
  assert(!(await hasHorizontalOverflow(page)), "desktop file-mode layout overflowed");
  await page.screenshot({
    path: join(artifactDir, "file-data-desktop.png"),
    fullPage: true,
  });

  await page.setViewportSize({ width: 375, height: 812 });
  assert(!(await hasHorizontalOverflow(page)), "mobile file-mode layout overflowed");
  await page.screenshot({
    path: join(artifactDir, "file-data-mobile.png"),
  });
  await page.setViewportSize({ width: 320, height: 812 });
  assert(!(await hasHorizontalOverflow(page)), "320px file-mode layout overflowed");
  const undersized = await undersizedButtons(page);
  assert(
    undersized.length === 0,
    `320px controls smaller than 40px: ${undersized.join(", ")}`,
  );

  console.log(
    JSON.stringify(
      {
        dataset: "web-file-e2e-v1",
        selected_waybill: "YD2026101042",
        route: "宁波 → 西安",
        run_status: "completed",
        stale_response_suppressed: true,
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
