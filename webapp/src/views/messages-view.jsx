import React, { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { CheckCircle2, ChevronDown, ChevronRight, Circle, CircleDot, MoreHorizontal, SendHorizontal, Square } from 'lucide-react';
import { api, wsSendMessage, wsSendStreamCancel, wsSendTyping, wsSendRead, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import ChatMessage from '../widgets/chat-message';
import GroupSettings from '../widgets/group-settings';
import Avatar from '../widgets/avatar';

const PAGE_SIZE = 50;
const TYPING_TIMEOUT_MS = 10000;
const WORKING_MESSAGE_TYPES = new Set(['thinking', 'tool_use', 'tool_result']);
const WORKING_TEXT_PREFIX = 'AI文本:';
const MAX_ATTACHMENT_SIZE_MB = 300;
const MAX_ATTACHMENT_SIZE = MAX_ATTACHMENT_SIZE_MB * 1024 * 1024;
const MAX_DROPPED_FILES = 200;
const HISTORY_AUTO_LOAD_THRESHOLD = 120;
const STICK_TO_BOTTOM_THRESHOLD = 96;
const IMAGE_EXTENSIONS = new Set(['.jpg', '.jpeg', '.png', '.gif', '.webp', '.bmp', '.svg', '.heic', '.heif']);

export default function MessagesView({ topic, topicName, user, isGroup, groupId, topicAvatarUrl, onTopicUpdated }) {
  const [input, setInput] = useState('');
  const [messages, setMessages] = useState([]);
  const [pendingAttachments, setPendingAttachments] = useState([]);
  const [isUploadingAttachment, setIsUploadingAttachment] = useState(false);
  const [isDragActive, setIsDragActive] = useState(false);
  const [peerTyping, setPeerTyping] = useState(false);
  const [runtimePlan, setRuntimePlan] = useState(null);
  const [members, setMembers] = useState([]);
  const [groupInfo, setGroupInfo] = useState(null);
  const [peerProfile, setPeerProfile] = useState(null);
  const [showMentionPicker, setShowMentionPicker] = useState(false);
  const [mentionFilter, setMentionFilter] = useState('');
  const [replyTo, setReplyTo] = useState(null);
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [showGroupSettings, setShowGroupSettings] = useState(false);
  const [isStopRequested, setIsStopRequested] = useState(false);
  const [showThinking, setShowThinking] = useState(() => {
    const saved = localStorage.getItem('cc_show_thinking');
    return saved === null ? true : saved === 'true';
  });
  const bottomRef = useRef(null);
  const lastTypingSent = useRef(0);
  const peerTypingTimer = useRef(null);
  const timelineRef = useRef(null);
  const previousScrollRef = useRef(null);
  const stickToBottomRef = useRef(true);
  const fileInputRef = useRef(null);
  const imageInputRef = useRef(null);
  const textareaRef = useRef(null);
  const dragDepthRef = useRef(0);
  const runtimePlanRef = useRef(null);
  const runtimePlanClearTimer = useRef(null);
  const historyOffsetRef = useRef(0);
  const hasMoreHistoryRef = useRef(false);
  const loadingOlderRef = useRef(false);
  const activeTopicRef = useRef(topic);

  const resizeComposerInput = useCallback(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    const maxHeight = 220;
    textarea.style.height = 'auto';
    const nextHeight = Math.min(Math.max(textarea.scrollHeight, 44), maxHeight);
    textarea.style.height = `${nextHeight}px`;
    textarea.style.overflowY = textarea.scrollHeight > maxHeight ? 'auto' : 'hidden';
  }, []);

  useEffect(() => {
    resizeComposerInput();
  }, [input, resizeComposerInput]);

  const clearRuntimePlan = useCallback(() => {
    if (runtimePlanClearTimer.current) {
      clearTimeout(runtimePlanClearTimer.current);
      runtimePlanClearTimer.current = null;
    }
    runtimePlanRef.current = null;
    setRuntimePlan(null);
  }, []);

  const applyRuntimePlan = useCallback((plan) => {
    if (runtimePlanClearTimer.current) {
      clearTimeout(runtimePlanClearTimer.current);
      runtimePlanClearTimer.current = null;
    }
    if (!plan || !Array.isArray(plan.steps) || plan.steps.length === 0) {
      runtimePlanRef.current = null;
      setRuntimePlan(null);
      return;
    }
    runtimePlanRef.current = plan;
    setRuntimePlan(plan);
  }, []);

  const clearCompletedRuntimePlanSoon = useCallback(() => {
    if (!isRuntimePlanComplete(runtimePlanRef.current)) return;
    if (runtimePlanClearTimer.current) {
      clearTimeout(runtimePlanClearTimer.current);
    }
    runtimePlanClearTimer.current = setTimeout(() => {
      runtimePlanRef.current = null;
      runtimePlanClearTimer.current = null;
      setRuntimePlan(null);
    }, 1800);
  }, []);

  useEffect(() => () => {
    if (runtimePlanClearTimer.current) {
      clearTimeout(runtimePlanClearTimer.current);
    }
  }, []);

  // Load message history and group members when topic changes
  useEffect(() => {
    if (!topic) return;
    activeTopicRef.current = topic;
    setInput('');
    setMessages([]);
    setPendingAttachments([]);
    setIsUploadingAttachment(false);
    setIsDragActive(false);
    dragDepthRef.current = 0;
    setPeerTyping(false);
    setShowMentionPicker(false);
    setMentionFilter('');
    clearRuntimePlan();
    setReplyTo(null);
    setMembers([]);
    setGroupInfo(null);
    setPeerProfile(null);
    historyOffsetRef.current = 0;
    hasMoreHistoryRef.current = false;
    loadingOlderRef.current = false;
    stickToBottomRef.current = true;
    setHasMoreHistory(false);
    setLoadingOlder(false);
    setIsStopRequested(false);
    loadHistory();
    if (isGroup && groupId) {
      loadGroupMembers();
    } else {
      loadPeerProfile();
    }
  }, [topic]);

  useEffect(() => {
    const preventBrowserFileOpen = (event) => {
      if (hasFileDrag(event.dataTransfer)) {
        event.preventDefault();
      }
    };
    const resetDragState = () => {
      dragDepthRef.current = 0;
      setIsDragActive(false);
    };

    window.addEventListener('dragover', preventBrowserFileOpen);
    window.addEventListener('drop', preventBrowserFileOpen);
    window.addEventListener('dragend', resetDragState);
    window.addEventListener('blur', resetDragState);
    return () => {
      window.removeEventListener('dragover', preventBrowserFileOpen);
      window.removeEventListener('drop', preventBrowserFileOpen);
      window.removeEventListener('dragend', resetDragState);
      window.removeEventListener('blur', resetDragState);
    };
  }, []);

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
    }
  };

  // Listen for incoming WebSocket messages
  useEffect(() => {
    const unsub = onWSMessage((msg) => {
      // New message from server
      if (msg.data && msg.data.topic === topic) {
        if (isStreamCancel(msg.data)) {
          const streamId = getStreamId(msg.data);
          if (streamId) {
            setMessages((prev) => prev.filter((message) => message._stream_id !== streamId));
          }
          clearRuntimePlan();
          return;
        }

        const incomingRuntimePlan = runtimePlanFromMessage(msg.data);
        if (incomingRuntimePlan) {
          applyRuntimePlan(incomingRuntimePlan);
          if (isRuntimePlanComplete(incomingRuntimePlan)) {
            clearCompletedRuntimePlanSoon();
          }
          return;
        }

        if (isStreamDelta(msg.data)) {
          const fromUid = parseUid(msg.data.from);
          const streamId = getStreamId(msg.data);
          const delta = streamDeltaText(msg.data.content);
          if (streamId && delta) {
            setMessages((prev) => upsertStreamingMessage(prev, {
              streamId,
              topic,
              fromUid,
              content: delta,
              metadata: msg.data.metadata || null,
            }));
          }
          return;
        }

        const fromUid = parseUid(msg.data.from);
        const serverMsg = normalizeIncomingMessage({
          id: msg.data.seq_id || msg.data.seq,
          seq_id: msg.data.seq_id || msg.data.seq,
          topic_id: msg.data.topic,
          from_uid: fromUid,
          from_name: msg.data.from,
          content: msg.data.content,
          content_blocks: msg.data.content_blocks,
          mode: msg.data.mode,
          role: msg.data.role,
          type: msg.data.type,
          metadata: msg.data.metadata || null,
          msg_type: msg.data.msg_type || msg.data.type || 'text',
          reply_to: msg.data.reply_to || 0,
          created_at: new Date().toISOString(),
        });

        setMessages((prev) => {
          const streamId = getStreamId(serverMsg);
          if (streamId) {
            const streamIdx = prev.findIndex((m) => m._stream_id === streamId);
            if (streamIdx !== -1) {
              const next = [...prev];
              next[streamIdx] = serverMsg;
              return mergeMessages([], next);
            }
          }
          // Deduplicate by seq ID
          if (prev.some((m) => m.id === serverMsg.id)) return prev;
          // If this is our own message echoed back, replace the optimistic entry
          if (fromUid === user.uid) {
            const serverContentKey = getComparableContent(serverMsg.content);
            const pendingIdx = prev.findIndex((m) => (
              m._pending && getComparableContent(m.content) === serverContentKey
            ));
            if (pendingIdx !== -1) {
              const next = [...prev];
              next[pendingIdx] = serverMsg;
              return next;
            }
          }
          return mergeMessages(prev, [serverMsg]);
        });
        if (fromUid === user.uid && isFinalTextMessage(serverMsg)) {
          clearRuntimePlan();
        } else if (fromUid !== user.uid && isFinalTextMessage(serverMsg)) {
          clearRuntimePlan();
        }
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
    const timeline = timelineRef.current;
    if (!timeline) return;

    if (previousScrollRef.current) {
      // Anchoring condition: We just prepended older history.
      const { scrollHeight, scrollTop } = previousScrollRef.current;
      const newScrollHeight = timeline.scrollHeight;
      timeline.scrollTop = scrollTop + (newScrollHeight - scrollHeight);
      previousScrollRef.current = null; // Clear atomic lock
      stickToBottomRef.current = isTimelineNearBottom(timeline);
    } else if (stickToBottomRef.current) {
      // Only follow fresh messages while the user is already near the bottom.
      bottomRef.current?.scrollIntoView({ behavior: 'auto' });
    }
  }, [messages, runtimePlan, peerTyping]);

  const loadHistory = async () => {
    try {
      const res = await api.getMessages(topic, PAGE_SIZE, 0, true);
      if (res.messages) {
        const { visibleMessages } = normalizeHistoryMessages(res.messages);
        setMessages(visibleMessages);
        historyOffsetRef.current = (res.messages || []).length;
        setHasMoreHistory((res.messages || []).length === PAGE_SIZE);
        hasMoreHistoryRef.current = (res.messages || []).length === PAGE_SIZE;
      }
    } catch (e) {
    }
  };

  const loadOlderHistory = useCallback(async () => {
    if (loadingOlderRef.current || !hasMoreHistoryRef.current) return;
    
    // Capture the absolute scroll geometry BEFORE rendering the older batch
    if (timelineRef.current) {
      previousScrollRef.current = {
        scrollHeight: timelineRef.current.scrollHeight,
        scrollTop: timelineRef.current.scrollTop,
      };
    }
    
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    try {
      const res = await api.getMessages(topic, PAGE_SIZE, historyOffsetRef.current, true);
      const { visibleMessages } = normalizeHistoryMessages(res.messages);
      setMessages((prev) => mergeMessages(visibleMessages, prev));
      historyOffsetRef.current += (res.messages || []).length;
      const hasMore = (res.messages || []).length === PAGE_SIZE;
      hasMoreHistoryRef.current = hasMore;
      setHasMoreHistory(hasMore);
    } catch (e) {
    } finally {
      loadingOlderRef.current = false;
      setLoadingOlder(false);
    }
  }, [topic]);

  useEffect(() => {
    const el = timelineRef.current;
    if (!el || !hasMoreHistory || loadingOlder) return;
    if (el.scrollHeight <= el.clientHeight + HISTORY_AUTO_LOAD_THRESHOLD) {
      loadOlderHistory();
    }
  }, [messages.length, hasMoreHistory, loadingOlder, loadOlderHistory]);

  const activeBotWorking = useMemo(() => {
    let lastWorkingIndex = -1;
    let lastBotTextIndex = -1;

    messages.forEach((message, index) => {
      if (message.from_uid === user.uid) return;
      if (isWorkingMessage(message)) {
        lastWorkingIndex = index;
        return;
      }
      const type = message.type || message.msg_type || '';
      if (type === 'text' && typeof message.content === 'string' && message.content.trim()) {
        lastBotTextIndex = index;
      }
    });

    return lastWorkingIndex > lastBotTextIndex;
  }, [messages, user.uid]);

  useEffect(() => {
    if (!activeBotWorking) {
      setIsStopRequested(false);
    }
  }, [activeBotWorking]);

  const hasComposerDraft = input.trim().length > 0 || pendingAttachments.length > 0;
  const showStopButton = activeBotWorking && !hasComposerDraft && !isUploadingAttachment;

  const finalizeOptimisticMessage = useCallback((tempId, result) => {
    if (!result || (!result.seq_id && !result.id)) return;
    setMessages((prev) => {
      const idx = prev.findIndex((message) => message.id === tempId);
      if (idx === -1) return prev;
      const next = [...prev];
      next[idx] = {
        ...next[idx],
        id: result.seq_id || result.id,
        seq_id: result.seq_id || result.id,
        _pending: false,
      };
      return next.sort((a, b) => (a.seq_id || a.id) - (b.seq_id || b.id));
    });
  }, []);

  const removeOptimisticMessage = useCallback((tempId) => {
    setMessages((prev) => prev.filter((message) => message.id !== tempId));
  }, []);

  const handleSend = useCallback(async () => {
    const text = input.trim();
    if (!text && pendingAttachments.length === 0) return;
    if (isUploadingAttachment) return;

    const sendTopic = topic;
    clearRuntimePlan();
    const attachmentsToSend = pendingAttachments;
    setInput('');
    const currentReplyTo = replyTo;
    setReplyTo(null);
    setPendingAttachments([]);

    const contentBlocks = buildAtomicContentBlocks(text, attachmentsToSend);
    const displayContent = text || summarizeAttachments(attachmentsToSend);
    const payload = attachmentsToSend.length > 0
      ? {
          type: 'text',
          content: displayContent,
          content_blocks: contentBlocks,
        }
      : text;

    const tempId = Date.now();
    stickToBottomRef.current = true;
    setMessages((prev) => mergeMessages(prev, [{
      id: tempId,
      seq_id: tempId,
      topic_id: sendTopic,
      from_uid: user.uid,
      content: displayContent,
      content_blocks: attachmentsToSend.length > 0 ? contentBlocks : undefined,
      type: 'text',
      msg_type: 'text',
      reply_to: currentReplyTo ? currentReplyTo.id : 0,
      created_at: new Date().toISOString(),
      _pending: true,
    }]));

    try {
      const result = await api.sendMessage(sendTopic, payload, currentReplyTo ? currentReplyTo.id : undefined);
      finalizeOptimisticMessage(tempId, result);
    } catch (err) {
      removeOptimisticMessage(tempId);
      if (activeTopicRef.current !== sendTopic) return;
      setInput(text);
      setPendingAttachments(attachmentsToSend);
      setReplyTo(currentReplyTo);
    }
  }, [clearRuntimePlan, finalizeOptimisticMessage, input, isUploadingAttachment, pendingAttachments, removeOptimisticMessage, replyTo, topic, user.uid]);

  const handleStopGeneration = useCallback(async () => {
    if (!activeBotWorking || isStopRequested) return;
    setIsStopRequested(true);
    try {
      await wsSendStreamCancel(topic);
    } catch (err) {
      setIsStopRequested(false);
    }
  }, [activeBotWorking, isStopRequested, topic]);

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

  const uploadAttachmentFile = async (file, requestedType) => {
    const uploadTopic = activeTopicRef.current;
    const type = inferAttachmentType(file, requestedType);
    if (file.size > MAX_ATTACHMENT_SIZE) {
      window.alert(`文件过大：${(file.size / 1024 / 1024).toFixed(1)}MB。当前最多支持 ${MAX_ATTACHMENT_SIZE_MB}MB。`);
      return false;
    }

    try {
      setIsUploadingAttachment(true);
      const formData = new FormData();
      formData.append('file', file);
      const res = await fetch(`${process.env.REACT_APP_API_BASE || ''}/api/upload?type=${type}`, {
        method: 'POST',
        headers: { 'Authorization': `Bearer ${localStorage.getItem('oc_token')}` },
        body: formData,
      });
      const raw = await res.text();
      let data = {};
      if (raw) {
        try {
          data = JSON.parse(raw);
        } catch (parseErr) {
          if (!res.ok) {
            throw new Error(`Upload failed with HTTP ${res.status}`);
          }
          throw new Error('Upload failed: invalid server response');
        }
      }
      if (!res.ok) throw new Error(data.error || `Upload failed with HTTP ${res.status}`);

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

      const attachment = {
        type,
        name: data.name,
        size: data.size,
        content,
      };
      if (activeTopicRef.current !== uploadTopic) return attachment;
      setPendingAttachments((prev) => [...prev, attachment]);
      setTimeout(() => textareaRef.current?.focus(), 0);
      return attachment;
    } catch (err) {
      // Fallback: If the server returns a raw Nginx HTML 413 instead of JSON, 
      // res.json() will throw a generic SyntaxError. We explicitly alert the user.
      const errorMsg = err.message.includes('Unexpected token') || err.message.includes('JSON')
        ? 'Upload failed: Server rejected the file (likely Payload Too Large / 413).'
        : `Upload failed: ${err.message}`;
      window.alert(errorMsg);
      return null;
    } finally {
      setIsUploadingAttachment(false);
    }
  };

  const uploadAttachmentFiles = async (files, requestedType) => {
    const fileList = Array.from(files || []).filter(Boolean);
    if (fileList.length === 0) return;
    for (const file of fileList.slice(0, MAX_DROPPED_FILES)) {
      const uploaded = await uploadAttachmentFile(file, requestedType);
      if (!uploaded) break;
    }
  };

  const handleFileUpload = async (e, type) => {
    const files = Array.from(e.target.files || []);
    e.target.value = '';
    if (!files || files.length === 0) return;
    await uploadAttachmentFiles(files, type);
  };

  const openAttachmentPicker = (inputRef) => {
    if (isUploadingAttachment) return;
    if (inputRef.current) {
      inputRef.current.value = '';
      inputRef.current.click();
    }
  };

  const handleDragEnter = (e) => {
    if (!hasFileDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    dragDepthRef.current += 1;
    setIsDragActive(true);
  };

  const handleDragOver = (e) => {
    if (!hasFileDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = 'copy';
    setIsDragActive(true);
  };

  const handleDragLeave = (e) => {
    if (!hasFileDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) {
      setIsDragActive(false);
    }
  };

  const handleDrop = async (e) => {
    if (!hasFileDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    dragDepthRef.current = 0;
    setIsDragActive(false);

    if (isUploadingAttachment) {
      window.alert('Upload in progress. Please wait before dropping another file.');
      return;
    }
    const files = await collectDroppedFiles(e.dataTransfer);
    if (files.length === 0) {
      window.alert('No uploadable files were found in this drop.');
      return;
    }

    await uploadAttachmentFiles(files);
  };

  const handlePaste = async (e) => {
    const files = collectClipboardFiles(e.clipboardData);
    if (files.length === 0) return;

    e.preventDefault();
    e.stopPropagation();

    if (isUploadingAttachment) {
      window.alert('Upload in progress. Please wait before pasting another file.');
      return;
    }
    await uploadAttachmentFiles(files);
  };

  // Find the display name for a uid in group context
  const getMemberName = (fromUid) => {
    if (!isGroup || !members.length) return null;
    const m = members.find((mem) => mem.user_id === fromUid);
    return m ? (m.display_name || m.username) : `usr${fromUid}`;
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


  const messageById = useMemo(() => {
    const map = new Map();
    messages.forEach((message) => {
      map.set(message.id, message);
    });
    return map;
  }, [messages]);

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

      if (isWorkingMessage(msg)) {
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
        groups.push({
          type: 'text',
          message: msg,
          sender: getSender(msg),
          replyMessage: msg.reply_to ? (messageById.get(msg.reply_to) || null) : null,
          isConsecutive: textIsConsecutive,
        });
        prevSenderUid = senderUid;
        prevTime = msgTime;
      }
    });

    if (currentWorking) {
      groups.push(currentWorking);
    }

    return groups;
  }, [messages, user.uid, isGroup, memberMap, messageById, peerProfile, topicName, topic, topicAvatarUrl]);

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
    stickToBottomRef.current = isTimelineNearBottom(el);
    if (el.scrollTop <= HISTORY_AUTO_LOAD_THRESHOLD) {
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
              <MoreHorizontal size={16} />
            </button>
          )}
        </div>
      </div>
      <div
        className={`v3-timeline${isDragActive ? ' is-drag-active' : ''}`}
        ref={timelineRef}
        onScroll={handleTimelineScroll}
        onDragEnter={handleDragEnter}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        <div style={{ maxWidth: 900, margin: '0 auto', width: '100%', display: 'flex', flexDirection: 'column' }}>
          <div className="v3-date-divider">
            <span>Chat History</span>
          </div>
        
        {loadingOlder && (
          <div className="oc-history-load" style={{textAlign:'center', padding:'10px 0 24px 0'}}>
            <span>{t('loading')}</span>
          </div>
        )}
        
        {groupedMessages.map((group, i) => {
          if (group.type === 'working') {
            if (!showThinking) return null;
            return (
              <div key={group.messages[0].id || i} className="oc-working-group">
                <ChatMessage
                  message={group.messages[0]}
                  workingMessages={group.messages}
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
              replyMessage={group.replyMessage}
              onReply={() => setReplyTo(group.message)}
              showThinking={showThinking}
              isConsecutive={group.isConsecutive}
            />
          );
        })}
          {runtimePlan && <RuntimePlanCard plan={runtimePlan} />}
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

      <div
        className={`v3-composer${isDragActive ? ' is-drag-active' : ''}`}
        onDragEnter={handleDragEnter}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
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
          {isDragActive && (
            <div className="v3-drop-overlay" aria-hidden="true">
              <div className="v3-drop-title">Drop files to upload</div>
              <div className="v3-drop-subtitle">Images, files, and folders are supported. The attachment will wait here before sending.</div>
            </div>
          )}
          
          <div className="v3-composer-toolbar">
            <button
              className="v3-tool"
              onClick={() => openAttachmentPicker(imageInputRef)}
              title="Upload Image"
              aria-label="Upload Image"
              disabled={isUploadingAttachment}
              type="button"
            >
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="18" height="18" rx="2" ry="2"></rect><circle cx="8.5" cy="8.5" r="1.5"></circle><polyline points="21 15 16 10 5 21"></polyline></svg>
            </button>
            <button
              className="v3-tool"
              onClick={() => openAttachmentPicker(fileInputRef)}
              title="Upload File"
              aria-label="Upload File"
              disabled={isUploadingAttachment}
              type="button"
            >
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round"><path d="M21.44 11.05l-9.19 9.19a6 6 0 0 1-8.49-8.49l9.19-9.19a4 4 0 0 1 5.66 5.66l-9.2 9.19a2 2 0 0 1-2.83-2.83l8.49-8.48"></path></svg>
            </button>
            <div style={{flex:1}}></div>
            <button className="v3-tool" style={{ fontWeight: 600 }} onClick={() => { if(isGroup && textareaRef.current) { const pos = textareaRef.current.selectionStart; setInput(input.slice(0,pos) + '@' + input.slice(pos)); textareaRef.current.focus(); } }} title="Mention" type="button">@</button>
          </div>

          {activeBotWorking && (
            <div className="v3-live-input-status" role="status">
              {isStopRequested
                ? '已请求 CatsCo 停止当前工作。'
                : 'CatsCo 正在处理。没有补充内容时可点停止；输入内容后仍可继续发送。'}
            </div>
          )}

          <textarea
            ref={textareaRef}
            className="v3-composer-input"
            rows={1}
            placeholder={t('chat_input_placeholder')}
            value={input}
            onChange={handleInputChange}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
          />

          {(isUploadingAttachment || pendingAttachments.length > 0) && (
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, padding: '10px 12px', marginTop: 10, borderRadius: 10, background: 'rgba(255,255,255,0.04)', border: '1px solid var(--v3-border)', color: 'var(--v3-text-main)' }}>
              <div style={{ minWidth: 0 }}>
                <div style={{ fontSize: 12, fontWeight: 600, marginBottom: 2 }}>
                  {isUploadingAttachment ? 'Uploading attachment...' : `${pendingAttachments.length} attachment${pendingAttachments.length === 1 ? '' : 's'} ready to send`}
                </div>
                {!isUploadingAttachment && pendingAttachments.length > 0 && (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                    {pendingAttachments.map((attachment, index) => (
                      <div key={`${attachment.name}-${index}`} style={{ fontSize: 12, color: 'var(--v3-text-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {attachment.type === 'image' ? 'Image' : 'File'}: {attachment.name}
                        {attachment.size ? ` • ${formatFileSize(attachment.size)}` : ''}
                      </div>
                    ))}
                  </div>
                )}
              </div>
              {pendingAttachments.length > 0 && !isUploadingAttachment && (
                <button
                  className="v3-action-btn"
                  aria-label="Remove attachments"
                  onClick={() => setPendingAttachments([])}
                  type="button"
                >
                  x
                </button>
              )}
            </div>
          )}
          
          <div className="v3-composer-footer">
            <span><strong>Return</strong> to send, <strong>Shift + Return</strong> to add a new line</span>
            <button
className={`v3-send${showStopButton ? ' stop' : ''}`}
              disabled={showStopButton ? isStopRequested : isUploadingAttachment || (!input.trim() && pendingAttachments.length === 0)}
              onClick={showStopButton ? handleStopGeneration : handleSend}
              aria-label={showStopButton ? '停止当前工作' : t('chat_send')}
              title={showStopButton ? '停止当前工作' : t('chat_send')}
              type="button"
            >
              {showStopButton
                ? <Square size={12} fill="currentColor" strokeWidth={2.5} />
                : <SendHorizontal size={13} strokeWidth={2.5} />}
              <span>{showStopButton ? (isStopRequested ? '停止中' : '停止') : t('chat_send')}</span>
            </button>
          </div>
          
          <input ref={imageInputRef} type="file" accept="image/*" multiple style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'image')} />
          <input ref={fileInputRef} type="file" multiple style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'file')} />
        </div>
      </div>
      {showGroupSettings && isGroup && groupId && (
        <GroupSettings
          groupId={groupId}
          currentUser={user}
          onClose={() => setShowGroupSettings(false)}
          onSaved={handleGroupSaved}
        />
      )}
    </>
  );
}

function hasFileDrag(dataTransfer) {
  if (!dataTransfer?.types) return false;
  return Array.from(dataTransfer.types).includes('Files');
}

function inferAttachmentType(file, requestedType) {
  if (requestedType) return requestedType;
  if (file?.type?.toLowerCase().startsWith('image/')) return 'image';
  const name = file?.name?.toLowerCase() || '';
  const extension = name.includes('.') ? name.slice(name.lastIndexOf('.')) : '';
  return IMAGE_EXTENSIONS.has(extension) ? 'image' : 'file';
}

function buildAtomicContentBlocks(text, attachments) {
  const blocks = [];
  if (text) {
    blocks.push({ type: 'text', text });
  }
  for (const attachment of attachments || []) {
    const payload = attachment?.content?.payload;
    if (!payload) continue;
    blocks.push({
      type: attachment.type === 'image' ? 'image' : 'file',
      payload,
    });
  }
  return blocks;
}

function summarizeAttachments(attachments) {
  const list = attachments || [];
  if (list.length === 0) return '';
  if (list.length === 1) {
    const item = list[0];
    return `[${item.type === 'image' ? '图片' : '文件'}] ${item.name || 'attachment'}`;
  }
  return `[附件] ${list.map((item) => item.name || 'attachment').join(', ')}`;
}

async function collectDroppedFiles(dataTransfer) {
  const files = [];
  const addFile = (file) => {
    if (file && files.length < MAX_DROPPED_FILES) {
      files.push(file);
    }
  };

  const items = Array.from(dataTransfer?.items || []);
  if (items.length > 0) {
    for (const item of items) {
      if (files.length >= MAX_DROPPED_FILES) break;
      if (item.kind !== 'file') continue;

      const entry = typeof item.webkitGetAsEntry === 'function' ? item.webkitGetAsEntry() : null;
      if (entry) {
        const entryFiles = await readEntryFiles(entry, MAX_DROPPED_FILES - files.length);
        entryFiles.forEach(addFile);
      } else if (typeof item.getAsFile === 'function') {
        addFile(item.getAsFile());
      }
    }
  }

  if (files.length === 0) {
    Array.from(dataTransfer?.files || []).forEach(addFile);
  }

  return files;
}

function collectClipboardFiles(clipboardData) {
  const files = [];
  const addFile = (file) => {
    if (file && files.length < MAX_DROPPED_FILES) {
      files.push(file);
    }
  };

  const items = Array.from(clipboardData?.items || []);
  if (items.length > 0) {
    for (const item of items) {
      if (files.length >= MAX_DROPPED_FILES) break;
      if (item.kind !== 'file') continue;
      if (typeof item.getAsFile === 'function') {
        addFile(item.getAsFile());
      }
    }
  }

  if (files.length === 0) {
    Array.from(clipboardData?.files || []).forEach(addFile);
  }

  return files;
}

async function readEntryFiles(entry, limit) {
  if (!entry || limit <= 0) return [];
  if (entry.isFile) {
    return new Promise((resolve) => {
      entry.file(
        (file) => resolve(file ? [file] : []),
        () => resolve([]),
      );
    });
  }

  if (!entry.isDirectory) return [];

  const reader = entry.createReader();
  const entries = await readDirectoryEntries(reader);
  const files = [];
  for (const child of entries) {
    if (files.length >= limit) break;
    const childFiles = await readEntryFiles(child, limit - files.length);
    files.push(...childFiles);
  }
  return files;
}

function readDirectoryEntries(reader) {
  return new Promise((resolve) => {
    const entries = [];
    const readBatch = () => {
      reader.readEntries(
        (batch) => {
          if (!batch.length) {
            resolve(entries);
            return;
          }
          entries.push(...batch);
          readBatch();
        },
        () => resolve(entries),
      );
    };
    readBatch();
  });
}

function normalizeIncomingMessage(message) {
  const normalized = { ...message };
  normalized.content_blocks = Array.isArray(message?.content_blocks) ? message.content_blocks : [];
  normalized.metadata = message?.metadata || null;
  normalized.msg_type = message?.msg_type || 'text';

  const runtimePlan = normalizeRuntimePlan(message?.content);
  let inferredType = runtimePlan ? 'runtime_plan' : message?.type;
  if (!inferredType) {
    inferredType = inferWorkingTypeFromBlocks(normalized.content_blocks);
  }
  if (!inferredType && message?.content && typeof message.content === 'object' && message.content.type) {
    inferredType = message.content.type;
  }
  if (!inferredType && typeof message?.content === 'string') {
    try {
      const parsed = JSON.parse(message.content);
      if (parsed && typeof parsed === 'object' && parsed.type) {
        inferredType = parsed.type;
      }
    } catch (err) {
      // plain text payload
    }
  }
  if (!inferredType) {
    inferredType = normalized.msg_type || 'text';
  }

  normalized.type = inferredType;
  return normalized;
}

function isStreamDelta(data) {
  return data?.type === 'stream_delta' || data?.metadata?.stream_event === 'delta';
}

function isStreamCancel(data) {
  return data?.type === 'stream_cancel' || data?.metadata?.stream_event === 'cancel';
}

function runtimePlanFromMessage(data) {
  if (!data) return null;
  const explicitPlan = data.type === 'runtime_plan' || data.msg_type === 'runtime_plan';
  const plan = normalizeRuntimePlan(data.content);
  if (plan) return plan;
  return explicitPlan ? normalizeRuntimePlan(data.payload || data.metadata?.plan || data) : null;
}

function normalizeRuntimePlan(content) {
  let value = content;
  if (typeof value === 'string') {
    try {
      value = JSON.parse(value);
    } catch (err) {
      return null;
    }
  }
  if (value && typeof value === 'object') {
    if (value.type === 'runtime_plan') {
      value = value.payload || value.plan || value.content || value;
    } else if (!Array.isArray(value.steps) && value.payload && Array.isArray(value.payload.steps)) {
      value = value.payload;
    } else if (!Array.isArray(value.steps) && value.plan && Array.isArray(value.plan.steps)) {
      value = value.plan;
    }
  }
  if (!value || typeof value !== 'object' || !Array.isArray(value.steps)) {
    return null;
  }
  const steps = value.steps
    .map((step) => ({
      text: String(step?.text || '').trim(),
      status: normalizePlanStatus(step?.status),
    }))
    .filter((step) => step.text);
  return {
    revision: Number(value.revision || 0),
    updatedAt: Number(value.updatedAt || value.updated_at || Date.now()),
    steps,
  };
}

function normalizePlanStatus(status) {
  if (status === 'completed' || status === 'in_progress' || status === 'pending') {
    return status;
  }
  return 'pending';
}

function isRuntimePlanComplete(plan) {
  return Boolean(
    plan &&
    Array.isArray(plan.steps) &&
    plan.steps.length > 0 &&
    plan.steps.every((step) => step.status === 'completed'),
  );
}

function normalizeHistoryMessages(rawMessages) {
  const visibleMessages = [];
  for (const raw of rawMessages || []) {
    const normalized = normalizeIncomingMessage(raw);
    if (runtimePlanFromMessage(normalized)) {
      continue;
    }
    visibleMessages.push(normalized);
  }
  return { visibleMessages };
}

function isFinalTextMessage(message) {
  const type = message?.type || message?.msg_type || '';
  if (type !== 'text') return false;
  if (isWorkingTextMessage(message)) return false;
  return typeof message?.content === 'string' && message.content.trim().length > 0;
}

function RuntimePlanCard({ plan }) {
  const [open, setOpen] = useState(false);
  if (!plan || !Array.isArray(plan.steps) || plan.steps.length === 0) return null;

  const completed = plan.steps.filter((step) => step.status === 'completed').length;
  const current = plan.steps.find((step) => step.status === 'in_progress') || plan.steps.find((step) => step.status === 'pending');

  return (
    <div className="v3-runtime-plan-card" role="status">
      <button className="v3-runtime-plan-toggle" type="button" onClick={() => setOpen(!open)}>
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        <span className="v3-runtime-plan-title">计划</span>
        <span className="v3-runtime-plan-count">{completed}/{plan.steps.length}</span>
        {!open && current && <span className="v3-runtime-plan-current">{current.text}</span>}
      </button>
      {open && (
        <div className="v3-runtime-plan-steps">
          {plan.steps.map((step, index) => (
            <div className={`v3-runtime-plan-step ${step.status}`} key={`${index}-${step.text}`}>
              {step.status === 'completed'
                ? <CheckCircle2 size={14} />
                : step.status === 'in_progress'
                  ? <CircleDot size={14} />
                  : <Circle size={14} />}
              <span className="v3-runtime-plan-step-text">{step.text}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function getStreamId(message) {
  const id = message?.metadata?.stream_id || message?._stream_id;
  return typeof id === 'string' && id.trim() ? id.trim() : '';
}

function isTimelineNearBottom(el) {
  if (!el) return true;
  return el.scrollHeight - el.scrollTop - el.clientHeight <= STICK_TO_BOTTOM_THRESHOLD;
}

function streamDeltaText(content) {
  if (typeof content === 'string') return content;
  if (content == null) return '';
  if (typeof content === 'object' && typeof content.text === 'string') return content.text;
  return String(content);
}

function upsertStreamingMessage(messages, { streamId, topic, fromUid, content, metadata }) {
  const existingIdx = messages.findIndex((message) => message._stream_id === streamId);
  if (existingIdx !== -1) {
    const next = [...messages];
    const existing = next[existingIdx];
    next[existingIdx] = {
      ...existing,
      content: `${streamDeltaText(existing.content)}${content}`,
      metadata: {
        ...(existing.metadata || {}),
        ...(metadata || {}),
        stream_id: streamId,
      },
      _streaming: true,
      _stream_id: streamId,
    };
    return next;
  }

  const now = Date.now();
  return [
    ...messages,
    normalizeIncomingMessage({
      id: `stream:${streamId}`,
      seq_id: now,
      topic_id: topic,
      from_uid: fromUid,
      content,
      type: 'text',
      msg_type: 'text',
      metadata: {
        ...(metadata || {}),
        stream_id: streamId,
      },
      created_at: new Date(now).toISOString(),
      _streaming: true,
      _stream_id: streamId,
    }),
  ];
}

function inferWorkingTypeFromBlocks(blocks) {
  if (!Array.isArray(blocks)) return '';
  const workingBlock = blocks.find((block) => WORKING_MESSAGE_TYPES.has(block?.type));
  return workingBlock?.type || '';
}

function isWorkingMessage(message) {
  if (WORKING_MESSAGE_TYPES.has(message?.type)) return true;
  if (isWorkingTextMessage(message)) return true;
  return Boolean(inferWorkingTypeFromBlocks(message?.content_blocks));
}

function isWorkingTextMessage(message) {
  const type = message?.type || message?.msg_type || '';
  if (type !== 'text') return false;
  const content = typeof message?.content === 'string' ? message.content.trim() : '';
  return content.startsWith(WORKING_TEXT_PREFIX);
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

function getComparableContent(content) {
  if (typeof content === 'string') {
    const trimmed = content.trim();
    if (!trimmed) return '';
    try {
      const parsed = JSON.parse(trimmed);
      if (parsed && typeof parsed === 'object') {
        return JSON.stringify(parsed);
      }
    } catch (err) {
      return trimmed;
    }
    return trimmed;
  }
  if (content && typeof content === 'object') {
    return JSON.stringify(content);
  }
  return String(content ?? '');
}

function formatFileSize(size) {
  if (!size || size <= 0) return '';
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  if (size < 1024 * 1024 * 1024) return `${(size / (1024 * 1024)).toFixed(1)} MB`;
  return `${(size / (1024 * 1024 * 1024)).toFixed(1)} GB`;
}
