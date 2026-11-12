import { afterEach, describe, expect, test, vi } from 'vitest';
import { gatewayAppAllowsBindingOrigin } from './gateway-app-origin';
import { GATEWAY_METADATA_TIMEOUT_MS, loadGatewayArtifactMetadata } from './gateway-artifact-metadata';

const app = { id: 'demo', url: 'https://artifact.catsco.cc/demo/',
  urls: ['https://artifact.catsco.cc/demo/', 'https://artifact.catsco.cn/demo/'] };

describe('gateway binding origins', () => {
  test.each(['cc', 'cn'])('accepts the advertised %s origin when launch agrees', (domain) => {
    const origin = `https://artifact.catsco.${domain}`;
    expect(gatewayAppAllowsBindingOrigin(app, origin, `${origin}/_launch/code`)).toBe(true);
  });
  test.each([
    [{ ...app, urls: [] }, 'https://artifact.catsco.cn', 'https://artifact.catsco.cn/_launch/code'],
    [app, 'https://evil.example', 'https://evil.example/_launch/code'],
    [app, 'https://artifact.catsco.cn', 'https://artifact.catsco.cc/_launch/code'],
    [{ ...app, urls: ['https://artifact.catsco.cn/other/'] }, 'https://artifact.catsco.cn', 'https://artifact.catsco.cn/_launch/code'],
    [{ ...app, urls: ['https://artifact.catsco.cn/demo/?next=evil'] }, 'https://artifact.catsco.cn', 'https://artifact.catsco.cn/_launch/code'],
    [{ ...app, urls: ['http://artifact.catsco.cn/demo/'] }, 'http://artifact.catsco.cn', 'http://artifact.catsco.cn/_launch/code'],
    [app, 'https://artifact.catsco.cn', 'https://user:pass@artifact.catsco.cn/_launch/code'],
    [app, 'https://artifact.catsco.cn', 'not a URL'],
  ])('rejects unadvertised, mismatched or unsafe origins (%#)', (candidate, origin, launch) => {
    expect(gatewayAppAllowsBindingOrigin(candidate, origin, launch)).toBe(false);
  });
});

describe('optional gateway metadata', () => {
  afterEach(() => vi.useRealTimers());
  test('finds only the matching versioned record and bypasses the media barrier', async () => {
    const record = { id: 'demo', publish_version: 3 };
    const api = { getCloudArtifacts: vi.fn().mockResolvedValue({ artifacts: [{ id: 'other' }, record] }) };
    await expect(loadGatewayArtifactMetadata(api, 440, 'demo')).resolves.toBe(record);
    expect(api.getCloudArtifacts).toHaveBeenCalledWith(440, 'active', {
      signal: expect.any(AbortSignal), timeoutMs: 2500, waitForMediaSession: false,
    });
    await expect(loadGatewayArtifactMetadata(api, 440, 'missing')).resolves.toBeNull();
  });
  test('a permanently pending lookup settles at the deadline and aborts its signal', async () => {
    vi.useFakeTimers();
    const api = { getCloudArtifacts: vi.fn(() => new Promise(() => {})) };
    let settled = false;
    const pending = loadGatewayArtifactMetadata(api, 440, 'demo').then((value) => { settled = true; return value; });
    await vi.advanceTimersByTimeAsync(GATEWAY_METADATA_TIMEOUT_MS - 1);
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await expect(pending).resolves.toBeNull();
    expect(api.getCloudArtifacts.mock.calls[0][2].signal.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
  test('errors degrade to no metadata rather than failing application launch', async () => {
    const api = { getCloudArtifacts: vi.fn().mockRejectedValue(new Error('offline')) };
    await expect(loadGatewayArtifactMetadata(api, 440, 'demo')).resolves.toBeNull();
  });
});
