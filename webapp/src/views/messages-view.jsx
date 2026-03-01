import React, { useState, useRef, useEffect, useCallback } from 'react';
import { api, wsSendMessage, wsSendTyping, wsSendRead, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import ChatMessage from '../widgets/chat-message';

export default function MessagesView({ topic, topicName, user, isGroup, groupId }) {
  const [input, setInput] = useState('');
  const [messages, setMessages] = useState([]);
  const [peerTyping, setPeerTyping] = useState(false);
  const [typingUser, setTypingUser] = useState('');
  const [members, setMembers] = useState([]);
  const [showMentionPicker, setShowMentionPicker] = useState(false);
  const [mentionFilter, setMentionFilter] = useState('');
  const [replyTo, setReplyTo] = useState(null);
  const bottomRef = useRef(null);
  const typingTimer = useRef(null);
  const lastTypingSent = useRef(0);
  const peerTypingTimer = useRef(null);
  const fileInputRef = useRef(null);
  const imageInputRef = useRef(null);
  const textareaRef = useRef(null);

  // Load message history and group members when topic changes
  useEffect(() => {
    if (!topic) return;
    setMessages([]);
    setPeerTyping(false);
    setReplyTo(null);
    setMembers([]);
    loadHistory();
    if (isGroup && groupId) {
      loadGroupMembers();
    }
  }, [topic]);

  const loadGroupMembers = async () => {
    try {
      const res = await api.getGroupInfo(groupId);
      if (res.members) {
        setMembers(res.members);
      }
    } catch (e) {
      console.error('Failed to load group members:', e);
    }
  };

  // Listen for incoming WebSocket messages
  useEffect(() => {
    const unsub = onWSMessage((msg) => {
      // New message from server
      if (msg.data && msg.data.topic === topic) {
        const fromUid = parseUid(msg.data.from);
        setMessages((prev) => {
          // Deduplicate by seq ID
          if (prev.some((m) => m.id === msg.data.seq)) return prev;
          const serverMsg = {
            id: msg.data.seq,
            topic_id: msg.data.topic,
            from_uid: fromUid,
            from_name: msg.data.from,
            content: msg.data.content,
            msg_type: 'text',
            reply_to: msg.data.reply_to || 0,
            created_at: new Date().toISOString(),
          };
          // If this is our own message echoed back, replace the optimistic entry
          if (fromUid === user.uid) {
            const pendingIdx = prev.findIndex((m) => m._pending && m.content === serverMsg.content);
            if (pendingIdx !== -1) {
              const next = [...prev];
              next[pendingIdx] = serverMsg;
              return next;
            }
          }
          return [...prev, serverMsg];
        });
        updateTopicSeq(topic, msg.data.seq);
        // Send read receipt if message is from peer
        if (fromUid !== user.uid) {
          wsSendRead(topic, msg.data.seq);
        }
      }

      // Typing indicator from peer
      if (msg.info && msg.info.topic === topic && msg.info.what === 'kp') {
        const fromUid = parseUid(msg.info.from);
        if (fromUid !== user.uid) {
          setPeerTyping(true);
          setTypingUser(msg.info.from);
          clearTimeout(peerTypingTimer.current);
          peerTypingTimer.current = setTimeout(() => setPeerTyping(false), 3000);
        }
      }

      // Read receipt from peer
      if (msg.info && msg.info.topic === topic && msg.info.what === 'read') {
        // Could update message status here in the future
      }
    });

    return () => unsub();
  }, [topic, user.uid]);

  // Auto-scroll on new messages
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' });
  }, [messages.length, peerTyping]);

  const loadHistory = async () => {
    try {
      const res = await api.getMessages(topic);
      if (res.messages) {
        setMessages(res.messages);
      }
    } catch (e) {
      console.error('Failed to load messages:', e);
    }
  };

  const handleSend = useCallback(async () => {
    const text = input.trim();
    if (!text) return;
    setInput('');
    const currentReplyTo = replyTo;
    setReplyTo(null);

    // Optimistic local append
    const tempId = Date.now();
    setMessages((prev) => [...prev, {
      id: tempId,
      topic_id: topic,
      from_uid: user.uid,
      content: text,
      msg_type: 'text',
      reply_to: currentReplyTo ? currentReplyTo.id : 0,
      created_at: new Date().toISOString(),
      _pending: true,
    }]);

    // Send via WebSocket (falls back to REST if WS not connected)
    const wsId = await wsSendMessage(topic, text, currentReplyTo ? currentReplyTo.id : undefined);
    if (wsId === null) {
      // REST fallback was used -- reload history to get server-assigned ID
      const res = await api.getMessages(topic);
      if (res.messages) setMessages(res.messages);
    }
  }, [input, topic, user.uid, replyTo]);

  const handleKeyDown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const handleInputChange = (e) => {
    const val = e.target.value;
    setInput(val);

    // Detect @mention trigger
    if (isGroup) {
      const cursorPos = e.target.selectionStart;
      const textBeforeCursor = val.slice(0, cursorPos);
      const atMatch = textBeforeCursor.match(/@(\w*)$/);
      if (atMatch) {
        setShowMentionPicker(true);
        setMentionFilter(atMatch[1].toLowerCase());
      } else {
        setShowMentionPicker(false);
        setMentionFilter('');
      }
    }

    // Send typing indicator (throttled to once per 2s)
    const now = Date.now();
    if (now - lastTypingSent.current > 2000) {
      lastTypingSent.current = now;
      wsSendTyping(topic);
    }
  };

  const insertMention = (member) => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    const cursorPos = textarea.selectionStart;
    const textBeforeCursor = input.slice(0, cursorPos);
    const textAfterCursor = input.slice(cursorPos);
    const atIndex = textBeforeCursor.lastIndexOf('@');
    const mention = `@usr${member.user_id} `;
    const newText = textBeforeCursor.slice(0, atIndex) + mention + textAfterCursor;
    setInput(newText);
    setShowMentionPicker(false);
    setMentionFilter('');
    // Focus back on textarea
    setTimeout(() => {
      textarea.focus();
      const newPos = atIndex + mention.length;
      textarea.setSelectionRange(newPos, newPos);
    }, 0);
  };

  const handleFileUpload = async (e, type) => {
    const file = e.target.files?.[0];
    if (!file) return;
    e.target.value = ''; // reset input

    const formData = new FormData();
    formData.append('file', file);

    try {
      const res = await fetch(`${process.env.REACT_APP_API_BASE || ''}/api/upload?type=${type}`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${localStorage.getItem('oc_token')}` },
        body: formData,
      });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error);

      // Send rich content message via WebSocket
      const content = {
        type,
        payload: {
          file_key: data.file_key,
          url: data.url,
          name: data.name,
          size: data.size,
        },
      };
      if (type === 'image') {
        content.payload.thumbnail = data.url;
      }
      await wsSendMessage(topic, content);

      // Rich messages are not always echoed back with a shape the live view can merge reliably.
      // Refresh history after send so uploads appear immediately without a manual reopen.
      for (let attempt = 0; attempt < 3; attempt++) {
        await new Promise((resolve) => window.setTimeout(resolve, 400));
        const history = await api.getMessages(topic);
        if (history.messages) {
          setMessages(history.messages);
          const matched = history.messages.some((msg) => {
            if (typeof msg.content === 'string') {
              try {
                const parsed = JSON.parse(msg.content);
                return parsed?.payload?.file_key === data.file_key;
              } catch (parseErr) {
                return false;
              }
            }
            return msg.content?.payload?.file_key === data.file_key;
          });
          if (matched) break;
        }
      }
    } catch (err) {
      console.error('Upload failed:', err);
    }
  };

  // Find the display name for a uid in group context
  const getMemberName = (fromUid) => {
    if (!isGroup || !members.length) return null;
    const m = members.find((mem) => mem.user_id === fromUid);
    return m ? (m.display_name || m.username) : `usr${fromUid}`;
  };

  // Find the replied-to message
  const getReplyMessage = (replyToId) => {
    if (!replyToId) return null;
    return messages.find((m) => m.id === replyToId) || null;
  };

  const filteredMembers = members.filter((m) => {
    if (m.user_id === user.uid) return false;
    if (!mentionFilter) return true;
    const name = (m.display_name || m.username || '').toLowerCase();
    return name.includes(mentionFilter);
  });

  const displayName = topicName || topic;

  return (
    <>
      <div className="oc-header">
        {displayName}
        {isGroup && members.length > 0 && (
          <span style={{ fontSize: 12, color: '#888', marginLeft: 8 }}>
            ({members.length})
          </span>
        )}
      </div>
      <div className="oc-messages">
        {messages.map((msg, i) => (
          <ChatMessage
            key={msg.id || i}
            message={msg}
            isSelf={msg.from_uid === user.uid}
            isGroup={isGroup}
            senderName={isGroup ? getMemberName(msg.from_uid) : null}
            replyMessage={getReplyMessage(msg.reply_to)}
            onReply={() => setReplyTo(msg)}
          />
        ))}
        {peerTyping && (
          <div className="oc-typing-indicator">
            <span className="oc-typing-dots">
              <span /><span /><span />
            </span>
            {t('typing')}
          </div>
        )}
        <div ref={bottomRef} />
      </div>

      {/* Reply preview bar */}
      {replyTo && (
        <div className="oc-reply-bar">
          <div className="oc-reply-bar-content">
            <span className="oc-reply-bar-label">{t('chat_reply')}: </span>
            <span className="oc-reply-bar-text">
              {typeof replyTo.content === 'string' ? replyTo.content.slice(0, 60) : '[media]'}
            </span>
          </div>
          <button className="oc-reply-bar-close" onClick={() => setReplyTo(null)}>x</button>
        </div>
      )}

      <div className="oc-input-bar" style={{ position: 'relative' }}>
        {/* @mention picker */}
        {showMentionPicker && isGroup && filteredMembers.length > 0 && (
          <div className="oc-mention-picker">
            {filteredMembers.map((m) => (
              <div
                key={m.user_id}
                className="oc-mention-item"
                onClick={() => insertMention(m)}
              >
                <div className="oc-contact-avatar" style={{ width: 24, height: 24, borderRadius: 4 }} />
                <span>{m.display_name || m.username}</span>
              </div>
            ))}
          </div>
        )}

        <div className="oc-input-actions">
          <button className="oc-upload-btn" onClick={() => imageInputRef.current?.click()} title={t('chat_image')}>
            {'\uD83D\uDDBC'}
          </button>
          <button className="oc-upload-btn" onClick={() => fileInputRef.current?.click()} title={t('chat_file')}>
            {'\uD83D\uDCCE'}
          </button>
        </div>
        <textarea
          ref={textareaRef}
          rows={1}
          placeholder={t('chat_input_placeholder')}
          value={input}
          onChange={handleInputChange}
          onKeyDown={handleKeyDown}
        />
        <button
          className="oc-send-btn"
          disabled={!input.trim()}
          onClick={handleSend}
        >
          {t('chat_send')}
        </button>
        <input ref={imageInputRef} type="file" accept="image/*" style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'image')} />
        <input ref={fileInputRef} type="file" style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'file')} />
      </div>
    </>
  );
}

// Parse "usr123" -> 123
function parseUid(uidStr) {
  if (!uidStr) return 0;
  if (uidStr.startsWith('usr')) {
    return parseInt(uidStr.slice(3), 10) || 0;
  }
  return parseInt(uidStr, 10) || 0;
}
