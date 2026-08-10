import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import PwaDownloadLink from './pwa-download-link';

describe('PwaDownloadLink', () => {
  let container;
  let root;
  let clickSpy;
  let createObjectURL;
  let revokeObjectURL;

  beforeEach(() => {
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: true });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:test');
    revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    clickSpy.mockRestore();
    createObjectURL.mockRestore();
    revokeObjectURL.mockRestore();
    vi.restoreAllMocks();
  });

  test('downloads the blob without navigating the installed PWA', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: () => Promise.resolve(new Blob(['pdf'])),
    });

    await act(async () => {
      root.render(
        <PwaDownloadLink href="/uploads/files/report.pdf" download="report.pdf">
          下载
        </PwaDownloadLink>,
      );
    });

    await act(async () => {
      Simulate.click(container.querySelector('a'));
      await Promise.resolve();
    });

    expect(global.fetch).toHaveBeenCalledWith('/uploads/files/report.pdf', { credentials: 'include' });
    expect(createObjectURL).toHaveBeenCalledWith(expect.any(Blob));
    expect(clickSpy).toHaveBeenCalled();
    expect(container.querySelector('a').getAttribute('target')).toBeNull();
    expect(window.location.pathname).not.toBe('/uploads/files/report.pdf');
  });

  test('keeps the app and exposes a return-home fallback when blob download fails', async () => {
    global.fetch = vi.fn().mockRejectedValue(new Error('CORS blocked'));

    await act(async () => {
      root.render(<PwaDownloadLink href="https://oss.catsco.cc/report.pdf">下载</PwaDownloadLink>);
    });
    await act(async () => {
      Simulate.click(container.querySelector('a'));
      await Promise.resolve();
    });

    expect(document.querySelector('[role="alert"]').textContent).toContain('下载未开始');
    expect(document.querySelector('[role="alert"] a[href="/"]').textContent).toBe('返回 CatsCo');
    expect(document.querySelector('[role="alert"] a[target="_blank"]')).not.toBeNull();
  });

  test('does not send app cookies to an external object-storage URL', async () => {
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: () => Promise.resolve(new Blob(['pdf'])),
    });

    await act(async () => {
      root.render(<PwaDownloadLink href="https://oss.catsco.cc/report.pdf">下载</PwaDownloadLink>);
    });
    await act(async () => {
      Simulate.click(container.querySelector('a'));
      await Promise.resolve();
    });

    expect(global.fetch).toHaveBeenCalledWith('https://oss.catsco.cc/report.pdf', { credentials: 'omit' });
  });

  test('does not fetch the current app when a download URL is unavailable', async () => {
    global.fetch = vi.fn();

    await act(async () => {
      root.render(<PwaDownloadLink>下载</PwaDownloadLink>);
    });
    await act(async () => Simulate.click(container.querySelector('a')));

    expect(global.fetch).not.toHaveBeenCalled();
  });

  test('preserves ordinary browser new-tab behavior', async () => {
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
    global.fetch = vi.fn();

    await act(async () => {
      root.render(
        <PwaDownloadLink href="/uploads/files/report.pdf" target="_blank" rel="noopener noreferrer">
          下载
        </PwaDownloadLink>,
      );
    });
    await act(async () => Simulate.click(container.querySelector('a')));

    expect(global.fetch).not.toHaveBeenCalled();
    expect(container.querySelector('a').getAttribute('target')).toBe('_blank');
  });
});
