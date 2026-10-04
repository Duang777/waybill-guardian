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
const dataDir = await mkdtemp(join(tmpdir(), "waybill-guardian-e2e-"));
const backendPort = await availablePort(process.env.E2E_BACKEND_PORT);
const webPort = await availablePort(process.env.E2E_WEB_PORT);
const backendURL = `http://127.0.0.1:${backendPort}`;
const webURL = `http://127.0.0.1:${webPort}`;
const processes = [];
let browser = null;

try {
  await mkdir(artifactDir, { recursive: true });
  const goEnvironment = { ...process.env };
  delete goEnvironment.GOROOT;
  const startBackend = () =>
    startProcess("go", ["run", "./cmd/server"], {
      cwd: repoDir,
      env: {
        ...goEnvironment,
        DATA_DIR: dataDir,
        HTTP_ADDR: `127.0.0.1:${backendPort}`,
        DEMO_STEP_DELAY: "25ms",
      },
    });
  let backendProcess = startBackend();
  processes.push(backendProcess);
  await waitForHTTP(`${backendURL}/healthz`, backendProcess, processes);

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
  await waitForHTTP(webURL, webProcess, processes);

  browser = await chromium.launch({
    executablePath: await findChrome(),
    headless: true,
  });
  const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
  let triggerRequests = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/runs")) {
      triggerRequests += 1;
    }
  });
  await page.goto(`${webURL}/waybills/YD2026101001`, { waitUntil: "networkidle" });
  await page.getByText("精密电子元件", { exact: true }).waitFor();

  const runs = [];
  for (let index = 0; index < 3; index += 1) {
    const buttonName = index === 0 ? "启动处置" : "重新处置";
    await page.getByRole("button", { name: buttonName, exact: true }).click();
    await page.getByText("改派至川行快运", { exact: true }).waitFor();
    await page.getByText("待确认", { exact: true }).waitFor();
    await page.getByText("发送至货主 · 川行快运", { exact: true }).waitFor();
    await page.getByText("发送至司机 · 川行快运", { exact: true }).waitFor();

    if (index === 0) {
      await page.screenshot({
        path: join(artifactDir, "desktop-pending.png"),
        fullPage: true,
      });
      await page.reload({ waitUntil: "networkidle" });
      await page.getByText("改派至川行快运", { exact: true }).waitFor();
      await page.getByText("待确认", { exact: true }).waitFor();
      assert(triggerRequests === 1, `page reload triggered ${triggerRequests} demo runs`);

      await stopProcess(backendProcess);
      backendProcess = startBackend();
      processes.push(backendProcess);
      await waitForHTTP(`${backendURL}/healthz`, backendProcess, processes);
      await page.reload({ waitUntil: "networkidle" });
      await page.getByText("改派至川行快运", { exact: true }).waitFor();
      await page.getByText("待确认", { exact: true }).waitFor();
      assert(triggerRequests === 1, `backend restart triggered ${triggerRequests} demo runs`);
    }

    await page.getByRole("button", { name: "确认并执行", exact: true }).click();
    await page.getByText("方案已执行", { exact: true }).waitFor();
    await page
      .locator('[aria-label="运单状态摘要"]')
      .getByText("处置完成", { exact: true })
      .waitFor();
    const eventCount = await page.locator("ol li").count();
    assert(eventCount === 26, `run ${index + 1} produced ${eventCount} events, want 26`);
    assert(
      !(await hasHorizontalOverflow(page)),
      `run ${index + 1} has horizontal overflow`,
    );
    if (index === 0) {
      await page.getByLabel("审计回放进度").fill("1");
      await page
        .locator('[aria-label="运单状态摘要"]')
        .getByText("正在归因", { exact: true })
        .waitFor();
      assert(
        (await page.getByText("方案已执行", { exact: true }).count()) === 0,
        "approval panel did not follow the playback cursor",
      );
      await page.getByRole("button", { name: "实时", exact: true }).click();
      await page.getByText("方案已执行", { exact: true }).waitFor();
    }
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

  await page.getByRole("button", { name: "重新处置", exact: true }).click();
  await page.getByText("改派至川行快运", { exact: true }).waitFor();
  await page.getByRole("button", { name: "驳回方案", exact: true }).click();
  const rejectionReason = "首选承运商当前无可用车辆";
  await page.getByLabel("驳回原因").fill(rejectionReason);
  const rejectPattern = "**/api/approvals/*/reject";
  await page.route(rejectPattern, (route) =>
    route.fulfill({
      status: 409,
      contentType: "application/json",
      body: JSON.stringify({
        error: { code: "decision_conflict", message: "simulated conflict" },
      }),
    }),
  );
  await page.getByRole("button", { name: "确认驳回", exact: true }).click();
  await page.getByText("simulated conflict", { exact: true }).waitFor();
  assert(
    (await page.getByLabel("驳回原因").inputValue()) === rejectionReason,
    "failed rejection discarded the operator reason",
  );
  await page.unroute(rejectPattern);

  const pendingResponse = await page.request.get(`${backendURL}/api/approvals?status=pending`);
  const pendingBody = await pendingResponse.json();
  const pendingApprovalID = pendingBody.approvals?.[0]?.id;
  assert(typeof pendingApprovalID === "string", "pending approval was not discoverable");
  const externalReject = await page.request.post(
    `${backendURL}/api/approvals/${encodeURIComponent(pendingApprovalID)}/reject`,
    { data: { reason: rejectionReason } },
  );
  assert(externalReject.ok(), `external rejection failed with ${externalReject.status()}`);
  await page.getByText("改派至蜀道联运", { exact: true }).waitFor();
  assert(
    (await page.getByLabel("驳回原因").count()) === 0,
    "new approval retained the previous rejection draft",
  );
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

async function stopProcess(child) {
  if (child.pid === undefined || child.exitCode !== null) {
    return;
  }
  const stopped = new Promise((resolveStopped) => child.once("exit", resolveStopped));
  process.kill(-child.pid, "SIGTERM");
  await stopped;
}
