import React, { act, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

import SidebarResizeHandle, {
  APP_SIDEBAR_WIDTH_STORAGE_KEY,
  DEFAULT_APP_SIDEBAR_WIDTH,
  MAX_APP_SIDEBAR_WIDTH,
  MIN_APP_SIDEBAR_WIDTH,
  clampSidebarWidth,
  getSidebarMaxWidth,
  loadSidebarWidth,
  saveSidebarWidth,
} from './sidebar-resizer';

function ResizeHarness({
  initialWidth = DEFAULT_APP_SIDEBAR_WIDTH,
  maxWidth = MAX_APP_SIDEBAR_WIDTH,
  disabled = false,
}) {
  const [width, setWidth] = useState(initialWidth);
  const [committedWidth, setCommittedWidth] = useState('');
  const [resizing, setResizing] = useState(false);

  return (
    <div>
      <output data-testid="width">{width}</output>
      <output data-testid="committed-width">{committedWidth}</output>
      <output data-testid="resizing">{String(resizing)}</output>
      <SidebarResizeHandle
        width={width}
        maxWidth={maxWidth}
        disabled={disabled}
        onWidthChange={setWidth}
        onWidthCommit={setCommittedWidth}
        onResizeChange={setResizing}
      />
    </div>
  );
}

describe('SidebarResizeHandle', () => {
  let container;
  let root;

  beforeEach(() => {
    localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  async function renderHarness(props = {}) {
    await act(async () => {
      root.render(<ResizeHarness {...props} />);
    });
  }

  function text(testId) {
    return container.querySelector(`[data-testid="${testId}"]`).textContent;
  }

  it('clamps widths and derives a viewport-safe maximum', () => {
    expect(clampSidebarWidth(100)).toBe(MIN_APP_SIDEBAR_WIDTH);
    expect(clampSidebarWidth(999)).toBe(MAX_APP_SIDEBAR_WIDTH);
    expect(clampSidebarWidth('312.4')).toBe(312);
    expect(clampSidebarWidth('invalid')).toBe(DEFAULT_APP_SIDEBAR_WIDTH);

    expect(getSidebarMaxWidth(300)).toBe(MIN_APP_SIDEBAR_WIDTH);
    expect(getSidebarMaxWidth(800)).toBe(320);
    expect(getSidebarMaxWidth(1000)).toBe(MAX_APP_SIDEBAR_WIDTH);
    expect(getSidebarMaxWidth(2000)).toBe(MAX_APP_SIDEBAR_WIDTH);
    expect(getSidebarMaxWidth('invalid')).toBe(MAX_APP_SIDEBAR_WIDTH);
  });

  it('loads and saves a clamped width in localStorage', () => {
    expect(loadSidebarWidth()).toBe(DEFAULT_APP_SIDEBAR_WIDTH);

    saveSidebarWidth(338.6);
    expect(localStorage.getItem(APP_SIDEBAR_WIDTH_STORAGE_KEY)).toBe('339');
    expect(loadSidebarWidth()).toBe(339);

    localStorage.setItem(APP_SIDEBAR_WIDTH_STORAGE_KEY, '9999');
    expect(loadSidebarWidth()).toBe(MAX_APP_SIDEBAR_WIDTH);

    localStorage.setItem(APP_SIDEBAR_WIDTH_STORAGE_KEY, 'not-a-number');
    expect(loadSidebarWidth()).toBe(DEFAULT_APP_SIDEBAR_WIDTH);
  });

  it('drags in both directions, clamps at the bounds, and stops after pointerup', async () => {
    await renderHarness();
    const handle = container.querySelector('.v3-sidebar-resize-handle');

    expect(handle.getAttribute('role')).toBe('separator');
    expect(handle.getAttribute('aria-orientation')).toBe('vertical');
    expect(handle.getAttribute('aria-valuenow')).toBe('260');

    await act(async () => {
      Simulate.pointerDown(handle, { button: 0, clientX: 260, pointerId: 7 });
    });
    expect(text('resizing')).toBe('true');

    await act(async () => {
      Simulate.pointerMove(handle, { clientX: 360, pointerId: 7 });
    });
    expect(text('width')).toBe('360');

    await act(async () => {
      Simulate.pointerMove(handle, { clientX: 900, pointerId: 7 });
    });
    expect(text('width')).toBe(String(MAX_APP_SIDEBAR_WIDTH));

    await act(async () => {
      Simulate.pointerMove(handle, { clientX: -100, pointerId: 7 });
    });
    expect(text('width')).toBe(String(MIN_APP_SIDEBAR_WIDTH));

    await act(async () => {
      Simulate.pointerUp(handle, { pointerId: 7 });
    });
    expect(text('committed-width')).toBe(String(MIN_APP_SIDEBAR_WIDTH));
    expect(text('resizing')).toBe('false');

    await act(async () => {
      Simulate.pointerMove(handle, { clientX: 420, pointerId: 7 });
    });
    expect(text('width')).toBe(String(MIN_APP_SIDEBAR_WIDTH));
  });

  it('supports Arrow, Home, and End keyboard resizing', async () => {
    await renderHarness({ maxWidth: 410 });
    const handle = container.querySelector('.v3-sidebar-resize-handle');

    await act(async () => {
      Simulate.keyDown(handle, { key: 'ArrowRight' });
    });
    expect(text('width')).toBe('272');
    expect(text('committed-width')).toBe('272');

    await act(async () => {
      Simulate.keyDown(handle, { key: 'ArrowRight', shiftKey: true });
    });
    expect(text('width')).toBe('304');

    await act(async () => {
      Simulate.keyDown(handle, { key: 'Home' });
    });
    expect(text('width')).toBe(String(MIN_APP_SIDEBAR_WIDTH));

    await act(async () => {
      Simulate.keyDown(handle, { key: 'End' });
    });
    expect(text('width')).toBe('410');
    expect(text('committed-width')).toBe('410');
  });

  it('resets to the default width on double click', async () => {
    await renderHarness({ initialWidth: 390 });
    const handle = container.querySelector('.v3-sidebar-resize-handle');

    await act(async () => {
      Simulate.doubleClick(handle);
    });

    expect(text('width')).toBe(String(DEFAULT_APP_SIDEBAR_WIDTH));
    expect(text('committed-width')).toBe(String(DEFAULT_APP_SIDEBAR_WIDTH));
  });

  it('does not render a resize target while disabled', async () => {
    await renderHarness({ disabled: true });

    expect(container.querySelector('.v3-sidebar-resize-handle')).toBeNull();
    expect(text('width')).toBe(String(DEFAULT_APP_SIDEBAR_WIDTH));
    expect(text('committed-width')).toBe('');
    expect(text('resizing')).toBe('false');
  });

  it('clears an active resize when the sidebar becomes disabled', async () => {
    await renderHarness();
    const handle = container.querySelector('.v3-sidebar-resize-handle');

    await act(async () => {
      Simulate.pointerDown(handle, { button: 0, clientX: 260, pointerId: 9 });
    });
    expect(text('resizing')).toBe('true');

    await act(async () => {
      root.render(<ResizeHarness disabled />);
    });

    expect(container.querySelector('.v3-sidebar-resize-handle')).toBeNull();
    expect(text('resizing')).toBe('false');
  });
});
