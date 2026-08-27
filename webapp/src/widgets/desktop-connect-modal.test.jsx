import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import DesktopConnectModal from './desktop-connect-modal';

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
    api.getCatsCoDesktopReleases.mockResolvedValue({ version: '1.5.0' });
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
  });

  test('combines connection and downloads in one modal with downloads as the requested initial section', async () => {
    await act(async () => {
      root.render(<DesktopConnectModal onClose={vi.fn()} initialMode="download" />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('CatsCo 桌面端');
    expect(container.textContent).toContain('连接我的电脑助手');
    expect(container.textContent).toContain('当前版本');
    expect(container.textContent).toContain('Windows');
    expect(container.textContent).toContain('收起下载');
  });

  test('keeps connected device management in the unified modal', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'office-pc',
        displayName: 'Office PC',
        routable: true,
        capabilities: ['read_file'],
      }],
    });

    await act(async () => {
      root.render(<DesktopConnectModal onClose={vi.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('已连接设备');
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
});
