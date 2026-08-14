import { isValidEmailFormat } from './email-format';

describe('isValidEmailFormat', () => {
  test('accepts common and less common real emails', () => {
    for (const email of [
      'user@qq.com',
      'a@example.com.cn',
      'dev@github.io',
      'user@outlook.com',
      'x@163.com',
      'someone@sub.example.dev',
      'upper@EXAMPLE.COM',
      'user@example.museum',        // real suffix outside any small allowlist
      'user@example.international', // real suffix outside any small allowlist
    ]) {
      expect(isValidEmailFormat(email)).toBe(true);
    }
  });

  test('lets structurally valid typo domains pass to the server (authoritative)', () => {
    // The client only checks structure; the server rejects non-real suffixes
    // (e.g. qq.cpm) via publicsuffix, so these must NOT be blocked client-side.
    for (const email of ['user@qq.cpm', 'user@example.c0m']) {
      expect(isValidEmailFormat(email)).toBe(true);
    }
  });

  test('rejects structurally invalid emails', () => {
    for (const email of [
      'user@localhost',   // no dotted domain
      '@qq.com',          // missing local part
      'no-at-sign',       // missing @
      'user@com',         // domain without a dot
      'user@.com',        // empty domain label
      '  ',               // blank
      '',                 // empty
    ]) {
      expect(isValidEmailFormat(email)).toBe(false);
    }
  });
});
