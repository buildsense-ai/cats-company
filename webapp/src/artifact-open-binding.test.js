import { describe, expect, test, vi, afterEach } from 'vitest';
import { normalizeArtifactOpenBinding, artifactOpenBindingUsable, createGatewayAnnotationHost } from './gateway-annotations';

const open = (ref = 'aob_123456789012345678901234') => ({
  contract_version: 'catsco.artifact-open-binding.v1', open_ref: ref,
  topic_id: 'topic-A', agent_uid: 7, app_id: 'board',
  app_origin: 'https://artifact.catsco.cc', expires_at: '2099-01-01T00:00:00Z',
});
afterEach(() => vi.unstubAllGlobals());
describe('parent-only open binding', () => {
  test('rejects guest, malformed, unsafe agent and invalid origin; expires closed', () => {
    expect(normalizeArtifactOpenBinding(null)).toBeNull();
    for (const patch of [{ open_ref: 'guess' }, { agent_uid: 0 }, { topic_id: '' },
      { app_origin: 'https://artifact.catsco.cc/path' }, { expires_at: 'bad' }]) {
      expect(normalizeArtifactOpenBinding({ ...open(), ...patch })).toBeNull();
    }
    const expired = { ...open(), expires_at: '2000-01-01T00:00:00Z' };
    expect(normalizeArtifactOpenBinding(expired)).not.toBeNull();
    expect(artifactOpenBindingUsable(expired)).toBe(false);
  });
  test('refresh rotates document session without exposing open; same app new open revokes old session', () => {
    const postMessage = vi.fn();
    let binding = { agentUid: 7, appId: 'board', url: open().app_origin,
      frame: { contentWindow: { postMessage } }, openBinding: open() };
    const host = createGatewayAnnotationHost({ getBinding: () => binding });
    host.connect(); const original = host.sessionToken;
    host.connect(); expect(host.sessionToken).not.toBe(original);
    expect(binding.openBinding.open_ref).toBe(open().open_ref);
    expect(JSON.stringify(postMessage.mock.calls)).not.toContain('aob_');
    binding = { ...binding, openBinding: open('aob_other_open_123456789012') };
    expect(host.setMode('element')).toBe(false);
    expect(host.hasSession()).toBe(false);
    host.dispose();
  });
  test('bound API has no ordinary fallback; logout revokes using original auth', async () => {
    vi.resetModules();
    const fetchMock = vi.fn(async (url) => ({ ok: true, status: 200,
      json: async () => url.endsWith('/launch') ? { open_binding: open() } : { seq_id: 1 } }));
    vi.stubGlobal('fetch', fetchMock);
    const module = await import('./api');
    module.setToken('old-auth');
    await module.api.requestArtifactLaunch({ app: 'board', topic_id: 'topic-A' });
    await module.api.sendArtifactAnnotations({ open_ref: open().open_ref, content: 'review' });
    expect(fetchMock.mock.calls.at(-1)[0]).toBe('/api/artifacts/annotations');
    module.setToken(null);
    expect(fetchMock.mock.calls.at(-1)[0]).toBe(`/api/artifacts/open-bindings/${open().open_ref}`);
    expect(fetchMock.mock.calls.at(-1)[1].method).toBe('DELETE');
    expect(fetchMock.mock.calls.at(-1)[1].headers.Authorization).toBe('Bearer old-auth');
    expect(fetchMock.mock.calls.some(([url]) => url.includes('/messages/send'))).toBe(false);
  });
});
