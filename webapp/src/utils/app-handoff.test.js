import { describe, it, expect, vi } from 'vitest';
import { normalizeHandoff, validateParameters, startAppHandoff } from './app-handoff';

const request = { request_id: 'request-123', topic_id: 'p2p_7_9', action: 'initialize', parameters: { data_path: '/data/sample', server: 'compute-1' }, expires_at: Date.now() + 60000 };
const schema = { data_path: { type: 'path', required: true }, server: { type: 'server_ref' } };
function harness(r = request) {
  const listeners = new Set();
  const host = { addEventListener: (_, cb) => listeners.add(cb), removeEventListener: (_, cb) => listeners.delete(cb) };
  const child = { postMessage: vi.fn() };
  const states = [];
  const stop = startAppHandoff({ host, frame: { contentWindow: child }, url: 'https://app.example/', request: r, agentUid: 9, topicId: 'p2p_7_9', onState: s => states.push(s), timeoutMs: 100 });
  const hello = child.postMessage.mock.calls[0]?.[0];
  const receive = (data, overrides = {}) => [...listeners].forEach(cb => cb({ source: child, origin: 'https://app.example', data: { protocol: 'catsco.app-handoff.v1', nonce: hello.nonce, request_id: r.request_id, ...data }, ...overrides }));
  return { child, states, stop, receive, listeners };
}
describe('handoff safety and lifecycle', () => {
  it('bounds parameters and refuses credential keys, URLs and private keys', () => {
    expect(normalizeHandoff(request)).toBeTruthy();
    for (const parameters of [{ token: 'secret' }, { server: 'ssh://u:pass@host' }, { key: '-----BEGIN PRIVATE KEY-----' }, { nested: {} }, { command: 'rm -rf /' }]) expect(normalizeHandoff({ ...request, parameters })).toBeNull();
  });
  it('validates declared schema and rejects unknown/invalid parameters', () => {
    expect(validateParameters(request.parameters, schema)).toBe(true);
    expect(validateParameters({ ...request.parameters, unknown: 1 }, schema)).toBe(false);
    expect(validateParameters({ data_path: '../secret' }, schema)).toBe(false);
    expect(validateParameters({}, schema)).toBe(false);
  });
  it('only sends parameters after exact source/origin/nonce ready and never twice', () => {
    const h = harness();
    h.receive({ type: 'ready', schema }, { origin: 'https://evil.example' });
    h.receive({ type: 'ready', schema }, { source: {} });
    h.receive({ type: 'ready', schema, nonce: 'wrong' });
    expect(h.child.postMessage).toHaveBeenCalledTimes(1);
    h.receive({ type: 'ready', schema }); h.receive({ type: 'ready', schema });
    expect(h.child.postMessage).toHaveBeenCalledTimes(2);
    expect(h.child.postMessage.mock.calls[1][1]).toBe('https://app.example');
    h.receive({ type: 'ack', status: 'initialized' });
    expect(h.states.at(-1)).toBe('initialized');
    expect(h.listeners.size).toBe(0);
  });
  it('delivers the authenticated identity and deadline required by strict receivers', () => {
    const h = harness();
    h.receive({ type: 'ready', schema });
    const payload = h.child.postMessage.mock.calls[1][0];
    expect(payload.expires_at).toBe(request.expires_at);
    expect(payload.agent_uid).toBe('9');
    expect(Object.keys(payload).sort()).toEqual(['protocol', 'type', 'nonce', 'request_id', 'topic_id', 'agent_uid', 'action', 'parameters', 'expires_at'].sort());
    h.stop();
  });
  it('run requires an Agent intent assertion and application capability, not execution authorization', () => {
    expect(normalizeHandoff({ ...request, action: 'run' })).toBeNull();
    const h = harness({ ...request, action: 'run', user_requested_run: true });
    h.receive({ type: 'ready', schema, supports_run: false });
    expect(h.states.at(-1)).toBe('unsupported'); expect(h.child.postMessage).toHaveBeenCalledTimes(1);
  });
  it('run acceptance is not completion; requires completed or failed result', () => {
    const h = harness({ ...request, action: 'run', user_requested_run: true });
    h.receive({ type: 'ready', schema, supports_run: true }); h.receive({ type: 'ack', status: 'running' });
    expect(h.states.at(-1)).toBe('running'); expect(h.listeners.size).toBe(1);
    h.receive({ type: 'ack', status: 'completed' }); expect(h.states.at(-1)).toBe('completed'); expect(h.listeners.size).toBe(0);
  });
  it('rechecks expiry on ready when the clock advances without timer execution', () => {
    vi.useFakeTimers();
    try {
      const now = Date.now();
      const h = harness({ ...request, expires_at: now + 50 });
      vi.setSystemTime(now + 51); // Deliberately do NOT advance timers.
      expect(h.states.at(-1)).toBe('waiting_ready');
      h.receive({ type: 'ready', schema });
      expect(h.states.at(-1)).toBe('expired');
      expect(h.child.postMessage).toHaveBeenCalledTimes(1); // hello only
      expect(h.listeners.size).toBe(0);
      vi.runAllTimers();
      expect(h.states.at(-1)).toBe('expired');
    } finally { vi.useRealTimers(); }
  });
  it('does not mistake a scalar denylist for a secret detector', () => {
    expect(normalizeHandoff({ ...request, parameters: { label: 'opaque-sensitive-value' } })).toBeTruthy();
  });
  it('times out explicitly and cleanup ignores late responses', () => {
    vi.useFakeTimers(); const h = harness(); vi.advanceTimersByTime(101);
    expect(h.states.at(-1)).toBe('timeout'); expect(h.listeners.size).toBe(0);
    const h2 = harness(); h2.stop(); h2.receive({ type: 'ready', schema }); expect(h2.child.postMessage).toHaveBeenCalledTimes(1); vi.useRealTimers();
  });
  it('fails closed on binding mismatch, expiry and unsafe catalog URL', () => {
    for (const r of [{ ...request, topic_id: 'other' }, { ...request, expires_at: 1 }]) { const h = harness(r); expect(h.states.at(-1)).toBe('invalid'); expect(h.child.postMessage).not.toHaveBeenCalled(); }
  });
});
