export const THEME_STORAGE_KEY = 'catsco_theme';
export const LIQUID_THEME_UNLOCK_STORAGE_KEY = 'catsco_liquid_theme_unlocked_v1';

const LIQUID_THEME_PASSWORD_SALT = 'catsco-liquid-theme-v1';
const LIQUID_THEME_PASSWORD_ITERATIONS = 210000;
const LIQUID_THEME_PASSWORD_VERIFIER = 'q3V748DOo0hRhrrZ8n8N5T7yVPS0uQCFU4xRuO8CW1s=';

export function isLiquidTheme(value) {
  return value === 'liquid' || value === 'liquid-green';
}

export function normalizeTheme(value) {
  return value === 'dark' || isLiquidTheme(value) ? value : 'light';
}

export function isLiquidThemeUnlocked(storage = globalThis.localStorage) {
  try {
    if (storage?.getItem(LIQUID_THEME_UNLOCK_STORAGE_KEY) === '1') return true;
    return isLiquidTheme(storage?.getItem(THEME_STORAGE_KEY));
  } catch {
    return false;
  }
}

export function saveLiquidThemeUnlock(storage = globalThis.localStorage) {
  try {
    storage?.setItem(LIQUID_THEME_UNLOCK_STORAGE_KEY, '1');
    return storage?.getItem(LIQUID_THEME_UNLOCK_STORAGE_KEY) === '1';
  } catch {
    return false;
  }
}

function bytesToBase64(buffer) {
  const bytes = new Uint8Array(buffer);
  let binary = '';
  bytes.forEach((byte) => { binary += String.fromCharCode(byte); });
  return btoa(binary);
}

export async function verifyLiquidThemePassword(password, subtle = globalThis.crypto?.subtle) {
  const normalized = String(password || '').trim();
  if (!normalized || !subtle) return false;

  const encoder = new TextEncoder();
  const key = await subtle.importKey(
    'raw',
    encoder.encode(normalized),
    'PBKDF2',
    false,
    ['deriveBits'],
  );
  const derived = await subtle.deriveBits({
    name: 'PBKDF2',
    hash: 'SHA-256',
    salt: encoder.encode(LIQUID_THEME_PASSWORD_SALT),
    iterations: LIQUID_THEME_PASSWORD_ITERATIONS,
  }, key, 256);

  return bytesToBase64(derived) === LIQUID_THEME_PASSWORD_VERIFIER;
}
