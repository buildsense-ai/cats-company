import React, { useState, useEffect } from 'react';
import { api, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import CreateGroup from '../widgets/create-group';
import AddFriend from '../widgets/add-friend';
import FriendRequest from '../widgets/friend-request';
import AgentStoreModal from '../widgets/agent-store-modal';
import { Users, UserPlus, Zap, Bot, Trash2, Plus, MessageSquare } from 'lucide-react';

export default function ChatListView({ activeTopic, onSelectTopic, user, onlineUsers }) {
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
  const [collapsed, setCollapsed] = useState({ ai: false, friends: false, groups: false, agents: false });
  const [namingAgent, setNamingAgent] = useState(null);
  const [newChatName, setNewChatName] = useState('');

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
      const conversations = conversationItems.map((item) => ({
        id: item.id,
        friendId: item.friend_id,
        groupId: item.group_id,
        name: item.name,
        preview: item.preview || '',
        time: item.last_time ? formatTime(new Date(item.last_time)) : '',
        isGroup: item.is_group,
        avatar_url: item.avatar_url,
        isBot: item.is_bot,
        hasBot: Boolean(item.has_bot || item.is_agent_group),
        isOnline: item.is_online,
        seq: item.latest_seq || 0,
      }));
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
      setAgents(resA.agents || []);
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
      setChats((prev) => [
        {
          id: topicId,
          groupId: group.id,
          name: group.name,
          preview: '',
          time: formatTime(new Date(group.created_at || Date.now())),
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

  const handleDeleteGroup = async ({ groupId, topicId, name }) => {
    if (!groupId || !topicId) return;

    const confirmed = window.confirm(
      `Delete group "${name}" permanently?\n\nThis will remove the group, all members, and all chat history.`
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
      onSelectTopic({
        ...fallbackTopic,
        topicId: opened.topic_id || res.topic || fallbackTopicId,
        name: opened.display_name || fallbackTopic.name,
        avatar_url: opened.avatar_url || fallbackTopic.avatar_url,
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
  const filteredChats = chats.filter(c => c.name.toLowerCase().includes(lowerSearch));
  const filteredFriends = friends.filter(f => (f.display_name || f.username).toLowerCase().includes(lowerSearch));
  const filteredGroups = groups.filter(g => g.name.toLowerCase().includes(lowerSearch));
  const filteredAgents = agents.filter(a => (a.display_name || a.username).toLowerCase().includes(lowerSearch));

  const botGroupIds = readStoredBotGroupIds();
  const isAIGroupChat = (chat) => chat.isGroup && (chat.hasBot || chat.isAgentGroup || botGroupIds.has(chat.id));
  const aiChats = filteredChats.filter(c => (c.isBot && !c.isGroup) || isAIGroupChat(c));
  const friendChats = filteredChats.filter(c => !c.isGroup && !c.isBot);
  const groupChats = filteredChats.filter(c => c.isGroup && !isAIGroupChat(c));
  const hasSearchResults = aiChats.length > 0 || friendChats.length > 0 || groupChats.length > 0 || filteredAgents.length > 0;

  return (
    <>
      <div style={{padding: '12px 16px', borderBottom: '1px solid var(--v3-border)'}}>
        <input
          style={{width: '100%', background: 'rgba(255,255,255,0.03)', border: 'none', color: '#fff', padding: '8px 12px', borderRadius: '6px', outline: 'none', fontSize: '13px'}}
          placeholder="搜索..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>

      <div className="v3-chat-list">

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
        <div className="v3-chat-section" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
          <span style={{display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer'}} onClick={() => setCollapsed(p => ({...p, ai: !p.ai}))}><span style={{fontSize: 12, color: '#666'}}>{collapsed.ai ? '▶' : '▼'}</span><Bot size={20} style={{color: 'var(--v3-primary)'}} /> AI 对话</span>
          <span onClick={() => setShowNewChat(true)} style={{cursor: 'pointer', fontSize: 25, color: '#888', lineHeight: 1}} title="新对话">+</span>
        </div>
        {(isSearching || !collapsed.ai) && (aiChats.length === 0 && !isSearching ? (
          <div style={{ padding: '12px 20px', color: '#666', fontSize: '13px' }}>点击 + 开始新对话</div>
        ) : (
          aiChats.map((chat) => {
            const canDelete = chat.isGroup && groupOwnerById.get(String(chat.groupId)) === String(user.uid);
            const isOnline = !chat.isGroup && ((onlineUsers && onlineUsers[chat.friendId]) || chat.isOnline);
            return (
              <div key={chat.id} className={`v3-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
                onClick={() => onSelectTopic({ topicId: chat.id, name: chat.name, isGroup: chat.isGroup, groupId: chat.groupId, avatar_url: chat.avatar_url, friendId: chat.friendId })}>
                <span className="prefix" style={{fontSize: '16px'}}>{chat.isGroup ? '#' : '●'}</span>
                <div style={{flex: 1, overflow: 'hidden'}}>
                  <span className="v3-chat-item-label">{chat.name}</span>
                  {chat.preview && <div style={{fontSize: 12, color: '#555', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{chat.preview}</div>}
                </div>
                {chat.time && <span style={{fontSize: 11, color: '#555', flexShrink: 0}}>{chat.time}</span>}
                {!chat.isGroup && <span className={`v3-status-dot ${isOnline ? 'online' : 'offline'}`} style={{marginLeft: 4}} />}
                {canDelete && (
                  <button type="button" className="v3-chat-item-delete" disabled={deletingTopicId === chat.id}
                    onClick={(e) => { e.stopPropagation(); handleDeleteGroup({ groupId: chat.groupId, topicId: chat.id, name: chat.name }); }} title="删除">
                    <Trash2 size={14} />
                  </button>
                )}
              </div>
            );
          })
        ))}

        {/* 好友 */}
        <div className="v3-chat-section" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 12}}>
          <span style={{display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer'}} onClick={() => setCollapsed(p => ({...p, friends: !p.friends}))}><span style={{fontSize: 12, color: '#666'}}>{collapsed.friends ? '▶' : '▼'}</span><MessageSquare size={20} style={{color: '#888'}} /> 好友</span>
          <span onClick={() => setShowAddFriend(true)} style={{cursor: 'pointer', fontSize: 25, color: '#888', lineHeight: 1}} title="添加好友">+</span>
        </div>
        {(isSearching || !collapsed.friends) && (friendChats.length === 0 && !isSearching ? (
          <div style={{ padding: '12px 20px', color: '#666', fontSize: '13px' }}>暂无好友对话</div>
        ) : (
          friendChats.map((chat) => {
            const isOnline = (onlineUsers && onlineUsers[chat.friendId]) || chat.isOnline;
            return (
              <div key={chat.id} className={`v3-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
                onClick={() => onSelectTopic({ topicId: chat.id, name: chat.name, isGroup: false, avatar_url: chat.avatar_url, friendId: chat.friendId })}>
                <span className={`v3-status-dot ${isOnline ? 'online' : 'offline'}`} style={{marginRight: 8}} />
                <div style={{flex: 1, overflow: 'hidden'}}>
                  <span className="v3-chat-item-label">{chat.name}</span>
                  {chat.preview && <div style={{fontSize: 12, color: '#555', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{chat.preview}</div>}
                </div>
                {chat.time && <span style={{fontSize: 11, color: '#555', flexShrink: 0}}>{chat.time}</span>}
              </div>
            );
          })
        ))}

        {/* 群聊 */}
        <div className="v3-chat-section" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 12}}>
          <span style={{display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer'}} onClick={() => setCollapsed(p => ({...p, groups: !p.groups}))}><span style={{fontSize: 12, color: '#666'}}>{collapsed.groups ? '▶' : '▼'}</span><Users size={20} style={{color: '#888'}} /> 群聊</span>
          <span onClick={() => setShowCreateGroup(true)} style={{cursor: 'pointer', fontSize: 25, color: '#888', lineHeight: 1}} title="创建群聊">+</span>
        </div>
        {(isSearching || !collapsed.groups) && (groupChats.length === 0 && !isSearching ? (
          <div style={{ padding: '12px 20px', color: '#666', fontSize: '13px' }}>暂无群聊</div>
        ) : (
          groupChats.map((chat) => {
            const canDelete = groupOwnerById.get(String(chat.groupId)) === String(user.uid);
            return (
              <div key={chat.id} className={`v3-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
                onClick={() => onSelectTopic({ topicId: chat.id, name: chat.name, isGroup: true, groupId: chat.groupId, avatar_url: chat.avatar_url })}>
                <span className="prefix" style={{fontSize: '16px'}}>#</span>
                <div style={{flex: 1, overflow: 'hidden'}}>
                  <span className="v3-chat-item-label">{chat.name}</span>
                  {chat.preview && <div style={{fontSize: 12, color: '#555', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap'}}>{chat.preview}</div>}
                </div>
                {chat.time && <span style={{fontSize: 11, color: '#555', flexShrink: 0}}>{chat.time}</span>}
                {canDelete && (
                  <button type="button" className="v3-chat-item-delete" disabled={deletingTopicId === chat.id}
                    onClick={(e) => { e.stopPropagation(); handleDeleteGroup({ groupId: chat.groupId, topicId: chat.id, name: chat.name }); }} title="删除">
                    <Trash2 size={14} />
                  </button>
                )}
              </div>
            );
          })
        ))}

        {/* AI 助手 */}
        <div className="v3-chat-section" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 12}}>
          <span style={{display: 'flex', alignItems: 'center', gap: 6, cursor: 'pointer'}} onClick={() => setCollapsed(p => ({...p, agents: !p.agents}))}><span style={{fontSize: 12, color: '#666'}}>{collapsed.agents ? '▶' : '▼'}</span><Zap size={20} fill="currentColor" style={{color: 'var(--v3-primary)'}} /> AI 助手</span>
          <span onClick={() => setShowAgentStore(true)} style={{cursor: 'pointer', fontSize: 25, color: '#888', lineHeight: 1}} title="管理 AI 助手">+</span>
        </div>
        {(isSearching || !collapsed.agents) && (filteredAgents.length === 0 ? (
          <div style={{ padding: '12px 20px', color: '#666', fontSize: '13px' }}>暂无 AI 助手，点击 + 创建</div>
        ) : (
          filteredAgents.map((agent) => {
            const agentId = agent.uid || agent.id;
            const isOnline = Boolean((onlineUsers && onlineUsers[agentId]) || agent.is_online);
            return (
              <div key={agentId} className="v3-chat-item" style={{opacity: 0.7, cursor: 'default'}}>
                <span className="prefix" style={{display: 'flex', alignItems: 'center'}}><Bot size={18} /></span>
                <span className="v3-chat-item-label">{agent.display_name || agent.username}</span>
                <span className={`v3-status-dot ${isOnline ? 'online' : 'offline'}`} style={{marginLeft: 'auto'}} />
              </div>
            );
          })
        ))}

        {isSearching && !hasSearchResults && (
          <div style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)', fontSize: '13px' }}>没有匹配结果</div>
        )}

      </div>

      {showNewChat && (
        <div style={{position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.5)', zIndex: 100, display: 'flex', alignItems: 'center', justifyContent: 'center'}} onClick={() => { setShowNewChat(false); setNamingAgent(null); }}>
          <div style={{background: 'var(--v3-bg-secondary, #1a1a2e)', borderRadius: 12, padding: 24, minWidth: 300, maxWidth: 400}} onClick={(e) => e.stopPropagation()}>
            {!namingAgent ? (
              <>
                <h3 style={{margin: '0 0 16px', fontSize: 16, color: '#fff'}}>选择 AI 助手开始对话</h3>
                {agents.length === 0 ? (
                  <div style={{color: '#888', fontSize: 13, textAlign: 'center', padding: 20}}>暂无 AI 助手，请先在 AI 助手区创建</div>
                ) : (
                  <div style={{display: 'flex', flexDirection: 'column', gap: 8}}>
                    {agents.map((agent) => (
                      <button key={agent.uid || agent.id} onClick={() => handleNewChatWithAgent(agent)}
                        style={{display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px', background: 'rgba(255,255,255,0.05)', border: '1px solid var(--v3-border)', borderRadius: 8, cursor: 'pointer', color: '#fff', fontSize: 14, textAlign: 'left'}}>
                        <Bot size={20} style={{color: 'var(--v3-primary)', flexShrink: 0}} />
                        <span>{agent.display_name || agent.username}</span>
                      </button>
                    ))}
                  </div>
                )}
              </>
            ) : (
              <>
                <h3 style={{margin: '0 0 16px', fontSize: 16, color: '#fff'}}>为对话取个名字</h3>
                <input
                  autoFocus
                  className="oc-auth-input"
                  style={{width: '100%', padding: '10px 14px', marginBottom: 12}}
                  value={newChatName}
                  onChange={(e) => setNewChatName(e.target.value)}
                  onKeyDown={(e) => { if (e.key === 'Enter') handleConfirmNewChat(); }}
                  placeholder="对话名称"
                />
                <div style={{display: 'flex', gap: 8}}>
                  <button onClick={() => setNamingAgent(null)}
                    style={{flex: 1, padding: '10px', background: 'none', border: '1px solid var(--v3-border)', borderRadius: 8, cursor: 'pointer', color: '#888', fontSize: 14}}>
                    返回
                  </button>
                  <button onClick={handleConfirmNewChat}
                    style={{flex: 1, padding: '10px', background: 'var(--v3-primary)', border: 'none', borderRadius: 8, cursor: 'pointer', color: '#fff', fontSize: 14}}>
                    创建
                  </button>
                </div>
              </>
            )}
          </div>
        </div>
      )}

      {showCreateGroup && <CreateGroup onClose={() => setShowCreateGroup(false)} onCreated={handleGroupCreated} />}
      {showAddFriend && <AddFriend currentUser={user} onClose={() => setShowAddFriend(false)} onSent={() => loadAll()} />}
      {showAgentStore && <AgentStoreModal onClose={() => setShowAgentStore(false)} user={user} onBotsChanged={() => loadAll()} />}
    </>
  );
}

function p2pTopicId(uid1, uid2) {
  let u1 = parseInt(uid1, 10);
  let u2 = parseInt(uid2, 10);
  if (u1 > u2) [u1, u2] = [u2, u1];
  return `p2p_${u1}_${u2}`;
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
  return {
    id: `grp_${group.id}`,
    groupId: group.id,
    name: group.name,
    preview: '',
    time: group.created_at ? formatTime(new Date(group.created_at)) : '',
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

function readStoredBotGroupIds() {
  try {
    return new Set(JSON.parse(localStorage.getItem('cc_bot_groups') || '[]'));
  } catch (err) {
    return new Set();
  }
}
