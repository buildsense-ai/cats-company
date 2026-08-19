import {
  LIQUID_THEME_UNLOCK_STORAGE_KEY,
  THEME_STORAGE_KEY,
  isLiquidTheme,
  isLiquidThemeUnlocked,
  normalizeTheme,
  saveLiquidThemeUnlock,
  syncThemeColor,
  themeColor,
  verifyLiquidThemePassword,
} from './theme-access';

describe('theme access', () => {
  it('normalizes supported themes and rejects unknown stored values', () => {
    expect(normalizeTheme('light')).toBe('light');
    expect(normalizeTheme('dark')).toBe('dark');
    expect(normalizeTheme('liquid')).toBe('liquid');
    expect(normalizeTheme('liquid-green')).toBe('liquid-green');
    expect(normalizeTheme('neon')).toBe('light');
    expect(isLiquidTheme('liquid')).toBe(true);
    expect(isLiquidTheme('liquid-green')).toBe(true);
    expect(isLiquidTheme('dark')).toBe(false);
  });

  it('matches browser chrome to every supported theme surface', () => {
    expect(themeColor('light')).toBe('#f8f8f8');
    expect(themeColor('dark')).toBe('#0f0f0f');
    expect(themeColor('liquid')).toBe('#f5f7fc');
    expect(themeColor('liquid-green')).toBe('#151718');
    expect(themeColor('neon')).toBe('#f8f8f8');
  });

  it('syncs the browser theme-color metadata', () => {
    const meta = document.createElement('meta');
    meta.name = 'theme-color';
    document.head.appendChild(meta);

    syncThemeColor('dark');

    expect(meta.content).toBe('#0f0f0f');
    meta.remove();
  });

  it('stores the local liquid-theme unlock without storing a password', () => {
    const values = new Map();
    const storage = {
      getItem: (key) => values.get(key) || null,
      setItem: (key, value) => values.set(key, value),
    };

    expect(isLiquidThemeUnlocked(storage)).toBe(false);
    expect(saveLiquidThemeUnlock(storage)).toBe(true);
    expect(values.get(LIQUID_THEME_UNLOCK_STORAGE_KEY)).toBe('1');
    expect(isLiquidThemeUnlocked(storage)).toBe(true);
  });

  it('restores access from an already saved liquid theme after a refresh', () => {
    const values = new Map([[THEME_STORAGE_KEY, 'liquid-green']]);
    const storage = {
      getItem: (key) => values.get(key) || null,
      setItem: (key, value) => values.set(key, value),
    };

    expect(values.has(LIQUID_THEME_UNLOCK_STORAGE_KEY)).toBe(false);
    expect(isLiquidThemeUnlocked(storage)).toBe(true);
    values.set(THEME_STORAGE_KEY, 'light');
    expect(isLiquidThemeUnlocked(storage)).toBe(false);
  });

  it('reports when the browser cannot persist the unlock marker', () => {
    const storage = {
      getItem: () => null,
      setItem: () => {
        throw new Error('storage blocked');
      },
    };

    expect(saveLiquidThemeUnlock(storage)).toBe(false);
  });

  it('rejects an empty password before using Web Crypto', async () => {
    expect(await verifyLiquidThemePassword('', null)).toBe(false);
  });
});
