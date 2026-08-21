import { beforeEach, describe, expect, test, vi } from 'vitest';
import {
  getPwaInstallState,
  promptPwaInstall,
  startPwaInstallLifecycle,
  subscribePwaInstall,
} from './pwa-install';

beforeEach(() => {
  globalThis.__CATSCO_PWA_INSTALL__ = undefined;
  Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
  Object.defineProperty(navigator, 'userAgent', { configurable: true, value: 'Desktop Browser' });
  window.matchMedia = vi.fn(() => ({ matches: false }));
});

describe('PWA installation lifecycle', () => {
  test('captures the browser install event and exposes a user-triggered prompt', async () => {
    const prompt = vi.fn().mockResolvedValue(undefined);
    const listener = vi.fn();
    startPwaInstallLifecycle();
    const unsubscribe = subscribePwaInstall(listener);

    const event = new Event('beforeinstallprompt', { cancelable: true });
    event.prompt = prompt;
    event.userChoice = Promise.resolve({ outcome: 'accepted' });
    window.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(getPwaInstallState().canPrompt).toBe(true);
    expect(listener).toHaveBeenCalled();

    await expect(promptPwaInstall()).resolves.toBe('accepted');
    expect(prompt).toHaveBeenCalledTimes(1);
    unsubscribe();
  });

  test('identifies iOS where manual home-screen installation is required', () => {
    Object.defineProperty(navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1',
    });

    startPwaInstallLifecycle();

    expect(getPwaInstallState()).toEqual(expect.objectContaining({
      installed: false,
      requiresManualIOSInstall: true,
    }));
  });
});
