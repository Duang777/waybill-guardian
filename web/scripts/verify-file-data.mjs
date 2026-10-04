import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright-core";
import {
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
const backendPort = Number(process.env.FILE_E2E_BACKEND_PORT ?? "18381");
const webPort = Number(process.env.FILE_E2E_WEB_PORT ?? "15373");
const backendURL = `http://127.0.0.1:${backendPort}`;
const webURL = `http://127.0.0.1:${webPort}`;
const processes = [];
let browser = null;

try {
  await mkdir(artifactDir, { recursive: true });
  const goEnvironment = { ...process.env };
  delete goEnvironment.GOROOT;
  processes.push(
    startProcess("go", ["run", "./cmd/server"], {
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
    }),
  );
  await waitForHTTP(`${backendURL}/healthz`, processes);

  processes.push(
    startProcess(
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
    ),
  );
  await waitForHTTP(webURL, processes);

  browser = await chromium.launch({
    executablePath: await findChrome(),
    headless: true,
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  const runRequests = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/runs")) {
      runRequests.push(request.postDataJSON());
    }
  });

  await page.goto(webURL, { waitUntil: "networkidle" });
  const selector = page.getByLabel("选择异常运单");
  await page.getByText("南京 → 青岛", { exact: true }).waitFor();
  assert(
    (await selector.locator("option").count()) === 2,
    "file catalog did not expose both waybills",
  );

  let delayedRequests = 0;
  await page.route("**/api/waybills/YD2026101042", async (route) => {
    delayedRequests += 1;
    if (delayedRequests === 1) {
      await delay(500);
    }
    await route.continue().catch(() => undefined);
  });
  await selector.selectOption("YD2026101042");
  await selector.selectOption("YD2026101041");
  await delay(650);
  assert(
    (await selector.inputValue()) === "YD2026101041",
    "a stale detail response replaced the latest waybill selection",
  );
  await page.getByText("医疗器械", { exact: true }).waitFor();

  await selector.selectOption("YD2026101042");
  await page.getByText("宁波 → 西安", { exact: true }).waitFor();
  await page.getByText("工业传感器", { exact: true }).waitFor();
  await page.getByText("襄阳服务区停留 4.5 小时", { exact: true }).waitFor();
  await page.getByText("宁波", { exact: true }).last().waitFor();
  await page.getByText("西安", { exact: true }).last().waitFor();

  await page.getByRole("button", { name: "启动处置", exact: true }).click();
  await page.getByText("改派至秦岭货运", { exact: true }).waitFor();
  await page.getByText("待确认", { exact: true }).waitFor();
  assert(runRequests.length === 1, `started ${runRequests.length} runs, want 1`);
  assert(
    runRequests[0]?.waybill_id === "YD2026101042",
    "run request did not use the selected file waybill",
  );

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

function delay(milliseconds) {
  return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds));
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
