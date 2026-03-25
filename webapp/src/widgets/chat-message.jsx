import React, { useState } from 'react';
import { marked } from 'marked';
import { ChevronDown, ChevronRight, Terminal, Brain, FileText, Download } from 'lucide-react';
import t from '../i18n';
import Avatar from './avatar';
import { resolveMediaURL } from '../api';

marked.setOptions({ breaks: false, gfm: true });

/* Extract concise summary from tool input */
function toolInputSummary(name, input) {
  if (!input) return '';
  if (typeof input === 'string') return input;
  if (input.command) return input.command;
  if (input.file_path) return input.file_path;
  if (input.pattern) return input.pattern;
  if (input.content && typeof input.content === 'string') return input.content.slice(0, 120) + (input.content.length > 120 ? '…' : '');
  const vals = Object.values(input);
  const first = vals.find(v => typeof v === 'string');
  if (first) return first.slice(0, 120) + (first.length > 120 ? '…' : '');
  return JSON.stringify(input).slice(0, 120);
}

function truncateResult(text, max = 300) {
  if (!text) return '';
  if (typeof text !== 'string') text = JSON.stringify(text);
  if (text.length <= max) return text;
  return text.slice(0, max) + '…';
}

function groupBlocks(messages) {
  const items = [];
  const pendingTools = {};

  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    if (msg.type === 'thinking') {
      items.push({ type: 'thinking', text: msg.content });
    } else if (msg.type === 'tool_use') {
      const toolId = msg.metadata?.id || msg.metadata?.tool_call_id;
      const pair = {
        type: 'tool_pair',
        name: msg.content,
        input: msg.metadata?.input,
        result: null,
        isError: false,
        id: toolId
      };
      if (toolId) pendingTools[toolId] = pair;
      items.push(pair);
    } else if (msg.type === 'tool_result') {
      const toolId = msg.metadata?.id || msg.metadata?.tool_call_id;
      let matched = false;
      if (toolId && pendingTools[toolId]) {
        pendingTools[toolId].result = msg.content;
        pendingTools[toolId].isError = msg.metadata?.is_error || false;
        matched = true;
      } else {
        // Fallback: match with first unfulfilled tool_pair
        for (const item of items) {
          if (item.type === 'tool_pair' && item.result === null) {
            item.result = msg.content;
            item.isError = msg.metadata?.is_error || false;
            matched = true;
            break;
          }
        }
      }
      if (!matched) {
        items.push({ type: 'tool_result_orphan', content: msg.content, isError: msg.metadata?.is_error || false });
      }
    }
  }
  return items;
}

function WorkingProcess({ blocks }) {
  const [open, setOpen] = useState(false);
  if (!blocks || blocks.length === 0) return null;

  return (
    <div className="v3-working-process">
      <button className="v3-working-toggle" onClick={() => setOpen(!open)}>
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        <span className="v3-working-label">WORKING...</span>
        {!open && <span className="v3-working-hint">展开详情</span>}
      </button>
      {open && (
        <div className="v3-working-steps">
          {blocks.map((item, i) => {
            if (item.type === 'thinking') {
              return (
                <div key={i} className="v3-wpi-thinking">
                  <Brain size={14} className="v3-wpi-icon" />
                  <span className="v3-wpi-text">{item.text}</span>
                </div>
              );
            }
            if (item.type === 'tool_pair') {
              return (
                <div key={i} className="v3-wpi-tool">
                  <div className="v3-wpi-tool-header">
                    <Terminal size={14} className="v3-wpi-icon" />
                    <span className="v3-wpi-tool-name">{item.name}</span>
                    <span className="oc-wpi-tool-input" style={{ marginLeft: 8, opacity: 0.7, fontSize: 11 }}>
                      {toolInputSummary(item.name, item.input)}
                    </span>
                  </div>
                  {item.result != null && (
                    <div className="v3-wpi-tool-result">
                      <div className="v3-wpi-code-block result">
                        <pre><code>{typeof item.result === 'string' ? item.result : JSON.stringify(item.result, null, 2)}</code></pre>
                      </div>
                    </div>
                  )}
                </div>
              );
            }
            if (item.type === 'tool_result_orphan') {
              return (
                <div key={i} className="v3-wpi-tool-result">
                  <div className="v3-wpi-code-block result">
                     <pre><code>{typeof item.content === 'string' ? item.content : JSON.stringify(item.content, null, 2)}</code></pre>
                  </div>
                </div>
              );
            }
            return null;
          })}
        </div>
      )}
    </div>
  );
}

export default function ChatMessage({ message, isSelf, isGroup, senderName, senderAvatarUrl, senderIsBot, replyMessage, onReply, showThinking = true, isConsecutive }) {
  const content = message.content;
  const workingMessages = message._working || [];
  const workingBlocks = workingMessages.length > 0 ? groupBlocks(workingMessages) : [];
  const hasText = typeof content === 'string' ? content.trim().length > 0 : (content != null);

  if (!hasText && workingBlocks.length === 0) return null;

  let parsed = null;
  if (typeof content === 'object' && content !== null && content.type) {
    parsed = content;
  } else if (typeof content === 'string') {
    try {
      const obj = JSON.parse(content);
      if (obj && obj.type) parsed = obj;
    } catch (e) {
      // plain text
    }
  }

  const timeString = new Date(message.created_at || Date.now()).toLocaleTimeString([], {hour: '2-digit', minute:'2-digit'});
  const displayName = senderName || message.from_name || `User ${message.from_uid || ''}`;

  return (
    <div className={`v3-message ${isConsecutive ? 'grouped' : ''}`}>
      <div className="v3-message-actions">
        <button className="v3-action-btn" aria-label="Add Reaction">😀</button>
        {onReply && <button className="v3-action-btn" onClick={onReply} aria-label="Reply">↲</button>}
        <button className="v3-action-btn" aria-label="More Options">⋯</button>
      </div>

      <div className="v3-avatar-col">
        {isConsecutive ? (
          timeString
        ) : (
          <Avatar
            name={displayName}
            src={senderAvatarUrl}
            size={36}
            isBot={senderIsBot}
            className={`v3-avatar ${senderIsBot ? 'bot' : ''}`}
            style={{ borderRadius: 4, background: senderIsBot ? 'linear-gradient(135deg, #a855f7 0%, #ec4899 100%)' : '#E8E8E8', color: senderIsBot ? '#fff' : '#333' }}
          />
        )}
      </div>

      <div className="v3-msg-body">
        {!isConsecutive && (
          <div className="v3-msg-header">
            <span className="v3-msg-name">{displayName}</span>
            <span className="v3-msg-time">{timeString}</span>
          </div>
        )}

        {replyMessage && (
          <div style={{ padding: '4px 8px', background: 'rgba(255,255,255,0.05)', borderRadius: 4, marginBottom: 4, fontSize: 13, color: '#aaa', borderLeft: '3px solid var(--v3-primary)', width: 'fit-content' }}>
            <span style={{opacity: 0.8}}>
              {typeof replyMessage.content === 'string' ? replyMessage.content.slice(0, 80) : '[media]'}
            </span>
          </div>
        )}

        {!isSelf && showThinking && <WorkingProcess blocks={workingBlocks} />}

        {hasText && workingBlocks.length === 0 && (
          <div style={{lineHeight: 1.46}}>
            {parsed ? <RichContent content={parsed} /> : <TextContent content={content} isGroup={isGroup} />}
          </div>
        )}
      </div>
    </div>
  );
}

function TextContent({ content, isGroup }) {
  const text = typeof content === 'string' ? content : content?.text || String(content || '');

  // Check if text has markdown syntax
  const hasMarkdown = /(\*\*|__|`|#{1,6}\s|^\s*[-*+]\s|^\s*\d+\.\s|\[.*\]\(.*\))/m.test(text);

  if (hasMarkdown) {
    try {
      const html = marked.parse(text);
      return <div dangerouslySetInnerHTML={{ __html: html }} className="oc-markdown" />;
    } catch (e) {
      console.error('Markdown parse error:', e);
    }
  }

  if (isGroup) {
    const parts = text.split(/(@usr\d+)/g);
    return (
      <span>
        {parts.map((part, i) =>
          part.match(/^@usr\d+$/) ? (
            <span key={i} className="oc-mention">{part}</span>
          ) : (
            <span key={i}>{part}</span>
          )
        )}
      </span>
    );
  }

  return <span style={{ whiteSpace: 'pre-wrap' }}>{text}</span>;
}

function RichContent({ content }) {
  switch (content.type) {
    case 'image':
      return <ImageContent payload={content.payload} />;
    case 'file':
      return <FileContent payload={content.payload} />;
    case 'link_preview':
      return <LinkPreviewContent payload={content.payload} />;
    case 'card':
      return <CardContent payload={content.payload} />;
    default:
      return <TextContent content={content.payload?.text || JSON.stringify(content)} />;
  }
}

function ImageContent({ payload }) {
  const [expanded, setExpanded] = useState(false);
  if (!payload) return null;
  const src = payload.url || payload.thumbnail;
  return (
    <div className="oc-rich-image">
      <img
        src={resolveMediaURL(src)}
        alt="image"
        className="oc-rich-image-thumb"
        onClick={() => setExpanded(true)}
        style={{ maxWidth: 240, maxHeight: 240, borderRadius: 4, cursor: 'pointer' }}
      />
      {expanded && (
        <div className="oc-modal-overlay" onClick={() => setExpanded(false)}>
          <img src={resolveMediaURL(payload.url || src)} alt="full" style={{ maxWidth: '90vw', maxHeight: '90vh', borderRadius: 8 }} />
        </div>
      )}
    </div>
  );
}

function FileContent({ payload }) {
  const [preview, setPreview] = useState(false);
  const [textContent, setTextContent] = useState(null);
  const [loadingText, setLoadingText] = useState(false);

  if (!payload) return null;
  const sizeStr = payload.size ? formatFileSize(payload.size) : '';
  const url = resolveMediaURL(payload.url);
  const ext = payload.name?.split('.').pop()?.toUpperCase() || 'FILE';
  const canPreview = ['PDF', 'TXT', 'JSON', 'MD', 'CSV', 'JS', 'PY', 'GO', 'HTML', 'CSS'].includes(ext);
  
  const subtitle = `${sizeStr} ${sizeStr ? '• ' : ''}${ext} Document`;

  const handleOpenPreview = async () => {
    setPreview(true);
    if (ext !== 'PDF') {
      setLoadingText(true);
      setTextContent(null);
      try {
        // [CORS Fix]: Strip the absolute domain from the URL strings so that 
        // the XHR cleanly pipes through the Webpack proxy, avoiding browser CORS blocks.
        let fetchUrl = url;
        try {
          const urlObj = new URL(url);
          fetchUrl = urlObj.pathname + urlObj.search;
        } catch (e) {}

        const res = await fetch(fetchUrl);
        if (!res.ok) throw new Error(`HTTP Error ${res.status}`);
        const text = await res.text();
        setTextContent(text);
      } catch (err) {
        setTextContent('Error loading file preview: ' + err.message);
      } finally {
        setLoadingText(false);
      }
    }
  };

  return (
    <>
      <div 
        className="v3-attachment-card" 
        onClick={() => {
          if (canPreview) handleOpenPreview();
          else if (url) window.open(url, '_blank');
        }}
        title={canPreview ? "Click to Preview" : "Click to Open/Download"}
      >
        <div className="v3-attachment-icon">
          <FileText size={18} strokeWidth={1.5} />
        </div>
        <div className="v3-attachment-info">
          <span className="v3-attachment-name">{payload.name || 'File'}</span>
          <span className="v3-attachment-size">{subtitle}</span>
        </div>
        {!canPreview && payload.url && (
           <div style={{ marginLeft: 16, opacity: 0.4 }}>
             <Download size={14} />
           </div>
        )}
      </div>
      {preview && (
        <div className="oc-modal-overlay" onClick={() => setPreview(false)} style={{ background: 'rgba(0,0,0,0.7)', backdropFilter: 'blur(4px)' }}>
          <div className="oc-modal" onClick={(e) => e.stopPropagation()} style={{ maxWidth: '90vw', width: ext === 'PDF' ? '90vw' : 800, maxHeight: '90vh', overflow: 'hidden', display: 'flex', flexDirection: 'column', background: 'var(--v3-bg-sidebar)', border: '1px solid var(--v3-border)', borderRadius: '12px', boxShadow: '0 24px 48px rgba(0,0,0,0.5)', color: 'var(--v3-text-name)' }}>
            <div className="oc-modal-header" style={{ padding: '16px 24px', borderBottom: '1px solid var(--v3-border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <div style={{ display: 'flex', alignItems: 'center' }}>
                <FileText size={18} style={{ marginRight: 12, opacity: 0.7 }} />
                <h3 style={{ margin: 0, fontSize: 16, fontWeight: 600 }}>{payload.name}</h3>
              </div>
              <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                <a href={url} download title="Download Original" style={{ color: 'var(--v3-text-muted)', cursor: 'pointer', display: 'flex', alignItems: 'center' }} target="_blank" rel="noopener noreferrer">
                  <Download size={18} />
                </a>
                <button onClick={() => setPreview(false)} style={{ background: 'transparent', border: 'none', color: 'var(--v3-text-muted)', fontSize: 26, cursor: 'pointer', display: 'flex', alignItems: 'center', margin: '-4px 0 -4px 8px' }}>×</button>
              </div>
            </div>
            <div className="oc-modal-body" style={{ flex: 1, padding: 0, overflow: 'hidden' }}>
              {ext === 'PDF' ? (
                <iframe src={url} style={{ width: '100%', height: '75vh', border: 'none', display: 'block' }} title="PDF Preview" />
              ) : (
                <div 
                   className={ext === 'MD' ? 'oc-markdown' : ''} 
                   style={{ width: '100%', height: '75vh', overflow: 'auto', background: 'var(--v3-bg-app)', color: 'var(--v3-text-main)', padding: '32px 40px', boxSizing: 'border-box', fontFamily: ext === 'MD' ? 'inherit' : '"SFMono-Regular", Consolas, "Liberation Mono", Menlo, Courier, monospace', whiteSpace: ext === 'MD' ? 'normal' : 'pre-wrap', fontSize: 14, lineHeight: 1.6 }}
                   dangerouslySetInnerHTML={ext === 'MD' && textContent ? { __html: marked.parse(textContent) } : undefined}
                >
                  {loadingText ? (
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', opacity: 0.5 }}>Loading preview...</div>
                  ) : ext !== 'MD' ? (
                    textContent || 'No content available.'
                  ) : null}
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function LinkPreviewContent({ payload }) {
  if (!payload) return null;
  return (
    <a href={resolveMediaURL(payload.url)} target="_blank" rel="noopener noreferrer" className="oc-rich-link" style={{ textDecoration: 'none', color: 'inherit' }}>
      {payload.image && <img src={resolveMediaURL(payload.image)} alt="" style={{ width: '100%', maxHeight: 160, objectFit: 'cover', borderRadius: '4px 4px 0 0' }} />}
      <div style={{ padding: '8px 0' }}>
        <div style={{ fontWeight: 500, fontSize: 14 }}>{payload.title || payload.url}</div>
        {payload.description && <div style={{ fontSize: 12, color: '#888', marginTop: 4 }}>{payload.description}</div>}
        {payload.site_name && <div style={{ fontSize: 11, color: '#aaa', marginTop: 4 }}>{payload.site_name}</div>}
      </div>
    </a>
  );
}

function CardContent({ payload }) {
  if (!payload) return null;
  return (
    <div className="oc-rich-card">
      {payload.image && <img src={resolveMediaURL(payload.image)} alt="" style={{ width: '100%', maxHeight: 120, objectFit: 'cover', borderRadius: '4px 4px 0 0' }} />}
      <div style={{ padding: 8 }}>
        <div style={{ fontWeight: 600, fontSize: 14 }}>{payload.title}</div>
        {payload.text && <div style={{ fontSize: 13, color: '#666', marginTop: 4 }}>{payload.text}</div>}
      </div>
      {payload.buttons && payload.buttons.length > 0 && (
        <div className="oc-rich-card-buttons">
          {payload.buttons.map((btn, i) => (
            <button
              key={i}
              className="oc-btn oc-btn-default"
              onClick={() => {
                if (btn.action === 'url') window.open(btn.value, '_blank');
                if (btn.action === 'copy') navigator.clipboard?.writeText(btn.value);
              }}
              style={{ flex: 1 }}
            >
              {btn.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function formatFileSize(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}
