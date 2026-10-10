import React, { useEffect, useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowLeft, Bot, Check, Clipboard, FolderOpen, Globe, Info, Plus, Trash2,
  Activity, AppWindow, AudioLines, BookOpenCheck, BriefcaseBusiness, ChartNoAxesCombined,
  ClipboardCheck, Code2, FileText, FlaskConical, Globe2, GraduationCap, Image as ImageIcon,
  Landmark, Lock, Package, PackageMinus, PanelsTopLeft, Palette, PenLine, Save, ShieldCheck, Sparkles,
  RefreshCw, Search, Share2, Users,
  Wrench, X,
} from 'lucide-react';
import CustomSelect from '../widgets/custom-select';
import useDialogBehavior from '../utils/use-dialog-behavior';
import { useFeedback } from '../components/feedback-system';
import { mergeMarketplaceLibrary, useMarketplace, useMarketplaceCatalogue } from './skillhub-marketplace-state';
import { MarketplaceIntroduction } from './skillhub-marketplace-intro';
import { marketplaceApi } from '../skillhub-marketplace-api';
import { categoryKey, readCategoryDrafts, inferSkillCategory, localizedSkillText, skillUploadedTimestamp, SkillCategoryField, SkillCategoryFilters } from './skillhub-categories';
import { skillSourceQuery } from './skillhub-presentation';
import { skillIconKey } from './skillhub-icons';
import {
  formatSkillHubPublisher,
  formatSkillHubVersion,
  isSkillHubUpdateAvailable,
} from '../utils/skillhub-entry';

function normalizeSkillSearchValue(value) {
  return String(value || '')
    .trim()
    .toLocaleLowerCase()
    .replace(/\\/g, '/')
    .replace(/\/{2,}/g, '/');
}

function formatUnavailableSkillHint(skillIds = []) {
  const ids = [...new Set((Array.isArray(skillIds) ? skillIds : [])
    .map((skillId) => String(skillId || '').trim())
    .filter(Boolean))];
  if (ids.length === 0) return '部分 Skill 暂时无法从 SkillHub 确认更新状态';
  const shown = ids.slice(0, 3).join('、');
  const suffix = ids.length > 3 ? ` 等 ${ids.length} 个 Skill` : '';
  return `部分 Skill 暂时无法从 SkillHub 确认更新状态：${shown}${suffix}`;
}

export default function SkillHubContent(props) {
  const { navigate } = useMarketplace();
  const categoryOwner = props.categoryOwnerUID || 'guest';
  const [category, setCategory] = useState('');
  const [languagePreference, setLanguagePreference] = useState(() => ({ owner: categoryOwner, value: readSkillLanguage(categoryOwner) }));
  const language = languagePreference.owner === categoryOwner ? languagePreference.value : readSkillLanguage(categoryOwner);
  const changeLanguage = value => {
    setLanguagePreference({ owner: categoryOwner, value });
    try { localStorage.setItem(`catsco.skillhub.language.${categoryOwner}`, value); } catch {}
  };
  const [categoryDrafts, setCategoryDrafts] = useState(() => ({ owner: categoryOwner, values: readCategoryDrafts(categoryOwner) }));
  const drafts = categoryDrafts.owner === categoryOwner ? categoryDrafts.values : readCategoryDrafts(categoryOwner);
  const categoryOf = skill => drafts[categoryKey(skill)] || inferSkillCategory(skill);
  const changeCategory = (skill, value) => {
    const values = { ...drafts, [categoryKey(skill)]: value };
    setCategoryDrafts({ owner: categoryOwner, values });
    try { localStorage.setItem(`catsco.skillhub.categories.${categoryOwner}`, JSON.stringify(values)); } catch {}
  };
  const categorizedProps = { ...props, language, selectedCategory: category, categoryOf, onSkillCategoryChange: changeCategory };
  const guardedProps = {
    ...props,
    onSelectAgent: (value) => navigate ? navigate(() => props.onSelectAgent(value)) : props.onSelectAgent(value),
    onChangeSection: (value) => navigate ? navigate(() => props.onChangeSection(value)) : props.onChangeSection(value),
  };
  const {
    actionNotice, activeSection, definition, definitionError, isLocalEnabled, runtimeRouteError,
    isReadOnly, loadingDefinition, onChangeSection, onToggleUpdates, saving, selectedAgentName,
    selectedAgentRelation, selectedUpdateCount, selectedUpdateStatus, skillAction, updatesOnly,
  } = props;
  // A friend Bot is metadata-only. Keep this guard in the rendering boundary
  // as well as in the Agent-switch handler so stale UI state can never expose
  // the owner's Runtime workspace actions, even for a single render.
  const visibleSection = activeSection === 'custom' && !isLocalEnabled
    ? 'added'
    : activeSection;
  return (
    <main className='cc-skillhub-page'>
      <div className='cc-skillhub-shell'>
        <header className='cc-skillhub-header'>
          <div className='cc-skillhub-title-block'>
            <h1>Agent 能力</h1>
            <p>为 Agent 添加和管理可用能力。</p>
          </div>
          {visibleSection !== 'catalogue' && <AgentContext {...guardedProps} />}
        </header>
        {definitionError && <div className='cc-skillhub-alert error' role='alert'>{definitionError}</div>}
        {runtimeRouteError && <div className='cc-skillhub-alert error' role='alert'>{runtimeRouteError}</div>}
        {actionNotice && <div className='cc-skillhub-alert success' role='status'>{actionNotice}</div>}
        {visibleSection === 'custom' ? <CustomSkills {...categorizedProps} /> : (
          <>
            <SkillNavigation
              {...guardedProps}
              activeSection={visibleSection}
              addedCount={definition.skills.length}
              selectedUpdateCount={selectedUpdateCount}
              selectedUpdateStatus={selectedUpdateStatus}
              updatesOnly={updatesOnly}
              onToggleUpdates={onToggleUpdates}
              language={language}
              onLanguageChange={changeLanguage}
            />
            <SkillCategoryFilters value={category} onChange={setCategory} showOther={category === 'other' || [...(props.librarySkills || []), ...definition.skills].some(skill => categoryOf(skill) === 'other')} />
            {(loadingDefinition || saving) && (
              <div className='cc-skillhub-progress' role='status'>
                <RefreshCw className='is-spinning' size={14} aria-hidden='true' />
                {loadingDefinition ? `正在更新${selectedAgentName ? ` Agent“${selectedAgentName}”` : '当前 Agent'}的能力…` : skillAction?.type === 'remove' ? '正在移除能力…' : skillAction?.update ? '正在更新能力…' : '正在添加能力…'}
              </div>
            )}
            {visibleSection === 'added' ? <AddedSkills key={props.selectedBotUID} {...categorizedProps} /> : <Catalogue key={props.selectedBotUID} {...categorizedProps} />}
          </>
        )}
      </div>
    </main>
  );
}

function readSkillLanguage(uid) {
  try { return localStorage.getItem(`catsco.skillhub.language.${uid}`) === 'zh-CN' ? 'zh-CN' : 'source'; }
  catch { return 'source'; }
}

function AgentContext({
  agentOptions, loadingBots, onSelectAgent, saving, selectedBotUID, sharingSkill, syncingWorkspace,
}) {
  const disabled = loadingBots || agentOptions.length === 0 || Boolean(sharingSkill) || saving || syncingWorkspace;
  return (
    <div className='cc-skillhub-agent-context'>
      <label className='cc-skillhub-bot-picker'>
        <span className='cc-skillhub-agent-label'><Bot size={15} aria-hidden='true' /> 当前 Agent</span>
        <AgentSelect
          agents={agentOptions}
          disabled={disabled}
          onChange={onSelectAgent}
          value={selectedBotUID}
        />
      </label>
    </div>
  );
}

function AgentSelect({ agents, disabled, onChange, value }) {
  return (
    <span className='cc-skillhub-select-wrap'>
      <select
        className='cc-skillhub-native-select cc-skillhub-agent-native-select'
        value={value}
        disabled={disabled}
        tabIndex={-1}
        aria-hidden='true'
        onChange={(event) => onChange(event.target.value)}
      >
        {agents.length === 0 && <option value=''>暂无自己拥有的 Agent</option>}
        {agents.map((agent) => <option key={agent.value} value={agent.value}>{agent.label}</option>)}
      </select>
      <CustomSelect
        ariaLabel='当前 Agent'
        className='cc-skillhub-agent-select'
        density='comfortable'
        disabled={disabled}
        listboxAriaLabel='Agent 列表'
        menuClassName='cc-skillhub-agent-options'
        selectedLabelTitle={agents.find((agent) => agent.value === value)?.label}
        triggerClassName='cc-skillhub-agent-select-trigger'
        value={value}
        onValueChange={onChange}
      >
        {agents.length === 0 && <option value=''>暂无自己拥有的 Agent</option>}
        {agents.map((agent) => (
          <option
            key={agent.value}
            value={agent.value}
            data-title={agent.label}
          >
            {agent.label}
          </option>
        ))}
      </CustomSelect>
    </span>
  );
}

function SkillNavigation(props) {
  const { activeSection, addedCount, isLocalEnabled, isReadOnly, onChangeSection, onToggleUpdates, selectedUnavailableSkillIds = [], selectedUpdateCount = 0, selectedUpdateStatus = '', updatesOnly = false, onRefreshDefinition, selectedBotUID, loadingDefinition, saving, sharingSkill } = props;
  return (
    <nav className='cc-skillhub-navigation' aria-label='Agent 能力视图'>
      <div className='cc-skillhub-tabs-actions'>
        <div className='cc-skillhub-tabs' role='tablist' aria-label='能力管理'>
          <button type='button' id='skillhub-added-tab' role='tab' aria-selected={activeSection === 'added'} aria-controls='skillhub-added-panel' className={activeSection === 'added' ? 'active' : ''} onClick={() => onChangeSection('added')}>
            Agent 能力总览 <span>{addedCount}</span>
          </button>
          <button type='button' id='skillhub-catalogue-tab' role='tab' aria-selected={activeSection === 'catalogue'} aria-controls='skillhub-catalogue-panel' className={activeSection === 'catalogue' ? 'active' : ''} onClick={() => onChangeSection('catalogue')}>
            能力库
          </button>
        </div>
        {!isReadOnly && selectedUpdateCount > 0 && (
          <button
            type='button'
            className={`cc-skillhub-updates-filter${updatesOnly ? ' active' : ''}`}
            aria-pressed={updatesOnly}
            onClick={onToggleUpdates}
          >
            可更新 <span>{selectedUpdateCount}</span>
          </button>
        )}
        {!isReadOnly && selectedUpdateStatus === 'partial' && (
          <span className='cc-skillhub-update-uncertain-note' role='status' title={formatUnavailableSkillHint(selectedUnavailableSkillIds)}>
            部分 Skill 无法确认更新
          </span>
        )}
      </div>
      <div className='cc-skillhub-navigation-tools'>
        <div className='cc-skillhub-language' role='group' aria-label='能力介绍语言'>
          <button type='button' aria-pressed={props.language === 'source'} onClick={() => props.onLanguageChange('source')}>原文</button>
          <button type='button' aria-pressed={props.language === 'zh-CN'} onClick={() => props.onLanguageChange('zh-CN')}>中文</button>
        </div>
        <SkillSearch {...props} />
        {activeSection === 'added' && <button type='button' className='icon-button' aria-label='刷新当前 Agent 的能力' title='刷新能力' onClick={onRefreshDefinition} disabled={!selectedBotUID || loadingDefinition || saving || Boolean(sharingSkill)}>
          <RefreshCw className={loadingDefinition ? 'is-spinning' : ''} size={16} aria-hidden='true' />
        </button>}
      </div>
      {isLocalEnabled && (
        <button type='button' className='cc-skillhub-custom-entry' onClick={() => onChangeSection('custom')}>
          <Wrench size={14} aria-hidden='true' /> 运行工作区（真实目录）
        </button>
      )}
    </nav>
  );
}

function SkillSearch({ activeSection, addedSkillQuery, onAddedSkillQuery, query, onQueryChange, onSearch, loadingCatalogue }) {
  const added = activeSection === 'added';
  const value = added ? addedSkillQuery || '' : query || '';
  const change = added ? onAddedSkillQuery : onQueryChange;
  const id = added ? 'cc-skillhub-added-search-input' : 'cc-skillhub-search-input';
  return (
    <form className={added ? 'cc-skillhub-added-search' : 'cc-skillhub-search'} role='search' onSubmit={(event) => { event.preventDefault(); if (!added) onSearch(value); }}>
      <Search size={16} aria-hidden='true' />
      <label className='cc-visually-hidden' htmlFor={id}>{added ? '搜索当前 Agent 能力' : '搜索能力名称'}</label>
      <div className='cc-skillhub-search-field'>
        <input id={id} name='skillhub-query' type='search' autoComplete='off' value={value} onChange={(event) => change(event.target.value)} placeholder={added ? '搜索当前 Agent 能力…' : '搜索能力名称…'} />
        {value && <button type='button' className='cc-skillhub-search-clear' aria-label={added ? '清除当前 Agent 能力搜索' : '清除搜索内容'} title='清除' onClick={() => change('')}><X size={14} aria-hidden='true' /></button>}
      </div>
      {!added && <button type='submit' className='cc-skillhub-search-submit' disabled={loadingCatalogue} aria-label='搜索能力' title='搜索'><Search size={16} aria-hidden='true' /></button>}
    </form>
  );
}

function AddedSkills(props) {
  const {
    catalogueByID, definition, definitionReady, loadingDefinition, onChangeSection,
    saving, selectedBotUID, sharingSkill, isReadOnly, addedSkillQuery, updatesOnly,
  } = props;
  const formalSkills = definition.skills.filter((skill) => !skill.localOnly);
  const localOnlySkills = definition.skills.filter((skill) => skill.localOnly);
  const normalizedQuery = normalizeSkillSearchValue(addedSkillQuery);
  const matches = (skill) => {
    if (props.selectedCategory && props.categoryOf({ ...skill, ...props.addedSkillPresentationByID.get(skill.skillId)?.details }) !== props.selectedCategory) return false;
    if (!normalizedQuery) return true;
    const presentation = props.addedSkillPresentationByID.get(skill.skillId);
    return [
      localizedSkillText({ ...skill, ...presentation?.details }, 'zh-CN').name,
      presentation?.label,
      skill?.displayName,
      skill?.skillId,
      skill?.localName,
      skill?.localDetails?.name,
      skill?.localDetails?.relativePath,
      skill?.relativePath,
      skill?.path,
    ].some((value) => normalizeSkillSearchValue(value).includes(normalizedQuery));
  };
  const isUpdateable = (skill) => !skill.localOnly && isSkillHubUpdateAvailable(
    skill,
    props.addedSkillPresentationByID.get(skill.skillId)?.details,
  );
  // Keep the server order for legacy references with no recorded addition time.
  const visibleSkills = [...definition.skills]
    .filter(matches).filter(skill => !updatesOnly || isUpdateable(skill))
    .sort((left, right) => skillAddedTimestamp(right) - skillAddedTimestamp(left));
  const visibleSkillCount = visibleSkills.length;
  const totalSkillCount = formalSkills.length + localOnlySkills.length;
  return (
    <section id='skillhub-added-panel' className='cc-skillhub-surface cc-skillhub-added' role='tabpanel' aria-labelledby='skillhub-added-tab'>
      {selectedBotUID && !loadingDefinition && definition.skills.length > 0 && (normalizedQuery || updatesOnly) && (
        <span className='cc-visually-hidden cc-skillhub-added-search-count' role='status'>{visibleSkillCount} / {totalSkillCount}</span>
      )}
      {!selectedBotUID ? (
        <EmptyState icon={<Bot size={21} />} title='请先选择 Agent' copy='选择后即可查看它已经具备的能力。' />
      ) : loadingDefinition ? (
        <EmptyState icon={<RefreshCw className='is-spinning' size={20} />} title='正在读取 Agent 能力' status />
      ) : definition.skills.length === 0 ? (
        <div className='cc-skillhub-empty cc-skillhub-empty-added'>
          <Package size={22} aria-hidden='true' /><strong>{isReadOnly ? '暂无已同步能力' : '还没有添加能力'}</strong>
          <span>{isReadOnly ? '该 Agent 尚未把能力同步到 BotDefinition，其运行环境中独有的本地 Skill 也不会在这里显示。' : '前往能力库，为当前 Agent 选择第一项能力。'}</span>
          {!isReadOnly && <button type='button' className='primary' onClick={() => onChangeSection('catalogue')}>浏览能力库</button>}
        </div>
      ) : (
        <div className='cc-skillhub-added-groups'>
          {visibleSkills.length > 0 && (
            <AbilityGroup
              label={isReadOnly ? '已同步能力' : '已配置能力'}
              skills={visibleSkills}
              {...props}
            />
          )}
          {visibleSkillCount === 0 && (
            <EmptyState
              icon={<Search size={21} />}
              title={updatesOnly && !normalizedQuery ? '当前没有可更新的能力' : '没有找到匹配的能力'}
              copy={updatesOnly && !normalizedQuery ? '当前 Agent 已配置的 Skill 都是最新版本，或暂时无法确认更新状态。' : '试试能力名称、SkillHub ID 或运行目录名。'}
            />
          )}
        </div>
      )}
    </section>
  );
}

function AbilityGroup({ label, skills, ...props }) {
  return (
    <section className='cc-skillhub-ability-group' aria-label={label}>
      <div className='cc-skillhub-added-list'>
        {skills.map((skill) => <AddedSkillItem key={`${props.selectedBotUID}:${skill.skillId}`} skill={skill} {...props} />)}
      </div>
    </section>
  );
}

function skillAddedTimestamp(skill) {
  const value = skill.addedAt || skill.added_at || skill.installedAt || skill.installed_at;
  const timestamp = typeof value === 'number' ? value : Date.parse(String(value || ''));
  return Number.isFinite(timestamp) ? timestamp : 0;
}

function AddedSkillItem({ addedSkillPresentationByID, categoryOf, definitionReady, isReadOnly, language, onLoadSkillHistory, onRemoveSkill, onUpdateSkill, saving, selectedBotUID, sharingSkill, skill, skillAction }) {
  const presentation = addedSkillPresentationByID.get(skill.skillId);
  const { description: originalDescription, details, label: originalLabel, localDetails, privateReference } = presentation;
  const metadata = { ...skill, ...details };
  const localized = localizedSkillText({ ...metadata, displayName: originalLabel, description: originalDescription }, language);
  const label = localized.name;
  const removing = skillAction?.type === 'remove' && skillAction.skillId === skill.skillId;
  const updating = skillAction?.skillId === skill.skillId && (skillAction?.type === 'update' || skillAction?.update);
  const actionsDisabled = saving || Boolean(sharingSkill) || !definitionReady || Boolean(skillAction);
  const localVersionMismatch = !skill.localOnly && Boolean(skill.local) && !localDetails;
  const versionLabel = formatAddedSkillVersion(skill, privateReference);
  const updatable = !isReadOnly && !skill.localOnly && isSkillHubUpdateAvailable(skill, details);
  const authorLabel = privateReference ? `最近变更：${skill.lastChangedBy || '修改者未记录'}` : formatSkillHubPublisher(details || skill);
  const uploadedAt = skillUploadedTimestamp(metadata);
  const removeLabel = skill.localOnly ? '删除本地能力'
    : skill.local ? localVersionMismatch ? '从 Agent 移除并删除本地旧版本' : '从 Agent 移除并删除本地' : '从 Agent 移除';
  const triggerRef = useRef(null);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const closeDetails = () => {
    setDetailsOpen(false);
    triggerRef.current?.focus({ preventScroll: true });
  };

  return (
    <article className='cc-skillhub-card cc-skillhub-overview-card cc-skillhub-added-item'>
      <button ref={triggerRef} type='button' className='cc-skillhub-card-open' aria-label={`查看 ${label} 详情`} aria-haspopup='dialog' onClick={() => setDetailsOpen(true)}>
        <span className='cc-visually-hidden'>查看 {label} 详情</span>
      </button>
      <div className='cc-skillhub-card-main'>
        <SkillIllustration skill={metadata} category={categoryOf(metadata)} />
        <div className='cc-skillhub-card-copy'>
          <div className='cc-skillhub-card-title cc-skillhub-added-title'><h3 title={label}>{label}</h3></div>
          <p>{localized.description}</p>
        </div>
        <div className='cc-skillhub-card-actions cc-skillhub-added-actions'>
          {updatable && <button type='button' className='cc-skillhub-update-action'
            aria-label={`更新 ${label} 到 ${formatSkillHubVersion(details?.latestVersion) || '最新版本'}`}
            title={`更新到 ${formatSkillHubVersion(details?.latestVersion) || '最新版本'}`}
            disabled={actionsDisabled} onClick={() => onUpdateSkill?.(skill.skillId)}>
            <RefreshCw className={updating ? 'is-spinning' : ''} size={18} aria-hidden='true' />
            <span className='cc-visually-hidden'>{updating ? '更新中…' : `更新到 ${formatSkillHubVersion(details?.latestVersion) || '最新版本'}`}</span>
          </button>}
          {!isReadOnly && <button type='button' className='cc-skillhub-delete-action' aria-label={`删除 ${label}`}
            title={removeLabel} disabled={actionsDisabled} onClick={() => onRemoveSkill(skill.skillId)}>
            {removing ? <RefreshCw className='is-spinning' size={18} aria-hidden='true' /> : <Trash2 size={18} aria-hidden='true' />}
            <span className='cc-visually-hidden'>{removing ? '删除中…' : removeLabel}</span>
          </button>}
        </div>
      </div>
      <div className='cc-skillhub-card-footer'>
        <div className='cc-skillhub-card-source'>
          <span className='cc-skillhub-card-publisher'>{skill.localOnly ? '尚未发布 · 当前运行工作区'
            : privateReference ? `${authorLabel} · Bot 私有 · 仅当前 Agent 可用` : `发布人：${authorLabel}`}</span>
          <time dateTime={uploadedAt ? new Date(uploadedAt).toISOString() : undefined}>{uploadedAt ? formatCataloguePublishedTime(uploadedAt) : '上传时间待确认'}</time>
          <span className='cc-skillhub-card-version'>版本：{versionLabel}</span>
        </div>
      </div>
      {detailsOpen && createPortal(
        <SkillDetailsDialog readOnly={isReadOnly} details={{ ...details, description: localized.description }}
          historyBotUID={selectedBotUID} label={label} localDetails={localDetails} onClose={closeDetails}
          onLoadSkillHistory={onLoadSkillHistory} privateReference={privateReference} skill={skill} />,
        document.body,
      )}
    </article>
  );
}

function SkillDetailsDialog({ cataloguePreview = false, readOnly = false, details, historyBotUID, label, localDetails, onClose, onLoadSkillHistory, privateReference, skill }) {
  const dialogRef = useRef(null);
  const closeButtonRef = useRef(null);
  const titleId = useId();
  const descriptionId = useId();
  const localOnly = Boolean(skill?.localOnly);
  const { enabled: marketEnabled, visibilityEnabled, visibilityWritesEnabled } = useMarketplace();
  const feedback = useFeedback();
  const [editorState, setEditorState] = useState({ dirty: false, busy: false });
  const requestClose = async () => {
    if (editorState.busy) return;
    if (editorState.dirty && !await feedback.confirm({ title: '关闭介绍编辑？', message: '尚未保存的修改将丢失。', confirmLabel: '放弃修改并关闭' })) return;
    onClose();
  };
  const [history, setHistory] = useState([]);
  const [historyCursor, setHistoryCursor] = useState(0);
  const [historyError, setHistoryError] = useState('');
  const [historyLoading, setHistoryLoading] = useState(!localOnly);
  const description = localOnly && !cataloguePreview
    ? '该能力当前存在于此 Agent 的 XiaoBa 运行工作区，可供该运行时使用；尚未发布到 SkillHub，也未写入 Agent 的云端能力配置。'
    : details?.description || skill?.description || localDetails?.description
      || (cataloguePreview ? '这个能力暂时没有补充说明。' : '此能力已写入当前 Agent 的配置。');

  useDialogBehavior(dialogRef, { onClose: requestClose, initialFocusRef: closeButtonRef });

  const loadHistory = async ({ append = false, beforeRevisionNumber = 0 } = {}) => {
    if (cataloguePreview || localOnly || typeof onLoadSkillHistory !== 'function') return;
    setHistoryLoading(true);
    setHistoryError('');
    try {
      const result = await onLoadSkillHistory(historyBotUID, skill, { beforeRevisionNumber });
      setHistory((current) => append ? [...current, ...(result?.versions || [])] : (result?.versions || []));
      setHistoryCursor(Number(result?.nextBeforeRevisionNumber || 0));
    } catch (error) {
      setHistoryError(error?.message || '暂时无法读取版本历史。');
    } finally {
      setHistoryLoading(false);
    }
  };

  useEffect(() => {
    let active = true;
    if (cataloguePreview || localOnly || typeof onLoadSkillHistory !== 'function') {
      setHistoryLoading(false);
      return () => { active = false; };
    }
    setHistoryLoading(true);
    setHistoryError('');
    onLoadSkillHistory(historyBotUID, skill, { beforeRevisionNumber: 0 }).then((result) => {
      if (!active) return;
      setHistory(result?.versions || []);
      setHistoryCursor(Number(result?.nextBeforeRevisionNumber || 0));
    }).catch((error) => {
      if (active) setHistoryError(error?.message || '暂时无法读取版本历史。');
    }).finally(() => {
      if (active) setHistoryLoading(false);
    });
    return () => { active = false; };
  }, [cataloguePreview, historyBotUID, localOnly, onLoadSkillHistory, skill]);

  return (
    <div
      className='cc-skillhub-detail-overlay'
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) requestClose();
      }}
    >
      <section
        ref={dialogRef}
        tabIndex={-1}
        className={`cc-skillhub-detail-dialog${marketEnabled && !localOnly && !privateReference ? ' cc-market-detail' : ''}`}
        role='dialog'
        aria-modal='true'
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
      >
        <header className='cc-skillhub-detail-header'>
          <span className='cc-skillhub-detail-icon' aria-hidden='true'><Package size={19} /></span>
          <div>
            <span>{localOnly ? '本地能力' : privateReference ? 'Agent 私有能力' : 'SkillHub 能力'}</span>
            <h2 id={titleId}>{label}</h2>
          </div>
          <button ref={closeButtonRef} type='button' className='icon-button' aria-label='关闭能力详情' disabled={editorState.busy} onClick={requestClose}>
            <X size={17} aria-hidden='true' />
          </button>
        </header>
        <p id={descriptionId} className='cc-skillhub-detail-description'>{description}</p>
        {!localOnly && !privateReference && skill?.skillId && skill?.version && <MarketplaceIntroduction
          key={`${skill.skillId}:${skill.version}:${readOnly}`}
          target={{ skillId: skill.skillId, version: skill.version }} readOnly={readOnly} onEditorState={setEditorState}
        />}
        <dl className='cc-skillhub-detail-meta'>
          <div><dt>{localOnly ? '本地能力名' : privateReference ? '能力引用' : 'SkillHub ID'}</dt><dd><code translate='no'>{localOnly ? skill.localName || label : skill.skillId}</code></dd></div>
          <div><dt>{localOnly ? '发布状态' : cataloguePreview ? '最新版本' : '当前版本'}</dt><dd>{localOnly ? '尚未发布' : formatAddedSkillVersion(skill, privateReference)}</dd></div>
          <div><dt>{localOnly ? '存放范围' : privateReference ? '可见范围' : '发布者'}</dt><dd>{localOnly ? '当前运行工作区' : privateReference ? '仅当前 Agent' : formatSkillHubPublisher(details || skill, 'SkillHub')}</dd></div>
        </dl>
        {!localOnly && !privateReference && skill?.skillId && <SkillVisibilityControl
          skillId={skill.skillId}
          enabled={visibilityEnabled}
          writesEnabled={visibilityWritesEnabled}
        />}
        {!cataloguePreview && <section className='cc-skillhub-history' aria-labelledby={`${titleId}-history`}>
          <div className='cc-skillhub-history-heading'>
            <div>
              <h3 id={`${titleId}-history`}>版本历史</h3>
              <p>{localOnly ? '本地能力发布后才会生成云端版本历史。' : '版本历史仅供查看，暂不支持回退或切换版本。'}</p>
            </div>
            {!localOnly && historyError && <button type='button' onClick={() => loadHistory()}>重试</button>}
          </div>
          {localOnly ? (
            <p className='cc-skillhub-history-empty'>尚无云端版本记录。</p>
          ) : historyLoading && history.length === 0 ? (
            <p className='cc-skillhub-history-empty' role='status'><RefreshCw className='is-spinning' size={13} aria-hidden='true' /> 正在读取版本历史…</p>
          ) : historyError && history.length === 0 ? (
            <p className='cc-skillhub-history-error' role='alert'>{historyError}</p>
          ) : history.length === 0 ? (
            <p className='cc-skillhub-history-empty'>SkillHub 暂无可展示的历史版本。</p>
          ) : (
            <div className='cc-skillhub-history-list'>
              {history.map((version) => (
                <article className='cc-skillhub-history-item' key={`${version.version}-${version.revisionNumber || 0}`}>
                  <div className='cc-skillhub-history-title'>
                    <strong>{formatHistoryVersion(version)}</strong>
                    {version.current && <span>当前使用</span>}
                  </div>
                  <p>{formatHistoryActor(version, privateReference)}{version.lastChangedAt ? ` · ${formatHistoryTime(version.lastChangedAt)}` : ''}</p>
                  {privateReference && <code title={version.version} translate='no'>{shortPrivateVersion(version.version)}</code>}
                </article>
              ))}
            </div>
          )}
          {historyError && history.length > 0 && <p className='cc-skillhub-history-error' role='alert'>{historyError}</p>}
          {historyCursor > 0 && <button
            type='button'
            className='cc-skillhub-history-more'
            disabled={historyLoading}
            onClick={() => loadHistory({ append: true, beforeRevisionNumber: historyCursor })}
          >{historyLoading ? '读取中…' : '加载更早版本'}</button>}
        </section>}
        <div className='cc-skillhub-detail-footer'>
          <button type='button' disabled={editorState.busy} onClick={requestClose}>完成</button>
        </div>
      </section>
    </div>
  );
}

const VISIBILITY_LABELS = {
  public: '公开',
  shared: '指定共享',
  private: '私有',
};

function normalizeSharedUIDs(value) {
  return [...new Set(String(value || '').split(/[\s,，、;；]+/).map((item) => item.trim()).filter((item) => /^[1-9]\d*$/.test(item)))];
}

export function SkillVisibilityControl({ skillId, enabled, writesEnabled }) {
  const [state, setState] = useState({ loading: Boolean(enabled), available: false, error: '' });
  const [scope, setScope] = useState('public');
  const [uids, setUIDs] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');

  useEffect(() => {
    if (!enabled || !skillId) {
      setState({ loading: false, available: false, error: '' });
      return undefined;
    }
    const controller = new AbortController();
    setState({ loading: true, available: false, error: '' });
    marketplaceApi.skillVisibility(skillId, controller.signal).then((result) => {
      if (controller.signal.aborted) return;
      const visibility = result?.visibility;
      if (!visibility || !VISIBILITY_LABELS[visibility.visibilityScope] || !Number.isSafeInteger(Number(visibility.visibilityRevision))) throw new Error('可见范围数据格式暂不支持');
      setScope(visibility.visibilityScope);
      setUIDs(Array.isArray(visibility.sharedUserUids) ? visibility.sharedUserUids.join(', ') : '');
      setState({ loading: false, available: true, visibility, error: '' });
    }).catch((error) => {
      if (controller.signal.aborted) return;
      // A 403/404 is the normal result for a Skill owned by another account.
      // Do not disclose ownership or existence in the detail dialog.
      if (error?.status === 403 || error?.status === 404) {
        setState({ loading: false, available: false, error: '' });
      } else {
        setState({ loading: false, available: false, error: '暂时无法读取可见范围' });
      }
    });
    return () => controller.abort();
  }, [enabled, skillId]);

  if (!enabled || state.loading || !state.available) return state.error ? <p className='cc-skillhub-visibility-error' role='status'>{state.error}</p> : null;

  const save = async () => {
    const sharedUserUids = normalizeSharedUIDs(uids);
    if (scope === 'shared' && sharedUserUids.length === 0) {
      setNotice('指定共享至少需要填写一个正整数 CatsCo UID。');
      return;
    }
    setBusy(true);
    setNotice('');
    try {
      const result = await marketplaceApi.updateSkillVisibility({
        skillId,
        visibilityScope: scope,
        sharedUserUids: scope === 'shared' ? sharedUserUids : [],
        expectedRevision: Number(state.visibility.visibilityRevision),
      });
      if (result?.visibility) {
        setState((current) => ({ ...current, visibility: result.visibility }));
        setUIDs(Array.isArray(result.visibility.sharedUserUids) ? result.visibility.sharedUserUids.join(', ') : '');
        setNotice('可见范围已保存。新版本会自动继承此设置。');
      }
    } catch (error) {
      setNotice(error?.status === 409
        ? '该 Skill 的可见范围已被其他操作更新，请关闭详情后重新打开再保存。'
        : error?.status === 403
          ? '当前账号已没有修改此 Skill 可见范围的权限。'
          : '保存失败，请稍后重试。');
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className='cc-skillhub-visibility' aria-labelledby={`skill-visibility-${skillId}`}>
      <div className='cc-skillhub-visibility-heading'>
        <div><h3 id={`skill-visibility-${skillId}`}><Globe size={14} aria-hidden='true' /> 可见范围</h3><p>由 Skill Owner 或管理员管理；新版本会自动继承。</p></div>
        <span className='cc-skillhub-visibility-badge'>{VISIBILITY_LABELS[state.visibility.visibilityScope]}</span>
      </div>
      {writesEnabled ? (
        <div className='cc-skillhub-visibility-form'>
          <label><span>范围</span><select value={scope} disabled={busy} onChange={(event) => setScope(event.target.value)}>
            <option value='public'>公开：所有 SkillHub 用户可见</option>
            <option value='shared'>指定共享：仅指定 CatsCo UID 可见</option>
            <option value='private'>私有：仅 Owner 和管理员可见</option>
          </select></label>
          {scope === 'shared' && <label><span><Users size={13} aria-hidden='true' /> CatsCo UID</span><input value={uids} disabled={busy} onChange={(event) => setUIDs(event.target.value)} placeholder='例如 951, 42001' inputMode='numeric' /></label>}
          <button type='button' className='primary' disabled={busy} onClick={save}><Save size={14} aria-hidden='true' /> {busy ? '保存中…' : '保存可见范围'}</button>
        </div>
      ) : <p className='cc-skillhub-visibility-readonly'><Lock size={13} aria-hidden='true' /> 当前环境只开放查看，修改入口尚未启用。</p>}
      {notice && <p className='cc-skillhub-visibility-notice' role='status'>{notice}</p>}
    </section>
  );
}

function formatHistoryVersion(version) {
  if (version?.privateReference && version.revisionNumber > 0) return `第 ${version.revisionNumber} 版`;
  const value = String(version?.version || '').trim();
  if (!value) return '版本待确认';
  return value.startsWith('v') ? value : `v${value}`;
}

function formatHistoryActor(version, privateReference) {
  if (!privateReference) return formatSkillHubPublisher(version);
  if (version?.author) return version.author;
  return version?.changeSource === 'runtime_backup' ? 'Bot 自动同步' : '修改者未记录';
}

function formatHistoryTime(value) {
  const timestamp = Date.parse(String(value || ''));
  if (!Number.isFinite(timestamp)) return '时间待确认';
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(new Date(timestamp));
}

function shortPrivateVersion(value) {
  const version = String(value || '').trim();
  return version.length > 18 ? `${version.slice(0, 10)}…${version.slice(-6)}` : version;
}

function Catalogue(props) {
  const {
    catalogueError, libraryLocalError, librarySkills: legacySkills, loadingCatalogue,
    loadingLibraryLocalSkills,
  } = props;
  const market = useMarketplaceCatalogue(skillSourceQuery(props.query || ''));
  const hasMarketPage = market.enabled && Boolean(market.state?.skills);
  // Render the legacy catalogue immediately while the optional marketplace
  // request is warming up. The cloud response replaces it in place once it
  // arrives, so opening this tab never waits on a secondary capability check.
  const availableSkills = hasMarketPage
    ? mergeMarketplaceLibrary(legacySkills, market.state.skills, market.category)
    : legacySkills;
  const search = normalizeSkillSearchValue(props.query);
  const librarySkills = availableSkills
    .filter(skill => !props.selectedCategory || props.categoryOf(skill) === props.selectedCategory)
    .filter(skill => !search || [skill.displayName, skill.skillId, localizedSkillText(skill, 'zh-CN').name].some(value => normalizeSkillSearchValue(value).includes(search)))
    .sort((left, right) => skillUploadedTimestamp(right) - skillUploadedTimestamp(left));
  const loading = (market.enabled ? Boolean(market.state?.loading) : loadingCatalogue) || loadingLibraryLocalSkills;
  return (
    <section id='skillhub-catalogue-panel' className='cc-skillhub-surface cc-skillhub-catalogue' role='tabpanel' aria-labelledby='skillhub-catalogue-tab'>
      {market.state?.error && <div className='cc-skillhub-alert error' role='status'>{market.state.error}<button type='button' className='icon-button' disabled={market.state.loading} onClick={market.refresh} aria-label='重新加载能力分类' title='重试'><RefreshCw size={15} /></button></div>}
      {!hasMarketPage && catalogueError && <div className='cc-skillhub-alert error' role='alert'>{catalogueError}</div>}
      {libraryLocalError && <div className='cc-skillhub-alert error cc-skillhub-library-alert' role='alert'>{libraryLocalError}</div>}
      {loading && librarySkills.length === 0 ? (
        <EmptyState icon={<RefreshCw className='is-spinning' size={20} />} title='正在读取能力库' status />
      ) : librarySkills.length === 0 ? (
        <EmptyState icon={<Search size={21} />} title='没有找到匹配的能力' copy='换一个更宽泛的关键词再试试。' />
      ) : (
        <>
          {loading && <div className='cc-skillhub-library-status' role='status'><RefreshCw className='is-spinning' size={13} aria-hidden='true' /> 正在更新能力…</div>}
          <div className='cc-skillhub-grid'>
            {librarySkills.map((skill) => <CatalogueCard key={skill.skillId} skill={skill} {...props} />)}
          </div>
        </>
      )}
      {hasMarketPage && market.state.cursor && <button type='button' className='cc-market-more' disabled={market.state.loading} onClick={market.more}>{market.state.loading ? '读取中…' : '加载更多能力'}</button>}
    </section>
  );
}

function SkillIllustration({ skill, category }) {
  const iconKey = skillIconKey(skill, category);
  const icons = {
    browser: Globe2,
    app: AppWindow,
    image: ImageIcon,
    audio: AudioLines,
    document: FileText,
    review: ClipboardCheck,
    workspace: PanelsTopLeft,
    collaboration: PanelsTopLeft,
    monitoring: Activity,
    research: BookOpenCheck,
    test: FlaskConical,
    development: Code2,
    design: Palette,
    writing: PenLine,
    office: Clipboard,
    data: ChartNoAxesCombined,
    business: BriefcaseBusiness,
    finance: Landmark,
    search: Search,
    education: GraduationCap,
    life: Sparkles,
    other: Package,
  };
  const Icon = icons[iconKey] || Package;
  return <span className='cc-skillhub-card-illustration' data-icon-key={iconKey} aria-hidden='true'><Icon size={30} strokeWidth={1.5} /></span>;
}

function CatalogueCard({ categoryOf, onSkillCategoryChange, definitionReady, installedByID, isReadOnly, language, onInstallSkill, saving, sharingSkill, skill, skillAction }) {
  const { enabled: marketEnabled } = useMarketplace();
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [publishOpen, setPublishOpen] = useState(false);
  const actionRef = useRef(null);
  const detailsTriggerRef = useRef(null);
  const closeDetails = () => {
    setDetailsOpen(false);
    detailsTriggerRef.current?.focus({ preventScroll: true });
  };
  const localized = localizedSkillText(skill, language);
  const label = localized.name;
  const installedReference = installedByID.get(skill.skillId) || null;
  const installed = Boolean(installedReference);
  const updatable = isSkillHubUpdateAvailable(installedReference, skill);
  const adding = skillAction?.type === 'add' && skillAction.skillId === skill.skillId;
  const sharing = skill.isLocalSkill && sharingSkill === skill.localSkill?.name;
  const unavailable = skill.isLocalSkill && !skill.canBind
    && (!skill.localSkill?.canShare || skill.localSkill?.source === 'system');
  const ActionIcon = updatable ? RefreshCw : installed ? Check : Plus;
  const actionLabel = updatable
    ? (adding ? '更新中…' : '更新')
    : installed
      ? '已添加'
      : (adding || sharing ? '添加中…' : '添加');
  const version = formatSkillHubVersion(skill.latestVersion) || '待确认';
  const publisher = formatSkillHubPublisher(skill);
  const uploadedAt = skillUploadedTimestamp(skill);
  const publishedTime = uploadedAt ? formatCataloguePublishedTime(uploadedAt) : '上传时间待确认';
  const sourceMetadata = skill.isLocalSkill
    ? skill.sourceLabel || '本机'
    : `发布人：${publisher} · ${publishedTime} · 版本：${version}`;
  const summary = typeof skill.presentationSummary?.summary === 'string' ? skill.presentationSummary.summary : '';
  const description = language === 'zh-CN' ? localized.description : (marketEnabled && summary) || skill.description;
  return (
    <article className={`cc-skillhub-card${installed ? ' is-added' : ''}`}>
      <button ref={detailsTriggerRef} type='button' className='cc-skillhub-card-open' aria-label={`查看 ${label} 详情`} aria-haspopup='dialog' onClick={() => setDetailsOpen(true)}><span className='cc-visually-hidden'>查看 {label} 详情</span></button>
      <div className='cc-skillhub-card-main'>
        <SkillIllustration skill={skill} category={categoryOf(skill)} />
        <div className='cc-skillhub-card-copy'>
          <div className='cc-skillhub-card-title'>
            <h3 title={label}>{label}</h3>
          </div>
          <p>{description || '这个能力暂时没有补充说明。'}</p>
          {language === 'zh-CN' && !localized.translated && !/[\u3400-\u9fff]/.test(localized.description) && <span className='cc-skillhub-translation-note'>暂无中文介绍</span>}
        </div>
        <div className='cc-skillhub-card-actions'>
          {!isReadOnly && <button
            type='button'
            className={updatable ? 'update' : installed ? 'added' : 'primary'}
            aria-label={updatable ? `更新 ${label} 到 ${formatSkillHubVersion(skill.latestVersion) || '最新版本'}` : `${actionLabel} ${label}`}
            ref={actionRef}
            disabled={!definitionReady || (installed && !updatable) || unavailable || saving || Boolean(sharingSkill)}
            title={unavailable
              ? '此能力暂时不能同步'
              : updatable ? `更新到 ${formatSkillHubVersion(skill.latestVersion)}` : `${actionLabel} ${label}`}
            onClick={() => skill.isLocalSkill && !skill.canBind ? setPublishOpen(true) : onInstallSkill(skill)}
          >
            {adding || sharing ? <RefreshCw className='is-spinning' size={14} aria-hidden='true' /> : <ActionIcon size={14} aria-hidden='true' />}
            <span className='cc-visually-hidden'>{actionLabel}</span>
          </button>}
        </div>
      </div>
      <div className='cc-skillhub-card-footer'>
        <div className={`cc-skillhub-card-source${skill.isLocalSkill ? ' is-local' : ''}`} title={sourceMetadata}>
          {skill.isLocalSkill ? <span>{sourceMetadata}</span> : <>
            <span className='cc-skillhub-card-publisher'>发布人：{publisher}</span>
            <time dateTime={uploadedAt ? new Date(uploadedAt).toISOString() : undefined}>{publishedTime}</time>
            <span className='cc-skillhub-card-version'>版本：{version}</span>
          </>}
        </div>
      </div>
      {detailsOpen && createPortal(
        <SkillDetailsDialog
          cataloguePreview
          readOnly={isReadOnly}
          details={{ ...skill, description: language === 'zh-CN' ? localized.description : skill.description }}
          label={label}
          skill={{
            ...skill,
            version: skill.latestVersion,
            localOnly: skill.isLocalSkill && !skill.canBind,
            localName: skill.localSkill?.name,
          }}
          onClose={closeDetails}
        />,
        document.body,
      )}
      {publishOpen && <SkillPublishDialog skill={skill} category={categoryOf(skill)} returnFocusRef={actionRef}
        onClose={() => setPublishOpen(false)} onConfirm={value => {
          onSkillCategoryChange(skill, value);
          setPublishOpen(false);
          onInstallSkill(skill);
        }} />}
    </article>
  );
}

function formatCataloguePublishedTime(value) {
  const timestamp = typeof value === 'number' ? value : Date.parse(String(value || ''));
  if (!Number.isFinite(timestamp)) return '发布时间待确认';
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  }).format(new Date(timestamp));
}

function formatAddedSkillVersion(skill, privateReference) {
  const displayVersion = String(skill?.displayVersion || '').trim();
  if (displayVersion) return /^\d+(?:\.\d+)*$/.test(displayVersion) ? `v${displayVersion}` : displayVersion;
  if (privateReference && Number.isSafeInteger(skill?.revisionNumber) && skill.revisionNumber > 0) {
    return `第 ${skill.revisionNumber} 版`;
  }
  if (privateReference) return '私有版本待确认';
  const version = String(skill?.version || '').trim();
  if (!version) return '版本待确认';
  return version.startsWith('v') ? version : `v${version}`;
}

function CustomSkills(props) {
  const { devices, localSkills, localSkillsError, loadingDevices, loadingLocalSkills, localNotice, localSkillsPath, onChangeSection, runtimeRouteError, selectedDeviceID } = props;
  return (
    <section className='cc-skillhub-surface cc-skillhub-custom' aria-labelledby='skillhub-custom-title'>
      <div className='cc-skillhub-custom-header'>
        <div><span className='cc-skillhub-section-kicker'>开发者工具</span><h2 id='skillhub-custom-title'>运行工作区（真实目录）</h2><p>这里直接读取当前 Agent 对应 XiaoBa 的真实 skills 目录，不是 BotDefinition 配置列表。</p></div>
        <button type='button' className='cc-skillhub-back' onClick={() => onChangeSection('added')}><ArrowLeft size={15} aria-hidden='true' /> 返回 Agent 能力总览</button>
      </div>
      <CustomToolbar {...props} localSkillsPath={localSkillsPath} />
      {!loadingDevices && devices?.length === 0 && !runtimeRouteError && (
        <div className='cc-skillhub-alert error' role='alert'>没有检测到支持 SkillHub 的在线 XiaoBa 运行环境，请确认对应 XiaoBa 已启动、已更新并连接到 CatsCo。</div>
      )}
      {!loadingDevices && devices?.length > 1 && !selectedDeviceID && (
        <div className='cc-skillhub-alert error' role='alert'>检测到多个可用的 XiaoBa 运行环境。为避免修改错工作区，请只保留一个对应运行环境在线后刷新。</div>
      )}
      {localNotice && <div className='cc-skillhub-alert success' role='status'>{localNotice}</div>}
      {localSkillsError && <div className='cc-skillhub-alert error' role='alert'>{localSkillsError}</div>}
      {loadingLocalSkills ? (
        <EmptyState icon={<RefreshCw className='is-spinning' size={20} />} title='正在读取工作区能力' copy='正在同步当前 Agent 对应的 XiaoBa 运行工作区。' status />
      ) : localSkills.length === 0 ? (
        <EmptyState icon={<Wrench size={21} />} title='还没有自定义能力' copy='在 XiaoBa 中创建 Skill 后，回到这里刷新。' />
      ) : <CustomGrid {...props} />}
    </section>
  );
}

function CustomToolbar({
  devices, loadingDevices, loadingLocalSkills, localSkills, localSkillsPath,
  localWorkspaceRevision, onCopyLocalPath, onRefreshLocal, onSyncWorkspace,
  saving, selectedBotUID, selectedDeviceID, sharingSkill, supportsWorkspaceSync,
  syncingWorkspace,
}) {
  const selectedDevice = devices?.find(device => String(device?.deviceId || '') === String(selectedDeviceID || ''));
  const isServerRuntime = selectedDevice?.runtimeRole === 'server';
  const hasInvalidSkill = localSkills.some(skill => Boolean(skill.shareError));
  const syncDisabled = !selectedBotUID
    || !selectedDeviceID
    || !supportsWorkspaceSync
    || !localWorkspaceRevision
    || localSkills.length === 0
    || hasInvalidSkill
    || loadingDevices
    || loadingLocalSkills
    || saving
    || Boolean(sharingSkill)
    || syncingWorkspace;
  const syncTitle = !supportsWorkspaceSync
    ? '目标 XiaoBa 尚不支持批量同步，请更新到最新 main 并重启。'
    : hasInvalidSkill
      ? '工作区中存在无法同步的 Skill，请先修复。'
      : '用当前运行工作区完整覆盖该 Agent 的 BotDefinition 配置；未发布 Skill 保持 Bot 私有。';
  return (
    <div className='cc-skillhub-custom-toolbar'>
      <div className='cc-skillhub-local-path'><FolderOpen size={15} aria-hidden='true' />{isServerRuntime
        ? <span>{`服务器运行工作区 · ${selectedDevice?.displayName || 'XiaoBa Server'}`}</span>
        : <code>{localSkillsPath || '尚未读取本地 Skills 目录'}</code>}</div>
      <div className='cc-skillhub-local-actions'>
        {!isServerRuntime && <button type='button' onClick={onCopyLocalPath} disabled={!localSkillsPath}><Clipboard size={14} aria-hidden='true' /> 复制路径</button>}
        <button type='button' className='primary' onClick={onSyncWorkspace} disabled={syncDisabled} title={syncTitle}>
          <Share2 size={14} aria-hidden='true' /> {syncingWorkspace ? '覆盖中…' : '用此工作区覆盖 Agent 配置'}
        </button>
        <button type='button' onClick={onRefreshLocal} disabled={!selectedBotUID || loadingDevices || loadingLocalSkills || saving || Boolean(sharingSkill) || syncingWorkspace}>
          <RefreshCw className={loadingLocalSkills ? 'is-spinning' : ''} size={14} aria-hidden='true' /> {loadingLocalSkills ? '刷新中…' : '刷新'}
        </button>
      </div>
    </div>
  );
}

function CustomGrid(props) {
  return <div className='cc-skillhub-local-grid'>{props.localSkills.map((skill) => <CustomCard key={`${skill.relativePath}:${skill.name}`} skill={skill} {...props} />)}</div>;
}

export function resolveRuntimeSkillPresentation(skill, catalogueByID, fallbackDetailsByID = {}) {
  const reference = skill?.skillHub?.reference || skill?.reference || {};
  const skillId = String(
    reference?.skillId
    || reference?.skill_id
    || skill?.cloudSkillId
    || skill?.skillId
    || '',
  ).trim();
  const details = skillId
    ? (catalogueByID?.get(skillId) || fallbackDetailsByID?.[skillId] || null)
    : null;
  const displayName = String(
    details?.displayName
    || skill?.skillHub?.displayName
    || skill?.skillHub?.display_name
    || skill?.displayName
    || skill?.display_name
    || skill?.title
    || skill?.name
    || '',
  ).trim();
  const directory = String(skill?.relativePath || skill?.path || skill?.name || '').trim();
  const version = formatSkillHubVersion(
    reference?.version
    || skill?.skillHub?.version
    || skill?.version
    || details?.latestVersion,
  );
  const publisher = formatSkillHubPublisher({
    ...(skill?.skillHub || {}),
    ...skill,
    ...(details || {}),
  }, '');
  return { details, directory, displayName, publisher, skillId, version };
}

function CustomCard({ categoryOf, onSkillCategoryChange, catalogueByID, definitionReady, installedByID, isLocalSkillShared, loadingLocalSkills, onShareLocalSkill, saving, selectedDeviceID, sharingSkill, skill, skillHubUpdateDetailsByID, syncingWorkspace }) {
  const [publishOpen, setPublishOpen] = useState(false);
  const actionRef = useRef(null);
  const reference = skill.skillHub?.reference;
  const installedReference = reference?.skillId ? installedByID.get(reference.skillId) : null;
  const shared = isLocalSkillShared(skill, installedReference);
  const blocked = Boolean(skill.shareError);
  const canShare = !blocked && skill.canShare !== false && skill.source !== 'system' && !shared;
  const statusClass = blocked ? 'blocked' : shared ? 'synced' : 'local';
  const statusLabel = blocked ? '无法发布' : shared ? '已发布' : '未发布';
  const { directory, displayName, publisher, version } = resolveRuntimeSkillPresentation(skill, catalogueByID, skillHubUpdateDetailsByID);
  return (
    <article className='cc-skillhub-local-card'>
      <div className='cc-skillhub-local-card-heading'><strong>{displayName}</strong><span className={`cc-skillhub-status ${statusClass}`}>{statusLabel}</span></div>
      <p className={blocked ? 'cc-skillhub-validation-error' : undefined} title={skill.shareError || skill.description || undefined}>
        {skill.shareError || skill.description || '这个自定义能力暂时没有补充说明。'}
      </p>
      {directory && <div className='cc-skillhub-local-directory'><span>目录：</span><code>{directory}</code></div>}
      {(version || publisher) && <div className='cc-skillhub-local-meta'>
        {version && <span>版本 {version}</span>}
        {publisher && <span>发布者 {publisher}</span>}
      </div>}
      <button ref={actionRef} type='button' className={shared ? 'added' : 'primary'} disabled={!canShare || !selectedDeviceID || !definitionReady || loadingLocalSkills || saving || Boolean(sharingSkill) || syncingWorkspace} onClick={() => setPublishOpen(true)} title={blocked ? skill.shareError : undefined}>
        {blocked ? <Info size={14} aria-hidden='true' /> : shared ? <Check size={14} aria-hidden='true' /> : <Share2 size={14} aria-hidden='true' />}
        {blocked ? '请先修复此 Skill' : shared ? '已发布到团队' : sharingSkill === skill.name ? '发布并添加中…' : '发布并添加'}
      </button>
      {publishOpen && <SkillPublishDialog skill={skill} category={categoryOf(skill)} returnFocusRef={actionRef}
        onClose={() => setPublishOpen(false)} onConfirm={value => {
          onSkillCategoryChange(skill, value);
          setPublishOpen(false);
          onShareLocalSkill(skill);
        }} />}
    </article>
  );
}

function SkillPublishDialog({ skill, category, onClose, onConfirm, returnFocusRef }) {
  const [value, setValue] = useState(category);
  const dialogRef = useRef(null);
  const cancelRef = useRef(null);
  const titleId = useId();
  const descriptionId = useId();
  useDialogBehavior(dialogRef, { onClose, initialFocusRef: cancelRef, returnFocusRef });
  return createPortal(
    <div className='cc-skillhub-detail-overlay' onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}>
      <section ref={dialogRef} tabIndex={-1} className='cc-skillhub-detail-dialog cc-skillhub-publish-dialog' role='dialog' aria-modal='true' aria-labelledby={titleId} aria-describedby={descriptionId}>
        <header className='cc-skillhub-detail-header'>
          <span className='cc-skillhub-detail-icon' aria-hidden='true'><Share2 size={19} /></span>
          <div><span>发布能力</span><h2 id={titleId}>{skill.displayName || skill.name || skill.skillId}</h2></div>
          <button type='button' className='icon-button' aria-label='关闭发布确认' onClick={onClose}><X size={17} aria-hidden='true' /></button>
        </header>
        <p id={descriptionId} className='cc-skillhub-detail-description'>发布到团队，并添加到当前 Agent。</p>
        <div className='cc-skillhub-publish-category'><SkillCategoryField skill={skill} value={value} onChange={(_, next) => setValue(next)} /></div>
        <footer className='cc-skillhub-detail-footer'>
          <button ref={cancelRef} type='button' onClick={onClose}>取消</button>
          <button type='button' disabled={!value} onClick={() => onConfirm(value)}>发布并添加</button>
        </footer>
      </section>
    </div>, document.body,
  );
}

function EmptyState({ copy, icon, status = false, title }) {
  return <div className='cc-skillhub-empty' role={status ? 'status' : undefined}>{icon}{title && <strong>{title}</strong>}{copy && <span>{copy}</span>}</div>;
}
