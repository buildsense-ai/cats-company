#!/usr/bin/env node

import { createHash, randomBytes } from "node:crypto";
import { spawn } from "node:child_process";
import { access, mkdir, readFile, rename, stat, unlink, writeFile } from "node:fs/promises";
import http from "node:http";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  createReferenceDescriptor,
  inspectReferenceImage,
} from "./runtime/reference-image-utils.mjs";

const ROOT = path.dirname(fileURLToPath(import.meta.url));
const EXECUTOR = path.join(ROOT, "runtime", "generate-dreamina-image.mjs");
const HOST = process.env.DREAMINA_WORKER_HOST || "0.0.0.0";
const PORT = parseInteger(process.env.DREAMINA_WORKER_PORT, 7070, 1, 65535);
const DATA_ROOT = path.resolve(process.env.DREAMINA_WORKER_DATA_ROOT || "/var/lib/dreamina-worker");
const MAX_BODY_BYTES = parseInteger(
  process.env.DREAMINA_WORKER_MAX_BODY_BYTES,
  24 * 1024 * 1024,
  1024,
  100 * 1024 * 1024,
);
const POLL_WAIT_SECONDS = parseInteger(process.env.DREAMINA_WORKER_POLL_WAIT_SECONDS, 12, 0, 60);
const CLI_TIMEOUT_MS = parseInteger(process.env.DREAMINA_CLI_TIMEOUT_MS, 120_000, 1000, 600_000);
const TASK_ID_PATTERN = /^dreamina_[a-z0-9]{12}_[a-f0-9]{24}$/;
const activeRuns = new Map();
const supportedRatios = new Set(["21:9", "16:9", "3:2", "4:3", "1:1", "3:4", "2:3", "9:16"]);

function parseInteger(raw, fallback, min, max) {
  if (raw === undefined || raw === null || raw === "") return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < min || value > max) {
    throw new Error(`Expected an integer from ${min} to ${max}.`);
  }
  return value;
}

function jsonResponse(response, status, body, headers = {}) {
  const encoded = Buffer.from(`${JSON.stringify(body)}\n`, "utf8");
  response.writeHead(status, {
    "Cache-Control": "no-store",
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": String(encoded.length),
    ...headers,
  });
  response.end(encoded);
}

function requestError(status, code, message) {
  const error = new Error(message);
  error.status = status;
  error.code = code;
  return error;
}

async function readJsonBody(request) {
  const chunks = [];
  let bytes = 0;
  for await (const chunk of request) {
    bytes += chunk.length;
    if (bytes > MAX_BODY_BYTES) {
      throw requestError(413, "request_too_large", "Dreamina request body is too large.");
    }
    chunks.push(chunk);
  }
  let body;
  try {
    body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw requestError(400, "invalid_json", "Dreamina request body must be valid JSON.");
  }
  if (!body || typeof body !== "object" || Array.isArray(body)) {
    throw requestError(400, "invalid_request", "Dreamina request body must be a JSON object.");
  }
  return body;
}

function ownerUID(request) {
  const value = String(request.headers["x-catsco-owner-uid"] || "").trim();
  if (!/^[1-9][0-9]{0,18}$/.test(value)) {
    throw requestError(400, "missing_owner", "X-CatsCo-Owner-UID is required.");
  }
  return value;
}

function taskDirectory(taskID) {
  if (!TASK_ID_PATTERN.test(taskID)) {
    throw requestError(404, "task_not_found", "Dreamina task does not exist.");
  }
  return path.join(DATA_ROOT, "tasks", taskID);
}

function dataURLImage(value, index) {
  if (typeof value !== "string") {
    throw requestError(400, "invalid_reference", `Reference image ${index + 1} is missing image_url.`);
  }
  const match = /^data:(image\/(?:png|jpeg|webp));base64,([A-Za-z0-9+/]+={0,2})$/.exec(value);
  if (!match) {
    throw requestError(400, "invalid_reference", `Reference image ${index + 1} must be a base64 PNG, JPEG, or WebP data URL.`);
  }
  let buffer;
  try {
    buffer = Buffer.from(match[2], "base64");
  } catch {
    throw requestError(400, "invalid_reference", `Reference image ${index + 1} contains invalid base64.`);
  }
  const image = inspectReferenceImage(buffer, `reference image ${index + 1}`);
  if (image.mediaType !== match[1]) {
    throw requestError(400, "invalid_reference", `Reference image ${index + 1} bytes do not match its media type.`);
  }
  return { buffer, image };
}

function ratioFromSize(size) {
  if (typeof size !== "string" || size === "auto") return undefined;
  const match = /^([0-9]{3,4})x([0-9]{3,4})$/.exec(size);
  if (!match) return undefined;
  let left = Number(match[1]);
  let right = Number(match[2]);
  const gcd = (a, b) => {
    while (b) [a, b] = [b, a % b];
    return a || 1;
  };
  const divisor = gcd(left, right);
  left /= divisor;
  right /= divisor;
  const ratio = `${left}:${right}`;
  return supportedRatios.has(ratio) ? ratio : undefined;
}

async function atomicWrite(filePath, contents) {
  await mkdir(path.dirname(filePath), { recursive: true });
  const temporary = `${filePath}.tmp-${process.pid}-${randomBytes(4).toString("hex")}`;
  await writeFile(temporary, contents);
  try {
    await rename(temporary, filePath);
  } finally {
    await unlink(temporary).catch(() => {});
  }
}

async function prepareTask(payload, operation, uid, routing) {
  const prompt = typeof payload.prompt === "string" ? payload.prompt.trim() : "";
  if (!prompt) throw requestError(400, "invalid_request", "prompt is required.");
  if (prompt.length > 12_000) throw requestError(400, "invalid_request", "prompt is too long.");

  const images = operation === "edits" ? payload.images : [];
  if (operation === "edits" && (!Array.isArray(images) || images.length < 1 || images.length > 3)) {
    throw requestError(400, "invalid_reference", "images must contain 1-3 reference images.");
  }

  const taskID = `dreamina_${Date.now().toString(36).padStart(12, "0")}_${randomBytes(12).toString("hex")}`;
  const taskDir = taskDirectory(taskID);
  const referencesDir = path.join(taskDir, "references");
  await mkdir(referencesDir, { recursive: true });

  const references = [];
  for (let index = 0; index < images.length; index += 1) {
    const raw = images[index];
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
      throw requestError(400, "invalid_reference", `Reference image ${index + 1} is invalid.`);
    }
    const prepared = dataURLImage(raw.image_url, index);
    const filename = `reference-${String(index + 1).padStart(2, "0")}.${prepared.image.extension}`;
    const relativePath = path.posix.join("references", filename);
    await atomicWrite(path.join(referencesDir, filename), prepared.buffer);
    references.push(createReferenceDescriptor({
      relativePath,
      buffer: prepared.buffer,
      useFor: `Use reference image ${index + 1} only as directed by the prompt.`,
    }));
  }

  const size = typeof payload.size === "string" ? payload.size : "auto";
  const requestRecord = {
    operation: "generate",
    prompt,
    reference_images: references,
    ...(ratioFromSize(size) ? { aspect_ratio: ratioFromSize(size) } : {}),
    size,
    quality: typeof payload.quality === "string" ? payload.quality : "auto",
    output_format: typeof payload.output_format === "string" ? payload.output_format : "png",
    filename: "generated-image",
    count: 1,
    background: "opaque",
  };
  const metadata = {
    schema_version: 1,
    task_id: taskID,
    owner_uid: uid,
    operation,
    created_at: new Date().toISOString(),
    request_sha256: createHash("sha256").update(JSON.stringify(payload)).digest("hex"),
    provider_role: routing.providerRole,
    fallback_from: routing.fallbackFrom,
    fallback_reason: routing.fallbackReason,
  };
  await atomicWrite(path.join(taskDir, "request.json"), `${JSON.stringify(requestRecord, null, 2)}\n`);
  await atomicWrite(path.join(taskDir, "worker-task.json"), `${JSON.stringify(metadata, null, 2)}\n`);
  return { taskID, taskDir };
}

async function runExecutor(taskDir, waitSeconds, metadata) {
  const existing = activeRuns.get(taskDir);
  if (existing) return await existing;
  const providerRole = metadata.provider_role === "primary" ? "primary" : "fallback";

  const promise = new Promise((resolveRun) => {
    let stdout = "";
    let stderr = "";
    let spawnError = null;
    const executorArguments = [
      EXECUTOR,
      "--request", path.join(taskDir, "request.json"),
      "--out-dir", taskDir,
      "--provider-role", providerRole,
      "--wait-seconds", String(waitSeconds),
    ];
    if (providerRole === "fallback") {
      executorArguments.push(
        "--fallback-from", metadata.fallback_from || "image2",
        "--fallback-reason", metadata.fallback_reason || "image2_race_exhausted",
      );
    }
    const child = spawn(process.execPath, executorArguments, {
      cwd: taskDir,
      env: {
        ...process.env,
        DREAMINA_PREPARED_GATEWAY_REQUEST: "true",
        DREAMINA_CLI_TIMEOUT_MS: String(CLI_TIMEOUT_MS),
      },
      shell: false,
      windowsHide: true,
      stdio: ["ignore", "pipe", "pipe"],
    });
    child.stdout.on("data", (chunk) => {
      if (stdout.length < 200_000) stdout += chunk.toString("utf8");
    });
    child.stderr.on("data", (chunk) => {
      if (stderr.length < 200_000) stderr += chunk.toString("utf8");
    });
    child.on("error", (error) => { spawnError = error; });
    child.on("close", (exitCode, signal) => {
      resolveRun({ exitCode, signal, error: spawnError, stdout, stderr });
    });
  });
  activeRuns.set(taskDir, promise);
  try {
    return await promise;
  } finally {
    activeRuns.delete(taskDir);
  }
}

async function readTaskMetadata(taskDir, uid) {
  let metadata;
  try {
    metadata = JSON.parse(await readFile(path.join(taskDir, "worker-task.json"), "utf8"));
  } catch {
    throw requestError(404, "task_not_found", "Dreamina task does not exist.");
  }
  if (String(metadata.owner_uid) !== String(uid)) {
    throw requestError(404, "task_not_found", "Dreamina task does not exist.");
  }
  return metadata;
}

async function readResult(taskDir) {
  try {
    return JSON.parse(await readFile(path.join(taskDir, "result.json"), "utf8"));
  } catch {
    return null;
  }
}

async function publicResult(taskID, taskDir, result) {
  if (result?.ok === true && result.status === "generated") {
    const imagePath = result?.output?.image_path;
    const image = imagePath ? await readFile(imagePath) : null;
    if (!image?.length) throw new Error("Dreamina reported success without a readable image.");
    return {
      status: 200,
      body: {
        id: taskID,
        task_id: taskID,
        status: "completed",
        provider: "dreamina",
        created: Math.floor(Date.now() / 1000),
        data: [{
          b64_json: image.toString("base64"),
          revised_prompt: null,
        }],
      },
    };
  }
  if (result?.status === "pending") {
    return {
      status: 202,
      body: {
        id: taskID,
        task_id: taskID,
        status: "processing",
        provider: "dreamina",
        retry_after_ms: Number(result.retry_after_ms || 3000),
      },
    };
  }
  const message = result?.error?.message || "Dreamina generation failed.";
  return {
    status: 503,
    body: {
      id: taskID,
      task_id: taskID,
      status: "failed",
      provider: "dreamina",
      error: {
        code: result?.error?.code || result?.status || "dreamina_failed",
        message,
      },
    },
  };
}

async function processTask(taskID, uid, waitSeconds) {
  const taskDir = taskDirectory(taskID);
  const metadata = await readTaskMetadata(taskDir, uid);
  let result = await readResult(taskDir);
  if (result?.status !== "generated" && result?.status !== "generation_failed") {
    await runExecutor(taskDir, waitSeconds, metadata);
    result = await readResult(taskDir);
  }
  return await publicResult(taskID, taskDir, result);
}

async function executableReady() {
  const executable = process.env.DREAMINA_CLI_BIN || "dreamina";
  if (path.isAbsolute(executable)) {
    await access(executable);
    const details = await stat(executable);
    return details.isFile();
  }
  for (const directory of String(process.env.PATH || "").split(path.delimiter)) {
    if (!directory) continue;
    try {
      await access(path.join(directory, executable));
      return true;
    } catch {
      // Continue searching PATH.
    }
  }
  return false;
}

export function createDreaminaWorkerServer() {
  return http.createServer(async (request, response) => {
    try {
      const url = new URL(request.url || "/", "http://dreamina-worker.local");
      if (request.method === "GET" && url.pathname === "/healthz") {
        await mkdir(path.join(DATA_ROOT, "tasks"), { recursive: true });
        const ready = await executableReady();
        jsonResponse(response, ready ? 200 : 503, { ok: ready });
        return;
      }

      if (request.method === "POST" && (
        url.pathname === "/v1/images/generations"
        || url.pathname === "/v1/images/edits"
      )) {
        const uid = ownerUID(request);
        const payload = await readJsonBody(request);
        const operation = url.pathname.endsWith("/edits") ? "edits" : "generations";
        const providerRole = request.headers["x-catsco-dreamina-provider-role"] === "primary"
          ? "primary"
          : "fallback";
        const routing = {
          providerRole,
          fallbackFrom: providerRole === "fallback"
            ? String(request.headers["x-catsco-dreamina-fallback-from"] || "image2")
            : null,
          fallbackReason: providerRole === "fallback"
            ? String(request.headers["x-catsco-dreamina-fallback-reason"] || "image2_race_exhausted")
            : null,
        };
        const { taskID } = await prepareTask(payload, operation, uid, routing);
        void processTask(taskID, uid, 0).catch((error) => {
          process.stderr.write(`Dreamina background task ${taskID} failed: ${error?.stack || error}\n`);
        });
        jsonResponse(response, 202, {
          id: taskID,
          task_id: taskID,
          status: "processing",
          provider: "dreamina",
          retry_after_ms: 1000,
        }, {
          "X-CatsCo-Image-Provider": "dreamina",
        });
        return;
      }

      if (request.method === "GET" && url.pathname.startsWith("/v1/tasks/")) {
        const uid = ownerUID(request);
        const taskID = decodeURIComponent(url.pathname.slice("/v1/tasks/".length));
        const result = await processTask(taskID, uid, POLL_WAIT_SECONDS);
        const taskStatus = result.body?.status === "processing" || result.body?.status === "failed"
          ? 200
          : result.status;
        jsonResponse(response, taskStatus, result.body, {
          "X-CatsCo-Image-Provider": "dreamina",
        });
        return;
      }

      jsonResponse(response, 404, { error: { code: "not_found", message: "Not found." } });
    } catch (error) {
      const status = Number(error?.status) || 500;
      jsonResponse(response, status, {
        error: {
          code: error?.code || "internal_error",
          message: status >= 500 ? "Dreamina worker failed." : error.message,
        },
      });
    }
  });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await mkdir(path.join(DATA_ROOT, "tasks"), { recursive: true });
  createDreaminaWorkerServer().listen(PORT, HOST, () => {
    process.stdout.write(`Dreamina worker listening on ${HOST}:${PORT}\n`);
  });
}
