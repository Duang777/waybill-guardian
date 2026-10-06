import { createServer } from "node:http";
import { afterEach, describe, expect, it } from "vitest";
import { waitForHTTP } from "./demo-runtime.mjs";

const servers = [];

afterEach(async () => {
  await Promise.all(
    servers.splice(0).map(
      (server) =>
        new Promise((resolve, reject) => {
          server.close((error) => {
            if (error === undefined) {
              resolve();
            } else {
              reject(error);
            }
          });
        }),
    ),
  );
});

describe("waitForHTTP", () => {
  it("uses HTTP readiness even when process output contains ANSI codes", async () => {
    const server = createServer((_, response) => {
      response.writeHead(200).end("ok");
    });
    servers.push(server);
    await new Promise((resolve) => {
      server.listen(0, "127.0.0.1", resolve);
    });
    const address = server.address();
    if (address === null || typeof address === "string") {
      throw new Error("test server did not expose a TCP port");
    }
    const process = {
      exitCode: null,
      signalCode: null,
      spawnError: () => null,
      diagnosticOutput: () => "\u001b[1mLocal\u001b[22m:\u001b[39m",
    };

    await expect(
      waitForHTTP(
        `http://127.0.0.1:${address.port}`,
        process,
        [process],
        { timeoutMilliseconds: 500 },
      ),
    ).resolves.toBeUndefined();
  });
});
