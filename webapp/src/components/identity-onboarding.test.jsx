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
    expect(container.textContent).toContain('确认名字');
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
});

function setInputValue(input, value) {
  const valueSetter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    'value',
  ).set;
  valueSetter.call(input, value);
}
