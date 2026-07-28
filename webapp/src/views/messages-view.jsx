import React, { useState, useRef, useEffect, useCallback, useMemo } from 'react';
import { CheckCircle2, ChevronDown, ChevronRight, Circle, CircleDot, FileText, Image, LoaderCircle, RefreshCw, Smartphone, Users, X } from 'lucide-react';
import { api, wsSendMessage, wsSendStreamCancel, wsSendTyping, wsSendRead, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import ChatMessage, { FilePreviewPanel } from '../widgets/chat-message';
import Avatar from '../widgets/avatar';
import QRCode from '../widgets/qr-code';
import { TutorialEmptyState, TutorialTaskModal, TutorialTaskPicker, TUTORIAL_TASKS } from '../widgets/tutorial-tasks';
import ChatComposer from '../widgets/chat-composer';
import { IMAGE_UPLOAD_ACCEPT, MAX_ATTACHMENT_SIZE, MAX_ATTACHMENT_SIZE_MB, inferAttachmentType, validateImageUpload } from '../utils/upload-rules';

const PAGE_SIZE = 50;
const HISTORY_CACHE_MAX_TOPICS = 12;
const QUESTION_HISTORY_PAGE_SIZE = 500;
const QUESTION_INDEX_MAX_SCANNED_PER_LOAD = 2000;
const QUESTION_INDEX_MAX_ITEMS = 250;
const STRUCTURED_MENTION_ALL = 'all';
const TYPING_TIMEOUT_MS = 10000;
const WORKING_MESSAGE_TYPES = new Set(['thinking', 'tool_use', 'tool_result']);
const WORKING_TEXT_PREFIX = 'AI文本:';
const MAX_DROPPED_FILES = 200;
const HISTORY_AUTO_LOAD_THRESHOLD = 120;
const STICK_TO_BOTTOM_THRESHOLD = 96;
const QUESTION_NAV_BOTTOM_EPSILON = 2;
const QUESTION_JUMP_RELEASE_DELAY = 240;
const ASSISTANT_REPLY_MERGE_WINDOW_MS = 90 * 1000;
const GROUP_MEMBER_REFRESH_EVENTS = new Set([
  'members_invited',
  'member_left',
  'member_kicked',
  'role_updated',
  'group_updated',
]);
const PREVIEW_WIDTH_STORAGE_KEY = 'cc_file_preview_width_v1';
const PREVIEW_WIDTH_MIN = 360;
const PREVIEW_WIDTH_DEFAULT = 640;
const PREVIEW_WIDTH_MAX = 980;

function questionNavigationKey(message, index) {
  return String(message?.id ?? message?.seq_id ?? `question-${index}`);
}

function questionNavigationLabel(message) {
  const content = message?.content;
  const rawText = typeof content === 'string'
    ? content
    : (content && typeof content === 'object' && typeof content.text === 'string' ? content.text : '');
  const normalized = rawText.replace(/\s+/g, ' ').trim();
  return normalized ? normalized.slice(0, 60) : '附件指令';
}

function questionNavigationItem(message, index, userUID) {
  const type = message?.type || message?.msg_type || '';
  if (
    type !== 'text'
    || Number(message?.from_uid) !== Number(userUID)
    || isWorkingMessage(message)
  ) {
    return null;
  }
  return {
    key: questionNavigationKey(message, index),
    id: historyMessageID(message),
    label: questionNavigationLabel(message),
  };
}

function collectQuestionNavigationItems(messages, userUID) {
  return (messages || [])
    .map((message, index) => questionNavigationItem(message, index, userUID))
    .filter(Boolean);
}

function mergeQuestionNavigationItems(...collections) {
  const byKey = new Map();
  collections.flat().forEach((item) => {
    if (item?.key) byKey.set(item.key, item);
  });
  return Array.from(byKey.values())
    .sort((left, right) => {
      if (left.id > 0 && right.id > 0) return left.id - right.id;
      return left.key.localeCompare(right.key);
    })
    .slice(-QUESTION_INDEX_MAX_ITEMS);
}

function cacheQuestionIndex(cache, key, entry) {
  cache.delete(key);
  cache.set(key, entry);
  while (cache.size > HISTORY_CACHE_MAX_TOPICS) {
    cache.delete(cache.keys().next().value);
  }
}

function clampPreviewWidth(width) {
  const viewport = typeof window !== 'undefined' ? window.innerWidth : 1440;
  const viewportMax = Number.isFinite(viewport)
    ? Math.max(PREVIEW_WIDTH_MIN, viewport - 520)
    : PREVIEW_WIDTH_MAX;
  const maxWidth = Math.min(PREVIEW_WIDTH_MAX, viewportMax);
  const numericWidth = Number(width);
  if (!Number.isFinite(numericWidth)) return PREVIEW_WIDTH_DEFAULT;
  return Math.min(Math.max(numericWidth, PREVIEW_WIDTH_MIN), maxWidth);
}

function loadPreviewWidth() {
  if (typeof window === 'undefined' || !window.localStorage) return PREVIEW_WIDTH_DEFAULT;
  return clampPreviewWidth(Number(window.localStorage.getItem(PREVIEW_WIDTH_STORAGE_KEY)) || PREVIEW_WIDTH_DEFAULT);
}

function savePreviewWidth(width) {
  if (typeof window === 'undefined' || !window.localStorage) return;
  window.localStorage.setItem(PREVIEW_WIDTH_STORAGE_KEY, String(Math.round(width)));
}

function resolvePhoneUploadLink(uploadUrl) {
  if (!uploadUrl) return '';
  if (/^https?:\/\//i.test(uploadUrl)) return uploadUrl;
  const normalizedPath = uploadUrl.startsWith('/') ? uploadUrl : `/${uploadUrl}`;
  return `${window.location.origin}${normalizedPath}`;
}

function isStructuredMentionSelectionIntact(text, target, start, end) {
  const token = target === STRUCTURED_MENTION_ALL ? '@所有人' : `@${target}`;
  if (start < 0 || end > text.length || text.slice(start, end) !== token) return false;
  const trailingCharacter = text.slice(end, end + 1);
  return !trailingCharacter || !/[\p{L}\p{N}_]/u.test(trailingCharacter);
}

export function reconcileStructuredMentionSelections(previousText, nextText, selections = []) {
  const previous = typeof previousText === 'string' ? previousText : '';
  const next = typeof nextText === 'string' ? nextText : '';
  if (!Array.isArray(selections) || selections.length === 0) return [];

  let prefixLength = 0;
  while (prefixLength < previous.length
    && prefixLength < next.length
    && previous[prefixLength] === next[prefixLength]) {
    prefixLength += 1;
  }

  let previousSuffixStart = previous.length;
  let nextSuffixStart = next.length;
  while (previousSuffixStart > prefixLength
    && nextSuffixStart > prefixLength
    && previous[previousSuffixStart - 1] === next[nextSuffixStart - 1]) {
    previousSuffixStart -= 1;
    nextSuffixStart -= 1;
  }

  const delta = next.length - previous.length;
  const nextChangedText = next.slice(prefixLength, nextSuffixStart);
  return selections.flatMap((selection) => {
    const target = typeof selection?.target === 'string' ? selection.target : '';
    let start = Number.isInteger(selection?.start) ? selection.start : -1;
    let end = Number.isInteger(selection?.end) ? selection.end : -1;
    if (target !== STRUCTURED_MENTION_ALL && !/^usr\d+$/u.test(target)) return [];
    if (start < 0 || end <= start) return [];

    const touchesRightBoundary = prefixLength === end
      && /[\p{L}\p{N}_]/u.test(nextChangedText.slice(0, 1));
    const touchesLeftBoundary = previousSuffixStart === start
      && /[\p{L}\p{N}_]/u.test(nextChangedText.slice(-1));
    if (touchesRightBoundary || touchesLeftBoundary) return [];

    if (end <= prefixLength) {
      // The selected token is before the edit and remains unchanged.
    } else if (start >= previousSuffixStart) {
      start += delta;
      end += delta;
    } else {
      return [];
    }

    if (!isStructuredMentionSelectionIntact(next, target, start, end)) return [];
    return [{ target, start, end }];
  });
}

export function collectStructuredMentionTargets(text, selections = []) {
  const value = typeof text === 'string' ? text : '';
  if (!Array.isArray(selections)) return [];
  return [...new Set(selections.flatMap((selection) => {
    const target = typeof selection?.target === 'string' ? selection.target : '';
    const start = Number.isInteger(selection?.start) ? selection.start : -1;
    const end = Number.isInteger(selection?.end) ? selection.end : -1;
    if (target !== STRUCTURED_MENTION_ALL && !/^usr\d+$/u.test(target)) return [];
    if (start < 0 || end <= start) return [];
    return isStructuredMentionSelectionIntact(value, target, start, end) ? [target] : [];
  }))];
}

function historyMessageID(message) {
  const id = Number(message?.seq_id || message?.id || 0);
  return Number.isFinite(id) && id > 0 ? id : 0;
}

function oldestHistoryMessageID(messages) {
  for (const message of messages || []) {
    const id = historyMessageID(message);
    if (id > 0) return id;
  }
  return 0;
}

function historyCacheKey(userID, topic) {
  return `${userID || 'anonymous'}:${topic}`;
}

function cacheHistoryPage(cache, key, entry) {
  cache.delete(key);
  cache.set(key, entry);
  while (cache.size > HISTORY_CACHE_MAX_TOPICS) {
    cache.delete(cache.keys().next().value);
  }
}

export default function MessagesView({
  topic,
  topicName,
  user,
  isGroup,
  groupId,
  topicAvatarUrl,
  localAssistantStatus = 'connected',
  onOpenDesktopConnect,
  onResolveAgentTopic,
  onActivateTopic,
  onAgentModelChange,
  onActiveAgentChange,
}) {
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
  const [mentionActiveIndex, setMentionActiveIndex] = useState(0);
  const [replyTo, setReplyTo] = useState(null);
  const [previewFile, setPreviewFile] = useState(null);
  const [previewWidth, setPreviewWidth] = useState(() => loadPreviewWidth());
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [historyLoaded, setHistoryLoaded] = useState(false);
  const [refreshingHistory, setRefreshingHistory] = useState(false);
  const [historyError, setHistoryError] = useState('');
  const [olderHistoryError, setOlderHistoryError] = useState('');
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [isStopRequested, setIsStopRequested] = useState(false);
  const [suppressedWorkingKey, setSuppressedWorkingKey] = useState('');
  const [liveWorkingKey, setLiveWorkingKey] = useState('');
  const [attachmentStatus, setAttachmentStatus] = useState(null);
  const [phoneUploadDialogOpen, setPhoneUploadDialogOpen] = useState(false);
  const [phoneUploadSession, setPhoneUploadSession] = useState(null);
  const [phoneUploadError, setPhoneUploadError] = useState('');
  const [showTutorialPicker, setShowTutorialPicker] = useState(false);
  const [selectedTutorialTask, setSelectedTutorialTask] = useState(null);
  const [tutorialTasks, setTutorialTasks] = useState(TUTORIAL_TASKS);
  const [tutorialDismissed, setTutorialDismissed] = useState(() => localStorage.getItem(tutorialDismissStorageKey(user.uid, topic)) === '1');
  const [attachmentMenuOpen, setAttachmentMenuOpen] = useState(false);
  const [availableAgents, setAvailableAgents] = useState([]);
  const [isSendingMessage, setIsSendingMessage] = useState(false);
  const [activeQuestionKey, setActiveQuestionKey] = useState('');
  const [questionIndexItems, setQuestionIndexItems] = useState([]);
  const [questionIndexLoading, setQuestionIndexLoading] = useState(false);
  const [questionIndexHasMore, setQuestionIndexHasMore] = useState(false);
  const [questionIndexLimitReached, setQuestionIndexLimitReached] = useState(false);
  const [showThinking, setShowThinking] = useState(() => {
    const saved = localStorage.getItem('cc_show_thinking');
    return saved === null ? true : saved === 'true';
  });
  const bottomRef = useRef(null);
  const chatColumnRef = useRef(null);
  const lastTypingSent = useRef(0);
  const peerTypingTimer = useRef(null);
  const liveWorkingTimer = useRef(null);
  const timelineRef = useRef(null);
  const pendingQuestionJumpRef = useRef('');
  const questionJumpReleaseTimerRef = useRef(null);
  const previousScrollRef = useRef(null);
  const stickToBottomRef = useRef(true);
  const fileInputRef = useRef(null);
  const imageInputRef = useRef(null);
  const textareaRef = useRef(null);
  const mentionRangeRef = useRef(null);
  const dragDepthRef = useRef(0);
  const runtimePlanRef = useRef(null);
  const runtimePlanClearTimer = useRef(null);
  const historyOffsetRef = useRef(0);
  const historyBeforeIDRef = useRef(0);
  const historyRequestRef = useRef(0);
  const historyLoadingRef = useRef(false);
  const historyCacheRef = useRef(new Map());
  const hasMoreHistoryRef = useRef(false);
  const loadingOlderRef = useRef(false);
  const activeTopicRef = useRef(topic);
  const questionIndexCacheRef = useRef(new Map());
  const questionIndexRequestRef = useRef(0);
  const questionIndexLoadingRef = useRef(false);
  const composerDraftsRef = useRef(new Map());
  const structuredMentionDraftsRef = useRef(new Map());
  const attachmentDraftsRef = useRef(new Map());
  const pendingAttachmentsRef = useRef([]);
  const previewWidthRef = useRef(previewWidth);
  const phoneUploadFileKeysRef = useRef(new Set());
  const phoneUploadSessionRef = useRef(null);
  const phoneUploadTopicRef = useRef('');
  const phoneUploadSyncRef = useRef(null);
  const sendInFlightRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    const loadAgents = async () => {
      try {
        const response = await api.getAgents();
        if (cancelled) return;
        const agents = response.agents || [];
        setAvailableAgents(agents);
      } catch (error) {
        if (!cancelled) setAvailableAgents([]);
      }
    };
    loadAgents();
    const refresh = () => loadAgents();
    window.addEventListener('cc:data-changed', refresh);
    return () => {
      cancelled = true;
      window.removeEventListener('cc:data-changed', refresh);
    };
  }, [topic]);

  const updateComposerDraft = useCallback((draftTopic, value) => {
    if (!draftTopic) return;
    if (value) {
      composerDraftsRef.current.set(draftTopic, value);
    } else {
      composerDraftsRef.current.delete(draftTopic);
    }
  }, []);

  const updateStructuredMentionDraft = useCallback((draftTopic, selections) => {
    if (!draftTopic) return;
    if (Array.isArray(selections) && selections.length > 0) {
      structuredMentionDraftsRef.current.set(draftTopic, selections);
    } else {
      structuredMentionDraftsRef.current.delete(draftTopic);
    }
  }, []);

  const updateAttachmentDraft = useCallback((draftTopic, nextValue) => {
    if (!draftTopic) return [];
    const current = attachmentDraftsRef.current.get(draftTopic) || [];
    const next = typeof nextValue === 'function' ? nextValue(current) : nextValue;
    const normalized = Array.isArray(next) ? next : [];
    if (normalized.length > 0) {
      attachmentDraftsRef.current.set(draftTopic, normalized);
    } else {
      attachmentDraftsRef.current.delete(draftTopic);
    }
    if (activeTopicRef.current === draftTopic) {
      pendingAttachmentsRef.current = normalized;
      setPendingAttachments(normalized);
    }
    return normalized;
  }, []);

  useEffect(() => {
    previewWidthRef.current = previewWidth;
  }, [previewWidth]);

  const updatePreviewWidth = useCallback((nextWidth) => {
    const clamped = clampPreviewWidth(nextWidth);
    previewWidthRef.current = clamped;
    setPreviewWidth(clamped);
    savePreviewWidth(clamped);
  }, []);

  const handlePreviewResizePointerDown = useCallback((event) => {
    if (!previewFile) return;
    event.preventDefault();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    const startX = event.clientX;
    const startWidth = previewWidthRef.current;

    const handlePointerMove = (moveEvent) => {
      const nextWidth = startWidth + (startX - moveEvent.clientX);
      updatePreviewWidth(nextWidth);
    };
    const handlePointerUp = () => {
      window.removeEventListener('pointermove', handlePointerMove);
      window.removeEventListener('pointerup', handlePointerUp);
    };

    window.addEventListener('pointermove', handlePointerMove);
    window.addEventListener('pointerup', handlePointerUp);
  }, [previewFile, updatePreviewWidth]);

  const handlePreviewResizeKeyDown = useCallback((event) => {
    if (!previewFile) return;
    if (event.key === 'ArrowLeft') {
      event.preventDefault();
      updatePreviewWidth(previewWidthRef.current + 40);
    } else if (event.key === 'ArrowRight') {
      event.preventDefault();
      updatePreviewWidth(previewWidthRef.current - 40);
    } else if (event.key === 'Home') {
      event.preventDefault();
      updatePreviewWidth(PREVIEW_WIDTH_MIN);
    } else if (event.key === 'End') {
      event.preventDefault();
      updatePreviewWidth(PREVIEW_WIDTH_MAX);
    }
  }, [previewFile, updatePreviewWidth]);

  const resizeComposerInput = useCallback(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    const maxHeight = 200;
    textarea.style.height = 'auto';
    const nextHeight = Math.min(Math.max(textarea.scrollHeight, 40), maxHeight);
    textarea.style.height = `${nextHeight}px`;
    textarea.style.overflowY = textarea.scrollHeight > maxHeight ? 'auto' : 'hidden';
  }, []);

  useEffect(() => {
    resizeComposerInput();
  }, [input, resizeComposerInput]);

  useEffect(() => {
    setTutorialDismissed(localStorage.getItem(tutorialDismissStorageKey(user.uid, topic)) === '1');
  }, [topic, user.uid]);

  useEffect(() => {
    let cancelled = false;
    api.getTutorialTasks()
      .then((data) => {
        const tasks = Array.isArray(data.tasks) ? data.tasks.filter((task) => task && task.prompt).slice(0, Number(data.limit) || 6) : [];
        if (!cancelled && tasks.length > 0) setTutorialTasks(tasks);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

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
    questionIndexRequestRef.current += 1;
    if (runtimePlanClearTimer.current) {
      clearTimeout(runtimePlanClearTimer.current);
    }
    if (liveWorkingTimer.current) {
      clearTimeout(liveWorkingTimer.current);
    }
  }, []);

  // Load message history and group members when topic changes
  useEffect(() => {
    if (!topic) return;
    activeTopicRef.current = topic;
    setInput(composerDraftsRef.current.get(topic) || '');
    const cacheKey = historyCacheKey(user.uid, topic);
    const cachedHistory = historyCacheRef.current.get(cacheKey);
    const cachedQuestionIndex = questionIndexCacheRef.current.get(cacheKey);
    setMessages(cachedHistory?.messages || []);
    setQuestionIndexItems(cachedQuestionIndex?.items || []);
    setQuestionIndexHasMore(Boolean(cachedQuestionIndex?.hasMore));
    setQuestionIndexLimitReached(Boolean(cachedQuestionIndex?.limitReached));
    setQuestionIndexLoading(false);
    questionIndexLoadingRef.current = false;
    const attachmentDraft = attachmentDraftsRef.current.get(topic) || [];
    pendingAttachmentsRef.current = attachmentDraft;
    setPendingAttachments(attachmentDraft);
    setIsDragActive(false);
    dragDepthRef.current = 0;
    setPeerTyping(false);
    setShowMentionPicker(false);
    setMentionFilter('');
    setMentionActiveIndex(0);
    mentionRangeRef.current = null;
    clearRuntimePlan();
    setReplyTo(null);
    setPreviewFile(null);
    setMembers([]);
    setGroupInfo(null);
    setPeerProfile(null);
    setHistoryLoaded(Boolean(cachedHistory));
    setHistoryError('');
    setOlderHistoryError('');
    historyOffsetRef.current = cachedHistory?.offset || 0;
    historyBeforeIDRef.current = cachedHistory?.nextBeforeID || 0;
    hasMoreHistoryRef.current = Boolean(cachedHistory?.hasMore);
    previousScrollRef.current = null;
    loadingOlderRef.current = false;
    questionIndexRequestRef.current += 1;
    stickToBottomRef.current = true;
    setHasMoreHistory(Boolean(cachedHistory?.hasMore));
    setLoadingOlder(false);
    setIsStopRequested(false);
    setSuppressedWorkingKey('');
    setLiveWorkingKey('');
    if (liveWorkingTimer.current) {
      clearTimeout(liveWorkingTimer.current);
      liveWorkingTimer.current = null;
    }
    setAttachmentStatus(null);
    setAttachmentMenuOpen(false);
    setPhoneUploadDialogOpen(false);
    setPhoneUploadSession(null);
    setPhoneUploadError('');
    phoneUploadSessionRef.current = null;
    phoneUploadTopicRef.current = '';
    phoneUploadSyncRef.current = null;
    phoneUploadFileKeysRef.current = new Set();
    loadHistory(topic);
    if (isGroup && groupId) {
      loadGroupMembers();
    } else {
      loadPeerProfile();
    }
  }, [topic, user.uid]);

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
      const [left, right] = topic.replace('p2p_', '').split('_').map((n) => parseInt(n, 10));
      const peerId = left === user.uid ? right : left;
      const [friendsRes, agentsRes] = await Promise.all([
        api.getFriends().catch(() => ({})),
        api.getAgents ? api.getAgents().catch(() => ({})) : Promise.resolve({}),
      ]);
      const friends = friendsRes.friends || [];
      const agents = agentsRes.agents || [];
      const friendPeer = friends.find((friend) => friend.id === peerId);
      const agentPeer = agents.find((agent) => agent.uid === peerId || agent.id === peerId);
      const peer = agentPeer ? { ...friendPeer, ...agentPeer } : friendPeer;
      if (peer) setPeerProfile(peer);
    } catch (e) {
    }
  };

  const markLiveWorking = useCallback((message) => {
    const key = workingMessageKey(message);
    setLiveWorkingKey(key);
    if (liveWorkingTimer.current) clearTimeout(liveWorkingTimer.current);
    liveWorkingTimer.current = setTimeout(() => {
      liveWorkingTimer.current = null;
      setLiveWorkingKey('');
    }, TYPING_TIMEOUT_MS);
  }, []);

  const clearLiveWorking = useCallback(() => {
    if (liveWorkingTimer.current) clearTimeout(liveWorkingTimer.current);
    liveWorkingTimer.current = null;
    setLiveWorkingKey('');
  }, []);

  // Listen for incoming WebSocket messages
  useEffect(() => {
    const unsub = onWSMessage((msg) => {
      if (
        isGroup
        && groupId
        && msg.pres?.topic === topic
        && GROUP_MEMBER_REFRESH_EVENTS.has(msg.pres.what)
      ) {
        loadGroupMembers();
      }

      // New message from server
      if (msg.data && msg.data.topic === topic) {
        if (isStreamCancel(msg.data)) {
          const streamId = getStreamId(msg.data);
          if (streamId) {
            setMessages((prev) => prev.filter((message) => message._stream_id !== streamId));
          }
          clearRuntimePlan();
          clearLiveWorking();
          clearTimeout(peerTypingTimer.current);
          setPeerTyping(false);
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
        if (isWorkingMessage(serverMsg)) markLiveWorking(serverMsg);

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
          clearLiveWorking();
          clearTimeout(peerTypingTimer.current);
          setPeerTyping(false);
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
  }, [clearLiveWorking, groupId, isGroup, markLiveWorking, topic, user.uid]);

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

  const loadQuestionNavigationHistory = useCallback(async ({ continueOlder = false } = {}) => {
    const targetTopic = topic;
    const cacheKey = historyCacheKey(user.uid, targetTopic);
    const cached = questionIndexCacheRef.current.get(cacheKey);
    if (
      !targetTopic
      || questionIndexLoadingRef.current
      || !cached?.hasMore
      || cached.limitReached
      || (cached.requested && !continueOlder)
    ) {
      return;
    }

    const requestId = ++questionIndexRequestRef.current;
    let entry = { ...cached, requested: true };
    let scannedThisLoad = 0;
    questionIndexLoadingRef.current = true;
    setQuestionIndexLoading(true);
    cacheQuestionIndex(questionIndexCacheRef.current, cacheKey, entry);

    try {
      while (
        entry.hasMore
        && !entry.limitReached
        && scannedThisLoad < QUESTION_INDEX_MAX_SCANNED_PER_LOAD
      ) {
        const res = await api.getMessages(
          targetTopic,
          QUESTION_HISTORY_PAGE_SIZE,
          entry.offset,
          true,
          entry.beforeId,
        );
        if (
          activeTopicRef.current !== targetTopic
          || questionIndexRequestRef.current !== requestId
        ) {
          return;
        }

        const rawBatch = Array.isArray(res.messages) ? res.messages : [];
        const { visibleMessages } = normalizeHistoryMessages(rawBatch);
        const batchItems = collectQuestionNavigationItems(visibleMessages, user.uid);
        const mergedItems = mergeQuestionNavigationItems(entry.items, batchItems);
        const hasMore = rawBatch.length > 0 && (
          typeof res.has_more === 'boolean'
            ? res.has_more
            : rawBatch.length === QUESTION_HISTORY_PAGE_SIZE
        );
        const limitReached = mergedItems.length >= QUESTION_INDEX_MAX_ITEMS && hasMore;
        entry = {
          ...entry,
          items: mergedItems,
          offset: entry.offset + rawBatch.length,
          beforeId: Number(res.next_before_id) || oldestHistoryMessageID(rawBatch),
          hasMore,
          limitReached,
        };
        scannedThisLoad += rawBatch.length;
        cacheQuestionIndex(questionIndexCacheRef.current, cacheKey, entry);
        setQuestionIndexItems(mergedItems);
        setQuestionIndexHasMore(hasMore);
        setQuestionIndexLimitReached(limitReached);
        if (rawBatch.length === 0) break;
      }
    } catch (e) {
      // Keep the lightweight anchors already collected; normal scroll history is unaffected.
    } finally {
      if (
        activeTopicRef.current === targetTopic
        && questionIndexRequestRef.current === requestId
      ) {
        questionIndexLoadingRef.current = false;
        setQuestionIndexLoading(false);
      }
    }
  }, [topic, user.uid]);

  const loadHistory = async (targetTopic = topic) => {
    const requestID = ++historyRequestRef.current;
    const cacheKey = historyCacheKey(user.uid, targetTopic);
    const hasCachedHistory = historyCacheRef.current.has(cacheKey);
    historyLoadingRef.current = true;
    previousScrollRef.current = null;
    setRefreshingHistory(true);
    setHistoryError('');
    setOlderHistoryError('');
    loadingOlderRef.current = false;
    setLoadingOlder(false);
    if (!hasCachedHistory) {
      setHistoryLoaded(false);
    }
    try {
      const res = await api.getMessages(targetTopic, PAGE_SIZE, 0, true);
      if (activeTopicRef.current !== targetTopic || historyRequestRef.current !== requestID) return;
      const rawMessages = res.messages || [];
      const { visibleMessages } = normalizeHistoryMessages(rawMessages);
      const hasMore = typeof res.has_more === 'boolean'
        ? res.has_more
        : rawMessages.length === PAGE_SIZE;
      const nextBeforeID = Number(res.next_before_id) || oldestHistoryMessageID(rawMessages);
      const newestHistoryID = rawMessages.reduce(
        (latestID, message) => Math.max(latestID, historyMessageID(message)),
        0,
      );
      setMessages((current) => {
        const newerMessages = rawMessages.length === 0
          ? current.filter((message) => message._pending)
          : current.filter((message) => message._pending || historyMessageID(message) > newestHistoryID);
        return mergeMessages(visibleMessages, newerMessages);
      });
      historyOffsetRef.current = rawMessages.length;
      historyBeforeIDRef.current = nextBeforeID;
      hasMoreHistoryRef.current = hasMore;
      setHasMoreHistory(hasMore);
      cacheHistoryPage(historyCacheRef.current, cacheKey, {
        messages: visibleMessages,
        offset: rawMessages.length,
        nextBeforeID,
        hasMore,
      });
      const cachedQuestionIndex = questionIndexCacheRef.current.get(cacheKey);
      if (!cachedQuestionIndex) {
        const nextQuestionIndex = {
          items: [],
          offset: rawMessages.length,
          beforeId: nextBeforeID,
          hasMore,
          requested: false,
          limitReached: false,
        };
        cacheQuestionIndex(questionIndexCacheRef.current, cacheKey, nextQuestionIndex);
        setQuestionIndexHasMore(hasMore);
      }
    } catch (e) {
      if (activeTopicRef.current === targetTopic && historyRequestRef.current === requestID) {
        setHistoryError('聊天记录加载失败，请检查网络后重试。');
      }
    } finally {
      if (activeTopicRef.current === targetTopic && historyRequestRef.current === requestID) {
        historyLoadingRef.current = false;
        setRefreshingHistory(false);
        setHistoryLoaded(true);
      }
    }
  };

  const loadOlderHistory = useCallback(async () => {
    if (historyLoadingRef.current || loadingOlderRef.current || !hasMoreHistoryRef.current) return;
    const targetTopic = topic;
    const requestID = historyRequestRef.current;
    
    // Capture the absolute scroll geometry BEFORE rendering the older batch
    if (timelineRef.current) {
      previousScrollRef.current = {
        scrollHeight: timelineRef.current.scrollHeight,
        scrollTop: timelineRef.current.scrollTop,
      };
    }
    
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    setOlderHistoryError('');
    try {
      const res = await api.getMessages(
        targetTopic,
        PAGE_SIZE,
        historyOffsetRef.current,
        true,
        historyBeforeIDRef.current,
      );
      if (activeTopicRef.current !== targetTopic || historyRequestRef.current !== requestID) return;
      const rawMessages = res.messages || [];
      const { visibleMessages } = normalizeHistoryMessages(rawMessages);
      setMessages((prev) => mergeMessages(visibleMessages, prev));
      historyOffsetRef.current += rawMessages.length;
      historyBeforeIDRef.current = Number(res.next_before_id) || oldestHistoryMessageID(rawMessages);
      const hasMore = typeof res.has_more === 'boolean'
        ? res.has_more
        : rawMessages.length === PAGE_SIZE;
      hasMoreHistoryRef.current = hasMore;
      setHasMoreHistory(hasMore);
      const cacheKey = historyCacheKey(user.uid, targetTopic);
      const cachedQuestionIndex = questionIndexCacheRef.current.get(cacheKey);
      if (cachedQuestionIndex) {
        const ordinaryReachedFurther = historyOffsetRef.current >= cachedQuestionIndex.offset
          || (
            historyBeforeIDRef.current > 0
            && cachedQuestionIndex.beforeId > 0
            && historyBeforeIDRef.current < cachedQuestionIndex.beforeId
          );
        const nextQuestionItems = mergeQuestionNavigationItems(
          cachedQuestionIndex.items,
          collectQuestionNavigationItems(visibleMessages, user.uid),
        );
        const nextQuestionIndex = {
          ...cachedQuestionIndex,
          items: nextQuestionItems,
          offset: Math.max(cachedQuestionIndex.offset, historyOffsetRef.current),
          beforeId: ordinaryReachedFurther
            ? historyBeforeIDRef.current
            : cachedQuestionIndex.beforeId,
          hasMore: ordinaryReachedFurther ? hasMore : cachedQuestionIndex.hasMore,
          limitReached: nextQuestionItems.length >= QUESTION_INDEX_MAX_ITEMS
            && (ordinaryReachedFurther ? hasMore : cachedQuestionIndex.hasMore),
        };
        cacheQuestionIndex(questionIndexCacheRef.current, cacheKey, nextQuestionIndex);
        setQuestionIndexItems(nextQuestionItems);
        setQuestionIndexHasMore(nextQuestionIndex.hasMore);
        setQuestionIndexLimitReached(nextQuestionIndex.limitReached);
      }
    } catch (e) {
      if (activeTopicRef.current === targetTopic && historyRequestRef.current === requestID) {
        previousScrollRef.current = null;
        setOlderHistoryError('更早的聊天记录加载失败。');
      }
    } finally {
      if (activeTopicRef.current === targetTopic && historyRequestRef.current === requestID) {
        loadingOlderRef.current = false;
        setLoadingOlder(false);
      }
    }
  }, [topic, user.uid]);

  useEffect(() => {
    const el = timelineRef.current;
    if (!el || refreshingHistory || !hasMoreHistory || loadingOlder || historyError || olderHistoryError) return;
    if (el.scrollTop <= HISTORY_AUTO_LOAD_THRESHOLD
      || el.scrollHeight <= el.clientHeight + HISTORY_AUTO_LOAD_THRESHOLD) {
      loadOlderHistory();
    }
  }, [messages.length, refreshingHistory, hasMoreHistory, loadingOlder, historyError, olderHistoryError, loadOlderHistory]);

  const workingState = useMemo(() => {
    let lastWorkingIndex = -1;
    let lastBotTextIndex = -1;
    const groupBotUIDs = new Set([
      ...members
        .filter((member) => member?.is_bot || member?.account_type === 'bot')
        .map((member) => Number(member.user_id)),
      ...availableAgents
        .map((agent) => Number(agent.uid || agent.id)),
    ].filter((uid) => Number.isFinite(uid) && uid > 0));
    const currentUserUID = Number(user.uid);
    const groupMemberUIDs = new Set(
      members
        .map((member) => Number(member?.user_id))
        .filter((uid) => Number.isFinite(uid) && uid > 0),
    );
    const exclusiveToCurrentUser = isGroup
      && Number.isFinite(currentUserUID)
      && currentUserUID > 0
      && groupMemberUIDs.size === 2
      && groupMemberUIDs.has(currentUserUID)
      && Array.from(groupMemberUIDs).some(
        (uid) => uid !== currentUserUID && groupBotUIDs.has(uid),
      );

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

    const active = lastWorkingIndex > lastBotTextIndex;
    return {
      active,
      key: active ? workingMessageKey(messages[lastWorkingIndex], lastWorkingIndex) : '',
      initiatorUid: active && isGroup
        ? resolveWorkingInitiatorUid(messages, lastWorkingIndex, groupBotUIDs)
        : Number(user.uid),
      responderUid: active ? Number(messages[lastWorkingIndex]?.from_uid || 0) : 0,
      exclusiveToCurrentUser,
    };
  }, [availableAgents, isGroup, members, messages, user.uid]);
  const activeBotWorking = workingState.active
    && (peerTyping || workingState.key === liveWorkingKey)
    && workingState.key !== suppressedWorkingKey;
  const canStopActiveBotWorking = activeBotWorking
    && (
      !isGroup
      || workingState.exclusiveToCurrentUser
      || workingState.initiatorUid === Number(user.uid)
    );

  useEffect(() => {
    if (!activeBotWorking) {
      setIsStopRequested(false);
    }
  }, [activeBotWorking]);

  const topicAgent = availableAgents.find((agent) => agent.topic_id === topic) || null;
  const groupAgent = isGroup
    ? availableAgents.find((agent) => members.some((member) => member.user_id === (agent.uid || agent.id))) || null
    : null;
  const selectedAgent = isGroup
    ? groupAgent
    : topicAgent;

  const syncPhoneUploads = useCallback(async ({ final = false } = {}) => {
    const sessionId = phoneUploadSessionRef.current?.session_id;
    const sessionTopic = phoneUploadTopicRef.current;
    if (!sessionId || !sessionTopic || activeTopicRef.current !== sessionTopic) return [];

    let operation = phoneUploadSyncRef.current;
    if (!operation) {
      operation = (async () => {
        const data = await api.getMobileUploadSession(sessionId);
        if (
          phoneUploadSessionRef.current?.session_id !== sessionId
          || phoneUploadTopicRef.current !== sessionTopic
          || activeTopicRef.current !== sessionTopic
        ) {
          return [];
        }
        if (data?.topic && data.topic !== sessionTopic) {
          throw new Error('手机上传会话与当前对话不匹配，请重新打开二维码。');
        }

        const nextAttachments = [];
        for (const file of Array.isArray(data?.files) ? data.files : []) {
          const fileKey = file.file_key || file.url || file.name;
          if (!fileKey || phoneUploadFileKeysRef.current.has(fileKey)) continue;
          phoneUploadFileKeysRef.current.add(fileKey);
          const type = file.type === 'image' ? 'image' : 'file';
          const payload = {
            file_key: file.file_key,
            url: file.url,
            name: file.name,
            size: file.size,
            mime_type: file.mime_type || '',
          };
          if (type === 'image') payload.thumbnail = file.url;
          nextAttachments.push({
            type,
            name: file.name,
            size: file.size,
            content: { type, payload },
          });
        }

        if (nextAttachments.length > 0) {
          const updated = updateAttachmentDraft(sessionTopic, (current) => [...current, ...nextAttachments]);
          if (activeTopicRef.current === sessionTopic) {
            setAttachmentStatus({ tone: 'success', message: `手机已上传 ${updated.length} 个附件，发送后对方可见。` });
          }
        }
        if (activeTopicRef.current === sessionTopic) setPhoneUploadError('');
        return nextAttachments;
      })();
      phoneUploadSyncRef.current = operation;
    }

    try {
      return await operation;
    } catch (error) {
      if (
        activeTopicRef.current === sessionTopic
        && phoneUploadSessionRef.current?.session_id === sessionId
      ) {
        setPhoneUploadError(error?.message || '读取手机上传结果失败');
      }
      if (final) throw error;
      return [];
    } finally {
      if (phoneUploadSyncRef.current === operation) phoneUploadSyncRef.current = null;
    }
  }, [updateAttachmentDraft]);

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
    const originalInput = input;
    const initialText = originalInput.trim();
    const initialAttachments = attachmentDraftsRef.current.get(topic) || pendingAttachmentsRef.current;
    if (!initialText && initialAttachments.length === 0) return;
    if (isUploadingAttachment || sendInFlightRef.current) return;

    sendInFlightRef.current = true;
    setIsSendingMessage(true);
    setAttachmentMenuOpen(false);

    let sendTopic = topic;
    let topicToActivate = null;
    let switchesTopic = false;
    let stateCleared = false;
    let messageSent = false;
    let optimisticMessageAdded = false;
    let attachmentsToSend = [...initialAttachments];
    const text = initialText;
    const originalReplyTo = replyTo;
    const originalStructuredMentions = structuredMentionDraftsRef.current.get(topic) || [];
    const mentions = isGroup
      ? collectStructuredMentionTargets(input, originalStructuredMentions)
      : [];
    const tempId = Date.now();

    try {
      if (!isGroup && selectedAgent && selectedAgent.topic_id !== topic && onResolveAgentTopic) {
        topicToActivate = await onResolveAgentTopic(selectedAgent);
        sendTopic = topicToActivate?.topicId || topicToActivate?.topic_id || sendTopic;
      }
      switchesTopic = sendTopic !== topic;

      await syncPhoneUploads({ final: true });
      attachmentsToSend = [...(attachmentDraftsRef.current.get(topic) || [])];
      if (!text && attachmentsToSend.length === 0) return;

      const currentReplyTo = switchesTopic ? null : originalReplyTo;
      const contentBlocks = buildAtomicContentBlocks(text, attachmentsToSend);
      const displayContent = text || summarizeAttachments(attachmentsToSend);
      const payload = attachmentsToSend.length > 0
        ? {
            type: 'text',
            content: displayContent,
            content_blocks: contentBlocks,
          }
        : text;

      updateComposerDraft(topic, '');
      updateStructuredMentionDraft(topic, []);
      updateAttachmentDraft(topic, []);
      stateCleared = true;
      if (activeTopicRef.current === topic) {
        clearRuntimePlan();
        setAttachmentStatus(null);
        setInput('');
        setReplyTo(null);
      }

      stickToBottomRef.current = true;
      if (!switchesTopic && activeTopicRef.current === topic) {
        optimisticMessageAdded = true;
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
      }

      const result = mentions.length > 0
        ? await api.sendMessage(sendTopic, payload, currentReplyTo ? currentReplyTo.id : undefined, mentions)
        : await api.sendMessage(sendTopic, payload, currentReplyTo ? currentReplyTo.id : undefined);
      messageSent = true;
      if (switchesTopic) {
        if (activeTopicRef.current === topic) {
          await onActivateTopic?.(topicToActivate);
        }
        window.dispatchEvent(new Event('cc:data-changed'));
      } else if (activeTopicRef.current === sendTopic) {
        finalizeOptimisticMessage(tempId, result);
      }
    } catch (err) {
      if (messageSent) {
        if (activeTopicRef.current === topic) {
          setAttachmentStatus({
            tone: 'error',
            message: '消息已发送，但暂时无法打开目标会话。请从会话列表中重新进入。',
          });
        }
        return;
      }

      if (optimisticMessageAdded && activeTopicRef.current === topic) removeOptimisticMessage(tempId);
      if (stateCleared) {
        updateComposerDraft(topic, originalInput);
        updateStructuredMentionDraft(topic, originalStructuredMentions);
        updateAttachmentDraft(topic, attachmentsToSend);
      }
      if (activeTopicRef.current === topic) {
        if (stateCleared) {
          setInput(originalInput);
          setReplyTo(originalReplyTo);
        }
        setAttachmentStatus({
          tone: 'error',
          message: err?.message ? `发送失败：${err.message}` : '连接失败，请检查本地模型和网络后重试。',
        });
      }
    } finally {
      sendInFlightRef.current = false;
      setIsSendingMessage(false);
    }
  }, [clearRuntimePlan, finalizeOptimisticMessage, input, isGroup, isUploadingAttachment, onActivateTopic, onResolveAgentTopic, removeOptimisticMessage, replyTo, selectedAgent, syncPhoneUploads, topic, updateAttachmentDraft, updateComposerDraft, updateStructuredMentionDraft, user.uid]);

  const handleStopGeneration = useCallback(async () => {
    if (!canStopActiveBotWorking || isStopRequested) return;
    setIsStopRequested(true);
    try {
      await wsSendStreamCancel(topic, workingState.responderUid);
      setSuppressedWorkingKey(workingState.key);
      clearRuntimePlan();
      clearLiveWorking();
      clearTimeout(peerTypingTimer.current);
      setPeerTyping(false);
      setIsStopRequested(false);
    } catch (err) {
      setIsStopRequested(false);
    }
  }, [canStopActiveBotWorking, clearLiveWorking, clearRuntimePlan, isStopRequested, topic, workingState.key, workingState.responderUid]);

  const handleRegenerateMessage = useCallback(async (message) => {
    if (sendInFlightRef.current) {
      throw new Error('当前有消息正在发送');
    }

    const messageIndex = messages.findIndex((item) => item.id === message?.id);
    const previousTask = (messageIndex < 0 ? messages : messages.slice(0, messageIndex))
      .slice()
      .reverse()
      .find((item) => item.from_uid === user.uid && isFinalTextMessage(item));
    const taskText = typeof previousTask?.content === 'string' ? previousTask.content.trim() : '';
    if (!taskText) {
      throw new Error('没有找到可以重新发送的上一条任务');
    }

    sendInFlightRef.current = true;
    setIsSendingMessage(true);
    clearRuntimePlan();
    const tempId = Date.now();
    stickToBottomRef.current = true;
    setMessages((current) => mergeMessages(current, [{
      id: tempId,
      seq_id: tempId,
      topic_id: topic,
      from_uid: user.uid,
      content: taskText,
      type: 'text',
      msg_type: 'text',
      created_at: new Date().toISOString(),
      _pending: true,
    }]));

    try {
      const result = await api.sendMessage(topic, taskText, undefined);
      finalizeOptimisticMessage(tempId, result);
    } catch (error) {
      removeOptimisticMessage(tempId);
      setAttachmentStatus({
        tone: 'error',
        message: error?.message ? `重新生成失败：${error.message}` : '重新生成失败，请稍后重试。',
      });
      throw error;
    } finally {
      sendInFlightRef.current = false;
      setIsSendingMessage(false);
    }
  }, [clearRuntimePlan, finalizeOptimisticMessage, messages, removeOptimisticMessage, topic, user.uid]);

  const handleKeyDown = (e) => {
    if (
      e.isComposing
      || e.nativeEvent?.isComposing
      || e.keyCode === 229
      || e.nativeEvent?.keyCode === 229
    ) return;
    if (showMentionPicker && mentionableBots.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setMentionActiveIndex((current) => (current + 1) % mentionableBots.length);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setMentionActiveIndex((current) => (current - 1 + mentionableBots.length) % mentionableBots.length);
        return;
      }
      if ((e.key === 'Enter' && !e.shiftKey) || e.key === 'Tab') {
        e.preventDefault();
        insertMention(mentionableBots[Math.min(mentionActiveIndex, mentionableBots.length - 1)]);
        return;
      }
    }
    if (e.key === 'Escape' && showMentionPicker) {
      e.preventDefault();
      setShowMentionPicker(false);
      setMentionFilter('');
      setMentionActiveIndex(0);
      mentionRangeRef.current = null;
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const handleInputChange = (e) => {
    const val = e.target.value;
    const nextStructuredMentions = reconcileStructuredMentionSelections(
      input,
      val,
      structuredMentionDraftsRef.current.get(topic) || [],
    );
    setInput(val);
    updateComposerDraft(topic, val);
    updateStructuredMentionDraft(topic, nextStructuredMentions);

    // Detect @mention trigger
    if (isGroup) {
      const cursorPos = e.target.selectionStart;
      const textBeforeCursor = val.slice(0, cursorPos);
      const atMatch = textBeforeCursor.match(/@([^@\s]*)$/u);
      if (atMatch) {
        setShowMentionPicker(true);
        setMentionFilter(atMatch[1].toLowerCase());
        setMentionActiveIndex(0);
        mentionRangeRef.current = {
          start: cursorPos - atMatch[0].length,
          end: cursorPos,
        };
      } else {
        setShowMentionPicker(false);
        setMentionFilter('');
        setMentionActiveIndex(0);
        mentionRangeRef.current = null;
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
    const range = mentionRangeRef.current;
    if (!range || range.start < 0 || range.end < range.start || range.end > input.length) return;
    const target = member.mention_target || `usr${member.user_id}`;
    const mention = target === STRUCTURED_MENTION_ALL ? '@所有人 ' : `@${target} `;
    const newText = input.slice(0, range.start) + mention + input.slice(range.end);
    const reconciledSelections = reconcileStructuredMentionSelections(
      input,
      newText,
      structuredMentionDraftsRef.current.get(topic) || [],
    );
    updateStructuredMentionDraft(topic, [
      ...reconciledSelections,
      { target, start: range.start, end: range.start + mention.length - 1 },
    ]);
    setInput(newText);
    updateComposerDraft(topic, newText);
    setShowMentionPicker(false);
    setMentionFilter('');
    setMentionActiveIndex(0);
    mentionRangeRef.current = null;
    // Focus back on textarea
    setTimeout(() => {
      textarea.focus();
      const newPos = range.start + mention.length;
      textarea.setSelectionRange(newPos, newPos);
    }, 0);
  };

  const openMentionPicker = () => {
    const textarea = textareaRef.current;
    if (!isGroup || !textarea) return;
    const cursorPos = textarea.selectionStart;
    const nextInput = input.slice(0, cursorPos) + '@' + input.slice(cursorPos);
    const nextStructuredMentions = reconcileStructuredMentionSelections(
      input,
      nextInput,
      structuredMentionDraftsRef.current.get(topic) || [],
    );
    setInput(nextInput);
    updateComposerDraft(topic, nextInput);
    updateStructuredMentionDraft(topic, nextStructuredMentions);
    setShowMentionPicker(true);
    setMentionFilter('');
    setMentionActiveIndex(0);
    mentionRangeRef.current = { start: cursorPos, end: cursorPos + 1 };
    setTimeout(() => {
      textarea.focus();
      textarea.setSelectionRange(cursorPos + 1, cursorPos + 1);
    }, 0);
  };

  const uploadAttachmentFile = async (file, requestedType, uploadTopic = activeTopicRef.current) => {
    const type = inferAttachmentType(file, requestedType);
    const validationError = validateAttachmentBeforeUpload(file, type);
    if (validationError) {
      setAttachmentStatus({ tone: 'error', message: validationError });
      return null;
    }

    try {
      setAttachmentStatus({ tone: 'info', message: `正在上传 ${file.name || '附件'}...` });
      const data = await api.uploadFile(file, type);

      const content = {
        type,
        payload: {
          file_key: data.file_key,
          url: data.url,
          name: data.name,
          size: data.size,
          mime_type: data.mime_type || file.type || '',
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
      updateAttachmentDraft(uploadTopic, (current) => [...current, attachment]);
      if (activeTopicRef.current === uploadTopic) {
        setAttachmentStatus({ tone: 'success', message: `已添加${type === 'image' ? '图片' : '文件'}：${data.name}` });
        setTimeout(() => textareaRef.current?.focus(), 0);
      }
      return attachment;
    } catch (err) {
      if (activeTopicRef.current === uploadTopic) {
        setAttachmentStatus({ tone: 'error', message: formatUploadError(err) });
      }
      return null;
    }
  };

  const uploadAttachmentFiles = async (files, requestedType) => {
    const fileList = Array.from(files || []).filter(Boolean);
    if (fileList.length === 0 || sendInFlightRef.current) return;
    const uploadTopic = activeTopicRef.current;
    let uploadedCount = 0;
    let failedCount = 0;
    setIsUploadingAttachment(true);
    try {
      for (const file of fileList.slice(0, MAX_DROPPED_FILES)) {
        const uploaded = await uploadAttachmentFile(file, requestedType, uploadTopic);
        if (uploaded) {
          uploadedCount += 1;
        } else {
          failedCount += 1;
        }
      }
    } finally {
      setIsUploadingAttachment(false);
    }

    if (failedCount > 0 && fileList.length > 1 && activeTopicRef.current === uploadTopic) {
      setAttachmentStatus({
        tone: 'error',
        message: uploadedCount > 0
          ? `已添加 ${uploadedCount} 个附件，另有 ${failedCount} 个上传失败。`
          : `${failedCount} 个附件上传失败，请检查格式、大小或网络后重试。`,
      });
    } else if (uploadedCount > 1 && activeTopicRef.current === uploadTopic) {
      setAttachmentStatus({ tone: 'success', message: `已添加 ${uploadedCount} 个附件，发送后对方可见。` });
    }
  };

  const handleFileUpload = async (e, type) => {
    const files = Array.from(e.target.files || []);
    e.target.value = '';
    if (!files || files.length === 0) return;
    await uploadAttachmentFiles(files, type);
  };

  const openAttachmentPicker = (inputRef) => {
    if (isUploadingAttachment || sendInFlightRef.current) return;
    setAttachmentStatus(null);
    if (inputRef.current) {
      inputRef.current.value = '';
      inputRef.current.click();
    }
  };

  const openPhoneUploadDialog = async () => {
    if (!topic || phoneUploadDialogOpen || sendInFlightRef.current) return;
    const sessionTopic = topic;
    setPhoneUploadDialogOpen(true);
    setPhoneUploadError('');
    setPhoneUploadSession(null);
    phoneUploadSessionRef.current = null;
    phoneUploadTopicRef.current = '';
    phoneUploadSyncRef.current = null;
    phoneUploadFileKeysRef.current = new Set();
    try {
      const session = await api.createMobileUploadSession(sessionTopic);
      if (activeTopicRef.current !== sessionTopic) return;
      phoneUploadSessionRef.current = session;
      phoneUploadTopicRef.current = sessionTopic;
      setPhoneUploadSession(session);
    } catch (err) {
      if (activeTopicRef.current === sessionTopic) {
        setPhoneUploadError(err.message || '手机上传入口创建失败');
      }
    }
  };

  const closePhoneUploadDialog = () => {
    setPhoneUploadDialogOpen(false);
    setPhoneUploadError('');
  };

  const phoneUploadLink = resolvePhoneUploadLink(phoneUploadSession?.upload_url);

  useEffect(() => {
    if (!phoneUploadSession?.session_id) return undefined;
    if (phoneUploadTopicRef.current !== topic) return undefined;
    syncPhoneUploads();
    const timer = setInterval(() => syncPhoneUploads(), 2000);
    return () => clearInterval(timer);
  }, [phoneUploadSession?.session_id, syncPhoneUploads, topic]);

  useEffect(() => {
    if (!phoneUploadDialogOpen) return undefined;
    const handleEscape = (event) => {
      if (event.key === 'Escape') closePhoneUploadDialog();
    };
    document.addEventListener('keydown', handleEscape);
    return () => document.removeEventListener('keydown', handleEscape);
  }, [phoneUploadDialogOpen]);

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
      setAttachmentStatus({ tone: 'info', message: '附件仍在上传中，请稍后再拖入新的文件。' });
      return;
    }
    const files = await collectDroppedFiles(e.dataTransfer);
    if (files.length === 0) {
      setAttachmentStatus({ tone: 'error', message: '这次拖入没有识别到可上传的文件。' });
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
      setAttachmentStatus({ tone: 'info', message: '附件仍在上传中，请稍后再粘贴新的文件。' });
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


  const groupBots = members.filter((m) => {
    if (m.user_id === user.uid) return false;
    return m.is_bot === true || m.account_type === 'bot';
  });
  const normalizedMentionFilter = mentionFilter.toLowerCase();
  const mentionAllAliases = ['所有人', '所有机器人', '全部机器人', 'all'];
  const mentionAllMatches = groupBots.length > 0 && (
    !normalizedMentionFilter
    || mentionAllAliases.some((alias) => alias.includes(normalizedMentionFilter))
  );
  const mentionableBots = [
    ...(mentionAllMatches ? [{
      user_id: STRUCTURED_MENTION_ALL,
      mention_target: STRUCTURED_MENTION_ALL,
      display_name: '所有人',
      username: '全部机器人',
      is_all: true,
    }] : []),
    ...groupBots.filter((m) => {
      if (!mentionFilter) return true;
      const searchable = [m.display_name, m.username, `usr${m.user_id}`]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      return searchable.includes(mentionFilter);
    }),
  ];

  const peerUID = useMemo(() => {
    if (isGroup || !topic || !String(topic).startsWith('p2p_')) return 0;
    const [left, right] = String(topic).replace('p2p_', '').split('_').map((n) => parseInt(n, 10));
    if (!Number.isFinite(left) || !Number.isFinite(right)) return 0;
    return left === user.uid ? right : left;
  }, [isGroup, topic, user.uid]);
  const rosterPeer = availableAgents.find((agent) => agent.uid === peerUID || agent.id === peerUID);
  const resolvedPeerProfile = rosterPeer ? { ...peerProfile, ...rosterPeer } : peerProfile;
  const peerIsBot = Boolean(rosterPeer)
    || resolvedPeerProfile?.bot === true
    || resolvedPeerProfile?.is_bot === true
    || resolvedPeerProfile?.account_type === 'bot';
  const peerIsOwnedBot = Boolean(
    rosterPeer?.is_owner === true
    || rosterPeer?.relation === 'owner'
    || resolvedPeerProfile?.is_owner === true
    || resolvedPeerProfile?.relation === 'owner'
  );
  const isAgentTask = isGroup && Boolean(
    groupInfo?.is_agent_task || groupInfo?.kind === 'agent_task',
  );
  const availableAgentUIDs = useMemo(() => new Set(
    availableAgents
      .map((agent) => Number(agent.uid || agent.id))
      .filter((uid) => Number.isFinite(uid) && uid > 0),
  ), [availableAgents]);
  const taskBotUIDs = useMemo(() => {
    if (!isAgentTask) return [];
    return members
      .filter((member) => member?.is_bot || availableAgentUIDs.has(Number(member?.user_id)))
      .map((member) => Number(member.user_id))
      .filter((uid) => Number.isFinite(uid) && uid > 0);
  }, [availableAgentUIDs, isAgentTask, members]);
  const taskBotUID = taskBotUIDs.length === 1 ? taskBotUIDs[0] : 0;
  const isTwoPersonGroupWithCurrentUser = useMemo(() => {
    if (!isGroup) return false;
    const memberUIDs = new Set(
      members
        .map((member) => Number(member?.user_id))
        .filter((uid) => Number.isFinite(uid) && uid > 0),
    );
    return memberUIDs.size === 2 && memberUIDs.has(Number(user.uid));
  }, [isGroup, members, user.uid]);
  const isOneUserOneAgentGroup = useMemo(() => {
    if (!isTwoPersonGroupWithCurrentUser) return false;
    const peerMember = members.find((member) => Number(member?.user_id) !== Number(user.uid));
    if (!peerMember) return false;
    return Boolean(
      peerMember.is_bot
      || peerMember.account_type === 'bot'
      || availableAgentUIDs.has(Number(peerMember.user_id)),
    );
  }, [availableAgentUIDs, isTwoPersonGroupWithCurrentUser, members, user.uid]);
  const supportsTutorialTasks = isGroup
    ? Boolean(
      isAgentTask
      || groupInfo?.has_bot
      || members.some((member) => member?.is_bot),
    )
    : peerIsBot;
  const composerPlaceholder = isGroup
    ? (
      isOneUserOneAgentGroup
        ? '输入指令，我帮您完成'
        : (supportsTutorialTasks ? '输入消息，@机器人即可回复' : '输入消息')
    )
    : (peerIsBot ? '输入指令，我帮您完成' : '输入消息');
  const displayName = isGroup ? (groupInfo?.name || topicName || topic) : (resolvedPeerProfile?.display_name || resolvedPeerProfile?.username || topicName || topic);
  const displayAvatarUrl = isGroup ? (groupInfo?.avatar_url || topicAvatarUrl) : (resolvedPeerProfile?.avatar_url || topicAvatarUrl);
  const canRegenerateAssistantMessages = !isGroup || isAgentTask;
  useEffect(() => {
    if (isGroup) {
      const groupAgentUID = Number(groupAgent?.uid || groupAgent?.id || 0);
      const isSingleAgentTask = isAgentTask && taskBotUID > 0 && groupAgentUID === taskBotUID;
      const isTwoPersonArtifactGroup = isTwoPersonGroupWithCurrentUser
        && groupAgentUID > 0
        && groupAgent?.cloud_artifacts_enabled === true;
      if (!isSingleAgentTask && !isTwoPersonArtifactGroup) {
        onActiveAgentChange?.(null);
        return;
      }
      const groupAgentIsOwner = groupAgent?.is_owner === true || groupAgent?.relation === 'owner';
      onActiveAgentChange?.({
        uid: groupAgentUID,
        relation: groupAgentIsOwner ? 'owner' : (groupAgent?.relation || 'friend'),
        isOwner: groupAgentIsOwner,
        cloud_artifacts_enabled: groupAgent?.cloud_artifacts_enabled === true,
      });
      return;
    }
    if (!peerIsBot || peerUID <= 0) {
      onActiveAgentChange?.(null);
      return;
    }
    onActiveAgentChange?.({
      uid: peerUID,
      relation: peerIsOwnedBot ? 'owner' : (resolvedPeerProfile?.relation || 'friend'),
      isOwner: peerIsOwnedBot,
      cloud_artifacts_enabled: resolvedPeerProfile?.cloud_artifacts_enabled === true,
    });
  }, [
    groupAgent?.cloud_artifacts_enabled,
    groupAgent?.id,
    groupAgent?.is_owner,
    groupAgent?.relation,
    groupAgent?.uid,
    isAgentTask,
    isGroup,
    isTwoPersonGroupWithCurrentUser,
    onActiveAgentChange,
    peerIsBot,
    peerIsOwnedBot,
    peerUID,
    resolvedPeerProfile?.cloud_artifacts_enabled,
    resolvedPeerProfile?.relation,
    taskBotUID,
  ]);

  useEffect(() => {
    if (isGroup && taskBotUID <= 0) {
      onAgentModelChange?.({ isBot: false, state: 'hidden', summary: null });
      return undefined;
    }
    if (!isGroup && (!peerIsBot || peerUID <= 0)) {
      onAgentModelChange?.({ isBot: false, state: 'hidden', summary: null });
      return undefined;
    }

    const quotaUID = isGroup ? taskBotUID : peerUID;
    let cancelled = false;
    onAgentModelChange?.({ isBot: true, state: 'loading', summary: null });
    const loadQuota = () => {
      api.getAgentQuota(quotaUID)
        .then((response) => {
          if (!cancelled) {
            const summary = response?.summary || null;
            onAgentModelChange?.({
              isBot: true,
              state: summary ? 'ready' : 'unavailable',
              summary,
            });
          }
        })
        .catch(() => {
          if (!cancelled) {
            onAgentModelChange?.({ isBot: true, state: 'unavailable', summary: null });
          }
        });
    };
    loadQuota();
    const interval = window.setInterval(loadQuota, 60000);
    return () => {
      cancelled = true;
      window.clearInterval(interval);
    };
  }, [isGroup, onAgentModelChange, peerIsBot, peerUID, taskBotUID]);

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
      isBot: peerIsBot,
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
        const sender = getSender(msg);
        const previousGroup = groups[groups.length - 1];
        const previousSourceMessages = previousGroup?.type === 'text'
          ? (previousGroup.sourceMessages || [previousGroup.message])
          : [];
        const previousMessage = previousSourceMessages[previousSourceMessages.length - 1];

        if (shouldMergeAssistantReply(previousMessage, msg, previousGroup?.sender, sender, user.uid)) {
          const sourceMessages = [...previousSourceMessages, msg];
          groups[groups.length - 1] = {
            ...previousGroup,
            message: mergeAssistantDisplayMessages(sourceMessages),
            sourceMessages,
          };
          prevSenderUid = senderUid;
          prevTime = msgTime;
          return;
        }

        groups.push({
          type: 'text',
          message: msg,
          sourceMessages: [msg],
          sender,
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
  }, [messages, user.uid, isGroup, memberMap, messageById, peerProfile, peerIsBot, topicName, topic, topicAvatarUrl]);

  const openTutorialTask = (task) => {
    setShowTutorialPicker(false);
    setSelectedTutorialTask(task);
  };

  const dismissTutorialEmptyState = () => {
    localStorage.setItem(tutorialDismissStorageKey(user.uid, topic), '1');
    setTutorialDismissed(true);
  };

  const applyTutorialPrompt = (prompt) => {
    setInput(prompt);
    updateComposerDraft(topic, prompt);
    updateStructuredMentionDraft(topic, []);
    setAttachmentStatus({ tone: 'success', message: '已填入示例任务，你可以直接发送。' });
    setSelectedTutorialTask(null);
    window.setTimeout(() => {
      textareaRef.current?.focus();
      resizeComposerInput();
    }, 0);
  };

  const handleEditMessage = useCallback((message) => {
    const originalText = typeof message?.content === 'string' ? message.content : '';
    if (!originalText.trim()) return;
    setInput(originalText);
    updateComposerDraft(topic, originalText);
    updateStructuredMentionDraft(topic, []);
    setReplyTo(null);
    setAttachmentStatus({ tone: 'success', message: '已将原指令放回输入框，编辑后可直接发送。' });
    window.setTimeout(() => {
      const textarea = textareaRef.current;
      if (!textarea) return;
      textarea.focus();
      textarea.setSelectionRange(originalText.length, originalText.length);
      resizeComposerInput();
    }, 0);
  }, [resizeComposerInput, topic, updateComposerDraft, updateStructuredMentionDraft]);

  const questionNavigationItems = useMemo(
    () => mergeQuestionNavigationItems(
      questionIndexItems,
      collectQuestionNavigationItems(messages, user.uid),
    ),
    [messages, questionIndexItems, user.uid],
  );

  const clearPendingQuestionJump = useCallback(() => {
    pendingQuestionJumpRef.current = '';
    if (questionJumpReleaseTimerRef.current) {
      window.clearTimeout(questionJumpReleaseTimerRef.current);
      questionJumpReleaseTimerRef.current = null;
    }
  }, []);

  const scheduleQuestionJumpRelease = useCallback(() => {
    if (questionJumpReleaseTimerRef.current) {
      window.clearTimeout(questionJumpReleaseTimerRef.current);
    }
    questionJumpReleaseTimerRef.current = window.setTimeout(() => {
      pendingQuestionJumpRef.current = '';
      questionJumpReleaseTimerRef.current = null;
    }, QUESTION_JUMP_RELEASE_DELAY);
  }, []);

  const syncActiveQuestion = useCallback((timeline = timelineRef.current) => {
    if (!timeline) return;
    const anchors = Array.from(timeline.querySelectorAll('[data-conversation-question]'));
    if (anchors.length === 0) {
      setActiveQuestionKey('');
      return;
    }

    const timelineRect = timeline.getBoundingClientRect();
    const readingLine = timelineRect.top + Math.min(160, timelineRect.height * 0.28);
    let nextKey = anchors[0].dataset.conversationQuestion || '';
    for (const anchor of anchors) {
      if (anchor.getBoundingClientRect().top > readingLine) break;
      nextKey = anchor.dataset.conversationQuestion || nextKey;
    }
    const distanceToBottom = timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight;
    if (distanceToBottom <= QUESTION_NAV_BOTTOM_EPSILON) {
      nextKey = anchors[anchors.length - 1].dataset.conversationQuestion || nextKey;
    }
    setActiveQuestionKey((current) => current === nextKey ? current : nextKey);
  }, []);

  React.useLayoutEffect(() => {
    if (pendingQuestionJumpRef.current) return;
    syncActiveQuestion();
  }, [questionNavigationItems, syncActiveQuestion]);

  useEffect(() => () => {
    if (questionJumpReleaseTimerRef.current) {
      window.clearTimeout(questionJumpReleaseTimerRef.current);
    }
  }, []);

  const jumpToQuestion = useCallback(async (questionKey) => {
    const timeline = timelineRef.current;
    if (!timeline) return;
    const target = Array.from(timeline.querySelectorAll('[data-conversation-question]'))
      .find((anchor) => anchor.dataset.conversationQuestion === questionKey);
    clearPendingQuestionJump();
    pendingQuestionJumpRef.current = questionKey;
    setActiveQuestionKey(questionKey);
    if (target) {
      scheduleQuestionJumpRelease();
      target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      return;
    }

    const archivedQuestion = questionNavigationItems.find((item) => item.key === questionKey);
    if (!archivedQuestion?.id) {
      clearPendingQuestionJump();
      return;
    }

    stickToBottomRef.current = false;
    previousScrollRef.current = null;
    try {
      const res = await api.getMessages(
        topic,
        PAGE_SIZE,
        0,
        true,
        archivedQuestion.id + 1,
      );
      if (activeTopicRef.current !== topic) {
        clearPendingQuestionJump();
        return;
      }
      const { visibleMessages } = normalizeHistoryMessages(res.messages || []);
      if (!visibleMessages.some(
        (message, index) => questionNavigationKey(message, index) === questionKey,
      )) {
        clearPendingQuestionJump();
        return;
      }
      setMessages((prev) => mergeMessages(visibleMessages, prev));
    } catch (error) {
      clearPendingQuestionJump();
    }
  }, [clearPendingQuestionJump, questionNavigationItems, scheduleQuestionJumpRelease, topic]);

  React.useLayoutEffect(() => {
    const questionKey = pendingQuestionJumpRef.current;
    const timeline = timelineRef.current;
    if (!questionKey || !timeline) return;
    const target = Array.from(timeline.querySelectorAll('[data-conversation-question]'))
      .find((anchor) => anchor.dataset.conversationQuestion === questionKey);
    if (!target) return;
    scheduleQuestionJumpRelease();
    target.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }, [messages, scheduleQuestionJumpRelease]);

  const handleTimelineScroll = (e) => {
    const el = e.target;
    stickToBottomRef.current = isTimelineNearBottom(el);
    const pendingQuestionKey = pendingQuestionJumpRef.current;
    if (pendingQuestionKey) {
      setActiveQuestionKey((current) => current === pendingQuestionKey ? current : pendingQuestionKey);
      scheduleQuestionJumpRelease();
    } else {
      syncActiveQuestion(el);
    }
    if (el.scrollTop <= HISTORY_AUTO_LOAD_THRESHOLD) {
      loadOlderHistory();
    }
  };

  return (
    <>
      <div
        className={`v3-message-workspace${previewFile ? ' has-preview' : ''}`}
        style={previewFile ? { '--v3-file-preview-width': `${previewWidth}px` } : undefined}
      >
        <div ref={chatColumnRef} className="v3-chat-column">
          <div
            className={`v3-timeline${isDragActive ? ' is-drag-active' : ''}`}
            ref={timelineRef}
            onScroll={handleTimelineScroll}
            onWheel={clearPendingQuestionJump}
            onTouchStart={clearPendingQuestionJump}
            onPointerDown={clearPendingQuestionJump}
            onDragEnter={handleDragEnter}
            onDragOver={handleDragOver}
            onDragLeave={handleDragLeave}
            onDrop={handleDrop}
          >
            <div className="v3-timeline-inner">
              <div className="v3-date-divider">
                <span>聊天记录</span>
              </div>

        {!historyLoaded && (
          <div className="v3-history-state" role="status" aria-live="polite">
            <LoaderCircle className="is-spinning" size={18} aria-hidden="true" />
            <span>正在加载聊天记录...</span>
          </div>
        )}

        {historyLoaded && historyError && messages.length === 0 && (
          <div className="v3-history-state" role="alert">
            <span>{historyError}</span>
            <button type="button" className="v3-history-retry" onClick={() => loadHistory(topic)}>
              <RefreshCw size={15} aria-hidden="true" />
              重新加载
            </button>
          </div>
        )}

        {historyLoaded && historyError && messages.length > 0 && (
          <div className="v3-history-state is-compact" role="status">
            <span>已显示上次记录，本次刷新失败。</span>
            <button type="button" className="v3-history-retry" onClick={() => loadHistory(topic)}>
              <RefreshCw size={14} aria-hidden="true" />
              重试
            </button>
          </div>
        )}

        {olderHistoryError && (
          <div className="v3-history-state is-compact" role="status">
            <span>{olderHistoryError}</span>
            <button type="button" className="v3-history-retry" onClick={loadOlderHistory}>
              <RefreshCw size={14} aria-hidden="true" />
              重试
            </button>
          </div>
        )}

        {loadingOlder && (
          <div className="v3-history-state is-compact oc-history-load" role="status">
            <LoaderCircle className="is-spinning" size={15} aria-hidden="true" />
            <span>{t('loading')}</span>
          </div>
        )}
        
        {supportsTutorialTasks && historyLoaded && !historyError && messages.length === 0 && !runtimePlan && !peerTyping && !tutorialDismissed && (
          <TutorialEmptyState tasks={tutorialTasks} onSelectTask={openTutorialTask} onDismiss={dismissTutorialEmptyState} />
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
                  workingOnly
                  showThinking={showThinking}
                  isConsecutive={group.isConsecutive}
                  onPreviewFile={setPreviewFile}
                  activePreviewFile={previewFile}
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
              questionAnchorKey={group.message.from_uid === user.uid
                ? questionNavigationKey(group.message, i)
                : undefined}
              onReply={() => setReplyTo(group.message)}
              onEdit={group.message.from_uid === user.uid ? handleEditMessage : undefined}
              onRegenerate={canRegenerateAssistantMessages
                && group.message.from_uid !== user.uid
                && isAssistantAuthoredMessage(group.message, group.sender.isBot)
                ? handleRegenerateMessage
                : undefined}
              showThinking={showThinking}
              isConsecutive={group.isConsecutive}
              onPreviewFile={setPreviewFile}
              activePreviewFile={previewFile}
            />
          );
        })}
          {runtimePlan && <RuntimePlanCard plan={runtimePlan} />}
          {peerTyping && (
            <div className="v3-peer-typing" role="status">
              <span className="v3-peer-typing-label">{t('typing')}</span>
            </div>
          )}
          <div ref={bottomRef} />
        </div>
      </div>

      {(questionNavigationItems.length >= 2 || questionIndexHasMore) && (
        <nav
          className="cc-question-navigator"
          aria-label="对话问题导航"
          onMouseEnter={() => void loadQuestionNavigationHistory()}
          onFocusCapture={() => void loadQuestionNavigationHistory()}
        >
          <div className="cc-question-navigator-dots">
            {questionNavigationItems.length === 0 && questionIndexHasMore && (
              <button
                type="button"
                className="cc-question-navigator-item"
                aria-label="加载问题导航"
                title="加载问题导航"
                onClick={() => void loadQuestionNavigationHistory()}
              />
            )}
            {questionNavigationItems.map((item, index) => {
              const isActive = activeQuestionKey === item.key;
              const title = `问题 ${index + 1}：${item.label}`;
              return (
                <button
                  key={item.key}
                  type="button"
                  className={`cc-question-navigator-item${isActive ? ' is-active' : ''}`}
                  aria-label={`跳转到${title}`}
                  aria-current={isActive ? 'true' : undefined}
                  title={title}
                  onClick={() => jumpToQuestion(item.key)}
                />
              );
            })}
          </div>

          <div className="cc-question-navigator-panel" aria-label="问题列表">
            <div className="cc-question-navigator-heading">问题</div>
            <div className="cc-question-navigator-list">
              {questionNavigationItems.map((item, index) => {
                const isActive = activeQuestionKey === item.key;
                const title = `问题 ${index + 1}：${item.label}`;
                return (
                  <button
                    key={`question-list-${item.key}`}
                    type="button"
                    className={`cc-question-list-item${isActive ? ' is-active' : ''}`}
                    aria-label={`跳转到${title}`}
                    aria-current={isActive ? 'true' : undefined}
                    title={title}
                    onClick={() => jumpToQuestion(item.key)}
                  >
                    <span className="cc-question-list-index">{index + 1}</span>
                    <span className="cc-question-list-label">{item.label}</span>
                  </button>
                );
              })}
            </div>
            {(questionIndexLoading || questionIndexLimitReached || questionIndexHasMore) && (
              <div className="cc-question-index-status">
                {questionIndexLoading && <span>正在索引更早问题…</span>}
                {!questionIndexLoading && questionIndexLimitReached && (
                  <span>仅显示最近 {QUESTION_INDEX_MAX_ITEMS} 个问题</span>
                )}
                {!questionIndexLoading && !questionIndexLimitReached && questionIndexHasMore && (
                  <button
                    type="button"
                    className="cc-question-index-action"
                    onClick={() => void loadQuestionNavigationHistory({ continueOlder: true })}
                  >
                    加载更早问题
                  </button>
                )}
              </div>
            )}
          </div>
        </nav>
      )}

      {/* Reply preview bar */}
      {replyTo && (
        <div className="oc-reply-bar">
          <div className="oc-reply-bar-content">
            <span className="oc-reply-bar-label">{t('chat_reply')}: </span>
            <span className="oc-reply-bar-text">
              {typeof replyTo.content === 'string' ? replyTo.content : '[media]'}
            </span>
          </div>
          <button
            type="button"
            className="oc-reply-bar-close"
            aria-label="取消回复"
            onClick={() => setReplyTo(null)}
          >
            x
          </button>
        </div>
      )}

      <ChatComposer
        className={isDragActive ? 'is-drag-active' : ''}
        rootProps={{
          onDragEnter: handleDragEnter,
          onDragOver: handleDragOver,
          onDragLeave: handleDragLeave,
          onDrop: handleDrop,
        }}
        textareaRef={textareaRef}
        value={input}
        placeholder={composerPlaceholder}
        disabled={isSendingMessage}
        onChange={handleInputChange}
        onKeyDown={handleKeyDown}
        onPaste={handlePaste}
        textareaProps={{
          'aria-controls': showMentionPicker ? 'mention-picker' : undefined,
          'aria-expanded': showMentionPicker,
          'aria-haspopup': isGroup ? 'listbox' : undefined,
          'aria-activedescendant': showMentionPicker && mentionableBots.length > 0
            ? `mention-option-${mentionableBots[Math.min(mentionActiveIndex, mentionableBots.length - 1)].user_id}`
            : undefined,
        }}
        attachmentOpen={attachmentMenuOpen}
        attachmentDisabled={isUploadingAttachment || isSendingMessage}
        onAttachmentToggle={() => {
          setAttachmentMenuOpen((open) => !open);
        }}
        attachmentMenu={(
          <div className={`v3-attachment-menu${attachmentMenuOpen ? ' is-open' : ''}`} aria-hidden={!attachmentMenuOpen}>
            <button type="button" onClick={() => { setAttachmentMenuOpen(false); openAttachmentPicker(imageInputRef); }}><Image size={16} /><span>上传图片</span></button>
            <button type="button" onClick={() => { setAttachmentMenuOpen(false); openAttachmentPicker(fileInputRef); }}><FileText size={16} /><span>上传文件</span></button>
            <button type="button" aria-label="手机扫码上传" data-tooltip="手机扫码上传" onClick={() => { setAttachmentMenuOpen(false); openPhoneUploadDialog(); }}><Smartphone size={16} /><span>手机扫码上传</span></button>
            {isGroup && <button type="button" aria-label="@机器人" onClick={() => { setAttachmentMenuOpen(false); openMentionPicker(); }}><span className="v3-at-sign">@</span><span>提及机器人</span></button>}
          </div>
        )}
        onSend={handleSend}
        sendDisabled={isSendingMessage || isUploadingAttachment || (!input.trim() && pendingAttachments.length === 0)}
        stop={canStopActiveBotWorking && !input.trim() && pendingAttachments.length === 0}
        stopDisabled={isStopRequested}
        onStop={handleStopGeneration}
        onCloseMenus={() => {
          setAttachmentMenuOpen(false);
        }}
        attachments={pendingAttachments}
        attachmentRemovalDisabled={isUploadingAttachment || isSendingMessage}
        onRemoveAttachment={(index) => {
          updateAttachmentDraft(topic, (current) => current.filter((_, attachmentIndex) => attachmentIndex !== index));
          setAttachmentStatus(null);
        }}
        overlay={showMentionPicker && isGroup && (
          <div id="mention-picker" className="oc-mention-picker v3-composer-mention-picker" role="listbox" aria-label="可提及的机器人">
            {mentionableBots.map((m, index) => (
              <button
                key={m.user_id}
                id={`mention-option-${m.user_id}`}
                className={`oc-mention-item${index === mentionActiveIndex ? ' is-active' : ''}`}
                type="button"
                role="option"
                aria-selected={index === mentionActiveIndex}
                onMouseDown={(event) => {
                  event.preventDefault();
                  insertMention(m);
                }}
                onMouseEnter={() => setMentionActiveIndex(index)}
              >
                {m.is_all
                  ? <span className="oc-mention-all-icon" aria-hidden="true"><Users size={15} /></span>
                  : <Avatar name={m.display_name || m.username} src={m.avatar_url} size={24} isBot />}
                <span className="oc-mention-item-copy">
                  <span className="oc-mention-item-name">{m.display_name || m.username || `usr${m.user_id}`}</span>
                  <span className="oc-mention-item-handle">{m.is_all ? '全部机器人' : `@usr${m.user_id}`}</span>
                </span>
              </button>
            ))}
            {mentionableBots.length === 0 && (
              <div className="oc-mention-empty">没有匹配的机器人</div>
            )}
          </div>
        )}
        boxOverlay={isDragActive && (
          <div className="v3-drop-overlay" aria-hidden="true">
            <div className="v3-drop-title">拖放文件以上传</div>
            <div className="v3-drop-subtitle">支持图片、文件和文件夹，附件会先放在这里等待发送。</div>
          </div>
        )}
        notices={(
          <>
            {activeBotWorking && (
              <div className="v3-live-input-status" role="status">
                {canStopActiveBotWorking
                  ? (isStopRequested ? '已请求 CatsCo 停止当前工作。' : 'CatsCo 正在处理，可点击红色按钮停止。')
                  : 'CatsCo 正在回复其他成员。'}
              </div>
            )}
            {(attachmentStatus?.message || isUploadingAttachment || pendingAttachments.length > 0) && (
              <div
                className={`v3-live-input-status v3-attachment-notice v3-live-input-status-${attachmentStatus?.tone || 'info'}`}
                role="status"
              >
                <span>
                  {attachmentStatus?.tone === 'error'
                    ? attachmentStatus.message
                    : isUploadingAttachment
                      ? (attachmentStatus?.message || '正在上传附件...')
                      : attachmentStatus?.message
                        || (pendingAttachments.length > 0
                          ? `${pendingAttachments.length} 个附件待发送${pendingAttachments.length === 1 ? `：${pendingAttachments[0].name}` : ''}`
                          : '')}
                </span>
              </div>
            )}
          </>
        )}
      />
      <input ref={imageInputRef} type="file" accept={IMAGE_UPLOAD_ACCEPT} multiple style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'image')} />
      <input ref={fileInputRef} type="file" multiple style={{ display: 'none' }} onChange={(e) => handleFileUpload(e, 'file')} />
      {phoneUploadDialogOpen && (
        <div
          className="v3-phone-upload-backdrop"
          role="dialog"
          aria-modal="true"
          aria-label="手机扫码上传"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) closePhoneUploadDialog();
          }}
        >
          <div className="v3-phone-upload-modal">
            <div className="v3-phone-upload-header">
              <div>
                <div className="v3-phone-upload-title">手机扫码上传</div>
                <div className="v3-phone-upload-subtitle">用手机打开后可多选图片或文件上传到当前会话。</div>
              </div>
              <button className="v3-tool" type="button" aria-label="关闭手机上传" onClick={closePhoneUploadDialog}>
                <X size={16} strokeWidth={2} />
              </button>
            </div>
            <div className="v3-phone-upload-body">
              {phoneUploadError ? (
                <div className="v3-phone-upload-error">{phoneUploadError}</div>
              ) : phoneUploadLink ? (
                <>
                  <QRCode value={phoneUploadLink} size={180} />
                  <div className="v3-phone-upload-link">{phoneUploadLink}</div>
                </>
              ) : (
                <div className="v3-phone-upload-loading">正在创建上传入口...</div>
              )}
            </div>
          </div>
        </div>
      )}
      </div>
        {previewFile && (
          <div className="v3-file-preview-shell">
            <div
              className="v3-preview-resize-handle"
              role="separator"
              aria-label="调整预览宽度"
              aria-orientation="vertical"
              tabIndex={0}
              onPointerDown={handlePreviewResizePointerDown}
              onKeyDown={handlePreviewResizeKeyDown}
              title="拖动调整预览宽度"
            />
            <FilePreviewPanel file={previewFile} onClose={() => setPreviewFile(null)} backgroundRef={chatColumnRef} />
          </div>
        )}
      </div>
      {showTutorialPicker && (
        <TutorialTaskPicker
          tasks={tutorialTasks}
          onClose={() => setShowTutorialPicker(false)}
          onSelectTask={openTutorialTask}
        />
      )}
      {selectedTutorialTask && (
        <TutorialTaskModal
          task={selectedTutorialTask}
          desktopReady={localAssistantStatus === 'connected'}
          onClose={() => setSelectedTutorialTask(null)}
          onBack={() => {
            setSelectedTutorialTask(null);
            setShowTutorialPicker(true);
          }}
          onApplyPrompt={applyTutorialPrompt}
          onOpenDesktopConnect={onOpenDesktopConnect}
        />
      )}
    </>
  );
}

function tutorialDismissStorageKey(uid, topic) {
  return `cc_tutorial_empty_dismissed:v1:${uid || 'anon'}:${topic || 'unknown'}`;
}

function hasFileDrag(dataTransfer) {
  if (!dataTransfer?.types) return false;
  return Array.from(dataTransfer.types).includes('Files');
}


function validateAttachmentBeforeUpload(file, type) {
  if (!file) return '未找到可上传的文件。';
  if (file.size > MAX_ATTACHMENT_SIZE) {
    return `文件过大：${(file.size / 1024 / 1024).toFixed(1)}MB。当前最多支持 ${MAX_ATTACHMENT_SIZE_MB}MB。`;
  }
  if (type !== 'image') return '';

  return validateImageUpload(file);
}

function formatUploadError(err) {
  const message = String(err?.message || '上传失败');
  if (message.includes('413') || message.includes('Payload Too Large')) {
    return `上传失败：文件超过 ${MAX_ATTACHMENT_SIZE_MB}MB 限制。`;
  }
  if (message.includes('invalid image type')) {
    return '上传失败：当前仅支持 JPG、PNG、GIF、WebP 图片。';
  }
  if (message.includes('file type not allowed')) {
    return '上传失败：该文件类型暂不支持。';
  }
  if (message.includes('Unexpected token') || message.includes('invalid server response') || message.includes('JSON')) {
    return '上传失败：服务器返回了无法识别的响应。';
  }
  return `上传失败：${message}`;
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

function isAssistantAuthoredMessage(message, senderIsBot = false) {
  return Boolean(
    senderIsBot
    || message?.role === 'assistant'
    || message?.metadata?.role === 'assistant'
    || message?.metadata?.sender_type === 'agent',
  );
}

function assistantReplyTurnKey(message) {
  const metadata = message?.metadata || {};
  const value = metadata.turn_id
    ?? metadata.turnId
    ?? metadata.response_id
    ?? metadata.responseId
    ?? metadata.run_id
    ?? metadata.runId
    ?? metadata.stream_id
    ?? message?._stream_id;
  return value == null ? '' : String(value).trim();
}

function messageCreatedAtMs(message) {
  const timestamp = new Date(message?.created_at || '').getTime();
  return Number.isFinite(timestamp) ? timestamp : null;
}

function hasRichMessageBlocks(message) {
  return Array.isArray(message?.content_blocks) && message.content_blocks.length > 0;
}

function shouldMergeAssistantReply(previous, current, previousSender, currentSender, currentUserUid) {
  if (!previous || !current) return false;
  if (previous.from_uid !== current.from_uid || current.from_uid === currentUserUid) return false;
  if (previous.topic_id && current.topic_id && previous.topic_id !== current.topic_id) return false;
  if (!isFinalTextMessage(previous) || !isFinalTextMessage(current)) return false;
  if (!isAssistantAuthoredMessage(previous, previousSender?.isBot)) return false;
  if (!isAssistantAuthoredMessage(current, currentSender?.isBot)) return false;
  if (previous.reply_to || current.reply_to) return false;
  if (previous._streaming || current._streaming) return false;
  if (hasRichMessageBlocks(previous) || hasRichMessageBlocks(current)) return false;

  const previousTurnKey = assistantReplyTurnKey(previous);
  const currentTurnKey = assistantReplyTurnKey(current);
  if (previousTurnKey || currentTurnKey) {
    return Boolean(previousTurnKey && currentTurnKey && previousTurnKey === currentTurnKey);
  }

  const previousTime = messageCreatedAtMs(previous);
  const currentTime = messageCreatedAtMs(current);
  if (previousTime == null || currentTime == null) return false;
  const gap = currentTime - previousTime;
  return gap >= 0 && gap <= ASSISTANT_REPLY_MERGE_WINDOW_MS;
}

function mergeAssistantDisplayMessages(sourceMessages) {
  const lastMessage = sourceMessages[sourceMessages.length - 1];
  return {
    ...lastMessage,
    content: sourceMessages
      .map((message) => String(message.content || '').trim())
      .filter(Boolean)
      .join('\n\n'),
    content_blocks: [],
    _display_source_messages: sourceMessages,
  };
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

function resolveWorkingInitiatorUid(messages, workingIndex, botUIDs) {
  const workingMessage = messages[workingIndex];
  const replyTo = Number(workingMessage?.reply_to || 0);
  if (replyTo > 0) {
    const repliedMessage = messages.find((message) => Number(message?.id || message?.seq_id) === replyTo);
    const repliedUID = Number(repliedMessage?.from_uid);
    if (
      repliedMessage
      && isFinalTextMessage(repliedMessage)
      && Number.isFinite(repliedUID)
      && repliedUID > 0
      && !botUIDs.has(repliedUID)
      && !isAssistantAuthoredMessage(repliedMessage)
    ) {
      return repliedUID;
    }
  }

  const metadata = workingMessage?.metadata || {};
  const metadataUID = Number(
    metadata.initiator_uid
    ?? metadata.requester_uid
    ?? metadata.trigger_uid
    ?? 0,
  );
  if (Number.isFinite(metadataUID) && metadataUID > 0 && !botUIDs.has(metadataUID)) {
    return metadataUID;
  }

  for (let index = workingIndex - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (!isFinalTextMessage(message)) continue;
    const senderUID = Number(message?.from_uid);
    if (!Number.isFinite(senderUID) || senderUID <= 0) continue;
    if (botUIDs.has(senderUID) || isAssistantAuthoredMessage(message)) continue;
    return senderUID;
  }
  return 0;
}

function workingMessageKey(message) {
  return [
    message?.id ?? message?.seq_id ?? message?.seq ?? message?._stream_id ?? '',
    message?.type || message?.msg_type || '',
    message?.created_at || '',
    getComparableContent(message?.content),
  ].join(':');
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
  const normalized = String(uidStr);
  if (normalized.startsWith('usr')) {
    return parseInt(normalized.slice(3), 10) || 0;
  }
  return parseInt(normalized, 10) || 0;
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
