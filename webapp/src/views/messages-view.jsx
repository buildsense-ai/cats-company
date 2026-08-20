import React, { useState, useRef, useEffect, useCallback, useId, useMemo } from 'react';
import { createPortal } from 'react-dom';
import { ArrowLeft, Check, CheckCircle2, CheckSquare, ChevronDown, ChevronLeft, ChevronRight, Circle, CircleDot, Download, FileText, Image, ImageDown, LoaderCircle, RefreshCw, Smartphone, Users, X } from 'lucide-react';
import { api, resolveMediaURL, wsSendMessage, wsSendStreamCancel, wsSendTyping, wsSendRead, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import ChatMessage, { createCloudArtifactPreviewFile, FilePreviewPanel } from '../widgets/chat-message';
import Avatar from '../widgets/avatar';
import CloudArtifactsPanel from '../widgets/cloud-artifacts-panel';
import QRCode from '../widgets/qr-code';
import { TutorialEmptyState, TutorialTaskModal, TutorialTaskPicker, TUTORIAL_TASKS } from '../widgets/tutorial-tasks';
import { attachmentFromContentBlock, attachmentIdentity, clearChatAttachmentDrag, hasChatAttachmentDrag, readChatAttachmentDrag } from '../chat-attachment-drag';
import ChatComposer from '../widgets/chat-composer';
import { useFeedback } from '../components/feedback-system';
import { insertTranscriptAtSelection } from '../utils/composer-transcript';
import { readStorageValue, writeStorageValue } from '../utils/storage-access';
import { IMAGE_UPLOAD_ACCEPT, MAX_ATTACHMENT_SIZE, MAX_ATTACHMENT_SIZE_MB, inferAttachmentType, validateImageUpload } from '../utils/upload-rules';
import {
  artifactContextRefFromSnapshot,
  artifactRefFromPreviewFile,
  artifactURLForVersion,
  requestArtifactPageContext,
  withArtifactContextRef,
} from '../artifact-context';
import {
  conversationShareMessageKey,
  conversationShareText,
  downloadConversationShareImage,
  downloadConversationShareImages,
  isMobileConversationShareBrowser,
  openConversationShareImageForManualSave,
  renderConversationShareImage,
} from '../utils/conversation-share-image';

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
const LONG_PASTE_CHAR_THRESHOLD = 4000;
const LONG_PASTE_LINE_THRESHOLD = 60;
const LONG_PASTE_MULTILINE_CHAR_THRESHOLD = 2000;
const HISTORY_AUTO_LOAD_THRESHOLD = 120;
const HISTORY_REQUEST_TIMEOUT_MS = 15000;
const HISTORY_AUTO_FILL_MAX_PAGES = 6;
const STICK_TO_BOTTOM_THRESHOLD = 96;
const QUESTION_JUMP_RELEASE_DELAY = 240;
const PENDING_HISTORY_MATCH_CLOCK_SKEW_MS = 5 * 60 * 1000;
const CONSECUTIVE_HUMAN_MESSAGE_WINDOW_MS = 5 * 60 * 1000;
const IDENTITY_TEXT_FIELDS = ['display_name', 'username', 'avatar_url', 'name'];
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
const CLOUD_ARTIFACTS_CHANGED_EVENT = 'cc:cloud-artifacts-changed';
const ARTIFACT_REGISTRY_POLL_MS = 5000;
const ARTIFACT_SNAPSHOT_TIMEOUT_MS = 2200;
const DELIVERY_ARTIFACT_TYPES = new Set(['file', 'image', 'audio', 'voice']);

function artifactRefreshFileKey(file) {
  if (!file?.artifact_id || !file?.url) return '';
  return [
    Number(file.artifact_agent_uid || 0),
    String(file.artifact_id),
    Number(file.publish_version || 0),
    String(file.url),
  ].join('|');
}

function artifactMessageFocusFromPreviewFile(file, topic, topicGeneration = 0) {
  const agentUid = Number(file?.artifact_agent_uid || 0);
  const artifactRef = artifactRefFromPreviewFile(file, agentUid);
  const previewKey = artifactRefreshFileKey(file);
  if (!topic || agentUid <= 0 || !artifactRef || !previewKey) return null;
  return {
    topic,
    topicGeneration,
    agentUid,
    artifactId: artifactRef.id,
    displayedVersion: Number(artifactRef.displayed_version || 0),
    url: String(file.url),
    previewKey,
    artifactRef,
  };
}

function artifactBindingMatchesFocus(binding, focus) {
  return Boolean(binding
    && focus
    && binding.artifactId === focus.artifactId
    && Number(binding.agentUid || 0) === focus.agentUid
    && String(binding.url || '') === focus.url);
}
const MAX_CONVERSATION_SHARE_MESSAGES = 50;

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
    || !sameUID(message?.from_uid, userUID)
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
  return clampPreviewWidth(Number(readStorageValue(PREVIEW_WIDTH_STORAGE_KEY)) || PREVIEW_WIDTH_DEFAULT);
}

function savePreviewWidth(width) {
  writeStorageValue(PREVIEW_WIDTH_STORAGE_KEY, String(Math.round(width)));
}

function resolvePhoneUploadLink(uploadUrl) {
  if (!uploadUrl) return '';
  if (/^https?:\/\//i.test(uploadUrl)) return uploadUrl;
  const normalizedPath = uploadUrl.startsWith('/') ? uploadUrl : `/${uploadUrl}`;
  return `${window.location.origin}${normalizedPath}`;
}

function imageGalleryItemId(message, blockIndex, payload) {
  const src = payload?.url || payload?.thumbnail || '';
  if (!src) return '';
  return `${message?.id || message?.seq_id || 'message'}:${blockIndex}:${src}`;
}

function structuredMentionToken(selection) {
  const target = typeof selection?.target === 'string' ? selection.target : '';
  if (target === STRUCTURED_MENTION_ALL) return '@所有人';
  const label = typeof selection?.label === 'string' && selection.label.trim()
    ? selection.label.trim()
    : target;
  return `@${label}`;
}

function isStructuredMentionSelectionIntact(text, selection) {
  const start = Number.isInteger(selection?.start) ? selection.start : -1;
  const end = Number.isInteger(selection?.end) ? selection.end : -1;
  const token = structuredMentionToken(selection);
  if (start < 0 || end <= start || end > text.length || text.slice(start, end) !== token) return false;
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
    const label = typeof selection?.label === 'string' && selection.label.trim()
      ? selection.label.trim()
      : target;
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

    const nextSelection = {
      target,
      ...(selection?.label ? { label: target === STRUCTURED_MENTION_ALL ? '所有人' : label } : {}),
      start,
      end,
    };
    return isStructuredMentionSelectionIntact(next, nextSelection) ? [nextSelection] : [];
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
    return isStructuredMentionSelectionIntact(value, selection) ? [target] : [];
  }))];
}

export function canonicalizeStructuredMentionText(text, selections = []) {
  const value = typeof text === 'string' ? text : '';
  if (!Array.isArray(selections) || selections.length === 0) return value;

  return selections
    .filter((selection) => {
      const target = typeof selection?.target === 'string' ? selection.target : '';
      return (target === STRUCTURED_MENTION_ALL || /^usr\d+$/u.test(target))
        && isStructuredMentionSelectionIntact(value, selection);
    })
    .sort((left, right) => right.start - left.start)
    .reduce((result, selection) => {
      const canonicalToken = selection.target === STRUCTURED_MENTION_ALL
        ? '@所有人'
        : `@${selection.target}`;
      return `${result.slice(0, selection.start)}${canonicalToken}${result.slice(selection.end)}`;
    }, value);
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

function hasIdentityText(value) {
  return value != null && String(value).trim() !== '';
}

function mergeIdentityRecord(fallback, preferred) {
  const fallbackRecord = fallback && typeof fallback === 'object' ? fallback : null;
  const preferredRecord = preferred && typeof preferred === 'object' ? preferred : null;
  if (!fallbackRecord && !preferredRecord) return null;

  const merged = { ...(fallbackRecord || {}), ...(preferredRecord || {}) };
  IDENTITY_TEXT_FIELDS.forEach((field) => {
    if (!hasIdentityText(preferredRecord?.[field]) && hasIdentityText(fallbackRecord?.[field])) {
      merged[field] = fallbackRecord[field];
    }
  });
  return merged;
}

function messageActorIdentity(message) {
  const actor = message?.metadata?.catsco_identity?.actor;
  if (!actor || typeof actor !== 'object' || Array.isArray(actor)) return null;

  const messageUID = parseUid(message?.from_uid || message?.from);
  const actorUID = parseUid(actor.user_id || actor.uid || actor.id);
  if (messageUID > 0 && actorUID !== messageUID) return null;
  return actor;
}

function artifactURLsInMessage(message) {
  if (message?._streaming) return [];
  const textBlocks = Array.isArray(message?.content_blocks)
    ? message.content_blocks.filter((block) => block?.type === 'text').map((block) => block.text || '')
    : [];
  const text = [typeof message?.content === 'string' ? message.content : '', ...textBlocks].join('\n');
  return (text.match(/https?:\/\/[^\s<>"'`]+/gi) || [])
    .map((url) => url.replace(/[)\]}>.,;:!?，。；：！？]+$/g, ''))
    .filter(Boolean)
    .sort();
}

function artifactNotificationURL(value) {
  try {
    const parsed = new URL(String(value || ''));
    parsed.hash = '';
    parsed.search = '';
    parsed.pathname = parsed.pathname.replace(/\/+$/, '/') || '/';
    return parsed.toString();
  } catch {
    return '';
  }
}

function artifactPublishURLsInMessage(message) {
  const textBlocks = Array.isArray(message?.content_blocks)
    ? message.content_blocks.filter((block) => block?.type === 'text').map((block) => block.text || '')
    : [];
  const text = [typeof message?.content === 'string' ? message.content : '', ...textBlocks].join('\n');
  if (!/(发布|共享到云端|上传到云端)/u.test(text)) return [];
  return artifactURLsInMessage(message);
}

function artifactPublishCandidates(messages) {
  return (messages || []).flatMap((message, index) => {
    const messageKey = String(message?.id || message?.seq_id || message?.created_at || index);
    return artifactPublishURLsInMessage(message).map((url) => ({
      key: `${messageKey}|${artifactNotificationURL(url)}`,
      url: artifactNotificationURL(url),
    })).filter((candidate) => candidate.url);
  });
}

function cacheHistoryPage(cache, key, entry) {
  cache.delete(key);
  cache.set(key, entry);
  while (cache.size > HISTORY_CACHE_MAX_TOPICS) {
    cache.delete(cache.keys().next().value);
  }
}

export default function MessagesView({
  topBar = null,
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
  cloudArtifactsRequest,
  onCloudArtifactsRequestConsumed,
  messageLocationRequest,
  onBackToSearch,
}) {
  const feedback = useFeedback();
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
  const [previewImageId, setPreviewImageId] = useState('');
  const [cloudArtifactsAgentUID, setCloudArtifactsAgentUID] = useState(0);
  const [cloudArtifactsListOpen, setCloudArtifactsListOpen] = useState(false);
  const [cloudArtifactsReturnOpen, setCloudArtifactsReturnOpen] = useState(false);
  const [cloudArtifactsTab, setCloudArtifactsTab] = useState('files');
  const [artifactRegistryState, setArtifactRegistryState] = useState({ agentUID: 0, artifacts: [] });
  const [artifactRegistryRefreshEpoch, setArtifactRegistryRefreshEpoch] = useState(0);
  const [artifactRegistryRevision, setArtifactRegistryRevision] = useState(0);
  const [pendingArtifactRefresh, setPendingArtifactRefresh] = useState(null);
  const [previewWidth, setPreviewWidth] = useState(() => loadPreviewWidth());
  const [hasMoreHistory, setHasMoreHistory] = useState(false);
  const [historyLoaded, setHistoryLoaded] = useState(false);
  const [highlightedMessageId, setHighlightedMessageId] = useState(0);
  const [refreshingHistory, setRefreshingHistory] = useState(false);
  const [historyError, setHistoryError] = useState('');
  const [olderHistoryError, setOlderHistoryError] = useState('');
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [autoHistoryLimitReached, setAutoHistoryLimitReached] = useState(false);
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
  const [tutorialDismissed, setTutorialDismissed] = useState(() => (
    readStorageValue(tutorialDismissStorageKey(user.uid, topic)) === '1'
  ));
  const [attachmentMenuOpen, setAttachmentMenuOpen] = useState(false);
  const [availableAgents, setAvailableAgents] = useState([]);
  const [isSendingMessage, setIsSendingMessage] = useState(false);
  const [awaitingAgentReply, setAwaitingAgentReply] = useState(false);
  const [activeQuestionKey, setActiveQuestionKey] = useState('');
  const [questionIndexItems, setQuestionIndexItems] = useState([]);
  const [questionIndexLoading, setQuestionIndexLoading] = useState(false);
  const [questionIndexHasMore, setQuestionIndexHasMore] = useState(false);
  const [questionIndexLimitReached, setQuestionIndexLimitReached] = useState(false);
  const [showThinking, setShowThinking] = useState(() => {
    const saved = readStorageValue('cc_show_thinking');
    return saved === null ? true : saved === 'true';
  });
  const [conversationShareMode, setConversationShareMode] = useState(false);
  const [conversationShareSelectedKeys, setConversationShareSelectedKeys] = useState([]);
  const [conversationSharePreviewOpen, setConversationSharePreviewOpen] = useState(false);
  const [conversationShareImages, setConversationShareImages] = useState([]);
  const [conversationSharePreviewPage, setConversationSharePreviewPage] = useState(0);
  const [conversationShareGenerating, setConversationShareGenerating] = useState(false);
  const [conversationShareDownloading, setConversationShareDownloading] = useState(false);
  const [conversationShareError, setConversationShareError] = useState('');
  const [conversationShareManualSaveAvailable, setConversationShareManualSaveAvailable] = useState(false);
  const conversationSharePreviewImage = conversationShareImages[conversationSharePreviewPage] || null;
  const imageGallery = useMemo(() => {
    const result = [];
    (messages || []).forEach((message, messageIndex) => {
      const blocks = contentBlocksFromMessage(message);
      blocks.forEach((block, blockIndex) => {
        if (block?.type !== 'image' || !block?.payload) return;
        const payload = block.payload;
        const src = payload.url || payload.thumbnail;
        if (!src) return;
        const id = imageGalleryItemId(message, blockIndex, payload) || `${message.id || message.seq_id || `message-${messageIndex}`}:${blockIndex}:${src}`;
        result.push({ id, payload });
      });
      if (blocks.length === 0) {
        const structured = parseStructuredMessageContent(message?.content);
        if (structured?.type === 'image' && structured.payload) {
          const payload = structured.payload;
          const src = payload.url || payload.thumbnail;
          if (src) result.push({ id: imageGalleryItemId(message, 0, payload), payload });
        }
      }
    });
    return result;
  }, [messages]);
  const sidePanelOpen = Boolean(previewFile || cloudArtifactsListOpen);
  const bottomRef = useRef(null);
  const previewImageTriggerRef = useRef(null);
  const chatColumnRef = useRef(null);
  const lastTypingSent = useRef(0);
  const peerTypingTimer = useRef(null);
  const liveWorkingTimer = useRef(null);
  const timelineRef = useRef(null);
  const pendingQuestionJumpRef = useRef('');
  const questionJumpReleaseTimerRef = useRef(null);
  const visibleQuestionAnchorsRef = useRef(new Map());
  const messageHighlightTimerRef = useRef(null);
  const previousScrollRef = useRef(null);
  const stickToBottomRef = useRef(true);
  const fileInputRef = useRef(null);
  const imageInputRef = useRef(null);
  const textareaRef = useRef(null);
  const mentionRangeRef = useRef(null);
  const dragDepthRef = useRef(0);
  const runtimePlanRef = useRef(null);
  const runtimePlanClearTimer = useRef(null);
  const activeArtifactFrameRef = useRef(null);
  const activeArtifactFocusRef = useRef(null);
  const activeArtifactSnapshotRef = useRef(null);
  const historyOffsetRef = useRef(0);
  const historyBeforeIDRef = useRef(0);
  const historyRequestRef = useRef(0);
  const historyLoadingRef = useRef(false);
  const historyAbortControllerRef = useRef(null);
  const olderHistoryAbortControllerRef = useRef(null);
  const galleryHistoryLoadingRef = useRef(false);
  const autoHistoryPageCountRef = useRef(0);
  const groupMembersRequestRef = useRef(0);
  const peerProfileRequestRef = useRef(0);
  const artifactRegistryRequestRef = useRef(0);
  const activeArtifactAgentUIDRef = useRef(0);
  const artifactShareNotificationRef = useRef({
    topic: '',
    initialized: false,
    observed: new Set(),
    pending: new Map(),
  });
  const historyCacheRef = useRef(new Map());
  const groupProfileCacheRef = useRef(new Map());
  const hasMoreHistoryRef = useRef(false);
  const loadingOlderRef = useRef(false);
  const activeTopicRef = useRef(topic);
  const artifactTopicRef = useRef(topic);
  const artifactTopicGenerationRef = useRef(0);
  const questionIndexCacheRef = useRef(new Map());
  const questionIndexRequestRef = useRef(0);
  const questionIndexLoadingRef = useRef(false);
  const questionIndexAbortControllerRef = useRef(null);
  const questionJumpAbortControllerRef = useRef(null);
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
  const conversationShareGenerateButtonRef = useRef(null);
  const conversationSharePreviewRef = useRef(null);
  const conversationSharePreviewCloseRef = useRef(null);

  if (artifactTopicRef.current !== topic) {
    artifactTopicRef.current = topic;
    artifactTopicGenerationRef.current += 1;
    activeArtifactFocusRef.current = null;
    activeArtifactFrameRef.current = null;
  }

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
    if (!sidePanelOpen) return;
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
  }, [sidePanelOpen, updatePreviewWidth]);

  const handlePreviewResizeKeyDown = useCallback((event) => {
    if (!sidePanelOpen) return;
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
  }, [sidePanelOpen, updatePreviewWidth]);

  const invalidateArtifactSnapshot = useCallback((snapshot = activeArtifactSnapshotRef.current) => {
    const contextRef = String(snapshot?.contextRef || '');
    if (!contextRef) return;
    if (activeArtifactSnapshotRef.current?.contextRef === contextRef) {
      activeArtifactSnapshotRef.current = null;
    }
    api.invalidateArtifactContextSnapshot(contextRef, { timeoutMs: ARTIFACT_SNAPSHOT_TIMEOUT_MS })
      .catch(() => {});
  }, []);

  const clearActiveArtifactFocus = useCallback(() => {
    invalidateArtifactSnapshot();
    activeArtifactFocusRef.current = null;
    activeArtifactFrameRef.current = null;
  }, [invalidateArtifactSnapshot]);

  const setPreviewFileWithFocus = useCallback((file) => {
    invalidateArtifactSnapshot();
    activeArtifactFocusRef.current = artifactMessageFocusFromPreviewFile(
      file,
      artifactTopicRef.current,
      artifactTopicGenerationRef.current,
    );
    activeArtifactFrameRef.current = null;
    setPreviewFile(file);
  }, [invalidateArtifactSnapshot]);

  const handleRemoteArtifactFrameChange = useCallback((binding) => {
    activeArtifactFrameRef.current = artifactBindingMatchesFocus(
      binding,
      activeArtifactFocusRef.current,
    ) ? binding : null;
  }, []);

  const openFilePreview = useCallback((file) => {
    setCloudArtifactsAgentUID(0);
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(false);
    setPendingArtifactRefresh(null);
    setPreviewFileWithFocus(file);
  }, [setPreviewFileWithFocus]);

  const closeSidePanel = useCallback(() => {
    setPendingArtifactRefresh(null);
    clearActiveArtifactFocus();
    setPreviewFile(null);
    setCloudArtifactsAgentUID(0);
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(false);
    setCloudArtifactsTab('files');
  }, [clearActiveArtifactFocus]);

  const previewCloudArtifact = useCallback((artifact) => {
    setPendingArtifactRefresh(null);
    setPreviewFileWithFocus(createCloudArtifactPreviewFile({
      ...artifact,
      agent_uid: artifact?.agent_uid || cloudArtifactsAgentUID,
    }));
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(true);
  }, [cloudArtifactsAgentUID, setPreviewFileWithFocus]);

  const captureArtifactMessageContext = useCallback(async () => {
    const focus = activeArtifactFocusRef.current;
    const topicGeneration = artifactTopicGenerationRef.current;
    const empty = { contextRef: '' };
    if (!focus
      || focus.topic !== topic
      || focus.topicGeneration !== topicGeneration
      || artifactTopicRef.current !== topic
      || activeTopicRef.current !== topic
      || focus.agentUid !== activeArtifactAgentUIDRef.current) return empty;

    const binding = activeArtifactFrameRef.current;
    const hasMatchingBinding = artifactBindingMatchesFocus(binding, focus);
    let pageContext = null;
    if (hasMatchingBinding) {
      pageContext = await requestArtifactPageContext(binding, focus.artifactRef);
    }

    if (activeArtifactFocusRef.current !== focus
      || (hasMatchingBinding && activeArtifactFrameRef.current !== binding)
      || artifactTopicRef.current !== topic
      || artifactTopicGenerationRef.current !== topicGeneration
      || activeTopicRef.current !== topic
      || activeArtifactAgentUIDRef.current !== focus.agentUid) return empty;

    let response;
    try {
      response = await api.createArtifactContextSnapshot({
        topic_id: topic,
        artifact_ref: focus.artifactRef,
        ...(pageContext ? { page_context: pageContext } : {}),
      }, { timeoutMs: ARTIFACT_SNAPSHOT_TIMEOUT_MS });
    } catch {
      return empty;
    }
    const contextRef = artifactContextRefFromSnapshot(response);
    if (!contextRef) return empty;
    const snapshot = {
      contextRef,
      topic,
      topicGeneration,
      agentUid: focus.agentUid,
      artifactId: focus.artifactId,
    };
    if (activeArtifactFocusRef.current !== focus
      || artifactTopicRef.current !== topic
      || artifactTopicGenerationRef.current !== topicGeneration
      || activeTopicRef.current !== topic
      || activeArtifactAgentUIDRef.current !== focus.agentUid) {
      invalidateArtifactSnapshot(snapshot);
      return empty;
    }
    activeArtifactSnapshotRef.current = snapshot;
    return { contextRef };
  }, [invalidateArtifactSnapshot, topic]);

  const previewAgentFile = useCallback((file) => {
    setPendingArtifactRefresh(null);
    setPreviewFileWithFocus({
      name: file.name,
      url: file.url,
      file_key: file.file_key,
      mime_type: file.mime_type,
      size: file.size,
    });
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(true);
  }, [setPreviewFileWithFocus]);

  const returnToCloudArtifacts = useCallback(() => {
    setPendingArtifactRefresh(null);
    clearActiveArtifactFocus();
    setPreviewFile(null);
    setCloudArtifactsListOpen(true);
    setCloudArtifactsReturnOpen(false);
  }, [clearActiveArtifactFocus]);

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
    setTutorialDismissed(readStorageValue(tutorialDismissStorageKey(user.uid, topic)) === '1');
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
    invalidateArtifactSnapshot();
    questionIndexRequestRef.current += 1;
    questionIndexAbortControllerRef.current?.abort();
    questionJumpAbortControllerRef.current?.abort();
    if (runtimePlanClearTimer.current) {
      clearTimeout(runtimePlanClearTimer.current);
    }
    if (liveWorkingTimer.current) {
      clearTimeout(liveWorkingTimer.current);
    }
  }, [invalidateArtifactSnapshot]);

  // Load message history and group members when topic changes
  useEffect(() => {
    if (!topic) return;
    clearChatAttachmentDrag();
    historyAbortControllerRef.current?.abort();
    olderHistoryAbortControllerRef.current?.abort();
    questionIndexAbortControllerRef.current?.abort();
    questionJumpAbortControllerRef.current?.abort();
    groupMembersRequestRef.current += 1;
    peerProfileRequestRef.current += 1;
    invalidateArtifactSnapshot();
    activeTopicRef.current = topic;
    activeArtifactFocusRef.current = null;
    activeArtifactFrameRef.current = null;
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
    setCloudArtifactsAgentUID(0);
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(false);
    setCloudArtifactsTab('files');
    const cachedGroupProfile = isGroup && groupId
      ? groupProfileCacheRef.current.get(String(groupId))
      : null;
    setMembers(cachedGroupProfile?.members || []);
    setGroupInfo(cachedGroupProfile?.group || null);
    setPeerProfile(null);
    setHistoryLoaded(Boolean(cachedHistory));
    setHistoryError('');
    setOlderHistoryError('');
    setAutoHistoryLimitReached(false);
    autoHistoryPageCountRef.current = 0;
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
    setAwaitingAgentReply(false);
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
    const targetMessageId = messageLocationRequest?.topicId === topic
      ? Number(messageLocationRequest.messageId) || 0
      : 0;
    setHighlightedMessageId(targetMessageId);
    loadHistory(topic, targetMessageId);
    if (isGroup && groupId) {
      loadGroupMembers();
    } else {
      loadPeerProfile();
    }
    return () => {
      historyAbortControllerRef.current?.abort();
      olderHistoryAbortControllerRef.current?.abort();
      questionIndexAbortControllerRef.current?.abort();
      questionJumpAbortControllerRef.current?.abort();
    };
  }, [groupId, invalidateArtifactSnapshot, isGroup, topic, user.uid, messageLocationRequest?.requestId]);

  useEffect(() => {
    const agentUID = Number(cloudArtifactsRequest?.agentUid || 0);
    if (!cloudArtifactsRequest?.requestId) return;
    if (cloudArtifactsRequest.topicId && cloudArtifactsRequest.topicId !== topic) return;
    clearActiveArtifactFocus();
    setPreviewFile(null);
    setCloudArtifactsAgentUID(agentUID);
    setCloudArtifactsTab(cloudArtifactsRequest.initialTab || 'files');
    setCloudArtifactsListOpen(true);
    setCloudArtifactsReturnOpen(false);
    onCloudArtifactsRequestConsumed?.(cloudArtifactsRequest.requestId);
  }, [
    clearActiveArtifactFocus,
    cloudArtifactsRequest,
    onCloudArtifactsRequestConsumed,
    topic,
  ]);

  useEffect(() => {
    const preventBrowserFileOpen = (event) => {
      if (hasFileDrag(event.dataTransfer)) {
        event.preventDefault();
      }
      if (event.type === 'drop') clearChatAttachmentDrag();
    };
    const resetDragState = () => {
      clearChatAttachmentDrag();
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
    const requestID = ++groupMembersRequestRef.current;
    const requestTopic = topic;
    const requestGroupID = groupId;
    try {
      const res = await api.getGroupInfo(requestGroupID);
      if (requestID !== groupMembersRequestRef.current || activeTopicRef.current !== requestTopic) return;
      const cachedProfile = groupProfileCacheRef.current.get(String(requestGroupID));
      const nextMembers = Array.isArray(res.members)
        ? res.members
        : (cachedProfile?.members || []);
      const nextGroup = res.group || cachedProfile?.group || null;
      groupProfileCacheRef.current.set(String(requestGroupID), {
        members: nextMembers,
        group: nextGroup,
      });
      setMembers(nextMembers);
      setGroupInfo(nextGroup);
    } catch (e) {
      // Cached members, the Agent roster, and message metadata keep sender identity
      // stable while group details are temporarily unavailable.
    }
  };

  const loadPeerProfile = async () => {
    const requestID = ++peerProfileRequestRef.current;
    const requestTopic = topic;
    try {
      const [left, right] = requestTopic.replace('p2p_', '').split('_').map((n) => parseInt(n, 10));
      const peerId = left === parseUid(user.uid) ? right : left;
      const [friendsRes, agentsRes] = await Promise.all([
        api.getFriends().catch(() => ({})),
        api.getAgents ? api.getAgents().catch(() => ({})) : Promise.resolve({}),
      ]);
      const friends = friendsRes.friends || [];
      const agents = agentsRes.agents || [];
      const friendPeer = friends.find((friend) => sameUID(friend.id, peerId));
      const agentPeer = agents.find((agent) => sameUID(agent.uid || agent.id, peerId));
      const peer = agentPeer ? mergeIdentityRecord(friendPeer, agentPeer) : friendPeer;
      if (requestID !== peerProfileRequestRef.current || activeTopicRef.current !== requestTopic) return;
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
          // A cancel packet identifies the control request, not the Agent's
          // transient response stream, so it cannot safely reconcile a row.
          clearRuntimePlan();
          clearLiveWorking();
          clearTimeout(peerTypingTimer.current);
          setPeerTyping(false);
          setAwaitingAgentReply(false);
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
              from: msg.data.from,
              content: delta,
              metadata: msg.data.metadata || null,
              role: msg.data.role || 'assistant',
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
          client_msg_id: msg.data.client_msg_id || '',
          msg_type: msg.data.msg_type || msg.data.type || 'text',
          reply_to: msg.data.reply_to || 0,
          created_at: new Date().toISOString(),
        });
        if (isWorkingMessage(serverMsg)) markLiveWorking(serverMsg);

        setMessages((prev) => {
          const streamIdx = findStreamingMessageForFinal(prev, serverMsg);
          if (streamIdx !== -1) {
            const next = [...prev];
            next[streamIdx] = serverMsg;
            return mergeMessages([], next);
          }
          const current = removeStaleStreamingMessagesForFinal(prev, serverMsg);
          // An HTTP ACK may assign the durable sequence before its matching
          // WebSocket echo arrives. Merge that echo so its server-canonical
          // identity and normalized payload are not lost.
          const existingIdx = current.findIndex((message) => message.id === serverMsg.id);
          if (existingIdx !== -1) {
            const existing = current[existingIdx];
            const metadata = existing.metadata || serverMsg.metadata
              ? { ...(existing.metadata || {}), ...(serverMsg.metadata || {}) }
              : null;
            const next = [...current];
            next[existingIdx] = {
              ...existing,
              ...serverMsg,
              client_msg_id: messageClientMsgID(serverMsg) || messageClientMsgID(existing),
              metadata,
            };
            return mergeMessages([], next);
          }
          // If this is our own message echoed back, replace the optimistic entry
          if (sameUID(fromUid, user.uid)) {
            const pendingIdx = current.findIndex((m) => (
              m._pending && pendingMatchesHistoryMessage(m, serverMsg, new Set())
            ));
            if (pendingIdx !== -1) {
              const next = [...current];
              next[pendingIdx] = serverMsg;
              // An Agent reply can arrive before our own server echo. Re-sort
              // after replacing the provisional message so the user message
              // remains between the previous and new Agent turns.
              return mergeMessages([], next);
            }
          }
          // Keep the display/canonical-content fallback for legacy echoes that
          // predate client_msg_id propagation. The ID/anchor match above stays
          // authoritative when the server provides enough correlation data.
          if (sameUID(fromUid, user.uid)) {
            const mergedEcho = mergeOwnServerEcho(current, serverMsg, user.uid);
            if (mergedEcho) return mergeMessages([], mergedEcho);
          }
          return mergeMessages(current, [serverMsg]);
        });
        if (sameUID(fromUid, user.uid) && isFinalTextMessage(serverMsg)) {
          clearRuntimePlan();
        } else if (!sameUID(fromUid, user.uid) && isFinalTextMessage(serverMsg)) {
          clearRuntimePlan();
          clearLiveWorking();
          clearTimeout(peerTypingTimer.current);
          setPeerTyping(false);
          setAwaitingAgentReply(false);
        }
        updateTopicSeq(topic, serverMsg.id);

        // Send read receipt if message is from peer
        if (!sameUID(fromUid, user.uid)) {
          wsSendRead(topic, serverMsg.id);
        }
      }

      // Typing indicator from peer
      if (msg.info && msg.info.topic === topic && msg.info.what === 'kp') {
        const fromUid = parseUid(msg.info.from);
        if (!sameUID(fromUid, user.uid)) {
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
    questionIndexAbortControllerRef.current?.abort();
    const controller = new AbortController();
    questionIndexAbortControllerRef.current = controller;
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
          { signal: controller.signal, timeoutMs: HISTORY_REQUEST_TIMEOUT_MS },
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
      if (questionIndexAbortControllerRef.current === controller) {
        questionIndexAbortControllerRef.current = null;
      }
      if (
        activeTopicRef.current === targetTopic
        && questionIndexRequestRef.current === requestId
      ) {
        questionIndexLoadingRef.current = false;
        setQuestionIndexLoading(false);
      }
    }
  }, [topic, user.uid]);

  const loadHistory = async (targetTopic = topic, aroundId = 0) => {
    const requestID = ++historyRequestRef.current;
    historyAbortControllerRef.current?.abort();
    olderHistoryAbortControllerRef.current?.abort();
    const controller = new AbortController();
    historyAbortControllerRef.current = controller;
    olderHistoryAbortControllerRef.current = null;
    const cacheKey = historyCacheKey(user.uid, targetTopic);
    const hasCachedHistory = !aroundId && historyCacheRef.current.has(cacheKey);
    historyLoadingRef.current = true;
    previousScrollRef.current = null;
    setRefreshingHistory(true);
    setHistoryError('');
    setOlderHistoryError('');
    loadingOlderRef.current = false;
    setLoadingOlder(false);
    autoHistoryPageCountRef.current = 0;
    setAutoHistoryLimitReached(false);
    if (!hasCachedHistory) {
      setHistoryLoaded(false);
    }
    try {
      const res = await api.getMessages(
        targetTopic,
        PAGE_SIZE,
        0,
        !aroundId,
        0,
        { signal: controller.signal, timeoutMs: HISTORY_REQUEST_TIMEOUT_MS, aroundId },
      );
      if (activeTopicRef.current !== targetTopic || historyRequestRef.current !== requestID) return;
      const rawMessages = res.messages || [];
      const { visibleMessages } = normalizeHistoryMessages(rawMessages);
      const hasMore = typeof res.has_more === 'boolean'
        ? res.has_more
        : rawMessages.length === PAGE_SIZE;
      const nextBeforeID = Number(res.next_before_id) || oldestHistoryMessageID(rawMessages);
      setMessages((current) => {
        return mergeHistoryWithCurrentMessages(visibleMessages, current);
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
        if (e?.code !== 'REQUEST_ABORTED') {
          setHistoryError(e?.code === 'REQUEST_TIMEOUT'
            ? '聊天记录加载超时，请重试。'
            : '聊天记录加载失败，请检查网络后重试。');
        }
      }
    } finally {
      if (historyAbortControllerRef.current === controller) {
        historyAbortControllerRef.current = null;
      }
      if (activeTopicRef.current === targetTopic && historyRequestRef.current === requestID) {
        historyLoadingRef.current = false;
        setRefreshingHistory(false);
        setHistoryLoaded(true);
      }
    }
  };

  const loadOlderHistory = useCallback(async ({ automatic = false } = {}) => {
    if (historyLoadingRef.current || loadingOlderRef.current || !hasMoreHistoryRef.current) return;
    if (automatic && autoHistoryPageCountRef.current >= HISTORY_AUTO_FILL_MAX_PAGES) {
      setAutoHistoryLimitReached(true);
      return;
    }
    if (!automatic) {
      autoHistoryPageCountRef.current = 0;
      setAutoHistoryLimitReached(false);
    }
    const targetTopic = topic;
    const requestID = historyRequestRef.current;
    const controller = new AbortController();
    olderHistoryAbortControllerRef.current = controller;
    
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
        { signal: controller.signal, timeoutMs: HISTORY_REQUEST_TIMEOUT_MS },
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
      if (automatic) {
        autoHistoryPageCountRef.current += 1;
        setAutoHistoryLimitReached(
          hasMore && autoHistoryPageCountRef.current >= HISTORY_AUTO_FILL_MAX_PAGES,
        );
      }
    } catch (e) {
      if (activeTopicRef.current === targetTopic && historyRequestRef.current === requestID) {
        previousScrollRef.current = null;
        if (e?.code !== 'REQUEST_ABORTED') {
          setOlderHistoryError(e?.code === 'REQUEST_TIMEOUT'
            ? '更早的聊天记录加载超时，请重试。'
            : '更早的聊天记录加载失败。');
        }
      }
    } finally {
      if (olderHistoryAbortControllerRef.current === controller) {
        olderHistoryAbortControllerRef.current = null;
      }
      if (activeTopicRef.current === targetTopic && historyRequestRef.current === requestID) {
        loadingOlderRef.current = false;
        setLoadingOlder(false);
      }
    }
  }, [topic, user.uid]);

  const loadAllHistoryForImageGallery = useCallback(async () => {
    if (galleryHistoryLoadingRef.current || !hasMoreHistoryRef.current) return;
    galleryHistoryLoadingRef.current = true;
    try {
      let pageCount = 0;
      while (hasMoreHistoryRef.current && pageCount < 100) {
        const beforeID = historyBeforeIDRef.current;
        await loadOlderHistory({ automatic: false });
        pageCount += 1;
        if (historyBeforeIDRef.current === beforeID) break;
      }
    } finally {
      galleryHistoryLoadingRef.current = false;
    }
  }, [loadOlderHistory]);

  useEffect(() => {
    const el = timelineRef.current;
    if (!el || refreshingHistory || !hasMoreHistory || loadingOlder || historyError
      || olderHistoryError || autoHistoryLimitReached) return;
    const needsConversationContent = !hasOrdinaryChatMessage(messages);
    const needsViewportFill = el.scrollTop <= HISTORY_AUTO_LOAD_THRESHOLD
      || el.scrollHeight <= el.clientHeight + HISTORY_AUTO_LOAD_THRESHOLD;
    if (needsConversationContent || needsViewportFill) {
      loadOlderHistory({ automatic: true });
    }
  }, [
    messages,
    refreshingHistory,
    hasMoreHistory,
    loadingOlder,
    historyError,
    olderHistoryError,
    autoHistoryLimitReached,
    loadOlderHistory,
  ]);

  const workingState = useMemo(() => {
    let lastWorkingIndex = -1;
    let lastBotTextIndex = -1;
    const groupBotUIDs = new Set([
      ...members
        .filter((member) => member?.is_bot || member?.account_type === 'bot')
        .map((member) => parseUid(member.user_id)),
      ...availableAgents
        .map((agent) => parseUid(agent.uid || agent.id)),
    ].filter((uid) => uid > 0));
    const currentUserUID = parseUid(user.uid);
    const groupMemberUIDs = new Set(
      members
        .map((member) => parseUid(member?.user_id))
        .filter((uid) => uid > 0),
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
      if (sameUID(message.from_uid, user.uid)) return;
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
        : parseUid(user.uid),
      responderUid: active ? parseUid(messages[lastWorkingIndex]?.from_uid) : 0,
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
      || workingState.initiatorUid === parseUid(user.uid)
    );

  useEffect(() => {
    if (!activeBotWorking) {
      setIsStopRequested(false);
    }
  }, [activeBotWorking]);

  const topicAgent = availableAgents.find((agent) => agent.topic_id === topic) || null;
  const groupAgent = isGroup
    ? availableAgents.find((agent) => members.some((member) => sameUID(member.user_id, agent.uid || agent.id))) || null
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
        client_msg_id: result.client_msg_id || next[idx].client_msg_id || '',
        _pending: false,
      };
      return mergeMessages([], next);
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
    setAwaitingAgentReply(Boolean(selectedAgent));
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
    const protocolText = isGroup
      ? canonicalizeStructuredMentionText(originalInput, originalStructuredMentions).trim()
      : text;
    const mentions = isGroup
      ? collectStructuredMentionTargets(input, originalStructuredMentions)
      : [];
    const tempId = Date.now();
    const clientMsgID = createClientMessageID();

    try {
      if (!isGroup && selectedAgent && selectedAgent.topic_id !== topic && onResolveAgentTopic) {
        topicToActivate = await onResolveAgentTopic(selectedAgent);
        sendTopic = topicToActivate?.topicId || topicToActivate?.topic_id || sendTopic;
      }
      switchesTopic = sendTopic !== topic;

      await syncPhoneUploads({ final: true });
      attachmentsToSend = [...(attachmentDraftsRef.current.get(topic) || [])];
      if (!text && attachmentsToSend.length === 0) {
        setAwaitingAgentReply(false);
        return;
      }

      const currentReplyTo = switchesTopic ? null : originalReplyTo;
      const contentBlocks = buildAtomicContentBlocks(protocolText, attachmentsToSend);
      const displayContent = text || summarizeAttachments(attachmentsToSend);
      const payload = attachmentsToSend.length > 0
        ? {
            type: 'text',
            content: protocolText || summarizeAttachments(attachmentsToSend),
            content_blocks: contentBlocks,
          }
        : protocolText;
      const artifactContext = switchesTopic
        ? { contextRef: '' }
        : await captureArtifactMessageContext();
      const sendPayload = withArtifactContextRef(payload, artifactContext.contextRef);

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
          ...createOptimisticUserMessage({
            id: tempId,
            topicId: sendTopic,
            userUID: user.uid,
            content: displayContent,
            contentBlocks: attachmentsToSend.length > 0 ? contentBlocks : undefined,
            replyToID: currentReplyTo ? currentReplyTo.id : 0,
            pendingAfterSeq: latestPersistedMessageSequence(prev),
            clientMsgID,
          }),
          _canonical_content: protocolText,
        }]));
      }

      const result = mentions.length > 0
        ? await api.sendMessage(sendTopic, sendPayload, currentReplyTo ? currentReplyTo.id : undefined, mentions, clientMsgID)
        : await api.sendMessage(sendTopic, sendPayload, currentReplyTo ? currentReplyTo.id : undefined, undefined, clientMsgID);
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

      setAwaitingAgentReply(false);

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
  }, [captureArtifactMessageContext, clearRuntimePlan, finalizeOptimisticMessage, input, isGroup, isUploadingAttachment, onActivateTopic, onResolveAgentTopic, removeOptimisticMessage, replyTo, selectedAgent, syncPhoneUploads, topic, updateAttachmentDraft, updateComposerDraft, updateStructuredMentionDraft, user.uid]);

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
      setAwaitingAgentReply(false);
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
      .find((item) => sameUID(item.from_uid, user.uid) && isFinalTextMessage(item));
    const taskText = typeof previousTask?.content === 'string' ? previousTask.content.trim() : '';
    if (!taskText) {
      throw new Error('没有找到可以重新发送的上一条任务');
    }

    sendInFlightRef.current = true;
    setIsSendingMessage(true);
    setAwaitingAgentReply(true);
    clearRuntimePlan();
    const tempId = Date.now();
    const clientMsgID = createClientMessageID();
    stickToBottomRef.current = true;
    setMessages((current) => mergeMessages(current, [createOptimisticUserMessage({
      id: tempId,
      topicId: topic,
      userUID: user.uid,
      content: taskText,
      pendingAfterSeq: latestPersistedMessageSequence(current),
      clientMsgID,
    })]));

    try {
      const artifactContext = await captureArtifactMessageContext();
      const sendPayload = withArtifactContextRef(taskText, artifactContext.contextRef);
      const result = await api.sendMessage(topic, sendPayload, undefined, undefined, clientMsgID);
      finalizeOptimisticMessage(tempId, result);
    } catch (error) {
      removeOptimisticMessage(tempId);
      setAwaitingAgentReply(false);
      setAttachmentStatus({
        tone: 'error',
        message: error?.message ? `重新生成失败：${error.message}` : '重新生成失败，请稍后重试。',
      });
      throw error;
    } finally {
      sendInFlightRef.current = false;
      setIsSendingMessage(false);
    }
  }, [captureArtifactMessageContext, clearRuntimePlan, finalizeOptimisticMessage, messages, removeOptimisticMessage, topic, user.uid]);

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
    if (!val.trim()) {
      setAttachmentStatus((current) => (
        current?.source === 'edit-resend' ? null : current
      ));
    }

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

  const handleVoiceFinal = (transcript, insertion) => {
    const textarea = textareaRef.current;
    const result = insertTranscriptAtSelection(transcript, insertion, textarea, input);
    if (!result) return;
    const nextStructuredMentions = reconcileStructuredMentionSelections(
      result.baseValue,
      result.value,
      structuredMentionDraftsRef.current.get(topic) || [],
    );
    setInput(result.value);
    updateComposerDraft(topic, result.value);
    updateStructuredMentionDraft(topic, nextStructuredMentions);
    setTimeout(() => {
      textareaRef.current?.focus();
      textareaRef.current?.setSelectionRange(result.caret, result.caret);
    }, 0);
  };

  const insertMention = (member) => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    const range = mentionRangeRef.current;
    if (!range || range.start < 0 || range.end < range.start || range.end > input.length) return;
    const target = member.mention_target || `usr${member.user_id}`;
    const label = target === STRUCTURED_MENTION_ALL
      ? '所有人'
      : (member.display_name || member.username || target);
    const mention = `@${label} `;
    const newText = input.slice(0, range.start) + mention + input.slice(range.end);
    const reconciledSelections = reconcileStructuredMentionSelections(
      input,
      newText,
      structuredMentionDraftsRef.current.get(topic) || [],
    );
    updateStructuredMentionDraft(topic, [
      ...reconciledSelections,
      { target, label, start: range.start, end: range.start + mention.length - 1 },
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

  const hasSupportedAttachmentDrag = (dataTransfer) => (
    hasFileDrag(dataTransfer) || hasChatAttachmentDrag(dataTransfer)
  );

  const handleDragEnter = (e) => {
    if (!hasSupportedAttachmentDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    dragDepthRef.current += 1;
    setIsDragActive(true);
  };

  const handleDragOver = (e) => {
    if (!hasSupportedAttachmentDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    e.dataTransfer.dropEffect = 'copy';
    setIsDragActive(true);
  };

  const handleDragLeave = (e) => {
    if (!hasSupportedAttachmentDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) {
      setIsDragActive(false);
    }
  };

  const handleDrop = async (e) => {
    if (!hasSupportedAttachmentDrag(e.dataTransfer)) return;
    e.preventDefault();
    e.stopPropagation();
    dragDepthRef.current = 0;
    setIsDragActive(false);

    if (isUploadingAttachment) {
      setAttachmentStatus({ tone: 'info', message: '附件仍在上传中，请稍后再拖入新的文件。' });
      return;
    }

    const chatAttachment = readChatAttachmentDrag(e.dataTransfer);
    if (chatAttachment) {
      const droppedIdentity = attachmentIdentity(chatAttachment);
      let added = false;
      updateAttachmentDraft(topic, (current) => {
        const alreadyAdded = current.some((item) => attachmentIdentity(item) === droppedIdentity);
        added = !alreadyAdded;
        return alreadyAdded ? current : [...current, chatAttachment];
      });
      setAttachmentStatus(added
        ? { tone: 'success', message: `已添加${chatAttachment.type === 'image' ? '图片' : '文件'}：${chatAttachment.name}` }
        : { tone: 'info', message: `${chatAttachment.name} 已在待发送附件中。` });
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
    if (files.length > 0) {
      e.preventDefault();
      e.stopPropagation();

      if (isUploadingAttachment) {
        setAttachmentStatus({ tone: 'info', message: '附件仍在上传中，请稍后再粘贴新的文件。' });
        return;
      }
      await uploadAttachmentFiles(files);
      return;
    }

    const pastedText = e.clipboardData?.getData?.('text/plain') || '';
    if (!shouldConvertPastedTextToDocument(pastedText)) return;
    if (isUploadingAttachment || sendInFlightRef.current) {
      setAttachmentStatus({ tone: 'info', message: '附件仍在处理中，长文本已保留在输入框中。' });
      return;
    }

    const pasteTopic = activeTopicRef.current;
    const textarea = e.currentTarget;
    const selectionStart = Number.isInteger(textarea?.selectionStart) ? textarea.selectionStart : input.length;
    const selectionEnd = Number.isInteger(textarea?.selectionEnd) ? textarea.selectionEnd : selectionStart;
    const documentFile = createPastedTextDocument(pastedText);

    e.preventDefault();
    e.stopPropagation();
    setIsUploadingAttachment(true);
    let uploaded = null;
    try {
      uploaded = await uploadAttachmentFile(documentFile, 'file', pasteTopic);
    } finally {
      setIsUploadingAttachment(false);
    }

    if (uploaded) {
      if (activeTopicRef.current === pasteTopic) {
        setAttachmentStatus({
          tone: 'success',
          message: `长文本已整理为文档：${uploaded.name}。发送前可以移除。`,
        });
      }
      return;
    }

    const currentText = pasteTopic === activeTopicRef.current
      ? (textareaRef.current?.value ?? input)
      : (composerDraftsRef.current.get(pasteTopic) || '');
    const start = Math.min(Math.max(selectionStart, 0), currentText.length);
    const end = Math.min(Math.max(selectionEnd, start), currentText.length);
    const restoredText = `${currentText.slice(0, start)}${pastedText}${currentText.slice(end)}`;
    const restoredMentions = reconcileStructuredMentionSelections(
      currentText,
      restoredText,
      structuredMentionDraftsRef.current.get(pasteTopic) || [],
    );
    updateComposerDraft(pasteTopic, restoredText);
    updateStructuredMentionDraft(pasteTopic, restoredMentions);
    if (activeTopicRef.current === pasteTopic) {
      setInput(restoredText);
      setAttachmentStatus((current) => ({
        tone: 'error',
        message: `${current?.message || '长文本文档上传失败。'} 原文已恢复到输入框，可直接发送或稍后重试。`,
      }));
      setTimeout(() => {
        const activeTextarea = textareaRef.current;
        if (!activeTextarea) return;
        const nextCursor = start + pastedText.length;
        activeTextarea.focus();
        activeTextarea.setSelectionRange(nextCursor, nextCursor);
      }, 0);
    }
  };

  // Find the display name for a uid in group context
  const getMemberName = (fromUid) => {
    if (!isGroup || !members.length) return null;
    const normalizedUID = parseUid(fromUid);
    const m = members.find((mem) => sameUID(mem.user_id, normalizedUID));
    return m ? (m.display_name || m.username) : `usr${normalizedUID || fromUid}`;
  };


  const groupBots = members.filter((m) => {
    if (sameUID(m.user_id, user.uid)) return false;
    return m.is_bot === true || m.account_type === 'bot';
  });
  const mentionDisplayNames = useMemo(() => {
    if (!isGroup) return {};
    return members.reduce((names, member) => {
      const uid = parseUid(member?.user_id);
      if (!uid) return names;
      const agent = availableAgents.find((candidate) => sameUID(candidate?.uid || candidate?.id, uid));
      const displayName = member?.display_name
        || agent?.display_name
        || agent?.name
        || member?.username;
      if (displayName && displayName !== `usr${uid}`) {
        names[String(uid)] = displayName;
      }
      return names;
    }, {});
  }, [availableAgents, isGroup, members]);
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
    return left === parseUid(user.uid) ? right : left;
  }, [isGroup, topic, user.uid]);
  const rosterPeer = availableAgents.find((agent) => sameUID(agent.uid || agent.id, peerUID));
  const resolvedPeerProfile = mergeIdentityRecord(peerProfile, rosterPeer);
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
  const availableAgentByUID = useMemo(() => new Map(
    availableAgents
      .map((agent) => [parseUid(agent.uid || agent.id), agent])
      .filter(([uid]) => uid > 0),
  ), [availableAgents]);
  const availableAgentUIDs = useMemo(
    () => new Set(availableAgentByUID.keys()),
    [availableAgentByUID],
  );
  const taskBotUIDs = useMemo(() => {
    if (!isAgentTask) return [];
    return members
      .filter((member) => member?.is_bot || availableAgentUIDs.has(parseUid(member?.user_id)))
      .map((member) => parseUid(member.user_id))
      .filter((uid) => uid > 0);
  }, [availableAgentUIDs, isAgentTask, members]);
  const taskBotUID = taskBotUIDs.length === 1 ? taskBotUIDs[0] : 0;
  const isTwoPersonGroupWithCurrentUser = useMemo(() => {
    if (!isGroup) return false;
    const memberUIDs = new Set(
      members
        .map((member) => parseUid(member?.user_id))
        .filter((uid) => uid > 0),
    );
    return memberUIDs.size === 2 && memberUIDs.has(parseUid(user.uid));
  }, [isGroup, members, user.uid]);
  const isOneUserOneAgentGroup = useMemo(() => {
    if (!isTwoPersonGroupWithCurrentUser) return false;
    const peerMember = members.find((member) => !sameUID(member?.user_id, user.uid));
    if (!peerMember) return false;
    return Boolean(
      peerMember.is_bot
      || peerMember.account_type === 'bot'
      || availableAgentUIDs.has(parseUid(peerMember.user_id)),
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
  const displayName = isGroup
    ? (groupInfo?.name || topicName || topic)
    : (resolvedPeerProfile?.display_name || resolvedPeerProfile?.username || topicName || topic);
  const canRegenerateAssistantMessages = !isGroup || isAgentTask;
  const groupAgentUID = parseUid(groupAgent?.uid || groupAgent?.id);
  const groupSupportsArtifacts = groupAgent?.cloud_artifacts_enabled === true
    && ((isAgentTask && taskBotUID > 0 && groupAgentUID === taskBotUID)
      || (isTwoPersonGroupWithCurrentUser && groupAgentUID > 0));
  const activeArtifactAgentUID = isGroup
    ? (groupSupportsArtifacts ? groupAgentUID : 0)
    : (peerIsBot && peerUID > 0 && resolvedPeerProfile?.cloud_artifacts_enabled === true ? peerUID : 0);
  activeArtifactAgentUIDRef.current = activeArtifactAgentUID;
  const knownArtifacts = artifactRegistryState.agentUID === activeArtifactAgentUID
    ? artifactRegistryState.artifacts
    : [];

  useEffect(() => {
    if (!historyLoaded || activeArtifactAgentUID <= 0) return;

    let state = artifactShareNotificationRef.current;
    if (state.topic !== topic) {
      state = {
        topic,
        initialized: false,
        observed: new Set(),
        pending: new Map(),
      };
      artifactShareNotificationRef.current = state;
    }

    const candidates = artifactPublishCandidates(messages);
    if (!state.initialized) {
      state.observed = new Set(candidates.map((candidate) => candidate.key));
      state.initialized = true;
      return;
    }

    candidates.forEach((candidate) => {
      if (state.observed.has(candidate.key)) return;
      state.observed.add(candidate.key);
      state.pending.set(candidate.key, {
        url: candidate.url,
        registryRevision: artifactRegistryRevision,
      });
    });
    while (state.pending.size > 32) {
      state.pending.delete(state.pending.keys().next().value);
    }

    const confirmedURLs = new Set(
      knownArtifacts.map((artifact) => artifactNotificationURL(artifact?.url)).filter(Boolean),
    );
    let shared = false;
    state.pending.forEach((pending, key) => {
      if (artifactRegistryRevision <= pending.registryRevision || !confirmedURLs.has(pending.url)) return;
      state.pending.delete(key);
      shared = true;
    });
    if (shared) feedback.notify({ tone: 'success', message: '已共享内容到云端' });
  }, [activeArtifactAgentUID, artifactRegistryRevision, feedback, historyLoaded, knownArtifacts, messages, topic]);

  const activePreviewArtifactRef = artifactRefFromPreviewFile(previewFile, activeArtifactAgentUID);
  const activePreviewArtifactId = activePreviewArtifactRef?.id || '';
  const activePreviewArtifactVersion = Number(activePreviewArtifactRef?.displayed_version || 0);
  const latestActivePreviewArtifact = activePreviewArtifactId
    ? knownArtifacts.find((artifact) => String(artifact?.id || artifact?.artifact_id || '') === activePreviewArtifactId)
    : null;
  const latestActivePreviewVersion = Number(latestActivePreviewArtifact?.publish_version || 0);
  const latestActivePreviewURL = String(latestActivePreviewArtifact?.url || '');
  const latestActivePreviewTitle = String(latestActivePreviewArtifact?.title || '');
  const latestActivePreviewKind = String(latestActivePreviewArtifact?.kind || 'html');
  const artifactRegistryRefreshKey = useMemo(() => {
    for (let index = messages.length - 1; index >= 0; index -= 1) {
      const message = messages[index];
      const urls = artifactURLsInMessage(message);
      if (urls.length > 0) {
        return `${String(message.id || message.seq_id || message.created_at || index)}|${urls.join('|')}`;
      }
    }
    return '';
  }, [messages]);

  useEffect(() => {
    const handleArtifactsChanged = (event) => {
      const changedAgentUID = Number(event?.detail?.agentUid || 0);
      if (changedAgentUID > 0 && changedAgentUID !== activeArtifactAgentUID) return;
      setArtifactRegistryRefreshEpoch((current) => current + 1);
    };
    window.addEventListener(CLOUD_ARTIFACTS_CHANGED_EVENT, handleArtifactsChanged);
    return () => window.removeEventListener(CLOUD_ARTIFACTS_CHANGED_EVENT, handleArtifactsChanged);
  }, [activeArtifactAgentUID]);

  useEffect(() => {
    let cancelled = false;
    let requestTimer = null;
    let hadSuccessfulResponse = false;
    const requestID = ++artifactRegistryRequestRef.current;
    if (activeArtifactAgentUID <= 0) return () => {
      cancelled = true;
    };

    const requestAgentUID = activeArtifactAgentUID;
    const controller = new AbortController();
    const retryDelays = artifactRegistryRefreshKey ? [750, 1750] : [];
    const shouldPollActivePreview = Boolean(activePreviewArtifactId);
    const isCurrentRequest = () => (
      !cancelled && requestID === artifactRegistryRequestRef.current
    );
    const loadArtifacts = async (attempt = 0, polling = false) => {
      try {
        const result = await api.getCloudArtifacts(requestAgentUID, 'active', {
          signal: controller.signal,
        });
        if (!isCurrentRequest()) return;
        hadSuccessfulResponse = true;
        setArtifactRegistryState({
          agentUID: requestAgentUID,
          artifacts: Array.isArray(result?.artifacts) ? result.artifacts : [],
        });
        setArtifactRegistryRevision((current) => current + 1);
      } catch {
        if (!isCurrentRequest()) return;
        if ((polling || attempt >= retryDelays.length) && !hadSuccessfulResponse) {
          setArtifactRegistryState({ agentUID: requestAgentUID, artifacts: [] });
        }
      }

      if (!isCurrentRequest()) return;
      if (!polling && attempt < retryDelays.length) {
        requestTimer = window.setTimeout(() => {
          requestTimer = null;
          loadArtifacts(attempt + 1, false);
        }, retryDelays[attempt]);
        return;
      }
      if (shouldPollActivePreview) {
        requestTimer = window.setTimeout(() => {
          requestTimer = null;
          loadArtifacts(0, true);
        }, ARTIFACT_REGISTRY_POLL_MS);
      }
    };

    loadArtifacts();
    return () => {
      cancelled = true;
      controller.abort();
      if (requestTimer) window.clearTimeout(requestTimer);
    };
  }, [
    activeArtifactAgentUID,
    activePreviewArtifactId,
    artifactRegistryRefreshEpoch,
    artifactRegistryRefreshKey,
  ]);

  useEffect(() => {
    if (!activePreviewArtifactId
      || latestActivePreviewVersion <= activePreviewArtifactVersion
      || !latestActivePreviewURL) {
      setPendingArtifactRefresh(null);
      return;
    }
    const refreshURL = artifactURLForVersion(latestActivePreviewURL, latestActivePreviewVersion);
    if (!refreshURL) {
      setPendingArtifactRefresh(null);
      return;
    }

    const candidate = createCloudArtifactPreviewFile({
      id: activePreviewArtifactId,
      title: latestActivePreviewTitle || previewFile?.name || activePreviewArtifactId,
      kind: latestActivePreviewKind,
      url: refreshURL,
      publish_version: latestActivePreviewVersion,
      agent_uid: activeArtifactAgentUID,
    });
    const candidateKey = artifactRefreshFileKey(candidate);
    setPendingArtifactRefresh((current) => (
      artifactRefreshFileKey(current) === candidateKey ? current : candidate
    ));
  }, [
    activeArtifactAgentUID,
    activePreviewArtifactId,
    activePreviewArtifactVersion,
    artifactRegistryRevision,
    latestActivePreviewKind,
    latestActivePreviewTitle,
    latestActivePreviewURL,
    latestActivePreviewVersion,
    previewFile?.name,
  ]);

  const handleArtifactRefreshReady = useCallback((candidate) => {
    const candidateKey = artifactRefreshFileKey(candidate);
    const candidateAgentUID = Number(candidate?.artifact_agent_uid || 0);
    const candidateArtifactID = String(candidate?.artifact_id || '');
    const candidateVersion = Number(candidate?.publish_version || 0);
    const currentFocus = activeArtifactFocusRef.current;
    const candidateFocus = artifactMessageFocusFromPreviewFile(
      candidate,
      topic,
      artifactTopicGenerationRef.current,
    );
    if (!candidateKey
      || !currentFocus
      || !candidateFocus
      || activeTopicRef.current !== topic
      || activeArtifactAgentUIDRef.current !== candidateAgentUID
      || currentFocus.topic !== topic
      || currentFocus.topicGeneration !== artifactTopicGenerationRef.current
      || candidateFocus.topicGeneration !== artifactTopicGenerationRef.current
      || currentFocus.agentUid !== candidateAgentUID
      || currentFocus.artifactId !== candidateArtifactID
      || candidateVersion <= currentFocus.displayedVersion) {
      setPendingArtifactRefresh((current) => (
        artifactRefreshFileKey(current) === candidateKey ? null : current
      ));
      return;
    }
    setPreviewFileWithFocus(candidate);
    setPendingArtifactRefresh((current) => (
      artifactRefreshFileKey(current) === candidateKey ? null : current
    ));
  }, [setPreviewFileWithFocus, topic]);

  const handleArtifactRefreshFailed = useCallback((candidate) => {
    const candidateKey = artifactRefreshFileKey(candidate);
    setPendingArtifactRefresh((current) => (
      artifactRefreshFileKey(current) === candidateKey ? null : current
    ));
  }, []);

  useEffect(() => {
    if (isGroup) {
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
      const uid = parseUid(member?.user_id);
      if (uid > 0) map.set(uid, member);
    });
    return map;
  }, [members]);
  const inferredAgentUIDs = useMemo(() => {
    const uids = new Set(availableAgentUIDs);
    members.forEach((member) => {
      if (member?.is_bot || member?.account_type === 'bot') {
        const uid = parseUid(member.user_id);
        if (uid > 0) uids.add(uid);
      }
    });
    messages.forEach((message) => {
      if (isWorkingMessage(message) || isAssistantAuthoredMessage(message)) {
        const uid = parseUid(message?.from_uid);
        if (uid > 0) uids.add(uid);
      }
    });
    return uids;
  }, [availableAgentUIDs, members, messages]);

  const messageById = useMemo(() => {
    const map = new Map();
    messages.forEach((message) => {
      map.set(message.id, message);
    });
    return map;
  }, [messages]);

  // Keep the identity used by existing self-authored rows reactive without
  // recomputing their display groups for unrelated parent-prop changes.
  const currentUserIdentity = useMemo(() => ({
    account_type: user.account_type,
    avatar_url: user.avatar_url,
    bot: user.bot,
    display_name: user.display_name,
    is_bot: user.is_bot,
    name: user.name,
    username: user.username,
  }), [
    user.account_type,
    user.avatar_url,
    user.bot,
    user.display_name,
    user.is_bot,
    user.name,
    user.username,
  ]);

  const getSender = (msg) => {
    if (sameUID(msg.from_uid, user.uid)) {
      // The active account profile is authoritative for the current user. A
      // persisted message identity only fills fields that have not loaded yet.
      const senderProfile = mergeIdentityRecord(messageActorIdentity(msg), currentUserIdentity);
      return {
        name: senderProfile?.display_name || senderProfile?.username,
        avatarUrl: senderProfile?.avatar_url,
        isBot: senderProfile?.account_type === 'bot'
          || senderProfile?.bot === true
          || senderProfile?.is_bot === true,
      };
    }
    if (isGroup) {
      const senderUID = parseUid(msg.from_uid);
      const member = memberMap.get(senderUID);
      const rosterAgent = availableAgentByUID.get(senderUID);
      const senderProfile = mergeIdentityRecord(
        mergeIdentityRecord(rosterAgent, member),
        messageActorIdentity(msg),
      );
      return {
        name: senderProfile?.display_name
          || senderProfile?.username
          || msg.from_name
          || `usr${senderUID || msg.from_uid}`,
        avatarUrl: senderProfile?.avatar_url,
        isBot: Boolean(
          member?.is_bot
          || member?.account_type === 'bot'
          || rosterAgent
          || senderProfile?.bot === true
          || senderProfile?.is_bot === true
          || senderProfile?.account_type === 'bot'
          || inferredAgentUIDs.has(senderUID)
          || isAssistantAuthoredMessage(msg),
        ),
      };
    }
    const senderProfile = mergeIdentityRecord(resolvedPeerProfile, messageActorIdentity(msg));
    return {
      name: senderProfile?.display_name || senderProfile?.username || topicName || topic,
      avatarUrl: senderProfile?.avatar_url || topicAvatarUrl,
      isBot: Boolean(
        peerIsBot
        || senderProfile?.bot === true
        || senderProfile?.is_bot === true
        || senderProfile?.account_type === 'bot',
      ),
    };
  };

  // Group messages into working areas and text messages with consecutive checking
  const groupedMessages = useMemo(() => {
    const groups = [];
    const workingByExplicitTurn = new Map();
    let currentWorking = null;
    let previousDisplayContext = null;
    let previousVisibleDisplayContext = null;

    const registerWorkingGroup = (group) => {
      if (group.explicitTurnKey) {
        workingByExplicitTurn.set(group.explicitTurnKey, group);
      }
    };

    const flushCurrentWorking = () => {
      if (!currentWorking) return;
      groups.push(currentWorking);
      registerWorkingGroup(currentWorking);
      currentWorking = null;
    };

    const findWorkingGroup = ({ explicitTurnKey }) => {
      return explicitTurnKey ? workingByExplicitTurn.get(explicitTurnKey) || null : null;
    };

    const belongsToCurrentWorking = ({ explicitTurnKey }) => {
      if (!currentWorking) return false;
      return hasSameExplicitExecutionKey(currentWorking, { explicitTurnKey });
    };

    messages.forEach((msg) => {
      const sender = getSender(msg);
      const assistantAuthored = isAssistantAuthoredMessage(msg, sender.isBot);

      const displayContext = messageDisplayContext(msg, sender.isBot);
      const { turn } = displayContext;
      const isConsecutive = areMessagesConsecutive(previousDisplayContext, displayContext);

      if (isWorkingMessage(msg)) {
        let leadingNarrativeMessages = [];
        let leadingNarrativeIsConsecutive = null;
        if (messageHasActionTool(msg)) {
          const previousGroup = groups[groups.length - 1];
          const previousMessage = previousGroup?.message;
          const sameSender = messageSenderIdentity(previousMessage) === messageSenderIdentity(msg);
          const canAdoptLeadingNarrative = hasSameExplicitExecutionKey(
            previousGroup,
            { explicitTurnKey: turn.explicitTurnKey },
          );
          if (
            previousGroup?.type === 'text'
            && previousGroup.assistantAuthored
            && sameSender
            && canAdoptLeadingNarrative
            && !displayGroupHasDeliveryArtifact(previousGroup)
          ) {
            const sourceMessages = previousGroup.sourceMessages || [previousGroup.message];
            leadingNarrativeMessages = sourceMessages.map(assistantProcessMessage);
            leadingNarrativeIsConsecutive = previousGroup.isConsecutive;
            groups.pop();
          }
        }

        if (currentWorking && !belongsToCurrentWorking(turn)) {
          flushCurrentWorking();
          // A different working key starts a new execution segment. Do not
          // resurrect an older group if this segment later returns to it.
          workingByExplicitTurn.clear();
        }

        if (
          !currentWorking
          && !hasSameExplicitExecutionKey(previousDisplayContext, turn)
        ) {
          // When the previous visible row ended one execution segment, only
          // the immediately preceding segment may be continued. Discard older
          // keyed groups before looking up this new working row.
          workingByExplicitTurn.clear();
        }

        if (currentWorking) {
          currentWorking.messages.push(...leadingNarrativeMessages, msg);
        } else {
          const existingWorking = findWorkingGroup(turn);
          if (existingWorking) {
            existingWorking.messages.push(...leadingNarrativeMessages, msg);
            registerWorkingGroup(existingWorking);
          } else {
            currentWorking = {
              type: 'working',
              messages: [...leadingNarrativeMessages, msg],
              sender,
              isConsecutive: leadingNarrativeIsConsecutive ?? isConsecutive,
              explicitTurnKey: turn.explicitTurnKey,
            };
          }
        }
        previousDisplayContext = displayContext;
      } else {
        flushCurrentWorking();
        if (!hasSameExplicitExecutionKey(previousDisplayContext, displayContext)) {
          // A visible row without the same proven execution key is a hard
          // boundary. Keeping its predecessor's group would compact
          // non-adjacent rows and hide this row's sender identity.
          workingByExplicitTurn.clear();
        }
        const displayMessage = msg;
        const textIsConsecutive = areMessagesConsecutive(previousDisplayContext, displayContext);
        const previousGroup = groups[groups.length - 1];
        const previousSourceMessages = previousGroup?.type === 'text'
          ? (previousGroup.sourceMessages || [previousGroup.message])
          : [];
        const previousMessage = previousSourceMessages[previousSourceMessages.length - 1];

        if (shouldMergeAssistantReply(previousMessage, displayMessage, previousGroup?.sender, sender, user.uid)) {
          const sourceMessages = [...previousSourceMessages, displayMessage];
          groups[groups.length - 1] = {
            ...previousGroup,
            message: mergeAssistantDisplayMessages(sourceMessages),
            sourceMessages,
            sender,
            explicitTurnKey: previousGroup.explicitTurnKey || turn.explicitTurnKey,
          };
          previousDisplayContext = displayContext;
          previousVisibleDisplayContext = displayContext;
          return;
        }

        const textIsConsecutiveWithoutWorking = areMessagesConsecutive(
          previousVisibleDisplayContext,
          displayContext,
        );

        groups.push({
          type: 'text',
          message: displayMessage,
          sourceMessages: [displayMessage],
          sender,
          replyMessage: displayMessage.reply_to ? (messageById.get(displayMessage.reply_to) || null) : null,
          isConsecutive: textIsConsecutive,
          isConsecutiveWithoutWorking: textIsConsecutiveWithoutWorking,
          assistantAuthored,
          explicitTurnKey: turn.explicitTurnKey,
        });
        previousDisplayContext = displayContext;
        previousVisibleDisplayContext = displayContext;
      }
    });

    flushCurrentWorking();

    return reconcileRenderedGroupConsecutiveness(reorderAssistantTurnGroups(groups));
  }, [
    availableAgentByUID,
    currentUserIdentity,
    inferredAgentUIDs,
    isGroup,
    memberMap,
    messageById,
    messages,
    peerIsBot,
    resolvedPeerProfile,
    topic,
    topicAvatarUrl,
    topicName,
    user.uid,
  ]);

  const conversationShareCandidates = useMemo(() => (
    groupedMessages
      .filter((group) => group.type === 'text' && Boolean(conversationShareText(group.message)))
      .map((group, index) => ({
        key: conversationShareMessageKey(group.message, index),
        message: group.message,
        senderName: group.sender?.name || group.message?.from_name || 'CatsCo',
        isSelf: sameUID(group.message?.from_uid, user.uid),
      }))
  ), [groupedMessages, user.uid]);
  const conversationShareCandidateByMessage = useMemo(() => new Map(
    conversationShareCandidates.map((candidate) => [candidate.message, candidate]),
  ), [conversationShareCandidates]);
  const conversationShareCandidateByKey = useMemo(() => new Map(
    conversationShareCandidates.map((candidate) => [candidate.key, candidate]),
  ), [conversationShareCandidates]);
  const selectedConversationShareItems = useMemo(() => (
    conversationShareCandidates.filter((candidate) => conversationShareSelectedKeys.includes(candidate.key))
  ), [conversationShareCandidates, conversationShareSelectedKeys]);
  const canOpenConversationShare = historyLoaded && (messages.length > 0 || !historyError);

  const resetConversationSharePreview = useCallback(() => {
    setConversationSharePreviewOpen(false);
    setConversationShareImages([]);
    setConversationSharePreviewPage(0);
    setConversationShareDownloading(false);
    setConversationShareManualSaveAvailable(false);
  }, []);

  const transitionConversationShare = useCallback(({ mode, selectedKeys = [] }) => {
    setConversationShareMode(mode);
    setConversationShareSelectedKeys(selectedKeys);
    resetConversationSharePreview();
    setConversationShareError('');
  }, [resetConversationSharePreview]);

  const closeConversationShare = useCallback(() => {
    transitionConversationShare({ mode: false });
  }, [transitionConversationShare]);

  const startConversationShareFromMessage = useCallback((candidate) => {
    if (!candidate?.key) return;
    transitionConversationShare({
      mode: true,
      selectedKeys: [candidate.key],
    });
  }, [transitionConversationShare]);

  useEffect(() => {
    closeConversationShare();
  }, [closeConversationShare, topic]);

  const toggleConversationShareMessage = useCallback((candidate) => {
    if (!candidate?.key) return;
    setConversationShareSelectedKeys((current) => {
      if (current.includes(candidate.key)) {
        setConversationShareError('');
        return current.filter((key) => key !== candidate.key);
      }
      if (current.length >= MAX_CONVERSATION_SHARE_MESSAGES) {
        setConversationShareError(`一次最多选择 ${MAX_CONVERSATION_SHARE_MESSAGES} 条消息。`);
        return current;
      }
      setConversationShareError('');
      return [...current, candidate.key];
    });
  }, []);

  const generateConversationShareImage = useCallback(async () => {
    if (selectedConversationShareItems.length === 0) {
      setConversationShareError('请先选择至少一条有内容的消息。');
      return;
    }
    setConversationShareGenerating(true);
    setConversationShareError('');
    setConversationShareManualSaveAvailable(false);
    try {
      const root = typeof document === 'undefined' ? null : document.documentElement;
      const theme = root?.dataset.theme === 'liquid' && root.dataset.liquidVariant === 'green'
        ? 'liquid-green'
        : (root?.dataset.theme || 'light');
      const result = await renderConversationShareImage({
        items: selectedConversationShareItems,
        topicName: displayName || topicName || topic || '对话',
        theme,
      });
      const pages = Array.isArray(result.pages) && result.pages.length > 0
        ? result.pages
        : [result];
      if (!pages.every((page) => page?.dataUrl)) {
        throw new Error('生成分享图失败，请重试。');
      }
      setConversationShareImages(pages);
      setConversationSharePreviewPage(0);
      setConversationSharePreviewOpen(true);
    } catch (error) {
      setConversationShareError(error?.message || '生成分享图失败，请重试。');
    } finally {
      setConversationShareGenerating(false);
    }
  }, [displayName, selectedConversationShareItems, topic, topicName]);

  const saveConversationShareImages = useCallback(async ({ all = false } = {}) => {
    if (conversationShareDownloading || !conversationSharePreviewImage) return;
    setConversationShareDownloading(true);
    setConversationShareError('');
    setConversationShareManualSaveAvailable(false);
    try {
      let saved;
      if (all && conversationShareImages.length > 1) {
        saved = await downloadConversationShareImages(conversationShareImages.map((image) => image.dataUrl));
      } else if (conversationShareImages.length > 1) {
        saved = await downloadConversationShareImage(
          conversationSharePreviewImage.dataUrl,
          `catsco-conversation-share-${String(conversationSharePreviewPage + 1).padStart(2, '0')}.png`,
        );
      } else {
        saved = await downloadConversationShareImage(conversationSharePreviewImage.dataUrl);
      }
      if (!saved) {
        const canOpenCurrentImage = !all || conversationShareImages.length <= 1;
        setConversationShareManualSaveAvailable(canOpenCurrentImage);
        setConversationShareError(
          all && conversationShareImages.length > 1
            ? '无法一次保存全部图片。请使用“下载当前 PNG”逐张保存，或在系统分享菜单中选择保存。'
            : '无法启动图片保存。请在新标签页中打开图片后，使用浏览器的保存功能。',
        );
      }
    } catch {
      setConversationShareError('无法启动图片保存。请检查浏览器的下载或弹窗权限后重试。');
    } finally {
      setConversationShareDownloading(false);
    }
  }, [
    conversationShareDownloading,
    conversationShareImages,
    conversationSharePreviewImage,
    conversationSharePreviewPage,
  ]);

  const openConversationShareImageManually = useCallback(() => {
    if (conversationShareDownloading || !conversationSharePreviewImage) return;
    setConversationShareError('');
    if (openConversationShareImageForManualSave(conversationSharePreviewImage.dataUrl)) {
      setConversationShareManualSaveAvailable(false);
      return;
    }
    setConversationShareError('无法在新标签页中打开图片。请检查浏览器的弹窗权限后重试。');
  }, [conversationShareDownloading, conversationSharePreviewImage]);

  useEffect(() => {
    if (!conversationShareMode) return;
    setConversationShareSelectedKeys((current) => current.filter(
      (key) => conversationShareCandidateByKey.has(key),
    ));
  }, [conversationShareCandidateByKey, conversationShareMode]);

  useEffect(() => {
    if (!conversationSharePreviewOpen) return undefined;
    const previousActiveElement = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null;
    const focusableSelector = [
      'a[href]',
      'button:not([disabled])',
      'input:not([disabled])',
      'select:not([disabled])',
      'textarea:not([disabled])',
      '[tabindex]:not([tabindex="-1"])',
    ].join(',');
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setConversationSharePreviewOpen(false);
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(
        conversationSharePreviewRef.current?.querySelectorAll(focusableSelector) || [],
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!focusable.includes(document.activeElement)) {
        event.preventDefault();
        first.focus();
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    conversationSharePreviewCloseRef.current?.focus();
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      if (previousActiveElement?.isConnected) previousActiveElement.focus();
    };
  }, [conversationSharePreviewOpen]);

  const hasPersistedRuntimePlan = useMemo(() => {
    if (!runtimePlan) return false;

    let latestHumanPromptIndex = -1;
    messages.forEach((message, index) => {
      const senderIsBot = sameUID(message.from_uid, user.uid)
        ? user.account_type === 'bot'
        : isGroup
          ? inferredAgentUIDs.has(parseUid(message.from_uid))
          : peerIsBot;
      if (isFinalTextMessage(message) && !isAssistantAuthoredMessage(message, senderIsBot)) {
        latestHumanPromptIndex = index;
      }
    });

    const currentTurnMessages = runtimePlan.turnKey
      ? messages
      : messages.slice(latestHumanPromptIndex + 1);
    const latestPersistedPlan = [...currentTurnMessages].reverse().find((message) => (
      messageContainsUpdatePlan(message)
      && runtimePlanSourceMatches(message, runtimePlan)
    ));
    return Boolean(
      latestPersistedPlan
      && workingPlanMatchesRuntimePlan(latestPersistedPlan, runtimePlan)
    );
  }, [
    isGroup,
    inferredAgentUIDs,
    messages,
    peerIsBot,
    runtimePlan,
    user.account_type,
    user.uid,
  ]);
  const openTutorialTask = (task) => {
    setShowTutorialPicker(false);
    setSelectedTutorialTask(task);
  };

  const dismissTutorialEmptyState = () => {
    writeStorageValue(tutorialDismissStorageKey(user.uid, topic), '1');
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
    }, 0);
  };

  const handleEditMessage = useCallback((message) => {
    const contentBlocks = Array.isArray(message?.content_blocks) ? message.content_blocks : [];
    const blockText = contentBlocks
      .filter((block) => block?.type === 'text' && typeof block.text === 'string')
      .map((block) => block.text)
      .join('\n\n');
    const restoredAttachments = contentBlocks
      .map(attachmentFromContentBlock)
      .filter(Boolean);
    const legacyContent = typeof message?.content === 'string' ? message.content : '';
    const attachmentSummary = summarizeAttachments(restoredAttachments);
    const originalText = blockText || (legacyContent === attachmentSummary ? '' : legacyContent);
    if (!originalText.trim() && restoredAttachments.length === 0) return;
    setInput(originalText);
    updateComposerDraft(topic, originalText);
    updateStructuredMentionDraft(topic, []);
    updateAttachmentDraft(topic, restoredAttachments);
    setReplyTo(null);
    setAttachmentStatus({
      tone: 'success',
      source: 'edit-resend',
      message: restoredAttachments.length > 0
        ? `已将原文字和 ${restoredAttachments.length} 个附件放回输入框，修改后可重新发送。`
        : '已将原指令放回输入框，修改后可重新发送。',
    });
    window.setTimeout(() => {
      const textarea = textareaRef.current;
      if (!textarea) return;
      textarea.focus();
      textarea.setSelectionRange(originalText.length, originalText.length);
    }, 0);
  }, [
    topic,
    updateAttachmentDraft,
    updateComposerDraft,
    updateStructuredMentionDraft,
  ]);

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

  useEffect(() => {
    const timeline = timelineRef.current;
    if (!timeline) return undefined;
    const anchors = Array.from(timeline.querySelectorAll('[data-conversation-question]'));
    visibleQuestionAnchorsRef.current = new Map();
    if (anchors.length === 0) {
      setActiveQuestionKey('');
      return undefined;
    }

    if (typeof window.IntersectionObserver !== 'function') {
      const fallbackKey = anchors[0].dataset.conversationQuestion || '';
      setActiveQuestionKey((current) => current || fallbackKey);
      return undefined;
    }

    const observer = new window.IntersectionObserver((entries) => {
      if (pendingQuestionJumpRef.current) return;
      const visibleAnchors = visibleQuestionAnchorsRef.current;
      entries.forEach((entry) => {
        const key = entry.target.dataset.conversationQuestion || '';
        if (!key) return;
        if (entry.isIntersecting) {
          visibleAnchors.set(key, entry.boundingClientRect.top);
        } else {
          visibleAnchors.delete(key);
        }
      });
      const nextEntry = Array.from(visibleAnchors.entries())
        .sort((left, right) => left[1] - right[1])[0];
      if (!nextEntry) return;
      setActiveQuestionKey((current) => current === nextEntry[0] ? current : nextEntry[0]);
    }, {
      root: timeline,
      rootMargin: '-18% 0px -68% 0px',
      threshold: 0,
    });

    anchors.forEach((anchor) => observer.observe(anchor));
    return () => {
      observer.disconnect();
      visibleQuestionAnchorsRef.current = new Map();
    };
  }, [questionNavigationItems]);

  useEffect(() => () => {
    if (questionJumpReleaseTimerRef.current) {
      window.clearTimeout(questionJumpReleaseTimerRef.current);
    }
  }, []);

  const jumpToQuestion = useCallback(async (questionKey) => {
    const timeline = timelineRef.current;
    if (!timeline) return;
    questionJumpAbortControllerRef.current?.abort();
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
    const targetTopic = topic;
    const controller = new AbortController();
    questionJumpAbortControllerRef.current = controller;
    try {
      const res = await api.getMessages(
        targetTopic,
        PAGE_SIZE,
        0,
        true,
        archivedQuestion.id + 1,
        { signal: controller.signal, timeoutMs: HISTORY_REQUEST_TIMEOUT_MS },
      );
      if (activeTopicRef.current !== targetTopic) {
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
    } finally {
      if (questionJumpAbortControllerRef.current === controller) {
        questionJumpAbortControllerRef.current = null;
      }
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

  useEffect(() => {
    const targetMessageId = messageLocationRequest?.topicId === topic
      ? Number(messageLocationRequest.messageId) || 0
      : 0;
    if (!targetMessageId || !historyLoaded || refreshingHistory) return undefined;
    const target = timelineRef.current?.querySelector(`[data-search-message-id="${targetMessageId}"]`);
    if (!target) return undefined;
    target.scrollIntoView({ behavior: 'smooth', block: 'center' });
    setHighlightedMessageId(targetMessageId);
    if (messageHighlightTimerRef.current) window.clearTimeout(messageHighlightTimerRef.current);
    messageHighlightTimerRef.current = window.setTimeout(() => setHighlightedMessageId(0), 3000);
    return () => {
      if (messageHighlightTimerRef.current) window.clearTimeout(messageHighlightTimerRef.current);
    };
  }, [historyLoaded, messageLocationRequest?.requestId, refreshingHistory, topic]);

  const handleTimelineScroll = (e) => {
    const el = e.target;
    stickToBottomRef.current = isTimelineNearBottom(el);
    const pendingQuestionKey = pendingQuestionJumpRef.current;
    if (pendingQuestionKey) {
      setActiveQuestionKey((current) => current === pendingQuestionKey ? current : pendingQuestionKey);
      scheduleQuestionJumpRelease();
    } else if (stickToBottomRef.current && questionNavigationItems.length > 0) {
      const latestQuestionKey = questionNavigationItems[questionNavigationItems.length - 1].key;
      setActiveQuestionKey((current) => current === latestQuestionKey ? current : latestQuestionKey);
    }
    if (el.scrollTop <= HISTORY_AUTO_LOAD_THRESHOLD) {
      loadOlderHistory({ automatic: true });
    }
  };

  const openImagePreview = useCallback((imageId, trigger, payload = null) => {
    const resolvedItem = imageGallery.find((item) => item.id === imageId)
      || imageGallery.find((item) => (
        payload?.url && item?.payload?.url === payload.url
      ))
      || imageGallery.find((item) => (
        payload?.thumbnail && item?.payload?.thumbnail === payload.thumbnail
      ));
    if (!resolvedItem) return;
    previewImageTriggerRef.current = trigger || null;
    setPreviewImageId(resolvedItem.id);
    void loadAllHistoryForImageGallery();
  }, [imageGallery, loadAllHistoryForImageGallery]);

  const closeImagePreview = useCallback(() => {
    setPreviewImageId('');
  }, []);

  const previewImageIndex = imageGallery.findIndex((item) => item.id === previewImageId);
  const previewImage = previewImageIndex >= 0 ? imageGallery[previewImageIndex] : null;

  return (
    <>
      <div
        className={`v3-message-workspace${sidePanelOpen ? ' has-preview' : ''}`}
        style={sidePanelOpen ? { '--v3-file-preview-width': `${previewWidth}px` } : undefined}
      >
        <div ref={chatColumnRef} className="v3-chat-column">
          {topBar}
          {messageLocationRequest?.topicId === topic && onBackToSearch && (
            <button type="button" className="cc-search-return" onClick={onBackToSearch}>
              <ArrowLeft size={16} />
              返回搜索结果
            </button>
          )}
          <div
            className={`v3-timeline${isDragActive ? ' is-drag-active' : ''}${conversationShareMode ? ' is-conversation-share-mode' : ''}`}
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
              {conversationShareMode && (
                <section className="cc-conversation-share-toolbar" aria-label="对话分享图选择">
                  <div className="cc-conversation-share-toolbar-copy">
                    <span className="cc-conversation-share-toolbar-icon" aria-hidden="true"><CheckSquare size={18} /></span>
                    <div>
                      <strong>选择要展示的消息</strong>
                      <span aria-live="polite">{conversationShareCandidates.length > 0 ? `已选 ${selectedConversationShareItems.length} 条，最多 ${MAX_CONVERSATION_SHARE_MESSAGES} 条，按原顺序生成分享图` : '没有可展示的消息'}</span>
                    </div>
                  </div>
                  <div className="cc-conversation-share-toolbar-actions">
                    <button type="button" className="cc-conversation-share-secondary" onClick={closeConversationShare}>
                      取消
                    </button>
                    <button
                      ref={conversationShareGenerateButtonRef}
                      type="button"
                      className="cc-conversation-share-primary"
                      disabled={!canOpenConversationShare || conversationShareGenerating || selectedConversationShareItems.length === 0}
                      onClick={() => void generateConversationShareImage()}
                    >
                      {conversationShareGenerating ? <LoaderCircle className="is-spinning" size={16} aria-hidden="true" /> : <ImageDown size={16} aria-hidden="true" />}
                      {conversationShareGenerating ? '正在生成' : '生成分享图'}
                    </button>
                  </div>
                  {conversationShareError && (
                    <p className="cc-conversation-share-toolbar-error" role="alert">{conversationShareError}</p>
                  )}
                  {!canOpenConversationShare && !conversationShareError && (
                    <p className="cc-conversation-share-toolbar-error" role="status">聊天记录加载失败，暂不能生成分享图。</p>
                  )}
                </section>
              )}
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
            <button type="button" className="v3-history-retry" onClick={() => loadOlderHistory()}>
              <RefreshCw size={14} aria-hidden="true" />
              重试
            </button>
          </div>
        )}

        {autoHistoryLimitReached && !olderHistoryError && (
          <div className="v3-history-state is-compact" role="status">
            <span>较早记录较多，已暂停自动加载。</span>
            <button type="button" className="v3-history-retry" onClick={() => loadOlderHistory()}>
              继续加载
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
              <div
                key={`working:${group.messages[0].id || 'group'}:${i}`}
                className={`oc-working-group cc-message-anchor${!conversationShareMode && highlightedMessageId > 0 && group.messages.some((message) => historyMessageID(message) === highlightedMessageId) ? ' cc-message-search-hit' : ''}`}
              >
                {group.messages.map((message, messageIndex) => (
                  <span
                    key={`search-anchor-${historyMessageID(message)}-${messageIndex}`}
                    className="cc-message-search-anchor"
                    data-search-message-id={historyMessageID(message) || undefined}
                    aria-hidden="true"
                  />
                ))}
                <ChatMessage
                  message={group.messages[0]}
                  workingMessages={group.messages}
                  isSelf={sameUID(group.messages[0].from_uid, user.uid)}
                  isGroup={isGroup}
                  senderName={group.sender.name}
                  senderAvatarUrl={group.sender.avatarUrl}
                  senderIsBot={group.sender.isBot}
                  mentionDisplayNames={mentionDisplayNames}
                  workingOnly
                  workingComplete={group.workingComplete}
                  showThinking={showThinking}
                  isConsecutive={group.isConsecutive}
                  onPreviewFile={openFilePreview}
                  activePreviewFile={previewFile}
                  knownArtifacts={knownArtifacts}
                  imageGallery={imageGallery}
                  onOpenImage={openImagePreview}
                />
              </div>
            );
          }
          const candidate = conversationShareCandidateByMessage.get(group.message);
          const selectable = conversationShareMode && Boolean(candidate);
          const selected = selectable && conversationShareSelectedKeys.includes(candidate.key);
          return (
            <div
              key={`message:${group.message.id || 'group'}:${i}`}
              className={`cc-message-anchor${!conversationShareMode && highlightedMessageId > 0 && historyMessageID(group.message) === highlightedMessageId ? ' cc-message-search-hit' : ''}${selectable ? ' is-conversation-share-selectable' : ''}${selected ? ' is-conversation-share-selected' : ''}`}
              data-search-message-id={historyMessageID(group.message) || undefined}
            >
              {selectable && (
                <button
                  type="button"
                  className={`cc-conversation-share-message-toggle${selected ? ' is-selected' : ''}`}
                  aria-label={`${selected ? '取消选择' : '选择'}消息：${conversationShareText(group.message).slice(0, 36) || '消息'}`}
                  aria-pressed={selected}
                  title={selected ? '取消选择此消息' : '选择此消息'}
                  onClick={(event) => {
                    event.stopPropagation();
                    toggleConversationShareMessage(candidate);
                  }}
                >
                  <span className="cc-conversation-share-message-toggle-indicator" aria-hidden="true">
                    {selected && <Check size={14} strokeWidth={2.5} />}
                  </span>
                </button>
              )}
              <ChatMessage
                message={group.message}
                isSelf={sameUID(group.message.from_uid, user.uid)}
                isGroup={isGroup}
                senderName={group.sender.name}
                senderAvatarUrl={group.sender.avatarUrl}
                senderIsBot={group.sender.isBot}
                mentionDisplayNames={mentionDisplayNames}
                replyMessage={group.replyMessage}
                questionAnchorKey={sameUID(group.message.from_uid, user.uid)
                  ? questionNavigationKey(group.message, i)
                  : undefined}
                onReply={() => setReplyTo(group.message)}
                onEdit={sameUID(group.message.from_uid, user.uid) ? handleEditMessage : undefined}
                onRegenerate={canRegenerateAssistantMessages
                  && !sameUID(group.message.from_uid, user.uid)
                  && isAssistantAuthoredMessage(group.message, group.sender.isBot)
                  ? handleRegenerateMessage
                  : undefined}
                onCreateConversationShare={candidate && !conversationShareMode
                  ? () => startConversationShareFromMessage(candidate)
                  : undefined}
                showThinking={showThinking}
                isConsecutive={showThinking
                  ? group.isConsecutive
                  : (group.isConsecutiveWithoutWorking ?? group.isConsecutive)}
                artifactsFirst={group.artifactsFirst}
                onPreviewFile={openFilePreview}
                activePreviewFile={previewFile}
                knownArtifacts={knownArtifacts}
                imageGallery={imageGallery}
                onOpenImage={openImagePreview}
              />
            </div>
          );
        })}
          {runtimePlan && !hasPersistedRuntimePlan && (
            <RuntimePlanCard plan={runtimePlan} />
          )}
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
        onVoiceFinal={handleVoiceFinal}
        voiceInputDisabled={isSendingMessage || isUploadingAttachment}
        voiceSessionKey={topic}
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
        agentReplyActive={Boolean(selectedAgent) && (
          isSendingMessage || awaitingAgentReply || peerTyping || activeBotWorking
        )}
        sendDisabled={isSendingMessage || isUploadingAttachment || (!input.trim() && pendingAttachments.length === 0)}
        stop={canStopActiveBotWorking && !input.trim() && pendingAttachments.length === 0}
        stopDisabled={isStopRequested}
        onStop={handleStopGeneration}
        onCloseMenus={() => {
          setAttachmentMenuOpen(false);
        }}
        context={replyTo && (
          <div className="oc-reply-bar">
            <div className="oc-reply-bar-content">
              <span className="oc-reply-bar-label">{t('chat_reply')}：</span>
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
              <X size={16} aria-hidden="true" />
            </button>
          </div>
        )}
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
            <div className="v3-drop-title">拖放图片或文件到这里</div>
            <div className="v3-drop-subtitle">支持聊天中的图片、本地图片、文件和文件夹，附件会先放在这里等待发送。</div>
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
        {sidePanelOpen && (
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
            {cloudArtifactsListOpen ? (
              <CloudArtifactsPanel
                agentUid={cloudArtifactsAgentUID}
                topicId={topic}
                tab={cloudArtifactsTab}
                onTabChange={setCloudArtifactsTab}
                onClose={closeSidePanel}
                onPreviewArtifact={previewCloudArtifact}
                onPreviewFile={previewAgentFile}
              />
            ) : (
              <FilePreviewPanel
                file={previewFile}
                pendingRemoteArtifactFile={pendingArtifactRefresh}
                onBack={cloudArtifactsReturnOpen ? returnToCloudArtifacts : undefined}
                onClose={closeSidePanel}
                backgroundRef={chatColumnRef}
                onRemoteArtifactRefreshReady={handleArtifactRefreshReady}
                onRemoteArtifactRefreshFailed={handleArtifactRefreshFailed}
                onRemoteArtifactFrameChange={handleRemoteArtifactFrameChange}
              />
            )}
          </div>
        )}
      </div>
      {previewImage && typeof document !== 'undefined' && (
        <ImageGalleryPreview
          item={previewImage}
          index={previewImageIndex}
          items={imageGallery}
          onClose={closeImagePreview}
          onChange={(nextIndex) => {
            const next = imageGallery[nextIndex];
            if (!next) return;
            setPreviewImageId(next.id);
          }}
          triggerRef={previewImageTriggerRef}
        />
      )}
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
      {conversationSharePreviewOpen && conversationSharePreviewImage && typeof document !== 'undefined' && createPortal(
        <div
          className="cc-conversation-share-preview-backdrop"
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) setConversationSharePreviewOpen(false);
          }}
        >
          <section
            ref={conversationSharePreviewRef}
            className="cc-conversation-share-preview"
            role="dialog"
            aria-modal="true"
            aria-labelledby="conversation-share-preview-title"
            aria-describedby="conversation-share-preview-description"
            tabIndex={-1}
            onMouseDown={(event) => event.stopPropagation()}
          >
            <header className="cc-conversation-share-preview-header">
              <div>
                <span className="catsco-brand-mark" aria-hidden="true" />
                <div>
                  <strong id="conversation-share-preview-title">对话分享图已生成</strong>
                  <span id="conversation-share-preview-description">
                    已保留 {selectedConversationShareItems.length} 条选中消息
                    {conversationShareImages.length > 1 ? `，共 ${conversationShareImages.length} 张` : ''}
                  </span>
                </div>
              </div>
              <button ref={conversationSharePreviewCloseRef} type="button" className="v3-tool" aria-label="关闭分享图预览" onClick={() => setConversationSharePreviewOpen(false)}>
                <X size={17} aria-hidden="true" />
              </button>
            </header>
            {conversationShareImages.length > 1 && (
              <div className="cc-conversation-share-preview-page-nav" aria-label="分享图分页">
                <button
                  type="button"
                  className="v3-tool"
                  aria-label="查看上一张分享图"
                  disabled={conversationSharePreviewPage === 0}
                  onClick={() => setConversationSharePreviewPage((current) => Math.max(0, current - 1))}
                >
                  <ChevronLeft size={17} aria-hidden="true" />
                </button>
                <span aria-live="polite">第 {conversationSharePreviewPage + 1} / {conversationShareImages.length} 张</span>
                <button
                  type="button"
                  className="v3-tool"
                  aria-label="查看下一张分享图"
                  disabled={conversationSharePreviewPage >= conversationShareImages.length - 1}
                  onClick={() => setConversationSharePreviewPage((current) => Math.min(conversationShareImages.length - 1, current + 1))}
                >
                  <ChevronRight size={17} aria-hidden="true" />
                </button>
              </div>
            )}
            <div className="cc-conversation-share-preview-canvas">
              <img
                src={conversationSharePreviewImage.dataUrl}
                alt={`${displayName || topicName || '对话'}的 CatsCo 分享图，第 ${conversationSharePreviewPage + 1} 张，共 ${conversationShareImages.length} 张`}
              />
            </div>
            {conversationShareDownloading && (
              <p className="cc-conversation-share-preview-status" role="status">正在打开图片保存…</p>
            )}
            {conversationShareError && (
              <p className="cc-conversation-share-preview-status is-error" role="alert">{conversationShareError}</p>
            )}
            <footer className="cc-conversation-share-preview-actions">
              <button type="button" className="cc-conversation-share-secondary" onClick={() => setConversationSharePreviewOpen(false)}>
                返回选择
              </button>
              {conversationShareImages.length > 1 && (
                <button
                  type="button"
                  className="cc-conversation-share-secondary"
                  disabled={conversationShareDownloading}
                  onClick={() => void saveConversationShareImages()}
                >
                  {conversationShareDownloading ? '正在打开…' : '下载当前 PNG'}
                </button>
              )}
              {conversationShareManualSaveAvailable && (
                <button
                  type="button"
                  className="cc-conversation-share-secondary"
                  disabled={conversationShareDownloading}
                  onClick={openConversationShareImageManually}
                >
                  在新标签页打开图片
                </button>
              )}
              <button
                type="button"
                className="cc-conversation-share-primary"
                disabled={conversationShareDownloading}
                onClick={() => void saveConversationShareImages({ all: conversationShareImages.length > 1 })}
              >
                <Download size={16} aria-hidden="true" />
                {conversationShareDownloading
                  ? '正在打开…'
                  : (conversationShareImages.length > 1
                    ? (isMobileConversationShareBrowser() ? '系统分享全部图片' : '下载全部图片（ZIP）')
                    : '下载 PNG')}
              </button>
            </footer>
          </section>
        </div>,
        document.body,
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

export function shouldConvertPastedTextToDocument(text) {
  const value = typeof text === 'string' ? text : '';
  if (!value.trim()) return false;
  if (value.length >= LONG_PASTE_CHAR_THRESHOLD) return true;
  if (value.length < LONG_PASTE_MULTILINE_CHAR_THRESHOLD) return false;
  return value.split(/\r\n?|\n/u).length >= LONG_PASTE_LINE_THRESHOLD;
}

function createPastedTextDocument(text, now = new Date()) {
  const normalizedText = String(text || '').replace(/\r\n?/gu, '\n');
  const timestampParts = new Intl.DateTimeFormat('en-CA', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).formatToParts(now);
  const timestamp = Object.fromEntries(timestampParts.map(({ type, value }) => [type, value]));
  const filename = `粘贴内容-${timestamp.year}${timestamp.month}${timestamp.day}-${timestamp.hour}${timestamp.minute}${timestamp.second}.md`;
  return new File([normalizedText], filename, {
    type: 'text/markdown;charset=utf-8',
    lastModified: now.getTime(),
  });
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

function contentBlocksFromMessage(message) {
  const direct = parseContentBlocks(message?.content_blocks);
  if (direct.length > 0) return direct;

  const content = parseStructuredMessageContent(message?.content);
  return parseContentBlocks(content?.content_blocks || content?.contentBlocks);
}

function parseContentBlocks(value) {
  if (Array.isArray(value)) return value;
  if (typeof value !== 'string') return [];
  try {
    const parsed = JSON.parse(value);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function parseStructuredMessageContent(value) {
  if (value && typeof value === 'object' && !Array.isArray(value)) return value;
  if (typeof value !== 'string') return null;
  try {
    const parsed = JSON.parse(value);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed;
    return null;
  } catch {
    return null;
  }
}

function normalizeIncomingMessage(message) {
  const normalized = { ...message };
  normalized.content_blocks = contentBlocksFromMessage(message);
  normalized.metadata = message?.metadata || null;
  normalized.client_msg_id = messageClientMsgID(message);
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
  const normalizedPlan = plan || (
    explicitPlan ? normalizeRuntimePlan(data.payload || data.metadata?.plan || data) : null
  );
  if (!normalizedPlan) return null;
  return {
    ...normalizedPlan,
    senderKey: messageSenderIdentity(data),
    turnKey: assistantReplyTurnKey(data),
  };
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

function messageContainsUpdatePlan(message) {
  const storedBlocks = Array.isArray(message?.content_blocks) ? message.content_blocks : [];
  return storedBlocks.some((block) => (
    block?.type === 'tool_use'
    && String(block?.name || block?.content || '').trim() === 'update_plan'
  )) || (
    message?.type === 'tool_use'
    && String(message?.content || '').trim() === 'update_plan'
  );
}

function runtimePlanSourceMatches(message, runtimePlan) {
  const runtimeSenderKey = String(runtimePlan?.senderKey || '');
  const messageSenderKey = messageSenderIdentity(message);
  if (runtimeSenderKey && messageSenderKey && runtimeSenderKey !== messageSenderKey) {
    return false;
  }

  const runtimeTurnKey = String(runtimePlan?.turnKey || '');
  const messageTurnKey = assistantReplyTurnKey(message);
  if (runtimeTurnKey && messageTurnKey && runtimeTurnKey !== messageTurnKey) {
    return false;
  }
  return true;
}

function workingPlanMatchesRuntimePlan(message, runtimePlan) {
  if (!messageContainsUpdatePlan(message) || !runtimePlanSourceMatches(message, runtimePlan)) {
    return false;
  }
  const runtimeSteps = normalizedPlanSteps(runtimePlan?.steps);
  if (runtimeSteps.length === 0) return false;

  const storedBlocks = Array.isArray(message?.content_blocks) ? message.content_blocks : [];
  const planBlock = [...storedBlocks].reverse().find((block) => (
    block?.type === 'tool_use'
    && String(block?.name || block?.content || '').trim() === 'update_plan'
  ));
  const isDirectPlanMessage = message?.type === 'tool_use'
    && String(message?.content || '').trim() === 'update_plan';
  if (!planBlock && !isDirectPlanMessage) return false;

  const input = planBlock?.input
    || planBlock?.metadata?.input
    || message?.metadata?.input;
  const persistedSteps = normalizedPlanSteps(input?.steps || input?.plan);
  return persistedSteps.length === runtimeSteps.length
    && persistedSteps.every((step, index) => (
      step.text === runtimeSteps[index].text
      && step.status === runtimeSteps[index].status
    ));
}

function normalizedPlanSteps(steps) {
  if (!Array.isArray(steps)) return [];
  return steps
    .map((step) => ({
      text: String(
        typeof step === 'string'
          ? step
          : (step?.text || step?.step || step?.title || step?.name || ''),
      ).trim(),
      status: normalizePlanStatus(typeof step === 'string' ? 'pending' : step?.status),
    }))
    .filter((step) => step.text);
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
  // These identifiers describe the Agent execution that produced the row.
  // `stream_id` is intentionally excluded: it is a transport/lifecycle
  // identifier used to reconcile transient deltas, and a runtime may reuse it
  // for a later round. Reusing it for visual grouping would hide the later
  // row's avatar and name.
  const candidates = [
    ['run', metadata.run_id],
    ['run', metadata.runId],
    ['turn', metadata.turn_id],
    ['turn', metadata.turnId],
    ['response', metadata.response_id],
    ['response', metadata.responseId],
  ];
  for (const [kind, candidate] of candidates) {
    if (typeof candidate !== 'string') continue;
    const value = candidate.trim();
    if (value) return `${kind}:${value}`;
  }
  return '';
}

function messageSenderIdentity(message) {
  const rawSender = message?.from_uid ?? message?.from ?? '';
  const parsedSender = parseUid(rawSender);
  return parsedSender ? String(parsedSender) : String(rawSender).trim();
}

function assistantExecutionKey(message) {
  const senderKey = messageSenderIdentity(message);
  const turnKey = assistantReplyTurnKey(message);
  return senderKey && turnKey ? `${senderKey}:turn:${turnKey}` : '';
}

function renderedGroupBoundaryMessage(group, edge = 'first') {
  const sourceMessages = group?.type === 'working'
    ? (group.messages || [])
    : (group?.sourceMessages || (group?.message ? [group.message] : []));
  return edge === 'last'
    ? sourceMessages[sourceMessages.length - 1]
    : sourceMessages[0];
}

function renderedGroupSenderIdentity(group, edge = 'first') {
  const messageIdentity = messageSenderIdentity(renderedGroupBoundaryMessage(group, edge));
  const senderRole = group?.sender?.isBot ? 'agent' : 'member';
  return `${messageIdentity}:${senderRole}`;
}

export function reconcileRenderedGroupConsecutiveness(groups = []) {
  return groups.map((group, index) => {
    if (index === 0 || !group?.isConsecutive) return group;

    const previousGroup = groups[index - 1];
    const previousSender = renderedGroupSenderIdentity(previousGroup, 'last');
    const currentSender = renderedGroupSenderIdentity(group, 'first');
    if (previousSender === currentSender) return group;

    return {
      ...group,
      isConsecutive: false,
    };
  });
}

function messageTurnIdentity(message, index) {
  const value = message?.id
    ?? message?.seq_id
    ?? message?.seq
    ?? message?.client_msg_id
    ?? message?.created_at
    ?? index;
  return String(value);
}

function explicitExecutionKey(value) {
  if (typeof value === 'string') return value.trim();
  return typeof value?.explicitTurnKey === 'string' ? value.explicitTurnKey.trim() : '';
}

function hasSameExplicitExecutionKey(previous, current) {
  const previousKey = explicitExecutionKey(previous);
  const currentKey = explicitExecutionKey(current);
  return Boolean(previousKey && currentKey && previousKey === currentKey);
}

function assistantWorkTurn(message, senderIsBot) {
  if (!isWorkingMessage(message) && !isAssistantAuthoredMessage(message, senderIsBot)) {
    return { explicitTurnKey: '' };
  }

  return {
    explicitTurnKey: assistantExecutionKey(message),
  };
}

function messageDisplayContext(message, senderIsBot) {
  const senderKey = messageSenderIdentity(message);
  const turn = assistantWorkTurn(message, senderIsBot);
  return {
    senderKey,
    turn,
    explicitTurnKey: turn.explicitTurnKey,
    isAssistant: isWorkingMessage(message) || isAssistantAuthoredMessage(message, senderIsBot),
    sentAt: new Date(message?.created_at || Date.now()).getTime(),
  };
}

function areMessagesConsecutive(previousContext, currentContext) {
  if (!previousContext || !currentContext) return false;
  if (previousContext.senderKey !== currentContext.senderKey) return false;

  if (!previousContext.isAssistant && !currentContext.isAssistant) {
    return currentContext.sentAt - previousContext.sentAt < CONSECUTIVE_HUMAN_MESSAGE_WINDOW_MS;
  }

  // Identity is a navigation affordance, not expendable decoration. Only
  // compact entries when both rows can be tied to the same Agent execution.
  // A missing correlation key must fail open and show the sender again.
  return Boolean(
    previousContext.isAssistant
    && currentContext.isAssistant
    && hasSameExplicitExecutionKey(previousContext, currentContext),
  );
}

function assistantProcessMessage(message) {
  const content = assistantOutputText(message);
  return {
    ...message,
    type: 'text',
    content,
    content_blocks: [],
    _display_text_role: 'process',
  };
}

function isDeliveryArtifactType(type) {
  return DELIVERY_ARTIFACT_TYPES.has(type);
}

function messageHasDeliveryArtifact(message) {
  if (Array.isArray(message?.content_blocks)) {
    if (message.content_blocks.some((block) => isDeliveryArtifactType(block?.type))) {
      return true;
    }
  }

  let content = message?.content;
  if (typeof content === 'string') {
    try {
      content = JSON.parse(content);
    } catch (error) {
      return false;
    }
  }
  return isDeliveryArtifactType(content?.type);
}

function displayGroupHasDeliveryArtifact(group) {
  const sourceMessages = group?.sourceMessages || (group?.message ? [group.message] : []);
  return sourceMessages.some(messageHasDeliveryArtifact);
}

function deliveryArtifactBlocks(message) {
  if (Array.isArray(message?.content_blocks)) {
    const storedBlocks = message.content_blocks.filter(
      (block) => isDeliveryArtifactType(block?.type),
    );
    if (storedBlocks.length > 0) return storedBlocks;
  }

  let content = message?.content;
  if (typeof content === 'string') {
    try {
      content = JSON.parse(content);
    } catch (error) {
      return [];
    }
  }
  return isDeliveryArtifactType(content?.type) ? [content] : [];
}

function hasAssistantBlockFormatting(value) {
  const text = String(value || '');
  return /(?:^|\n)[\t ]*(?:#{1,6}[\t ]+|[-*+][\t ]+|\d+[.)][\t ]+|>[\t ]+|```|~~~|\|.+\|[\t ]*$)/m.test(text);
}

function assistantTextFragmentBoundary(previous, next) {
  if (
    previous.includes('\n')
    || next.includes('\n')
    || hasAssistantBlockFormatting(previous)
    || hasAssistantBlockFormatting(next)
  ) {
    return '\n\n';
  }

  const previousCharacter = previous.at(-1) || '';
  const nextCharacter = next.charAt(0);
  const cjkCharacterOrPunctuation = /[\u3000-\u303f\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\uff00-\uffef]/u;
  if (
    cjkCharacterOrPunctuation.test(previousCharacter)
    || cjkCharacterOrPunctuation.test(nextCharacter)
  ) {
    return '';
  }
  if (
    /[\s([{'"“‘]$/u.test(previous)
    || /^[\s,.;:!?)}\]'"”’]/u.test(next)
  ) {
    return '';
  }
  return ' ';
}

function mergeAssistantTextFragments(fragments) {
  const normalized = fragments
    .map((fragment) => String(fragment || '').trim())
    .filter(Boolean);
  let merged = '';
  let previous = '';
  for (const next of normalized) {
    if (!merged) {
      merged = next;
    } else {
      merged += `${assistantTextFragmentBoundary(previous, next)}${next}`;
    }
    previous = next;
  }
  return merged;
}

function assistantOutputText(message) {
  const textBlocks = Array.isArray(message?.content_blocks)
    ? message.content_blocks
      .filter((block) => block?.type === 'text')
      .map((block) => String(block.text || block.content || '').trim())
      .filter(Boolean)
    : [];
  if (textBlocks.length > 0) return textBlocks.join('\n\n');

  const content = typeof message?.content === 'string' ? message.content.trim() : '';
  if (content) {
    try {
      const parsed = JSON.parse(content);
      if (isDeliveryArtifactType(parsed?.type)) return '';
    } catch (error) {
      // Plain assistant text.
    }
  }
  if (/^\[(?:文件|图片|语音)\]\s*[^\n]*$/u.test(content)) return '';
  return content;
}

function mergeAssistantOutputGroups(groups) {
  if (groups.length === 0) return null;

  const executionKey = explicitExecutionKey(groups[0]);
  if (!executionKey || groups.some((group) => !hasSameExplicitExecutionKey(executionKey, group))) {
    return null;
  }

  const sourceMessages = groups.flatMap((group) => (
    group.sourceMessages || (group.message ? [group.message] : [])
  ));
  const artifactBlocks = sourceMessages.flatMap(deliveryArtifactBlocks);
  const hasArtifacts = artifactBlocks.length > 0;
  const textByRole = new Map();

  sourceMessages.forEach((message) => {
    const text = assistantOutputText(message);
    if (!text) return;

    let role = message?._display_text_role || 'body';
    if (role === 'body' && hasArtifacts) {
      role = 'result';
    }
    const fragments = textByRole.get(role) || [];
    fragments.push(text);
    textByRole.set(role, fragments);
  });

  const textRoles = hasArtifacts
    ? ['result', 'body', 'process']
    : ['body', 'process', 'result'];
  const textBlocks = textRoles
    .map((role) => {
      const text = mergeAssistantTextFragments(textByRole.get(role) || []);
      return text ? { type: 'text', text, presentation_role: role } : null;
    })
    .filter(Boolean);
  const content = textBlocks.map((block) => block.text).join('\n\n');
  const contentBlocks = [
    ...artifactBlocks,
    ...textBlocks,
  ];
  const lastGroup = groups[groups.length - 1];
  const firstGroup = groups[0];

  return {
    ...lastGroup,
    message: {
      ...lastGroup.message,
      content,
      content_blocks: contentBlocks,
    },
    sourceMessages,
    sender: lastGroup.sender || firstGroup.sender,
    replyMessage: lastGroup.replyMessage || null,
    explicitTurnKey: executionKey,
    isConsecutive: Boolean(firstGroup.isConsecutive),
    isConsecutiveWithoutWorking: Boolean(firstGroup.isConsecutiveWithoutWorking),
    artifactsFirst: artifactBlocks.length > 0,
  };
}

function messageHasActionTool(message) {
  const messageTypes = [message?.type, message?.msg_type].filter(Boolean);
  if (messageTypes.includes('tool_use')) {
    return String(message?.content || '').trim() !== 'update_plan';
  }
  return Array.isArray(message?.content_blocks) && message.content_blocks.some((block) => (
    block?.type === 'tool_use'
    && String(block.name || block.content || '').trim() !== 'update_plan'
  ));
}

function displayGroupHasExplicitProcessText(group) {
  if (displayGroupHasDeliveryArtifact(group)) return false;
  const sourceMessages = group?.sourceMessages || (group?.message ? [group.message] : []);
  return sourceMessages.some((message) => (
    message?._display_text_role === 'process'
    || isWorkingTextMessage(message)
    || message?.content_blocks?.some((block) => (
      block?.type === 'text' && block.presentation_role === 'process'
    ))
  ));
}

function reorderAssistantTurnBundle(groups) {
  if (!groups.some((group) => group.type === 'working')) {
    return groups;
  }

  const executionKey = explicitExecutionKey(groups[0]);
  if (!executionKey || groups.some((group) => !hasSameExplicitExecutionKey(executionKey, group))) {
    return groups;
  }

  const firstIsConsecutive = Boolean(groups[0]?.isConsecutive);
  const sourceWorkingGroups = groups.filter((group) => group.type === 'working');
  const processGroupIndexes = new Set();
  groups.forEach((group, index) => {
    if (group.type !== 'text' || displayGroupHasDeliveryArtifact(group)) return;
    if (displayGroupHasExplicitProcessText(group)) {
      processGroupIndexes.add(index);
    }
  });
  const executionMessages = groups.flatMap((group, index) => {
    if (group.type === 'working') return group.messages || [];
    if (!processGroupIndexes.has(index)) return [];
    const sourceMessages = group.sourceMessages || (group.message ? [group.message] : []);
    return sourceMessages.map(assistantProcessMessage);
  });
  const outputGroups = groups.filter((group, index) => (
    group.type !== 'working' && !processGroupIndexes.has(index)
  ));
  const mergedOutput = mergeAssistantOutputGroups(outputGroups);
  const workingGroups = sourceWorkingGroups.length > 0
    ? [{
      ...sourceWorkingGroups[0],
      messages: executionMessages,
      workingComplete: Boolean(mergedOutput),
      sender: sourceWorkingGroups[sourceWorkingGroups.length - 1]?.sender || sourceWorkingGroups[0]?.sender,
      explicitTurnKey: executionKey,
    }]
    : [];
  const ordered = [...workingGroups, ...(mergedOutput ? [mergedOutput] : [])];
  let firstOutputFound = false;

  return ordered.map((group, index) => {
    const next = {
      ...group,
      // The working trace inherits its place relative to the preceding row.
      // The final reply already carries an independently computed display
      // context. Do not turn it into a grouped row merely because it was
      // reordered after the trace: without a proven turn ID, that would hide
      // the Agent avatar and name on a standalone reply.
      isConsecutive: index === 0 ? firstIsConsecutive : Boolean(group.isConsecutive),
    };
    if (group.type !== 'working') {
      if (!firstOutputFound) {
        next.isConsecutiveWithoutWorking = firstIsConsecutive;
        firstOutputFound = true;
      }
      if (displayGroupHasDeliveryArtifact(group)) {
        next.artifactsFirst = true;
      }
    }
    return next;
  });
}

function reorderAssistantSegment(groups) {
  const entries = [];

  for (const group of groups) {
    const senderKey = messageSenderIdentity(
      group?.messages?.[0] || group?.message,
    ) || String(group?.sender?.name || '');
    const explicitTurnKey = explicitExecutionKey(group);
    let bundle = entries[entries.length - 1];
    const canJoinBundle = Boolean(
      bundle
      && bundle.senderKey === senderKey
      && hasSameExplicitExecutionKey(bundle, group),
    );
    if (!canJoinBundle) {
      bundle = { type: 'bundle', senderKey, explicitTurnKey, groups: [] };
      entries.push(bundle);
    }
    bundle.groups.push(group);
  }

  return entries.flatMap((entry) => reorderAssistantTurnBundle(entry.groups));
}

function reorderAssistantTurnGroups(groups) {
  const ordered = [];
  let assistantSegment = [];

  const flushAssistantSegment = () => {
    if (assistantSegment.length === 0) return;
    ordered.push(...reorderAssistantSegment(assistantSegment));
    assistantSegment = [];
  };

  for (const group of groups) {
    if (group.type === 'working' || group.assistantAuthored) {
      assistantSegment.push(group);
      continue;
    }
    flushAssistantSegment();
    ordered.push(group);
  }
  flushAssistantSegment();
  return ordered;
}

function hasRichMessageBlocks(message) {
  return Array.isArray(message?.content_blocks) && message.content_blocks.length > 0;
}

function shouldMergeAssistantReply(previous, current, previousSender, currentSender, currentUserUid) {
  if (!previous || !current) return false;
  if (!sameUID(previous.from_uid, current.from_uid) || sameUID(current.from_uid, currentUserUid)) return false;
  if (previous.topic_id && current.topic_id && previous.topic_id !== current.topic_id) return false;
  if (!isFinalTextMessage(previous) || !isFinalTextMessage(current)) return false;
  if (!isAssistantAuthoredMessage(previous, previousSender?.isBot)) return false;
  if (!isAssistantAuthoredMessage(current, currentSender?.isBot)) return false;
  if (previous.reply_to || current.reply_to) return false;
  if (previous._streaming || current._streaming) return false;
  if (hasRichMessageBlocks(previous) || hasRichMessageBlocks(current)) return false;

  return hasSameExplicitExecutionKey(
    { explicitTurnKey: assistantExecutionKey(previous) },
    { explicitTurnKey: assistantExecutionKey(current) },
  );
}

function mergeAssistantDisplayMessages(sourceMessages) {
  const lastMessage = sourceMessages[sourceMessages.length - 1];
  return {
    ...lastMessage,
    content: mergeAssistantTextFragments(
      sourceMessages.map((message) => String(message.content || '')),
    ),
    content_blocks: [],
    _display_source_messages: sourceMessages,
  };
}

function RuntimePlanCard({ plan }) {
  const [open, setOpen] = useState(false);
  const stepsID = `runtime-plan-steps-${useId().replace(/:/g, '')}`;
  if (!plan || !Array.isArray(plan.steps) || plan.steps.length === 0) return null;

  const completed = plan.steps.filter((step) => step.status === 'completed').length;
  const current = plan.steps.find((step) => step.status === 'in_progress') || plan.steps.find((step) => step.status === 'pending');

  return (
    <div className="v3-runtime-plan-card" role="status">
      <button
        className="v3-runtime-plan-toggle"
        type="button"
        aria-expanded={open}
        aria-controls={stepsID}
        onClick={() => setOpen(!open)}
      >
        {open
          ? <ChevronDown size={14} aria-hidden="true" />
          : <ChevronRight size={14} aria-hidden="true" />}
        <span className="v3-runtime-plan-title">计划</span>
        <span className="v3-runtime-plan-count">{completed}/{plan.steps.length}</span>
        {!open && current && <span className="v3-runtime-plan-current">{current.text}</span>}
      </button>
      {open && (
        <div
          id={stepsID}
          className="v3-runtime-plan-steps"
          role="region"
          aria-label="实时计划步骤"
        >
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

function streamSenderKey({ fromUid, from }) {
  const parsedUID = parseUid(fromUid);
  if (parsedUID > 0) return String(parsedUID);
  const parsedFrom = parseUid(from);
  if (parsedFrom > 0) return String(parsedFrom);
  return String(from ?? fromUid ?? '').trim();
}

function streamMessageParts({ streamId, topic, fromUid, from }) {
  const normalizedStreamID = typeof streamId === 'string' ? streamId.trim() : '';
  const senderKey = streamSenderKey({ fromUid, from });
  return normalizedStreamID && topic && senderKey
    ? [topic, senderKey, normalizedStreamID]
    : null;
}

function streamMessageBaseKey(message) {
  const parts = streamMessageParts(message);
  return parts ? JSON.stringify(parts) : '';
}

function streamMessageKey({ executionKey = '', ...message }) {
  const parts = streamMessageParts(message);
  const normalizedExecutionKey = typeof executionKey === 'string' ? executionKey.trim() : '';
  return parts ? JSON.stringify([...parts, normalizedExecutionKey]) : '';
}

function streamPlaceholderKey(message) {
  if (!message?._streaming) return '';
  return message?._stream_key || streamMessageKey({
    streamId: getStreamId(message),
    topic: message?.topic_id,
    fromUid: message?.from_uid,
    from: message?.from,
    executionKey: assistantReplyTurnKey(message),
  });
}

function streamPlaceholderBaseKey(message) {
  if (!message?._streaming) return '';
  return streamMessageBaseKey({
    streamId: getStreamId(message),
    topic: message?.topic_id,
    fromUid: message?.from_uid,
    from: message?.from,
  });
}

function findStreamingMessageForFinal(messages, finalMessage) {
  const finalStreamID = getStreamId(finalMessage);
  const finalTurnKey = assistantReplyTurnKey(finalMessage);
  const finalSenderKey = messageSenderIdentity(finalMessage);
  if (!finalSenderKey || (!finalStreamID && !finalTurnKey)) return -1;

  const candidates = messages
    .map((message, index) => ({ message, index }))
    .filter(({ message }) => (
      message?._streaming && messageSenderIdentity(message) === finalSenderKey
    ));
  if (finalTurnKey) {
    const sameTurn = candidates.filter(({ message }) => (
      assistantReplyTurnKey(message) === finalTurnKey
    ));
    if (sameTurn.length === 1) return sameTurn[0].index;

    const uncorrelated = candidates.filter(({ message }) => (
      !assistantReplyTurnKey(message)
      && (!finalStreamID || getStreamId(message) === finalStreamID)
    ));
    return uncorrelated.length === 1 ? uncorrelated[0].index : -1;
  }

  const sameStream = candidates.filter(({ message }) => getStreamId(message) === finalStreamID);
  return sameStream.length === 1 ? sameStream[0].index : -1;
}

function isUncorrelatedFinalReply(message) {
  return isFinalTextMessage(message)
    && !getStreamId(message)
    && !assistantReplyTurnKey(message);
}

function removeStaleStreamingMessagesForFinal(messages, finalMessage) {
  if (!isUncorrelatedFinalReply(finalMessage)) return messages;
  const finalSenderKey = messageSenderIdentity(finalMessage);
  if (!finalSenderKey) return messages;
  const candidates = messages.filter((message) => (
    message?._streaming && messageSenderIdentity(message) === finalSenderKey
  ));
  if (candidates.length !== 1) return messages;
  return messages.filter((message) => message !== candidates[0]);
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

function upsertStreamingMessage(messages, {
  streamId,
  topic,
  fromUid,
  from,
  content,
  metadata,
  role,
}) {
  const executionKey = assistantReplyTurnKey({ metadata });
  const streamKey = streamMessageKey({ streamId, topic, fromUid, from, executionKey });
  if (!streamKey) return messages;
  const streamBaseKey = streamMessageBaseKey({ streamId, topic, fromUid, from });
  let existingIdx = messages.findIndex((message) => streamPlaceholderKey(message) === streamKey);
  if (existingIdx === -1) {
    const compatible = messages
      .map((message, index) => ({ message, index }))
      .filter(({ message }) => streamPlaceholderBaseKey(message) === streamBaseKey)
      .filter(({ message }) => {
        const existingExecutionKey = assistantReplyTurnKey(message);
        return !executionKey || !existingExecutionKey;
      });
    if (compatible.length === 1) existingIdx = compatible[0].index;
  }
  if (existingIdx !== -1) {
    const next = [...messages];
    const existing = next[existingIdx];
    const existingExecutionKey = assistantReplyTurnKey(existing);
    const nextStreamKey = existingExecutionKey && !executionKey
      ? streamPlaceholderKey(existing)
      : streamKey;
    next[existingIdx] = {
      ...existing,
      content: `${streamDeltaText(existing.content)}${content}`,
      metadata: {
        ...(existing.metadata || {}),
        ...(metadata || {}),
        stream_id: streamId,
      },
      role: role || existing.role || 'assistant',
      _streaming: true,
      _stream_id: streamId,
      _stream_key: nextStreamKey,
    };
    return next;
  }

  const now = Date.now();
  return [
    ...messages,
    normalizeIncomingMessage({
      id: `stream:${streamId}:${streamSenderKey({ fromUid, from })}:${executionKey || 'uncorrelated'}`,
      seq_id: now,
      topic_id: topic,
      from_uid: fromUid,
      from,
      content,
      type: 'text',
      msg_type: 'text',
      role: role || 'assistant',
      metadata: {
        ...(metadata || {}),
        stream_id: streamId,
      },
      created_at: new Date(now).toISOString(),
      _streaming: true,
      _stream_id: streamId,
      _stream_key: streamKey,
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
    const repliedUID = parseUid(repliedMessage?.from_uid);
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
  const metadataUID = parseUid(
    metadata.initiator_uid
    ?? metadata.requester_uid
    ?? metadata.trigger_uid,
  );
  if (metadataUID > 0 && !botUIDs.has(metadataUID)) {
    return metadataUID;
  }

  for (let index = workingIndex - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (!isFinalTextMessage(message)) continue;
    const senderUID = parseUid(message?.from_uid);
    if (senderUID <= 0) continue;
    if (botUIDs.has(senderUID) || isAssistantAuthoredMessage(message)) continue;
    return senderUID;
  }
  return 0;
}

function hasOrdinaryChatMessage(messages) {
  return (messages || []).some((message) => {
    if (!message || isWorkingMessage(message) || runtimePlanFromMessage(message)) return false;
    if (isFinalTextMessage(message)) return true;
    if (['file', 'image', 'attachment'].includes(message.type || message.msg_type || '')) return true;
    return Array.isArray(message.content_blocks) && message.content_blocks.some((block) => (
      ['text', 'file', 'image'].includes(block?.type)
      && (block.type !== 'text' || String(block.text || '').trim())
    ));
  });
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

function sameUID(left, right) {
  if (left === right) return true;
  const leftUID = parseUid(left);
  const rightUID = parseUid(right);
  return leftUID > 0 && leftUID === rightUID;
}

function mergeMessages(primary, secondary) {
  const byId = new Map();
  [...primary, ...secondary].forEach((message) => {
    byId.set(message.id, message);
  });
  return Array.from(byId.values()).sort(compareMessageSequence);
}

function createOptimisticUserMessage({
  id,
  topicId,
  userUID,
  content,
  contentBlocks,
  replyToID = 0,
  pendingAfterSeq = 0,
  clientMsgID = '',
}) {
  const message = {
    id,
    seq_id: id,
    topic_id: topicId,
    from_uid: userUID,
    content,
    type: 'text',
    msg_type: 'text',
    reply_to: replyToID,
    created_at: new Date().toISOString(),
    client_msg_id: clientMsgID,
    _pending: true,
    _pending_after_seq: pendingAfterSeq,
  };
  if (Array.isArray(contentBlocks) && contentBlocks.length > 0) {
    message.content_blocks = contentBlocks;
  }
  return message;
}

function createClientMessageID() {
  if (typeof globalThis.crypto?.randomUUID === 'function') {
    return `web-${globalThis.crypto.randomUUID()}`;
  }
  return `web-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function messageClientMsgID(message) {
  const candidates = [
    message?.client_msg_id,
    message?.clientMsgID,
    message?.clientMsgId,
    message?.metadata?.client_msg_id,
    message?.metadata?.clientMsgID,
    message?.metadata?.clientMessageId,
    message?.metadata?.client_message_id,
  ];
  for (const candidate of candidates) {
    const value = candidate == null ? '' : String(candidate).trim();
    if (value) return value;
  }
  return '';
}

function comparableContentBlocks(message) {
  const blocks = contentBlocksFromMessage(message);
  return blocks.length > 0 ? getComparableContent(blocks) : '';
}

function pendingMatchesHistoryMessage(pending, historyMessage, usedHistoryIDs) {
  const historyID = historyMessageID(historyMessage);
  const pendingAnchor = pendingMessageAnchor(pending);
  const pendingCreatedAt = Date.parse(pending?.created_at || '');
  const historyCreatedAt = Date.parse(historyMessage?.created_at || '');
  const pendingClientMsgID = messageClientMsgID(pending);
  const historyClientMsgID = messageClientMsgID(historyMessage);
  const hasStableClientMatch = Boolean(
    pendingClientMsgID
    && historyClientMsgID
    && pendingClientMsgID === historyClientMsgID,
  );
  if (
    historyID <= 0
    || historyID <= pendingAnchor
    || usedHistoryIDs.has(historyID)
    || historyMessage?._pending
    || historyMessage?._streaming
    || !sameUID(pending?.from_uid, historyMessage?.from_uid)
    || (historyClientMsgID && pendingClientMsgID && historyClientMsgID !== pendingClientMsgID)
    || (!hasStableClientMatch && getComparableContent(pending?.content) !== getComparableContent(historyMessage?.content))
    || (!hasStableClientMatch && comparableContentBlocks(pending) !== comparableContentBlocks(historyMessage))
    || (
      !hasStableClientMatch
      && Number.isFinite(pendingCreatedAt)
      && Number.isFinite(historyCreatedAt)
      && Math.abs(historyCreatedAt - pendingCreatedAt) > PENDING_HISTORY_MATCH_CLOCK_SKEW_MS
    )
  ) {
    return false;
  }

  const pendingReplyTo = Number(pending?.reply_to || 0);
  const historyReplyTo = Number(historyMessage?.reply_to || 0);
  return pendingReplyTo <= 0 || historyReplyTo <= 0 || pendingReplyTo === historyReplyTo;
}

function findHistoryMatchForPending(pending, historyMessages, usedHistoryIDs) {
  return historyMessages
    .filter((historyMessage) => pendingMatchesHistoryMessage(pending, historyMessage, usedHistoryIDs))
    .sort((left, right) => {
      const pendingClientMsgID = messageClientMsgID(pending);
      const leftExact = Number(Boolean(
        pendingClientMsgID && messageClientMsgID(left) === pendingClientMsgID,
      ));
      const rightExact = Number(Boolean(
        pendingClientMsgID && messageClientMsgID(right) === pendingClientMsgID,
      ));
      return rightExact - leftExact || historyMessageID(left) - historyMessageID(right);
    })[0] || null;
}

function historyContainsFinalForStreamingMessage(streamingMessage, historyMessages) {
  const streamingTurnKey = assistantReplyTurnKey(streamingMessage);
  const streamingSenderKey = messageSenderIdentity(streamingMessage);
  // A history snapshot has no causal ordering relative to an active stream.
  // Transport stream IDs can be reused, so only a shared execution key proves
  // that a persisted reply supersedes this placeholder.
  if (!streamingTurnKey || !streamingSenderKey) return false;
  return (historyMessages || []).some((historyMessage) => (
    isFinalTextMessage(historyMessage)
    && messageSenderIdentity(historyMessage) === streamingSenderKey
    && assistantReplyTurnKey(historyMessage) === streamingTurnKey
  ));
}

function mergeHistoryWithCurrentMessages(historyMessages, currentMessages) {
  const visibleMessages = Array.isArray(historyMessages) ? historyMessages : [];
  const current = Array.isArray(currentMessages) ? currentMessages : [];
  const historyIDs = new Set(
    visibleMessages
      .map((message) => historyMessageID(message))
      .filter((sequence) => sequence > 0),
  );
  const usedHistoryIDs = new Set();
  const pendingToKeep = [];
  const streamingToKeep = current.filter((message) => (
    message?._streaming
    && !historyContainsFinalForStreamingMessage(message, visibleMessages)
  ));

  current.forEach((pending) => {
    if (!pending?._pending) return;
    const historyMatch = findHistoryMatchForPending(pending, visibleMessages, usedHistoryIDs);
    if (historyMatch) {
      usedHistoryIDs.add(historyMessageID(historyMatch));
      return;
    }

    // Browser and server clocks cannot establish causal ordering. Until a
    // matching client message ID or acknowledgement supplies the durable
    // sequence, keep the anchor captured when the user sent the message.
    pendingToKeep.push(pending);
  });

  const pendingByID = new Map(pendingToKeep.map((pending) => [pending.id, pending]));
  const newerMessages = current.flatMap((message) => {
    if (message?._pending) {
      const pending = pendingByID.get(message.id);
      return pending ? [pending] : [];
    }
    if (message?._streaming) {
      return streamingToKeep.includes(message) ? [message] : [];
    }
    const sequence = historyMessageID(message);
    return sequence <= 0 || !historyIDs.has(sequence) ? [message] : [];
  });
  return mergeMessages(visibleMessages, newerMessages);
}

function latestPersistedMessageSequence(messages) {
  return (messages || []).reduce((latest, message) => {
    // Streaming rows use Date.now() only as a local display placeholder. They
    // do not have a durable server sequence yet, so anchoring an optimistic
    // user message after one would make every real reply look older.
    if (message?._pending || message?._streaming) return latest;
    return Math.max(latest, historyMessageID(message));
  }, 0);
}

function pendingMessageAnchor(message) {
  const anchor = Number(message?._pending_after_seq);
  return Number.isFinite(anchor) && anchor >= 0 ? anchor : 0;
}

function compareMessageSequence(left, right) {
  const leftPending = Boolean(left?._pending);
  const rightPending = Boolean(right?._pending);
  const leftSequence = historyMessageID(left);
  const rightSequence = historyMessageID(right);

  if (leftPending && !rightPending && rightSequence > 0) {
    return rightSequence <= pendingMessageAnchor(left) ? 1 : -1;
  }
  if (rightPending && !leftPending && leftSequence > 0) {
    return leftSequence <= pendingMessageAnchor(right) ? -1 : 1;
  }
  if (leftPending && rightPending) {
    const anchorDifference = pendingMessageAnchor(left) - pendingMessageAnchor(right);
    if (anchorDifference !== 0) return anchorDifference;
  }
  if (leftSequence > 0 && rightSequence > 0) {
    return leftSequence - rightSequence;
  }
  return 0;
}

export function mergeOwnServerEcho(messages, serverMessage, ownUID) {
  if (!sameUID(serverMessage?.from_uid, ownUID)) return null;
  const serverContentKey = getComparableContent(serverMessage?.content);
  const pendingIdx = messages.findIndex((message) => (
    message._pending && (
      getComparableContent(message.content) === serverContentKey
      || getComparableContent(message._canonical_content) === serverContentKey
    )
  ));
  if (pendingIdx === -1) return null;

  const next = [...messages];
  next[pendingIdx] = serverMessage;
  return next;
}

export function ImageGalleryPreview({ item, index, items, onClose, onChange, triggerRef }) {
  const dialogRef = useRef(null);
  const closeRef = useRef(null);
  const stateRef = useRef({ item, index, items, onClose, onChange, triggerRef });
  stateRef.current = { item, index, items, onClose, onChange, triggerRef };
  const hasPrevious = index > 0;
  const hasNext = index < items.length - 1;

  useEffect(() => {
    closeRef.current?.focus({ preventScroll: true });
    const handleKeyDown = (event) => {
      const state = stateRef.current;
      const currentHasPrevious = state.index > 0;
      const currentHasNext = state.index < state.items.length - 1;
      if (event.key === 'Escape') {
        event.preventDefault();
        state.onClose();
        return;
      }
      if (event.key === 'ArrowLeft' && currentHasPrevious) {
        event.preventDefault();
        state.onChange(state.index - 1);
        return;
      }
      if (event.key === 'ArrowRight' && currentHasNext) {
        event.preventDefault();
        state.onChange(state.index + 1);
        return;
      }
      if (event.key !== 'Tab') return;
      const focusable = Array.from(dialogRef.current?.querySelectorAll(
        'button:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ) || []);
      if (focusable.length === 0) {
        event.preventDefault();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const focusIsOutsideDialog = !dialogRef.current?.contains(document.activeElement);
      if (event.shiftKey && (document.activeElement === first || focusIsOutsideDialog)) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (document.activeElement === last || focusIsOutsideDialog)) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      const previousTrigger = stateRef.current.triggerRef?.current;
      if (previousTrigger?.isConnected) previousTrigger.focus({ preventScroll: true });
    };
  }, []);

  return createPortal(
    <div
      className="oc-modal-overlay oc-rich-image-preview oc-rich-image-gallery-preview"
      role="dialog"
      aria-modal="true"
      aria-label={`图片预览 ${item.payload?.name || ''}`.trim()}
      ref={dialogRef}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <button
        type="button"
        className="oc-rich-image-gallery-nav is-previous"
        aria-label="上一张图片"
        disabled={!hasPrevious}
        onClick={() => onChange(index - 1)}
      >
        <ChevronLeft size={56} strokeWidth={1.5} aria-hidden="true" />
      </button>
      <button
        ref={closeRef}
        type="button"
        aria-label="关闭图片预览"
        className="oc-rich-media-preview-close oc-rich-image-preview-close"
        onClick={onClose}
      >
        <X size={28} aria-hidden="true" />
      </button>
      <img
        src={resolveMediaURL(item.payload?.url || item.payload?.thumbnail)}
        alt={item.payload?.name ? `${item.payload.name} preview` : 'image preview'}
        className="oc-rich-image-preview-media"
        onClick={(event) => event.stopPropagation()}
      />
      <button
        type="button"
        className="oc-rich-image-gallery-nav is-next"
        aria-label="下一张图片"
        disabled={!hasNext}
        onClick={() => onChange(index + 1)}
      >
        <ChevronRight size={56} strokeWidth={1.5} aria-hidden="true" />
      </button>
    </div>,
    document.body,
  );
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
