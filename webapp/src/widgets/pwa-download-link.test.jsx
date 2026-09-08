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
    document.getElementById('catsco-pwa-download-notices')?.remove();
    clickSpy.mockRestore();
    createObjectURL.mockRestore();
    revokeObjectURL.mockRestore();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  test('downloads the blob without navigating the installed PWA', async () => {
    const onDownloadStart = vi.fn();
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      blob: () => Promise.resolve(new Blob(['pdf'])),
    });

    await act(async () => {
      root.render(
        <PwaDownloadLink href="/uploads/files/report.pdf" download="report.pdf" onDownloadStart={onDownloadStart}>
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
    expect(onDownloadStart).toHaveBeenCalledTimes(1);
    expect(container.querySelector('a').getAttribute('target')).toBeNull();
    expect(window.location.pathname).not.toBe('/uploads/files/report.pdf');
    expect(document.querySelector('[role="status"]').textContent).toContain('已发起下载');
    expect(document.querySelector('[role="status"]').textContent).not.toContain('下载完成');
  });

  test('keeps the app and exposes a return-home fallback when a same-origin blob download fails', async () => {
    const onDownloadStart = vi.fn();
    global.fetch = vi.fn().mockRejectedValue(new Error('CORS blocked'));

    await act(async () => {
      root.render(
        <PwaDownloadLink href="/uploads/files/report.pdf" onDownloadStart={onDownloadStart}>
          下载
        </PwaDownloadLink>,
      );
    });
    await act(async () => {
      Simulate.click(container.querySelector('a'));
      await Promise.resolve();
    });

    expect(document.querySelector('[role="alert"]').textContent).toContain('下载未开始');
    expect(document.querySelector('[role="alert"] a[href="/"]').textContent).toBe('返回 CatsCo');
    expect(document.querySelector('[role="alert"] a[target="_blank"]')).not.toBeNull();
    expect(onDownloadStart).not.toHaveBeenCalled();
  });

  test('uses native new-tab downloads for external object-storage URLs', async () => {
    global.fetch = vi.fn();
    const onDownloadStart = vi.fn();

    await act(async () => {
      root.render(
        <PwaDownloadLink
          href="https://oss.catsco.cc/report.pdf"
          target="_blank"
          rel="noopener noreferrer"
          onDownloadStart={onDownloadStart}
        >
          下载
        </PwaDownloadLink>,
      );
    });
    await act(async () => Simulate.click(container.querySelector('a')));

    expect(global.fetch).not.toHaveBeenCalled();
    expect(container.querySelector('a').getAttribute('target')).toBe('_blank');
    expect(onDownloadStart).toHaveBeenCalledTimes(1);
  });

  test('stacks notices from concurrent downloads in one host', async () => {
    global.fetch = vi.fn().mockImplementation(() => new Promise(() => {}));

    await act(async () => {
      root.render(
        <>
          <PwaDownloadLink href="/uploads/files/first.pdf">第一个</PwaDownloadLink>
          <PwaDownloadLink href="/uploads/files/second.pdf">第二个</PwaDownloadLink>
        </>,
      );
    });
    await act(async () => {
      container.querySelectorAll('a').forEach((link) => Simulate.click(link));
    });

    const host = document.getElementById('catsco-pwa-download-notices');
    expect(host.querySelectorAll('[role="status"]')).toHaveLength(2);
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

  test('shows the native download filename briefly and restarts the notice timer on another click', async () => {
    vi.useFakeTimers();
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
    await act(async () => root.render(<PwaDownloadLink href="/report.pdf" download="报告.pdf">下载</PwaDownloadLink>));
    await act(async () => Simulate.click(container.querySelector('a')));
    expect(document.querySelector('[role="status"]').textContent).toBe('已发起下载报告.pdf');
    await act(async () => vi.advanceTimersByTime(2000));
    await act(async () => Simulate.click(container.querySelector('a')));
    await act(async () => vi.advanceTimersByTime(1000));
    expect(document.querySelector('[role="status"]')).not.toBeNull();
    await act(async () => vi.advanceTimersByTime(1400));
    expect(document.querySelector('[role="status"]')).toBeNull();
  });

  test('keeps a dismissed preparation busy and prevents duplicate downloads until it settles', async () => {
    let finish;
    global.fetch = vi.fn().mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    const onDownloadStart = vi.fn();
    await act(async () => root.render(<PwaDownloadLink href="/report.pdf" onDownloadStart={onDownloadStart}>下载</PwaDownloadLink>));
    await act(async () => {
      Simulate.click(container.querySelector('a'));
      Simulate.click(container.querySelector('a'));
    });
    expect(global.fetch).toHaveBeenCalledTimes(1);
    expect(document.querySelector('[role="status"]').textContent).toContain('正在准备下载');
    await act(async () => Simulate.click(document.querySelector('[aria-label="关闭下载提示"]')));
    expect(document.querySelector('[role="status"]')).toBeNull();
    expect(container.querySelector('a').getAttribute('aria-busy')).toBe('true');
    await act(async () => Simulate.click(container.querySelector('a')));
    expect(global.fetch).toHaveBeenCalledTimes(1);
    await act(async () => finish({ ok: true, blob: async () => new Blob(['pdf']) }));
    expect(container.querySelector('a').getAttribute('aria-busy')).toBeNull();
    expect(document.querySelector('[role="status"]')).toBeNull();
    expect(onDownloadStart).toHaveBeenCalledTimes(1);
  });

  test('keeps failure feedback until dismissal and permits a successful retry', async () => {
    vi.useFakeTimers();
    global.fetch = vi.fn().mockResolvedValueOnce({ ok: false, status: 503 })
      .mockResolvedValueOnce({ ok: true, blob: async () => new Blob(['pdf']) });
    await act(async () => root.render(<PwaDownloadLink href="/report.pdf">下载</PwaDownloadLink>));
    await act(async () => Simulate.click(container.querySelector('a')));
    await act(async () => vi.advanceTimersByTime(5000));
    expect(document.querySelector('[role="alert"]').textContent).toContain('report.pdf');
    await act(async () => Simulate.click(container.querySelector('a')));
    expect(document.querySelector('[role="alert"]')).toBeNull();
    expect(document.querySelector('[role="status"]').textContent).toContain('已发起下载');
  });

  test('does not create download feedback when the caller cancels the click', async () => {
    global.fetch = vi.fn();
    await act(async () => root.render(<PwaDownloadLink href="/report.pdf" onClick={(event) => event.preventDefault()}>下载</PwaDownloadLink>));
    await act(async () => Simulate.click(container.querySelector('a')));
    expect(global.fetch).not.toHaveBeenCalled();
    expect(document.getElementById('catsco-pwa-download-notices')).toBeNull();
  });
});
