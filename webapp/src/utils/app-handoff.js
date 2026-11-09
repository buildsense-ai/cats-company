// Gateway handoff is deliberately independent of versioned artifact tasks.
export const HANDOFF_PROTOCOL = 'catsco.app-handoff.v1';
const plain = value => value !== null && typeof value === 'object' && !Array.isArray(value);
// Request identities only (no parameters) survive panel remounts in this tab.
const consumedRequests = new Set();
export function claimHandoffRequest(key) {
  if (consumedRequests.has(key)) return false;
  try { if (sessionStorage.getItem(`catsco.handoff:${key}`)) return false; sessionStorage.setItem(`catsco.handoff:${key}`, '1'); } catch { /* in-memory fallback */ }
  consumedRequests.add(key); return true;
}
// A denylist is only a format guard, not a privacy boundary or secret detector.
const sensitive = /password|passwd|token|secret|credential|private.?key|authorization|cookie|command|script/i;
const unsafeValue = /-----BEGIN|(?:https?|ssh|sftp):\/\/|bearer\s|[\r\n\x00-\x1f]/i;
export function safeParameters(parameters) {
  if (!plain(parameters) || Object.keys(parameters).length > 16) return false;
  if (JSON.stringify(parameters).length > 4096) return false;
  return Object.entries(parameters).every(([key, value]) => /^[a-z][a-z0-9_]{0,47}$/.test(key)
    && !sensitive.test(key) && (typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value))
      || (typeof value === 'string' && value.length <= 512 && !unsafeValue.test(value))));
}
export function normalizeHandoff(value) {
  if (!plain(value) || !/^[a-zA-Z0-9_-]{8,80}$/.test(value.request_id || '')
    || typeof value.topic_id !== 'string' || !value.topic_id || value.topic_id.length > 128
    || !['initialize', 'run'].includes(value.action) || !safeParameters(value.parameters)
    || !Number.isSafeInteger(value.expires_at) || value.expires_at <= 0
    // Agent assertion only; the receiving app must independently authorize execution.
    || (value.action === 'run' && value.user_requested_run !== true)) return null;
  return { request_id: value.request_id, topic_id: value.topic_id, action: value.action,
    parameters: { ...value.parameters }, expires_at: value.expires_at, user_requested_run: value.action === 'run' };
}
// A bounded, intentionally small schema dialect, advertised by the receiving app.
// No executable schemas, arbitrary JSON schema references, or dynamic code.
export function validateParameters(parameters, schema) {
  if (!safeParameters(parameters) || !plain(schema) || Object.keys(schema).length > 16) return false;
  for (const [key, spec] of Object.entries(schema)) {
    if (!plain(spec) || !['string', 'path', 'server_ref', 'number', 'boolean', 'enum'].includes(spec.type)) return false;
    if (spec.required === true && !Object.hasOwn(parameters, key)) return false;
  }
  return Object.entries(parameters).every(([key, value]) => {
    const spec = schema[key];
    if (!plain(spec)) return false;
    if (spec.type === 'boolean') return typeof value === 'boolean';
    if (spec.type === 'number') return typeof value === 'number' && Number.isFinite(value)
      && (spec.minimum === undefined || value >= spec.minimum) && (spec.maximum === undefined || value <= spec.maximum);
    if (typeof value !== 'string') return false;
    if (spec.type === 'enum') return Array.isArray(spec.values) && spec.values.length <= 32 && spec.values.includes(value);
    if (value.length > Math.min(512, spec.maxLength ?? 512)) return false;
    if (spec.type === 'server_ref') return /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$/.test(value);
    if (spec.type === 'path') return /^(\/|[a-zA-Z]:[\\/])/.test(value)
      && !value.split(/[\\/]/).includes('..') && !/[;&|`$<>]/.test(value);
    return spec.type === 'string';
  });
}
export function trustedAppOrigin(url) {
  try { const parsed = new URL(url); return parsed.protocol === 'https:' && !parsed.username && !parsed.password && !parsed.search && !parsed.hash ? parsed.origin : null; } catch { return null; }
}
// The panel is the single lifecycle owner. One hello, one payload, no retries.
// Every frame load retires the session; the caller never replays a sent request.
export function startAppHandoff({ host = window, frame, url, request, agentUid, topicId, onState, timeoutMs = 15000 }) {
  const origin = trustedAppOrigin(url);
  const normalized = normalizeHandoff(request);
  if (!origin || !normalized || !frame?.contentWindow || !Number.isSafeInteger(Number(agentUid)) || Number(agentUid) <= 0
    || normalized.topic_id !== String(topicId) || normalized.expires_at <= Date.now() || normalized.expires_at > Date.now() + 300000) {
    onState('invalid'); return () => {};
  }
  const child = frame.contentWindow;
  const nonce = crypto.randomUUID();
  let phase = 'ready'; let closed = false; let timer;
  function stop() { closed = true; clearTimeout(timer); host.removeEventListener('message', receive); }
  function finish(state) { if (closed) return; stop(); onState(state); }
  function send(data) { child.postMessage({ protocol: HANDOFF_PROTOCOL, nonce, request_id: normalized.request_id, ...data }, origin); }
  function receive(event) {
    if (closed || event.source !== child || event.origin !== origin) return;
    const data = event.data;
    if (!plain(data) || data.protocol !== HANDOFF_PROTOCOL || data.nonce !== nonce || data.request_id !== normalized.request_id) return;
    if (phase === 'ready' && data.type === 'ready') {
      // Timers may be throttled in background tabs; check the deadline at delivery.
      if (normalized.expires_at <= Date.now()) return finish('expired');
      if (normalized.action === 'run' && data.supports_run !== true) return finish('unsupported');
      if (!validateParameters(normalized.parameters, data.schema)) return finish('invalid_parameters');
      phase = 'ack'; onState('awaiting_ack');
      send({ type: 'handoff', topic_id: String(topicId), agent_uid: Number(agentUid), action: normalized.action, parameters: normalized.parameters });
    } else if (phase === 'ack' && data.type === 'ack') {
      if (data.status === 'failed' || data.status === 'unsupported') return finish(data.status);
      if (normalized.action === 'initialize' && data.status === 'initialized') return finish('initialized');
      if (normalized.action === 'run' && data.status === 'completed') return finish('completed');
      if (normalized.action === 'run' && data.status === 'running') onState('running');
    }
  }
  host.addEventListener('message', receive);
  timer = setTimeout(() => finish('timeout'), Math.min(timeoutMs, normalized.expires_at - Date.now()));
  onState('waiting_ready');
  try { send({ type: 'hello', topic_id: String(topicId), agent_uid: Number(agentUid) }); } catch { finish('failed'); }
  return stop;
}
export const HANDOFF_LABELS = {
  waiting_ready: '等待应用就绪，尚未交接', awaiting_ack: '参数已发送，等待应用确认',
  initialized: '应用已确认参数初始化，未运行', running: '应用报告正在运行，尚未完成', completed: '应用报告运行成功',
  failed: '应用报告交接或运行失败', unsupported: '应用不支持请求的运行操作，未运行',
  expired: '交接请求已过期，未发送参数',
  timeout: '应用确认超时，结果未知；不会自动重试', invalid: '交接绑定、有效期或应用地址无效，未发送',
  invalid_parameters: '参数与应用声明不匹配，未发送', reloaded: '应用已重载，交接已停止；不会重复发送',
};
