import React, { useEffect, useMemo, useState } from 'react';
import { Search, X } from 'lucide-react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';

export default function CreateGroup({ onClose, onCreated }) {
  const [name, setName] = useState('');
  const [friends, setFriends] = useState([]);
  const [selected, setSelected] = useState(new Set());
  const [memberType, setMemberType] = useState('friends');
  const [query, setQuery] = useState('');
  const [loading, setLoading] = useState(false);
  const [loadingMembers, setLoadingMembers] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    loadFriends();
  }, []);

  const loadFriends = async () => {
    setLoadingMembers(true);
    try {
      const res = await api.getFriends();
      setFriends(res.friends || []);
    } catch (e) {
      console.error('load friends for group:', e);
      setError(e.message || t('error_server'));
    } finally {
      setLoadingMembers(false);
    }
  };

  const toggleMember = (id) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const handleCreate = async () => {
    if (!name.trim()) {
      setError('请输入群聊名称');
      return;
    }
    setLoading(true);
    setError('');
    try {
      const res = await api.createGroup(name.trim(), Array.from(selected));
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
    return friends.filter((friend) => {
      const isAgent = friend.bot || friend.account_type === 'bot';
      if ((memberType === 'agents') !== isAgent) return false;
      if (!normalizedQuery) return true;
      return [friend.display_name, friend.username, friend.id, friend.uid]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()
        .includes(normalizedQuery);
    });
  }, [friends, memberType, query]);

  const selectedMembers = useMemo(
    () => friends.filter((friend) => selected.has(friend.id)),
    [friends, selected],
  );

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <section
        className="oc-modal oc-collaboration-modal oc-create-group-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="create-group-title"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="oc-collaboration-modal-header">
          <h2 id="create-group-title">创建群聊</h2>
          <button type="button" className="oc-modal-close" onClick={onClose} aria-label="关闭">
            <X size={18} strokeWidth={1.8} />
          </button>
        </header>

        <div className="oc-collaboration-modal-body">
          <section className="oc-collaboration-section">
            <div className="oc-collaboration-section-intro">
              <div>
                <h3>群聊信息</h3>
                <p>设置名称，并从好友或 Agent 中选择成员。</p>
              </div>
            </div>
            <label className="oc-collaboration-field">
              <span>群聊名称</span>
              <input
                className="oc-collaboration-input"
                placeholder="#新的话题"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
          </section>

          <div className="oc-member-picker-shell">
            <section className="oc-member-picker-source">
              <label className="oc-member-search">
                <Search size={15} strokeWidth={1.8} />
                <input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索成员" />
              </label>
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
              <div className="oc-member-picker-list">
                {filteredMembers.map((friend) => {
                  const checked = selected.has(friend.id);
                  return (
                    <label key={friend.id} className={`oc-member-picker-item ${checked ? 'selected' : ''}`}>
                      <input type="checkbox" checked={checked} onChange={() => toggleMember(friend.id)} />
                      <Avatar
                        name={friend.display_name || friend.username}
                        src={friend.avatar_url}
                        size={34}
                        isBot={friend.bot || friend.account_type === 'bot'}
                        className="oc-contact-avatar"
                      />
                      <span>
                        <strong>{friend.display_name || friend.username}</strong>
                        <small>{friend.bot || friend.account_type === 'bot' ? 'Agent' : friend.description || '好友'}</small>
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
                <strong>已选成员</strong>
                <span>{selected.size} 人</span>
              </div>
              <div className="oc-member-picker-selected-list">
                {selectedMembers.map((friend) => (
                  <div key={friend.id} className="oc-member-picker-selected-item">
                    <Avatar
                      name={friend.display_name || friend.username}
                      src={friend.avatar_url}
                      size={34}
                      isBot={friend.bot || friend.account_type === 'bot'}
                      className="oc-contact-avatar"
                    />
                    <span>
                      <strong>{friend.display_name || friend.username}</strong>
                      <small>{friend.bot || friend.account_type === 'bot' ? 'Agent' : '好友'}</small>
                    </span>
                    <button type="button" onClick={() => toggleMember(friend.id)} aria-label={`移除 ${friend.display_name || friend.username}`}>
                      <X size={15} strokeWidth={1.8} />
                    </button>
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
          <button type="button" className="oc-btn oc-btn-primary" onClick={handleCreate} disabled={loading || !name.trim()}>
            {loading ? t('loading') : '创建'}
          </button>
        </footer>
      </section>
    </div>
  );
}
