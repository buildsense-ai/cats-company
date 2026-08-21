import { statusMessage } from './auth-session';

describe('request status copy', () => {
  test('describes gateway failures without claiming the backend is broken', () => {
    expect(statusMessage(502)).toBe('服务暂时不可用，请稍后重试');
    expect(statusMessage(503)).toBe('服务暂时不可用，请稍后重试');
    expect(statusMessage(504)).toBe('服务暂时不可用，请稍后重试');
    expect(statusMessage(500)).toBe('服务请求失败，请稍后重试');
    expect(statusMessage(502)).not.toContain('后端');
  });
});
