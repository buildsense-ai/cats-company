import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    createDeviceConnectorPairing: vi.fn(),
    getDeviceConnectorPairing: vi.fn(),
    getCatsCoDesktopReleases: vi.fn(),
    getDevices: vi.fn(),
    getDeviceAudit: vi.fn(),
    unlinkDevice: vi.fn(),
  },
  getApiBaseURL: vi.fn(() => 'https://app.catsco.cc'),
  getWebSocketURL: vi.fn(() => 'wss://app.catsco.cc/v0/channels'),
  requestExternalHistory: vi.fn(),
}));

import CatsCoDownloadModal, {
  DOWNLOAD_OPTIONS,
  buildDeviceConnectorDeepLink,
  externalHistoryDuration,
  summarizeExternalHistoryProgress,
} from './catsco-download-modal';
import { api, getApiBaseURL, getWebSocketURL, requestExternalHistory } from '../api';

describe('CatsCoDownloadModal', () => {
  let container;
  let root;
  let clickSpy;
  let clickedHref;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.useFakeTimers();
    api.createDeviceConnectorPairing.mockReset();
    api.getDeviceConnectorPairing.mockReset();
    api.getCatsCoDesktopReleases.mockResolvedValue({ version: '1.4.1' });
    api.getDevices.mockResolvedValue({ devices: [] });
    api.getDeviceAudit.mockReset();
    requestExternalHistory.mockReset();
    getApiBaseURL.mockReturnValue('https://app.catsco.cc');
    getWebSocketURL.mockReturnValue('wss://app.catsco.cc/v0/channels');
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
    clickedHref = '';
    clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function click() {
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
    vi.useRealTimers();
  });

  test('builds a CatsCo desktop pairing deep link without shell/write capabilities', () => {
    const link = buildDeviceConnectorDeepLink({ pairing_code: 'BC0450AC9FE18B8D' });

    expect(link).toBe('catsco://device-connector/pair?code=BC0450AC9FE18B8D&http_base_url=https%3A%2F%2Fapp.catsco.cc&server_url=wss%3A%2F%2Fapp.catsco.cc%2Fv0%2Fchannels');
    expect(link).not.toContain('allowShell');
    expect(link).not.toContain('execute_shell');
    expect(link).not.toContain('write_file');
  });

  test('normalizes external history windows without exposing operation details', () => {
    expect(externalHistoryDuration('none', 7)).toBe('');
    expect(externalHistoryDuration('7d', 30)).toBe('7d');
    expect(externalHistoryDuration('custom', 30)).toBe('30d');
    expect(externalHistoryDuration('custom', 0)).toBe('7d');
    expect(externalHistoryDuration('custom', 900)).toBe('365d');
  });

  test('weights progress by the exact processed and total counts', () => {
    expect(summarizeExternalHistoryProgress({
      codex: { processed: 37, total: 100 },
      pi: { processed: 20, total: 50 },
    })).toEqual({ processed: 57, total: 150, indeterminate: false, percentage: 38 });
  });

  test('aggregate stays indeterminate when any running provider reports total=null', () => {
    expect(summarizeExternalHistoryProgress({
      codex: { processed: 0, total: null },
      pi: { processed: 20, total: 50 },
    })).toEqual({ processed: 20, total: 50, indeterminate: true, percentage: 0 });
  });

  test('aggregate keeps stable empty catalog total=0 determinate', () => {
    expect(summarizeExternalHistoryProgress({
      codex: { processed: 0, total: 0 },
    })).toEqual({ processed: 0, total: 0, indeterminate: false, percentage: 0 });
  });

  test('reports that continuous learning starts immediately after provider selection', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'local-device',
        displayName: 'This Mac',
        routable: true,
        capabilities: ['external_history'],
      }],
    });
    requestExternalHistory.mockImplementation(async (_deviceId, payload) => {
      if (payload.action === 'status') {
        return {
          providers: [
            { provider: 'codex', enabled: true },
            { provider: 'pi', enabled: true },
          ],
        };
      }
      return { appliedImmediately: true, wakeScheduled: true, restartRequired: false };
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const saveButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('保存选择'));
    expect(saveButton).toBeDefined();

    await act(async () => {
      Simulate.click(saveButton);
      await Promise.resolve();
    });

    expect(requestExternalHistory).toHaveBeenCalledWith('local-device', {
      action: 'configure',
      providers: ['codex', 'pi'],
    });
    expect(container.textContent).toContain('选择已保存，持续学习已启用。');
  });

  test('shows clear progress while importing selected history', async () => {
    let finishImport;
    let reportProgress;
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'local-device',
        displayName: 'This Mac',
        routable: true,
        capabilities: ['external_history'],
      }],
    });
    requestExternalHistory.mockImplementation(async (_deviceId, payload, options) => {
      if (payload.action === 'status') {
        return { providers: [{ provider: 'codex', enabled: true }] };
      }
      if (payload.action === 'preview') {
        return { selectedCount: 100, cutoff: 'cutoff-1', operationId: 'operation-1' };
      }
      reportProgress = options.onProgress;
      return new Promise(resolve => {
        finishImport = resolve;
      });
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });

    let progress = container.querySelector('[role="progressbar"]');
    expect(progress).not.toBeNull();
    expect(progress.getAttribute('aria-valuenow')).toBe('0');
    expect(container.textContent).toContain('检查范围后开始导入');

    const previewButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('检查范围'));
    await act(async () => {
      Simulate.click(previewButton);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.textContent).toContain('范围已确认，等待开始导入');

    const startButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('开始导入'));
    await act(async () => {
      Simulate.click(startButton);
    });
    const confirmButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('确认开始导入'));
    await act(async () => {
      Simulate.click(confirmButton);
      await Promise.resolve();
    });

    progress = container.querySelector('[role="progressbar"]');
    expect(container.textContent).toContain('正在等待 Codex 返回进度');
    expect(progress.hasAttribute('aria-valuenow')).toBe(false);
    expect(progress.getAttribute('aria-valuemax')).toBe('100');

    await act(async () => {
      reportProgress({ processed: 37, total: 100, provider: 'codex', phase: 'importing' });
    });
    progress = container.querySelector('[role="progressbar"]');
    expect(container.textContent).toContain('37%');
    expect(container.textContent).toContain('已处理 37 / 100');
    expect(progress.getAttribute('aria-valuenow')).toBe('37');
    expect(progress.getAttribute('aria-valuemax')).toBe('100');

    await act(async () => {
      finishImport({ processedResources: 100, status: 'completed' });
      await Promise.resolve();
      await Promise.resolve();
    });
    progress = container.querySelector('[role="progressbar"]');
    expect(container.textContent).toContain('所选历史已全部导入');
    expect(container.textContent).toContain('100%');
    expect(progress.getAttribute('aria-valuenow')).toBe('100');
  });

  test('shows discovering copy and indeterminate bar when a running provider reports total=null', async () => {
    let reportProgress;
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'local-device',
        displayName: 'This Mac',
        routable: true,
        capabilities: ['external_history'],
      }],
    });
    requestExternalHistory.mockImplementation(async (_deviceId, payload, options) => {
      if (payload.action === 'status') return { providers: [{ provider: 'codex', enabled: true }] };
      if (payload.action === 'preview') return { selectedCount: 100, cutoff: 'cutoff-1', operationId: 'operation-1' };
      reportProgress = options.onProgress;
      return new Promise(() => {});
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });
    const previewButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('检查范围'));
    await act(async () => { Simulate.click(previewButton); await Promise.resolve(); await Promise.resolve(); });
    const startButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('开始导入'));
    await act(async () => { Simulate.click(startButton); });
    const confirmButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('确认开始导入'));
    await act(async () => { Simulate.click(confirmButton); await Promise.resolve(); });

    // Running provider reports total=null (discovering) — modal must stay
    // indeterminate with discovering copy, not a fake 0%.
    await act(async () => {
      reportProgress({ processed: 0, total: null, provider: 'codex', phase: 'discovering' });
    });
    const progress = container.querySelector('[role="progressbar"]');
    expect(progress.hasAttribute('aria-valuenow')).toBe(false);
    expect(container.textContent).toContain('正在确认范围');
  });

  test('oversized record with resumable:false shows cannot-import copy, not continue-remaining', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'local-device',
        displayName: 'This Mac',
        routable: true,
        capabilities: ['external_history'],
      }],
    });
    requestExternalHistory.mockImplementation(async (_deviceId, payload) => {
      if (payload.action === 'status') return { providers: [{ provider: 'codex', enabled: true }] };
      if (payload.action === 'preview') return { selectedCount: 100, cutoff: 'cutoff-1', operationId: 'operation-1' };
      throw Object.assign(new Error('历史记录超过安全限制（4 MiB），该条记录目前无法导入；已完成的历史进度已保留。'), {
        code: 'external_history_record_too_large', oversized: true, resumable: false,
      });
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve(); await Promise.resolve();
    });
    const previewButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('检查范围'));
    await act(async () => { Simulate.click(previewButton); await Promise.resolve(); await Promise.resolve(); });
    const startButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('开始导入'));
    await act(async () => { Simulate.click(startButton); });
    const confirmButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('确认开始导入'));
    await act(async () => { Simulate.click(confirmButton); await Promise.resolve(); await Promise.resolve(); });

    expect(container.textContent).toContain('该条记录目前无法导入');
    expect(container.textContent).not.toContain('可以继续导入剩余历史');
  });

  test('generic source failure shows distinct source-failed copy', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'local-device',
        displayName: 'This Mac',
        routable: true,
        capabilities: ['external_history'],
      }],
    });
    requestExternalHistory.mockImplementation(async (_deviceId, payload) => {
      if (payload.action === 'status') return { providers: [{ provider: 'pi', enabled: true }] };
      if (payload.action === 'preview') return { selectedCount: 100, cutoff: 'cutoff-1', operationId: 'operation-1' };
      throw Object.assign(new Error('外部历史来源执行失败，请检查来源状态后重试。'), {
        code: 'external_history_source_failed', sourceFailed: true,
      });
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve(); await Promise.resolve();
    });
    const previewButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('检查范围'));
    await act(async () => { Simulate.click(previewButton); await Promise.resolve(); await Promise.resolve(); });
    const startButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('开始导入'));
    await act(async () => { Simulate.click(startButton); });
    const confirmButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('确认开始导入'));
    await act(async () => { Simulate.click(confirmButton); await Promise.resolve(); await Promise.resolve(); });

    expect(container.textContent).toContain('外部历史来源执行失败');
  });

  test('keeps the completed import marker after reopening the modal', async () => {
    let imported = false;
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'local-device',
        displayName: 'This Mac',
        routable: true,
        capabilities: ['external_history'],
      }],
    });
    requestExternalHistory.mockImplementation(async (_deviceId, payload) => {
      if (payload.action === 'status') {
        return {
          providers: [{ provider: 'codex', enabled: true }],
          imports: imported ? [{
            provider: 'codex',
            operationId: 'operation-1',
            status: 'completed',
            selectedCount: 12,
            processedResources: 12,
            pendingResources: 0,
            resumable: false,
            quotaReached: false,
          }] : [],
        };
      }
      if (payload.action === 'preview') {
        return { selectedCount: 12, cutoff: 'cutoff-1', operationId: 'operation-1' };
      }
      imported = true;
      return { processedResources: 12, status: 'completed' };
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });
    const previewButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('检查范围'));
    await act(async () => {
      Simulate.click(previewButton);
      await Promise.resolve();
      await Promise.resolve();
    });
    const startButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('开始导入'));
    await act(async () => {
      Simulate.click(startButton);
    });
    const confirmButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('确认开始导入'));
    await act(async () => {
      Simulate.click(confirmButton);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.textContent).toContain('已处理 12，已完成');

    await act(async () => {
      root.render(null);
    });
    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('已完成 12 / 12');
  });

  test('restores and resumes the durable operation instead of starting over', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'local-device',
        displayName: 'This Mac',
        routable: true,
        capabilities: ['external_history'],
      }],
    });
    requestExternalHistory.mockImplementation(async (_deviceId, payload) => {
      if (payload.action === 'status') {
        return {
          providers: [{ provider: 'pi', enabled: true }],
          imports: [{
            provider: 'pi',
            operationId: 'operation-existing',
            status: 'quota_reached',
            selectedCount: 99,
            processedResources: 22,
            pendingResources: 77,
            resumable: true,
            quotaReached: true,
          }],
        };
      }
      return {
        status: 'quota_reached',
        processedResources: 32,
        pendingResources: 67,
        resumable: true,
        quotaReached: true,
      };
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('已导入 22 / 99');
    const continueButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('继续导入'));
    expect(continueButton).toBeDefined();

    await act(async () => {
      Simulate.click(continueButton);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(requestExternalHistory).toHaveBeenCalledWith('local-device', {
      action: 'execute',
      provider: 'pi',
      updatedSince: '7d',
      operationId: 'operation-existing',
    }, expect.objectContaining({ onProgress: expect.any(Function) }));
  });

  test('uses the CatsCo 1.4.1 fallback release download links', async () => {
    expect(DOWNLOAD_OPTIONS).toHaveLength(5);
    expect(DOWNLOAD_OPTIONS.map((option) => option.href)).toEqual([
      'https://github-release.tos-cn-guangzhou.volces.com/update/CatsCo-1.4.1-win.exe',
      'https://github-release.tos-cn-guangzhou.volces.com/update/macos-arm64/CatsCo-1.4.1-mac-arm64.dmg',
      'https://github-release.tos-cn-guangzhou.volces.com/update/macos-x64/CatsCo-1.4.1-mac-x64.dmg',
      'https://github-release.tos-cn-guangzhou.volces.com/update/CatsCo-1.4.1-linux.AppImage',
      'https://github-release.tos-cn-guangzhou.volces.com/update/CatsCo-1.4.1-linux.deb',
    ]);

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
    });

    expect(container.textContent).toContain('当前版本 v1.4.1');
    expect(container.querySelector('.catsco-download-section-title')?.textContent).toBe('可下载版本');
    const scrollRegion = container.querySelector('.catsco-download-body');
    expect(scrollRegion).not.toBeNull();
    expect(Array.from(scrollRegion.children).filter((element) => (
      element.classList.contains('catsco-download-list')
    ))).toHaveLength(2);
    expect(Array.from(container.querySelectorAll('.catsco-download-release-list a')).every(
      (link) => link.getAttribute('target') === '_blank',
    )).toBe(true);
  });

  test('uses native new-tab downloads for cross-origin desktop releases in an installed PWA', async () => {
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: true });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
    });

    expect(Array.from(container.querySelectorAll('.catsco-download-release-list a')).every(
      (link) => link.getAttribute('target') === '_blank',
    )).toBe(true);
  });

  test('updates download links from the desktop release API', async () => {
    api.getCatsCoDesktopReleases.mockResolvedValue({
      version: '1.5.0',
      downloads: {
        windows: 'https://download.example/CatsCo-1.5.0-win.exe',
        'mac-arm': 'https://download.example/macos-arm64/CatsCo-1.5.0-mac-arm64.dmg',
        'mac-intel': 'https://download.example/macos-x64/CatsCo-1.5.0-mac-x64.dmg',
        'linux-appimage': 'https://download.example/CatsCo-1.5.0-linux.AppImage',
        'linux-deb': 'https://download.example/CatsCo-1.5.0-linux.deb',
      },
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('当前版本 v1.5.0');
    const hrefs = Array.from(container.querySelectorAll('a.catsco-download-card')).map((link) => link.href);
    expect(hrefs).toEqual(expect.arrayContaining([
      'https://download.example/CatsCo-1.5.0-win.exe',
      'https://download.example/macos-arm64/CatsCo-1.5.0-mac-arm64.dmg',
      'https://download.example/macos-x64/CatsCo-1.5.0-mac-x64.dmg',
      'https://download.example/CatsCo-1.5.0-linux.AppImage',
      'https://download.example/CatsCo-1.5.0-linux.deb',
    ]));
  });

  test('keeps device status separate from wrapping capability labels', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'device-1',
        displayName: 'LAPTOP-GIHN1H8',
        routable: true,
        capabilities: ['read_file', 'resolve_common_directory', 'execute_shell'],
      }],
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const card = container.querySelector('.catsco-device-card');
    expect(card.querySelector('.catsco-download-desc').textContent).toBe('可用');
    expect(Array.from(card.querySelectorAll('.catsco-device-capabilities > span')).map((item) => item.textContent)).toEqual([
      'read_file',
      'resolve_common_directory',
      'execute_shell',
    ]);
    expect(card.querySelector('.catsco-download-meta')).toBeNull();
  });

  test('opens the desktop connector from the primary action', async () => {
    api.createDeviceConnectorPairing.mockResolvedValue({
      pairing_id: 'pair-1',
      pairing_code: 'PAIRCODE123',
      status: 'pending',
    });

    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
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
      vi.runOnlyPendingTimers();
    });
    expect(container.textContent).toContain('如果桌面端没有弹出');
  });

  test('keeps device RPC audit history out of the user-facing setup flow', async () => {
    await act(async () => {
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
    });

    expect(api.getDeviceAudit).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain('设备任务完成');
    expect(container.textContent).not.toContain('任务已发送到设备');
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
      root.render(React.createElement(CatsCoDownloadModal, { onClose: vi.fn() }));
      await Promise.resolve();
    });

    const button = container.querySelector('button[title="打开 CatsCo 桌面端连接"]');
    await act(async () => {
      Simulate.click(button);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('CONSUMED123');

    await act(async () => {
      vi.advanceTimersByTime(3000);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('这台电脑已连接');
    expect(container.textContent).not.toContain('CONSUMED123');
    expect(container.textContent).not.toContain('备用命令');
  });
});
