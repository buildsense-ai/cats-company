import React, { useEffect, useMemo, useRef, useState } from 'react';

import { FileText, Link2, LoaderCircle } from 'lucide-react';

import { api } from '../api';
import ChatMessage, { FilePreviewPanel } from '../widgets/chat-message';
import '../css/conversation-share.css';

function displayNameForSpeaker(speaker) {
  if (speaker === 'self') return '分享者';
  if (speaker === 'assistant') return 'CatsCo';
  return '参与者';
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

function SharedConversationLoading() {
  return (
    <main className="cc-shared-conversation cc-shared-conversation-state" role="status" aria-live="polite">
      <LoaderCircle className="is-spinning" size={20} aria-hidden="true" />
      <span>正在打开分享片段…</span>
    </main>
  );
}

function SharedConversationUnavailable() {
  return (
    <main className="cc-shared-conversation cc-shared-conversation-state" role="main">
      <FileText size={24} aria-hidden="true" />
      <h1>该分享已不可用</h1>
      <p>链接可能已过期、被撤销，或不完整。</p>
    </main>
  );
}

export default function SharedConversationView({ token }) {
  const [state, setState] = useState({ status: 'loading', share: null });
  const [previewFile, setPreviewFile] = useState(null);
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
      .catch(() => {
        if (!controller.signal.aborted) {
          setState({ status: 'unavailable', share: null });
        }
      });
    return () => controller.abort();
  }, [normalizedToken]);

  const title = String(state.share?.title || '会话片段').trim() || '会话片段';
  const items = useMemo(() => normalizeSharedItems(state.share?.items), [state.share?.items]);

  useEffect(() => {
    if (state.status !== 'ready') return undefined;
    const previousTitle = document.title;
    document.title = `${title} · CatsCo`;
    return () => {
      document.title = previousTitle;
    };
  }, [state.status, title]);

  if (state.status === 'loading') return <SharedConversationLoading />;
  if (state.status !== 'ready') return <SharedConversationUnavailable />;

  return (
    <main className="cc-shared-conversation" role="main">
      <div className="cc-shared-conversation-header">
        <div className="cc-shared-conversation-brand" aria-label="CatsCo 会话片段">
          <Link2 size={18} aria-hidden="true" />
          <span>CatsCo</span>
        </div>
        <div className="cc-shared-conversation-heading">
          <h1>{title}</h1>
          <span>只读摘录</span>
        </div>
        <span className="cc-shared-conversation-readonly">只读分享</span>
      </div>

      <section className={`v3-message-workspace cc-shared-message-workspace${previewFile ? ' has-preview' : ''}`}>
        <div ref={chatColumnRef} className="v3-chat-column">
          <div className="v3-timeline cc-shared-timeline">
            <div className="v3-timeline-inner">
              <p className="cc-shared-conversation-notice">仅显示分享者明确选择的消息，不能继续对话。</p>
              <div className="v3-date-divider"><span>已分享的内容</span></div>
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
                <div className="cc-shared-conversation-empty">此分享中没有可显示的消息。</div>
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
