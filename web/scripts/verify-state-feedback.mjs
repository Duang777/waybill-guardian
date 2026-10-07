import { mkdir } from "node:fs/promises";
import { join } from "node:path";
import { chromium } from "playwright-core";
import {
  availablePort,
  findChrome,
  startProcess,
  stopProcesses,
  waitForHTTP,
  webDir,
} from "./demo-runtime.mjs";

const artifactDir = join(webDir, "artifacts");
const backendPort = await availablePort();
const webPort = await availablePort();
const webURL = `http://127.0.0.1:${webPort}`;
const processes = [];
let browser = null;

try {
  await mkdir(artifactDir, { recursive: true });
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
        VITE_API_TARGET: `http://127.0.0.1:${backendPort}`,
      },
    },
  );
  processes.push(webProcess);
  await waitForHTTP(webURL, webProcess, processes);

  browser = await chromium.launch({
    executablePath: await findChrome(),
    headless: true,
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });

  await page.goto(webURL, { waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { name: "服务暂不可用" }).waitFor();
  await page.screenshot({
    path: join(artifactDir, "state-backend-stopped.png"),
    fullPage: true,
  });

  await page.route("**/api/**", (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({
        error: {
          code: "internal_error",
          message: "upstream unavailable",
        },
      }),
    }),
  );
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { name: "服务暂不可用" }).waitFor();
  await page.getByText("upstream unavailable", { exact: true }).waitFor();
  await page.screenshot({
    path: join(artifactDir, "state-http-500.png"),
    fullPage: true,
  });
  await page.unroute("**/api/**");

  await page.route("**/api/**", (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/plain",
      body: "<html>not json</html>",
    }),
  );
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.getByRole("heading", { name: "响应格式错误" }).waitFor();
  await page.getByText("不是有效 JSON", { exact: false }).waitFor();
  await page.screenshot({
    path: join(artifactDir, "state-invalid-json.png"),
    fullPage: true,
  });
  await page.unroute("**/api/**");

  let unexpectedDetailRequests = 0;
  await page.route("**/api/**", (route) => {
    const requestURL = new URL(route.request().url());
    if (requestURL.pathname === "/api/waybills") {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ waybills: [] }),
      });
    }
    if (
      requestURL.pathname === "/api/runs" &&
      requestURL.searchParams.get("status") === "active"
    ) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ runs: [] }),
      });
    }
    if (
      requestURL.pathname === "/api/approvals" &&
      requestURL.searchParams.get("status") === "pending"
    ) {
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ approvals: [] }),
      });
    }
    unexpectedDetailRequests += 1;
    return route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "not_found", message: "not found" },
      }),
    });
  });
  await page.setViewportSize({ width: 375, height: 812 });
  await page.goto(`${webURL}/waybills/YD2026101001`, {
    waitUntil: "domcontentloaded",
  });
  await page.getByRole("heading", { name: "当前没有异常运单" }).waitFor();
  assert(unexpectedDetailRequests === 0, "empty catalog requested fake detail data");
  assert(
    !(await page.locator("body").textContent()).includes("杭州 → 成都"),
    "empty state rendered the old hard-coded route",
  );
  const overflows = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth,
  );
  assert(!overflows, "empty state overflows at 375px");
  await page.screenshot({
    path: join(artifactDir, "state-empty-mobile.png"),
    fullPage: true,
  });

  console.log("state feedback verification passed");
} finally {
  await browser?.close();
  await stopProcesses(processes);
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
