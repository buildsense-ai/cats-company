import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { act, Simulate } from 'react-dom/test-utils';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import EditableConversationTitle from './editable-conversation-title';

const topbarCss = readFileSync(resolve(process.cwd(), 'src/css/catsco-topbar.css'), 'utf8');

function Harness({ onSave }) {
  const [title, setTitle] = useState('Original task');
  return (
    <EditableConversationTitle
      title={title}
      editable
      onSave={async (nextTitle) => {
        await onSave(nextTitle);
        setTitle(nextTitle);
      }}
    />
  );
}

describe('EditableConversationTitle', () => {
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

  async function mount(onSave = vi.fn().mockResolvedValue(undefined)) {
    await act(async () => {
      root.render(<Harness onSave={onSave} />);
      await Promise.resolve();
    });
    return onSave;
  }

  it('saves a changed title with Enter', async () => {
    const onSave = await mount();

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="修改对话标题 Original task"]'));
    });
    const input = container.querySelector('input[aria-label="修改对话标题 Original task"]');
    expect(input).toBeTruthy();

    await act(async () => {
      Simulate.change(input, { target: { value: 'Renamed task' } });
    });
    await act(async () => {
      Simulate.keyDown(input, { key: 'Enter' });
      await Promise.resolve();
    });

    expect(onSave).toHaveBeenCalledWith('Renamed task');
    expect(container.querySelector('.v3-shell-title-button')?.textContent).toBe('Renamed task');
  });

  it('saves on blur and cancels with Escape', async () => {
    const onSave = await mount();

    await act(async () => {
      Simulate.click(container.querySelector('.v3-shell-title-button'));
    });
    let input = container.querySelector('.v3-shell-title-input');
    await act(async () => {
      Simulate.change(input, { target: { value: 'Blurred title' } });
    });
    await act(async () => {
      Simulate.blur(input);
      await Promise.resolve();
    });
    expect(onSave).toHaveBeenCalledWith('Blurred title');

    await act(async () => {
      Simulate.click(container.querySelector('.v3-shell-title-button'));
    });
    input = container.querySelector('.v3-shell-title-input');
    await act(async () => {
      Simulate.change(input, { target: { value: 'Discard me' } });
    });
    await act(async () => {
      Simulate.keyDown(input, { key: 'Escape' });
    });

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(container.querySelector('.v3-shell-title-button')?.textContent).toBe('Blurred title');
  });

  it('uses a neutral single focus border for title editing', () => {
    const baseRule = topbarCss.match(/\.v3-shell-title-input\s*\{[^}]*\}/)?.[0];
    const focusRule = topbarCss.match(
      /\.v3-shell-title-input:focus,\s*\.v3-shell-title-input:focus-visible\s*\{[^}]*\}/,
    )?.[0];

    expect(baseRule).toContain('border: 1px solid var(--cc-border);');
    expect(focusRule).toContain('border-color: var(--cc-border-strong);');
    expect(focusRule).toContain('outline: none;');
    expect(focusRule).toContain('box-shadow: none;');
    expect(focusRule).not.toContain('var(--cc-accent)');

    const buttonFocusRule = topbarCss.match(/\.v3-shell-title-button:focus-visible\s*\{[^}]*\}/)?.[0];
    expect(buttonFocusRule).toContain('border-color: var(--cc-border-strong);');
    expect(buttonFocusRule).toContain('outline: 1px solid var(--cc-border-strong);');
    expect(buttonFocusRule).not.toContain('var(--cc-accent)');
  });
});
