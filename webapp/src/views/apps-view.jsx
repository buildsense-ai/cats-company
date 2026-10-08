import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AppWindow,
  ArrowLeft,
  Bot,
  ExternalLink,
  MoreHorizontal,
  Pencil,
  RefreshCw,
  Search,
  Star,
  X,
} from 'lucide-react';
import { api } from '../api';
import CustomSelect from '../widgets/custom-select';
import AppMetadataEditor from '../widgets/app-metadata-editor';
import '../css/apps-view.css';
import '../css/skillhub-view.css';

function formatUpdatedAt(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit' }).format(date);
}

function agentUID(agent) {
  return String(agent?.uid || agent?.id || '').trim();
}

function agentTitle(agent) {
  return String(agent?.display_name || agent?.displayName || agent?.username || `Agent ${agentUID(agent)}`).trim();
}

function agentOptionLabel(agent) {
  return `${agentTitle(agent)}${agent?.relation === 'friend' ? '（好友）' : ''}`;
}

function appTitle(app) {
  return String(app?.title || app?.name || app?.id || '未命名应用').trim();
}

function appSubtitle(app, agent) {
  const value = app?.subtitle || app?.owner_name || app?.organization;
  return String(value || agentTitle(agent)).trim();
}

function appDescription(app) {
  return String(app?.description || '在线打开共享的 HTML 文件，保留原应用的交互体验。').trim();
}

function appTags(app) {
  const tags = Array.isArray(app?.tags)
    ? app.tags.map((tag) => String(tag || '').trim()).filter(Boolean)
    : [];
  if (tags.length > 0) return tags.slice(0, 3);
  return ['HTML', '共享', '在线打开'];
}

function appIconURL(app) {
  return String(app?.icon_url || app?.avatar_url || app?.image_url || '').trim();
}

function normalizeApps(result) {
  if (Array.isArray(result)) return result;
  return Array.isArray(result?.apps) ? result.apps : [];
}

export function normalizeAccessibleAgents(response, userUID) {
  const agents = Array.isArray(response) ? response : (response?.agents || response?.bots || []);
  return agents
    .filter((agent) => {
      const uid = agentUID(agent);
      if (!uid) return false;
      if (agent?.relation === 'owner' || agent?.relation === 'friend') return true;
      if (agent?.is_owner !== undefined) return Boolean(agent.is_owner);
      const ownerUID = Number(agent?.owner_id || agent?.owner_uid || 0);
      return ownerUID > 0 && ownerUID === Number(userUID);
    })
    .map((agent) => ({
      ...agent,
      uid: agentUID(agent),
      relation: agent?.relation === 'friend' ? 'friend' : 'owner',
    }));
}

function appTimestamp(app) {
  return Date.parse(app?.updated_at) || Date.parse(app?.created_at) || 0;
}

function appKey(app) {
  return JSON.stringify([agentUID(app.sourceAgent), String(app.id || app.url)]);
}

function readFavorites(uid) {
  try {
    const value = JSON.parse(localStorage.getItem(`catsco.apps.favorites.${uid || 'guest'}`) || '[]');
    return Array.isArray(value) ? value.filter((key) => typeof key === 'string') : [];
  } catch {
    return [];
  }
}

function AppCardSkeletons() {
  return (
    <div className="cc-apps-grid cc-apps-skeleton-grid" aria-hidden="true">
      {Array.from({ length: 6 }, (_, index) => (
        <div className="cc-app-card cc-app-card-skeleton" key={index}>
          <div className="cc-app-skeleton-heading">
            <span className="cc-app-skeleton-block cc-app-skeleton-avatar" />
            <div className="cc-app-skeleton-copy">
              <span className="cc-app-skeleton-block cc-app-skeleton-title" />
              <span className="cc-app-skeleton-block cc-app-skeleton-agent" />
            </div>
          </div>
          <div className="cc-app-skeleton-description">
            <span className="cc-app-skeleton-block" />
            <span className="cc-app-skeleton-block" />
          </div>
          <div className="cc-app-skeleton-tags">
            <span className="cc-app-skeleton-block" />
          </div>
        </div>
      ))}
    </div>
  );
}

function AppCard({ app, agent, openingID, onOpen, onOpenWindow, onEdit, favorite, onToggleFavorite }) {
  const id = String(app?.id || app?.url || appTitle(app));
  const title = appTitle(app);
  const iconURL = appIconURL(app) || String(agent?.avatar_url || '').trim();
  const updatedAt = formatUpdatedAt(app?.updated_at || app?.created_at);
  return (
    <article className={`cc-app-card${app.can_manage === true ? ' is-manageable' : ''}`} key={id}>
      <button type="button" className="cc-app-card-button" onClick={() => onOpen(app)} disabled={openingID === String(app?.id || '')} aria-label={`打开应用 ${title}`}>
        <div className="cc-app-card-heading">
          <span className="cc-app-card-avatar" aria-hidden="true">
            {iconURL ? <img src={iconURL} alt="" /> : <AppWindow size={22} strokeWidth={1.8} />}
          </span>
          <span className="cc-app-card-heading-copy">
            <strong>{title}</strong>
            <span>{appSubtitle(app, agent)}</span>
          </span>
        </div>
        <p className="cc-app-card-description">{appDescription(app)}</p>
        <span className="cc-app-card-footer">
          {updatedAt && <span className="cc-app-card-updated">更新于 {updatedAt}</span>}
        </span>
        {openingID === String(app?.id || '') && <span className="cc-app-card-loading" role="status">正在打开…</span>}
      </button>
      <div className="cc-app-card-actions">
        <button type="button" className="cc-app-card-favorite" aria-pressed={favorite} onClick={() => onToggleFavorite(app)} aria-label={`${favorite ? '取消收藏' : '收藏'} ${title}`} title={favorite ? '取消收藏' : '收藏'}>
          <Star size={16} fill={favorite ? 'currentColor' : 'none'} aria-hidden="true" />
        </button>
        <button type="button" className="cc-app-card-external" onClick={() => onOpenWindow(app)} disabled={Boolean(openingID)} aria-label={`在新页面打开 ${title}`} title="新页面打开">
          <ExternalLink size={16} aria-hidden="true" />
        </button>
      </div>
      {app.can_manage === true && (
        <details className="cc-app-card-menu" onBlur={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget)) event.currentTarget.open = false;
        }} onKeyDown={(event) => {
          if (event.key === 'Escape') {
            event.currentTarget.open = false;
            event.currentTarget.querySelector('summary').focus();
          }
        }}>
          <summary aria-label={`管理应用 ${title}`} title="管理应用"><MoreHorizontal size={18} aria-hidden="true" /></summary>
          <div className="cc-app-card-menu-items">
            <button type="button" onClick={(event) => {
              const menu = event.currentTarget.closest('details');
              menu.open = false;
              menu.querySelector('summary').focus();
              onEdit(app);
            }}><Pencil size={15} aria-hidden="true" />编辑信息</button>
          </div>
        </details>
      )}
    </article>
  );
}

export default function AppsView({ user = null, topicId = '' }) {
  const [agents, setAgents] = useState([]);
  const [selectedAgentUID, setSelectedAgentUID] = useState('');
  const [apps, setApps] = useState([]);
  const [loading, setLoading] = useState(true);
  const [hasLoaded, setHasLoaded] = useState(false);
  const [showSkeletons, setShowSkeletons] = useState(false);
  const [failedAgentUIDs, setFailedAgentUIDs] = useState([]);
  const [error, setError] = useState('');
  const [selectedApp, setSelectedApp] = useState(null);
  const [openingID, setOpeningID] = useState('');
  const [manageableOnly, setManageableOnly] = useState(false);
  const [favoritesOnly, setFavoritesOnly] = useState(false);
  const [query, setQuery] = useState('');
  const [favoriteState, setFavoriteState] = useState(() => ({ uid: user?.uid, keys: readFavorites(user?.uid) }));
  const favoriteKeys = favoriteState.uid === user?.uid ? favoriteState.keys : readFavorites(user?.uid);
  const toggleFavorite = (app) => {
    const key = appKey(app);
    const keys = favoriteKeys.includes(key) ? favoriteKeys.filter((item) => item !== key) : [...favoriteKeys, key];
    setFavoriteState({ uid: user?.uid, keys });
    try { localStorage.setItem(`catsco.apps.favorites.${user?.uid || 'guest'}`, JSON.stringify(keys)); } catch { /* Keep favorites available for this session. */ }
  };
  const [editingApp, setEditingApp] = useState(null);
  const appsRequestRef = useRef(0);

  useEffect(() => {
    if (!loading || hasLoaded) {
      setShowSkeletons(false);
      return undefined;
    }
    const timer = window.setTimeout(() => setShowSkeletons(true), 150);
    return () => window.clearTimeout(timer);
  }, [loading, hasLoaded]);

  const loadApps = useCallback(async () => {
    const requestID = ++appsRequestRef.current;
    setLoading(true);
    setError('');
    setFailedAgentUIDs([]);
    try {
      const result = await api.getMyBots();
      if (requestID !== appsRequestRef.current) return;
      const accessible = [...new Map(normalizeAccessibleAgents(result, user?.uid)
        .map((agent) => [agentUID(agent), agent])).values()];
      accessible.sort((left, right) => agentTitle(left).localeCompare(agentTitle(right), 'zh-CN'));
      setAgents(accessible);
      setSelectedAgentUID((current) => accessible.some((agent) => agentUID(agent) === current) ? current : '');
      const results = await Promise.allSettled(accessible.map(async (agent) => ({
        agent,
        apps: normalizeApps(await api.listArtifactApps(agentUID(agent))),
      })));
      if (requestID !== appsRequestRef.current) return;
      const nextApps = [];
      const failed = [];
      results.forEach((result, index) => {
        if (result.status === 'rejected') {
          failed.push(agentUID(accessible[index]));
          return;
        }
        const { agent, apps: agentApps } = result.value;
        const seen = new Set();
        agentApps.forEach((app) => {
          const key = String(app?.id || app?.url || '');
          if (!key || seen.has(key)) return;
          seen.add(key);
          nextApps.push({ ...app, sourceAgent: agent });
        });
      });
      setApps(nextApps);
      setFailedAgentUIDs(failed);
    } catch {
      if (requestID !== appsRequestRef.current) return;
      setError('Agent 列表暂时无法加载，请稍后重试。');
    } finally {
      if (requestID === appsRequestRef.current) {
        setLoading(false);
        setHasLoaded(true);
      }
    }
  }, [user?.uid]);

  useEffect(() => {
    setSelectedAgentUID('');
    setSelectedApp(null);
    setEditingApp(null);
    setManageableOnly(false);
    setFavoritesOnly(false);
    setQuery('');
    setFavoriteState({ uid: user?.uid, keys: readFavorites(user?.uid) });
    setApps([]);
    setHasLoaded(false);
    setShowSkeletons(false);
    loadApps();
    return () => { appsRequestRef.current += 1; };
  }, [loadApps]);

  const visibleApps = useMemo(() => apps
    .filter((app) => !selectedAgentUID || agentUID(app.sourceAgent) === selectedAgentUID)
    .filter((app) => !manageableOnly || app.can_manage === true)
    .filter((app) => !favoritesOnly || favoriteKeys.includes(appKey(app)))
    .filter((app) => !query.trim() || [appTitle(app), appDescription(app), appSubtitle(app, app.sourceAgent), ...appTags(app)]
      .some((value) => value.toLocaleLowerCase().includes(query.trim().toLocaleLowerCase())))
    .sort((left, right) => appTimestamp(right) - appTimestamp(left)
      || appTitle(left).localeCompare(appTitle(right), 'zh-CN')), [apps, selectedAgentUID, manageableOnly, favoritesOnly, favoriteKeys, query]);
  const selectedAgent = agents.find((agent) => agentUID(agent) === selectedAgentUID);
  const emptyTitle = query.trim() ? '没有找到匹配的应用' : favoritesOnly ? '当前范围内还没有收藏的应用' : manageableOnly ? '当前范围内没有可管理的应用' : selectedAgent ? '这个 Agent 还没有共享应用' : '还没有共享应用';
  const emptyCopy = query.trim() ? '换一个关键词，或清除搜索后查看全部。' : favoritesOnly ? '点击应用卡片上的星标，即可在这里快速找到它。' : manageableOnly ? '切换到「全部应用」可继续浏览共享应用。' : 'Agent 分享的应用会出现在这里。';
  const listError = error || (failedAgentUIDs.includes(selectedAgentUID)
    ? '这个 Agent 的应用暂时无法加载，请重试。'
    : !selectedAgentUID && failedAgentUIDs.length > 0
      ? (visibleApps.length > 0 ? '部分 Agent 的应用加载失败，已展示其余应用。' : '共享应用暂时无法加载，请重试。')
      : '');

  const openApp = useCallback(async (app, target = 'viewer') => {
    if (!app?.id || !app?.url) return;
    const id = String(app.id);
    setOpeningID(id);
    let viewerURL = app.url;
    try {
      const launch = await api.requestArtifactLaunch({ app: id, topic_id: topicId });
      if (launch?.launch_url) viewerURL = launch.launch_url;
    } catch {
      // A public app can still open as a guest when the launch handoff fails.
    }
    setOpeningID('');
    if (target === 'window') {
      window.open(viewerURL, '_blank', 'noopener,noreferrer');
      return;
    }
    setSelectedApp({ ...app, viewerURL });
  }, [topicId]);

  const pageTitle = useMemo(() => selectedApp
    ? appTitle(selectedApp)
    : selectedAgent ? `${agentTitle(selectedAgent)}的应用` : '应用', [selectedAgent, selectedApp]);

  if (selectedApp) {
    return (
      <section className="cc-apps-page cc-apps-viewer-page" aria-label="应用预览">
        <div className="cc-apps-viewer-header">
          <button type="button" className="cc-apps-back-button" onClick={() => setSelectedApp(null)} aria-label="返回应用列表">
            <ArrowLeft size={17} aria-hidden="true" /><span>返回应用</span>
          </button>
          <div className="cc-apps-viewer-title" title={pageTitle}>{pageTitle}</div>
          <button type="button" className="cc-apps-viewer-external" onClick={() => openApp(selectedApp, 'window')}>
            <ExternalLink size={16} aria-hidden="true" /><span>新页面打开</span>
          </button>
        </div>
        <iframe className="cc-apps-viewer-frame" src={selectedApp.viewerURL || selectedApp.url} title={pageTitle} sandbox="allow-scripts allow-forms allow-same-origin allow-popups allow-modals" referrerPolicy="no-referrer" />
      </section>
    );
  }

  return (
    <section className="cc-apps-page" aria-label="共享应用" aria-busy={loading}>
      <header className="cc-apps-header">
        <div>
          <h1>应用</h1>
          <p className="cc-apps-intro">{selectedAgent ? `${agentTitle(selectedAgent)}分享的应用` : '来自我的 Agent 和好友 Agent 的共享应用'}</p>
        </div>
        <div className="cc-skillhub-agent-context cc-apps-agent-filter">
          <label className="cc-skillhub-bot-picker">
            <span className="cc-skillhub-agent-label"><Bot size={15} aria-hidden="true" /> 来源 Agent</span>
            <span className="cc-skillhub-select-wrap">
              <CustomSelect
                ariaLabel="选择 Agent"
                className="cc-skillhub-agent-select"
                density="comfortable"
                listboxAriaLabel="Agent 列表"
                menuClassName="cc-skillhub-agent-options"
                selectedLabelTitle={selectedAgent ? agentOptionLabel(selectedAgent) : '全部 Agent'}
                triggerClassName="cc-skillhub-agent-select-trigger"
                value={selectedAgentUID}
                onValueChange={setSelectedAgentUID}
              >
                <option value="" data-title="全部 Agent">全部 Agent</option>
                {agents.map((agent) => <option key={agentUID(agent)} value={agentUID(agent)} data-title={agentOptionLabel(agent)}>{agentOptionLabel(agent)}</option>)}
              </CustomSelect>
            </span>
          </label>
        </div>
      </header>
      <div className="cc-apps-toolbar">
        <div className="cc-apps-toolbar-filters">
          <div className="cc-apps-scope" role="group" aria-label="应用范围">
            <button type="button" aria-pressed={!manageableOnly && !favoritesOnly} onClick={() => { setManageableOnly(false); setFavoritesOnly(false); }}>全部应用</button>
            <button type="button" aria-pressed={favoritesOnly} onClick={() => { setManageableOnly(false); setFavoritesOnly(true); }}><Star size={15} aria-hidden="true" />收藏</button>
            <button type="button" aria-pressed={manageableOnly} onClick={() => { setManageableOnly(true); setFavoritesOnly(false); }}>我可管理</button>
          </div>
        </div>
        <div className="cc-apps-toolbar-actions">
          <div className="cc-library-search" role="search">
            <Search size={16} aria-hidden="true" />
            <input type="search" aria-label="搜索应用" placeholder="搜索应用…" value={query} onChange={(event) => setQuery(event.target.value)} />
            {query && <button type="button" aria-label="清除应用搜索" title="清除" onClick={() => setQuery('')}><X size={15} aria-hidden="true" /></button>}
          </div>
        <button type="button" className="cc-apps-refresh" onClick={loadApps} disabled={loading} aria-label="刷新应用列表" title="刷新应用列表">
          <RefreshCw size={17} aria-hidden="true" className={loading ? 'is-spinning' : ''} />
        </button>
        </div>
      </div>

      {loading && <span className="cc-apps-loading-announcement" role="status">正在加载共享应用…</span>}
      {loading && !hasLoaded && <div className="cc-apps-initial-loading">{showSkeletons && <AppCardSkeletons />}</div>}
      {!loading && listError && <div className="cc-apps-state is-error" role="alert"><span>{listError}</span><button type="button" onClick={loadApps}>重试</button></div>}
      {!loading && !listError && visibleApps.length === 0 && <div className="cc-apps-state is-empty"><AppWindow size={24} aria-hidden="true" /><strong>{emptyTitle}</strong><span>{emptyCopy}</span></div>}
      {visibleApps.length > 0 && (
        <div className="cc-apps-grid cc-apps-results">
          {visibleApps.map((app) => <AppCard key={appKey(app)} app={app} agent={app.sourceAgent} openingID={openingID} onOpen={openApp} onOpenWindow={(nextApp) => openApp(nextApp, 'window')} onEdit={setEditingApp} favorite={favoriteKeys.includes(appKey(app))} onToggleFavorite={toggleFavorite} />)}
        </div>
      )}
      {editingApp && <AppMetadataEditor key={`${user?.uid}:${editingApp.id}`} app={editingApp} onClose={() => setEditingApp(null)} onSaved={(updated) => {
        // An earlier list refresh must not overwrite the just-saved metadata.
        appsRequestRef.current += 1;
        setLoading(false);
        setApps((current) => current.map((app) => app.id === editingApp.id && agentUID(app.sourceAgent) === agentUID(editingApp.sourceAgent)
          ? { ...app, ...updated, sourceAgent: app.sourceAgent } : app));
        setEditingApp(null);
      }} />}
    </section>
  );
}
