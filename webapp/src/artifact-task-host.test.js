import { describe, expect, it, vi } from "vitest";
import { ARTIFACT_TASK_REQUEST_TYPE } from "./artifact-context";
import { createArtifactTaskHost } from "./artifact-task-host";

const taskId = `atk_${"t".repeat(43)}`;
const taskRef = `atr_${"r".repeat(43)}`;
const runId = `run_${"u".repeat(43)}`;

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
  await Promise.resolve();
}

function createTaskHarness(persistent) {
  const contentWindow = { postMessage: vi.fn() };
  const binding = {
    url: "https://agent-440.artifacts.catsco.fun:19991/artifacts/project-board/v8/",
    frame: { contentWindow },
    artifactId: "project-board",
    agentUid: 440,
  };
  const session = {
    token: { id: "focus" },
    identityKey: "p2p_7_440:440:project-board:8",
    topicId: "p2p_7_440",
    topicGeneration: 1,
    agentUid: 440,
    artifactId: "project-board",
    displayedVersion: 8,
    artifactRef: {
      contract_version: "catsco.artifact-ref.v1",
      id: "project-board",
      displayed_version: 8,
      currently_visible: true,
    },
    binding,
  };
  const apiClient = {
    createArtifactTask: vi.fn(async () => ({
      contract_version: "catsco.artifact-task-ref.v1",
      task_id: taskId,
      task_ref: taskRef,
      status: "submitted",
      visible_message: "来自「项目看板」：生成推进建议",
      expires_at: "2026-08-31T12:00:00Z",
      ...(persistent
        ? { run_id: runId, completion_mode: "runtime_state" }
        : {}),
    })),
    sendMessage: vi.fn(async () => ({ ok: true })),
    getArtifactTask: vi.fn(async () => ({
      contract_version: "catsco.artifact-task-status.v1",
      task_id: taskId,
      status: "submitted",
      delivery_status: "delivered",
      updated_at: "2026-08-31T10:00:00Z",
      expires_at: "2026-08-31T12:00:00Z",
    })),
    failArtifactTask: vi.fn(async () => ({ ok: true })),
  };
  const timers = [];
  const host = createArtifactTaskHost({
    getCurrentSession: () => session,
    confirmTask: async () => true,
    pageContextReader: async () => null,
    apiClient,
    setTimer(callback, timeoutMs) {
      const timer = { callback, timeoutMs };
      timers.push(timer);
      return timer;
    },
    clearTimer: vi.fn(),
  });
  return {
    host,
    apiClient,
    event: {
      source: contentWindow,
      origin: new URL(binding.url).origin,
      data: {
        type: ARTIFACT_TASK_REQUEST_TYPE,
        request_id: "request-plan-1",
        intent_id: "tasks.plan.v1",
        payload: { scope: "week" },
      },
    },
  };
}

describe("createArtifactTaskHost Runtime 0.2 lifecycle", () => {
  it("does not fail a persistent Run when the preview closes", async () => {
    const harness = createTaskHarness(true);
    harness.host.handleWindowMessage(harness.event);
    await flush();
    expect(harness.apiClient.sendMessage).toHaveBeenCalledTimes(1);
    harness.host.deactivate();
    await flush();
    expect(harness.apiClient.failArtifactTask).not.toHaveBeenCalled();
  });

  it("keeps the V4.1 disconnect failure behavior for page-result tasks", async () => {
    const harness = createTaskHarness(false);
    harness.host.handleWindowMessage(harness.event);
    await flush();
    expect(harness.apiClient.sendMessage).toHaveBeenCalledTimes(1);
    harness.host.deactivate();
    await flush();
    expect(harness.apiClient.failArtifactTask).toHaveBeenCalledWith(
      taskId,
      expect.any(Object),
    );
  });
});
