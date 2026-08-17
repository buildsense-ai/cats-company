import React, { useState, useEffect, useCallback, useRef } from 'react';
import { createPortal } from 'react-dom';
import { api, setToken, getToken, getAuthRevision, isCurrentAuthSession, getPushCleanupRegistrationIDs, connectWS, reconnectWS, disconnectWS, sendWSActiveTopic, sendWSPageFocus, sendWSPageVisibility } from '../api';
import { enqueuePushOperation } from '../utils/push-operation';
import { pushTabCoordinator } from '../utils/push-tab-coordination';
import { cleanupPushForSession } from '../utils/push-session-cleanup';
import t from '../i18n';
import RelayAdminPanel from './relay-admin-panel';
import ChatListView from './sidepanel-view';
import FriendsView from './friends-view';
import MessagesView from './messages-view';
import SearchOverlay from './search-overlay';
import AgentEntryBindView from './agent-entry-bind-view';
import ChannelDeviceLinkView from './channel-device-link-view';
import MobileUploadView from './mobile-upload-view';
import SkillHubView from './skillhub-view';
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
import GroupSettings from '../widgets/group-settings';
import EditableConversationTitle from '../widgets/editable-conversation-title';
import { useFeedback } from '../components/feedback-system';
import { AuthView } from './auth-gateway';
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
  applyDocumentTheme,
  verifyLiquidThemePassword,
} from '../utils/theme-access';
import {
  authModeForPathname,
  authPathForMode,
  authenticationRedirectPath,
  navigateBrowserPath,
  postAuthenticationPathFromSearch,
} from '../utils/auth-routes';
import { Cloud, Download, Frown, KeyRound, Laptop, Package, Settings, Settings2, LogOut, PanelLeftClose, PanelLeftOpen, Search } from 'lucide-react';
import './workspace-styles';

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

export default function TinodeWeb({ location = window.location } = {}) {
  const { pathname = '/', search = '' } = location;
  const mobileUploadMatch = pathname.match(/^\/mobile-upload\/([^/]+)$/);
  if (mobileUploadMatch) {
    return <MobileUploadView sessionId={decodeURIComponent(mobileUploadMatch[1])} />;
  }

  const demoParams = new URLSearchParams(search);
  const showWorkflowDemo = demoParams.get('workflow_demo') === '1';
  if (showWorkflowDemo) {
    return <WorkflowRichMediaDemo />;
  }

  return <TinodeWebApp location={location} />;
}

function TinodeWebApp({ location }) {
  const feedback = useFeedback();
  const { pathname = '/', search = '', hash = '' } = location;
  const entryMatch = pathname.match(/^\/e\/([^/]+)$/);
  const entrySceneKey = entryMatch ? decodeURIComponent(entryMatch[1]) : '';
  const channelDeviceLink = pathname === '/channel-device-link';
  const channelAccountLink = pathname === '/channel-account-link';
  const [user, setUser] = useState(() => getInitialUser());
  const [activeTab, setActiveTab] = useState(TABS.CHATS);
  const [activeView, setActiveView] = useState('chats');
  const [skillHubInitialAgent, setSkillHubInitialAgent] = useState(null);
  const [activeTopic, _setActiveTopic] = useState(() => (
    user?.uid ? readStoredTopic(user.uid) : null
  ));
  const [taskDraft, setTaskDraft] = useState(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [messageLocationRequest, setMessageLocationRequest] = useState(null);
  const messageLocationSequenceRef = useRef(0);
  const taskDraftSequenceRef = useRef(0);

  useEffect(() => {
    if (!user) return undefined;
    const handleGlobalSearchShortcut = (event) => {
      if (!(event.ctrlKey || event.metaKey) || event.altKey || event.key.toLowerCase() !== 'k') return;
      event.preventDefault();
      setSearchOpen(true);
    };
    document.addEventListener('keydown', handleGlobalSearchShortcut);
    return () => document.removeEventListener('keydown', handleGlobalSearchShortcut);
  }, [user]);

  const setActiveTopic = useCallback((nextValue) => {
    _setActiveTopic((prev) => {
      const next = typeof nextValue === 'function' ? nextValue(prev) : nextValue;
      const normalized = normalizeActiveTopic(next);
      writeStoredTopic(user?.uid, normalized);
      return normalized;
    });
  }, [user?.uid]);
  const authMode = authModeForPathname(pathname);
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
  const [relayAdminAllowed, setRelayAdminAllowed] = useState(false);
  const [relayAdminOpen, setRelayAdminOpen] = useState(false);

  useEffect(() => {
    const redirectPath = authenticationRedirectPath({
      authenticated: Boolean(user),
      location: { pathname, search, hash },
    });
    if (redirectPath) navigateBrowserPath(redirectPath, { replace: true });
  }, [hash, pathname, search, user]);

  const navigateToAuthMode = useCallback((mode) => {
    navigateBrowserPath(authPathForMode(mode, postAuthenticationPathFromSearch(search)));
  }, [search]);

  useEffect(() => {
    let cancelled = false;
    if (!user) return undefined;
    api.getRelayAdminAccess()
      .then((res) => { if (!cancelled) setRelayAdminAllowed(Boolean(res?.allowed)); })
      .catch(() => { /* non-whitelisted users just see no button */ });
    return () => { cancelled = true; };
  }, [user?.uid]);
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
    if (!activeTopicId) return;
    cloudArtifactsRequestSequenceRef.current += 1;
    setCloudArtifactsRequest({
      agentUid,
      requestId: cloudArtifactsRequestSequenceRef.current,
    });
  }, [activeTopicId, displayedActiveAgent?.uid]);
  const handleOpenManagedAgentArtifacts = useCallback((agentUid) => {
    const normalizedAgentUid = Number(agentUid || 0);
    if (!activeTopicId || normalizedAgentUid <= 0) return;
    setActiveView('chats');
    cloudArtifactsRequestSequenceRef.current += 1;
    setCloudArtifactsRequest({
      agentUid: normalizedAgentUid,
      requestId: cloudArtifactsRequestSequenceRef.current,
    });
  }, [activeTopicId]);
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
    applyDocumentTheme(theme);
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
    if (!saveLiquidThemeUnlock()) {
      throw new Error('浏览器未能保存解锁状态，请检查站点存储设置。');
    }
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
        const previewSessionRevision = getAuthRevision();
        const profile = normalizeUserProfile(await api.getMe().catch(() => null));
        if (cancelled || !isCurrentAuthSession(session.token, previewSessionRevision)) return;
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

  const clearAuthenticatedSession = useCallback((
    authToken = getToken(),
    expectedSessionRevision = getAuthRevision(),
  ) => {
    if (!isCurrentAuthSession(authToken, expectedSessionRevision)) return;
    const registrationIDs = getPushCleanupRegistrationIDs();
    const registrationID = registrationIDs[0] || '';
    const sessionRevision = expectedSessionRevision;
    enqueuePushOperation(() => cleanupPushForSession({
      coordinator: pushTabCoordinator,
      registrationID,
      registrationIDs,
      getCurrentToken: getToken,
      sessionRevision,
      getCurrentSessionRevision: getAuthRevision,
      unsubscribeOnServer: (endpoint, id) => api.unsubscribePush(endpoint, authToken, id),
    })).catch((error) => {
      console.warn('Push subscription cleanup failed while clearing session:', error);
    });
    disconnectWS();
    setToken(null);
    localStorage.removeItem('oc_user');
    setUser(null);
    setOnlineUsers({});
    setTaskDraft(null);
    setActiveView('chats');
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
    sendWSActiveTopic(user?.uid ? activeTopicId : '');
  }, [activeTopicId, user?.uid]);

  useEffect(() => {
    if (!user?.uid) return undefined;

    const syncPageVisibility = () => sendWSPageVisibility(document.visibilityState);
    let hiddenAt = 0;
    const recoverConnection = (force = false) => {
      if (document.visibilityState !== 'visible' || navigator.onLine === false) return;
      if (force) reconnectWS(handleWSMessage);
      else connectWS(handleWSMessage);
    };
    const handleVisibilityChange = () => {
      syncPageVisibility();
      if (document.visibilityState === 'hidden') {
        hiddenAt = Date.now();
        return;
      }
      sendWSPageFocus(document.hasFocus());
      const suspendedLongEnoughToStale = hiddenAt > 0 && Date.now() - hiddenAt >= 30000;
      recoverConnection(suspendedLongEnoughToStale);
      hiddenAt = 0;
    };
    const handlePageShow = (event) => recoverConnection(Boolean(event.persisted));
    const handleOnline = () => recoverConnection(true);
    const handleFocus = () => {
      sendWSPageFocus(true);
      recoverConnection(false);
    };
    const handleBlur = () => sendWSPageFocus(false);

    syncPageVisibility();
    document.addEventListener('visibilitychange', handleVisibilityChange);
    window.addEventListener('pageshow', handlePageShow);
    window.addEventListener('online', handleOnline);
    window.addEventListener('focus', handleFocus);
    window.addEventListener('blur', handleBlur);
    return () => {
      document.removeEventListener('visibilitychange', handleVisibilityChange);
      window.removeEventListener('pageshow', handlePageShow);
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('focus', handleFocus);
      window.removeEventListener('blur', handleBlur);
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
    const requestSessionRevision = getAuthRevision();
    if (!requestToken) return undefined;

    let cancelled = false;
    api.getMe()
      .then((profile) => {
        if (!cancelled && isCurrentAuthSession(requestToken, requestSessionRevision)) {
          const normalized = normalizeUserProfile(profile);
          if (normalized) persistUser(normalized);
        }
      })
      .catch((error) => {
        console.warn('Failed to refresh current user profile:', error);
        if (!cancelled && error?.status === 401
          && isCurrentAuthSession(requestToken, requestSessionRevision)) {
          clearAuthenticatedSession(requestToken, requestSessionRevision);
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

    const params = new URLSearchParams(search);
    if (params.get('relay_login') !== '1') return;

    let cancelled = false;
    const fallBackToRelayPanel = () => {
      params.delete('relay_login');
      const nextSearch = params.toString();
      const nextUrl = `${window.location.pathname}${nextSearch ? `?${nextSearch}` : ''}${window.location.hash}`;
      navigateBrowserPath(nextUrl, { replace: true });
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
  }, [search, user?.uid]);

  const handleLogin = async (account, password) => {
    const res = await api.login({ account, password });
    setToken(res.token);
    persistUser(normalizeUserProfile(res));
    navigateBrowserPath(postAuthenticationPathFromSearch(search), { replace: true });
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
    setActiveView('chats');
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

  const handleSearchResultSelect = useCallback((result) => {
    if (!result?.topicId) return;
    const targetMessageId = Number(result.messageId) || 0;
    messageLocationSequenceRef.current += 1;
    setTaskDraft(null);
    setActiveView('chats');
    setActiveTopic({
      topicId: result.topicId,
      name: result.source || result.topicId,
      isGroup: result.isGroup || result.topicId.startsWith('grp_'),
      groupId: result.groupId,
      avatar_url: result.avatarUrl,
    });
    setMessageLocationRequest(targetMessageId ? {
      topicId: result.topicId,
      messageId: targetMessageId,
      requestId: messageLocationSequenceRef.current,
    } : null);
    setSearchOpen(false);
    setMobileSidebarOpen(false);
  }, [setActiveTopic]);

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
    return (
      <AuthView
        mode={authMode}
        nextPath={postAuthenticationPathFromSearch(search)}
        onNavigate={navigateToAuthMode}
        onLogin={handleLogin}
        onRegister={handleRegister}
      />
    );
  }

  if (entrySceneKey) {
    return <AgentEntryBindView sceneKey={entrySceneKey} />;
  }

  const localAssistantBar = (
    <LocalAssistantBar
      agentModelState={displayedAgentModel}
      activeAgent={displayedActiveAgent}
      currentModelName={currentModelName}
      onDownload={() => setShowDownloadModal(true)}
      onOpenCloudArtifacts={showCloudArtifactsAction ? handleOpenCloudArtifacts : undefined}
      title={activeTopic?.name || taskDraftTitle(taskDraft)}
      onRenameTitle={activeTopic ? handleRenameActiveTopic : undefined}
      relayAdminAllowed={relayAdminAllowed}
      onOpenRelayAdmin={() => setRelayAdminOpen(true)}
    />
  );

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
          <div className="v3-sidebar-header-actions">
            {!appSidebarCollapsed && (
              <button
                className="v3-sidebar-collapse-btn v3-sidebar-header-search-btn"
                type="button"
                onClick={() => setSearchOpen(true)}
                aria-label="打开全局搜索"
                aria-keyshortcuts="Control+K Meta+K"
                title="搜索"
              >
                <Search size={18} aria-hidden="true" />
              </button>
            )}
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
        </div>
        
        <div className="cc-sidebar-content-shell">
          <SidebarContent
            activeTopic={activeTopic ? activeTopic.topicId : null}
            onSelectTopic={(topic) => {
              setTaskDraft(null);
              setMessageLocationRequest(null);
              setActiveView('chats');
              setActiveTopic(topic);
              setMobileSidebarOpen(false);
            }}
            onOpenSearch={() => setSearchOpen(true)}
            onStartAgentTask={handleStartAgentTask}
            onOpenSkillHub={(agentId, agent) => {
              setSkillHubInitialAgent(agent || { uid: agentId, id: agentId });
              setActiveView('skillhub');
              setMobileSidebarOpen(false);
            }}
            onOpenCloudArtifacts={activeTopicId ? handleOpenManagedAgentArtifacts : undefined}
            user={user}
            onlineUsers={onlineUsers}
            compact={appSidebarCollapsed}
            additionalSidebarTools={(
              <>
                <SkillHubSidebarButton
                  active={activeView === 'skillhub'}
                  onClick={() => {
                    setSkillHubInitialAgent(null);
                    setActiveView('skillhub');
                    setMobileSidebarOpen(false);
                  }}
                />
              </>
            )}
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
          <ProfilePopover
            compact={appSidebarCollapsed}
            popoverRef={profilePopoverRef}
            onLogout={() => {
              setShowProfilePopover(false);
              handleLogout();
            }}
          >
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
              <KeyRound size={16} style={{marginRight: 10}} /> 套餐与权益
            </div>
            <div className="v3-popover-item" onClick={() => { setShowProfilePopover(false); setShowProfileEditor(true); }}>
              <Settings size={16} style={{marginRight: 10}} /> 设置与资料
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
        <div className="v3-main-body">
          <div className="v3-main-content">
            {activeView === 'skillhub' ? (
              <SkillHubView user={user} initialAgent={skillHubInitialAgent} />
            ) : activeTopic ? (
              <MessagesView
                topBar={localAssistantBar}
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
                messageLocationRequest={messageLocationRequest}
                onBackToSearch={() => setSearchOpen(true)}
              />
            ) : (
              <>
                {localAssistantBar}
                <NoActiveTask
                  key={taskDraft?.key || 'new-task'}
                  user={user}
                  initialAgent={taskDraft?.agent}
                  onResolveAgentTopic={createDraftAgentTaskTopic}
                  onActivateTopic={activateResolvedTopic}
                />
              </>
            )}
          </div>
          {relayAdminAllowed && relayAdminOpen && (
            <RelayAdminPanel onClose={() => setRelayAdminOpen(false)} />
          )}
        </div>
      </div>

      <SearchOverlay
        open={searchOpen}
        onClose={() => setSearchOpen(false)}
        onSelectResult={handleSearchResultSelect}
      />

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

export function LocalAssistantBar({ agentModelState, activeAgent, currentModelName, onDownload, onOpenCloudArtifacts, title, onRenameTitle, relayAdminAllowed = false, onOpenRelayAdmin }) {
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
        {relayAdminAllowed && (
          <button type="button" className="v3-action-btn" onClick={onOpenRelayAdmin} aria-label="模型用量" title="模型用量管理">
            <Settings2 size={17} />
          </button>
        )}
        <button
          type="button"
          className="v3-action-btn v3-cloud-action"
          onClick={onOpenCloudArtifacts}
          disabled={!onOpenCloudArtifacts}
          aria-label={onOpenCloudArtifacts ? '打开云文件' : '云文件，需要先进入聊天'}
          title={onOpenCloudArtifacts ? '云文件' : '请先进入聊天'}
        >
          <Cloud size={17} aria-hidden="true" />
        </button>
        <button type="button" className="v3-action-btn" onClick={onDownload} aria-label="下载桌面端">
          <Download size={17} />
        </button>
      </div>
    </header>
  );
}

export { canOpenCloudArtifacts, describeModelApplyError, describeModelConfigRequestError, resolveDisplayedActiveAgent };

function canOpenCloudArtifacts(activeTopic, activeAgent) {
  return Boolean(activeTopic?.topicId);
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
  onOpenSearch,
  additionalSidebarTools,
  onStartAgentTask,
  onOpenSkillHub,
  onOpenCloudArtifacts,
  user,
  onlineUsers,
  compact,
  onManageGroup,
}) {
  return (
    <ChatListView
      activeTopic={activeTopic}
      onSelectTopic={onSelectTopic}
      onOpenSearch={onOpenSearch}
      additionalSidebarTools={additionalSidebarTools}
      onStartAgentTask={onStartAgentTask}
      onOpenSkillHub={onOpenSkillHub}
      onOpenCloudArtifacts={onOpenCloudArtifacts}
      user={user}
      onlineUsers={onlineUsers}
      compact={compact}
      onManageGroup={onManageGroup}
    />
  );
}

function SkillHubSidebarButton({ active, onClick }) {
  return (
    <button
      type="button"
      className={`cc-sidebar-primary cc-sidebar-skillhub-entry${active ? ' active' : ''}`}
      onClick={onClick}
      aria-label="打开 SkillHub"
      aria-current={active ? 'page' : undefined}
      title="SkillHub"
    >
      <Package size={17} />
      <span>SkillHub</span>
    </button>
  );
}

export function ProfilePopover({ compact = false, popoverRef, children, onLogout }) {
  if (typeof document === 'undefined') return null;
  return createPortal(
    <div
      className={`v3-profile-popover${compact ? ' is-compact' : ''}`}
      ref={popoverRef}
    >
      {children}
      {onLogout && (
        <div
          className="v3-popover-item danger"
          role="button"
          tabIndex={0}
          aria-label="退出登录"
          onClick={onLogout}
          onKeyDown={(event) => {
            if (event.key !== 'Enter' && event.key !== ' ') return;
            event.preventDefault();
            onLogout();
          }}
        >
          <LogOut size={16} strokeWidth={1.8} style={{ marginRight: 10 }} /> 退出登录
        </div>
      )}
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
