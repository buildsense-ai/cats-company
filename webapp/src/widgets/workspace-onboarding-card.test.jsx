import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getAgents: vi.fn(),
    redeemBotInviteCode: vi.fn(),
  },
}));

import { api } from '../api';
import WorkspaceOnboardingCard, {
  workspaceOnboardingStorageKey,
} from './workspace-onboarding-card';

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

function buttonWithText(text) {
  return [...document.querySelectorAll('button')].find((button) => button.textContent.trim() === text);
}

describe('WorkspaceOnboardingCard', () => {
  let container;
  let root;
  let showModal;
  let close;

  beforeEach(() => {
    api.getAgents.mockReset().mockResolvedValue({ agents: [] });
    api.redeemBotInviteCode.mockReset().mockResolvedValue({});
    localStorage.clear();
    showModal = HTMLDialogElement.prototype.showModal;
    close = HTMLDialogElement.prototype.close;
    HTMLDialogElement.prototype.showModal = function showModalForTest() {
      this.setAttribute('open', '');
    };
    HTMLDialogElement.prototype.close = function closeForTest() {
      this.removeAttribute('open');
    };
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    localStorage.clear();
    HTMLDialogElement.prototype.showModal = showModal;
    HTMLDialogElement.prototype.close = close;
    vi.clearAllMocks();
  });

  async function mount(props = {}) {
    await act(async () => {
      root.render(<WorkspaceOnboardingCard userId={42} {...props} />);
      await flushPromises();
    });
  }

  it('shows only for a user without available agents and remembers dismissal', async () => {
    await mount();

    expect(api.getAgents).toHaveBeenCalledTimes(1);
    expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull();

    await act(async () => {
      Simulate.click(document.querySelector('[aria-label="稍后设置助手"]'));
    });

    expect(localStorage.getItem(workspaceOnboardingStorageKey(42))).toBe('dismissed');
    expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  });

  it('does not interrupt users who already have an available agent', async () => {
    api.getAgents.mockResolvedValueOnce({ agents: [{ uid: 7 }] });

    await mount();

    expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  });

  it('redeems a real invite and refreshes the workspace agent list', async () => {
    const onDataChanged = vi.fn();
    window.addEventListener('cc:data-changed', onDataChanged);
    await mount();

    await act(async () => {
      Simulate.click(buttonWithText('使用邀请码'));
    });
    const input = document.querySelector('#workspace-onboarding-invite');
    await act(async () => {
      Simulate.change(input, { target: { value: 'join-123' } });
    });
    await act(async () => {
      Simulate.submit(input.closest('form'));
      await flushPromises();
    });

    expect(api.redeemBotInviteCode).toHaveBeenCalledWith('JOIN-123');
    expect(onDataChanged).toHaveBeenCalledTimes(1);
    expect(document.querySelector('#workspace-invite-title').textContent).toBe('云端助手已添加');

    await act(async () => {
      Simulate.click(buttonWithText('开始创建任务'));
    });

    expect(localStorage.getItem(workspaceOnboardingStorageKey(42))).toBe('dismissed');
    expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
    window.removeEventListener('cc:data-changed', onDataChanged);
  });

  it('surfaces an invalid invite error without dismissing the onboarding', async () => {
    api.redeemBotInviteCode.mockRejectedValueOnce(new Error('bot invite code is invalid or expired'));
    await mount();

    await act(async () => {
      Simulate.click(buttonWithText('使用邀请码'));
    });
    const input = document.querySelector('#workspace-onboarding-invite');
    await act(async () => {
      Simulate.change(input, { target: { value: 'bad-code' } });
    });
    await act(async () => {
      Simulate.submit(input.closest('form'));
      await flushPromises();
    });

    expect(document.querySelector('[role="alert"]').textContent).toBe('邀请码无效或已失效');
    expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull();
  });
});
