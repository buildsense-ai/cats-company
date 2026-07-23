import React, { useState, useEffect, useLayoutEffect, useRef } from 'react';
import { createPortal } from 'react-dom';
import { api, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import CreateGroup from '../widgets/create-group';
import AddFriend from '../widgets/add-friend';
import FriendRequest from '../widgets/friend-request';
import AgentStoreModal from '../widgets/agent-store-modal';
import MobileChannelBindModal from '../widgets/mobile-channel-bind-modal';
import Avatar from '../widgets/avatar';
import { useFeedback } from '../components/feedback-system';
import { formatSidebarTime } from '../utils/sidebar-time';
import { Users, UserRound, UserPlus, Zap, Bot, Trash2, Smartphone, Check, X, Pin, Pencil, ChevronRight, Plus, Search, MoreHorizontal, UserX, Ban, LoaderCircle, Folder, FolderOpen, FolderPlus } from 'lucide-react';

const SIDEBAR_COLLAPSED_STORAGE_PREFIX = 'cc_sidebar_collapsed_v1';
const DEFAULT_COLLAPSED_SECTIONS = { conversations: false, contacts: false, projects: false };
const PINNED_GROUPS_STORAGE_PREFIX = 'cc_pinned_groups_v1';
const PINNED_HISTORY_STORAGE_PREFIX = 'cc_pinned_history_v1';
const HIDDEN_HISTORY_STORAGE_PREFIX = 'cc_hidden_history_v1';
const TASK_STATUS_DISMISSED_STORAGE_PREFIX = 'cc_task_status_dismissed_v1';
const FRIEND_SYNC_STORAGE_PREFIX = 'cc_friend_sync_v1';

function sidebarCollapsedStorageKey(uid) {
  return `${SIDEBAR_COLLAPSED_STORAGE_PREFIX}:${uid || 'guest'}`;
}

function pinnedGroupsStorageKey(uid) {
  return `${PINNED_GROUPS_STORAGE_PREFIX}:${uid || 'guest'}`;
}

function pinnedHistoryStorageKey(uid) {
  return `${PINNED_HISTORY_STORAGE_PREFIX}:${uid || 'guest'}`;
}

function hiddenHistoryStorageKey(uid) {
  return `${HIDDEN_HISTORY_STORAGE_PREFIX}:${uid || 'guest'}`;
}

function taskStatusDismissedStorageKey(uid) {
  return `${TASK_STATUS_DISMISSED_STORAGE_PREFIX}:${uid || 'guest'}`;
}

function friendSyncStorageKey(uid) {
  return `${FRIEND_SYNC_STORAGE_PREFIX}:${uid || 'guest'}`;
}

function normalizeCollapsedSections(value) {
  return {
    conversations: typeof value?.conversations === 'boolean'
      ? value.conversations
      : (typeof value?.ai === 'boolean' ? value.ai : DEFAULT_COLLAPSED_SECTIONS.conversations),
    contacts: typeof value?.contacts === 'boolean'
      ? value.contacts
      : (typeof value?.collaboration === 'boolean' ? value.collaboration : DEFAULT_COLLAPSED_SECTIONS.contacts),
    projects: typeof value?.projects === 'boolean' ? value.projects : DEFAULT_COLLAPSED_SECTIONS.projects,
  };
}

function loadCollapsedSections(uid) {
  if (typeof window === 'undefined' || !window.localStorage) {
    return { ...DEFAULT_COLLAPSED_SECTIONS };
  }

  try {
    const raw = window.localStorage.getItem(sidebarCollapsedStorageKey(uid));
    return raw ? normalizeCollapsedSections(JSON.parse(raw)) : { ...DEFAULT_COLLAPSED_SECTIONS };
  } catch (error) {
    console.warn('Failed to restore sidebar collapsed state:', error);
    return { ...DEFAULT_COLLAPSED_SECTIONS };
  }
}

function saveCollapsedSections(uid, next) {
  if (typeof window === 'undefined' || !window.localStorage) return;

  try {
    window.localStorage.setItem(sidebarCollapsedStorageKey(uid), JSON.stringify(next));
  } catch (error) {
    console.warn('Failed to save sidebar collapsed state:', error);
  }
}

function loadPinnedGroupIds(uid) {
  if (typeof window === 'undefined' || !window.localStorage) return new Set();

  try {
    const raw = window.localStorage.getItem(pinnedGroupsStorageKey(uid));
    const parsed = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.map((value) => String(value || '').trim()).filter(Boolean));
  } catch (error) {
    console.warn('Failed to restore pinned groups:', error);
    return new Set();
  }
}

function savePinnedGroupIds(uid, next) {
  if (typeof window === 'undefined' || !window.localStorage) return;

  try {
    window.localStorage.setItem(pinnedGroupsStorageKey(uid), JSON.stringify([...next]));
  } catch (error) {
    console.warn('Failed to save pinned groups:', error);
  }
}

function loadPinnedHistoryIds(uid) {
  if (typeof window === 'undefined' || !window.localStorage) return new Set();

  try {
    const raw = window.localStorage.getItem(pinnedHistoryStorageKey(uid));
    const parsed = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.map((value) => String(value || '').trim()).filter(Boolean));
  } catch (error) {
    console.warn('Failed to restore pinned history:', error);
    return new Set();
  }
}

function savePinnedHistoryIds(uid, next) {
  if (typeof window === 'undefined' || !window.localStorage) return;

  try {
    window.localStorage.setItem(pinnedHistoryStorageKey(uid), JSON.stringify([...next]));
  } catch (error) {
    console.warn('Failed to save pinned history:', error);
  }
}

function loadHiddenHistoryIds(uid) {
  if (typeof window === 'undefined' || !window.localStorage) return new Set();

  try {
    const raw = window.localStorage.getItem(hiddenHistoryStorageKey(uid));
    const parsed = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.map((value) => String(value || '').trim()).filter(Boolean));
  } catch (error) {
    console.warn('Failed to restore hidden history:', error);
    return new Set();
  }
}

function saveHiddenHistoryIds(uid, next) {
  if (typeof window === 'undefined' || !window.localStorage) return;

  try {
    window.localStorage.setItem(hiddenHistoryStorageKey(uid), JSON.stringify([...next]));
  } catch (error) {
    console.warn('Failed to save hidden history:', error);
  }
}

function loadDismissedTaskStatuses(uid) {
  if (typeof window === 'undefined' || !window.localStorage) return {};

  try {
    const raw = window.localStorage.getItem(taskStatusDismissedStorageKey(uid));
    const parsed = raw ? JSON.parse(raw) : {};
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
  } catch (error) {
    console.warn('Failed to restore dismissed task statuses:', error);
    return {};
  }
}

function saveDismissedTaskStatuses(uid, next) {
  if (typeof window === 'undefined' || !window.localStorage) return;

  try {
    window.localStorage.setItem(taskStatusDismissedStorageKey(uid), JSON.stringify(next));
  } catch (error) {
    console.warn('Failed to save dismissed task statuses:', error);
  }
}

function SidebarSectionHeader({
  className = '',
  label,
  expanded,
  onToggle,
  toggleContent = null,
  status = null,
  action = null,
  children = null,
}) {
  return (
    <div className={`v3-chat-section cc-sidebar-section-row cc-top-level-section ${className}`.trim()}>
      <button type="button" className="cc-section-toggle" onClick={onToggle} aria-expanded={expanded}>
        <span>{label}</span>
        {toggleContent}
        <ChevronRight size={14} />
      </button>
      {status}
      {action}
      {children}
    </div>
  );
}

function SidebarItemRow({
  as: Component = 'div',
  className = '',
  active = false,
  level = 1,
  children,
  ...props
}) {
  return (
    <Component
      className={`v3-chat-item cc-sidebar-item-row cc-sidebar-item-level-${level} ${className} ${active ? 'active' : ''}`.trim()}
      data-sidebar-level={level}
      {...props}
    >
      {children}
    </Component>
  );
}

function SidebarRowTrailing({
  className = '',
  children,
  actions = null,
  actionsClassName = '',
}) {
  return (
    <div className={`cc-chat-row-trailing cc-sidebar-row-trailing ${className}`.trim()}>
      {children}
      {actions && (
        <div className={`cc-chat-row-actions cc-sidebar-row-actions ${actionsClassName}`.trim()}>
          {actions}
        </div>
      )}
    </div>
  );
}

export default function ChatListView({
  activeTopic,
  onSelectTopic,
  user,
  onlineUsers,
  compact = false,
  onManageGroup,
  onStartAgentTask,
  onDeleteHistoryTask,
  onOpenMobileLink,
}) {
  const feedback = useFeedback();
  const [chats, setChats] = useState([]);
  const [friends, setFriends] = useState([]);
  const [groups, setGroups] = useState([]);
  const [pending, setPending] = useState([]);
  const [agents, setAgents] = useState([]);
  const [projects, setProjects] = useState([]);
  const [search, setSearch] = useState('');
  const [deletingTopicId, setDeletingTopicId] = useState('');
  const [showCreateGroup, setShowCreateGroup] = useState(false);
  const [showAddFriend, setShowAddFriend] = useState(false);
  const [showAgentStore, setShowAgentStore] = useState(false);
  const [showNewChat, setShowNewChat] = useState(false);
  const [collapsed, setCollapsed] = useState(() => loadCollapsedSections(user?.uid));
  const [mobileLinkAgent, setMobileLinkAgent] = useState(null);
  const [mobileLinkGroup, setMobileLinkGroup] = useState(null);
  const [collaborationUpgradeTask, setCollaborationUpgradeTask] = useState(null);
  const [agentActionId, setAgentActionId] = useState('');
  const [agentPendingRequests, setAgentPendingRequests] = useState([]);
  const [agentReviewingKey, setAgentReviewingKey] = useState('');
  const [pinnedGroupIds, setPinnedGroupIds] = useState(() => loadPinnedGroupIds(user?.uid));
  const [pinnedHistoryIds, setPinnedHistoryIds] = useState(() => loadPinnedHistoryIds(user?.uid));
  const [hiddenHistoryIds, setHiddenHistoryIds] = useState(() => loadHiddenHistoryIds(user?.uid));
  const [openFriendMenuId, setOpenFriendMenuId] = useState('');
  const [openChatMenuKey, setOpenChatMenuKey] = useState('');
  const [chatMenuPlacement, setChatMenuPlacement] = useState('down');
  const [unreadFriendTopicIds, setUnreadFriendTopicIds] = useState(() => new Set());
  const [openProjectMenuId, setOpenProjectMenuId] = useState(null);
  const [newTaskProject, setNewTaskProject] = useState(null);
  const [taskPickerProject, setTaskPickerProject] = useState(null);
  const [selectedProjectTaskIds, setSelectedProjectTaskIds] = useState(() => new Set());
  const [showContactActions, setShowContactActions] = useState(false);
  const [friendActionId, setFriendActionId] = useState('');
  const [dismissedTaskStatuses, setDismissedTaskStatuses] = useState(() => loadDismissedTaskStatuses(user?.uid));
  const [projectPickerTask, setProjectPickerTask] = useState(null);
  const [showCreateProject, setShowCreateProject] = useState(false);
  const [newProjectName, setNewProjectName] = useState('');
  const [projectActionTopicId, setProjectActionTopicId] = useState('');
  const [expandedProjectId, setExpandedProjectId] = useState(null);
  const [editingProject, setEditingProject] = useState(null);
  const [projectNameDraft, setProjectNameDraft] = useState('');
  const [projectActionId, setProjectActionId] = useState(null);
  const [editingHistoryTopicId, setEditingHistoryTopicId] = useState('');
  const [historyNameDraft, setHistoryNameDraft] = useState('');
  const [renamingTopicId, setRenamingTopicId] = useState('');
  const [sidebarTimeNowMs, setSidebarTimeNowMs] = useState(() => Date.now());
  const [compactTaskHint, setCompactTaskHint] = useState(null);
  const justHiddenHistoryRef = useRef('');
  const activeTopicRef = useRef(activeTopic);
  const userUidRef = useRef(user?.uid);
  const chatMenuTriggerRef = useRef(null);
  const chatMenuRef = useRef(null);
  const chatsRef = useRef(chats);
  const friendsRef = useRef(friends);
  const agentsRef = useRef(agents);
  const friendSyncPromiseRef = useRef(null);
  const friendSyncQueuedRef = useRef(false);

  useEffect(() => {
    setCollapsed(loadCollapsedSections(user?.uid));
    setPinnedGroupIds(loadPinnedGroupIds(user?.uid));
    setPinnedHistoryIds(loadPinnedHistoryIds(user?.uid));
    setHiddenHistoryIds(loadHiddenHistoryIds(user?.uid));
    setDismissedTaskStatuses(loadDismissedTaskStatuses(user?.uid));
    setUnreadFriendTopicIds(new Set());
  }, [user?.uid]);

  useEffect(() => {
    activeTopicRef.current = activeTopic;
  }, [activeTopic]);

  useEffect(() => {
    userUidRef.current = user?.uid;
  }, [user?.uid]);

  useEffect(() => {
    chatsRef.current = chats;
  }, [chats]);

  useEffect(() => {
    friendsRef.current = friends;
  }, [friends]);

  useEffect(() => {
    agentsRef.current = agents;
  }, [agents]);

  useEffect(() => {
    let midnightTimer;
    const scheduleMidnightRefresh = () => {
      const now = new Date();
      const nextMidnight = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
      midnightTimer = window.setTimeout(() => {
        setSidebarTimeNowMs(Date.now());
        scheduleMidnightRefresh();
      }, Math.max(1000, nextMidnight.getTime() - now.getTime() + 50));
    };
    scheduleMidnightRefresh();
    return () => window.clearTimeout(midnightTimer);
  }, []);

  useEffect(() => {
    const topicId = String(activeTopic || '').trim();
    if (!topicId) return;
    setUnreadFriendTopicIds((previous) => removeSetValue(previous, topicId));
  }, [activeTopic]);

  const rememberDismissedTaskStatus = (topicId, status) => {
    const normalized = normalizeTaskStatus(status);
    if (!topicId || !isDismissibleTaskStatus(normalized)) return;
    const dismissedKey = taskStatusDismissKey(normalized);
    if (!dismissedKey) return;

    setDismissedTaskStatuses((previous) => {
      if (previous[topicId] === dismissedKey) return previous;
      const next = { ...previous, [topicId]: dismissedKey };
      saveDismissedTaskStatuses(userUidRef.current, next);
      return next;
    });
  };

  useEffect(() => {
    if (!activeTopic) return;
    const activeChat = chats.find((chat) => chat.id === activeTopic);
    if (activeChat?.taskStatus) rememberDismissedTaskStatus(activeTopic, activeChat.taskStatus);
  }, [activeTopic, chats]);

  useEffect(() => {
    if (!openFriendMenuId && !openChatMenuKey && !openProjectMenuId && !showContactActions) return undefined;
    const closeMenus = () => {
      setOpenFriendMenuId('');
      setOpenChatMenuKey('');
      setOpenProjectMenuId(null);
      setShowContactActions(false);
    };
    const closeMenusFromOutside = (event) => {
      const target = event.target;
      if (target instanceof Element && target.closest([
        '.v3-friend-action-menu',
        '.v3-friend-menu-trigger',
        '.v3-group-menu-trigger',
        '.v3-history-menu-trigger',
        '.cc-project-action-menu',
        '.cc-project-menu-trigger',
        '.cc-contact-section-menu',
        '.cc-contact-section-menu-trigger',
      ].join(','))) {
        return;
      }
      closeMenus();
    };
    const closeMenusOnEscape = (event) => {
      if (event.key === 'Escape') closeMenus();
    };
    document.addEventListener('pointerdown', closeMenusFromOutside);
    document.addEventListener('keydown', closeMenusOnEscape);
    return () => {
      document.removeEventListener('pointerdown', closeMenusFromOutside);
      document.removeEventListener('keydown', closeMenusOnEscape);
    };
  }, [openFriendMenuId, openChatMenuKey, openProjectMenuId, showContactActions]);

  useLayoutEffect(() => {
    if (!openChatMenuKey || !chatMenuTriggerRef.current || !chatMenuRef.current) return undefined;

    const trigger = chatMenuTriggerRef.current;
    const menu = chatMenuRef.current;
    const scrollContainer = trigger.closest('.v3-chat-list');
    const updatePlacement = () => {
      const triggerRect = trigger.getBoundingClientRect();
      const menuRect = menu.getBoundingClientRect();
      const boundaryRect = scrollContainer?.getBoundingClientRect() || {
        top: 0,
        bottom: window.innerHeight,
      };
      const availableBelow = boundaryRect.bottom - triggerRect.bottom;
      const availableAbove = triggerRect.top - boundaryRect.top;
      const nextPlacement = menuRect.height > availableBelow && availableAbove > availableBelow
        ? 'up'
        : 'down';
      setChatMenuPlacement((current) => current === nextPlacement ? current : nextPlacement);
    };

    updatePlacement();
    scrollContainer?.addEventListener('scroll', updatePlacement, { passive: true });
    window.addEventListener('resize', updatePlacement);
    return () => {
      scrollContainer?.removeEventListener('scroll', updatePlacement);
      window.removeEventListener('resize', updatePlacement);
    };
  }, [openChatMenuKey]);

  useEffect(() => {
    const openNewTask = () => {
      setNewTaskProject(null);
      setShowNewChat(true);
    };
    window.addEventListener('catsco:new-task', openNewTask);
    return () => window.removeEventListener('catsco:new-task', openNewTask);
  }, []);

  const toggleCollapsed = (section) => {
    setCollapsed((prev) => {
      const next = { ...prev, [section]: !prev[section] };
      saveCollapsedSections(user?.uid, next);
      return next;
    });
  };

  const togglePinnedGroup = (topicId) => {
    setPinnedGroupIds((prev) => {
      const next = new Set(prev);
      const key = String(topicId || '').trim();
      if (!key) return prev;
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      savePinnedGroupIds(user?.uid, next);
      return next;
    });
  };

  const togglePinnedTask = (chat) => {
    const key = String(chat?.id || '').trim();
    if (!key) return;
    const isPinned = pinnedHistoryIds.has(key) || (chat?.isGroup && pinnedGroupIds.has(key));
    setPinnedHistoryIds((previous) => {
      const next = new Set(previous);
      if (isPinned) next.delete(key);
      else next.add(key);
      savePinnedHistoryIds(user?.uid, next);
      return next;
    });
    if (chat?.isGroup && pinnedGroupIds.has(key)) {
      setPinnedGroupIds((previous) => {
        const next = new Set(previous);
        next.delete(key);
        savePinnedGroupIds(user?.uid, next);
        return next;
      });
    }
  };

  const hideHistoryTask = (topicId) => {
    setHiddenHistoryIds((prev) => {
      const key = String(topicId || '').trim();
      if (!key || prev.has(key)) return prev;
      justHiddenHistoryRef.current = key;
      const next = new Set(prev);
      next.add(key);
      saveHiddenHistoryIds(user?.uid, next);
      return next;
    });
  };

  const restoreHistoryTask = (topicId) => {
    setHiddenHistoryIds((prev) => {
      const key = String(topicId || '').trim();
      if (!key || !prev.has(key)) return prev;
      const next = new Set(prev);
      next.delete(key);
      saveHiddenHistoryIds(user?.uid, next);
      return next;
    });
  };

  useEffect(() => {
    const key = String(activeTopic || '').trim();
    if (justHiddenHistoryRef.current && justHiddenHistoryRef.current !== key) {
      justHiddenHistoryRef.current = '';
    }
    if (!key || justHiddenHistoryRef.current === key || !hiddenHistoryIds.has(key)) return;
    const reopenedTask = chats.some((chat) => String(chat.id) === key && isHistoryTask(chat));
    if (reopenedTask) restoreHistoryTask(key);
  }, [activeTopic, chats, hiddenHistoryIds]);

  const loadAgentPendingRequests = async (nextAgents) => {
    const ownedAgents = (nextAgents || []).filter(isOwnedAgent);
    if (ownedAgents.length === 0) {
      setAgentPendingRequests([]);
      return;
    }

    const results = await Promise.all(ownedAgents.map(async (agent) => {
      const agentId = agent.uid || agent.id;
      if (!agentId) return { agentId: '', requests: [] };
      try {
        const res = await api.getPendingRequests(agentId);
        return {
          agentId: String(agentId),
          requests: (res.requests || []).map((request) => ({
            ...request,
            agent_uid: agentId,
            agent_name: agent.display_name || agent.username || `助手 ${agentId}`,
          })),
        };
      } catch (error) {
        console.warn(`Failed to load friend requests for Agent ${agentId}:`, error);
        return { agentId: String(agentId), requests: null };
      }
    }));
    const ownedAgentIds = new Set(results.map((result) => result.agentId).filter(Boolean));
    setAgentPendingRequests((previous) => results.flatMap((result) => {
      if (result.requests) return result.requests;
      return previous.filter((request) => (
        ownedAgentIds.has(String(request.agent_uid))
        && String(request.agent_uid) === result.agentId
      ));
    }));
  };

  const syncFriendState = () => {
    if (friendSyncPromiseRef.current) {
      friendSyncQueuedRef.current = true;
      return friendSyncPromiseRef.current;
    }

    const syncPromise = (async () => {
      const [friendResult, pendingResult, agentResult] = await Promise.all([
        api.getFriends()
          .then((value) => ({ value }))
          .catch((error) => ({ error })),
        api.getPendingRequests()
          .then((value) => ({ value }))
          .catch((error) => ({ error })),
        api.getAgents()
          .then((value) => ({ value }))
          .catch((error) => ({ error })),
      ]);

      if (!friendResult.error && Array.isArray(friendResult.value?.friends)) {
        setFriends(friendResult.value.friends);
      }
      if (!pendingResult.error && Array.isArray(pendingResult.value?.requests)) {
        setPending(pendingResult.value.requests);
      }

      const nextAgents = !agentResult.error && Array.isArray(agentResult.value?.agents)
        ? agentResult.value.agents
        : agentsRef.current;
      if (!agentResult.error && Array.isArray(agentResult.value?.agents)) {
        setAgents(nextAgents);
      }
      await loadAgentPendingRequests(nextAgents);

      [friendResult, pendingResult, agentResult].forEach((result) => {
        if (result.error) console.warn('Failed to synchronize friend data:', result.error);
      });
    })();

    friendSyncPromiseRef.current = syncPromise;
    syncPromise.finally(() => {
      if (friendSyncPromiseRef.current === syncPromise) {
        friendSyncPromiseRef.current = null;
      }
      if (friendSyncQueuedRef.current) {
        friendSyncQueuedRef.current = false;
        syncFriendState();
      }
    });
    return syncPromise;
  };

  const broadcastFriendSync = () => {
    if (typeof window === 'undefined' || !window.localStorage) return;
    try {
      window.localStorage.setItem(friendSyncStorageKey(userUidRef.current), JSON.stringify({
        at: Date.now(),
        nonce: Math.random().toString(36).slice(2),
      }));
    } catch (error) {
      console.warn('Failed to notify other tabs about friend changes:', error);
    }
  };

  const loadAll = async () => {
    try {
      const [resC, resF, resG, resP, resA, resProjects] = await Promise.all([
        api.getConversations().catch((error) => ({ error })),
        api.getFriends().catch(()=>({})),
        api.getGroups().catch(()=>({})),
        api.getPendingRequests().catch(()=>({})),
        api.getAgents().catch(()=>({})),
        api.getProjects().catch(()=>({})),
      ]);
      const groups = resG.groups || [];
      const conversationItems = resC.conversations || [];
      const conversations = conversationItems.map(conversationSummaryToChat);
      const friends = resF.friends || [];
      const fallbackConversations = resC.error
        ? [...groups.map(groupToConversation), ...friends.map((friend) => friendToConversation(user.uid, friend))]
        : [];
      setChats(resC.error ? fallbackConversations : conversations);
      setFriends(friends);
      setGroups(groups);
      if (resC.error) {
        console.error('Failed to load conversations, falling back to groups:', resC.error);
      }
      setPending(resP.requests || []);
      const nextAgents = resA.agents || [];
      setAgents(nextAgents);
      setProjects(resProjects.projects || []);
      await loadAgentPendingRequests(nextAgents);
    } catch (e) {
      console.error('Failed to load sidebar data:', e);
    }
  };

  useEffect(() => { loadAll(); }, []);

  useEffect(() => {
    const reload = () => loadAll();
    window.addEventListener('cc:data-changed', reload);
    return () => window.removeEventListener('cc:data-changed', reload);
  }, []);

  useEffect(() => {
    const syncWhenVisible = () => {
      if (typeof document === 'undefined' || document.visibilityState === 'visible') {
        syncFriendState();
      }
    };
    const syncFromOtherTab = (event) => {
      if (event.key === friendSyncStorageKey(userUidRef.current)) {
        syncFriendState();
      }
    };
    document.addEventListener('visibilitychange', syncWhenVisible);
    window.addEventListener('online', syncWhenVisible);
    window.addEventListener('storage', syncFromOtherTab);
    return () => {
      document.removeEventListener('visibilitychange', syncWhenVisible);
      window.removeEventListener('online', syncWhenVisible);
      window.removeEventListener('storage', syncFromOtherTab);
    };
  }, []);

  useEffect(() => {
    const unsub = onWSMessage((msg) => {
      if (msg?._type === 'ws_open' || msg?.friend) {
        syncFriendState();
      }
      if (msg.data) {
        const topicId = msg.data.topic;
        const seq = msg.data.seq;
        const senderUid = numericUid(msg.data.from_uid ?? msg.data.from);
        const currentChat = chatsRef.current.find((chat) => chat.id === topicId);
        const isKnownFriend = currentChat
          ? !currentChat.isGroup && !currentChat.isBot
          : friendsRef.current.some((friend) => numericUid(friend?.id ?? friend?.uid) === senderUid);
        const isKnownAgent = agentsRef.current.some((agent) => (
          numericUid(agent?.id ?? agent?.uid) === senderUid
        ));
        const isUnreadFriendMessage = String(topicId || '').startsWith('p2p_')
          && senderUid > 0
          && senderUid !== numericUid(userUidRef.current)
          && isKnownFriend
          && !isKnownAgent
          && activeTopicRef.current !== topicId;
        if (isUnreadFriendMessage) {
          setUnreadFriendTopicIds((previous) => addSetValue(previous, topicId));
        }
        updateTopicSeq(topicId, seq);
        setChats((prev) => {
          const idx = prev.findIndex((c) => c.id === topicId);
          if (idx !== -1) {
            const updated = {
              ...prev[idx],
              preview: summarizeMessage({ content: msg.data.content }),
              time: formatSidebarTime(Date.now()),
              lastTimeMs: Date.now(),
              seq,
            };
            return [updated, ...prev.filter((c) => c.id !== topicId)];
          }
          if (topicId.startsWith('grp_') || topicId.startsWith('p2p_')) {
            loadAll();
          }
          return prev;
        });
      }

      const taskStatus = normalizeTaskStatus(msg.task_status || msg.ctrl?.params?.task_status);
      if (taskStatus?.topic_id) {
        const topicId = taskStatus.topic_id;
        const updatedAtMs = taskStatusUpdatedMs(taskStatus) || Date.now();
        const shouldDismissImmediately = isDismissibleTaskStatus(taskStatus) && activeTopicRef.current === topicId;
        if (shouldDismissImmediately) rememberDismissedTaskStatus(topicId, taskStatus);

        setChats((previous) => {
          const index = previous.findIndex((chat) => chat.id === topicId);
          if (index === -1) {
            if (topicId.startsWith('grp_') || topicId.startsWith('p2p_')) loadAll();
            return previous;
          }
          const currentUpdatedAtMs = taskStatusUpdatedMs(previous[index].taskStatus);
          if (currentUpdatedAtMs && updatedAtMs < currentUpdatedAtMs) return previous;

          const updated = {
            ...previous[index],
            taskStatus,
            time: formatSidebarTime(updatedAtMs),
            lastTimeMs: Math.max(updatedAtMs, previous[index].lastTimeMs || 0),
          };
          return [updated, ...previous.filter((_, itemIndex) => itemIndex !== index)];
        });
      }

      if (msg.pres && msg.pres.what && msg.pres.what.startsWith('group_')) { loadAll(); }
      if (msg.pres && msg.pres.what === 'members_invited') { loadAll(); }
      // 同步 Bot 在线/离线状态到会话列表
      if (msg.pres && (msg.pres.what === 'on' || msg.pres.what === 'off')) {
        const rawUid = msg.pres.src || '';
        const uid = rawUid.startsWith('usr') ? parseInt(rawUid.slice(3), 10) : parseInt(rawUid, 10);
        if (uid > 0) {
          setChats((prev) => prev.map((c) => {
            if (!c.isGroup && c.friendId === uid) {
              return { ...c, isOnline: msg.pres.what === 'on' };
            }
            return c;
          }));
        }
      }
    });
    return () => unsub();
  }, []);

  const handleGroupCreated = (created) => {
    const group = normalizeCreatedGroup(created);
    if (group) {
      const topicId = created.topic || `grp_${group.id}`;
      const createdAtMs = toTimeMs(group.created_at) || Date.now();
      setChats((prev) => [
        {
          id: topicId,
          groupId: group.id,
          name: group.name,
          preview: '',
          time: formatSidebarTime(createdAtMs),
          lastTimeMs: createdAtMs,
          createdAtMs,
          isGroup: true,
          avatar_url: group.avatar_url,
          hasBot: Boolean(group.has_bot),
          isAgentTask: Boolean(group.is_agent_task || group.kind === 'agent_task'),
          memberCount: Number(group.member_count || 0),
          agentIds: taskAgentIdsFromPayload(group),
          memberIds: normalizedEntityIds(group.member_ids),
          seq: 0,
        },
        ...prev.filter((chat) => chat.id !== topicId),
      ]);
      setGroups((prev) => [group, ...prev.filter((item) => String(item.id) !== String(group.id))]);
    }
    loadAll();
  };
  const handleAccept = async (userId) => {
    try {
      await api.acceptFriend(userId);
      await loadAll();
      broadcastFriendSync();
      feedback.notify({ tone: 'success', message: '已接受好友申请' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '接受失败', message: err.message || '请稍后重试' });
    }
  };
  const handleReject = async (userId) => {
    try {
      await api.rejectFriend(userId);
      await loadAll();
      broadcastFriendSync();
      feedback.notify({ tone: 'info', message: '已拒绝好友申请' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '拒绝失败', message: err.message || '请稍后重试' });
    }
  };
  const groupOwnerById = new Map(groups.map((group) => [String(group.id), String(group.owner_id)]));

  const handleReviewAgentRequest = async (request, action) => {
    const agentId = request?.agent_uid;
    const fromUID = request?.from_user_id;
    if (!agentId || !fromUID) return;
    const key = `${agentId}:${fromUID}`;
    try {
      setAgentReviewingKey(key);
      if (action === 'accept') {
        await api.acceptAgentFriend(agentId, fromUID);
      } else {
        await api.rejectAgentFriend(agentId, fromUID);
      }
      await loadAll();
      broadcastFriendSync();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({
        tone: 'success',
        message: action === 'accept' ? '已接受助手好友申请' : '已拒绝助手好友申请',
      });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '操作失败', message: err.message || '处理助手好友申请失败' });
    } finally {
      setAgentReviewingKey('');
    }
  };

  const handleRemoveAgent = async (agent) => {
    const agentId = agent?.uid || agent?.id;
    if (!agentId || isOwnedAgent(agent)) return;
    const confirmed = await feedback.confirm({
      title: '移除 AI 助手？',
      message: `将从列表中移除“${agent.display_name || agent.username}”。这只会解除好友关系，不会删除对方创建的虚拟员工。`,
      confirmLabel: '移除',
      tone: 'danger',
    });
    if (!confirmed) return;
    try {
      setAgentActionId(String(agentId));
      await api.removeFriend(agentId);
      const topicId = agent.topic_id || p2pTopicId(user.uid, agentId);
      if (activeTopic === topicId) {
        onSelectTopic(null);
      }
      await loadAll();
      broadcastFriendSync();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '已移除助手' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '移除失败', message: err.message || '移除助手失败' });
    } finally {
      setAgentActionId('');
    }
  };

  const handleFriendAction = async (chat, action) => {
    const friendId = chat?.friendId;
    if (!friendId) return;
    const isBlock = action === 'block';
    const confirmed = await feedback.confirm({
      title: isBlock ? `拉黑“${chat.name}”？` : `删除好友“${chat.name}”？`,
      message: isBlock ? '拉黑后，对方将无法再向你发送消息。' : '删除后，需要重新添加才能恢复好友关系。',
      confirmLabel: isBlock ? '拉黑' : '删除',
      tone: 'danger',
    });
    if (!confirmed) return;

    try {
      setFriendActionId(String(friendId));
      if (isBlock) {
        await api.blockUser(friendId);
      } else {
        await api.removeFriend(friendId);
      }
      if (activeTopic === chat.id) onSelectTopic(null);
      setOpenFriendMenuId('');
      await loadAll();
      broadcastFriendSync();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: isBlock ? '已拉黑好友' : '已删除好友' });
    } catch (err) {
      feedback.notify({
        tone: 'error',
        title: isBlock ? '拉黑失败' : '删除失败',
        message: err.message || (isBlock ? '拉黑好友失败' : '删除好友失败'),
      });
    } finally {
      setFriendActionId('');
    }
  };

  const handleDeleteGroup = async ({ groupId, topicId, name }) => {
    if (!groupId || !topicId) return;

    const confirmed = await feedback.confirm({
      title: `永久删除群聊“${name}”？`,
      message: '删除后会移除群聊、所有成员和聊天记录，且无法恢复。',
      confirmLabel: '永久删除',
      tone: 'danger',
    });
    if (!confirmed) return;

    setDeletingTopicId(topicId);
    try {
      await api.disbandGroup(groupId);
      if (activeTopic === topicId) {
        onSelectTopic(null);
      }
      await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '群聊已删除' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '删除失败', message: err.message || '删除群聊失败' });
    } finally {
      setDeletingTopicId('');
    }
  };

  const handleSelectAgent = (agent) => {
    const agentId = agent.uid || agent.id;
    if (!agentId) return;
    onStartAgentTask?.(agent);
  };

  const openNewTaskDialog = (project = null) => {
    setNewTaskProject(project);
    setShowNewChat(true);
  };

  const closeNewTaskDialog = () => {
    setShowNewChat(false);
    setNewTaskProject(null);
  };

  const handleNewChatWithAgent = (agent) => {
    const agentId = agent.uid || agent.id;
    if (!agentId) return;
    const project = newTaskProject;
    closeNewTaskDialog();
    if (project) {
      onStartAgentTask?.(agent, {
        projectId: Number(project.id),
        projectName: String(project.name || ''),
      });
    } else {
      onStartAgentTask?.(agent);
    }
  };

  const trimmedSearch = search.trim();
  const lowerSearch = trimmedSearch.toLowerCase();
  const isSearching = trimmedSearch.length > 0;
  const recentChats = sortConversationsByRecent(chats);
  const visibleRecentChats = recentChats.filter((chat) => (
    !isHistoryTask(chat) || chat.isGroup || !hiddenHistoryIds.has(String(chat.id))
  ));
  const filteredChats = visibleRecentChats.filter(c => c.name.toLowerCase().includes(lowerSearch));
  const directChats = filteredChats.filter(c => !c.isGroup);
  const mergedGroups = mergeGroupsWithConversations(groups, chats.filter(c => c.isGroup));
  const filteredFriends = friends.filter(f => userSearchText(f).includes(lowerSearch));
  const filteredGroups = mergedGroups.filter(g => g.name.toLowerCase().includes(lowerSearch));
  const filteredAgents = agents.filter(a => userSearchText(a).includes(lowerSearch));
  const projectTasksById = visibleRecentChats.reduce((result, chat) => {
    const projectId = projectIdFor(chat);
    if (!isHistoryTask(chat) || !projectId) return result;
    const tasks = result.get(projectId) || [];
    tasks.push(chat);
    result.set(projectId, tasks);
    return result;
  }, new Map());
  const filteredProjects = projects.filter((project) => {
    if (String(project.name || '').toLowerCase().includes(lowerSearch)) return true;
    return (projectTasksById.get(Number(project.id)) || []).some((chat) => chat.name.toLowerCase().includes(lowerSearch));
  });

  const taskCandidates = [
    ...filteredChats.filter((chat) => !chat.isGroup),
    ...filteredGroups,
  ];
  const taskChats = sortConversationsWithPins(
    taskCandidates.filter((chat) => (
      isHistoryTask(chat)
      && !projectIdFor(chat)
      && (chat.isGroup || !hiddenHistoryIds.has(String(chat.id)))
    )),
    pinnedHistoryIds,
  );
  const friendChats = directChats.filter(c => !c.isBot);
  const groupTaskIds = new Set(taskChats.filter((chat) => chat.isGroup).map((chat) => String(chat.id)));
  const taskConversationIsPinned = (chat) => (
    pinnedHistoryIds.has(String(chat.id))
    || (groupTaskIds.has(String(chat.id)) && pinnedGroupIds.has(String(chat.id)))
  );
  const conversationChats = taskChats
    .sort((left, right) => {
      const pinDifference = Number(taskConversationIsPinned(right)) - Number(taskConversationIsPinned(left));
      return pinDifference || conversationRecentLess(left, right);
    });
  const availableProjectTasks = sortConversationsWithPins(
    [
      ...visibleRecentChats.filter((chat) => !chat.isGroup),
      ...mergedGroups,
    ].filter((chat) => (
      isHistoryTask(chat)
      && !projectIdFor(chat)
      && (chat.isGroup || !hiddenHistoryIds.has(String(chat.id)))
    )),
    pinnedHistoryIds,
  ).sort((left, right) => {
    const pinDifference = Number(taskConversationIsPinned(right)) - Number(taskConversationIsPinned(left));
    return pinDifference || conversationRecentLess(left, right);
  });
  const agentById = new Map();
  agents.forEach((agent) => {
    [agent?.id, agent?.uid].filter(Boolean).forEach((agentId) => {
      agentById.set(String(agentId), agent);
    });
  });
  const agentContactIds = new Set(agentById.keys());
  const friendContactMap = new Map();
  filteredFriends
    .filter((friend) => {
      const friendId = friend?.id || friend?.uid;
      return !isBotContact(friend) && !agentContactIds.has(String(friendId || ''));
    })
    .forEach((friend) => {
      const chat = friendToConversation(user.uid, friend);
      friendContactMap.set(String(chat.friendId), { ...chat, contact: friend });
    });
  friendChats.forEach((chat) => {
    const key = String(chat.friendId || '');
    if (!key || agentContactIds.has(key)) return;
    const existing = friendContactMap.get(key);
    friendContactMap.set(key, { ...existing, ...chat, contact: existing?.contact || null });
  });
  const friendContacts = [...friendContactMap.values()];
  const hasUnreadFriendMessages = friendContacts.some((chat) => unreadFriendTopicIds.has(chat.id));
  const contactItems = [
    ...friendContacts.map((chat) => ({ kind: 'friend', name: chat.name, recentMs: conversationSortTime(chat), pinned: false, item: chat })),
    ...filteredAgents.map((agent) => ({
      kind: 'agent',
      name: agent.display_name || agent.username || '',
      recentMs: latestAgentTaskTime(taskChats, agent),
      pinned: false,
      item: agent,
    })),
  ].sort((left, right) => (
    Number(left.kind === 'agent') - Number(right.kind === 'agent')
    || Number(right.pinned) - Number(left.pinned)
    || (right.recentMs || 0) - (left.recentMs || 0)
    || left.name.localeCompare(right.name, 'zh-CN')
  ));
  const hasSearchResults = conversationChats.length > 0 || contactItems.length > 0 || filteredProjects.length > 0;
  const compactChats = conversationChats.slice(0, 12);

  const selectConversation = (chat) => {
    rememberDismissedTaskStatus(chat.id, chat.taskStatus);
    setUnreadFriendTopicIds((previous) => removeSetValue(previous, chat.id));
    onSelectTopic({
      topicId: chat.id,
      name: chat.name,
      isGroup: chat.isGroup,
      groupId: chat.groupId,
      avatar_url: chat.avatar_url,
      friendId: chat.friendId,
      isBot: Boolean(chat.isBot),
      hasBot: Boolean(chat.hasBot),
      isAgentTask: Boolean(chat.isAgentTask),
      memberCount: Number(chat.memberCount || 0),
    });
  };

  const topicPayloadForChat = (chat, isGroup = Boolean(chat?.isGroup)) => ({
    topicId: chat?.id,
    name: chat?.name,
    isGroup,
    groupId: chat?.groupId,
    avatar_url: chat?.avatar_url,
    friendId: chat?.friendId,
    isBot: chat?.isBot,
  });

  const handleOpenMobileLink = (chat, isGroup = Boolean(chat?.isGroup)) => {
    const payload = topicPayloadForChat(chat, isGroup);
    setOpenChatMenuKey('');
    if (onOpenMobileLink) {
      onOpenMobileLink(payload);
      return;
    }
    if (isGroup) {
      setMobileLinkGroup({ groupId: chat.groupId, topicId: chat.id, name: chat.name });
    } else if (chat.friendId) {
      setMobileLinkAgent({ uid: chat.friendId, display_name: chat.name });
    }
  };

  const handleOpenCollaborationManagement = (chat) => {
    setOpenChatMenuKey('');
    if (chat?.isGroup && chat?.groupId) {
      onManageGroup?.(topicPayloadForChat(chat, true));
      return;
    }
    if (!chat?.friendId) {
      feedback.notify({
        tone: 'error',
        title: '暂时无法管理协作',
        message: '没有找到当前任务中的 Agent，请刷新后重试。',
      });
      return;
    }
    setCollaborationUpgradeTask(chat);
  };

  const createCollaborationUpgrade = async (name, memberIds) => {
    const sourceTask = collaborationUpgradeTask;
    if (!sourceTask?.friendId) throw new Error('没有找到当前任务中的 Agent');

    const created = await api.createGroup(name, memberIds);
    const groupId = created?.group?.id || created?.group_id;
    const topicId = created?.topic || created?.topic_id || (groupId ? `grp_${groupId}` : '');
    if (!groupId || !topicId) throw new Error('协作任务创建失败，请稍后重试');

    const rollbackCreatedGroup = async ({ removeProjectAssignment = false } = {}) => {
      if (removeProjectAssignment) {
        try {
          await api.removeProjectTopic(topicId);
        } catch (rollbackError) {
          console.warn('Failed to roll back collaboration project assignment:', rollbackError);
        }
      }
      try {
        await api.disbandGroup(groupId);
      } catch (rollbackError) {
        console.warn('Failed to roll back collaboration upgrade:', rollbackError);
      }
    };

    const expectedMemberIds = new Set(
      memberIds
        .map((memberId) => numericUid(memberId))
        .filter((memberId) => memberId > 0 && memberId !== numericUid(user?.uid)),
    );
    const createdGroup = created?.group || created;
    const createdMemberCount = Number(createdGroup?.member_count || created?.member_count || 0);
    const createdAgentIds = new Set(
      normalizedEntityIds(createdGroup?.agent_ids || created?.agent_ids).map(String),
    );
    const sourceAgentId = String(numericUid(sourceTask.friendId));
    const hasAllMembers = createdMemberCount >= expectedMemberIds.size + 1;
    const hasSourceAgent = createdAgentIds.has(sourceAgentId);
    if (!hasAllMembers || !hasSourceAgent) {
      await rollbackCreatedGroup();
      throw new Error('部分协作成员添加失败，原任务未作修改，请重试');
    }

    const projectId = projectIdFor(sourceTask);
    if (projectId > 0) {
      let assignedNewTopic = false;
      try {
        await api.assignProjectTopic(projectId, topicId);
        assignedNewTopic = true;
        await api.removeProjectTopic(sourceTask.id);
      } catch (error) {
        await rollbackCreatedGroup({ removeProjectAssignment: assignedNewTopic });
        throw error;
      }
    }

    return created;
  };

  const handleCollaborationUpgradeCreated = (created) => {
    const sourceTask = collaborationUpgradeTask;
    const group = normalizeCreatedGroup(created);
    if (!sourceTask || !group) return;

    const topicId = created.topic || created.topic_id || `grp_${group.id}`;
    hideHistoryTask(sourceTask.id);
    handleGroupCreated(created);
    onSelectTopic({
      topicId,
      name: group.name,
      isGroup: true,
      groupId: group.id,
      avatar_url: group.avatar_url,
      hasBot: Boolean(group.has_bot),
      isAgentTask: Boolean(group.is_agent_task || group.kind === 'agent_task'),
      memberCount: Number(group.member_count || 0),
    });
    setCollaborationUpgradeTask(null);
    window.dispatchEvent(new Event('cc:data-changed'));
    feedback.notify({ tone: 'success', message: '已升级为协作任务' });
  };

  const closeProjectDialog = () => {
    setProjectPickerTask(null);
    setShowCreateProject(false);
    setNewProjectName('');
  };

  const handleOpenProjectPicker = (chat) => {
    setOpenChatMenuKey('');
    setProjectPickerTask(chat);
    setShowCreateProject(false);
  };

  const handleCreateTaskInProject = (project) => {
    setOpenProjectMenuId(null);
    openNewTaskDialog(project);
  };

  const handleOpenProjectTaskPicker = (project) => {
    setOpenProjectMenuId(null);
    setSelectedProjectTaskIds(new Set());
    setTaskPickerProject(project);
  };

  const closeProjectTaskPicker = () => {
    setTaskPickerProject(null);
    setSelectedProjectTaskIds(new Set());
  };

  const toggleProjectTaskSelection = (topicId) => {
    if (!topicId || projectActionTopicId) return;
    const key = String(topicId);
    setSelectedProjectTaskIds((previous) => {
      const next = new Set(previous);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const handleAddTasksToProject = async () => {
    if (!taskPickerProject?.id || selectedProjectTaskIds.size === 0) return;
    const project = taskPickerProject;
    const selectedTasks = availableProjectTasks.filter((chat) => (
      selectedProjectTaskIds.has(String(chat.id))
    ));
    if (selectedTasks.length === 0) return;

    setProjectActionTopicId('project-task-batch');
    try {
      const failedTaskIds = [];
      let completedCount = 0;

      for (const chat of selectedTasks) {
        try {
          await api.assignProjectTopic(project.id, chat.id);
          completedCount += 1;
        } catch {
          failedTaskIds.push(String(chat.id));
        }
      }

      if (completedCount > 0) {
        setExpandedProjectId(Number(project.id));
        await loadAll();
        window.dispatchEvent(new Event('cc:data-changed'));
      }

      if (failedTaskIds.length > 0) {
        setSelectedProjectTaskIds(new Set(failedTaskIds));
        feedback.notify({
          tone: 'warning',
          title: '部分任务未添加',
          message: `${failedTaskIds.length} 个任务添加失败，请重试`,
        });
        return;
      }

      closeProjectTaskPicker();
      feedback.notify({ tone: 'success', message: `已添加 ${completedCount} 个任务` });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '添加失败', message: err.message || '添加任务失败' });
    } finally {
      setProjectActionTopicId('');
    }
  };

  const handleAssignProject = async (project) => {
    if (!projectPickerTask || !project?.id) return;
    setProjectActionTopicId(projectPickerTask.id);
    try {
      await api.assignProjectTopic(project.id, projectPickerTask.id);
      setExpandedProjectId(Number(project.id));
      await loadAll();
      closeProjectDialog();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: `已加入项目“${project.name}”` });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '加入项目失败', message: err.message || '请稍后重试' });
    } finally {
      setProjectActionTopicId('');
    }
  };

  const handleRemoveFromProject = async (chat = projectPickerTask) => {
    if (!chat?.id) return;
    setOpenChatMenuKey('');
    setProjectActionTopicId(chat.id);
    try {
      await api.removeProjectTopic(chat.id);
      await loadAll();
      closeProjectDialog();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '已移出项目' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '移出项目失败', message: err.message || '请稍后重试' });
    } finally {
      setProjectActionTopicId('');
    }
  };

  const handleCreateProject = async () => {
    const name = newProjectName.trim();
    if (!name) return;
    const pendingTask = projectPickerTask;
    setProjectActionTopicId(pendingTask?.id || 'create-project');
    try {
      const res = await api.createProject(name);
      const project = res.project;
      if (pendingTask && project?.id) {
        await api.assignProjectTopic(project.id, pendingTask.id);
        setExpandedProjectId(Number(project.id));
      }
      await loadAll();
      closeProjectDialog();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: `项目“${name}”已创建` });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '创建项目失败', message: err.message || '请稍后重试' });
    } finally {
      setProjectActionTopicId('');
    }
  };

  const openProjectRename = (project) => {
    setOpenProjectMenuId(null);
    setEditingProject(project);
    setProjectNameDraft(String(project?.name || ''));
  };

  const closeProjectRename = () => {
    if (projectActionId) return;
    setEditingProject(null);
    setProjectNameDraft('');
  };

  const handleRenameProject = async () => {
    const projectId = Number(editingProject?.id);
    const name = projectNameDraft.trim();
    if (!projectId || !name) return;
    setProjectActionId(projectId);
    try {
      await api.renameProject(projectId, name);
      await loadAll();
      setEditingProject(null);
      setProjectNameDraft('');
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '项目名称已更新' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '更改名称失败', message: err.message || '更改项目名称失败' });
    } finally {
      setProjectActionId(null);
    }
  };

  const handleDeleteProject = async (project) => {
    const projectId = Number(project?.id);
    if (!projectId) return;
    setOpenProjectMenuId(null);
    const confirmed = await feedback.confirm({
      title: `删除项目“${project.name}”？`,
      message: '项目中的任务会保留，并回到任务列表。',
      confirmLabel: '删除项目',
      tone: 'danger',
    });
    if (!confirmed) return;
    setProjectActionId(projectId);
    try {
      await api.deleteProject(projectId);
      if (expandedProjectId === projectId) setExpandedProjectId(null);
      await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '项目已删除，任务已回到任务列表' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '删除项目失败', message: err.message || '请稍后重试' });
    } finally {
      setProjectActionId(null);
    }
  };

  const handleDeleteHistoryTask = async (chat) => {
    const actionLabel = onDeleteHistoryTask ? '删除任务' : '从列表移除';
    const confirmed = await feedback.confirm({
      title: onDeleteHistoryTask ? `删除任务“${chat.name}”？` : `从列表移除“${chat.name}”？`,
      message: onDeleteHistoryTask
        ? '删除后无法在任务列表中继续访问该任务。'
        : '此操作只影响当前浏览器，不会删除历史消息。',
      confirmLabel: actionLabel,
      tone: onDeleteHistoryTask ? 'danger' : 'default',
    });
    if (!confirmed) return;

    setOpenChatMenuKey('');
    setDeletingTopicId(chat.id);
    try {
      if (onDeleteHistoryTask) {
        await onDeleteHistoryTask(topicPayloadForChat(chat));
      }
      hideHistoryTask(chat.id);
      if (activeTopic === chat.id) onSelectTopic(null);
      if (onDeleteHistoryTask) await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: `${actionLabel}成功` });
    } catch (err) {
      feedback.notify({ tone: 'error', title: `${actionLabel}失败`, message: err.message || '请稍后重试' });
    } finally {
      setDeletingTopicId('');
    }
  };

  const startRenamingHistoryTask = (chat) => {
    setOpenChatMenuKey('');
    setEditingHistoryTopicId(chat.id);
    setHistoryNameDraft(chat.name);
  };

  const cancelRenamingHistoryTask = () => {
    if (renamingTopicId) return;
    setEditingHistoryTopicId('');
    setHistoryNameDraft('');
  };

  const handleRenameHistoryTask = async (event, chat) => {
    event.preventDefault();
    event.stopPropagation();
    const nextName = historyNameDraft.trim();
    if (!nextName || nextName === chat.name) {
      cancelRenamingHistoryTask();
      return;
    }

    setRenamingTopicId(chat.id);
    try {
      if (chat.isGroup && chat.groupId) {
        await api.updateGroup(chat.groupId, nextName, chat.avatar_url || '');
      } else {
        await api.updateConversationTitle(chat.id, nextName);
      }
      setChats((prev) => prev.map((item) => item.id === chat.id ? { ...item, name: nextName } : item));
      if (activeTopic === chat.id) {
        onSelectTopic({ ...topicPayloadForChat(chat), name: nextName });
      }
      setEditingHistoryTopicId('');
      setHistoryNameDraft('');
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '任务名称已更新' });
    } catch (err) {
      feedback.notify({ tone: 'error', title: '修改名称失败', message: err.message || '修改任务名称失败' });
    } finally {
      setRenamingTopicId('');
    }
  };

  const renderTaskCopy = (chat, fallback = null, kindLabel = '') => (
    <div className="cc-chat-row-copy">
      {editingHistoryTopicId === chat.id ? (
        <form className="cc-history-rename-form" onSubmit={(event) => handleRenameHistoryTask(event, chat)} onClick={(event) => event.stopPropagation()}>
          <input
            value={historyNameDraft}
            onChange={(event) => setHistoryNameDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Escape') {
                event.preventDefault();
                cancelRenamingHistoryTask();
              }
            }}
            aria-label={`修改任务名称 ${chat.name}`}
            maxLength={80}
            autoFocus
            disabled={renamingTopicId === chat.id}
          />
          <button type="submit" aria-label={`保存任务名称 ${chat.name}`} disabled={!historyNameDraft.trim() || renamingTopicId === chat.id}><Check size={13} /></button>
          <button type="button" aria-label={`取消修改任务名称 ${chat.name}`} onClick={cancelRenamingHistoryTask} disabled={renamingTopicId === chat.id}><X size={13} /></button>
        </form>
      ) : (
        <>
          <span className="cc-chat-row-title">
            <span className="v3-chat-item-label">{chat.name}</span>
            {kindLabel && <span className={`cc-item-kind cc-item-kind-${kindLabel === '群聊' || kindLabel === '群组' ? 'group' : kindLabel === '单聊' ? 'direct' : 'agent'}`}>{kindLabel}</span>}
          </span>
          {fallback && <span className="cc-chat-row-preview">{fallback}</span>}
        </>
      )}
    </div>
  );

  const displayTimeForChat = (chat) => {
    const timestamp = conversationSortTime(chat);
    return timestamp ? formatSidebarTime(timestamp, sidebarTimeNowMs) : chat.time || '';
  };

  const renderTaskControls = (chat, menuKey, { showPin = false, showTime = false } = {}) => {
    const isPinned = pinnedHistoryIds.has(String(chat.id))
      || (chat.isGroup && pinnedGroupIds.has(String(chat.id)));
    const visibleStatus = activeTopic === chat.id && normalizeTaskStatus(chat.taskStatus)?.state === 'completed'
      ? null
      : visibleTaskStatus(chat.taskStatus, dismissedTaskStatuses, chat.id);
    const canDeleteGroup = chat.isGroup
      && groupOwnerById.get(String(chat.groupId)) === String(user.uid);
    const assignedProjectId = projectIdFor(chat);
    const removeLabel = onDeleteHistoryTask ? '删除任务' : '从列表移除';
    const displayTime = displayTimeForChat(chat);
    return (
      <>
        <SidebarRowTrailing
          actions={(
            <>
              {showPin && (
                <button
                  type="button"
                  className="v3-chat-item-action v3-history-pin-trigger"
                  title={isPinned ? '取消置顶任务' : '置顶任务'}
                  aria-label={`${isPinned ? '取消置顶任务' : '置顶任务'} ${chat.name}`}
                  aria-pressed={isPinned}
                  onClick={(event) => {
                    event.stopPropagation();
                    togglePinnedTask(chat);
                    setOpenChatMenuKey('');
                  }}
                >
                  <Pin size={14} fill={isPinned ? 'currentColor' : 'none'} />
                </button>
              )}
              <button
                type="button"
                className="v3-chat-item-action v3-history-menu-trigger"
                title="任务操作"
                aria-label={`${chat.name} 更多操作`}
                aria-haspopup="menu"
                aria-expanded={openChatMenuKey === menuKey}
                onClick={(event) => {
                  event.stopPropagation();
                  setOpenFriendMenuId('');
                  if (openChatMenuKey === menuKey) {
                    setOpenChatMenuKey('');
                    return;
                  }
                  chatMenuTriggerRef.current = event.currentTarget;
                  setChatMenuPlacement('down');
                  setOpenChatMenuKey(menuKey);
                }}
              >
                <MoreHorizontal size={15} />
              </button>
            </>
          )}
        >
          <TaskRowStatusIndicator status={visibleStatus} time={displayTime} showTime={showTime} />
        </SidebarRowTrailing>
        {openChatMenuKey === menuKey && (
          <div
            ref={chatMenuRef}
            className={`v3-friend-action-menu cc-chat-action-menu ${chatMenuPlacement === 'up' ? 'cc-chat-action-menu-up' : ''}`}
            role="menu"
            onClick={(event) => event.stopPropagation()}
          >
            <button type="button" role="menuitem" aria-label={`修改任务名称 ${chat.name}`} onClick={() => startRenamingHistoryTask(chat)}>
              <Pencil size={14} />
              <span>修改任务名称</span>
            </button>
            <button
              type="button"
              role="menuitem"
              aria-label={`${assignedProjectId ? '移动到项目' : '加入项目'} ${chat.name}`}
              onClick={() => handleOpenProjectPicker(chat)}
            >
              <FolderPlus size={14} />
              <span>{assignedProjectId ? '移动到项目' : '加入项目'}</span>
            </button>
            {assignedProjectId > 0 && (
              <button
                type="button"
                role="menuitem"
                aria-label={`移出当前项目 ${chat.name}`}
                disabled={projectActionTopicId === chat.id}
                onClick={() => handleRemoveFromProject(chat)}
              >
                <X size={14} />
                <span>移出当前项目</span>
              </button>
            )}
            <button type="button" role="menuitem" aria-label={`${chat.name} 手机扫码`} onClick={() => handleOpenMobileLink(chat, chat.isGroup)}>
              <Smartphone size={14} />
              <span>手机扫码</span>
            </button>
            <button
              type="button"
              role="menuitem"
              aria-label={`${chat.name} 协作管理`}
              disabled={chat.isGroup && !onManageGroup}
              title={chat.isGroup && !onManageGroup ? '协作管理入口暂未接入' : '协作管理'}
              onClick={() => handleOpenCollaborationManagement(chat)}
            >
              <Users size={14} />
              <span>协作管理</span>
            </button>
            {canDeleteGroup ? (
              <button
                type="button"
                role="menuitem"
                className="danger"
                aria-label={`删除任务 ${chat.name}`}
                disabled={deletingTopicId === chat.id}
                onClick={() => {
                  setOpenChatMenuKey('');
                  handleDeleteGroup({ groupId: chat.groupId, topicId: chat.id, name: chat.name });
                }}
              >
                <Trash2 size={14} />
                <span>删除任务</span>
              </button>
            ) : !chat.isGroup && (
              <button
                type="button"
                role="menuitem"
                className="danger"
                aria-label={`${removeLabel} ${chat.name}`}
                disabled={deletingTopicId === chat.id}
                title={removeLabel}
                onClick={() => handleDeleteHistoryTask(chat)}
              >
                <Trash2 size={14} />
                <span>{removeLabel}</span>
              </button>
            )}
          </div>
        )}
      </>
    );
  };

  const renderConversationRow = (chat) => {
    const taskKind = conversationKind(chat);
    const displayTime = displayTimeForChat(chat);
    if (taskKind === 'solo_agent' || taskKind === 'multi_agent') {
      const taskLabel = taskKind === 'multi_agent' ? '协作' : '';
      const menuKey = `task:${chat.id}`;
      return (
        <SidebarItemRow
          key={chat.id}
          className="cc-history-item cc-conversation-item"
          active={activeTopic === chat.id}
          data-conversation-kind="agent"
          data-task-kind={taskKind === 'multi_agent' ? 'collaboration' : 'solo'}
          onClick={() => selectConversation(chat)}
        >
          {renderTaskAgentIcon(chat, agentById, onlineUsers)}
          {renderTaskCopy(chat, null, taskLabel)}
          {renderTaskControls(chat, menuKey, { showPin: true, showTime: true })}
        </SidebarItemRow>
      );
    }

    if (chat.isGroup && !chat.isAgentTask) {
      const canDelete = groupOwnerById.get(String(chat.groupId)) === String(user.uid);
      const isPinned = pinnedGroupIds.has(String(chat.id));
      const menuKey = `group:${chat.id}`;
      return (
        <SidebarItemRow
          key={chat.id}
          className="cc-conversation-item cc-group-conversation-item"
          active={activeTopic === chat.id}
          data-conversation-kind="group"
          onClick={() => selectConversation(chat)}
        >
          <Users size={14} className="prefix cc-chat-row-icon" aria-label="群聊" />
          {renderTaskCopy(chat, chat.preview, '群聊')}
          <SidebarRowTrailing
            actions={(
              <button
                type="button"
                className="v3-chat-item-action v3-group-menu-trigger"
                title="群聊操作"
                aria-label={`${chat.name} 更多操作`}
                aria-haspopup="menu"
                aria-expanded={openChatMenuKey === menuKey}
                onClick={(event) => {
                  event.stopPropagation();
                  setOpenFriendMenuId('');
                  setOpenChatMenuKey((current) => current === menuKey ? '' : menuKey);
                }}
              >
                <MoreHorizontal size={15} />
              </button>
            )}
          >
            {displayTime && <span className="cc-chat-row-time">{displayTime}</span>}
          </SidebarRowTrailing>
          {openChatMenuKey === menuKey && (
            <div className="v3-friend-action-menu cc-chat-action-menu" role="menu" onClick={(event) => event.stopPropagation()}>
              <button
                type="button"
                role="menuitem"
                aria-label={`${isPinned ? '取消置顶' : '置顶'} ${chat.name}`}
                onClick={() => {
                  togglePinnedGroup(chat.id);
                  setOpenChatMenuKey('');
                }}
              >
                <Pin size={14} fill={isPinned ? 'currentColor' : 'none'} />
                <span>{isPinned ? '取消置顶群聊' : '置顶群聊'}</span>
              </button>
              <button type="button" role="menuitem" aria-label={`${chat.name} 移动端使用`} onClick={() => handleOpenMobileLink(chat, true)}>
                <Smartphone size={14} />
                <span>移动端使用</span>
              </button>
              <button
                type="button"
                role="menuitem"
                aria-label={`${chat.name} 协作管理`}
                disabled={!onManageGroup}
                title={!onManageGroup ? '协作管理入口暂未接入' : '协作管理'}
                onClick={() => {
                  setOpenChatMenuKey('');
                  onManageGroup?.(topicPayloadForChat(chat, true));
                }}
              >
                <Users size={14} />
                <span>协作管理</span>
              </button>
              {canDelete && (
                <button
                  type="button"
                  role="menuitem"
                  className="danger"
                  aria-label={`删除群聊 ${chat.name}`}
                  disabled={deletingTopicId === chat.id}
                  onClick={() => {
                    setOpenChatMenuKey('');
                    handleDeleteGroup({ groupId: chat.groupId, topicId: chat.id, name: chat.name });
                  }}
                >
                  <Trash2 size={14} />
                  <span>删除群聊</span>
                </button>
              )}
            </div>
          )}
        </SidebarItemRow>
      );
    }

    if (!chat.isGroup && !chat.isBot) {
      const isOnline = onlineStatusFor(onlineUsers, chat.friendId, chat.isOnline);
      return (
        <SidebarItemRow
          key={chat.id}
          className="cc-conversation-item v3-friend-chat-item"
          active={activeTopic === chat.id}
          data-conversation-kind="direct"
          onClick={() => selectConversation(chat)}
        >
          <UserRound size={14} className="prefix cc-chat-row-icon" aria-label="单聊" />
          {renderTaskCopy(chat, chat.preview, '单聊')}
          <SidebarRowTrailing
            actions={(
              <button
                type="button"
                className="v3-chat-item-action v3-friend-menu-trigger"
                title="好友操作"
                aria-label={`${chat.name} 更多操作`}
                aria-haspopup="menu"
                aria-expanded={openFriendMenuId === String(chat.friendId)}
                disabled={friendActionId === String(chat.friendId)}
                onClick={(event) => {
                  event.stopPropagation();
                  setOpenChatMenuKey('');
                  setOpenFriendMenuId((current) => current === String(chat.friendId) ? '' : String(chat.friendId));
                }}
              >
                <MoreHorizontal size={15} />
              </button>
            )}
          >
            {displayTime && <span className="cc-chat-row-time">{displayTime}</span>}
          </SidebarRowTrailing>
          {openFriendMenuId === String(chat.friendId) && (
            <div className="v3-friend-action-menu" role="menu" onClick={(event) => event.stopPropagation()}>
              <button type="button" role="menuitem" onClick={() => handleFriendAction(chat, 'remove')}>
                <UserX size={14} />
                <span>删除好友</span>
              </button>
              <button type="button" role="menuitem" className="danger" onClick={() => handleFriendAction(chat, 'block')}>
                <Ban size={14} />
                <span>拉黑好友</span>
              </button>
            </div>
          )}
          <span className={`cc-conversation-presence ${isOnline ? 'online' : 'offline'}`} aria-label={isOnline ? '在线' : '离线'} />
        </SidebarItemRow>
      );
    }

    const menuKey = `history:${chat.id}`;
    return (
      <SidebarItemRow
        key={chat.id}
        className="cc-history-item cc-conversation-item"
        active={activeTopic === chat.id}
        data-conversation-kind="agent"
        onClick={() => selectConversation(chat)}
      >
        {renderTaskAgentIcon(chat, agentById, onlineUsers)}
        {renderTaskCopy(chat, null, '任务')}
        {renderTaskControls(chat, menuKey, { showPin: true, showTime: true })}
      </SidebarItemRow>
    );
  };

  const renderContactRow = ({ kind, item }) => {
    if (kind === 'group') {
      const chat = item;
      const displayTime = displayTimeForChat(chat);
      const canDelete = groupOwnerById.get(String(chat.groupId)) === String(user.uid);
      const isPinned = pinnedGroupIds.has(String(chat.id));
      const menuKey = `group:${chat.id}`;
      return (
        <SidebarItemRow
          key={`group:${chat.id}`}
          className="cc-contact-item cc-group-contact-item"
          active={activeTopic === chat.id}
          data-contact-kind="group"
          onClick={() => selectConversation(chat)}
        >
          <Users size={14} className="prefix cc-chat-row-icon" aria-label="群组" />
          {renderTaskCopy(chat, chat.preview)}
          <SidebarRowTrailing
            actions={(
              <button
                type="button"
                className="v3-chat-item-action v3-group-menu-trigger"
                title="群组操作"
                aria-label={`${chat.name} 更多操作`}
                aria-haspopup="menu"
                aria-expanded={openChatMenuKey === menuKey}
                onClick={(event) => {
                  event.stopPropagation();
                  setOpenFriendMenuId('');
                  setOpenChatMenuKey((current) => current === menuKey ? '' : menuKey);
                }}
              >
                <MoreHorizontal size={15} />
              </button>
            )}
          >
            {displayTime && <span className="cc-chat-row-time">{displayTime}</span>}
          </SidebarRowTrailing>
          {openChatMenuKey === menuKey && (
            <div className="v3-friend-action-menu cc-chat-action-menu" role="menu" onClick={(event) => event.stopPropagation()}>
              <button
                type="button"
                role="menuitem"
                aria-label={`${isPinned ? '取消置顶' : '置顶'} ${chat.name}`}
                onClick={() => {
                  togglePinnedGroup(chat.id);
                  setOpenChatMenuKey('');
                }}
              >
                <Pin size={14} fill={isPinned ? 'currentColor' : 'none'} />
                <span>{isPinned ? '取消置顶群组' : '置顶群组'}</span>
              </button>
              <button type="button" role="menuitem" aria-label={`${chat.name} 移动端使用`} onClick={() => handleOpenMobileLink(chat, true)}>
                <Smartphone size={14} />
                <span>移动端使用</span>
              </button>
              <button
                type="button"
                role="menuitem"
                aria-label={`${chat.name} 协作管理`}
                disabled={!onManageGroup}
                title={!onManageGroup ? '协作管理入口暂未接入' : '协作管理'}
                onClick={() => {
                  setOpenChatMenuKey('');
                  onManageGroup?.(topicPayloadForChat(chat, true));
                }}
              >
                <Users size={14} />
                <span>协作管理</span>
              </button>
              {canDelete && (
                <button
                  type="button"
                  role="menuitem"
                  className="danger"
                  aria-label={`删除群聊 ${chat.name}`}
                  disabled={deletingTopicId === chat.id}
                  onClick={() => {
                    setOpenChatMenuKey('');
                    handleDeleteGroup({ groupId: chat.groupId, topicId: chat.id, name: chat.name });
                  }}
                >
                  <Trash2 size={14} />
                  <span>删除群聊</span>
                </button>
              )}
            </div>
          )}
        </SidebarItemRow>
      );
    }

    if (kind === 'friend') {
      const chat = item;
      const displayTime = displayTimeForChat(chat);
      const isOnline = onlineStatusFor(onlineUsers, chat.friendId, chat.isOnline);
      const hasUnreadMessage = unreadFriendTopicIds.has(chat.id);
      return (
        <SidebarItemRow
          key={`friend:${chat.friendId}`}
          className="cc-contact-item v3-friend-chat-item"
          active={activeTopic === chat.id}
          data-contact-kind="friend"
          data-unread={hasUnreadMessage ? 'true' : undefined}
          onClick={() => selectConversation(chat)}
        >
          <UserRound
            size={16}
            className={`prefix cc-chat-row-icon cc-friend-contact-icon ${isOnline ? 'online' : 'offline'}`}
            title={isOnline ? '在线' : '离线'}
            aria-label={`好友，${isOnline ? '在线' : '离线'}`}
          />
          <div className="cc-chat-row-copy">
            <span className="cc-chat-row-title">
              <span className="v3-chat-item-label">{chat.name}</span>
            </span>
          </div>
          <SidebarRowTrailing
            actions={(
              <button
                type="button"
                className="v3-chat-item-action v3-friend-menu-trigger"
                title="好友操作"
                aria-label={`${chat.name} 联系人操作`}
                aria-haspopup="menu"
                aria-expanded={openFriendMenuId === String(chat.friendId)}
                disabled={friendActionId === String(chat.friendId)}
                onClick={(event) => {
                  event.stopPropagation();
                  setOpenChatMenuKey('');
                  setOpenFriendMenuId((current) => current === String(chat.friendId) ? '' : String(chat.friendId));
                }}
              >
                <MoreHorizontal size={15} />
              </button>
            )}
          >
            {hasUnreadMessage
              ? <span className="cc-friend-unread-dot" role="status" aria-label={`${chat.name} 有新消息`} />
              : displayTime && <span className="cc-chat-row-time">{displayTime}</span>}
          </SidebarRowTrailing>
          {openFriendMenuId === String(chat.friendId) && (
            <div className="v3-friend-action-menu" role="menu" onClick={(event) => event.stopPropagation()}>
              <button type="button" role="menuitem" onClick={() => handleFriendAction(chat, 'remove')}>
                <UserX size={14} />
                <span>删除好友</span>
              </button>
              <button type="button" role="menuitem" className="danger" onClick={() => handleFriendAction(chat, 'block')}>
                <Ban size={14} />
                <span>拉黑好友</span>
              </button>
            </div>
          )}
        </SidebarItemRow>
      );
    }

    const agent = item;
    const agentId = agent.uid || agent.id;
    const isOnline = onlineStatusFor(onlineUsers, agentId, agent.is_online);
    const owned = isOwnedAgent(agent);
    return (
      <SidebarItemRow
        key={`agent:${agentId}`}
        className="cc-contact-item cc-agent-roster-item"
        data-contact-kind="agent"
        title={agentIdentity(agent)}
        onClick={() => handleSelectAgent(agent)}
      >
        <Bot
          size={16}
          className={`prefix cc-chat-row-icon cc-agent-contact-icon ${isOnline ? 'online' : 'offline'}`}
          title={isOnline ? '在线' : '离线'}
          aria-label={`Agent 助手，${isOnline ? '在线' : '离线'}`}
        />
        <div className="cc-chat-row-copy">
          <span className="cc-chat-row-title">
            <span className="v3-chat-item-label">{agent.display_name || agent.username}</span>
          </span>
        </div>
        <SidebarRowTrailing
          className="cc-agent-row-trailing"
          actionsClassName="v3-agent-row-actions"
          actions={(
            <>
              {!owned && (
                <button
                  type="button"
                  className="v3-chat-item-action danger"
                  title="移除助手"
                  aria-label={`移除 ${agent.display_name || agent.username}`}
                  disabled={agentActionId === String(agentId)}
                  onClick={(event) => {
                    event.stopPropagation();
                    handleRemoveAgent(agent);
                  }}
                >
                  <Trash2 size={14} />
                </button>
              )}
              <button
                type="button"
                className="v3-chat-item-action cc-agent-mobile-action"
                title="移动端使用"
                aria-label={`${agent.display_name || agent.username} 移动端使用`}
                onClick={(event) => {
                  event.stopPropagation();
                  setMobileLinkAgent(agent);
                }}
              >
                <Smartphone size={14} />
              </button>
            </>
          )}
        />
      </SidebarItemRow>
    );
  };

  const renderAgentPendingRequests = () => {
    if (isSearching || agentPendingRequests.length === 0) return null;
    return (
      <div className="v3-agent-request-panel">
        <div className="v3-agent-request-panel-title">新的助手好友申请</div>
        {agentPendingRequests.map((request) => {
          const key = `${request.agent_uid}:${request.from_user_id}`;
          const isReviewing = agentReviewingKey === key;
          return (
            <div key={`${key}:${request.created_at || ''}`} className="v3-agent-request-row">
              <div className="v3-agent-request-main">
                <span className="v3-agent-request-name">{request.display_name || request.from_username || `用户 ${request.from_user_id}`}</span>
                <span className="v3-agent-request-target">申请添加 {request.agent_name}</span>
              </div>
              <button
                type="button"
                className="v3-agent-request-action"
                title="拒绝"
                aria-label="拒绝助手好友申请"
                disabled={isReviewing}
                onClick={() => handleReviewAgentRequest(request, 'reject')}
              >
                <X size={13} />
              </button>
              <button
                type="button"
                className="v3-agent-request-action primary"
                title="通过"
                aria-label="通过助手好友申请"
                disabled={isReviewing}
                onClick={() => handleReviewAgentRequest(request, 'accept')}
              >
                <Check size={13} />
              </button>
            </div>
          );
        })}
      </div>
    );
  };

  return (
    <>
      {compact && (
        <nav className="cc-sidebar-compact-rail" aria-label="任务快捷栏">
          <button
            type="button"
            className="cc-compact-new-chat"
            onClick={() => openNewTaskDialog()}
            aria-label="新建任务"
            title="新建任务"
          >
            <Plus size={20} />
          </button>
          <div className="cc-compact-conversations" aria-label="最近任务">
            {compactChats.map((chat) => (
              <button
                type="button"
                key={chat.id}
                className={`cc-compact-conversation${activeTopic === chat.id ? ' active' : ''}`}
                onClick={() => {
                  setCompactTaskHint(null);
                  selectConversation(chat);
                }}
                onPointerEnter={(event) => {
                  const rect = event.currentTarget.getBoundingClientRect();
                  setCompactTaskHint({
                    id: chat.id,
                    name: chat.name,
                    left: rect.right + 8,
                    top: rect.top + (rect.height / 2),
                  });
                }}
                onPointerLeave={() => setCompactTaskHint(null)}
                onFocus={(event) => {
                  const rect = event.currentTarget.getBoundingClientRect();
                  setCompactTaskHint({
                    id: chat.id,
                    name: chat.name,
                    left: rect.right + 8,
                    top: rect.top + (rect.height / 2),
                  });
                }}
                onBlur={() => setCompactTaskHint(null)}
                aria-label={`打开任务：${chat.name}`}
              >
                <Avatar name={chat.name} src={chat.avatar_url} size={32} />
              </button>
            ))}
          </div>
          {compactTaskHint && createPortal(
            <span
              className="cc-compact-task-hint"
              style={{ left: compactTaskHint.left, top: compactTaskHint.top }}
              aria-hidden="true"
            >
              {compactTaskHint.name}
            </span>,
            document.body,
          )}
        </nav>
      )}

      {!compact && <div className="cc-sidebar-tools">
        <button type="button" className="cc-sidebar-primary" onClick={() => openNewTaskDialog()}>
          <Plus size={17} />
          <span>新建任务</span>
        </button>
        <label className="cc-sidebar-search">
          <Search size={15} />
        <input
          placeholder="搜索"
          aria-label="搜索任务、联系人或助手"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        </label>
      </div>}

      {!compact && <div className="v3-chat-list">

        {/* 任务：只承载单人 + Agent 与多人 + Agent 两种工作会话 */}
        <SidebarSectionHeader
          className="cc-conversation-section"
          label="任务"
          expanded={!collapsed.conversations}
          onToggle={() => toggleCollapsed('conversations')}
          action={(
            <button type="button" className="cc-section-add" onClick={() => openNewTaskDialog()} title="新建任务" aria-label="新建任务">
              <Plus size={15} />
            </button>
          )}
        />
        {(isSearching || !collapsed.conversations) && (conversationChats.length === 0 && !isSearching ? (
          <div className="cc-sidebar-empty cc-conversation-empty">选择一个 Agent 开始新任务</div>
        ) : (
          conversationChats.map(renderConversationRow)
        ))}

        {/* 联系人：好友与 Agent 共用入口，通过行内类型软区分 */}
        <SidebarSectionHeader
          className="cc-contacts-section"
          label="联系人"
          expanded={!collapsed.contacts}
          onToggle={() => toggleCollapsed('contacts')}
          toggleContent={hasUnreadFriendMessages && (
            <span className="cc-section-unread-dot" role="status" aria-label="联系人有新消息" />
          )}
          status={(pending.length + agentPendingRequests.length) > 0 && (
            <span
              className="v3-agent-request-badge cc-section-request-badge"
              role="status"
              aria-label={`${pending.length + agentPendingRequests.length} 个待处理好友申请`}
            >
              {pending.length + agentPendingRequests.length}
            </span>
          )}
          action={(
            <button
              type="button"
              className="cc-section-add cc-contact-section-menu-trigger"
              title="联系人操作"
              aria-label="联系人更多操作"
              aria-haspopup="menu"
              aria-expanded={showContactActions}
              aria-controls="cc-contact-section-menu"
              onClick={() => {
                setOpenFriendMenuId('');
                setOpenChatMenuKey('');
                setOpenProjectMenuId(null);
                setShowContactActions((current) => !current);
              }}
            >
              <MoreHorizontal size={15} />
            </button>
          )}
        >
          {showContactActions && (
            <div
              id="cc-contact-section-menu"
              className="v3-friend-action-menu cc-project-action-menu cc-contact-section-menu"
              role="menu"
              aria-label="联系人操作"
            >
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  setShowContactActions(false);
                  setShowAddFriend(true);
                }}
              >
                <UserPlus size={14} />
                <span>添加好友</span>
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  setShowContactActions(false);
                  setShowCreateGroup(true);
                }}
              >
                <Users size={14} />
                <span>创建群组</span>
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => {
                  setShowContactActions(false);
                  setShowAgentStore(true);
                }}
              >
                <Bot size={14} />
                <span>管理 Agent 助手</span>
              </button>
            </div>
          )}
        </SidebarSectionHeader>
        {(isSearching || !collapsed.contacts) && (
          <>
            {!isSearching && pending.length > 0 && (
              <div className="cc-contact-requests">
                <div className="cc-contact-requests-title">
                  好友请求 ({pending.length})
                </div>
                {pending.map((req) => (
                  <FriendRequest key={req.id} request={req} onAccept={() => handleAccept(req.from_user_id)} onReject={() => handleReject(req.from_user_id)} />
                ))}
              </div>
            )}
            {renderAgentPendingRequests()}
            {contactItems.length === 0 && !isSearching ? (
              <div className="cc-sidebar-empty cc-contacts-empty">暂无联系人</div>
            ) : (
              contactItems.map(renderContactRow)
            )}
          </>
        )}
        <SidebarSectionHeader
          className="cc-project-section"
          label="项目"
          expanded={!collapsed.projects}
          onToggle={() => toggleCollapsed('projects')}
          action={(
            <button
              type="button"
              className="cc-section-add"
              title="新建项目"
              aria-label="新建项目"
              onClick={() => {
                setProjectPickerTask(null);
                setShowCreateProject(true);
                setNewProjectName('');
              }}
            >
              <Plus size={15} />
            </button>
          )}
        />
        {(isSearching || !collapsed.projects) && (filteredProjects.length === 0 && !isSearching ? (
          <div className="cc-sidebar-empty cc-project-empty">暂无项目</div>
        ) : (
          filteredProjects.map((project) => {
            const projectId = Number(project.id);
            const projectNameMatches = String(project.name || '').toLowerCase().includes(lowerSearch);
            const projectTasks = (projectTasksById.get(projectId) || []).filter((chat) => (
              !isSearching || projectNameMatches || chat.name.toLowerCase().includes(lowerSearch)
            ));
            const expanded = isSearching ? projectTasks.length > 0 : expandedProjectId === projectId;
            return (
              <React.Fragment key={project.id}>
                <SidebarItemRow className="cc-project-row">
                  <button
                    type="button"
                    className="cc-project-item cc-sidebar-row-main"
                    aria-label={`${expanded ? '收起项目' : '打开项目'} ${project.name}`}
                    aria-expanded={expanded}
                    onClick={() => setExpandedProjectId(expanded ? null : projectId)}
                  >
                    {expanded
                      ? <FolderOpen size={14} className="prefix cc-chat-row-icon" />
                      : <Folder size={14} className="prefix cc-chat-row-icon" />}
                    <div className="cc-chat-row-copy">
                      <span className="v3-chat-item-label">{project.name}</span>
                    </div>
                  </button>
                  <SidebarRowTrailing
                    actions={(
                      <button
                        type="button"
                        className="v3-chat-item-action cc-project-menu-trigger"
                        title="项目操作"
                        aria-label={`${project.name} 项目操作`}
                        aria-haspopup="menu"
                        aria-expanded={openProjectMenuId === projectId}
                        disabled={projectActionId === projectId}
                        onClick={(event) => {
                          event.stopPropagation();
                          setOpenFriendMenuId('');
                          setOpenChatMenuKey('');
                          setOpenProjectMenuId((current) => current === projectId ? null : projectId);
                        }}
                      >
                        <MoreHorizontal size={15} />
                      </button>
                    )}
                  >
                    <span className="cc-project-count" aria-label={`${project.task_count || 0} 个任务`}>{project.task_count || 0}</span>
                  </SidebarRowTrailing>
                  {openProjectMenuId === projectId && (
                    <div className="v3-friend-action-menu cc-project-action-menu" role="menu" onClick={(event) => event.stopPropagation()}>
                      <button type="button" role="menuitem" onClick={() => handleCreateTaskInProject(project)}>
                        <Plus size={14} />
                        <span>新建任务</span>
                      </button>
                      <button type="button" role="menuitem" onClick={() => handleOpenProjectTaskPicker(project)}>
                        <FolderPlus size={14} />
                        <span>添加已有任务</span>
                      </button>
                      <button type="button" role="menuitem" onClick={() => openProjectRename(project)}>
                        <Pencil size={14} />
                        <span>更改项目名称</span>
                      </button>
                      <button type="button" role="menuitem" className="danger" onClick={() => handleDeleteProject(project)}>
                        <Trash2 size={14} />
                        <span>删除项目</span>
                      </button>
                    </div>
                  )}
                </SidebarItemRow>
                {expanded && (projectTasks.length > 0 ? projectTasks.map((chat) => {
                  const menuKey = `project:${chat.id}`;
                  const taskKind = conversationKind(chat);
                  const taskLabel = taskKind === 'multi_agent' ? '协作' : '';
                  return (
                    <SidebarItemRow
                      key={chat.id}
                      className="cc-history-item cc-conversation-item cc-project-task-item"
                      active={activeTopic === chat.id}
                      level={2}
                      data-task-kind={taskKind === 'multi_agent' ? 'collaboration' : 'solo'}
                      aria-label={`打开项目任务 ${chat.name}`}
                      onClick={() => selectConversation(chat)}
                    >
                      {renderTaskAgentIcon(chat, agentById, onlineUsers)}
                      {renderTaskCopy(chat, null, taskLabel)}
                      {renderTaskControls(chat, menuKey, { showPin: true, showTime: true })}
                    </SidebarItemRow>
                  );
                }) : (
                  <div className="cc-sidebar-empty cc-project-task-empty">暂无任务</div>
                ))}
              </React.Fragment>
            );
          })
        ))}

        {isSearching && !hasSearchResults && (
          <div className="cc-search-empty" style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)', fontSize: '13px' }}>没有匹配结果</div>
        )}

      </div>}

      {showNewChat && createPortal(
        <div className="name-dialog-overlay cc-new-task-overlay" onClick={closeNewTaskDialog}>
          <section className="name-dialog cc-new-task-dialog" role="dialog" aria-modal="true" aria-label={newTaskProject ? `在${newTaskProject.name}中新建任务` : '新建任务'} onClick={(e) => e.stopPropagation()}>
            <header className="cc-new-task-header">
              <h3>{newTaskProject ? `在“${newTaskProject.name}”中新建任务` : '选择 AI 助手开始任务'}</h3>
              <button type="button" className="cc-dialog-close" onClick={closeNewTaskDialog} aria-label="关闭">
                <X size={18} />
              </button>
            </header>
            <div className="cc-new-task-body">
            {agents.length === 0 ? (
              <div className="cc-new-task-empty">
                <strong>暂无 AI 助手</strong>
                <span>请先在“联系人”中添加或创建 AI 助手</span>
              </div>
            ) : (
              <div className="cc-new-task-agent-list">
                {agents.map((agent) => (
                  <button type="button" className="cc-new-task-agent" key={agent.uid || agent.id} onClick={() => handleNewChatWithAgent(agent)}>
                    <Bot size={18} />
                    <span>{agent.display_name || agent.username}</span>
                  </button>
                ))}
              </div>
            )}
            </div>
          </section>
        </div>,
        document.body,
      )}

      {taskPickerProject && createPortal(
        <div className="name-dialog-overlay cc-new-task-overlay" onClick={closeProjectTaskPicker}>
          <section className="name-dialog cc-new-task-dialog cc-project-picker-dialog" role="dialog" aria-modal="true" aria-label="添加已有任务" onClick={(event) => event.stopPropagation()}>
            <header className="cc-new-task-header">
              <h3>添加已有任务</h3>
              <button type="button" className="cc-dialog-close" onClick={closeProjectTaskPicker} aria-label="关闭添加已有任务">
                <X size={18} />
              </button>
            </header>
            <div className="cc-new-task-body">
              <div className="cc-project-picker-target">选择要加入“{taskPickerProject.name}”的任务</div>
              {availableProjectTasks.length === 0 ? (
                <div className="cc-new-task-empty cc-project-picker-empty">
                  <Zap size={20} aria-hidden="true" />
                  <strong>暂无可添加任务</strong>
                  <span>已归入项目的任务不会出现在这里</span>
                </div>
              ) : (
                <div className="cc-new-task-agent-list cc-project-picker-list">
                  {availableProjectTasks.map((chat) => {
                    const selected = selectedProjectTaskIds.has(String(chat.id));
                    return (
                      <button
                        type="button"
                        className={`cc-new-task-agent cc-project-task-option ${selected ? 'is-selected' : ''}`}
                        key={chat.id}
                        aria-pressed={selected}
                        disabled={Boolean(projectActionTopicId)}
                        onClick={() => toggleProjectTaskSelection(chat.id)}
                      >
                        <Zap size={17} />
                        <span className="cc-project-task-option-name">{chat.name}</span>
                        <span className="cc-project-task-selection-indicator" aria-hidden="true">
                          {selected && <Check size={14} />}
                        </span>
                      </button>
                    );
                  })}
                </div>
              )}
              <div className="cc-new-task-actions cc-project-picker-actions">
                <button type="button" className="oc-btn oc-btn-default" disabled={Boolean(projectActionTopicId)} onClick={closeProjectTaskPicker}>取消</button>
                <button
                  type="button"
                  className="oc-btn oc-btn-primary"
                  disabled={selectedProjectTaskIds.size === 0 || Boolean(projectActionTopicId)}
                  onClick={handleAddTasksToProject}
                >
                  {projectActionTopicId ? '添加中…' : `添加${selectedProjectTaskIds.size ? `（${selectedProjectTaskIds.size}）` : ''}`}
                </button>
              </div>
            </div>
          </section>
        </div>,
        document.body,
      )}

      {projectPickerTask && !showCreateProject && createPortal(
        <div className="name-dialog-overlay cc-new-task-overlay" onClick={closeProjectDialog}>
          <section className="name-dialog cc-new-task-dialog cc-project-picker-dialog" role="dialog" aria-modal="true" aria-label="选择项目" aria-labelledby="cc-project-picker-title" onClick={(e) => e.stopPropagation()}>
            <header className="cc-new-task-header">
              <h3 id="cc-project-picker-title">加入项目</h3>
              <button type="button" className="cc-dialog-close" onClick={closeProjectDialog} aria-label="关闭加入项目">
                <X size={18} />
              </button>
            </header>
            <div className="cc-new-task-body">
              <div className="cc-project-picker-target">将“{projectPickerTask.name}”加入项目</div>
              {projects.length === 0 ? (
                <div className="cc-new-task-empty cc-project-picker-empty">
                  <FolderPlus size={20} aria-hidden="true" />
                  <strong>暂无可用项目</strong>
                  <span>先新建一个项目，再加入当前任务</span>
                </div>
              ) : (
                <div className="cc-new-task-agent-list cc-project-picker-list">
                  {projects.map((project) => {
                    const selected = projectIdFor(projectPickerTask) === Number(project.id);
                    return (
                      <button
                        type="button"
                        className="cc-new-task-agent"
                        key={project.id}
                        disabled={selected || projectActionTopicId === projectPickerTask.id}
                        onClick={() => handleAssignProject(project)}
                      >
                        {selected ? <Check size={17} /> : <Folder size={17} />}
                        <span>{project.name}</span>
                      </button>
                    );
                  })}
                  {projectIdFor(projectPickerTask) > 0 && (
                    <button
                      type="button"
                      className="cc-new-task-agent cc-project-remove-option"
                      disabled={projectActionTopicId === projectPickerTask.id}
                      onClick={() => handleRemoveFromProject(projectPickerTask)}
                    >
                      <X size={17} />
                      <span>移出当前项目</span>
                    </button>
                  )}
                </div>
              )}
              <div className="cc-new-task-actions cc-project-picker-actions">
                <button type="button" className="oc-btn oc-btn-default" onClick={closeProjectDialog}>取消</button>
                <button type="button" className="oc-btn oc-btn-primary" onClick={() => { setShowCreateProject(true); setNewProjectName(''); }}>新建项目</button>
              </div>
            </div>
          </section>
        </div>,
        document.body,
      )}

      {showCreateProject && createPortal(
        <div className="name-dialog-overlay cc-new-task-overlay" onClick={closeProjectDialog}>
          <section className="name-dialog cc-new-task-dialog" role="dialog" aria-modal="true" aria-label="新建项目" onClick={(e) => e.stopPropagation()}>
            <header className="cc-new-task-header">
              <h3>新建项目</h3>
              <button type="button" className="cc-dialog-close" onClick={closeProjectDialog} aria-label="关闭">
                <X size={18} />
              </button>
            </header>
            <div className="cc-new-task-body">
              <input
                autoFocus
                className="oc-auth-input cc-new-task-name"
                value={newProjectName}
                maxLength={128}
                onChange={(e) => setNewProjectName(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') handleCreateProject(); }}
                placeholder="项目名称"
                aria-label="项目名称"
              />
              <div className="cc-new-task-actions">
                <button
                  type="button"
                  className="oc-btn oc-btn-default"
                  onClick={() => {
                    if (projectPickerTask) {
                      setShowCreateProject(false);
                      setNewProjectName('');
                    } else {
                      closeProjectDialog();
                    }
                  }}
                >
                  {projectPickerTask ? '返回' : '取消'}
                </button>
                <button
                  type="button"
                  className="oc-btn oc-btn-primary"
                  disabled={!newProjectName.trim() || Boolean(projectActionTopicId)}
                  onClick={handleCreateProject}
                >
                  创建
                </button>
              </div>
            </div>
          </section>
        </div>,
        document.body,
      )}

      {editingProject && createPortal(
        <div className="name-dialog-overlay cc-new-task-overlay" onClick={closeProjectRename}>
          <section className="name-dialog cc-new-task-dialog" role="dialog" aria-modal="true" aria-label="更改项目名称" onClick={(event) => event.stopPropagation()}>
            <header className="cc-new-task-header">
              <h3>更改项目名称</h3>
              <button type="button" className="cc-dialog-close" onClick={closeProjectRename} aria-label="关闭">
                <X size={18} />
              </button>
            </header>
            <div className="cc-new-task-body">
              <input
                autoFocus
                className="oc-auth-input cc-new-task-name"
                value={projectNameDraft}
                maxLength={128}
                onChange={(event) => setProjectNameDraft(event.target.value)}
                onKeyDown={(event) => { if (event.key === 'Enter') handleRenameProject(); }}
                placeholder="项目名称"
                aria-label="新的项目名称"
              />
              <div className="cc-new-task-actions">
                <button type="button" className="oc-btn oc-btn-default" disabled={Boolean(projectActionId)} onClick={closeProjectRename}>取消</button>
                <button
                  type="button"
                  className="oc-btn oc-btn-primary"
                  disabled={!projectNameDraft.trim() || projectNameDraft.trim() === String(editingProject.name || '').trim() || Boolean(projectActionId)}
                  onClick={handleRenameProject}
                >
                  保存
                </button>
              </div>
            </div>
          </section>
        </div>,
        document.body,
      )}

      {showCreateGroup && createPortal(
        <CreateGroup onClose={() => setShowCreateGroup(false)} onCreated={handleGroupCreated} />,
        document.body,
      )}
      {collaborationUpgradeTask && createPortal(
        <CreateGroup
          key={collaborationUpgradeTask.id}
          mode="task_upgrade"
          initialName={collaborationUpgradeTask.name}
          lockedMemberIds={[collaborationUpgradeTask.friendId]}
          onCreate={createCollaborationUpgrade}
          onCreated={handleCollaborationUpgradeCreated}
          onClose={() => setCollaborationUpgradeTask(null)}
        />,
        document.body,
      )}
      {showAddFriend && createPortal(
        <AddFriend currentUser={user} onClose={() => setShowAddFriend(false)} onSent={() => loadAll()} />,
        document.body,
      )}
      {showAgentStore && createPortal(
        <AgentStoreModal onClose={() => setShowAgentStore(false)} user={user} onBotsChanged={() => loadAll()} />,
        document.body,
      )}
      {mobileLinkAgent && createPortal(
        <MobileChannelBindModal
          agentUid={mobileLinkAgent.uid || mobileLinkAgent.id}
          agentName={mobileLinkAgent.display_name || mobileLinkAgent.username}
          onClose={() => setMobileLinkAgent(null)}
        />,
        document.body,
      )}
      {mobileLinkGroup && createPortal(
        <MobileChannelBindModal
          groupId={mobileLinkGroup.groupId}
          topicId={mobileLinkGroup.topicId}
          groupName={mobileLinkGroup.name}
          onClose={() => setMobileLinkGroup(null)}
        />,
        document.body,
      )}
    </>
  );
}

function onlineStatusFor(onlineUsers, uid, fallback = false) {
  if (!uid) return Boolean(fallback);
  if (onlineUsers && Object.prototype.hasOwnProperty.call(onlineUsers, uid)) {
    return Boolean(onlineUsers[uid]);
  }
  return Boolean(fallback);
}

function normalizedEntityIds(values) {
  const source = Array.isArray(values) ? values : [values];
  return [...new Set(source
    .flatMap((value) => Array.isArray(value) ? value : [value])
    .map((value) => String(value ?? '').trim())
    .filter((value) => value && value !== '0'))];
}

function taskAgentIdsFromPayload(item) {
  return normalizedEntityIds([
    item?.agentIds,
    item?.agent_ids,
    item?.agentId,
    item?.agent_id,
  ]);
}

function agentIdsForTask(chat, agentById) {
  const explicitAgentIds = taskAgentIdsFromPayload(chat);
  if (explicitAgentIds.length > 0) return explicitAgentIds;

  if (!chat?.isGroup && chat?.friendId) {
    return [String(chat.friendId)];
  }

  return normalizedEntityIds([chat?.memberIds, chat?.member_ids])
    .filter((agentId) => agentById.has(agentId));
}

function taskAgentOnlineSummary(chat, agentById, onlineUsers) {
  const agentIds = agentIdsForTask(chat, agentById);
  const onlineCount = agentIds.reduce((count, agentId) => {
    const agent = agentById.get(agentId);
    const isDirectTaskAgent = !chat?.isGroup && String(chat?.friendId || '') === agentId;
    const fallback = isDirectTaskAgent && chat?.isOnline !== undefined
      ? chat.isOnline
      : agent?.is_online;
    return count + Number(onlineStatusFor(onlineUsers, agentId, fallback));
  }, 0);
  return { agentCount: agentIds.length, onlineCount, isOnline: onlineCount > 0 };
}

function renderTaskAgentIcon(chat, agentById, onlineUsers) {
  const status = taskAgentOnlineSummary(chat, agentById, onlineUsers);
  const stateLabel = status.isOnline ? '在线' : '离线';
  const detail = status.agentCount === 0
    ? '未关联 Agent'
    : status.agentCount > 1
      ? `${status.onlineCount}/${status.agentCount} 个 Agent 在线`
      : `Agent ${stateLabel}`;
  return (
    <Zap
      size={14}
      className={`prefix cc-chat-row-icon cc-task-agent-icon ${status.isOnline ? 'online' : 'offline'}`}
      title={detail}
      aria-label={`Agent 任务，${detail}`}
    />
  );
}

function isOwnedAgent(agent) {
  return agent?.is_owner === true || agent?.relation === 'owner';
}

function isBotContact(contact) {
  return contact?.bot === true
    || contact?.is_bot === true
    || contact?.account_type === 'bot'
    || contact?.accountType === 'bot';
}

function numericUid(value) {
  const match = String(value ?? '').match(/(\d+)$/);
  return match ? Number(match[1]) : 0;
}

function addSetValue(previous, value) {
  const key = String(value || '').trim();
  if (!key || previous.has(key)) return previous;
  const next = new Set(previous);
  next.add(key);
  return next;
}

function removeSetValue(previous, value) {
  const key = String(value || '').trim();
  if (!key || !previous.has(key)) return previous;
  const next = new Set(previous);
  next.delete(key);
  return next;
}

function TaskRowStatusIndicator({ status, time, showTime }) {
  const expiresAtMs = taskStatusExpiresMs(status);
  const [nowMs, setNowMs] = useState(() => Date.now());

  useEffect(() => {
    if (!expiresAtMs || expiresAtMs <= Date.now()) return undefined;
    const timer = window.setTimeout(
      () => setNowMs(Date.now()),
      Math.min(expiresAtMs - Date.now() + 50, 2147483647),
    );
    return () => window.clearTimeout(timer);
  }, [expiresAtMs]);

  const normalized = normalizeTaskStatus(status);
  if (!normalized || normalized.state === 'idle' || (expiresAtMs && expiresAtMs <= nowMs)) {
    return showTime && time ? <span className="cc-chat-row-time">{time}</span> : null;
  }

  const detail = normalized.summary || normalized.error;
  if (normalized.state === 'running') {
    return (
      <span className="cc-task-row-status running" title={detail || '任务进行中'} aria-label="任务进行中" role="status">
        <LoaderCircle size={15} strokeWidth={2.2} />
      </span>
    );
  }

  if (normalized.state === 'completed') {
    return (
      <span className="cc-task-row-status completed" title={detail || '任务已完成'} aria-label="任务已完成" role="status">
        <span className="cc-task-status-dot cc-task-completed-dot" />
      </span>
    );
  }

  if (normalized.state === 'failed') {
    return (
      <span className="cc-task-row-status failed" title={detail || '任务执行失败'} aria-label="任务执行失败" role="status">
        <span className="cc-task-status-dot" />
      </span>
    );
  }

  if (normalized.state === 'cancelled' || normalized.state === 'stale') {
    const isStale = normalized.state === 'stale';
    const label = isStale ? '任务已自动中止' : '任务已中止';
    return (
      <span className={`cc-task-row-status ${normalized.state}`} title={detail || label} aria-label={label} role="status">
        <span className="cc-task-status-dot" />
      </span>
    );
  }

  return showTime && time ? <span className="cc-chat-row-time">{time}</span> : null;
}

function normalizeTaskStatus(status) {
  if (!status || typeof status !== 'object') return null;
  const state = String(status.state || '').trim().toLowerCase();
  if (!state) return null;
  return {
    ...status,
    state,
    summary: String(status.summary || '').trim(),
    error: String(status.error || '').trim(),
  };
}

function isDismissibleTaskStatus(status) {
  return ['completed', 'failed', 'cancelled', 'stale'].includes(normalizeTaskStatus(status)?.state);
}

function taskStatusDismissKey(status) {
  const normalized = normalizeTaskStatus(status);
  if (!normalized) return '';
  return [
    normalized.state,
    normalized.run_id || normalized.runId || '',
    normalized.updated_at || normalized.updatedAt || '',
    normalized.summary || '',
    normalized.error || '',
  ].map((value) => String(value)).join('|');
}

function visibleTaskStatus(status, dismissedTaskStatuses, topicId) {
  const normalized = normalizeTaskStatus(status);
  if (!normalized || dismissedTaskStatuses?.[topicId] === taskStatusDismissKey(normalized)) return null;
  return normalized;
}

function taskStatusUpdatedMs(status) {
  return toTimeMs(status?.updated_at || status?.updatedAt);
}

function taskStatusExpiresMs(status) {
  return toTimeMs(status?.expires_at || status?.expiresAt);
}

function conversationSummaryToChat(item) {
  const createdAtMs = toTimeMs(item.created_at);
  const lastTimeMs = toTimeMs(item.last_time) || createdAtMs;
  return {
    id: item.id,
    friendId: item.friend_id,
    groupId: item.group_id,
    name: item.name,
    preview: item.preview || '',
    time: lastTimeMs ? formatSidebarTime(lastTimeMs) : '',
    lastTimeMs,
    createdAtMs,
    isGroup: item.is_group,
    avatar_url: item.avatar_url,
    isBot: item.is_bot,
    hasBot: Boolean(item.has_bot || item.is_agent_group),
    isAgentTask: Boolean(item.is_agent_task || item.kind === 'agent_task'),
    memberCount: Number(item.member_count || 0),
    agentIds: taskAgentIdsFromPayload(item),
    memberIds: normalizedEntityIds(item.member_ids || item.memberIds),
    isOnline: item.is_online,
    seq: item.latest_seq || 0,
    taskStatus: normalizeTaskStatus(item.task_status),
    projectId: Number(item.project_id) > 0 ? Number(item.project_id) : 0,
    projectName: item.project_name || '',
  };
}

function mergeGroupsWithConversations(groups, groupConversations) {
  const byTopic = new Map();
  for (const group of groups || []) {
    const normalized = normalizeGroupListItem(group);
    if (normalized) byTopic.set(normalized.id, normalized);
  }
  for (const chat of groupConversations || []) {
    const normalized = normalizeGroupListItem(chat);
    if (!normalized) continue;
    const existing = byTopic.get(normalized.id) || {};
    const normalizedSortTime = conversationSortTime(normalized);
    const existingSortTime = conversationSortTime(existing);
    const preserveExistingTime = !normalizedSortTime && existingSortTime;
    byTopic.set(normalized.id, {
      ...existing,
      ...normalized,
      owner_id: normalized.owner_id ?? existing.owner_id,
      avatar_url: normalized.avatar_url ?? existing.avatar_url,
      hasBot: Boolean(existing.hasBot || normalized.hasBot),
      isAgentTask: Boolean(existing.isAgentTask || normalized.isAgentTask),
      memberCount: normalized.memberCount || existing.memberCount || 0,
      agentIds: normalizedEntityIds([existing.agentIds, normalized.agentIds]),
      memberIds: normalizedEntityIds([existing.memberIds, normalized.memberIds]),
      time: normalized.time || existing.time || '',
      lastTimeMs: preserveExistingTime ? existing.lastTimeMs : normalized.lastTimeMs,
      createdAtMs: normalized.createdAtMs || existing.createdAtMs,
    });
  }
  return sortConversationsByRecent(Array.from(byTopic.values()));
}

function normalizeGroupListItem(item) {
  if (!item) return null;
  const groupId = item.groupId || item.group_id || numericGroupIdFromTopic(item.id) || item.id;
  const name = item.name;
  if (!groupId || !name) return null;
  const id = String(item.id || '').startsWith('grp_') ? item.id : `grp_${groupId}`;
  const createdAtMs = toTimeMs(item.createdAtMs || item.created_at);
  const lastTimeMs = toTimeMs(item.lastTimeMs || item.last_time) || createdAtMs;
  return {
    ...item,
    id,
    groupId,
    owner_id: item.owner_id,
    name,
    isGroup: true,
    isAgentTask: Boolean(item.isAgentTask || item.is_agent_task || item.kind === 'agent_task'),
    hasBot: Boolean(item.hasBot || item.has_bot || item.is_agent_group),
    memberCount: Number(item.memberCount || item.member_count || 0),
    agentIds: taskAgentIdsFromPayload(item),
    memberIds: normalizedEntityIds(item.memberIds || item.member_ids),
    projectId: projectIdFor(item),
    avatar_url: item.avatar_url,
    preview: item.preview || '',
    time: item.time || (lastTimeMs ? formatSidebarTime(lastTimeMs) : ''),
    lastTimeMs,
    createdAtMs,
    seq: item.seq || 0,
    taskStatus: normalizeTaskStatus(item.taskStatus || item.task_status),
  };
}

function numericGroupIdFromTopic(topicId) {
  const match = String(topicId || '').match(/^grp_(\d+)$/);
  return match ? Number(match[1]) : 0;
}

function sortConversationsByRecent(items) {
  return [...items].sort(conversationRecentLess);
}

function isHistoryTask(chat) {
  const kind = conversationKind(chat);
  return kind === 'solo_agent' || kind === 'multi_agent';
}

function conversationKind(chat) {
  if (!chat?.isGroup) return chat?.isBot ? 'solo_agent' : 'friend';
  const memberCount = Number(chat.memberCount || 0);
  const includesAgent = Boolean(chat.isAgentTask || chat.hasBot);

  if (includesAgent) {
    return memberCount > 2 ? 'multi_agent' : 'solo_agent';
  }
  return memberCount === 1 ? 'solo_agent' : 'multi_agent';
}

function projectIdFor(chat) {
  for (const value of [chat?.projectId, chat?.project_id]) {
    const projectId = Number(value);
    if (Number.isFinite(projectId) && projectId > 0) return projectId;
  }
  return 0;
}

function latestAgentTaskTime(tasks, agent) {
  const agentId = String(agent?.uid || agent?.id || '');
  if (!agentId) return 0;
  return (tasks || []).reduce((latest, chat) => {
    if (chat.isGroup || String(chat.friendId || '') !== agentId) return latest;
    return Math.max(latest, conversationSortTime(chat));
  }, 0);
}

function sortConversationsWithPins(items, pinnedTopicIds) {
  return [...items].sort((left, right) => {
    const leftPinned = pinnedTopicIds?.has(String(left.id));
    const rightPinned = pinnedTopicIds?.has(String(right.id));
    if (leftPinned !== rightPinned) return leftPinned ? -1 : 1;
    return conversationRecentLess(left, right);
  });
}

function conversationRecentLess(left, right) {
  const leftTime = conversationSortTime(left);
  const rightTime = conversationSortTime(right);
  if (leftTime !== rightTime) return rightTime - leftTime;

  const leftSeq = Number(left.seq || 0);
  const rightSeq = Number(right.seq || 0);
  if (leftSeq !== rightSeq) return rightSeq - leftSeq;

  if (Boolean(left.isGroup) !== Boolean(right.isGroup)) {
    return left.isGroup ? -1 : 1;
  }
  if (left.groupId && right.groupId && String(left.groupId) !== String(right.groupId)) {
    return Number(right.groupId) - Number(left.groupId);
  }
  if (left.friendId && right.friendId && String(left.friendId) !== String(right.friendId)) {
    return Number(right.friendId) - Number(left.friendId);
  }
  return String(left.name || '').localeCompare(String(right.name || ''));
}

function conversationSortTime(item) {
  return toTimeMs(item?.lastTimeMs || item?.last_time || item?.createdAtMs || item?.created_at);
}

function toTimeMs(value) {
  if (!value) return 0;
  if (typeof value === 'number') {
    return Number.isFinite(value) ? value : 0;
  }
  const parsed = new Date(value).getTime();
  return Number.isFinite(parsed) ? parsed : 0;
}

function p2pTopicId(uid1, uid2) {
  let u1 = parseInt(uid1, 10);
  let u2 = parseInt(uid2, 10);
  if (u1 > u2) [u1, u2] = [u2, u1];
  return `p2p_${u1}_${u2}`;
}

function agentIdentity(agent) {
  const username = agent?.username ? `@${agent.username}` : '';
  const uid = agent?.uid || agent?.id ? `uid ${agent.uid || agent.id}` : '';
  return [username, uid].filter(Boolean).join(' · ');
}

function userSearchText(user) {
  return [
    user?.display_name,
    user?.username,
    user?.id,
    user?.uid,
  ].filter(Boolean).join(' ').toLowerCase();
}

function normalizeCreatedGroup(created) {
  if (!created) return null;
  const rawGroup = created.group || {};
  const id = rawGroup.id || created.group_id;
  const name = rawGroup.name || created.name;
  if (!id || !name) return null;
  return {
    ...rawGroup,
    id,
    name,
    owner_id: rawGroup.owner_id,
    avatar_url: rawGroup.avatar_url || created.avatar_url || '',
    created_at: rawGroup.created_at || created.created_at || new Date().toISOString(),
    has_bot: rawGroup.has_bot || created.has_bot || false,
    member_count: Number(rawGroup.member_count || created.member_count || 0),
    agent_ids: taskAgentIdsFromPayload(rawGroup).length > 0
      ? taskAgentIdsFromPayload(rawGroup)
      : taskAgentIdsFromPayload(created),
    member_ids: normalizedEntityIds(rawGroup.member_ids || created.member_ids),
    kind: rawGroup.kind || created.kind || 'standard',
    is_agent_task: Boolean(rawGroup.is_agent_task || created.is_agent_task),
  };
}

function groupToConversation(group) {
  const createdAtMs = toTimeMs(group.created_at);
  return {
    id: `grp_${group.id}`,
    groupId: group.id,
    name: group.name,
    preview: '',
    time: createdAtMs ? formatSidebarTime(createdAtMs) : '',
    lastTimeMs: createdAtMs,
    createdAtMs,
    isGroup: true,
    avatar_url: group.avatar_url,
    hasBot: Boolean(group.has_bot || group.is_agent_group),
    isAgentTask: Boolean(group.is_agent_task || group.kind === 'agent_task'),
    memberCount: Number(group.member_count || 0),
    agentIds: taskAgentIdsFromPayload(group),
    memberIds: normalizedEntityIds(group.member_ids),
    seq: 0,
  };
}

function friendToConversation(currentUid, friend) {
  return {
    id: p2pTopicId(currentUid, friend.id),
    friendId: friend.id,
    name: friend.display_name || friend.username,
    preview: '',
    time: '',
    isGroup: false,
    avatar_url: friend.avatar_url,
    isBot: friend.bot,
    seq: 0,
  };
}

function summarizeMessage(message) {
  if (!message) return '';
  if (typeof message.content === 'string') {
    try {
      const parsed = JSON.parse(message.content);
      if (parsed?.type === 'file') return parsed?.payload?.name || '[文件]';
      if (parsed?.type === 'image') return '[图片]';
    } catch (err) {
      return message.content;
    }
    return message.content;
  }
  if (message.content?.type === 'file') return message.content?.payload?.name || '[文件]';
  if (message.content?.type === 'image') return '[图片]';
  return message.content?.text || '';
}
