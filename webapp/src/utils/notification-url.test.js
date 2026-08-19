import { describe, expect, it } from 'vitest';

import { sameOriginNotificationURL } from './notification-url';

describe('sameOriginNotificationURL', () => {
  const origin = 'https://app.catsco.test';

  it('allows relative and same-origin notification targets', () => {
    expect(sameOriginNotificationURL('/conversations/active', origin))
      .toBe('https://app.catsco.test/conversations/active');
    expect(sameOriginNotificationURL('https://app.catsco.test/settings', origin))
      .toBe('https://app.catsco.test/settings');
  });

  it.each([
    'https://phishing.example/login',
    '//phishing.example/login',
    'javascript:alert(1)',
    'https://[invalid',
    null,
  ])('falls back for unsafe target %s', (target) => {
    expect(sameOriginNotificationURL(target, origin)).toBe('https://app.catsco.test/');
  });
});
