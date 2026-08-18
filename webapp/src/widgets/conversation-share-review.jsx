import React, { useCallback, useEffect, useState } from 'react';
import { Check, Copy, Link2, LoaderCircle, RefreshCw, RotateCcw, X } from 'lucide-react';

import { api } from '../api';
import { copyTextToClipboard } from '../utils/clipboard';

const EXPIRY_OPTIONS = [
  { seconds: 24 * 60 * 60, label: '24 小时后失效' },
  { seconds: 7 * 24 * 60 * 60, label: '7 天后失效' },
  { seconds: 30 * 24 * 60 * 60, label: '30 天后失效' },
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
  if (share?.state === 'revoked') return '已撤销';
  if (share?.state === 'expired') return '已过期';
  const expiresAt = formatShareTime(share?.expires_at);
  return expiresAt ? `${expiresAt} 失效` : '有效';
}

function normalizeManagedShares(value) {
  if (!Array.isArray(value)) return [];
  return value
    .filter((share) => share && typeof share === 'object' && share.id)
    .map((share) => ({
      id: String(share.id),
      title: String(share.title || '会话片段'),
      state: ['active', 'revoked', 'expired'].includes(share.state) ? share.state : 'expired',
      expires_at: share.expires_at,
    }));
}

export default function ConversationShareReview({ topicId, messageIds = [], mode = 'create', onClose, onComplete = onClose }) {
  const [title, setTitle] = useState('会话片段');
  const [expiresIn, setExpiresIn] = useState(EXPIRY_OPTIONS[1].seconds);
  const [status, setStatus] = useState('ready');
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState(false);
  const [managedShares, setManagedShares] = useState([]);
  const [manageStatus, setManageStatus] = useState('idle');
  const [manageError, setManageError] = useState('');
  const [revokingShareID, setRevokingShareID] = useState('');
  const selectedMessageIDs = Array.isArray(messageIds) ? messageIds : [];

  const loadManagedShares = useCallback(async () => {
    setManageStatus('loading');
    setManageError('');
    try {
      const response = await api.listConversationShares(topicId);
      setManagedShares(normalizeManagedShares(response?.shares));
      setManageStatus('ready');
    } catch (cause) {
      setManageError(cause?.message || '加载已创建链接失败，请重试。');
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
        title: title.trim() || '会话片段',
        expiresIn: Number(expiresIn),
      });
      setResult(response);
      setStatus('created');
    } catch (cause) {
      setError(cause?.message || '创建分享链接失败，请重试。');
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
      setError('复制链接失败，请手动复制。');
    }
  };

  const revokeCreatedShare = async () => {
    if (!result?.id || status === 'revoking') return;
    setStatus('revoking');
    setError('');
    try {
      await api.revokeConversationShare(result.id);
      setStatus('revoked');
    } catch (cause) {
      setError(cause?.message || '撤销失败，请重试。');
      setStatus('created');
    }
  };

  const revokeManagedShare = async (shareID) => {
    if (!shareID || revokingShareID) return;
    setRevokingShareID(shareID);
    setManageError('');
    try {
      await api.revokeConversationShare(shareID);
      setManagedShares((current) => current.map((share) => (
        share.id === shareID ? { ...share, state: 'revoked' } : share
      )));
    } catch (cause) {
      setManageError(cause?.message || '撤销失败，请重试。');
    } finally {
      setRevokingShareID('');
    }
  };

  if (mode === 'manage') {
    return (
      <section className="cc-conversation-link-share-review" aria-live="polite">
        <div className="cc-conversation-link-share-heading">
          <div>
            <span className="cc-conversation-link-share-kicker"><Link2 size={15} /> 已创建链接</span>
            <h2>管理分享链接</h2>
            <p>撤销后，访客将无法再打开对应的片段和附件。</p>
          </div>
          <button type="button" className="cc-conversation-link-share-close" aria-label="关闭分享面板" onClick={onClose}>
            <X size={17} />
          </button>
        </div>
        {manageError && <p className="cc-conversation-link-share-error" role="alert">{manageError}</p>}
        {manageStatus === 'loading' && (
          <div className="cc-conversation-link-share-loading" role="status">
            <LoaderCircle className="is-spinning" size={16} /> 正在加载链接
          </div>
        )}
        {manageStatus === 'error' && (
          <div className="cc-conversation-link-share-actions">
            <button type="button" className="v3-btn-secondary" onClick={() => void loadManagedShares()}>
              <RefreshCw size={16} /> 重试
            </button>
          </div>
        )}
        {manageStatus === 'ready' && (
          <div className="cc-conversation-link-share-list">
            {managedShares.length === 0 && (
              <p className="cc-conversation-link-share-empty">当前会话还没有可管理的分享链接。</p>
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
                    className="v3-btn-danger"
                    aria-label={`撤销分享 ${share.title}`}
                    disabled={revokingShareID === share.id}
                    onClick={() => void revokeManagedShare(share.id)}
                  >
                    {revokingShareID === share.id ? <LoaderCircle className="is-spinning" size={16} /> : <RotateCcw size={16} />}
                    撤销
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
      <section className="cc-conversation-link-share-review" aria-live="polite">
        <div className="cc-conversation-link-share-heading">
          <div>
            <span className="cc-conversation-link-share-kicker"><Check size={15} /> 只读片段</span>
            <h2>分享链接已创建</h2>
            <p>访客只能浏览这 {result?.message_count || selectedMessageIDs.length} 条选中消息及其附件。</p>
          </div>
          <button type="button" className="cc-conversation-link-share-close" aria-label="关闭分享面板" onClick={onComplete}>
            <X size={17} />
          </button>
        </div>
        <div className="cc-conversation-link-share-url-row">
          <input aria-label="分享链接" value={result?.url || ''} readOnly onFocus={(event) => event.currentTarget.select()} />
          <button type="button" className="v3-action-btn" aria-label={copied ? '已复制链接' : '复制分享链接'} onClick={copyLink}>
            {copied ? <Check size={17} /> : <Copy size={17} />}
          </button>
        </div>
        {error && <p className="cc-conversation-link-share-error" role="alert">{error}</p>}
        <div className="cc-conversation-link-share-actions">
          <button type="button" className="v3-btn-secondary" onClick={onComplete}>完成</button>
          <button type="button" className="v3-btn-danger" aria-label="撤销此分享" onClick={revokeCreatedShare} disabled={status === 'revoking'}>
            {status === 'revoking' ? <LoaderCircle className="is-spinning" size={16} /> : <RotateCcw size={16} />}
            撤销分享
          </button>
        </div>
      </section>
    );
  }

  if (status === 'revoked') {
    return (
      <section className="cc-conversation-link-share-review" aria-live="polite">
        <div className="cc-conversation-link-share-heading">
          <div>
            <span className="cc-conversation-link-share-kicker"><Check size={15} /> 已处理</span>
            <h2>已撤销分享链接</h2>
            <p>该链接和它的附件预览已无法继续访问。</p>
          </div>
          <button type="button" className="cc-conversation-link-share-close" aria-label="关闭分享面板" onClick={onComplete}>
            <X size={17} />
          </button>
        </div>
        <div className="cc-conversation-link-share-actions">
          <button type="button" className="v3-btn-secondary" onClick={onComplete}>关闭</button>
        </div>
      </section>
    );
  }

  return (
    <section className="cc-conversation-link-share-review">
      <div className="cc-conversation-link-share-heading">
        <div>
          <span className="cc-conversation-link-share-kicker"><Link2 size={15} /> 只读分享</span>
          <h2>确认分享内容</h2>
          <p>只会导出已选的 {selectedMessageIDs.length} 条消息。不会携带原会话、成员或设备上下文。</p>
        </div>
        <button type="button" className="cc-conversation-link-share-close" aria-label="关闭分享面板" onClick={onClose}>
          <X size={17} />
        </button>
      </div>
      <form className="cc-conversation-link-share-form" onSubmit={createShare}>
        <label>
          <span>访客标题</span>
          <input value={title} maxLength={80} onChange={(event) => setTitle(event.target.value)} />
        </label>
        <label>
          <span>有效期</span>
          <select value={expiresIn} onChange={(event) => setExpiresIn(event.target.value)}>
            {EXPIRY_OPTIONS.map((option) => (
              <option key={option.seconds} value={option.seconds}>{option.label}</option>
            ))}
          </select>
        </label>
        {error && <p className="cc-conversation-link-share-error" role="alert">{error}</p>}
        <div className="cc-conversation-link-share-actions">
          <button type="button" className="v3-btn-secondary" onClick={onClose}>返回选择</button>
          <button type="submit" className="v3-btn-primary" disabled={status === 'saving'}>
            {status === 'saving' ? <LoaderCircle className="is-spinning" size={16} /> : <Link2 size={16} />}
            创建分享链接
          </button>
        </div>
      </form>
    </section>
  );
}
