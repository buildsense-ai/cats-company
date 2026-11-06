import { api, setToken } from './api';

const id = `pubentry_${'a'.repeat(32)}`;
beforeEach(() => { setToken('human-repair-task-token'); vi.stubGlobal('fetch', vi.fn()); });
afterEach(() => { setToken(null); vi.unstubAllGlobals(); vi.useRealTimers(); });

it('only reads the existing task or sends exact schema/topic with human Bearer auth', async () => {
  fetch.mockResolvedValue({ ok: true, json: async () => ({ schemaVersion: 1 }) });
  await api.getSkillHubPublicationRepairTask(id);
  await api.admitSkillHubPublicationRepairTask(id, 'grp_77');
  const path = `/api/skillhub/publications/commit/entries/${id}/repair-task`;
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(fetch.mock.calls[0][0]).toBe(path);
  expect(fetch.mock.calls[0][1].body).toBeUndefined();
  expect(fetch.mock.calls[1][0]).toBe(path);
  expect(fetch.mock.calls[1][1]).toMatchObject({ method: 'POST', headers: { Authorization: 'Bearer human-repair-task-token' } });
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({ schemaVersion: 1, topicId: 'grp_77' });
});

it.each(['../secret', '', `${id}/commit`, null, [[id]]])('rejects malformed entries locally: %s', async value => {
  await expect(api.getSkillHubPublicationRepairTask(value)).rejects.toThrow();
  await expect(api.admitSkillHubPublicationRepairTask(value, 'grp_77')).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
it.each(['grp_0', 'grp_01', 'grp_77/commit', 'grp_9223372036854775808', '', null, [['grp_77']]])('rejects malformed topics locally: %s', async value => {
  await expect(api.admitSkillHubPublicationRepairTask(id, value)).rejects.toThrow();
  expect(fetch).not.toHaveBeenCalled();
});

it('does not replay or fall back on uncertain admission', async () => {
  fetch.mockResolvedValue({ ok: false, status: 503, json: async () => ({ error: { code: 'publication.repair_uncertain' } }) });
  await expect(api.admitSkillHubPublicationRepairTask(id, 'grp_77')).rejects.toMatchObject({ status: 503 });
  expect(fetch).toHaveBeenCalledTimes(1);
});

it('bounds GET headers to seven seconds but lets explicit POST outlive the server report budget', async () => {
  vi.useFakeTimers();
  fetch.mockImplementation((_, options) => new Promise((resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new DOMException('abort', 'AbortError')));
  }));
  const get = api.getSkillHubPublicationRepairTask(id).catch(e => e);
  await vi.advanceTimersByTimeAsync(7000);
  expect(await get).toMatchObject({ code: 'REQUEST_TIMEOUT' });
  const post = api.admitSkillHubPublicationRepairTask(id, 'grp_77').catch(e => e);
  const signal = fetch.mock.calls[1][1].signal;
  await vi.advanceTimersByTimeAsync(55000); expect(signal.aborted).toBe(false);
  await vi.advanceTimersByTimeAsync(5000); expect(await post).toMatchObject({ code: 'REQUEST_TIMEOUT' });
  expect(fetch).toHaveBeenCalledTimes(2);
});

it('preserves explicit cancellation without replaying', async () => {
  fetch.mockImplementation((_, options) => new Promise((resolve, reject) => {
    options.signal.addEventListener('abort', () => reject(new DOMException('abort', 'AbortError')));
  }));
  const controller = new AbortController();
  const post = api.admitSkillHubPublicationRepairTask(id, 'grp_77', { signal: controller.signal }).catch(e => e);
  controller.abort(); expect(await post).toMatchObject({ code: 'REQUEST_ABORTED' });
  expect(fetch).toHaveBeenCalledTimes(1);
});
