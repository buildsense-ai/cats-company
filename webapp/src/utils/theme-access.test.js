import {
  LIQUID_THEME_UNLOCK_STORAGE_KEY,
  isLiquidThemeUnlocked,
  normalizeTheme,
  saveLiquidThemeUnlock,
  verifyLiquidThemePassword,
} from './theme-access';

describe('theme access', () => {
  it('normalizes supported themes and rejects unknown stored values', () => {
    expect(normalizeTheme('light')).toBe('light');
    expect(normalizeTheme('dark')).toBe('dark');
    expect(normalizeTheme('liquid')).toBe('liquid');
    expect(normalizeTheme('neon')).toBe('light');
  });

  it('stores the local liquid-theme unlock without storing a password', () => {
    const values = new Map();
    const storage = {
      getItem: (key) => values.get(key) || null,
      setItem: (key, value) => values.set(key, value),
    };

    expect(isLiquidThemeUnlocked(storage)).toBe(false);
    saveLiquidThemeUnlock(storage);
    expect(values.get(LIQUID_THEME_UNLOCK_STORAGE_KEY)).toBe('1');
    expect(isLiquidThemeUnlocked(storage)).toBe(true);
  });

  it('rejects an empty password before using Web Crypto', async () => {
    expect(await verifyLiquidThemePassword('', null)).toBe(false);
  });
});
