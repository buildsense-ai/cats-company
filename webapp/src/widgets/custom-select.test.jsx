import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

import CustomSelect from './custom-select';

function rect({ bottom, left, top, width }) {
  return {
    bottom,
    height: bottom - top,
    left,
    right: left + width,
    top,
    width,
    x: left,
    y: top,
    toJSON: () => ({}),
  };
}

describe('CustomSelect', () => {
  let container;
  let root;
  let originalInnerHeight;
  let originalInnerWidth;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    originalInnerHeight = window.innerHeight;
    originalInnerWidth = window.innerWidth;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    document.body.querySelectorAll('.v3-custom-model-select-options.is-portal')
      .forEach((element) => element.remove());
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
        <CustomSelect
          ariaLabel="Model protocol"
          onValueChange={vi.fn()}
          value="alpha"
          {...props}
        >
          <option value="alpha">Alpha</option>
          <option value="beta" disabled>Beta</option>
          <option value="gamma">Gamma</option>
        </CustomSelect>,
      );
    });
    return container.querySelector('.v3-custom-model-select-trigger');
  }

  it('renders in a body portal without changing an overflow ancestor', async () => {
    const overflowAncestor = document.createElement('div');
    overflowAncestor.style.overflow = 'auto';
    overflowAncestor.scrollTop = 37;
    Object.defineProperty(overflowAncestor, 'scrollHeight', {
      configurable: true,
      value: 800,
    });
    overflowAncestor.appendChild(container);
    document.body.appendChild(overflowAncestor);

    const trigger = await mount();
    trigger.getBoundingClientRect = () => rect({
      bottom: 82,
      left: 32,
      top: 40,
      width: 160,
    });

    await act(async () => Simulate.click(trigger));

    const listbox = document.body.querySelector('.v3-custom-model-select-options.is-portal');
    expect(listbox).not.toBeNull();
    expect(listbox.parentElement).toBe(document.body);
    expect(listbox.style.position).toBe('fixed');
    expect(listbox.style.left).toBe('32px');
    expect(listbox.style.width).toBe('160px');
    expect(overflowAncestor.scrollTop).toBe(37);
    expect(overflowAncestor.scrollHeight).toBe(800);

    overflowAncestor.remove();
  });

  it('flips above when the viewport has no useful space below', async () => {
    Object.defineProperty(window, 'innerHeight', {
      configurable: true,
      value: 300,
    });
    Object.defineProperty(window, 'innerWidth', {
      configurable: true,
      value: 390,
    });
    const trigger = await mount();
    trigger.getBoundingClientRect = () => rect({
      bottom: 280,
      left: 24,
      top: 238,
      width: 180,
    });

    await act(async () => Simulate.click(trigger));

    const listbox = document.body.querySelector('.v3-custom-model-select-options.is-portal');
    expect(Number.parseFloat(listbox.style.top)).toBeLessThan(238);
    expect(listbox.style.left).toBe('24px');
    expect(listbox.style.maxHeight).toBe('226px');
  });

  it('propagates density and surface classes to the portal', async () => {
    const trigger = await mount({
      density: 'compact',
      menuClassName: 'test-select-menu',
      optionClassName: 'test-select-option',
      triggerClassName: 'test-select-trigger',
    });
    trigger.getBoundingClientRect = () => rect({
      bottom: 72,
      left: 24,
      top: 40,
      width: 96,
    });

    await act(async () => Simulate.click(trigger));

    const listbox = document.body.querySelector('.test-select-menu');
    expect(trigger.classList.contains('test-select-trigger')).toBe(true);
    expect(listbox.classList.contains('is-compact')).toBe(true);
    expect(listbox.dataset.placement).toBe('bottom');
    expect(listbox.querySelector('.test-select-option')).not.toBeNull();
  });

  it('exposes full string labels while keeping option text in a truncation wrapper', async () => {
    const trigger = await mount();
    expect(trigger.title).toBe('Alpha');
    trigger.getBoundingClientRect = () => rect({
      bottom: 82,
      left: 32,
      top: 40,
      width: 120,
    });

    await act(async () => Simulate.click(trigger));

    const gamma = [...document.body.querySelectorAll('.v3-custom-model-select-option')]
      .find((option) => option.textContent.includes('Gamma'));
    expect(gamma.title).toBe('Gamma');
    expect(gamma.querySelector('.v3-custom-model-select-option-label')?.textContent).toBe('Gamma');
  });

  it('supports disabled-option skipping, edge keys, selection, and focus restoration', async () => {
    const onValueChange = vi.fn();
    const trigger = await mount({ onValueChange });
    trigger.getBoundingClientRect = () => rect({
      bottom: 82,
      left: 32,
      top: 40,
      width: 160,
    });

    await act(async () => {
      trigger.focus();
      Simulate.keyDown(trigger, { key: 'ArrowDown' });
    });

    let listbox = document.body.querySelector('[role="listbox"][aria-label="Model protocol"]');
    expect(document.activeElement).toBe(listbox);
    expect(listbox.getAttribute('aria-activedescendant')).toContain('option-2');

    await act(async () => Simulate.keyDown(listbox, { key: 'Home' }));
    expect(listbox.getAttribute('aria-activedescendant')).toContain('option-0');

    await act(async () => Simulate.keyDown(listbox, { key: 'End' }));
    expect(listbox.getAttribute('aria-activedescendant')).toContain('option-2');

    await act(async () => Simulate.keyDown(listbox, { key: 'Enter' }));
    expect(onValueChange).toHaveBeenCalledWith('gamma');
    expect(document.body.querySelector('[role="listbox"][aria-label="Model protocol"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    await act(async () => Simulate.click(trigger));
    listbox = document.body.querySelector('[role="listbox"][aria-label="Model protocol"]');
    await act(async () => Simulate.keyDown(listbox, { key: 'Escape' }));
    expect(document.body.querySelector('[role="listbox"][aria-label="Model protocol"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('closes on outside pointer and Tab', async () => {
    const trigger = await mount();
    trigger.getBoundingClientRect = () => rect({
      bottom: 82,
      left: 32,
      top: 40,
      width: 160,
    });

    await act(async () => Simulate.click(trigger));
    await act(async () => {
      document.body.dispatchEvent(new Event('pointerdown', { bubbles: true }));
    });
    expect(document.body.querySelector('[role="listbox"][aria-label="Model protocol"]')).toBeNull();

    await act(async () => Simulate.click(trigger));
    const listbox = document.body.querySelector('[role="listbox"][aria-label="Model protocol"]');
    await act(async () => Simulate.keyDown(listbox, { key: 'Tab' }));
    expect(document.body.querySelector('[role="listbox"][aria-label="Model protocol"]')).toBeNull();
    expect(document.activeElement).not.toBe(listbox);
  });
});
