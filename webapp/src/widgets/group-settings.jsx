import React, { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';

export default function GroupSettings({ groupId, currentUser, onClose, onSaved }) {
  const fileInputRef = useRef(null);
  const [group, setGroup] = useState(null);
  const [members, setMembers] = useState([]);
  const [friends, setFriends] = useState([]);
  const [name, setName] = useState('');
  const [avatarUrl, setAvatarUrl] = useState('');
  const [announcement, setAnnouncement] = useState('');
  const [selected, setSelected] = useState(new Set());
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const currentUserId = currentUser?.uid || currentUser?.id || 0;
  const currentMember = useMemo(
    () => members.find((member) => member.user_id === currentUserId) || null,
    [currentUserId, members],
  );
  const currentRole = currentMember?.role || '';
  const canEditGroup = currentRole === 'owner' || currentRole === 'admin';

  useEffect(() => {
    loadData();
  }, [groupId]);

  const applyGroupInfo = (groupRes) => {
    const nextGroup = groupRes.group || null;
    setGroup(nextGroup);
    setMembers(groupRes.members || []);
    setName(nextGroup?.name || '');
    setAvatarUrl(nextGroup?.avatar_url || '');
    setAnnouncement(nextGroup?.announcement || '');
    return nextGroup;
  };

  const loadData = async () => {
    try {
      const [groupRes, friendsRes] = await Promise.all([
        api.getGroupInfo(groupId),
        api.getFriends(),
      ]);
      applyGroupInfo(groupRes);
      setFriends(friendsRes.friends || []);
      setSelected(new Set());
      setError('');
    } catch (err) {
      setError(err.message || t('error_server'));
    }
  };

  const refreshGroupInfo = async () => {
    const refreshed = await api.getGroupInfo(groupId);
    setSelected(new Set());
    return applyGroupInfo(refreshed);
  };

  const availableFriends = useMemo(() => {
    const memberIds = new Set(members.map((member) => member.user_id));
    return friends.filter((friend) => !memberIds.has(friend.id));
  }, [friends, members]);

  const toggleInvite = (userId) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(userId)) next.delete(userId);
      else next.add(userId);
      return next;
    });
  };

  const handleSelectAvatar = async (event) => {
    const file = event.target.files?.[0];
    if (!file) return;

    const validationError = validateImageUpload(file);
    if (validationError) {
      setError(validationError);
      event.target.value = '';
      return;
    }

    setError('');
    try {
      const uploaded = await api.uploadFile(file, 'image');
      setAvatarUrl(uploaded.url || '');
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      event.target.value = '';
    }
  };

  const handleSave = async () => {
    if (!canEditGroup && selected.size === 0) {
      onClose();
      return;
    }
    if (canEditGroup && !name.trim()) {
      setError(t('group_name_placeholder'));
      return;
    }
    setSaving(true);
    setError('');
    try {
      if (canEditGroup && group && (group.name !== name.trim() || (group.avatar_url || '') !== (avatarUrl || ''))) {
        await api.updateGroup(groupId, name.trim(), avatarUrl || '');
      }
      if (canEditGroup && (group?.announcement || '') !== announcement.trim()) {
        await api.setGroupAnnouncement(groupId, announcement.trim());
      }
      if (canEditGroup && selected.size > 0) {
        await api.inviteToGroup(groupId, Array.from(selected));
      }
      const refreshedGroup = await refreshGroupInfo();
      if (onSaved) onSaved(refreshedGroup);
      onClose();
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  const runMemberAction = async (action) => {
    setSaving(true);
    setError('');
    try {
      await action();
      const refreshedGroup = await refreshGroupInfo();
      if (onSaved) onSaved(refreshedGroup);
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  const handleRoleChange = (member) => {
    const nextRole = member.role === 'admin' ? 'member' : 'admin';
    const displayName = member.display_name || member.username;
    if (!window.confirm(t('confirm_update_group_role', { name: displayName }))) return;
    runMemberAction(() => api.updateMemberRole(groupId, member.user_id, nextRole));
  };

  const handleMuteToggle = (member) => {
    const action = member.muted
      ? () => api.unmuteMember(groupId, member.user_id)
      : () => api.muteMember(groupId, member.user_id);
    runMemberAction(action);
  };

  const handleKick = (member) => {
    const displayName = member.display_name || member.username;
    if (!window.confirm(t('confirm_kick_group_member', { name: displayName }))) return;
    runMemberAction(() => api.kickMember(groupId, member.user_id));
  };

  const handleLeave = async () => {
    if (!window.confirm(t('confirm_leave_group'))) return;
    setSaving(true);
    setError('');
    try {
      await api.leaveGroup(groupId);
      if (onSaved) onSaved(null);
      window.dispatchEvent(new Event('cc:data-changed'));
      onClose();
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  const handleDisband = async () => {
    if (!window.confirm(t('confirm_disband_group'))) return;
    setSaving(true);
    setError('');
    try {
      await api.disbandGroup(groupId);
      if (onSaved) onSaved(null);
      window.dispatchEvent(new Event('cc:data-changed'));
      onClose();
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  const canChangeRole = (member) => (
    currentRole === 'owner' &&
    member.user_id !== currentUserId &&
    member.role !== 'owner'
  );

  const canManageMember = (member) => {
    if (member.user_id === currentUserId || member.role === 'owner') return false;
    if (currentRole === 'owner') return true;
    return currentRole === 'admin' && member.role === 'member';
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal oc-modal-wide oc-group-settings-modal" onClick={(e) => e.stopPropagation()}>
        <div className="oc-modal-title">{t('group_settings')}</div>
        <div className="oc-group-settings-body">
          <div className="oc-settings-avatar-block">
            <Avatar name={name || group?.name || t('contacts_groups')} src={avatarUrl} size={88} isGroup />
            {canEditGroup && (
              <button className="oc-btn oc-btn-default" onClick={() => fileInputRef.current?.click()}>
                {t('group_avatar_pick')}
              </button>
            )}
            <input
              ref={fileInputRef}
              type="file"
              accept={IMAGE_UPLOAD_ACCEPT}
              style={{ display: 'none' }}
              onChange={handleSelectAvatar}
            />
          </div>
          <input
            className="oc-auth-input"
            placeholder={t('group_name_placeholder')}
            value={name}
            disabled={!canEditGroup}
            onChange={(e) => setName(e.target.value)}
          />

          <div className="oc-settings-section">
            <div className="oc-settings-section-title">{t('group_announcement')}</div>
            <textarea
              className="oc-auth-input oc-settings-textarea"
              placeholder={t('group_announcement_placeholder')}
              value={announcement}
              disabled={!canEditGroup}
              onChange={(e) => setAnnouncement(e.target.value)}
            />
          </div>

          <div className="oc-settings-section">
            <div className="oc-settings-section-title">{t('group_members')} ({members.length})</div>
            <div className="oc-settings-list">
              {members.map((member) => (
                <div key={member.user_id} className="oc-settings-list-item oc-settings-member-item">
                  <Avatar
                    name={member.display_name || member.username}
                    src={member.avatar_url}
                    size={32}
                    isBot={member.is_bot}
                  />
                  <div className="oc-settings-list-text">
                    <div>{member.display_name || member.username}</div>
                    <div className="oc-settings-secondary">
                      @{member.username} · {roleLabel(member.role)}
                      {member.muted ? ` · ${t('group_muted')}` : ''}
                    </div>
                  </div>
                  <div className="oc-settings-member-actions">
                    {canChangeRole(member) && (
                      <button
                        type="button"
                        className="oc-btn oc-btn-default oc-settings-small-btn"
                        disabled={saving}
                        onClick={() => handleRoleChange(member)}
                      >
                        {member.role === 'admin' ? t('group_demote_member') : t('group_set_admin')}
                      </button>
                    )}
                    {canManageMember(member) && (
                      <button
                        type="button"
                        className="oc-btn oc-btn-default oc-settings-small-btn"
                        disabled={saving}
                        onClick={() => handleMuteToggle(member)}
                      >
                        {member.muted ? t('group_unmute') : t('group_mute')}
                      </button>
                    )}
                    {canManageMember(member) && (
                      <button
                        type="button"
                        className="oc-btn oc-btn-danger oc-settings-small-btn"
                        disabled={saving}
                        onClick={() => handleKick(member)}
                      >
                        {t('group_kick')}
                      </button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>

          {canEditGroup && (
            <div className="oc-settings-section">
              <div className="oc-settings-section-title">{t('group_add_members')}</div>
              <div className="oc-settings-list">
                {availableFriends.length === 0 ? (
                  <div className="oc-settings-empty">{t('group_no_invitable_members')}</div>
                ) : availableFriends.map((friend) => (
                  <button
                    key={friend.id}
                    type="button"
                    className="oc-settings-list-item oc-settings-list-button"
                    onClick={() => toggleInvite(friend.id)}
                  >
                    <Avatar name={friend.display_name || friend.username} src={friend.avatar_url} size={32} isBot={friend.account_type === 'bot'} />
                    <div className="oc-settings-list-text">
                      <div>{friend.display_name || friend.username}</div>
                      <div className="oc-settings-secondary">@{friend.username}</div>
                    </div>
                    <div className="oc-settings-check">{selected.has(friend.id) ? '✓' : ''}</div>
                  </button>
                ))}
              </div>
            </div>
          )}

          {error && <div className="oc-form-error">{error}</div>}
        </div>
        <div className="oc-settings-actions oc-settings-actions-split">
          <div>
            {currentRole === 'owner' ? (
              <button className="oc-btn oc-btn-danger" onClick={handleDisband} disabled={saving}>{t('group_disband')}</button>
            ) : currentRole ? (
              <button className="oc-btn oc-btn-danger" onClick={handleLeave} disabled={saving}>{t('group_leave')}</button>
            ) : null}
          </div>
          <div className="oc-settings-inline-actions">
            <button className="oc-btn oc-btn-default" onClick={onClose}>{t('cancel')}</button>
            {canEditGroup && (
              <button className="oc-btn oc-btn-primary" onClick={handleSave} disabled={saving}>
                {saving ? t('loading') : t('save')}
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );
}

function roleLabel(role) {
  if (role === 'owner') return t('group_owner');
  if (role === 'admin') return t('group_admin');
  return t('group_member');
}
