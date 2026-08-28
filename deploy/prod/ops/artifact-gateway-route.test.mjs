import { test } from "node:test";
import * as assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import {
  ROUTE_REGISTRY_CONTRACT,
  readRoute,
  registerRoute,
  removeRoute,
  syncRoutes
} from "./artifact-gateway-route.mjs";

function sandbox() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-artifact-routes-"));
  return {
    root,
    options: {
      registry: path.join(root, "routes.json"),
      nginxRoutes: path.join(root, "routes.conf"),
      hostSuffix: "artifacts.catsco.fun",
      backendPort: 19990,
      skipReload: true
    }
  };
}

test("register is idempotent and renders a Host-to-private-IP map", () => {
  const sb = sandbox();
  const first = registerRoute({ agentUid: "407", privateIp: "192.168.2.3" }, sb.options);
  const second = registerRoute({ agentUid: "407", privateIp: "192.168.2.3" }, sb.options);
  assert.equal(first.status, "registered");
  assert.equal(second.status, "unchanged");
  const registry = JSON.parse(fs.readFileSync(sb.options.registry, "utf8"));
  assert.equal(registry.contract_version, ROUTE_REGISTRY_CONTRACT);
  assert.equal(registry.routes["407"].private_ip, "192.168.2.3");
  const nginx = fs.readFileSync(sb.options.nginxRoutes, "utf8");
  assert.match(nginx, /agent-407\.artifacts\.catsco\.fun 192\.168\.2\.3:19990;/);
});

test("register atomically replaces a worker private IP", () => {
  const sb = sandbox();
  registerRoute({ agentUid: "407", privateIp: "192.168.2.3" }, sb.options);
  registerRoute({ agentUid: "407", privateIp: "192.168.2.19" }, sb.options);
  assert.equal(readRoute({ agentUid: "407" }, sb.options).private_ip, "192.168.2.19");
  assert.doesNotMatch(fs.readFileSync(sb.options.nginxRoutes, "utf8"), /192\.168\.2\.3:19990/);
});

test("remove is idempotent and removes only the requested Agent route", () => {
  const sb = sandbox();
  syncRoutes({ "407": "192.168.2.3", "535": "10.0.0.8" }, sb.options);
  assert.equal(removeRoute({ agentUid: "407" }, sb.options).status, "removed");
  assert.equal(removeRoute({ agentUid: "407" }, sb.options).status, "not-found");
  assert.equal(readRoute({ agentUid: "535" }, sb.options).status, "registered");
});

test("sync replaces stale routes and rejects unsafe identity or public backends", () => {
  const sb = sandbox();
  registerRoute({ agentUid: "407", privateIp: "192.168.2.3" }, sb.options);
  const result = syncRoutes({ routes: { "535": { private_ip: "172.16.1.4" } } }, sb.options);
  assert.equal(result.route_count, 1);
  assert.equal(readRoute({ agentUid: "407" }, sb.options).status, "not-found");
  assert.throws(() => registerRoute({ agentUid: "worker-407", privateIp: "192.168.2.3" }, sb.options));
  assert.throws(() => registerRoute({ agentUid: "407", privateIp: "8.8.8.8" }, sb.options));
});
