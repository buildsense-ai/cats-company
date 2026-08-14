import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    acceptFriend: vi.fn(),
    getPendingRequests: vi.fn(),
    rejectFriend: vi.fn(),
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
    expect(input.getAttribute('aria-label')).toBe('好友名称');
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
      .find((option) => option.textContent.includes('按 UID'));
    await act(async () => Simulate.click(uidOption));

    expect(container.querySelector('.oc-contact-item')).toBeNull();
    expect(onClose).not.toHaveBeenCalled();
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
      .find((option) => option.textContent.includes('按 UID'));
    await act(async () => Simulate.click(uidOption));

    expect(document.body.querySelector('.oc-friend-search-mode-menu')).toBeNull();
    expect(trigger.textContent).toContain('按 UID');
    expect(trigger.getAttribute('aria-label')).toBe('搜索方式：按 UID');
    expect(document.activeElement).toBe(trigger);
    expect(container.querySelector('.oc-friend-search-input').placeholder).toBe('搜索联系人');
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
    expect(trigger.textContent).toContain('按 UID');
  });

  it('closes on Tab and follows the dialog focus order in both directions', async () => {
    await mount();

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
