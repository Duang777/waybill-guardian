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
const revisionID = "REV-HZ-1010-07";
const deliveryURL = `${webURL}/delivery/plans/${revisionID}`;
const workspacePattern =
  `**/api/delivery/plan-revisions/${revisionID}/workspace`;
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
  const browserErrors = [];
  page.on("pageerror", (error) => browserErrors.push(error.message));
  page.on("console", (message) => {
    if (message.type() === "error") {
      browserErrors.push(message.text());
    }
  });
  await installDeliveryEventSource(page);
  await page.route("**/api/**", (route) => route.abort("blockedbyclient"));
  await page.goto(webURL, { waitUntil: "domcontentloaded" });
  const fixtures = await page.evaluate(async () => {
    const module = await import("/src/delivery/test-fixture.ts");
    return {
      normal: module.deliveryWorkspaceStateFixture("normal"),
      empty: module.deliveryWorkspaceStateFixture("empty"),
      failed: module.deliveryWorkspaceStateFixture("failed"),
      expired: module.deliveryWorkspaceStateFixture("expired"),
      stale: module.deliveryWorkspaceStateFixture("stale"),
      rejected: module.deliveryWorkspaceStateFixture("rejected"),
      approved: module.deliveryWorkspaceStateFixture("approved"),
      partial: module.deliveryWorkspaceStateFixture("partial"),
      reconciliation:
        module.deliveryWorkspaceStateFixture("reconciliation"),
      large: module.deliveryLargeWorkspaceFixture(300),
    };
  });
  await page.unroute("**/api/**");

  let currentFixture = fixtures.normal;
  let workspaceRequests = 0;
  await page.route(workspacePattern, (route) => {
    workspaceRequests += 1;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(currentFixture),
    });
  });
  await page.route("**/api/delivery/approvals/**", (route) =>
    route.fulfill({ status: 204 }),
  );
  browserErrors.length = 0;

  const loadState = async (state) => {
    currentFixture = fixtures[state];
    await page.goto(deliveryURL, { waitUntil: "networkidle" });
    await page
      .getByRole("heading", { name: "城市配送计划", exact: true })
      .waitFor();
  };

  await loadState("normal");
  await page.getByText("Validator 通过", { exact: true }).waitFor();
  await page.getByText("实时流在线", { exact: true }).waitFor();
  await page.getByText("确认并执行", { exact: true }).waitFor();
  await page.getByText("计划修订差异", { exact: true }).waitFor();
  await page.getByText("REV-HZ-1010-06", { exact: true }).waitFor();

  const initialRequestCount = workspaceRequests;
  await emitDeliveryEvent(page, {
    schema_version: "delivery.workspace-event.v1",
    event_id: "DELIVERY-EVENT-5",
    seq: 5,
    revision_id: revisionID,
    workspace_version: 5,
    occurred_at: "2026-10-10T07:45:00Z",
    kind: "approval_changed",
  });
  await page.getByText("实时流重连中", { exact: true }).waitFor();
  assert(
    workspaceRequests === initialRequestCount + 1,
    "stale workspace snapshot did not preserve the SSE target cursor",
  );
  currentFixture = fixtures.approved;
  await page
    .getByRole("heading", { name: "计划已确认", exact: true })
    .waitFor();
  assert(
    workspaceRequests === initialRequestCount + 2,
    "workspace did not retry after the read replica caught up",
  );
  await emitDeliveryEvent(page, {
    schema_version: "delivery.workspace-event.v1",
    event_id: "DELIVERY-EVENT-5-REPLAY",
    seq: 5,
    revision_id: revisionID,
    workspace_version: 5,
    occurred_at: "2026-10-10T07:45:00Z",
    kind: "approval_changed",
  });
  await page.waitForTimeout(120);
  assert(
    workspaceRequests === initialRequestCount + 2,
    "replayed SSE event was not deduplicated by cursor",
  );

  await loadState("normal");
  await failDeliveryStream(page);
  await page.getByText("实时流重连中", { exact: true }).waitFor();
  assert(
    await page.getByRole("button", { name: "确认并执行" }).isDisabled(),
    "approval remained enabled while SSE was reconnecting",
  );
  await reopenDeliveryStream(page);
  await page.getByText("实时流在线", { exact: true }).waitFor();
  assert(
    await page.getByRole("button", { name: "确认并执行" }).isEnabled(),
    "approval did not unlock after SSE recovered",
  );

  const cargoStage = page.locator('[data-cargo-renderer="webgl"]');
  await cargoStage.waitFor();
  const cargoCanvas = cargoStage.locator("canvas");
  await waitForCargoCanvas(page, cargoCanvas);
  assert(
    (await cargoStage.getAttribute("data-cargo-count")) === "8",
    "initial load stage did not contain eight cargo placements",
  );
  assert(
    (await cargoStage.getAttribute("data-cargo-mode")) === "individual",
    "small load stage unexpectedly used instancing",
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
  await page.getByRole("button", { name: "向右旋转车厢" }).click();
  await page.waitForTimeout(60);
  const rotatedPixelProbe = await probeCanvasPixels(cargoCanvas);
  assert(
    rotatedPixelProbe !== null &&
      rotatedPixelProbe.signature !== pixelProbe.signature,
    "camera rotation did not produce a new rendered frame",
  );
  const cameraButtonSizes = await page
    .getByRole("toolbar", { name: "3D 装载视角" })
    .getByRole("button")
    .evaluateAll((buttons) =>
      buttons.map((button) => {
        const rect = button.getBoundingClientRect();
        return { width: rect.width, height: rect.height };
      }),
    );
  assert(
    cameraButtonSizes.every(
      (size) => size.width >= 40 && size.height >= 40,
    ),
    `camera controls missed the 40px target: ${JSON.stringify(cameraButtonSizes)}`,
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

  const stateCases = [
    ["normal", "计划等待人工确认"],
    ["empty", "车厢已清空"],
    ["failed", "尚未发起审批"],
    ["expired", "审批已过期"],
    ["stale", "计划事实已过期"],
    ["rejected", "计划已驳回"],
    ["approved", "计划已确认"],
    ["partial", "部分写入"],
    ["reconciliation", "等待对账"],
  ];
  const viewports = [
    { name: "desktop", width: 1440, height: 1000 },
    { name: "mobile", width: 375, height: 812 },
  ];
  const matrix = [];
  for (const viewport of viewports) {
    await page.setViewportSize({
      width: viewport.width,
      height: viewport.height,
    });
    for (const [state, expectedText] of stateCases) {
      await loadState(state);
      await page.getByText(expectedText, { exact: true }).first().waitFor();
      await assertNoHorizontalOverflow(page, `${viewport.name}/${state}`);
      matrix.push(`${viewport.name}/${state}`);
      if (state === "normal") {
        await page.screenshot({
          path: join(
            artifactDir,
            viewport.name === "desktop"
              ? "delivery-console-1440.png"
              : "delivery-console-375.png",
          ),
          fullPage: true,
        });
      }
    }

    await loadState("normal");
    const fallbackSource = page.locator(
      '[data-cargo-renderer="webgl"] canvas',
    );
    await waitForCargoCanvas(page, fallbackSource);
    await fallbackSource.evaluate((canvas) => {
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
      (await fallback.getAttribute("data-cargo-count")) === "8",
      `${viewport.name} SVG fallback lost cargo placements`,
    );
    assert(
      (await fallback.locator('g[role="button"]').count()) === 16,
      `${viewport.name} SVG fallback did not expose both cargo projections`,
    );
    await fallback
      .getByRole("img", { name: /车厢侧视图/ })
      .waitFor();
    await assertNoHorizontalOverflow(page, `${viewport.name}/fallback`);
    matrix.push(`${viewport.name}/fallback`);
  }

  await page.setViewportSize({ width: 1440, height: 1000 });
  await loadState("large");
  const largeStage = page.locator('[data-cargo-renderer="webgl"]');
  await largeStage.waitFor();
  const largeCanvas = largeStage.locator("canvas");
  await waitForCargoCanvas(page, largeCanvas);
  assert(
    (await largeStage.getAttribute("data-cargo-count")) === "300",
    "large fixture did not render 300 placements",
  );
  assert(
    (await largeStage.getAttribute("data-cargo-mode")) === "instanced",
    "300 placements did not switch to InstancedMesh",
  );
  const largePixelProbe = await probeCanvasPixels(largeCanvas);
  assert(
    largePixelProbe !== null &&
      largePixelProbe.colors >= 8 &&
      largePixelProbe.chromaticPixels >= 3,
    `300-placement canvas was blank: ${JSON.stringify(largePixelProbe)}`,
  );
  await largeCanvas.screenshot({
    path: join(artifactDir, "delivery-cargo-300.png"),
  });
  for (let rotation = 0; rotation < 18; rotation += 1) {
    await page.getByRole("button", { name: "向右旋转车厢" }).click();
  }
  await page.waitForTimeout(80);
  const rotatedLargeProbe = await probeCanvasPixels(largeCanvas);
  assert(
    rotatedLargeProbe !== null &&
      rotatedLargeProbe.signature !== largePixelProbe.signature &&
      rotatedLargeProbe.chromaticPixels >= 3,
    `continuous 300-placement rotation failed: ${JSON.stringify(rotatedLargeProbe)}`,
  );
  await page.getByRole("button", { name: "下一个站点" }).click();
  await page.getByText("200 件在舱", { exact: true }).waitFor();
  assert(
    (await largeStage.getAttribute("data-cargo-mode")) === "instanced",
    "large route playback left instanced rendering mode",
  );
  const playedLargeProbe = await probeCanvasPixels(largeCanvas);
  assert(
    playedLargeProbe !== null && playedLargeProbe.chromaticPixels >= 3,
    `300-placement playback produced a blank frame: ${JSON.stringify(playedLargeProbe)}`,
  );
  await page.getByRole("button", { name: "上一个站点" }).click();
  await page.getByText("300 件在舱", { exact: true }).waitFor();

  const cdp = await page.context().newCDPSession(page);
  await cdp.send("HeapProfiler.enable");
  const mountSamples = [await collectDOMCounters(cdp)];
  for (let cycle = 0; cycle < 20; cycle += 1) {
    await page.goto(`${webURL}/invalid-delivery-route`, {
      waitUntil: "networkidle",
    });
    assert(
      (await page.locator('[data-cargo-renderer="webgl"]').count()) === 0,
      `cargo canvas survived unmount cycle ${cycle + 1}`,
    );
    await page.goto(deliveryURL, { waitUntil: "networkidle" });
    const mountedStage = page.locator('[data-cargo-renderer="webgl"]');
    await mountedStage.waitFor();
    assert(
      (await mountedStage.getAttribute("data-cargo-mode")) === "instanced",
      `instanced cargo did not remount in cycle ${cycle + 1}`,
    );
    assert(
      (await page.locator('[data-cargo-renderer="webgl"] canvas').count()) === 1,
      `mount cycle ${cycle + 1} retained duplicate canvases`,
    );
    mountSamples.push(await collectDOMCounters(cdp));
  }
  assert(
    counterSpread(mountSamples, "documents") <= 1,
    `document count grew across mount cycles: ${JSON.stringify(mountSamples)}`,
  );
  assert(
    counterSpread(mountSamples, "nodes") <= 250,
    `DOM nodes grew across mount cycles: ${JSON.stringify(mountSamples)}`,
  );
  assert(
    counterSpread(mountSamples, "jsEventListeners") <= 40,
    `event listeners grew across mount cycles: ${JSON.stringify(mountSamples)}`,
  );

  currentFixture = { schema_version: "delivery.workspace.v0" };
  await page.goto(deliveryURL, { waitUntil: "networkidle" });
  await page
    .getByRole("heading", { name: "响应格式错误", exact: true })
    .waitFor();
  assert(
    browserErrors.length === 0,
    `browser emitted errors: ${JSON.stringify(browserErrors)}`,
  );

  console.log(
    JSON.stringify(
      {
        ok: true,
        canvas: pixelProbe,
        largeCanvas: largePixelProbe,
        stateMatrix: matrix,
        mountSamples,
        desktop: join(artifactDir, "delivery-console-1440.png"),
        mobile: join(artifactDir, "delivery-console-375.png"),
        capacity: join(artifactDir, "delivery-cargo-300.png"),
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

async function installDeliveryEventSource(page) {
  await page.addInitScript(() => {
    const sources = [];

    class DeliveryTestEventSource extends EventTarget {
      static CONNECTING = 0;
      static OPEN = 1;
      static CLOSED = 2;

      CONNECTING = 0;
      OPEN = 1;
      CLOSED = 2;
      readyState = 0;
      withCredentials = false;
      onopen = null;
      onerror = null;
      onmessage = null;
      closed = false;

      constructor(url) {
        super();
        this.url = String(url);
        sources.push(this);
        queueMicrotask(() => this.reopen());
      }

      close() {
        this.closed = true;
        this.readyState = this.CLOSED;
      }

      reopen() {
        if (this.closed) {
          return;
        }
        this.readyState = this.OPEN;
        this.onopen?.(new Event("open"));
      }

      fail() {
        if (this.closed) {
          return;
        }
        this.readyState = this.CONNECTING;
        this.onerror?.(new Event("error"));
      }

      emit(payload) {
        if (this.closed) {
          return;
        }
        this.dispatchEvent(
          new MessageEvent(payload.kind, {
            data: JSON.stringify(payload),
            lastEventId: String(payload.seq),
          }),
        );
      }
    }

    const activeSource = () =>
      [...sources].reverse().find((source) => !source.closed);
    Object.defineProperty(window, "EventSource", {
      configurable: true,
      value: DeliveryTestEventSource,
    });
    Object.defineProperty(window, "__deliveryStream", {
      configurable: true,
      value: {
        emit(payload) {
          activeSource()?.emit(payload);
        },
        fail() {
          activeSource()?.fail();
        },
        reopen() {
          activeSource()?.reopen();
        },
      },
    });
  });
}

async function emitDeliveryEvent(page, event) {
  await page.evaluate((payload) => {
    window.__deliveryStream.emit(payload);
  }, event);
}

async function failDeliveryStream(page) {
  await page.evaluate(() => {
    window.__deliveryStream.fail();
  });
}

async function reopenDeliveryStream(page) {
  await page.evaluate(() => {
    window.__deliveryStream.reopen();
  });
}

async function waitForCargoCanvas(page, canvas) {
  await canvas.waitFor();
  await page.waitForFunction(() => {
    const element = document.querySelector(
      '[data-cargo-renderer="webgl"] canvas',
    );
    return element?.getAttribute("data-scene-ready") === "true";
  });
}

async function assertNoHorizontalOverflow(page, label) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  assert(overflow <= 1, `${label} overflowed horizontally by ${overflow}px`);
}

async function collectDOMCounters(cdp) {
  await cdp.send("HeapProfiler.collectGarbage");
  return cdp.send("Memory.getDOMCounters");
}

function counterSpread(samples, key) {
  const values = samples.map((sample) => sample[key]);
  return Math.max(...values) - Math.min(...values);
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
    let chromaticPixels = 0;
    let signature = 2_166_136_261;
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
        if (
          Math.max(pixel[0], pixel[1], pixel[2]) -
            Math.min(pixel[0], pixel[1], pixel[2]) >=
          24
        ) {
          chromaticPixels += 1;
        }
        for (const channel of pixel) {
          signature ^= channel;
          signature = Math.imul(signature, 16_777_619);
        }
        samples.push([...pixel].join(","));
      }
    }
    return {
      width: gl.drawingBufferWidth,
      height: gl.drawingBufferHeight,
      colors: new Set(samples).size,
      chromaticPixels,
      signature: signature >>> 0,
    };
  });
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}
