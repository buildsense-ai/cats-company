import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    createChannelIdentityMobileLink: vi.fn(),
    createChannelGroupMobileLink: vi.fn(),
    getChannelPrivateBindings: vi.fn(),
    unlinkChannelPrivateBinding: vi.fn(),
    getWeixinClawBotQRCodeStatus: vi.fn(),
  },
}));

vi.mock('./qr-code', () => ({
  default: ({ value }) => React.createElement('div', { 'data-testid': 'qr-code' }, value),
}));

import MobileChannelBindModal from './mobile-channel-bind-modal';
import { api } from '../api';

const SELECTED_AT = '2026-07-20T03:00:00Z';

describe('MobileChannelBindModal Feishu bindings', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.createChannelIdentityMobileLink.mockReset().mockResolvedValue({ qr_value: 'https://example.test/bind' });
    api.createChannelGroupMobileLink.mockReset().mockResolvedValue({ qr_value: 'https://example.test/group' });
    api.getChannelPrivateBindings.mockReset().mockResolvedValue({ bindings: [] });
    api.unlinkChannelPrivateBinding.mockReset().mockResolvedValue({});
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
  });

  async function renderAndOpenFeishu(props = {}) {
    await act(async () => {
      root.render(React.createElement(MobileChannelBindModal, {
        agentUid: 42,
        agentName: 'Saturday',
        onClose: vi.fn(),
        ...props,
      }));
      await Promise.resolve();
    });
    const feishuButton = Array.from(container.querySelectorAll('.mobile-channel-tabs button'))[1];
    await act(async () => {
      Simulate.click(feishuButton);
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  test('keeps the unbound Feishu view unchanged', async () => {
    await renderAndOpenFeishu();

    expect(api.getChannelPrivateBindings).toHaveBeenCalledWith({ agentUid: 42, groupId: null, topicId: null });
    expect(container.querySelector('[data-testid="qr-code"]')).not.toBeNull();
    expect(container.querySelector('.mobile-channel-bindings')).toBeNull();
  });

  test('shows the bound Feishu user and unlinks private sync', async () => {
    api.getChannelPrivateBindings.mockResolvedValue({
      bindings: [{ binding_key: 'binding-1', display_name: '陈大为', avatar_url: '', selected_at: SELECTED_AT }],
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await renderAndOpenFeishu();

    expect(container.textContent).toContain('已绑定飞书用户');
    expect(container.textContent).toContain('陈大为');
    const unlinkButton = container.querySelector('.mobile-channel-unlink-btn');
    expect(unlinkButton.getAttribute('aria-label')).toBe('解除陈大为的飞书绑定');

    await act(async () => {
      Simulate.click(unlinkButton);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.unlinkChannelPrivateBinding).toHaveBeenCalledWith({
      bindingKey: 'binding-1', agentUid: 42, groupId: null, topicId: null, selectedAt: SELECTED_AT,
    });
    expect(container.textContent).toContain('已解除飞书私聊绑定');
    expect(container.textContent).not.toContain('陈大为');
  });

  test('keeps a failed binding row and offers retry', async () => {
    api.getChannelPrivateBindings.mockResolvedValue({
      bindings: [{ binding_key: 'binding-1', display_name: '陈大为', selected_at: SELECTED_AT }],
    });
    api.unlinkChannelPrivateBinding.mockRejectedValue(new Error('解绑失败'));
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await renderAndOpenFeishu();

    await act(async () => {
      Simulate.click(container.querySelector('.mobile-channel-unlink-btn'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('解绑失败');
    expect(container.textContent).toContain('陈大为');
    expect(Array.from(container.querySelectorAll('button')).some((button) => button.textContent === '重试')).toBe(true);
  });

  test('does not unlink when confirmation is cancelled', async () => {
    api.getChannelPrivateBindings.mockResolvedValue({
      bindings: [{ binding_key: 'binding-1', display_name: '陈大为', selected_at: SELECTED_AT }],
    });
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    await renderAndOpenFeishu();

    await act(async () => {
      Simulate.click(container.querySelector('.mobile-channel-unlink-btn'));
      await Promise.resolve();
    });

    expect(api.unlinkChannelPrivateBinding).not.toHaveBeenCalled();
    expect(container.textContent).toContain('陈大为');
  });

  test('uses the CatsCo group target for listing and unlinking', async () => {
    api.getChannelPrivateBindings.mockResolvedValue({
      bindings: [{ binding_key: 'binding-group', display_name: '林益', selected_at: SELECTED_AT }],
    });
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    await renderAndOpenFeishu({
      agentUid: null,
      agentName: null,
      groupId: 697,
      topicId: 'grp_697',
      groupName: '查云端log',
    });

    expect(api.getChannelPrivateBindings).toHaveBeenCalledWith({
      agentUid: null, groupId: 697, topicId: 'grp_697',
    });
    await act(async () => {
      Simulate.click(container.querySelector('.mobile-channel-unlink-btn'));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('群聊“查云端log”'));
    expect(api.unlinkChannelPrivateBinding).toHaveBeenCalledWith({
      bindingKey: 'binding-group', agentUid: null, groupId: 697, topicId: 'grp_697', selectedAt: SELECTED_AT,
    });
  });

  test('disables unlink during refresh and clears stale rows when refresh fails', async () => {
    api.getChannelPrivateBindings.mockResolvedValueOnce({
      bindings: [{ binding_key: 'binding-1', display_name: '陈大为', selected_at: SELECTED_AT }],
    });
    await renderAndOpenFeishu();
    expect(container.textContent).toContain('陈大为');

    let rejectRefresh;
    api.getChannelPrivateBindings.mockImplementationOnce(() => new Promise((resolve, reject) => {
      rejectRefresh = reject;
    }));
    const refreshButton = Array.from(container.querySelectorAll('.mobile-channel-actions button'))[1];
    await act(async () => {
      Simulate.click(refreshButton);
      await Promise.resolve();
    });
    expect(container.querySelector('.mobile-channel-unlink-btn').disabled).toBe(true);

    await act(async () => {
      rejectRefresh(new Error('状态读取失败'));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.textContent).toContain('状态读取失败');
    expect(container.textContent).not.toContain('陈大为');
  });

  test('does not show a late Feishu response after switching tabs', async () => {
    let resolveBindings;
    api.getChannelPrivateBindings.mockImplementationOnce(() => new Promise((resolve) => {
      resolveBindings = resolve;
    }));
    await renderAndOpenFeishu();
    const weixinButton = Array.from(container.querySelectorAll('.mobile-channel-tabs button'))[0];
    await act(async () => {
      Simulate.click(weixinButton);
      await Promise.resolve();
      resolveBindings({ bindings: [{ binding_key: 'late', display_name: '迟到用户', selected_at: SELECTED_AT }] });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.mobile-channel-bindings')).toBeNull();
    expect(container.textContent).not.toContain('迟到用户');
  });
});
