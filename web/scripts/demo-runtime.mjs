import { spawn } from "node:child_process";
import { access } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const webDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
export const repoDir = resolve(webDir, "..");

export function startProcess(command, args, options) {
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

export function stopProcesses(processes) {
  for (const child of processes.reverse()) {
    if (child.pid === undefined || child.exitCode !== null) {
      continue;
    }
    try {
      process.kill(-child.pid, "SIGTERM");
    } catch (error) {
      if (error.code !== "ESRCH") {
        throw error;
      }
    }
  }
}

export async function waitForHTTP(url, processes) {
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

export async function findChrome() {
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
