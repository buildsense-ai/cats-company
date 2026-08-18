import React, { useState } from 'react';
import { Check, Copy, Link2, LoaderCircle, RotateCcw, X } from 'lucide-react';

import { api } from '../api';

const EXPIRY_OPTIONS = [
  { seconds: 24 * 60 * 60, label: '24 小时后失效' },
  { seconds: 7 * 24 * 60 * 60, label: '7 天后失效' },
  { seconds: 30 * 24 * 60 * 60, label: '30 天后失效' },
];

async function copyShareURL(url) {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(url);
    return;
  }
  const input = document.createElement('textarea');
  input.value = url;
  input.setAttribute('readonly', '');
  input.style.position = 'fixed';
  input.style.left = '-9999px';
  document.body.appendChild(input);
  input.select();
  document.execCommand('copy');
  document.body.removeChild(input);
}

export default function ConversationShareReview({ topicId, messageIds, onClose, onComplete = onClose }) {
  const [title, setTitle] = useState('会话片段');
  const [expiresIn, setExpiresIn] = useState(EXPIRY_OPTIONS[1].seconds);
  const [status, setStatus] = useState('ready');
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState(false);

  const createShare = async (event) => {
    event.preventDefault();
    if (status === 'saving' || messageIds.length === 0) return;
    setStatus('saving');
    setError('');
    try {
      const response = await api.createConversationShare({
        topicId,
        messageIds,
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
      await copyShareURL(result.url);
      setCopied(true);
    } catch {
      setCopied(false);
      setError('复制链接失败，请手动复制。');
    }
  };

  const revokeShare = async () => {
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

  if (status === 'created' || status === 'revoking') {
    return (
      <section className="cc-conversation-link-share-review" aria-live="polite">
        <div className="cc-conversation-link-share-heading">
          <div>
            <span className="cc-conversation-link-share-kicker"><Check size={15} /> 只读片段</span>
            <h2>分享链接已创建</h2>
            <p>访客只能浏览这 {result?.message_count || messageIds.length} 条选中消息及其附件。</p>
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
          <button type="button" className="v3-btn-danger" aria-label="撤销此分享" onClick={revokeShare} disabled={status === 'revoking'}>
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
          <p>只会导出已选的 {messageIds.length} 条消息。不会携带原会话、成员或设备上下文。</p>
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
