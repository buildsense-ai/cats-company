#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

export const DIRECT_HOST_CONTRACT_VERSION = "cloud-html-artifact.direct-host.v1";
export const DIRECT_HOST_SERVICE_NAME = "cloud-html-artifact-direct-host";
export const DIRECT_HOST_PORT = 19990;

const scriptPath = fileURLToPath(import.meta.url);
const HEALTH_PATH = "/__artifact_health";

export function createDirectHostServer(options = {}) {
  const root = path.resolve(requiredText(options.root, "static root"));
  const port = positivePort(options.port, DIRECT_HOST_PORT);
  const authorization = authorizationOptions(options);
  const rootId = rootIdentifier(root);
  return http.createServer((request, response) => {
    void handleRequest({ request, response, root, rootId, port, authorization });
  });
}

function authorizationOptions(options) {
  const secret = String(options.originSecret ?? process.env.CATSCO_ARTIFACT_ORIGIN_SECRET ?? "").trim();
  const rawUrl = String(options.authorizationUrl ?? process.env.CATSCO_ARTIFACT_AUTHORIZATION_URL ?? "").trim();
  if (!/^[A-Za-z0-9_-]{32,256}$/.test(secret) || !rawUrl) return null;
  const url = new URL(rawUrl);
  const loopback = ["127.0.0.1", "[::1]", "localhost"].includes(url.hostname);
  if ((url.protocol !== "https:" && !(url.protocol === "http:" && loopback))
    || url.username || url.password || url.search || url.hash) {
    throw new Error("Artifact authorization URL must be a fixed HTTPS endpoint (HTTP allowed on loopback only)");
  }
  const publicBase = String(options.publicOrigin ?? process.env.CATSCO_ARTIFACT_PUBLIC_BASE_URL ?? "").trim();
  if (!publicBase) return null;
  const base = new URL(publicBase);
  if (base.protocol !== "https:" || base.username || base.password || base.search || base.hash
    || !/^(?:\/artifacts(?:\/by-agent\/[1-9][0-9]*)?|\/by-agent(?:\/[1-9][0-9]*)?)?\/?$/.test(base.pathname)) {
    throw new Error("Artifact public base must use HTTPS and a supported artifact namespace");
  }
  const namespace = base.pathname.replace(/\/$/, "") || "/artifacts";
  const mount = namespace.startsWith("/artifacts") ? "/artifacts" : "";
  return { secret, origin: base.origin, namespace, mount, url: url.href, fetchImpl: options.fetchImpl || fetch };
}

async function authorizeRequest(request, requestUrl, authorization) {
  if (!authorization) return 503;
  const supplied = Buffer.from(String(request.headers["x-catsco-artifact-origin-secret"] || ""));
  const expected = Buffer.from(authorization.secret);
  if (supplied.length !== expected.length || !crypto.timingSafeEqual(supplied, expected)) return 404;
  const origin = authorization.origin;
  if (!requestUrl.pathname.startsWith(`${authorization.namespace}/`)) return 404;
  // Never forward credentials to a URL supplied by the visitor or a redirect.
  const callback = new URL(authorization.url);
  callback.searchParams.set("path", requestUrl.pathname);
  callback.searchParams.set("origin", origin);
  const messageID = requestUrl.searchParams.get("message_id");
  if (messageID !== null) callback.searchParams.set("message_id", messageID);
  const headers = { "X-Catsco-Artifact-Origin-Secret": authorization.secret };
  const mediaToken = artifactMediaToken(request, requestUrl);
  if (requestUrl.searchParams.has("media_token") && !validMediaToken(mediaToken, requestUrl, authorization)) return 404;
  if (!requestUrl.searchParams.has("media_token") && request.headers.authorization) {
    // Ordinary reads honor explicit identity even when another account's
    // scoped cookie is present. A failed header must never fall back to it.
    headers.Authorization = request.headers.authorization;
  } else if (mediaToken) {
    // Establish a cookie only when this exact token was validated, never
    // because a separate platform credential succeeded.
    if (!validMediaToken(mediaToken, requestUrl, authorization)) return 404;
    headers["X-Catsco-Artifact-Media"] = mediaToken;
  } else {
    if (request.headers.cookie) headers.Cookie = request.headers.cookie;
    if (request.headers.authorization) headers.Authorization = request.headers.authorization;
  }
  try {
    const result = await authorization.fetchImpl(callback.href, {
      method: "GET", headers, redirect: "error", signal: AbortSignal.timeout(5_000)
    });
    const status = result.status;
    await result.body?.cancel();
    if (status !== 204) return status >= 500 ? 503 : 404;
    // Independently refuse aggregate indexes and ambiguous path layouts, even
    // if an incorrectly configured auth endpoint returns success for them.
    const rawPath = String(request.url || "").split("?")[0];
    if (rawPath !== requestUrl.pathname || !artifactPathParts(requestUrl.pathname, authorization.mount)) return 404;
    return 204;
  } catch {
    return 503;
  }
}

function artifactMediaToken(request, requestUrl) {
  const query = requestUrl.searchParams.get("media_token");
  if (query) return query;
  for (const cookie of String(request.headers.cookie || "").split(";")) {
    const match = cookie.trim().match(/^catsco_artifact_media=([A-Za-z0-9_.-]+)$/);
    if (match) return match[1];
  }
  return "";
}

function validMediaToken(token, requestUrl, authorization) {
  if (!token || token.length > 8192 || !/^[A-Za-z0-9_-]+\.[a-f0-9]{64}$/.test(token)) return false;
  const parts = artifactPathParts(requestUrl.pathname, authorization.mount);
  if (!parts) return false;
  const scope = `artifact:${authorization.origin}${parts.resourcePath}`;
  return token.split(".")[0] === Buffer.from(scope).toString("base64url");
}

function establishArtifactCookie(requestUrl, response, authorization) {
  if (!requestUrl.searchParams.has("media_token")) return false;
  const token = requestUrl.searchParams.get("media_token");
  const { resourcePath } = artifactPathParts(requestUrl.pathname, authorization.mount);
  response.setHeader("Set-Cookie", `catsco_artifact_media=${token}; Path=${resourcePath}; HttpOnly; Secure; SameSite=None; Max-Age=600`);
  requestUrl.searchParams.delete("media_token");
  response.writeHead(303, { Location: `${requestUrl.pathname}${requestUrl.search}` }).end();
  return true;
}

function artifactPathParts(escapedPath, mount = "/artifacts") {
  let decoded;
  try { decoded = decodeURIComponent(escapedPath); } catch { return null; }
  if (!decoded.startsWith(`${mount}/`) || /[\\\0\r\n%?#]/.test(decoded)) return null;
  const all = decoded.slice(mount.length + 1).replace(/\/$/, "").split("/");
  if (all.some(part => !part || part === "." || part === "..")) return null;
  const start = all[0] === "by-agent" ? 2 : 0;
  if (start && (all.length < 3 || !/^[1-9][0-9]*$/.test(all[1]) || BigInt(all[1]) > 9223372036854775807n)) return null;
  if (!mount && !start) return null;
  if (!/^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$/.test(all[start]) || all[start].length > 64
    || ["artifacts-index.json", "by-agent"].includes(all[start])) return null;
  return { resourcePath: `${mount}/${all.slice(0, start + 1).join("/")}` };
}

function realPathStaysInsideArtifact(target, root, relative) {
  try {
    const parts = relative.split("/");
    const prefix = parts[0] === "by-agent" ? parts.slice(0, 3) : parts.slice(0, 1);
    const artifactRoot = path.resolve(fs.realpathSync(root), ...prefix);
    // The artifact directory itself must not alias a different artifact.
    return fs.realpathSync(path.resolve(root, ...prefix)) === artifactRoot
      && isInsideOrEqual(fs.realpathSync(target), artifactRoot);
  } catch {
    return false;
  }
}

export async function waitForDirectHostHealth(options = {}) {
  const url = requiredText(options.url, "health URL");
  const expectedPort = positivePort(options.port, DIRECT_HOST_PORT);
  const timeoutMs = positiveDuration(options.timeoutMs, 15_000, "health timeout");
  const intervalMs = positiveDuration(options.intervalMs, 500, "health interval");
  const fetchImpl = options.fetchImpl || fetch;
  const delayImpl = options.delayImpl || (milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds)));
  const deadline = Date.now() + timeoutMs;
  let lastError = "Artifact static service did not become ready";

  do {
    try {
      const remaining = Math.max(1, deadline - Date.now());
      const signal = typeof AbortSignal !== "undefined" && typeof AbortSignal.timeout === "function"
        ? AbortSignal.timeout(Math.min(2_000, remaining))
        : undefined;
      const response = await fetchImpl(url, { signal });
      if (!response.ok) throw new Error(`health HTTP ${response.status}`);
      const health = await response.json();
      if (health?.contract_version !== DIRECT_HOST_CONTRACT_VERSION
        || Number(health?.port) !== expectedPort) {
        const error = new Error("Artifact static service health contract mismatch");
        error.terminal = true;
        throw error;
      }
      return health;
    } catch (error) {
      if (error?.terminal) throw error;
      lastError = error instanceof Error ? error.message : String(error);
    }
    if (Date.now() < deadline) await delayImpl(Math.min(intervalMs, Math.max(1, deadline - Date.now())));
  } while (Date.now() < deadline);

  throw new Error(`Artifact static service did not become ready: ${lastError}`);
}

async function handleRequest({ request, response, root, rootId, port, authorization }) {
  try {
    response.setHeader("Cache-Control", "no-store");
    response.setHeader("Vary", "Cookie, Authorization");
    response.setHeader("X-Content-Type-Options", "nosniff");
    response.setHeader("Referrer-Policy", "no-referrer");
    if (!["GET", "HEAD"].includes(request.method || "")) {
      send(response, 405, "Method Not Allowed", {
        Allow: "GET, HEAD",
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }

    const requestUrl = new URL(request.url || "/", "http://127.0.0.1");
    if (requestUrl.pathname === HEALTH_PATH) {
      const body = Buffer.from(`${JSON.stringify({
        ok: true,
        contract_version: DIRECT_HOST_CONTRACT_VERSION,
        service: DIRECT_HOST_SERVICE_NAME,
        pid: process.pid,
        port,
        privacy_enforced: true,
        authorization_configured: Boolean(authorization),
        root_id: rootId
      })}\n`);
      send(response, 200, body, {
        "Content-Type": "application/json; charset=utf-8",
        "Cache-Control": "no-store"
      }, request.method);
      return;
    }

    // Authorize before filesystem metadata, redirects, HEAD or Range handling.
    // The health response above contains no artifact data.
    const authorized = await authorizeRequest(request, requestUrl, authorization);
    if (authorized !== 204) {
      send(response, authorized, authorized === 503 ? "Service Unavailable" : "Not Found", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }
    if (establishArtifactCookie(requestUrl, response, authorization)) return;

    let relative;
    try {
      relative = decodeURIComponent(requestUrl.pathname.slice(authorization.mount.length + 1));
    } catch {
      send(response, 400, "Bad Request", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }
    if (relative.includes("\0") || relative.includes("\\") || relative.includes("%")) {
      send(response, 400, "Bad Request", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }

    let target = path.resolve(root, relative || ".");
    if (!isInsideOrEqual(target, root)) {
      send(response, 403, "Forbidden", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }
    if (!fs.existsSync(target)) {
      send(response, 404, "Not Found", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }

    if (!realPathStaysInside(target, root) || !realPathStaysInsideArtifact(target, root, relative)) {
      send(response, 404, "Not Found", { "Content-Type": "text/plain; charset=utf-8" }, request.method);
      return;
    }
    let stat = fs.statSync(target);
    if (stat.isDirectory()) {
      if (!requestUrl.pathname.endsWith("/")) {
        response.writeHead(308, {
          Location: `${requestUrl.pathname}/${requestUrl.search}`
        }).end();
        return;
      }
      target = path.join(target, "index.html");
      if (!fs.existsSync(target)) {
        send(response, 404, "Not Found", {
          "Content-Type": "text/plain; charset=utf-8"
        }, request.method);
        return;
      }
      stat = fs.statSync(target);
    }
    if (!stat.isFile() || !realPathStaysInside(target, root)
      || !realPathStaysInsideArtifact(target, root, relative)) {
      send(response, 404, "Not Found", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }

    const range = parseByteRange(request.headers.range, stat.size);
    if (range?.invalid) {
      response.writeHead(416, {
        "Content-Range": `bytes */${stat.size}`,
        "Accept-Ranges": "bytes"
      }).end();
      return;
    }

    const start = range?.start ?? 0;
    const end = range?.end ?? Math.max(0, stat.size - 1);
    const contentLength = stat.size === 0 ? 0 : end - start + 1;
    const headers = {
      "Content-Type": contentType(target),
      "Content-Length": String(contentLength),
      "Accept-Ranges": "bytes",
      "Cache-Control": "no-store"
    };
    if (range) headers["Content-Range"] = `bytes ${start}-${end}/${stat.size}`;
    response.writeHead(range ? 206 : 200, headers);
    if (request.method === "HEAD" || stat.size === 0) {
      response.end();
      return;
    }
    const stream = fs.createReadStream(target, { start, end });
    stream.on("error", () => response.destroy());
    stream.pipe(response);
  } catch {
    if (!response.headersSent) {
      response.writeHead(500, { "Content-Type": "text/plain; charset=utf-8" });
    }
    response.end("Internal Server Error");
  }
}

async function runServeCommand(args) {
  const root = path.resolve(requiredText(args.root, "--root"));
  const port = positivePort(args.port, DIRECT_HOST_PORT);
  fs.mkdirSync(root, { recursive: true });
  const server = createDirectHostServer({ root, port });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, "0.0.0.0", resolve);
  });
  console.log(JSON.stringify({
    ok: true,
    contract_version: DIRECT_HOST_CONTRACT_VERSION,
    service: DIRECT_HOST_SERVICE_NAME,
    pid: process.pid,
    host: "0.0.0.0",
    port,
    root_id: rootIdentifier(root)
  }));

  const close = () => server.close(() => process.exit(0));
  process.once("SIGTERM", close);
  process.once("SIGINT", close);
}

async function runProbeCommand(args) {
  const port = positivePort(args.port, DIRECT_HOST_PORT);
  const health = await waitForDirectHostHealth({
    url: requiredText(args.url, "--url"),
    port,
    timeoutMs: args["timeout-ms"] ?? 15_000
  });
  console.log(JSON.stringify({ ok: true, status: "ready", health }));
}

function parseByteRange(value, size) {
  const text = String(value || "").trim();
  if (!text) return null;
  const match = text.match(/^bytes=(\d*)-(\d*)$/i);
  if (!match || size < 1) return { invalid: true };
  let start;
  let end;
  if (!match[1]) {
    const suffixLength = Number(match[2]);
    if (!Number.isInteger(suffixLength) || suffixLength < 1) return { invalid: true };
    start = Math.max(0, size - suffixLength);
    end = size - 1;
  } else {
    start = Number(match[1]);
    end = match[2] ? Number(match[2]) : size - 1;
    if (!Number.isInteger(start) || !Number.isInteger(end)
      || start < 0 || start >= size || end < start) {
      return { invalid: true };
    }
    end = Math.min(end, size - 1);
  }
  return { start, end };
}

function contentType(filePath) {
  return ({
    ".html": "text/html; charset=utf-8",
    ".htm": "text/html; charset=utf-8",
    ".css": "text/css; charset=utf-8",
    ".js": "text/javascript; charset=utf-8",
    ".mjs": "text/javascript; charset=utf-8",
    ".json": "application/json; charset=utf-8",
    ".map": "application/json; charset=utf-8",
    ".txt": "text/plain; charset=utf-8",
    ".xml": "application/xml; charset=utf-8",
    ".svg": "image/svg+xml",
    ".png": "image/png",
    ".jpg": "image/jpeg",
    ".jpeg": "image/jpeg",
    ".webp": "image/webp",
    ".gif": "image/gif",
    ".ico": "image/x-icon",
    ".avif": "image/avif",
    ".woff": "font/woff",
    ".woff2": "font/woff2",
    ".ttf": "font/ttf",
    ".otf": "font/otf",
    ".wasm": "application/wasm",
    ".pdf": "application/pdf",
    ".mp4": "video/mp4",
    ".webm": "video/webm",
    ".mp3": "audio/mpeg",
    ".wav": "audio/wav",
    ".ogg": "audio/ogg"
  })[path.extname(filePath).toLowerCase()] || "application/octet-stream";
}

function realPathStaysInside(target, root) {
  try {
    return isInsideOrEqual(fs.realpathSync(target), fs.realpathSync(root));
  } catch {
    return false;
  }
}

function isInsideOrEqual(target, root) {
  const relative = path.relative(path.resolve(root), path.resolve(target));
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

function rootIdentifier(root) {
  return crypto.createHash("sha256").update(path.resolve(root)).digest("hex");
}

function send(response, status, value, headers, method) {
  const body = Buffer.isBuffer(value) ? value : Buffer.from(String(value));
  response.writeHead(status, { "Content-Length": String(body.length), ...headers });
  if (method === "HEAD") response.end();
  else response.end(body);
}

function positivePort(value, fallback) {
  const number = Number(value ?? fallback);
  if (!Number.isInteger(number) || number < 0 || number > 65_535) {
    throw new Error("Artifact static host port is invalid");
  }
  return number;
}

function positiveDuration(value, fallback, label) {
  const number = Number(value ?? fallback);
  if (!Number.isInteger(number) || number < 1 || number > 300_000) {
    throw new Error(`${label} is invalid`);
  }
  return number;
}

function requiredText(value, label) {
  const text = String(value ?? "").trim();
  if (!text) throw new Error(`${label} is required`);
  return text;
}

function parseArgs(argv) {
  const parsed = { _: [] };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (!arg.startsWith("--")) parsed._.push(arg);
    else {
      const key = arg.slice(2);
      const next = argv[index + 1];
      if (!next || next.startsWith("--")) parsed[key] = true;
      else {
        parsed[key] = next;
        index += 1;
      }
    }
  }
  return parsed;
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(scriptPath)) {
  const args = parseArgs(process.argv.slice(2));
  const command = String(args._[0] || "").trim();
  try {
    if (command === "serve") await runServeCommand(args);
    else if (command === "probe") await runProbeCommand(args);
    else throw new Error("usage: artifact-static-host.mjs <serve --root PATH --port PORT|probe --url URL --port PORT>");
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}
