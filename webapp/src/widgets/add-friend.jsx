import React, { useEffect, useState } from 'react';
import { X } from 'lucide-react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';
import CustomSelect from './custom-select';
import FriendRequest from './friend-request';

const FRIEND_SEARCH_MODES = [
  { value: 'name', label: '按名字' },
  { value: 'uid', label: '按 UID' },
];
function FriendSearchModeSelect({ value, onValueChange }) {
  const selectedMode = FRIEND_SEARCH_MODES.find((option) => option.value === value)
    || FRIEND_SEARCH_MODES[0];
  return (
    <CustomSelect
      ariaLabel={`搜索方式：${selectedMode.label}`}
      className="oc-friend-search-mode-select"
      density="compact"
      listboxAriaLabel="搜索方式"
      menuClassName="oc-friend-search-mode-menu"
      optionClassName="oc-friend-search-mode-option"
      triggerClassName="oc-friend-search-mode-trigger"
      value={value}
      onValueChange={onValueChange}
    >
      {FRIEND_SEARCH_MODES.map((option) => (
        <option key={option.value} value={option.value}>{option.label}</option>
      ))}
    </CustomSelect>
  );
}

export default function AddFriend({ currentUser, onClose, onSent }) {
  const [query, setQuery] = useState('');
  const [searchMode, setSearchMode] = useState('name');
  const [message, setMessage] = useState(() => defaultFriendMessage(currentUser));
  const [results, setResults] = useState([]);
  const [pending, setPending] = useState([]);
  const [sent, setSent] = useState(new Set());
  const [loading, setLoading] = useState(false);
  const [pendingLoading, setPendingLoading] = useState(true);
  const [error, setError] = useState('');

  useEffect(() => {
    loadPending();
  }, []);

  const loadPending = async () => {
    setPendingLoading(true);
    try {
      const res = await api.getPendingRequests();
      setPending(res.requests || []);
    } catch (e) {
      console.error('load pending friend requests:', e);
    } finally {
      setPendingLoading(false);
    }
  };

  const handleSearch = async () => {
    const trimmedQuery = query.trim();
    if (searchMode === 'uid' && !/^\d+$/.test(trimmedQuery)) {
      setError('请输入数字 UID');
      return;
    }
    if (searchMode === 'name' && trimmedQuery.length < 2) {
      setError(t('friend_search_too_short'));
      return;
    }

    setLoading(true);
    setError('');
    try {
      const res = await api.searchUsers(trimmedQuery, searchMode);
      setResults(res.users || []);
    } catch (e) {
      console.error('search:', e);
      setError(e.message || t('error_server'));
    } finally {
      setLoading(false);
    }
  };

  const handleSend = async (userId) => {
    setError('');
    try {
      await api.sendFriendRequest(userId, message.trim());
      setSent((prev) => new Set([...prev, userId]));
      if (onSent) onSent();
    } catch (e) {
      console.error('send request:', e);
      setError(e.message || t('error_server'));
    }
  };

  const handleAccept = async (userId) => {
    try {
      await api.acceptFriend(userId);
      await loadPending();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const handleReject = async (userId) => {
    try {
      await api.rejectFriend(userId);
      await loadPending();
    } catch (e) {
      setError(e.message || t('error_server'));
    }
  };

  const handleSearchModeChange = (nextMode) => {
    setSearchMode(nextMode);
    setResults([]);
    setError('');
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <section
        className="oc-modal oc-collaboration-modal oc-friend-manager-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="friend-manager-title"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="oc-collaboration-modal-header">
          <h2 id="friend-manager-title">好友</h2>
          <button type="button" className="oc-modal-close" onClick={onClose} aria-label="关闭">
            <X size={18} strokeWidth={1.8} aria-hidden="true" />
          </button>
        </header>

        <div className="oc-collaboration-modal-body">
          <section className="oc-collaboration-section">
            <div className="oc-collaboration-section-intro">
              <h3>添加好友</h3>
              <p>通过名字或 UID 查找联系人并发送申请。</p>
            </div>

            <div className="oc-friend-search-row">
              <div className="oc-friend-search-control">
                <input
                  autoFocus
                  className="oc-friend-search-input"
                  aria-label={searchMode === 'uid' ? '好友 UID' : '好友名称'}
                  name="friend-search"
                  placeholder={t('contacts_search_placeholder')}
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && handleSearch()}
                />
                <FriendSearchModeSelect
                  value={searchMode}
                  onValueChange={handleSearchModeChange}
                />
              </div>
              <button type="button" className="oc-btn oc-btn-primary oc-friend-search-submit" onClick={handleSearch}>
                {loading ? t('loading') : '搜索'}
              </button>
            </div>

            <label className="oc-collaboration-field">
              <span>验证消息</span>
              <textarea value={message} onChange={(e) => setMessage(e.target.value)} rows={3} />
            </label>

            {error && <div className="oc-form-error">{error}</div>}

            {results.length > 0 && (
              <div className="oc-collaboration-list oc-friend-search-results">
                {results.map((user) => (
                  <div key={user.id} className="oc-contact-item">
                    <Avatar
                      name={user.display_name || user.username}
                      src={user.avatar_url}
                      size={40}
                      isBot={user.account_type === 'bot'}
                      className="oc-contact-avatar"
                    />
                    <div className="oc-contact-info">
                      <span className="oc-contact-name">{user.display_name || user.username}</span>
                      <span className="oc-contact-identity">{userIdentity(user)}</span>
                    </div>
                    {sent.has(user.id) ? (
                      <span className="oc-request-sent">已发送</span>
                    ) : (
                      <button type="button" className="oc-btn oc-btn-default" onClick={() => handleSend(user.id)}>
                        发送申请
                      </button>
                    )}
                  </div>
                ))}
              </div>
            )}

            {results.length === 0 && canShowEmptyState(query, searchMode) && !loading && (
              <div className="oc-collaboration-empty">{t('no_data')}</div>
            )}
          </section>

          <section className="oc-collaboration-section oc-friend-requests-section">
            <div className="oc-collaboration-subhead">
              <div>
                <strong>好友申请</strong>
                <span>待处理的好友请求</span>
              </div>
              {!pendingLoading && pending.length > 0 && <span>{pending.length}</span>}
            </div>
            <div className="oc-collaboration-list">
              {pending.map((request) => (
                <FriendRequest
                  key={request.id || request.from_user_id}
                  request={request}
                  onAccept={() => handleAccept(request.from_user_id)}
                  onReject={() => handleReject(request.from_user_id)}
                />
              ))}
              {!pendingLoading && pending.length === 0 && (
                <div className="oc-collaboration-empty">
                  <strong>暂无好友申请</strong>
                  <span>新的申请会显示在这里</span>
                </div>
              )}
              {pendingLoading && <div className="oc-collaboration-empty">正在加载...</div>}
            </div>
          </section>
        </div>
      </section>
    </div>
  );
}

function defaultFriendMessage(user) {
  const name = user?.display_name || user?.username || '';
  return name ? t('friend_request_default_msg', { name }) : '你好，我想添加你为好友';
}

function canShowEmptyState(query, searchMode) {
  const trimmedQuery = query.trim();
  if (searchMode === 'uid') return /^\d+$/.test(trimmedQuery);
  return trimmedQuery.length >= 2;
}

function userIdentity(user) {
  const username = user?.username ? `@${user.username}` : '';
  const uid = user?.id || user?.uid ? `uid ${user.id || user.uid}` : '';
  return [username, uid].filter(Boolean).join(' · ');
}
