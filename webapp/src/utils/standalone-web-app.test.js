import { isStandaloneWebApp } from './standalone-web-app';

describe('isStandaloneWebApp', () => {
  beforeEach(() => {
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn(() => ({ matches: false })),
    });
  });

  test('recognizes the iOS standalone flag', () => {
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: true });

    expect(isStandaloneWebApp()).toBe(true);
  });

  test('recognizes the standard display-mode media query', () => {
    window.matchMedia.mockReturnValue({ matches: true });

    expect(isStandaloneWebApp()).toBe(true);
    expect(window.matchMedia).toHaveBeenCalledWith('(display-mode: standalone)');
  });

  test('does not treat an ordinary browser tab as standalone', () => {
    expect(isStandaloneWebApp()).toBe(false);
  });
});
