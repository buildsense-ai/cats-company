import React, { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { api, wsSendMessage, wsSendTyping, wsSendRead, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import ChatMessage from '../widgets/chat-message';
import GroupSettings from '../widgets/group-settings';
import Avatar from '../widgets/avatar';

const PAGE_SIZE = 50;
const TYPING_TIMEOUT_MS = 10000;

export default function MessagesView({ topic, topicName, user, isGroup, groupId, topicAvatarUrl, onTopicUpdated }) {
  const [input, setInput] = useState('');
  const [messages, setMessages] = useState([]);
  const [peerTyping, setPeerTyping] = useState(false);
  const [members, setMembers] = useState([]);
  const [groupInfo, setGroupInfo] = useState(null);
  const [peerProfile, setPeerProfile] = useState(null);
  const [showMentionPicker, setShowMentionPicker] = useState(false);
  const [mentionFilter, setMentionFilter] = useState('');
  const [replyTo, setReplyTo] = useState(null);
  const [historyOffset, setHistoryOffset] = useState(0);
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [showGroupSettings, setShowGroupSettings] = useState(false);
  const [showThinking, setShowThinking] = useState(() => {
    const saved = localStorage.getItem('cc_show_thinking');
    return saved === null ? true : saved === 'true';
  });
  const bottomRef = useRef(null);
  const lastTypingSent = useRef(0);
  const peerTypingTimer = useRef(null);
  const timelineRef = useRef(null);
  const previousScrollRef = useRef(null);
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
    setGroupInfo(null);
    setPeerProfile(null);
    setHistoryOffset(0);
    setHasMoreHistory(false);
    loadHistory();
    if (isGroup && groupId) {
      loadGroupMembers();
    } else {
      loadPeerProfile();
    }
  }, [topic]);

  const loadGroupMembers = async () => {
    try {
      const res = await api.getGroupInfo(groupId);
      if (res.members) {
        setMembers(res.members);
      }
      if (res.group) {
        setGroupInfo(res.group);
      }
    } catch (e) {
      console.error('Failed to load group members:', e);
    }
  };

  const loadPeerProfile = async () => {
    try {
      const res = await api.getFriends();
      const friends = res.friends || [];
      const [left, right] = topic.replace('p2p_', '').split('_').map((n) => parseInt(n, 10));
      const peerId = left === user.uid ? right : left;
      const peer = friends.find((friend) => friend.id === peerId);
      if (peer) setPeerProfile(peer);
    } catch (e) {
      console.error('Failed to load peer profile:', e);
    }
  };

  // Listen for incoming WebSocket messages
  useEffect(() => {
    const unsub = onWSMessage((msg) => {
      // New message from server
      if (msg.data && msg.data.topic === topic) {
        const fromUid = parseUid(msg.data.from);
        const serverMsg = {
          id: msg.data.seq_id || msg.data.seq,
          seq_id: msg.data.seq_id || msg.data.seq,
          topic_id: msg.data.topic,
          from_uid: fromUid,
          from_name: msg.data.from,
          content: msg.data.content,
          type: msg.data.type || 'text',
          metadata: msg.data.metadata || null,
          msg_type: 'text',
          reply_to: msg.data.reply_to || 0,
          created_at: new Date().toISOString(),
        };

        console.log('[WS收到消息]', 'seq:', serverMsg.id, 'seq_id:', serverMsg.seq_id, 'type:', serverMsg.type, 'content:', serverMsg.content?.substring(0, 30));

        setMessages((prev) => {
          console.log('[WS合并前] 当前消息数:', prev.length, '最后3条seq:', prev.slice(-3).map(m => m.seq_id || m.id));
          // Deduplicate by seq ID
          if (prev.some((m) => m.id === serverMsg.id)) {
            console.log('[WS去重] 消息已存在:', serverMsg.id);
            return prev;
          }
          // If this is our own message echoed back, replace the optimistic entry
          if (fromUid === user.uid) {
            const pendingIdx = prev.findIndex((m) => m._pending && m.content.trim() === serverMsg.content.trim());
            if (pendingIdx !== -1) {
              const next = [...prev];
              next[pendingIdx] = serverMsg;
              return next;
            }
          }
          return mergeMessages(prev, [serverMsg]);
        });
        updateTopicSeq(topic, serverMsg.id);

        // Send read receipt if message is from peer
        if (fromUid !== user.uid) {
          wsSendRead(topic, serverMsg.id);
        }
      }

      // Typing indicator from peer
      if (msg.info && msg.info.topic === topic && msg.info.what === 'kp') {
        const fromUid = parseUid(msg.info.from);
        if (fromUid !== user.uid) {
          setPeerTyping(true);
          clearTimeout(peerTypingTimer.current);
          peerTypingTimer.current = setTimeout(() => setPeerTyping(false), TYPING_TIMEOUT_MS);
        }
      }

      // Read receipt from peer
      if (msg.info && msg.info.topic === topic && msg.info.what === 'read') {
        // Could update message status here in the future
      }
    });

    return () => unsub();
  }, [topic, user.uid]);

  // Auto-scroll to bottom or restore scroll anchor depending on state
  React.useLayoutEffect(() => {
    if (previousScrollRef.current && timelineRef.current) {
      // Anchoring condition: We just prepended older history.
      const { scrollHeight, scrollTop } = previousScrollRef.current;
      const newScrollHeight = timelineRef.current.scrollHeight;
      timelineRef.current.scrollTop = scrollTop + (newScrollHeight - scrollHeight);
      previousScrollRef.current = null; // Clear atomic lock
    } else {
      // Standard condition: New message arrived or initial load. Scroll to bottom.
      bottomRef.current?.scrollIntoView({ behavior: 'auto' });
    }
  }, [messages.length, peerTyping]);

  const loadHistory = async () => {
    try {
      const res = await api.getMessages(topic, PAGE_SIZE, 0, true);
      if (res.messages) {
        setMessages(res.messages);
        setHistoryOffset(res.messages.length);
        setHasMoreHistory(res.messages.length === PAGE_SIZE);
      }
    } catch (e) {
      console.error('Failed to load messages:', e);
    }
  };

  const loadOlderHistory = async () => {
    if (loadingOlder || !hasMoreHistory) return;
    
    // Capture the absolute scroll geometry BEFORE rendering the older batch
    if (timelineRef.current) {
      previousScrollRef.current = {
        scrollHeight: timelineRef.current.scrollHeight,
        scrollTop: timelineRef.current.scrollTop,
      };
    }
    
    setLoadingOlder(true);
    try {
      const res = await api.getMessages(topic, PAGE_SIZE, historyOffset, true);
      const older = res.messages || [];
      setMessages((prev) => mergeMessages(older, prev));
      setHistoryOffset((prev) => prev + older.length);
      setHasMoreHistory(older.length === PAGE_SIZE);
    } catch (e) {
      console.error('Failed to load older messages:', e);
    } finally {
      setLoadingOlder(false);
    }
  };

  const handleSend = useCallback(async () => {
    const text = input.trim();
    if (!text) return;
    setInput('');
    const currentReplyTo = replyTo;
    setReplyTo(null);

    // Optimistic local append
    const tempId = Date.now(); // Provide a large positive seq_id so it sorts to the bottom
    setMessages((prev) => [...prev, {
      id: tempId,
      seq_id: tempId,
      topic_id: topic,
      from_uid: user.uid,
      content: text,
      type: 'text',
      msg_type: 'text',
      reply_to: currentReplyTo ? currentReplyTo.id : 0,
      created_at: new Date().toISOString(),
      _pending: true,
    }]);

    // Send via REST API (unified with Code Mode)
    try {
      console.log('[发送消息] 使用REST API:', topic, text.substring(0, 30));
      const result = await api.sendMessage(topic, text, currentReplyTo ? currentReplyTo.id : undefined);
      console.log('[发送成功]', result);
      
      // Update optimistic message with real database sequence ID
      if (result && (result.seq_id || result.id)) {
        setMessages((prev) => {
          const idx = prev.findIndex(m => m.id === tempId);
          if (idx !== -1) {
            const next = [...prev];
            next[idx] = {
              ...next[idx],
              id: result.seq_id || result.id,
              seq_id: result.seq_id || result.id,
              _pending: false
            };
            // Re-sort to position appropriately
            return next.sort((a, b) => (a.seq_id || a.id) - (b.seq_id || b.id));
          }
          return prev;
        });
      }
    } catch (err) {
      console.error('[发送失败]', err);
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

    const MAX_SIZE = 100 * 1024 * 1024; // 100MB
    if (file.size > MAX_SIZE) {
      window.alert(`Upload failed: File is too large (${(file.size / 1024 / 1024).toFixed(1)}MB). Maximum supported size is 100MB.`);
      return;
    }

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

      // Send rich content message via REST API (unified with Code Mode)
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

      // Optimistic local append for rich media
      const tempId = Date.now();
      setMessages((prev) => [...prev, {
        id: tempId,
        seq_id: tempId,
        topic_id: topic,
        from_uid: user.uid,
        content: content,
        type: type,
        msg_type: type,
        reply_to: 0,
        created_at: new Date().toISOString(),
        _pending: true,
      }]);

      const result = await api.sendMessage(topic, content);
      
      // Update optimistic message with real database sequence ID
      if (result && (result.seq_id || result.id)) {
        setMessages((prev) => {
          const idx = prev.findIndex(m => m.id === tempId);
          if (idx !== -1) {
            const next = [...prev];
            next[idx] = {
              ...next[idx],
              id: result.seq_id || result.id,
              seq_id: result.seq_id || result.id,
              _pending: false
            };
            return next.sort((a, b) => (a.seq_id || a.id) - (b.seq_id || b.id));
          }
          return prev;
        });
      }
    } catch (err) {
      console.error('Upload failed:', err);
      // Fallback: If the server returns a raw Nginx HTML 413 instead of JSON, 
      // res.json() will throw a generic SyntaxError. We explicitly alert the user.
      const errorMsg = err.message.includes('Unexpected token') || err.message.includes('JSON')
        ? 'Upload failed: Server rejected the file (likely Payload Too Large / 413).'
        : `Upload failed: ${err.message}`;
      window.alert(errorMsg);
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

  const displayName = isGroup ? (groupInfo?.name || topicName || topic) : (peerProfile?.display_name || peerProfile?.username || topicName || topic);
  const displayAvatarUrl = isGroup ? (groupInfo?.avatar_url || topicAvatarUrl) : (peerProfile?.avatar_url || topicAvatarUrl);

  const memberMap = useMemo(() => {
    const map = new Map();
    members.forEach((member) => {
      map.set(member.user_id, member);
    });
    return map;
  }, [members]);

  const getSender = (msg) => {
    if (msg.from_uid === user.uid) {
      return {
        name: user.display_name || user.username,
        avatarUrl: user.avatar_url,
        isBot: user.account_type === 'bot',
      };
    }
    if (isGroup) {
      const member = memberMap.get(msg.from_uid);
      return {
        name: member ? (member.display_name || member.username) : `usr${msg.from_uid}`,
        avatarUrl: member?.avatar_url,
        isBot: member?.is_bot,
      };
    }
    return {
      name: peerProfile?.display_name || peerProfile?.username || topicName || topic,
      avatarUrl: peerProfile?.avatar_url || topicAvatarUrl,
      isBot: peerProfile?.account_type === 'bot',
    };
  };

  // Group messages into working areas and text messages with consecutive checking
  const groupedMessages = useMemo(() => {
    const groups = [];
    let currentWorking = null;
    let prevSenderUid = null;
    let prevTime = 0;

    messages.forEach(msg => {
      const msgTime = new Date(msg.created_at || Date.now()).getTime();
      const senderUid = msg.from_uid;
      const isConsecutive = (prevSenderUid === senderUid && (msgTime - prevTime < 5 * 60 * 1000));

      if (msg.type === 'thinking' || msg.type === 'tool_use' || msg.type === 'tool_result') {
        if (!currentWorking) {
          currentWorking = { type: 'working', messages: [], sender: getSender(msg), isConsecutive: isConsecutive };
        }
        currentWorking.messages.push(msg);
        prevSenderUid = senderUid;
        prevTime = msgTime;
      } else {
        if (currentWorking) {
          groups.push(currentWorking);
          currentWorking = null;
        }
        // Recalculate isConsecutive in case a working block just processed
        const textIsConsecutive = (prevSenderUid === senderUid && (msgTime - prevTime < 5 * 60 * 1000));
        groups.push({ type: 'text', message: msg, sender: getSender(msg), isConsecutive: textIsConsecutive });
        prevSenderUid = senderUid;
        prevTime = msgTime;
      }
    });

    if (currentWorking) {
      groups.push(currentWorking);
    }

    return groups;
  }, [messages, user.uid, isGroup, memberMap, peerProfile, topicName, topic, topicAvatarUrl]);

  const handleGroupSaved = (updatedGroup) => {
    setShowGroupSettings(false);
    if (updatedGroup) {
      setGroupInfo(updatedGroup);
      if (onTopicUpdated) {
        onTopicUpdated({
          topicId: topic,
          name: updatedGroup.name,
          avatar_url: updatedGroup.avatar_url,
          isGroup: true,
          groupId,
        });
      }
    }
    loadGroupMembers();
    window.dispatchEvent(new Event('cc:data-changed'));
  };

  const handleTimelineScroll = (e) => {
    const el = e.target;
    // Trigger infinite fetch when within 100 pixels of the top
    if (el.scrollTop < 100 && hasMoreHistory && !loadingOlder) {
      loadOlderHistory();
    }
  };

  return (
    <>
      <div className="v3-header">
        <div className="v3-header-left">
          <div style={{display: 'flex', flexDirection: 'column'}}>
            <span className="v3-header-title" style={{ fontSize: 17, letterSpacing: '-0.3px' }}>{displayName}</span>
            {isGroup && members.length > 0 && <span className="v3-header-desc">{members.length} members</span>}
          </div>
        </div>
        <div className="v3-header-actions">
          {isGroup && (
            <button className="v3-action-btn" onClick={() => setShowGroupSettings(true)} title={t('group_settings')}>
              ⋯
            </button>
          )}
        </div>
      </div>
      <div className="v3-timeline" ref={timelineRef} onScroll={handleTimelineScroll}>
        <div style={{ maxWidth: 900, margin: '0 auto', width: '100%', display: 'flex', flexDirection: 'column' }}>
          <div className="v3-date-divider">
            <span>Chat History</span>
          </div>
        
        {hasMoreHistory && (
          <div className="oc-history-load" style={{textAlign:'center', padding:'10px 0 24px 0'}}>
            <button className="v3-btn-secondary" onClick={loadOlderHistory} disabled={loadingOlder}>
              {loadingOlder ? t('loading') : t('chat_load_older')}
            </button>
          </div>
        )}
        
        {groupedMessages.map((group, i) => {
          if (group.type === 'working') {
            if (!showThinking) return null;
            return (
              <div key={i} className="oc-working-group">
                <ChatMessage
                  message={{ ...group.messages[0], _working: group.messages }}
                  isSelf={group.messages[0].from_uid === user.uid}
                  isGroup={isGroup}
                  senderName={group.sender.name}
                  senderAvatarUrl={group.sender.avatarUrl}
                  senderIsBot={group.sender.isBot}
                  showThinking={showThinking}
                  isConsecutive={group.isConsecutive}
                />
              </div>
            );
          }
          return (
            <ChatMessage
              key={group.message.id || i}
              message={group.message}
              isSelf={group.message.from_uid === user.uid}
              isGroup={isGroup}
              senderName={group.sender.name}
              senderAvatarUrl={group.sender.avatarUrl}
              senderIsBot={group.sender.isBot}
              replyMessage={getReplyMessage(group.message.reply_to)}
              onReply={() => setReplyTo(group.message)}
              showThinking={showThinking}
              isConsecutive={group.isConsecutive}
            />
          );
        })}
          {peerTyping && (
            <div style={{padding:'4px 20px', fontSize:'12px', color:'var(--v3-text-muted)'}}>
              {t('typing')}...
            </div>
          )}
          <div ref={bottomRef} />
        </div>
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

      <div className="v3-composer">
        {/* @mention picker */}
        {showMentionPicker && isGroup && filteredMembers.length > 0 && (
          <div className="oc-mention-picker" style={{position:'absolute', bottom: '100%', left: 20, zIndex: 100}}>
            {filteredMembers.map((m) => (
              <div
                key={m.user_id}
                className="oc-mention-item"
                onClick={() => insertMention(m)}
                style={{display:'flex', alignItems:'center', padding:'8px', cursor:'pointer', background:'var(--v3-bg-app)', border:'1px solid var(--v3-border)'}}
              >
                <Avatar name={m.display_name || m.username} src={m.avatar_url} size={24} isBot={m.is_bot} style={{marginRight:8}} />
                <span>{m.display_name || m.username}</span>
              </div>
            ))}
          </div>
        )}

        <div className="v3-composer-box">
          
          <div className="v3-composer-toolbar">
            <button className="v3-tool" onClick={() => imageInputRef.current?.click()} title="Upload Image">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>
            </button>
            <button className="v3-tool" onClick={() => fileInputRef.current?.click()} title="Upload File">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"></path></svg>
            </button>
            <div style={{flex:1}}></div>
            <button className="v3-tool" style={{ fontWeight: 600 }} onClick={() => { if(isGroup && textareaRef.current) { const pos = textareaRef.current.selectionStart; setInput(input.slice(0,pos) + '@' + input.slice(pos)); textareaRef.current.focus(); } }} title="Mention">@</button>
          </div>

          <textarea
            ref={textareaRef}
            className="v3-composer-input"
            rows={1}
            placeholder={t('chat_input_placeholder')}
            value={input}
            onChange={handleInputChange}
            onKeyDown={handleKeyDown}
          />
          
          <div className="v3-composer-footer">
            <span><strong>Return</strong> to send, <strong>Shift + Return</strong> to add a new line</span>
            <button
              className="v3-send"
              disabled={!input.trim()}
              onClick={handleSend}
            >
              <svg width="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3"><path d="M22 2L11 13M22 2l-7 20-4-9-9-4 20-7z"></path></svg>
              <span>{t('chat_send')}</span>
            </button>
          </div>
          
          <input ref={imageInputRef} type="file" accept="image/*" style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'image')} />
          <input ref={fileInputRef} type="file" style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'file')} />
        </div>
      </div>
      {showGroupSettings && isGroup && groupId && (
        <GroupSettings
          groupId={groupId}
          onClose={() => setShowGroupSettings(false)}
          onSaved={handleGroupSaved}
        />
      )}
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

function mergeMessages(primary, secondary) {
  const byId = new Map();
  [...primary, ...secondary].forEach((message) => {
    byId.set(message.id, message);
  });
  // Sort by seq_id (now unified for all messages)
  return Array.from(byId.values()).sort((a, b) => {
    const aSeq = a.seq_id || a.id;
    const bSeq = b.seq_id || b.id;
    return aSeq - bSeq;
  });
}
