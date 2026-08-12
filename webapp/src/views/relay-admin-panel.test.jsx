import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import RelayAdminPanel, {
  RELAY_ADMIN_WIDTH_MIN,
  RELAY_ADMIN_WIDTH_DEFAULT,
  RELAY_ADMIN_WIDTH_MAX,
  RELAY_ADMIN_WIDTH_STORAGE_KEY,
  clampRelayAdminWidth,
} from './relay-admin-panel';

describe('RelayAdminPanel', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    Object.defineProperty(window, 'innerWidth', { value: 1920, configurable: true, writable: true });
    localStorage.clear();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    localStorage.clear();
  });

  const renderPanel = async (props = {}) => {
    await act(async () => {
      root.render(<RelayAdminPanel onClose={() => {}} {...props} />);
      await Promise.resolve();
      await Promise.resolve();
    });
  };

  const panelWidth = () => {
    const aside = container.querySelector('.v3-relay-admin-panel');
    const value = aside?.style.getPropertyValue('--v3-relay-admin-width') || '';
    return Number(value.replace('px', ''));
  };

  it('embeds the usage-admin page through the proxy', async () => {
    await renderPanel();
    const panel = container.querySelector('.v3-relay-admin-panel');
    expect(panel).toBeTruthy();
    const iframe = container.querySelector('iframe');
    expect(iframe?.getAttribute('src')).toBe('/api/admin/relay/local/usage-admin');
    expect(iframe?.getAttribute('sandbox')).toContain('allow-scripts');
  });

  it('renders a close button that invokes onClose', async () => {
    const onClose = vi.fn();
    await renderPanel({ onClose });
    const close = container.querySelector('button[aria-label="关闭模型用量管理"]');
    expect(close).toBeTruthy();
    await act(async () => close.click());
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('renders a draggable resize handle on the left edge', async () => {
    await renderPanel();
    const handle = container.querySelector('.v3-relay-admin-resize-handle');
    expect(handle).toBeTruthy();
    expect(handle?.getAttribute('role')).toBe('separator');
    expect(handle?.getAttribute('aria-orientation')).toBe('vertical');
    expect(panelWidth()).toBe(RELAY_ADMIN_WIDTH_DEFAULT);
  });

  it('grows with ArrowLeft, shrinks with ArrowRight and jumps with Home/End', async () => {
    localStorage.setItem(RELAY_ADMIN_WIDTH_STORAGE_KEY, '500');
    await renderPanel();
    const aside = container.querySelector('.v3-relay-admin-panel');
    const handle = container.querySelector('.v3-relay-admin-resize-handle');
    expect(panelWidth()).toBe(500);

    await act(async () => handle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true })));
    expect(panelWidth()).toBe(540);
    await act(async () => handle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true })));
    expect(panelWidth()).toBe(500);
    await act(async () => handle.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true })));
    expect(panelWidth()).toBe(RELAY_ADMIN_WIDTH_MIN);
    await act(async () => handle.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true })));
    expect(panelWidth()).toBe(RELAY_ADMIN_WIDTH_MAX);
    expect(aside.style.getPropertyValue('--v3-relay-admin-width')).toBe(`${RELAY_ADMIN_WIDTH_MAX}px`);
    expect(Number(localStorage.getItem(RELAY_ADMIN_WIDTH_STORAGE_KEY))).toBe(RELAY_ADMIN_WIDTH_MAX);
  });

  it('clamps persisted and programmatic widths to the min/max range', () => {
    expect(clampRelayAdminWidth(120)).toBe(RELAY_ADMIN_WIDTH_MIN);
    expect(clampRelayAdminWidth(9999)).toBe(RELAY_ADMIN_WIDTH_MAX);
    expect(clampRelayAdminWidth('oops')).toBe(RELAY_ADMIN_WIDTH_DEFAULT);
    expect(clampRelayAdminWidth(700)).toBe(700);
  });

  it('resizes by dragging the handle', async () => {
    localStorage.setItem(RELAY_ADMIN_WIDTH_STORAGE_KEY, '500');
    await renderPanel();
    const handle = container.querySelector('.v3-relay-admin-resize-handle');
    expect(panelWidth()).toBe(500);

    await act(async () => handle.dispatchEvent(new PointerEvent('pointerdown', { clientX: 800, bubbles: true })));
    await act(async () => window.dispatchEvent(new PointerEvent('pointermove', { clientX: 700 })));
    await act(async () => window.dispatchEvent(new PointerEvent('pointerup', { clientX: 700 })));
    // dragging the left edge leftwards grows the panel
    expect(panelWidth()).toBe(600);
    expect(Number(localStorage.getItem(RELAY_ADMIN_WIDTH_STORAGE_KEY))).toBe(600);
  });
});
