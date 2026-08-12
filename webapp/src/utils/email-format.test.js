import { isValidEmailFormat } from './email-format';

describe('isValidEmailFormat', () => {
  test('accepts common real emails', () => {
    for (const email of [
      'user@qq.com',
      'a@example.com.cn',
      'dev@github.io',
      'user@outlook.com',
      'x@163.com',
      'someone@sub.example.dev',
      'upper@EXAMPLE.COM',
    ]) {
      expect(isValidEmailFormat(email)).toBe(true);
    }
  });

  test('rejects typo and structurally invalid emails', () => {
    for (const email of [
      'user@qq.cpm',      // the reported typo (cpm is not a TLD)
      'user@example.c0m', // typo TLD
      'user@localhost',   // no dotted domain
      '@qq.com',          // missing local part
      'no-at-sign',       // missing @
      'user@com',         // domain without a dot
      '  ',               // blank
      '',                 // empty
    ]) {
      expect(isValidEmailFormat(email)).toBe(false);
    }
  });
});
