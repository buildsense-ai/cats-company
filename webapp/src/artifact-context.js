export const ARTIFACT_REF_CONTRACT = 'catsco.artifact-ref.v1';
export const ARTIFACT_CONTEXT_REF_CONTRACT = 'catsco.artifact-context-ref.v1';
export const ARTIFACT_PAGE_CONTEXT_CONTRACT = 'catsco.artifact-page-context.v1';
export const ARTIFACT_CONTEXT_REQUEST_TYPE = 'catsco.artifact.context.request.v1';
export const ARTIFACT_CONTEXT_RESPONSE_TYPE = 'catsco.artifact.context.response.v1';
export const ARTIFACT_RESULT_CONTRACT = 'catsco.artifact-result.v1';
export const ARTIFACT_RESULT_RECEIPT_CONTRACT = 'catsco.artifact-result-receipt.v1';
export const ARTIFACT_RESULT_REQUEST_TYPE = 'catsco.artifact.result.request.v1';
export const ARTIFACT_RESULT_RESPONSE_TYPE = 'catsco.artifact.result.response.v1';

const ARTIFACT_ID_PATTERN = /^[a-z0-9]+(?:[a-z0-9._-]*[a-z0-9])?$/;
const ARTIFACT_CONTEXT_REF_PATTERN = /^acr_[A-Za-z0-9_-]{43}$/;
const ARTIFACT_WRITEBACK_REF_PATTERN = /^awr_[A-Za-z0-9_-]{43}$/;
const ARTIFACT_RESULT_ID_PATTERN = /^arr_[A-Za-z0-9_-]{43}$/;
const ARTIFACT_RESULT_SINK_ID_PATTERN = /^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*\.v[1-9]\d*$/;
const ARTIFACT_RUNTIME_NODE_PATTERN = /^[A-Za-z0-9._:-]{1,128}$/;
const ARTIFACT_ID_MAX_LENGTH = 64;
const PAGE_CONTEXT_TIMEOUT_MS = 250;
const PAGE_CONTEXT_MAX_BYTES = 16 * 1024;
const PAGE_CONTEXT_MAX_CONTROLS = 24;
const PAGE_CONTEXT_MAX_SEMANTIC_BYTES = 8 * 1024;
const PAGE_CONTEXT_SEMANTIC_MAX_DEPTH = 6;
const PAGE_CONTEXT_SEMANTIC_MAX_ARRAY_ITEMS = 50;
const PAGE_CONTEXT_SEMANTIC_MAX_OBJECT_KEYS = 50;
const PAGE_CONTEXT_SEMANTIC_MAX_KEY_LENGTH = 128;
const PAGE_CONTEXT_SEMANTIC_MAX_STRING_LENGTH = 1000;
const PAGE_CONTEXT_SEMANTIC_MAX_VISITS = 4096;
const INVALID_SEMANTIC_VALUE = Symbol('invalid-semantic-value');
const UNSAFE_SEMANTIC_KEYS = new Set(['__proto__', 'constructor', 'prototype']);
const PAGE_CONTEXT_CONTROL_TYPES = new Set([
  'checkbox',
  'radio',
  'select-one',
  'select-multiple',
  'text',
  'search',
  'number',
  'range',
  'textarea',
]);
const ARTIFACT_RESULT_MAX_BYTES = 64 * 1024;
const ARTIFACT_RESULT_RECEIPT_MAX_BYTES = 8 * 1024;
const ARTIFACT_RESULT_TIMEOUT_MS = 17_000;

function artifactFrameMessagePolicy(url) {
  let frameOrigin;
  try {
    frameOrigin = new URL(url).origin;
  } catch {
    return null;
  }

  // Managed same-origin Artifacts are loaded with an opaque sandbox origin.
  // They can only receive messages sent with `*`, and their replies report
  // `null`; keep source identity as the second half of the trust check below.
  const isOpaqueSameOriginFrame = typeof window !== 'undefined'
    && frameOrigin === window.location.origin;
  return {
    targetOrigin: isOpaqueSameOriginFrame ? '*' : frameOrigin,
    responseOrigin: isOpaqueSameOriginFrame ? 'null' : frameOrigin,
  };
}

function positiveInteger(value) {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : 0;
}

export function artifactRefFromPreviewFile(file, expectedAgentUid = null) {
  const artifactId = String(file?.artifact_id || '');
  const artifactURL = String(file?.url || '').trim();
  const mimeType = String(file?.mime_type || '').trim().toLowerCase();
  if (artifactId !== artifactId.trim()
    || artifactId.length > ARTIFACT_ID_MAX_LENGTH
    || !ARTIFACT_ID_PATTERN.test(artifactId)
    || mimeType !== 'text/html') return null;

  if (expectedAgentUid !== null && expectedAgentUid !== undefined) {
    const expectedAgent = positiveInteger(expectedAgentUid);
    const previewAgent = positiveInteger(file?.artifact_agent_uid);
    if (expectedAgent <= 0 || previewAgent !== expectedAgent) return null;
  }

  try {
    const parsedURL = new URL(artifactURL);
    if (parsedURL.protocol !== 'https:' && parsedURL.protocol !== 'http:') return null;
  } catch {
    return null;
  }

  const ref = {
    contract_version: ARTIFACT_REF_CONTRACT,
    id: artifactId,
    currently_visible: true,
  };
  const displayedVersion = positiveInteger(file?.publish_version);
  if (displayedVersion > 0) ref.displayed_version = displayedVersion;
  return ref;
}

export function artifactURLForVersion(value, version) {
  const publishVersion = positiveInteger(version);
  if (publishVersion <= 0) return '';
  try {
    const parsed = new URL(String(value || '').trim());
    if (!['http:', 'https:'].includes(parsed.protocol)) return '';
    const versionedPath = parsed.pathname.replace(
      /(\/artifacts\/[^/]+\/)(?:latest|v[1-9]\d*)(?=\/|$)/,
      `$1v${publishVersion}`,
    );
    if (versionedPath !== parsed.pathname) {
      parsed.pathname = versionedPath;
      parsed.searchParams.delete('artifact_version');
      return parsed.toString();
    }
    parsed.searchParams.set('artifact_version', String(publishVersion));
    return parsed.toString();
  } catch {
    return '';
  }
}

export function artifactContextRefFromSnapshot(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return '';
  if (value.contract_version !== ARTIFACT_CONTEXT_REF_CONTRACT) return '';
  const contextRef = String(value.context_ref || '');
  return ARTIFACT_CONTEXT_REF_PATTERN.test(contextRef) ? contextRef : '';
}

export function withArtifactContextRef(payload, contextRef) {
  if (!ARTIFACT_CONTEXT_REF_PATTERN.test(String(contextRef || ''))) return payload;
  const metadata = { artifact_context_ref: contextRef };
  if (typeof payload === 'string') {
    return {
      type: 'text',
      content: payload,
      metadata,
    };
  }
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) return payload;
  return {
    ...payload,
    metadata: {
      ...(payload.metadata || {}),
      ...metadata,
    },
  };
}

export async function requestArtifactPageContext(binding, artifactRef, timeoutMs = PAGE_CONTEXT_TIMEOUT_MS) {
  const frame = binding?.frame;
  const contentWindow = frame?.contentWindow;
  if (!artifactRef || binding?.artifactId !== artifactRef.id || !contentWindow?.postMessage) return null;

  const messagePolicy = artifactFrameMessagePolicy(binding.url);
  if (!messagePolicy) return null;
  const requestId = artifactContextRequestId();
  const boundedTimeout = Number.isFinite(timeoutMs)
    ? Math.max(0, Math.min(1000, Math.round(timeoutMs)))
    : PAGE_CONTEXT_TIMEOUT_MS;

  return new Promise((resolve) => {
    let settled = false;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      window.removeEventListener('message', handleMessage);
      window.clearTimeout(timer);
      resolve(value);
    };
    const handleMessage = (event) => {
      if (event.source !== contentWindow || event.origin !== messagePolicy.responseOrigin) return;
      if (event.data?.type !== ARTIFACT_CONTEXT_RESPONSE_TYPE || event.data?.request_id !== requestId) return;
      finish(normalizeArtifactPageContext(event.data.context));
    };
    const timer = window.setTimeout(() => finish(null), boundedTimeout);
    window.addEventListener('message', handleMessage);
    try {
      contentWindow.postMessage({
        type: ARTIFACT_CONTEXT_REQUEST_TYPE,
        request_id: requestId,
      }, messagePolicy.targetOrigin);
    } catch {
      finish(null);
    }
  });
}

export function normalizeArtifactResultDelivery(value) {
  if (!semanticPlainObject(value) || value.type !== 'request') return null;
  const contextRef = String(value.context_ref || '');
  const writebackRef = String(value.writeback_ref || '');
  const resultId = String(value.result_id || '');
  const sinkId = String(value.sink_id || '');
  const artifactId = String(value.artifact_id || '');
  const originNodeId = String(value.origin_node_id || '');
  const topicId = String(value.topic_id || '').trim();
  const agentUid = positiveInteger(value.agent_uid);
  const displayedVersion = positiveInteger(value.displayed_version);
  if (!ARTIFACT_RUNTIME_NODE_PATTERN.test(originNodeId)
    || !ARTIFACT_CONTEXT_REF_PATTERN.test(contextRef)
    || !ARTIFACT_WRITEBACK_REF_PATTERN.test(writebackRef)
    || !ARTIFACT_RESULT_ID_PATTERN.test(resultId)
    || !ARTIFACT_RESULT_SINK_ID_PATTERN.test(sinkId)
    || !ARTIFACT_ID_PATTERN.test(artifactId)
    || !topicId || topicId.length > 512 || agentUid <= 0 || displayedVersion <= 0) return null;
  const expectedRevision = optionalArtifactResultRevision(value.expected_state_revision);
  if (expectedRevision === false) return null;
  const payload = cloneBoundedArtifactResultJSON(value.payload, ARTIFACT_RESULT_MAX_BYTES);
  if (payload === INVALID_SEMANTIC_VALUE) return null;
  return {
    type: 'request',
    originNodeId,
    contextRef,
    writebackRef,
    topicId,
    agentUid,
    artifactId,
    displayedVersion,
    sinkId,
    resultId,
    expectedStateRevision: expectedRevision || '',
    payload,
  };
}

export async function requestArtifactResultApply(binding, delivery, timeoutMs = ARTIFACT_RESULT_TIMEOUT_MS) {
  const normalized = delivery?.resultId ? delivery : normalizeArtifactResultDelivery(delivery);
  const frame = binding?.frame;
  const contentWindow = frame?.contentWindow;
  if (!normalized || !contentWindow?.postMessage
    || binding?.artifactId !== normalized.artifactId
    || Number(binding?.agentUid || 0) !== normalized.agentUid) return null;

  const messagePolicy = artifactFrameMessagePolicy(binding.url);
  if (!messagePolicy) return null;
  const requestId = artifactContextRequestId();
  const boundedTimeout = Number.isFinite(timeoutMs)
    ? Math.max(100, Math.min(20_000, Math.round(timeoutMs)))
    : ARTIFACT_RESULT_TIMEOUT_MS;
  const result = {
    contract_version: ARTIFACT_RESULT_CONTRACT,
    artifact_id: normalized.artifactId,
    displayed_version: normalized.displayedVersion,
    sink_id: normalized.sinkId,
    result_id: normalized.resultId,
    payload: normalized.payload,
  };
  if (normalized.expectedStateRevision) {
    result.expected_state_revision = normalized.expectedStateRevision;
  }

  return new Promise((resolve) => {
    let settled = false;
    const finish = (receipt) => {
      if (settled) return;
      settled = true;
      window.removeEventListener('message', handleMessage);
      window.clearTimeout(timer);
      resolve(receipt);
    };
    const handleMessage = (event) => {
      if (event.source !== contentWindow || event.origin !== messagePolicy.responseOrigin) return;
      if (event.data?.type !== ARTIFACT_RESULT_RESPONSE_TYPE || event.data?.request_id !== requestId) return;
      finish(normalizeArtifactResultReceipt(event.data.receipt, normalized.resultId)
        || artifactResultFailureReceipt(normalized.resultId, 'invalid_receipt'));
    };
    const timer = window.setTimeout(() => finish(null), boundedTimeout);
    window.addEventListener('message', handleMessage);
    try {
      contentWindow.postMessage({
        type: ARTIFACT_RESULT_REQUEST_TYPE,
        request_id: requestId,
        result,
      }, messagePolicy.targetOrigin);
    } catch {
      finish(null);
    }
  });
}

export function normalizeArtifactResultReceipt(value, expectedResultId) {
  if (!semanticPlainObject(value)
    || value.contract_version !== ARTIFACT_RESULT_RECEIPT_CONTRACT
    || value.result_id !== expectedResultId
    || !ARTIFACT_RESULT_ID_PATTERN.test(String(value.result_id || ''))
    || !new Set(['applied', 'rejected', 'failed']).has(value.status)) return null;
  const code = value.code === undefined ? '' : String(value.code);
  const message = value.message === undefined ? '' : String(value.message);
  if ((code && !/^[a-z][a-z0-9_]{0,63}$/.test(code))
    || (message && (message !== message.trim() || message.length > 2000 || /[\0\r\n]/.test(message)))) return null;
  let receipt;
  if (value.receipt !== undefined) {
    receipt = cloneBoundedArtifactResultJSON(value.receipt, ARTIFACT_RESULT_RECEIPT_MAX_BYTES, 2048);
    if (receipt === INVALID_SEMANTIC_VALUE) return null;
  }
  const normalized = {
    contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
    result_id: expectedResultId,
    status: value.status,
  };
  if (code) normalized.code = code;
  if (message) normalized.message = message;
  if (receipt !== undefined) normalized.receipt = receipt;
  return jsonSize(normalized) <= ARTIFACT_RESULT_RECEIPT_MAX_BYTES ? normalized : null;
}

function artifactResultFailureReceipt(resultId, code) {
  return {
    contract_version: ARTIFACT_RESULT_RECEIPT_CONTRACT,
    result_id: resultId,
    status: 'failed',
    code,
  };
}

function optionalArtifactResultRevision(value) {
  if (value === undefined || value === null || value === '') return '';
  return typeof value === 'string' && value === value.trim() && value.length <= 128
    && !/[\0\r\n]/.test(value) ? value : false;
}

function cloneBoundedArtifactResultJSON(value, maxBytes, maxVisits = 16_384) {
  const visits = { remaining: maxVisits };
  const clone = cloneArtifactResultJSON(value, 0, visits);
  return clone !== INVALID_SEMANTIC_VALUE && jsonSize(clone) <= maxBytes
    ? clone
    : INVALID_SEMANTIC_VALUE;
}

function cloneArtifactResultJSON(value, depth, visits) {
  if (depth > 12 || visits.remaining <= 0) return INVALID_SEMANTIC_VALUE;
  visits.remaining -= 1;
  if (value === null || typeof value === 'boolean') return value;
  if (typeof value === 'number') return Number.isFinite(value) ? value : INVALID_SEMANTIC_VALUE;
  if (typeof value === 'string') return value.length <= 16_384 ? value : INVALID_SEMANTIC_VALUE;
  if (Array.isArray(value)) {
    if (value.length > 1000) return INVALID_SEMANTIC_VALUE;
    const result = [];
    for (const item of value) {
      const child = cloneArtifactResultJSON(item, depth + 1, visits);
      if (child === INVALID_SEMANTIC_VALUE) return INVALID_SEMANTIC_VALUE;
      result.push(child);
    }
    return result;
  }
  if (!semanticPlainObject(value)) return INVALID_SEMANTIC_VALUE;
  const keys = Object.keys(value);
  if (keys.length > 256) return INVALID_SEMANTIC_VALUE;
  const result = {};
  for (const key of keys.sort()) {
    if (key.length > 128 || UNSAFE_SEMANTIC_KEYS.has(key)) return INVALID_SEMANTIC_VALUE;
    const child = cloneArtifactResultJSON(value[key], depth + 1, visits);
    if (child === INVALID_SEMANTIC_VALUE) return INVALID_SEMANTIC_VALUE;
    result[key] = child;
  }
  return result;
}

export function normalizeArtifactPageContext(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  if (value.contract_version !== ARTIFACT_PAGE_CONTEXT_CONTRACT) return null;
  const rawSize = jsonSize({
    contract_version: value.contract_version,
    observed_at: value.observed_at,
    title: value.title,
    location: value.location,
    selected_text: value.selected_text,
    last_interaction: value.last_interaction,
    controls: value.controls,
    dirty: value.dirty,
    artifact_version: value.artifact_version,
  });
  if (rawSize <= 0 || rawSize > PAGE_CONTEXT_MAX_BYTES) return null;

  const observedAt = boundedText(value.observed_at, 64, false);
  if (!observedAt || !Number.isFinite(Date.parse(observedAt))) return null;
  const normalized = {
    contract_version: ARTIFACT_PAGE_CONTEXT_CONTRACT,
    observed_at: observedAt,
  };
  const title = boundedText(value.title, 256);
  if (title) normalized.title = title;

  const location = normalizeLocation(value.location);
  if (location) normalized.location = location;
  const selectedText = boundedText(value.selected_text, 2000);
  if (selectedText) normalized.selected_text = selectedText;
  const interaction = normalizeInteraction(value.last_interaction);
  if (interaction) normalized.last_interaction = interaction;
  if (typeof value.dirty === 'boolean') normalized.dirty = value.dirty;
  const artifactVersion = positiveInteger(value.artifact_version);
  if (artifactVersion > 0) normalized.artifact_version = artifactVersion;

  const controls = Array.isArray(value.controls)
    ? value.controls.slice(0, PAGE_CONTEXT_MAX_CONTROLS).map(normalizeControl).filter(Boolean)
    : [];
  if (controls.length > 0) normalized.controls = controls;

  const semanticContext = normalizeSemanticContext(value.semantic_context);
  if (semanticContext !== INVALID_SEMANTIC_VALUE) normalized.semantic_context = semanticContext;
  if (Object.keys(normalized).length === 2) return null;
  if (jsonSize(normalized) <= PAGE_CONTEXT_MAX_BYTES) return normalized;
  delete normalized.semantic_context;
  return Object.keys(normalized).length > 2 && jsonSize(normalized) <= PAGE_CONTEXT_MAX_BYTES
    ? normalized
    : null;
}

function normalizeSemanticContext(value) {
  try {
    const sanitized = sanitizeSemanticValue(value, 0, new WeakSet(), {
      remaining: PAGE_CONTEXT_SEMANTIC_MAX_VISITS,
    });
    if (sanitized === INVALID_SEMANTIC_VALUE || !hasSemanticContent(sanitized)) {
      return INVALID_SEMANTIC_VALUE;
    }
    const size = jsonSize(sanitized);
    return size > 0 && size <= PAGE_CONTEXT_MAX_SEMANTIC_BYTES
      ? sanitized
      : INVALID_SEMANTIC_VALUE;
  } catch {
    return INVALID_SEMANTIC_VALUE;
  }
}

function sanitizeSemanticValue(value, depth, ancestors, visits) {
  if (depth > PAGE_CONTEXT_SEMANTIC_MAX_DEPTH || visits.remaining <= 0) return INVALID_SEMANTIC_VALUE;
  visits.remaining -= 1;
  if (value === null) return null;
  if (typeof value === 'string') return truncateSemanticString(value, PAGE_CONTEXT_SEMANTIC_MAX_STRING_LENGTH);
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number') return Number.isFinite(value) ? value : INVALID_SEMANTIC_VALUE;
  if (!value || typeof value !== 'object' || ancestors.has(value)) return INVALID_SEMANTIC_VALUE;

  ancestors.add(value);
  try {
    if (Array.isArray(value)) {
      const result = [];
      const limit = Math.min(value.length, PAGE_CONTEXT_SEMANTIC_MAX_ARRAY_ITEMS);
      for (let index = 0; index < limit && visits.remaining > 0; index += 1) {
        let item;
        try {
          item = value[index];
        } catch {
          continue;
        }
        const sanitized = sanitizeSemanticValue(item, depth + 1, ancestors, visits);
        if (sanitized !== INVALID_SEMANTIC_VALUE) result.push(sanitized);
      }
      return result;
    }
    if (!semanticPlainObject(value)) return INVALID_SEMANTIC_VALUE;

    const result = {};
    let keys;
    try {
      keys = [];
      for (const key in value) {
        if (!Object.prototype.hasOwnProperty.call(value, key)) continue;
        if (!semanticLengthAtMost(key, PAGE_CONTEXT_SEMANTIC_MAX_KEY_LENGTH)
          || UNSAFE_SEMANTIC_KEYS.has(key)) continue;
        keys.push(key);
        if (keys.length >= PAGE_CONTEXT_SEMANTIC_MAX_OBJECT_KEYS) break;
      }
      keys.sort();
    } catch {
      return INVALID_SEMANTIC_VALUE;
    }
    for (const key of keys) {
      if (visits.remaining <= 0) break;
      let child;
      try {
        child = value[key];
      } catch {
        continue;
      }
      const sanitized = sanitizeSemanticValue(child, depth + 1, ancestors, visits);
      if (sanitized !== INVALID_SEMANTIC_VALUE) result[key] = sanitized;
    }
    return result;
  } finally {
    ancestors.delete(value);
  }
}

function semanticPlainObject(value) {
  try {
    const prototype = Object.getPrototypeOf(value);
    return prototype === null || prototype === Object.prototype;
  } catch {
    return false;
  }
}

function truncateSemanticString(value, limit) {
  let count = 0;
  let end = 0;
  for (const character of value) {
    if (count >= limit) break;
    count += 1;
    end += character.length;
  }
  return value.slice(0, end);
}

function semanticLengthAtMost(value, limit) {
  let count = 0;
  for (const unused of value) {
    void unused;
    count += 1;
    if (count > limit) return false;
  }
  return true;
}

function hasSemanticContent(value) {
  if (value === null) return false;
  if (typeof value === 'string') return value.length > 0;
  if (typeof value === 'boolean' || typeof value === 'number') return true;
  if (Array.isArray(value)) return value.length > 0;
  return semanticPlainObject(value) && Object.keys(value).length > 0;
}

function normalizeLocation(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const pathname = boundedText(value.pathname, 1024, false);
  const hash = boundedText(value.hash, 512, false);
  const normalized = {};
  if (pathname?.startsWith('/')) normalized.pathname = pathname;
  if (hash === '' || hash?.startsWith('#')) normalized.hash = hash;
  return Object.keys(normalized).length > 0 ? normalized : null;
}

function normalizeInteraction(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const normalized = {};
  const tag = boundedText(value.tag, 32)?.toLowerCase();
  if (tag && /^[a-z][a-z0-9-]*$/.test(tag)) normalized.tag = tag;
  for (const [key, limit] of [['role', 64], ['name', 256], ['text', 256]]) {
    const text = boundedText(value[key], limit);
    if (text) normalized[key] = text;
  }
  return Object.keys(normalized).length > 0 ? normalized : null;
}

function normalizeControl(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const type = boundedText(value.type, 32)?.toLowerCase();
  if (!type || !PAGE_CONTEXT_CONTROL_TYPES.has(type)) return null;
  const normalized = { type };
  for (const [key, limit] of [['name', 256], ['aria_label', 256], ['role', 64], ['value', 512], ['text', 256]]) {
    const text = boundedText(value[key], limit);
    if (text) normalized[key] = text;
  }
  if (typeof value.checked === 'boolean' && (type === 'checkbox' || type === 'radio')) {
    normalized.checked = value.checked;
  }
  return Object.keys(normalized).length > 1 ? normalized : null;
}

function boundedText(value, maxLength, trim = true) {
  if (typeof value !== 'string') return '';
  const text = trim ? value.trim() : value;
  return text.length <= maxLength ? text : text.slice(0, maxLength);
}

function jsonSize(value) {
  try {
    return new TextEncoder().encode(JSON.stringify(value)).byteLength;
  } catch {
    return 0;
  }
}

function artifactContextRequestId() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  const random = Math.random().toString(36).slice(2);
  return `artifact-${Date.now().toString(36)}-${random}`;
}
