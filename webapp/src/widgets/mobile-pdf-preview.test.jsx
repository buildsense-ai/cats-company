import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import MobilePdfPreview from './mobile-pdf-preview';

async function flushAsync() {
  await Promise.resolve();
  await Promise.resolve();
  await new Promise((resolve) => setTimeout(resolve, 0));
}

describe('MobilePdfPreview', () => {
  let container;
  let root;
  let getContext;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    getContext = vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({});
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    getContext.mockRestore();
    container.remove();
  });

  function createPdfFixture(pageCount = 3, dimensions = { width: 600, height: 800 }) {
    const renderTasks = [];
    const page = {
      getViewport: vi.fn(({ scale }) => ({ width: dimensions.width * scale, height: dimensions.height * scale })),
      render: vi.fn(() => {
        const task = { promise: Promise.resolve(), cancel: vi.fn() };
        renderTasks.push(task);
        return task;
      }),
    };
    const document = {
      numPages: pageCount,
      getPage: vi.fn().mockResolvedValue(page),
      destroy: vi.fn(),
    };
    return { document, page, renderTasks };
  }

  it('loads a document and supports page navigation, zoom controls, and fit width', async () => {
    const fixture = createPdfFixture();
    const loadDocument = vi.fn().mockResolvedValue(fixture.document);

    await act(async () => {
      root.render(<MobilePdfPreview url="/uploads/report.pdf" loadDocument={loadDocument} />);
      await flushAsync();
    });

    expect(loadDocument).toHaveBeenCalledWith(
      '/uploads/report.pdf',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(container.querySelector('.v3-mobile-pdf-page-count').textContent).toBe('1 / 3');
    expect(fixture.document.getPage).toHaveBeenCalledWith(1);
    expect(container.querySelector('canvas').getAttribute('aria-label')).toBe('PDF 第 1 页，共 3 页');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="下一页"]'));
      await flushAsync();
    });
    expect(container.querySelector('.v3-mobile-pdf-page-count').textContent).toBe('2 / 3');
    expect(fixture.document.getPage).toHaveBeenCalledWith(2);

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="放大"]'));
      await flushAsync();
    });
    expect(container.querySelector('.v3-mobile-pdf-zoom').textContent).toBe('125%');

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="适合宽度"]'));
      await flushAsync();
    });
    expect(container.querySelector('.v3-mobile-pdf-zoom').textContent).toBe('100%');
    expect(container.querySelector('button[aria-label="适合宽度"]').disabled).toBe(true);
  });

  it('pans with one pointer and commits one render after a pinch ends', async () => {
    const fixture = createPdfFixture();
    const loadDocument = vi.fn().mockResolvedValue(fixture.document);

    await act(async () => {
      root.render(<MobilePdfPreview url="/uploads/report.pdf" loadDocument={loadDocument} />);
      await flushAsync();
    });

    const viewport = container.querySelector('.v3-mobile-pdf-viewport');
    viewport.scrollLeft = 40;
    viewport.scrollTop = 50;
    await act(async () => {
      Simulate.pointerDown(viewport, { pointerType: 'touch', pointerId: 1, clientX: 100, clientY: 100 });
      Simulate.pointerMove(viewport, { pointerType: 'touch', pointerId: 1, clientX: 70, clientY: 50 });
      Simulate.pointerUp(viewport, { pointerType: 'touch', pointerId: 1, clientX: 70, clientY: 50 });
    });
    expect(viewport.scrollLeft).toBe(70);
    expect(viewport.scrollTop).toBe(100);

    const renderCountBeforePinch = fixture.page.render.mock.calls.length;
    await act(async () => {
      Simulate.pointerDown(viewport, { pointerType: 'touch', pointerId: 1, clientX: 100, clientY: 100 });
      Simulate.pointerDown(viewport, { pointerType: 'touch', pointerId: 2, clientX: 200, clientY: 100 });
      Simulate.pointerMove(viewport, { pointerType: 'touch', pointerId: 2, clientX: 250, clientY: 100 });
    });

    expect(container.querySelector('.v3-mobile-pdf-zoom').textContent).toBe('100%');
    expect(container.querySelector('canvas').style.transform).toBe('scale(1.5)');
    expect(fixture.page.render).toHaveBeenCalledTimes(renderCountBeforePinch);

    await act(async () => {
      Simulate.pointerUp(viewport, { pointerType: 'touch', pointerId: 2, clientX: 250, clientY: 100 });
      await flushAsync();
    });
    expect(container.querySelector('.v3-mobile-pdf-zoom').textContent).toBe('150%');
    expect(fixture.page.render).toHaveBeenCalledTimes(renderCountBeforePinch + 1);
  });

  it('cancels a pending PDF.js loading task when the preview closes', async () => {
    const neverResolves = new Promise(() => {});
    const loadingTask = { promise: neverResolves, destroy: vi.fn() };
    const loadDocument = vi.fn().mockResolvedValue(loadingTask);

    await act(async () => {
      root.render(<MobilePdfPreview url="/uploads/slow.pdf" loadDocument={loadDocument} />);
      await flushAsync();
    });
    await act(async () => root.unmount());
    expect(loadingTask.destroy).toHaveBeenCalledTimes(1);
    root = createRoot(container);
  });

  it('caps the backing canvas for extremely tall PDF pages', async () => {
    const fixture = createPdfFixture(1, { width: 600, height: 6_000_000 });

    await act(async () => {
      root.render(
        <MobilePdfPreview url="/uploads/tall.pdf" loadDocument={() => Promise.resolve(fixture.document)} />,
      );
      await flushAsync();
    });

    const canvas = container.querySelector('canvas');
    expect(canvas.width).toBeLessThanOrEqual(8192);
    expect(canvas.height).toBeLessThanOrEqual(8192);
    expect(canvas.width * canvas.height).toBeLessThanOrEqual(8_000_000);
  });

  it('keeps navigation available after a page render error', async () => {
    const fixture = createPdfFixture();
    fixture.page.render.mockImplementation(() => ({
      promise: Promise.reject(new Error('damaged page')),
      cancel: vi.fn(),
    }));

    await act(async () => {
      root.render(
        <MobilePdfPreview url="/uploads/damaged.pdf" loadDocument={() => Promise.resolve(fixture.document)} />,
      );
      await flushAsync();
    });

    expect(container.querySelector('[role="alert"]').textContent).toContain('damaged page');
    expect(container.querySelector('button[aria-label="下一页"]').disabled).toBe(false);
  });

  it('shows a useful load error and destroys a loaded document on unmount', async () => {
    const failedContainer = document.createElement('div');
    const failedRoot = createRoot(failedContainer);
    await act(async () => {
      failedRoot.render(
        <MobilePdfPreview
          url="/uploads/broken.pdf"
          loadDocument={() => Promise.reject(new Error('network unavailable'))}
        />,
      );
      await flushAsync();
    });
    expect(failedContainer.querySelector('[role="alert"]').textContent).toContain('network unavailable');
    await act(async () => failedRoot.unmount());

    const fixture = createPdfFixture();
    await act(async () => {
      root.render(
        <MobilePdfPreview url="/uploads/report.pdf" loadDocument={() => Promise.resolve(fixture.document)} />,
      );
      await flushAsync();
    });
    await act(async () => root.unmount());
    expect(fixture.document.destroy).toHaveBeenCalledTimes(1);
    root = createRoot(container);
  });
});
