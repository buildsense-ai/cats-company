import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import WordPreview from './ui/WordPreview.jsx';
import { buildSampleDocx } from './test-fixtures/build-docx.js';

function arrayBufferOf(bytes) {
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
}

function italicButton(container) {
  return [...container.querySelectorAll('.office-bar-btn')]
    .find(button => button.getAttribute('title') === '斜体');
}

function italicRuns(container) {
  return [...container.querySelectorAll('.office-docx-stage span')]
    .filter(span => span.style.fontStyle === 'italic');
}

describe('Word italic toggle reaches the page', () => {
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
  });

  afterEach(() => {
    document.body.innerHTML = '';
    vi.restoreAllMocks();
  });

  it('applies italic to the selected paragraph', async () => {
    const bytes = await buildSampleDocx();
    global.fetch = vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => arrayBufferOf(bytes) });
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);
    await act(async () => {
      root.render(
        <WordPreview file={{ name: 'doc.docx' }} url="/uploads/files/doc.docx" onSave={vi.fn()} />,
      );
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(container.querySelector('[data-office-mode]')).not.toBeNull());
    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));

    // The editing surface is the floating bar now, not the old property column.
    expect(container.querySelector('[data-office-bar]')).not.toBeNull();

    const italic = italicButton(container);
    expect(italic).toBeDefined();
    expect(italic.disabled).toBe(false);

    const before = italicRuns(container).length;
    await act(async () => Simulate.click(italic));
    expect(italicRuns(container).length).toBeGreaterThan(before);

    act(() => root.unmount());
  });

  it('shows the italic button as pressed while the paragraph is italic', async () => {
    const bytes = await buildSampleDocx();
    global.fetch = vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => arrayBufferOf(bytes) });
    const container = document.createElement('div');
    document.body.appendChild(container);
    const root = createRoot(container);
    await act(async () => {
      root.render(
        <WordPreview file={{ name: 'doc.docx' }} url="/uploads/files/doc.docx" onSave={vi.fn()} />,
      );
      await Promise.resolve();
    });
    await vi.waitFor(() => expect(container.querySelector('[data-office-mode]')).not.toBeNull());
    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));

    const italic = italicButton(container);
    // The reason the button felt dead: it looked identical whether the text was italic or not.
    expect(italic.getAttribute('aria-pressed')).toBe('false');
    await act(async () => Simulate.click(italic));
    expect(italicButton(container).getAttribute('aria-pressed')).toBe('true');
    expect(italicButton(container).className).toContain('is-on');

    act(() => root.unmount());
  });
});
