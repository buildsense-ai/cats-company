import React, { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import {
  ArrowLeft, Bot, Check, ChevronDown, Clipboard, FolderOpen, Info,
  Package, PackageMinus, RefreshCw, Search, Share2,
  ShieldCheck, Wrench, X,
} from 'lucide-react';

export default function SkillHubContent(props) {
  const {
    actionNotice, activeSection, definition, definitionError, isLocalEnabled,
    loadingDefinition, onChangeSection, saving, selectedAgentName, skillAction,
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
  agentOptions, loadingBots, onSelectAgent, saving, selectedBotUID, sharingSkill,
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
    </div>
  );
}

function AgentSelect({ agents, disabled, onChange, value }) {
  const triggerRef = useRef(null);
  const listRef = useRef(null);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(() => Math.max(0, agents.findIndex((agent) => agent.value === value)));
  const [placement, setPlacement] = useState(null);
  const selectedIndex = Math.max(0, agents.findIndex((agent) => agent.value === value));
  const selected = agents[selectedIndex] || null;

  useEffect(() => {
    setActiveIndex(selectedIndex);
  }, [selectedIndex]);

  useLayoutEffect(() => {
    if (!open || !triggerRef.current) return undefined;
    const updatePlacement = () => {
      const rect = triggerRef.current.getBoundingClientRect();
      const gutter = 8;
      const width = Math.min(rect.width, Math.max(0, window.innerWidth - gutter * 2));
      const maxHeight = Math.min(280, Math.max(120, window.innerHeight - rect.bottom - gutter));
      const top = rect.bottom + gutter + maxHeight <= window.innerHeight
        ? rect.bottom + gutter
        : Math.max(gutter, rect.top - gutter - maxHeight);
      const left = Math.min(Math.max(gutter, rect.left), Math.max(gutter, window.innerWidth - width - gutter));
      setPlacement({ left, top, width, maxHeight });
    };
    updatePlacement();
    window.addEventListener('resize', updatePlacement);
    window.addEventListener('scroll', updatePlacement, true);
    return () => {
      window.removeEventListener('resize', updatePlacement);
      window.removeEventListener('scroll', updatePlacement, true);
    };
  }, [open]);

  useEffect(() => {
    if (!open) return undefined;
    const handlePointerDown = (event) => {
      if (triggerRef.current?.contains(event.target) || listRef.current?.contains(event.target)) return;
      setOpen(false);
    };
    document.addEventListener('pointerdown', handlePointerDown);
    return () => document.removeEventListener('pointerdown', handlePointerDown);
  }, [open]);

  const selectAgent = (agent) => {
    onChange(agent.value);
    setOpen(false);
    requestAnimationFrame(() => triggerRef.current?.focus());
  };

  const handleKeyDown = (event) => {
    if (disabled) return;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (!open) {
        setActiveIndex(selectedIndex);
        setOpen(true);
        return;
      }
      const delta = event.key === 'ArrowDown' ? 1 : -1;
      setActiveIndex((index) => (index + delta + agents.length) % agents.length);
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault();
      setActiveIndex(event.key === 'Home' ? 0 : Math.max(0, agents.length - 1));
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      if (!open) setOpen(true);
      else if (agents[activeIndex]) selectAgent(agents[activeIndex]);
    } else if (event.key === 'Escape' && open) {
      event.preventDefault();
      setOpen(false);
    }
  };

  return (
    <span className='cc-skillhub-select-wrap'>
      <select
        className='cc-skillhub-agent-native-select'
        value={value}
        disabled={disabled}
        tabIndex={-1}
        aria-hidden='true'
        onChange={(event) => onChange(event.target.value)}
      >
        {agents.length === 0 && <option value=''>暂无自己拥有的 Agent</option>}
        {agents.map((agent) => <option key={agent.value} value={agent.value}>{agent.label}</option>)}
      </select>
      <button
        ref={triggerRef}
        type='button'
        className='cc-skillhub-agent-select-trigger'
        disabled={disabled}
        aria-haspopup='listbox'
        aria-expanded={open}
        aria-label='当前 Agent'
        onClick={() => setOpen((current) => !current)}
        onKeyDown={handleKeyDown}
      >
        <span>{selected?.label || (agents.length ? '选择 Agent' : '暂无自己拥有的 Agent')}</span>
        <ChevronDown className='cc-skillhub-select-chevron' size={15} aria-hidden='true' />
      </button>
      {open && placement && createPortal(
        <div
          ref={listRef}
          className='cc-skillhub-agent-options'
          role='listbox'
          aria-label='Agent 列表'
          style={{ left: placement.left, top: placement.top, width: placement.width, maxHeight: placement.maxHeight }}
        >
          {agents.map((agent, index) => (
            <button
              key={agent.value}
              type='button'
              role='option'
              aria-selected={agent.value === value}
              className={index === activeIndex ? 'is-active' : ''}
              onMouseEnter={() => setActiveIndex(index)}
              onClick={() => selectAgent(agent)}
            >
              <span>{agent.label}</span>
              {agent.value === value && <Check size={14} aria-hidden='true' />}
            </button>
          ))}
        </div>,
        document.body,
      )}
    </span>
  );
}

function SkillNavigation({ activeSection, addedCount, isLocalEnabled, onChangeSection }) {
  return (
    <nav className='cc-skillhub-navigation' aria-label='Agent 能力视图'>
      <div className='cc-skillhub-tabs' role='tablist' aria-label='能力管理'>
        <button type='button' id='skillhub-added-tab' role='tab' aria-selected={activeSection === 'added'} aria-controls='skillhub-added-panel' className={activeSection === 'added' ? 'active' : ''} onClick={() => onChangeSection('added')}>
          已添加 <span>{addedCount}</span>
        </button>
        <button type='button' id='skillhub-catalogue-tab' role='tab' aria-selected={activeSection === 'catalogue'} aria-controls='skillhub-catalogue-panel' className={activeSection === 'catalogue' ? 'active' : ''} onClick={() => onChangeSection('catalogue')}>
          能力库
        </button>
      </div>
      {isLocalEnabled && (
        <button type='button' className='cc-skillhub-custom-entry' onClick={() => onChangeSection('custom')}>
          <Wrench size={14} aria-hidden='true' /> 管理自定义能力
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
        <div><h2>已添加能力</h2><p>这些能力当前可供 {selectedAgentName ? `Agent“${selectedAgentName}”` : '所选 Agent'} 使用。</p></div>
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

function AddedSkillItem({ catalogueByID, definitionReady, onCopySkill, onRemoveSkill, saving, sharingSkill, skill, skillAction }) {
  const details = catalogueByID.get(skill.skillId);
  const label = details?.displayName || skill.skillId;
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
          <h3>{label}</h3><span className='cc-skillhub-availability'><Check size={12} aria-hidden='true' /> 可用</span>
        </div>
        <p>{details?.description || '此能力已添加到当前 Agent，可立即使用。'}</p>
        <span className='cc-skillhub-version-note'><ShieldCheck size={12} aria-hidden='true' /> 自动保持稳定版本</span>
      </div>
      <div className='cc-skillhub-added-actions'>
        <button type='button' className='subtle cc-skillhub-copy-action' aria-label={`复制 ${label}`} disabled={actionsDisabled} onClick={() => onCopySkill(skill.skillId)}>
          {copying ? '复制中…' : '复制'}
        </button>
        <button
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
        </button>
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
        <SkillDetailsDialog details={details} label={label} onClose={closeDetails} skill={skill} />,
        document.body,
      )}
    </article>
  );
}

function SkillDetailsDialog({ details, label, onClose, skill }) {
  const dialogRef = useRef(null);
  const closeButtonRef = useRef(null);
  const titleId = useId();
  const descriptionId = useId();
  const description = details?.description || '此能力已添加到当前 Agent，可立即使用。';

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
            <span>SkillHub 能力</span>
            <h2 id={titleId}>{label}</h2>
          </div>
          <button ref={closeButtonRef} type='button' className='icon-button' aria-label='关闭能力详情' onClick={onClose}>
            <X size={17} aria-hidden='true' />
          </button>
        </header>
        <p id={descriptionId} className='cc-skillhub-detail-description'>{description}</p>
        <dl className='cc-skillhub-detail-meta'>
          <div><dt>SkillHub ID</dt><dd><code translate='no'>{skill.skillId}</code></dd></div>
          <div><dt>当前版本</dt><dd>{skill.version ? <code translate='no'>v{skill.version}</code> : '版本待确认'}</dd></div>
          <div><dt>发布者</dt><dd>{details?.author || 'SkillHub'}</dd></div>
        </dl>
        <div className='cc-skillhub-detail-footer'>
          <button type='button' onClick={onClose}>完成</button>
        </div>
      </section>
    </div>
  );
}

function Catalogue(props) {
  const { catalogue, catalogueError, loadingCatalogue, onQueryChange, onSearch, query } = props;
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
      {catalogueError ? (
        <div className='cc-skillhub-alert error' role='alert'>{catalogueError}</div>
      ) : loadingCatalogue ? (
        <EmptyState icon={<RefreshCw className='is-spinning' size={20} />} title='正在读取能力库' status />
      ) : catalogue.length === 0 ? (
        <EmptyState icon={<Search size={21} />} title='没有找到匹配的能力' copy='换一个更宽泛的关键词再试试。' />
      ) : (
        <div className='cc-skillhub-grid'>
          {catalogue.map((skill) => <CatalogueCard key={skill.skillId} skill={skill} {...props} />)}
        </div>
      )}
    </section>
  );
}

function CatalogueCard({ definitionReady, installedByID, onInstallSkill, saving, sharingSkill, skill, skillAction }) {
  const installed = installedByID.has(skill.skillId);
  const adding = skillAction?.type === 'add' && skillAction.skillId === skill.skillId;
  const stable = Boolean(skill.latestVersion && /^[0-9a-f]{64}$/.test(String(skill.contentHash || '')));
  return (
    <article className={`cc-skillhub-card${installed ? ' is-added' : ''}`}>
      <div className='cc-skillhub-card-title'>
        <span className='cc-skillhub-card-icon' aria-hidden='true'><Package size={17} /></span>
        <h3>{skill.displayName || skill.skillId}</h3>
      </div>
      <p>{skill.description || '这个能力暂时没有补充说明。'}</p>
      <div className='cc-skillhub-card-footer'>
        <span className={stable ? 'verified' : ''}>{stable && <ShieldCheck size={13} aria-hidden='true' />}{stable ? '稳定版本' : '版本确认中'}</span>
        <button type='button' className={installed ? 'added' : 'primary'} disabled={!definitionReady || installed || saving || Boolean(sharingSkill)} onClick={() => onInstallSkill(skill)}>
          {installed ? <Check size={14} aria-hidden='true' /> : <Package size={14} aria-hidden='true' />}
          {installed ? '已添加' : adding ? '添加中…' : '添加'}
        </button>
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
      <label className='cc-skillhub-device-picker'>
        <span>本地 XiaoBa</span>
        <select
          value={selectedDeviceID || ''}
          disabled={loadingDevices || devices.length === 0 || Boolean(sharingSkill)}
          onChange={(event) => onSelectDevice?.(event.target.value)}
        >
          {devices.length === 0 && <option value=''>暂无支持 SkillHub 的在线设备</option>}
          {devices.length > 1 && <option value=''>请选择要操作的设备</option>}
          {devices.map((device) => <option key={device.deviceId} value={device.deviceId}>{device.displayName || device.deviceId}</option>)}
        </select>
      </label>
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

function CustomCard({ definitionReady, loadingLocalSkills, onShareLocalSkill, saving, selectedDeviceID, sharingSkill, skill }) {
  const shared = Boolean(skill.skillHub?.author && skill.skillHub?.version);
  const canShare = skill.source !== 'system' && !shared;
  return (
    <article className='cc-skillhub-local-card'>
      <div className='cc-skillhub-local-card-heading'><strong>{skill.name}</strong><span className={`cc-skillhub-status ${shared ? 'synced' : 'local'}`}>{shared ? '已发布' : '仅本地'}</span></div>
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
