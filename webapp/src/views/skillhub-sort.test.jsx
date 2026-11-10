import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import SkillHubContent from './skillhub-content';
import { skillUploadedTimestamp } from './skillhub-categories';
import { readSkillSort, writeSkillSort, skillAddedTimestamp, sortSkills } from './skillhub-sort';

const skills = [
  { skillId: 'owner/b', displayName: 'Beta', addedAt: '2026-11-02', publishedAt: '2026-11-01' },
  { skillId: 'owner/a', displayName: 'Alpha', addedAt: '2026-11-01', publishedAt: '2026-11-02' },
  { skillId: 'owner/z', displayName: 'Zulu' },
  { skillId: 'owner/c', displayName: 'Charlie', addedAt: 'invalid' },
];
const ids = values => values.map(skill => skill.skillId.split('/')[1]);

beforeEach(() => localStorage.clear());

it('keeps unknown dates last in both directions and resolves ties by name then ID', () => {
  const sort = order => ids(sortSkills(skills, { order, timeOf: skillAddedTimestamp }));
  expect(sort('time-desc')).toEqual(['b', 'a', 'c', 'z']);
  expect(sort('time-asc')).toEqual(['a', 'b', 'c', 'z']);
  expect(ids(skills)).toEqual(['b', 'a', 'z', 'c']);
  expect(ids(sortSkills([
    { skillId: 'owner/b', displayName: 'Same', addedAt: '2026-11-01' },
    { skillId: 'owner/a', displayName: 'Same', addedAt: '2026-11-01' },
  ], { timeOf: skillAddedTimestamp }))).toEqual(['a', 'b']);
});

it('uses addition time for Agent skills and publication time for the library', () => {
  expect(ids(sortSkills(skills, { timeOf: skillAddedTimestamp }))).toEqual(['b', 'a', 'c', 'z']);
  expect(ids(sortSkills(skills, { timeOf: skillUploadedTimestamp }))).toEqual(['a', 'b', 'c', 'z']);
});

it('sorts the displayed localized name, with Chinese pinyin and natural numbers', () => {
  const entries = [
    { skillId: 'owner/z', displayName: 'Zulu', translations: { 'zh-CN': { name: '阿尔法' } } },
    { skillId: 'owner/a', displayName: 'Alpha', translations: { 'zh-CN': { name: '北京' } } },
  ];
  expect(ids(sortSkills(entries, { order: 'name-asc', timeOf: skillAddedTimestamp }))).toEqual(['a', 'z']);
  expect(ids(sortSkills(entries, { order: 'name-asc', language: 'zh-CN', timeOf: skillAddedTimestamp }))).toEqual(['z', 'a']);
  expect(ids(sortSkills(entries, { order: 'name-desc', language: 'zh-CN', timeOf: skillAddedTimestamp }))).toEqual(['a', 'z']);
  expect(sortSkills([{ displayName: 'Skill 10' }, { displayName: 'skill 2' }], {
    order: 'name-asc', timeOf: skillAddedTimestamp,
  }).map(skill => skill.displayName)).toEqual(['skill 2', 'Skill 10']);
});

it('isolates saved preferences by user and tab and tolerates unavailable storage', () => {
  writeSkillSort('7', 'added', 'name-desc');
  expect(readSkillSort('7', 'added')).toBe('name-desc');
  expect(readSkillSort('7', 'catalogue')).toBe('time-desc');
  expect(readSkillSort('8', 'added')).toBe('time-desc');
  localStorage.setItem('catsco.skillhub.sort.7.added', 'unknown');
  expect(readSkillSort('7', 'added')).toBe('time-desc');
  const getter = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
  const setter = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
  try {
    expect(readSkillSort('7', 'added')).toBe('time-desc');
    expect(() => writeSkillSort('7', 'added', 'name-asc')).not.toThrow();
  } finally { getter.mockRestore(); setter.mockRestore(); }
});

describe('sorting controls', () => {
  let container; let root;
  let section; let query;
  const names = () => [...container.querySelectorAll('article h3')].map(node => node.textContent);
  const render = async (owner = '7') => {
    await act(async () => root.render(<SkillHubContent
      activeSection={section} categoryOwnerUID={owner} selectedBotUID='42'
      agentOptions={[{ value: '42', label: 'David' }]} definition={{ skills }}
      definitionReady installedByID={new Map()} librarySkills={skills}
      addedSkillPresentationByID={new Map(skills.map(skill => [skill.skillId, { label: skill.displayName, details: skill }]))}
      addedSkillQuery={query} query={query}
      onChangeSection={value => { section = value; }} onSelectAgent={() => {}}
      onAddedSkillQuery={() => {}} onQueryChange={() => {}} onSearch={() => {}}
    />));
  };
  const choose = async label => {
    await act(async () => container.querySelector('.cc-skillhub-sort-trigger').click());
    const options = document.querySelector('[role="listbox"][aria-label="Skill 排序方式"]');
    const option = [...options.querySelectorAll('[role="option"]')].find(node => node.textContent === `排序：${label}`);
    await act(async () => option.click());
  };
  beforeEach(() => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div'); document.body.appendChild(container);
    root = createRoot(container); section = 'added'; query = '';
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

  it('retains independent tab choices across searches, rerenders and remounts', async () => {
    await render();
    expect(names()).toEqual(['Beta', 'Alpha', 'Charlie', 'Zulu']);
    const tools = container.querySelector('.cc-skillhub-navigation-tools');
    expect([...tools.children].map(node => node.className)).toEqual(expect.arrayContaining([
      'cc-skillhub-language', expect.stringContaining('cc-skillhub-sort'), 'cc-skillhub-added-search',
    ]));
    await choose('名称 Z → A');
    expect(names()).toEqual(['Zulu', 'Charlie', 'Beta', 'Alpha']);
    expect(container.querySelector('.cc-skillhub-sort-trigger').getAttribute('aria-label')).toBe('排序：名称 Z → A');
    query = 'Alpha'; await render(); expect(names()).toEqual(['Alpha']);
    query = ''; section = 'catalogue'; await render();
    expect(names()).toEqual(['Alpha', 'Beta', 'Charlie', 'Zulu']);
    await choose('最早发布／更新');
    expect(names()).toEqual(['Beta', 'Alpha', 'Charlie', 'Zulu']);
    section = 'added'; await render();
    expect(names()).toEqual(['Zulu', 'Charlie', 'Beta', 'Alpha']);
    await act(async () => root.render(null));
    await render();
    expect(names()).toEqual(['Zulu', 'Charlie', 'Beta', 'Alpha']);
    await render('8');
    expect(names()).toEqual(['Beta', 'Alpha', 'Charlie', 'Zulu']);
  });

  it('resorts using the visible label when changing language', async () => {
    const translations = [
      { skillId: 'owner/z', displayName: 'Zulu', translations: { 'zh-CN': { name: '阿尔法' } } },
      { skillId: 'owner/a', displayName: 'Alpha', translations: { 'zh-CN': { name: '北京' } } },
    ];
    section = 'catalogue';
    await act(async () => root.render(<SkillHubContent
      activeSection={section} categoryOwnerUID='7' selectedBotUID='42'
      agentOptions={[]} definition={{ skills: [] }} installedByID={new Map()} librarySkills={translations}
    />));
    await choose('名称 A → Z'); expect(names()).toEqual(['Alpha', 'Zulu']);
    await act(async () => [...container.querySelectorAll('.cc-skillhub-language button')].find(node => node.textContent === '中文').click());
    expect(names()).toEqual(['阿尔法', '北京']);
  });
});
