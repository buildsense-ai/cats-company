import {
  readStorageValue,
  removeStorageValue,
  writeStorageValue,
} from './utils/storage-access';
import {
  REQUEST_ERROR_CODE,
  REQUEST_FAILURE_KIND,
  requestFailureKind,
} from './utils/request-error';

export const API_BASE = import.meta.env.VITE_API_BASE || '';

let token = readStorageValue('oc_token');
const PUSH_REGISTRATION_ID_KEY = 'oc_push_registration_id';
const PUSH_REGISTRATION_OWNER_KEY = 'oc_push_registration_owner';
// A registration ID guards server deletes. sessionStorage preserves it across
// reloads and cross-origin returns in this browsing context. A copied storage
// area is safe because cleanup first coordinates with active peer tabs.
let pushRegistrationID = '';
let pushRegistrationOwner = '';
let authRevision = 0;

const newPushRegistrationID = () => {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
};

const decodeTokenPayload = (candidate) => {
  try {
    const encodedPayload = candidate?.split('.')[1];
    if (!encodedPayload) return null;
    const normalized = encodedPayload.replace(/-/g, '+').replace(/_/g, '/');
    const padded = normalized.padEnd(Math.ceil(normalized.length / 4) * 4, '=');
    return JSON.parse(atob(padded));
  } catch {
    return null;
  }
};

const pushRegistrationOwnerForToken = (candidate) => {
  const userID = decodeTokenPayload(candidate)?.userId;
  return userID === undefined || userID === null ? null : `user:${userID}`;
};

const readPushRegistration = () => {
  return {
    id: String(readStorageValue(PUSH_REGISTRATION_ID_KEY, 'sessionStorage') || '').trim(),
    owner: String(readStorageValue(PUSH_REGISTRATION_OWNER_KEY, 'sessionStorage') || '').trim(),
  };
};

const writePushRegistration = (id, owner) => {
  writeStorageValue(PUSH_REGISTRATION_ID_KEY, id, 'sessionStorage');
  writeStorageValue(PUSH_REGISTRATION_OWNER_KEY, owner, 'sessionStorage');
};

const registrationIDForToken = (candidate) => {
  const owner = pushRegistrationOwnerForToken(candidate);
  if (!owner) return newPushRegistrationID();
  if (pushRegistrationID && pushRegistrationOwner === owner) {
    return pushRegistrationID;
  }
  const saved = readPushRegistration();
  if (saved.owner === owner && saved.id && saved.id.length <= 64) {
    pushRegistrationID = saved.id;
    pushRegistrationOwner = owner;
    return pushRegistrationID;
  }
  const registrationID = newPushRegistrationID();
  pushRegistrationID = registrationID;
  pushRegistrationOwner = owner;
  writePushRegistration(registrationID, owner);
  return registrationID;
};

const legacyRegistrationIDForToken = (candidate) => {
  const owner = pushRegistrationOwnerForToken(candidate);
  if (!owner) return '';
  const id = String(readStorageValue(PUSH_REGISTRATION_ID_KEY) || '').trim();
  const legacyOwner = String(readStorageValue(PUSH_REGISTRATION_OWNER_KEY) || '').trim();
  if (!id || id.length > 64 || (legacyOwner && legacyOwner !== owner)) return '';
  return id;
};

const clearPushRegistration = () => {
  pushRegistrationID = '';
  pushRegistrationOwner = '';
  removeStorageValue(PUSH_REGISTRATION_ID_KEY, 'sessionStorage');
  removeStorageValue(PUSH_REGISTRATION_OWNER_KEY, 'sessionStorage');
};

export function setToken(nextToken) {
  token = nextToken;
  authRevision += 1;
  if (nextToken) writeStorageValue('oc_token', nextToken);
  else {
    removeStorageValue('oc_token');
    clearPushRegistration();
  }
  window.dispatchEvent(new CustomEvent('cc:auth-changed', {
    detail: {
      loggedIn: Boolean(nextToken),
      revision: authRevision,
    },
  }));
}

export function getToken() {
  return token;
}

export function getAuthRevision() {
  return authRevision;
}

export function isCurrentAuthSession(candidate, revision) {
  return Boolean(candidate)
    && Number.isInteger(revision)
    && token === candidate
    && authRevision === revision;
}

export function getPushRegistrationID() {
  return token ? registrationIDForToken(token) : '';
}

export function getPushCleanupRegistrationIDs() {
  const current = getPushRegistrationID();
  const legacy = legacyRegistrationIDForToken(token);
  return [...new Set([current, legacy].filter(Boolean))];
}

export function getPushPromptOwner() {
  return pushRegistrationOwnerForToken(token) || '';
}

export function isTokenExpired(candidate = token) {
  if (!candidate) return false;
  const payload = decodeTokenPayload(candidate);
  if (!payload) return false;
  try {
    const expiresAt = Number(payload.exp);
    return Number.isFinite(expiresAt) && Date.now() >= expiresAt * 1000;
  } catch {
    return false;
  }
}

export function statusMessage(status) {
  if (status === 400) return '请求内容有误，请检查后重试';
  if (status === 401) return '登录状态已失效，请重新登录';
  if (status === 403) return '当前账号没有执行此操作的权限';
  if (status === 404) return '请求的功能暂时不可用';
  if (status === 409) return '当前数据已发生变化，请刷新后重试';
  if (status === 429) return '操作过于频繁，请稍后再试';
  const failureKind = requestFailureKind({ status });
  if (failureKind === REQUEST_FAILURE_KIND.SERVICE_UNAVAILABLE) {
    return '服务暂时不可用，请稍后重试';
  }
  if (failureKind === REQUEST_FAILURE_KIND.SERVER_ERROR) return '服务请求失败，请稍后重试';
  return '请求失败，请稍后重试';
}

export async function request(method, path, body, options = {}) {
  const { signal, timeoutMs = 0 } = options;
  const headers = { 'Content-Type': 'application/json' };
  const authToken = options.authToken === undefined ? token : options.authToken;
  if (authToken) headers.Authorization = `Bearer ${authToken}`;

  const controller = new AbortController();
  let timedOut = false;
  let timeoutID = null;
  const abortFromCaller = () => controller.abort(signal?.reason);

  if (signal?.aborted) {
    abortFromCaller();
  } else if (signal) {
    signal.addEventListener('abort', abortFromCaller, { once: true });
  }
  if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
    timeoutID = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, timeoutMs);
  }

  let res;
  try {
    res = await fetch(`${API_BASE}${path}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
      signal: controller.signal,
    });

    let data = {};
    try {
      data = await res.json();
    } catch {
      data = {};
    }
    if (!res.ok) {
      const error = new Error(data.error || statusMessage(res.status));
      error.status = res.status;
      error.data = data;
      throw error;
    }
    return data;
  } catch (cause) {
    if (timedOut) {
      const error = new Error('请求超时，请稍后重试');
      error.code = REQUEST_ERROR_CODE.TIMEOUT;
      error.cause = cause;
      throw error;
    }
    if (signal?.aborted || cause?.name === 'AbortError') {
      const error = new Error('请求已取消');
      error.code = REQUEST_ERROR_CODE.ABORTED;
      error.cause = cause;
      throw error;
    }
    if (cause?.status) throw cause;
    const error = new Error(
      globalThis.navigator?.onLine === false
        ? '当前无网络连接，连接网络后再试'
        : '暂时无法连接服务，请稍后重试',
    );
    error.code = REQUEST_ERROR_CODE.NETWORK;
    error.cause = cause;
    throw error;
  } finally {
    if (timeoutID) clearTimeout(timeoutID);
    signal?.removeEventListener('abort', abortFromCaller);
  }
}

export const authApi = {
  sendVerificationCode: (email) => request('POST', '/api/auth/send-code', { email }),
  sendPasswordResetCode: (email) => request('POST', '/api/auth/reset-password/send-code', { email }),
  resetPassword: (data) => request('POST', '/api/auth/reset-password', data),
  register: (data) => request('POST', '/api/auth/register', data),
  login: (data) => request('POST', '/api/auth/login', data),
};
