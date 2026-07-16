import React, { useState, useEffect, useCallback, useRef } from 'react';
import { api, setToken, getToken, connectWS, reconnectWS, disconnectWS } from '../api';
import t from '../i18n';
import ChatListView from './sidepanel-view';
import FriendsView from './friends-view';
import MessagesView from './messages-view';
import AgentEntryBindView from './agent-entry-bind-view';
import ChannelDeviceLinkView from './channel-device-link-view';
import MobileUploadView from './mobile-upload-view';
import ProfileEditor from '../widgets/profile-editor';
import FeedbackModal from '../widgets/feedback-modal';
import CatsCoDownloadModal from '../widgets/catsco-download-modal';
import DesktopConnectModal from '../widgets/desktop-connect-modal';
import RelayAccessModal from '../widgets/relay-access-modal';
import PasswordResetForm from '../widgets/password-reset-form';
import WorkflowRichMediaDemo from './workflow-rich-media-demo';
import Avatar from '../widgets/avatar';
import { Bug, Download, KeyRound, Laptop, Settings, LogOut, Eye, EyeOff, CheckCircle2, Menu, PanelLeftOpen, Sun, Moon, Plus, ArrowUp, ChevronDown, Check } from 'lucide-react';
import '../css/openchat-theme.css';
import '../css/catsco-ui-system.css';

const TABS = {
  CHATS: 'chats'
};
const APP_SIDEBAR_COLLAPSED_STORAGE_KEY = 'cc_app_sidebar_collapsed_v1';
const MODEL_STORAGE_KEY = 'catsco_selected_model';
const MODEL_OPTIONS = [
  { id: 'minimax-m2.7', name: 'MiniMax-M2.7', description: '标准额度 · Anthropic SDK' },
  { id: 'minimax-m3', name: 'MiniMax-M3', description: '多模态 · Anthropic SDK' },
  { id: 'gpt-5.6', name: 'GPT-5.6', description: '通用推理与编程' },
  { id: 'opus-4.8', name: 'Opus-4.8', description: '复杂分析与长文' },
  { id: 'fable-5', name: 'Fable-5', description: '创意写作与表达' },
  { id: 'glm-5.1', name: 'GLM-5.1', description: '中文任务与知识' },
];
const DEV_PREVIEW_ENABLED = import.meta.env.DEV && import.meta.env.VITE_DEV_BYPASS_AUTH === 'true';
const DEV_PREVIEW_USER = {
  uid: 'local-preview',
  username: 'preview',
  email: '',
  display_name: '本地预览',
  avatar_url: '',
  account_type: 'human',
};

function normalizeUserProfile(raw) {
  if (!raw) return null;
  const username = raw.username || '';
  return {
    uid: raw.uid || raw.id,
    username,
    email: raw.email || '',
    display_name: raw.display_name || username,
    avatar_url: raw.avatar_url || '',
    account_type: raw.account_type || 'human',
  };
}

function getInitialUser() {
  if (DEV_PREVIEW_ENABLED) return DEV_PREVIEW_USER;

  const token = getToken();
  if (!token) return null;

  try {
    const saved = localStorage.getItem('oc_user');
    return saved ? normalizeUserProfile(JSON.parse(saved)) : null;
  } catch (error) {
    console.warn('Failed to restore saved user from localStorage:', error);
    localStorage.removeItem('oc_user');
    return null;
  }
}

function loadAppSidebarCollapsed() {
  if (typeof window === 'undefined' || !window.localStorage) return false;
  return window.localStorage.getItem(APP_SIDEBAR_COLLAPSED_STORAGE_KEY) === 'true';
}

function saveAppSidebarCollapsed(collapsed) {
  if (typeof window === 'undefined' || !window.localStorage) return;
  window.localStorage.setItem(APP_SIDEBAR_COLLAPSED_STORAGE_KEY, collapsed ? 'true' : 'false');
}

function lastTopicStorageKey(uid) {
  return uid ? `v3_last_topic:${uid}` : 'v3_last_topic';
}

function normalizeActiveTopic(value) {
  if (!value) return null;

  if (typeof value === 'string') {
    if (!value || value === '[object Object]') return null;
    return { topicId: value, name: '' };
  }

  if (typeof value === 'object' && value.topicId) {
    return {
      topicId: value.topicId,
      name: value.name || '',
      isGroup: Boolean(value.isGroup),
      groupId: value.groupId,
      avatar_url: value.avatar_url || '',
      friendId: value.friendId,
    };
  }

  return null;
}

function readStoredTopic(uid) {
  const keys = [lastTopicStorageKey(uid), 'v3_last_topic'];
  for (const key of keys) {
    const raw = localStorage.getItem(key);
    if (!raw) continue;

    try {
      const parsed = JSON.parse(raw);
      const topic = normalizeActiveTopic(parsed);
      if (topic) return topic;
    } catch (error) {
      const topic = normalizeActiveTopic(raw);
      if (topic) return topic;
    }
  }

  return null;
}

function writeStoredTopic(uid, topic) {
  const key = lastTopicStorageKey(uid);
  const normalized = normalizeActiveTopic(topic);
  if (!normalized) {
    localStorage.removeItem(key);
    localStorage.removeItem('v3_last_topic');
    return;
  }

  localStorage.setItem(key, JSON.stringify(normalized));
  localStorage.setItem('v3_last_topic', JSON.stringify(normalized));
}

function desktopPromptStorageKey(uid) {
  return `catsco_desktop_connect_prompted:v1:${uid}`;
}

function todayKey() {
  const now = new Date();
  const year = now.getFullYear();
  const month = String(now.getMonth() + 1).padStart(2, '0');
  const day = String(now.getDate()).padStart(2, '0');
  return `${year}-${month}-${day}`;
}

function findConnectedLocalAgent(agents) {
  return (agents || []).find((agent) => agent.relation === 'owner' && agent.is_online);
}

export default function TinodeWeb() {
  const mobileUploadMatch = window.location.pathname.match(/^\/mobile-upload\/([^/]+)$/);
  if (mobileUploadMatch) {
    return <MobileUploadView sessionId={decodeURIComponent(mobileUploadMatch[1])} />;
  }

  const demoParams = new URLSearchParams(window.location.search);
  const showWorkflowDemo = demoParams.get('workflow_demo') === '1';
  if (showWorkflowDemo) {
    return <WorkflowRichMediaDemo />;
  }

  return <TinodeWebApp />;
}

function TinodeWebApp() {
  const entryMatch = window.location.pathname.match(/^\/e\/([^/]+)$/);
  const entrySceneKey = entryMatch ? decodeURIComponent(entryMatch[1]) : '';
  const channelDeviceLink = window.location.pathname === '/channel-device-link';
  const [user, setUser] = useState(() => getInitialUser());
  const [activeTab, setActiveTab] = useState(TABS.CHATS);
  const [activeTopic, _setActiveTopic] = useState(null);

  const setActiveTopic = useCallback((nextValue) => {
    _setActiveTopic((prev) => {
      const next = typeof nextValue === 'function' ? nextValue(prev) : nextValue;
      const normalized = normalizeActiveTopic(next);
      writeStoredTopic(user?.uid, normalized);
      return normalized;
    });
  }, [user?.uid]);
  const [authMode, setAuthMode] = useState('login');
  const [onlineUsers, setOnlineUsers] = useState({});
  const [wsStatus, setWsStatus] = useState(user ? 'connecting' : 'disconnected');
  const [showProfileEditor, setShowProfileEditor] = useState(false);
  const [showProfilePopover, setShowProfilePopover] = useState(false);
  const profilePopoverRef = useRef(null);
  const [showFeedbackModal, setShowFeedbackModal] = useState(false);
  const [showDownloadModal, setShowDownloadModal] = useState(false);
  const [showDesktopConnectModal, setShowDesktopConnectModal] = useState(false);
  const [localAgentStatus, setLocalAgentStatus] = useState('checking');
  const [showRelayModal, setShowRelayModal] = useState(false);
  const [appSidebarCollapsed, setAppSidebarCollapsed] = useState(() => loadAppSidebarCollapsed());
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const [tutorialOpenToken, setTutorialOpenToken] = useState(0);
  const [showTutorialMenuHint, setShowTutorialMenuHint] = useState(false);
  const [theme, setTheme] = useState(() => localStorage.getItem('catsco_theme') || 'light');
  const [selectedModel, setSelectedModel] = useState(() => {
    const stored = localStorage.getItem(MODEL_STORAGE_KEY);
    return MODEL_OPTIONS.some((model) => model.id === stored) ? stored : MODEL_OPTIONS[0].id;
  });

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem('catsco_theme', theme);
  }, [theme]);

  useEffect(() => {
    localStorage.setItem(MODEL_STORAGE_KEY, selectedModel);
  }, [selectedModel]);

  useEffect(() => {
    if (!showProfilePopover) return undefined;

    const handlePointerDown = (event) => {
      const target = event.target;
      if (profilePopoverRef.current?.contains(target)) return;
      if (target?.closest?.('.v3-profile-footer')) return;
      setShowProfilePopover(false);
    };
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') setShowProfilePopover(false);
    };

    document.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [showProfilePopover]);



  const persistUser = useCallback((nextUser) => {
    localStorage.setItem('oc_user', JSON.stringify(nextUser));
    setUser(nextUser);
  }, []);

  const toggleAppSidebar = useCallback(() => {
    if (window.matchMedia('(max-width: 768px)').matches) {
      setMobileSidebarOpen((open) => !open);
      setAppSidebarCollapsed(false);
      saveAppSidebarCollapsed(false);
      setShowProfilePopover(false);
      return;
    }
    setAppSidebarCollapsed((prev) => {
      const next = !prev;
      saveAppSidebarCollapsed(next);
      if (next) setShowProfilePopover(false);
      return next;
    });
  }, []);

  useEffect(() => {
    if (!mobileSidebarOpen) return undefined;

    const handleKeyDown = (event) => {
      if (event.key === 'Escape') setMobileSidebarOpen(false);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [mobileSidebarOpen]);

  // WebSocket message handler
  const handleWSMessage = useCallback((msg) => {
    if (msg._type === 'ws_auth_expired') {
      disconnectWS();
      setToken(null);
      localStorage.removeItem('oc_user');
      setUser(null);
      setActiveTopic(null);
      return;
    }
    if (msg._type === 'ws_open') {
      setWsStatus('connected');
      return;
    }
    if (msg._type === 'ws_connecting') {
      setWsStatus(msg.attempt > 0 ? 'reconnecting' : 'connecting');
      return;
    }
    if (msg._type === 'ws_close') {
      setWsStatus('reconnecting');
      return;
    }

    if (msg.meta && msg.meta.sub) {
      setOnlineUsers((prev) => {
        const next = { ...prev };
        for (const u of msg.meta.sub) {
          if (!u.uid) continue;
          next[u.uid] = Boolean(u.online);
        }
        return next;
      });
    }

    if (msg.pres) {
      const uid = parseUid(msg.pres.src);
      if (uid > 0) {
        setOnlineUsers((prev) => {
          const next = { ...prev };
          if (msg.pres.what === 'on') {
            next[uid] = true;
          } else if (msg.pres.what === 'off') {
            next[uid] = false;
          }
          return next;
        });
      }
    }
  }, [setActiveTopic]);

  useEffect(() => {
    if (user?.uid) {
      connectWS(handleWSMessage);
    }
    return () => {
      if (user?.uid) disconnectWS();
    };
  }, [user?.uid, handleWSMessage]);

  useEffect(() => {
    if (!user?.uid) return undefined;

    let hiddenAt = 0;
    const recoverConnection = (force = false) => {
      if (document.visibilityState !== 'visible' || navigator.onLine === false) return;
      if (force) reconnectWS(handleWSMessage);
      else connectWS(handleWSMessage);
    };
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'hidden') {
        hiddenAt = Date.now();
        return;
      }
      const suspendedLongEnoughToStale = hiddenAt > 0 && Date.now() - hiddenAt >= 30000;
      recoverConnection(suspendedLongEnoughToStale);
      hiddenAt = 0;
    };
    const handlePageShow = (event) => recoverConnection(Boolean(event.persisted));
    const handleOnline = () => recoverConnection(true);
    const handleFocus = () => recoverConnection(false);

    document.addEventListener('visibilitychange', handleVisibilityChange);
    window.addEventListener('pageshow', handlePageShow);
    window.addEventListener('online', handleOnline);
    window.addEventListener('focus', handleFocus);
    return () => {
      document.removeEventListener('visibilitychange', handleVisibilityChange);
      window.removeEventListener('pageshow', handlePageShow);
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('focus', handleFocus);
    };
  }, [user?.uid, handleWSMessage]);

  useEffect(() => {
    if (!user?.uid) {
      _setActiveTopic(null);
      return;
    }

    _setActiveTopic(readStoredTopic(user.uid));
  }, [user?.uid]);

  useEffect(() => {
    if (!user?.uid) return;
    if (DEV_PREVIEW_ENABLED) return undefined;

    let cancelled = false;
    api.getMe()
      .then((profile) => {
        if (!cancelled) {
          const normalized = normalizeUserProfile(profile);
          if (normalized) persistUser(normalized);
        }
      })
      .catch((error) => {
        console.warn('Failed to refresh current user profile:', error);
        if (!cancelled && error?.status === 401) {
          disconnectWS();
          setToken(null);
          localStorage.removeItem('oc_user');
          setUser(null);
          setActiveTopic(null);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [user?.uid, persistUser]);

  const refreshLocalAgentStatus = useCallback(async ({ allowDailyPrompt = false } = {}) => {
    if (!user?.uid) return;
    try {
      setLocalAgentStatus((status) => (status === 'connected' ? status : 'checking'));
      const res = await api.getAgents();
      const connected = findConnectedLocalAgent(res.agents || []);
      if (connected) {
        setLocalAgentStatus('connected');
        return;
      }
      setLocalAgentStatus('disconnected');
      if (allowDailyPrompt) {
        const promptKey = desktopPromptStorageKey(user.uid);
        if (localStorage.getItem(promptKey) !== todayKey()) {
          localStorage.setItem(promptKey, todayKey());
          setShowDesktopConnectModal(true);
        }
      }
    } catch (error) {
      console.warn('Failed to check desktop agent connection:', error);
      setLocalAgentStatus('unknown');
    }
  }, [user?.uid]);

  useEffect(() => {
    if (!user?.uid) return undefined;
    let cancelled = false;
    refreshLocalAgentStatus({ allowDailyPrompt: true }).catch(() => {
      if (!cancelled) setLocalAgentStatus('unknown');
    });
    const onDataChanged = () => refreshLocalAgentStatus().catch(() => {});
    window.addEventListener('cc:data-changed', onDataChanged);
    return () => {
      cancelled = true;
      window.removeEventListener('cc:data-changed', onDataChanged);
    };
  }, [user?.uid, refreshLocalAgentStatus]);

  useEffect(() => {
    if (!user?.uid) return;

    const params = new URLSearchParams(window.location.search);
    if (params.get('relay_login') !== '1') return;

    let cancelled = false;
    const fallBackToRelayPanel = () => {
      params.delete('relay_login');
      const nextSearch = params.toString();
      const nextUrl = `${window.location.pathname}${nextSearch ? `?${nextSearch}` : ''}${window.location.hash}`;
      window.history.replaceState(null, '', nextUrl);
      setShowRelayModal(true);
    };

    api.createRelaySession()
      .then((session) => {
        if (!cancelled && session?.url) {
          window.location.href = session.url;
        } else if (!cancelled) {
          fallBackToRelayPanel();
        }
      })
      .catch((error) => {
        console.warn('Failed to create relay login session:', error);
        if (!cancelled) {
          fallBackToRelayPanel();
        }
      });

    return () => {
      cancelled = true;
    };
  }, [user?.uid]);

  const handleLogin = async (account, password) => {
    const res = await api.login({ account, password });
    setToken(res.token);
    persistUser(normalizeUserProfile(res));
  };

  const handleRegister = async (email, password, loginName, code) => {
    const username = loginName.trim();
    if (!username) {
      throw new Error('请输入登录名称');
    }
    if (username.length < 3) {
      throw new Error('登录名称至少 3 个字符');
    }
    await api.register({
      email,
      username,
      password,
      code,
    });
    await handleLogin(email, password);
  };

  const handleLogout = () => {
    disconnectWS();
    setToken(null);
    localStorage.removeItem('oc_user');
    setUser(null);
    setOnlineUsers({});
    setActiveTopic(null);
  };

  const handleUserUpdated = (nextUser) => {
    persistUser(normalizeUserProfile(nextUser));
    window.dispatchEvent(new Event('cc:data-changed'));
  };

  const handleTopicUpdated = (nextTopic) => {
    setActiveTopic((prev) => {
      if (!prev || prev.topicId !== nextTopic.topicId) return prev;
      return { ...prev, ...nextTopic };
    });
  };

  const handleDesktopConnected = async (agent) => {
    try {
      const agentUid = agent?.uid || agent?.id;
      if (!agentUid) {
        setLocalAgentStatus('connected');
        setShowDesktopConnectModal(false);
        window.dispatchEvent(new Event('cc:data-changed'));
        return;
      }
      const res = await api.openAgent(agentUid);
      const opened = res.agent || agent;
      setActiveTopic({
        topicId: opened.topic_id || res.topic || agent.topic_id,
        name: opened.display_name || agent.display_name || agent.username,
        isGroup: false,
        avatar_url: opened.avatar_url || agent.avatar_url,
        friendId: opened.uid || agent.uid || agent.id,
      });
      setLocalAgentStatus('connected');
      setShowDesktopConnectModal(false);
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (error) {
      console.warn('Failed to open connected desktop agent:', error);
    }
  };

  const handleSelectComposerAgent = useCallback(async (agent) => {
    const agentUid = agent?.uid || agent?.id;
    if (!agentUid) return;
    const res = await api.openAgent(agentUid);
    const opened = res.agent || agent;
    setActiveTopic({
      topicId: opened.topic_id || res.topic || agent.topic_id,
      name: opened.display_name || agent.display_name || agent.username,
      isGroup: false,
      avatar_url: opened.avatar_url || agent.avatar_url,
      friendId: opened.uid || agentUid,
    });
    window.dispatchEvent(new Event('cc:data-changed'));
  }, [setActiveTopic]);

  if (channelDeviceLink && user) {
    const params = new URLSearchParams(window.location.search);
    return (
      <ChannelDeviceLinkView
        bindingId={params.get('binding_id') || ''}
        linkToken={params.get('link_token') || ''}
        user={user}
      />
    );
  }

  if (!user) {
    return <AuthView mode={authMode} setMode={setAuthMode} onLogin={handleLogin} onRegister={handleRegister} />;
  }

  if (entrySceneKey) {
    return <AgentEntryBindView sceneKey={entrySceneKey} />;
  }

  return (
    <div className="v3-app">
      {mobileSidebarOpen && (
        <button
          type="button"
          className="v3-mobile-sidebar-backdrop"
          onClick={() => setMobileSidebarOpen(false)}
          aria-label="关闭左侧栏"
        />
      )}
      <div className={`v3-sidebar${appSidebarCollapsed ? ' collapsed' : ''}${mobileSidebarOpen ? ' open' : ''}`}>
        <div className="v3-sidebar-header">
          <div className="v3-brand-title">
            <span className="catsco-brand-mark" aria-hidden="true" />
            <span className="catsco-brand-name">CatsCo</span>
          </div>
          <button
            className="v3-sidebar-collapse-btn"
            type="button"
            onClick={toggleAppSidebar}
            aria-label={appSidebarCollapsed ? '展开左侧栏' : '收起左侧栏'}
            title={appSidebarCollapsed ? '展开左侧栏' : '收起左侧栏'}
          >
            {appSidebarCollapsed ? <span className="catsco-brand-mark" aria-hidden="true" /> : <Menu size={18} />}
          </button>
        </div>
        
        <div className="cc-sidebar-content-shell">
          <SidebarContent
            activeTopic={activeTopic ? activeTopic.topicId : null}
            onSelectTopic={(topic) => {
              setActiveTopic(topic);
              setMobileSidebarOpen(false);
            }}
            user={user}
            onlineUsers={onlineUsers}
          />
        </div>
        
        <ProfileFooter
          user={user}
          wsStatus={wsStatus}
          onTogglePopover={() => {
            if (!appSidebarCollapsed) setShowProfilePopover((open) => !open);
          }}
        />

        {showProfilePopover && !appSidebarCollapsed && (
          <div className="v3-profile-popover" ref={profilePopoverRef}>
            <div className="v3-popover-item v3-popover-status" aria-disabled="true">
              <CheckCircle2 size={16} style={{marginRight: 10}} /> 公司账号已连接
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowFeedbackModal(true); }}>
              <Bug size={16} style={{marginRight: 10}} /> 意见反馈
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowDownloadModal(true); }}>
              <Download size={16} style={{marginRight: 10}} /> 下载 CatsCo 桌面端
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowDesktopConnectModal(true); }}>
              <Laptop size={16} style={{marginRight: 10}} /> 连接我的电脑助手
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowRelayModal(true); }}>
              <KeyRound size={16} style={{marginRight: 10}} /> CatsCo 中转站
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowProfileEditor(true); }}>
              <Settings size={16} style={{marginRight: 10}} /> 设置与资料
            </div>
            <div className="v3-popover-item danger" onClick={() => { localStorage.clear(); window.location.reload(); }}>
              <LogOut size={16} style={{marginRight: 10}} /> 退出登录
            </div>
          </div>
        )}
      </div>
      
      <div className="v3-main">
        <button
          type="button"
          className="v3-mobile-sidebar-toggle"
          onClick={() => {
            setAppSidebarCollapsed(false);
            saveAppSidebarCollapsed(false);
            setMobileSidebarOpen(true);
          }}
          aria-label="打开左侧栏"
          aria-expanded={mobileSidebarOpen}
        >
          <PanelLeftOpen size={18} />
        </button>
        <LocalAssistantBar
          status={localAgentStatus}
          onConnect={() => setShowDesktopConnectModal(true)}
          selectedModel={selectedModel}
          onSelectModel={setSelectedModel}
          theme={theme}
          onToggleTheme={() => setTheme((value) => value === 'light' ? 'dark' : 'light')}
          onDownload={() => setShowDownloadModal(true)}
          title={activeTopic?.name || '新对话'}
        />
        {activeTopic ? (
          <MessagesView
            topic={activeTopic.topicId}
            topicName={activeTopic.name}
            user={user}
            isGroup={activeTopic.isGroup || (activeTopic.topicId && activeTopic.topicId.startsWith('grp_'))}
            groupId={activeTopic.groupId}
            topicAvatarUrl={activeTopic.avatar_url}
            onTopicUpdated={handleTopicUpdated}
            tutorialOpenToken={tutorialOpenToken}
            localAssistantStatus={localAgentStatus}
            onOpenDesktopConnect={() => setShowDesktopConnectModal(true)}
            onSelectAgent={handleSelectComposerAgent}
            onTutorialHint={() => setShowTutorialMenuHint(true)}
          />
        ) : <NoActiveTask onCreate={() => window.dispatchEvent(new Event('catsco:new-task'))} />}
      </div>

      {showProfileEditor && (
        <ProfileEditor
          user={user}
          onClose={() => setShowProfileEditor(false)}
          onSaved={handleUserUpdated}
          onOpenRelay={() => setShowRelayModal(true)}
        />
      )}

      {showFeedbackModal && (
        <FeedbackModal user={user} onClose={() => setShowFeedbackModal(false)} />
      )}

      {showDownloadModal && (
        <CatsCoDownloadModal onClose={() => setShowDownloadModal(false)} />
      )}

      {showDesktopConnectModal && (
        <DesktopConnectModal
          onClose={() => setShowDesktopConnectModal(false)}
          onConnected={handleDesktopConnected}
          onStatusChange={(status) => setLocalAgentStatus(status)}
        />
      )}

      {showRelayModal && (
        <RelayAccessModal onClose={() => setShowRelayModal(false)} />
      )}

      {showTutorialMenuHint && (
        <div className="cc-tutorial-menu-hint">
          <button type="button" onClick={() => setShowTutorialMenuHint(false)} aria-label="关闭">x</button>
          以后可从头像菜单里的“示例任务”重新打开。
        </div>
      )}
    </div>
  );
}

function LocalAssistantBar({ status, onConnect, selectedModel, onSelectModel, theme, onToggleTheme, onDownload, title }) {
  const connected = status === 'connected';
  const checking = status === 'checking';
  const [menuOpen, setMenuOpen] = useState(false);
  const currentModel = MODEL_OPTIONS.find((model) => model.id === selectedModel) || MODEL_OPTIONS[0];
  const statusClass = connected ? 'connected' : checking ? 'connecting' : 'disconnected';
  return (
    <header className="v3-local-assistant-bar">
      <div className="v3-model-select">
        <button
          type="button"
          className={`v3-local-assistant-status ${statusClass}`}
          onClick={() => setMenuOpen((open) => !open)}
          aria-expanded={menuOpen}
          aria-haspopup="listbox"
        >
          <span className="v3-model-status-dot" aria-hidden="true" />
          <span>{currentModel.name}</span>
          <ChevronDown size={14} className={menuOpen ? 'is-open' : ''} />
        </button>
        {menuOpen && (
          <div className="v3-model-menu" role="listbox" aria-label="选择模型">
            {MODEL_OPTIONS.map((model) => (
              <button
                type="button"
                role="option"
                aria-selected={model.id === selectedModel}
                className={`v3-model-option${model.id === selectedModel ? ' selected' : ''}`}
                key={model.id}
                onClick={() => {
                  onSelectModel(model.id);
                  setMenuOpen(false);
                  if (!connected) onConnect();
                }}
              >
                <span><strong>{model.name}</strong><small>{model.description}</small></span>
                {model.id === selectedModel && <Check size={15} />}
              </button>
            ))}
            {!connected && (
              <button type="button" className="v3-model-connect" onClick={() => { setMenuOpen(false); onConnect(); }}>
                {checking ? '正在连接桌面端…' : '连接本地模型'}
              </button>
            )}
          </div>
        )}
      </div>
      <strong className="v3-shell-title">{title}</strong>
      <div className="v3-shell-actions">
        <button type="button" className="v3-action-btn" onClick={onToggleTheme} aria-label="切换日夜模式">
          {theme === 'light' ? <Sun size={17} /> : <Moon size={17} />}
        </button>
        <button type="button" className="v3-action-btn" onClick={onDownload} aria-label="下载桌面端">
          <Download size={17} />
        </button>
      </div>
    </header>
  );
}

function NoActiveTask({ onCreate }) {
  return (
    <main className="cc-empty-task">
      <div className="cc-empty-task-inner">
        <span className="catsco-brand-mark cc-empty-task-mark" aria-hidden="true" />
        <h1>需要我为您做什么？</h1>
        <div className="cc-empty-composer">
          <button type="button" className="v3-tool cc-empty-tool" onClick={onCreate} aria-label="添加文件或图片">
            <Plus size={20} />
          </button>
          <button type="button" className="cc-empty-composer-input" onClick={onCreate}>输入指令，我帮您完成</button>
          <button type="button" className="v3-agent-picker-button cc-empty-agent" onClick={onCreate}>
            <span>选择 Agent</span><ChevronDown size={14} />
          </button>
          <button type="button" className="cc-empty-send" onClick={onCreate} aria-label="开始任务"><ArrowUp size={17} /></button>
        </div>
        <p>Enter 发送 · Shift+Enter 换行 · Ctrl+Enter 发送 · Ctrl+B 折叠侧栏 · 点击红色按钮停止生成</p>
      </div>
    </main>
  );
}

function SidebarContent({ activeTopic, onSelectTopic, user, onlineUsers }) {
  return <ChatListView activeTopic={activeTopic} onSelectTopic={onSelectTopic} user={user} onlineUsers={onlineUsers} />;
}

function ProfileFooter({ user, wsStatus, onTogglePopover }) {
  const connected = wsStatus === 'connected';
  const reconnecting = wsStatus === 'connecting' || wsStatus === 'reconnecting';
  const statusClass = connected ? 'online' : reconnecting ? 'reconnecting' : 'offline';
  const statusLabel = connected ? '在线' : wsStatus === 'connecting' ? '连接中' : reconnecting ? '重新连接中' : '离线';
  const displayName = user.display_name || user.username;
  return (
    <div className="v3-profile-footer" onClick={onTogglePopover} style={{cursor: 'pointer'}}>
      <Avatar name={displayName} src={user.avatar_url} size={32} className="v3-profile-avatar" />
      <div className="v3-profile-info">
        <div className="v3-profile-name">{displayName}</div>
        <div className="v3-profile-roles">
           <span className={`v3-status-dot ${statusClass}`} style={{marginLeft: 0, marginRight: 6}}></span>
           {statusLabel}
        </div>
      </div>
      <div className="v3-profile-settings" style={{color: '#888'}}>
        <Settings size={18} />
      </div>
    </div>
  );
}

function formatAuthError(message) {
  const text = String(message || '').toLowerCase();
  if (text.includes('user not found')) return '账号不存在，请检查用户名或邮箱';
  if (text.includes('password mismatch')) return '密码错误，请重试';
  if (text.includes('username taken')) return '登录名称已被占用，请换一个';
  if (text.includes('email already')) return '该邮箱已经注册，请直接登录';
  if (text.includes('invalid or expired verification code')) return '验证码无效或已过期';
  if (text.includes('username min 3')) return '登录名称至少 3 个字符';
  if (text.includes('password min 6')) return '密码至少 6 位';
  if (text.includes('failed to send verification code')) return '发送验证码失败，请稍后再试';
  return message || '操作失败，请稍后再试';
}

function AuthView({ mode, setMode, onLogin, onRegister }) {
  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [loginName, setLoginName] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [codeSent, setCodeSent] = useState(false);
  const [countdown, setCountdown] = useState(0);

  useEffect(() => {
    if (countdown > 0) {
      const timer = setTimeout(() => setCountdown(countdown - 1), 1000);
      return () => clearTimeout(timer);
    }
  }, [countdown]);

  const handleSendCode = async () => {
    if (!email || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      setError('请输入有效的邮箱地址');
      return;
    }
    try {
      await api.sendVerificationCode(email);
      setCodeSent(true);
      setCountdown(60);
      setError('');
    } catch (err) {
      setError(err.message || '发送验证码失败，请稍后再试');
    }
  };

  const handleSubmit = async (e) => {
    e.preventDefault();
    setError('');
    try {
      if (mode === 'login') {
        await onLogin(username, password);
      } else {
        await onRegister(email, password, loginName, code);
      }
    } catch (err) {
      setError(formatAuthError(err.message));
    }
  };

  const authShell = (content) => (
    <div className="oc-auth">
      {content}
    </div>
  );

  if (mode === 'reset') {
    return authShell(
      <div className="oc-auth-card">
        <div className="oc-auth-logo"><span className="catsco-brand-mark" aria-hidden="true" /><span>CatsCo</span></div>
        <div className="oc-settings-secondary" style={{ marginBottom: 14 }}>
          输入注册邮箱，验证后设置新密码。
        </div>
        <PasswordResetForm />
        <div className="oc-auth-link">
          <span>想起密码了？<a href="#" onClick={(e) => { e.preventDefault(); setMode('login'); }}>返回登录</a></span>
        </div>
      </div>
    );
  }

  return authShell(
    <form className="oc-auth-card" onSubmit={handleSubmit}>
      <div className="oc-auth-logo"><span className="catsco-brand-mark" aria-hidden="true" /><span>CatsCo</span></div>
      {error && <div style={{ color: '#FA5151', marginBottom: 12, fontSize: 13 }}>{error}</div>}

      {mode === 'login' ? (
        <>
          <input
            className="oc-auth-input"
            placeholder={t('username')}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
          />
          <div style={{ position: 'relative' }}>
            <input
              className="oc-auth-input"
              type={showPassword ? 'text' : 'password'}
              placeholder={t('password')}
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              style={{ paddingRight: 48 }}
            />
            <span
              onClick={() => setShowPassword(!showPassword)}
              style={{ position: 'absolute', right: 12, top: '40%', transform: 'translateY(-50%)', cursor: 'pointer', color: '#888', userSelect: 'none', display: 'flex', alignItems: 'center' }}
            >
              {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
            </span>
          </div>
        </>
      ) : (
        <>
          <input
            className="oc-auth-input"
            type="email"
            placeholder="邮箱地址"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
          />
          <div style={{ display: 'flex', gap: '8px' }}>
            <input
              className="oc-auth-input"
              placeholder="邮箱验证码"
              value={code}
              onChange={(e) => setCode(e.target.value)}
              style={{ flex: 1 }}
            />
            <button
              type="button"
              className="oc-auth-btn"
              onClick={handleSendCode}
              disabled={countdown > 0}
              style={{ width: '120px', fontSize: '13px' }}
            >
              {countdown > 0 ? `${countdown}秒` : '发送验证码'}
            </button>
          </div>
          <input
            className="oc-auth-input"
            placeholder="登录名称（可用于登录）"
            value={loginName}
            onChange={(e) => setLoginName(e.target.value)}
          />
          <div style={{ position: 'relative' }}>
            <input
              className="oc-auth-input"
              type={showPassword ? 'text' : 'password'}
              placeholder="设置密码（至少6位）"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              style={{ paddingRight: 48 }}
            />
            <span
              onClick={() => setShowPassword(!showPassword)}
              style={{ position: 'absolute', right: 12, top: '40%', transform: 'translateY(-50%)', cursor: 'pointer', color: '#888', userSelect: 'none', display: 'flex', alignItems: 'center' }}
            >
              {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
            </span>
          </div>
        </>
      )}

      <button className="oc-auth-btn" type="submit">
        {mode === 'login' ? t('login') : t('register')}
      </button>
      <div className="oc-auth-link">
        {mode === 'login' ? (
          <>
            <span>还没有账号？<a href="#" onClick={(e) => { e.preventDefault(); setMode('register'); }}>立即注册</a></span>
            <span style={{ marginLeft: 12 }}>
              <a href="#" onClick={(e) => { e.preventDefault(); setMode('reset'); }}>忘记密码？</a>
            </span>
          </>
        ) : (
          <span>已有账号？<a href="#" onClick={(e) => { e.preventDefault(); setMode('login'); }}>立即登录</a></span>
        )}
      </div>
    </form>
  );
}

function parseUid(uidStr) {
  if (!uidStr) return 0;
  if (uidStr.startsWith('usr')) {
    return parseInt(uidStr.slice(3), 10) || 0;
  }
  return parseInt(uidStr, 10) || 0;
}
