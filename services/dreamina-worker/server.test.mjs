import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const tempRoot = await mkdtemp(path.join(os.tmpdir(), "dreamina-worker-test-"));
process.env.DREAMINA_WORKER_DATA_ROOT = tempRoot;
process.env.DREAMINA_CLI_BIN = process.execPath;
process.env.DREAMINA_CLI_PREFIX_ARGS_JSON = JSON.stringify([
  path.join(HERE, "runtime", "fake-dreamina-image.mjs"),
]);
process.env.FAKE_DREAMINA_STATE = path.join(tempRoot, "fake-state.json");
process.env.DREAMINA_WORKER_POLL_WAIT_SECONDS = "0";
process.env.DREAMINA_IMAGE_PROMPT_MAX_CHARS = "900";

const { createDreaminaWorkerServer } = await import("./server.mjs");
const server = createDreaminaWorkerServer();
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const address = server.address();
const baseURL = `http://127.0.0.1:${address.port}`;

test.after(async () => {
  await new Promise((resolve) => server.close(resolve));
  await rm(tempRoot, { recursive: true, force: true });
});

async function workerFetch(pathname, options = {}) {
  return await fetch(baseURL + pathname, {
    ...options,
    headers: {
      "X-CatsCo-Owner-UID": "42",
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...options.headers,
    },
  });
}

async function waitForTask(taskID, attempts = 20) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    const response = await workerFetch(`/v1/tasks/${taskID}`);
    const body = await response.json();
    if (body.status !== "processing") return { response, body };
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`Task ${taskID} did not finish in the test window.`);
}

test("health reports the configured CLI", async () => {
  const response = await fetch(baseURL + "/healthz");
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { ok: true });
});

test("generation returns an OpenAI-compatible completed image", async () => {
  process.env.FAKE_DREAMINA_SCENARIO = "success";
  const response = await workerFetch("/v1/images/generations", {
    method: "POST",
    headers: {
      "X-CatsCo-Dreamina-Provider-Role": "primary",
    },
    body: JSON.stringify({
      prompt: "A bright editorial portrait",
      size: "1536x1024",
      output_format: "png",
    }),
  });
  assert.equal(response.status, 202);
  const pending = await response.json();
  const { response: completedResponse, body } = await waitForTask(pending.task_id);
  assert.equal(completedResponse.status, 200);
  assert.equal(body.status, "completed");
  assert.equal(body.provider, "dreamina");
  assert.match(body.task_id, /^dreamina_/);
  assert.ok(Buffer.from(body.data[0].b64_json, "base64").length > 100);
  const fakeState = JSON.parse(await readFile(process.env.FAKE_DREAMINA_STATE, "utf8"));
  assert.equal(fakeState.last_submit_command, "text2image");
  assert.ok(fakeState.last_submit_args.includes("--resolution_type=2k"));
  const metadata = JSON.parse(await readFile(
    path.join(tempRoot, "tasks", body.task_id, "worker-task.json"),
    "utf8",
  ));
  assert.equal(metadata.provider_role, "primary");
  const result = JSON.parse(await readFile(
    path.join(tempRoot, "tasks", body.task_id, "result.json"),
    "utf8",
  ));
  assert.equal(result.routing.provider_role, "primary");
});

test("reference generation preserves the image and uses image2image", async () => {
  process.env.FAKE_DREAMINA_SCENARIO = "success";
  const sourceResponse = await workerFetch("/v1/images/generations", {
    method: "POST",
    body: JSON.stringify({ prompt: "Reference source", size: "1024x1024" }),
  });
  const sourcePending = await sourceResponse.json();
  const { body: source } = await waitForTask(sourcePending.task_id);
  const response = await workerFetch("/v1/images/edits", {
    method: "POST",
    body: JSON.stringify({
      prompt: "Keep the character identity and make the scene brighter",
      images: [{ image_url: `data:image/png;base64,${source.data[0].b64_json}` }],
      size: "3840x2160",
      output_format: "png",
    }),
  });
  assert.equal(response.status, 202);
  const pending = await response.json();
  const { response: completedResponse, body } = await waitForTask(pending.task_id);
  assert.equal(completedResponse.status, 200);
  assert.equal(body.status, "completed");
  const fakeState = JSON.parse(await readFile(process.env.FAKE_DREAMINA_STATE, "utf8"));
  assert.equal(fakeState.last_submit_command, "image2image");
  assert.ok(fakeState.last_submit_args.some((value) => value.startsWith("--images=")));
  assert.ok(fakeState.last_submit_args.includes("--resolution_type=4k"));
});

test("explicit 4K requests select Dreamina 4K for text generation", async () => {
  process.env.FAKE_DREAMINA_SCENARIO = "success";
  const response = await workerFetch("/v1/images/generations", {
    method: "POST",
    body: JSON.stringify({
      prompt: "A clean architectural visualization",
      size: "3840x2160",
      output_format: "png",
    }),
  });
  assert.equal(response.status, 202);
  const pending = await response.json();
  const { response: completedResponse, body } = await waitForTask(pending.task_id);
  assert.equal(completedResponse.status, 200);
  assert.equal(body.status, "completed");
  const fakeState = JSON.parse(await readFile(process.env.FAKE_DREAMINA_STATE, "utf8"));
  assert.equal(fakeState.last_submit_command, "text2image");
  assert.ok(fakeState.last_submit_args.includes("--resolution_type=4k"));
});

test("2K-sized requests are not promoted to Dreamina 4K", async () => {
  process.env.FAKE_DREAMINA_SCENARIO = "success";
  const response = await workerFetch("/v1/images/generations", {
    method: "POST",
    body: JSON.stringify({
      prompt: "A wide editorial scene",
      size: "2560x1440",
      output_format: "png",
    }),
  });
  assert.equal(response.status, 202);
  const pending = await response.json();
  const { response: completedResponse, body } = await waitForTask(pending.task_id);
  assert.equal(completedResponse.status, 200);
  assert.equal(body.status, "completed");
  const fakeState = JSON.parse(await readFile(process.env.FAKE_DREAMINA_STATE, "utf8"));
  assert.equal(fakeState.last_submit_command, "text2image");
  assert.ok(fakeState.last_submit_args.includes("--resolution_type=2k"));
});

test("pending tasks resume without another submission", async () => {
  process.env.FAKE_DREAMINA_SCENARIO = "always_pending";
  const response = await workerFetch("/v1/images/generations", {
    method: "POST",
    body: JSON.stringify({ prompt: "A task that waits", size: "1024x1024" }),
  });
  assert.equal(response.status, 202);
  const pending = await response.json();
  await workerFetch(`/v1/tasks/${pending.task_id}`);
  const stateAfterSubmit = JSON.parse(await readFile(process.env.FAKE_DREAMINA_STATE, "utf8"));
  const submits = stateAfterSubmit.submit_count;

  process.env.FAKE_DREAMINA_SCENARIO = "success";
  const completedResponse = await workerFetch(`/v1/tasks/${pending.task_id}`);
  assert.equal(completedResponse.status, 200);
  const completed = await completedResponse.json();
  assert.equal(completed.status, "completed");
  const finalState = JSON.parse(await readFile(process.env.FAKE_DREAMINA_STATE, "utf8"));
  assert.equal(finalState.submit_count, submits);
});

test("terminal task failures use a successful polling response", async () => {
  process.env.FAKE_DREAMINA_SCENARIO = "empty_auth";
  const response = await workerFetch("/v1/images/generations", {
    method: "POST",
    body: JSON.stringify({ prompt: "A task that cannot authenticate", size: "1024x1024" }),
  });
  const pending = await response.json();
  const failedResponse = await workerFetch(`/v1/tasks/${pending.task_id}`);
  assert.equal(failedResponse.status, 200);
  const failed = await failedResponse.json();
  assert.equal(failed.status, "failed");
  assert.equal(failed.error.code, "AUTH_REQUIRED");
});

test("task ownership is enforced", async () => {
  process.env.FAKE_DREAMINA_SCENARIO = "always_pending";
  const response = await workerFetch("/v1/images/generations", {
    method: "POST",
    body: JSON.stringify({ prompt: "Private task", size: "1024x1024" }),
  });
  const pending = await response.json();
  const denied = await workerFetch(`/v1/tasks/${pending.task_id}`, {
    headers: { "X-CatsCo-Owner-UID": "99" },
  });
  assert.equal(denied.status, 404);
  process.env.FAKE_DREAMINA_SCENARIO = "success";
  await workerFetch(`/v1/tasks/${pending.task_id}`);
});
