import { describe, expect, it, vi } from 'vitest';
import { createFileMediaSession } from './file-media-session';

const deferred = () => {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
};
const flush = async () => { for (let i = 0; i < 12; i += 1) await Promise.resolve(); };

function fixture() {
  let token = 'alice';
  let revision = 1;
  let time = 0;
  let cookie = '';
  const held = [];
  const calls = [];
  const fetcher = vi.fn((_url, options) => {
    const response = deferred();
    const actor = options.headers.Authorization.slice('Bearer '.length);
    calls.push(`${options.method}:${actor}`);
    held.push(() => {
      cookie = options.method === 'POST' ? actor : '';
      response.resolve({ ok: true });
    });
    return response.promise;
  });
  const session = createFileMediaSession({
    current: (candidate, version) => candidate === token && version === revision,
    fetcher,
    now: () => time,
  });
  return {
    session, calls, fetcher,
    cookie: () => cookie,
    complete: async () => { expect(held.length).toBeGreaterThan(0); held.shift()(); await flush(); },
    advance: (ms) => { time += ms; },
    change: (next) => { const previous = token; token = next; revision += 1; return session.change(previous, next, revision); },
  };
}

describe('file media identity ordering', () => {
  it('deduplicates bootstrap and renews before the 15-minute expiry', async () => {
    const f = fixture();
    const first = f.session.ensure('alice', 1);
    expect(f.session.ensure('alice', 1)).toBe(first);
    await flush();
    expect(f.calls).toEqual(['POST:alice']);
    expect(f.fetcher.mock.calls[0][1]).toMatchObject({ credentials: 'include', cache: 'no-store' });
    await f.complete();
    await expect(first).resolves.toBe(true);
    await f.session.ensure('alice', 1);
    expect(f.calls).toHaveLength(1);
    f.advance(14 * 60 * 1000);
    const renewed = f.session.ensure('alice', 1);
    await flush();
    expect(f.calls).toEqual(['POST:alice', 'POST:alice']);
    await f.complete();
    await expect(renewed).resolves.toBe(true);
  });

  it('revokes a late POST before logout finishes', async () => {
    const f = fixture();
    const first = f.session.ensure('alice', 1);
    await flush();
    const logout = f.change(null);
    expect(f.calls).toEqual(['POST:alice']);
    await f.complete();
    expect(f.calls).toEqual(['POST:alice', 'DELETE:alice']);
    await f.complete();
    await expect(first).resolves.toBe(false);
    await f.complete();
    await logout;
    expect(f.cookie()).toBe('');
    await expect(f.session.ensure('alice', 1)).resolves.toBe(false);
    expect(f.calls).toEqual(['POST:alice', 'DELETE:alice', 'DELETE:alice']);
  });

  it('serializes account switches so an old response cannot overwrite the new actor', async () => {
    const f = fixture();
    const old = f.session.ensure('alice', 1);
    await flush();
    const switched = f.change('bob');
    await f.complete(); // delayed alice POST
    await f.complete(); // revoke delayed result
    await f.complete(); // revoke the old parent on switch
    expect(f.calls).toEqual(['POST:alice', 'DELETE:alice', 'DELETE:alice', 'POST:bob']);
    await f.complete();
    await expect(old).resolves.toBe(false);
    await expect(switched).resolves.toBe(true);
    expect(f.cookie()).toBe('bob');
  });

  it('skips an account superseded before its queued POST starts', async () => {
    const f = fixture();
    const old = f.session.ensure('alice', 1);
    await flush();
    const bob = f.change('bob');
    const carol = f.change('carol');
    for (let i = 0; i < 5; i += 1) await f.complete();
    await Promise.all([old, bob, carol]);
    expect(f.calls).not.toContain('POST:bob');
    expect(f.cookie()).toBe('carol');
  });

  it('retries failed establishment and does not cache an unsuccessful cookie', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce({ ok: false }).mockResolvedValue({ ok: true });
    const session = createFileMediaSession({ current: () => true, fetcher });
    await expect(session.ensure('alice', 1)).rejects.toThrow('File media session unavailable');
    await expect(session.ensure('alice', 1)).resolves.toBe(true);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});

describe('browser media bootstrap', () => {
  it('establishes the stored identity before returning the bootstrap API', async () => {
    vi.resetModules();
    const token = `header.${btoa(JSON.stringify({ userId: 7, exp: Math.floor(Date.now() / 1000) + 3600 }))}.signature`;
    localStorage.setItem('oc_token', token);
    const held = deferred();
    const fetcher = vi.fn((url) => String(url).endsWith('/media-session')
      ? held.promise : Promise.resolve({ ok: true, json: async () => ({ id: 7 }) }));
    vi.stubGlobal('fetch', fetcher);
    try {
      const auth = await import('./auth-session');
      const bootstrap = auth.request('GET', '/api/me');
      await flush();
      expect(fetcher).toHaveBeenCalledTimes(1);
      expect(fetcher.mock.calls[0][0]).toBe('/api/files/media-session');
      held.resolve({ ok: true });
      await expect(bootstrap).resolves.toEqual({ id: 7 });
      expect(fetcher.mock.calls[1][0]).toBe('/api/me');
      auth.setToken(null);
      await flush();
    } finally {
      localStorage.removeItem('oc_token');
      vi.unstubAllGlobals();
    }
  });

  it('keeps protected uploads on the cookie host despite a legacy media configuration', async () => {
    vi.resetModules();
    vi.stubEnv('VITE_MEDIA_BASE', 'https://i.catsco.cn');
    try {
      const api = await import('./api');
      expect(api.resolveMediaURL('/uploads/private.pdf')).toBe('/uploads/private.pdf');
      expect(api.resolveMediaURL('https://i.catsco.cn/uploads/a.png?size=2'))
        .toBe('/uploads/a.png?size=2');
      expect(api.resolveMediaURL('https://external.example/photo.png'))
        .toBe('https://external.example/photo.png');
    } finally {
      vi.unstubAllEnvs();
    }
  });
});
