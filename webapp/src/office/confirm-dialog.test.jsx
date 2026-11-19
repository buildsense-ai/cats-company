import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ConfirmDialog from './ui/ConfirmDialog.jsx';

function mount(props) {
  const container = document.createElement('div');
  document.body.appendChild(container);
  const root = createRoot(container);
  act(() => {
    root.render(<ConfirmDialog {...props} />);
  });
  return { container, root };
}

describe('attach confirmation dialog', () => {
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
  });

  afterEach(() => {
    document.body.innerHTML = '';
    vi.restoreAllMocks();
  });

  it('lists every document it is about to send', () => {
    const { container, root } = mount({
      title: '这条消息会带上 2 个改过的文档',
      lines: ['报告.docx', '清单.pptx'],
      onConfirm: vi.fn(),
      onCancel: vi.fn(),
    });
    const items = [...container.querySelectorAll('.office-confirm-list li')].map(li => li.textContent);
    expect(items).toEqual(['报告.docx', '清单.pptx']);
    // The count in the title and the number of names have to agree, or the dialog is lying
    // about what it is about to attach.
    expect(container.querySelector('.office-confirm-title').textContent).toContain('2');
    act(() => root.unmount());
  });

  it('reports both answers instead of guessing', () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    const { container, root } = mount({
      title: 't', lines: ['a.docx'], onConfirm, onCancel,
    });
    act(() => Simulate.click(container.querySelector('[data-office-confirm-ok]')));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(onCancel).not.toHaveBeenCalled();

    act(() => Simulate.click(container.querySelector('[data-office-confirm-cancel]')));
    expect(onCancel).toHaveBeenCalledTimes(1);
    act(() => root.unmount());
  });

  it('renders inside the page rather than as a native browser prompt', () => {
    const windowConfirm = vi.spyOn(window, 'confirm');
    const { container, root } = mount({ title: 't', lines: ['a.docx'], onConfirm: vi.fn() });
    expect(container.querySelector('[data-office-confirm]')).not.toBeNull();
    expect(container.querySelector('[role="dialog"]')).not.toBeNull();
    expect(windowConfirm).not.toHaveBeenCalled();
    act(() => root.unmount());
  });

  it('omits the optional buttons when the caller does not offer them', () => {
    const { container, root } = mount({
      title: 't', lines: [], onConfirm: vi.fn(), dismissLabel: '', cancelLabel: '',
    });
    expect(container.querySelector('[data-office-confirm-dismiss]')).toBeNull();
    expect(container.querySelector('[data-office-confirm-cancel]')).toBeNull();
    expect(container.querySelector('[data-office-confirm-ok]')).not.toBeNull();
    act(() => root.unmount());
  });
});
