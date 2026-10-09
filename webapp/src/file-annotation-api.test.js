import { describe, it, expect, vi, afterEach } from 'vitest';

const source = { topic_id: 'p2p_7_9', message_id: 42, attachment_index: 1,
  name: 'demo.mp4', url: '/uploads/files/demo.mp4', file_key: 'demo.mp4',
  mime_type: 'video/mp4', type: 'file', size: 100, version: 'a'.repeat(64) };
const binding = () => ({ contract_version: 'catsco.file-open-binding.v1',
  open_ref: `fob_${'b'.repeat(43)}`, topic_id: source.topic_id,
  expires_at: '2099-01-01T00:00:00Z', source: { ...source } });
const response = data => ({ ok: true, status: 200, json: async () => data });
afterEach(() => vi.unstubAllGlobals());

describe('file annotation API session ownership', () => {
  it('uses dedicated binding and submit endpoints and revokes with the original auth token', async () => {
    vi.resetModules();
    const fetch = vi.fn(async url => response(url.endsWith('/open-bindings') ? binding() : { id: 99 }));
    vi.stubGlobal('fetch', fetch);
    const module = await import('./api');
    module.setToken('file-original-session');
    await module.api.openFileAnnotationBinding(source);
    expect(fetch.mock.calls[0][0]).toBe('/api/files/open-bindings');
    expect(JSON.parse(fetch.mock.calls[0][1].body)).toEqual({ topic_id: source.topic_id, message_id: 42, attachment_index: 1 });
    const payload = { open_ref: binding().open_ref, client_msg_id: 'fa_send_1', file_annotations: { contract_version: 'catsco.file-annotations.v1', source, annotations: [] } };
    await module.api.sendFileAnnotations(payload);
    expect(fetch.mock.calls.at(-1)[0]).toBe('/api/files/annotations');
    expect(JSON.parse(fetch.mock.calls.at(-1)[1].body)).toEqual(payload);
    module.setToken(null);
    expect(fetch.mock.calls.at(-1)[0]).toBe(`/api/files/open-bindings/${binding().open_ref}`);
    expect(fetch.mock.calls.at(-1)[1].headers.Authorization).toBe('Bearer file-original-session');
    expect(fetch.mock.calls.some(([url]) => url.includes('messages/send'))).toBe(false);
  });
  it('rejects binding responses for another topic or attachment, or with an expired reference', async () => {
    vi.resetModules();
    let returned = binding();
    vi.stubGlobal('fetch', vi.fn(async () => response(returned)));
    const { api } = await import('./api');
    for (const patch of [
      { topic_id: 'p2p_7_10' },
      { source: { ...source, attachment_index: 0 } },
      { source: { ...source, version: 'opaque-client-version' } },
      { expires_at: '2000-01-01T00:00:00Z' },
    ]) {
      returned = { ...binding(), ...patch };
      await expect(api.openFileAnnotationBinding(source)).rejects.toThrow('文件会话绑定无效');
    }
  });
  it('releases a binding that finishes opening after logout without returning it to the editor', async () => {
    vi.resetModules();
    let resolveOpen;
    const fetch = vi.fn(url => url.endsWith('/open-bindings')
      ? new Promise(resolve => { resolveOpen = () => resolve(response(binding())); })
      : Promise.resolve(response({})));
    vi.stubGlobal('fetch', fetch);
    const module = await import('./api');
    module.setToken('late-file-original-session');
    const opening = module.api.openFileAnnotationBinding(source);
    module.setToken(null);
    resolveOpen();
    await expect(opening).rejects.toThrow('登录状态已变化');
    expect(fetch.mock.calls.at(-1)[0]).toBe(`/api/files/open-bindings/${binding().open_ref}`);
    expect(fetch.mock.calls.at(-1)[1].headers.Authorization).toBe('Bearer late-file-original-session');
  });
});
