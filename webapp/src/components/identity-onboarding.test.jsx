import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

import IdentityOnboarding, { IdentityCat } from './identity-onboarding';

describe('IdentityOnboarding', () => {
  let container;
  let root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
  });

  test('changes the illustrated cat as the name changes', async () => {
    await act(async () => root.render(<IdentityOnboarding onComplete={vi.fn()} />));
    const input = container.querySelector('#catsco-display-name');
    const cat = container.querySelector('.cc-identity-cat');
    const emptySeed = cat.getAttribute('data-identity-seed');

    await act(async () => {
      setInputValue(input, 'alex');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });

    expect(container.querySelector('.cc-identity-cat').getAttribute('data-identity-seed'))
      .not.toBe(emptySeed);
    expect(container.textContent).toContain('继续');
  });

  test('requires a name and saves it through the primary action', async () => {
    const onComplete = vi.fn().mockResolvedValue(undefined);
    await act(async () => root.render(<IdentityOnboarding onComplete={onComplete} />));
    const form = container.querySelector('form');
    const input = container.querySelector('#catsco-display-name');

    await act(async () => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
    expect(container.textContent).toContain('请输入一个名字');

    await act(async () => {
      setInputValue(input, 'Alex');
      input.dispatchEvent(new Event('input', { bubbles: true }));
    });
    await act(async () => form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
    expect(onComplete).toHaveBeenCalledWith('Alex');
  });

  test('exposes an accessible name for the generated cat', async () => {
    await act(async () => root.render(<IdentityCat name="Mia" />));
    expect(container.querySelector('svg').getAttribute('aria-label')).toContain('Mia');
  });

  test('uses the current CatsCo wordmark in the onboarding brand', async () => {
    await act(async () => root.render(<IdentityOnboarding onComplete={vi.fn()} />));
    expect(container.querySelector('.cc-identity-onboarding__brand .catsco-brand-name')?.textContent)
      .toBe('CatsCo');
  });

  test('shows the clarified onboarding copy and character counter', async () => {
    await act(async () => root.render(<IdentityOnboarding onComplete={vi.fn()} />));
    expect(container.querySelector('h1')?.textContent).toBe('怎么称呼你？');
    expect(container.querySelector('.cc-identity-onboarding__counter')?.textContent).toBe('0/32');
    expect(container.querySelector('button')?.textContent).toContain('继续');
  });

  test('supports a CatsCo green identity accessory on generated cats', async () => {
    await act(async () => root.render(<IdentityCat name="Mia" />));
    expect(container.querySelector('.cc-identity-cat__brand-badge')).not.toBeNull();
  });

  test('keeps the badge fixed and varies optional glasses by name', async () => {
    const names = ['Mia', 'Alex', '本地预览'];
    for (const name of names) {
      await act(async () => root.render(<IdentityCat name={name} />));
      expect(container.querySelector('.cc-identity-cat__brand-badge')).not.toBeNull();
      expect(container.querySelectorAll('.cc-identity-cat__brand-glasses').length).toBeLessThanOrEqual(1);
    }
  });

  test('keeps sunglasses occasional across generated names', async () => {
    let glassesCount = 0;
    for (let index = 0; index < 20; index += 1) {
      await act(async () => root.render(<IdentityCat name={`CatsCo-${index}`} />));
      if (container.querySelector('.cc-identity-cat__brand-glasses')) glassesCount += 1;
    }
    expect(glassesCount).toBeGreaterThan(0);
    expect(glassesCount).toBeLessThanOrEqual(6);
  });

  test('adjusts belly contrast for light and dark cat palettes', async () => {
    await act(async () => root.render(<IdentityCat name="Mia" />));
    const lightPaletteBelly = container.querySelector('.cc-identity-cat__belly').style.fill;
    await act(async () => root.render(<IdentityCat name="Kira" />));
    const darkPaletteBelly = container.querySelector('.cc-identity-cat__belly').style.fill;
    expect(lightPaletteBelly).toContain('255');
    expect(darkPaletteBelly).toContain('255');
    expect(lightPaletteBelly).not.toBe(darkPaletteBelly);
  });

  test('provides reduced-motion-aware blink and tail animation hooks', async () => {
    await act(async () => root.render(<IdentityCat name="Mia" />));
    expect(container.querySelectorAll('.cc-identity-cat__eye-group')).toHaveLength(2);
    expect(container.querySelector('.cc-identity-cat__tail-motion')).not.toBeNull();
  });

});

function setInputValue(input, value) {
  const valueSetter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    'value',
  ).set;
  valueSetter.call(input, value);
}
