import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import SkillHubView, {
  assertSkillHubDeviceResult,
  isRetryableSkillHubDeviceListError,
  isRetryableSkillHubSwitchError,
  isSkillHubWorkspaceSwitchingError,
  buildSkillLibrary,
  normalizeOwnedBots,
  normalizeAccessibleBots,
  buildCurrentAgentSkills,
  resolveLocalSkillForAgentSkill,
  normalizeViewerSkills,
  normalizeSkillVersionHistory,
  normalizeSkillHubDevices,
  resolveSkillHubRuntimeRouteForBot,
  resolveAutomaticSkillHubDeviceID,
  resolveSkillHubDevicesForBot,
  normalizeLocalSkills,
  normalizeSkillHubSkills,
  isLocalSkillShared,
  isPrivateSkillHubReference,
  readRememberedSkillHubBotUID,
  rememberSkillHubBotUID,
  resolvePreferredSkillHubBotUID,
  resolveAddedSkillPresentation,
  resolveSkillHubEntry,
  resolveSharedSkillHubMetadata,
  upsertSkillRef,
  collectSkillHubWorkspacePages,
  waitForSkillHubWorkspaceAfterSwitch,
  waitForPublishedSkillHubEntry,
} from './skillhub-view';
import { api, requestSkillHubDeviceTool } from '../api';
import { FeedbackProvider } from '../components/feedback-system';

vi.mock('../api', () => ({
  api: {
    getAgentSkills: vi.fn(),
    getAgentSkillVersions: vi.fn(),
    getMyBots: vi.fn(),
    getBotDefinitionSkills: vi.fn(),
    updateBotDefinitionSkills: vi.fn(),
    searchSkillHubSkills: vi.fn(),
    getSkillHubSkill: vi.fn(),
    getSkillHubVersions: vi.fn(),
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
        { uid: 43, display_name: 'Friend Bot', relation: 'friend', is_owner: false, owner_id: 99 },
      ],
    });
    api.getAgentSkills.mockResolvedValue({
      botId: '43',
      skills_visibility: 'owner',
      skills: [{
        source: 'skillhub',
        skillId: 'private/review',
        version: 'v2',
        displayName: 'cloud-html-artifact',
        revisionNumber: 2,
        lastChangedBy: 'lin',
        lastChangedAt: '2026-08-22T02:03:04Z',
        changeSource: 'conversation_mutation',
      }],
    });
    api.getAgentSkillVersions.mockResolvedValue({
      botId: '43',
      skillId: 'private/review',
      currentVersion: 'v2',
      versions: [{
        source: 'skillhub',
        skillId: 'private/review',
        version: 'v2',
        displayName: 'cloud-html-artifact',
        revisionNumber: 2,
        lastChangedBy: '修改者未记录',
        lastChangedAt: '2026-08-22T02:03:04Z',
        changeSource: 'conversation_mutation',
        current: true,
      }, {
        source: 'skillhub',
        skillId: 'private/review',
        version: 'v1',
        displayName: 'cloud-html-artifact',
        revisionNumber: 1,
        lastChangedBy: 'Bot 自动同步',
        lastChangedAt: '2026-08-20T02:03:04Z',
        changeSource: 'runtime_backup',
      }],
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
        author: 'arrowhaken',
        latestVersion: '2.0.0',
        publishedAt: '2026-08-20T02:03:04Z',
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
    api.getSkillHubVersions.mockResolvedValue({
      versions: [{
        skillId: 'tools/review',
        latestVersion: '1.0.0',
        author: { name: 'arrowhaken' },
        publishedAt: '2026-08-20T02:03:04Z',
      }],
    });
    api.getDevices.mockResolvedValue({ devices: [] });
    api.getLocalSkills.mockResolvedValue({ skills: [] });
    api.shareLocalSkill.mockResolvedValue({});
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  it('normalizes accessible owner and friend bots and merges local-only skills', () => {
    expect(normalizeAccessibleBots({ agents: [
      { uid: 42, relation: 'owner' },
      { uid: 43, relation: 'friend', owner_id: 99 },
      { uid: 44, display_name: 'Human Friend', relation: 'friend', is_bot: true },
    ] }, 7).map((bot) => bot.relation)).toEqual(['owner', 'friend']);
    expect(buildCurrentAgentSkills([
      { skillId: 'tools/review', version: '1' },
    ], [{ name: 'draft', localSkillId: 'draft' }])).toEqual(expect.arrayContaining([
      expect.objectContaining({ skillId: 'tools/review', formal: true }),
      expect.objectContaining({ skillId: 'local:draft', localOnly: true }),
    ]));
    expect(normalizeViewerSkills({ skills: [{ skillId: 'private/review', version: 'v2' }] })[0]).toMatchObject({
      skillId: 'private/review', version: 'v2',
    });
    expect(normalizeSkillVersionHistory({
      currentVersion: 'v2',
      versions: [{
        skillId: 'private/review', version: 'v2', revisionNumber: 2,
        lastChangedBy: 'lin', lastChangedAt: '2026-08-22T02:03:04Z',
      }],
      nextBeforeRevisionNumber: 2,
    }, { privateReference: true })).toMatchObject({
      versions: [expect.objectContaining({
        version: 'v2', revisionNumber: 2, author: 'lin', current: true, privateReference: true,
      })],
      nextBeforeRevisionNumber: 2,
    });
    const merged = buildCurrentAgentSkills([], [{ name: 'draft', localSkillId: 'draft-id' }]);
    expect(resolveLocalSkillForAgentSkill(merged[0], [{ name: 'draft', localSkillId: 'draft-id' }]))
      .toMatchObject({ localSkillId: 'draft-id' });
  });

  it('collects every paginated Runtime workspace Skill beyond the old 200-item boundary', async () => {
    const revision = 'a'.repeat(64);
    const allSkills = Array.from({ length: 450 }, (_, index) => ({
      local_skill_id: `local-${String(index).padStart(3, '0')}`,
      name: `skill-${index}`,
    }));
    const page = (offset) => {
      const skills = allSkills.slice(offset, offset + 200);
      const nextOffset = offset + skills.length;
      return {
        schema: 'xiaoba.skillhub.local_workspace.v1',
        bot_uid: '42',
        active_bot_uid: '42',
        skills_path: 'C:\\xiaoba\\skills',
        workspace_revision: revision,
        total_skills: allSkills.length,
        page_offset: offset,
        page_limit: 200,
        next_offset: nextOffset < allSkills.length ? nextOffset : null,
        truncated: nextOffset < allSkills.length,
        skills,
      };
    };
    const readPage = vi.fn(async ({ offset = 0, workspace_revision: expectedRevision }) => {
      if (offset > 0) expect(expectedRevision).toBe(revision);
      return page(offset);
    });

    const result = await collectSkillHubWorkspacePages({
      initialWorkspace: page(0),
      readPage,
    });

    expect(result.skills).toHaveLength(450);
    expect(result.skills.at(-1).local_skill_id).toBe('local-449');
    expect(result.truncated).toBe(false);
    expect(result.legacyTruncated).toBe(false);
    expect(readPage).toHaveBeenCalledTimes(2);
    expect(readPage).toHaveBeenNthCalledWith(1, {
      offset: 200,
      limit: 200,
      workspace_revision: revision,
    });
  });

  it('marks an old Runtime 200-item response as potentially truncated', async () => {
    const result = await collectSkillHubWorkspacePages({
      initialWorkspace: {
        schema: 'xiaoba.skillhub.local_workspace.v1',
        skills: Array.from({ length: 200 }, (_, index) => ({
          local_skill_id: `legacy-${index}`,
          name: `legacy-${index}`,
        })),
      },
      readPage: vi.fn(),
    });
    expect(result.skills).toHaveLength(200);
    expect(result.legacyTruncated).toBe(true);
  });

  it('restarts workspace pagination once when the Runtime reports a concurrent change', async () => {
    const changed = new Error('workspace changed');
    changed.code = 'WORKSPACE_CHANGED';
    const readPage = vi.fn()
      .mockRejectedValueOnce(changed)
      .mockResolvedValueOnce({
        schema: 'xiaoba.skillhub.local_workspace.v1',
        bot_uid: '42',
        active_bot_uid: '42',
        workspace_revision: 'b'.repeat(64),
        total_skills: 1,
        page_offset: 0,
        page_limit: 200,
        next_offset: null,
        truncated: false,
        skills: [{ local_skill_id: 'fresh', name: 'fresh' }],
      });
    const result = await collectSkillHubWorkspacePages({
      initialWorkspace: {
        schema: 'xiaoba.skillhub.local_workspace.v1',
        bot_uid: '42',
        active_bot_uid: '42',
        workspace_revision: 'a'.repeat(64),
        total_skills: 201,
        page_offset: 0,
        page_limit: 200,
        next_offset: 200,
        truncated: true,
        skills: Array.from({ length: 200 }, (_, index) => ({
          local_skill_id: `stale-${index}`,
          name: `stale-${index}`,
        })),
      },
      readPage,
    });
    expect(result.skills).toEqual([{ local_skill_id: 'fresh', name: 'fresh' }]);
    expect(readPage).toHaveBeenCalledTimes(2);
    expect(readPage).toHaveBeenLastCalledWith({ limit: 200 });
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
        .find((button) => button.textContent.includes('运行工作区')));
      await Promise.resolve();
    });
  }

  it('normalizes owner bots and SkillHub entries', () => {
    expect(normalizeOwnedBots({ bots: [
      { uid: 1, relation: 'owner' },
      { uid: 2, relation: 'friend' },
    ] }, 10).map((bot) => bot.uid)).toEqual([1]);
    expect(normalizeSkillHubSkills({ items: [{
      id: 'a',
      name: 'A',
      latest_version: '1.2.0',
      published_at: '2026-08-20T02:03:04Z',
    }] })[0]).toMatchObject({
      skillId: 'a',
      displayName: 'A',
      latestVersion: '1.2.0',
      publishedAt: '2026-08-20T02:03:04Z',
    });
    expect(normalizeLocalSkills({ skills: [{
      name: 'local-demo',
      relative_path: 'local-demo',
      skill_hub: { version: '1.0.0' },
      share_error: 'Skill contains sensitive material.',
    }] })[0]).toMatchObject({
      name: 'local-demo',
      relativePath: 'local-demo',
      skillHub: { version: '1.0.0' },
      shareError: 'Skill contains sensitive material.',
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
    }, {
      skillId: 'alice/local-demo',
      version: '1.0.0',
      contentHash: 'a'.repeat(64),
    })).toBe(true);
    expect(isLocalSkillShared({
      canShare: false,
      shareError: 'Skill contains sensitive material.',
      skillHub: { author: 'alice', version: '1.0.0' },
    })).toBe(false);
    expect(upsertSkillRef([{ skillId: 'a', version: '1' }], { skillId: 'b', version: '2' }))
      .toEqual([{ skillId: 'a', version: '1' }, { skillId: 'b', version: '2' }]);
    expect(resolveSkillHubEntry(
      { skillId: 'a', latestVersion: '2.0.0', contentHash: '' },
      { skill: { id: 'a', latestVersion: '2.0.0' }, versions: [{ id: 'a', version: '2.0.0', contentHash: 'c'.repeat(64) }] },
    )).toMatchObject({ latestVersion: '2.0.0', contentHash: 'c'.repeat(64) });
    expect(normalizeSkillHubDevices({ devices: [
      {
        deviceId: 'ready',
        runtimeRole: 'desktop',
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
        runtimeRole: 'desktop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: ['skillhub.localWorkspace.get'],
      },
      {
        deviceId: 'legacy',
        runtimeRole: 'desktop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: ['read_file'],
      },
      {
        deviceId: 'server-runtime',
        runtimeRole: 'server',
        botUid: 42,
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
        ],
      },
      {
        deviceId: 'unknown-runtime',
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
    ] }).map((device) => device.deviceId)).toEqual(['ready', 'server-runtime']);
    expect(resolveSkillHubDevicesForBot([
      { deviceId: 'desktop', runtimeRole: 'desktop' },
      { deviceId: 'server-42', runtimeRole: 'server', botUid: 42 },
      { deviceId: 'server-44', runtimeRole: 'server', botUid: 44 },
    ], '42').map(device => device.deviceId)).toEqual(['server-42']);
    expect(resolveSkillHubDevicesForBot([
      { deviceId: 'desktop', runtimeRole: 'desktop' },
      { deviceId: 'server-44', runtimeRole: 'server', botUid: 44 },
    ], '42').map(device => device.deviceId)).toEqual(['desktop']);
    const oldServerRoute = resolveSkillHubRuntimeRouteForBot({ devices: [{
      deviceId: 'desktop',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    }, {
      deviceId: 'old-server-42',
      runtimeRole: 'server',
      botUid: 42,
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: ['read_file'],
    }] }, '42');
    expect(oldServerRoute.kind).toBe('server-upgrade-required');
    expect(oldServerRoute.devices).toEqual([]);
    expect(oldServerRoute.blockedServers.map(device => device.deviceId)).toEqual(['old-server-42']);
    expect(resolveSkillHubRuntimeRouteForBot({ devices: [{
      ...oldServerRoute.blockedServers[0], botUid: 44,
    }, {
      deviceId: 'desktop',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    }] }, '42')).toMatchObject({
      kind: 'desktop-fallback',
      devices: [expect.objectContaining({ deviceId: 'desktop' })],
    });
    expect(resolveSkillHubRuntimeRouteForBot({ devices: [{
      ...oldServerRoute.blockedServers[0], routable: false,
    }, {
      deviceId: 'desktop',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    }] }, '42')).toMatchObject({
      kind: 'desktop-fallback',
      devices: [expect.objectContaining({ deviceId: 'desktop' })],
    });
    expect(resolveAutomaticSkillHubDeviceID([{ deviceId: 'device-a' }])).toBe('device-a');
    expect(resolveAutomaticSkillHubDeviceID([
      { deviceId: 'device-a' },
      { deviceId: 'device-b' },
    ])).toBe('');
    expect(resolveSharedSkillHubMetadata({
      skill_hub: { author: 'alice', version: '1.0.0', uploaded_at: '2026-08-05T00:00:00.000Z' },
    }, {})).toEqual({
      author: 'alice',
      version: '1.0.0',
      uploadedAt: '2026-08-05T00:00:00.000Z',
    });
    expect(isPrivateSkillHubReference('priv_0123456789abcdef')).toBe(true);
    expect(isPrivateSkillHubReference('alice/local-demo')).toBe(false);
    expect(resolveAddedSkillPresentation({
      skillId: 'priv_local1',
      version: 'private-v1',
      contentHash: 'a'.repeat(64),
    }, new Map(), new Map([['priv_local1', {
      name: 'stale-local-name',
      skillHub: { reference: {
        skillId: 'priv_local1',
        version: 'private-v1',
        contentHash: 'b'.repeat(64),
      } },
    }]]))).toMatchObject({
      label: '私有能力',
      localDetails: null,
      privateReference: true,
    });
    expect(() => assertSkillHubDeviceResult({ schema: 'legacy', bot_uid: '42' }, {
      toolName: 'skillhub.localWorkspace.get',
      botUID: '42',
    })).toThrow(/不兼容/);
    expect(() => assertSkillHubDeviceResult({
      schema: 'xiaoba.skillhub.local_delete.v1',
      bot_uid: '42',
      local_skill_id: 'local-other',
      deleted: true,
    }, {
      toolName: 'skillhub.localSkill.delete',
      botUID: '42',
      localSkillID: 'local-selected',
    })).toThrow(/未确认删除当前选中的 Skill/);
    expect(() => assertSkillHubDeviceResult({
      schema: 'xiaoba.skillhub.local_delete.v1',
      bot_uid: '42',
      local_skill_id: 'local-selected',
      deleted: false,
    }, {
      toolName: 'skillhub.localSkill.delete',
      botUID: '42',
      localSkillID: 'local-selected',
    })).toThrow(/未确认删除当前选中的 Skill/);
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
      runtimeRole: 'desktop',
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
    expect(isRetryableSkillHubSwitchError({ code: 'WORKSPACE_SWITCHING' })).toBe(true);
    expect(isSkillHubWorkspaceSwitchingError({
      code: 'SKILLHUB_OPERATION_FAILED',
      message: 'Bot Skill workspace ownership is changing (575 -> 412); retry the write.',
    })).toBe(true);
    expect(isSkillHubWorkspaceSwitchingError({
      code: 'SKILLHUB_OPERATION_FAILED',
      message: 'unrelated failure',
    })).toBe(false);
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
      runtimeRole: 'desktop',
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
      runtimeRole: 'desktop',
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

  it('builds one library with local abilities first and preserves online metadata', () => {
    const library = buildSkillLibrary({
      catalogue: [{
        skillId: 'online/writer',
        displayName: 'Online Writer',
        description: 'Cloud ability',
        author: 'alice',
        latestVersion: '1.0.0',
        publishedAt: '2026-08-20T02:03:04Z',
        contentHash: 'b'.repeat(64),
      }],
      localSkills: [{
        localSkillId: 'local-writer',
        name: 'Local Writer',
        description: 'Local ability',
        source: 'user',
        canShare: true,
      }],
    });

    expect(library.map((skill) => skill.displayName)).toEqual(['Local Writer', 'Online Writer']);
    expect(library.map((skill) => skill.sourceLabel)).toEqual(['本机', undefined]);
    expect(library[0]).toMatchObject({ isLocalSkill: true, canBind: false });
    expect(library[1]).toMatchObject({ latestVersion: '1.0.0', author: 'alice' });
  });

  it('shows the stable version, CatsCo publisher, and publication time on catalogue cards', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();

    const card = [...container.querySelectorAll('.cc-skillhub-card')]
      .find(candidate => candidate.textContent.includes('Summarize'));
    const source = card?.querySelector('.cc-skillhub-card-source');
    const expectedTime = `发布于 ${new Intl.DateTimeFormat('zh-CN', {
      year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
    }).format(new Date('2026-08-20T02:03:04Z'))}`;
    expect(source?.textContent).toBe(`v2.0.0 · arrowhaken${expectedTime}`);
    expect(source?.getAttribute('title')).toBe(`v2.0.0 · arrowhaken · ${expectedTime}`);
    expect(source?.querySelector('time')?.getAttribute('datetime')).toBe('2026-08-20T02:03:04Z');
  });

  it('keeps catalogue metadata placeholders visible when an old response omits fields', async () => {
    api.searchSkillHubSkills.mockResolvedValueOnce({
      skills: [{ id: 'tools/legacy', name: 'Legacy Tool', description: 'Legacy response' }],
    });
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();

    const card = [...container.querySelectorAll('.cc-skillhub-card')]
      .find(candidate => candidate.textContent.includes('Legacy Tool'));
    expect(card?.querySelector('.cc-skillhub-card-source')?.textContent)
      .toBe('版本待确认 · 发布者待确认发布时间待确认');
  });

  it('explains account sync before adding a local-only ability from the library', async () => {
    api.getLocalSkills.mockResolvedValue({
      skills: [{
        local_skill_id: 'local-writer',
        name: 'Local Writer',
        description: 'Local ability',
        source: 'user',
      }],
    });
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        runtimeRole: 'desktop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localBot.switch',
        ],
      }, {
        deviceId: 'cloud-bot-runtime',
        displayName: 'XiaoBa Doubao Runtime',
        runtimeRole: 'server',
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
    requestSkillHubDeviceTool.mockResolvedValue({
      schema: 'xiaoba.skillhub.local_workspace.v1',
      bot_uid: '42',
      active_bot_uid: '42',
      skills_path: 'C:\\xiaoba\\skills',
      skills: [{
        local_skill_id: 'local-writer',
        name: 'Local Writer',
        description: 'Local ability',
        source: 'user',
        can_share: true,
      }],
    });

    await act(async () => {
      root.render(<FeedbackProvider><SkillHubView user={{ uid: 7 }} /></FeedbackProvider>);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();

    const cards = [...container.querySelectorAll('.cc-skillhub-card')];
    expect(cards[0].textContent).toContain('Local Writer');
    expect(cards[0].textContent).toContain('本机');
    expect(cards[1].textContent).not.toContain('在线');
    expect(cards[1].textContent).toContain('v2.0.0 · arrowhaken');
    expect(cards[1].textContent).toContain('发布于');

    await act(async () => {
      Simulate.click(cards[0].querySelector('button'));
      await Promise.resolve();
    });

    const confirmation = document.body.querySelector('[role="alertdialog"]');
    expect(confirmation?.textContent).toContain('此能力目前只在本机');
    expect(confirmation?.textContent).toContain('需要同步到你的账号');
    expect(confirmation?.textContent).toContain('继续添加');
    expect(requestSkillHubDeviceTool.mock.calls.filter(([request]) => request.toolName === 'skillhub.localSkill.share')).toHaveLength(0);

    await act(async () => {
      Simulate.click(confirmation.querySelector('.cc-confirm-cancel'));
      await Promise.resolve();
    });
  });

  it('keeps local abilities visible and explains a sync failure', async () => {
    api.getLocalSkills.mockResolvedValue({
      skills: [{
        local_skill_id: 'local-writer',
        name: 'Local Writer',
        description: 'Local ability',
        source: 'user',
        can_share: true,
      }],
    });
    api.shareLocalSkill.mockRejectedValue(new Error('无法连接本地 Skill 服务。'));

    await act(async () => {
      root.render(<FeedbackProvider><SkillHubView user={{ uid: 7 }} /></FeedbackProvider>);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCatalogue();

    const localCard = [...container.querySelectorAll('.cc-skillhub-card')]
      .find((card) => card.textContent.includes('Local Writer'));
    await act(async () => {
      Simulate.click(localCard.querySelector('button'));
      await Promise.resolve();
    });
    const confirmation = document.body.querySelector('[role="alertdialog"]');
    await act(async () => {
      Simulate.click([...confirmation.querySelectorAll('button')]
        .find((button) => button.textContent === '继续添加'));
      await Promise.resolve();
      await Promise.resolve();
    });

    const alert = container.querySelector('.cc-skillhub-library-alert');
    expect(alert?.textContent).toContain('“Local Writer”同步失败');
    expect(alert?.textContent).toContain('尚未添加到当前 Agent');
    expect(alert?.textContent).toContain('无法连接本地 Skill 服务');
    expect(container.textContent).toContain('Local Writer');
    expect(localCard.querySelector('button').disabled).toBe(false);
    expect(api.updateBotDefinitionSkills).not.toHaveBeenCalled();
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
    expect(container.textContent).toContain('运行工作区');
    expect(container.textContent).not.toContain('已开启');
    expect(container.querySelector('button[aria-label="复制 tools/review"]')).toBeNull();
    expect(container.querySelector('button[aria-label="更多操作 tools/review"]')).toBeTruthy();
    expect(container.querySelector('button[aria-label="从当前 Agent 移除 tools/review"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(container.querySelector('.cc-skillhub-custom-entry'));
      await Promise.resolve();
    });
    expect(container.querySelector('#skillhub-custom-title')?.textContent).toBe('管理自定义能力');
    expect(container.textContent).toContain('Skills 目录');
  });

  it('opens with the Agent requested by the management summary', async () => {
    api.getMyBots.mockResolvedValue({
      bots: [
        { uid: 42, display_name: 'Owner Bot', relation: 'owner', is_owner: true },
        { uid: 44, display_name: 'Design Bot', relation: 'owner', is_owner: true },
      ],
    });

    await act(async () => {
      root.render(<SkillHubView
        user={{ uid: 7 }}
        initialAgent={{ uid: 44, display_name: 'Design Bot' }}
      />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getBotDefinitionSkills).toHaveBeenCalledWith('44');
    expect(container.querySelector('.cc-skillhub-agent-select-trigger')?.textContent)
      .toContain('Design Bot');
  });

  it('presents local-only abilities as unpublished device-local content', async () => {
    api.getBotDefinitionSkills.mockResolvedValueOnce({ botId: '42', revision: 3, skills: [] });
    api.getDevices.mockResolvedValueOnce({
      devices: [{
        deviceId: 'alice-device',
        displayName: 'Alice Laptop',
        runtimeRole: 'desktop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localSkill.delete',
          'skillhub.localBot.switch',
        ],
      }],
    });
    requestSkillHubDeviceTool.mockResolvedValue({
      schema: 'xiaoba.skillhub.local_workspace.v1',
      bot_uid: '42',
      active_bot_uid: '42',
      skills_path: 'C:\\xiaoba\\skills',
      skills: [{
        local_skill_id: 'draft-1',
        name: 'web-search',
        description: 'Search the web',
        relative_path: 'web-search',
        source: 'user',
        can_share: true,
        skill_hub: {},
      }],
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const localItem = [...container.querySelectorAll('.cc-skillhub-added-item')]
      .find((item) => item.querySelector('h3')?.textContent === 'web-search');
    expect(localItem).toBeTruthy();
    expect(container.textContent).toContain('正式能力来自 BotDefinition');
    expect(container.textContent).toContain('当前运行工作区能力');
    expect(container.textContent).toContain('来自当前 Agent 正在运行的 XiaoBa');
    expect(localItem.textContent).toContain('仅本地');
    expect(localItem.textContent).toContain('尚未发布 · 当前运行工作区');
    expect(localItem.textContent).not.toContain('版本未确认');

    await act(async () => {
      Simulate.click(localItem.querySelector('button[aria-label="更多操作 web-search"]'));
      await new Promise((resolve) => requestAnimationFrame(resolve));
    });
    const menu = document.body.querySelector('[role="menu"][aria-label="web-search 操作"]');
    await act(async () => {
      Simulate.click([...menu.querySelectorAll('[role="menuitem"]')]
        .find((button) => button.textContent.includes('查看详情')));
      await Promise.resolve();
    });

    const dialog = document.body.querySelector('[role="dialog"]');
    expect(dialog).toBeTruthy();
    expect(dialog.textContent).toContain('本地能力');
    expect(dialog.textContent).toContain('本地能力名web-search');
    expect(dialog.textContent).toContain('发布状态尚未发布');
    expect(dialog.textContent).toContain('存放范围当前运行工作区');
    expect(dialog.textContent).toContain('尚未发布到 SkillHub');
    expect(dialog.textContent).not.toContain('local:draft-1');
    expect(dialog.textContent).not.toContain('版本待确认');
    expect(dialog.textContent).not.toContain('发布者SkillHub');
    expect(api.getAgentSkillVersions).not.toHaveBeenCalled();
    expect(api.getSkillHubVersions).not.toHaveBeenCalled();
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
    expect(dialog.textContent).toContain('版本历史仅供查看');
    expect(dialog.textContent).toContain('arrowhaken');
    expect(api.getSkillHubVersions).toHaveBeenCalledWith('tools/review');
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

  it('deletes a local-only ability from the exact desktop XiaoBa workspace', async () => {
    api.getDevices.mockResolvedValue({ devices: [{
      deviceId: 'alice-device',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localSkill.delete',
        'skillhub.localBot.switch',
      ],
    }] });
    let deleted = false;
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get') return {
        schema: 'xiaoba.skillhub.local_workspace.v1',
        bot_uid: '42',
        active_bot_uid: '42',
        skills_path: 'C:\\xiaoba\\skills',
        skills: deleted ? [] : [{
          local_skill_id: 'local-draft-id',
          name: 'local-draft',
          description: 'Local draft ability',
          relative_path: 'local-draft',
          source: 'user',
          can_share: true,
        }],
      };
      if (toolName === 'skillhub.localSkill.delete') {
        expect(payload).toMatchObject({ bot_uid: '42', local_skill_id: 'local-draft-id' });
        deleted = true;
        return {
          schema: 'xiaoba.skillhub.local_delete.v1',
          bot_uid: '42',
          local_skill_id: 'local-draft-id',
          deleted: true,
          backup_expires_at: '2026-09-23T00:00:00.000Z',
        };
      }
      throw new Error(`unexpected tool ${toolName}`);
    });

    await act(async () => {
      root.render(<FeedbackProvider><SkillHubView user={{ uid: 7 }} /></FeedbackProvider>);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="更多操作 local-draft"]'));
      await new Promise(resolve => requestAnimationFrame(resolve));
    });
    const menu = document.body.querySelector('[role="menu"][aria-label="local-draft 操作"]');
    expect(menu.textContent).toContain('删除本地能力');
    await act(async () => {
      Simulate.click([...menu.querySelectorAll('[role="menuitem"]')]
        .find(button => button.textContent.includes('删除本地能力')));
      await Promise.resolve();
    });
    const confirmation = document.body.querySelector('[role="alertdialog"]');
    expect(confirmation.textContent).toContain('保留 30 天备份（当前需管理员恢复）');
    await act(async () => {
      Simulate.click([...confirmation.querySelectorAll('button')]
        .find(button => button.textContent === '确认删除并备份'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.updateBotDefinitionSkills).not.toHaveBeenCalled();
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localSkill.delete',
      payload: { bot_uid: '42', local_skill_id: 'local-draft-id' },
    }));
    expect(container.textContent).toContain('已删除 local-draft，并保留 30 天备份（当前需管理员恢复）');
    expect(container.textContent).not.toContain('Local draft ability');
  });

  it('loads and shares from the server Runtime bound to the selected owner Bot without switching', async () => {
    api.getDevices.mockResolvedValue({ devices: [{
      deviceId: 'server-42',
      displayName: 'Saturday Runtime',
      runtimeRole: 'server',
      botUid: 42,
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localSkill.delete',
      ],
    }, {
      deviceId: 'server-44',
      displayName: 'Other Runtime',
      runtimeRole: 'server',
      botUid: 44,
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
      ],
    }] });
    requestSkillHubDeviceTool.mockImplementation(async ({ deviceId, toolName }) => {
      expect(deviceId).toBe('server-42');
      if (toolName === 'skillhub.localWorkspace.get') return {
        schema: 'xiaoba.skillhub.local_workspace.v1',
        bot_uid: '42',
        active_bot_uid: '42',
        skills_path: '/srv/xiaoba/private/skills',
        skills: [{
          local_skill_id: 'server-local-id',
          name: 'server-local',
          description: 'Server local ability',
          relative_path: 'server-local',
          source: 'user',
          can_share: true,
        }],
      };
      if (toolName === 'skillhub.localSkill.share') return {
        schema: 'xiaoba.skillhub.local_share.v1',
        bot_uid: '42',
        skill: { id: 'alice/local-demo', name: 'server-local' },
        latest_version: '1.0.0',
        content_hash: 'd'.repeat(64),
      };
      if (toolName === 'skillhub.localSkill.finalize') return {
        schema: 'xiaoba.skillhub.local_finalize.v1',
        bot_uid: '42',
        skill_id: 'alice/local-demo',
        version: '1.0.0',
        content_hash: 'd'.repeat(64),
        direction: 'local_to_cloud',
      };
      throw new Error(`unexpected tool ${toolName}`);
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    await openCustomSkills();
    expect(container.textContent).toContain('服务器运行工作区 · Saturday Runtime');
    expect(container.textContent).not.toContain('/srv/xiaoba/private/skills');
    expect([...container.querySelectorAll('button')].some(button => button.textContent.includes('复制路径'))).toBe(false);

    await act(async () => {
      Simulate.click(container.querySelector('.cc-skillhub-local-card button'));
      await new Promise(resolve => setTimeout(resolve, 0));
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      deviceId: 'server-42',
      toolName: 'skillhub.localSkill.share',
      payload: expect.objectContaining({
        bot_uid: '42',
        local_skill_id: 'server-local-id',
      }),
    }));
    expect(requestSkillHubDeviceTool.mock.calls.some(([call]) => (
      call.toolName === 'skillhub.localBot.switch'
    ))).toBe(false);
  });

  it('blocks desktop fallback when the selected Bot runs on an older server Runtime', async () => {
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Chandler', relation: 'owner' },
        { id: 44, display_name: 'Monica', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => Promise.resolve({
      botId: String(uid),
      revision: 1,
      skills: [],
    }));
    api.getDevices.mockResolvedValue({ devices: [{
      deviceId: 'desktop-7',
      displayName: 'Alice Desktop',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    }, {
      deviceId: 'old-server-42',
      displayName: 'Old Monica Runtime',
      runtimeRole: 'server',
      botUid: 44,
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: ['read_file'],
    }] });
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get' && payload.bot_uid === '42') {
        return {
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: '42',
          active_bot_uid: '42',
          skills_path: 'C:\\xiaoba\\chandler\\skills',
          skills: [],
        };
      }
      throw new Error(`unexpected tool ${toolName}`);
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(resolve => setTimeout(resolve, 0));
    });

    await openCustomSkills();
    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    expect(container.textContent).toContain('当前 Agent 已在服务器运行');
    expect(container.textContent).toContain('已停止操作');
    expect(container.textContent).not.toContain('没有检测到支持 SkillHub 的在线 XiaoBa 运行环境');
    expect(requestSkillHubDeviceTool).not.toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localBot.switch',
      payload: expect.objectContaining({ bot_uid: '44' }),
    }));
  });

  it('explains a server-side Bot switch safety rejection without retrying', async () => {
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { id: 42, display_name: 'Chandler', relation: 'owner' },
        { id: 44, display_name: 'Monica', relation: 'owner' },
      ],
    });
    api.getBotDefinitionSkills.mockImplementation((uid) => Promise.resolve({
      botId: String(uid),
      revision: 1,
      skills: [],
    }));
    api.getDevices.mockResolvedValue({ devices: [{
      deviceId: 'desktop-7',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities: [
        'skillhub.localWorkspace.get',
        'skillhub.localSkill.share',
        'skillhub.localSkill.finalize',
        'skillhub.localBot.switch',
      ],
    }] });
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get' && payload.bot_uid === '42') {
        return {
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: '42',
          active_bot_uid: '42',
          skills_path: 'C:\\xiaoba\\chandler\\skills',
          skills: [],
        };
      }
      if (toolName === 'skillhub.localBot.switch' && payload.bot_uid === '44') {
        throw Object.assign(new Error('desktop switch was not performed'), {
          code: 'BOT_ACTIVE_ON_SERVER_RUNTIME',
        });
      }
      throw new Error(`unexpected tool ${toolName}`);
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(resolve => setTimeout(resolve, 0));
    });
    await openCustomSkills();
    const picker = container.querySelector('.cc-skillhub-bot-picker select');
    await act(async () => {
      picker.value = '44';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise(resolve => setTimeout(resolve, 0));
    });

    expect(container.textContent).toContain('已停止切换本地 XiaoBa');
    const monicaSwitches = requestSkillHubDeviceTool.mock.calls.filter(([request]) => (
      request.toolName === 'skillhub.localBot.switch'
      && request.payload.bot_uid === '44'
    ));
    expect(monicaSwitches).toHaveLength(1);
  });

  it('uses a matching local name when removing a private ability', async () => {
    api.getBotDefinitionSkills.mockResolvedValue({
      botId: '42',
      revision: 3,
      skills: [{
        source: 'skillhub',
        skillId: 'priv_local1',
        version: 'private-v1',
        contentHash: 'c'.repeat(64),
      }],
    });
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        runtimeRole: 'desktop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localSkill.delete',
          'skillhub.localBot.switch',
        ],
      }],
    });
    let deleted = false;
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get') return {
        schema: 'xiaoba.skillhub.local_workspace.v1',
        bot_uid: '42',
        active_bot_uid: '42',
        skills_path: 'C:\\xiaoba\\skills',
        skills: deleted ? [] : [{
          local_skill_id: 'local-1',
          name: 'local-demo',
          description: 'Local demo',
          relative_path: 'local-demo',
          source: 'user',
          can_share: true,
          skill_hub: { reference: {
            source: 'skillhub',
            skillId: 'priv_local1',
            version: 'private-v1',
            contentHash: 'c'.repeat(64),
          } },
        }],
      };
      if (toolName === 'skillhub.localSkill.delete') {
        expect(payload).toMatchObject({ bot_uid: '42', local_skill_id: 'local-1' });
        deleted = true;
        return {
          schema: 'xiaoba.skillhub.local_delete.v1',
          bot_uid: '42',
          local_skill_id: 'local-1',
          deleted: true,
          backup_expires_at: '2026-09-23T00:00:00.000Z',
        };
      }
      throw new Error(`unexpected tool ${toolName}`);
    });
    api.updateBotDefinitionSkills.mockResolvedValueOnce({ botId: '42', revision: 4, skills: [] });

    await act(async () => {
      root.render(<FeedbackProvider><SkillHubView user={{ uid: 7 }} /></FeedbackProvider>);
      await Promise.resolve();
      await Promise.resolve();
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    expect(container.querySelector('.cc-skillhub-added-title h3')?.textContent).toBe('local-demo');
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="更多操作 local-demo"]'));
      await new Promise((resolve) => requestAnimationFrame(resolve));
    });
    const menu = document.body.querySelector('[role="menu"][aria-label="local-demo 操作"]');
    await act(async () => {
      Simulate.click([...menu.querySelectorAll('[role="menuitem"]')]
        .find((button) => button.textContent.includes('从 Agent 移除')));
      await Promise.resolve();
    });

    const confirmation = document.body.querySelector('[role="alertdialog"]');
    expect(confirmation.textContent).toContain('删除“local-demo”的本地能力');
    expect(confirmation.textContent).toContain('保留 30 天备份（当前需管理员恢复）');
    expect(confirmation.textContent).not.toContain('priv_local1');
    await act(async () => {
      Simulate.click([...confirmation.querySelectorAll('button')]
        .find((button) => button.textContent === '确认删除并备份'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.updateBotDefinitionSkills).toHaveBeenCalledWith('42', 3, []);
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localSkill.delete',
      payload: { bot_uid: '42', local_skill_id: 'local-1' },
    }));
    expect(container.textContent).toContain('已删除 local-demo，并保留 30 天备份（当前需管理员恢复）；同时已从 Agent“Owner Bot”移除');
    expect(container.textContent).not.toContain('priv_local1');
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
    expect(trigger?.title).toBe('Owner Bot');
    await act(async () => {
      Simulate.click(trigger);
    });
    expect(trigger.getAttribute('aria-expanded')).toBe('true');
    const listbox = document.body.querySelector('[role="listbox"][aria-label="Agent 列表"]');
    expect(listbox).toBeTruthy();
    expect(listbox.style.left).toBe('100px');
    expect(listbox.style.width).toBe('176px');
    expect(document.body.querySelector('[role="option"][aria-selected="true"]')?.textContent).toContain('Owner Bot');
    expect(document.body.querySelector('[role="option"][aria-selected="true"]')?.title).toBe('Owner Bot');

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

    expect(container.querySelectorAll('.cc-skillhub-bot-picker:first-child option')).toHaveLength(2);
    expect(container.textContent).toContain('tools/review');
    expect(container.textContent).toContain('Friend Bot');

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

  it('loads a friend Bot as read-only metadata without touching local devices', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    api.getDevices.mockClear();
    await act(async () => {
      Simulate.change(container.querySelector('.cc-skillhub-agent-native-select'), {
        target: { value: '43' },
      });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(api.getAgentSkills).toHaveBeenCalledWith('43');
    expect(api.getBotDefinitionSkills).toHaveBeenCalledWith('42');
    expect(api.getDevices).not.toHaveBeenCalled();
    expect(container.textContent).toContain('cloud-html-artifact');
    expect(container.textContent).not.toContain('私有能力');
    expect(container.textContent).toContain('第 2 版 · 最近变更：lin');
    expect(container.textContent).not.toContain('v2');
    expect(container.textContent).toContain('只读查看');
    expect(container.textContent).toContain('该 Agent 运行环境中尚未同步的本地 Skill 不会显示');
    expect(container.textContent).toContain('已同步能力');
    expect(container.textContent).toContain('来自该 Agent 已同步到 BotDefinition 的只读元数据');
    expect(container.querySelector('.cc-skillhub-custom-entry')).toBeNull();
    expect(container.querySelector('.cc-skillhub-copy-action')).toBeNull();
    expect(container.querySelector('.cc-skillhub-more-action')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="查看 cloud-html-artifact 详情"]'));
      await Promise.resolve();
      await Promise.resolve();
    });
    const dialog = document.body.querySelector('[role="dialog"]');
    expect(dialog).toBeTruthy();
    expect(api.getAgentSkillVersions).toHaveBeenCalledWith('43', 'private/review', {
      limit: 20,
      beforeRevision: 0,
    });
    expect(dialog.textContent).toContain('版本历史');
    expect(dialog.textContent).toContain('第 2 版');
    expect(dialog.textContent).toContain('当前使用');
    expect(dialog.textContent).toContain('第 1 版');
    expect(dialog.textContent).toContain('Bot 自动同步');
    expect(dialog.textContent).toContain('版本历史仅供查看');
    expect(dialog.textContent).not.toContain('回退到此版本');
  });

  it('discards an open history request when the selected Agent changes', async () => {
    const pendingHistory = deferred();
    api.getMyBots.mockResolvedValueOnce({
      bots: [
        { uid: 42, display_name: 'Owner Bot', relation: 'owner', is_owner: true },
        { uid: 43, display_name: 'Friend A', relation: 'friend', is_owner: false, owner_id: 98 },
        { uid: 44, display_name: 'Friend B', relation: 'friend', is_owner: false, owner_id: 99 },
      ],
    });
    api.getAgentSkills.mockImplementation(async (uid) => ({
      botId: String(uid),
      skills_visibility: 'owner',
      skills: [{
        source: 'skillhub', skillId: 'private/review', version: `v-${uid}`,
        displayName: `review-${uid}`, revisionNumber: 1,
      }],
    }));
    api.getAgentSkillVersions.mockReturnValueOnce(pendingHistory.promise);

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.change(container.querySelector('.cc-skillhub-agent-native-select'), {
        target: { value: '43' },
      });
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="查看 review-43 详情"]'));
      await Promise.resolve();
    });
    expect(document.body.querySelector('[role="dialog"]')).toBeTruthy();
    expect(api.getAgentSkillVersions).toHaveBeenCalledWith('43', 'private/review', {
      limit: 20,
      beforeRevision: 0,
    });

    await act(async () => {
      Simulate.change(container.querySelector('.cc-skillhub-agent-native-select'), {
        target: { value: '44' },
      });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(document.body.querySelector('[role="dialog"]')).toBeFalsy();
    await act(async () => {
      pendingHistory.resolve({ currentVersion: 'v-43', versions: [] });
      await Promise.resolve();
    });
    expect(document.body.querySelector('[role="dialog"]')).toBeFalsy();
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
        runtimeRole: 'desktop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities: [
          'skillhub.localWorkspace.get',
          'skillhub.localSkill.share',
          'skillhub.localSkill.finalize',
          'skillhub.localBot.switch',
        ],
      }, {
        deviceId: 'cloud-bot-runtime',
        displayName: 'XiaoBa Doubao Runtime',
        runtimeRole: 'server',
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

    expect(container.querySelector('button[aria-label="复制 local-demo"]')).toBeNull();

    await openCustomSkills();
    expect(container.querySelector('.cc-skillhub-device-picker')).toBeNull();
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

  it('shows a blocked local Skill without falsely marking it as published', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        displayName: 'Alice Laptop',
        runtimeRole: 'desktop',
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
    requestSkillHubDeviceTool.mockResolvedValue({
      schema: 'xiaoba.skillhub.local_workspace.v1',
      bot_uid: '42',
      active_bot_uid: '42',
      skills_path: 'C:\\xiaoba\\skills',
      skills: [{
        local_skill_id: 'blocked-1',
        name: 'cloud-html-artifact',
        description: 'Publish HTML artifacts',
        relative_path: 'cloud-html-artifact',
        source: 'user',
        can_share: false,
        share_error: 'Skill contains sensitive material and cannot be uploaded: scripts/publish-html-directory.mjs',
        skill_hub: {
          author: 'alice',
          version: '1.0.0',
          uploaded_at: '2026-08-05T00:00:00.000Z',
        },
      }],
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCustomSkills();

    const card = container.querySelector('.cc-skillhub-local-card');
    expect(card.textContent).toContain('cloud-html-artifact');
    expect(card.textContent).toContain('无法发布');
    expect(card.textContent).toContain('scripts/publish-html-directory.mjs');
    expect(card.textContent).not.toContain('已发布到团队');
    expect(card.querySelector('.cc-skillhub-validation-error')).not.toBeNull();
    expect(card.querySelector('button').disabled).toBe(true);
    expect(card.querySelector('button').textContent).toContain('请先修复此 Skill');
    expect(requestSkillHubDeviceTool).toHaveBeenCalledTimes(1);
  });

  it('keeps local Skill cards visible when publishing one Skill fails', async () => {
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        displayName: 'Alice Laptop',
        runtimeRole: 'desktop',
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
            local_skill_id: 'short-description',
            name: 'ocr',
            description: '图片 OCR',
            relative_path: 'ocr',
            source: 'user',
            can_share: true,
          }],
        };
      }
      if (toolName === 'skillhub.localSkill.share') {
        throw new Error('description 校验失败');
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

    await act(async () => {
      Simulate.click(container.querySelector('.cc-skillhub-local-card button'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('[role="alert"]')?.textContent).toContain('description 校验失败');
    expect(container.querySelector('.cc-skillhub-local-card')?.textContent).toContain('ocr');
    expect(container.querySelector('.cc-skillhub-local-card button')?.disabled).toBe(false);
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
        runtimeRole: 'desktop',
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
        runtimeRole: 'desktop',
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

  it('recovers an unavailable route and workspace handoff during an explicit Bot switch', async () => {
    vi.useFakeTimers();
    const readyDevice = {
      deviceId: 'alice-device',
      runtimeRole: 'desktop',
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
          return Promise.reject(Object.assign(new Error('no route'), { code: 'target_device_unavailable' }));
        }
        if (botBWorkspaceAttempts === 2) {
          return Promise.reject(Object.assign(new Error('Bot is not active'), { code: 'BOT_NOT_ACTIVE' }));
        }
        if (botBWorkspaceAttempts === 3) {
          return Promise.reject(Object.assign(
            new Error('Bot Skill workspace ownership is changing (42 -> 44); retry the write.'),
            { code: 'SKILLHUB_OPERATION_FAILED' },
          ));
        }
        if (botBWorkspaceAttempts === 4) {
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
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
      await Promise.resolve();
    });
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      toolName: 'skillhub.localBot.switch',
      payload: expect.objectContaining({ bot_uid: '44' }),
    }));

    await act(async () => {
      await vi.runAllTimersAsync();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('C:\\xiaoba\\bot-b\\skills');
    expect(container.textContent).not.toContain('no route');
    expect(globalThis.localStorage.getItem('catsco.skillhub.selectedBot.7')).toBe('44');
  });

  it('sends a newer switch intent when the user returns to the currently active Bot', async () => {
    vi.useFakeTimers();
    const capabilities = [
      'skillhub.localWorkspace.get',
      'skillhub.localSkill.share',
      'skillhub.localSkill.finalize',
      'skillhub.localBot.switch',
    ];
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
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        runtimeRole: 'desktop',
        active: true,
        routeConnected: true,
        routable: true,
        capabilities,
      }],
    });
    requestSkillHubDeviceTool.mockImplementation(({ toolName, payload }) => {
      if (toolName === 'skillhub.localWorkspace.get') {
        return Promise.resolve({
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: payload.bot_uid,
          active_bot_uid: payload.bot_uid,
          skills_path: `C:\\xiaoba\\bot-${payload.bot_uid}\\skills`,
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
    await act(async () => {
      picker.value = '42';
      Simulate.change(picker);
      await Promise.resolve();
      await Promise.resolve();
    });

    const switchIntents = requestSkillHubDeviceTool.mock.calls
      .map(([request]) => request)
      .filter((request) => request.toolName === 'skillhub.localBot.switch')
      .map((request) => request.payload.bot_uid);
    expect(switchIntents).toEqual(['44', '42']);
  });

  it('re-submits an accepted switch once without creating a connector restart loop', async () => {
    vi.useFakeTimers();
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
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        runtimeRole: 'desktop',
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
        if (botBWorkspaceAttempts <= 4) {
          return Promise.reject(Object.assign(new Error('Bot is not active'), { code: 'BOT_NOT_ACTIVE' }));
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
    await act(async () => {
      await vi.runAllTimersAsync();
      await Promise.resolve();
    });

    const botBSwitches = requestSkillHubDeviceTool.mock.calls
      .map(([request]) => request)
      .filter((request) => (
        request.toolName === 'skillhub.localBot.switch'
        && request.payload.bot_uid === '44'
      ));
    expect(botBSwitches).toHaveLength(2);
    expect(container.textContent).toContain('C:\\xiaoba\\bot-b\\skills');
  });

  it('does not repeat recovery when the one re-submitted switch returns a retryable error', async () => {
    vi.useFakeTimers();
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
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        runtimeRole: 'desktop',
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
    let botBWorkspaceAttempts = 0;
    let switchAttempts = 0;
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
        if (botBWorkspaceAttempts <= 4) {
          return Promise.reject(Object.assign(new Error('Bot is not active'), { code: 'BOT_NOT_ACTIVE' }));
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
        switchAttempts += 1;
        if (payload.bot_uid === '44' && switchAttempts === 2) {
          return Promise.reject(Object.assign(new Error('Workspace is changing'), {
            code: 'WORKSPACE_SWITCHING',
          }));
        }
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
    await act(async () => {
      await vi.runAllTimersAsync();
      await Promise.resolve();
    });

    const botBSwitches = requestSkillHubDeviceTool.mock.calls
      .map(([request]) => request)
      .filter((request) => (
        request.toolName === 'skillhub.localBot.switch'
        && request.payload.bot_uid === '44'
      ));
    expect(botBSwitches).toHaveLength(2);
    expect(container.textContent).toContain('C:\\xiaoba\\bot-b\\skills');
  });

  it('does not switch back to a stale Bot after a late BOT_NOT_ACTIVE response', async () => {
    vi.useFakeTimers();
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
    api.getDevices.mockResolvedValue({
      devices: [{
        deviceId: 'alice-device',
        runtimeRole: 'desktop',
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
      await vi.advanceTimersByTimeAsync(2_000);
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

  it('switches the current Bot after one refresh discovers the first desktop XiaoBa', async () => {
    vi.useFakeTimers();
    const capabilities = [
      'skillhub.localWorkspace.get',
      'skillhub.localSkill.share',
      'skillhub.localSkill.finalize',
      'skillhub.localBot.switch',
    ];
    const device = {
      deviceId: 'device-a',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities,
    };
    api.getDevices
      .mockResolvedValueOnce({ devices: [] })
      .mockResolvedValue({ devices: [device] });
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName, payload }) => {
      if (toolName === 'skillhub.localBot.switch') {
        return { schema: 'xiaoba.skillhub.bot_switch.v1', bot_uid: payload.bot_uid };
      }
      if (toolName === 'skillhub.localWorkspace.get') {
        return {
          schema: 'xiaoba.skillhub.local_workspace.v1',
          bot_uid: payload.bot_uid,
          active_bot_uid: payload.bot_uid,
          skills_path: 'C:\\xiaoba\\skills',
          skills: [],
        };
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
    expect(container.textContent).toContain('没有检测到支持 SkillHub 的在线 XiaoBa 运行环境');

    await act(async () => {
      Simulate.click(container.querySelector('.cc-skillhub-local-actions button:last-child'));
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(requestSkillHubDeviceTool).toHaveBeenCalledWith(expect.objectContaining({
      deviceId: 'device-a',
      toolName: 'skillhub.localBot.switch',
    }));

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.textContent).toContain('C:\\xiaoba\\skills');
    expect(requestSkillHubDeviceTool.mock.calls.filter(([request]) => (
      request.toolName === 'skillhub.localWorkspace.get'
    ))).toHaveLength(1);
  });

  it('clears an automatic route when a second desktop XiaoBa comes online', async () => {
    const capabilities = [
      'skillhub.localWorkspace.get',
      'skillhub.localSkill.share',
      'skillhub.localSkill.finalize',
      'skillhub.localBot.switch',
    ];
    const deviceA = {
      deviceId: 'device-a',
      displayName: 'Device A',
      runtimeRole: 'desktop',
      active: true,
      routeConnected: true,
      routable: true,
      capabilities,
    };
    const deviceB = {
      ...deviceA,
      deviceId: 'device-b',
      displayName: 'Device B',
    };
    api.getDevices
      .mockResolvedValueOnce({ devices: [deviceA] })
      .mockResolvedValue({ devices: [deviceA, deviceB] });
    requestSkillHubDeviceTool.mockImplementation(async ({ toolName, payload }) => {
      if (toolName !== 'skillhub.localWorkspace.get') throw new Error(`unexpected tool ${toolName}`);
      return {
        schema: 'xiaoba.skillhub.local_workspace.v1',
        bot_uid: payload.bot_uid,
        active_bot_uid: payload.bot_uid,
        skills_path: 'C:\\xiaoba\\skills',
        skills: [],
      };
    });
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
    await openCustomSkills();
    expect(container.textContent).toContain('C:\\xiaoba\\skills');
    expect(requestSkillHubDeviceTool).toHaveBeenCalledTimes(1);

    await act(async () => {
      Simulate.click(container.querySelector('.cc-skillhub-local-actions button:last-child'));
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-skillhub-device-picker')).toBeNull();
    expect(container.textContent).toContain('检测到多个可用的 XiaoBa 运行环境');
    expect(container.textContent).not.toContain('正在读取本地能力');
    expect(requestSkillHubDeviceTool).toHaveBeenCalledTimes(1);
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
    expect(api.searchSkillHubSkills).toHaveBeenCalledWith('first', { searchMode: 'name' });
    expect(api.searchSkillHubSkills).toHaveBeenCalledWith('second', { searchMode: 'name' });
    expect(form.querySelector('input').placeholder).toBe('搜索能力名称…');
  });
});
