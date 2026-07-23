import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import {
  FeedbackProvider,
  InlineFeedback,
  useFeedback,
} from './feedback-system';

function FeedbackHarness() {
  const feedback = useFeedback();
  return (
    <div>
      <button
        type="button"
        onClick={() => feedback.notify({
          tone: 'success',
          title: '保存成功',
          message: '设置已经更新',
          duration: 0,
        })}
      >
        通知
      </button>
      <button
        type="button"
        onClick={async () => {
          const accepted = await feedback.confirm({
            title: '删除项目？',
            message: '任务会保留。',
            confirmLabel: '删除',
            tone: 'danger',
          });
          if (accepted) feedback.notify({ tone: 'success', message: '已删除', duration: 0 });
        }}
      >
        确认
      </button>
      <InlineFeedback tone="error" title="保存失败">请稍后重试</InlineFeedback>
    </div>
  );
}

describe('feedback system', () => {
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
    document.body.querySelectorAll('.cc-toast-viewport, .cc-confirm-overlay').forEach((node) => node.remove());
  });

  async function mount() {
    await act(async () => {
      root.render(
        <FeedbackProvider>
          <FeedbackHarness />
        </FeedbackProvider>,
      );
    });
  }

  it('renders accessible inline feedback and dismissible toasts', async () => {
    await mount();

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('保存失败');
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button')).find((button) => button.textContent === '通知'));
    });

    const toast = document.body.querySelector('.cc-toast-success');
    expect(toast?.textContent).toContain('设置已经更新');
    expect(toast?.getAttribute('role')).toBe('status');

    await act(async () => {
      Simulate.click(toast.querySelector('.cc-toast-close'));
    });
    expect(document.body.querySelector('.cc-toast-success')).toBeFalsy();
  });

  it('uses a cancel-first destructive confirmation and reports acceptance', async () => {
    await mount();

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button')).find((button) => button.textContent === '确认'));
    });

    const dialog = document.body.querySelector('[role="alertdialog"]');
    expect(dialog?.textContent).toContain('删除项目？');
    expect(document.activeElement).toBe(dialog.querySelector('.cc-confirm-cancel'));

    await act(async () => {
      Simulate.click(dialog.querySelector('.cc-confirm-submit'));
      await Promise.resolve();
    });

    expect(document.body.querySelector('[role="alertdialog"]')).toBeFalsy();
    expect(document.body.querySelector('.cc-toast-success')?.textContent).toContain('已删除');
  });

  it('cancels confirmation with Escape', async () => {
    await mount();
    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button')).find((button) => button.textContent === '确认'));
    });

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await Promise.resolve();
    });

    expect(document.body.querySelector('[role="alertdialog"]')).toBeFalsy();
    expect(document.body.querySelector('.cc-toast-success')).toBeFalsy();
  });
});
