import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import SkillHubView, {
  assertSkillHubDeviceResult,
  isRetryableSkillHubDeviceListError,
  isRetryableSkillHubSwitchError,
  normalizeOwnedBots,
  normalizeSkillHubDevices,
  normalizeLocalSkills,
  normalizeSkillHubSkills,
  isLocalSkillShared,
  isPrivateSkillHubReference,
  readRememberedSkillHubBotUID,
  rememberSkillHubBotUID,
  resolvePreferredSkillHubBotUID,
  resolveSkillHubEntry,
  resolveSharedSkillHubMetadata,
  upsertSkillRef,
  waitForSkillHubWorkspaceAfterSwitch,
  waitForPublishedSkillHubEntry,
} from './skillhub-view';
import { api, requestSkillHubDeviceTool } from '../api';
import { FeedbackProvider } from '../components/feedback-system';

vi.mock('../api', () => ({
  api: {
    getMyBots: vi.fn(),
    getBotDefinitionSkills: vi.fn(),
    updateBotDefinitionSkills: vi.fn(),
    searchSkillHubSkills: vi.fn(),
    getSkillHubSkill: vi.fn(),
    getSkillHubVersion: vi.fn(),
    getDevices: vi.fn(),
    switchLocalBot: vi.fn(),
    getLocalCatsStatus: vi.fn(),
    getLocalSkills: vi.fn(),
    getLocalStatusDetails: vi.fn(),
    shareLocalSkill: vi.fn(),
  },
  requestSkillHubDeviceTool: vi.fn(),
}));

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((next, fail) => {
    resolve = next;
    reject = fail;
  });
  return { promise, reject, resolve };
}

function addButton(container) {
  return [...container.querySelectorAll('.cc-skillhub-card button')]
    .find((button) => button.textContent.includes('添加'));
}

describe('SkillHubView', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.clearAllMocks();
    globalThis.localStorage?.clear();
    api.getMyBots.mockResolvedValue({
      bots: [
        { uid: 42, display_name: 'Owner Bot', relation: 'owner', is_owner: true },
        { uid: 43, display_name: 'Friend Bot', relation: 'friend', is_owner: false },
      ],
    });
    api.getBotDefinitionSkills.mockResolvedValue({
      botId: '42',
      revision: 3,
      skills: [{ source: 'skillhub', skillId: 'tools/review', version: '1.0.0', contentHash: 'a'.repeat(64) }],
    });
    api.searchSkillHubSkills.mockResolvedValue({
      skills: [{
        id: 'tools/summarize',
        name: 'Summarize',
        description: 'Summarize text',
        latestVersion: '2.0.0',
        contentHash: 'b'.repeat(64),
      }],
    });
    api.updateBotDefinitionSkills.mockResolvedValue({
      botId: '42',
      revision: 4,
      skills: [
        { source: 'skillhub', skillId: 'tools/review', version: '1.0.0', contentHash: 'a'.repeat(64) },
        { source: 'skillhub', skillId: 'tools/summarize', version: '2.0.0', contentHash: 'b'.repeat(64) },
      ],
    });
    api.getSkillHubVersion.mockResolvedValue({
      version: {
        id: 'alice/local-demo',
        version: '1.0.0',
        contentHash: 'd'.repeat(64),
      },
    });
    api.getDevices.mockResolvedValue({ devices: [] });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    vi.useRealTimers();
    container.remove();
    vi.unstubAllGlobals();
  });

  async function openCatalogue() {
    await act(async () => {
      Simulate.click(container.querySelector('#skillhub-catalogue-tab'));
      await Promise.resolve();
    });
  }

  async function openAdded() {
    await act(async () => {
      Simulate.click(container.querySelector('#skillhub-added-tab'));
      await Promise.resolve();
    });
  }

  async function openCustomSkills() {
    await act(async () => {
      Simulate.click([...container.querySelectorAll('button')]
        .find((button) => button.textContent.includes('管理自定义能力')));
      await Promise.resolve();
    });
  }

  it('normalizes owner bots and SkillHub entries', () => {
    expect(normalizeOwnedBots({ bots: [
      { uid: 1, relation: 'owner' },
      { uid: 2, relation: 'friend' },
    ] }, 10).map((bot) => bot.uid)).toEqual([1]);
    expect(normalizeSkillHubSkills({ items: [{ id: 'a', name: 'A', latest_version: '1.2.0' }] })[0]).toMatchObject({
      skillId: 'a',
      displayName: 'A',
      latestVersion: '1.2.0',
    });
    expect(normalizeLocalSkills({ skills: [{
      name: 'local-demo',
      relative_path: 'local-demo',
      skill_hub: { version: '1.0.0' },
    }] })[0]).toMatchObject({
      name: 'local-demo',
      relativePath: 'local-demo',
      skillHub: { version: '1.0.0' },
    });
    expect(isLocalSkillShared({
      canShare: true,
      skillHub: { author: 'alice', version: '1.0.0' },
    })).toBe(false);
    expect(isLocalSkillShared({
      canShare: true,
      skillHub: {
        author: 'legacy-author',
        version: '1.0.0',
        reference: {
          skillId: 'priv_local1',
          version: 'sha256-private',
          contentHash: 'c'.repeat(64),
        },
      },
    })).toBe(false);
    expect(isLocalSkillShared({
      canShare: false,
      skillHub: {
        author: 'alice',
        version: '1.0.0',
        reference: {
          skillId: 'alice/local-demo',
          version: '1.0.0',
          contentHash: 'a'.repeat(64),
        },
      },
    })).toBe(true);
    expect(upsertSkillRef([{ skillId: 'a', version: '1' }], { skillId: 'b', version: '2' }))
      .toEqual([{ skillId: 'a', version: '1' }, { skillId: 'b', version: '2' }]);
    expect(resolveSkillHubEntry(
      { skillId: 'a', latestVersion: '2.0.0', contentHash: '' },
      { skill: { id: 'a', latestVersion: '2.0.0' }, versions: [{ id: 'a', version: '2.0.0', contentHash: 'c'.repeat(64) }] },
    )).toMatchObject({ latestVersion: '2.0.0', contentHash: 'c'.repeat(64) });
    expect(normalizeSkillHubDevices({ devices: [
      {
        deviceId: 'ready',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localBot.switch',
        ],
      },
      {
        deviceId: 'partial',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: ['skillhub.localWorkspace.get'],
      },
      {
        deviceId: 'legacy',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: ['read_file'],
      },
    ] }).map((device) => device.deviceId)).toEqual(['ready']);
    expect(resolveSharedSkillHubMetadata({
      skill_hub: { author: 'alice', version: '1.0.0', uploaded_at: '2026-08-05T00:00:00.000Z' },
    }, {})).toEqual({
      author: 'alice',
      version: '1.0.0',
      uploadedAt: '2026-08-05T00:00:00.000Z',
    });
    expect(isPrivateSkillHubReference('priv_0123456789abcdef')).toBe(true);
    expect(isPrivateSkillHubReference('alice/local-demo')).toBe(false);
    expect(() => assertSkillHubDeviceResult({ schema: 'legacy', bot_uid: '42' }, {
      toolName: 'skillhub.localWorkspace.get',
      botUID: '42',
    })).toThrow(/不兼容/);
  });

  it('remembers the selected Bot per CatsCo user and ignores stale selections', () => {
    const values = new Map();
    const storage = {
      getItem: (key) => values.get(key) || null,
      setItem: (key, value) => values.set(key, value),
      removeItem: (key) => values.delete(key),
    };
    const bots = [
      { uid: 42, relation: 'owner' },
      { uid: 44, relation: 'owner' },
    ];

    rememberSkillHubBotUID(7, '44', storage);
    expect(readRememberedSkillHubBotUID(7, storage)).toBe('44');
    expect(resolvePreferredSkillHubBotUID(bots, 7, storage)).toBe('44');
    expect(resolvePreferredSkillHubBotUID([bots[0]], 7, storage)).toBe('42');
  });

  it('waits for the selected device route and retries transient switch errors', async () => {
    const readyDevice = {
      deviceId: 'alice-device',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    };
    const getDevices = vi.fn()
      .mockResolvedValueOnce({ devices: [{ ...readyDevice, routeConnected: false, routable: false }] })
      .mockResolvedValue({ devices: [readyDevice] });
    const readWorkspace = vi.fn()
      .mockRejectedValueOnce(Object.assign(new Error('no route'), { code: 'target_device_unavailable' }))
      .mockResolvedValue({ bot_uid: '44' });
    const waitFor = vi.fn().mockResolvedValue(undefined);

    await expect(waitForSkillHubWorkspaceAfterSwitch({
      deviceId: 'alice-device',
      getDevices,
      readWorkspace,
      waitFor,
      maxAttempts: 3,
    })).resolves.toEqual({ bot_uid: '44' });
    expect(getDevices).toHaveBeenCalledTimes(3);
    expect(readWorkspace).toHaveBeenCalledTimes(2);
    expect(waitFor).toHaveBeenCalledTimes(3);
    expect(isRetryableSkillHubSwitchError({ code: 'target_device_unavailable' })).toBe(true);
    expect(isRetryableSkillHubSwitchError({ code: 'OWNER_MISMATCH' })).toBe(false);

    await expect(waitForSkillHubWorkspaceAfterSwitch({
      deviceId: 'alice-device',
      getDevices: vi.fn().mockResolvedValue({ devices: [readyDevice] }),
      readWorkspace: vi.fn().mockRejectedValue(
        Object.assign(new Error('owner mismatch'), { code: 'OWNER_MISMATCH' }),
      ),
      waitFor: vi.fn().mockResolvedValue(undefined),
      maxAttempts: 3,
    })).rejects.toMatchObject({ code: 'OWNER_MISMATCH' });
  });

  it('classifies only transient device-list failures as retryable', () => {
    expect(isRetryableSkillHubDeviceListError({ code: 'NETWORK_ERROR' })).toBe(true);
    expect(isRetryableSkillHubDeviceListError({ code: 'REQUEST_TIMEOUT' })).toBe(true);
    expect(isRetryableSkillHubDeviceListError({ status: 500 })).toBe(true);
    expect(isRetryableSkillHubDeviceListError({ status: 502 })).toBe(true);
    expect(isRetryableSkillHubDeviceListError({ status: 503 })).toBe(true);
    expect(isRetryableSkillHubDeviceListError({ status: 504 })).toBe(true);
    expect(isRetryableSkillHubDeviceListError({ status: 401 })).toBe(false);
    expect(isRetryableSkillHubDeviceListError({ status: 403 })).toBe(false);
    expect(isRetryableSkillHubDeviceListError({ status: 404 })).toBe(false);
    expect(isRetryableSkillHubDeviceListError({ status: 501 })).toBe(false);
    expect(isRetryableSkillHubDeviceListError({ code: 'REQUEST_ABORTED' })).toBe(false);
    expect(isRetryableSkillHubDeviceListError({
      code: 'NETWORK_ERROR',
      status: 403,
    })).toBe(false);
  });

  it('retries transient device-list failures before reading the workspace', async () => {
    const readyDevice = {
      deviceId: 'alice-device',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    };
    const getDevices = vi.fn()
      .mockRejectedValueOnce(Object.assign(new Error('offline'), { code: 'NETWORK_ERROR' }))
      .mockRejectedValueOnce(Object.assign(new Error('unavailable'), { status: 503 }))
      .mockResolvedValue({ devices: [readyDevice] });
    const readWorkspace = vi.fn().mockResolvedValue({ bot_uid: '44' });
    const waitFor = vi.fn().mockResolvedValue(undefined);

    await expect(waitForSkillHubWorkspaceAfterSwitch({
      deviceId: 'alice-device',
      getDevices,
      readWorkspace,
      waitFor,
      maxAttempts: 3,
    })).resolves.toEqual({ bot_uid: '44' });
    expect(getDevices).toHaveBeenCalledTimes(3);
    expect(readWorkspace).toHaveBeenCalledTimes(1);
    expect(waitFor).toHaveBeenCalledTimes(3);
  });

  it.each([401, 403])('stops immediately when the device list returns HTTP %s', async (status) => {
    const permanentError = Object.assign(new Error(`HTTP ${status}`), { status });
    const getDevices = vi.fn().mockRejectedValue(permanentError);
    const readWorkspace = vi.fn();
    const waitFor = vi.fn().mockResolvedValue(undefined);

    await expect(waitForSkillHubWorkspaceAfterSwitch({
      deviceId: 'alice-device',
      getDevices,
      readWorkspace,
      waitFor,
      maxAttempts: 3,
    })).rejects.toBe(permanentError);
    expect(getDevices).toHaveBeenCalledTimes(1);
    expect(readWorkspace).not.toHaveBeenCalled();
    expect(waitFor).toHaveBeenCalledTimes(1);
  });

  it('stops at the absolute deadline when the device list never settles', async () => {
    vi.useFakeTimers();
    const getDevices = vi.fn(() => new Promise(() => {}));
    const readWorkspace = vi.fn();

    const result = waitForSkillHubWorkspaceAfterSwitch({
      deviceId: 'alice-device',
      getDevices,
      readWorkspace,
      timeoutMs: 250,
      initialDelayMs: 25,
      retryDelayMs: 25,
      deviceListTimeoutMs: 50,
    }).catch((error) => error);

    await vi.advanceTimersByTimeAsync(251);

    await expect(result).resolves.toMatchObject({
      code: 'skillhub_device_switch_timeout',
      cause: { code: 'REQUEST_TIMEOUT' },
    });
    expect(getDevices).toHaveBeenCalledTimes(3);
    expect(getDevices.mock.calls.map(([options]) => options.timeoutMs)).toEqual([50, 50, 50]);
    expect(readWorkspace).not.toHaveBeenCalled();
  });

  it('caps repeated workspace attempts to the remaining absolute deadline', async () => {
    const readyDevice = {
      deviceId: 'alice-device',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    };
    let clock = 0;
    const waitFor = vi.fn(async (delayMs) => { clock += delayMs; });
    const readWorkspace = vi.fn(async (requestTimeoutMs) => {
      clock += requestTimeoutMs;
      throw Object.assign(new Error('workspace timeout'), { code: 'skillhub_device_timeout' });
    });

    await expect(waitForSkillHubWorkspaceAfterSwitch({
      deviceId: 'alice-device',
      getDevices: vi.fn().mockResolvedValue({ devices: [readyDevice] }),
      readWorkspace,
      waitFor,
      timeoutMs: 103,
      initialDelayMs: 10,
      retryDelayMs: 10,
      deviceListTimeoutMs: 20,
      workspaceTimeoutMs: 20,
      now: () => clock,
    })).rejects.toMatchObject({
      code: 'skillhub_device_switch_timeout',
      cause: { code: 'skillhub_device_timeout' },
    });
    expect(readWorkspace.mock.calls.map(([requestTimeoutMs]) => requestTimeoutMs))
      .toEqual([20, 20, 20, 3]);
    expect(clock).toBe(103);
  });

  it('waits for an asynchronously published Skill when share initially returns only its ID', async () => {
    const getSkill = vi.fn()
      .mockRejectedValueOnce(Object.assign(new Error('not found'), { status: 404 }))
      .mockResolvedValue({
        skill: {
          id: 'alice/local-demo',
          latestVersion: '1.0.0',
          contentHash: 'd'.repeat(64),
        },
      });
    const getVersion = vi.fn()
      .mockRejectedValueOnce(Object.assign(new Error('not found'), { status: 404 }))
      .mockResolvedValue({
        version: {
          id: 'alice/local-demo',
          version: '1.0.0',
          contentHash: 'd'.repeat(64),
        },
      });
    const waitFor = vi.fn().mockResolvedValue(undefined);

    await expect(waitForPublishedSkillHubEntry({
      skillId: 'alice/local-demo',
      shared: { skill: { id: 'alice/local-demo' } },
      getSkill,
      getVersion,
      waitFor,
    })).resolves.toMatchObject({
      skillId: 'alice/local-demo',
      latestVersion: '1.0.0',
      contentHash: 'd'.repeat(64),
    });
    expect(getSkill).toHaveBeenCalledTimes(2);
    expect(getVersion).toHaveBeenCalledTimes(2);
    expect(waitFor).toHaveBeenCalledTimes(2);
  });

  it('opens with the simplified Agent capability workspace', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('h1')?.textContent).toBe('Agent 能力');
    expect(container.querySelector('#skillhub-added-tab')?.getAttribute('aria-selected')).toBe('true');
    expect(container.querySelectorAll('[role="tab"]')).toHaveLength(2);
    expect(container.querySelector('.cc-skillhub-installed')).toBeNull();
    expect(container.textContent).toContain('管理自定义能力');
    expect(container.textContent).not.toContain('已开启');
    expect(container.querySelector('button[aria-label="复制 tools/review"]')).toBeTruthy();
    expect(container.querySelector('button[aria-label="更多操作 tools/review"]')).toBeTruthy();
    expect(container.querySelector('button[aria-label="从当前 Agent 移除 tools/review"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(container.querySelector('.cc-skillhub-custom-entry'));
      await Promise.resolve();
    });
    expect(container.querySelector('#skillhub-custom-title')?.textContent).toBe('管理自定义能力');
    expect(container.textContent).toContain('本地 Skills 目录');
  });

  it('copies an added SkillHub ability without opening the platform share action', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    const share = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } });
    Object.defineProperty(navigator, 'share', { configurable: true, value: share });
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    vi.useFakeTimers();
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="复制 tools/review"]'));
      await Promise.resolve();
    });

    expect(writeText).toHaveBeenCalledWith('tools/review');
    expect(share).not.toHaveBeenCalled();
    expect(container.textContent).toContain('已复制 tools/review 的 SkillHub ID');
    await act(async () => {
      vi.advanceTimersByTime(3000);
    });
    expect(container.textContent).not.toContain('已复制 tools/review 的 SkillHub ID');
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined });
    Object.defineProperty(navigator, 'share', { configurable: true, value: undefined });
  });

  it('opens accessible details and removal actions from the more menu', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    const trigger = container.querySelector('button[aria-label="更多操作 tools/review"]');
    await act(async () => {
      Simulate.click(trigger);
      await new Promise((resolve) => requestAnimationFrame(resolve));
    });

    const menu = document.body.querySelector('[role="menu"][aria-label="tools/review 操作"]');
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    expect(menu).toBeTruthy();
    expect(menu.textContent).toContain('查看详情');
    expect(menu.textContent).toContain('从 Agent 移除');

    await act(async () => {
      Simulate.click([...menu.querySelectorAll('[role="menuitem"]')].find((button) => button.textContent.includes('查看详情')));
      await Promise.resolve();
    });

    const dialog = document.body.querySelector('[role="dialog"]');
    expect(dialog).toBeTruthy();
    expect(dialog.textContent).toContain('tools/review');
    expect(dialog.textContent).toContain('v1.0.0');
    await act(async () => {
      Simulate.click(dialog.querySelector('button[aria-label="关闭能力详情"]'));
      await Promise.resolve();
    });
    expect(document.body.querySelector('[role="dialog"]')).toBeFalsy();
  });

  it('confirms before removing an ability from the current Agent', async () => {
    api.updateBotDefinitionSkills.mockResolvedValueOnce({ botId: '42', revision: 4, skills: [] });
    await act(async () => {
      root.render(<FeedbackProvider><SkillHubView user={{ uid: 7 }} /></FeedbackProvider>);
      await Promise.resolve();
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="更多操作 tools/review"]'));
      await new Promise((resolve) => requestAnimationFrame(resolve));
    });
    const menu = document.body.querySelector('[role="menu"][aria-label="tools/review 操作"]');
    await act(async () => {
      Simulate.click([...menu.querySelectorAll('[role="menuitem"]')].find((button) => button.textContent.includes('从 Agent 移除')));
      await Promise.resolve();
    });

    const confirmation = document.body.querySelector('[role="alertdialog"]');
    expect(confirmation).toBeTruthy();
    expect(confirmation.textContent).toContain('从“Owner Bot”移除“tools/review”');
    expect(confirmation.textContent).toContain('技能本身不会从 SkillHub 删除');
    expect(api.updateBotDefinitionSkills).not.toHaveBeenCalled();

    await act(async () => {
      Simulate.click([...confirmation.querySelectorAll('button')].find((button) => button.textContent === '从 Agent 移除'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.updateBotDefinitionSkills).toHaveBeenCalledWith('42', 3, []);
    expect(container.textContent).toContain('已从 Agent“Owner Bot”移除 tools/review');
  });

  it('uses an accessible themed Agent listbox', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    const trigger = container.querySelector('.cc-skillhub-agent-select-trigger');
    trigger.getBoundingClientRect = () => ({
      bottom: 104,
      height: 44,
      left: 100,
      right: 276,
      top: 60,
      width: 176,
      x: 100,
      y: 60,
      toJSON: () => ({}),
    });
    expect(trigger?.getAttribute('aria-expanded')).toBe('false');
    await act(async () => {
      Simulate.click(trigger);
    });
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    const listbox = document.body.querySelector('[role="listbox"][aria-label="Agent 列表"]');
    expect(listbox).toBeTruthy();
    expect(listbox.style.left).toBe('100px');
    expect(listbox.style.width).toBe('176px');
    expect(document.body.querySelector('[role="option"][aria-selected="true"]')?.textContent).toContain('Owner Bot');

    await act(async () => {
      Simulate.keyDown(trigger, { key: 'Escape' });
    });
    expect(trigger.getAttribute('aria-expanded')).toBe('false');
    expect(document.body.querySelector('[role="listbox"][aria-label="Agent 列表"]')).toBeFalsy();
  });

  it('loads only owner bots and binds a precise SkillHub reference', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelectorAll('.cc-skillhub-bot-picker:first-child option')).toHaveLength(1);
    expect(container.textContent).toContain('tools/review');
    expect(container.textContent).not.toContain('Friend Bot');

    await openCatalogue();
    const installButton = addButton(container);
    await act(async () => {
      Simulate.click(installButton);
      await Promise.resolve();
    });

    expect(api.updateBotDefinitionSkills).toHaveBeenCalledWith('42', 3, expect.arrayContaining([
      expect.objectContaining({
        source: 'skillhub',
        skillId: 'tools/summarize',
        version: '2.0.0',
        contentHash: 'b'.repeat(64),
      }),
    ]));
  });

  it('loads a production local workspace and shares through the selected XiaoBa device', async () => {
    api.getBotDefinitionSkills.mockResolvedValue({
      botId: '42',
      revision: 3,
      skills: [{
        source: 'skillhub',
        skillId: 'priv_local1',
        version: 'sha256-private',
        contentHash: 'c'.repeat(64),
      }],
    });
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        displayName: 'Alice Laptop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localBot.switch',
        ],
      }],
    });
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName }) => {
      if (toolName === 'skillhub.localWorkspace.get') {
        return {
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: '42',
          active_bot_uid: '42',
          skills_path: 'C:\\xiaoba\\skills',
          skills: [{
            local_skill_id: 'local-1',
            name: 'local-demo',
            description: 'Local demo',
            relative_path: 'local-demo',
            source: 'user',
            can_share: true,
            skill_hub: {
              author: 'legacy-author',
              version: '1.0.0',
              reference: {
                source: 'skillhub',
                skillId: 'priv_local1',
                version: 'sha256-private',
                contentHash: 'c'.repeat(64),
              },
            },
          }],
        };
      }
      if (toolName === 'skillhub.localSkill.share') {
        return {
          schema: 'xiaoba.skillhub.local_share.v1',
          bot_uid: '42',
          skill: { id: 'alice/local-demo', name: 'local-demo' },
          latest_version: '1.0.0',
          content_hash: 'd'.repeat(64),
          skill_hub: {
            author: 'alice',
            version: '1.0.0',
            uploaded_at: '2026-08-05T00:00:00.000Z',
          },
        };
      }
      if (toolName === 'skillhub.localSkill.finalize') {
        return {
          schema: 'xiaoba.skillhub.local_finalize.v1',
          bot_uid: '42',
          skill_id: 'alice/local-demo',
          version: '1.0.0',
          content_hash: 'd'.repeat(64),
          direction: 'local_to_cloud',
        };
      }
      throw new Error(`unexpected tool ${toolName}`);
    });
    api.updateBotDefinitionSkills.mockResolvedValue({
      botId: '42',
      revision: 4,
      skills: [{
        source: 'skillhub',
        skillId: 'alice/local-demo',
        version: '1.0.0',
        contentHash: 'd'.repeat(64),
      }],
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.querySelector('.cc-skillhub-added-title h3')?.textContent).toBe('local-demo');
    expect(container.querySelector('.cc-skillhub-version-note')?.textContent).toContain('仅当前 Agent 可用');
    expect(container.textContent).not.toContain('priv_local1');
    await openCustomSkills();
    expect(container.textContent).toContain('Alice Laptop');
    expect(container.textContent).toContain('local-demo');
    expect(container.textContent).toContain('C:\\xiaoba\\skills');

    const shareButton = container.querySelector('.cc-skillhub-local-card button');
    expect(container.querySelector('.cc-skillhub-local-card')?.textContent).toContain('未发布');
    expect(shareButton.textContent).toContain('发布并添加');
    expect(shareButton.textContent).not.toContain('已发布到团队');
    await act(async () => {
      Simulate.click(shareButton);
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      deviceId: 'alice-device',
      toolName: 'skillhub.localSkill.share',
      payload: expect.objectContaining({
        bot_uid: '42',
        local_skill_id: 'local-1',
        skill_name: 'local-demo',
      }),
    }));
    expect(api.updateBotDefinitionSkills).toHaveBeenCalledWith('42', 3, [expect.objectContaining({
      skillId: 'alice/local-demo',
      version: '1.0.0',
      contentHash: 'd'.repeat(64),
    })]);
    expect(api.getSkillHubVersion).toHaveBeenCalledWith(
      'alice/local-demo',
      '1.0.0',
      expect.objectContaining({ timeoutMs: expect.any(Number) }),
    );
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localSkill.finalize',
      payload: expect.objectContaining({
        skill_id: 'alice/local-demo',
        author: 'alice',
        uploaded_at: '2026-08-05T00:00:00.000Z',
      }),
    }));
  });

  it('retries a version share only after confirmation and sends confirm_publish', async () => {
    api.getBotDefinitionSkills.mockResolvedValue({
      botId: '42',
      revision: 3,
      skills: [{
        source: 'skillhub',
        skillId: 'priv_local1',
        version: 'sha256-private',
        contentHash: 'c'.repeat(64),
      }],
    });
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localBot.switch',
        ],
      }],
    });
    let shareAttempts = 0;
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName }) => {
      if (toolName === 'skillhub.localWorkspace.get') {
        return {
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: '42',
          active_bot_uid: '42',
          skills_path: 'C:\\xiaoba\\skills',
          skills: [{
            local_skill_id: 'local-1',
            name: 'local-demo',
            relative_path: 'local-demo',
            source: 'user',
            can_share: true,
          }],
        };
      }
      if (toolName === 'skillhub.localSkill.share') {
        shareAttempts += 1;
        if (shareAttempts === 1) {
          return {
            schema: 'xiaoba.skillhub.local_share.v1',
            bot_uid: '42',
            requires_confirmation: true,
          };
        }
        return {
          schema: 'xiaoba.skillhub.local_share.v1',
          bot_uid: '42',
          skill: { id: 'alice/local-demo', name: 'local-demo' },
          latest_version: '2.0.0',
          content_hash: 'd'.repeat(64),
        };
      }
      if (toolName === 'skillhub.localSkill.finalize') {
        return {
          schema: 'xiaoba.skillhub.local_finalize.v1',
          bot_uid: '42',
          skill_id: 'alice/local-demo',
          version: '2.0.0',
          content_hash: 'd'.repeat(64),
        };
      }
      throw new Error(`unexpected tool ${toolName}`);
    });
    api.getSkillHubVersion.mockResolvedValue({
      version: {
        id: 'alice/local-demo',
        version: '2.0.0',
        contentHash: 'd'.repeat(64),
      },
    });
    api.updateBotDefinitionSkills.mockResolvedValue({
      botId: '42',
      revision: 4,
      skills: [{
        source: 'skillhub',
        skillId: 'alice/local-demo',
        version: '2.0.0',
        contentHash: 'd'.repeat(64),
      }],
    });
    const confirm = vi.fn(() => true);
    vi.stubGlobal('confirm', confirm);

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCustomSkills();
    await act(async () => {
      Simulate.click(container.querySelector('.cc-skillhub-local-card button'));
      await new Promise((resolve) => setTimeout(resolve, 0));
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const shareCalls = requestSkillHubDeviceTool.mock.calls
      .map(([request]) => request)
      .filter((request) => request.toolName === 'skillhub.localSkill.share');
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(shareCalls).toHaveLength(2);
    expect(shareCalls[0].payload.confirm_publish).toBeUndefined();
    expect(shareCalls[1].payload.confirm_publish).toBe(true);
  });

  it('refreshes after a revision conflict instead of overwriting remote changes', async () => {
    api.updateBotDefinitionSkills.mockRejectedValueOnce(Object.assign(new Error('conflict'), { status: 409 }));
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();
    const installButton = addButton(container);
    await act(async () => {
      Simulate.click(installButton);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(api.getBotDefinitionSkills).toHaveBeenCalledTimes(2);
    expect(container.textContent).toContain('已刷新，请再试一次');
  });

  it('ignores a late definition response after switching bots', async () => {
    const botA = deferred();
    const botB = deferred();
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => (
      String(uid) === '42' ? botA.promise : botB.promise
    ));

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
    });
    await act(async () => {
      botA.resolve({ revision: 1, skills: [{ source: 'skillhub', skillId: 'bot-a/skill', version: '1', contentHash: 'a'.repeat(64) }] });
      await Promise.resolve();
    });
    expect(container.textContent).toContain('正在读取 Agent 能力');
    expect(container.textContent).not.toContain('bot-a/skill');
    await act(async () => {
      botB.resolve({ revision: 2, skills: [{ source: 'skillhub', skillId: 'bot-b/skill', version: '1', contentHash: 'b'.repeat(64) }] });
      await Promise.resolve();
    });

    expect(container.textContent).toContain('bot-b/skill');
    expect(container.textContent).not.toContain('bot-a/skill');
  });

  it('restores the remembered Bot without treating page load as a switch request', async () => {
    globalThis.localStorage.setItem('catsco.skillhub.selectedBot.7', '44');
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => Promise.resolve({
      botId: String(uid),
      revision: 1,
      skills: [],
    }));

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-skillhub-bot-picker select').value).toBe('44');
    expect(requestSkillHubDeviceTool).not.toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localBot.switch',
    }));
  });

  it('does not switch the fallback Bot until the user explicitly requests it', async () => {
    api.getDevices.mockResolvedValueOnce({
      devices: [{
        deviceId: 'alice-device',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localBot.switch',
        ],
      }],
    });
    requestSkillHubDeviceTool.mockRejectedValue(
      Object.assign(new Error('Bot is not active'), { code: 'BOT_NOT_ACTIVE' }),
    );

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCustomSkills();

    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localWorkspace.get',
    }));
    expect(requestSkillHubDeviceTool).not.toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localBot.switch',
    }));
    expect(container.textContent).toContain('当前 Bot 尚未在本地 XiaoBa 激活');
  });

  it('recovers a transient device route loss after an explicit Bot switch', async () => {
    vi.useFakeTimers();
    const readyDevice = {
      deviceId: 'alice-device',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    };
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => Promise.resolve({
      botId: String(uid),
      revision: 1,
      skills: [],
    }));
    api.getDevices.mockResolvedValue({ devices: [readyDevice] });
    let botBWorkspaceAttempts = 0;
    requestSkillHubDeviceTool.mockImplementation(({ toolName, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get' && payload.bot_uid === '42') {
        return Promise.resolve({
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: '42',
          active_bot_uid: '42',
          skills_path: 'C:\\xiaoba\\bot-a\\skills',
          skills: [],
        });
      }
      if (toolName === 'skillhub.localWorkspace.get' && payload.bot_uid === '44') {
        botBWorkspaceAttempts += 1;
        if (botBWorkspaceAttempts === 1) {
          return Promise.reject(Object.assign(new Error('Bot is not active'), { code: 'BOT_NOT_ACTIVE' }));
        }
        if (botBWorkspaceAttempts === 2) {
          return Promise.reject(Object.assign(new Error('no route'), { code: 'target_device_unavailable' }));
        }
        return Promise.resolve({
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: '44',
          active_bot_uid: '44',
          skills_path: 'C:\\xiaoba\\bot-b\\skills',
          skills: [],
        });
      }
      if (toolName === 'skillhub.localBot.switch') {
        return Promise.resolve({
          schema: 'xiaoba.skillhub.bot_switch.v1',
          bot_uid: payload.bot_uid,
          switching: true,
        });
      }
      throw new Error(`unexpected tool ${toolName}`);
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCustomSkills();
    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localBot.switch',
      payload: expect.objectContaining({ bot_uid: '44' }),
    }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
      await Promise.resolve();
      await vi.advanceTimersByTimeAsync(1_500);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('C:\\xiaoba\\bot-b\\skills');
    expect(container.textContent).not.toContain('no route');
    expect(globalThis.localStorage.getItem('catsco.skillhub.selectedBot.7')).toBe('44');
  });

  it('does not switch back to a stale Bot after a late BOT_NOT_ACTIVE response', async () => {
    const botAWorkspace = deferred();
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => Promise.resolve({
      botId: String(uid),
      revision: 1,
      skills: [],
    }));
    api.getDevices.mockResolvedValueOnce({
      devices: [{
        deviceId: 'alice-device',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localBot.switch',
        ],
      }],
    });
    requestSkillHubDeviceTool.mockImplementation(({ toolName, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get' && payload.bot_uid === '42') {
        return botAWorkspace.promise;
      }
      if (toolName === 'skillhub.localWorkspace.get' && payload.bot_uid === '44') {
        return Promise.resolve({
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: '44',
          active_bot_uid: '44',
          skills_path: 'C:\\xiaoba\\bot-b\\skills',
          skills: [],
        });
      }
      if (toolName === 'skillhub.localBot.switch') {
        return Promise.resolve({
          schema: 'xiaoba.skillhub.bot_switch.v1',
          bot_uid: payload.bot_uid,
        });
      }
      throw new Error(`unexpected tool ${toolName}`);
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCustomSkills();
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localWorkspace.get',
      payload: expect.objectContaining({ bot_uid: '42' }),
    }));

    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      botAWorkspace.reject(Object.assign(new Error('Bot is not active'), { code: 'BOT_NOT_ACTIVE' }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const staleSwitches = requestSkillHubDeviceTool.mock.calls
      .map(([request]) => request)
      .filter((request) => (
        request.toolName === 'skillhub.localBot.switch'
        && request.payload.bot_uid === '42'
      ));
    expect(staleSwitches).toHaveLength(0);
    expect(container.textContent).toContain('C:\\xiaoba\\bot-b\\skills');
  });

  it('clears loading and does not switch a stale device after its selection is cleared', async () => {
    const deviceAWorkspace = deferred();
    const capabilities = [
      'skillhub.localWorkspace.get',
      'skillhub.localSkill.share',
      'skillhub.localSkill.finalize',
      'skillhub.localBot.switch',
    ];
    api.getDevices.mockResolvedValueOnce({
      devices: [
        {
          deviceId: 'device-a',
          displayName: 'Device A',
          active: true,
          routeConnected: true,
          routable: true,
          capabilities,
        },
        {
          deviceId: 'device-b',
          displayName: 'Device B',
          active: true,
          routeConnected: true,
          routable: true,
          capabilities,
        },
      ],
    });
    requestSkillHubDeviceTool.mockImplementation(({ toolName, deviceId, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get' && deviceId === 'device-a') {
        return deviceAWorkspace.promise;
      }
      if (toolName === 'skillhub.localBot.switch') {
        return Promise.resolve({
          schema: 'xiaoba.skillhub.bot_switch.v1',
          bot_uid: payload.bot_uid,
        });
      }
      throw new Error(`unexpected tool ${toolName}`);
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCustomSkills();
    const devicePicker = container.querySelector('.cc-skillhub-device-picker select');
    await act(async () => {
      devicePicker.value = 'device-a';
      Simulate.change(devicePicker);
      await Promise.resolve();
    });
    expect(container.textContent).toContain('正在读取本地能力');

    await act(async () => {
      devicePicker.value = '';
      Simulate.change(devicePicker);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      deviceAWorkspace.reject(Object.assign(new Error('Bot is not active'), { code: 'BOT_NOT_ACTIVE' }));
      await Promise.resolve();
      await Promise.resolve();
    });

    const staleSwitches = requestSkillHubDeviceTool.mock.calls
      .map(([request]) => request)
      .filter((request) => (
        request.toolName === 'skillhub.localBot.switch'
        && request.deviceId === 'device-a'
      ));
    expect(staleSwitches).toHaveLength(0);
    expect(container.textContent).toContain('请选择要操作的本地 XiaoBa');
    expect(container.textContent).not.toContain('正在读取本地能力');
  });

  it('ignores a late save response after switching bots', async () => {
    const saveBotA = deferred();
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => Promise.resolve(
      String(uid) === '42'
        ? { revision: 3, skills: [{ source: 'skillhub', skillId: 'bot-a/current', version: '1', contentHash: 'a'.repeat(64) }] }
        : { revision: 8, skills: [{ source: 'skillhub', skillId: 'bot-b/current', version: '1', contentHash: 'c'.repeat(64) }] },
    ));
    api.updateBotDefinitionSkills.mockReturnValueOnce(saveBotA.promise);

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();
    const installButton = addButton(container);
    await act(async () => {
      Simulate.click(installButton);
      await Promise.resolve();
    });

    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
    });
    await openAdded();
    expect(container.textContent).toContain('bot-b/current');

    await act(async () => {
      saveBotA.resolve({
        revision: 4,
        skills: [{ source: 'skillhub', skillId: 'bot-a/saved', version: '1', contentHash: 'd'.repeat(64) }],
      });
      await Promise.resolve();
    });

    expect(container.textContent).toContain('bot-b/current');
    expect(container.textContent).not.toContain('bot-a/saved');
  });

  it('does not bind while the selected Bot definition is still loading', async () => {
    const botB = deferred();
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => (
      String(uid) === '42'
        ? Promise.resolve({ revision: 3, skills: [{ source: 'skillhub', skillId: 'bot-a/current', version: '1', contentHash: 'a'.repeat(64) }] })
        : botB.promise
    ));

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
    });

    await openCatalogue();
    const installButton = addButton(container);
    expect(installButton.disabled).toBe(true);
    await act(async () => {
      Simulate.click(installButton);
      await Promise.resolve();
    });
    expect(api.updateBotDefinitionSkills).not.toHaveBeenCalled();

    await act(async () => {
      botB.resolve({ revision: 8, skills: [] });
      await Promise.resolve();
    });
  });

  it('ignores late Skill details after switching bots', async () => {
    const detail = deferred();
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => Promise.resolve(
      String(uid) === '42'
        ? { revision: 3, skills: [{ source: 'skillhub', skillId: 'bot-a/current', version: '1', contentHash: 'a'.repeat(64) }] }
        : { revision: 8, skills: [{ source: 'skillhub', skillId: 'bot-b/current', version: '1', contentHash: 'c'.repeat(64) }] },
    ));
    api.searchSkillHubSkills.mockResolvedValueOnce({
      skills: [{ id: 'tools/detail-required', name: 'Detail required' }],
    });
    api.getSkillHubSkill.mockReturnValueOnce(detail.promise);

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();
    const installButton = addButton(container);
    await act(async () => {
      Simulate.click(installButton);
      await Promise.resolve();
    });

    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      detail.resolve({
        skill: { id: 'tools/detail-required', latestVersion: '1.0.0' },
        versions: [{ id: 'tools/detail-required', version: '1.0.0', contentHash: 'e'.repeat(64) }],
      });
      await Promise.resolve();
    });

    expect(api.updateBotDefinitionSkills).not.toHaveBeenCalled();
    await openAdded();
    expect(container.textContent).toContain('bot-b/current');
  });

  it('does not show the previous Bot definition when the new Bot fails to load', async () => {
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Bot A', relation: 'owner' },
        { id: 44, display_name: 'Bot B', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => (
      String(uid) === '42'
        ? Promise.resolve({ revision: 3, skills: [{ source: 'skillhub', skillId: 'bot-a/current', version: '1', contentHash: 'a'.repeat(64) }] })
        : Promise.reject(new Error('Bot B unavailable'))
    ));

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.textContent).toContain('bot-a/current');

    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('Bot B unavailable');
    expect(container.textContent).not.toContain('bot-a/current');
  });

  it('prevents a refresh from racing with an in-flight save', async () => {
    const save = deferred();
    api.updateBotDefinitionSkills.mockReturnValueOnce(save.promise);

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();
    const installButton = addButton(container);
    await act(async () => {
      Simulate.click(installButton);
      await Promise.resolve();
    });

    await openAdded();
    const refreshButton = container.querySelector('button[aria-label="刷新当前 Agent 的能力"]');
    expect(refreshButton.disabled).toBe(true);
    await act(async () => {
      Simulate.click(refreshButton);
      await Promise.resolve();
    });
    expect(api.getBotDefinitionSkills).toHaveBeenCalledTimes(1);

    await act(async () => {
      save.resolve({
        revision: 4,
        skills: [{ source: 'skillhub', skillId: 'tools/summarize', version: '2.0.0', contentHash: 'b'.repeat(64) }],
      });
      await Promise.resolve();
    });
    expect(container.textContent).toContain('Summarize');
  });

  it('keeps the latest catalogue search result', async () => {
    const firstSearch = deferred();
    const secondSearch = deferred();
    api.searchSkillHubSkills.mockImplementation((searchQuery) => {
      if (searchQuery === 'first') return firstSearch.promise;
      if (searchQuery === 'second') return secondSearch.promise;
      return Promise.resolve({ skills: [] });
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();
    const form = container.querySelector('.cc-skillhub-search');
    const input = form.querySelector('input');
    await act(async () => {
      input.value = 'first';
      Simulate.change(input);
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.submit(form);
      await Promise.resolve();
    });
    await act(async () => {
      input.value = 'second';
      Simulate.change(input);
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.submit(form);
      await Promise.resolve();
    });
    await act(async () => {
      secondSearch.resolve({ skills: [{ id: 'latest/result', name: 'Latest result' }] });
      await Promise.resolve();
    });
    await act(async () => {
      firstSearch.resolve({ skills: [{ id: 'stale/result', name: 'Stale result' }] });
      await Promise.resolve();
    });

    expect(container.textContent).toContain('Latest result');
    expect(container.textContent).not.toContain('Stale result');
  });
});
