import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    acceptFriend: vi.fn(),
    getPendingRequests: vi.fn(),
    rejectFriend: vi.fn(),
    redeemBotInviteCode: vi.fn(),
    searchUsers: vi.fn(),
    sendFriendRequest: vi.fn(),
  },
}));

import { api } from '../api';
import AddFriend from './add-friend';

async function flushPromises() {
  await Promise.resolve();
  await Promise.resolve();
}

function mockRect({
  bottom,
  height,
  left,
  right,
  top,
  width,
}) {
  return {
    bottom,
    height,
    left,
    right,
    top,
    width,
    x: left,
    y: top,
    toJSON: () => ({}),
  };
}

describe('AddFriend search mode', () => {
  let container;
  let root;
  let originalInnerHeight;
  let originalInnerWidth;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    Object.values(api).forEach((mock) => mock.mockReset());
    api.getPendingRequests.mockResolvedValue({ requests: [] });
    api.searchUsers.mockResolvedValue({
      users: [{
        id: 42,
        username: 'developer',
        display_name: '开发者',
      }],
    });
    originalInnerHeight = window.innerHeight;
    originalInnerWidth = window.innerWidth;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      value: originalInnerHeight,
    });
    Object.defineProperty(window, 'innerWidth', {
      configurable: true,
      value: originalInnerWidth,
    });
  });

  async function mount(props = {}) {
    await act(async () => {
      root.render(
        <AddFriend
          currentUser={{ username: 'cycren', display_name: 'Cycren' }}
          onClose={vi.fn()}
          {...props}
        />,
      );
      await flushPromises();
    });
  }

  it('labels the primary action as 搜索 and keeps 发送申请 on result rows', async () => {
    const onClose = vi.fn();
    await mount({ onClose });

    const input = container.querySelector('.oc-friend-search-input');
    expect(input.name).toBe('friend-search');
    expect(input.getAttribute('aria-label')).toBe('好友或助手名字');
    await act(async () => Simulate.change(input, { target: { value: '开发者' } }));
    await act(async () => {
      Simulate.click(container.querySelector('.oc-friend-search-submit'));
      await flushPromises();
    });

    expect(container.querySelector('.oc-friend-search-submit').textContent.trim()).toBe('搜索');
    expect(container.querySelector('.oc-contact-item .oc-btn-default').textContent.trim())
      .toBe('发送申请');

    const trigger = container.querySelector('.oc-friend-search-mode-trigger');
    trigger.getBoundingClientRect = () => mockRect({
      bottom: 82,
      height: 42,
      left: 32,
      right: 128,
      top: 40,
      width: 96,
    });
    await act(async () => Simulate.click(trigger));
    const uidOption = Array.from(document.body.querySelectorAll('.oc-friend-search-mode-option'))
      .find((option) => option.textContent.includes('UID'));
    await act(async () => Simulate.click(uidOption));

    expect(container.querySelector('.oc-contact-item')).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('disables empty searches and does not submit whitespace or an IME confirmation', async () => {
    await mount();
    const input = container.querySelector('.oc-friend-search-input');
    const submit = container.querySelector('.oc-friend-search-submit');
    expect(input.placeholder).toBe('输入名字…');
    expect(submit.disabled).toBe(true);
    await act(async () => Simulate.change(input, { target: { value: '   ' } }));
    await act(async () => Simulate.keyDown(input, { key: 'Enter' }));
    expect(submit.disabled).toBe(true);
    expect(api.searchUsers).not.toHaveBeenCalled();
    expect(container.querySelector('[role="alert"]')).toBeNull();
    await act(async () => Simulate.change(input, { target: { value: '好友' } }));
    expect(submit.disabled).toBe(false);
    await act(async () => input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', isComposing: true, bubbles: true })));
    expect(api.searchUsers).not.toHaveBeenCalled();
    await act(async () => Simulate.keyDown(input, { key: 'Enter' }));
    expect(api.searchUsers).toHaveBeenCalledWith('好友', 'name');
  });

  it('focuses the search, isolates the background, wraps Tab and restores the opener', async () => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();
    const onClose = vi.fn();
    await mount({ onClose });
    const input = container.querySelector('.oc-friend-search-input');
    const first = container.querySelector('.oc-modal-close');
    const last = container.querySelector('textarea');
    expect(document.activeElement).toBe(input);
    expect(opener.hasAttribute('inert')).toBe(true);
    last.focus();
    last.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(first);
    first.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true, cancelable: true }));
    expect(document.activeElement).toBe(last);
    opener.focus();
    expect(document.activeElement).toBe(input);
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', isComposing: true, bubbles: true }));
    expect(onClose).not.toHaveBeenCalled();
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    expect(onClose).toHaveBeenCalledOnce();
    await act(async () => root.render(null));
    expect(opener.hasAttribute('inert')).toBe(false);
    expect(document.activeElement).toBe(opener);
    opener.remove();
  });

  it('only shows empty results after a completed search and prevents duplicate pending searches', async () => {
    let resolve;
    api.searchUsers.mockImplementation(() => new Promise((done) => { resolve = done; }));
    await mount();
    const input = container.querySelector('.oc-friend-search-input');
    await act(async () => Simulate.change(input, { target: { value: '没有此人' } }));
    expect(container.textContent).not.toContain('没有找到匹配的好友或助手');
    const submit = container.querySelector('.oc-friend-search-submit');
    await act(async () => {
      Simulate.click(submit);
      Simulate.keyDown(input, { key: 'Enter' });
    });
    expect(api.searchUsers).toHaveBeenCalledOnce();
    expect(submit.disabled).toBe(true);
    expect(submit.getAttribute('aria-label')).toBe('正在搜索');
    expect(submit.querySelector('.oc-friend-search-spinner')).not.toBeNull();
    await act(async () => resolve({ users: [] }));
    expect(submit.disabled).toBe(false);
    expect(submit.querySelector('.oc-friend-search-spinner')).toBeNull();
    expect(container.textContent).toContain('没有找到匹配的好友或助手');
  });

  it('ignores earlier query results and errors after the mode changes', async () => {
    let resolveOld;
    let rejectNew;
    api.searchUsers
      .mockImplementationOnce(() => new Promise((done) => { resolveOld = done; }))
      .mockImplementationOnce(() => new Promise((_, reject) => { rejectNew = reject; }));
    await mount();
    const input = container.querySelector('.oc-friend-search-input');
    const submit = container.querySelector('.oc-friend-search-submit');
    await act(async () => Simulate.change(input, { target: { value: '旧查询' } }));
    await act(async () => Simulate.click(submit));
    await act(async () => Simulate.change(input, { target: { value: '新查询' } }));
    await act(async () => Simulate.click(submit));
    await act(async () => resolveOld({ users: [{ id: 1, display_name: '过期结果' }] }));
    expect(container.textContent).not.toContain('过期结果');
    expect(submit.disabled).toBe(true);
    await act(async () => Simulate.click(container.querySelector('.oc-friend-search-mode-trigger')));
    const uid = [...document.querySelectorAll('.oc-friend-search-mode-option')].find((node) => node.textContent === '按 UID');
    await act(async () => Simulate.click(uid));
    await act(async () => rejectNew(new Error('过期错误')));
    expect(container.textContent).not.toContain('过期错误');
    expect(submit.disabled).toBe(false);
    expect(container.textContent).not.toContain('没有找到匹配的好友或助手');
  });

  it('shows failed searches as an error rather than an empty result and permits retry', async () => {
    api.searchUsers.mockRejectedValueOnce(new Error('网络暂不可用'));
    await mount();
    await act(async () => Simulate.change(container.querySelector('.oc-friend-search-input'), { target: { value: '好友' } }));
    const submit = container.querySelector('.oc-friend-search-submit');
    await act(async () => Simulate.click(submit));
    expect(container.querySelector('[role="alert"]').textContent).toBe('网络暂不可用');
    expect(container.textContent).not.toContain('没有找到匹配的好友或助手');
    expect(submit.disabled).toBe(false);
  });

  it('shows a localized error when a bot invite code is invalid or expired', async () => {
    api.redeemBotInviteCode.mockRejectedValue(
      new Error('bot invite code is invalid or expired'),
    );
    await mount();

    const input = container.querySelector('[aria-label="助手邀请码"]');
    await act(async () => Simulate.change(input, { target: { value: 'EXPIRED1' } }));
    const redeemButton = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.trim() === '使用邀请码');
    await act(async () => {
      Simulate.click(redeemButton);
      await flushPromises();
    });

    expect(api.redeemBotInviteCode).toHaveBeenCalledWith('EXPIRED1');
    expect(container.querySelector('.oc-form-error').textContent).toBe('邀请码无效或已失效');
  });

  it('focuses assistant invitations from settings and confirms only a successful redemption', async () => {
    let finish;
    api.redeemBotInviteCode.mockImplementation(() => new Promise((resolve) => { finish = resolve; }));
    await mount({ initialFocus: 'invite' });
    const invite = container.querySelector('[aria-label="助手邀请码"]');
    expect(document.activeElement).toBe(invite);
    await act(async () => Simulate.change(invite, { target: { value: 'code123' } }));
    await act(async () => {
      Simulate.keyDown(invite, { key: 'Enter' });
      Simulate.keyDown(invite, { key: 'Enter' });
    });
    expect(api.redeemBotInviteCode).toHaveBeenCalledOnce();
    expect(container.textContent).not.toContain('已添加助手');
    await act(async () => finish({}));
    expect(container.querySelector('[role="status"]').textContent).toBe('已添加助手');
    expect(invite.value).toBe('');
    expect(api.sendFriendRequest).not.toHaveBeenCalled();
  });

  it('labels assistant results separately from human contacts', async () => {
    api.searchUsers.mockResolvedValue({ users: [{ id: 42, display_name: '同名', account_type: 'bot' }, { id: 43, display_name: '同名', account_type: 'human' }] });
    await mount();
    await act(async () => Simulate.change(container.querySelector('.oc-friend-search-input'), { target: { value: '同名' } }));
    await act(async () => Simulate.click(container.querySelector('.oc-friend-search-submit')));
    expect(container.querySelectorAll('.oc-friend-assistant-badge')).toHaveLength(1);
  });

  it('opens a body portal aligned to the trigger and selects a mode', async () => {
    await mount();

    const trigger = container.querySelector('.oc-friend-search-mode-trigger');
    trigger.getBoundingClientRect = () => mockRect({
      bottom: 82,
      height: 42,
      left: 32,
      right: 128,
      top: 40,
      width: 96,
    });

    await act(async () => Simulate.click(trigger));

    const listbox = document.body.querySelector('.oc-friend-search-mode-menu');
    expect(listbox).not.toBeNull();
    expect(listbox.parentElement).toBe(document.body);
    expect(listbox.getAttribute('role')).toBe('listbox');
    expect(listbox.dataset.placement).toBe('bottom');
    expect(listbox.style.position).toBe('fixed');
    expect(listbox.style.left).toBe('32px');
    expect(listbox.style.top).toBe('86px');
    expect(listbox.style.width).toBe('96px');
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    expect(trigger.getAttribute('aria-controls')).toBe(listbox.id);

    const uidOption = Array.from(listbox.querySelectorAll('[role="option"]'))
      .find((option) => option.textContent.includes('UID'));
    await act(async () => Simulate.click(uidOption));

    expect(document.body.querySelector('.oc-friend-search-mode-menu')).toBeNull();
    expect(trigger.textContent).toContain('UID');
    expect(trigger.getAttribute('aria-label')).toBe('搜索模式：按 UID');
    expect(document.activeElement).toBe(container.querySelector('.oc-friend-search-input'));
    expect(container.querySelector('.oc-friend-search-input').placeholder).toBe('输入 UID…');
    expect(container.querySelector('.oc-friend-search-input').inputMode).toBe('numeric');
  });

  it('supports keyboard navigation and Escape with focus restoration', async () => {
    await mount();

    const trigger = container.querySelector('.oc-friend-search-mode-trigger');
    trigger.getBoundingClientRect = () => mockRect({
      bottom: 82,
      height: 42,
      left: 32,
      right: 128,
      top: 40,
      width: 96,
    });

    await act(async () => {
      trigger.focus();
      Simulate.keyDown(trigger, { key: 'ArrowDown' });
    });

    let listbox = document.body.querySelector('.oc-friend-search-mode-menu');
    expect(document.activeElement).toBe(listbox);
    expect(listbox.getAttribute('aria-activedescendant')).toContain('option-1');

    await act(async () => Simulate.keyDown(listbox, { key: 'Escape' }));
    expect(document.body.querySelector('.oc-friend-search-mode-menu')).toBeNull();
    expect(document.activeElement).toBe(trigger);
    expect(trigger.getAttribute('aria-expanded')).toBe('false');

    await act(async () => Simulate.keyDown(trigger, { key: 'End' }));
    listbox = document.body.querySelector('.oc-friend-search-mode-menu');
    await act(async () => Simulate.keyDown(listbox, { key: 'Enter' }));
    expect(trigger.textContent).toContain('UID');
  });

  it('closes on Tab and follows the dialog focus order in both directions', async () => {
    await mount();
    await act(async () => Simulate.change(container.querySelector('.oc-friend-search-input'), { target: { value: '好友' } }));

    const trigger = container.querySelector('.oc-friend-search-mode-trigger');
    const searchInput = container.querySelector('.oc-friend-search-input');
    const searchSubmit = container.querySelector('.oc-friend-search-submit');
    const dialogFocusOrder = Array.from(container.querySelector('[role="dialog"]').querySelectorAll([
      'a[href]',
      'button:not(:disabled)',
      'input:not(:disabled)',
      'select:not(:disabled)',
      'textarea:not(:disabled)',
      '[tabindex]:not([tabindex="-1"])',
    ].join(','))).sort((left, right) => (
      left.compareDocumentPosition(right) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1
    ));
    expect(dialogFocusOrder[dialogFocusOrder.indexOf(trigger) + 1]).toBe(searchSubmit);
    expect(dialogFocusOrder[dialogFocusOrder.indexOf(trigger) - 1]).toBe(searchInput);
    trigger.getBoundingClientRect = () => mockRect({
      bottom: 82,
      height: 42,
      left: 32,
      right: 128,
      top: 40,
      width: 96,
    });

    await act(async () => {
      trigger.focus();
      Simulate.keyDown(trigger, { key: 'ArrowDown' });
    });
    let listbox = document.body.querySelector('.oc-friend-search-mode-menu');
    expect(document.activeElement).toBe(listbox);

    await act(async () => Simulate.keyDown(listbox, { key: 'Tab' }));
    expect(document.body.querySelector('.oc-friend-search-mode-menu')).toBeNull();
    expect(document.activeElement).toBe(searchSubmit);

    await act(async () => {
      trigger.focus();
      Simulate.keyDown(trigger, { key: 'ArrowDown' });
    });
    listbox = document.body.querySelector('.oc-friend-search-mode-menu');

    await act(async () => Simulate.keyDown(listbox, { key: 'Tab', shiftKey: true }));
    expect(document.body.querySelector('.oc-friend-search-mode-menu')).toBeNull();
    expect(document.activeElement).toBe(searchInput);
  });

  it('flips above in a constrained viewport and closes on outside pointer', async () => {
    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      value: 844,
    });
    Object.defineProperty(window, 'innerWidth', {
      configurable: true,
      value: 390,
    });
    await mount();

    const trigger = container.querySelector('.oc-friend-search-mode-trigger');
    trigger.getBoundingClientRect = () => mockRect({
      bottom: 822,
      height: 42,
      left: 24,
      right: 120,
      top: 780,
      width: 96,
    });

    await act(async () => Simulate.click(trigger));

    const listbox = document.body.querySelector('.oc-friend-search-mode-menu');
    expect(listbox.dataset.placement).toBe('top');
    expect(listbox.style.left).toBe('24px');
    expect(listbox.style.top).toBe('708px');
    expect(listbox.style.width).toBe('96px');
    expect(listbox.style.overflowY).toBe('');

    await act(async () => {
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });
    expect(document.body.querySelector('.oc-friend-search-mode-menu')).toBeNull();
  });
});
