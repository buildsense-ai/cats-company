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

const secret = "test-artifact-origin-secret-0123456789";
const trustedHeaders = { "X-Catsco-Artifact-Origin-Secret": secret, Authorization: "Bearer reader" };

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return `http://127.0.0.1:${server.address().port}`;
}

async function close(server) {
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}

async function withServer(run, options = {}) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "catsco-artifact-static-"));
  const calls = [];
  const state = { denied: false };
  const auth = http.createServer((request, response) => {
    calls.push({ url: request.url, headers: request.headers });
    if (options.authorize) return options.authorize(request, response, state);
    const authenticated = request.headers.authorization === "Bearer reader"
      || request.headers.cookie === "session=reader";
    response.writeHead(authenticated && !state.denied ? 204 : 401).end();
  });
  const authBase = await listen(auth);
  const server = createDirectHostServer({
    root, port: 0, originSecret: secret, publicOrigin: "https://artifact.example", authorizationUrl: `${authBase}/authorize`, ...options.host
  });
  const baseUrl = await listen(server);
  const request = (pathname, init = {}) => fetch(`${baseUrl}${pathname}`, {
    ...init, headers: { ...trustedHeaders, ...init.headers }
  });
  try {
    await run({ root, baseUrl, request, calls, state });
  } finally {
    await close(server);
    await close(auth);
    fs.rmSync(root, { recursive: true, force: true });
  }
}

function fixture(root) {
  fs.mkdirSync(path.join(root, "demo", "v1"), { recursive: true });
  fs.writeFileSync(path.join(root, "demo", "v1", "index.html"), "<!doctype html><title>ok</title>");
  fs.writeFileSync(path.join(root, "demo", "v1", "app.js"), "private-script");
  fs.writeFileSync(path.join(root, "demo", "v1", "style.css"), "private-style");
  fs.writeFileSync(path.join(root, "demo", "v1", "data.json"), '{"private":true}');
  fs.writeFileSync(path.join(root, "artifacts-index.json"), '{"private":"all artifacts"}');
}

test("health is public; every file type, HEAD and Range requires a fresh callback", async () => {
  await withServer(async ({ root, baseUrl, request, calls, state }) => {
    fixture(root);
    const health = await fetch(`${baseUrl}/__artifact_health`).then(response => response.json());
    assert.equal(health.contract_version, DIRECT_HOST_CONTRACT_VERSION);
    assert.equal(health.privacy_enforced, true);
    assert.equal(health.authorization_configured, true);
    for (const pathname of ["/artifacts/demo/v1/", "/artifacts/demo/v1/app.js", "/artifacts/demo/v1/style.css", "/artifacts/demo/v1/data.json"]) {
      const response = await request(pathname);
      assert.equal(response.status, 200);
      assert.equal(response.headers.get("cache-control"), "no-store");
      await response.arrayBuffer();
    }
    const head = await request("/artifacts/demo/v1/app.js", { method: "HEAD" });
    assert.equal(head.status, 200);
    assert.equal(await head.text(), "");
    const partial = await request("/artifacts/demo/v1/app.js", { headers: { Range: "bytes=0-6" } });
    assert.equal(partial.status, 206);
    assert.equal(await partial.text(), "private");
    assert.equal(calls.length, 6);
    state.denied = true;
    assert.equal((await request("/artifacts/demo/v1/app.js")).status, 404);
    assert.equal(calls.length, 7);
  });
});

test("origin secret and authenticated actor are independently required for every method and subresource", async () => {
  await withServer(async ({ root, baseUrl, request, calls }) => {
    fixture(root);
    for (const init of [{}, { method: "HEAD" }, { headers: { Range: "bytes=0-1" } }]) {
      assert.equal((await fetch(`${baseUrl}/artifacts/demo/v1/`, init)).status, 404);
      const response = await request("/artifacts/demo/v1/app.js", {
        ...init, headers: { ...init.headers, Authorization: "" }
      });
      assert.equal(response.status, 404);
      assert.doesNotMatch(await response.text(), /private-script/);
    }
    assert.equal(calls.length, 3, "missing origin secrets never reach the callback");
    const cookie = await request("/artifacts/demo/v1/", { headers: { Authorization: "", Cookie: "session=reader" } });
    assert.equal(cookie.status, 200);
  });
});

test("aggregate indexes and directory listings remain closed even when callback returns success", async () => {
  await withServer(async ({ root, request, calls }) => {
    fixture(root);
    fs.mkdirSync(path.join(root, "by-agent", "42"), { recursive: true });
    fs.writeFileSync(path.join(root, "by-agent", "42", "artifacts-index.json"), "all artifacts");
    for (const pathname of ["/artifacts", "/artifacts/", "/artifacts/artifacts-index.json", "/artifacts/by-agent/42/artifacts-index.json"]) {
      for (const method of ["GET", "HEAD"]) {
        assert.equal((await request(pathname, { method })).status, 404);
      }
    }
    assert.equal(calls.length, 6);
  });
});

test("callbacks receive only fixed-target credentials and a canonical path, never caller identity headers", async () => {
  await withServer(async ({ root, request, calls }) => {
    fixture(root);
    const response = await request("/artifacts/demo/v1/?message_id=18&url=https://attacker.invalid", {
      headers: { "X-Actor-UID": "999", "X-Forwarded-User": "999", Cookie: "session=reader" }
    });
    assert.equal(response.status, 200);
    const callback = new URL(calls[0].url, "http://localhost");
    assert.equal(callback.pathname, "/authorize");
    assert.equal(callback.searchParams.get("origin"), "https://artifact.example");
    assert.equal(callback.searchParams.get("path"), "/artifacts/demo/v1/");
    assert.equal(callback.searchParams.get("message_id"), "18");
    assert.equal(callback.searchParams.has("url"), false);
    assert.equal(calls[0].headers["x-actor-uid"], undefined);
    assert.equal(calls[0].headers["x-forwarded-user"], undefined);
    assert.equal(calls[0].headers.authorization, "Bearer reader");
  });
});

test("callback redirects never forward cookies, Authorization or the origin secret", async () => {
  let stolen = 0;
  const attacker = http.createServer((request, response) => { stolen += 1; response.writeHead(204).end(); });
  const attackerBase = await listen(attacker);
  try {
    await withServer(async ({ root, request }) => {
      fixture(root);
      assert.equal((await request("/artifacts/demo/v1/", { headers: { Cookie: "session=reader" } })).status, 503);
      assert.equal(stolen, 0);
    }, { authorize: (_request, response) => response.writeHead(302, { Location: attackerBase }).end() });
  } finally {
    await close(attacker);
  }
});

test("unconfigured auth, callback outage, and a login HTML response all fail closed", async () => {
  for (const host of [
    { originSecret: "" },
    { authorizationUrl: "" },
    { publicOrigin: "" },
    { fetchImpl: async () => { throw new Error("offline"); } },
    { fetchImpl: async () => new Response("login", { status: 200 }) }
  ]) {
    await withServer(async ({ root, request }) => {
      fixture(root);
      assert.ok([404, 503].includes((await request("/artifacts/demo/v1/")).status));
    }, { host });
  }
  assert.throws(() => createDirectHostServer({ root: ".", originSecret: secret, authorizationUrl: "http://external.invalid/auth" }), /fixed HTTPS/);
});

test("traversal, double encoding, malformed escapes and cross-artifact junctions cannot expose files", async t => {
  await withServer(async ({ root, baseUrl, request }) => {
    fixture(root);
    for (const pathname of ["/artifacts/demo/%2e%2e/private/data.json", "/artifacts/demo/%252e%252e/data.json", "/artifacts/demo/%5cprivate", "/artifacts/demo/%zz"]) {
      const response = await rawGet(`${baseUrl}${pathname}`, trustedHeaders);
      assert.ok([400, 403, 404].includes(response.status));
      assert.doesNotMatch(response.body, /private-script/);
    }
    fs.mkdirSync(path.join(root, "private"));
    fs.writeFileSync(path.join(root, "private", "secret.json"), "other-artifact-secret");
    try {
      fs.symlinkSync(path.join(root, "private"), path.join(root, "demo", "v1", "cross"), "junction");
      fs.symlinkSync(path.join(root, "private"), path.join(root, "alias"), "junction");
    } catch (error) {
      if (error.code === "EPERM") { t.diagnostic("junction checks unavailable on this filesystem"); return; }
      throw error;
    }
    for (const pathname of ["/artifacts/demo/v1/cross/secret.json", "/artifacts/alias/secret.json"]) {
      const response = await request(pathname);
      assert.equal(response.status, 404);
      assert.doesNotMatch(await response.text(), /other-artifact-secret/);
    }
  });
});

test("agent-scoped historical URLs authorize entry documents and nested resources", async () => {
  await withServer(async ({ root, request }) => {
    fs.mkdirSync(path.join(root, "by-agent", "42", "demo", "v1"), { recursive: true });
    fs.writeFileSync(path.join(root, "by-agent", "42", "demo", "v1", "index.html"), "agent page");
    assert.equal((await request("/artifacts/by-agent/42/demo/v1/")).status, 200);
  });
});

test("worker health probe retries startup refusal and validates the contract", async () => {
  let attempts = 0;
  const health = await waitForDirectHostHealth({
    url: "http://127.0.0.1:19990/__artifact_health", port: 19990, timeoutMs: 1_000, intervalMs: 1,
    delayImpl: async () => {},
    fetchImpl: async () => {
      attempts += 1;
      if (attempts < 3) throw new Error("connection refused");
      return { ok: true, async json() { return { contract_version: DIRECT_HOST_CONTRACT_VERSION, port: 19990 }; } };
    }
  });
  assert.equal(attempts, 3);
  assert.equal(health.port, 19990);
});

test("worker health probe rejects a wrong service contract without retrying", async () => {
  let attempts = 0;
  await assert.rejects(waitForDirectHostHealth({
    url: "http://127.0.0.1:19990/__artifact_health", port: 19990, timeoutMs: 1_000, intervalMs: 1,
    delayImpl: async () => {},
    fetchImpl: async () => {
      attempts += 1;
      return { ok: true, async json() { return { contract_version: "wrong", port: 19990 }; } };
    }
  }), /contract mismatch/);
  assert.equal(attempts, 1);
});

function rawGet(url, headers = {}) {
  return new Promise((resolve, reject) => {
    const request = http.get(url, { headers }, response => {
      const chunks = [];
      response.on("data", chunk => chunks.push(chunk));
      response.on("end", () => resolve({ status: response.statusCode, body: Buffer.concat(chunks).toString("utf8") }));
    });
    request.on("error", reject);
  });
}

function scopedToken(resource, random = "a") {
  return `${Buffer.from(`artifact:https://artifact.example${resource}`).toString("base64url")}.${random.repeat(64)}`;
}

test("scoped bootstrap validates token alone, sets host-only resource cookie, strips only token with 303", async () => {
  const token = scopedToken("/artifacts/demo");
  await withServer(async ({ root, request, calls, state }) => {
    fixture(root);
    const first = await request(`/artifacts/demo/v1/?keep=a%2Fb&media_token=${token}&keep=two&message_id=18`, {
      redirect: "manual", headers: { Cookie: "session=reader" }
    });
    assert.equal(first.status, 303);
    assert.equal(await first.text(), "");
    const location = first.headers.get("location");
    const clean = new URL(location, "https://artifact.example");
    assert.equal(clean.searchParams.has("media_token"), false);
    assert.deepEqual(clean.searchParams.getAll("keep"), ["a/b", "two"]);
    assert.equal(clean.searchParams.get("message_id"), "18");
    const cookie = first.headers.get("set-cookie");
    assert.match(cookie, /Path=\/artifacts\/demo; HttpOnly; Secure; SameSite=None; Max-Age=600$/);
    assert.doesNotMatch(cookie, /Domain=/i);
    assert.ok(cookie.startsWith(`catsco_artifact_media=${token};`));
    assert.equal(calls[0].headers.authorization, undefined);
    assert.equal(calls[0].headers.cookie, undefined);
    const headers = { Authorization: "", Cookie: `catsco_artifact_media=${token}` };
    for (const [pathname, init, status] of [
      [location, {}, 200],
      ["/artifacts/demo/v1/app.js", {}, 200],
      ["/artifacts/demo/v1/style.css", { method: "HEAD" }, 200],
      ["/artifacts/demo/v1/app.js", { headers: { Range: "bytes=0-6" } }, 206]
    ]) {
      const before = calls.length;
      const response = await request(pathname, { ...init, headers: { ...headers, ...init.headers } });
      assert.equal(response.status, status);
      assert.equal(calls.length, before + 1);
      assert.equal(calls.at(-1).headers["x-catsco-artifact-media"], token);
      if (status === 206) assert.equal(await response.text(), "private");
      else await response.arrayBuffer();
    }
    state.denied = true;
    for (const init of [{}, { method: "HEAD" }, { headers: { Range: "bytes=0-6" } }]) {
      const before = calls.length;
      const response = await request("/artifacts/demo/v1/app.js", { ...init, headers: { ...headers, ...init.headers } });
      assert.equal(response.status, 404);
      assert.equal(calls.length, before + 1);
      assert.doesNotMatch(await response.text(), /private/);
    }
  }, { authorize: (request, response, state) => {
    const callback = new URL(request.url, "http://localhost");
    const allowed = !state.denied && request.headers["x-catsco-artifact-media"] === token
      && callback.searchParams.get("origin") === "https://artifact.example"
      && callback.searchParams.get("path").startsWith("/artifacts/demo/");
    response.writeHead(allowed ? 204 : 403).end();
  } });
});

test("invalid, expired, wrong-origin and wrong-namespace bootstrap tokens never set cookies or expose bytes", async () => {
  await withServer(async ({ root, request, calls }) => {
    fixture(root);
    const wrongOrigin = `${Buffer.from("artifact:https://other.example/artifacts/demo").toString("base64url")}.${"a".repeat(64)}`;
    for (const token of ["", "malformed", "c2NvcGU." + "a".repeat(64), wrongOrigin,
      scopedToken("/artifacts/by-agent/42/demo"), scopedToken("/artifacts/other"), scopedToken("/artifacts/demo", "b")]) {
      const response = await request(`/artifacts/demo/v1/?media_token=${token}`, { redirect: "manual" });
      assert.equal(response.status, 404);
      assert.equal(response.headers.get("set-cookie"), null);
      assert.equal(response.headers.get("location"), null);
      assert.doesNotMatch(await response.text(), /doctype/);
    }
    assert.equal(calls.length, 1, "only well-formed matching scopes reach token validation");
  }, { authorize: (_request, response) => response.writeHead(401).end() });
});

test("configured namespaces retain their full resource cookie paths and reject aliases", async () => {
  for (const namespace of ["/artifacts", "/artifacts/by-agent/42", "/by-agent/42"]) {
    const resource = namespace === "/artifacts" ? "/artifacts/by-agent/42/demo" : `${namespace}/demo`;
    const token = scopedToken(resource);
    await withServer(async ({ root, request, calls }) => {
      const folder = path.join(root, "by-agent", "42", "demo", "v1");
      fs.mkdirSync(folder, { recursive: true });
      fs.writeFileSync(path.join(folder, "index.html"), "agent page");
      const first = await request(`${resource}/v1/?media_token=${token}`, { redirect: "manual" });
      assert.equal(first.status, 303);
      assert.ok(first.headers.get("set-cookie").includes(`Path=${resource};`));
      const response = await request(first.headers.get("location"), {
        headers: { Authorization: "", Cookie: `catsco_artifact_media=${token}` }
      });
      assert.equal(response.status, 200);
      assert.equal(await response.text(), "agent page");
      assert.equal(new URL(calls[1].url, "http://localhost").searchParams.get("path"), `${resource}/v1/`);
      assert.equal((await request("/artifacts/demo/v1/", {
        headers: { Authorization: "", Cookie: `catsco_artifact_media=${token}` }
      })).status, 404);
      assert.equal((await request(`${namespace}/artifacts-index.json`)).status, 404);
    }, { host: { publicOrigin: `https://artifact.example${namespace}` },
      authorize: (request, response) => response.writeHead(request.headers["x-catsco-artifact-media"] === token ? 204 : 403).end() });
  }
});

test("visitor headers cannot choose callback origin or supply a media session", async () => {
  await withServer(async ({ root, request, calls }) => {
    fixture(root);
    const response = await request("/artifacts/demo/v1/", { headers: {
      Authorization: "", "X-Catsco-Artifact-Origin": "https://attacker.invalid",
      "X-Catsco-Artifact-Media": scopedToken("/artifacts/demo"),
      "X-Catsco-Artifact-Path": "/artifacts/other/v1/"
    } });
    assert.equal(response.status, 404);
    const callback = new URL(calls[0].url, "http://localhost");
    assert.equal(callback.searchParams.get("origin"), "https://artifact.example");
    assert.equal(callback.searchParams.get("path"), "/artifacts/demo/v1/");
    assert.equal(calls[0].headers["x-catsco-artifact-media"], undefined);
  });
});

test("ordinary reads honor explicit Authorization over valid, expired or malformed artifact cookies", async () => {
  const token = scopedToken("/artifacts/demo");
  await withServer(async ({ root, request, calls }) => {
    fixture(root);
    for (const [authorization, cookie, expected] of [
      ["Bearer denied", token, 404],
      ["Bearer reader", scopedToken("/artifacts/demo", "b"), 200],
      ["Bearer reader", "malformed", 200],
    ]) {
      const response = await request("/artifacts/demo/v1/app.js", {
        headers: { Authorization: authorization, Cookie: `catsco_artifact_media=${cookie}` },
      });
      assert.equal(response.status, expected);
      assert.equal(calls.at(-1).headers.authorization, authorization);
      assert.equal(calls.at(-1).headers["x-catsco-artifact-media"], undefined);
      assert.equal(calls.at(-1).headers.cookie, undefined);
      if (expected === 404) assert.doesNotMatch(await response.text(), /private-script/);
      else assert.equal(await response.text(), "private-script");
    }
  }, { authorize(request, response) {
    const allowed = request.headers.authorization === "Bearer reader"
      || request.headers["x-catsco-artifact-media"] === token;
    response.writeHead(allowed ? 204 : 401).end();
  } });
});

test("explicit HTTPS default port uses the same canonical bootstrap scope as the browser", async () => {
  const token = scopedToken("/artifacts/demo");
  await withServer(async ({ root, request, calls }) => {
    fixture(root);
    const response = await request(`/artifacts/demo/v1/?media_token=${token}`, { redirect: "manual" });
    assert.equal(response.status, 303);
    assert.equal(new URL(calls[0].url, "https://callback.example").searchParams.get("origin"), "https://artifact.example");
    assert.equal(calls[0].headers["x-catsco-artifact-media"], token);
    assert.ok(response.headers.get("set-cookie").startsWith(`catsco_artifact_media=${token};`));
  }, { host: { publicOrigin: "https://artifact.example:443" }, authorize(request, response) {
    response.writeHead(request.headers["x-catsco-artifact-media"] === token ? 204 : 401).end();
  } });
});
