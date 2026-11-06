import { api, setToken } from './api';

const id = `pubentry_${'a'.repeat(32)}`;
const key = 'b'.repeat(64);
beforeEach(() => { setToken('human-draft-token'); vi.stubGlobal('fetch', vi.fn()); });
afterEach(() => { setToken(null); vi.unstubAllGlobals(); vi.useRealTimers(); });

it('reads without a body and confirms only an exact key with the human Bearer token', async () => {
  fetch.mockResolvedValue({ ok: true, json: async () => ({ schemaVersion: 1 }) });
  await api.getSkillHubPublicationDescriptionDraft(id);
  await api.confirmSkillHubPublicationDescriptionDraft(id, key);
  expect(fetch).toHaveBeenCalledTimes(2);
  const path = `/api/skillhub/publications/commit/entries/${id}/repair-description`;
  expect(fetch.mock.calls[0][0]).toBe(path);
  expect(fetch.mock.calls[0][1].body).toBeUndefined();
  expect(fetch.mock.calls[1][0]).toBe(path);
  expect(fetch.mock.calls[1][1]).toMatchObject({ method: 'POST', headers: { Authorization: 'Bearer human-draft-token' } });
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ schemaVersion: 1, confirmationKey: key });
});

it.each(['../secret', '', `${id}/confirm`, null, [[id]]])('refuses invalid entries without requests: %s', async value => {
  await expect(api.getSkillHubPublicationDescriptionDraft(value)).rejects.toThrow();
  await expect(api.confirmSkillHubPublicationDescriptionDraft(value, key)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});

it.each(['', true, 'A'.repeat(64), 'b'.repeat(63), [[key]]])('refuses nonexact keys without requests: %s', async value => {
  await expect(api.confirmSkillHubPublicationDescriptionDraft(id, value)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});

it('does not automatically replay an uncertain POST', async () => {
  fetch.mockResolvedValue({ ok: false, status: 503, json: async () => ({ error: { code: 'publication.description_uncertain' } }) });
  await expect(api.confirmSkillHubPublicationDescriptionDraft(id, key)).rejects.toMatchObject({ status: 503 });
  expect(fetch).toHaveBeenCalledTimes(1);
});

it('preserves cancellation and bounds confirmation headers', async () => {
  vi.useFakeTimers();
  fetch.mockImplementation((_, options) => new Promise((resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new DOMException('abort', 'AbortError')));
  }));
  const controller = new AbortController();
  const first = api.getSkillHubPublicationDescriptionDraft(id, { signal: controller.signal }).catch(e => e);
  controller.abort();
  expect(await first).toMatchObject({ code: 'REQUEST_ABORTED' });
  const second = api.confirmSkillHubPublicationDescriptionDraft(id, key).catch(e => e);
  await vi.advanceTimersByTimeAsync(7000);
  expect(await second).toMatchObject({ code: 'REQUEST_TIMEOUT' });
  expect(fetch).toHaveBeenCalledTimes(2);
});
