import React, { useState, useEffect, useCallback, useRef } from 'react';
import { createPortal } from 'react-dom';
import { api, setToken, getToken, getPushRegistrationID, retirePushRegistrationID, connectWS, reconnectWS, disconnectWS } from '../api';
import { cleanupPushSubscription } from '../utils/push-notifications';
import { enqueuePushOperation } from '../utils/push-operation';
import { pushTabCoordinator } from '../utils/push-tab-coordination';
import t from '../i18n';
import ChatListView from './sidepanel-view';
import FriendsView from './friends-view';
import MessagesView from './messages-view';
import AgentEntryBindView from './agent-entry-bind-view';
import ChannelDeviceLinkView from './channel-device-link-view';
import MobileUploadView from './mobile-upload-view';
import EmptyTaskComposer from '../widgets/empty-task-composer';
import SidebarResizeHandle, {
  MIN_APP_SIDEBAR_WIDTH,
  clampSidebarWidth,
  getSidebarMaxWidth,
  loadSidebarWidth,
  saveSidebarWidth,
} from '../widgets/sidebar-resizer';
import ProfileEditor from '../widgets/profile-editor';
import FeedbackModal from '../widgets/feedback-modal';
import CatsCoDownloadModal from '../widgets/catsco-download-modal';
import DesktopConnectModal from '../widgets/desktop-connect-modal';
import RelayAccessModal from '../widgets/relay-access-modal';
import PasswordResetForm from '../widgets/password-reset-form';
import GroupSettings from '../widgets/group-settings';
import EditableConversationTitle from '../widgets/editable-conversation-title';
import AuthFlowBackground from '../components/auth-flow-background';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import WorkflowRichMediaDemo from './workflow-rich-media-demo';
import Avatar from '../widgets/avatar';
import BotModelSelector, {
  describeModelApplyError,
  describeModelConfigRequestError,
} from '../widgets/bot-model-selector';
import {
  relayUsageTone,
  resolveConversationModelDisplay,
  resolveCurrentModelName,
} from '../utils/relay-usage';
import {
  normalizeActiveTopic,
  readStoredTopic,
  shouldForgetStoredTopic,
  writeStoredTopic,
} from '../utils/active-topic';
import {
  applyScopedModelUpdate,
  resolveScopedModelState,
} from '../utils/conversation-model-state';
import { createAgentTaskTopicRecord } from '../utils/agent-task-topic';
import { formatEmptyTaskGreeting } from '../utils/empty-task-greeting';
import {
  THEME_STORAGE_KEY,
  isLiquidTheme,
  isLiquidThemeUnlocked,
  normalizeTheme,
  saveLiquidThemeUnlock,
  verifyLiquidThemePassword,
} from '../utils/theme-access';
import { Cloud, Download, Frown, KeyRound, Laptop, Settings, LogOut, Eye, EyeOff, PanelLeftClose, PanelLeftOpen } from 'lucide-react';
import '../css/openchat-theme.css';
import '../css/catsco-ui-system.css';
import '../css/catsco-liquid-green.css';

const TABS = {
  CHATS: 'chats'
};
const APP_SIDEBAR_COLLAPSED_STORAGE_KEY = 'cc_app_sidebar_collapsed_v1';
const DEFAULT_MODEL_NAME = 'MiniMax-M2.7';
const DEV_PREVIEW_ENABLED = import.meta.env.DEV && import.meta.env.VITE_DEV_BYPASS_AUTH === 'true';
const DEV_PREVIEW_UID = Number(import.meta.env.VITE_DEV_PREVIEW_UID || 100);
const DEV_PREVIEW_ACCOUNT = import.meta.env.VITE_DEV_PREVIEW_ACCOUNT || 'ui-reviewer';
const DEV_PREVIEW_PASSWORD = import.meta.env.VITE_DEV_PREVIEW_PASSWORD || 'demo123456';
const requestedThemePreview = import.meta.env.DEV
  ? new URLSearchParams(window.location.search).get('theme_preview')
  : '';
const DEV_THEME_PREVIEW = ['light', 'dark', 'liquid', 'liquid-green'].includes(requestedThemePreview)
  ? requestedThemePreview
  : '';
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

export function resolveInitialUser({
  themePreview = '',
  previewEnabled = false,
  token = '',
  savedUser = null,
} = {}) {
  const authenticatedUser = token ? normalizeUserProfile(savedUser) : null;
  if (authenticatedUser) return authenticatedUser;
  if (themePreview) return { ...DEV_PREVIEW_USER, uid: 'theme-preview' };
  if (previewEnabled || !token) return null;
  return null;
}

function getInitialUser() {
  const token = getToken();
  let savedUser = null;
  try {
    const saved = token ? localStorage.getItem('oc_user') : '';
    savedUser = saved ? JSON.parse(saved) : null;
  } catch (error) {
    console.warn('Failed to restore saved user from localStorage:', error);
    localStorage.removeItem('oc_user');
  }
  return resolveInitialUser({
    themePreview: DEV_THEME_PREVIEW,
    previewEnabled: DEV_PREVIEW_ENABLED,
    token,
    savedUser,
  });
}

function loadAppSidebarCollapsed() {
  if (typeof window === 'undefined' || !window.localStorage) return false;
  return window.localStorage.getItem(APP_SIDEBAR_COLLAPSED_STORAGE_KEY) === 'true';
}

function saveAppSidebarCollapsed(collapsed) {
  if (typeof window === 'undefined' || !window.localStorage) return;
  window.localStorage.setItem(APP_SIDEBAR_COLLAPSED_STORAGE_KEY, collapsed ? 'true' : 'false');
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
  const feedback = useFeedback();
  const entryMatch = window.location.pathname.match(/^\/e\/([^/]+)$/);
  const entrySceneKey = entryMatch ? decodeURIComponent(entryMatch[1]) : '';
  const channelDeviceLink = window.location.pathname === '/channel-device-link';
  const channelAccountLink = window.location.pathname === '/channel-account-link';
  const [user, setUser] = useState(() => getInitialUser());
  const [activeTab, setActiveTab] = useState(TABS.CHATS);
  const [activeTopic, _setActiveTopic] = useState(() => (
    user?.uid ? readStoredTopic(user.uid) : null
  ));
  const [taskDraft, setTaskDraft] = useState(null);
  const taskDraftSequenceRef = useRef(0);

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
  const [cloudArtifactsRequest, setCloudArtifactsRequest] = useState(null);
  const cloudArtifactsRequestSequenceRef = useRef(0);
  const [managedGroup, setManagedGroup] = useState(null);
  const appShellRef = useRef(null);
  const [appSidebarCollapsed, setAppSidebarCollapsed] = useState(() => loadAppSidebarCollapsed());
  const [appSidebarPreferredWidth, setAppSidebarPreferredWidth] = useState(() => loadSidebarWidth());
  const [sidebarViewportWidth, setSidebarViewportWidth] = useState(() => window.innerWidth);
  const [isSidebarResizing, setIsSidebarResizing] = useState(false);
  const [mobileSidebarOpen, setMobileSidebarOpen] = useState(false);
  const [theme, setTheme] = useState(() => DEV_THEME_PREVIEW || normalizeTheme(localStorage.getItem(THEME_STORAGE_KEY)));
  const [liquidThemeAccess, setLiquidThemeAccess] = useState(() => ({
    loading: false,
    unlocked: isLiquidTheme(DEV_THEME_PREVIEW) || isLiquidThemeUnlocked(),
  }));
  const [currentModelName, setCurrentModelName] = useState(DEFAULT_MODEL_NAME);
  const [activeAgentModel, setActiveAgentModel] = useState(null);
  const [activeAgentState, setActiveAgentState] = useState(null);
  const activeTopicId = activeTopic?.topicId || '';
  const draftAgentUID = Number(taskDraft?.agent?.uid || taskDraft?.agent?.id || 0);
  const modelContextId = activeTopicId || (draftAgentUID > 0 ? `draft:${taskDraft?.key || draftAgentUID}` : '');
  const modelContext = activeTopic || (modelContextId ? { topicId: modelContextId, isGroup: false } : null);
  const modelContextIdRef = useRef(modelContextId);
  modelContextIdRef.current = modelContextId;
  const handleActiveAgentModelChange = useCallback((modelState) => {
    const topicId = activeTopicId;
    setActiveAgentModel((current) => applyScopedModelUpdate(current, {
      activeTopicId: modelContextIdRef.current,
      topicId,
      modelState,
    }));
  }, [activeTopicId]);
  const displayedAgentModel = resolveScopedModelState(modelContext, activeAgentModel);
  const handleActiveAgentChange = useCallback((agent) => {
    const topicId = activeTopicId;
    setActiveAgentState((current) => {
      if (!topicId || topicId !== modelContextIdRef.current) return current;
      return { topicId, agent };
    });
  }, [activeTopicId]);
  const displayedActiveAgent = resolveDisplayedActiveAgent(activeTopicId, activeAgentState, taskDraft);
  const showCloudArtifactsAction = canOpenCloudArtifacts(activeTopic, displayedActiveAgent);
  const handleOpenCloudArtifacts = useCallback(() => {
    const agentUid = Number(displayedActiveAgent?.uid || 0);
    if (agentUid <= 0) return;
    cloudArtifactsRequestSequenceRef.current += 1;
    setCloudArtifactsRequest({
      agentUid,
      requestId: cloudArtifactsRequestSequenceRef.current,
    });
  }, [displayedActiveAgent?.uid]);
  const appSidebarMaxWidth = getSidebarMaxWidth(sidebarViewportWidth);
  const appSidebarWidth = clampSidebarWidth(
    appSidebarPreferredWidth,
    MIN_APP_SIDEBAR_WIDTH,
    appSidebarMaxWidth,
  );
  const previewAppSidebarWidth = useCallback((nextWidth) => {
    appShellRef.current?.style.setProperty('--cc-sidebar-user-width', `${nextWidth}px`);
  }, []);
  const commitAppSidebarWidth = useCallback((nextWidth) => {
    setAppSidebarPreferredWidth(nextWidth);
    saveSidebarWidth(nextWidth);
  }, []);

  useEffect(() => {
    const greenLiquid = theme === 'liquid-green';
    document.documentElement.dataset.theme = greenLiquid ? 'liquid' : theme;
    if (greenLiquid) {
      document.documentElement.dataset.liquidVariant = 'green';
    } else {
      delete document.documentElement.dataset.liquidVariant;
    }
    localStorage.setItem(THEME_STORAGE_KEY, theme);
  }, [theme]);

  useEffect(() => {
    if (DEV_THEME_PREVIEW) {
      setLiquidThemeAccess({ loading: false, unlocked: isLiquidTheme(DEV_THEME_PREVIEW) });
      setTheme(DEV_THEME_PREVIEW);
      return;
    }

    const unlocked = isLiquidThemeUnlocked();
    setLiquidThemeAccess({ loading: false, unlocked });
    if (!unlocked) setTheme((current) => isLiquidTheme(current) ? 'light' : current);
  }, []);

  const selectTheme = useCallback((nextTheme) => {
    const normalized = normalizeTheme(nextTheme);
    if (isLiquidTheme(normalized) && !liquidThemeAccess.unlocked) return false;
    setTheme(normalized);
    return true;
  }, [liquidThemeAccess.unlocked]);

  const unlockLiquidTheme = useCallback(async (password, requestedTheme = 'liquid') => {
    const unlocked = await verifyLiquidThemePassword(password);
    if (!unlocked) throw new Error('密码不正确。');
    saveLiquidThemeUnlock();
    setLiquidThemeAccess({ loading: false, unlocked: true });
    const normalized = normalizeTheme(requestedTheme);
    setTheme(isLiquidTheme(normalized) ? normalized : 'liquid');
    return { ok: true };
  }, []);

  useEffect(() => {
    const handleViewportResize = () => setSidebarViewportWidth(window.innerWidth);
    window.addEventListener('resize', handleViewportResize);
    return () => window.removeEventListener('resize', handleViewportResize);
  }, []);

  useEffect(() => {
    if (!user?.uid) return undefined;
    let cancelled = false;
    const refresh = async () => {
      const [usageResult, configResult] = await Promise.allSettled([api.getRelayUsage(), api.getRelayConfig()]);
      if (cancelled) return;
      const usage = usageResult.status === 'fulfilled' ? usageResult.value?.summary : null;
      const config = configResult.status === 'fulfilled' ? configResult.value : null;
      setCurrentModelName(resolveCurrentModelName(usage, config?.default_model || DEFAULT_MODEL_NAME));
    };
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'visible') refresh();
    };

    refresh();
    window.addEventListener('focus', refresh);
    window.addEventListener('cc:data-changed', refresh);
    document.addEventListener('visibilitychange', handleVisibilityChange);
    return () => {
      cancelled = true;
      window.removeEventListener('focus', refresh);
      window.removeEventListener('cc:data-changed', refresh);
      document.removeEventListener('visibilitychange', handleVisibilityChange);
    };
  }, [user?.uid]);

  useEffect(() => {
    if (activeTopicId || draftAgentUID <= 0 || !modelContextId) return undefined;
    let cancelled = false;
    const updateDraftModel = (modelState) => {
      setActiveAgentModel((current) => applyScopedModelUpdate(current, {
        activeTopicId: modelContextIdRef.current,
        topicId: modelContextId,
        modelState,
      }));
    };

    updateDraftModel({ isBot: true, state: 'loading', summary: null });
    api.getAgentQuota(draftAgentUID)
      .then((response) => {
        if (cancelled) return;
        const summary = response?.summary || null;
        updateDraftModel({
          isBot: true,
          state: summary ? 'ready' : 'unavailable',
          summary,
        });
      })
      .catch(() => {
        if (!cancelled) {
          updateDraftModel({ isBot: true, state: 'unavailable', summary: null });
        }
      });

    return () => {
      cancelled = true;
    };
  }, [activeTopicId, draftAgentUID, modelContextId]);

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

  useEffect(() => {
    if (!DEV_PREVIEW_ENABLED) return undefined;
    let cancelled = false;

    const activatePreviewAccount = async () => {
      try {
        const session = await api.login({
          account: DEV_PREVIEW_ACCOUNT,
          password: DEV_PREVIEW_PASSWORD,
        });
        if (cancelled) return;
        setToken(session.token);
        const profile = normalizeUserProfile(await api.getMe().catch(() => null));
        if (cancelled) return;
        persistUser(profile || {
          ...DEV_PREVIEW_USER,
          uid: DEV_PREVIEW_UID,
        });
      } catch (error) {
        console.warn('Failed to activate local preview account:', error);
      }
    };

    activatePreviewAccount();
    return () => {
      cancelled = true;
    };
  }, [persistUser]);

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
    const handleSidebarShortcut = (event) => {
      if (!event.ctrlKey || event.altKey || event.metaKey || event.key.toLowerCase() !== 'b') return;
      event.preventDefault();
      toggleAppSidebar();
    };
    document.addEventListener('keydown', handleSidebarShortcut);
    return () => document.removeEventListener('keydown', handleSidebarShortcut);
  }, [toggleAppSidebar]);

  useEffect(() => {
    if (!mobileSidebarOpen) return undefined;

    const handleKeyDown = (event) => {
      if (event.key === 'Escape') setMobileSidebarOpen(false);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [mobileSidebarOpen]);

  const clearAuthenticatedSession = useCallback((authToken = getToken()) => {
    if (!authToken || getToken() !== authToken) return;
    const registrationID = getPushRegistrationID();
    pushTabCoordinator.setActive(false);
    enqueuePushOperation(() => cleanupPushSubscription(
      (endpoint) => api.unsubscribePush(endpoint, authToken, registrationID),
      () => {
        const currentToken = getToken();
        return !currentToken || currentToken === authToken;
      },
      () => pushTabCoordinator.hasOtherActiveTab(),
      () => retirePushRegistrationID(registrationID),
    )).catch((error) => {
      console.warn('Push subscription cleanup failed while clearing session:', error);
    });
    disconnectWS();
    setToken(null, { pushCleanupHandled: true });
    localStorage.removeItem('oc_user');
    setUser(null);
    setOnlineUsers({});
    setTaskDraft(null);
    setActiveTopic(null);
  }, [setActiveTopic]);

  // WebSocket message handler
  const handleWSMessage = useCallback((msg) => {
    if (msg._type === 'ws_auth_expired') {
      clearAuthenticatedSession();
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
  }, [clearAuthenticatedSession, setActiveTopic]);

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
    setTaskDraft(null);
    if (!user?.uid) {
      _setActiveTopic(null);
      return;
    }

    const stored = readStoredTopic(user.uid);
    _setActiveTopic(stored);
    if (!stored?.topicId?.startsWith('grp_')) {
      return;
    }

    let cancelled = false;
    const groupId = stored.groupId || Number(stored.topicId.slice(4));
    api.getGroupInfo(groupId)
      .catch((error) => {
        if (cancelled) return;
        if (!shouldForgetStoredTopic(error)) {
          console.warn('Failed to validate the restored task; keeping it for retry:', error);
          return;
        }
        _setActiveTopic((current) => {
          if (current?.topicId !== stored.topicId) return current;
          writeStoredTopic(user.uid, null);
          return null;
        });
      });
    return () => {
      cancelled = true;
    };
  }, [user?.uid]);

  useEffect(() => {
    if (!user?.uid) return;
    const requestToken = getToken();
    if (!requestToken) return undefined;

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
        if (!cancelled && error?.status === 401 && getToken() === requestToken) {
          clearAuthenticatedSession(requestToken);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [user?.uid, persistUser, clearAuthenticatedSession]);

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
    clearAuthenticatedSession();
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

  const handleRenameActiveTopic = useCallback(async (nextName) => {
    const topic = activeTopic;
    if (!topic?.topicId) return;

    try {
      if (topic.isGroup || topic.topicId.startsWith('grp_')) {
        const groupId = Number(topic.groupId || topic.topicId.slice(4));
        if (!groupId) throw new Error('无法识别当前协作任务');
        await api.updateGroup(groupId, nextName, topic.avatar_url || '');
      } else {
        await api.updateConversationTitle(topic.topicId, nextName);
      }

      setActiveTopic((current) => (
        current?.topicId === topic.topicId ? { ...current, name: nextName } : current
      ));
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '对话标题已更新' });
    } catch (error) {
      feedback.notify({ tone: 'error', title: '修改标题失败', message: error.message || '请稍后重试' });
      throw error;
    }
  }, [activeTopic, feedback, setActiveTopic]);

  const resolveAgentTopic = useCallback(async (agent) => {
    const agentUid = agent?.uid || agent?.id;
    if (!agentUid) throw new Error('请选择一个可用的 Agent');

    const res = await api.openAgent(agentUid);
    const opened = res.agent || agent;
    const topicId = opened.topic_id || res.topic || agent.topic_id;
    if (!topicId) throw new Error('暂时无法打开所选 Agent');
    let conversationName = '';

    if (topicId) {
      try {
        const conversations = await api.getConversations();
        conversationName = (conversations.conversations || [])
          .find((conversation) => conversation.id === topicId)?.name || '';
      } catch (error) {
        console.warn('Failed to resolve the conversation title:', error);
      }
    }

    return {
      topicId,
      name: conversationName
        || opened.display_name
        || agent.display_name
        || agent.username,
      isGroup: false,
      avatar_url: opened.avatar_url || agent.avatar_url,
      friendId: opened.uid || agentUid,
      isBot: true,
    };
  }, []);

  const createAgentTaskTopic = useCallback(async (agent, draft = {}) => {
    const taskName = buildAgentTaskName(agent, draft);
    return createAgentTaskTopicRecord({
      agent,
      taskName,
      projectId: draft.projectId,
      projectName: draft.projectName,
    });
  }, []);

  const activateResolvedTopic = useCallback((nextTopic) => {
    if (!nextTopic?.topicId) return;
    setTaskDraft(null);
    setActiveTopic(nextTopic);
  }, [setActiveTopic]);

  const handleStartAgentTask = useCallback((agent, options = {}) => {
    const agentUid = agent?.uid || agent?.id;
    if (!agentUid) return;
    const projectId = Number(options?.projectId || 0);
    taskDraftSequenceRef.current += 1;
    setActiveTopic(null);
    setTaskDraft({
      agent,
      key: `${agentUid}:${taskDraftSequenceRef.current}`,
      projectId: projectId > 0 ? projectId : 0,
      projectName: projectId > 0 ? String(options?.projectName || '') : '',
    });
    setMobileSidebarOpen(false);
  }, [setActiveTopic]);

  const createDraftAgentTaskTopic = useCallback((agent, draft = {}) => (
    createAgentTaskTopic(agent, {
      ...draft,
      projectId: taskDraft?.projectId || 0,
      projectName: taskDraft?.projectName || '',
    })
  ), [createAgentTaskTopic, taskDraft?.projectId, taskDraft?.projectName]);

  const activateAgentTopic = useCallback(async (agent) => {
    const nextTopic = await resolveAgentTopic(agent);
    activateResolvedTopic(nextTopic);
    return nextTopic;
  }, [activateResolvedTopic, resolveAgentTopic]);

  const handleDesktopConnected = async (agent) => {
    try {
      const agentUid = agent?.uid || agent?.id;
      if (!agentUid) {
        setLocalAgentStatus('connected');
        setShowDesktopConnectModal(false);
        window.dispatchEvent(new Event('cc:data-changed'));
        return;
      }
      await activateAgentTopic(agent);
      setLocalAgentStatus('connected');
      setShowDesktopConnectModal(false);
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (error) {
      console.warn('Failed to open connected desktop agent:', error);
    }
  };

  if ((channelDeviceLink || channelAccountLink) && user) {
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
    <div
      ref={appShellRef}
      className={`v3-app${isSidebarResizing ? ' sidebar-resizing' : ''}`}
      style={{ '--cc-sidebar-user-width': `${appSidebarWidth}px` }}
    >
      {mobileSidebarOpen && (
        <button
          type="button"
          className="v3-mobile-sidebar-backdrop"
          onClick={() => setMobileSidebarOpen(false)}
          aria-label="关闭左侧栏"
        />
      )}
      <div
        id="catsco-function-sidebar"
        className={`v3-sidebar${appSidebarCollapsed ? ' collapsed' : ''}${mobileSidebarOpen ? ' open' : ''}`}
      >
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
            {appSidebarCollapsed ? (
              <>
                <span className="catsco-brand-mark v3-collapsed-brand-icon" aria-hidden="true" />
                <PanelLeftClose size={18} className="v3-collapsed-expand-icon" aria-hidden="true" />
              </>
            ) : <PanelLeftClose size={18} />}
          </button>
        </div>
        
        <div className="cc-sidebar-content-shell">
          <SidebarContent
            activeTopic={activeTopic ? activeTopic.topicId : null}
            onSelectTopic={(topic) => {
              setTaskDraft(null);
              setActiveTopic(topic);
              setMobileSidebarOpen(false);
            }}
            onStartAgentTask={handleStartAgentTask}
            user={user}
            onlineUsers={onlineUsers}
            compact={appSidebarCollapsed}
            onManageGroup={(group) => {
              setManagedGroup(group);
              setMobileSidebarOpen(false);
            }}
          />
        </div>
        
        <ProfileFooter
          user={user}
          wsStatus={wsStatus}
          popoverOpen={showProfilePopover}
          onTogglePopover={() => setShowProfilePopover((open) => !open)}
        />

        {showProfilePopover && (
          <ProfilePopover compact={appSidebarCollapsed} popoverRef={profilePopoverRef}>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowFeedbackModal(true); }}>
              <Frown size={16} strokeWidth={1.8} style={{marginRight: 10}} /> 意见反馈
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
          </ProfilePopover>
        )}

        <SidebarResizeHandle
          width={appSidebarWidth}
          maxWidth={appSidebarMaxWidth}
          disabled={appSidebarCollapsed || sidebarViewportWidth <= 768}
          onWidthChange={previewAppSidebarWidth}
          onWidthCommit={commitAppSidebarWidth}
          onResizeChange={setIsSidebarResizing}
        />
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
          agentModelState={displayedAgentModel}
          activeAgent={displayedActiveAgent}
          currentModelName={currentModelName}
          onDownload={() => setShowDownloadModal(true)}
          onOpenCloudArtifacts={showCloudArtifactsAction ? handleOpenCloudArtifacts : undefined}
          title={activeTopic?.name || taskDraftTitle(taskDraft)}
          onRenameTitle={activeTopic ? handleRenameActiveTopic : undefined}
        />
        {activeTopic ? (
          <MessagesView
            topic={activeTopic.topicId}
            topicName={activeTopic.name}
            user={user}
            isGroup={activeTopic.isGroup || (activeTopic.topicId && activeTopic.topicId.startsWith('grp_'))}
            groupId={activeTopic.groupId}
            topicAvatarUrl={activeTopic.avatar_url}
            localAssistantStatus={localAgentStatus}
            onAgentModelChange={handleActiveAgentModelChange}
            onActiveAgentChange={handleActiveAgentChange}
            onOpenDesktopConnect={() => setShowDesktopConnectModal(true)}
            onResolveAgentTopic={resolveAgentTopic}
            onActivateTopic={activateResolvedTopic}
            cloudArtifactsRequest={cloudArtifactsRequest}
          />
        ) : (
          <NoActiveTask
            key={taskDraft?.key || 'new-task'}
            user={user}
            initialAgent={taskDraft?.agent}
            onResolveAgentTopic={createDraftAgentTaskTopic}
            onActivateTopic={activateResolvedTopic}
          />
        )}
      </div>

      {showProfileEditor && (
        <ProfileEditor
          user={user}
          theme={theme}
          onThemeChange={selectTheme}
          liquidThemeAccess={liquidThemeAccess}
          onUnlockLiquidTheme={unlockLiquidTheme}
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

      {managedGroup?.groupId && (
        <GroupSettings
          groupId={managedGroup.groupId}
          currentUser={user}
          onClose={() => setManagedGroup(null)}
          onSaved={(updatedGroup) => {
            if (updatedGroup) {
              handleTopicUpdated({
                topicId: managedGroup.topicId,
                name: updatedGroup.name,
                avatar_url: updatedGroup.avatar_url,
              });
            } else {
              setActiveTopic((current) => (
                current?.topicId === managedGroup.topicId ? null : current
              ));
            }
            window.dispatchEvent(new Event('cc:data-changed'));
          }}
        />
      )}

    </div>
  );
}

export function LocalAssistantBar({ agentModelState, activeAgent, currentModelName, onDownload, onOpenCloudArtifacts, title, onRenameTitle }) {
  return (
    <header className="v3-local-assistant-bar">
      <div className="v3-model-select">
        <BotModelSelector
          currentModelName={currentModelName}
          agentModelState={agentModelState}
          activeAgent={activeAgent}
        />
      </div>
      <EditableConversationTitle title={title} editable={Boolean(onRenameTitle)} onSave={onRenameTitle} />
      <div className="v3-shell-actions">
        {onOpenCloudArtifacts && (
          <button type="button" className="v3-action-btn" onClick={onOpenCloudArtifacts} aria-label="打开产物" title="产物">
            <Cloud size={17} />
          </button>
        )}
        <button type="button" className="v3-action-btn" onClick={onDownload} aria-label="下载桌面端">
          <Download size={17} />
        </button>
      </div>
    </header>
  );
}

export { canOpenCloudArtifacts, describeModelApplyError, describeModelConfigRequestError, resolveDisplayedActiveAgent };

function canOpenCloudArtifacts(activeTopic, activeAgent) {
  return Boolean(activeAgent?.cloud_artifacts_enabled && activeTopic?.topicId);
}

function resolveDisplayedActiveAgent(activeTopicId, activeAgentState, taskDraft) {
  if (activeTopicId) {
    return activeAgentState?.topicId === activeTopicId ? activeAgentState.agent : null;
  }

  const draftAgent = taskDraft?.agent;
  const uid = Number(draftAgent?.uid || draftAgent?.id || 0);
  if (uid <= 0) return null;

  const isOwner = draftAgent?.isOwner === true
    || draftAgent?.is_owner === true
    || draftAgent?.relation === 'owner';
  return {
    ...draftAgent,
    uid,
    isOwner,
    relation: isOwner ? 'owner' : (draftAgent?.relation || 'friend'),
  };
}

function NoActiveTask({ user, initialAgent, onResolveAgentTopic, onActivateTopic }) {
  return (
    <main className="cc-empty-task">
      <div className="cc-empty-task-inner">
        <div className="cc-empty-task-heading">
          <span className="catsco-brand-mark cc-empty-task-mark" aria-hidden="true" />
          <h1>{formatEmptyTaskGreeting(user)}</h1>
        </div>
        <EmptyTaskComposer
          initialAgent={initialAgent}
          onResolveAgentTopic={onResolveAgentTopic}
          onActivateTopic={onActivateTopic}
        />
      </div>
    </main>
  );
}

function SidebarContent({
  activeTopic,
  onSelectTopic,
  onStartAgentTask,
  user,
  onlineUsers,
  compact,
  onManageGroup,
}) {
  return (
    <ChatListView
      activeTopic={activeTopic}
      onSelectTopic={onSelectTopic}
      onStartAgentTask={onStartAgentTask}
      user={user}
      onlineUsers={onlineUsers}
      compact={compact}
      onManageGroup={onManageGroup}
    />
  );
}

export function ProfilePopover({ compact = false, popoverRef, children }) {
  if (typeof document === 'undefined') return null;
  return createPortal(
    <div
      className={`v3-profile-popover${compact ? ' is-compact' : ''}`}
      ref={popoverRef}
    >
      {children}
    </div>,
    document.body,
  );
}

function ProfileFooter({ user, wsStatus, popoverOpen, onTogglePopover }) {
  const connected = wsStatus === 'connected';
  const reconnecting = wsStatus === 'connecting' || wsStatus === 'reconnecting';
  const statusClass = connected ? 'online' : reconnecting ? 'reconnecting' : 'offline';
  const statusLabel = connected ? '在线' : wsStatus === 'connecting' ? '连接中' : reconnecting ? '重新连接中' : '离线';
  const displayName = user.display_name || user.username;
  return (
    <button
      type="button"
      className="v3-profile-footer"
      onClick={onTogglePopover}
      aria-label={`${displayName}，打开个人菜单`}
      aria-expanded={popoverOpen}
    >
      <Avatar name={displayName} src={user.avatar_url} size={32} className="v3-profile-avatar" />
      <div className="v3-profile-info">
        <div className="v3-profile-name">{displayName}</div>
        <div className="v3-profile-roles">
           <span className={`v3-status-dot ${statusClass}`} style={{marginLeft: 0, marginRight: 6}}></span>
           {statusLabel}
        </div>
      </div>
      <div className="v3-profile-settings">
        <Settings size={18} />
      </div>
    </button>
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
      <AuthFlowBackground />
      {content}
    </div>
  );

  if (mode === 'reset') {
    return authShell(
      <div className="oc-auth-card">
        <div className="oc-auth-logo">CatsCo</div>
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
      <div className="oc-auth-logo">CatsCo</div>
      {error && <InlineFeedback tone="error" className="oc-auth-feedback">{error}</InlineFeedback>}

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
          <div className="oc-auth-code-row">
            <input
              className="oc-auth-input"
              placeholder="邮箱验证码"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
            <button
              type="button"
              className="oc-auth-btn"
              onClick={handleSendCode}
              disabled={countdown > 0}
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

function taskDraftTitle(taskDraft) {
  const agent = taskDraft?.agent;
  const agentName = agent?.display_name || agent?.username || '';
  const projectName = String(taskDraft?.projectName || '').trim();
  if (agentName && projectName) return `新任务 · ${agentName} · ${projectName}`;
  return agentName ? `新任务 · ${agentName}` : '新任务';
}

function buildAgentTaskName(agent, draft = {}) {
  const instruction = String(draft.text || '').replace(/\s+/g, ' ').trim();
  if (instruction) return truncateTaskName(instruction);

  const attachments = Array.isArray(draft.attachments) ? draft.attachments : [];
  const agentName = agent?.display_name || agent?.username || 'Agent';
  if (attachments.length === 1) {
    const attachmentName = String(attachments[0]?.name || '').trim();
    if (attachmentName) return truncateTaskName(`${agentName} · ${attachmentName}`);
  }
  if (attachments.length > 1) return truncateTaskName(`${agentName} · ${attachments.length} 个附件`);
  return truncateTaskName(`${agentName} 新任务`);
}

function truncateTaskName(value, maxLength = 36) {
  const characters = Array.from(String(value || '').trim());
  if (characters.length <= maxLength) return characters.join('');
  return `${characters.slice(0, maxLength).join('')}…`;
}
