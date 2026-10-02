import { spawn } from "node:child_process";
import { access, mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright-core";

const webDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repoDir = resolve(webDir, "..");
const artifactDir = join(webDir, "artifacts");
const dataDir = await mkdtemp(join(tmpdir(), "waybill-guardian-e2e-"));
const backendPort = Number(process.env.E2E_BACKEND_PORT ?? "18181");
const webPort = Number(process.env.E2E_WEB_PORT ?? "15173");
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
        DATA_DIR: dataDir,
        HTTP_ADDR: `127.0.0.1:${backendPort}`,
        DEMO_STEP_DELAY: "25ms",
      },
    }),
  );
  await waitForHTTP(`${backendURL}/healthz`);

  processes.push(
    startProcess("npm", [
      "run",
      "dev",
      "--",
      "--host",
      "127.0.0.1",
      "--port",
      String(webPort),
      "--strictPort",
    ], {
      cwd: webDir,
      env: {
        ...process.env,
        VITE_API_TARGET: backendURL,
      },
    }),
  );
  await waitForHTTP(webURL);

  browser = await chromium.launch({
    executablePath: await findChrome(),
    headless: true,
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  await page.goto(webURL, { waitUntil: "networkidle" });
  await page.getByText("精密电子元件", { exact: true }).waitFor();

  const runs = [];
  for (let index = 0; index < 3; index += 1) {
    const buttonName = index === 0 ? "启动演示" : "重新演示";
    await page.getByRole("button", { name: buttonName, exact: true }).click();
    await page.getByText("改派至川行快运", { exact: true }).waitFor();
    await page.getByText("待确认", { exact: true }).waitFor();

    if (index === 0) {
      await page.screenshot({
        path: join(artifactDir, "desktop-pending.png"),
        fullPage: true,
      });
    }

    await page.getByRole("button", { name: "确认并执行", exact: true }).click();
    await page.getByText("方案已执行", { exact: true }).waitFor();
    await page
      .locator('[aria-label="运单状态摘要"]')
      .getByText("处置完成", { exact: true })
      .waitFor();
    const eventCount = await page.locator("ol li").count();
    assert(eventCount === 22, `run ${index + 1} produced ${eventCount} events, want 22`);
    assert(
      !(await hasHorizontalOverflow(page)),
      `run ${index + 1} has horizontal overflow`,
    );
    runs.push({ run: index + 1, eventCount, status: "completed" });
  }

  await page.setViewportSize({ width: 375, height: 812 });
  assert(!(await hasHorizontalOverflow(page)), "mobile layout has horizontal overflow");
  const undersized = await undersizedButtons(page);
  assert(undersized.length === 0, `mobile controls smaller than 40px: ${undersized.join(", ")}`);
  assert(await skipLinkIsHidden(page), "skip link is visible without keyboard focus");
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: join(artifactDir, "mobile-completed.png"),
  });

  await page.getByRole("button", { name: "重新演示", exact: true }).click();
  await page.getByText("改派至川行快运", { exact: true }).waitFor();
  await page.getByRole("button", { name: "驳回方案", exact: true }).click();
  await page.getByLabel("驳回原因").fill("首选承运商当前无可用车辆");
  await page.getByRole("button", { name: "确认驳回", exact: true }).click();
  await page.getByText("改派至蜀道联运", { exact: true }).waitFor();
  const rejectionEventCount = await page.locator("ol li").count();
  assert(rejectionEventCount === 14, `reject path produced ${rejectionEventCount} events, want 14`);
  assert(await skipLinkIsHidden(page), "skip link is visible after the reject flow");
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: join(artifactDir, "mobile-alternative.png"),
  });
  await page.getByText("异常轨迹", { exact: true }).scrollIntoViewIfNeeded();
  await page.screenshot({
    path: join(artifactDir, "mobile-route.png"),
  });

  console.log(JSON.stringify({
    runs,
    rejection: {
      eventCount: rejectionEventCount,
      alternative: "蜀道联运",
    },
    screenshots: [
      join(artifactDir, "desktop-pending.png"),
      join(artifactDir, "mobile-completed.png"),
      join(artifactDir, "mobile-alternative.png"),
      join(artifactDir, "mobile-route.png"),
    ],
  }, null, 2));
} finally {
  await browser?.close();
  for (const child of processes.reverse()) {
    stopProcess(child);
  }
  await rm(dataDir, { recursive: true, force: true });
}

function startProcess(command, args, options) {
  const child = spawn(command, args, {
    ...options,
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  const output = [];
  const collect = (chunk) => {
    output.push(chunk.toString());
    if (output.length > 40) {
      output.shift();
    }
  };
  child.stdout.on("data", collect);
  child.stderr.on("data", collect);
  child.diagnosticOutput = () => output.join("");
  return child;
}

function stopProcess(child) {
  if (child.pid === undefined || child.exitCode !== null) {
    return;
  }
  try {
    process.kill(-child.pid, "SIGTERM");
  } catch (error) {
    if (error.code !== "ESRCH") {
      throw error;
    }
  }
}

async function waitForHTTP(url) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) {
        return;
      }
    } catch {
      // The process may still be compiling or binding its port.
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  }
  const diagnostics = processes.map((child) => child.diagnosticOutput()).join("\n");
  throw new Error(`timed out waiting for ${url}\n${diagnostics}`);
}

async function findChrome() {
  const candidates = [
    process.env.CHROME_PATH,
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
  ].filter((candidate) => typeof candidate === "string" && candidate.length > 0);
  for (const candidate of candidates) {
    try {
      await access(candidate);
      return candidate;
    } catch {
      // Try the next standard installation path.
    }
  }
  throw new Error("Chrome was not found. Set CHROME_PATH to a Chromium executable.");
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

async function skipLinkIsHidden(page) {
  return page.locator('a[href="#main-content"]').evaluate((link) => {
    const bounds = link.getBoundingClientRect();
    return bounds.bottom <= 0;
  });
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
