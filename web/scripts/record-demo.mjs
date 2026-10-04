import { spawn } from "node:child_process";
import { mkdir, mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, extname, join, resolve } from "node:path";
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
const outputPath = resolve(
  process.env.RECORD_OUTPUT ?? join(artifactDir, "waybill-guardian-demo.mp4"),
);
const dataDir = await mkdtemp(join(tmpdir(), "waybill-guardian-recording-"));
const rawVideoDir = join(dataDir, "video");
const backendPort = Number(process.env.RECORD_BACKEND_PORT ?? "18282");
const webPort = Number(process.env.RECORD_WEB_PORT ?? "15276");
const backendURL = `http://127.0.0.1:${backendPort}`;
const webURL = `http://127.0.0.1:${webPort}`;
const processes = [];
let browser = null;
let context = null;

if (extname(outputPath).toLowerCase() !== ".mp4") {
  throw new Error("RECORD_OUTPUT must end with .mp4");
}

try {
  await mkdir(artifactDir, { recursive: true });
  await mkdir(dirname(outputPath), { recursive: true });
  await mkdir(rawVideoDir, { recursive: true });

  const goEnvironment = { ...process.env };
  delete goEnvironment.GOROOT;
  processes.push(
    startProcess("go", ["run", "./cmd/server"], {
      cwd: repoDir,
      env: {
        ...goEnvironment,
        DATA_DIR: dataDir,
        HTTP_ADDR: `127.0.0.1:${backendPort}`,
        DEMO_STEP_DELAY: "120ms",
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
  context = await browser.newContext({
    viewport: { width: 1600, height: 900 },
    recordVideo: {
      dir: rawVideoDir,
      size: { width: 1600, height: 900 },
    },
  });
  const page = await context.newPage();
  const video = page.video();
  await page.goto(webURL, { waitUntil: "networkidle" });
  await page.getByText("精密电子元件", { exact: true }).waitFor();
  await installCaption(page);

  await scene(
    page,
    "Waybill Guardian：异常运单处置 Agent",
    "自动调查异常，所有平台写操作先经过人工审批。",
    3_500,
  );
  await scene(
    page,
    "一屏完成运营决策",
    "运单、风险、异常轨迹、审计时间线和人工决策集中展示。",
    3_500,
  );

  await setCaption(
    page,
    "Agent 开始调查",
    "系统依次查询运单、轨迹、司机和天气，工具调用实时写入审计日志。",
  );
  await page.getByRole("button", { name: "启动处置", exact: true }).click();
  await page.getByText("改派至川行快运", { exact: true }).waitFor();
  await page.getByText("待确认", { exact: true }).waitFor();
  await hold(page, 2_000);

  await scene(
    page,
    "归因证据链",
    "连续驾驶 9 小时，绵阳北服务区停留 6 小时；天气晴、无预警。",
    4_000,
  );
  await scene(
    page,
    "写操作仍未执行",
    "改派、货主通知和司机通知组成一个审批批次，参数、理由和证据都对审批人可见。",
    4_000,
  );

  await setCaption(
    page,
    "人工确认后才恢复执行",
    "服务端先持久化决定，再校验审批范围和 effect 身份，最后调用平台。",
  );
  await page.getByRole("button", { name: "确认并执行", exact: true }).click();
  await page.getByText("方案已执行", { exact: true }).waitFor();
  await page
    .locator('[aria-label="运单状态摘要"]')
    .getByText("处置完成", { exact: true })
    .waitFor();
  await hold(page, 4_000);

  await setCaption(
    page,
    "审计事件可回放",
    "事件包含连续序号和哈希链；SSE 断线后按最后事件序号补齐。",
  );
  await page.getByRole("button", { name: "播放回放", exact: true }).click();
  await hold(page, 5_000);
  await page.getByRole("button", { name: "实时", exact: true }).click();
  await hold(page, 1_500);

  await setCaption(
    page,
    "人工可以驳回方案",
    "驳回必须填写原因，Agent 会读取决定并提交第二个候选运力。",
  );
  await page.getByRole("button", { name: "重新处置", exact: true }).click();
  await page.getByText("改派至川行快运", { exact: true }).waitFor();
  await page.getByRole("button", { name: "驳回方案", exact: true }).click();
  await page
    .getByLabel("驳回原因")
    .fill("首选承运商当前无可用车辆");
  await hold(page, 1_500);
  await page.getByRole("button", { name: "确认驳回", exact: true }).click();
  await page.getByText("改派至蜀道联运", { exact: true }).waitFor();
  await hold(page, 4_000);

  await scene(
    page,
    "第二版方案等待人工决定",
    "超时同样按拒绝处理。系统不会因为无人操作而自动放行。",
    4_000,
  );
  await scene(
    page,
    "异常调查、人工决策、幂等写入、完整审计",
    "默认数据可离线复现，接入真实系统时替换 platform adapter。",
    5_000,
  );
  await hideCaption(page);
  await hold(page, 1_000);

  await context.close();
  context = null;
  const rawVideoPath = await video.path();
  await browser.close();
  browser = null;

  await runProcess("ffmpeg", [
    "-y",
    "-i",
    rawVideoPath,
    "-an",
    "-c:v",
    "libx264",
    "-preset",
    "medium",
    "-crf",
    "20",
    "-pix_fmt",
    "yuv420p",
    "-movflags",
    "+faststart",
    outputPath,
  ]);

  const metadata = await probeVideo(outputPath);
  console.log(
    JSON.stringify(
      {
        output: outputPath,
        duration_seconds: Number(metadata.duration).toFixed(2),
        size_bytes: Number(metadata.size),
        resolution: "1600x900",
        audio: false,
      },
      null,
      2,
    ),
  );
} finally {
  await context?.close();
  await browser?.close();
  stopProcesses(processes);
  await rm(dataDir, { recursive: true, force: true });
}

async function installCaption(page) {
  await page.evaluate(() => {
    const caption = document.createElement("aside");
    caption.id = "demo-caption";
    caption.setAttribute("aria-live", "polite");
    caption.innerHTML = "<strong></strong><span></span>";
    document.body.append(caption);

    const style = document.createElement("style");
    style.textContent = `
      #demo-caption {
        position: fixed;
        z-index: 10000;
        left: 50%;
        bottom: 22px;
        width: min(1040px, calc(100vw - 64px));
        transform: translateX(-50%);
        box-sizing: border-box;
        border: 1px solid rgba(245, 158, 11, 0.75);
        border-left-width: 5px;
        border-radius: 6px;
        background: rgba(17, 24, 31, 0.94);
        box-shadow: 0 14px 36px rgba(0, 0, 0, 0.28);
        color: #f8fafc;
        font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
        padding: 15px 20px 16px;
        pointer-events: none;
        transition: opacity 180ms ease;
      }
      #demo-caption strong {
        display: block;
        color: #fbbf24;
        font-size: 22px;
        font-weight: 750;
        line-height: 1.3;
      }
      #demo-caption span {
        display: block;
        margin-top: 5px;
        font-size: 17px;
        line-height: 1.45;
      }
      #demo-caption[hidden] {
        display: none;
      }
    `;
    document.head.append(style);
  });
}

async function setCaption(page, title, detail) {
  await page.evaluate(
    ({ nextTitle, nextDetail }) => {
      const caption = document.querySelector("#demo-caption");
      if (!(caption instanceof HTMLElement)) {
        throw new Error("demo caption is missing");
      }
      const titleElement = caption.querySelector("strong");
      const detailElement = caption.querySelector("span");
      if (titleElement === null || detailElement === null) {
        throw new Error("demo caption content is missing");
      }
      titleElement.textContent = nextTitle;
      detailElement.textContent = nextDetail;
      caption.hidden = false;
    },
    { nextTitle: title, nextDetail: detail },
  );
}

async function hideCaption(page) {
  await page.evaluate(() => {
    const caption = document.querySelector("#demo-caption");
    if (caption instanceof HTMLElement) {
      caption.hidden = true;
    }
  });
}

async function scene(page, title, detail, duration) {
  await setCaption(page, title, detail);
  await hold(page, duration);
}

async function hold(page, duration) {
  await page.waitForTimeout(duration);
}

function runProcess(command, args) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(command, args, {
      cwd: repoDir,
      stdio: ["ignore", "inherit", "inherit"],
    });
    child.once("error", rejectPromise);
    child.once("exit", (code, signal) => {
      if (code === 0) {
        resolvePromise();
        return;
      }
      rejectPromise(
        new Error(`${command} exited with ${code ?? `signal ${signal}`}`),
      );
    });
  });
}

async function probeVideo(path) {
  const output = [];
  await new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(
      "ffprobe",
      [
        "-v",
        "error",
        "-show_entries",
        "format=duration,size",
        "-of",
        "json",
        path,
      ],
      { stdio: ["ignore", "pipe", "inherit"] },
    );
    child.stdout.on("data", (chunk) => output.push(chunk.toString()));
    child.once("error", rejectPromise);
    child.once("exit", (code, signal) => {
      if (code === 0) {
        resolvePromise();
        return;
      }
      rejectPromise(
        new Error(`ffprobe exited with ${code ?? `signal ${signal}`}`),
      );
    });
  });
  const parsed = JSON.parse(output.join(""));
  return parsed.format;
}
