import React, { useCallback, useEffect, useId, useRef, useState } from 'react';
import { Check, Copy, Link2, LoaderCircle, RefreshCw, RotateCcw, X } from 'lucide-react';

import { api } from '../api';
import { useFeedback } from '../components/feedback-system';
import t from '../i18n';
import { copyTextToClipboard } from '../utils/clipboard';

const EXPIRY_OPTIONS = [
  { seconds: 24 * 60 * 60, label: 'conversation_share_expiry_24h' },
  { seconds: 7 * 24 * 60 * 60, label: 'conversation_share_expiry_7d' },
  { seconds: 30 * 24 * 60 * 60, label: 'conversation_share_expiry_30d' },
];

function formatShareTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat('zh-CN', {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}

function shareStateLabel(share) {
  if (share?.state === 'revoked') return t('conversation_share_state_revoked');
  if (share?.state === 'expired') return t('conversation_share_state_expired');
  const expiresAt = formatShareTime(share?.expires_at);
  return expiresAt
    ? t('conversation_share_state_expires', { time: expiresAt })
    : t('conversation_share_state_active');
}

function normalizeManagedShares(value) {
  if (!Array.isArray(value)) return [];
  return value
    .filter((share) => share && typeof share === 'object' && share.id)
    .map((share) => ({
      id: String(share.id),
      title: String(share.title || t('conversation_share_default_title')),
      state: ['active', 'revoked', 'expired'].includes(share.state) ? share.state : 'expired',
      expires_at: share.expires_at,
    }));
}

export default function ConversationShareReview({ topicId, messageIds = [], mode = 'create', onClose, onComplete = onClose }) {
  const [title, setTitle] = useState(() => t('conversation_share_default_title'));
  const [expiresIn, setExpiresIn] = useState(EXPIRY_OPTIONS[1].seconds);
  const [status, setStatus] = useState('ready');
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState(false);
  const [managedShares, setManagedShares] = useState([]);
  const [manageStatus, setManageStatus] = useState('idle');
  const [manageError, setManageError] = useState('');
  const [revokingShareID, setRevokingShareID] = useState('');
  const panelHeadingID = useId();
  const panelHeadingRef = useRef(null);
  const titleInputRef = useRef(null);
  const focusBeforePanelRef = useRef(null);
  const selectedMessageIDs = Array.isArray(messageIds) ? messageIds : [];
  const { confirm } = useFeedback();

  useEffect(() => {
    focusBeforePanelRef.current = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    return () => {
      const previous = focusBeforePanelRef.current;
      if (previous?.isConnected) previous.focus({ preventScroll: true });
    };
  }, []);

  useEffect(() => {
    if (mode === 'manage' || status !== 'ready') {
      panelHeadingRef.current?.focus();
      return;
    }
    titleInputRef.current?.focus();
  }, [mode, status]);

  const loadManagedShares = useCallback(async () => {
    setManageStatus('loading');
    setManageError('');
    try {
      const response = await api.listConversationShares(topicId);
      setManagedShares(normalizeManagedShares(response?.shares));
      setManageStatus('ready');
    } catch (cause) {
      setManageError(cause?.message || t('conversation_share_load_error'));
      setManageStatus('error');
    }
  }, [topicId]);

  useEffect(() => {
    if (mode !== 'manage') return;
    void loadManagedShares();
  }, [loadManagedShares, mode]);

  const createShare = async (event) => {
    event.preventDefault();
    if (status === 'saving' || selectedMessageIDs.length === 0) return;
    setStatus('saving');
    setError('');
    try {
      const response = await api.createConversationShare({
        topicId,
        messageIds: selectedMessageIDs,
        title: title.trim() || t('conversation_share_default_title'),
        expiresIn: Number(expiresIn),
      });
      setResult(response);
      setStatus('created');
    } catch (cause) {
      setError(cause?.message || t('conversation_share_create_error'));
      setStatus('ready');
    }
  };

  const copyLink = async () => {
    if (!result?.url) return;
    try {
      await copyTextToClipboard(result.url);
      setCopied(true);
    } catch {
      setCopied(false);
      setError(t('conversation_share_copy_error'));
    }
  };

  const revokeCreatedShare = async () => {
    if (!result?.id || status === 'revoking') return;
    const confirmed = await confirm({
      title: t('conversation_share_revoke_confirm_title'),
      message: t('conversation_share_confirm_revoke'),
      confirmLabel: t('conversation_share_revoke_current'),
      cancelLabel: t('cancel'),
      tone: 'danger',
    });
    if (!confirmed) return;
    setStatus('revoking');
    setError('');
    try {
      await api.revokeConversationShare(result.id);
      setStatus('revoked');
    } catch (cause) {
      setError(cause?.message || t('conversation_share_revoke_error'));
      setStatus('created');
    }
  };

  const revokeManagedShare = async (shareID) => {
    if (!shareID || revokingShareID) return;
    const share = managedShares.find((candidate) => candidate.id === shareID);
    if (!share) return;
    const confirmed = await confirm({
      title: t('conversation_share_revoke_confirm_title'),
      message: t('conversation_share_confirm_revoke'),
      confirmLabel: t('conversation_share_revoke_button'),
      cancelLabel: t('cancel'),
      tone: 'danger',
    });
    if (!confirmed) return;
    setRevokingShareID(shareID);
    setManageError('');
    try {
      await api.revokeConversationShare(shareID);
      setManagedShares((current) => current.map((share) => (
        share.id === shareID ? { ...share, state: 'revoked' } : share
      )));
    } catch (cause) {
      setManageError(cause?.message || t('conversation_share_revoke_error'));
    } finally {
      setRevokingShareID('');
    }
  };

  if (mode === 'manage') {
    return (
      <section className="cc-conversation-link-share-review" aria-live="polite" aria-labelledby={panelHeadingID}>
        <div className="cc-conversation-link-share-heading">
          <div>
            <span className="cc-conversation-link-share-kicker"><Link2 size={15} /> {t('conversation_share_manage_kicker')}</span>
            <h2 id={panelHeadingID} ref={panelHeadingRef} tabIndex="-1">{t('conversation_share_manage_title')}</h2>
            <p>{t('conversation_share_manage_description')}</p>
          </div>
          <button type="button" className="cc-conversation-link-share-close" aria-label={t('conversation_share_close_panel')} onClick={onClose}>
            <X size={17} />
          </button>
        </div>
        {manageError && <p className="cc-conversation-link-share-error" role="alert">{manageError}</p>}
        {manageStatus === 'loading' && (
          <div className="cc-conversation-link-share-loading" role="status">
            <LoaderCircle className="is-spinning" size={16} /> {t('conversation_share_loading_links')}
          </div>
        )}
        {manageStatus === 'error' && (
          <div className="cc-conversation-link-share-actions">
            <button type="button" className="cc-conversation-share-secondary" onClick={() => void loadManagedShares()}>
              <RefreshCw size={16} /> {t('conversation_share_retry')}
            </button>
          </div>
        )}
        {manageStatus === 'ready' && (
          <div className="cc-conversation-link-share-list">
            {managedShares.length === 0 && (
              <p className="cc-conversation-link-share-empty">{t('conversation_share_empty_links')}</p>
            )}
            {managedShares.map((share) => (
              <article className="cc-conversation-link-share-item" key={share.id}>
                <div className="cc-conversation-link-share-item-copy">
                  <strong title={share.title}>{share.title}</strong>
                  <span className={`cc-conversation-link-share-state is-${share.state}`}>{shareStateLabel(share)}</span>
                </div>
                {share.state === 'active' && (
                  <button
                    type="button"
                    className="cc-conversation-share-danger"
                    aria-label={t('conversation_share_revoke_label', { title: share.title })}
                    disabled={revokingShareID === share.id}
                    onClick={() => void revokeManagedShare(share.id)}
                  >
                    {revokingShareID === share.id ? <LoaderCircle className="is-spinning" size={16} /> : <RotateCcw size={16} />}
                    {t('conversation_share_revoke_button')}
                  </button>
                )}
              </article>
            ))}
          </div>
        )}
      </section>
    );
  }

  if (status === 'created' || status === 'revoking') {
    return (
      <section className="cc-conversation-link-share-review" aria-live="polite" aria-labelledby={panelHeadingID}>
        <div className="cc-conversation-link-share-heading">
          <div>
            <span className="cc-conversation-link-share-kicker"><Check size={15} /> {t('conversation_share_created_kicker')}</span>
            <h2 id={panelHeadingID} ref={panelHeadingRef} tabIndex="-1">{t('conversation_share_created_title')}</h2>
            <p>{t('conversation_share_created_description', { count: result?.message_count || selectedMessageIDs.length })}</p>
          </div>
          <button type="button" className="cc-conversation-link-share-close" aria-label={t('conversation_share_close_panel')} onClick={onComplete}>
            <X size={17} />
          </button>
        </div>
        <div className="cc-conversation-link-share-url-row">
          <input aria-label={t('conversation_share_link_label')} value={result?.url || ''} readOnly onFocus={(event) => event.currentTarget.select()} />
          <button type="button" className="v3-action-btn" aria-label={copied ? t('conversation_share_copied_link') : t('conversation_share_copy_link')} onClick={copyLink}>
            {copied ? <Check size={17} /> : <Copy size={17} />}
          </button>
        </div>
        {error && <p className="cc-conversation-link-share-error" role="alert">{error}</p>}
        <div className="cc-conversation-link-share-actions">
          <button type="button" className="cc-conversation-share-secondary" onClick={onComplete}>{t('conversation_share_done')}</button>
          <button type="button" className="cc-conversation-share-danger" aria-label={t('conversation_share_revoke_current_aria')} onClick={revokeCreatedShare} disabled={status === 'revoking'}>
            {status === 'revoking' ? <LoaderCircle className="is-spinning" size={16} /> : <RotateCcw size={16} />}
            {t('conversation_share_revoke_current')}
          </button>
        </div>
      </section>
    );
  }

  if (status === 'revoked') {
    return (
      <section className="cc-conversation-link-share-review" aria-live="polite" aria-labelledby={panelHeadingID}>
        <div className="cc-conversation-link-share-heading">
          <div>
            <span className="cc-conversation-link-share-kicker"><Check size={15} /> {t('conversation_share_processed_kicker')}</span>
            <h2 id={panelHeadingID} ref={panelHeadingRef} tabIndex="-1">{t('conversation_share_processed_title')}</h2>
            <p>{t('conversation_share_processed_description')}</p>
          </div>
          <button type="button" className="cc-conversation-link-share-close" aria-label={t('conversation_share_close_panel')} onClick={onComplete}>
            <X size={17} />
          </button>
        </div>
        <div className="cc-conversation-link-share-actions">
          <button type="button" className="cc-conversation-share-secondary" onClick={onComplete}>{t('conversation_share_close')}</button>
        </div>
      </section>
    );
  }

  return (
    <section className="cc-conversation-link-share-review" aria-live="polite" aria-labelledby={panelHeadingID}>
      <div className="cc-conversation-link-share-heading">
        <div>
          <span className="cc-conversation-link-share-kicker"><Link2 size={15} /> {t('conversation_share_create_kicker')}</span>
          <h2 id={panelHeadingID} ref={panelHeadingRef} tabIndex="-1">{t('conversation_share_create_title')}</h2>
          <p>{t('conversation_share_create_description', { count: selectedMessageIDs.length })}</p>
        </div>
        <button type="button" className="cc-conversation-link-share-close" aria-label={t('conversation_share_close_panel')} onClick={onClose}>
          <X size={17} />
        </button>
      </div>
      <form className="cc-conversation-link-share-form" onSubmit={createShare}>
        <label>
          <span>{t('conversation_share_guest_title')}</span>
          <input ref={titleInputRef} value={title} maxLength={80} onChange={(event) => setTitle(event.target.value)} />
        </label>
        <label>
          <span>{t('conversation_share_expiry_label')}</span>
          <select value={expiresIn} onChange={(event) => setExpiresIn(event.target.value)}>
            {EXPIRY_OPTIONS.map((option) => (
              <option key={option.seconds} value={option.seconds}>{t(option.label)}</option>
            ))}
          </select>
        </label>
        {error && <p className="cc-conversation-link-share-error" role="alert">{error}</p>}
        <div className="cc-conversation-link-share-actions">
          <button type="button" className="cc-conversation-share-secondary" onClick={onClose}>{t('conversation_share_back_to_selection')}</button>
          <button type="submit" className="cc-conversation-share-primary" disabled={status === 'saving'}>
            {status === 'saving' ? <LoaderCircle className="is-spinning" size={16} /> : <Link2 size={16} />}
            {t('conversation_share_create_button')}
          </button>
        </div>
      </form>
    </section>
  );
}
