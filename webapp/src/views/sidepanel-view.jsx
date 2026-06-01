import React, { useState, useEffect } from 'react';
import { api, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import CreateGroup from '../widgets/create-group';
import AddFriend from '../widgets/add-friend';
import FriendRequest from '../widgets/friend-request';
import AgentStoreModal from '../widgets/agent-store-modal';
import { Users, UserPlus, Zap, Bot, Trash2 } from 'lucide-react';

export default function ChatListView({ activeTopic, onSelectTopic, user, onlineUsers }) {
  const [chats, setChats] = useState([]);
  const [friends, setFriends] = useState([]);
  const [groups, setGroups] = useState([]);
  const [pending, setPending] = useState([]);
  const [bots, setBots] = useState([]);
  const [search, setSearch] = useState('');
  const [deletingTopicId, setDeletingTopicId] = useState('');
  const [showCreateGroup, setShowCreateGroup] = useState(false);
  const [showAddFriend, setShowAddFriend] = useState(false);
  const [showAgentStore, setShowAgentStore] = useState(false);

  const loadAll = async () => {
    try {
      const [resC, resF, resG, resP, resB] = await Promise.all([
        api.getConversations().catch(()=>({})),
        api.getFriends().catch(()=>({})),
        api.getGroups().catch(()=>({})),
        api.getPendingRequests().catch(()=>({})),
        api.getAgents().catch(()=>({}))
      ]);
      const conversations = (resC.conversations || []).map((item) => ({
        id: item.id,
        friendId: item.friend_id,
        groupId: item.group_id,
        name: item.name,
        preview: item.preview || '',
        time: item.last_time ? formatTime(new Date(item.last_time)) : '',
        isGroup: item.is_group,
        avatar_url: item.avatar_url,
        isBot: item.is_bot,
        isOnline: item.is_online,
        seq: item.latest_seq || 0,
      }));
      setChats(conversations);
      setFriends(resF.friends || []);
      setGroups(resG.groups || []);
      setPending(resP.requests || []);
      setBots(resB.agents || resB.bots || []);
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

  const handleGroupCreated = () => loadAll();
  const handleAccept = async (userId) => { await api.acceptFriend(userId); loadAll(); };
  const handleReject = async (userId) => { await api.rejectFriend(userId); loadAll(); };
  const handleOpenAgent = async (bot) => {
    if (!bot?.can_chat) return;
    try {
      const result = await api.openAgent(bot.id);
      const topicId = result.topic_id || bot.topic_id || p2pTopicId(user.uid, bot.id);
      onSelectTopic({
        topicId,
        name: bot.display_name || bot.username,
        isGroup: false,
        avatar_url: bot.avatar_url,
        friendId: bot.id,
        catscoIdentity: result.catsco_identity,
      });
    } catch (err) {
      window.alert(err.message || 'Failed to open agent.');
    }
  };
  const handleAcceptAgentInvite = async (event, bot) => {
    event.stopPropagation();
    try {
      await api.acceptAgentInvite(bot.id);
      await loadAll();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (err) {
      window.alert(err.message || 'Failed to accept invite.');
    }
  };
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

  const lowerSearch = search.toLowerCase();
  const filteredChats = chats.filter(c => c.name.toLowerCase().includes(lowerSearch));
  const filteredFriends = friends.filter(f => (f.display_name || f.username).toLowerCase().includes(lowerSearch));
  const filteredGroups = groups.filter(g => g.name.toLowerCase().includes(lowerSearch));
  const filteredBots = bots.filter(b => (b.display_name || b.username).toLowerCase().includes(lowerSearch));

  return (
    <>
      <div style={{padding: '12px 16px', borderBottom: '1px solid var(--v3-border)'}}>
        <input
          style={{width: '100%', background: 'rgba(255,255,255,0.05)', border: 'none', color: '#fff', padding: '8px 12px', borderRadius: '6px', outline: 'none', fontSize: '14px'}}
          placeholder="Search chats, groups, friends..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
        />
      </div>
      
      <div className="v3-chat-list">
        
        {!search && pending.length > 0 && (
          <div style={{ padding: '0 16px', marginBottom: 12 }}>
            <div style={{ fontSize: 11, fontWeight: 700, color: 'var(--v3-primary)', textTransform: 'uppercase', marginBottom: 8 }}>
              New Requests ({pending.length})
            </div>
            {pending.map((req) => (
              <FriendRequest key={req.id} request={req} onAccept={() => handleAccept(req.from_user_id)} onReject={() => handleReject(req.from_user_id)} />
            ))}
          </div>
        )}

        <div className="v3-chat-section" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center'}}>
          {search ? 'Matching Conversations' : 'Conversations'}
          {!search && (
            <div style={{display:'flex', gap: 12}}>
              <span style={{cursor:'pointer', display:'flex', alignItems:'center', color:'#888'}} onClick={()=>setShowCreateGroup(true)} title="Create Group"><Users size={16} /></span>
              <span style={{cursor:'pointer', display:'flex', alignItems:'center', color:'#888'}} onClick={()=>setShowAddFriend(true)} title="Add Friend"><UserPlus size={16} /></span>
            </div>
          )}
        </div>
        
        {filteredChats.length === 0 && !search ? (
           <div style={{ padding: 40, textAlign: 'center', color: '#888', fontSize: '13px' }}>{t('chats_empty')}</div>
        ) : (
          filteredChats.map((chat) => {
            const isOnline = !chat.isGroup && ((onlineUsers && onlineUsers[chat.friendId]) || chat.isOnline);
            const canDeleteGroup = chat.isGroup && groupOwnerById.get(String(chat.groupId)) === String(user.uid);
            return (
              <div
                key={chat.id}
                className={`v3-chat-item ${activeTopic === chat.id ? 'active' : ''}`}
                onClick={() => onSelectTopic({
                  topicId: chat.id,
                  name: chat.name,
                  isGroup: chat.isGroup,
                  groupId: chat.groupId,
                  avatar_url: chat.avatar_url,
                  friendId: chat.friendId,
                })}
              >
                <span className="prefix" style={{fontSize: '18px'}}>{chat.isGroup ? '#' : (isOnline ? '○' : '●')}</span>
                <span className="v3-chat-item-label">{chat.name}</span>
                {canDeleteGroup && (
                  <button
                    type="button"
                    className="v3-chat-item-delete"
                    disabled={deletingTopicId === chat.id}
                    onClick={(event) => {
                      event.stopPropagation();
                      handleDeleteGroup({
                        groupId: chat.groupId,
                        topicId: chat.id,
                        name: chat.name,
                      });
                    }}
                    title="Delete group"
                  >
                    <Trash2 size={14} />
                  </button>
                )}
                {!chat.isGroup && <span className={`v3-status-dot ${isOnline ? 'online' : 'offline'}`} style={{marginLeft: 'auto'}} />}
              </div>
            );
          })
        )}

        {(!search || filteredBots.length > 0) && (
          <>
            <div className="v3-chat-section" style={{display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 16}}>
              <div style={{display:'flex', alignItems:'center'}}><Zap size={14} fill="currentColor" style={{marginRight:6, color:'var(--v3-primary)'}} /> AI Apps</div>
              {!search && <span style={{cursor:'pointer', fontSize:14}} onClick={()=>setShowAgentStore(true)} title="Agent Store">＋</span>}
            </div>
            
            {filteredBots.length === 0 ? (
               <div style={{ padding: '20px', textAlign: 'center', color: '#888', fontSize: '13px' }}>Workspace has no agents.</div>
            ) : (
              filteredBots.map((bot) => {
                const topicId = bot.topic_id || p2pTopicId(user.uid, bot.id);
                const isOnline = Boolean((onlineUsers && onlineUsers[bot.id]) || bot.is_online);
                const statusLabel = agentStatusLabel(bot);
                const canChat = Boolean(bot.can_chat);
                return (
                  <div
                    key={bot.id}
                    className={`v3-chat-item ${activeTopic === topicId ? 'active' : ''}`}
                    onClick={() => handleOpenAgent(bot)}
                    style={{ opacity: canChat ? 1 : 0.72, cursor: canChat ? 'pointer' : 'default' }}
                  >
                    <span className="prefix" style={{display:'flex', alignItems:'center'}}><Bot size={18} /></span>
                    <span className="v3-chat-item-label">{bot.display_name || bot.username}</span>
                    {bot.access_status === 'pending_accept' ? (
                      <button
                        type="button"
                        className="oc-btn oc-btn-default"
                        style={{ marginLeft: 'auto', padding: '3px 8px', borderRadius: 6, fontSize: 11 }}
                        onClick={(event) => handleAcceptAgentInvite(event, bot)}
                      >
                        Accept
                      </button>
                    ) : (
                      <span
                        style={{
                          marginLeft: 'auto',
                          fontSize: 11,
                          color: canChat ? 'var(--v3-primary)' : 'var(--v3-text-muted)',
                          whiteSpace: 'nowrap',
                        }}
                      >
                        {statusLabel}
                      </span>
                    )}
                    <span
                      className={`v3-status-dot ${isOnline ? 'online' : 'offline'}`}
                      style={{marginLeft: 8}}
                      title={isOnline ? 'Online' : 'Offline'}
                      aria-label={isOnline ? 'Online' : 'Offline'}
                    />
                  </div>
                );
              })
            )}
          </>
        )}

        {search && (filteredGroups.length > 0 || filteredFriends.length > 0) && (
          <>
            <div className="v3-chat-section" style={{marginTop: 16}}>Directory Matches</div>
            {filteredGroups.map(group => (
              <div key={`grp_${group.id}`} className="v3-chat-item" onClick={() => onSelectTopic({ topicId: `grp_${group.id}`, name: group.name, isGroup: true, groupId: group.id, avatar_url: group.avatar_url })}>
                <span className="prefix" style={{fontSize: '18px'}}>#</span>
                <span className="v3-chat-item-label">{group.name}</span>
                {String(group.owner_id) === String(user.uid) && (
                  <button
                    type="button"
                    className="v3-chat-item-delete"
                    disabled={deletingTopicId === `grp_${group.id}`}
                    onClick={(event) => {
                      event.stopPropagation();
                      handleDeleteGroup({
                        groupId: group.id,
                        topicId: `grp_${group.id}`,
                        name: group.name,
                      });
                    }}
                    title="Delete group"
                  >
                    <Trash2 size={14} />
                  </button>
                )}
              </div>
            ))}
            {filteredFriends.map(friend => (
              <div key={`p2p_${friend.id}`} className="v3-chat-item" onClick={() => onSelectTopic({ topicId: p2pTopicId(user.uid, friend.id), name: friend.display_name || friend.username, isGroup: false, avatar_url: friend.avatar_url, friendId: friend.id })}>
                <span className="prefix" style={{fontSize: '18px'}}>{friend.account_type === 'bot' ? '●' : '○'}</span>
                <span>{friend.display_name || friend.username}</span>
              </div>
            ))}
          </>
        )}

        {search && filteredChats.length === 0 && filteredGroups.length === 0 && filteredFriends.length === 0 && filteredBots.length === 0 && (
          <div style={{ padding: 40, textAlign: 'center', color: 'var(--v3-text-muted)', fontSize: '13px' }}>No matches found.</div>
        )}

      </div>

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

function agentStatusLabel(agent) {
  if (!agent) return '';
  if (agent.access_status === 'blocked') return 'Blocked';
  if (agent.access_status === 'pending_accept') return 'Pending';
  if (agent.access_status === 'revoked') return 'Unavailable';
  if (agent.can_manage) return 'Manage';
  if (agent.can_chat) return 'Ready';
  return 'Unavailable';
}

function formatTime(date) {
  const h = date.getHours().toString().padStart(2, '0');
  const m = date.getMinutes().toString().padStart(2, '0');
  return `${h}:${m}`;
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
