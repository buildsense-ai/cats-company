import React, { useState, useEffect } from 'react';
import { UserPlus, UsersRound, X } from 'lucide-react';
import { api } from '../api';
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

  const handleGroupCreated = (created) => {
    const group = normalizeCreatedGroup(created);
    if (group) {
      setGroups((prev) => [group, ...prev.filter((item) => String(item.id) !== String(group.id))]);
    }
    window.dispatchEvent(new Event('cc:data-changed'));
  };

  // Search filter
  const [search, setSearch] = useState('');
  const filteredFriends = friends.filter(f => userSearchText(f).includes(search.toLowerCase()));
  const filteredGroups = groups.filter(g => g.name.toLowerCase().includes(search.toLowerCase()));

  return (
    <div className="oc-modal-overlay cc-directory-overlay" onClick={onClose}>
      <section
        className="oc-modal v3-directory-modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="directory-dialog-title"
        onClick={e => e.stopPropagation()}
      >
        {/* Header / Search */}
        <header className="cc-directory-header">
          <div className="cc-directory-title-row">
            <h2 id="directory-dialog-title">Directory &amp; New Chat</h2>
            <button type="button" className="cc-directory-close" onClick={onClose} aria-label="关闭">
              <X size={18} strokeWidth={1.8} aria-hidden="true" />
            </button>
          </div>
          <input
            className="cc-directory-search"
            autoFocus
            type="text"
            aria-label="搜索联系人或群组"
            placeholder="Search users or groups..." 
            value={search}
            onChange={e => setSearch(e.target.value)}
          />
        </header>

        {/* Content area */}
        <div className="cc-directory-content">
          {/* Quick Actions */}
          <div className="cc-directory-actions">
            <button type="button" className="v3-btn-secondary" onClick={() => setShowCreateGroup(true)}>
              <UsersRound size={17} strokeWidth={1.8} aria-hidden="true" /> Create New Group
            </button>
            <button type="button" className="v3-btn-secondary" onClick={() => setShowAdd(true)}>
              <UserPlus size={17} strokeWidth={1.8} aria-hidden="true" /> Add Friend by ID
            </button>
          </div>

          {/* Pending Requests (Only shows if there are any) */}
          {pending.length > 0 && !search && (
            <section className="cc-directory-section">
              <h3 className="cc-directory-section-title is-accent">
                New Friend Requests ({pending.length})
              </h3>
              <div className="cc-directory-request-list">
                {pending.map((req) => (
                  <FriendRequest key={req.id} request={req} onAccept={() => handleAccept(req.from_user_id)} onReject={() => handleReject(req.from_user_id)} />
                ))}
              </div>
            </section>
          )}

          {/* Combined Directory */}
          <section className="cc-directory-section">
            <h3 className="cc-directory-section-title">
              Groups & Friends
            </h3>
            
            <div className="cc-directory-list">
              {filteredGroups.map(group => (
                <button type="button" key={group.id} className="v3-dir-item" onClick={() => onSelectUser({ topicId: `grp_${group.id}`, name: group.name, isGroup: true, groupId: group.id, avatar_url: group.avatar_url })}>
                  <Avatar name={group.name} src={group.avatar_url} size={36} isGroup className="v3-avatar" />
                  <span className="cc-directory-name">{group.name}</span>
                </button>
              ))}
              
              {filteredFriends.map(friend => (
                <button type="button" key={friend.id} className="v3-dir-item" onClick={() => onSelectUser({ topicId: p2pTopicId(user.uid, friend.id), name: friend.display_name || friend.username, isGroup: false, avatar_url: friend.avatar_url, friendId: friend.id })}>
                  <Avatar name={friend.display_name || friend.username} src={friend.avatar_url} size={36} isBot={friend.account_type === 'bot'} className={`v3-avatar ${friend.account_type === 'bot' ? 'bot' : ''}`} />
                  <span className="cc-directory-person">
                    <span className="cc-directory-name">{friend.display_name || friend.username}</span>
                    <span className="cc-directory-identity">{userIdentity(friend)}</span>
                  </span>
                </button>
              ))}

              {filteredGroups.length === 0 && filteredFriends.length === 0 && (
                <div className="cc-directory-empty">
                  No matches found.
                </div>
              )}
            </div>
          </section>
        </div>
      </section>

      {showAdd && <AddFriend currentUser={user} onClose={() => setShowAdd(false)} onSent={loadPending} />}
      {showCreateGroup && <CreateGroup onClose={() => setShowCreateGroup(false)} onCreated={handleGroupCreated} />}
    </div>
  );
}

function p2pTopicId(uid1, uid2) {
  if (uid1 > uid2) [uid1, uid2] = [uid2, uid1];
  return `p2p_${uid1}_${uid2}`;
}

function userIdentity(user) {
  const username = user?.username ? `@${user.username}` : '';
  const uid = user?.id || user?.uid ? `uid ${user.id || user.uid}` : '';
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
    avatar_url: rawGroup.avatar_url || created.avatar_url || '',
    created_at: rawGroup.created_at || created.created_at || new Date().toISOString(),
  };
}
