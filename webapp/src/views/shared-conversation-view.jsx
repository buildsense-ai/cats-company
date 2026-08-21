import React, { useEffect, useMemo, useRef, useState } from 'react';

import { FileText, LoaderCircle, RefreshCw } from 'lucide-react';

import { api } from '../api';
import t from '../i18n';
import ChatMessage, { FilePreviewPanel } from '../widgets/chat-message';
import '../css/conversation-share.css';

function displayNameForSpeaker(speaker) {
  if (speaker === 'self') return t('conversation_share_speaker_self');
  if (speaker === 'assistant') return t('conversation_share_speaker_assistant');
  return t('conversation_share_speaker_participant');
}

function isAssistantSpeaker(speaker) {
  return speaker === 'assistant';
}

function normalizeSharedItems(items) {
  if (!Array.isArray(items)) return [];
  return items
    .filter((item) => item && typeof item === 'object')
    .map((item, index) => ({
      id: String(item.id || `shared-item-${index + 1}`),
      speaker: ['self', 'assistant', 'participant'].includes(item.speaker) ? item.speaker : 'participant',
      created_at: typeof item.created_at === 'string' ? item.created_at : undefined,
      content: typeof item.content === 'string' ? item.content : '',
      content_blocks: Array.isArray(item.content_blocks) ? item.content_blocks : [],
    }));
}

function isRetryableShareError(error) {
  const status = Number(error?.status || 0);
  return error?.code === 'NETWORK_ERROR'
    || error?.code === 'REQUEST_TIMEOUT'
    || status === 408
    || status === 429
    || status >= 500;
}

function SharedConversationLoading() {
  return (
    <main className="cc-shared-conversation cc-shared-conversation-state" role="status" aria-live="polite">
      <LoaderCircle className="is-spinning" size={20} aria-hidden="true" />
      <span>{t('conversation_share_loading')}</span>
    </main>
  );
}

function SharedConversationUnavailable({ retryable = false, onRetry }) {
  return (
    <main className="cc-shared-conversation cc-shared-conversation-state" role={retryable ? 'alert' : 'main'}>
      <FileText size={24} aria-hidden="true" />
      <h1>{t(retryable ? 'conversation_share_retryable_title' : 'conversation_share_unavailable_title')}</h1>
      <p>{t(retryable ? 'conversation_share_retryable_description' : 'conversation_share_unavailable_description')}</p>
      {retryable && (
        <button type="button" className="cc-conversation-share-secondary" onClick={onRetry}>
          <RefreshCw size={16} aria-hidden="true" />
          {t('conversation_share_retry')}
        </button>
      )}
    </main>
  );
}

export default function SharedConversationView({ token }) {
  const [state, setState] = useState({ status: 'loading', share: null });
  const [previewFile, setPreviewFile] = useState(null);
  const [loadAttempt, setLoadAttempt] = useState(0);
  const chatColumnRef = useRef(null);
  const normalizedToken = String(token || '').trim();

  useEffect(() => {
    if (!normalizedToken) {
      setState({ status: 'unavailable', share: null });
      return undefined;
    }
    const controller = new AbortController();
    setState({ status: 'loading', share: null });
    setPreviewFile(null);
    api.getConversationShare(normalizedToken, { signal: controller.signal })
      .then((share) => {
        if (!controller.signal.aborted) {
          setState({ status: 'ready', share });
        }
      })
      .catch((cause) => {
        if (!controller.signal.aborted) {
          setState({
            status: isRetryableShareError(cause) ? 'retryable' : 'unavailable',
            share: null,
          });
        }
      });
    return () => controller.abort();
  }, [normalizedToken, loadAttempt]);

  const defaultTitle = t('conversation_share_default_title');
  const title = String(state.share?.title || defaultTitle).trim() || defaultTitle;
  const items = useMemo(() => normalizeSharedItems(state.share?.items), [state.share?.items]);

  useEffect(() => {
    if (state.status !== 'ready') return undefined;
    const previousTitle = document.title;
    document.title = t('conversation_share_document_title', { title });
    return () => {
      document.title = previousTitle;
    };
  }, [state.status, title]);

  if (state.status === 'loading') return <SharedConversationLoading />;
  if (state.status !== 'ready') {
    return (
      <SharedConversationUnavailable
        retryable={state.status === 'retryable'}
        onRetry={() => setLoadAttempt((attempt) => attempt + 1)}
      />
    );
  }

  return (
    <main className="cc-shared-conversation" role="main">
      <header className="cc-shared-conversation-header v3-sidebar-header">
        <div className="v3-brand-title cc-shared-conversation-brand" aria-label={t('conversation_share_brand_aria')}>
          <span className="catsco-brand-mark" aria-hidden="true" />
          <span className="catsco-brand-name">CatsCo</span>
        </div>
        <div className="cc-shared-conversation-heading">
          <h1>{title}</h1>
          <span>{t('conversation_share_readonly_excerpt')}</span>
        </div>
        <span className="cc-shared-conversation-readonly">{t('conversation_share_readonly_badge')}</span>
      </header>

      <section className={`v3-message-workspace cc-shared-message-workspace${previewFile ? ' has-preview' : ''}`}>
        <div ref={chatColumnRef} className="v3-chat-column">
          <div className="v3-timeline cc-shared-timeline">
            <div className="v3-timeline-inner">
              <p className="cc-shared-conversation-notice">{t('conversation_share_notice')}</p>
              <div className="v3-date-divider"><span>{t('conversation_share_content_divider')}</span></div>
              {items.map((item, index) => {
                const prior = items[index - 1];
                const consecutive = prior?.speaker === item.speaker;
                return (
                  <div className="cc-message-anchor" key={item.id}>
                    <ChatMessage
                      message={item}
                      isSelf={item.speaker === 'self'}
                      isGroup={item.speaker === 'participant'}
                      senderName={displayNameForSpeaker(item.speaker)}
                      senderIsBot={isAssistantSpeaker(item.speaker)}
                      isConsecutive={consecutive}
                      onPreviewFile={setPreviewFile}
                      activePreviewFile={previewFile}
                    />
                  </div>
                );
              })}
              {items.length === 0 && (
                <div className="cc-shared-conversation-empty">{t('conversation_share_empty')}</div>
              )}
            </div>
          </div>
        </div>

        {previewFile && (
          <div className="v3-file-preview-shell">
            <FilePreviewPanel
              file={previewFile}
              onClose={() => setPreviewFile(null)}
              backgroundRef={chatColumnRef}
            />
          </div>
        )}
      </section>
    </main>
  );
}
