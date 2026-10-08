import { setToken } from './auth-session';
import { imageFileBase64, marketplaceApi, marketplaceImage, MAX_IMAGE_BYTES } from './skillhub-marketplace-api';

const target = { skillId: 'alice/test', version: '1.0.0' };
const asset = `pa_${'a'.repeat(32)}`;
beforeEach(() => { setToken('human-jwt'); vi.stubGlobal('fetch', vi.fn()); });
afterEach(() => { setToken(null); vi.unstubAllGlobals(); vi.useRealTimers(); });

it('uses only the human Bearer and exact target, never cookies or query JWT', async () => {
  fetch.mockResolvedValue({ ok: true, json: async () => ({ presentation: null }) });
  await marketplaceApi.presentation(target);
  expect(fetch.mock.calls[0][0]).toBe('/api/skillhub/marketplace/presentations?skillId=alice%2Ftest&version=1.0.0');
  expect(fetch.mock.calls[0][1]).toMatchObject({ credentials: 'omit', cache: 'no-store', redirect: 'error', headers: { Authorization: 'Bearer human-jwt' } });
});

it('rejects a successful body delivered after account switching', async () => {
  fetch.mockResolvedValue({ ok: true, json: async () => { setToken('other-human'); return { presentation: {} }; } });
  await expect(marketplaceApi.presentation(target)).rejects.toMatchObject({ name: 'AbortError' });
});

it('times out a stalled JSON body after headers and makes no automatic retry', async () => {
  vi.useFakeTimers();
  fetch.mockImplementation(async (_, options) => ({ ok: true, json: () => new Promise((resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new DOMException('timeout', 'AbortError')));
  }) }));
  const result = expect(marketplaceApi.save({ ...target, expectedRevision: 0, content: {} })).rejects.toMatchObject({ name: 'AbortError' });
  await vi.advanceTimersByTimeAsync(30001); await result; expect(fetch).toHaveBeenCalledTimes(1);
});

it('preserves CAS conflict status without replaying a mutation', async () => {
  fetch.mockResolvedValue({ ok: false, status: 409, json: async () => ({ error: { code: 'presentation.revision_conflict' } }) });
  await expect(marketplaceApi.publish({ ...target, expectedRevision: 1 })).rejects.toMatchObject({ status: 409 });
  expect(fetch).toHaveBeenCalledTimes(1);
});

it('aborts JSON body consumption when the detail unmounts', async () => {
  const controller = new AbortController();
  fetch.mockImplementation(async (_, options) => ({ ok: true, json: () => new Promise((resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')));
  }) }));
  const result = expect(marketplaceApi.draft(target, controller.signal)).rejects.toMatchObject({ name: 'AbortError' });
  await Promise.resolve(); controller.abort(); await result;
});

it('refuses arbitrary asset URLs before fetch', async () => {
  await expect(marketplaceImage('https://example.org/private')).rejects.toThrow('标识无效'); expect(fetch).not.toHaveBeenCalled();
});

it('does not use privileged preview after public 404', async () => {
  fetch.mockResolvedValue({ ok: false, status: 404 });
  await expect(marketplaceImage(asset)).rejects.toThrow('暂不可用');
  expect(fetch.mock.calls[0][0]).toBe(`/api/skillhub/marketplace/assets/${asset}`); expect(fetch).toHaveBeenCalledTimes(1);
});

it('bounds image body even without Content-Length', async () => {
  const reader = { read: vi.fn().mockResolvedValue({ value: new Uint8Array(MAX_IMAGE_BYTES + 1), done: false }), cancel: vi.fn(), releaseLock: vi.fn() };
  fetch.mockResolvedValue({ ok: true, headers: new Headers({ 'Content-Type': 'image/webp' }), body: { getReader: () => reader } });
  await expect(marketplaceImage(asset)).rejects.toThrow('图片过大'); expect(reader.cancel).toHaveBeenCalled();
});

it('validates local files before reading them', async () => {
  await expect(imageFileBase64({ type: 'image/svg+xml', size: 10 })).rejects.toThrow('静态');
  await expect(imageFileBase64({ type: 'image/png', size: MAX_IMAGE_BYTES + 1 })).rejects.toThrow('2 MiB');
});
