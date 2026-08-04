import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import RelayAdminModal from './relay-admin-modal';

describe('RelayAdminModal', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  const renderModal = async (props = {}) => {
    await act(async () => {
      root.render(<RelayAdminModal onClose={() => {}} {...props} />);
      await Promise.resolve();
      await Promise.resolve();
    });
  };

  it('embeds the usage-admin page through the proxy', async () => {
    await renderModal();
    const iframe = container.querySelector('iframe');
    expect(iframe?.getAttribute('src')).toBe('/api/admin/relay/local/usage-admin');
    expect(iframe?.getAttribute('sandbox')).toContain('allow-scripts');
  });

  it('renders a close button that invokes onClose', async () => {
    const onClose = vi.fn();
    await renderModal({ onClose });
    const close = container.querySelector('button[aria-label="关闭中转用量管理"]');
    expect(close).toBeTruthy();
    await act(async () => close.click());
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
