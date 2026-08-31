import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Check, FileText, Image, Smartphone, X } from 'lucide-react';
import { api } from '../api';
import { insertTranscriptAtSelection } from '../utils/composer-transcript';
import {
  invalidateComposerDraftRevision,
  isComposerDraftRevisionCurrent,
  NEW_TASK_DRAFT_KEY,
  persistComposerDraftStore,
  readComposerAttachmentDraft,
  readComposerDraftRevision,
  readComposerDraftMutationRevision,
  readComposerInputDraft,
  readComposerPhoneUploadSession,
  subscribeComposerDraftStore,
  writeComposerAttachmentDraft,
  writeComposerTaskContextDraft,
  writeComposerInputDraft,
  writeComposerPhoneUploadSession,
} from '../utils/composer-draft-storage';
import {
  IMAGE_UPLOAD_ACCEPT,
  MAX_ATTACHMENT_SIZE,
  MAX_ATTACHMENT_SIZE_MB,
  inferAttachmentType,
  validateImageUpload,
} from '../utils/upload-rules';
import ChatComposer from './chat-composer';
import QRCode from './qr-code';

const MAX_DROPPED_FILES = 200;
const PHONE_UPLOAD_POLL_INTERVAL_MS = 2000;

export default function EmptyTaskComposer({
  className = 'cc-empty-composer-wrap',
  placeholder = '输入指令，我帮您完成',
  initialAgent,
  composerDraftStore,
  draftKey = NEW_TASK_DRAFT_KEY,
  onSelectedAgentChange,
  onResolveAgentTopic,
  onActivateTopic,
  voiceInputAvailable,
  createVoiceSession,
  modelInfo = null,
}) {
  const normalizedDraftKey = String(draftKey || NEW_TASK_DRAFT_KEY);
  // Components used outside TinodeWeb may not receive the shared store. Keep
  // a per-instance revision object anyway so late callbacks cannot resurrect a
  // cleared local draft in that mode.
  const localRevisionStoreRef = useRef({});
  const revisionStore = composerDraftStore || localRevisionStoreRef.current;
  const initialDraftInput = readComposerInputDraft(composerDraftStore, normalizedDraftKey);
  const initialDraftAttachments = readComposerAttachmentDraft(composerDraftStore, normalizedDraftKey);
  const initialPhoneUploadSession = readComposerPhoneUploadSession(
    composerDraftStore,
    normalizedDraftKey,
  );
  const [input, setInput] = useState(initialDraftInput);
  const initialAgentId = agentKey(initialAgent);
  const [agents, setAgents] = useState(() => initialAgentId ? [initialAgent] : []);
  const [agentsLoading, setAgentsLoading] = useState(true);
  const [agentsError, setAgentsError] = useState('');
  const [selectedAgentId, setSelectedAgentId] = useState(initialAgentId);
  const [attachmentMenuOpen, setAttachmentMenuOpen] = useState(false);
  const [agentPickerOpen, setAgentPickerOpen] = useState(false);
  const [pendingAttachments, setPendingAttachments] = useState(initialDraftAttachments);
  const [attachmentStatus, setAttachmentStatus] = useState(null);
  const [isUploadingAttachment, setIsUploadingAttachment] = useState(false);
  const [isDragActive, setIsDragActive] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [phoneUploadDialogOpen, setPhoneUploadDialogOpen] = useState(false);
  const [phoneUploadSession, setPhoneUploadSession] = useState(initialPhoneUploadSession);
  const [phoneUploadError, setPhoneUploadError] = useState('');

  const textareaRef = useRef(null);
  const imageInputRef = useRef(null);
  const fileInputRef = useRef(null);
  const mountedRef = useRef(true);
  const inputValueRef = useRef(initialDraftInput);
  const initialAgentRef = useRef(initialAgent);
  const agentsRef = useRef(initialAgentId ? [initialAgent] : []);
  const selectedAgentIdRef = useRef(initialAgentId);
  const pendingAttachmentsRef = useRef(initialDraftAttachments);
  const dragDepthRef = useRef(0);
  const sendInFlightRef = useRef(false);
  const phoneUploadSessionRef = useRef(initialPhoneUploadSession);
  const phoneUploadFileKeysRef = useRef(new Set());
  const phoneUploadSyncRef = useRef(null);
  const sentDraftRef = useRef(false);

  const persistDraft = useCallback(() => {
    // Capture both values before either write. Store subscribers are notified
    // synchronously and can otherwise replace the refs between field writes,
    // making an attachment removal look like a no-op.
    const nextInput = String(inputValueRef.current || '');
    const nextAttachments = Array.isArray(pendingAttachmentsRef.current)
      ? [...pendingAttachmentsRef.current]
      : [];
    const currentInput = readComposerInputDraft(composerDraftStore, normalizedDraftKey);
    const currentAttachments = readComposerAttachmentDraft(composerDraftStore, normalizedDraftKey);
    if (currentInput !== nextInput) {
      writeComposerInputDraft(composerDraftStore, normalizedDraftKey, nextInput);
    }
    if (JSON.stringify(currentAttachments) !== JSON.stringify(nextAttachments)) {
      writeComposerAttachmentDraft(composerDraftStore, normalizedDraftKey, nextAttachments);
    }
    persistComposerDraftStore(composerDraftStore);
  }, [composerDraftStore, normalizedDraftKey]);

  const flushDraft = useCallback((event) => {
    const nextValue = event?.currentTarget?.value;
    if (typeof nextValue === 'string' && !event.currentTarget.readOnly) {
      inputValueRef.current = nextValue;
    }
    persistDraft();
  }, [persistDraft]);

  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      // Capture a controlled textarea value before a navigation unmount.
      mountedRef.current = false;
      if (sendInFlightRef.current) return;
      const textarea = textareaRef.current;
      if (textarea && !textarea.readOnly && typeof textarea.value === 'string') {
        inputValueRef.current = textarea.value;
      }
      persistDraft();
    };
  }, [persistDraft]);

  const persistAttachmentDraft = useCallback((attachments) => {
    writeComposerAttachmentDraft(composerDraftStore, normalizedDraftKey, attachments);
    persistComposerDraftStore(composerDraftStore);
  }, [composerDraftStore, normalizedDraftKey]);

  const clearDraftAfterSend = useCallback((
    expectedMutationRevision = null,
    expectedInput,
    expectedAttachments,
  ) => {
    // A newer composer may have written the same draft key while this send was
    // in flight. In that case only invalidate the old callbacks; never clear
    // the newer draft.
    if (
      expectedMutationRevision !== null
      && readComposerDraftMutationRevision(revisionStore, normalizedDraftKey)
        !== expectedMutationRevision
    ) return false;
    if (
      expectedInput !== undefined
      && (composerDraftStore
        ? readComposerInputDraft(composerDraftStore, normalizedDraftKey)
        : inputValueRef.current) !== expectedInput
    ) return false;
    if (
      expectedAttachments !== undefined
      && JSON.stringify(composerDraftStore
        ? readComposerAttachmentDraft(composerDraftStore, normalizedDraftKey)
        : pendingAttachmentsRef.current)
        !== JSON.stringify(expectedAttachments)
    ) return false;

    // Invalidate callbacks from an older composer before removing the draft.
    invalidateComposerDraftRevision(revisionStore, normalizedDraftKey);
    sentDraftRef.current = true;
    inputValueRef.current = '';
    pendingAttachmentsRef.current = [];
    phoneUploadSessionRef.current = null;
    // Persist the visible fields before clearing auxiliary fields; each setter
    // notifies synchronously and could otherwise repopulate the refs.
    persistDraft();
    writeComposerPhoneUploadSession(composerDraftStore, normalizedDraftKey, null);
    writeComposerTaskContextDraft(composerDraftStore, normalizedDraftKey, null);
    persistComposerDraftStore(composerDraftStore);
    if (mountedRef.current) setPhoneUploadSession(null);
    return true;
  }, [composerDraftStore, normalizedDraftKey, persistDraft, revisionStore]);

  useEffect(() => {
    const syncDraftFromStore = () => {
      const nextInput = readComposerInputDraft(composerDraftStore, normalizedDraftKey);
      const nextAttachments = readComposerAttachmentDraft(composerDraftStore, normalizedDraftKey);
      const nextPhoneUploadSession = readComposerPhoneUploadSession(
        composerDraftStore,
        normalizedDraftKey,
      );
      if (phoneUploadSessionRef.current?.session_id !== nextPhoneUploadSession?.session_id) {
        phoneUploadFileKeysRef.current = new Set();
      }
      inputValueRef.current = nextInput;
      pendingAttachmentsRef.current = nextAttachments;
      phoneUploadSessionRef.current = nextPhoneUploadSession;
      setInput(nextInput);
      setPendingAttachments(nextAttachments);
      setPhoneUploadSession(nextPhoneUploadSession);
    };

    syncDraftFromStore();
    return subscribeComposerDraftStore(composerDraftStore, ({ key } = {}) => {
      if (key === normalizedDraftKey && mountedRef.current) syncDraftFromStore();
    });
  }, [composerDraftStore, normalizedDraftKey]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  useEffect(() => {
    initialAgentRef.current = initialAgent;
    const preferredKey = agentKey(initialAgent);
    if (!preferredKey) return;

    if (!agentsRef.current.some((agent) => agentKey(agent) === preferredKey)) {
      agentsRef.current = [initialAgent, ...agentsRef.current];
      setAgents(agentsRef.current);
    }
    selectedAgentIdRef.current = preferredKey;
    setSelectedAgentId(preferredKey);
    setAttachmentStatus(null);
  }, [initialAgent]);

  const replaceAttachments = useCallback((nextAttachments) => {
    const normalized = Array.isArray(nextAttachments) ? [...nextAttachments] : [];
    invalidateComposerDraftRevision(revisionStore, normalizedDraftKey);
    pendingAttachmentsRef.current = normalized;
    if (mountedRef.current) setPendingAttachments(normalized);
    persistDraft();
  }, [composerDraftStore, normalizedDraftKey, persistDraft, revisionStore]);

  const appendAttachments = useCallback((attachments, expectedRevision) => {
    if (!attachments?.length) return false;
    if (!isComposerDraftRevisionCurrent(revisionStore, normalizedDraftKey, expectedRevision)) {
      return false;
    }
    // The shared store outlives this view while navigating to SkillHub, so an
    // upload that finishes after unmount must still be added and persisted.
    const hasSharedAttachmentStore = Boolean(
      composerDraftStore
      && (
        typeof composerDraftStore.getAttachmentDraft === 'function'
        || typeof composerDraftStore.attachmentDrafts?.get === 'function'
      )
    );
    const currentAttachments = hasSharedAttachmentStore
      ? readComposerAttachmentDraft(composerDraftStore, normalizedDraftKey)
      : pendingAttachmentsRef.current;
    const nextAttachments = [...currentAttachments, ...attachments];
    pendingAttachmentsRef.current = nextAttachments;
    if (mountedRef.current) setPendingAttachments(nextAttachments);
    persistAttachmentDraft(nextAttachments);
    return true;
  }, [composerDraftStore, normalizedDraftKey, persistAttachmentDraft, revisionStore]);

  useEffect(() => {
    let cancelled = false;

    const loadAgents = async () => {
      if (!cancelled && mountedRef.current) {
        setAgentsLoading(true);
        setAgentsError('');
      }
      try {
        const response = await api.getAgents();
        if (cancelled || !mountedRef.current) return;
        let nextAgents = Array.isArray(response?.agents) ? response.agents : [];
        const preferredAgent = initialAgentRef.current;
        const preferredKey = agentKey(preferredAgent);
        if (preferredKey && !nextAgents.some((agent) => agentKey(agent) === preferredKey)) {
          nextAgents = [preferredAgent, ...nextAgents];
        }
        agentsRef.current = nextAgents;
        setAgents(nextAgents);
        setSelectedAgentId((current) => {
          const currentKey = String(current || '');
          const currentExists = nextAgents.some((agent) => agentKey(agent) === currentKey);
          const preferredExists = nextAgents.some((agent) => agentKey(agent) === preferredKey);
          const nextKey = currentExists
            ? currentKey
            : (preferredExists ? preferredKey : agentKey(nextAgents[0]));
          selectedAgentIdRef.current = nextKey;
          return nextKey;
        });
      } catch (error) {
        if (cancelled || !mountedRef.current) return;
        agentsRef.current = [];
        setAgents([]);
        selectedAgentIdRef.current = '';
        setSelectedAgentId('');
        setAgentsError(error?.message || 'Agent 列表加载失败，请稍后重试。');
      } finally {
        if (!cancelled && mountedRef.current) setAgentsLoading(false);
      }
    };

    loadAgents();
    window.addEventListener('cc:data-changed', loadAgents);
    return () => {
      cancelled = true;
      window.removeEventListener('cc:data-changed', loadAgents);
    };
  }, []);

  const selectedAgent = useMemo(
    () => agents.find((agent) => agentKey(agent) === String(selectedAgentId || '')) || null,
    [agents, selectedAgentId],
  );
  const hasDraftPayload = Boolean(
    input.trim()
    || pendingAttachments.length > 0
    || phoneUploadSession?.session_id,
  );

  useEffect(() => {
    // A successful send ends this draft. Do not let an Agent roster refresh
    // re-persist context-only state; resume context persistence when the user
    // starts a new draft in the still-mounted composer.
    if (sentDraftRef.current && !hasDraftPayload) return;
    if (hasDraftPayload) sentDraftRef.current = false;
    onSelectedAgentChange?.(selectedAgent);
  }, [hasDraftPayload, onSelectedAgentChange, selectedAgent]);

  const selectedAgentName = selectedAgent
    ? (selectedAgent.display_name || selectedAgent.username || 'Agent')
    : (agentsLoading ? '正在加载 Agent' : '选择 Agent');

  const syncPhoneUploads = useCallback(async ({ final = false } = {}) => {
    const sessionId = phoneUploadSessionRef.current?.session_id;
    if (!sessionId) return [];
    if (sendInFlightRef.current && !final) return [];

    if (final && phoneUploadSyncRef.current) {
      const inFlightOperation = phoneUploadSyncRef.current;
      try {
        await inFlightOperation;
      } catch {
        // A dedicated final read below gets one more chance to collect the latest files.
      }
      if (phoneUploadSyncRef.current === inFlightOperation) phoneUploadSyncRef.current = null;
    }

    let operation = phoneUploadSyncRef.current;
    if (!operation) {
      const draftRevision = readComposerDraftRevision(revisionStore, normalizedDraftKey);
      operation = (async () => {
        const data = await api.getMobileUploadSession(sessionId);
        if (phoneUploadSessionRef.current?.session_id !== sessionId) return [];
        if (!isComposerDraftRevisionCurrent(revisionStore, normalizedDraftKey, draftRevision)) return [];

        // Keep the shared draft up to date even if navigation unmounted this
        // view while the polling request was in flight.
        const nextAttachments = [];
        const existingAttachmentKeys = new Set(
          readComposerAttachmentDraft(composerDraftStore, normalizedDraftKey)
            .map(attachmentFileKey)
            .filter(Boolean),
        );
        for (const file of Array.isArray(data?.files) ? data.files : []) {
          const fileKey = file.file_key || file.url || file.name;
          if (
            !fileKey
            || phoneUploadFileKeysRef.current.has(fileKey)
            || existingAttachmentKeys.has(fileKey)
          ) continue;
          phoneUploadFileKeysRef.current.add(fileKey);
          existingAttachmentKeys.add(fileKey);
          nextAttachments.push(attachmentFromUpload(file));
        }

        if (nextAttachments.length > 0) {
          const appended = appendAttachments(nextAttachments, draftRevision);
          if (appended && mountedRef.current) {
            setAttachmentStatus({
              tone: 'success',
              message: `手机已上传 ${pendingAttachmentsRef.current.length} 个附件，发送后会加入新任务。`,
            });
          }
          if (!appended) return [];
        }
        if (mountedRef.current) setPhoneUploadError('');
        return nextAttachments;
      })();
      phoneUploadSyncRef.current = operation;
    }

    try {
      return await operation;
    } catch (error) {
      if (mountedRef.current) {
        const message = error?.message || '读取手机上传结果失败';
        setPhoneUploadError(message);
        if (/session not found|not found|expired/i.test(message)) {
          phoneUploadSessionRef.current = null;
          writeComposerPhoneUploadSession(composerDraftStore, normalizedDraftKey, null);
          persistComposerDraftStore(composerDraftStore);
          setPhoneUploadSession(null);
        }
      }
      if (final) throw error;
      return [];
    } finally {
      if (phoneUploadSyncRef.current === operation) phoneUploadSyncRef.current = null;
    }
  }, [appendAttachments, composerDraftStore, normalizedDraftKey, revisionStore]);

  useEffect(() => {
    if (!phoneUploadSession?.session_id) return undefined;
    syncPhoneUploads();
    const timer = window.setInterval(() => {
      syncPhoneUploads();
    }, PHONE_UPLOAD_POLL_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [phoneUploadSession?.session_id, syncPhoneUploads]);

  useEffect(() => {
    if (!phoneUploadDialogOpen) return undefined;
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') setPhoneUploadDialogOpen(false);
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [phoneUploadDialogOpen]);

  const uploadAttachmentFiles = useCallback(async (files, requestedType, expectedRevision) => {
    const fileList = Array.from(files || []).filter(Boolean).slice(0, MAX_DROPPED_FILES);
    if (fileList.length === 0 || isUploadingAttachment || sendInFlightRef.current) return;

    const uploadRevision = expectedRevision ?? readComposerDraftRevision(
      revisionStore,
      normalizedDraftKey,
    );
    if (!isComposerDraftRevisionCurrent(revisionStore, normalizedDraftKey, uploadRevision)) return;

    setIsUploadingAttachment(true);
    let uploadedCount = 0;
    let failedCount = 0;
    try {
      for (const file of fileList) {
        if (!isComposerDraftRevisionCurrent(
          revisionStore,
          normalizedDraftKey,
          uploadRevision,
        )) return;
        const type = inferAttachmentType(file, requestedType);
        const validationError = validateAttachmentBeforeUpload(file, type);
        if (validationError) {
          if (mountedRef.current) setAttachmentStatus({ tone: 'error', message: validationError });
          failedCount += 1;
          continue;
        }

        if (mountedRef.current) {
          setAttachmentStatus({ tone: 'info', message: `正在上传 ${file.name || '附件'}...` });
        }
        try {
          const data = await api.uploadFile(file, type);
          if (!isComposerDraftRevisionCurrent(
            revisionStore,
            normalizedDraftKey,
            uploadRevision,
          )) return;
          if (appendAttachments([attachmentFromUpload(data, type, file.type)], uploadRevision)) {
            uploadedCount += 1;
          }
        } catch (error) {
          if (!isComposerDraftRevisionCurrent(
            revisionStore,
            normalizedDraftKey,
            uploadRevision,
          )) return;
          if (mountedRef.current) setAttachmentStatus({ tone: 'error', message: formatUploadError(error) });
          failedCount += 1;
        }
      }

      if (!mountedRef.current) return;
      if (failedCount > 0 && fileList.length > 1) {
        setAttachmentStatus({
          tone: 'error',
          message: uploadedCount > 0
            ? `已添加 ${uploadedCount} 个附件，另有 ${failedCount} 个上传失败。`
            : `${failedCount} 个附件上传失败，请检查格式、大小或网络后重试。`,
        });
      } else if (uploadedCount > 0) {
        setAttachmentStatus({
          tone: 'success',
          message: uploadedCount === 1
            ? '已添加 1 个附件，发送后会加入新任务。'
            : `已添加 ${uploadedCount} 个附件，发送后会加入新任务。`,
        });
      }
      if (uploadedCount > 0) window.setTimeout(() => textareaRef.current?.focus(), 0);
    } finally {
      if (mountedRef.current) setIsUploadingAttachment(false);
    }
  }, [appendAttachments, composerDraftStore, isUploadingAttachment, normalizedDraftKey, revisionStore]);

  const handleFileInput = useCallback(async (event, type) => {
    const files = Array.from(event.target.files || []);
    event.target.value = '';
    await uploadAttachmentFiles(files, type);
  }, [uploadAttachmentFiles]);

  const openAttachmentPicker = useCallback((inputRef) => {
    if (isUploadingAttachment || isSubmitting) return;
    setAttachmentStatus(null);
    if (inputRef.current) {
      inputRef.current.value = '';
      inputRef.current.click();
    }
  }, [isSubmitting, isUploadingAttachment]);

  const openPhoneUploadDialog = useCallback(async () => {
    if (isSubmitting) return;
    setAttachmentMenuOpen(false);
    setPhoneUploadDialogOpen(true);
    setPhoneUploadError('');

    if (phoneUploadSessionRef.current?.session_id) return;
    const draftRevision = readComposerDraftRevision(revisionStore, normalizedDraftKey);
    try {
      const session = await api.createMobileUploadSession('');
      if (!isComposerDraftRevisionCurrent(
        revisionStore,
        normalizedDraftKey,
        draftRevision,
      )) return;
      phoneUploadFileKeysRef.current = new Set();
      phoneUploadSessionRef.current = session;
      writeComposerPhoneUploadSession(composerDraftStore, normalizedDraftKey, session);
      persistComposerDraftStore(composerDraftStore);
      if (mountedRef.current) setPhoneUploadSession(session);
    } catch (error) {
      if (mountedRef.current) setPhoneUploadError(error?.message || '手机上传入口创建失败');
    }
  }, [composerDraftStore, isSubmitting, normalizedDraftKey, revisionStore]);

  const handleInputChange = useCallback((event) => {
    const value = event.target.value;
    inputValueRef.current = value;
    setInput(value);
    persistDraft();
  }, [persistDraft]);

  const handleVoiceFinal = useCallback((transcript, insertion) => {
    const textarea = textareaRef.current;
    const result = insertTranscriptAtSelection(transcript, insertion, textarea, inputValueRef.current);
    if (!result) return;
    inputValueRef.current = result.value;
    setInput(result.value);
    persistDraft();
    window.setTimeout(() => {
      textareaRef.current?.focus();
      textareaRef.current?.setSelectionRange(result.caret, result.caret);
    }, 0);
  }, [persistDraft]);

  const handlePaste = useCallback(async (event) => {
    const files = collectClipboardFiles(event.clipboardData);
    if (files.length === 0) return;
    event.preventDefault();
    event.stopPropagation();
    await uploadAttachmentFiles(files);
  }, [uploadAttachmentFiles]);

  const handleDragEnter = useCallback((event) => {
    if (!hasFileDrag(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current += 1;
    setIsDragActive(true);
  }, []);

  const handleDragOver = useCallback((event) => {
    if (!hasFileDrag(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    event.dataTransfer.dropEffect = 'copy';
    setIsDragActive(true);
  }, []);

  const handleDragLeave = useCallback((event) => {
    if (!hasFileDrag(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
    if (dragDepthRef.current === 0) setIsDragActive(false);
  }, []);

  const handleDrop = useCallback(async (event) => {
    if (!hasFileDrag(event.dataTransfer)) return;
    event.preventDefault();
    event.stopPropagation();
    dragDepthRef.current = 0;
    setIsDragActive(false);

    if (isUploadingAttachment || isSubmitting) return;
    const dropRevision = readComposerDraftRevision(revisionStore, normalizedDraftKey);
    const files = await collectDroppedFiles(event.dataTransfer);
    if (!isComposerDraftRevisionCurrent(revisionStore, normalizedDraftKey, dropRevision)) return;
    if (files.length === 0) {
      setAttachmentStatus({ tone: 'error', message: '这次拖入没有识别到可上传的文件。' });
      return;
    }
    await uploadAttachmentFiles(files, undefined, dropRevision);
  }, [composerDraftStore, isSubmitting, isUploadingAttachment, normalizedDraftKey, revisionStore, uploadAttachmentFiles]);

  const handleSend = useCallback(async () => {
    if (sendInFlightRef.current || isUploadingAttachment) return;

    const agent = agentsRef.current.find(
      (candidate) => agentKey(candidate) === String(selectedAgentIdRef.current || ''),
    );
    if (!agent) {
      setAttachmentStatus({ tone: 'error', message: '请先选择一个 Agent。' });
      setAgentPickerOpen(true);
      return;
    }
    if (
      !inputValueRef.current.trim()
      && pendingAttachmentsRef.current.length === 0
      && !phoneUploadSessionRef.current?.session_id
      && !readComposerPhoneUploadSession(composerDraftStore, normalizedDraftKey)?.session_id
    ) return;
    if (typeof onResolveAgentTopic !== 'function' || typeof onActivateTopic !== 'function') {
      setAttachmentStatus({ tone: 'error', message: '暂时无法创建任务，请稍后重试。' });
      return;
    }

    sendInFlightRef.current = true;
    setIsSubmitting(true);
    setAttachmentMenuOpen(false);
    setAgentPickerOpen(false);
    setAttachmentStatus({ tone: 'info', message: '正在创建任务并发送...' });

    let messageSent = false;
    let taskCreated = false;
    let resolvedTopic = null;
    try {
      await syncPhoneUploads({ final: true });
      if (!mountedRef.current) return;

      const text = inputValueRef.current.trim();
      const attachments = [...pendingAttachmentsRef.current];
      if (!text && attachments.length === 0) return;
      const sentDraftInput = String(inputValueRef.current || '');

      // Stop callbacks that belonged to the draft before this send. Phone
      // polling above is intentionally allowed to finish so its files can be
      // included in this message.
      invalidateComposerDraftRevision(revisionStore, normalizedDraftKey);
      const sentDraftMutationRevision = readComposerDraftMutationRevision(
        revisionStore,
        normalizedDraftKey,
      );

      const contentBlocks = buildAtomicContentBlocks(text, attachments);
      const displayContent = text || summarizeAttachments(attachments);
      const payload = attachments.length > 0
        ? { type: 'text', content: displayContent, content_blocks: contentBlocks }
        : text;

      resolvedTopic = await onResolveAgentTopic(agent, { text, attachments });
      const topicId = resolveTopicId(resolvedTopic);
      if (!topicId) throw new Error('任务创建失败，请稍后重试。');
      taskCreated = true;
      if (!mountedRef.current) {
        await rollbackCreatedTask(resolvedTopic);
        return;
      }

      await api.sendMessage(topicId, payload);
      messageSent = true;
      // The shared store must be cleared even when navigation already
      // unmounted this view while the request was in flight.
      const draftCleared = clearDraftAfterSend(
        sentDraftMutationRevision,
        sentDraftInput,
        attachments,
      );
      if (!mountedRef.current) return;
      if (draftCleared) {
        setInput('');
        setPendingAttachments([]);
        setPhoneUploadSession(null);
        setPhoneUploadDialogOpen(false);
        setAttachmentStatus(null);
      }

      await onActivateTopic(resolvedTopic);
      window.dispatchEvent(new Event('cc:data-changed'));
    } catch (error) {
      if (taskCreated && !messageSent) {
        const rolledBack = await rollbackCreatedTask(resolvedTopic);
        if (rolledBack) window.dispatchEvent(new Event('cc:data-changed'));
      }
      if (!mountedRef.current) return;
      setAttachmentStatus({
        tone: 'error',
        message: messageSent
          ? '消息已发送，但暂时无法打开新任务。请从任务列表中重新进入。'
          : (error?.message || (taskCreated ? '发送失败，请稍后重试。' : '暂时无法创建任务，请稍后重试。')),
      });
    } finally {
      sendInFlightRef.current = false;
      if (mountedRef.current) setIsSubmitting(false);
    }
  }, [clearDraftAfterSend, isUploadingAttachment, onActivateTopic, onResolveAgentTopic, syncPhoneUploads]);

  const handleKeyDown = useCallback((event) => {
    const nativeEvent = event.nativeEvent || event;
    if (
      nativeEvent.isComposing
      || event.isComposing
      || event.keyCode === 229
      || nativeEvent.keyCode === 229
    ) return;
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      handleSend();
    }
  }, [handleSend]);

  const attachmentMenu = (
    <div className={`v3-attachment-menu${attachmentMenuOpen ? ' is-open' : ''}`} aria-hidden={!attachmentMenuOpen}>
      <button type="button" onClick={() => { setAttachmentMenuOpen(false); openAttachmentPicker(imageInputRef); }}>
        <Image size={16} /><span>上传图片</span>
      </button>
      <button type="button" onClick={() => { setAttachmentMenuOpen(false); openAttachmentPicker(fileInputRef); }}>
        <FileText size={16} /><span>上传文件</span>
      </button>
      <button type="button" aria-label="手机扫码上传" data-tooltip="手机扫码上传" onClick={openPhoneUploadDialog}>
        <Smartphone size={16} /><span>手机扫码上传</span>
      </button>
    </div>
  );

  const agentMenu = agentPickerOpen ? (
    <div className="v3-agent-picker-menu" role="listbox" aria-label="选择 Agent">
      {agentsLoading ? (
        <div className="v3-picker-empty">正在加载 Agent...</div>
      ) : agents.length === 0 ? (
        <div className="v3-picker-empty">{agentsError || '暂无可用 Agent'}</div>
      ) : agents.map((agent) => {
        const key = agentKey(agent);
        const name = agent.display_name || agent.username || 'Agent';
        const selected = key === String(selectedAgentId || '');
        return (
          <button
            type="button"
            role="option"
            aria-selected={selected}
            className={selected ? 'selected' : ''}
            key={key}
            onClick={() => {
              selectedAgentIdRef.current = key;
              setSelectedAgentId(key);
              setAgentPickerOpen(false);
              setAttachmentStatus(null);
            }}
          >
            <span>{name}</span>{selected && <Check size={15} />}
          </button>
        );
      })}
    </div>
  ) : null;

  const notices = (
    <>
      {agentsError && agents.length === 0 && (
        <div className="v3-live-input-status v3-live-input-status-error" role="status">{agentsError}</div>
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
  );

  const phoneUploadOverlay = phoneUploadDialogOpen ? (
    <div
      className="v3-phone-upload-backdrop"
      role="dialog"
      aria-modal="true"
      aria-label="手机扫码上传"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) setPhoneUploadDialogOpen(false);
      }}
    >
      <div className="v3-phone-upload-modal">
        <div className="v3-phone-upload-header">
          <div>
            <div className="v3-phone-upload-title">手机扫码上传</div>
            <div className="v3-phone-upload-subtitle">上传到当前草稿，发送后会加入新任务。</div>
          </div>
          <button className="v3-tool" type="button" aria-label="关闭手机上传" onClick={() => setPhoneUploadDialogOpen(false)}>
            <X size={16} strokeWidth={2} />
          </button>
        </div>
        <div className="v3-phone-upload-body">
          {phoneUploadError ? (
            <div className="v3-phone-upload-error">{phoneUploadError}</div>
          ) : resolvePhoneUploadLink(phoneUploadSession?.upload_url) ? (
            <>
              <QRCode value={resolvePhoneUploadLink(phoneUploadSession.upload_url)} size={180} />
              <div className="v3-phone-upload-link">{resolvePhoneUploadLink(phoneUploadSession.upload_url)}</div>
            </>
          ) : (
            <div className="v3-phone-upload-loading">正在创建上传入口...</div>
          )}
        </div>
      </div>
    </div>
  ) : null;

  return (
    <>
      <ChatComposer
        className={className}
        textareaRef={textareaRef}
        value={input}
        placeholder={placeholder}
        disabled={isSubmitting}
        onChange={handleInputChange}
        textareaProps={{ onBlur: flushDraft }}
        onKeyDown={handleKeyDown}
        onPaste={handlePaste}
        onVoiceFinal={handleVoiceFinal}
        voiceInputAvailable={voiceInputAvailable}
        createVoiceSession={createVoiceSession}
        voiceInputDisabled={isSubmitting || isUploadingAttachment}
        voiceSessionKey={`new-task:${selectedAgentId || ''}`}
        attachmentOpen={attachmentMenuOpen}
        attachmentDisabled={isUploadingAttachment || isSubmitting}
        onAttachmentToggle={() => {
          setAgentPickerOpen(false);
          setAttachmentMenuOpen((open) => !open);
        }}
        attachmentMenu={attachmentMenu}
        agentName={selectedAgentName}
        agentOpen={agentPickerOpen}
        agentDisabled={isSubmitting}
        agentPickerVisible={false}
        onAgentToggle={() => {
          setAttachmentMenuOpen(false);
          setAgentPickerOpen((open) => !open);
        }}
        agentMenu={agentMenu}
        onSend={handleSend}
        sendDisabled={
          isSubmitting
          || isUploadingAttachment
          || !selectedAgent
          || (!input.trim() && pendingAttachments.length === 0)
        }
        onCloseMenus={() => {
          setAttachmentMenuOpen(false);
          setAgentPickerOpen(false);
        }}
        attachments={pendingAttachments}
        attachmentRemovalDisabled={isUploadingAttachment || isSubmitting}
        onRemoveAttachment={(index) => {
          replaceAttachments(pendingAttachmentsRef.current.filter((_, attachmentIndex) => attachmentIndex !== index));
          setAttachmentStatus(null);
        }}
        notices={notices}
        modelInfo={modelInfo}
        overlay={phoneUploadOverlay}
        boxOverlay={isDragActive ? (
          <div className="v3-drop-overlay" aria-hidden="true">
            <div className="v3-drop-title">拖放文件以上传</div>
            <div className="v3-drop-subtitle">支持图片、文件和文件夹，附件会先放在这里等待发送。</div>
          </div>
        ) : null}
        rootProps={{
          'aria-label': '新任务输入栏',
          onDragEnter: handleDragEnter,
          onDragOver: handleDragOver,
          onDragLeave: handleDragLeave,
          onDrop: handleDrop,
        }}
      />
      <input
        ref={imageInputRef}
        type="file"
        accept={IMAGE_UPLOAD_ACCEPT}
        multiple
        style={{ display: 'none' }}
        onChange={(event) => handleFileInput(event, 'image')}
      />
      <input
        ref={fileInputRef}
        type="file"
        multiple
        style={{ display: 'none' }}
        onChange={(event) => handleFileInput(event, 'file')}
      />
    </>
  );
}

function agentKey(agent) {
  return String(agent?.uid || agent?.id || '');
}

function resolveTopicId(topic) {
  return topic?.topicId || topic?.topic_id || topic?.topic || topic?.agent?.topic_id || '';
}

async function rollbackCreatedTask(topic) {
  const groupId = topic?.groupId || topic?.group_id || topic?.group?.id;
  if (!groupId || typeof api.disbandGroup !== 'function') return false;
  try {
    await api.disbandGroup(groupId);
    return true;
  } catch {
    return false;
  }
}

function resolvePhoneUploadLink(uploadUrl) {
  if (!uploadUrl) return '';
  if (/^https?:\/\//i.test(uploadUrl)) return uploadUrl;
  const path = uploadUrl.startsWith('/') ? uploadUrl : `/${uploadUrl}`;
  return `${window.location.origin}${path}`;
}

function attachmentFromUpload(file, requestedType, fallbackMimeType = '') {
  const type = requestedType || (file?.type === 'image' ? 'image' : 'file');
  const payload = {
    file_key: file?.file_key,
    url: file?.url,
    name: file?.name,
    size: file?.size,
    mime_type: file?.mime_type || fallbackMimeType || '',
  };
  if (type === 'image') payload.thumbnail = file?.url;
  return {
    type,
    name: file?.name,
    size: file?.size,
    content: { type, payload },
  };
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
  return type === 'image' ? validateImageUpload(file) : '';
}

function formatUploadError(error) {
  const message = String(error?.message || '上传失败');
  if (message.includes('413') || message.includes('Payload Too Large')) {
    return `上传失败：文件超过 ${MAX_ATTACHMENT_SIZE_MB}MB 限制。`;
  }
  if (message.includes('invalid image type')) return '上传失败：当前仅支持 JPG、PNG、GIF、WebP 图片。';
  if (message.includes('file type not allowed')) return '上传失败：该文件类型暂不支持。';
  if (message.includes('Unexpected token') || message.includes('invalid server response') || message.includes('JSON')) {
    return '上传失败：服务器返回了无法识别的响应。';
  }
  return `上传失败：${message}`;
}

function buildAtomicContentBlocks(text, attachments) {
  const blocks = [];
  if (text) blocks.push({ type: 'text', text });
  for (const attachment of attachments || []) {
    const payload = attachment?.content?.payload;
    if (!payload) continue;
    blocks.push({ type: attachment.type === 'image' ? 'image' : 'file', payload });
  }
  return blocks;
}

function summarizeAttachments(attachments) {
  const list = attachments || [];
  if (list.length === 0) return '';
  if (list.length === 1) {
    const attachment = list[0];
    return `[${attachment.type === 'image' ? '图片' : '文件'}] ${attachment.name || 'attachment'}`;
  }
  return `[附件] ${list.map((attachment) => attachment.name || 'attachment').join(', ')}`;
}

function hasFileDrag(dataTransfer) {
  return Array.from(dataTransfer?.types || []).includes('Files');
}

function collectClipboardFiles(clipboardData) {
  const files = [];
  for (const item of Array.from(clipboardData?.items || [])) {
    if (files.length >= MAX_DROPPED_FILES) break;
    if (item.kind === 'file' && typeof item.getAsFile === 'function') {
      const file = item.getAsFile();
      if (file) files.push(file);
    }
  }
  if (files.length === 0) {
    files.push(...Array.from(clipboardData?.files || []).slice(0, MAX_DROPPED_FILES));
  }
  return files;
}

async function collectDroppedFiles(dataTransfer) {
  const files = [];
  const addFiles = (items) => {
    for (const file of items) {
      if (file && files.length < MAX_DROPPED_FILES) files.push(file);
    }
  };

  for (const item of Array.from(dataTransfer?.items || [])) {
    if (files.length >= MAX_DROPPED_FILES || item.kind !== 'file') continue;
    const entry = typeof item.webkitGetAsEntry === 'function' ? item.webkitGetAsEntry() : null;
    if (entry) {
      addFiles(await readEntryFiles(entry, MAX_DROPPED_FILES - files.length));
    } else if (typeof item.getAsFile === 'function') {
      addFiles([item.getAsFile()]);
    }
  }

  if (files.length === 0) addFiles(Array.from(dataTransfer?.files || []));
  return files;
}

async function readEntryFiles(entry, limit) {
  if (!entry || limit <= 0) return [];
  if (entry.isFile) {
    return new Promise((resolve) => {
      entry.file((file) => resolve(file ? [file] : []), () => resolve([]));
    });
  }
  if (!entry.isDirectory) return [];

  const entries = await readDirectoryEntries(entry.createReader());
  const files = [];
  for (const child of entries) {
    if (files.length >= limit) break;
    files.push(...await readEntryFiles(child, limit - files.length));
  }
  return files;
}

function readDirectoryEntries(reader) {
  return new Promise((resolve) => {
    const entries = [];
    const readBatch = () => {
      reader.readEntries((batch) => {
        if (!batch.length) {
          resolve(entries);
          return;
        }
        entries.push(...batch);
        readBatch();
      }, () => resolve(entries));
    };
    readBatch();
  });
}
