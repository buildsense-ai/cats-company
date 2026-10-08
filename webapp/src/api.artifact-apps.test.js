import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

beforeEach(() => { vi.stubEnv('VITE_ARTIFACT_APPS_CATALOG', ''); });

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
  vi.resetModules();
  localStorage.clear();
});

describe('shared app list transport', () => {
  test.each([true, false])('explicit public compatibility mode is development-only: %s', async (development) => {
    vi.stubEnv('DEV', development);
    vi.stubEnv('VITE_ARTIFACT_APPS_CATALOG', 'public');
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ apps: [{ id: 'demo', can_manage: true }] }) });
    vi.stubGlobal('fetch', fetchMock);
    const { api, setToken } = await import('./api');
    setToken('test-token');
    const result = await api.listArtifactApps(42);
    const [url, options] = fetchMock.mock.calls[0];
    expect(url).toBe(development ? '/artifact-gateway/api/apps?agent=42' : '/api/artifacts/apps?agent=42');
    expect(result.apps[0].can_manage).toBe(!development);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    if (development) expect(options).toBeUndefined();
    else expect(options.headers.Authorization).toBe('Bearer test-token');
  });
  test.each([true, false])('uses the correct same-origin list in development=%s', async (development) => {
    vi.stubEnv('DEV', development);
    vi.resetModules();
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ apps: [{ id: 'demo' }] }) });
    vi.stubGlobal('fetch', fetchMock);
    const { api, setToken } = await import('./api');
    setToken('test-token');
    await expect(api.listArtifactApps(42)).resolves.toEqual({ apps: [{ id: 'demo' }] });
    const [url, options] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/artifacts/apps?agent=42');
    expect(options.headers.Authorization).toBe('Bearer test-token');
  });

  test('an older dev backend falls back to a read-only public catalog without forwarding credentials', async () => {
    vi.stubEnv('DEV', true);
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: false, status: 404, json: async () => ({ error: 'not_found' }) })
      .mockResolvedValueOnce({ ok: true, json: async () => ({ apps: [{ id: 'demo', can_manage: true }] }) });
    vi.stubGlobal('fetch', fetchMock);
    const { api, setToken } = await import('./api');
    setToken('test-token');
    expect(await api.listArtifactApps(42)).toEqual({ apps: [{ id: 'demo', can_manage: false }] });
    expect(fetchMock.mock.calls[1]).toEqual(['/artifact-gateway/api/apps?agent=42']);
  });

  test('does not fall back when the backend denies access', async () => {
    vi.stubEnv('DEV', true);
    const fetchMock = vi.fn().mockResolvedValue({ ok: false, status: 403, json: async () => ({ error: 'forbidden' }) });
    vi.stubGlobal('fetch', fetchMock);
    const { api, setToken } = await import('./api');
    setToken('test-token');
    await expect(api.listArtifactApps(42)).rejects.toMatchObject({ status: 403 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  test('sends presentation edits to the authenticated platform endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ id: 'demo', title: 'Updated' }) });
    vi.stubGlobal('fetch', fetchMock);
    const { api, setToken } = await import('./api');
    setToken('test-token');
    await api.updateArtifactApp('demo', { title: 'Updated', description: '', icon_url: '' });
    const [url, options] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/artifacts/apps/demo');
    expect(options.method).toBe('PATCH');
    expect(options.headers.Authorization).toBe('Bearer test-token');
    expect(JSON.parse(options.body)).toEqual({ title: 'Updated', description: '', icon_url: '' });
  });
});
