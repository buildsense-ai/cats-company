import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import WordPreview from './ui/WordPreview.jsx';
import { buildSampleDocx } from './test-fixtures/build-docx.js';

function arrayBufferOf(bytes) {
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
}

async function mountDocx() {
  const bytes = await buildSampleDocx();
  global.fetch = vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => arrayBufferOf(bytes) });
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(<WordPreview file={{ name: 'doc.docx' }} url="/uploads/files/doc.docx" onSave={vi.fn()} />);
    await Promise.resolve();
  });
  await vi.waitFor(() => expect(container.querySelector('[data-office-mode]')).not.toBeNull());
  return { container, root };
}

function firstParagraph(container) {
  return container.querySelector('[data-office-block-kind="paragraph"]');
}

describe('Word in-place editing', () => {
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
  });

  afterEach(() => {
    document.body.innerHTML = '';
    vi.restoreAllMocks();
  });

  it('ignores a double click while the document is read-only', async () => {
    const { container, root } = await mountDocx();
    const paragraph = firstParagraph(container);
    expect(paragraph).not.toBeNull();

    await act(async () => Simulate.doubleClick(paragraph));
    // A reader must not be able to start an edit, even by double-clicking.
    expect(container.querySelector('[data-office-inline-text]')).toBeNull();
    act(() => root.unmount());
  });

  it('opens an editor over the paragraph being edited', async () => {
    const { container, root } = await mountDocx();
    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));

    const paragraph = firstParagraph(container);
    await act(async () => Simulate.doubleClick(paragraph));

    const box = container.querySelector('[data-office-inline-text]');
    expect(box).not.toBeNull();
    // It starts from the paragraph's own text, so reopening never looks like it lost an edit.
    expect(box.value).toBe(paragraph.textContent);

    act(() => root.unmount());
  });

  it('writes the typed text into the rendered document on blur', async () => {
    const { container, root } = await mountDocx();
    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));

    await act(async () => Simulate.doubleClick(firstParagraph(container)));
    const box = container.querySelector('[data-office-inline-text]');

    // Typing and blurring are separate frames in a real browser, and the controlled textarea
    // has to have painted before the blur reads it back. Keeping both in one batch would test
    // a sequence no user can produce, and would fail for the wrong reason.
    await act(async () => {
      box.value = '就地改字';
      Simulate.change(box);
    });
    await act(async () => { Simulate.blur(box); });

    // The editor closes and the page shows the new text, without a save.
    expect(container.querySelector('[data-office-inline-text]')).toBeNull();
    expect(container.textContent).toContain('就地改字');
    act(() => root.unmount());
  });

  it('abandons the edit on Escape', async () => {
    const { container, root } = await mountDocx();
    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));
    await act(async () => Simulate.doubleClick(firstParagraph(container)));

    const box = container.querySelector('[data-office-inline-text]');
    await act(async () => {
      Simulate.change(box, { target: { value: '不该写进去' } });
      Simulate.keyDown(box, { key: 'Escape' });
    });

    expect(container.querySelector('[data-office-inline-text]')).toBeNull();
    expect(container.textContent).not.toContain('不该写进去');
    act(() => root.unmount());
  });
});
