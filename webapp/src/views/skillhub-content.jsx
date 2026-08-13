import React, { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowLeft, Bot, Check, Clipboard, FolderOpen, Info,
  Package, PackageMinus, RefreshCw, Search, Share2,
  ShieldCheck, Wrench, X,
} from 'lucide-react';
import CustomSelect from '../widgets/custom-select';

export default function SkillHubContent(props) {
  const {
    actionNotice, activeSection, definition, definitionError, isLocalEnabled,
    isReadOnly, loadingDefinition, onChangeSection, saving, selectedAgentName, selectedAgentRelation, skillAction,
  } = props;
  return (
    <main className='cc-skillhub-page'>
      <div className='cc-skillhub-shell'>
        <header className='cc-skillhub-header'>
          <div className='cc-skillhub-title-block'>
            <span className='cc-skillhub-eyebrow'><Package size={14} aria-hidden='true' /> SkillHub</span>
            <h1>Agent 能力</h1>
            <p>为 Agent 添加和管理可用能力。</p>
          </div>
          <AgentContext {...props} />
        </header>
        {definitionError && <div className='cc-skillhub-alert error' role='alert'>{definitionError}</div>}
        {actionNotice && <div className='cc-skillhub-alert success' role='status'>{actionNotice}</div>}
        {activeSection === 'custom' ? <CustomSkills {...props} /> : (
          <>
            <SkillNavigation {...props} addedCount={definition.skills.length} />
            {(loadingDefinition || saving) && (
              <div className='cc-skillhub-progress' role='status'>
                <RefreshCw className='is-spinning' size={14} aria-hidden='true' />
                {loadingDefinition ? `正在更新${selectedAgentName ? ` Agent“${selectedAgentName}”` : '当前 Agent'}的能力…` : skillAction?.type === 'remove' ? '正在移除能力…' : '正在添加能力…'}
              </div>
            )}
            {activeSection === 'added' ? <AddedSkills {...props} /> : <Catalogue {...props} />}
          </>
        )}
      </div>
    </main>
  );
}

function AgentContext({
  agentOptions, loadingBots, onSelectAgent, saving, selectedAgentRelation, selectedBotUID, sharingSkill,
}) {
  const disabled = loadingBots || agentOptions.length === 0 || Boolean(sharingSkill) || saving;
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
      {selectedAgentRelation === 'friend' && <span className='cc-skillhub-readonly-badge'><ShieldCheck size={13} aria-hidden='true' /> 只读查看</span>}
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
        triggerClassName='cc-skillhub-agent-select-trigger'
        value={value}
        onValueChange={onChange}
      >
        {agents.length === 0 && <option value=''>暂无自己拥有的 Agent</option>}
        {agents.map((agent) => <option key={agent.value} value={agent.value}>{agent.label}</option>)}
      </CustomSelect>
    </span>
  );
}

function SkillNavigation({ activeSection, addedCount, isLocalEnabled, onChangeSection }) {
  return (
    <nav className='cc-skillhub-navigation' aria-label='Agent 能力视图'>
      <div className='cc-skillhub-tabs' role='tablist' aria-label='能力管理'>
        <button type='button' id='skillhub-added-tab' role='tab' aria-selected={activeSection === 'added'} aria-controls='skillhub-added-panel' className={activeSection === 'added' ? 'active' : ''} onClick={() => onChangeSection('added')}>
          当前 Agent 能力 <span>{addedCount}</span>
        </button>
        <button type='button' id='skillhub-catalogue-tab' role='tab' aria-selected={activeSection === 'catalogue'} aria-controls='skillhub-catalogue-panel' className={activeSection === 'catalogue' ? 'active' : ''} onClick={() => onChangeSection('catalogue')}>
          能力库
        </button>
      </div>
      {isLocalEnabled && (
        <button type='button' className='cc-skillhub-custom-entry' onClick={() => onChangeSection('custom')}>
          <Wrench size={14} aria-hidden='true' /> 本地工作区
        </button>
      )}
    </nav>
  );
}

function AddedSkills(props) {
  const {
    catalogueByID, definition, definitionReady, loadingDefinition, onChangeSection,
    onCopySkill, onRefreshDefinition, onRemoveSkill, saving, selectedAgentName, selectedBotUID,
    sharingSkill, skillAction,
  } = props;
  return (
    <section id='skillhub-added-panel' className='cc-skillhub-surface cc-skillhub-added' role='tabpanel' aria-labelledby='skillhub-added-tab'>
      <div className='cc-skillhub-content-header'>
        <div><h2>当前 Agent 能力</h2><p>同时展示正式启用能力和本地工作区能力，本地能力会明确标记为未正式启用。</p></div>
        <button type='button' className='icon-button' aria-label='刷新当前 Agent 的能力' title='刷新能力' onClick={onRefreshDefinition} disabled={!selectedBotUID || loadingDefinition || saving || Boolean(sharingSkill)}>
          <RefreshCw className={loadingDefinition ? 'is-spinning' : ''} size={15} aria-hidden='true' />
        </button>
      </div>
      {!selectedBotUID ? (
        <EmptyState icon={<Bot size={21} />} title='请先选择 Agent' copy='选择后即可查看它已经具备的能力。' />
      ) : loadingDefinition ? (
        <EmptyState icon={<RefreshCw className='is-spinning' size={20} />} title='正在读取 Agent 能力' status />
      ) : definition.skills.length === 0 ? (
        <div className='cc-skillhub-empty cc-skillhub-empty-added'>
          <Package size={22} aria-hidden='true' /><strong>还没有添加能力</strong>
          <span>前往能力库，为当前 Agent 选择第一项能力。</span>
          <button type='button' className='primary' onClick={() => onChangeSection('catalogue')}>浏览能力库</button>
        </div>
      ) : (
        <div className='cc-skillhub-added-list'>
          {definition.skills.map((skill) => <AddedSkillItem key={skill.skillId} skill={skill} {...props} />)}
        </div>
      )}
    </section>
  );
}

function AddedSkillItem({ addedSkillPresentationByID, definitionReady, isReadOnly, onCopySkill, onRemoveSkill, saving, sharingSkill, skill, skillAction }) {
  const presentation = addedSkillPresentationByID.get(skill.skillId);
  const {
    description, details, label, localDetails, privateReference,
  } = presentation;
  const copying = skillAction?.type === 'copy' && skillAction.skillId === skill.skillId;
  const removing = skillAction?.type === 'remove' && skillAction.skillId === skill.skillId;
  const actionsDisabled = saving || Boolean(sharingSkill) || !definitionReady || Boolean(skillAction);
  const triggerRef = useRef(null);
  const menuRef = useRef(null);
  const firstMenuItemRef = useRef(null);
  const menuId = useId();
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [menuPosition, setMenuPosition] = useState(null);

  useLayoutEffect(() => {
    if (!menuOpen || !triggerRef.current) return undefined;
    let frame = 0;
    const updatePosition = () => {
      const rect = triggerRef.current.getBoundingClientRect();
      const gutter = 8;
      const width = 190;
      const height = menuRef.current?.offsetHeight || 92;
      const opensAbove = window.innerHeight - rect.bottom < height + gutter && rect.top > height + gutter;
      const top = opensAbove
        ? Math.max(gutter, rect.top - height - gutter)
        : Math.min(rect.bottom + gutter, Math.max(gutter, window.innerHeight - height - gutter));
      const left = Math.min(
        Math.max(gutter, rect.right - width),
        Math.max(gutter, window.innerWidth - width - gutter),
      );
      setMenuPosition({ left, top, width });
    };
    updatePosition();
    frame = window.requestAnimationFrame(updatePosition);
    window.addEventListener('resize', updatePosition);
    window.addEventListener('scroll', updatePosition, true);
    return () => {
      window.cancelAnimationFrame(frame);
      window.removeEventListener('resize', updatePosition);
      window.removeEventListener('scroll', updatePosition, true);
    };
  }, [menuOpen]);

  useEffect(() => {
    if (!menuOpen) {
      setMenuPosition(null);
      return undefined;
    }
    const handlePointerDown = (event) => {
      if (triggerRef.current?.contains(event.target) || menuRef.current?.contains(event.target)) return;
      setMenuOpen(false);
    };
    document.addEventListener('pointerdown', handlePointerDown);
    return () => document.removeEventListener('pointerdown', handlePointerDown);
  }, [menuOpen]);

  useEffect(() => {
    if (!menuOpen || !menuPosition) return undefined;
    const frame = window.requestAnimationFrame(() => firstMenuItemRef.current?.focus({ preventScroll: true }));
    return () => window.cancelAnimationFrame(frame);
  }, [menuOpen, menuPosition]);

  const closeMenu = (returnFocus = false) => {
    setMenuOpen(false);
    if (returnFocus) window.requestAnimationFrame(() => triggerRef.current?.focus({ preventScroll: true }));
  };

  const handleMenuKeyDown = (event) => {
    const items = [...(menuRef.current?.querySelectorAll('[role="menuitem"]:not(:disabled)') || [])];
    const currentIndex = items.indexOf(document.activeElement);
    if (event.key === 'Escape') {
      event.preventDefault();
      closeMenu(true);
    } else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const delta = event.key === 'ArrowDown' ? 1 : -1;
      items[(currentIndex + delta + items.length) % items.length]?.focus();
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault();
      items[event.key === 'Home' ? 0 : items.length - 1]?.focus();
    } else if (event.key === 'Tab') {
      setMenuOpen(false);
    }
  };

  const closeDetails = () => {
    setDetailsOpen(false);
    window.requestAnimationFrame(() => triggerRef.current?.focus({ preventScroll: true }));
  };

  return (
    <article className='cc-skillhub-added-item'>
      <span className='cc-skillhub-added-icon' aria-hidden='true'><Package size={17} /></span>
      <div className='cc-skillhub-added-copy'>
        <div className='cc-skillhub-added-title'>
          <h3>{label}</h3><span className={`cc-skillhub-availability${skill.localOnly ? ' is-local-only' : ''}`}><Check size={12} aria-hidden='true' /> {skill.localOnly ? '仅本地' : '已启用'}</span>
        </div>
        <p>{description}</p>
        <span className='cc-skillhub-version-note'><ShieldCheck size={12} aria-hidden='true' /> {skill.version ? (String(skill.version).startsWith('v') ? skill.version : `v${skill.version}`) : '版本未确认'}{(details?.author || skill.author) ? ` · ${details?.author || skill.author}` : ''}{skill.localOnly ? ' · 未正式启用' : (privateReference ? ' · Bot 私有 · 仅当前 Agent 可用' : '')}</span>
      </div>
      <div className='cc-skillhub-added-actions'>
        {!isReadOnly && !skill.localOnly && <button type='button' className='subtle cc-skillhub-copy-action' aria-label={`复制 ${label}`} disabled={actionsDisabled} onClick={() => onCopySkill(skill.skillId)}>
          {copying ? '复制中…' : '复制'}
        </button>}
        {!isReadOnly && !skill.localOnly && <button
          ref={triggerRef}
          type='button'
          className='subtle cc-skillhub-more-action'
          aria-label={`更多操作 ${label}`}
          aria-haspopup='menu'
          aria-expanded={menuOpen}
          aria-controls={menuOpen ? menuId : undefined}
          disabled={actionsDisabled}
          onClick={() => setMenuOpen((current) => !current)}
          onKeyDown={(event) => {
            if ((event.key === 'ArrowDown' || event.key === 'ArrowUp') && !menuOpen) {
              event.preventDefault();
              setMenuOpen(true);
            } else if (event.key === 'Escape' && menuOpen) {
              event.preventDefault();
              closeMenu(true);
            }
          }}
        >
          更多
        </button>}
      </div>
      {menuOpen && menuPosition && createPortal(
        <div
          ref={menuRef}
          id={menuId}
          className='cc-skillhub-action-menu'
          role='menu'
          aria-label={`${label} 操作`}
          style={menuPosition}
          onKeyDown={handleMenuKeyDown}
        >
          <button
            ref={firstMenuItemRef}
            type='button'
            role='menuitem'
            onClick={() => {
              setMenuOpen(false);
              setDetailsOpen(true);
            }}
          >
            <Info size={15} aria-hidden='true' /> 查看详情
          </button>
          <div className='cc-skillhub-action-menu-divider' role='separator' />
          <button
            type='button'
            role='menuitem'
            className='danger'
            disabled={removing}
            onClick={() => {
              setMenuOpen(false);
              onRemoveSkill(skill.skillId);
            }}
          >
            <PackageMinus size={15} aria-hidden='true' /> {removing ? '移除中…' : '从 Agent 移除'}
          </button>
        </div>,
        document.body,
      )}
      {detailsOpen && createPortal(
        <SkillDetailsDialog details={details} label={label} localDetails={localDetails} onClose={closeDetails} privateReference={privateReference} skill={skill} />,
        document.body,
      )}
    </article>
  );
}

function SkillDetailsDialog({ details, label, localDetails, onClose, privateReference, skill }) {
  const dialogRef = useRef(null);
  const closeButtonRef = useRef(null);
  const titleId = useId();
  const descriptionId = useId();
  const description = details?.description || localDetails?.description || '此能力已添加到当前 Agent，可立即使用。';

  useEffect(() => {
    closeButtonRef.current?.focus({ preventScroll: true });
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = [...(dialogRef.current?.querySelectorAll('button:not(:disabled), [href], [tabindex]:not([tabindex="-1"])') || [])];
      if (!focusable.length) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div
      className='cc-skillhub-detail-overlay'
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialogRef}
        className='cc-skillhub-detail-dialog'
        role='dialog'
        aria-modal='true'
        aria-labelledby={titleId}
        aria-describedby={descriptionId}
      >
        <header className='cc-skillhub-detail-header'>
          <span className='cc-skillhub-detail-icon' aria-hidden='true'><Package size={19} /></span>
          <div>
            <span>{privateReference ? 'Agent 私有能力' : 'SkillHub 能力'}</span>
            <h2 id={titleId}>{label}</h2>
          </div>
          <button ref={closeButtonRef} type='button' className='icon-button' aria-label='关闭能力详情' onClick={onClose}>
            <X size={17} aria-hidden='true' />
          </button>
        </header>
        <p id={descriptionId} className='cc-skillhub-detail-description'>{description}</p>
        <dl className='cc-skillhub-detail-meta'>
          <div><dt>{privateReference ? '能力引用' : 'SkillHub ID'}</dt><dd><code translate='no'>{skill.skillId}</code></dd></div>
          <div><dt>当前版本</dt><dd>{skill.version ? <code translate='no'>v{skill.version}</code> : '版本待确认'}</dd></div>
          <div><dt>{privateReference ? '可见范围' : '发布者'}</dt><dd>{privateReference ? '仅当前 Agent' : details?.author || 'SkillHub'}</dd></div>
        </dl>
        <div className='cc-skillhub-detail-footer'>
          <button type='button' onClick={onClose}>完成</button>
        </div>
      </section>
    </div>
  );
}

function Catalogue(props) {
  const {
    catalogueError, isReadOnly, libraryLocalError, librarySkills, loadingCatalogue,
    loadingLibraryLocalSkills, onQueryChange, onSearch, query,
  } = props;
  const loading = loadingCatalogue || loadingLibraryLocalSkills;
  return (
    <section id='skillhub-catalogue-panel' className='cc-skillhub-surface cc-skillhub-catalogue' role='tabpanel' aria-labelledby='skillhub-catalogue-tab'>
      <div className='cc-skillhub-content-header cc-skillhub-catalogue-header'>
        <div><h2>能力库</h2><p>找到需要的能力，一次点击即可添加到当前 Agent。</p></div>
      </div>
      <form className='cc-skillhub-search' role='search' onSubmit={(event) => { event.preventDefault(); onSearch(query); }}>
        <Search size={17} aria-hidden='true' />
        <label className='cc-visually-hidden' htmlFor='cc-skillhub-search-input'>搜索能力</label>
        <div className='cc-skillhub-search-field'>
          <input id='cc-skillhub-search-input' name='skillhub-query' type='search' autoComplete='off' value={query} onChange={(event) => onQueryChange(event.target.value)} placeholder='搜索能力名称或用途…' />
          {query && (
            <button type='button' className='cc-skillhub-search-clear' aria-label='清除搜索内容' title='清除' onClick={() => onQueryChange('')}>
              <X size={14} aria-hidden='true' />
            </button>
          )}
        </div>
        <button type='submit' disabled={loadingCatalogue}>{loadingCatalogue ? '搜索中…' : '搜索'}</button>
      </form>
      {catalogueError && <div className='cc-skillhub-alert error' role='alert'>{catalogueError}</div>}
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
    </section>
  );
}

function CatalogueCard({ definitionReady, installedByID, isReadOnly, onInstallSkill, saving, sharingSkill, skill, skillAction }) {
  const installed = installedByID.has(skill.skillId);
  const adding = skillAction?.type === 'add' && skillAction.skillId === skill.skillId;
  const sharing = skill.isLocalSkill && sharingSkill === skill.localSkill?.name;
  const unavailable = skill.isLocalSkill && !skill.canBind
    && (!skill.localSkill?.canShare || skill.localSkill?.source === 'system');
  return (
    <article className={`cc-skillhub-card${installed ? ' is-added' : ''}`}>
      <div className='cc-skillhub-card-title'>
        <span className='cc-skillhub-card-icon' aria-hidden='true'><Package size={17} /></span>
        <h3>{skill.displayName || skill.skillId}</h3>
      </div>
      <p>{skill.description || '这个能力暂时没有补充说明。'}</p>
      <div className='cc-skillhub-card-footer'>
        <span className={`cc-skillhub-card-source${skill.isLocalSkill ? ' is-local' : ''}`}>{skill.sourceLabel || '在线'}</span>
        {!isReadOnly && <button type='button' className={installed ? 'added' : 'primary'} disabled={!definitionReady || installed || unavailable || saving || Boolean(sharingSkill)} title={unavailable ? '此能力暂时不能同步' : undefined} onClick={() => onInstallSkill(skill)}>
          {installed ? <Check size={14} aria-hidden='true' /> : <Package size={14} aria-hidden='true' />}
          {installed ? '已添加' : adding || sharing ? '添加中…' : '添加'}
        </button>}
      </div>
    </article>
  );
}

function CustomSkills(props) {
  const { devices, localSkills, localSkillsError, loadingDevices, loadingLocalSkills, localNotice, localSkillsPath, onChangeSection, selectedDeviceID } = props;
  return (
    <section className='cc-skillhub-surface cc-skillhub-custom' aria-labelledby='skillhub-custom-title'>
      <div className='cc-skillhub-custom-header'>
        <div><span className='cc-skillhub-section-kicker'>开发者工具</span><h2 id='skillhub-custom-title'>管理自定义能力</h2><p>查看本地能力、验证内容并发布到团队。这里的操作面向 Skill 开发者。</p></div>
        <button type='button' className='cc-skillhub-back' onClick={() => onChangeSection('added')}><ArrowLeft size={15} aria-hidden='true' /> 返回能力管理</button>
      </div>
      <CustomToolbar {...props} localSkillsPath={localSkillsPath} />
      {!loadingDevices && devices?.length === 0 && (
        <div className='cc-skillhub-alert error' role='alert'>没有检测到支持 SkillHub 的在线 XiaoBa，请启动或更新本地 XiaoBa。</div>
      )}
      {!loadingDevices && devices?.length > 1 && !selectedDeviceID && (
        <div className='cc-skillhub-empty'>请选择要操作的本地 XiaoBa，避免修改到其他电脑。</div>
      )}
      {localNotice && <div className='cc-skillhub-alert success' role='status'>{localNotice}</div>}
      {localSkillsError ? <div className='cc-skillhub-alert error' role='alert'>{localSkillsError}</div> : loadingLocalSkills ? (
        <EmptyState icon={<RefreshCw className='is-spinning' size={20} />} title='正在读取本地能力' copy='正在同步当前 Agent 对应的 XiaoBa 工作区。' status />
      ) : localSkills.length === 0 ? (
        <EmptyState icon={<Wrench size={21} />} title='还没有自定义能力' copy='在 XiaoBa 中创建 Skill 后，回到这里刷新。' />
      ) : <CustomGrid {...props} />}
    </section>
  );
}

function CustomToolbar({ devices = [], loadingDevices, loadingLocalSkills, localSkillsPath, onCopyLocalPath, onRefreshLocal, onSelectDevice, saving, selectedBotUID, selectedDeviceID, sharingSkill }) {
  return (
    <div className='cc-skillhub-custom-toolbar'>
      <div className='cc-skillhub-device-picker'>
        <span>本地 XiaoBa</span>
        <select
          className='cc-skillhub-native-select'
          value={selectedDeviceID || ''}
          disabled={loadingDevices || devices.length === 0 || Boolean(sharingSkill)}
          tabIndex={-1}
          aria-hidden='true'
          onChange={(event) => onSelectDevice?.(event.target.value)}
        >
          {devices.length === 0 && <option value=''>暂无支持 SkillHub 的在线设备</option>}
          {devices.length > 1 && <option value=''>请选择要操作的设备</option>}
          {devices.map((device) => <option key={device.deviceId} value={device.deviceId}>{device.displayName || device.deviceId}</option>)}
        </select>
        <CustomSelect
          ariaLabel='本地 XiaoBa'
          className='cc-skillhub-device-select'
          density='comfortable'
          value={selectedDeviceID || ''}
          disabled={loadingDevices || devices.length === 0 || Boolean(sharingSkill)}
          onValueChange={(deviceID) => onSelectDevice?.(deviceID)}
        >
          {devices.length === 0 && <option value=''>暂无支持 SkillHub 的在线设备</option>}
          {devices.length > 1 && <option value=''>请选择要操作的设备</option>}
          {devices.map((device) => <option key={device.deviceId} value={device.deviceId}>{device.displayName || device.deviceId}</option>)}
        </CustomSelect>
      </div>
      <div className='cc-skillhub-local-path'><FolderOpen size={15} aria-hidden='true' /><code>{localSkillsPath || '尚未读取本地 Skills 目录'}</code></div>
      <div className='cc-skillhub-local-actions'>
        <button type='button' onClick={onCopyLocalPath} disabled={!localSkillsPath}><Clipboard size={14} aria-hidden='true' /> 复制路径</button>
        <button type='button' onClick={onRefreshLocal} disabled={!selectedBotUID || loadingLocalSkills || saving || Boolean(sharingSkill)}>
          <RefreshCw className={loadingLocalSkills ? 'is-spinning' : ''} size={14} aria-hidden='true' /> {loadingLocalSkills ? '刷新中…' : '刷新'}
        </button>
      </div>
    </div>
  );
}

function CustomGrid(props) {
  return <div className='cc-skillhub-local-grid'>{props.localSkills.map((skill) => <CustomCard key={`${skill.relativePath}:${skill.name}`} skill={skill} {...props} />)}</div>;
}

function CustomCard({ definitionReady, installedByID, isLocalSkillShared, loadingLocalSkills, onShareLocalSkill, saving, selectedDeviceID, sharingSkill, skill }) {
  const reference = skill.skillHub?.reference;
  const installedReference = reference?.skillId ? installedByID.get(reference.skillId) : null;
  const shared = isLocalSkillShared(skill, installedReference);
  const canShare = skill.canShare !== false && skill.source !== 'system' && !shared;
  return (
    <article className='cc-skillhub-local-card'>
      <div className='cc-skillhub-local-card-heading'><strong>{skill.name}</strong><span className={`cc-skillhub-status ${shared ? 'synced' : 'local'}`}>{shared ? '已发布' : '未发布'}</span></div>
      <p>{skill.description || '这个自定义能力暂时没有补充说明。'}</p>
      <code>{skill.relativePath || skill.path}</code>
      <button type='button' className={shared ? 'added' : 'primary'} disabled={!canShare || !selectedDeviceID || !definitionReady || loadingLocalSkills || saving || Boolean(sharingSkill)} onClick={() => onShareLocalSkill(skill)}>
        {shared ? <Check size={14} aria-hidden='true' /> : <Share2 size={14} aria-hidden='true' />}
        {shared ? '已发布到团队' : sharingSkill === skill.name ? '发布并添加中…' : '发布并添加'}
      </button>
    </article>
  );
}

function EmptyState({ copy, icon, status = false, title }) {
  return <div className='cc-skillhub-empty' role={status ? 'status' : undefined}>{icon}{title && <strong>{title}</strong>}{copy && <span>{copy}</span>}</div>;
}
