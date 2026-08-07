import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import SkillHubView, {
  assertSkillHubDeviceResult,
  normalizeOwnedBots,
  normalizeSkillHubDevices,
  normalizeLocalSkills,
  normalizeSkillHubSkills,
  isLocalSkillShared,
  isPrivateSkillHubReference,
  resolveSkillHubEntry,
  resolveSharedSkillHubMetadata,
  upsertSkillRef,
  waitForPublishedSkillHubEntry,
} from './skillhub-view';
import { api, requestSkillHubDeviceTool } from '../api';

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

describe('SkillHubView', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.clearAllMocks();
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
    container.remove();
    vi.unstubAllGlobals();
  });

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
      share_error: 'SKILL.md 缺少必填字段 name 或 description。',
    }] })[0]).toMatchObject({
      name: 'local-demo',
      relativePath: 'local-demo',
      skillHub: { version: '1.0.0' },
      shareError: 'SKILL.md 缺少必填字段 name 或 description。',
    });
    expect(isLocalSkillShared({
      canShare: true,
      skillHub: { author: 'alice', version: '1.0.0' },
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

  it('loads only owner bots and binds a precise SkillHub reference', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelectorAll('.cc-skillhub-bot-picker:first-child option')).toHaveLength(1);
    expect(container.textContent).toContain('tools/review');
    expect(container.textContent).not.toContain('Friend Bot');

    const installButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('绑定到当前 Bot'));
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
    expect(container.textContent).toContain('Alice Laptop');
    expect(container.textContent).toContain('local-demo');
    expect(container.textContent).toContain('C:\\xiaoba\\skills');

    const shareButton = container.querySelector('.cc-skillhub-local-card button');
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

  it('shows an invalid local Skill reason without exposing a share action', async () => {
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
    requestSkillHubDeviceTool.mockResolvedValue({
      schema: 'xiaoba.skillhub.local_workspace.v1',
      bot_uid: '42',
      active_bot_uid: '42',
      skills_path: 'C:\\xiaoba\\skills',
      skills: [{
        local_skill_id: 'invalid-1',
        name: 'test_8_7',
        relative_path: 'test_8_7',
        source: 'user',
        can_share: false,
        share_error: 'SKILL.md 缺少必填字段 name 或 description。请在文件顶部的 YAML frontmatter 中补全后重试。',
      }],
    });

    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    const card = container.querySelector('.cc-skillhub-local-card');
    expect(card.textContent).toContain('test_8_7');
    expect(card.textContent).toContain('缺少必填字段 name 或 description');
    expect(card.querySelector('.cc-skillhub-validation-error')).not.toBeNull();
    expect(card.querySelector('button')).toBeNull();
    expect(requestSkillHubDeviceTool).toHaveBeenCalledTimes(1);
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
    const installButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('绑定到当前 Bot'));
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
    expect(container.textContent).toContain('正在读取 BotDefinition');
    expect(container.textContent).not.toContain('bot-a/skill');
    await act(async () => {
      botB.resolve({ revision: 2, skills: [{ source: 'skillhub', skillId: 'bot-b/skill', version: '1', contentHash: 'b'.repeat(64) }] });
      await Promise.resolve();
    });

    expect(container.textContent).toContain('bot-b/skill');
    expect(container.textContent).not.toContain('bot-a/skill');
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
    const devicePicker = container.querySelectorAll('.cc-skillhub-bot-picker select')[1];
    await act(async () => {
      devicePicker.value = 'device-a';
      Simulate.change(devicePicker);
      await Promise.resolve();
    });
    expect(container.textContent).toContain('正在切换本地 Bot 并同步 Skills');

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
    expect(container.textContent).not.toContain('正在切换本地 Bot 并同步 Skills');
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
    const installButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('绑定到当前 Bot'));
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

    const installButton = [...container.querySelectorAll('.cc-skillhub-card button')][0];
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
    const installButton = [...container.querySelectorAll('.cc-skillhub-card button')][0];
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
    const installButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('绑定到当前 Bot'));
    await act(async () => {
      Simulate.click(installButton);
      await Promise.resolve();
    });

    const refreshButton = [...container.querySelectorAll('button')]
      .find((button) => button.textContent.includes('刷新'));
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
    expect(container.textContent).toContain('tools/summarize');
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

    expect(container.textContent).toContain('latest/result');
    expect(container.textContent).not.toContain('stale/result');
  });
});
