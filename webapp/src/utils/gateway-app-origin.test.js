import { describe, expect, test } from 'vitest';
import { gatewayAppAllowsBindingOrigin } from './gateway-app-origin';

const app = {
  id: 'demo',
  url: 'https://artifact.catsco.cc/demo/',
  urls: ['https://artifact.catsco.cc/demo/', 'https://artifact.catsco.cn/demo/'],
};

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
