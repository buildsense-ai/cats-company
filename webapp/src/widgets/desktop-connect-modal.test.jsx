import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import DesktopConnectModal, { resolveConnectedDesktopDevice } from './desktop-connect-modal';

vi.mock('../api', () => ({
  api: {
    createDesktopConnectSession: vi.fn(),
    getDesktopConnectStatus: vi.fn(),
    getAgents: vi.fn(),
    getCatsCoDesktopReleases: vi.fn(),
    getDevices: vi.fn(),
    getDeviceAudit: vi.fn(),
    unlinkDevice: vi.fn(),
    getConversationDevice: vi.fn(),
    confirmConversationDevice: vi.fn(),
    getGroupDeviceShares: vi.fn(),
    setGroupDeviceShare: vi.fn(),
  },
  getToken: () => 'test-session',
  getAuthRevision: () => 1,
  isCurrentAuthSession: () => true,
  getApiBaseURL: vi.fn(() => 'https://app.catsco.cc'),
  getWebSocketURL: vi.fn(() => 'wss://app.catsco.cc/v0/channels'),
}));

import { api } from '../api';

describe('DesktopConnectModal', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    window.localStorage.clear();
    api.getCatsCoDesktopReleases.mockResolvedValue({ version: '1.5.0' });
    api.createDesktopConnectSession.mockReset();
    api.getDesktopConnectStatus.mockReset();
    api.getDevices.mockResolvedValue({ devices: [] });
    api.getDeviceAudit.mockResolvedValue({ events: [] });
    api.unlinkDevice.mockReset();
    api.getConversationDevice.mockReset();
    api.confirmConversationDevice.mockReset();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  test('combines connection and downloads in one modal with downloads as the requested initial section', async () => {
    await act(async () => {
      root.render(<DesktopConnectModal onClose={vi.fn()} initialMode="download" />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('CatsCo 桌面端');
    expect(container.textContent).toContain('连接我的电脑助手');
    expect(container.textContent).toContain('v1.5.0');
    expect(container.textContent).toContain('收起下载');
    const recommended = container.querySelector('.catsco-download-card-primary');
    expect(recommended).not.toBeNull();
    expect([
      'Windows',
      'macOS Apple Silicon',
      'macOS Intel',
      'Linux AppImage',
    ].some((platform) => recommended.textContent.includes(platform))).toBe(true);
    const moreDownloads = container.querySelector('.catsco-download-more');
    expect(moreDownloads.firstChild.textContent).toBe('其他系统版本');
    expect(moreDownloads.lastElementChild?.tagName).toBe('svg');
  });

  test.each(['connect', 'download', 'computers'])('contains keyboard focus and handles Escape in %s mode', async initialMode => {
    const opener = document.createElement('button');
    document.body.appendChild(opener);
    opener.focus();
    const onClose = vi.fn();
    await act(async () => root.render(<DesktopConnectModal initialMode={initialMode} onClose={onClose} />));
    const heading = container.querySelector('h3');
    expect(document.activeElement).toBe(heading);
    expect(opener.closest('[inert]')).not.toBeNull();
    const buttons = [...container.querySelectorAll('button')].filter(button => !button.disabled && !button.closest('[hidden]'));
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', cancelable: true })));
    expect(document.activeElement).toBe(buttons[0]);
    buttons[0].focus();
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, cancelable: true })));
    expect(document.activeElement).toBe(buttons.at(-1));
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', isComposing: true })));
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', cancelable: true })));
    expect(onClose).toHaveBeenCalledOnce();
    await act(async () => root.render(null));
    expect(document.activeElement).toBe(opener);
    expect(opener.hasAttribute('inert')).toBe(false);
    opener.remove();
  });

  test.each([true, false])('restores a live opener or the stable account trigger (opener removed: %s)', async removeOpener => {
    const opener = document.createElement('button');
    const fallback = document.createElement('button');
    document.body.append(opener, fallback);
    opener.focus();
    await act(async () => root.render(<DesktopConnectModal onClose={vi.fn()} returnFocusRef={{ current: fallback }} />));
    if (removeOpener) opener.remove();
    await act(async () => root.render(null));
    expect(document.activeElement).toBe(removeOpener ? fallback : opener);
    opener.remove();
    fallback.remove();
  });

  test.each(['connect', 'computers'])('keeps the guide out of the %s computer panel', async initialMode => {
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode={initialMode} onClose={vi.fn()} />));
    expect(container.textContent).not.toContain('新手指引');
  });

  test('keeps downloads in the connection flow and returns to the task selection', async () => {
    const devices = [{ deviceId: 'office', displayName: '办公电脑', runtimeRole: 'desktop', routable: true }];
    api.getDevices.mockResolvedValue({ devices });
    api.getConversationDevice.mockResolvedValue({ status: 'selected', selected_device_id: 'office', revision: 'r1', agent_name: '工作助手', devices });
    const onClose = vi.fn();
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode="computers" topicId="p2p_38_42" activeAgent={{ uid: 42 }} onClose={onClose} />));
    expect(container.querySelectorAll('[role=dialog]')).toHaveLength(1);
    expect(container.querySelectorAll('h3')).toHaveLength(1);
    expect(container.querySelector('h3').textContent).toBe('任务设备');
    expect(container.querySelectorAll('.cc-device-option')).toHaveLength(2);
    expect(container.querySelector('.catsco-device-card')).toBeNull();
    expect(container.querySelector('.catsco-onboarding-replay')).toBeNull();
    expect(container.textContent).not.toContain('云端运行环境');
    expect(container.textContent).not.toContain('最近活动');
    expect(container.textContent).toContain('连接本机');
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
    const clickText = text => act(async () => [...container.querySelectorAll('button')].find(button => button.textContent === text).click());
    expect(container.textContent).not.toContain('下载桌面端');
    await clickText('连接本机');
    await clickText('下载桌面端');
    expect(container.querySelector('.catsco-download-card-primary')).not.toBeNull();
    expect(container.querySelectorAll('[role=dialog]')).toHaveLength(1);
    await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  test('only shows group authorization controls for the group entry', async () => {
    api.getGroupDeviceShares.mockResolvedValue({ actor_uid: 38, shares: [], devices: [], members: [] });
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode="group-devices" topicId="grp_38" onClose={vi.fn()} />));
    expect(container.querySelector('h3').textContent).toBe('设备与授权');
    expect(container.querySelector('.cc-group-devices')).not.toBeNull();
    expect(container.textContent).not.toContain('连接本机');
    expect(container.textContent).not.toContain('下载桌面端');
  });

  test('retains the correct owner for same-ID computers in a group-backed solo task', async () => {
    const snapshot = { status: 'selected', selected_owner_uid: 39, selected_device_id: 'pc', revision: 'r1', devices: [
      { ownerUserId: 'usr39', deviceId: 'pc', displayName: '他人电脑', routable: true },
      { ownerUserId: 'usr38', deviceId: 'pc', displayName: '办公电脑', routable: true },
    ] };
    api.getConversationDevice.mockResolvedValue(snapshot);
    api.confirmConversationDevice.mockResolvedValue({ ...snapshot, selected_owner_uid: 38, revision: 'r2' });
    const onClose = vi.fn();
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode="computers" topicId="grp_38" activeAgent={{ uid: 42 }} onClose={onClose} />));
    expect(container.querySelector('.cc-group-devices')).toBeNull();
    expect(container.querySelector('input:checked').closest('label').textContent).toContain('他人电脑');
    await act(async () => Simulate.change(container.querySelector('input[value="pc"]')));
    await act(async () => [...container.querySelectorAll('button')].find(button => button.textContent === '确认').click());
    expect(api.confirmConversationDevice).toHaveBeenCalledExactlyOnceWith('grp_38', 42, 'pc', 'r1');
    expect(onClose).toHaveBeenCalledOnce();
  });

  test('selects only an owned desktop for a draft after confirmation and does not infer it from browser preference', async () => {
    window.localStorage.setItem('catsco_preferred_desktop_device:v1:38', 'office');
    api.getDevices.mockResolvedValue({ devices: [
      { deviceId: 'office', displayName: '办公电脑', runtimeRole: 'desktop', routable: true, ownerUserId: 'usr38' },
      { deviceId: 'off', displayName: '离线电脑', runtimeRole: 'desktop', routable: false },
      { deviceId: 'server', runtimeRole: 'server', routable: true },
      { deviceId: 'other', runtimeRole: 'desktop', routable: true, ownerUserId: 'usr99' },
    ] });
    const changed = vi.fn().mockReturnValueOnce(false).mockReturnValue(true);
    const onClose = vi.fn();
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode="computers" activeAgent={{ uid: 42 }} onDraftDeviceChange={changed} onClose={onClose} />));
    expect(container.querySelectorAll('[type=radio]')).toHaveLength(3);
    expect(container.querySelector('input:checked').value).toBe('');
    expect(container.querySelector('input[value=off]').disabled).toBe(true);
    await act(async () => Simulate.change(container.querySelector('input[value=office]')));
    expect(changed).not.toHaveBeenCalled();
    await act(async () => [...container.querySelectorAll('button')].find(button => button.textContent === '确认').click());
    expect(onClose).not.toHaveBeenCalled();
    await act(async () => [...container.querySelectorAll('button')].find(button => button.textContent === '确认').click());
    expect(changed.mock.calls).toEqual([['office'], ['office']]);
    expect(onClose).toHaveBeenCalledOnce();
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
  });

  test('returns to cloud selection after connecting a computer without redirecting the new task', async () => {
    vi.useFakeTimers();
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    const devices = [{ deviceId: 'pc', displayName: '办公电脑', runtimeRole: 'desktop', routable: true }];
    api.getDevices.mockResolvedValue({ devices });
    api.createDesktopConnectSession.mockResolvedValue({ code: 'connect' });
    api.getDesktopConnectStatus.mockResolvedValue({ state: 'claimed', device_id: 'pc' });
    const changed = vi.fn(), connected = vi.fn();
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode="computers"
      onDraftDeviceChange={changed} onConnected={connected} onClose={vi.fn()} />));
    await act(async () => [...container.querySelectorAll('button')].find(button => button.textContent === '连接本机').click());
    await act(async () => container.querySelector('.catsco-connect-actions button').click());
    await act(async () => vi.advanceTimersByTimeAsync(2700));
    expect(connected).toHaveBeenCalledOnce();
    expect(container.querySelector('h3').textContent).toBe('任务设备');
    expect(container.querySelector('input:checked').value).toBe('');
    expect(changed).not.toHaveBeenCalled();
    expect(api.confirmConversationDevice).not.toHaveBeenCalled();
  });

  test('loads computers before showing a list or empty state and preserves it during refresh', async () => {
    vi.useFakeTimers();
    let finish;
    api.getDevices.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode="computers" onClose={vi.fn()} />));
    expect(container.querySelector('.cc-computer-list-region [data-loading-kind="connect"]')).not.toBeNull();
    expect(container.textContent).not.toContain('暂无已连接的电脑');
    expect(container.querySelector('.catsco-device-card')).toBeNull();
    const snapshot = { devices: [{ deviceId: 'office', displayName: 'Office PC', runtimeRole: 'desktop', routable: true }] };
    await act(async () => finish(snapshot));
    expect(container.querySelector('.cc-computer-list-region [data-loading-kind]')).toBeNull();
    expect(container.querySelector('.cc-device-list').textContent).toContain('Office PC');
    let refresh;
    api.getDevices.mockImplementationOnce(() => new Promise(resolve => { refresh = resolve; }));
    await act(async () => container.querySelector('[aria-label="刷新设备列表"]').click());
    expect(container.querySelector('.cc-device-list').textContent).toContain('Office PC');
    expect(container.querySelector('.cc-computer-list-region [data-loading-kind]')).toBeNull();
    await act(async () => refresh(snapshot));
  });

  test('offers retry after a failed initial read and shows empty only after a successful response', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    api.getDevices.mockRejectedValueOnce(new Error('offline'));
    await act(async () => root.render(<DesktopConnectModal userId="38" initialMode="computers" onClose={vi.fn()} />));
    expect(container.querySelector('[role="alert"]').textContent).toContain('暂时无法读取设备');
    expect(container.textContent).not.toContain('暂无已连接的电脑');
    let finish;
    api.getDevices.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    await act(async () => container.querySelector('[aria-label="刷新设备列表"]').click());
    expect(container.querySelector('.cc-computer-list-region [data-loading-kind="connect"]')).not.toBeNull();
    await act(async () => finish({ devices: [] }));
    expect(container.querySelector('[role="alert"]')).toBeNull();
    expect(container.textContent).toContain('暂无已连接的电脑');
    expect(container.querySelector('.cc-computer-list-region [data-loading-kind]')).toBeNull();
  });

  test('keeps connected device management in the unified modal', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'office-pc',
        displayName: 'Office PC',
        runtimeRole: 'desktop',
        routable: true,
        capabilities: ['read_file'],
      }],
    });

    await act(async () => {
      root.render(<DesktopConnectModal userId="38" onClose={vi.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('本地桌面设备');
    expect(container.textContent).toContain('本机：Office PC');
    expect(container.textContent).toContain('已连接');
    expect(container.textContent).toContain('Office PC');
    expect(container.textContent).toContain('可用');

    const unlink = container.querySelector('button[aria-label="解绑设备 Office PC"]');
    expect(unlink).not.toBeNull();
    api.unlinkDevice.mockResolvedValue({ ok: true });
    await act(async () => {
      unlink.click();
      await Promise.resolve();
    });
    expect(api.unlinkDevice).toHaveBeenCalledWith('office-pc');
  });

  test('separates local desktops from cloud runtimes', async () => {
    api.getDevices.mockResolvedValue({
      devices: [
        {
          deviceId: 'ck123',
          displayName: 'CK123',
          runtimeRole: 'desktop',
          routable: true,
          capabilities: ['read_file', 'skillhub.localBot.switch'],
        },
        {
          deviceId: 'cloud-worker',
          displayName: 'bot-bot-bot-9308',
          runtimeRole: 'server',
          routable: true,
          capabilities: ['read_file'],
        },
      ],
    });

    await act(async () => {
      root.render(<DesktopConnectModal userId="38" onClose={vi.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('本地桌面设备');
    expect(container.textContent).toContain('云端运行环境');
    expect(container.textContent).toContain('本机：CK123');
    expect(container.textContent).toContain('bot-bot-bot-9308');
  });

  test('requires an explicit local-machine choice when multiple desktops are online and remembers it', async () => {
    const onStatusChange = vi.fn();
    api.getDevices.mockResolvedValue({
      devices: [
        { deviceId: 'ck123', displayName: 'CK123', runtimeRole: 'desktop', routable: true },
        { deviceId: 'office-pc', displayName: 'Office PC', runtimeRole: 'desktop', routable: true },
      ],
    });

    await act(async () => {
      root.render(<DesktopConnectModal userId="38" onClose={vi.fn()} onStatusChange={onStatusChange} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('选择本机桌面设备');
    expect(onStatusChange).toHaveBeenLastCalledWith('connected');
    const selectCK123 = container.querySelector('button[aria-label="设为本机 CK123"]');
    expect(selectCK123).not.toBeNull();

    await act(async () => {
      selectCK123.click();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('本机：CK123');
    expect(container.textContent).toContain('重新选择本机');
    expect(window.localStorage.getItem('catsco_preferred_desktop_device:v1:38')).toBe('ck123');
  });

  test('restores the preferred local machine when the modal is reopened', async () => {
    window.localStorage.setItem('catsco_preferred_desktop_device:v1:38', 'ck123');
    api.getDevices.mockResolvedValue({
      devices: [
        { deviceId: 'ck123', displayName: 'CK123', runtimeRole: 'desktop', routable: true },
        { deviceId: 'office-pc', displayName: 'Office PC', runtimeRole: 'desktop', routable: true },
      ],
    });

    await act(async () => {
      root.render(<DesktopConnectModal userId="38" onClose={vi.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('本机：CK123');
    expect(container.textContent).toContain('已连接');
  });

  test('uses the claimed desktop identity instead of another online preferred machine', () => {
    const devices = [
      { deviceId: 'ck123', displayName: 'CK123', runtimeRole: 'desktop', routable: true },
      { deviceId: 'office-pc', displayName: 'Office PC', runtimeRole: 'desktop', routable: true },
      { deviceId: 'cloud-worker', runtimeRole: 'server', routable: true },
    ];

    expect(resolveConnectedDesktopDevice(devices, 'office-pc', 'ck123')?.deviceId).toBe('office-pc');
    expect(resolveConnectedDesktopDevice(devices, 'missing-device', 'ck123')).toBeNull();
  });

  test('does not silently replace a remembered machine when it is temporarily missing', async () => {
    window.localStorage.setItem('catsco_preferred_desktop_device:v1:38', 'ck123');
    api.getDevices.mockResolvedValue({
      devices: [
        { deviceId: 'office-pc', displayName: 'Office PC', runtimeRole: 'desktop', routable: true },
      ],
    });

    await act(async () => {
      root.render(<DesktopConnectModal userId="38" onClose={vi.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('默认本机暂不可用');
    expect(container.textContent).toContain('系统不会自动改选其他电脑');
    expect(window.localStorage.getItem('catsco_preferred_desktop_device:v1:38')).toBe('ck123');
  });

  test('shows connection motion only while waiting for the desktop confirmation', async () => {
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    let fail;
    api.createDesktopConnectSession.mockImplementationOnce(() => new Promise((_, reject) => { fail = reject; }));
    await act(async () => { root.render(<DesktopConnectModal userId="38" onClose={vi.fn()} />); });
    expect(container.querySelector('[data-loading-kind="connect"]')).toBeNull();
    await act(async () => { container.querySelector('.catsco-connect-actions .oc-btn-primary').click(); });
    expect(container.querySelector('[data-loading-kind="connect"]')).not.toBeNull();
    expect(container.querySelector('.catsco-connect-actions .oc-btn-primary').disabled).toBe(true);
    await act(async () => { fail(new Error('offline')); });
    expect(container.querySelector('[data-loading-kind="connect"]')).toBeNull();
    expect(container.querySelector('.catsco-connect-actions .oc-btn-primary').disabled).toBe(false);
  });

  test('completes a legacy claimed connection after the user chooses among multiple desktops', async () => {
    vi.useFakeTimers();
    const onConnected = vi.fn();
    const deepLinkClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
    api.getDevices.mockResolvedValue({
      devices: [
        { deviceId: 'ck123', displayName: 'CK123', runtimeRole: 'desktop', routable: true },
        { deviceId: 'office-pc', displayName: 'Office PC', runtimeRole: 'desktop', routable: true },
      ],
    });
    api.createDesktopConnectSession.mockResolvedValue({ code: 'legacy-code', deeplink_url: 'catsco://connect?code=legacy-code' });
    api.getDesktopConnectStatus.mockResolvedValue({ state: 'claimed' });

    await act(async () => {
      root.render(<DesktopConnectModal userId="38" onClose={vi.fn()} onConnected={onConnected} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => {
      container.querySelector('.catsco-connect-actions .oc-btn-primary').click();
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(2000);
    });

    expect(container.textContent).toContain('选择本机桌面设备');
    await act(async () => {
      container.querySelector('button[aria-label="设为本机 Office PC"]').click();
      await vi.advanceTimersByTimeAsync(600);
    });

    expect(onConnected).toHaveBeenCalledWith(expect.objectContaining({ deviceId: 'office-pc' }));
    expect(window.localStorage.getItem('catsco_preferred_desktop_device:v1:38')).toBe('office-pc');
    expect(deepLinkClick).toHaveBeenCalledTimes(1);
    deepLinkClick.mockRestore();
    vi.useRealTimers();
  });
});
