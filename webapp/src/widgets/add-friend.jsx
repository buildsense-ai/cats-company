import React, { useEffect, useRef, useState } from 'react';
import { LoaderCircle, UserPlus, X } from 'lucide-react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';
import CustomSelect from './custom-select';
import FriendRequest from './friend-request';
import useDialogBehavior from '../utils/use-dialog-behavior';

const FRIEND_SEARCH_MODES = [
  { value: 'name', label: '按名字' },
  { value: 'uid', label: '按 UID' },
];
function FriendSearchModeSelect({ value, onValueChange }) {
  const selectedMode = FRIEND_SEARCH_MODES.find((option) => option.value === value)
    || FRIEND_SEARCH_MODES[0];
  return (
    <CustomSelect
      ariaLabel={`搜索模式：${selectedMode.label}`}
      className="oc-friend-search-mode-select"
      density="compact"
      listboxAriaLabel="搜索模式"
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

function botInviteErrorMessage(error) {
  const message = String(error?.message || '').trim();
  if (
    !message
    || /bot invite code is invalid or expired/i.test(message)
    || /invalid or unavailable bot invite code/i.test(message)
  ) {
    return '邀请码无效或已失效';
  }
  return message;
}

export default function AddFriend({ currentUser, onClose, onSent, initialFocus = 'search' }) {
  const [query, setQuery] = useState('');
  const [searchMode, setSearchMode] = useState('name');
  const [message, setMessage] = useState(() => defaultFriendMessage(currentUser));
  const [results, setResults] = useState([]);
  const [pending, setPending] = useState([]);
  const [sent, setSent] = useState(new Set());
  const [loading, setLoading] = useState(false);
  const [searchStatus, setSearchStatus] = useState('idle');
  const searchRequestRef = useRef(0);
  const searchingRef = useRef(false);
  const dialogRef = useRef(null);
  const searchInputRef = useRef(null);
  const inviteInputRef = useRef(null);
  const previousSearchModeRef = useRef(searchMode);
  const redeemingInviteRef = useRef(false);
  const [pendingLoading, setPendingLoading] = useState(true);
  const [error, setError] = useState('');
  const [inviteCode, setInviteCode] = useState('');
  const [redeemingInvite, setRedeemingInvite] = useState(false);
  const [inviteSuccess, setInviteSuccess] = useState(false);
  useDialogBehavior(dialogRef, { onClose, initialFocusRef: initialFocus === 'invite' ? inviteInputRef : searchInputRef });

  useEffect(() => {
    loadPending();
    return () => { searchRequestRef.current += 1; };
  }, []);

  useEffect(() => {
    if (previousSearchModeRef.current !== searchMode) searchInputRef.current?.focus();
    previousSearchModeRef.current = searchMode;
  }, [searchMode]);

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
    if (searchingRef.current) return;
    const trimmedQuery = query.trim();
    if (!trimmedQuery) return;
    if (searchMode === 'uid' && !/^\d+$/.test(trimmedQuery)) {
      setError('请输入数字 UID');
      return;
    }
    if (searchMode === 'name' && trimmedQuery.length < 2) {
      setError(t('friend_search_too_short'));
      return;
    }

    const requestId = ++searchRequestRef.current;
    searchingRef.current = true;
    setLoading(true);
    setSearchStatus('loading');
    setResults([]);
    setError('');
    try {
      const res = await api.searchUsers(trimmedQuery, searchMode);
      if (requestId !== searchRequestRef.current) return;
      setResults(res.users || []);
      setSearchStatus('complete');
    } catch (e) {
      if (requestId !== searchRequestRef.current) return;
      console.error('search:', e);
      setError(e.message || t('error_server'));
      setSearchStatus('error');
    } finally {
      if (requestId === searchRequestRef.current) {
        searchingRef.current = false;
        setLoading(false);
      }
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

  const handleRedeemInvite = async () => {
    const code = inviteCode.trim();
    if (!code || redeemingInviteRef.current) return;
    redeemingInviteRef.current = true;
    setRedeemingInvite(true);
    setInviteSuccess(false);
    setError('');
    try {
      await api.redeemBotInviteCode(code);
      setInviteCode('');
      setInviteSuccess(true);
      if (onSent) onSent();
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (e) {
      setError(botInviteErrorMessage(e));
    } finally {
      redeemingInviteRef.current = false;
      setRedeemingInvite(false);
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

  const resetSearch = () => {
    searchRequestRef.current += 1;
    searchingRef.current = false;
    setLoading(false);
    setSearchStatus('idle');
    setResults([]);
    setError('');
  };
  const handleSearchModeChange = (nextMode) => {
    resetSearch();
    setSearchMode(nextMode);
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <section
        ref={dialogRef}
        tabIndex={-1}
        className="oc-modal oc-collaboration-modal oc-friend-manager-dialog cc-secondary-interface"
        role="dialog"
        aria-modal="true"
        aria-labelledby="friend-manager-title"
        onClick={(e) => e.stopPropagation()}
      >
        <header className="oc-collaboration-modal-header">
          <h2 id="friend-manager-title">
            <span className="oc-collaboration-modal-title-icon" aria-hidden="true">
              <UserPlus size={22} strokeWidth={1.8} />
            </span>
            <span>添加好友/助手</span>
          </h2>
          <button type="button" className="oc-modal-close" onClick={onClose} aria-label="关闭">
            <X size={18} strokeWidth={1.8} aria-hidden="true" />
          </button>
        </header>

        <div className="oc-collaboration-modal-body">
          <section className="oc-collaboration-section">
            <div className="oc-collaboration-section-intro">
              <h3>搜索好友或助手</h3>
              <p>通过名字或 UID 查找好友和助手，也可使用助手邀请码添加。</p>
            </div>

            <div className="oc-friend-search-row">
              <div className="oc-friend-search-control">
                <input
                  ref={searchInputRef}
                  className="oc-friend-search-input"
                  data-cc-focus-group="true"
                  aria-label={searchMode === 'uid' ? '好友或助手 UID' : '好友或助手名字'}
                  name="friend-search"
                  placeholder={searchMode === 'uid' ? '输入 UID…' : '输入名字…'}
                  inputMode={searchMode === 'uid' ? 'numeric' : 'text'}
                  value={query}
                  onChange={(e) => { resetSearch(); setQuery(e.target.value); }}
                  onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && e.keyCode !== 229 && handleSearch()}
                />
                <FriendSearchModeSelect
                  value={searchMode}
                  onValueChange={handleSearchModeChange}
                />
              </div>
              <button type="button" className="oc-btn oc-btn-primary oc-friend-search-submit" onClick={handleSearch} disabled={loading || !query.trim()} aria-busy={loading} aria-label={loading ? '正在搜索' : '搜索'}>
                <span className={loading ? 'oc-friend-search-label is-hidden' : 'oc-friend-search-label'}>搜索</span>
                {loading && <LoaderCircle size={16} className="oc-friend-search-spinner" aria-hidden="true" />}
              </button>
            </div>

            <div className="oc-friend-search-row">
              <input
                ref={inviteInputRef}
                className="oc-friend-invite-input"
                aria-label="助手邀请码"
                placeholder="输入助手邀请码"
                value={inviteCode}
                onChange={(e) => { setInviteSuccess(false); setInviteCode(e.target.value.toUpperCase()); }}
                onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && e.keyCode !== 229 && handleRedeemInvite()}
              />
              <button type="button" className="oc-btn oc-btn-default" onClick={handleRedeemInvite} disabled={redeemingInvite || !inviteCode.trim()}>
                {redeemingInvite ? '添加中...' : '使用邀请码'}
              </button>
            </div>

            {inviteSuccess && <div className="oc-request-sent" role="status">已添加助手</div>}

            <label className="oc-collaboration-field">
              <span>申请验证消息</span>
              <textarea value={message} onChange={(e) => setMessage(e.target.value)} rows={3} aria-describedby="friend-request-message-hint" />
              <small id="friend-request-message-hint" className="oc-friend-message-hint">发送申请时附带，使用邀请码无需填写。</small>
            </label>

            {error && <div className="oc-form-error" role="alert">{error}</div>}

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
                      <div className="oc-friend-result-heading">
                        <span className="oc-contact-name">{user.display_name || user.username}</span>
                        {user.account_type === 'bot' && <span className="oc-friend-assistant-badge">助手</span>}
                      </div>
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

            {results.length === 0 && searchStatus === 'complete' && (
              <div className="oc-collaboration-empty" role="status">没有找到匹配的好友或助手，试试其他名字或 UID</div>
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

function userIdentity(user) {
  const username = user?.username ? `@${user.username}` : '';
  const uid = user?.id || user?.uid ? `uid ${user.id || user.uid}` : '';
  return [username, uid].filter(Boolean).join(' · ');
}
