export const ARTIFACT_REF_CONTRACT = 'catsco.artifact-ref.v1';
export const ARTIFACT_PAGE_CONTEXT_CONTRACT = 'catsco.artifact-page-context.v1';
export const ARTIFACT_CONTEXT_REQUEST_TYPE = 'catsco.artifact.context.request.v1';
export const ARTIFACT_CONTEXT_RESPONSE_TYPE = 'catsco.artifact.context.response.v1';

const ARTIFACT_ID_PATTERN = /^[a-z0-9]+(?:[a-z0-9._-]*[a-z0-9])?$/;
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
    parsed.searchParams.set('artifact_version', String(publishVersion));
    return parsed.toString();
  } catch {
    return '';
  }
}

export function withArtifactRef(payload, artifactRef, pageContext = null) {
  if (!artifactRef) return payload;
  const metadata = { artifact_ref: artifactRef };
  const normalizedPageContext = normalizeArtifactPageContext(pageContext);
  if (normalizedPageContext) metadata.artifact_page_context = normalizedPageContext;
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

  let targetOrigin;
  try {
    targetOrigin = new URL(binding.url).origin;
  } catch {
    return null;
  }
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
      if (event.source !== contentWindow || event.origin !== targetOrigin) return;
      if (event.data?.type !== ARTIFACT_CONTEXT_RESPONSE_TYPE || event.data?.request_id !== requestId) return;
      finish(normalizeArtifactPageContext(event.data.context));
    };
    const timer = window.setTimeout(() => finish(null), boundedTimeout);
    window.addEventListener('message', handleMessage);
    try {
      contentWindow.postMessage({
        type: ARTIFACT_CONTEXT_REQUEST_TYPE,
        request_id: requestId,
      }, targetOrigin);
    } catch {
      finish(null);
    }
  });
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
