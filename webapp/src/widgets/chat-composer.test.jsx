import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

import ChatComposer, { CHAT_COMPOSER_HINT } from './chat-composer';

describe('ChatComposer', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    vi.clearAllMocks();
  });

  async function renderComposer(extraProps = {}) {
    await act(async () => {
      root.render(
        <ChatComposer
          value=""
          placeholder="输入指令，我帮您完成"
          onChange={vi.fn()}
          onAttachmentToggle={vi.fn()}
          onSend={vi.fn()}
          {...extraProps}
        />,
      );
    });
  }

  it('renders the shared control row and keeps the hint outside the pill', async () => {
    await renderComposer();

    const composer = container.querySelector('.v3-composer');
    const box = composer.querySelector('.v3-composer-box');
    const row = box.querySelector('.v3-composer-row');
    const hint = composer.querySelector('.v3-composer-hint');

    expect(row.querySelector('.v3-attachment-picker > button.v3-composer-plus')).not.toBeNull();
    expect(row.querySelector('textarea.v3-composer-input')).not.toBeNull();
    expect(row.querySelector('.v3-agent-picker')).toBeNull();
    expect(row.querySelector('button.v3-send')).not.toBeNull();
    expect(hint.textContent).toBe(CHAT_COMPOSER_HINT);
    expect(hint.parentElement).toBe(composer);
    expect(box.contains(hint)).toBe(false);
  });

  it('renders the optional Agent selector only when a caller provides it', async () => {
    const onAgentToggle = vi.fn();
    await renderComposer({
      agentName: '代码审查助手',
      agentOpen: true,
      onAgentToggle,
      agentMenu: <div className="test-agent-menu">Agent 菜单</div>,
    });

    const agentButton = container.querySelector('.v3-agent-picker-button');
    expect(agentButton?.textContent).toContain('代码审查助手');
    expect(container.querySelector('.test-agent-menu')).not.toBeNull();

    await act(async () => {
      agentButton.click();
    });
    expect(onAgentToggle).toHaveBeenCalledTimes(1);
  });

  it('forwards mention accessibility attributes to the textarea', async () => {
    await renderComposer({
      textareaProps: {
        'aria-controls': 'mention-picker',
        'aria-expanded': true,
        'aria-activedescendant': 'mention-option-42',
      },
    });

    const textarea = container.querySelector('textarea.v3-composer-input');
    expect(textarea.getAttribute('aria-controls')).toBe('mention-picker');
    expect(textarea.getAttribute('aria-expanded')).toBe('true');
    expect(textarea.getAttribute('aria-activedescendant')).toBe('mention-option-42');
  });

  it('uses the same action slot for stop while working and send after input', async () => {
    const onSend = vi.fn();
    const onStop = vi.fn();
    await renderComposer({ value: '', stop: true, onSend, onStop });

    const stopButton = container.querySelector('button[aria-label="停止当前工作"]');
    expect(stopButton).not.toBeNull();

    await act(async () => {
      stopButton.click();
    });
    expect(onStop).toHaveBeenCalledTimes(1);

    await renderComposer({ value: '追加消息', stop: false, onSend, onStop });
    const sendButton = container.querySelector('button[aria-label="发送"]');
    expect(sendButton).not.toBeNull();
    await act(async () => {
      sendButton.click();
    });
    expect(onSend).toHaveBeenCalledTimes(1);
  });

  it.each([
    ['attachment', { attachmentOpen: true, attachmentMenu: <div className="test-menu">附件菜单</div> }],
    ['Agent', { agentOpen: true, agentMenu: <div className="test-menu">Agent 菜单</div> }],
  ])('closes the open %s menu only when pointerdown occurs outside', async (_label, menuProps) => {
    const onCloseMenus = vi.fn();
    await renderComposer({ ...menuProps, onCloseMenus });

    const menu = container.querySelector('.test-menu');
    await act(async () => {
      menu.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });
    expect(onCloseMenus).not.toHaveBeenCalled();

    await act(async () => {
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });
    expect(onCloseMenus).toHaveBeenCalledTimes(1);
  });

  it.each([
    ['attachment', { attachmentOpen: true, attachmentMenu: <div>附件菜单</div> }],
    ['Agent', { agentOpen: true, agentMenu: <div>Agent 菜单</div> }],
  ])('closes the open %s menu when Escape is pressed', async (_label, menuProps) => {
    const onCloseMenus = vi.fn();
    await renderComposer({ ...menuProps, onCloseMenus });

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });

    expect(onCloseMenus).toHaveBeenCalledTimes(1);
  });

  it.each([
    [
      'attachment menu when the textarea is pressed',
      { attachmentOpen: true, attachmentMenu: <div>附件菜单</div> },
      'textarea.v3-composer-input',
    ],
    [
      'Agent menu when the send area is pressed',
      { agentOpen: true, agentMenu: <div>Agent 菜单</div> },
      'button.v3-send',
    ],
  ])('closes the open %s', async (_label, menuProps, targetSelector) => {
    const onCloseMenus = vi.fn();
    await renderComposer({ ...menuProps, onCloseMenus });

    await act(async () => {
      container.querySelector(targetSelector).dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });

    expect(onCloseMenus).toHaveBeenCalledTimes(1);
  });
});
