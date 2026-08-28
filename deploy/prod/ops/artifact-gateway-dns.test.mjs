import { test } from "node:test";
import * as assert from "node:assert/strict";
import { ensureGatewayWildcard } from "./artifact-gateway-dns.mjs";
import { createVolcengineDnsClient } from "./volcengine-dns.mjs";

test("gateway DNS creates one wildcard record", async () => {
  const calls = [];
  const client = {
    async findZone() { return { ZID: "12" }; },
    async listRecords() { return []; },
    async createRecord(input) { calls.push(input); return { RecordID: "record-1" }; }
  };
  const result = await ensureGatewayWildcard({
    env: {
      CATSCO_ARTIFACT_DNS_ZONE: "catsco.fun",
      CATSCO_ARTIFACT_HOST_SUFFIX: "artifacts.catsco.fun",
      CATSCO_ARTIFACT_GATEWAY_PUBLIC_IP: "203.0.113.10"
    },
    client
  });
  assert.equal(result.status, "created");
  assert.equal(calls[0].host, "*.artifacts");
  assert.equal(calls[0].value, "203.0.113.10");
});

test("Volcengine DNS client accepts a leftmost wildcard record and signs the write", async () => {
  let body = null;
  const client = createVolcengineDnsClient({
    env: { VOLC_ACCESSKEY: "ak", VOLC_SECRETKEY: "sk" },
    fetchImpl: async (_url, options) => {
      body = JSON.parse(options.body);
      return new Response(JSON.stringify({ Result: { RecordID: "record-1" } }), { status: 200 });
    },
    now: () => new Date("2026-08-27T00:00:00Z")
  });
  await client.createRecord({ zoneId: "12", host: "*.artifacts", type: "A", value: "203.0.113.10" });
  assert.equal(body.Host, "*.artifacts");
});
