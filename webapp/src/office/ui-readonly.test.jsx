import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import OfficePreview from './ui/OfficePreview.jsx';
import WordPreview from './ui/WordPreview.jsx';
import { buildPptx } from './test-fixtures/build-pptx.js';
import { buildSampleDocx } from './test-fixtures/build-docx.js';

function arrayBufferOf(bytes) {
  return bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
}

async function mountOffice(Component, props) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  await act(async () => {
    root.render(<Component {...props} />);
    await Promise.resolve();
  });
  await vi.waitFor(() => expect(container.querySelector('[data-office-mode]')).not.toBeNull());
  return { container, root };
}

function typeAndBlur(input, value) {
  input.value = value;
  Simulate.change(input, { target: { value, selectionStart: value.length } });
  Simulate.blur(input);
}

describe('Office read-only posture', () => {
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
  });

  afterEach(() => {
    document.body.innerHTML = '';
    vi.restoreAllMocks();
  });

  it('renders PPT read-only with no editing bar, then keeps edits after 完成', async () => {
    const bytes = await buildPptx();
    global.fetch = vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => arrayBufferOf(bytes) });
    const { container, root } = await mountOffice(OfficePreview, {
      file: { name: 'deck.pptx' }, url: '/uploads/files/deck.pptx', family: 'pptx', onSave: vi.fn(),
    });

    expect(container.querySelector('[data-office-mode]').getAttribute('data-office-mode')).toBe('readonly');
    // Read-only shows no editing surface at all, so a reader who never enters edit mode keeps
    // the full width for the document instead of a disabled toolbar.
    expect(container.querySelector('[data-office-bar]')).toBeNull();
    expect(container.querySelector('.office-preview-body').getAttribute('data-office-side')).toBe('off');
    expect(container.querySelector('[data-office-selection]')).toBeNull();
    expect(container.querySelector('[data-office-edit-toggle]').textContent).toBe('编辑');

    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));
    expect(container.querySelector('[data-office-mode]').getAttribute('data-office-mode')).toBe('edit');
    expect(container.querySelector('[data-office-selection]')).not.toBeNull();
    expect(container.querySelector('[data-office-bar]')).not.toBeNull();
    expect([...container.querySelectorAll('.office-bar input, .office-bar textarea, .office-bar select')]
      .some(field => !field.disabled)).toBe(true);

    const textarea = container.querySelector('.office-bar textarea[data-office-text-input]');
    expect(textarea).not.toBeNull();
    await act(async () => typeAndBlur(textarea, 'Draft survives completion'));
    expect(container.textContent).toContain('Draft survives completion');

    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));
    expect(container.querySelector('[data-office-mode]').getAttribute('data-office-mode')).toBe('readonly');
    expect(container.querySelector('[data-office-selection]')).toBeNull();
    expect(container.querySelector('[data-office-bar]')).toBeNull();
    expect(container.textContent).toContain('Draft survives completion');
    act(() => root.unmount());
  });

  it('renders Word read-only with disabled fields, enables them for editing, and keeps drafts', async () => {
    const bytes = await buildSampleDocx();
    global.fetch = vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => arrayBufferOf(bytes) });
    const { container, root } = await mountOffice(WordPreview, {
      file: { name: 'doc.docx' }, url: '/uploads/files/doc.docx', onSave: vi.fn(),
    });

    expect(container.querySelector('[data-office-mode]').getAttribute('data-office-mode')).toBe('readonly');
    expect(container.querySelector('[data-office-bar]')).toBeNull();
    expect(container.querySelector('.office-preview-body').getAttribute('data-office-side')).toBe('off');

    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));
    expect(container.querySelector('[data-office-mode]').getAttribute('data-office-mode')).toBe('edit');
    const textarea = container.querySelector('.office-bar textarea[data-office-text-input]');
    expect(textarea).not.toBeNull();
    expect(textarea.disabled).toBe(false);

    await act(async () => typeAndBlur(textarea, 'Word draft survives completion'));
    expect(container.textContent).toContain('Word draft survives completion');
    await act(async () => Simulate.click(container.querySelector('[data-office-edit-toggle]')));
    expect(container.querySelector('[data-office-mode]').getAttribute('data-office-mode')).toBe('readonly');
    expect(container.querySelector('[data-office-bar]')).toBeNull();
    expect(container.textContent).toContain('Word draft survives completion');
    act(() => root.unmount());
  });
});
