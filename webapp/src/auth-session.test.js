import { afterEach, expect, test, vi } from 'vitest';
import { request, statusMessage } from './auth-session';

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
});
