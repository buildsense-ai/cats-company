import React, { useState, useEffect, useRef } from 'react';
import { createPortal } from 'react-dom';
import { api, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import CreateGroup from '../widgets/create-group';
import AddFriend from '../widgets/add-friend';
import FriendRequest from '../widgets/friend-request';
import AgentStoreModal from '../widgets/agent-store-modal';
import MobileChannelBindModal from '../widgets/mobile-channel-bind-modal';
import Avatar from '../widgets/avatar';
import { Users, UserRound, Zap, Bot, Trash2, MessageSquare, Smartphone, Check, X, Pin, ChevronRight, Plus, Search, MoreHorizontal, UserX, Ban, AlertCircle, CheckCircle2, Clock3, LoaderCircle } from 'lucide-react';

const SIDEBAR_COLLAPSED_STORAGE_PREFIX = 'cc_sidebar_collapsed_v1';
const DEFAULT_COLLAPSED_SECTIONS = { collaboration: false, ai: false, friends: false, groups: false, agents: false };
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
    collaboration: typeof value?.collaboration === 'boolean' ? value.collaboration : DEFAULT_COLLAPSED_SECTIONS.collaboration,
    ai: typeof value?.ai === 'boolean' ? value.ai : DEFAULT_COLLAPSED_SECTIONS.ai,
    friends: typeof value?.friends === 'boolean' ? value.friends : DEFAULT_COLLAPSED_SECTIONS.friends,
    groups: typeof value?.groups === 'boolean' ? value.groups : DEFAULT_COLLAPSED_SECTIONS.groups,
    agents: typeof value?.agents === 'boolean' ? value.agents : DEFAULT_COLLAPSED_SECTIONS.agents,
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
  onDeleteHistoryTask,
  onOpenMobileLink,
}) {
  const [chats, setChats] = useState([]);
  const [friends, setFriends] = useState([]);
  const [groups, setGroups] = useState([]);
  const [pending, setPending] = useState([]);
  const [agents, setAgents] = useState([]);
  const [search, setSearch] = useState('');
  const [deletingTopicId, setDeletingTopicId] = useState('');
  const [showCreateGroup, setShowCreateGroup] = useState(false);
  const [showAddFriend, setShowAddFriend] = useState(false);
  const [showAgentStore, setShowAgentStore] = useState(false);
  const [showNewChat, setShowNewChat] = useState(false);
  const [collapsed, setCollapsed] = useState(() => loadCollapsedSections(user?.uid));
  const [namingAgent, setNamingAgent] = useState(null);
  const [newChatName, setNewChatName] = useState('');
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
  const [friendActionId, setFriendActionId] = useState('');
  const [dismissedTaskStatuses, setDismissedTaskStatuses] = useState(() => loadDismissedTaskStatuses(user?.uid));
  const justHiddenHistoryRef = useRef('');
  const activeTopicRef = useRef(activeTopic);
  const userUidRef = useRef(user?.uid);

  useEffect(() => {
    setCollapsed(loadCollapsedSections(user?.uid));
    setPinnedGroupIds(loadPinnedGroupIds(user?.uid));
    setPinnedHistoryIds(loadPinnedHistoryIds(user?.uid));
    setHiddenHistoryIds(loadHiddenHistoryIds(user?.uid));
    setDismissedTaskStatuses(loadDismissedTaskStatuses(user?.uid));
  }, [user?.uid]);

  useEffect(() => {
    activeTopicRef.current = activeTopic;
  }, [activeTopic]);

  useEffect(() => {
    userUidRef.current = user?.uid;
  }, [user?.uid]);

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
    if (!openFriendMenuId && !openChatMenuKey) return undefined;
    const closeMenus = () => {
      setOpenFriendMenuId('');
      setOpenChatMenuKey('');
    };
    const closeMenusFromOutside = (event) => {
      const target = event.target;
      if (target instanceof Element && target.closest([
        '.v3-friend-action-menu',
        '.v3-friend-menu-trigger',
        '.v3-group-menu-trigger',
        '.v3-history-menu-trigger',
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
  }, [openFriendMenuId, openChatMenuKey]);

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

  const togglePinnedHistory = (topicId) => {
    setPinnedHistoryIds((prev) => {
      const next = new Set(prev);
      const key = String(topicId || '').trim();
      if (!key) return prev;
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      savePinnedHistoryIds(user?.uid, next);
      return next;
    });
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
    const reopenedTask = chats.some((chat) => (
      String(chat.id) === key && chat.isBot && !chat.isGroup
    ));
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
      const [resC, resF, resG, resP, resA] = await Promise.all([
        api.getConversations().catch((error) => ({ error })),
        api.getFriends().catch(()=>({})),
        api.getGroups().catch(()=>({})),
        api.getPendingRequests().catch(()=>({})),
        api.getAgents().catch(()=>({}))
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

  const handleSelectAgent = async (agent) => {
    const agentId = agent.uid || agent.id;
    if (!agentId) return;

    const fallbackTopicId = agent.topic_id || p2pTopicId(user.uid, agentId);
    const fallbackTopic = {
      topicId: fallbackTopicId,
      name: agent.display_name || agent.username,
      isGroup: false,
      avatar_url: agent.avatar_url,
      friendId: agentId,
      isBot: true,
    };

    try {
      const res = await api.openAgent(agentId);
      const opened = res.agent || {};
      const openedTopicId = opened.topic_id || res.topic || fallbackTopicId;
      const existingConversation = chats.find((chat) => chat.id === openedTopicId);
      restoreHistoryTask(openedTopicId);
      onSelectTopic({
        ...fallbackTopic,
        topicId: openedTopicId,
        name: existingConversation?.name || opened.display_name || fallbackTopic.name,
        avatar_url: existingConversation?.avatar_url || opened.avatar_url || fallbackTopic.avatar_url,
      });
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      console.error('Failed to open agent:', err);
      window.alert(err.message || 'Unable to open this agent.');
    }
  };

  const handleNewChatWithAgent = async (agent) => {
    const agentId = agent.uid || agent.id;
    if (!agentId) return;
    setNamingAgent(agent);
    setNewChatName(agent.display_name || agent.username);
  };

  const handleConfirmNewChat = async () => {
    if (!namingAgent || !newChatName.trim()) return;
    const agentId = namingAgent.uid || namingAgent.id;
    try {
      const res = await api.createGroup(newChatName.trim(), [agentId]);
      const group = normalizeCreatedGroup(res);
      if (group) {
        const topicId = res.topic || `grp_${group.id}`;
        onSelectTopic({ topicId, name: group.name, isGroup: true, groupId: group.id, avatar_url: group.avatar_url, hasBot: true });
      }
      setNamingAgent(null);
      setNewChatName('');
      setShowNewChat(false);
      await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || '创建对话失败');
    }
  };

  const trimmedSearch = search.trim();
  const lowerSearch = trimmedSearch.toLowerCase();
  const isSearching = trimmedSearch.length > 0;
  const recentChats = sortConversationsByRecent(chats);
  const visibleRecentChats = recentChats.filter((chat) => (
    chat.isGroup || !chat.isBot || !hiddenHistoryIds.has(String(chat.id))
  ));
  const filteredChats = visibleRecentChats.filter(c => c.name.toLowerCase().includes(lowerSearch));
  const directChats = filteredChats.filter(c => !c.isGroup);
  const mergedGroups = mergeGroupsWithConversations(groups, chats.filter(c => c.isGroup));
  const filteredFriends = friends.filter(f => userSearchText(f).includes(lowerSearch));
  const filteredGroups = mergedGroups.filter(g => g.name.toLowerCase().includes(lowerSearch));
  const filteredAgents = agents.filter(a => userSearchText(a).includes(lowerSearch));

  const aiChats = sortConversationsWithPins(
    directChats.filter((chat) => chat.isBot && !hiddenHistoryIds.has(String(chat.id))),
    pinnedHistoryIds,
  );
  const friendChats = directChats.filter(c => !c.isBot);
  const groupChats = sortGroupsWithPins(filteredGroups, pinnedGroupIds);
  const hasSearchResults = aiChats.length > 0 || friendChats.length > 0 || groupChats.length > 0 || filteredAgents.length > 0;
  const compactChats = visibleRecentChats.slice(0, 12);

  const selectConversation = (chat) => {
    rememberDismissedTaskStatus(chat.id, chat.taskStatus);
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

  const handleDeleteHistoryTask = async (chat) => {
    const actionLabel = onDeleteHistoryTask ? '删除任务' : '从列表移除';
    const confirmation = onDeleteHistoryTask
      ? `确定删除任务“${chat.name}”吗？`
      : `确定从历史任务列表移除“${chat.name}”吗？\n\n此操作只影响当前浏览器，不会删除历史消息。`;
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

  return (
    <>
      {compact && (
        <nav className="cc-sidebar-compact-rail" aria-label="对话快捷栏">
          <button
            type="button"
            className="cc-compact-new-chat"
            onClick={() => setShowNewChat(true)}
            aria-label="新建对话"
            title="新建对话"
          >
            <Plus size={20} />
          </button>
          <div className="cc-compact-conversations" aria-label="曾经对话">
            {compactChats.map((chat) => (
              <button
                type="button"
                key={chat.id}
                className={`cc-compact-conversation${activeTopic === chat.id ? ' active' : ''}`}
                onClick={() => selectConversation(chat)}
                aria-label={`打开对话：${chat.name}`}
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
          aria-label="搜索会话、联系人或助手"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
        </label>
      </div>}

      {!compact && <div className="v3-chat-list">

        {!isSearching && pending.length > 0 && (
          <div style={{ padding: '0 16px', marginBottom: 12 }}>
            <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--v3-primary)', textTransform: 'uppercase', marginBottom: 8 }}>
              好友请求 ({pending.length})
            </div>
            {pending.map((req) => (
              <FriendRequest key={req.id} request={req} onAccept={() => handleAccept(req.from_user_id)} onReject={() => handleReject(req.from_user_id)} />
            ))}
          </div>
        )}

        {/* AI 对话 */}
        <div className="v3-chat-section cc-history-section">
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('ai')} aria-expanded={!collapsed.ai}>
            <span>历史任务</span>
            <ChevronRight size={14} />
          </button>
          <button type="button" className="cc-section-add" onClick={() => setShowNewChat(true)} title="新建任务" aria-label="新建任务"><Plus size={15} /></button>
        </div>
        {(isSearching || !collapsed.ai) && (aiChats.length === 0 && !isSearching ? (
          <div className="cc-sidebar-empty cc-history-empty">点击 + 开始新任务</div>
        ) : (
          aiChats.map((chat) => {
            const isPinned = pinnedHistoryIds.has(String(chat.id));
            const menuKey = `history:${chat.id}`;
            const removeLabel = onDeleteHistoryTask ? '删除任务' : '从列表移除';
            return (
              <div key={chat.id} className={`v3-chat-item cc-history-item ${activeTopic === chat.id ? 'active' : ''}`}
                onClick={() => selectConversation(chat)}>
                <span className="prefix cc-chat-row-icon">{chat.isGroup ? '#' : <MessageSquare size={14} />}</span>
                <div className="cc-chat-row-copy">
                  <span className="v3-chat-item-label">{chat.name}</span>
                  <ConversationTaskStatusLine status={visibleTaskStatus(chat.taskStatus, dismissedTaskStatuses, chat.id)} fallback={chat.preview} />
                </div>
                <div className="cc-chat-row-trailing">
                  {chat.time && <span className="cc-chat-row-time">{chat.time}</span>}
                  <div className="cc-chat-row-actions">
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
                      aria-label={`${isPinned ? '取消置顶历史任务' : '置顶历史任务'} ${chat.name}`}
                      onClick={() => {
                        togglePinnedHistory(chat.id);
                        setOpenChatMenuKey('');
                      }}
                    >
                      <Pin size={14} fill={isPinned ? 'currentColor' : 'none'} />
                      <span>{isPinned ? '取消置顶任务' : '置顶任务'}</span>
                    </button>
                    <button type="button" role="menuitem" aria-label={`${chat.name} 手机扫码`} onClick={() => handleOpenMobileLink(chat)}>
                      <Smartphone size={14} />
                      <span>手机扫码</span>
                    </button>
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
                  </div>
                )}
              </div>
            );
          })
        ))}

        <div className="v3-chat-section cc-top-level-section cc-collaboration-section">
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('collaboration')} aria-expanded={!collapsed.collaboration}>
            <span>协作</span>
            <ChevronRight size={14} />
          </button>
        </div>

        {(isSearching || !collapsed.collaboration) && <div className="cc-sidebar-nested">
        {/* 好友 */}
        <div className="v3-chat-section">
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('friends')} aria-expanded={!collapsed.friends}><UserRound size={15} /><span>好友</span><ChevronRight size={13} /></button>
          <button type="button" className="cc-section-add" onClick={() => setShowAddFriend(true)} title="添加好友" aria-label="添加好友"><Plus size={15} /></button>
        </div>
        {(isSearching || !collapsed.friends) && (friendChats.length === 0 && !isSearching ? (
          <div className="cc-sidebar-empty">暂无好友</div>
        ) : (
          friendChats.map((chat) => {
            const isOnline = onlineStatusFor(onlineUsers, chat.friendId, chat.isOnline);
            return (
              <div key={chat.id} className={`v3-chat-item v3-friend-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
                onClick={() => selectConversation(chat)}>
                <span
                  className={`v3-status-dot ${isOnline ? 'online' : 'offline'}`}
                  style={{marginRight: 8}}
                  title={isOnline ? 'Online' : 'Offline'}
                  aria-label={isOnline ? 'Online' : 'Offline'}
                />
                <div className="cc-chat-row-copy">
                  <span className="v3-chat-item-label">{chat.name}</span>
                  <ConversationTaskStatusLine status={visibleTaskStatus(chat.taskStatus, dismissedTaskStatuses, chat.id)} fallback={chat.preview} />
                </div>
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
              </div>
            );
          })
        ))}

        {/* 群聊 */}
        <div className="v3-chat-section">
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('groups')} aria-expanded={!collapsed.groups}><Users size={15} /><span>群聊</span><ChevronRight size={13} /></button>
          <button type="button" className="cc-section-add" onClick={() => setShowCreateGroup(true)} title="创建群聊" aria-label="创建群聊"><Plus size={15} /></button>
        </div>
        {(isSearching || !collapsed.groups) && (groupChats.length === 0 && !isSearching ? (
          <div className="cc-sidebar-empty">暂无群聊</div>
        ) : (
          groupChats.map((chat) => {
            const canDelete = groupOwnerById.get(String(chat.groupId)) === String(user.uid);
            const isPinned = pinnedGroupIds.has(String(chat.id));
            const menuKey = `group:${chat.id}`;
            return (
              <div key={chat.id} className={`v3-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
                onClick={() => selectConversation(chat)}>
                <span className="prefix cc-chat-row-icon">#</span>
                <div className="cc-chat-row-copy">
                  <span className="v3-chat-item-label">{chat.name}</span>
                  <ConversationTaskStatusLine status={visibleTaskStatus(chat.taskStatus, dismissedTaskStatuses, chat.id)} fallback={chat.preview} />
                </div>
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
                      aria-label={`${chat.name} 群管理`}
                      disabled={!onManageGroup}
                      title={!onManageGroup ? '群管理入口暂未接入' : '群管理'}
                      onClick={() => {
                        setOpenChatMenuKey('');
                        onManageGroup?.(topicPayloadForChat(chat, true));
                      }}
                    >
                      <Users size={14} />
                      <span>群管理</span>
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
          })
        ))}

        {/* AI 助手 */}
        <div className="v3-chat-section">
          <button type="button" className="cc-section-toggle" onClick={() => toggleCollapsed('agents')} aria-expanded={!collapsed.agents}>
            <Zap size={15} />
            <span>Agent 助手</span>
            <ChevronRight size={13} />
            {agentPendingRequests.length > 0 && <span className="v3-agent-request-badge">{agentPendingRequests.length}</span>}
          </button>
          <button type="button" className="cc-section-add" onClick={() => setShowAgentStore(true)} title="管理 Agent 助手" aria-label="管理 Agent 助手"><Plus size={15} /></button>
        </div>
        {!isSearching && agentPendingRequests.length > 0 && (
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
        )}
        {(isSearching || !collapsed.agents) && (filteredAgents.length === 0 ? (
          <div className="cc-sidebar-empty">暂无 Agent 助手</div>
        ) : (
          filteredAgents.map((agent) => {
            const agentId = agent.uid || agent.id;
            const isOnline = onlineStatusFor(onlineUsers, agentId, agent.is_online);
            const owned = isOwnedAgent(agent);
            return (
              <div
                key={agentId}
                className="v3-chat-item cc-agent-roster-item"
                title={agentIdentity(agent)}
                onClick={() => handleSelectAgent(agent)}
              >
                <span className="prefix" style={{display: 'flex', alignItems: 'center'}}><Bot size={18} /></span>
                <span className="v3-chat-item-main">
                  <span className="v3-chat-item-label">{agent.display_name || agent.username}</span>
                  <span className="v3-chat-item-identity">{agentVisibleIdentity(agent)}</span>
                </span>
                <div className="v3-agent-row-actions">
                  <button
                    type="button"
                    className="v3-chat-item-action"
                    title="移动端使用"
                    aria-label={`${agent.display_name || agent.username} 移动端使用`}
                    onClick={(e) => {
                      e.stopPropagation();
                      setMobileLinkAgent(agent);
                    }}
                  >
                    <Smartphone size={14} />
                  </button>
                  {!owned && (
                    <button
                      type="button"
                      className="v3-chat-item-action danger"
                      title="移除助手"
                      aria-label={`移除 ${agent.display_name || agent.username}`}
                      disabled={agentActionId === String(agentId)}
                      onClick={(e) => {
                        e.stopPropagation();
                        handleRemoveAgent(agent);
                      }}
                    >
                      <Trash2 size={14} />
                    </button>
                  )}
                  <span
                    className={`v3-status-dot ${isOnline ? 'online' : 'offline'}`}
                    title={isOnline ? 'Online' : 'Offline'}
                    aria-label={isOnline ? 'Online' : 'Offline'}
                  />
                </div>
              </div>
            );
          })
        ))}
        </div>}

        <div className="v3-chat-section cc-top-level-section cc-project-section">
          <button type="button" className="cc-section-toggle" aria-expanded="false">
            <span>{'项目'}</span>
            <ChevronRight size={14} />
          </button>
          <button type="button" className="cc-section-add" disabled title="项目接口尚未接入" aria-label="项目接口尚未接入"><Plus size={15} /></button>
        </div>

        {isSearching && !hasSearchResults && (
          <div className="cc-search-empty" style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)', fontSize: '13px' }}>没有匹配结果</div>
        )}

      </div>}

      {showNewChat && createPortal(
        <div className="name-dialog-overlay cc-new-task-overlay" onClick={() => { setShowNewChat(false); setNamingAgent(null); }}>
          <section className="name-dialog cc-new-task-dialog" role="dialog" aria-modal="true" onClick={(e) => e.stopPropagation()}>
            <header className="cc-new-task-header">
              <h3>{namingAgent ? '为对话取个名字' : '选择 AI 助手开始对话'}</h3>
              <button type="button" className="cc-dialog-close" onClick={() => { setShowNewChat(false); setNamingAgent(null); }} aria-label="关闭">
                <X size={18} />
              </button>
            </header>
            <div className="cc-new-task-body">
            {!namingAgent ? (
              <>
                {agents.length === 0 ? (
                  <div className="cc-new-task-empty">
                    <strong>暂无 AI 助手</strong>
                    <span>请先在“协作 &gt; Agent 助手”中创建</span>
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
              </>
            ) : (
              <>
                <input
                  autoFocus
                  className="oc-auth-input cc-new-task-name"
                  value={newChatName}
                  onChange={(e) => setNewChatName(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') handleConfirmNewChat(); }}
                  placeholder="对话名称"
                />
                <div className="cc-new-task-actions">
                  <button type="button" className="oc-btn oc-btn-default" onClick={() => setNamingAgent(null)}>
                    返回
                  </button>
                  <button type="button" className="oc-btn oc-btn-primary" onClick={handleConfirmNewChat}>
                    创建
                  </button>
                </div>
              </>
            )}
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

function ConversationTaskStatusLine({ status, fallback }) {
  const normalized = normalizeTaskStatus(status);
  if (!normalized || normalized.state === 'idle') {
    return fallback ? <span className="cc-chat-row-preview">{fallback}</span> : null;
  }

  const descriptor = taskStatusDescriptor(normalized.state);
  const Icon = descriptor.icon;
  const text = normalized.summary || normalized.error || descriptor.label;
  return (
    <span className={`cc-task-status-line ${descriptor.className}`} title={text}>
      <span className="cc-task-status-pill">
        <Icon size={11} strokeWidth={2.4} />
        <span>{descriptor.label}</span>
      </span>
      {text !== descriptor.label && <span className="cc-task-status-summary">{text}</span>}
    </span>
  );
}

function taskStatusDescriptor(state) {
  switch (state) {
    case 'running': return { label: '进行中', className: 'running', icon: LoaderCircle };
    case 'completed': return { label: '已完成', className: 'completed', icon: CheckCircle2 };
    case 'failed': return { label: '需处理', className: 'failed', icon: AlertCircle };
    case 'cancelled': return { label: '已停止', className: 'cancelled', icon: Clock3 };
    case 'stale': return { label: '超时', className: 'stale', icon: Clock3 };
    case 'waiting': return { label: '等待中', className: 'waiting', icon: Clock3 };
    default: return { label: '状态', className: 'idle', icon: Clock3 };
  }
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
    isOnline: item.is_online,
    seq: item.latest_seq || 0,
    taskStatus: normalizeTaskStatus(item.task_status),
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

function sortConversationsWithPins(items, pinnedTopicIds) {
  return [...items].sort((left, right) => {
    const leftPinned = pinnedTopicIds?.has(String(left.id));
    const rightPinned = pinnedTopicIds?.has(String(right.id));
    if (leftPinned !== rightPinned) return leftPinned ? -1 : 1;
    return conversationRecentLess(left, right);
  });
}

function sortGroupsWithPins(items, pinnedGroupIds) {
  return [...items].sort((left, right) => {
    const leftPinned = pinnedGroupIds?.has(String(left.id));
    const rightPinned = pinnedGroupIds?.has(String(right.id));
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

function agentVisibleIdentity(agent) {
  if (agent?.username) return `@${agent.username}`;
  return agent?.uid || agent?.id ? `uid ${agent.uid || agent.id}` : '';
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
