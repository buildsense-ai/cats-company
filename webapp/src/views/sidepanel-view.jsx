import React, { useState, useEffect } from 'react';
import { api, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import CreateGroup from '../widgets/create-group';
import AddFriend from '../widgets/add-friend';
import FriendRequest from '../widgets/friend-request';
import AgentStoreModal from '../widgets/agent-store-modal';
import { Users, UserPlus, Zap, Bot } from 'lucide-react';

export default function ChatListView({ activeTopic, onSelectTopic, user, onlineUsers }) {
  const [chats, setChats] = useState([]);
  const [friends, setFriends] = useState([]);
  const [groups, setGroups] = useState([]);
  const [pending, setPending] = useState([]);
  const [bots, setBots] = useState([]);
  const [search, setSearch] = useState('');
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
        api.getMyBots().catch(()=>({}))
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
      setBots(resB.bots || []);
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
    });
    return () => unsub();
  }, []);

  const handleGroupCreated = () => loadAll();
  const handleAccept = async (userId) => { await api.acceptFriend(userId); loadAll(); };
  const handleReject = async (userId) => { await api.rejectFriend(userId); loadAll(); };

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
                <span>{chat.name}</span>
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
                const topicId = p2pTopicId(user.uid, bot.id);
                return (
                  <div
                    key={bot.id}
                    className={`v3-chat-item ${activeTopic === topicId ? 'active' : ''}`}
                    onClick={() => onSelectTopic({
                      topicId,
                      name: bot.display_name || bot.username,
                      isGroup: false,
                      avatar_url: bot.avatar_url,
                      friendId: bot.id,
                    })}
                  >
                    <span className="prefix" style={{display:'flex', alignItems:'center'}}><Bot size={18} /></span>
                    <span>{bot.display_name || bot.username}</span>
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
                <span>{group.name}</span>
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
      {showAddFriend && <AddFriend onClose={() => setShowAddFriend(false)} onSent={() => loadAll()} />}
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
