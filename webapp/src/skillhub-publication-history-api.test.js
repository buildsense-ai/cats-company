import { api, setToken } from './api';

const id = `pubentry_${'a'.repeat(32)}`;
beforeEach(() => { setToken('human-history-token'); vi.stubGlobal('fetch', vi.fn()); });
afterEach(() => { setToken(null); vi.unstubAllGlobals(); vi.useRealTimers(); });

it('reads only the exact local history route with human auth and no mutation payload', async () => {
  const value = { schemaVersion: 1, entryRequestId: id, descriptionReleaseHistory: { stage: 'not_recorded' } };
  fetch.mockResolvedValue({ ok: true, json: async () => value });
  expect(await api.getSkillHubPublicationDescriptionReleaseHistory(id)).toEqual(value);
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(fetch.mock.calls[0][0]).toBe(`/api/skillhub/publications/entries/${id}/description-release-history`);
  expect(fetch.mock.calls[0][1]).toMatchObject({ method: 'GET', headers: { Authorization: 'Bearer human-history-token' } });
  expect(fetch.mock.calls[0][1].body).toBeUndefined();
});

it.each(['../secret', `${id}/commit`, '', 'pubentry_bad', null])('refuses malformed browser hints without making requests: %s', async value => {
  await expect(api.getSkillHubPublicationDescriptionReleaseHistory(value)).rejects.toThrow('标识无效');
  expect(fetch).not.toHaveBeenCalled();
});

it('preserves cancellation and a bounded header timeout without retrying or falling back', async () => {
  vi.useFakeTimers();
  fetch.mockImplementation((_, options) => new Promise((resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new DOMException('abort', 'AbortError')));
  }));
  const controller = new AbortController();
  const first = api.getSkillHubPublicationDescriptionReleaseHistory(id, { signal: controller.signal }).catch(error => error);
  controller.abort();
  expect(await first).toMatchObject({ code: 'REQUEST_ABORTED' });
  const second = api.getSkillHubPublicationDescriptionReleaseHistory(id).catch(error => error);
  await vi.advanceTimersByTimeAsync(7000);
  expect(await second).toMatchObject({ code: 'REQUEST_TIMEOUT' });
  expect(fetch).toHaveBeenCalledTimes(2);
});

it('does not replay a failed status read against any legacy or Cloud endpoint', async () => {
  fetch.mockResolvedValue({ ok: false, status: 503, json: async () => ({ error: { code: 'publication.read_unavailable' } }) });
  await expect(api.getSkillHubPublicationDescriptionReleaseHistory(id)).rejects.toMatchObject({ status: 503 });
  expect(fetch).toHaveBeenCalledTimes(1);
});
