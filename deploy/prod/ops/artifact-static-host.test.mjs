import { test } from "node:test";
import * as assert from "node:assert/strict";
import fs from "node:fs";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import {
  createDirectHostServer,
  DIRECT_HOST_CONTRACT_VERSION,
  waitForDirectHostHealth
} from "./artifact-static-host.mjs";

async function withServer(run) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-artifact-static-"));
  const server = createDirectHostServer({ root, port: 0 });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  const address = server.address();
  try {
    await run({ root, baseUrl: `http://127.0.0.1:${address.port}` });
  } finally {
    await new Promise(resolve => server.close(resolve));
  }
}

test("worker static host exposes health and Artifact files", async () => {
  await withServer(async ({ root, baseUrl }) => {
    fs.mkdirSync(path.join(root, "demo", "v1"), { recursive: true });
    fs.writeFileSync(path.join(root, "demo", "v1", "index.html"), "<!doctype html><title>ok</title>");
    const health = await fetch(`${baseUrl}/__artifact_health`).then(response => response.json());
    assert.equal(health.contract_version, DIRECT_HOST_CONTRACT_VERSION);
    const page = await fetch(`${baseUrl}/artifacts/demo/v1/`).then(response => response.text());
    assert.match(page, /<title>ok<\/title>/);
  });
});

test("worker static host rejects traversal and does not serve outside the Artifact root", async () => {
  await withServer(async ({ root, baseUrl }) => {
    fs.writeFileSync(path.join(path.dirname(root), "outside.txt"), "secret");
    const response = await rawGet(`${baseUrl}/artifacts/%2e%2e/outside.txt`);
    assert.ok([400, 403, 404].includes(response.status));
    assert.doesNotMatch(response.body, /secret/);
  });
});

test("worker health probe retries startup refusal and then validates the contract", async () => {
  let attempts = 0;
  const health = await waitForDirectHostHealth({
    url: "http://127.0.0.1:19990/__artifact_health",
    port: 19990,
    timeoutMs: 1_000,
    intervalMs: 1,
    delayImpl: async () => {},
    fetchImpl: async () => {
      attempts += 1;
      if (attempts < 3) throw new Error("connection refused");
      return {
        ok: true,
        async json() {
          return { contract_version: DIRECT_HOST_CONTRACT_VERSION, port: 19990 };
        }
      };
    }
  });
  assert.equal(attempts, 3);
  assert.equal(health.port, 19990);
});

test("worker health probe rejects a wrong service contract without retrying", async () => {
  let attempts = 0;
  await assert.rejects(waitForDirectHostHealth({
    url: "http://127.0.0.1:19990/__artifact_health",
    port: 19990,
    timeoutMs: 1_000,
    intervalMs: 1,
    delayImpl: async () => {},
    fetchImpl: async () => {
      attempts += 1;
      return { ok: true, async json() { return { contract_version: "wrong", port: 19990 }; } };
    }
  }), /contract mismatch/);
  assert.equal(attempts, 1);
});

function rawGet(url) {
  return new Promise((resolve, reject) => {
    const request = http.get(url, response => {
      const chunks = [];
      response.on("data", chunk => chunks.push(chunk));
      response.on("end", () => resolve({ status: response.statusCode, body: Buffer.concat(chunks).toString("utf8") }));
    });
    request.on("error", reject);
  });
}
