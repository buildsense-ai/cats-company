import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import FeedbackModal from './feedback-modal';

vi.mock('../api', () => ({
  api: {
    uploadFeedbackImage: vi.fn(),
    submitFeedback: vi.fn(),
  },
}));

describe('FeedbackModal clipboard attachments', () => {
  let container;
  let root;

  beforeEach(async () => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => 'blob:feedback-preview'),
    });
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: vi.fn(),
    });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);

    await act(async () => {
      root.render(<FeedbackModal onClose={vi.fn()} user={{ uid: 'feedback-user' }} />);
    });
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    localStorage.clear();
    vi.clearAllMocks();
  });

  function dispatchPaste(clipboardData) {
    const event = new Event('paste', { bubbles: true, cancelable: true });
    Object.defineProperty(event, 'clipboardData', { value: clipboardData });
    container.querySelector('.oc-feedback-message-field textarea').dispatchEvent(event);
    return event;
  }

  it('adds a pasted clipboard image through the existing attachment preview', async () => {
    const file = new File(['image'], 'clipboard.png', { type: 'image/png' });
    let pasteEvent;

    await act(async () => {
      pasteEvent = dispatchPaste({
        items: [{ kind: 'file', type: 'image/png', getAsFile: () => file }],
        files: [],
      });
    });

    expect(pasteEvent.defaultPrevented).toBe(true);
    expect(URL.createObjectURL).toHaveBeenCalledWith(file);
    expect(container.querySelector('.oc-feedback-preview img')?.alt).toBe('clipboard.png');
  });

  it('keeps normal text paste behavior unchanged', async () => {
    let pasteEvent;

    await act(async () => {
      pasteEvent = dispatchPaste({
        items: [{ kind: 'string', type: 'text/plain', getAsFile: () => null }],
        files: [],
      });
    });

    expect(pasteEvent.defaultPrevented).toBe(false);
    expect(container.querySelector('.oc-feedback-preview')).toBeNull();
  });
});
