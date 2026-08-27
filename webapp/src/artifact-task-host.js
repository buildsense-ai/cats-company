import { api, wsSendArtifactResultReceipt } from './api';
import {
  ARTIFACT_BRIDGE_READY_TYPE,
  ARTIFACT_HOST_CONNECT_TYPE,
  ARTIFACT_TASK_ACCEPTED_TYPE,
  ARTIFACT_TASK_REJECTED_TYPE,
  ARTIFACT_TASK_REQUEST_TYPE,
  ARTIFACT_TASK_STATUS_CONTRACT,
  ARTIFACT_TASK_STATUS_TYPE,
  classifyArtifactTaskPollFailure,
  normalizeArtifactResultDelivery,
  normalizeArtifactTaskCreated,
  normalizeArtifactTaskRequest,
  normalizeArtifactTaskStatus,
  requestArtifactPageContext,
  requestArtifactResultApply,
} from './artifact-context';

const TASK_REQUEST_TIMEOUT_MS = 5000;
const TASK_POLL_MS = 1000;
const TASK_POLL_MAX_FAILURES = 5;
const TASK_CONFIRMATION_MAX_AGE_MS = 12000;
const TASK_DELIVERY_ATTEMPTS = 2;
const TASK_DELIVERY_RECONCILE_ATTEMPTS = 4;
const TASK_DELIVERY_RECONCILE_MS = 150;
const TASK_CONCURRENCY_LIMIT = 8;
const TASK_RESULT_FALLBACK_GRACE_MS = 5 * 60_000;
const MAX_TIMER_MS = 2_147_000_000;

function bindingOrigin(binding) {
  try {
    return new URL(binding?.url || '').origin;
  } catch {
    return '';
  }
}

function postBridgeMessage(binding, message) {
  const contentWindow = binding?.frame?.contentWindow;
  const targetOrigin = bindingOrigin(binding);
  if (!contentWindow?.postMessage || !targetOrigin) return false;
  try {
    contentWindow.postMessage(message, targetOrigin);
    return true;
  } catch {
    return false;
  }
}

function sessionMatches(current, record) {
  return Boolean(current && record
    && current.token === record.token
    && current.identityKey === record.identityKey
    && current.topicId === record.topicId
    && current.topicGeneration === record.topicGeneration
    && current.agentUid === record.agentUid
    && current.artifactId === record.artifactId
    && current.displayedVersion === record.displayedVersion
    && current.binding === record.binding
    && current.binding?.artifactId === record.artifactId
    && Number(current.binding?.agentUid || 0) === record.agentUid);
}

function taskStatusMessage(taskId, expiresAt, status, extra = {}) {
  return {
    type: ARTIFACT_TASK_STATUS_TYPE,
    task: {
      contract_version: ARTIFACT_TASK_STATUS_CONTRACT,
      task_id: taskId,
      status,
      updated_at: new Date().toISOString(),
      expires_at: expiresAt,
      ...extra,
    },
  };
}

function taskResultGraceMs(expiresAt, currentTime) {
  const expiresAtMs = Date.parse(String(expiresAt || ''));
  if (!Number.isFinite(expiresAtMs)) return TASK_RESULT_FALLBACK_GRACE_MS;
  return Math.min(MAX_TIMER_MS, Math.max(1000, expiresAtMs - currentTime));
}

export function createArtifactTaskHost({
  getCurrentSession,
  confirmTask,
  apiClient = api,
  pageContextReader = requestArtifactPageContext,
  resultApplier = requestArtifactResultApply,
  sendResultReceipt = wsSendArtifactResultReceipt,
  now = () => Date.now(),
  setTimer = (callback, timeoutMs) => globalThis.setTimeout(callback, timeoutMs),
  clearTimer = (timer) => globalThis.clearTimeout(timer),
} = {}) {
  const activeTasks = new Map();
  const activeRequests = new Set();
  let connectedBinding = null;
  let disposed = false;
  let suspended = false;

  const currentSession = () => {
    if (disposed || suspended || typeof getCurrentSession !== 'function') return null;
    const session = getCurrentSession();
    if (!session?.identityKey || !session?.topicId || !session?.artifactRef
      || !session?.binding || !session?.token || Number(session.agentUid || 0) <= 0
      || !session.artifactId || Number(session.displayedVersion || 0) <= 0) return null;
    return {
      ...session,
      topicGeneration: Number(session.topicGeneration || 0),
      agentUid: Number(session.agentUid),
      displayedVersion: Number(session.displayedVersion),
    };
  };

  const recordIsCurrent = (record) => sessionMatches(currentSession(), record);

  const failServerTask = (taskId) => Promise.resolve()
    .then(() => apiClient.failArtifactTask(taskId, { timeoutMs: TASK_REQUEST_TIMEOUT_MS }))
    .catch(() => {});

  const cancelActiveTasks = () => {
    for (const record of activeTasks.values()) {
      if (record.timer) clearTimer(record.timer);
      failServerTask(record.taskId);
    }
    activeTasks.clear();
    connectedBinding = null;
  };

  const beginPolling = (record) => {
    const failLocally = (code, message) => {
      activeTasks.delete(record.taskId);
      if (!recordIsCurrent(record)) return;
      postBridgeMessage(record.binding, taskStatusMessage(
        record.taskId,
        record.expiresAt,
        'failed',
        { code, message },
      ));
    };
    const poll = async () => {
      if (disposed || activeTasks.get(record.taskId) !== record) return;
      if (suspended) {
        record.timer = null;
        return;
      }
      if (!recordIsCurrent(record)) {
        activeTasks.delete(record.taskId);
        failServerTask(record.taskId);
        return;
      }
      try {
        const status = normalizeArtifactTaskStatus(await apiClient.getArtifactTask(record.taskId, {
          timeoutMs: TASK_REQUEST_TIMEOUT_MS,
        }));
        if (suspended || activeTasks.get(record.taskId) !== record) {
          record.timer = null;
          return;
        }
        if (!status) throw new Error('Invalid Artifact task status response');
        if (recordIsCurrent(record)) {
          record.pollFailures = 0;
          postBridgeMessage(record.binding, {
            type: ARTIFACT_TASK_STATUS_TYPE,
            task: status,
          });
          if (status.status === 'failed') {
            activeTasks.delete(record.taskId);
            return;
          }
          if (status.status === 'completed') {
            record.awaitingResult = true;
            record.timer = setTimer(() => {
              if (activeTasks.get(record.taskId) === record) {
                activeTasks.delete(record.taskId);
              }
            }, taskResultGraceMs(record.expiresAt, now()));
            return;
          }
        }
      } catch (error) {
        if (suspended || activeTasks.get(record.taskId) !== record) {
          record.timer = null;
          return;
        }
        record.pollFailures += 1;
        const decision = classifyArtifactTaskPollFailure(
          error,
          record.pollFailures,
          TASK_POLL_MAX_FAILURES,
        );
        if (!decision.retry) {
          failLocally(decision.code, decision.message);
          return;
        }
      }
      if (activeTasks.get(record.taskId) === record) {
        record.timer = setTimer(poll, TASK_POLL_MS);
      }
    };
    record.timer = setTimer(poll, 0);
  };

  const handleTaskRequest = async (event, request) => {
    const session = currentSession();
    const targetOrigin = bindingOrigin(session?.binding);
    if (!session || !targetOrigin
      || event.source !== session.binding.frame?.contentWindow
      || event.origin !== targetOrigin) return;

    const requestKey = `${session.identityKey}:${request.requestId}`;
    const runningTaskCount = [...activeTasks.values()]
      .filter((record) => !record.awaitingResult).length;
    if (activeRequests.has(requestKey)
      || activeRequests.size >= TASK_CONCURRENCY_LIMIT
      || runningTaskCount >= TASK_CONCURRENCY_LIMIT) {
      postBridgeMessage(session.binding, {
        type: ARTIFACT_TASK_REJECTED_TYPE,
        request_id: request.requestId,
        code: 'task_request_busy',
        message: 'Artifact task request is already being processed',
      });
      return;
    }

    activeRequests.add(requestKey);
    try {
      const confirmationStartedAt = now();
      const confirmed = await confirmTask?.();
      if (!confirmed || now() - confirmationStartedAt > TASK_CONFIRMATION_MAX_AGE_MS) {
        postBridgeMessage(session.binding, {
          type: ARTIFACT_TASK_REJECTED_TYPE,
          request_id: request.requestId,
          code: confirmed ? 'task_confirmation_expired' : 'task_request_cancelled',
          message: confirmed
            ? 'Artifact task confirmation expired'
            : 'Artifact task request was cancelled',
        });
        return;
      }
      if (!sessionMatches(currentSession(), session)) return;

      const pageContext = await pageContextReader(session.binding, session.artifactRef);
      if (!sessionMatches(currentSession(), session)) return;

      const created = normalizeArtifactTaskCreated(await apiClient.createArtifactTask({
        topic_id: session.topicId,
        artifact_ref: session.artifactRef,
        intent_id: request.intentId,
        payload: request.payload,
        ...(pageContext ? { page_context: pageContext } : {}),
      }, { timeoutMs: TASK_REQUEST_TIMEOUT_MS }));
      if (!created) throw new Error('invalid Artifact task response');

      const record = {
        ...session,
        taskId: created.taskId,
        timer: null,
        pollFailures: 0,
        expiresAt: created.expiresAt,
      };
      activeTasks.set(record.taskId, record);

      if (!recordIsCurrent(record)) {
        activeTasks.delete(record.taskId);
        await failServerTask(record.taskId);
        throw new Error('Artifact preview changed before task delivery');
      }

      const turn = {
        type: 'text',
        content: created.visibleMessage,
        client_msg_id: `artifact-task:${record.taskId}`,
        metadata: { artifact_task_ref: created.taskRef },
      };
      let delivered = false;
      let deliveryError = null;
      let deliveryStatus = null;
      for (let attempt = 0; attempt < TASK_DELIVERY_ATTEMPTS && !delivered; attempt += 1) {
        if (!recordIsCurrent(record)) break;
        try {
          await apiClient.sendMessage(record.topicId, turn);
          delivered = true;
        } catch (error) {
          deliveryError = error;
          try {
            deliveryStatus = normalizeArtifactTaskStatus(await apiClient.getArtifactTask(record.taskId, {
              timeoutMs: TASK_REQUEST_TIMEOUT_MS,
            }));
          } catch {
            deliveryStatus = null;
          }
          if (deliveryStatus?.delivery_status === 'delivered') delivered = true;
          else if (deliveryStatus?.status === 'failed') break;
        }
      }
      for (let attempt = 0;
        !delivered
          && deliveryStatus?.delivery_status === 'pending'
          && attempt < TASK_DELIVERY_RECONCILE_ATTEMPTS
          && recordIsCurrent(record);
        attempt += 1) {
        await new Promise((resolve) => setTimer(resolve, TASK_DELIVERY_RECONCILE_MS));
        try {
          const nextStatus = normalizeArtifactTaskStatus(await apiClient.getArtifactTask(record.taskId, {
            timeoutMs: TASK_REQUEST_TIMEOUT_MS,
          }));
          if (nextStatus) deliveryStatus = nextStatus;
        } catch {}
        if (deliveryStatus?.delivery_status === 'delivered') delivered = true;
        if (deliveryStatus?.status === 'failed') break;
      }
      if (!delivered) {
        if (!recordIsCurrent(record)) {
          activeTasks.delete(record.taskId);
          await failServerTask(record.taskId);
          throw new Error('Artifact preview changed before task delivery');
        }
        const deliveryDefinitelyFailed = deliveryStatus?.status === 'failed';
        let deliveryRemainsServerOwned = false;
        if (!deliveryDefinitelyFailed) {
          try {
            await apiClient.failArtifactTask(record.taskId, {
              timeoutMs: TASK_REQUEST_TIMEOUT_MS,
            });
          } catch (error) {
            const deliveryConflict = error?.status === 409 ? error?.data : null;
            if (deliveryConflict?.code === 'artifact_task_delivery_pending') {
              deliveryRemainsServerOwned = true;
            } else if (deliveryConflict?.code === 'artifact_task_delivery_committed') {
              delivered = true;
              deliveryRemainsServerOwned = true;
            } else if (![404, 410].includes(Number(error?.status || 0))) {
              // A transport or server failure leaves ownership ambiguous. Keep
              // polling instead of dropping the only route for a late result.
              deliveryRemainsServerOwned = true;
            }
          }
        }
        if (!deliveryRemainsServerOwned) {
          activeTasks.delete(record.taskId);
          throw deliveryError || new Error(
            deliveryDefinitelyFailed
              ? (deliveryStatus.message || 'Artifact task delivery failed')
              : 'Artifact task delivery could not be confirmed',
          );
        }
      }

      postBridgeMessage(record.binding, {
        type: ARTIFACT_TASK_ACCEPTED_TYPE,
        request_id: request.requestId,
        task: {
          contract_version: ARTIFACT_TASK_STATUS_CONTRACT,
          task_id: record.taskId,
          status: 'submitted',
          delivery_status: delivered ? 'delivered' : 'pending',
          expires_at: created.expiresAt,
          updated_at: new Date().toISOString(),
        },
      });
      beginPolling(record);
    } catch (error) {
      if (sessionMatches(currentSession(), session)) {
        postBridgeMessage(session.binding, {
          type: ARTIFACT_TASK_REJECTED_TYPE,
          request_id: request.requestId,
          code: 'task_request_failed',
          message: error?.message || 'Artifact task request failed',
        });
      }
    } finally {
      activeRequests.delete(requestKey);
    }
  };

  const connect = (binding = currentSession()?.binding) => {
    if (disposed || !binding) {
      connectedBinding = null;
      return false;
    }
    const session = currentSession();
    if (session?.binding !== binding) return false;
    if (connectedBinding === binding) return true;
    connectedBinding = binding;
    Promise.resolve().then(() => {
      const session = currentSession();
      if (session?.binding === binding && connectedBinding === binding) {
        postBridgeMessage(binding, { type: ARTIFACT_HOST_CONNECT_TYPE });
      }
    });
    return true;
  };

  const handleResultDelivery = async (value) => {
    const delivery = normalizeArtifactResultDelivery(value);
    if (!delivery?.taskId) return false;
    const record = activeTasks.get(delivery.taskId);
    if (!record || !recordIsCurrent(record)
      || record.topicId !== delivery.topicId
      || record.agentUid !== delivery.agentUid
      || record.artifactId !== delivery.artifactId
      || record.displayedVersion !== delivery.displayedVersion) return false;

    const receipt = await resultApplier(record.binding, delivery);
    if (!receipt || activeTasks.get(delivery.taskId) !== record || !recordIsCurrent(record)) {
      return false;
    }
    sendResultReceipt({
      type: 'receipt',
      origin_node_id: delivery.originNodeId,
      task_id: delivery.taskId,
      writeback_ref: delivery.writebackRef,
      topic_id: delivery.topicId,
      agent_uid: String(delivery.agentUid),
      artifact_id: delivery.artifactId,
      displayed_version: delivery.displayedVersion,
      result_id: delivery.resultId,
      receipt,
    });
    if (record.timer) clearTimer(record.timer);
    activeTasks.delete(record.taskId);
    return true;
  };

  const handleWindowMessage = (event) => {
    const session = currentSession();
    if (!session) return;
    const targetOrigin = bindingOrigin(session.binding);
    if (event.data?.type === ARTIFACT_BRIDGE_READY_TYPE) {
      if (event.source === session.binding.frame?.contentWindow && event.origin === targetOrigin) {
        connectedBinding = session.binding;
        postBridgeMessage(session.binding, { type: ARTIFACT_HOST_CONNECT_TYPE });
      }
      return;
    }
    if (event.data?.type !== ARTIFACT_TASK_REQUEST_TYPE) return;
    const request = normalizeArtifactTaskRequest(event.data);
    if (request) void handleTaskRequest(event, request);
  };

  const deactivate = () => {
    suspended = false;
    cancelActiveTasks();
  };

  const suspend = () => {
    if (disposed || suspended) return;
    suspended = true;
    connectedBinding = null;
    for (const record of activeTasks.values()) {
      if (record.timer) clearTimer(record.timer);
      record.timer = null;
    }
  };

  const resume = () => {
    if (disposed || !suspended) return;
    suspended = false;
    for (const record of [...activeTasks.values()]) {
      if (!recordIsCurrent(record)) {
        activeTasks.delete(record.taskId);
        failServerTask(record.taskId);
        continue;
      }
      record.awaitingResult = false;
      beginPolling(record);
    }
  };

  const dispose = () => {
    if (disposed) return;
    disposed = true;
    cancelActiveTasks();
    activeRequests.clear();
  };

  return {
    connect,
    deactivate,
    dispose,
    handleResultDelivery,
    handleWindowMessage,
    resume,
    suspend,
  };
}
