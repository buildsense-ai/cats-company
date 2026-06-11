import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

jest.mock('../api', () => ({
  api: {
    createDeviceConnectorPairing: jest.fn(),
    getDeviceConnectorPairing: jest.fn(),
    getDevices: jest.fn(),
    getDeviceAudit: jest.fn(),
    unlinkDevice: jest.fn(),
  },
  getApiBaseURL: jest.fn(() => 'https://app.catsco.cc'),
  getWebSocketURL: jest.fn(() => 'wss://app.catsco.cc/v0/channels'),
}));

const CatsCoDownloadModal = require('./catsco-download-modal').default;
const { buildDeviceConnectorDeepLink, visibleDeviceAuditEvents } = require('./catsco-download-modal');
const { api, getApiBaseURL, getWebSocketURL } = require('../api');

describe('CatsCoDownloadModal', () => {
  let container;
  let root;
  let clickSpy;
  let clickedHref;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    jest.useFakeTimers();
    api.createDeviceConnectorPairing.mockReset();
    api.getDeviceConnectorPairing.mockReset();
    api.getDevices.mockResolvedValue({ devices: [] });
    api.getDeviceAudit.mockResolvedValue({ events: [] });
    getApiBaseURL.mockReturnValue('https://app.catsco.cc');
    getWebSocketURL.mockReturnValue('wss://app.catsco.cc/v0/channels');
    clickedHref = '';
    clickSpy = jest.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function click() {
      clickedHref = this.href;
    });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    clickSpy.mockRestore();
    jest.useRealTimers();
  });

  test('builds a CatsCo desktop pairing deep link without shell/write capabilities', () => {
    const link = buildDeviceConnectorDeepLink({ pairing_code: 'BC0450AC9FE18B8D' });

    expect(link).toBe('catsco://device-connector/pair?code=BC0450AC9FE18B8D&http_base_url=https%3A%2F%2Fapp.catsco.cc&server_url=wss%3A%2F%2Fapp.catsco.cc%2Fv0%2Fchannels');
    expect(link).not.toContain('allowShell');
    expect(link).not.toContain('execute_shell');
    expect(link).not.toContain('write_file');
  });

  test('opens the desktop connector from the primary action', async () => {
    api.createDeviceConnectorPairing.mockResolvedValue({
      pairing_id: 'pair-1',
      pairing_code: 'PAIRCODE123',
      status: 'pending',
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: jest.fn() }));
      await Promise.resolve();
    });

    const button = container.querySelector('button[title="打开 CatsCo 桌面端连接"]');
    expect(button).not.toBeNull();

    await act(async () => {
      Simulate.click(button);
      await Promise.resolve();
    });

    expect(api.createDeviceConnectorPairing).toHaveBeenCalledTimes(1);
    expect(clickedHref).toContain('catsco://device-connector/pair?code=PAIRCODE123');

    await act(async () => {
      jest.runOnlyPendingTimers();
    });
    expect(container.textContent).toContain('如果桌面端没有弹出');
  });

  test('hides routine pairing audit rows and keeps useful device activity', async () => {
    const events = [
      { id: 'audit-pair-1', phase: 'pairing_created', result: 'ok', reason: 'pair-1' },
      { id: 'audit-pair-2', phase: 'pairing_created', result: 'ok', reason: 'pair-2' },
      { id: 'audit-device-1', phase: 'device_enrolled', result: 'ok', device_id: 'office-pc' },
    ];
    expect(visibleDeviceAuditEvents(events).map((event) => event.id)).toEqual(['audit-device-1']);

    api.getDeviceAudit.mockResolvedValue({ events });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: jest.fn() }));
      await Promise.resolve();
    });

    expect(container.textContent).not.toContain('pairing_created');
    expect(container.textContent).not.toContain('audit-pair');
    expect(container.textContent).toContain('设备已连接');
    expect(container.textContent).toContain('office-pc');
  });

  test('hides the stale pairing code after the desktop connector consumes it', async () => {
    api.createDeviceConnectorPairing.mockResolvedValue({
      pairing_id: 'pair-consumed',
      pairing_code: 'CONSUMED123',
      status: 'pending',
    });
    api.getDeviceConnectorPairing.mockResolvedValue({
      pairing_id: 'pair-consumed',
      status: 'consumed',
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: jest.fn() }));
      await Promise.resolve();
    });

    const button = container.querySelector('button[title="打开 CatsCo 桌面端连接"]');
    await act(async () => {
      Simulate.click(button);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('CONSUMED123');

    await act(async () => {
      jest.advanceTimersByTime(3000);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('这台电脑已连接');
    expect(container.textContent).not.toContain('CONSUMED123');
    expect(container.textContent).not.toContain('备用命令');
  });
});
