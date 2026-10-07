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

const artifactDir = join(webDir, "artifacts", "motion");
const dataDir = await mkdtemp(join(tmpdir(), "waybill-guardian-motion-"));
const backendPort = await availablePort();
const webPort = await availablePort();
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
      DATA_DIR: dataDir,
      DEMO_STEP_DELAY: "260ms",
      HTTP_ADDR: `127.0.0.1:${backendPort}`,
    },
  });
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
  await page.goto(`${webURL}/waybills/YD2026101001`, {
    waitUntil: "networkidle",
  });
  await page.getByText("精密电子元件", { exact: true }).waitFor();

  const pointButtons = page.getByRole("button", { name: /^查看.+轨迹点$/ });
  const pointInspector = page.getByLabel("轨迹点详情");
  assert((await pointButtons.count()) >= 3, "route has fewer than three points");
  await pointInspector.screenshot({
    path: join(artifactDir, "point-01-before.png"),
  });
  await pointButtons.last().click();
  await page
    .locator('[aria-label="轨迹点详情"][data-motion-point="enter"]')
    .waitFor();
  const pointEntry = await motionState(pointInspector);
  assert(
    pointEntry.opacity < 1 || !isIdentityTransform(pointEntry.transform),
    `point inspector did not enter visibly: ${JSON.stringify(pointEntry)}`,
  );
  await pointInspector.screenshot({
    path: join(artifactDir, "point-02-enter.png"),
  });
  await page.waitForTimeout(90);
  await pointInspector.screenshot({
    path: join(artifactDir, "point-03-mid.png"),
  });
  await page.waitForTimeout(180);
  const pointSettled = await motionState(pointInspector);
  assert(
    pointSettled.opacity === 1 && isIdentityTransform(pointSettled.transform),
    `point inspector did not settle: ${JSON.stringify(pointSettled)}`,
  );
  await pointInspector.screenshot({
    path: join(artifactDir, "point-04-settled.png"),
  });

  const rapidTargetLabel = await pointButtons.nth(1).getAttribute("aria-label");
  await pointButtons.first().click();
  await pointButtons.last().click();
  await pointButtons.nth(1).click();
  await page.waitForTimeout(300);
  assert(
    rapidTargetLabel !== null &&
      (await pointInspector.textContent()).includes(
        rapidTargetLabel.replace(/^查看|轨迹点$/g, ""),
      ),
    "rapid point selection did not settle on the last choice",
  );

  await page
    .getByRole("button", { name: /完整审计记录/, exact: false })
    .click();
  await installMotionRecorder(
    page,
    "timelineMotionSamples",
    '[data-motion-event="enter"]',
  );
  await page.getByRole("button", { name: "启动处置", exact: true }).click();
  await page.getByText("改派至川行快运", { exact: true }).waitFor();
  const timelineSamples = await recordedSamples(page, "timelineMotionSamples");
  const timelineEntries = timelineSamples.filter(
    (sample) => sample.phase === "mutation",
  );
  assert(timelineEntries.length > 0, "no live timeline entry was observed");
  assert(
    timelineEntries.some(
      (sample) =>
        sample.opacity < 1 ||
        !isIdentityTransform(sample.transform) ||
        sample.highlightOpacity > 0,
    ),
    `timeline entries had no perceptible feedback: ${JSON.stringify(
      timelineEntries,
    )}`,
  );

  const heading = page.getByText("人工决策闸", { exact: true }).locator("../..");
  const pendingColor = await heading.evaluate(
    (element) => getComputedStyle(element).backgroundColor,
  );
  await installMotionRecorder(
    page,
    "approvalMotionSamples",
    '[data-motion-status]:not([data-motion-status="stable"]), [data-motion-icon="success"]',
  );
  await page
    .getByRole("button", { name: "确认并执行", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "方案已执行", exact: true })
    .waitFor();
  const approvalSamples = await recordedSamples(
    page,
    "approvalMotionSamples",
  );
  assert(
    approvalSamples.some((sample) => sample.marker === "executed"),
    `executed approval transition was not observed: ${JSON.stringify(
      approvalSamples,
    )}`,
  );
  assert(
    approvalSamples.some((sample) => sample.icon === "success"),
    "success icon feedback was not observed",
  );
  const settledColor = await heading.evaluate(
    (element) => getComputedStyle(element).backgroundColor,
  );
  const headingTransition = await heading.evaluate(
    (element) => getComputedStyle(element).transitionDuration,
  );
  assert(
    pendingColor !== settledColor,
    `approval heading color did not change: ${pendingColor}`,
  );
  assert(
    headingTransition.split(", ").includes("0.24s"),
    `approval heading does not use the 240ms status token: ${headingTransition}`,
  );

  await page.screenshot({
    path: join(artifactDir, "motion-desktop-1280.png"),
    fullPage: true,
  });

  const completedURL = page.url();
  await page
    .getByRole("button", { name: "重新处置", exact: true })
    .click();
  await page.waitForURL((url) => url.href !== completedURL);
  await page
    .getByRole("heading", { name: "改派至川行快运", exact: true })
    .waitFor();
  await page
    .getByRole("button", { name: "驳回方案", exact: true })
    .click();
  await page.getByLabel("驳回原因").fill("专项验证驳回状态反馈");
  await installMotionRecorder(
    page,
    "rejectionMotionSamples",
    '[data-motion-status]:not([data-motion-status="stable"])',
  );
  await page
    .getByRole("button", { name: "确认驳回", exact: true })
    .click();
  await page
    .getByRole("heading", { name: "方案已驳回", exact: true })
    .waitFor();
  const rejectionSamples = await recordedSamples(
    page,
    "rejectionMotionSamples",
  );
  assert(
    rejectionSamples.some(
      (sample) =>
        sample.marker === "rejected" &&
        !isIdentityTransform(sample.transform),
    ),
    `rejected approval did not enter from the side: ${JSON.stringify(
      rejectionSamples,
    )}`,
  );

  await page.emulateMedia({ reducedMotion: "reduce" });
  await installMotionRecorder(
    page,
    "reducedMotionSamples",
    '[data-motion-point="enter"]',
  );
  await pointButtons.first().click();
  await page.waitForTimeout(30);
  const reducedSamples = await recordedSamples(page, "reducedMotionSamples");
  assert(reducedSamples.length > 0, "reduced-motion point update was not observed");
  assert(
    reducedSamples.every((sample) => isIdentityTransform(sample.transform)),
    `reduced motion retained positional movement: ${JSON.stringify(
      reducedSamples,
    )}`,
  );

  for (const width of [375, 320]) {
    await page.setViewportSize({ width, height: 812 });
    await page.waitForTimeout(50);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    assert(!overflow, `motion workbench overflows at ${width}px`);
    await page.screenshot({
      path: join(artifactDir, `motion-mobile-${width}.png`),
      fullPage: true,
    });
  }

  console.log(
    `motion verification passed: timeline=${timelineEntries.length} ` +
      `approval=${approvalSamples.length} rejection=${rejectionSamples.length} ` +
      `reduced=${reducedSamples.length}`,
  );
} finally {
  await browser?.close();
  stopProcesses(processes);
  await rm(dataDir, { recursive: true, force: true });
}

async function installMotionRecorder(page, name, selector) {
  await page.evaluate(
    ({ name: recordName, selector: targetSelector }) => {
      const samples = [];
      window[recordName] = samples;
      const seen = new WeakSet();
      const capture = (element, phase) => {
        if (!element.isConnected) {
          return;
        }
        const style = getComputedStyle(element);
        const highlight = getComputedStyle(element, "::before");
        samples.push({
          phase,
          marker:
            element.getAttribute("data-motion-event") ??
            element.getAttribute("data-motion-status") ??
            element.getAttribute("data-motion-point"),
          icon: element.getAttribute("data-motion-icon"),
          opacity: Number(style.opacity),
          transform: style.transform,
          highlightOpacity: Number(highlight.opacity),
        });
      };
      const register = (element) => {
        if (!(element instanceof HTMLElement) || seen.has(element)) {
          return;
        }
        seen.add(element);
        capture(element, "mutation");
        requestAnimationFrame(() => capture(element, "frame"));
      };
      const scan = (node) => {
        if (!(node instanceof Element)) {
          return;
        }
        if (node.matches(targetSelector)) {
          register(node);
        }
        node.querySelectorAll(targetSelector).forEach(register);
      };
      const observer = new MutationObserver((records) => {
        records.forEach((record) => record.addedNodes.forEach(scan));
      });
      observer.observe(document.body, { childList: true, subtree: true });
    },
    { name, selector },
  );
}

async function recordedSamples(page, name) {
  return page.evaluate((recordName) => window[recordName] ?? [], name);
}

async function motionState(locator) {
  return locator.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      opacity: Number(style.opacity),
      transform: style.transform,
    };
  });
}

function isIdentityTransform(value) {
  return (
    value === "none" ||
    value === "matrix(1, 0, 0, 1, 0, 0)" ||
    value === "matrix3d(1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1)"
  );
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
