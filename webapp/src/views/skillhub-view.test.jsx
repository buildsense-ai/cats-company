import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import SkillHubView, {
  normalizeOwnedBots,
  normalizeSkillHubSkills,
  resolveSkillHubEntry,
  upsertSkillRef,
} from './skillhub-view';
import { api } from '../api';

vi.mock('../api', () => ({
  api: {
    getMyBots: vi.fn(),
    getBotDefinitionSkills: vi.fn(),
    updateBotDefinitionSkills: vi.fn(),
    searchSkillHubSkills: vi.fn(),
    getSkillHubSkill: vi.fn(),
  },
}));

function deferred() {
  let resolve;
  const promise = new Promise((next) => { resolve = next; });
  return { promise, resolve };
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
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
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
    expect(upsertSkillRef([{ skillId: 'a', version: '1' }], { skillId: 'b', version: '2' }))
      .toEqual([{ skillId: 'a', version: '1' }, { skillId: 'b', version: '2' }]);
    expect(resolveSkillHubEntry(
      { skillId: 'a', latestVersion: '2.0.0', contentHash: '' },
      { skill: { id: 'a', latestVersion: '2.0.0' }, versions: [{ id: 'a', version: '2.0.0', contentHash: 'c'.repeat(64) }] },
    )).toMatchObject({ latestVersion: '2.0.0', contentHash: 'c'.repeat(64) });
  });

  it('loads only owner bots and binds a precise SkillHub reference', async () => {
    await act(async () => {
      root.render(<SkillHubView user={{ uid: 7 }} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelectorAll('.cc-skillhub-bot-picker option')).toHaveLength(1);
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
