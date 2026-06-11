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
const { buildDeviceConnectorDeepLink } = require('./catsco-download-modal');
const { api, getApiBaseURL, getWebSocketURL } = require('../api');

describe('CatsCoDownloadModal', () => {
  let container;
  let root;
  let originalOpen;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    jest.useFakeTimers();
    api.createDeviceConnectorPairing.mockReset();
    api.getDeviceConnectorPairing.mockReset();
    api.getDevices.mockResolvedValue({ devices: [] });
    api.getDeviceAudit.mockResolvedValue({ events: [] });
    getApiBaseURL.mockReturnValue('https://app.catsco.cc');
    getWebSocketURL.mockReturnValue('wss://app.catsco.cc/v0/channels');
    originalOpen = window.open;
    window.open = jest.fn();
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    window.open = originalOpen;
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
    expect(window.open).toHaveBeenCalledWith(
      expect.stringContaining('catsco://device-connector/pair?code=PAIRCODE123'),
      '_self',
    );

    await act(async () => {
      jest.runOnlyPendingTimers();
    });
    expect(container.textContent).toContain('如果没有响应');
  });
});
