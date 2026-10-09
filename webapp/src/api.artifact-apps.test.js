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
    // The public catalog is bounded too: it is fetched while the artifact registry
    // is being rendered, so it carries the same timeout and caller signal. The
    // timeout is consumed by fetchWithRequestError and surfaces as an abortable
    // signal, which is what fetch actually receives.
    if (development) expect(options.signal).toBeDefined();
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
    const [url, options] = fetchMock.mock.calls[1];
    expect(url).toBe('/artifact-gateway/api/apps?agent=42');
    // Credentials must not be forwarded, but the bound must be.
    expect(options.headers).toBeUndefined();
    expect(options.signal).toBeDefined();
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

  test('bounds the platform catalog so a silent backend cannot stall the caller', async () => {
    vi.useFakeTimers();
    // The caller renders the artifact registry alongside this list, so a backend
    // that accepts the connection and never answers must not hold that render
    // open. Without a bound this promise never settles.
    const fetchMock = vi.fn((url, options) => new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('aborted');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
    }));
    vi.stubGlobal('fetch', fetchMock);
    const { api, setToken } = await import('./api');
    setToken('test-token');

    let settled = false;
    const request = api.listArtifactApps(42).catch((error) => { settled = true; throw error; });
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' });

    // Pin the value from both sides, so the test fails for any bound other than
    // the intended one rather than only for one that happens to cross 8s. The
    // bound is chosen from a measured cold list (~3.03s) with headroom, and is
    // deliberately shorter than the platform's own 10s upstream timeout.
    await vi.advanceTimersByTimeAsync(7_999);
    expect(settled).toBe(false);

    await vi.advanceTimersByTimeAsync(1);
    await rejection;
    expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true);
    vi.useRealTimers();
  });

  test('stops waiting on the platform catalog when the caller cancels', async () => {
    const fetchMock = vi.fn((url, options) => new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('aborted');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
    }));
    vi.stubGlobal('fetch', fetchMock);
    const { api, setToken } = await import('./api');
    setToken('test-token');

    const controller = new AbortController();
    const request = api.listArtifactApps(42, { signal: controller.signal });
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_ABORTED' });

    controller.abort();
    await rejection;
    expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true);
  });

  test('bounds the development public catalog too', async () => {
    vi.useFakeTimers();
    vi.stubEnv('DEV', true);
    vi.stubEnv('VITE_ARTIFACT_APPS_CATALOG', 'public');
    const fetchMock = vi.fn((url, options) => new Promise((resolve, reject) => {
      options.signal.addEventListener('abort', () => {
        const error = new Error('aborted');
        error.name = 'AbortError';
        reject(error);
      }, { once: true });
    }));
    vi.stubGlobal('fetch', fetchMock);
    const { api } = await import('./api');

    const request = api.listArtifactApps(42);
    const rejection = expect(request).rejects.toMatchObject({ code: 'REQUEST_TIMEOUT' });

    await vi.advanceTimersByTimeAsync(8000);
    await rejection;
    expect(fetchMock.mock.calls[0][0]).toBe('/artifact-gateway/api/apps?agent=42');
    expect(fetchMock.mock.calls[0][1].signal.aborted).toBe(true);
    vi.useRealTimers();
  });

});
