import React, { useState, useRef, useEffect, useCallback, useId, useMemo } from 'react';
import { createPortal } from 'react-dom';
import { ArrowLeft, Check, CheckCircle2, CheckSquare, ChevronDown, ChevronLeft, ChevronRight, Circle, CircleDot, Download, FileText, Image, ImageDown, LoaderCircle, RefreshCw, Smartphone, Users, X } from 'lucide-react';
import { api, resolveMediaURL, wsSendMessage, wsSendStreamCancel, wsSendTyping, wsSendRead, wsSendArtifactResultReceipt, onWSMessage, updateTopicSeq } from '../api';
import t from '../i18n';
import ChatMessage, { createCloudArtifactPreviewFile, downloadableMediaURL, FilePreviewPanel } from '../widgets/chat-message';
import Avatar from '../widgets/avatar';
import CloudArtifactsPanel from '../widgets/cloud-artifacts-panel';
import QRCode from '../widgets/qr-code';
import { TutorialEmptyState, TutorialTaskModal, TutorialTaskPicker, TUTORIAL_TASKS } from '../widgets/tutorial-tasks';
import { attachmentFromContentBlock, attachmentIdentity, clearChatAttachmentDrag, hasChatAttachmentDrag, readChatAttachmentDrag } from '../chat-attachment-drag';
import ChatComposer from '../widgets/chat-composer';
import PwaDownloadLink from '../widgets/pwa-download-link';
import { useFeedback } from '../components/feedback-system';
import { insertTranscriptAtSelection } from '../utils/composer-transcript';
import {
  invalidateComposerDraftRevision,
  isComposerDraftRevisionCurrent,
  markComposerPhoneUploadIgnoredFileKey,
  persistComposerDraftStore as persistComposerDraftStoreValue,
  readComposerAttachmentDraft,
  readComposerDraftMutationRevision,
  readComposerDraftRevision,
  readComposerInputDraft,
  readComposerMentionDraft,
  readComposerPhoneUploadIgnoredFileKeys,
  readComposerPhoneUploadSession,
  subscribeComposerDraftStore,
  writeComposerAttachmentDraft,
  writeComposerInputDraft,
  writeComposerMentionDraft,
  writeComposerPhoneUploadSession,
} from '../utils/composer-draft-storage';
import { readStorageValue, writeStorageValue } from '../utils/storage-access';
import { IMAGE_UPLOAD_ACCEPT, MAX_ATTACHMENT_SIZE, MAX_ATTACHMENT_SIZE_MB, inferAttachmentType, validateImageUpload } from '../utils/upload-rules';
import { describeResourceLoadError, REQUEST_ERROR_CODE } from '../utils/request-error';
import {
  artifactContextRefFromSnapshot,
  artifactRefFromPreviewFile,
  artifactURLForVersion,
  normalizeArtifactResultDelivery,
  requestArtifactPageContext,
  requestArtifactResultApply,
  withArtifactContextRef,
} from '../artifact-context';
import { createArtifactTaskHost } from '../artifact-task-host';
import { createArtifactRuntimeHost } from '../artifact-runtime-host';
import {
  artifactPreviewCoordinationID,
  createArtifactPreviewChatCoordinator,
  createArtifactPreviewLeaseStore,
  createArtifactViewerURL,
  sameArtifactPreviewIdentity,
} from '../artifact-preview-coordinator';
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
const TIMELINE_BOTTOM_EPSILON = 1;
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
const CLOUD_ARTIFACTS_CHANGED_EVENT = 'cc:cloud-artifacts-changed';
const ARTIFACT_REGISTRY_POLL_MS = 5000;
const ARTIFACT_SNAPSHOT_TIMEOUT_MS = 2200;
const DELIVERY_ARTIFACT_TYPES = new Set(['file', 'image', 'audio', 'voice']);

// A cloud-worker list can briefly be served without release metadata while
// the provider snapshot refreshes. Preserve a known update in that case;
// an explicit latest release still replaces it and can clear the notice.
export function mergeCloudWorkerSnapshots(previous, next) {
  const previousByUID = new Map(
    (Array.isArray(previous) ? previous : [])
      .map((worker) => [parseUid(worker?.uid), worker])
      .filter(([uid]) => uid > 0),
  );
  return (Array.isArray(next) ? next : []).map((worker) => {
    const prior = previousByUID.get(parseUid(worker?.uid));
    if (prior?.update_available && !worker?.latest_release) {
      return {
        ...worker,
        latest_release: prior.latest_release,
        update_available: true,
      };
    }
    return worker;
  });
}

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
  composerDraftStore,
  modelInfo = null,
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
  const [verifiedArtifactRefresh, setVerifiedArtifactRefresh] = useState(null);
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
  const [cloudWorkers, setCloudWorkers] = useState([]);
  const [cloudWorkerUpdateVisible, setCloudWorkerUpdateVisible] = useState(false);
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
  const pendingOlderHistoryAnchorRef = useRef(null);
  const stickToBottomRef = useRef(true);
  const lastTimelineScrollTopRef = useRef(0);
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
  const artifactTaskHostRef = useRef(null);
  const artifactRuntimeHostRef = useRef(null);
  const artifactTaskFeedbackRef = useRef(feedback);
  const artifactPreviewCoordinatorRef = useRef(null);
  const artifactViewerHandoffRef = useRef(null);
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
  const composerDraftStoreRef = useRef(null);
  const pendingAttachmentsRef = useRef([]);
  const previewWidthRef = useRef(previewWidth);
  const phoneUploadFileKeysRef = useRef(new Set());
  const phoneUploadSessionRef = useRef(null);
  artifactTaskFeedbackRef.current = feedback;
  const phoneUploadTopicRef = useRef('');
  const phoneUploadSyncRef = useRef(null);
  const sendInFlightRef = useRef(false);
  const conversationShareGenerateButtonRef = useRef(null);
  const conversationSharePreviewRef = useRef(null);
  const conversationSharePreviewCloseRef = useRef(null);

  if (composerDraftStoreRef.current === null) {
    composerDraftStoreRef.current = composerDraftStore || {
      inputDrafts: new Map(),
      structuredMentionDrafts: new Map(),
      attachmentDrafts: new Map(),
      phoneUploadSessions: new Map(),
    };
  }

  useEffect(() => {
    const syncDraftFromStore = ({ key } = {}) => {
      if (key !== undefined && key !== topic) return;
      if (activeTopicRef.current !== topic) return;
      const nextInput = readComposerInputDraft(composerDraftStoreRef.current, topic);
      const nextAttachments = readComposerAttachmentDraft(composerDraftStoreRef.current, topic);
      const nextPhoneUploadSession = readComposerPhoneUploadSession(
        composerDraftStoreRef.current,
        topic,
      );
      if (phoneUploadSessionRef.current?.session_id !== nextPhoneUploadSession?.session_id) {
        phoneUploadFileKeysRef.current = new Set();
      }
      setInput(nextInput);
      pendingAttachmentsRef.current = nextAttachments;
      setPendingAttachments(nextAttachments);
      phoneUploadSessionRef.current = nextPhoneUploadSession;
      phoneUploadTopicRef.current = nextPhoneUploadSession ? topic : '';
      setPhoneUploadSession(nextPhoneUploadSession);
    };

    syncDraftFromStore();
    return subscribeComposerDraftStore(composerDraftStoreRef.current, syncDraftFromStore);
  }, [composerDraftStore, topic]);

  if (artifactTopicRef.current !== topic) {
    artifactTopicRef.current = topic;
    artifactTopicGenerationRef.current += 1;
    activeArtifactFocusRef.current = null;
    activeArtifactFrameRef.current = null;
  }

  useEffect(() => {
    const leaseStore = createArtifactPreviewLeaseStore();
    const coordinator = createArtifactPreviewChatCoordinator({
      recoveryLease: leaseStore.read(),
      onViewerLeaseChange: (lease) => {
        if (lease) leaseStore.write(lease);
        else leaseStore.clear();
      },
    });
    artifactPreviewCoordinatorRef.current = coordinator;
    return () => {
      const pending = artifactViewerHandoffRef.current;
      artifactViewerHandoffRef.current = null;
      pending?.control?.cancel();
      coordinator?.close();
      if (artifactPreviewCoordinatorRef.current === coordinator) {
        artifactPreviewCoordinatorRef.current = null;
      }
    };
  }, []);

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

  // Cloud-worker release state is owner-scoped. Filter it again by the active
  // conversation bot before rendering a notice so other bots stay untouched.
  useEffect(() => {
    let cancelled = false;
    const loadCloudWorkers = async () => {
      if (!api.getCloudWorkers) return;
      try {
        const response = await api.getCloudWorkers({ timeoutMs: 15_000 });
        if (!cancelled && Array.isArray(response?.workers)) {
          setCloudWorkers((previous) => mergeCloudWorkerSnapshots(previous, response?.workers));
        }
      } catch {
        // Keep the last successful snapshot so a transient refresh failure
        // cannot make the active conversation's update notice disappear.
      }
    };
    loadCloudWorkers();
    const refresh = () => loadCloudWorkers();
    window.addEventListener('cc:data-changed', refresh);
    return () => {
      cancelled = true;
      window.removeEventListener('cc:data-changed', refresh);
    };
  }, [topic]);

  const persistComposerDraftStore = useCallback(() => {
    persistComposerDraftStoreValue(composerDraftStoreRef.current);
  }, []);

  const updateComposerDraft = useCallback((draftTopic, value) => {
    if (!draftTopic) return;
    writeComposerInputDraft(composerDraftStoreRef.current, draftTopic, value);
    persistComposerDraftStore();
  }, [persistComposerDraftStore]);

  const updateStructuredMentionDraft = useCallback((draftTopic, selections) => {
    if (!draftTopic) return;
    writeComposerMentionDraft(composerDraftStoreRef.current, draftTopic, selections);
    persistComposerDraftStore();
  }, [persistComposerDraftStore]);

  const updateAttachmentDraft = useCallback((draftTopic, nextValue, expectedRevision) => {
    if (!draftTopic) return [];
    if (!isComposerDraftRevisionCurrent(
      composerDraftStoreRef.current,
      draftTopic,
      expectedRevision,
    )) return null;
    const current = readComposerAttachmentDraft(composerDraftStoreRef.current, draftTopic);
    const next = typeof nextValue === 'function' ? nextValue(current) : nextValue;
    const normalized = Array.isArray(next) ? next : [];
    writeComposerAttachmentDraft(composerDraftStoreRef.current, draftTopic, normalized);
    if (activeTopicRef.current === draftTopic) {
      pendingAttachmentsRef.current = normalized;
      setPendingAttachments(normalized);
    }
    persistComposerDraftStore();
    return normalized;
  }, [persistComposerDraftStore]);

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
    artifactTaskHostRef.current?.deactivate();
    artifactRuntimeHostRef.current?.deactivate();
  }, [invalidateArtifactSnapshot]);

  const cancelArtifactViewerHandoff = useCallback(({ closeWindow = true } = {}) => {
    const pending = artifactViewerHandoffRef.current;
    if (!pending) return;
    artifactViewerHandoffRef.current = null;
    pending.control?.cancel({ release: true });
    if (closeWindow) {
      try {
        if (pending.openedWindow && !pending.openedWindow.closed) pending.openedWindow.close();
      } catch {
        // The browser may have detached the opened tab before cancellation.
      }
    }
  }, []);

  const claimArtifactSidebarControl = useCallback(() => {
    cancelArtifactViewerHandoff();
    artifactPreviewCoordinatorRef.current?.claimSidebar();
  }, [cancelArtifactViewerHandoff]);

  const setPreviewFileWithFocus = useCallback((file) => {
    if (artifactRefFromPreviewFile(file, Number(file?.artifact_agent_uid || 0))) {
      claimArtifactSidebarControl();
    }
    invalidateArtifactSnapshot();
    artifactTaskHostRef.current?.deactivate();
    artifactRuntimeHostRef.current?.deactivate();
    activeArtifactFocusRef.current = artifactMessageFocusFromPreviewFile(
      file,
      artifactTopicRef.current,
      artifactTopicGenerationRef.current,
    );
    activeArtifactFrameRef.current = null;
    setPreviewFile(file);
  }, [claimArtifactSidebarControl, invalidateArtifactSnapshot]);

  const handleRemoteArtifactFrameChange = useCallback((binding) => {
    const activeBinding = artifactBindingMatchesFocus(
      binding,
      activeArtifactFocusRef.current,
    ) ? binding : null;
    activeArtifactFrameRef.current = activeBinding;
    if (!activeBinding) {
      artifactTaskHostRef.current?.deactivate();
      artifactRuntimeHostRef.current?.deactivate();
      return;
    }
    artifactRuntimeHostRef.current?.resume();
    artifactTaskHostRef.current?.connect(activeBinding);
  }, []);

  useEffect(() => {
    const getCurrentSession = () => {
      const focus = activeArtifactFocusRef.current;
      const binding = activeArtifactFrameRef.current;
      if (!focus || !binding || activeTopicRef.current !== focus.topic
        || artifactTopicGenerationRef.current !== focus.topicGeneration
        || activeArtifactAgentUIDRef.current !== focus.agentUid
        || !artifactBindingMatchesFocus(binding, focus)) return null;
      return {
        token: focus,
        identityKey: focus.previewKey,
        topicId: focus.topic,
        topicGeneration: focus.topicGeneration,
        agentUid: focus.agentUid,
        artifactId: focus.artifactId,
        displayedVersion: focus.displayedVersion,
        artifactRef: focus.artifactRef,
        binding,
      };
    };
    const host = createArtifactTaskHost({
      getCurrentSession,
      confirmTask: () => artifactTaskFeedbackRef.current.confirm({
        title: '发送给虚拟员工？',
        message: '该应用希望把你刚才的操作作为一条新消息交给当前虚拟员工处理。',
        confirmLabel: '确认发送',
        cancelLabel: '取消',
      }),
    });
    const runtimeHost = createArtifactRuntimeHost({ getCurrentSession });
    artifactTaskHostRef.current = host;
    artifactRuntimeHostRef.current = runtimeHost;
    host.connect(activeArtifactFrameRef.current);
    window.addEventListener('message', host.handleWindowMessage);
    window.addEventListener('message', runtimeHost.handleWindowMessage);
    return () => {
      window.removeEventListener('message', host.handleWindowMessage);
      window.removeEventListener('message', runtimeHost.handleWindowMessage);
      host.dispose();
      runtimeHost.dispose();
      if (artifactTaskHostRef.current === host) artifactTaskHostRef.current = null;
      if (artifactRuntimeHostRef.current === runtimeHost) artifactRuntimeHostRef.current = null;
    };
  }, []);

  const openFilePreview = useCallback((file) => {
    setCloudArtifactsAgentUID(0);
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(false);
    setPendingArtifactRefresh(null);
    setPreviewFileWithFocus(file);
  }, [setPreviewFileWithFocus]);

  const closeSidePanel = useCallback(() => {
    cancelArtifactViewerHandoff();
    setPendingArtifactRefresh(null);
    clearActiveArtifactFocus();
    setPreviewFile(null);
    setCloudArtifactsAgentUID(0);
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(false);
    setCloudArtifactsTab('files');
  }, [cancelArtifactViewerHandoff, clearActiveArtifactFocus]);

  const openRemoteArtifactFullscreen = useCallback(async (file) => {
    const focus = artifactMessageFocusFromPreviewFile(
      file,
      artifactTopicRef.current,
      artifactTopicGenerationRef.current,
    );
    const coordinator = artifactPreviewCoordinatorRef.current;
    if (!focus || !coordinator) {
      feedback.notify({ tone: 'warning', message: '当前浏览器暂时无法打开应用新标签页。' });
      return;
    }
    cancelArtifactViewerHandoff();
    const identity = {
      topicId: focus.topic,
      agentUid: focus.agentUid,
      artifactId: focus.artifactId,
      displayedVersion: focus.displayedVersion,
    };
    const handoffId = artifactPreviewCoordinationID('handoff');
    const viewerURL = createArtifactViewerURL(identity, { handoffId });
    const control = coordinator.beginHandoff(identity, handoffId);
    if (!viewerURL || !control) {
      control?.cancel();
      feedback.notify({ tone: 'warning', message: '当前应用无法建立新标签页连接。' });
      return;
    }

    let openedWindow = null;
    try {
      openedWindow = window.open(viewerURL, '_blank');
    } catch {
      openedWindow = null;
    }
    if (!openedWindow) {
      control.cancel();
      feedback.notify({ tone: 'warning', message: '新标签页被浏览器拦截，右侧应用仍可继续使用。' });
      return;
    }
    try {
      openedWindow.opener = null;
      openedWindow.focus?.();
    } catch {
      // The handoff uses BroadcastChannel and does not depend on window.opener.
    }

    const pending = { control, focus, identity, openedWindow };
    artifactViewerHandoffRef.current = pending;
    const viewer = await control.promise;
    if (artifactViewerHandoffRef.current !== pending) return;
    artifactViewerHandoffRef.current = null;

    const currentFocus = activeArtifactFocusRef.current;
    const stillOwnsSidebar = Boolean(currentFocus
      && currentFocus.topic === identity.topicId
      && currentFocus.topic === artifactTopicRef.current
      && currentFocus.topicGeneration === artifactTopicGenerationRef.current
      && currentFocus.agentUid === identity.agentUid
      && currentFocus.artifactId === identity.artifactId
      && currentFocus.displayedVersion === identity.displayedVersion);
    if (!viewer || !stillOwnsSidebar || !sameArtifactPreviewIdentity(viewer, identity)) {
      coordinator.claimSidebar();
      try {
        if (!openedWindow.closed) openedWindow.close();
      } catch {
        // A detached tab will stop heartbeating and expire naturally.
      }
      if (stillOwnsSidebar) {
        feedback.notify({ tone: 'warning', message: '新标签页未能接管应用，右侧应用仍可继续使用。' });
      }
      return;
    }

    setPendingArtifactRefresh(null);
    clearActiveArtifactFocus();
    setPreviewFile(null);
    setCloudArtifactsAgentUID(0);
    setCloudArtifactsListOpen(false);
    setCloudArtifactsReturnOpen(false);
    setCloudArtifactsTab('files');
  }, [cancelArtifactViewerHandoff, clearActiveArtifactFocus, feedback]);

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
    const localFocusValid = Boolean(focus
      && focus.topic === topic
      && focus.topicGeneration === topicGeneration
      && artifactTopicRef.current === topic
      && activeTopicRef.current === topic
      && focus.agentUid === activeArtifactAgentUIDRef.current);

    if (localFocusValid) {
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
    }

    const coordinator = artifactPreviewCoordinatorRef.current;
    const viewer = coordinator?.getActiveViewer();
    if (!viewer
      || viewer.topicId !== topic
      || viewer.agentUid !== activeArtifactAgentUIDRef.current
      || artifactTopicRef.current !== topic
      || activeTopicRef.current !== topic) return empty;
    const contextRef = await coordinator.requestContext(viewer);
    if (!contextRef) return empty;
    const currentViewer = coordinator.getActiveViewer(viewer);
    if (!currentViewer
      || currentViewer.viewerId !== viewer.viewerId
      || !sameArtifactPreviewIdentity(currentViewer, viewer)
      || artifactTopicRef.current !== topic
      || artifactTopicGenerationRef.current !== topicGeneration
      || activeTopicRef.current !== topic
      || activeArtifactAgentUIDRef.current !== viewer.agentUid) {
      invalidateArtifactSnapshot({ contextRef });
      return empty;
    }
    return { contextRef };
  }, [invalidateArtifactSnapshot, topic]);

  const previewAgentFile = useCallback((file) => {
    setPendingArtifactRefresh(null);
    setPreviewFileWithFocus({
      type: file.type,
      name: file.name,
      url: file.url,
      file_key: file.file_key,
      thumbnail: file.thumbnail,
      width: file.width,
      height: file.height,
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
    setInput(readComposerInputDraft(composerDraftStoreRef.current, topic));
    const cacheKey = historyCacheKey(user.uid, topic);
    const cachedHistory = historyCacheRef.current.get(cacheKey);
    const cachedQuestionIndex = questionIndexCacheRef.current.get(cacheKey);
    setMessages(cachedHistory?.messages || []);
    setQuestionIndexItems(cachedQuestionIndex?.items || []);
    setQuestionIndexHasMore(Boolean(cachedQuestionIndex?.hasMore));
    setQuestionIndexLimitReached(Boolean(cachedQuestionIndex?.limitReached));
    setQuestionIndexLoading(false);
    questionIndexLoadingRef.current = false;
    const attachmentDraft = readComposerAttachmentDraft(composerDraftStoreRef.current, topic);
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
    pendingOlderHistoryAnchorRef.current = null;
    loadingOlderRef.current = false;
    questionIndexRequestRef.current += 1;
    stickToBottomRef.current = true;
    lastTimelineScrollTopRef.current = 0;
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
    const restoredPhoneUploadSession = readComposerPhoneUploadSession(
      composerDraftStoreRef.current,
      topic,
    );
    setPhoneUploadSession(restoredPhoneUploadSession);
    setPhoneUploadError('');
    phoneUploadSessionRef.current = restoredPhoneUploadSession;
    phoneUploadTopicRef.current = restoredPhoneUploadSession ? topic : '';
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
      const peer = agentPeer ? { ...friendPeer, ...agentPeer } : friendPeer;
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

  const handleArtifactResultRequest = useCallback(async (value) => {
    const delivery = normalizeArtifactResultDelivery(value);
    if (!delivery) return;
    if (delivery.taskId) {
      await artifactTaskHostRef.current?.handleResultDelivery(value);
      return;
    }

    const snapshot = activeArtifactSnapshotRef.current;
    const focus = activeArtifactFocusRef.current;
    const binding = activeArtifactFrameRef.current;
    if (!snapshot || snapshot.contextRef !== delivery.contextRef
      || snapshot.topic !== delivery.topicId
      || snapshot.topicGeneration !== artifactTopicGenerationRef.current
      || snapshot.agentUid !== delivery.agentUid
      || snapshot.artifactId !== delivery.artifactId
      || !focus || focus.topic !== delivery.topicId
      || focus.topicGeneration !== artifactTopicGenerationRef.current
      || focus.agentUid !== delivery.agentUid
      || focus.artifactId !== delivery.artifactId
      || focus.displayedVersion !== delivery.displayedVersion
      || activeTopicRef.current !== delivery.topicId
      || activeArtifactAgentUIDRef.current !== delivery.agentUid
      || !artifactBindingMatchesFocus(binding, focus)) return;

    const receipt = await requestArtifactResultApply(binding, delivery);
    if (!receipt) return;
    if (activeArtifactSnapshotRef.current !== snapshot
      || activeArtifactFocusRef.current !== focus
      || activeArtifactFrameRef.current !== binding
      || artifactTopicGenerationRef.current !== snapshot.topicGeneration
      || activeTopicRef.current !== delivery.topicId
      || activeArtifactAgentUIDRef.current !== delivery.agentUid
      || !artifactBindingMatchesFocus(activeArtifactFrameRef.current, focus)) return;
    wsSendArtifactResultReceipt({
      type: 'receipt',
      origin_node_id: delivery.originNodeId,
      ...(delivery.contextRef ? { context_ref: delivery.contextRef } : {}),
      ...(delivery.taskId ? { task_id: delivery.taskId } : {}),
      writeback_ref: delivery.writebackRef,
      topic_id: delivery.topicId,
      agent_uid: String(delivery.agentUid),
      artifact_id: delivery.artifactId,
      displayed_version: delivery.displayedVersion,
      result_id: delivery.resultId,
      receipt,
    });
  }, []);

  // Listen for incoming WebSocket messages
  useEffect(() => {
    const unsub = onWSMessage((msg) => {
      if (msg.artifact_result) {
        void handleArtifactResultRequest(msg.artifact_result);
      }
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
          if (sameUID(fromUid, user.uid)) {
            const mergedEcho = mergeOwnServerEcho(prev, serverMsg, user.uid);
            if (mergedEcho) return mergedEcho;
          }
          return mergeMessages(prev, [serverMsg]);
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
  }, [clearLiveWorking, groupId, handleArtifactResultRequest, isGroup, markLiveWorking, topic, user.uid]);

  // Restore an older-history anchor, or follow updates while the reader
  // remains at the latest position.
  React.useLayoutEffect(() => {
    const timeline = timelineRef.current;
    if (!timeline) return;

    if (previousScrollRef.current) {
      restoreTimelineReadingAnchor(timeline, previousScrollRef.current);
      previousScrollRef.current = null;
      lastTimelineScrollTopRef.current = timeline.scrollTop;
    } else if (stickToBottomRef.current) {
      timeline.scrollTop = timeline.scrollHeight;
      lastTimelineScrollTopRef.current = timeline.scrollTop;
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
    const cachedHistory = !aroundId ? historyCacheRef.current.get(cacheKey) : null;
    const hasCachedHistory = Boolean(cachedHistory);
    historyLoadingRef.current = true;
    previousScrollRef.current = null;
    pendingOlderHistoryAnchorRef.current = null;
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
        loadedAt: Date.now(),
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
        if (e?.code !== REQUEST_ERROR_CODE.ABORTED) {
          setHistoryError(describeResourceLoadError(e, '聊天记录', {
            hasPreviousResult: Boolean(cachedHistory),
            loadedAt: cachedHistory?.loadedAt,
          }));
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
    const cacheKey = historyCacheKey(user.uid, targetTopic);
    const cachedHistory = historyCacheRef.current.get(cacheKey);
    const controller = new AbortController();
    olderHistoryAbortControllerRef.current = controller;
    pendingOlderHistoryAnchorRef.current = captureTimelineReadingAnchor(timelineRef.current);

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
      previousScrollRef.current = stickToBottomRef.current
        ? null
        : pendingOlderHistoryAnchorRef.current;
      pendingOlderHistoryAnchorRef.current = null;
      setMessages((prev) => mergeMessages(visibleMessages, prev));
      historyOffsetRef.current += rawMessages.length;
      historyBeforeIDRef.current = Number(res.next_before_id) || oldestHistoryMessageID(rawMessages);
      const hasMore = typeof res.has_more === 'boolean'
        ? res.has_more
        : rawMessages.length === PAGE_SIZE;
      hasMoreHistoryRef.current = hasMore;
      setHasMoreHistory(hasMore);
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
        if (e?.code !== REQUEST_ERROR_CODE.ABORTED) {
          setOlderHistoryError(describeResourceLoadError(e, '更早的聊天记录', {
            hasPreviousResult: true,
            loadedAt: cachedHistory?.loadedAt,
          }));
        }
        pendingOlderHistoryAnchorRef.current = null;
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

    if (final && phoneUploadSyncRef.current) {
      const inFlightOperation = phoneUploadSyncRef.current;
      try {
        await inFlightOperation;
      } catch {
        // The fresh final read below gets one more chance to collect the latest files.
      }
      if (phoneUploadSyncRef.current === inFlightOperation) {
        phoneUploadSyncRef.current = null;
      }
    }

    let operation = phoneUploadSyncRef.current;
    if (!operation) {
      const draftRevision = readComposerDraftRevision(composerDraftStoreRef.current, sessionTopic);
      operation = (async () => {
        const data = await api.getMobileUploadSession(sessionId);
        if (
          phoneUploadSessionRef.current?.session_id !== sessionId
          || phoneUploadTopicRef.current !== sessionTopic
          || activeTopicRef.current !== sessionTopic
        ) {
          return [];
        }
        if (!isComposerDraftRevisionCurrent(
          composerDraftStoreRef.current,
          sessionTopic,
          draftRevision,
        )) return [];
        if (data?.topic && data.topic !== sessionTopic) {
          throw new Error('手机上传会话与当前对话不匹配，请重新打开二维码。');
        }

        const nextAttachments = [];
        const ignoredFileKeys = new Set(
          readComposerPhoneUploadIgnoredFileKeys(composerDraftStoreRef.current, sessionTopic),
        );
        const existingAttachmentKeys = new Set(
          readComposerAttachmentDraft(composerDraftStoreRef.current, sessionTopic)
            .map(attachmentFileKey)
            .filter(Boolean),
        );
        const nextAttachmentKeys = [];
        for (const file of Array.isArray(data?.files) ? data.files : []) {
          const fileKey = file.file_key || file.url || file.name;
          if (
            !fileKey
            || ignoredFileKeys.has(fileKey)
            || phoneUploadFileKeysRef.current.has(fileKey)
            || existingAttachmentKeys.has(fileKey)
          ) continue;
          existingAttachmentKeys.add(fileKey);
          nextAttachmentKeys.push(fileKey);
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
          const updated = updateAttachmentDraft(
            sessionTopic,
            (current) => [...current, ...nextAttachments],
            draftRevision,
          );
          if (updated && activeTopicRef.current === sessionTopic) {
            setAttachmentStatus({ tone: 'success', message: `手机已上传 ${updated.length} 个附件，发送后对方可见。` });
          }
          if (!updated) return [];
          nextAttachmentKeys.forEach((fileKey) => phoneUploadFileKeysRef.current.add(fileKey));
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
      if (
        /session not found|not found|expired/i.test(String(error?.message || ''))
        && phoneUploadSessionRef.current?.session_id === sessionId
      ) {
        writeComposerPhoneUploadSession(composerDraftStoreRef.current, sessionTopic, null);
        persistComposerDraftStore();
        if (
          activeTopicRef.current === sessionTopic
          && phoneUploadSessionRef.current?.session_id === sessionId
        ) {
          phoneUploadSessionRef.current = null;
          phoneUploadTopicRef.current = '';
          setPhoneUploadSession(null);
        }
      }
      if (final) throw error;
      return [];
    } finally {
      if (phoneUploadSyncRef.current === operation) phoneUploadSyncRef.current = null;
    }
  }, [persistComposerDraftStore, updateAttachmentDraft]);

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
    const storedAttachments = readComposerAttachmentDraft(composerDraftStoreRef.current, topic);
    const initialAttachments = storedAttachments.length > 0
      ? storedAttachments
      : pendingAttachmentsRef.current;
    const storedPhoneUploadSession = readComposerPhoneUploadSession(
      composerDraftStoreRef.current,
      topic,
    );
    if (
      !initialText
      && initialAttachments.length === 0
      && !storedPhoneUploadSession?.session_id
      && !phoneUploadSessionRef.current?.session_id
    ) return;
    if (isUploadingAttachment || sendInFlightRef.current) return;

    sendInFlightRef.current = true;
    setIsSendingMessage(true);
    setAwaitingAgentReply(Boolean(selectedAgent));
    setAttachmentMenuOpen(false);

    let sendTopic = topic;
    let topicToActivate = null;
    let switchesTopic = false;
    let stateCleared = false;
    let sendClearMutationRevision = null;
    let messageSent = false;
    let optimisticMessageAdded = false;
    let attachmentsToSend = [...initialAttachments];
    const text = initialText;
    const originalReplyTo = replyTo;
    const originalStructuredMentions = readComposerMentionDraft(composerDraftStoreRef.current, topic);
    const originalPhoneUploadSession = readComposerPhoneUploadSession(
      composerDraftStoreRef.current,
      topic,
    );
    const protocolText = isGroup
      ? canonicalizeStructuredMentionText(originalInput, originalStructuredMentions).trim()
      : text;
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
      attachmentsToSend = [...readComposerAttachmentDraft(composerDraftStoreRef.current, topic)];
      if (!text && attachmentsToSend.length === 0) {
        setAwaitingAgentReply(false);
        return;
      }
      const snapshotMutationRevision = readComposerDraftMutationRevision(
        composerDraftStoreRef.current,
        topic,
      );

      // Phone polling above is part of message preparation. Invalidate the
      // callback generation only after its final read so a file that arrived
      // while Send was starting is still included in this message.
      invalidateComposerDraftRevision(composerDraftStoreRef.current, topic);

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

      // If a newer composer wrote text/mentions while this send was being
      // prepared, leave that draft alone. Attachment polling above is part of
      // the message preparation, so its writes are represented by the
      // snapshot revision and do not count as a newer draft here.
      const currentInputDraft = readComposerInputDraft(composerDraftStoreRef.current, topic);
      const currentMentionDraft = readComposerMentionDraft(composerDraftStoreRef.current, topic);
      const draftChangedBeforeClear = (
        currentInputDraft !== originalInput
        || JSON.stringify(currentMentionDraft) !== JSON.stringify(originalStructuredMentions)
        || readComposerDraftMutationRevision(composerDraftStoreRef.current, topic)
          !== snapshotMutationRevision
      );
      if (!draftChangedBeforeClear) {
        // Close the generation again immediately before clearing. This also
        // rejects callbacks that started after the send began but still belong
        // to the draft being consumed.
        invalidateComposerDraftRevision(composerDraftStoreRef.current, topic);
        updateComposerDraft(topic, '');
        updateStructuredMentionDraft(topic, []);
        updateAttachmentDraft(topic, []);
        writeComposerPhoneUploadSession(composerDraftStoreRef.current, topic, null);
        persistComposerDraftStore();
        sendClearMutationRevision = readComposerDraftMutationRevision(
          composerDraftStoreRef.current,
          topic,
        );
        stateCleared = true;
      }
      if (activeTopicRef.current === topic) {
        if (stateCleared) {
          clearRuntimePlan();
          setAttachmentStatus(null);
          setInput('');
          setReplyTo(null);
          phoneUploadSessionRef.current = null;
          phoneUploadTopicRef.current = '';
          setPhoneUploadSession(null);
        }
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
          _canonical_content: protocolText,
        }]));
      }

      // Snapshot the payload mutation counter after preparation and before the
      // request. Finish consuming the same payload after success only when
      // nothing touched the draft in flight; the counter treats an identical
      // re-typed draft as newer and keeps it intact.
      const sendStartMutationRevision = readComposerDraftMutationRevision(
        composerDraftStoreRef.current,
        topic,
      );
      const result = mentions.length > 0
        ? await api.sendMessage(sendTopic, sendPayload, currentReplyTo ? currentReplyTo.id : undefined, mentions)
        : await api.sendMessage(sendTopic, sendPayload, currentReplyTo ? currentReplyTo.id : undefined);
      messageSent = true;
      const inputStillMatches = readComposerInputDraft(
        composerDraftStoreRef.current,
        topic,
      ) === originalInput;
      const attachmentsStillMatch = JSON.stringify(readComposerAttachmentDraft(
        composerDraftStoreRef.current,
        topic,
      )) === JSON.stringify(attachmentsToSend);
      if (
        !stateCleared
        && inputStillMatches
        && attachmentsStillMatch
        // The payload counter also moves on mention-draft writes, which the
        // checks above do not compare. Mid-send mention writes only happen
        // alongside an input-draft rewrite (edit-resend, tutorial prompts),
        // where skipping this clear is correct anyway, so an unchanged
        // counter reliably means "same payload, no newer draft".
        && readComposerDraftMutationRevision(composerDraftStoreRef.current, topic)
          === sendStartMutationRevision
      ) {
        invalidateComposerDraftRevision(composerDraftStoreRef.current, topic);
        updateComposerDraft(topic, '');
        updateStructuredMentionDraft(topic, []);
        updateAttachmentDraft(topic, []);
        writeComposerPhoneUploadSession(composerDraftStoreRef.current, topic, null);
        persistComposerDraftStore();
        stateCleared = true;
        if (activeTopicRef.current === topic) {
          setInput('');
          setPendingAttachments([]);
          phoneUploadSessionRef.current = null;
          phoneUploadTopicRef.current = '';
          setPhoneUploadSession(null);
        }
      }
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
      const draftHasChangesAfterClear = (
        readComposerInputDraft(composerDraftStoreRef.current, topic) !== ''
        || readComposerMentionDraft(composerDraftStoreRef.current, topic).length > 0
        || readComposerAttachmentDraft(composerDraftStoreRef.current, topic).length > 0
      );
      if (
        stateCleared
        && !draftHasChangesAfterClear
        && readComposerDraftMutationRevision(composerDraftStoreRef.current, topic)
          === sendClearMutationRevision
      ) {
        updateComposerDraft(topic, originalInput);
        updateStructuredMentionDraft(topic, originalStructuredMentions);
        updateAttachmentDraft(topic, attachmentsToSend);
        writeComposerPhoneUploadSession(
          composerDraftStoreRef.current,
          topic,
          originalPhoneUploadSession,
        );
        persistComposerDraftStore();
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
  }, [captureArtifactMessageContext, clearRuntimePlan, finalizeOptimisticMessage, input, isGroup, isUploadingAttachment, onActivateTopic, onResolveAgentTopic, persistComposerDraftStore, removeOptimisticMessage, replyTo, selectedAgent, syncPhoneUploads, topic, updateAttachmentDraft, updateComposerDraft, updateStructuredMentionDraft, user.uid]);

  const handleStopGeneration = useCallback(async () => {
    if (!canStopActiveBotWorking || isStopRequested) return;
    setIsStopRequested(true);
    try {
      await wsSendStreamCancel(topic, workingState.responderUid);
      if (activeTopicRef.current !== topic) return;
      setSuppressedWorkingKey(workingState.key);
      clearRuntimePlan();
      clearLiveWorking();
      clearTimeout(peerTypingTimer.current);
      setPeerTyping(false);
      setAwaitingAgentReply(false);
      setIsStopRequested(false);
    } catch (err) {
      if (activeTopicRef.current !== topic) return;
      setIsStopRequested(false);
      setAttachmentStatus({ tone: 'error', message: err?.message || '停止请求失败，请重试。' });
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
      const artifactContext = await captureArtifactMessageContext();
      const sendPayload = withArtifactContextRef(taskText, artifactContext.contextRef);
      const result = await api.sendMessage(topic, sendPayload, undefined);
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
      readComposerMentionDraft(composerDraftStoreRef.current, topic),
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
      readComposerMentionDraft(composerDraftStoreRef.current, topic),
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
      readComposerMentionDraft(composerDraftStoreRef.current, topic),
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
      readComposerMentionDraft(composerDraftStoreRef.current, topic),
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

  const uploadAttachmentFile = async (
    file,
    requestedType,
    uploadTopic = activeTopicRef.current,
    uploadRevision = readComposerDraftRevision(composerDraftStoreRef.current, uploadTopic),
  ) => {
    if (!isComposerDraftRevisionCurrent(
      composerDraftStoreRef.current,
      uploadTopic,
      uploadRevision,
    )) return null;
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
      const updated = updateAttachmentDraft(
        uploadTopic,
        (current) => [...current, attachment],
        uploadRevision,
      );
      if (!updated) return null;
      if (activeTopicRef.current === uploadTopic) {
        setAttachmentStatus({ tone: 'success', message: `已添加${type === 'image' ? '图片' : '文件'}：${data.name}` });
        setTimeout(() => textareaRef.current?.focus(), 0);
      }
      return attachment;
    } catch (err) {
      if (
        activeTopicRef.current === uploadTopic
        && isComposerDraftRevisionCurrent(
          composerDraftStoreRef.current,
          uploadTopic,
          uploadRevision,
        )
      ) {
        setAttachmentStatus({ tone: 'error', message: formatUploadError(err) });
      }
      return null;
    }
  };

  const uploadAttachmentFiles = async (files, requestedType, expectedRevision) => {
    const fileList = Array.from(files || []).filter(Boolean);
    if (fileList.length === 0 || sendInFlightRef.current) return;
    const uploadTopic = activeTopicRef.current;
    const uploadRevision = expectedRevision ?? readComposerDraftRevision(
      composerDraftStoreRef.current,
      uploadTopic,
    );
    if (!isComposerDraftRevisionCurrent(
      composerDraftStoreRef.current,
      uploadTopic,
      uploadRevision,
    )) return;
    let uploadedCount = 0;
    let failedCount = 0;
    setIsUploadingAttachment(true);
    try {
      for (const file of fileList.slice(0, MAX_DROPPED_FILES)) {
        const uploaded = await uploadAttachmentFile(file, requestedType, uploadTopic, uploadRevision);
        if (uploaded) {
          uploadedCount += 1;
        } else {
          if (!isComposerDraftRevisionCurrent(
            composerDraftStoreRef.current,
            uploadTopic,
            uploadRevision,
          )) return;
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
    const existingSession = readComposerPhoneUploadSession(
      composerDraftStoreRef.current,
      sessionTopic,
    );
    if (existingSession?.session_id) {
      phoneUploadSessionRef.current = existingSession;
      phoneUploadTopicRef.current = sessionTopic;
      setPhoneUploadSession(existingSession);
      return;
    }
    const draftRevision = readComposerDraftRevision(composerDraftStoreRef.current, sessionTopic);
    try {
      const session = await api.createMobileUploadSession(sessionTopic);
      if (
        !isComposerDraftRevisionCurrent(
          composerDraftStoreRef.current,
          sessionTopic,
          draftRevision,
        )
      ) return;
      writeComposerPhoneUploadSession(composerDraftStoreRef.current, sessionTopic, session);
      persistComposerDraftStore();
      if (activeTopicRef.current === sessionTopic) {
        phoneUploadSessionRef.current = session;
        phoneUploadTopicRef.current = sessionTopic;
        setPhoneUploadSession(session);
      }
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

    const dropTopic = activeTopicRef.current;
    const dropRevision = readComposerDraftRevision(composerDraftStoreRef.current, dropTopic);
    const files = await collectDroppedFiles(e.dataTransfer);
    if (
      activeTopicRef.current !== dropTopic
      || !isComposerDraftRevisionCurrent(
        composerDraftStoreRef.current,
        dropTopic,
        dropRevision,
      )
    ) return;
    if (files.length === 0) {
      setAttachmentStatus({ tone: 'error', message: '这次拖入没有识别到可上传的文件。' });
      return;
    }

    await uploadAttachmentFiles(files, undefined, dropRevision);
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
    const pasteRevision = readComposerDraftRevision(composerDraftStoreRef.current, pasteTopic);
    const pasteMutationRevision = readComposerDraftMutationRevision(
      composerDraftStoreRef.current,
      pasteTopic,
    );
    const textarea = e.currentTarget;
    const selectionStart = Number.isInteger(textarea?.selectionStart) ? textarea.selectionStart : input.length;
    const selectionEnd = Number.isInteger(textarea?.selectionEnd) ? textarea.selectionEnd : selectionStart;
    const documentFile = createPastedTextDocument(pastedText);

    e.preventDefault();
    e.stopPropagation();
    setIsUploadingAttachment(true);
    let uploaded = null;
    try {
      uploaded = await uploadAttachmentFile(documentFile, 'file', pasteTopic, pasteRevision);
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

    // A failed upload and an intentionally ignored stale upload both return
    // null. Do not restore the old clipboard text after a newer draft has
    // already been sent or replaced.
    if (!isComposerDraftRevisionCurrent(
      composerDraftStoreRef.current,
      pasteTopic,
      pasteRevision,
    ) || readComposerDraftMutationRevision(
      composerDraftStoreRef.current,
      pasteTopic,
    ) !== pasteMutationRevision) return;

    const currentText = pasteTopic === activeTopicRef.current
      ? (textareaRef.current?.value ?? input)
      : readComposerInputDraft(composerDraftStoreRef.current, pasteTopic);
    const start = Math.min(Math.max(selectionStart, 0), currentText.length);
    const end = Math.min(Math.max(selectionEnd, start), currentText.length);
    const restoredText = `${currentText.slice(0, start)}${pastedText}${currentText.slice(end)}`;
    const restoredMentions = reconcileStructuredMentionSelections(
      currentText,
      restoredText,
      readComposerMentionDraft(composerDraftStoreRef.current, pasteTopic),
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
  const conversationBotUID = isGroup ? taskBotUID : peerUID;
  const cloudWorkerUpdate = useMemo(() => {
    if (!conversationBotUID) return null;
    const worker = cloudWorkers.find((candidate) => sameUID(candidate?.uid, conversationBotUID));
    if (!worker || !worker.update_available || !worker.latest_release) return null;
    return worker;
  }, [cloudWorkers, conversationBotUID]);
  const cloudWorkerUpdateKey = cloudWorkerUpdate
    ? `${cloudWorkerUpdate.uid}:${cloudWorkerUpdate.latest_release}`
    : '';

  useEffect(() => {
    if (!cloudWorkerUpdateKey) {
      setCloudWorkerUpdateVisible(false);
      return undefined;
    }
    setCloudWorkerUpdateVisible(true);
    const timer = window.setTimeout(() => setCloudWorkerUpdateVisible(false), 8000);
    return () => window.clearTimeout(timer);
  }, [cloudWorkerUpdateKey]);
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
  const displayName = isGroup ? (groupInfo?.name || topicName || topic) : (resolvedPeerProfile?.display_name || resolvedPeerProfile?.username || topicName || topic);
  const displayAvatarUrl = isGroup ? (groupInfo?.avatar_url || topicAvatarUrl) : (resolvedPeerProfile?.avatar_url || topicAvatarUrl);
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
      setVerifiedArtifactRefresh(null);
      return;
    }
    const refreshURL = artifactURLForVersion(latestActivePreviewURL, latestActivePreviewVersion);
    if (!refreshURL) {
      setPendingArtifactRefresh(null);
      setVerifiedArtifactRefresh(null);
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
    setVerifiedArtifactRefresh((current) => (
      artifactRefreshFileKey(current) === candidateKey ? current : null
    ));
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
    if (!candidateKey || artifactRefreshFileKey(pendingArtifactRefresh) !== candidateKey) return;
    setVerifiedArtifactRefresh((current) => (
      artifactRefreshFileKey(current) === candidateKey ? current : candidate
    ));
  }, [pendingArtifactRefresh]);

  useEffect(() => {
    const candidate = verifiedArtifactRefresh;
    const candidateKey = artifactRefreshFileKey(candidate);
    if (!candidateKey || artifactRefreshFileKey(pendingArtifactRefresh) !== candidateKey) return undefined;

    let cancelled = false;
    const candidateAgentUID = Number(candidate?.artifact_agent_uid || 0);
    const candidateArtifactID = String(candidate?.artifact_id || '');
    const candidateVersion = Number(candidate?.publish_version || 0);
    const clearCandidate = () => {
      if (cancelled) return;
      setPendingArtifactRefresh((current) => (
        artifactRefreshFileKey(current) === candidateKey ? null : current
      ));
      setVerifiedArtifactRefresh((current) => (
        artifactRefreshFileKey(current) === candidateKey ? null : current
      ));
    };
    const applyWhenCurrentPageAllows = async () => {
      const currentFocus = activeArtifactFocusRef.current;
      const candidateFocus = artifactMessageFocusFromPreviewFile(
        candidate,
        topic,
        artifactTopicGenerationRef.current,
      );
      if (!currentFocus
        || !candidateFocus
        || activeTopicRef.current !== topic
        || activeArtifactAgentUIDRef.current !== candidateAgentUID
        || currentFocus.topic !== topic
        || currentFocus.topicGeneration !== artifactTopicGenerationRef.current
        || candidateFocus.topicGeneration !== artifactTopicGenerationRef.current
        || currentFocus.agentUid !== candidateAgentUID
        || currentFocus.artifactId !== candidateArtifactID
        || candidateVersion <= currentFocus.displayedVersion) {
        clearCandidate();
        return;
      }

      const currentBinding = activeArtifactFrameRef.current;
      const hasCurrentBinding = artifactBindingMatchesFocus(currentBinding, currentFocus);
      let currentPageContext = null;
      if (hasCurrentBinding) {
        currentPageContext = await requestArtifactPageContext(
          currentBinding,
          currentFocus.artifactRef,
        );
      }
      if (cancelled) return;
      const latestFocus = activeArtifactFocusRef.current;
      if (!latestFocus
        || latestFocus !== currentFocus
        || (hasCurrentBinding && activeArtifactFrameRef.current !== currentBinding)
        || activeTopicRef.current !== topic
        || artifactTopicGenerationRef.current !== currentFocus.topicGeneration
        || activeArtifactAgentUIDRef.current !== candidateAgentUID) {
        clearCandidate();
        return;
      }
      if (hasCurrentBinding && currentPageContext === null) return;
      if (currentPageContext?.dirty === true) return;

      setPreviewFileWithFocus(candidate);
      setPendingArtifactRefresh((current) => (
        artifactRefreshFileKey(current) === candidateKey ? null : current
      ));
      setVerifiedArtifactRefresh((current) => (
        artifactRefreshFileKey(current) === candidateKey ? null : current
      ));
    };

    void applyWhenCurrentPageAllows();
    return () => {
      cancelled = true;
    };
  }, [
    artifactRegistryRevision,
    pendingArtifactRefresh,
    setPreviewFileWithFocus,
    topic,
    verifiedArtifactRefresh,
  ]);

  const handleArtifactRefreshFailed = useCallback((candidate) => {
    const candidateKey = artifactRefreshFileKey(candidate);
    setPendingArtifactRefresh((current) => (
      artifactRefreshFileKey(current) === candidateKey ? null : current
    ));
    setVerifiedArtifactRefresh((current) => (
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

  const getSender = (msg) => {
    if (sameUID(msg.from_uid, user.uid)) {
      return {
        name: user.display_name || user.username,
        avatarUrl: user.avatar_url,
        isBot: user.account_type === 'bot',
      };
    }
    if (isGroup) {
      const senderUID = parseUid(msg.from_uid);
      const member = memberMap.get(senderUID);
      const rosterAgent = availableAgentByUID.get(senderUID);
      const senderProfile = member || rosterAgent;
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
          || inferredAgentUIDs.has(senderUID)
          || isAssistantAuthoredMessage(msg),
        ),
      };
    }
    return {
      name: resolvedPeerProfile?.display_name || resolvedPeerProfile?.username || topicName || topic,
      avatarUrl: displayAvatarUrl,
      isBot: peerIsBot,
    };
  };

  // Group messages into working areas and text messages with consecutive checking
  const groupedMessages = useMemo(() => {
    const groups = [];
    const workingByExplicitTurn = new Map();
    const workingByFallbackTurn = new Map();
    let currentWorking = null;
    let latestHumanPromptKey = '';
    let prevSenderUid = null;
    let prevTime = 0;
    let prevVisibleSenderUid = null;
    let prevVisibleTime = 0;

    const registerWorkingGroup = (group) => {
      if (group.explicitTurnKey) {
        workingByExplicitTurn.set(group.explicitTurnKey, group);
      }
      if (group.fallbackTurnKey) {
        workingByFallbackTurn.set(group.fallbackTurnKey, group);
      }
    };

    const flushCurrentWorking = () => {
      if (!currentWorking) return;
      groups.push(currentWorking);
      registerWorkingGroup(currentWorking);
      currentWorking = null;
    };

    const findWorkingGroup = ({ explicitTurnKey, fallbackTurnKey }) => {
      if (explicitTurnKey) {
        const explicitMatch = workingByExplicitTurn.get(explicitTurnKey);
        if (explicitMatch) return explicitMatch;
        const fallbackMatch = fallbackTurnKey
          ? workingByFallbackTurn.get(fallbackTurnKey)
          : null;
        return fallbackMatch && !fallbackMatch.explicitTurnKey ? fallbackMatch : null;
      }
      return fallbackTurnKey ? workingByFallbackTurn.get(fallbackTurnKey) : null;
    };

    const belongsToCurrentWorking = ({ explicitTurnKey, fallbackTurnKey }) => {
      if (!currentWorking) return false;
      if (
        currentWorking.explicitTurnKey
        && explicitTurnKey
        && currentWorking.explicitTurnKey !== explicitTurnKey
      ) {
        return false;
      }
      if (currentWorking.fallbackTurnKey && fallbackTurnKey) {
        return currentWorking.fallbackTurnKey === fallbackTurnKey;
      }
      return true;
    };

    messages.forEach((msg, index) => {
      const msgTime = new Date(msg.created_at || Date.now()).getTime();
      const senderUid = parseUid(msg.from_uid) || String(msg.from_uid || '');
      const isConsecutive = (prevSenderUid === senderUid && (msgTime - prevTime < 5 * 60 * 1000));
      const sender = getSender(msg);
      const assistantAuthored = isAssistantAuthoredMessage(msg, sender.isBot);

      if (isFinalTextMessage(msg) && !assistantAuthored) {
        latestHumanPromptKey = messageTurnIdentity(msg, index);
      }

      const turn = assistantWorkTurn(msg, sender.isBot, latestHumanPromptKey);

      if (isWorkingMessage(msg)) {
        let leadingNarrativeMessages = [];
        let leadingNarrativeIsConsecutive = null;
        if (messageHasActionTool(msg)) {
          const previousGroup = groups[groups.length - 1];
          const previousMessage = previousGroup?.message;
          const sameSender = messageSenderIdentity(previousMessage) === messageSenderIdentity(msg);
          const explicitTurnConflict = Boolean(
            previousGroup?.explicitTurnKey
            && turn.explicitTurnKey
            && previousGroup.explicitTurnKey !== turn.explicitTurnKey
          );
          const fallbackTurnConflict = Boolean(
            previousGroup?.fallbackTurnKey
            && turn.fallbackTurnKey
            && previousGroup.fallbackTurnKey !== turn.fallbackTurnKey
          );
          if (
            previousGroup?.type === 'text'
            && previousGroup.assistantAuthored
            && sameSender
            && !explicitTurnConflict
            && !fallbackTurnConflict
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
        }

        if (currentWorking) {
          currentWorking.messages.push(...leadingNarrativeMessages, msg);
          if (!currentWorking.explicitTurnKey && turn.explicitTurnKey) {
            currentWorking.explicitTurnKey = turn.explicitTurnKey;
          }
          if (!currentWorking.fallbackTurnKey && turn.fallbackTurnKey) {
            currentWorking.fallbackTurnKey = turn.fallbackTurnKey;
          }
        } else {
          const existingWorking = findWorkingGroup(turn);
          if (existingWorking) {
            existingWorking.messages.push(...leadingNarrativeMessages, msg);
            if (!existingWorking.explicitTurnKey && turn.explicitTurnKey) {
              existingWorking.explicitTurnKey = turn.explicitTurnKey;
            }
            if (!existingWorking.fallbackTurnKey && turn.fallbackTurnKey) {
              existingWorking.fallbackTurnKey = turn.fallbackTurnKey;
            }
            registerWorkingGroup(existingWorking);
          } else {
            currentWorking = {
              type: 'working',
              messages: [...leadingNarrativeMessages, msg],
              sender,
              isConsecutive: leadingNarrativeIsConsecutive ?? isConsecutive,
              explicitTurnKey: turn.explicitTurnKey,
              fallbackTurnKey: turn.fallbackTurnKey,
            };
          }
        }
        prevSenderUid = senderUid;
        prevTime = msgTime;
      } else {
        flushCurrentWorking();
        const displayMessage = msg;
        // Recalculate isConsecutive in case a working block just processed
        const textIsConsecutive = (prevSenderUid === senderUid && (msgTime - prevTime < 5 * 60 * 1000));
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
            explicitTurnKey: previousGroup.explicitTurnKey || turn.explicitTurnKey,
            fallbackTurnKey: previousGroup.fallbackTurnKey || turn.fallbackTurnKey,
          };
          prevSenderUid = senderUid;
          prevTime = msgTime;
          prevVisibleSenderUid = senderUid;
          prevVisibleTime = msgTime;
          return;
        }

        const textIsConsecutiveWithoutWorking = (
          prevVisibleSenderUid === senderUid
          && (msgTime - prevVisibleTime < 5 * 60 * 1000)
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
          fallbackTurnKey: turn.fallbackTurnKey,
        });
        prevSenderUid = senderUid;
        prevTime = msgTime;
        prevVisibleSenderUid = senderUid;
        prevVisibleTime = msgTime;
      }
    });

    flushCurrentWorking();

    return reconcileRenderedGroupConsecutiveness(reorderAssistantTurnGroups(groups));
  }, [
    availableAgentByUID,
    inferredAgentUIDs,
    isGroup,
    memberMap,
    messageById,
    messages,
    peerIsBot,
    resolvedPeerProfile,
    displayAvatarUrl,
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
    pendingOlderHistoryAnchorRef.current = null;
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
    const currentScrollTop = el.scrollTop;
    const previousScrollTop = lastTimelineScrollTopRef.current;
    const movedUp = currentScrollTop < previousScrollTop;
    const scrollPositionChanged = currentScrollTop !== previousScrollTop;
    lastTimelineScrollTopRef.current = currentScrollTop;
    if (movedUp && !isTimelineAtBottom(el)) {
      stickToBottomRef.current = false;
    } else if (isTimelineAtBottom(el)) {
      stickToBottomRef.current = true;
    }
    if (scrollPositionChanged
      && loadingOlderRef.current
      && pendingOlderHistoryAnchorRef.current) {
      pendingOlderHistoryAnchorRef.current = captureTimelineReadingAnchor(el);
    }
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
          {cloudWorkerUpdate && cloudWorkerUpdateVisible && (
            <section className="cc-cloud-worker-update-notice" role="status" aria-live="polite">
              <span>云员工「{cloudWorkerUpdate.display_name || cloudWorkerUpdate.username || '当前机器人'}」有新版本 {cloudWorkerUpdate.latest_release}，可在云托管管理中更新。</span>
              <button
                type="button"
                onClick={() => window.dispatchEvent(new CustomEvent('cc:open-cloud-worker-manager', {
                  detail: { workerUid: cloudWorkerUpdate.uid, tenantName: cloudWorkerUpdate.tenant_name },
                }))}
              >
                去更新
              </button>
            </section>
          )}
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
            <span>{historyError}</span>
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
          {runtimePlan && !hasPersistedRuntimePlan && <RuntimePlanCard plan={runtimePlan} />}
          {peerTyping && (
            <div className="v3-peer-typing" role="status">
              <span className="v3-peer-typing-label">{t('typing')}</span>
            </div>
          )}
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
          const current = readComposerAttachmentDraft(composerDraftStoreRef.current, topic);
          markComposerPhoneUploadIgnoredFileKey(
            composerDraftStoreRef.current,
            topic,
            attachmentFileKey(current[index]),
          );
          updateAttachmentDraft(topic, (items) => items.filter((_, attachmentIndex) => attachmentIndex !== index));
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
        modelInfo={modelInfo}
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
                onOpenRemoteArtifactFullscreen={openRemoteArtifactFullscreen}
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

function attachmentFileKey(attachment) {
  return attachment?.content?.payload?.file_key
    || attachment?.content?.payload?.url
    || attachment?.file_key
    || attachment?.url
    || attachment?.name
    || '';
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

function messageSenderIdentity(message) {
  const rawSender = message?.from_uid ?? message?.from ?? '';
  const parsedSender = parseUid(rawSender);
  return parsedSender ? String(parsedSender) : String(rawSender).trim();
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

function assistantWorkTurn(message, senderIsBot, latestHumanPromptKey) {
  if (!isWorkingMessage(message) && !isAssistantAuthoredMessage(message, senderIsBot)) {
    return { explicitTurnKey: '', fallbackTurnKey: '' };
  }

  const senderKey = messageSenderIdentity(message) || 'agent';
  const explicitTurn = assistantReplyTurnKey(message);
  return {
    explicitTurnKey: explicitTurn ? `${senderKey}:turn:${explicitTurn}` : '',
    fallbackTurnKey: latestHumanPromptKey ? `${senderKey}:prompt:${latestHumanPromptKey}` : '',
  };
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

  return {
    ...lastGroup,
    message: {
      ...lastGroup.message,
      content,
      content_blocks: contentBlocks,
    },
    sourceMessages,
    sender: groups[0].sender || lastGroup.sender,
    replyMessage: lastGroup.replyMessage || null,
    explicitTurnKey: lastGroup.explicitTurnKey || groups.find((group) => group.explicitTurnKey)?.explicitTurnKey || '',
    fallbackTurnKey: lastGroup.fallbackTurnKey || groups.find((group) => group.fallbackTurnKey)?.fallbackTurnKey || '',
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
      explicitTurnKey: [...sourceWorkingGroups].reverse().find((group) => group.explicitTurnKey)?.explicitTurnKey || '',
      fallbackTurnKey: [...sourceWorkingGroups].reverse().find((group) => group.fallbackTurnKey)?.fallbackTurnKey || '',
    }]
    : [];
  const ordered = [...workingGroups, ...(mergedOutput ? [mergedOutput] : [])];
  let firstOutputFound = false;

  return ordered.map((group, index) => {
    const next = {
      ...group,
      isConsecutive: index === 0 ? firstIsConsecutive : true,
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
    const turnKey = group?.fallbackTurnKey || group?.explicitTurnKey || '';
    let bundle = entries[entries.length - 1];
    const conflictsWithCurrentTurn = Boolean(
      bundle?.turnKey
      && turnKey
      && bundle.turnKey !== turnKey
    );
    if (!bundle || bundle.senderKey !== senderKey || conflictsWithCurrentTurn) {
      bundle = { type: 'bundle', senderKey, turnKey, groups: [] };
      entries.push(bundle);
    } else if (!bundle.turnKey && turnKey) {
      bundle.turnKey = turnKey;
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

function messageCreatedAtMs(message) {
  const timestamp = new Date(message?.created_at || '').getTime();
  return Number.isFinite(timestamp) ? timestamp : null;
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

function isTimelineAtBottom(el) {
  if (!el) return true;
  return el.scrollHeight - el.scrollTop - el.clientHeight <= TIMELINE_BOTTOM_EPSILON;
}

function captureTimelineReadingAnchor(timeline) {
  if (!timeline) return null;
  const timelineRect = timeline.getBoundingClientRect();
  const anchors = Array.from(timeline.querySelectorAll('[data-search-message-id]'));
  const anchor = anchors.find((element) => {
    const rect = element.getBoundingClientRect();
    return rect.bottom > timelineRect.top && rect.top < timelineRect.bottom;
  }) || anchors[0];
  return {
    scrollHeight: timeline.scrollHeight,
    scrollTop: timeline.scrollTop,
    messageID: anchor?.dataset.searchMessageId || '',
    offsetTop: anchor ? anchor.getBoundingClientRect().top - timelineRect.top : null,
  };
}

function restoreTimelineReadingAnchor(timeline, anchor) {
  if (anchor.messageID && Number.isFinite(anchor.offsetTop)) {
    const target = Array.from(timeline.querySelectorAll('[data-search-message-id]'))
      .find((element) => element.dataset.searchMessageId === anchor.messageID);
    if (target) {
      const offsetTop = target.getBoundingClientRect().top - timeline.getBoundingClientRect().top;
      timeline.scrollTop += offsetTop - anchor.offsetTop;
      return;
    }
  }
  timeline.scrollTop = anchor.scrollTop + (timeline.scrollHeight - anchor.scrollHeight);
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
  // Sort by seq_id (now unified for all messages)
  return Array.from(byId.values()).sort((a, b) => {
    const aSeq = a.seq_id || a.id;
    const bSeq = b.seq_id || b.id;
    return aSeq - bSeq;
  });
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
  const imageURL = resolveMediaURL(item.payload?.url || item.payload?.thumbnail);
  const downloadURL = downloadableMediaURL(imageURL);

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
        'button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
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
      <PwaDownloadLink
        aria-label={`下载图片 ${item.payload?.name || ''}`.trim()}
        className="oc-rich-media-preview-download"
        download={item.payload?.name || true}
        href={downloadURL || undefined}
        onClick={(event) => event.stopPropagation()}
        rel="noopener noreferrer"
        target="_blank"
        title="下载图片"
      >
        <Download size={24} aria-hidden="true" />
      </PwaDownloadLink>
      <img
        src={imageURL}
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
