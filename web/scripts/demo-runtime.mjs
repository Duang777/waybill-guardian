import { spawn } from "node:child_process";
import { access } from "node:fs/promises";
import { createServer } from "node:net";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

export const webDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
export const repoDir = resolve(webDir, "..");
const processStartupTimeoutMS = 60_000;

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
  let spawnError = null;
  child.on("error", (error) => {
    spawnError = error;
    collect(error);
  });
  child.diagnosticOutput = () => output.join("");
  child.spawnError = () => spawnError;
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

export async function waitForHTTP(
  url,
  process,
  diagnostics = [process],
  { timeoutMilliseconds = processStartupTimeoutMS } = {},
) {
  const deadline = Date.now() + timeoutMilliseconds;
  while (Date.now() < deadline) {
    const failure = processFailure(process);
    if (failure !== null) {
      throw new Error(`${failure}\n${diagnosticOutput(diagnostics)}`);
    }
    try {
      const response = await fetch(url);
      if (response.ok) {
        const responseFailure = processFailure(process);
        if (responseFailure !== null) {
          throw new Error(
            `${responseFailure}\n${diagnosticOutput(diagnostics)}`,
          );
        }
        return;
      }
    } catch {
      // The process may still be compiling or binding its port.
    }
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 100));
  }
  throw new Error(`timed out waiting for ${url}\n${diagnosticOutput(diagnostics)}`);
}

export async function availablePort(configuredValue) {
  if (configuredValue !== undefined) {
    const configured = Number(configuredValue);
    if (!Number.isInteger(configured) || configured < 1 || configured > 65_535) {
      throw new Error(`invalid configured port: ${configuredValue}`);
    }
    return configured;
  }
  return new Promise((resolvePort, rejectPort) => {
    const server = createServer();
    server.unref();
    server.once("error", rejectPort);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (address === null || typeof address === "string") {
        server.close();
        rejectPort(new Error("failed to allocate an ephemeral TCP port"));
        return;
      }
      server.close((error) => {
        if (error === undefined) {
          resolvePort(address.port);
        } else {
          rejectPort(error);
        }
      });
    });
  });
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

function processFailure(child) {
  const spawnError = child.spawnError();
  if (spawnError !== null) {
    return `failed to start child process: ${spawnError.message}`;
  }
  if (child.exitCode !== null) {
    return `child process exited during startup with status ${child.exitCode}`;
  }
  if (child.signalCode !== null) {
    return `child process exited during startup from signal ${child.signalCode}`;
  }
  return null;
}

function diagnosticOutput(processes) {
  return processes.map((child) => child.diagnosticOutput()).join("\n");
}
