import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    updateMe: vi.fn(),
    uploadFile: vi.fn(),
  },
}));

import ProfileEditor from './profile-editor';

describe('ProfileEditor appearance settings', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  async function renderEditor(props = {}) {
    await act(async () => {
      root.render(
        <ProfileEditor
          user={{ uid: 7, username: 'bruce', display_name: 'Bruce' }}
          theme="light"
          onClose={vi.fn()}
          onSaved={vi.fn()}
          {...props}
        />,
      );
    });
  }

  it('shows three explicit themes and selects an available theme', async () => {
    const onThemeChange = vi.fn();
    await renderEditor({ onThemeChange });

    const picker = container.querySelector('[role="radiogroup"][aria-label="界面主题"]');
    expect(picker).toBeTruthy();
    expect(picker.querySelectorAll('[role="radio"]')).toHaveLength(3);
    expect(container.querySelector('[aria-label="浅色主题"]').getAttribute('aria-checked')).toBe('true');

    await act(async () => Simulate.click(container.querySelector('[aria-label="深色主题"]')));
    expect(onThemeChange).toHaveBeenCalledWith('dark');
  });

  it('opens password verification instead of selecting a locked liquid theme', async () => {
    const onThemeChange = vi.fn();
    const onUnlockLiquidTheme = vi.fn().mockResolvedValue({ ok: true });
    await renderEditor({ onThemeChange, onUnlockLiquidTheme });

    await act(async () => Simulate.click(container.querySelector('[aria-label="液态主题，需要密码"]')));
    expect(onThemeChange).not.toHaveBeenCalled();
    const passwordInput = container.querySelector('[aria-label="液态主题密码"]');
    expect(passwordInput).toBeTruthy();
    expect(passwordInput.getAttribute('type')).toBe('password');

    await act(async () => {
      Simulate.change(passwordInput, { target: { value: 'test-password' } });
    });
    await act(async () => {
      Simulate.submit(container.querySelector('.oc-liquid-unlock'));
      await Promise.resolve();
    });
    expect(onUnlockLiquidTheme).toHaveBeenCalledWith('test-password');
  });

  it('allows an entitled account to select the liquid theme directly', async () => {
    const onThemeChange = vi.fn();
    await renderEditor({
      theme: 'dark',
      onThemeChange,
      liquidThemeAccess: { loading: false, unlocked: true },
    });

    const liquid = container.querySelector('[aria-label="液态主题"]');
    expect(liquid).toBeTruthy();
    await act(async () => Simulate.click(liquid));
    expect(onThemeChange).toHaveBeenCalledWith('liquid');
  });
});
