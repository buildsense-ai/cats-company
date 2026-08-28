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

  it('shows four explicit themes and selects an available theme', async () => {
    const onThemeChange = vi.fn();
    await renderEditor({ onThemeChange });

    const picker = container.querySelector('[role="radiogroup"][aria-label="界面主题"]');
    expect(picker).toBeTruthy();
    expect(picker.querySelectorAll('[role="radio"]')).toHaveLength(4);
    expect(container.querySelector('[aria-label="浅色主题"]').getAttribute('aria-checked')).toBe('true');

    await act(async () => Simulate.click(container.querySelector('[aria-label="深色主题"]')));
    expect(onThemeChange).toHaveBeenCalledWith('dark');
  });

  it('renders the mobile-native profile hierarchy without removing desktop controls', async () => {
    await renderEditor({ onThemeChange: vi.fn(), onOpenRelay: vi.fn() });

    expect(container.querySelector('.oc-profile-editor-overlay')).toBeTruthy();
    expect(container.querySelector('.oc-profile-avatar-wrap')).toBeTruthy();
    expect(container.querySelector('.oc-profile-mobile-name')?.textContent).toBe('Bruce');
    expect(container.querySelector('.oc-profile-details-section')).toBeTruthy();
    expect(container.querySelector('.oc-profile-account-section')).toBeTruthy();
    expect(container.querySelector('.oc-profile-behavior-section')).toBeTruthy();
    expect(container.querySelector('[aria-label="选择头像"]')).toBeTruthy();
    expect(container.querySelector('.oc-profile-avatar-desktop-action')).toBeTruthy();
  });

  it('uses a grouped mobile settings home while preserving access to existing destinations', async () => {
    const onOpenFeedback = vi.fn();
    const onLogout = vi.fn();
    await renderEditor({
      onThemeChange: vi.fn(),
      onOpenRelay: vi.fn(),
      onOpenFeedback,
      onOpenDownload: vi.fn(),
      onOpenDesktopConnect: vi.fn(),
      onLogout,
    });

    const dialog = container.querySelector('.oc-profile-editor-modal');
    expect(dialog?.dataset.mobilePane).toBe('home');
    expect(container.querySelector('.oc-profile-mobile-home')?.textContent).toContain('账户');
    expect(container.querySelector('.oc-profile-mobile-home')?.textContent).toContain('应用');
    expect(container.querySelector('.oc-profile-mobile-home')?.textContent).toContain('连接与支持');

    const personalProfile = Array.from(container.querySelectorAll('.oc-profile-mobile-home button'))
      .find((button) => button.textContent.includes('个人资料'));
    await act(async () => Simulate.click(personalProfile));
    expect(dialog?.dataset.mobilePane).toBe('profile');

    const back = container.querySelector('[aria-label="返回设置"]');
    await act(async () => Simulate.click(back));
    expect(dialog?.dataset.mobilePane).toBe('home');

    const feedback = Array.from(container.querySelectorAll('.oc-profile-mobile-home button'))
      .find((button) => button.textContent.includes('意见反馈'));
    await act(async () => Simulate.click(feedback));
    expect(onOpenFeedback).toHaveBeenCalledTimes(1);
  });

  it('opens password verification instead of selecting a locked liquid theme', async () => {
    const onThemeChange = vi.fn();
    const onUnlockLiquidTheme = vi.fn().mockResolvedValue({ ok: true });
    await renderEditor({ onThemeChange, onUnlockLiquidTheme });

    await act(async () => Simulate.click(container.querySelector('[aria-label="液态浅色主题，需要密码"]')));
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
    expect(onUnlockLiquidTheme).toHaveBeenCalledWith('test-password', 'liquid');
  });

  it('unlocks directly into the selected green liquid variant', async () => {
    const onUnlockLiquidTheme = vi.fn().mockResolvedValue({ ok: true });
    await renderEditor({ onThemeChange: vi.fn(), onUnlockLiquidTheme });

    await act(async () => Simulate.click(
      container.querySelector('[aria-label="液态绿色主题，需要密码"]'),
    ));
    const passwordInput = container.querySelector('[aria-label="液态主题密码"]');
    await act(async () => {
      Simulate.change(passwordInput, { target: { value: 'test-password' } });
    });
    await act(async () => {
      Simulate.submit(container.querySelector('.oc-liquid-unlock'));
      await Promise.resolve();
    });

    expect(onUnlockLiquidTheme).toHaveBeenCalledWith('test-password', 'liquid-green');
  });

  it('allows an entitled account to select the liquid theme directly', async () => {
    const onThemeChange = vi.fn();
    await renderEditor({
      theme: 'dark',
      onThemeChange,
      liquidThemeAccess: { loading: false, unlocked: true },
    });

    const liquid = container.querySelector('[aria-label="液态浅色主题"]');
    expect(liquid).toBeTruthy();
    await act(async () => Simulate.click(liquid));
    expect(onThemeChange).toHaveBeenCalledWith('liquid');

    const greenLiquid = container.querySelector('[aria-label="液态绿色主题"]');
    expect(greenLiquid).toBeTruthy();
    await act(async () => Simulate.click(greenLiquid));
    expect(onThemeChange).toHaveBeenCalledWith('liquid-green');
  });
});
