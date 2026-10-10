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
const webPort = await availablePort(process.env.DELIVERY_WEB_PORT);
const webURL = `http://127.0.0.1:${webPort}`;
const workspacePattern =
  "**/api/delivery/plan-revisions/REV-HZ-1010-07/workspace";
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
      env: process.env,
    },
  );
  processes.push(webProcess);
  await waitForHTTP(webURL, webProcess, processes);

  browser = await chromium.launch({
    executablePath: await findChrome(),
    headless: true,
  });
  const page = await browser.newPage({
    viewport: { width: 1440, height: 1000 },
  });
  await page.route("**/api/**", (route) => route.abort("blockedbyclient"));
  await page.goto(webURL, { waitUntil: "domcontentloaded" });
  const fixture = await page.evaluate(async () => {
    const module = await import("/src/delivery/test-fixture.ts");
    return module.deliveryWorkspaceFixture();
  });
  await page.unroute("**/api/**");
  await page.route(workspacePattern, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(fixture),
    }),
  );

  await page.goto(`${webURL}/delivery/plans/REV-HZ-1010-07`, {
    waitUntil: "networkidle",
  });
  await page
    .getByRole("heading", { name: "杭州城市配送计划", exact: true })
    .waitFor();
  await page.getByText("Validator 通过", { exact: true }).waitFor();
  await page.getByText("确认并执行", { exact: true }).waitFor();

  const cargoStage = page.locator('[data-cargo-renderer="webgl"]');
  await cargoStage.waitFor();
  const cargoCanvas = cargoStage.locator("canvas");
  await cargoCanvas.waitFor();
  await page.waitForFunction(() => {
    const canvas = document.querySelector(
      '[data-cargo-renderer="webgl"] canvas',
    );
    return canvas?.getAttribute("data-scene-ready") === "true";
  });
  assert(
    (await cargoStage.getAttribute("data-cargo-count")) === "8",
    "initial load stage did not contain eight cargo placements",
  );
  await cargoCanvas.screenshot({
    path: join(artifactDir, "delivery-cargo-canvas.png"),
  });
  const pixelProbe = await probeCanvasPixels(cargoCanvas);
  assert(
    pixelProbe !== null &&
      pixelProbe.width > 0 &&
      pixelProbe.height > 0 &&
      pixelProbe.colors >= 3,
    `cargo canvas was blank: ${JSON.stringify(pixelProbe)}`,
  );

  await page.getByRole("button", { name: "下一个站点" }).click();
  await page.getByText("萧山产业园", { exact: true }).last().waitFor();
  await page.getByText("6 件在舱", { exact: true }).waitFor();
  assert(
    (await cargoStage.getAttribute("data-cargo-count")) === "6",
    "route playback did not update the load stage",
  );

  const playback = page.getByRole("region", {
    name: "路线与装载联合回放",
  });
  await playback.focus();
  await playback.press("ArrowRight");
  await page.getByText("4 件在舱", { exact: true }).waitFor();
  await page.getByText("绍兴柯桥仓", { exact: true }).last().waitFor();

  const decisionButtons = page.locator(
    'section[aria-labelledby="decision-title"] button',
  );
  const decisionButtonSizes = await decisionButtons.evaluateAll((buttons) =>
    buttons.map((button) => {
      const rect = button.getBoundingClientRect();
      return { width: rect.width, height: rect.height };
    }),
  );
  assert(
    decisionButtonSizes.every(
      (size) => size.width >= 40 && size.height >= 40,
    ),
    `approval buttons missed the 40px target: ${JSON.stringify(decisionButtonSizes)}`,
  );

  await page.screenshot({
    path: join(artifactDir, "delivery-console-1440.png"),
    fullPage: true,
  });

  await cargoCanvas.evaluate((canvas) => {
    canvas.dispatchEvent(
      new Event("webglcontextlost", {
        bubbles: false,
        cancelable: true,
      }),
    );
  });
  const fallback = page.locator('[data-cargo-renderer="svg"]');
  await fallback.waitFor();
  assert(
    (await fallback.getAttribute("data-cargo-count")) === "4",
    "SVG fallback did not retain the selected load stage",
  );
  assert(
    (await fallback.locator('g[role="button"]').count()) === 4,
    "SVG fallback did not expose every cargo item to the keyboard",
  );

  await page.setViewportSize({ width: 375, height: 812 });
  await page.waitForTimeout(100);
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  assert(overflow <= 1, `375px layout overflowed by ${overflow}px`);
  await page.screenshot({
    path: join(artifactDir, "delivery-console-375.png"),
    fullPage: true,
  });

  await page.unroute(workspacePattern);
  await page.route(workspacePattern, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ schema_version: "delivery.workspace.v0" }),
    }),
  );
  await page.reload({ waitUntil: "networkidle" });
  await page
    .getByRole("heading", { name: "响应格式错误", exact: true })
    .waitFor();

  console.log(
    JSON.stringify(
      {
        ok: true,
        canvas: pixelProbe,
        desktop: join(artifactDir, "delivery-console-1440.png"),
        mobile: join(artifactDir, "delivery-console-375.png"),
      },
      null,
      2,
    ),
  );
} finally {
  if (browser !== null) {
    await browser.close();
  }
  stopProcesses(processes);
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

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
