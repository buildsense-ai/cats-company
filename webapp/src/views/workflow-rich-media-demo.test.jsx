import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

import WorkflowRichMediaDemo from './workflow-rich-media-demo';

describe('WorkflowRichMediaDemo file preview', () => {
  let container;
  let root;
  let originalMatchMedia;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    originalMatchMedia = window.matchMedia;
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn(() => ({
        matches: true,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: originalMatchMedia,
    });
  });

  it('passes its chat column as the mobile preview background', async () => {
    await act(async () => {
      root.render(<WorkflowRichMediaDemo />);
      await Promise.resolve();
    });

    const reportButton = [...container.querySelectorAll('button.v3-artifact-main')]
      .find((button) => button.textContent.includes('teaching-report.html'));
    expect(reportButton).toBeTruthy();

    await act(async () => {
      Simulate.click(reportButton);
      await Promise.resolve();
      await Promise.resolve();
    });

    const chatColumn = container.querySelector('.v3-chat-column');
    expect(container.querySelector('.v3-file-preview-panel').getAttribute('role')).toBe('dialog');
    expect(chatColumn.hasAttribute('inert')).toBe(true);
    expect(chatColumn.getAttribute('aria-hidden')).toBe('true');
  });
});
