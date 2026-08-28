import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
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
  },
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
