import React, { useEffect, useId, useRef, useState } from 'react';
import { ChevronLeft, ChevronRight, Maximize2, ZoomIn, ZoomOut } from 'lucide-react';

const MIN_ZOOM = 0.5;
const MAX_ZOOM = 3;
const ZOOM_STEP = 0.25;
const MAX_CANVAS_PIXELS = 8_000_000;
const MAX_CANVAS_DIMENSION = 8192;
const MAX_LAYOUT_PIXELS = 8_000_000;
const MAX_LAYOUT_DIMENSION = 8192;

let pdfJsPromise;

function clampZoom(value) {
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, value));
}

function pointerDistance(points) {
  const [first, second] = points;
  return Math.hypot(second.clientX - first.clientX, second.clientY - first.clientY);
}

function boundedPageViewport(page, scale) {
  const requestedViewport = page.getViewport({ scale });
  const layoutPixelScale = Math.sqrt(
    MAX_LAYOUT_PIXELS / Math.max(1, requestedViewport.width * requestedViewport.height),
  );
  const layoutDimensionScale = Math.min(
    MAX_LAYOUT_DIMENSION / Math.max(1, requestedViewport.width),
    MAX_LAYOUT_DIMENSION / Math.max(1, requestedViewport.height),
  );
  const boundedScale = Math.min(1, layoutPixelScale, layoutDimensionScale);
  return boundedScale < 1 ? page.getViewport({ scale: scale * boundedScale }) : requestedViewport;
}

function canvasOutputScale(viewport) {
  const requestedScale = Math.min(window.devicePixelRatio || 1, 2);
  const pixelBudgetScale = Math.sqrt(MAX_CANVAS_PIXELS / Math.max(1, viewport.width * viewport.height));
  const dimensionScale = Math.min(
    MAX_CANVAS_DIMENSION / Math.max(1, viewport.width),
    MAX_CANVAS_DIMENSION / Math.max(1, viewport.height),
  );
  return Math.max(Number.EPSILON, Math.min(requestedScale, pixelBudgetScale, dimensionScale));
}

function clearCanvas(canvas) {
  if (!canvas) return;
  canvas.width = 0;
  canvas.height = 0;
  canvas.style.width = '0px';
  canvas.style.height = '0px';
  canvas.style.transform = '';
  canvas.style.transformOrigin = '';
}

function clearTextLayer(container) {
  container?.replaceChildren?.();
}

function pageTextFromContent(textContent) {
  if (!Array.isArray(textContent?.items)) return '';
  return textContent.items
    .map((item) => {
      if (!item || typeof item.str !== 'string') return '';
      return `${item.str}${item.hasEOL ? '\n' : ' '}`;
    })
    .join('')
    .replace(/[ \t]+\n/g, '\n')
    .replace(/[ \t]{2,}/g, ' ')
    .trim();
}

async function loadPdfJs() {
  if (!pdfJsPromise) {
    pdfJsPromise = Promise.all([
      import('pdfjs-dist/legacy/build/pdf.mjs'),
      import('pdfjs-dist/legacy/build/pdf.worker.min.mjs?url'),
    ]).then(([pdfjs, workerModule]) => {
      pdfjs.GlobalWorkerOptions.workerSrc = workerModule.default;
      return pdfjs;
    }).catch((error) => {
      pdfJsPromise = null;
      throw error;
    });
  }
  return pdfJsPromise;
}

async function defaultLoadDocument(url, { signal } = {}) {
  const pdfjs = await loadPdfJs();
  if (signal?.aborted) return null;
  return {
    loadingTask: pdfjs.getDocument({ url, withCredentials: true }),
    TextLayer: pdfjs.TextLayer,
  };
}

export default function MobilePdfPreview({ url, loadDocument = defaultLoadDocument }) {
  const canvasRef = useRef(null);
  const viewportRef = useRef(null);
  const textLayerContainerRef = useRef(null);
  const renderTaskRef = useRef(null);
  const textLayerTaskRef = useRef(null);
  const textLayerConstructorRef = useRef(null);
  const pointersRef = useRef(new Map());
  const panRef = useRef(null);
  const pinchRef = useRef(null);
  const hintId = useId();
  const [pdfDocument, setPdfDocument] = useState(null);
  const [pageNumber, setPageNumber] = useState(1);
  const [pageCount, setPageCount] = useState(0);
  const [zoom, setZoom] = useState(1);
  const [viewportWidth, setViewportWidth] = useState(0);
  const [loadState, setLoadState] = useState('loading');
  const [renderState, setRenderState] = useState('idle');
  const [error, setError] = useState('');

  function resetPointerInteraction() {
    pointersRef.current.clear();
    panRef.current = null;
    pinchRef.current = null;
    if (canvasRef.current) {
      canvasRef.current.style.transform = '';
      canvasRef.current.style.transformOrigin = '';
    }
  }

  useEffect(() => {
    let cancelled = false;
    let loadingTask = null;
    let loadedDocument = null;
    const loadController = new AbortController();

    renderTaskRef.current?.cancel?.();
    renderTaskRef.current = null;
    textLayerTaskRef.current?.cancel?.();
    textLayerTaskRef.current = null;
    textLayerConstructorRef.current = null;
    resetPointerInteraction();
    clearCanvas(canvasRef.current);
    clearTextLayer(textLayerContainerRef.current);
    setPdfDocument(null);
    setPageNumber(1);
    setPageCount(0);
    setZoom(1);
    setError('');
    setLoadState('loading');
    setRenderState('idle');

    const loadPdf = async () => {
      try {
        const loadResult = await loadDocument(url, { signal: loadController.signal });
        if (!loadResult) return;
        if (loadResult?.loadingTask) {
          loadingTask = loadResult.loadingTask;
          textLayerConstructorRef.current = loadResult.TextLayer || null;
        } else if (loadResult?.promise && typeof loadResult.promise.then === 'function') {
          loadingTask = loadResult;
        } else {
          loadedDocument = loadResult;
        }
        if (loadingTask) {
          if (cancelled) {
            await loadingTask.destroy?.();
            return;
          }
          loadedDocument = await loadingTask.promise;
        }
        if (cancelled) {
          if (loadingTask?.destroy) await loadingTask.destroy();
          else await loadedDocument?.destroy?.();
          return;
        }
        setPdfDocument(loadedDocument);
        setPageCount(loadedDocument?.numPages || 0);
        setLoadState('ready');
      } catch (loadError) {
        if (cancelled) return;
        setError(`PDF 加载失败：${loadError?.message || '未知错误'}`);
        setLoadState('error');
      }
    };

    loadPdf();
    return () => {
      cancelled = true;
      loadController.abort();
      renderTaskRef.current?.cancel?.();
      textLayerTaskRef.current?.cancel?.();
      if (loadingTask?.destroy) loadingTask.destroy();
      else loadedDocument?.destroy?.();
    };
  }, [loadDocument, url]);

  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return undefined;

    const measure = () => setViewportWidth(viewport.clientWidth);
    measure();
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(measure) : null;
    observer?.observe(viewport);
    window.addEventListener('resize', measure);
    return () => {
      observer?.disconnect();
      window.removeEventListener('resize', measure);
    };
  }, []);

  useEffect(() => {
    if (!pdfDocument || !canvasRef.current) return undefined;

    let cancelled = false;
    setRenderState('rendering');
    setError('');

    const renderPage = async () => {
      try {
        const page = await pdfDocument.getPage(pageNumber);
        if (cancelled) return;
        const baseViewport = page.getViewport({ scale: 1 });
        const availableWidth = Math.max((viewportWidth || viewportRef.current?.clientWidth || 320) - 24, 240);
        const fitScale = availableWidth / Math.max(1, baseViewport.width);
        const viewport = boundedPageViewport(page, fitScale * zoom);
        const outputScale = canvasOutputScale(viewport);
        const canvas = canvasRef.current;
        const context = canvas.getContext('2d', { alpha: false });
        const textLayerContainer = textLayerContainerRef.current;

        resetPointerInteraction();
        textLayerTaskRef.current?.cancel?.();
        clearTextLayer(textLayerContainer);
        canvas.width = Math.max(1, Math.floor(viewport.width * outputScale));
        canvas.height = Math.max(1, Math.floor(viewport.height * outputScale));
        canvas.style.width = `${Math.max(1, Math.floor(viewport.width))}px`;
        canvas.style.height = `${Math.max(1, Math.floor(viewport.height))}px`;

        renderTaskRef.current?.cancel?.();
        const renderTask = page.render({
          canvasContext: context,
          viewport,
          transform: outputScale === 1 ? null : [outputScale, 0, 0, outputScale, 0, 0],
        });
        renderTaskRef.current = renderTask;

        const TextLayer = textLayerConstructorRef.current;
        if (TextLayer && textLayerContainer && typeof page.streamTextContent === 'function') {
          const textLayerTask = new TextLayer({
            textContentSource: page.streamTextContent({ includeMarkedContent: true }),
            container: textLayerContainer,
            viewport,
          });
          textLayerTaskRef.current = textLayerTask;
          await Promise.all([renderTask.promise, textLayerTask.render()]);
        } else {
          await renderTask.promise;
          const textContent = await page.getTextContent?.({ includeMarkedContent: true });
          if (!cancelled && textLayerContainer) {
            const pageText = pageTextFromContent(textContent);
            if (pageText) {
              const paragraph = document.createElement('p');
              paragraph.textContent = pageText;
              textLayerContainer.replaceChildren(paragraph);
            }
          }
        }
        if (!cancelled) setRenderState('ready');
      } catch (renderError) {
        if (cancelled || renderError?.name === 'RenderingCancelledException') return;
        setError(`PDF 页面渲染失败：${renderError?.message || '未知错误'}`);
        setRenderState('error');
      }
    };

    renderPage();
    return () => {
      cancelled = true;
      renderTaskRef.current?.cancel?.();
      renderTaskRef.current = null;
      textLayerTaskRef.current?.cancel?.();
      textLayerTaskRef.current = null;
    };
  }, [pageNumber, pdfDocument, viewportWidth, zoom]);

  const changeZoom = (nextZoom) => setZoom(clampZoom(nextZoom));
  const changePage = (nextPage) => {
    setPageNumber(Math.min(pageCount, Math.max(1, nextPage)));
    viewportRef.current?.scrollTo?.({ top: 0, left: 0 });
  };

  const handlePointerDown = (event) => {
    if (event.pointerType !== 'touch' || pointersRef.current.size >= 2) return;
    const point = { clientX: event.clientX, clientY: event.clientY };
    pointersRef.current.set(event.pointerId, point);
    event.currentTarget.setPointerCapture?.(event.pointerId);

    if (pointersRef.current.size === 1) {
      panRef.current = {
        pointerId: event.pointerId,
        clientX: event.clientX,
        clientY: event.clientY,
        scrollLeft: event.currentTarget.scrollLeft,
        scrollTop: event.currentTarget.scrollTop,
      };
    } else {
      panRef.current = null;
      const points = Array.from(pointersRef.current.values());
      pinchRef.current = { distance: pointerDistance(points), zoom, targetZoom: zoom };
    }
    if (event.cancelable) event.preventDefault();
  };

  const handlePointerMove = (event) => {
    if (!pointersRef.current.has(event.pointerId)) return;
    pointersRef.current.set(event.pointerId, { clientX: event.clientX, clientY: event.clientY });

    if (pointersRef.current.size === 1 && panRef.current?.pointerId === event.pointerId) {
      event.currentTarget.scrollLeft = panRef.current.scrollLeft - (event.clientX - panRef.current.clientX);
      event.currentTarget.scrollTop = panRef.current.scrollTop - (event.clientY - panRef.current.clientY);
    } else if (pointersRef.current.size === 2 && pinchRef.current) {
      const distance = pointerDistance(Array.from(pointersRef.current.values()));
      const ratio = distance / Math.max(1, pinchRef.current.distance);
      const targetZoom = clampZoom(Math.round(pinchRef.current.zoom * ratio * 20) / 20);
      pinchRef.current.targetZoom = targetZoom;
      if (canvasRef.current) {
        canvasRef.current.style.transformOrigin = 'center top';
        canvasRef.current.style.transform = `scale(${targetZoom / pinchRef.current.zoom})`;
      }
    }
    if (event.cancelable) event.preventDefault();
  };

  const handlePointerEnd = (event) => {
    if (!pointersRef.current.has(event.pointerId)) return;
    pointersRef.current.delete(event.pointerId);
    event.currentTarget.releasePointerCapture?.(event.pointerId);

    if (pinchRef.current && pointersRef.current.size < 2) {
      const targetZoom = pinchRef.current.targetZoom;
      pinchRef.current = null;
      if (canvasRef.current) {
        canvasRef.current.style.transform = '';
        canvasRef.current.style.transformOrigin = '';
      }
      if (targetZoom !== zoom) changeZoom(targetZoom);
    }

    const remainingPointer = Array.from(pointersRef.current.entries())[0];
    if (remainingPointer) {
      const [pointerId, point] = remainingPointer;
      panRef.current = {
        pointerId,
        clientX: point.clientX,
        clientY: point.clientY,
        scrollLeft: event.currentTarget.scrollLeft,
        scrollTop: event.currentTarget.scrollTop,
      };
    } else {
      panRef.current = null;
    }
  };

  const handleLostPointerCapture = (event) => {
    if (!pointersRef.current.has(event.pointerId)) return;
    resetPointerInteraction();
  };

  const controlsDisabled = loadState !== 'ready';
  const pageLabel = pageCount ? `PDF 第 ${pageNumber} 页，共 ${pageCount} 页` : 'PDF 页面';

  return (
    <section className="v3-mobile-pdf-preview" aria-label="PDF 阅读器">
      <div className="v3-mobile-pdf-toolbar">
        <div className="v3-mobile-pdf-toolbar-group" aria-label="翻页控件">
          <button
            type="button"
            aria-label="上一页"
            title="上一页"
            disabled={controlsDisabled || pageNumber <= 1}
            onClick={() => changePage(pageNumber - 1)}
          >
            <ChevronLeft size={18} />
          </button>
          <output className="v3-mobile-pdf-page-count" aria-live="polite">
            {pageCount ? `${pageNumber} / ${pageCount}` : '— / —'}
          </output>
          <button
            type="button"
            aria-label="下一页"
            title="下一页"
            disabled={controlsDisabled || pageNumber >= pageCount}
            onClick={() => changePage(pageNumber + 1)}
          >
            <ChevronRight size={18} />
          </button>
        </div>
        <div className="v3-mobile-pdf-toolbar-group" aria-label="缩放控件">
          <button
            type="button"
            aria-label="缩小"
            title="缩小"
            disabled={controlsDisabled || zoom <= MIN_ZOOM}
            onClick={() => changeZoom(zoom - ZOOM_STEP)}
          >
            <ZoomOut size={17} />
          </button>
          <output className="v3-mobile-pdf-zoom" aria-live="polite">{Math.round(zoom * 100)}%</output>
          <button
            type="button"
            aria-label="放大"
            title="放大"
            disabled={controlsDisabled || zoom >= MAX_ZOOM}
            onClick={() => changeZoom(zoom + ZOOM_STEP)}
          >
            <ZoomIn size={17} />
          </button>
          <button
            type="button"
            aria-label="适合宽度"
            title="适合宽度"
            disabled={controlsDisabled || zoom === 1}
            onClick={() => changeZoom(1)}
          >
            <Maximize2 size={17} />
          </button>
        </div>
      </div>
      <a
        className="v3-mobile-pdf-accessible-link"
        href={url}
        target="_blank"
        rel="noopener noreferrer"
      >
        使用系统 PDF 阅读器打开可访问版本
      </a>
      <div
        ref={viewportRef}
        className="v3-mobile-pdf-viewport"
        role="region"
        aria-label="PDF 页面滚动区域"
        aria-describedby={hintId}
        tabIndex={0}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerEnd}
        onPointerCancel={handlePointerEnd}
        onLostPointerCapture={handleLostPointerCapture}
      >
        {(loadState === 'loading' || renderState === 'rendering') && !error && (
          <div className="v3-mobile-pdf-status" role="status">正在加载 PDF…</div>
        )}
        {error && <div className="v3-mobile-pdf-status error" role="alert">{error}</div>}
        <canvas
          ref={canvasRef}
          className="v3-mobile-pdf-canvas"
          aria-hidden="true"
        />
        <div
          ref={textLayerContainerRef}
          className="textLayer v3-mobile-pdf-text-layer"
          role="document"
          aria-label={pageLabel}
        />
      </div>
      <p id={hintId} className="v3-mobile-pdf-hint">
        双指缩放，拖动查看页面；键盘可使用方向键或 Page Up / Page Down 滚动
      </p>
    </section>
  );
}
