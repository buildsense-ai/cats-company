import React, {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { Check, ChevronDown, X } from 'lucide-react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';
import FriendRequest from './friend-request';

const FRIEND_SEARCH_MODES = [
  { value: 'name', label: '按名字' },
  { value: 'uid', label: '按 UID' },
];
const FRIEND_SEARCH_MODE_VIEWPORT_GUTTER = 8;
const FRIEND_SEARCH_MODE_OPTION_HEIGHT = 42;

function FriendSearchModeSelect({ value, onValueChange }) {
  const triggerRef = useRef(null);
  const listboxRef = useRef(null);
  const measuredOpenListRef = useRef(false);
  const listboxID = useId();
  const selectedIndex = Math.max(
    0,
    FRIEND_SEARCH_MODES.findIndex((option) => option.value === value),
  );
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(selectedIndex);
  const [geometry, setGeometry] = useState(null);

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') return;

    const triggerRect = triggerRef.current.getBoundingClientRect();
    const listboxHeight = listboxRef.current?.scrollHeight
      || listboxRef.current?.getBoundingClientRect().height
      || FRIEND_SEARCH_MODES.length * FRIEND_SEARCH_MODE_OPTION_HEIGHT;
    const viewportWidth = window.innerWidth;
    const viewportHeight = window.innerHeight;
    const availableBelow = viewportHeight
      - triggerRect.bottom
      - FRIEND_SEARCH_MODE_VIEWPORT_GUTTER;
    const availableAbove = triggerRect.top - FRIEND_SEARCH_MODE_VIEWPORT_GUTTER;
    const placement = availableBelow < listboxHeight && availableAbove > availableBelow
      ? 'top'
      : 'bottom';
    const availableHeight = placement === 'top' ? availableAbove : availableBelow;
    const visibleHeight = Math.max(0, Math.min(listboxHeight, availableHeight));
    const width = Math.max(0, Math.min(
      triggerRect.width,
      viewportWidth - FRIEND_SEARCH_MODE_VIEWPORT_GUTTER * 2,
    ));
    const preferredLeft = Math.min(
      triggerRect.left,
      viewportWidth - FRIEND_SEARCH_MODE_VIEWPORT_GUTTER - width,
    );
    const left = Math.max(FRIEND_SEARCH_MODE_VIEWPORT_GUTTER, preferredLeft);
    const top = placement === 'top'
      ? Math.max(
        FRIEND_SEARCH_MODE_VIEWPORT_GUTTER,
        triggerRect.top - visibleHeight,
      )
      : triggerRect.bottom;

    setGeometry({
      left,
      maxHeight: visibleHeight,
      placement,
      top,
      triggerBottom: triggerRect.bottom,
      triggerLeft: triggerRect.left,
      triggerRight: triggerRect.right,
      triggerTop: triggerRect.top,
      width,
    });
  }, []);

  useLayoutEffect(() => {
    if (!open) return;
    updatePosition();
  }, [open, updatePosition]);

  useLayoutEffect(() => {
    if (!open || !geometry || measuredOpenListRef.current || !listboxRef.current) return;
    measuredOpenListRef.current = true;
    listboxRef.current.focus();
    updatePosition();
  }, [geometry, open, updatePosition]);

  useEffect(() => {
    if (!open) return undefined;

    const closeOnOutsidePointer = (event) => {
      if (
        !triggerRef.current?.contains(event.target)
        && !listboxRef.current?.contains(event.target)
      ) {
        setOpen(false);
      }
    };
    const reposition = () => updatePosition();

    document.addEventListener('pointerdown', closeOnOutsidePointer);
    window.addEventListener('resize', reposition);
    window.addEventListener('scroll', reposition, true);
    return () => {
      document.removeEventListener('pointerdown', closeOnOutsidePointer);
      window.removeEventListener('resize', reposition);
      window.removeEventListener('scroll', reposition, true);
    };
  }, [open, updatePosition]);

  const openList = (nextIndex = selectedIndex) => {
    measuredOpenListRef.current = false;
    setActiveIndex(nextIndex);
    setOpen(true);
  };

  const closeList = ({ restoreFocus = false } = {}) => {
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  };

  const chooseOption = (index) => {
    const option = FRIEND_SEARCH_MODES[index];
    if (!option) return;
    onValueChange(option.value);
    closeList({ restoreFocus: true });
  };

  const moveActiveOption = (direction) => {
    setActiveIndex((currentIndex) => (
      currentIndex + direction + FRIEND_SEARCH_MODES.length
    ) % FRIEND_SEARCH_MODES.length);
  };

  const closeAndMoveFocus = (backward) => {
    const trigger = triggerRef.current;
    const scope = trigger?.closest('[role="dialog"]') || document;
    const focusable = Array.from(scope.querySelectorAll([
      'a[href]',
      'button:not(:disabled)',
      'input:not(:disabled)',
      'select:not(:disabled)',
      'textarea:not(:disabled)',
      '[tabindex]:not([tabindex="-1"])',
    ].join(','))).filter((element) => (
      !element.hidden && element.getAttribute('aria-hidden') !== 'true'
    )).sort((left, right) => (
      left.compareDocumentPosition(right) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1
    ));
    const triggerIndex = focusable.indexOf(trigger);
    const target = triggerIndex >= 0
      ? focusable[triggerIndex + (backward ? -1 : 1)]
      : null;

    closeList();
    (target || trigger)?.focus();
  };

  const handleTriggerKeyDown = (event) => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const direction = event.key === 'ArrowDown' ? 1 : -1;
      openList((
        selectedIndex + direction + FRIEND_SEARCH_MODES.length
      ) % FRIEND_SEARCH_MODES.length);
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault();
      openList(event.key === 'Home' ? 0 : FRIEND_SEARCH_MODES.length - 1);
    }
  };

  const handleListKeyDown = (event) => {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      moveActiveOption(event.key === 'ArrowDown' ? 1 : -1);
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault();
      setActiveIndex(event.key === 'Home' ? 0 : FRIEND_SEARCH_MODES.length - 1);
    } else if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      chooseOption(activeIndex);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      closeList({ restoreFocus: true });
    } else if (event.key === 'Tab') {
      event.preventDefault();
      event.stopPropagation();
      closeAndMoveFocus(event.shiftKey);
    }
  };

  const selectedOption = FRIEND_SEARCH_MODES[selectedIndex];
  const listbox = open && geometry && (
    <div
      ref={listboxRef}
      id={listboxID}
      className={`oc-friend-search-mode-menu is-${geometry.placement}`}
      role="listbox"
      aria-label="搜索方式"
      aria-activedescendant={`${listboxID}-option-${activeIndex}`}
      data-placement={geometry.placement}
      data-trigger-bottom={geometry.triggerBottom}
      data-trigger-left={geometry.triggerLeft}
      data-trigger-right={geometry.triggerRight}
      data-trigger-top={geometry.triggerTop}
      tabIndex={-1}
      style={{
        left: `${geometry.left}px`,
        maxHeight: `${geometry.maxHeight}px`,
        overflowY: 'auto',
        position: 'fixed',
        top: `${geometry.top}px`,
        width: `${geometry.width}px`,
      }}
      onClick={(event) => event.stopPropagation()}
      onKeyDown={handleListKeyDown}
    >
      {FRIEND_SEARCH_MODES.map((option, index) => (
        <button
          type="button"
          id={`${listboxID}-option-${index}`}
          key={option.value}
          className={`oc-friend-search-mode-option ${index === activeIndex ? 'is-active' : ''}`}
          role="option"
          aria-selected={option.value === value}
          tabIndex={-1}
          onMouseEnter={() => setActiveIndex(index)}
          onClick={() => chooseOption(index)}
        >
          <span>{option.label}</span>
          {option.value === value && (
            <Check
              className="oc-friend-search-mode-option-check"
              size={14}
              aria-hidden="true"
            />
          )}
        </button>
      ))}
    </div>
  );

  return (
    <span className="oc-friend-search-mode-select">
      <button
        ref={triggerRef}
        type="button"
        className="oc-friend-search-mode oc-friend-search-mode-trigger"
        aria-label={`搜索方式：${selectedOption.label}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listboxID}
        onClick={() => (open ? closeList() : openList())}
        onKeyDown={handleTriggerKeyDown}
      >
        <span>{selectedOption.label}</span>
        <ChevronDown
          className="oc-friend-search-mode-chevron"
          size={14}
          aria-hidden="true"
        />
      </button>
      {typeof document !== 'undefined' && listbox
        ? createPortal(listbox, document.body)
        : null}
    </span>
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
                <FriendSearchModeSelect
                  value={searchMode}
                  onValueChange={handleSearchModeChange}
                />
                <input
                  autoFocus
                  className="oc-friend-search-input"
                  aria-label={searchMode === 'uid' ? '好友 UID' : '好友名称'}
                  name="friend-search"
                  placeholder={searchMode === 'uid' ? '输入对方 UID' : '搜索联系人'}
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && handleSearch()}
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
