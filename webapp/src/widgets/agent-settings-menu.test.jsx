import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import AgentSettingsMenu from './agent-settings-menu';

describe('Agent settings submenu', () => {
  let container, root, onManage, onAdd;
  beforeEach(async () => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    onManage = vi.fn(); onAdd = vi.fn();
    await act(async () => root.render(<AgentSettingsMenu onManage={onManage} onAdd={onAdd} />));
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.useRealTimers(); });
  const trigger = () => container.querySelector('[aria-haspopup="menu"]');
  const menu = () => container.querySelector('[role="menu"]');

  it('opens on hover, allows pointer travel to the submenu, and closes after leaving', async () => {
    vi.useFakeTimers();
    await act(async () => Simulate.mouseEnter(trigger()));
    expect(menu().textContent).toBe('Agent 助手管理添加AI 助手');
    const wrapper = container.firstChild;
    await act(async () => Simulate.mouseLeave(wrapper));
    await act(async () => vi.advanceTimersByTime(100));
    await act(async () => Simulate.mouseEnter(wrapper));
    await act(async () => vi.advanceTimersByTime(160));
    expect(menu()).not.toBeNull();
    await act(async () => Simulate.click(menu().querySelectorAll('button')[1]));
    expect(onAdd).toHaveBeenCalledOnce();
    expect(menu()).toBeNull();
    await act(async () => Simulate.mouseEnter(trigger()));
    await act(async () => Simulate.mouseLeave(wrapper));
    await act(async () => vi.advanceTimersByTime(160));
    expect(menu()).toBeNull();
  });

  it('supports arrows and Escape without closing the parent menu', async () => {
    trigger().focus();
    await act(async () => Simulate.keyDown(trigger(), { key: 'ArrowRight' }));
    const buttons = menu().querySelectorAll('button');
    expect(document.activeElement).toBe(buttons[0]);
    await act(async () => Simulate.keyDown(buttons[0], { key: 'ArrowDown' }));
    expect(document.activeElement).toBe(buttons[1]);
    const escape = new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true });
    const outer = vi.fn(); document.addEventListener('keydown', outer);
    await act(async () => buttons[1].dispatchEvent(escape));
    document.removeEventListener('keydown', outer);
    expect(outer).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(trigger());
    expect(menu()).toBeNull();
  });

  it('opens on click, delegates management, and dismisses on an outside click', async () => {
    await act(async () => Simulate.click(trigger()));
    await act(async () => Simulate.click(menu().querySelector('button')));
    expect(onManage).toHaveBeenCalledOnce();
    await act(async () => Simulate.click(trigger()));
    await act(async () => document.body.dispatchEvent(new Event('pointerdown', { bubbles: true })));
    expect(menu()).toBeNull();
  });
});
