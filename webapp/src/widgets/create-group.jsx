import React, { useEffect, useMemo, useState } from 'react';
import { Search, UsersRound, X } from 'lucide-react';
import { api } from '../api';
import t from '../i18n';
import { inviteMemberId, mergeInviteMemberCandidates } from '../utils/invite-member-candidates';
import Avatar from './avatar';

export default function CreateGroup({
  onClose,
  onCreated,
  onCreate,
  mode = 'create',
  initialName = '',
  lockedMemberIds = [],
}) {
  const isTaskUpgrade = mode === 'task_upgrade';
  const lockedMemberKeys = useMemo(
    () => new Set(lockedMemberIds.map((id) => String(id))),
    [lockedMemberIds],
  );
  const [name, setName] = useState(initialName);
  const [memberCandidates, setMemberCandidates] = useState({ friends: [], agents: [] });
  const [selected, setSelected] = useState(() => new Set(lockedMemberIds));
  const [memberType, setMemberType] = useState('friends');
  const [query, setQuery] = useState('');
  const [loading, setLoading] = useState(false);
  const [loadingMembers, setLoadingMembers] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    loadMembers();
  }, []);

  const loadMembers = async () => {
    setLoadingMembers(true);
    try {
      const [friendsRes, agentsRes] = await Promise.all([
        api.getFriends(),
        api.getAgents().catch((error) => {
          console.warn('load agents for group:', error);
          return { agents: [] };
        }),
      ]);
      setMemberCandidates(mergeInviteMemberCandidates(
        friendsRes.friends || [],
        agentsRes.agents || [],
      ));
    } catch (e) {
      console.error('load members for group:', e);
      setError(e.message || t('error_server'));
    } finally {
      setLoadingMembers(false);
    }
  };

  const toggleMember = (id) => {
    if (lockedMemberKeys.has(String(id))) return;
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleCreate = async () => {
    if (!name.trim()) {
      setError(isTaskUpgrade ? '请输入任务名称' : '请输入群聊名称');
      return;
    }
    const additionalMemberCount = [...selected]
      .filter((id) => !lockedMemberKeys.has(String(id)))
      .length;
    if (isTaskUpgrade && additionalMemberCount === 0) {
      setError('请至少选择一名新成员');
      return;
    }
    setLoading(true);
    setError('');
    try {
      const create = onCreate || ((nextName, memberIds) => api.createGroup(nextName, memberIds));
      const res = await create(name.trim(), Array.from(selected));
      if (onCreated) onCreated(res);
      onClose();
    } catch (e) {
      setError(e.message || t('error_server'));
    } finally {
      setLoading(false);
    }
  };

  const filteredMembers = useMemo(() => {
    const normalizedQuery = query.trim().toLowerCase();
    return memberCandidates[memberType].filter((member) => {
      if (!normalizedQuery) return true;
      return [member.display_name, member.username, member.id, member.uid]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
        .includes(normalizedQuery);
    });
  }, [memberCandidates, memberType, query]);

  const selectedMembers = useMemo(
    () => [...memberCandidates.friends, ...memberCandidates.agents]
      .filter((member) => selected.has(inviteMemberId(member))),
    [memberCandidates, selected],
  );

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <section
        className="oc-modal oc-collaboration-modal oc-create-group-dialog cc-secondary-interface"
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-group-title"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="oc-collaboration-modal-header">
          <h2 id="create-group-title">
            <span className="oc-collaboration-modal-title-icon" aria-hidden="true">
              <UsersRound size={22} strokeWidth={1.8} />
            </span>
            <span>{isTaskUpgrade ? '协作管理' : '创建群聊'}</span>
          </h2>
          <button type="button" className="oc-modal-close" onClick={onClose} aria-label="关闭">
            <X size={18} strokeWidth={1.8} />
          </button>
        </header>

        <div className="oc-collaboration-modal-body">
          <section className="oc-collaboration-section">
            <div className="oc-collaboration-section-intro">
              <div>
                <h3>{isTaskUpgrade ? '升级为协作任务' : '群聊信息'}</h3>
                <p>
                  {isTaskUpgrade
                    ? '添加新成员后创建协作任务；原单 Agent 会话仍可从联系人中打开。'
                    : '设置名称，并从好友或 Agent 中选择成员。'}
                </p>
              </div>
            </div>
            <label className="oc-collaboration-field">
              <span>{isTaskUpgrade ? '任务名称' : '群聊名称'}</span>
              <input
                className="oc-collaboration-input"
                placeholder={isTaskUpgrade ? '任务名称' : '#新的话题'}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
          </section>

          <div className="oc-member-picker-shell">
            <section className="oc-member-picker-source">
              <div className="oc-member-search">
                <Search size={15} strokeWidth={1.8} />
                <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索成员" />
                <div className="oc-segmented-control" role="tablist" aria-label="成员类型">
                  <button
                    type="button"
                    className={memberType === 'friends' ? 'active' : ''}
                    onClick={() => setMemberType('friends')}
                  >
                    好友
                  </button>
                  <button
                    type="button"
                    className={memberType === 'agents' ? 'active' : ''}
                    onClick={() => setMemberType('agents')}
                  >
                    Agent
                  </button>
                </div>
              </div>
              <div className="oc-member-picker-list">
                {filteredMembers.map((member) => {
                  const memberId = inviteMemberId(member);
                  const checked = selected.has(memberId);
                  const locked = lockedMemberKeys.has(String(memberId));
                  return (
                    <label key={memberId} className={`oc-member-picker-item ${checked ? 'selected' : ''} ${locked ? 'locked' : ''}`.trim()}>
                      <input type="checkbox" checked={checked} disabled={locked} onChange={() => toggleMember(memberId)} />
                      <Avatar
                        name={member.display_name || member.username}
                        src={member.avatar_url}
                        size={34}
                        isBot={member.isAgent}
                        className="oc-contact-avatar"
                      />
                      <span>
                        <strong>{member.display_name || member.username}</strong>
                        <small>{locked ? '当前任务成员' : member.isAgent ? 'Agent' : member.description || '好友'}</small>
                      </span>
                    </label>
                  );
                })}
                {!loadingMembers && filteredMembers.length === 0 && (
                  <div className="oc-collaboration-empty compact">
                    <strong>{query ? '没有匹配的成员' : memberType === 'agents' ? '暂无可邀请的 Agent' : '暂无可邀请的好友'}</strong>
                    <span>{query ? '尝试更换关键词' : '可用成员会显示在这里'}</span>
                  </div>
                )}
                {loadingMembers && <div className="oc-collaboration-empty">正在加载...</div>}
              </div>
            </section>

            <section className="oc-member-picker-selected">
              <div className="oc-member-picker-selected-head">
                <strong>{isTaskUpgrade ? '参与成员' : '已选成员'}</strong>
                <span>{selected.size} 人</span>
              </div>
              <div className="oc-member-picker-selected-list">
                {selectedMembers.map((member) => (
                  <div key={inviteMemberId(member)} className="oc-member-picker-selected-item">
                    <Avatar
                      name={member.display_name || member.username}
                      src={member.avatar_url}
                      size={34}
                      isBot={member.isAgent}
                      className="oc-contact-avatar"
                    />
                    <span>
                      <strong>{member.display_name || member.username}</strong>
                      <small>{lockedMemberKeys.has(String(inviteMemberId(member))) ? '当前任务成员' : member.isAgent ? 'Agent' : '好友'}</small>
                    </span>
                    {!lockedMemberKeys.has(String(inviteMemberId(member))) && (
                      <button type="button" onClick={() => toggleMember(inviteMemberId(member))} aria-label={`移除 ${member.display_name || member.username}`}>
                        <X size={15} strokeWidth={1.8} />
                      </button>
                    )}
                  </div>
                ))}
                {selectedMembers.length === 0 && (
                  <div className="oc-collaboration-empty compact">
                    <strong>尚未选择成员</strong>
                    <span>从左侧列表中添加</span>
                  </div>
                )}
              </div>
            </section>
          </div>

          {error && <div className="oc-form-error">{error}</div>}
        </div>

        <footer className="oc-collaboration-modal-footer">
          <button type="button" className="oc-btn oc-btn-default" onClick={onClose}>取消</button>
          <button
            type="button"
            className="oc-btn oc-btn-primary"
            onClick={handleCreate}
            disabled={loading || !name.trim() || (
              isTaskUpgrade
              && [...selected].every((id) => lockedMemberKeys.has(String(id)))
            )}
          >
            {loading ? t('loading') : isTaskUpgrade ? '升级并添加' : '创建'}
          </button>
        </footer>
      </section>
    </div>
  );
}
