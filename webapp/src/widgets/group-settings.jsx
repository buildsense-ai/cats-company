import React, { useEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import t from '../i18n';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import Avatar from './avatar';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';
import { UserPlus, X } from 'lucide-react';

export default function GroupSettings({ groupId, currentUser, onClose, onSaved }) {
  const feedback = useFeedback();
  const fileInputRef = useRef(null);
  const nameInputRef = useRef(null);
  const nameBeforeEditRef = useRef('');
  const [group, setGroup] = useState(null);
  const [members, setMembers] = useState([]);
  const [inviteRequests, setInviteRequests] = useState([]);
  const [friends, setFriends] = useState([]);
  const [name, setName] = useState('');
  const [editingName, setEditingName] = useState(false);
  const [avatarUrl, setAvatarUrl] = useState('');
  const [announcement, setAnnouncement] = useState('');
  const [selected, setSelected] = useState(new Set());
  const [showInvite, setShowInvite] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');

  const currentUserId = currentUser?.uid || currentUser?.id || 0;
  const currentMember = useMemo(
    () => members.find((member) => member.user_id === currentUserId) || null,
    [currentUserId, members],
  );
  const currentRole = currentMember?.role || '';
  const canEditGroup = currentRole === 'owner' || currentRole === 'admin';
  const canInviteMembers = Boolean(currentRole);

  useEffect(() => {
    loadData();
  }, [groupId]);

  useEffect(() => {
    if (!editingName) return;
    nameInputRef.current?.focus();
    nameInputRef.current?.select();
  }, [editingName]);

  const applyGroupInfo = (groupRes) => {
    const nextGroup = groupRes.group || null;
    setGroup(nextGroup);
    setMembers(groupRes.members || []);
    setInviteRequests(groupRes.invite_requests || []);
    setName(nextGroup?.name || '');
    setEditingName(false);
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
      setNotice('');
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
    const pendingInviteeIds = new Set(inviteRequests.map((request) => request.invitee_id));
    return friends.filter((friend) => !memberIds.has(friend.id) && !pendingInviteeIds.has(friend.id));
  }, [friends, inviteRequests, members]);

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

  const beginNameEdit = () => {
    if (!canEditGroup) return;
    nameBeforeEditRef.current = name;
    setEditingName(true);
  };

  const handleNameKeyDown = (event) => {
    if (event.key === 'Enter') {
      event.preventDefault();
      setEditingName(false);
      return;
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      setName(nameBeforeEditRef.current);
      setEditingName(false);
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
    setNotice('');
    try {
      if (canEditGroup && group && (group.name !== name.trim() || (group.avatar_url || '') !== (avatarUrl || ''))) {
        await api.updateGroup(groupId, name.trim(), avatarUrl || '');
      }
      if (canEditGroup && (group?.announcement || '') !== announcement.trim()) {
        await api.setGroupAnnouncement(groupId, announcement.trim());
      }
      let inviteResult = null;
      if (selected.size > 0) {
        inviteResult = await api.inviteToGroup(groupId, Array.from(selected));
      }
      const refreshedGroup = await refreshGroupInfo();
      if (onSaved) onSaved(refreshedGroup);
      if (!canEditGroup && inviteResult) {
        setShowInvite(false);
        setNotice(inviteResult.requested > 0
          ? `已提交 ${inviteResult.requested} 项邀请申请，等待群主或管理员审批。`
          : '没有新增邀请申请，对方可能已在群内或申请正在等待审批。');
        return;
      }
      onClose();
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  const runMemberAction = async (action, successMessage = '') => {
    setSaving(true);
    setError('');
    setNotice('');
    try {
      await action();
      const refreshedGroup = await refreshGroupInfo();
      if (onSaved) onSaved(refreshedGroup);
      if (successMessage) feedback.notify({ tone: 'success', message: successMessage });
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  const handleRoleChange = async (member) => {
    const nextRole = member.role === 'admin' ? 'member' : 'admin';
    const displayName = member.display_name || member.username;
    const confirmed = await feedback.confirm({
      title: '更改成员角色？',
      message: t('confirm_update_group_role', { name: displayName }),
      confirmLabel: '确认更改',
    });
    if (!confirmed) return;
    runMemberAction(
      () => api.updateMemberRole(groupId, member.user_id, nextRole),
      '成员角色已更新',
    );
  };

  const handleMuteToggle = (member) => {
    const action = member.muted
      ? () => api.unmuteMember(groupId, member.user_id)
      : () => api.muteMember(groupId, member.user_id);
    runMemberAction(action, member.muted ? '已取消成员禁言' : '成员已被禁言');
  };

  const handleKick = async (member) => {
    const displayName = member.display_name || member.username;
    const confirmed = await feedback.confirm({
      title: `移除成员“${displayName}”？`,
      message: t('confirm_kick_group_member', { name: displayName }),
      confirmLabel: '移除成员',
      tone: 'danger',
    });
    if (!confirmed) return;
    runMemberAction(() => api.kickMember(groupId, member.user_id), '成员已移除');
  };

  const handleInviteRequest = (request, action) => {
    runMemberAction(() => api.resolveGroupInviteRequest(groupId, request.id, action));
  };

  const handleLeave = async () => {
    const confirmed = await feedback.confirm({
      title: '退出群聊？',
      message: t('confirm_leave_group'),
      confirmLabel: '退出群聊',
      tone: 'danger',
    });
    if (!confirmed) return;
    setSaving(true);
    setError('');
    try {
      await api.leaveGroup(groupId);
      if (onSaved) onSaved(null);
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '已退出群聊' });
      onClose();
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  const handleDisband = async () => {
    const confirmed = await feedback.confirm({
      title: '解散群聊？',
      message: t('confirm_disband_group'),
      confirmLabel: '永久解散',
      tone: 'danger',
    });
    if (!confirmed) return;
    setSaving(true);
    setError('');
    try {
      await api.disbandGroup(groupId);
      if (onSaved) onSaved(null);
      window.dispatchEvent(new Event('cc:data-changed'));
      feedback.notify({ tone: 'success', message: '群聊已解散' });
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
        <header className="oc-group-settings-header">
          <h2>{t('group_settings')}</h2>
          <button type="button" className="oc-modal-close" onClick={onClose} aria-label="关闭">
            <X size={18} strokeWidth={1.8} />
          </button>
        </header>
        <div className="oc-group-settings-body">
          <section className="oc-group-summary">
            <Avatar name={name || group?.name || t('contacts_groups')} src={avatarUrl} size={48} isGroup />
            <div className="oc-group-summary-copy">
              {canEditGroup && editingName ? (
                <input
                  ref={nameInputRef}
                  className="oc-group-name-input"
                  aria-label="群聊名称"
                  placeholder={t('group_name_placeholder')}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  onBlur={() => setEditingName(false)}
                  onKeyDown={handleNameKeyDown}
                />
              ) : canEditGroup ? (
                <button
                  type="button"
                  className="oc-group-name-display"
                  aria-label="编辑群聊名称"
                  onClick={beginNameEdit}
                >
                  {name.trim() || t('group_name_placeholder')}
                </button>
              ) : (
                <div className="oc-group-name-display is-readonly">
                  {name.trim() || t('group_name_placeholder')}
                </div>
              )}
              <span className="oc-group-member-count">{members.length} 位成员</span>
            </div>
            {canEditGroup && (
              <button type="button" className="oc-btn oc-btn-default oc-group-avatar-button" onClick={() => fileInputRef.current?.click()}>
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
          </section>

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
            <div className="oc-settings-section-head">
              <div>
                <div className="oc-settings-section-title">{t('group_members')}</div>
                <div className="oc-settings-secondary">查看和管理当前成员</div>
              </div>
              {canInviteMembers && (
                <button type="button" className="oc-btn oc-btn-default oc-invite-members-button" onClick={() => setShowInvite((value) => !value)}>
                  <UserPlus size={15} strokeWidth={1.8} />
                  邀请成员
                </button>
              )}
            </div>
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

          {canEditGroup && inviteRequests.length > 0 && (
            <div className="oc-settings-section oc-invite-requests-section">
              <div className="oc-settings-section-head">
                <div>
                  <div className="oc-settings-section-title">待审批邀请</div>
                  <div className="oc-settings-secondary">普通成员提交的邀请需要群主或管理员确认</div>
                </div>
                <span className="oc-selection-count">{inviteRequests.length} 项</span>
              </div>
              <div className="oc-settings-list">
                {inviteRequests.map((request) => (
                  <div key={request.id} className="oc-settings-list-item oc-settings-member-item">
                    <Avatar
                      name={request.invitee_display_name || request.invitee_username}
                      src={request.invitee_avatar_url}
                      size={32}
                      isBot={request.invitee_is_bot}
                    />
                    <div className="oc-settings-list-text">
                      <div>{request.invitee_display_name || request.invitee_username}</div>
                      <div className="oc-settings-secondary">
                        由 {request.inviter_display_name || request.inviter_username || `成员 ${request.inviter_id}`} 提议邀请
                      </div>
                    </div>
                    <div className="oc-settings-member-actions">
                      <button
                        type="button"
                        className="oc-btn oc-btn-primary oc-settings-small-btn"
                        disabled={saving}
                        onClick={() => handleInviteRequest(request, 'approve')}
                      >
                        同意
                      </button>
                      <button
                        type="button"
                        className="oc-btn oc-btn-default oc-settings-small-btn"
                        disabled={saving}
                        onClick={() => handleInviteRequest(request, 'reject')}
                      >
                        拒绝
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {canInviteMembers && showInvite && (
            <div className="oc-settings-section">
              <div className="oc-settings-section-head">
                <div>
                  <div className="oc-settings-section-title">{t('group_add_members')}</div>
                  <div className="oc-settings-secondary">
                    {canEditGroup ? '选择后将直接加入群聊' : '选择后提交申请，由群主或管理员审批'}
                  </div>
                </div>
                {selected.size > 0 && <span className="oc-selection-count">已选 {selected.size}</span>}
              </div>
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

          {error && <InlineFeedback tone="error" className="oc-form-error">{error}</InlineFeedback>}
          {notice && <InlineFeedback tone="success" className="oc-form-notice">{notice}</InlineFeedback>}
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
            {(canEditGroup || selected.size > 0) && (
              <button className="oc-btn oc-btn-primary" onClick={handleSave} disabled={saving}>
                {saving ? t('loading') : canEditGroup ? t('save') : '提交邀请申请'}
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
