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
  const selectAll = async () => act(async () => {
    [...container.querySelectorAll('.cc-skillhub-category-filter button')].find(button => button.textContent === '全部').click();
  });
  const render = async (owner = '7', props = {}) => {
    await act(async () => root.render(<SkillHubContent
      activeSection={section} categoryOwnerUID={owner} selectedBotUID='42'
      agentOptions={[{ value: '42', label: 'David' }]} definition={{ skills }}
      definitionReady installedByID={new Map()} librarySkills={skills}
      addedSkillPresentationByID={new Map(skills.map(skill => [skill.skillId, { label: skill.displayName, details: skill }]))}
      addedSkillQuery={query} query={query}
      onChangeSection={value => { section = value; }} onSelectAgent={() => {}}
      onAddedSkillQuery={() => {}} onQueryChange={() => {}} onSearch={() => {}}
      {...props}
    />));
  };
  const choose = async label => {
    await act(async () => container.querySelector('.cc-skillhub-sort-trigger').click());
    const options = document.querySelector('[role="listbox"][aria-label="Skill 排序方式"]');
    const option = [...options.querySelectorAll('[role="option"]')].find(node => node.textContent === label);
    await act(async () => option.click());
  };
  beforeEach(() => {
    globalThis.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div'); document.body.appendChild(container);
    root = createRoot(container); section = 'added'; query = '';
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

  it('uses recent additions in the combined page even with an older sort preference', async () => {
    writeSkillSort('7', 'added', 'time-asc');
    const agentPickerContainer = document.createElement('div');
    const props = { embedded: true, agentPickerContainer };
    await render('7', props);

    expect(names()).toEqual(['Beta', 'Alpha', 'Charlie', 'Zulu']);
    expect(container.querySelector('.cc-skillhub-sort-trigger')).toBeNull();
    expect(agentPickerContainer.querySelector('.cc-skillhub-agent-select-trigger')).toBeTruthy();
    expect(container.querySelector('.cc-skillhub-agent-select-trigger')).toBeNull();
    expect(readSkillSort('7', 'added')).toBe('time-asc');
    expect(container.querySelector('.cc-skillhub-category-toolbar')).toBeNull();

    const filter = container.querySelector('.cc-library-filter > button');
    const panel = container.querySelector('[aria-label="能力筛选"]');
    expect(panel.hidden).toBe(true);
    await act(async () => filter.click());
    expect(panel.hidden).toBe(false);
    expect(panel.querySelectorAll('.cc-library-filter-select-trigger')).toHaveLength(2);

    section = 'catalogue';
    await render('7', props);
    const catalogueFilters = container.querySelector('[aria-label="能力筛选"]');
    expect(catalogueFilters.hidden).toBe(true);
    await act(async () => container.querySelector('.cc-library-filter > button').click());
    expect(catalogueFilters.querySelectorAll('.cc-library-filter-select-trigger')).toHaveLength(3);
    expect(catalogueFilters.querySelector('[aria-label="排序"]')?.dataset.value).toBe('time-desc');
  });

  it('keeps the chosen category separate from the library and makes it directly clearable', async () => {
    const props = { embedded: true, browseStateRef: { current: {} } };
    await render('7', props);
    await act(async () => container.querySelector('.cc-library-filter > button').click());
    await act(async () => container.querySelector('.cc-library-filter-panel [aria-label="分类"]').click());
    const categoryMenu = document.querySelector('[role="listbox"][aria-label="分类"]');
    await act(async () => [...categoryMenu.querySelectorAll('[role="option"]')].find(option => option.textContent === '其他').click());
    expect(container.querySelector('[aria-label="清除分类筛选 其他"]')).toBeTruthy();
    section = 'catalogue';
    await render('7', props);
    expect(container.querySelector('[aria-label="清除分类筛选 其他"]')).toBeNull();
    section = 'added';
    await render('7', props);
    await act(async () => container.querySelector('[aria-label="清除分类筛选 其他"]').click());
    expect(container.querySelector('.cc-library-filter-chips')).toBeNull();
    expect(document.activeElement).toBe(container.querySelector('.cc-library-filter > button'));
  });

  it('keeps empty inventory controls out of view and supplies retry for a failed definition load', async () => {
    const onRefreshDefinition = vi.fn();
    const props = { embedded: true, definition: { skills: [] }, onRefreshDefinition };
    await render('7', props);
    expect(container.querySelector('.cc-skillhub-navigation-tools')).toBeNull();
    expect(container.querySelector('#skillhub-added-tab span')).toBeNull();
    expect(container.querySelector('.cc-skillhub-empty-added button')?.textContent).toBe('浏览能力库');

    await render('7', { ...props, definitionReady: false, definitionError: '读取失败' });
    const retry = container.querySelector('[role="alert"] button');
    expect(retry?.textContent).toBe('重试');
    await act(async () => retry.click());
    expect(onRefreshDefinition).toHaveBeenCalledTimes(1);
  });

  it('retains independent tab choices across searches, rerenders and remounts', async () => {
    await render();
    expect(names()).toEqual(['Beta', 'Alpha', 'Charlie', 'Zulu']);
    const tools = container.querySelector('.cc-skillhub-navigation-tools');
    expect(tools.querySelector('.cc-skillhub-language')).toBeNull();
    expect(tools.querySelector('.cc-skillhub-search-toggle')).toBeTruthy();
    expect(tools.lastElementChild.className).toContain('cc-skillhub-sort');
    expect(container.querySelector('.cc-skillhub-category-toolbar').lastElementChild.className)
      .toContain('cc-skillhub-language');
    await choose('名称 Z → A');
    expect(names()).toEqual(['Zulu', 'Charlie', 'Beta', 'Alpha']);
    expect(container.querySelector('.cc-skillhub-sort-trigger').getAttribute('aria-label')).toBe('排序：名称 Z → A');
    query = 'Alpha'; await render(); expect(names()).toEqual(['Alpha']);
    query = ''; section = 'catalogue'; await render();
    await selectAll();
    expect(names()).toEqual(['Alpha', 'Beta', 'Charlie', 'Zulu']);
    await choose('最早发布');
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
    await selectAll();
    await choose('名称 A → Z'); expect(names()).toEqual(['Alpha', 'Zulu']);
    await act(async () => [...container.querySelectorAll('.cc-skillhub-language button')].find(node => node.textContent === '中文').click());
    expect(names()).toEqual(['阿尔法', '北京']);
  });

  it('expands search on demand and restores the button on Escape or empty blur', async () => {
    await render();
    expect(container.querySelector('#cc-skillhub-added-search-input')).toBeNull();
    await act(async () => container.querySelector('.cc-skillhub-search-toggle').click());
    const input = container.querySelector('#cc-skillhub-added-search-input');
    expect(document.activeElement).toBe(input);
    await act(async () => input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
    expect(container.querySelector('.cc-skillhub-search-toggle')).toBeTruthy();
    await act(async () => container.querySelector('.cc-skillhub-search-toggle').click());
    await act(async () => container.querySelector('.cc-skillhub-sort-trigger').focus());
    expect(container.querySelector('.cc-skillhub-search-toggle')).toBeTruthy();
    expect(container.querySelector('#cc-skillhub-added-search-input')).toBeNull();
  });

  it('prioritizes update cards in both views while respecting sorting, category and search', async () => {
    const entries = [
      { skillId: 'owner/alpha', displayName: 'Alpha', addedAt: '2026-11-12', primaryCategory: 'office' },
      { skillId: 'owner/zulu', displayName: 'Zulu', primaryCategory: 'development' },
      { skillId: 'owner/beta', displayName: 'Beta', addedAt: '2026-10-01', primaryCategory: 'development' },
    ];
    const references = entries.map(skill => ({ ...skill, source: 'skillhub', version: '1.0.0', contentHash: 'a'.repeat(64) }));
    const catalogue = entries.map(skill => ({ ...skill, publishedAt: skill.addedAt, latestVersion: skill.displayName === 'Alpha' ? '1.0.0' : '2.0.0', contentHash: 'a'.repeat(64) }));
    const present = async (activeSection, search = '') => {
      await act(async () => root.render(<SkillHubContent
        activeSection={activeSection} categoryOwnerUID='7' selectedBotUID='42'
        agentOptions={[]} definition={{ skills: references }} definitionReady
        librarySkills={catalogue} installedByID={new Map(references.map(skill => [skill.skillId, skill]))}
        addedSkillPresentationByID={new Map(catalogue.map(skill => [skill.skillId, { label: skill.displayName, details: skill }]))}
        addedSkillQuery={search} query={search} onChangeSection={() => {}}
        onAddedSkillQuery={() => {}} onQueryChange={() => {}}
      />));
    };
    for (const activeSection of ['added', 'catalogue']) {
      await present(activeSection);
      if (activeSection === 'catalogue') await selectAll();
      expect(names()).toEqual(['Beta', 'Zulu', 'Alpha']);
      expect(container.querySelectorAll('.cc-skillhub-card.is-updatable')).toHaveLength(2);
      expect(container.querySelector('.cc-skillhub-updates-filter')).toBeNull();
      await choose('名称 Z → A');
      expect(names()).toEqual(['Zulu', 'Beta', 'Alpha']);
      await act(async () => [...container.querySelectorAll('.cc-skillhub-category-filter button')].find(button => button.textContent === '办公协作').click());
      expect(names()).toEqual(['Alpha']);
      await act(async () => [...container.querySelectorAll('.cc-skillhub-category-filter button')].find(button => button.textContent === '全部').click());
      await present(activeSection, 'Alpha');
      expect(names()).toEqual(['Alpha']);
    }
  });
});
