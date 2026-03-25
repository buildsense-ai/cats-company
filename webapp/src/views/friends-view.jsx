import React, { useState, useEffect } from 'react';
import { api } from '../api';
import t from '../i18n';
import FriendRequest from '../widgets/friend-request';
import AddFriend from '../widgets/add-friend';
import CreateGroup from '../widgets/create-group';
import Avatar from '../widgets/avatar';

export default function FriendsView({ onSelectUser, user, onClose }) {
  const [friends, setFriends] = useState([]);
  const [groups, setGroups] = useState([]);
  const [pending, setPending] = useState([]);
  const [showAdd, setShowAdd] = useState(false);
  const [showPending, setShowPending] = useState(false);
  const [showCreateGroup, setShowCreateGroup] = useState(false);

  useEffect(() => {
    loadFriends();
    loadPending();
    loadGroups();
  }, []);

  useEffect(() => {
    const reload = () => {
      loadFriends();
      loadPending();
      loadGroups();
    };
    window.addEventListener('cc:data-changed', reload);
    return () => window.removeEventListener('cc:data-changed', reload);
  }, []);

  const loadFriends = async () => {
    try {
      const res = await api.getFriends();
      setFriends(res.friends || []);
    } catch (e) {
      console.error('load friends:', e);
    }
  };

  const loadPending = async () => {
    try {
      const res = await api.getPendingRequests();
      setPending(res.requests || []);
    } catch (e) {
      console.error('load pending:', e);
    }
  };

  const loadGroups = async () => {
    try {
      const res = await api.getGroups();
      setGroups(res.groups || []);
    } catch (e) {
      console.error('load groups:', e);
    }
  };

  const handleAccept = async (userId) => {
    await api.acceptFriend(userId);
    loadFriends();
    loadPending();
  };

  const handleReject = async (userId) => {
    await api.rejectFriend(userId);
    loadPending();
  };

  const handleGroupCreated = () => {
    loadGroups();
  };

  // Search filter
  const [search, setSearch] = useState('');
  const filteredFriends = friends.filter(f => (f.display_name || f.username).toLowerCase().includes(search.toLowerCase()));
  const filteredGroups = groups.filter(g => g.name.toLowerCase().includes(search.toLowerCase()));

  return (
    <div className="oc-modal-overlay" onClick={onClose} style={{ zIndex: 1000 }}>
      <div className="oc-modal v3-directory-modal" onClick={e => e.stopPropagation()} style={{ width: 600, maxWidth: '90vw', maxHeight: '85vh', display: 'flex', flexDirection: 'column', background: '#191B1F', borderRadius: 12, border: '1px solid var(--v3-border)', overflow: 'hidden' }}>
        
        {/* Header / Search */}
        <div style={{ padding: '24px 24px 16px', borderBottom: '1px solid var(--v3-border)' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
            <h2 style={{ margin: 0, fontSize: 18, color: '#fff', fontWeight: 600 }}>Directory & New Chat</h2>
            <button onClick={onClose} style={{ background: 'none', border: 'none', color: '#888', cursor: 'pointer', fontSize: 24, lineHeight: 1 }}>×</button>
          </div>
          <input 
            autoFocus
            type="text"
            placeholder="Search users or groups..." 
            value={search}
            onChange={e => setSearch(e.target.value)}
            style={{ width: '100%', background: 'rgba(255,255,255,0.06)', border: '1px solid rgba(255,255,255,0.1)', color: '#fff', padding: '12px 16px', borderRadius: 8, outline: 'none', fontSize: 15 }}
          />
        </div>

        {/* Content area */}
        <div style={{ flex: 1, overflowY: 'auto', padding: 24, display: 'flex', flexDirection: 'column', gap: 24 }}>
          
          {/* Quick Actions */}
          <div style={{ display: 'flex', gap: 12 }}>
            <button className="v3-btn-secondary" style={{ flex: 1, padding: '10px', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8 }} onClick={() => setShowCreateGroup(true)}>
              <span style={{ fontSize: 16 }}>🌍</span> Create New Group
            </button>
            <button className="v3-btn-secondary" style={{ flex: 1, padding: '10px', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 8 }} onClick={() => setShowAdd(true)}>
              <span style={{ fontSize: 16 }}>👤</span> Add Friend by ID
            </button>
          </div>

          {/* Pending Requests (Only shows if there are any) */}
          {pending.length > 0 && !search && (
            <div>
              <div style={{ fontSize: 12, fontWeight: 700, color: 'var(--v3-primary)', textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 12 }}>
                New Friend Requests ({pending.length})
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {pending.map((req) => (
                  <FriendRequest key={req.id} request={req} onAccept={() => handleAccept(req.from_user_id)} onReject={() => handleReject(req.from_user_id)} />
                ))}
              </div>
            </div>
          )}

          {/* Combined Directory */}
          <div>
            <div style={{ fontSize: 12, fontWeight: 700, color: 'var(--v3-text-muted)', textTransform: 'uppercase', letterSpacing: 0.5, marginBottom: 12 }}>
              Groups & Friends
            </div>
            
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {filteredGroups.map(group => (
                <div key={group.id} className="v3-dir-item" onClick={() => onSelectUser({ topicId: `grp_${group.id}`, name: group.name, isGroup: true, groupId: group.id, avatar_url: group.avatar_url })}>
                  <Avatar name={group.name} src={group.avatar_url} size={36} isGroup className="v3-avatar" style={{ marginRight: 12 }} />
                  <span style={{ color: '#E1E2E3', fontWeight: 500, fontSize: 15 }}>{group.name}</span>
                </div>
              ))}
              
              {filteredFriends.map(friend => (
                <div key={friend.id} className="v3-dir-item" onClick={() => onSelectUser({ topicId: p2pTopicId(user.uid, friend.id), name: friend.display_name || friend.username, isGroup: false, avatar_url: friend.avatar_url, friendId: friend.id })}>
                  <Avatar name={friend.display_name || friend.username} src={friend.avatar_url} size={36} isBot={friend.account_type === 'bot'} className={`v3-avatar ${friend.account_type === 'bot' ? 'bot' : ''}`} style={{ marginRight: 12 }} />
                  <span style={{ color: '#E1E2E3', fontWeight: 500, fontSize: 15 }}>{friend.display_name || friend.username}</span>
                </div>
              ))}

              {filteredGroups.length === 0 && filteredFriends.length === 0 && (
                <div style={{ padding: 20, textAlign: 'center', color: '#666', fontSize: 14 }}>
                  No matches found.
                </div>
              )}
            </div>
          </div>
        </div>

      </div>

      {showAdd && <AddFriend onClose={() => setShowAdd(false)} onSent={loadPending} />}
      {showCreateGroup && <CreateGroup onClose={() => setShowCreateGroup(false)} onCreated={handleGroupCreated} />}
    </div>
  );
}

function p2pTopicId(uid1, uid2) {
  if (uid1 > uid2) [uid1, uid2] = [uid2, uid1];
  return `p2p_${uid1}_${uid2}`;
}
