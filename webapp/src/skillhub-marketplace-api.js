import { API_BASE, getAuthRevision, getToken, isCurrentAuthSession, responseErrorMessage } from './auth-session';

const BASE = '/api/skillhub/marketplace';
const ASSET_ID = /^pa_[a-f0-9]{32}$/;
export const MAX_IMAGE_BYTES = 2 * 1024 * 1024;

// This client never uses the selected Bot, the local Dashboard, or query tokens.
// Capture the human session for each operation, including response-body reads.
export function marketplaceSession() {
  const token = getToken();
  const revision = getAuthRevision();
  return () => isCurrentAuthSession(token, revision);
}

async function json(method, path, body, signal) {
  const current = marketplaceSession();
  if (!current()) throw new Error('请先登录真人账号');
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (signal?.aborted) abort();
  signal?.addEventListener('abort', abort, { once: true });
  // Keep the timeout active through JSON body consumption, not only headers.
  const timer = setTimeout(abort, 30000);
  try {
    const response = await fetch(`${API_BASE}${BASE}${path}`, {
      method, signal: controller.signal, credentials: 'omit', cache: 'no-store', redirect: 'error',
      headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${getToken()}` },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const result = await response.json();
    if (controller.signal.aborted || !current()) throw new DOMException('请求已取消或登录账号已变化', 'AbortError');
    if (!response.ok) {
      const error = new Error(responseErrorMessage(response.status));
      error.status = response.status; error.data = result; throw error;
    }
    return result;
  } finally { clearTimeout(timer); signal?.removeEventListener('abort', abort); }
}

const targetQuery = (target) => new URLSearchParams({ skillId: target.skillId, version: target.version });
export const marketplaceApi = {
  capabilities: (signal) => json('GET', '/capabilities', undefined, signal),
  catalogue: (params, signal) => json('GET', `/catalogue/skills?${new URLSearchParams(params)}`, undefined, signal),
  presentation: (target, signal) => json('GET', `/presentations?${targetQuery(target)}`, undefined, signal),
  draft: (target, signal) => json('GET', `/presentations/draft?${targetQuery(target)}`, undefined, signal),
  save: (body, signal) => json('PUT', '/presentations/draft', body, signal),
  publish: (body, signal) => json('POST', '/presentations/publish', body, signal),
  unpublish: (body, signal) => json('POST', '/presentations/unpublish', body, signal),
  upload: (body, signal) => json('POST', '/assets', body, signal),
};

export async function marketplaceImage(assetId, { preview = false, signal } = {}) {
  if (!ASSET_ID.test(assetId)) throw new Error('图片标识无效');
  const current = marketplaceSession();
  if (!current()) throw new Error('请先登录真人账号');
  const response = await fetch(`${API_BASE}${BASE}/assets/${assetId}${preview ? '/preview' : ''}`, {
    headers: { Authorization: `Bearer ${getToken()}` },
    credentials: 'omit', cache: 'no-store', redirect: 'error', signal,
  });
  if (!response.ok || response.headers.get('Content-Type')?.split(';')[0] !== 'image/webp') {
    throw new Error('图片暂不可用');
  }
  if (Number(response.headers.get('Content-Length')) > MAX_IMAGE_BYTES) throw new Error('图片过大');
  // Bound streamed responses too; a missing Content-Length is not permission
  // to buffer arbitrary upstream data in the browser.
  const reader = response.body.getReader();
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.byteLength;
      if (size > MAX_IMAGE_BYTES) throw new Error('图片过大');
      chunks.push(value);
    }
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
  if (!current()) throw new DOMException('登录账号已变化', 'AbortError');
  return new Blob(chunks, { type: 'image/webp' });
}

export async function imageFileBase64(file) {
  if (!file || file.size > MAX_IMAGE_BYTES || !['image/png', 'image/jpeg', 'image/webp'].includes(file.type)) {
    throw new Error('请选择不超过 2 MiB 的静态 PNG、JPEG 或 WebP 图片');
  }
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = '';
  for (let offset = 0; offset < bytes.length; offset += 8192) {
    binary += String.fromCharCode(...bytes.subarray(offset, offset + 8192));
  }
  return btoa(binary);
}
