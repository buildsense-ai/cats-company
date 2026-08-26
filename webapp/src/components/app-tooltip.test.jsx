import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import AppTooltip from './app-tooltip';

function dispatchPointer(target, type, options = {}) {
  const event = new MouseEvent(type, { bubbles: true, ...options });
  Object.defineProperty(event, 'pointerType', {
    configurable: true,
    value: options.pointerType || 'mouse',
  });
  target.dispatchEvent(event);
}

describe('AppTooltip', () => {
  let container;
  let root;
  let originalInnerWidth;
  let originalOffsetWidth;
  let originalOffsetHeight;

  beforeEach(async () => {
    vi.useFakeTimers();
    originalInnerWidth = window.innerWidth;
    originalOffsetWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetWidth');
    originalOffsetHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight');
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: 800 });
    Object.defineProperty(HTMLElement.prototype, 'offsetWidth', {
      configurable: true,
      get() {
        return this.classList?.contains('cc-app-tooltip') ? 120 : 0;
      },
    });
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', {
      configurable: true,
      get() {
        return this.classList?.contains('cc-app-tooltip') ? 30 : 0;
      },
    });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root.render(
        <>
          <button type="button" title="任务操作" aria-describedby="existing-help">...</button>
          <button type="button" title="刷新" aria-label="刷新" />
          <div data-cc-tooltips="off">
            <button type="button" title="SkillHub" aria-label="打开 SkillHub" />
          </div>
          <AppTooltip />
        </>,
      );
    });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    document.body.querySelectorAll('.cc-app-tooltip').forEach((node) => node.remove());
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: originalInnerWidth });
    if (originalOffsetWidth) Object.defineProperty(HTMLElement.prototype, 'offsetWidth', originalOffsetWidth);
    else delete HTMLElement.prototype.offsetWidth;
    if (originalOffsetHeight) Object.defineProperty(HTMLElement.prototype, 'offsetHeight', originalOffsetHeight);
    else delete HTMLElement.prototype.offsetHeight;
    vi.useRealTimers();
  });

  it('replaces the native title and shows a positioned tooltip after the hover delay', async () => {
    const button = container.querySelector('button');
    button.getBoundingClientRect = () => ({
      left: 200, right: 240, top: 200, bottom: 240, width: 40, height: 40,
    });

    dispatchPointer(button, 'pointerover');
    expect(button.hasAttribute('title')).toBe(false);
    expect(button.dataset.ccTooltip).toBe('任务操作');
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();

    await act(async () => {
      await vi.advanceTimersByTimeAsync(320);
    });

    const tooltip = document.body.querySelector('[role="tooltip"]');
    expect(tooltip?.textContent).toBe('任务操作');
    expect(tooltip?.classList.contains('is-top')).toBe(true);
    expect(tooltip?.classList.contains('is-positioned')).toBe(true);
    expect(tooltip?.style.left).toBe('220px');
    expect(tooltip?.style.top).toBe('190px');
    expect(button.getAttribute('aria-describedby')).toBe('existing-help cc-app-tooltip');
  });

  it('shows immediately for keyboard focus and restores aria-describedby on Escape', async () => {
    const button = container.querySelector('button');
    button.getBoundingClientRect = () => ({
      left: 200, right: 240, top: 200, bottom: 240, width: 40, height: 40,
    });

    await act(async () => button.focus());
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe('任务操作');

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });

    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();
    expect(button.getAttribute('aria-describedby')).toBe('existing-help');
  });

  it('keeps a focused tooltip open when the pointer leaves the button', async () => {
    const button = container.querySelector('button');
    button.getBoundingClientRect = () => ({
      left: 200, right: 240, top: 200, bottom: 240, width: 40, height: 40,
    });

    await act(async () => button.focus());
    dispatchPointer(button, 'pointerout', { relatedTarget: document.body });

    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe('任务操作');
  });

  it('dismisses the tooltip on pointer press without suppressing later keyboard focus', async () => {
    const button = container.querySelector('button');
    button.getBoundingClientRect = () => ({
      left: 200, right: 240, top: 200, bottom: 240, width: 40, height: 40,
    });

    dispatchPointer(button, 'pointerover');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(320);
    });
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe('任务操作');

    await act(async () => {
      dispatchPointer(button, 'pointerdown');
      button.focus();
    });
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();
    expect(button.dataset.ccPointerFocus).toBe('true');

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
    });
    expect(button.dataset.ccPointerFocus).toBeUndefined();
  });

  it('flips below buttons near the viewport top', async () => {
    const button = container.querySelectorAll('button')[1];
    button.getBoundingClientRect = () => ({
      left: 300, right: 340, top: 4, bottom: 44, width: 40, height: 40,
    });

    await act(async () => button.focus());

    const tooltip = document.body.querySelector('[role="tooltip"]');
    expect(tooltip?.classList.contains('is-bottom')).toBe(true);
    expect(tooltip?.style.top).toBe('54px');
  });

  it('stays inside the viewport while keeping the arrow aimed at the trigger', async () => {
    const button = container.querySelector('button');
    button.getBoundingClientRect = () => ({
      left: 2, right: 34, top: 200, bottom: 232, width: 32, height: 32,
    });

    await act(async () => button.focus());

    const tooltip = document.body.querySelector('[role="tooltip"]');
    expect(tooltip?.style.left).toBe('68px');
    expect(tooltip?.style.getPropertyValue('--cc-tooltip-arrow-left')).toBe('10px');
  });

  it('does not open hover tooltips for touch pointers', async () => {
    const button = container.querySelector('button');
    dispatchPointer(button, 'pointerover', { pointerType: 'touch' });

    await act(async () => {
      await vi.advanceTimersByTimeAsync(400);
    });

    expect(button.getAttribute('title')).toBe('任务操作');
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();
  });

  it('suppresses tooltip text inside regions that opt out while keeping the accessible name', async () => {
    const button = container.querySelector('[data-cc-tooltips="off"] button');

    dispatchPointer(button, 'pointerover');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(400);
    });

    expect(button.hasAttribute('title')).toBe(false);
    expect(button.getAttribute('aria-label')).toBe('打开 SkillHub');
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
      button.focus();
    });
    expect(document.body.querySelector('[role="tooltip"]')).toBeNull();
  });
});
