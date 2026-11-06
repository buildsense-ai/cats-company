import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import useLoadingHandoff from './use-loading-handoff';

function Loader() {
  const ref = useLoadingHandoff();
  return <div ref={ref} className="cc-loading-state" role="status" style={{ opacity: 1 }}>Loading</div>;
}

describe('loading handoff', () => {
  let container, root, animations;
  beforeEach(() => {
    vi.useFakeTimers();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    animations = [];
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockReturnValue({ left: 10, top: 20, width: 300, height: 200 });
    Object.defineProperty(Element.prototype, 'animate', { configurable: true, value: vi.fn(() => {
      let resolve;
      const animation = { finished: new Promise(done => { resolve = done; }), cancel: vi.fn(), complete: () => resolve() };
      animations.push(animation);
      return animation;
    }) });
  });
  afterEach(async () => {
    await act(async () => { root.unmount(); vi.runAllTimers(); });
    document.querySelectorAll('.cc-loading-handoff').forEach(node => node.remove());
    container.remove();
    delete Element.prototype.animate;
    vi.restoreAllMocks();
    vi.useRealTimers();
  });
  const render = async loading => act(async () => {
    root.render(<section data-loading-region="">{loading ? <Loader /> : <button>Ready</button>}</section>);
  });

  it('makes content available immediately and retires the decorative exit frame', async () => {
    await render(true);
    await render(false);
    expect(container.querySelector('button').textContent).toBe('Ready');
    await act(async () => { vi.advanceTimersByTime(20); });
    const layer = document.querySelector('.cc-loading-handoff');
    expect(layer?.getAttribute('aria-hidden')).toBe('true');
    expect(layer?.querySelector('[role="status"]')).toBeNull();
    expect(layer?.style.pointerEvents).toBe('none');
    expect(animations).toHaveLength(2);
    await act(async () => { animations.forEach(animation => animation.complete()); });
    expect(document.querySelector('.cc-loading-handoff')).toBeNull();
  });

  it('keeps SVG cutouts and their frozen fill when the loader retires', async () => {
    function MaskedLoader() {
      const ref = useLoadingHandoff();
      return <div ref={ref} className="cc-loading-state" id="live-loader" style={{ opacity: 1 }}>
        <svg><defs><mask id="loading-cutout"><rect width="80" height="60" fill="white" /></mask></defs>
          <g mask="url(#loading-cutout)" style={{ fill: 'rgb(20, 30, 40)' }}><rect width="40" height="30" /></g>
        </svg>
      </div>;
    }
    await act(async () => { root.render(<section data-loading-region=""><MaskedLoader /></section>); });
    await act(async () => { root.render(<section data-loading-region=""><button>Ready</button></section>); });
    const ghost = document.querySelector('.cc-loading-handoff');
    expect(ghost.querySelector('#live-loader')).toBeNull();
    expect(ghost.querySelector('mask')?.id).toBe('loading-cutout');
    expect(ghost.querySelector('g').getAttribute('mask')).toBe('url(#loading-cutout)');
    expect(ghost.querySelector('g').style.fill).toBe('rgb(20, 30, 40)');
    expect(document.querySelectorAll('#loading-cutout')).toHaveLength(1);
  });

  it('cancels the old handoff when another request starts', async () => {
    await render(true);
    await render(false);
    await act(async () => { vi.advanceTimersByTime(20); });
    expect(document.querySelector('.cc-loading-handoff')).not.toBeNull();
    await render(true);
    expect(document.querySelector('.cc-loading-handoff')).toBeNull();
    expect(animations[0].cancel).toHaveBeenCalled();
  });

  it('does not leave a visual copy when the owning page unmounts', async () => {
    await render(true);
    await act(async () => { root.render(null); vi.advanceTimersByTime(20); });
    expect(document.querySelector('.cc-loading-handoff')).toBeNull();
    expect(animations).toHaveLength(0);
  });

  it('skips the handoff when reduced motion is preferred', async () => {
    vi.stubGlobal('matchMedia', () => ({ matches: true }));
    await render(true);
    await render(false);
    await act(async () => { vi.advanceTimersByTime(20); });
    expect(container.querySelector('button')).not.toBeNull();
    expect(animations).toHaveLength(0);
    vi.unstubAllGlobals();
  });

  it('removes the fixed exit frame if the user scrolls during the transition', async () => {
    await render(true);
    await render(false);
    await act(async () => { vi.advanceTimersByTime(20); });
    expect(document.querySelector('.cc-loading-handoff')).not.toBeNull();
    window.dispatchEvent(new Event('scroll'));
    expect(document.querySelector('.cc-loading-handoff')).toBeNull();
  });

  it('starts the handoff before the first animation frame to avoid a content flash', async () => {
    await render(true);
    await render(false);
    expect(document.querySelector('.cc-loading-handoff')).not.toBeNull();
    expect(animations).toHaveLength(2);
  });

  it('cleans up a running handoff when its owning page is removed', async () => {
    await render(true);
    await render(false);
    await act(async () => { vi.advanceTimersByTime(20); });
    expect(document.querySelector('.cc-loading-handoff')).not.toBeNull();
    await act(async () => { root.render(null); });
    expect(document.querySelector('.cc-loading-handoff')).toBeNull();
    expect(animations[0].cancel).toHaveBeenCalled();
  });

  it('does not block a page handoff when a nested image region is still loading', async () => {
    await render(true);
    await act(async () => {
      root.render(<section data-loading-region=""><article data-loading-content=""><section data-loading-region=""><Loader /></section></article></section>);
      vi.advanceTimersByTime(20);
    });
    expect(document.querySelector('.cc-loading-handoff')).not.toBeNull();
    expect(animations).toHaveLength(2);
  });

  it('does not fade existing content owned by a nested loading region', async () => {
    const view = loading => <section data-loading-region="">
      <section data-loading-region=""><div data-loading-content="">Existing image</div></section>
      {loading ? <Loader /> : <button>Ready</button>}
    </section>;
    await act(async () => { root.render(view(true)); });
    const image = container.querySelector('[data-loading-content]');
    await act(async () => { root.render(view(false)); });
    expect(document.querySelector('.cc-loading-handoff')).not.toBeNull();
    expect(container.querySelector('button').textContent).toBe('Ready');
    expect(Element.prototype.animate.mock.contexts).not.toContain(image);
    expect(animations).toHaveLength(2);
  });
});
