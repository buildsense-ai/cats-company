import { afterEach, expect, test, vi } from 'vitest';
import { request, responseErrorMessage, statusMessage } from './auth-session';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('request status copy', () => {
  test('describes gateway failures without claiming the backend is broken', () => {
    expect(statusMessage(502)).toBe('服务暂时不可用，请稍后重试');
    expect(statusMessage(503)).toBe('服务暂时不可用，请稍后重试');
    expect(statusMessage(504)).toBe('服务暂时不可用，请稍后重试');
    expect(statusMessage(500)).toBe('服务请求失败，请稍后重试');
    expect(statusMessage(502)).not.toContain('后端');
  });

  test('describes HTTP 408 responses as timeouts even when a response detail exists', () => {
    expect(responseErrorMessage(408, 'Request failed')).toBe('请求超时，请稍后重试');
  });

  test.each([502, 503, 504])('uses gateway status copy instead of a JSON server detail for HTTP %i', async (status) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: false,
      status,
      json: vi.fn().mockResolvedValue({ error: '后端暂时无法载入' }),
    }));

    await expect(request('GET', '/api/messages')).rejects.toMatchObject({
      message: '服务暂时不可用，请稍后重试',
      status,
      data: { error: '后端暂时无法载入' },
    });
  });

  test('normalizes a rejected fetch as an unreachable service', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));

    await expect(request('GET', '/api/messages')).rejects.toMatchObject({
      code: 'NETWORK_ERROR',
      message: '暂时无法连接服务，请稍后重试',
    });
  });

  test('normalizes a timed-out fetch', async () => {
    vi.useFakeTimers();
    let rejectOnAbort;
    vi.stubGlobal('fetch', vi.fn((url, options) => new Promise((resolve, reject) => {
      rejectOnAbort = reject;
      options.signal.addEventListener('abort', () => {
        reject(Object.assign(new Error('aborted'), { name: 'AbortError' }));
      }, { once: true });
    })));

    try {
      const pending = request('GET', '/api/messages', undefined, { timeoutMs: 10 });
      const rejection = expect(pending).rejects.toMatchObject({
        code: 'REQUEST_TIMEOUT',
        message: '请求超时，请稍后重试',
      });
      await vi.advanceTimersByTimeAsync(10);
      await rejection;
      expect(rejectOnAbort).toBeTypeOf('function');
    } finally {
      vi.useRealTimers();
    }
  });
});
