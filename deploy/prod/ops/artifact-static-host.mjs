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
const STATIC_PREFIX = "/artifacts";

export function createDirectHostServer(options = {}) {
  const root = path.resolve(requiredText(options.root, "static root"));
  const port = positivePort(options.port, DIRECT_HOST_PORT);
  const rootId = rootIdentifier(root);
  return http.createServer((request, response) => {
    void handleRequest({ request, response, root, rootId, port });
  });
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

async function handleRequest({ request, response, root, rootId, port }) {
  try {
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
        root_id: rootId
      })}\n`);
      send(response, 200, body, {
        "Content-Type": "application/json; charset=utf-8",
        "Cache-Control": "no-store"
      }, request.method);
      return;
    }

    if (requestUrl.pathname === STATIC_PREFIX) {
      response.writeHead(308, { Location: `${STATIC_PREFIX}/` }).end();
      return;
    }
    if (!requestUrl.pathname.startsWith(`${STATIC_PREFIX}/`)) {
      send(response, 404, "Not Found", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }

    let relative;
    try {
      relative = decodeURIComponent(requestUrl.pathname.slice(STATIC_PREFIX.length + 1));
    } catch {
      send(response, 400, "Bad Request", {
        "Content-Type": "text/plain; charset=utf-8"
      }, request.method);
      return;
    }
    if (relative.includes("\0")) {
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
    if (!stat.isFile() || !realPathStaysInside(target, root)) {
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
