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
import { Users, UserRound, UserPlus, Zap, Bot, Trash2, Smartphone, Check, X, Pin, Pencil, ChevronRight, Plus, Search, MoreHorizontal, UserX, Ban, AlertCircle, LoaderCircle, Folder, FolderOpen, FolderPlus } from 'lucide-react';

const SIDEBAR_COLLAPSED_STORAGE_PREFIX = 'cc_sidebar_collapsed_v1';
const DEFAULT_COLLAPSED_SECTIONS = { conversations: false, contacts: false, projects: false };
const PINNED_GROUPS_STORAGE_PREFIX = 'cc_pinned_groups_v1';
const PINNED_HISTORY_STORAGE_PREFIX = 'cc_pinned_history_v1';
const HIDDEN_HISTORY_STORAGE_PREFIX = 'cc_hidden_history_v1';
const TASK_STATUS_DISMISSED_STORAGE_PREFIX = 'cc_task_status_dismissed_v1';

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
  const justHiddenHistoryRef = useRef('');
  const activeTopicRef = useRef(activeTopic);
  const userUidRef = useRef(user?.uid);
  const chatMenuTriggerRef = useRef(null);
  const chatMenuRef = useRef(null);
  const chatsRef = useRef(chats);
  const friendsRef = useRef(friends);
  const agentsRef = useRef(agents);

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
    const openNewTask = () => setShowNewChat(true);
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

    try {
      const results = await Promise.all(ownedAgents.map(async (agent) => {
        const agentId = agent.uid || agent.id;
        if (!agentId) return [];
        const res = await api.getPendingRequests(agentId).catch(() => ({ requests: [] }));
        return (res.requests || []).map((request) => ({
          ...request,
          agent_uid: agentId,
          agent_name: agent.display_name || agent.username || `助手 ${agentId}`,
        }));
      }));
      setAgentPendingRequests(results.flat());
    } catch (error) {
      console.warn('Failed to load agent friend requests:', error);
      setAgentPendingRequests([]);
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
    const unsub = onWSMessage((msg) => {
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
              time: formatTime(new Date()),
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
            time: formatTime(new Date(updatedAtMs)),
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
          time: formatTime(new Date(createdAtMs)),
          lastTimeMs: createdAtMs,
          createdAtMs,
          isGroup: true,
          avatar_url: group.avatar_url,
          hasBot: Boolean(group.has_bot),
          isAgentTask: Boolean(group.is_agent_task || group.kind === 'agent_task'),
          memberCount: Number(group.member_count || 0),
          seq: 0,
        },
        ...prev.filter((chat) => chat.id !== topicId),
      ]);
      setGroups((prev) => [group, ...prev.filter((item) => String(item.id) !== String(group.id))]);
    }
    loadAll();
  };
  const handleAccept = async (userId) => { await api.acceptFriend(userId); loadAll(); };
  const handleReject = async (userId) => { await api.rejectFriend(userId); loadAll(); };
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
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || '处理助手好友申请失败');
    } finally {
      setAgentReviewingKey('');
    }
  };

  const handleRemoveAgent = async (agent) => {
    const agentId = agent?.uid || agent?.id;
    if (!agentId || isOwnedAgent(agent)) return;
    const confirmed = window.confirm(`确定从 AI 助手列表中移除“${agent.display_name || agent.username}”吗？\n\n这只会解除你的好友关系，不会删除对方创建的虚拟员工。`);
    if (!confirmed) return;
    try {
      setAgentActionId(String(agentId));
      await api.removeFriend(agentId);
      const topicId = agent.topic_id || p2pTopicId(user.uid, agentId);
      if (activeTopic === topicId) {
        onSelectTopic(null);
      }
      await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || '移除助手失败');
    } finally {
      setAgentActionId('');
    }
  };

  const handleFriendAction = async (chat, action) => {
    const friendId = chat?.friendId;
    if (!friendId) return;
    const isBlock = action === 'block';
    const confirmed = window.confirm(
      isBlock
        ? `确定拉黑“${chat.name}”吗？\n\n拉黑后对方将无法再向你发送消息。`
        : `确定删除好友“${chat.name}”吗？`
    );
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
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || (isBlock ? '拉黑好友失败' : '删除好友失败'));
    } finally {
      setFriendActionId('');
    }
  };

  const handleDeleteGroup = async ({ groupId, topicId, name }) => {
    if (!groupId || !topicId) return;

    const confirmed = window.confirm(
      `确定永久删除群聊“${name}”吗？\n\n删除后会移除群聊、所有成员和聊天记录。`
    );
    if (!confirmed) return;

    setDeletingTopicId(topicId);
    try {
      await api.disbandGroup(groupId);
      if (activeTopic === topicId) {
        onSelectTopic(null);
      }
      await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || 'Failed to delete group.');
    } finally {
      setDeletingTopicId('');
    }
  };

  const handleSelectAgent = (agent) => {
    const agentId = agent.uid || agent.id;
    if (!agentId) return;
    onStartAgentTask?.(agent);
  };

  const handleNewChatWithAgent = (agent) => {
    const agentId = agent.uid || agent.id;
    if (!agentId) return;
    setShowNewChat(false);
    onStartAgentTask?.(agent);
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
  const agentContactIds = new Set(agents.flatMap((agent) => (
    [agent?.id, agent?.uid].filter(Boolean).map(String)
  )));
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

  const handleAssignProject = async (project) => {
    if (!projectPickerTask || !project?.id) return;
    setProjectActionTopicId(projectPickerTask.id);
    try {
      await api.assignProjectTopic(project.id, projectPickerTask.id);
      setExpandedProjectId(Number(project.id));
      await loadAll();
      closeProjectDialog();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || '加入项目失败');
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
    } catch (err) {
      window.alert(err.message || '移出项目失败');
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
    } catch (err) {
      window.alert(err.message || '创建项目失败');
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
    } catch (err) {
      window.alert(err.message || '更改项目名称失败');
    } finally {
      setProjectActionId(null);
    }
  };

  const handleDeleteProject = async (project) => {
    const projectId = Number(project?.id);
    if (!projectId) return;
    setOpenProjectMenuId(null);
    const confirmed = window.confirm(`确定删除项目“${project.name}”吗？\n\n项目中的任务会保留，并回到任务列表。`);
    if (!confirmed) return;
    setProjectActionId(projectId);
    try {
      await api.deleteProject(projectId);
      if (expandedProjectId === projectId) setExpandedProjectId(null);
      await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || '删除项目失败');
    } finally {
      setProjectActionId(null);
    }
  };

  const handleDeleteHistoryTask = async (chat) => {
    const actionLabel = onDeleteHistoryTask ? '删除任务' : '从列表移除';
    const confirmation = onDeleteHistoryTask
      ? `确定删除任务“${chat.name}”吗？`
      : `确定从任务列表移除“${chat.name}”吗？\n\n此操作只影响当前浏览器，不会删除历史消息。`;
    const confirmed = window.confirm(confirmation);
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
    } catch (err) {
      window.alert(err.message || `${actionLabel}失败`);
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
    } catch (err) {
      window.alert(err.message || '修改任务名称失败');
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
    return (
      <>
        <div className="cc-chat-row-trailing">
          <TaskRowStatusIndicator status={visibleStatus} time={chat.time} showTime={showTime} />
          <div className="cc-chat-row-actions">
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
          </div>
        </div>
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
            {chat.isGroup && (
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
            )}
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
    if (taskKind === 'solo_agent' || taskKind === 'multi_agent') {
      const taskLabel = taskKind === 'multi_agent' ? '协作' : '';
      const menuKey = `task:${chat.id}`;
      return (
        <div
          key={chat.id}
          className={`v3-chat-item cc-history-item cc-conversation-item ${activeTopic === chat.id ? 'active' : ''}`}
          data-conversation-kind="agent"
          data-task-kind={taskKind === 'multi_agent' ? 'collaboration' : 'solo'}
          onClick={() => selectConversation(chat)}
        >
          <Zap size={14} className="prefix cc-chat-row-icon" aria-label="Agent 任务" />
          {renderTaskCopy(chat, null, taskLabel)}
          {renderTaskControls(chat, menuKey, { showPin: true, showTime: true })}
        </div>
      );
    }

    if (chat.isGroup && !chat.isAgentTask) {
      const canDelete = groupOwnerById.get(String(chat.groupId)) === String(user.uid);
      const isPinned = pinnedGroupIds.has(String(chat.id));
      const menuKey = `group:${chat.id}`;
      return (
        <div
          key={chat.id}
          className={`v3-chat-item cc-conversation-item cc-group-conversation-item ${activeTopic === chat.id ? 'active' : ''}`}
          data-conversation-kind="group"
          onClick={() => selectConversation(chat)}
        >
          <Users size={14} className="prefix cc-chat-row-icon" aria-label="群聊" />
          {renderTaskCopy(chat, chat.preview, '群聊')}
          <div className="cc-chat-row-trailing">
            {chat.time && <span className="cc-chat-row-time">{chat.time}</span>}
            <div className="cc-chat-row-actions">
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
            </div>
          </div>
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
        </div>
      );
    }

    if (!chat.isGroup && !chat.isBot) {
      const isOnline = onlineStatusFor(onlineUsers, chat.friendId, chat.isOnline);
      return (
        <div
          key={chat.id}
          className={`v3-chat-item cc-conversation-item v3-friend-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
          data-conversation-kind="direct"
          onClick={() => selectConversation(chat)}
        >
          <UserRound size={14} className="prefix cc-chat-row-icon" aria-label="单聊" />
          {renderTaskCopy(chat, chat.preview, '单聊')}
          <div className="cc-chat-row-trailing">
            {chat.time && <span className="cc-chat-row-time">{chat.time}</span>}
            <div className="cc-chat-row-actions">
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
            </div>
          </div>
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
        </div>
      );
    }

    const menuKey = `history:${chat.id}`;
    return (
      <div
        key={chat.id}
        className={`v3-chat-item cc-history-item cc-conversation-item ${activeTopic === chat.id ? 'active' : ''}`}
        data-conversation-kind="agent"
        onClick={() => selectConversation(chat)}
      >
        <Zap size={14} className="prefix cc-chat-row-icon" aria-label="Agent 任务" />
        {renderTaskCopy(chat, null, '任务')}
        {renderTaskControls(chat, menuKey, { showPin: true, showTime: true })}
      </div>
    );
  };

  const renderContactRow = ({ kind, item }) => {
    if (kind === 'group') {
      const chat = item;
      const canDelete = groupOwnerById.get(String(chat.groupId)) === String(user.uid);
      const isPinned = pinnedGroupIds.has(String(chat.id));
      const menuKey = `group:${chat.id}`;
      return (
        <div
          key={`group:${chat.id}`}
          className={`v3-chat-item cc-contact-item cc-group-contact-item ${activeTopic === chat.id ? 'active' : ''}`}
          data-contact-kind="group"
          onClick={() => selectConversation(chat)}
        >
          <Users size={14} className="prefix cc-chat-row-icon" aria-label="群组" />
          {renderTaskCopy(chat, chat.preview)}
          <div className="cc-chat-row-trailing">
            {chat.time && <span className="cc-chat-row-time">{chat.time}</span>}
            <div className="cc-chat-row-actions">
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
            </div>
          </div>
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
        </div>
      );
    }

    if (kind === 'friend') {
      const chat = item;
      const isOnline = onlineStatusFor(onlineUsers, chat.friendId, chat.isOnline);
      const hasUnreadMessage = unreadFriendTopicIds.has(chat.id);
      return (
        <div
          key={`friend:${chat.friendId}`}
          className={`v3-chat-item cc-contact-item v3-friend-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
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
          <div className="cc-chat-row-trailing">
            {hasUnreadMessage
              ? <span className="cc-friend-unread-dot" role="status" aria-label={`${chat.name} 有新消息`} />
              : chat.time && <span className="cc-chat-row-time">{chat.time}</span>}
            <div className="cc-chat-row-actions">
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
            </div>
          </div>
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
        </div>
      );
    }

    const agent = item;
    const agentId = agent.uid || agent.id;
    const isOnline = onlineStatusFor(onlineUsers, agentId, agent.is_online);
    const owned = isOwnedAgent(agent);
    return (
      <div
        key={`agent:${agentId}`}
        className="v3-chat-item cc-contact-item cc-agent-roster-item"
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
        <div className="cc-chat-row-trailing cc-agent-row-trailing">
          <div className="v3-agent-row-actions">
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
          </div>
        </div>
      </div>
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
            onClick={() => setShowNewChat(true)}
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
                onClick={() => selectConversation(chat)}
                aria-label={`打开任务：${chat.name}`}
                title={chat.name}
              >
                <Avatar name={chat.name} src={chat.avatar_url} size={32} />
              </button>
            ))}
          </div>
        </nav>
      )}

      {!compact && <div className="cc-sidebar-tools">
        <button type="button" className="cc-sidebar-primary" onClick={() => setShowNewChat(true)}>
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
        <div className="v3-chat-section cc-top-level-section cc-conversation-section">
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('conversations')} aria-expanded={!collapsed.conversations}>
            <span>任务</span>
            <ChevronRight size={14} />
          </button>
          <button type="button" className="cc-section-add" onClick={() => setShowNewChat(true)} title="新建任务" aria-label="新建任务"><Plus size={15} /></button>
        </div>
        {(isSearching || !collapsed.conversations) && (conversationChats.length === 0 && !isSearching ? (
          <div className="cc-sidebar-empty cc-conversation-empty">选择一个 Agent 开始新任务</div>
        ) : (
          conversationChats.map(renderConversationRow)
        ))}

        {/* 联系人：好友与 Agent 共用入口，通过行内类型软区分 */}
        <div className="v3-chat-section cc-top-level-section cc-contacts-section" style={{ position: 'relative' }}>
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('contacts')} aria-expanded={!collapsed.contacts}>
            <span>联系人</span>
            {hasUnreadFriendMessages && (
              <span className="cc-section-unread-dot" role="status" aria-label="联系人有新消息" />
            )}
            <ChevronRight size={14} />
            {(pending.length + agentPendingRequests.length) > 0 && (
              <span className="v3-agent-request-badge">{pending.length + agentPendingRequests.length}</span>
            )}
          </button>
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
        </div>
        {(isSearching || !collapsed.contacts) && (
          <>
            {!isSearching && pending.length > 0 && (
              <div className="cc-contact-requests" style={{ padding: '0 16px', marginBottom: 12 }}>
                <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--v3-primary)', textTransform: 'uppercase', marginBottom: 8 }}>
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
        <div className="v3-chat-section cc-top-level-section cc-project-section">
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('projects')} aria-expanded={!collapsed.projects}>
            <span>{'项目'}</span>
            <ChevronRight size={14} />
          </button>
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
        </div>
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
                <div className="cc-project-row">
                  <button
                    type="button"
                    className="v3-chat-item cc-project-item"
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
                    <span className="cc-project-count" aria-label={`${project.task_count || 0} 个任务`}>{project.task_count || 0}</span>
                  </button>
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
                  {openProjectMenuId === projectId && (
                    <div className="v3-friend-action-menu cc-project-action-menu" role="menu" onClick={(event) => event.stopPropagation()}>
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
                </div>
                {expanded && (projectTasks.length > 0 ? projectTasks.map((chat) => {
                  const menuKey = `project:${chat.id}`;
                  const taskKind = conversationKind(chat);
                  const taskLabel = taskKind === 'multi_agent' ? '协作' : '';
                  return (
                    <div
                      key={chat.id}
                      className={`v3-chat-item cc-history-item cc-conversation-item cc-project-task-item ${activeTopic === chat.id ? 'active' : ''}`}
                      data-task-kind={taskKind === 'multi_agent' ? 'collaboration' : 'solo'}
                      aria-label={`打开项目任务 ${chat.name}`}
                      onClick={() => selectConversation(chat)}
                    >
                      <Zap size={14} className="prefix cc-chat-row-icon" aria-label="Agent 任务" />
                      {renderTaskCopy(chat, null, taskLabel)}
                      {renderTaskControls(chat, menuKey, { showPin: true, showTime: true })}
                    </div>
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
        <div className="name-dialog-overlay cc-new-task-overlay" onClick={() => setShowNewChat(false)}>
          <section className="name-dialog cc-new-task-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
            <header className="cc-new-task-header">
              <h3>选择 AI 助手开始任务</h3>
              <button type="button" className="cc-dialog-close" onClick={() => setShowNewChat(false)} aria-label="关闭">
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
        <span className="cc-task-completed-dot" />
      </span>
    );
  }

  if (normalized.state === 'failed') {
    return (
      <span className="cc-task-row-status failed" title={detail || '任务执行失败'} aria-label="任务执行失败" role="status">
        <AlertCircle size={15} strokeWidth={2.3} />
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
    time: lastTimeMs ? formatTime(new Date(lastTimeMs)) : '',
    lastTimeMs,
    createdAtMs,
    isGroup: item.is_group,
    avatar_url: item.avatar_url,
    isBot: item.is_bot,
    hasBot: Boolean(item.has_bot || item.is_agent_group),
    isAgentTask: Boolean(item.is_agent_task || item.kind === 'agent_task'),
    memberCount: Number(item.member_count || 0),
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
    projectId: projectIdFor(item),
    avatar_url: item.avatar_url,
    preview: item.preview || '',
    time: item.time || (lastTimeMs ? formatTime(new Date(lastTimeMs)) : ''),
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
  return 'multi_agent';
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

function formatTime(date) {
  const h = date.getHours().toString().padStart(2, '0');
  const m = date.getMinutes().toString().padStart(2, '0');
  return `${h}:${m}`;
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
    time: createdAtMs ? formatTime(new Date(createdAtMs)) : '',
    lastTimeMs: createdAtMs,
    createdAtMs,
    isGroup: true,
    avatar_url: group.avatar_url,
    hasBot: Boolean(group.has_bot || group.is_agent_group),
    isAgentTask: Boolean(group.is_agent_task || group.kind === 'agent_task'),
    memberCount: Number(group.member_count || 0),
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
