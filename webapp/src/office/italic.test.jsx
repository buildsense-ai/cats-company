import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import OfficePreview from './ui/OfficePreview.jsx';
import { buildPptx } from './test-fixtures/build-pptx.js';

function arrayBufferOf(bytes) {
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
}

function italicButton(container) {
  return [...container.querySelectorAll('.office-bar-btn')]
    .find(button => button.getAttribute('title') === '斜体');
}

function italicSpans(container) {
  return [...container.querySelectorAll('.office-preview-stage span')]
    .filter(span => span.style.fontStyle === 'italic');
}

describe('italic toggle reaches the canvas', () => {
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
  });

  afterEach(() => {
    document.body.innerHTML = '';
    vi.restoreAllMocks();
  });

  it('renders the clicked paragraph in italic', async () => {
    const bytes = await buildPptx();
    global.fetch = vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => arrayBufferOf(bytes) });
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);
    await act(async () => {
      root.render(
        <OfficePreview
          file={{ name: 'deck.pptx' }}
          url="/uploads/files/deck.pptx"
          family="pptx"
          onSave={vi.fn()}
        />,
      );
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(container.querySelector('[data-office-mode]')).not.toBeNull());
    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));

    expect(italicSpans(container)).toHaveLength(0);

    const button = italicButton(container);
    expect(button).toBeDefined();
    expect(button.disabled).toBe(false);
    await act(async () => Simulate.click(button));

    // The point of the test: the draft reached the renderer, not just the button state.
    expect(italicSpans(container).length).toBeGreaterThan(0);

    act(() => root.unmount());
  });

  it('shows the bar beside the selection and switches it off again', async () => {
    const bytes = await buildPptx();
    global.fetch = vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => arrayBufferOf(bytes) });
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);
    await act(async () => {
      root.render(
        <OfficePreview
          file={{ name: 'deck.pptx' }}
          url="/uploads/files/deck.pptx"
          family="pptx"
          onSave={vi.fn()}
        />,
      );
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(container.querySelector('[data-office-mode]')).not.toBeNull());

    // Read-only has no editing bar at all: a reader gets the whole width for the document.
    expect(container.querySelector('[data-office-bar]')).toBeNull();

    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));
    const bar = container.querySelector('[data-office-bar]');
    expect(bar).not.toBeNull();
    expect(bar.getAttribute('data-office-bar-docked')).toBe('0');

    const italic = italicButton(container);
    await act(async () => Simulate.click(italic));
    await act(async () => Simulate.click(italic));
    expect(italicSpans(container)).toHaveLength(0);

    act(() => root.unmount());
  });
});
