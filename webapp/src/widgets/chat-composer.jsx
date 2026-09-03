import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ArrowUp, Bot, ChevronDown, FileText, Mic, Plus, Square, X } from 'lucide-react';

import { createStreamingSTTSession, isStreamingSTTSupported } from '../stt-client';

export const CHAT_COMPOSER_HINT = 'Enter 发送 · Shift+Enter 换行 · Ctrl+B 折叠侧栏 · 点击红色按钮停止生成';
const COMPOSER_INPUT_MIN_HEIGHT = 40;
const COMPOSER_INPUT_MAX_HEIGHT = 200;
const VOICE_HOLD_DELAY_MS = 280;
const VOICE_HOLD_CANCEL_DISTANCE = 72;
const VOICE_WAVE_RAMP_MS = 2200;

function formatVoiceDurationBoundaryNotice(hasText) {
  return hasText ? '已保存到输入框，可继续录音' : '没有识别到内容，可再次录音';
}

function formatVoiceDurationAnnouncement(seconds, hasRecentInput) {
  return hasRecentInput
    ? `语音输入将在约 ${seconds} 秒后结束，继续说即可，内容会保存到输入框`
    : '语音输入即将结束，已识别内容会保存到输入框';
}

export function voiceWavePhaseSpeed(elapsedMs) {
  const progress = Math.min(1, Math.max(0, elapsedMs / VOICE_WAVE_RAMP_MS));
  const eased = progress * progress * (3 - (2 * progress));
  return 0.08 + (1.72 * eased);
}

function voiceWavePath(level, phase, baseline) {
  const amplitude = 12 + (level * 64);
  const point = (offset, strength = 1) => Math.round(
    baseline + (Math.sin(phase + offset) * amplitude * strength),
  );
  return [
    `M0 ${point(0, 0.45)}`,
    `C120 ${point(0.8)} 232 ${point(1.9)} 360 ${point(2.8, 0.8)}`,
    `C493 ${point(3.7)} 609 ${point(4.8)} 742 ${point(5.8, 0.85)}`,
    `C852 ${point(6.8)} 929 ${point(7.7, 0.72)} 1000 ${point(8.5, 0.45)}`,
    'L1000 260 L0 260 Z',
  ].join(' ');
}

function findTouchAtPosition(touches, clientX, clientY, target = null) {
  const candidates = Array.from(touches || []).filter((touch) => (
    !target || target.contains?.(touch.target)
  ));
  return candidates.find((touch) => (
    Math.hypot(touch.clientX - clientX, touch.clientY - clientY) <= 8
  ));
}

const VoiceHoldWave = React.memo(function VoiceHoldWave({ levelRef }) {
  const rimPathRef = useRef(null);
  const fillPathRef = useRef(null);

  useEffect(() => {
    if (typeof window.requestAnimationFrame !== 'function') return undefined;
    if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return undefined;
    let animationFrame = 0;
    let startedAt = null;
    let previousAt = null;
    let phase = 0;
    let level = 0;
    const animate = (now) => {
      if (startedAt === null) startedAt = now;
      if (previousAt === null) previousAt = now;
      const deltaSeconds = Math.min(50, Math.max(0, now - previousAt)) / 1000;
      previousAt = now;
      phase += voiceWavePhaseSpeed(now - startedAt) * deltaSeconds;
      const targetLevel = Math.min(1, Math.max(0, Number(levelRef.current) || 0));
      level += (targetLevel - level) * (targetLevel > level ? 0.4 : 0.15);
      rimPathRef.current?.setAttribute('d', voiceWavePath(level * 0.72, phase + 0.42, 58));
      fillPathRef.current?.setAttribute('d', voiceWavePath(level, phase, 72));
      animationFrame = window.requestAnimationFrame(animate);
    };
    animationFrame = window.requestAnimationFrame(animate);
    return () => window.cancelAnimationFrame(animationFrame);
  }, [levelRef]);

  return (
    <div className="v3-voice-hold-wave" aria-hidden="true">
      <svg viewBox="0 0 1000 260" preserveAspectRatio="none" focusable="false">
        <path
          ref={rimPathRef}
          className="v3-voice-hold-wave-rim"
          d={voiceWavePath(0, 0.42, 58)}
        />
        <path
          ref={fillPathRef}
          className="v3-voice-hold-wave-fill"
          d={voiceWavePath(0, 0, 72)}
        />
      </svg>
      <div className="v3-voice-hold-mic"><Mic size={38} strokeWidth={2.1} /></div>
    </div>
  );
});

export default function ChatComposer({
  className = '',
  textareaRef,
  value,
  placeholder,
  disabled = false,
  onChange,
  onKeyDown,
  onPaste,
  textareaProps = {},
  onAttachmentToggle,
  attachmentOpen = false,
  attachmentDisabled = false,
  attachmentMenu,
  agentName = '选择 Agent',
  agentOpen = false,
  agentDisabled = false,
  agentPickerVisible = true,
  onAgentToggle,
  agentMenu,
  onSend,
  sendDisabled = false,
  stop = false,
  onStop,
  stopDisabled = false,
  agentReplyActive = false,
  onCloseMenus,
  onVoiceFinal,
  voiceInputAvailable = isStreamingSTTSupported(),
  voiceInputDisabled = false,
  voiceSessionKey = '',
  createVoiceSession = createStreamingSTTSession,
  context,
  notices,
  attachments = [],
  onRemoveAttachment,
  attachmentRemovalDisabled = false,
  overlay,
  boxOverlay,
  rootProps = {},
  modelInfo = null,
}) {
  const rootRef = useRef(null);
  const inputRef = useRef(null);
  const voiceButtonRef = useRef(null);
  const attachmentPickerRef = useRef(null);
  const agentPickerRef = useRef(null);
  const previewDialogRef = useRef(null);
  const previewCloseButtonRef = useRef(null);
  const previewReturnFocusRef = useRef(null);
  const [previewImage, setPreviewImage] = useState(null);
  const [voiceState, setVoiceState] = useState('idle');
  const [voicePartial, setVoicePartial] = useState('');
  const [voiceError, setVoiceError] = useState('');
  const [voiceNotice, setVoiceNotice] = useState('');
  const [voiceNoticeTone, setVoiceNoticeTone] = useState('notice');
  const [voiceLiveStatus, setVoiceLiveStatus] = useState('');
  const voiceSessionRef = useRef(null);
  const voiceInsertionRef = useRef(null);
  const voiceLatestTextRef = useRef('');
  const voiceWarningAnnouncementKeyRef = useRef(null);
  const voiceTranscriptRef = useRef(null);
  const voiceHoldTimerRef = useRef(null);
  const voiceHoldGestureRef = useRef(null);
  const voiceHoldTouchStartsRef = useRef([]);
  const voiceHoldFinishRef = useRef(null);
  const suppressVoiceClickRef = useRef(false);
  const voiceWaveLevelRef = useRef(0);
  const [voiceHoldActive, setVoiceHoldActive] = useState(false);
  const [voiceHoldCancel, setVoiceHoldCancel] = useState(false);
  const showAgentPicker = agentPickerVisible && (typeof onAgentToggle === 'function' || Boolean(agentMenu));
  const anyMenuOpen = attachmentOpen || (showAgentPicker && agentOpen);
  const hasComposerContent = Boolean(String(value || '').trim() || attachments.length > 0);

  const showVoiceDurationBoundaryNotice = (hasText) => {
    const notice = formatVoiceDurationBoundaryNotice(hasText);
    setVoiceError('');
    setVoiceNotice(notice);
    setVoiceNoticeTone('success');
    voiceWarningAnnouncementKeyRef.current = null;
    setVoiceLiveStatus(notice);
  };

  useEffect(() => {
    if (!anyMenuOpen || !onCloseMenus) return undefined;

    const handlePointerDown = (event) => {
      const clickedOpenAttachmentPicker = attachmentOpen && attachmentPickerRef.current?.contains(event.target);
      const clickedOpenAgentPicker = showAgentPicker && agentOpen && agentPickerRef.current?.contains(event.target);
      if (!clickedOpenAttachmentPicker && !clickedOpenAgentPicker) onCloseMenus();
    };
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') onCloseMenus();
    };

    document.addEventListener('pointerdown', handlePointerDown, true);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown, true);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [agentOpen, anyMenuOpen, attachmentOpen, onCloseMenus, showAgentPicker]);

  useEffect(() => {
    if (!previewImage) return undefined;

    previewCloseButtonRef.current?.focus({ preventScroll: true });

    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        setPreviewImage(null);
        return;
      }
      if (event.key !== 'Tab') return;

      const dialog = previewDialogRef.current;
      if (!dialog) return;
      const focusableElements = Array.from(dialog.querySelectorAll(
        'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), '
        + 'textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ));
      const firstFocusable = focusableElements[0];
      const lastFocusable = focusableElements[focusableElements.length - 1];
      if (!firstFocusable || !lastFocusable) {
        event.preventDefault();
        return;
      }

      const focusIsOutsideDialog = !dialog.contains(document.activeElement);
      if (event.shiftKey && (document.activeElement === firstFocusable || focusIsOutsideDialog)) {
        event.preventDefault();
        lastFocusable.focus();
      } else if (!event.shiftKey && (document.activeElement === lastFocusable || focusIsOutsideDialog)) {
        event.preventDefault();
        firstFocusable.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      previewReturnFocusRef.current?.focus({ preventScroll: true });
      previewReturnFocusRef.current = null;
    };
  }, [previewImage]);

  const clearVoiceHoldTimer = useCallback(() => {
    if (voiceHoldTimerRef.current) window.clearTimeout(voiceHoldTimerRef.current);
    voiceHoldTimerRef.current = null;
  }, []);

  useEffect(() => () => {
    clearVoiceHoldTimer();
    voiceSessionRef.current?.cancel();
    voiceSessionRef.current = null;
    voiceLatestTextRef.current = '';
    voiceWarningAnnouncementKeyRef.current = null;
  }, [clearVoiceHoldTimer]);

  useEffect(() => {
    voiceSessionRef.current?.cancel();
    voiceSessionRef.current = null;
    voiceInsertionRef.current = null;
    voiceLatestTextRef.current = '';
    voiceWarningAnnouncementKeyRef.current = null;
    clearVoiceHoldTimer();
    voiceHoldGestureRef.current = null;
    voiceHoldTouchStartsRef.current = [];
    setVoiceHoldActive(false);
    setVoiceHoldCancel(false);
    voiceWaveLevelRef.current = 0;
    setVoiceState('idle');
    setVoicePartial('');
    setVoiceError('');
    setVoiceNotice('');
    setVoiceNoticeTone('notice');
    setVoiceLiveStatus('');
  }, [clearVoiceHoldTimer, voiceSessionKey]);

  const voiceActive = ['starting', 'connecting', 'recording', 'finalizing'].includes(voiceState);

  useLayoutEffect(() => {
    const transcript = voiceTranscriptRef.current;
    if (!transcript || !voiceHoldActive) return;
    transcript.scrollTop = transcript.scrollHeight;
  }, [voiceHoldActive, voiceHoldCancel, voicePartial, voiceState]);

  const createPreparedVoiceSession = () => {
    if (voiceSessionRef.current) return voiceSessionRef.current;
    if (voiceActive) {
      return null;
    }
    setVoiceError('');
    setVoiceNotice('');
    setVoiceNoticeTone('notice');
    setVoiceLiveStatus('');
    setVoicePartial('');
    voiceLatestTextRef.current = '';
    voiceWarningAnnouncementKeyRef.current = null;
    const textarea = textareaRef?.current || inputRef.current;
    const baseValue = textarea ? textarea.value : String(value || '');
    const start = textarea ? textarea.selectionStart : baseValue.length;
    const end = textarea ? textarea.selectionEnd : start;
    voiceInsertionRef.current = { baseValue, start, end };
    let session;
    session = createVoiceSession({
      onState: (state) => {
        if (voiceSessionRef.current === session) setVoiceState(state);
      },
      onPartial: (text) => {
        if (voiceSessionRef.current !== session) return;
        voiceLatestTextRef.current = String(text || '');
        setVoicePartial(text);
      },
      onAudioLevel: (level) => {
        if (voiceSessionRef.current !== session) return;
        voiceWaveLevelRef.current = level;
      },
      onDurationWarning: ({ hasRecentInput, remainingMs, remainingSeconds } = {}) => {
        if (voiceSessionRef.current !== session) return;
        const hasRecentInputState = Boolean(hasRecentInput);
        const parsedRemainingSeconds = Number(remainingSeconds);
        const parsedRemainingMs = Number(remainingMs ?? 10_000);
        const seconds = Math.max(
          1,
          Number.isFinite(parsedRemainingSeconds)
            ? Math.ceil(parsedRemainingSeconds)
            : (Number.isFinite(parsedRemainingMs) ? Math.ceil(parsedRemainingMs / 1000) : 10),
        );
        setVoiceError('');
        setVoiceNotice(
          hasRecentInputState
            ? `还剩约 ${seconds} 秒，继续说即可，结束后会保存到输入框`
            : '这段快结束了，已识别内容会保存到输入框',
        );
        setVoiceNoticeTone('notice');
        const announcementKey = hasRecentInputState ? 'active' : 'quiet';
        if (voiceWarningAnnouncementKeyRef.current !== announcementKey) {
          voiceWarningAnnouncementKeyRef.current = announcementKey;
          setVoiceLiveStatus(formatVoiceDurationAnnouncement(seconds, hasRecentInputState));
        }
      },
      onDurationLimit: () => {
        if (voiceSessionRef.current !== session) return;
        setVoiceError('');
        setVoiceNotice('正在保存到输入框…');
        setVoiceNoticeTone('notice');
        voiceWarningAnnouncementKeyRef.current = null;
        setVoiceLiveStatus('正在保存到输入框…');
      },
      onIdleWarning: ({ remainingSeconds } = {}) => {
        if (voiceSessionRef.current !== session) return;
        const seconds = Math.max(1, Number(remainingSeconds) || 1);
        setVoiceError('');
        setVoiceNotice(`未检测到说话，${seconds} 秒后结束；继续说话即可取消`);
        setVoiceNoticeTone('notice');
        const announcementKey = 'idle';
        if (voiceWarningAnnouncementKeyRef.current !== announcementKey) {
          voiceWarningAnnouncementKeyRef.current = announcementKey;
          setVoiceLiveStatus(`未检测到说话，${seconds} 秒后结束，继续说话即可取消`);
        }
      },
      onIdleResumed: () => {
        if (voiceSessionRef.current !== session) return;
        setVoiceNotice('');
        setVoiceNoticeTone('notice');
        voiceWarningAnnouncementKeyRef.current = null;
        setVoiceLiveStatus('正在听…');
      },
      onFinal: (text, details = {}) => {
        if (voiceSessionRef.current !== session) return;
        const insertion = voiceInsertionRef.current;
        const holdWasTriggered = voiceHoldGestureRef.current?.triggered;
        voiceSessionRef.current = null;
        voiceInsertionRef.current = null;
        voiceLatestTextRef.current = '';
        clearVoiceHoldTimer();
        voiceHoldGestureRef.current = null;
        voiceHoldTouchStartsRef.current = [];
        if (holdWasTriggered) suppressVoiceClickRef.current = true;
        setVoiceHoldActive(false);
        setVoiceHoldCancel(false);
        setVoiceState('idle');
        setVoicePartial('');
        voiceWaveLevelRef.current = 0;
        if (details.reason === 'duration_limit' || details.reason === 'idle_timeout') {
          showVoiceDurationBoundaryNotice(Boolean(String(text || '').trim()));
        } else {
          setVoiceNotice('');
          setVoiceNoticeTone('notice');
          voiceWarningAnnouncementKeyRef.current = null;
          setVoiceLiveStatus('');
        }
        onVoiceFinal?.(text, insertion);
      },
      onError: (error, transcript, details = {}) => {
        if (voiceSessionRef.current !== session) return;
        const isDurationBoundary = details.reason === 'duration_limit';
        const insertion = voiceInsertionRef.current;
        const recoveredText = [
          transcript,
          voiceLatestTextRef.current,
          voicePartial,
        ].map((candidate) => String(candidate || '').trim()).find(Boolean) || '';
        voiceSessionRef.current = null;
        voiceInsertionRef.current = null;
        voiceLatestTextRef.current = '';
        const gesture = voiceHoldGestureRef.current;
        clearVoiceHoldTimer();
        voiceHoldGestureRef.current = null;
        voiceHoldTouchStartsRef.current = [];
        if (gesture?.triggered) suppressVoiceClickRef.current = true;
        setVoiceHoldActive(false);
        setVoiceHoldCancel(false);
        setVoiceState(isDurationBoundary ? 'idle' : 'error');
        setVoicePartial('');
        voiceWaveLevelRef.current = 0;
        if (isDurationBoundary) {
          showVoiceDurationBoundaryNotice(Boolean(recoveredText));
        } else {
          setVoiceNotice('');
          setVoiceNoticeTone('notice');
          const errorMessage = error?.message || '语音识别失败';
          voiceWarningAnnouncementKeyRef.current = null;
          setVoiceError(errorMessage);
          setVoiceLiveStatus(errorMessage);
        }
        // Commit the last usable snapshot after releasing the session. This
        // keeps the composer recoverable even if the parent synchronously
        // renders a new draft or starts another voice attempt from the error
        // state.
        if (recoveredText) onVoiceFinal?.(recoveredText, insertion);
      },
    });
    voiceSessionRef.current = session;
    return session;
  };

  const prepareVoiceInput = () => {
    const existingSession = voiceSessionRef.current;
    const session = createPreparedVoiceSession();
    if (session === existingSession) return session;
    try {
      const prepared = session?.prepare?.();
      if (prepared?.catch) void prepared.catch(() => {});
    } catch {
      // start() publishes the normalized error once the gesture becomes active.
    }
    return session;
  };

  const startVoiceInput = async ({ hold = false } = {}) => {
    if (voiceActive) {
      if (!hold) await voiceSessionRef.current?.stop();
      return;
    }
    const session = prepareVoiceInput();
    if (!session) return;
    await session.start();
  };

  const cancelVoiceInput = () => {
    const session = voiceSessionRef.current;
    voiceSessionRef.current = null;
    voiceInsertionRef.current = null;
    voiceLatestTextRef.current = '';
    voiceWarningAnnouncementKeyRef.current = null;
    voiceHoldTouchStartsRef.current = [];
    session?.cancel();
    setVoiceState('idle');
    setVoicePartial('');
    setVoiceNotice('');
    setVoiceNoticeTone('notice');
    setVoiceLiveStatus('');
    voiceWaveLevelRef.current = 0;
  };

  let voicePreviewText = '';
  if (voiceState === 'starting' || voiceState === 'connecting') voicePreviewText = '正在连接…';
  if (voiceState === 'recording') voicePreviewText = voicePartial || '正在听…';
  if (voiceState === 'finalizing') voicePreviewText = voicePartial || '正在整理文字…';
  let voiceStatusText = '';
  if (voiceState === 'starting' || voiceState === 'connecting') voiceStatusText = '正在连接语音输入…';
  if (voiceState === 'recording') voiceStatusText = '正在听…';
  if (voiceState === 'finalizing') voiceStatusText = '正在整理文字…';
  if (voiceError) voiceStatusText = voiceError;
  else if (voiceLiveStatus) voiceStatusText = voiceLiveStatus;
  const voiceFeedbackText = voiceError || voiceNotice;
  const voiceFeedbackTone = voiceError ? 'is-error' : (voiceNotice ? `is-${voiceNoticeTone}` : '');
  const insertion = voiceInsertionRef.current;
  const showVoicePreview = voiceActive && insertion;
  const displayedValue = showVoicePreview
    ? insertion.baseValue.slice(0, insertion.start) + voicePreviewText + insertion.baseValue.slice(insertion.end)
    : value;
  const voicePreviewPending = showVoicePreview && !voicePartial;

  const setInputNode = useCallback((node) => {
    inputRef.current = node;
    if (typeof textareaRef === 'function') textareaRef(node);
    else if (textareaRef) textareaRef.current = node;
  }, [textareaRef]);

  const resizeInput = useCallback((textarea = inputRef.current) => {
    if (!textarea) return;
    textarea.style.height = 'auto';
    const nextHeight = Math.min(
      Math.max(textarea.scrollHeight, COMPOSER_INPUT_MIN_HEIGHT),
      COMPOSER_INPUT_MAX_HEIGHT,
    );
    textarea.style.height = `${nextHeight}px`;
    textarea.style.overflowY = textarea.scrollHeight > COMPOSER_INPUT_MAX_HEIGHT ? 'auto' : 'hidden';
  }, []);

  useLayoutEffect(() => {
    const textarea = inputRef.current;
    resizeInput(textarea);
    if (showVoicePreview && textarea) textarea.scrollTop = textarea.scrollHeight;
  }, [displayedValue, resizeInput, showVoicePreview]);

  const finishVoiceHold = (event, cancelled = false) => {
    const gesture = voiceHoldGestureRef.current;
    const eventPointerId = event?.pointerId;
    if (!gesture || (eventPointerId != null && gesture.pointerId !== eventPointerId)) return;
    clearVoiceHoldTimer();
    voiceHoldGestureRef.current = null;
    voiceHoldTouchStartsRef.current = [];
    // Mobile browsers can lose pointer capture before delivering the final
    // event. A NotFoundError here must not prevent stop() from running.
    try {
      voiceButtonRef.current?.releasePointerCapture?.(gesture.pointerId);
    } catch {
      // The browser has already released capture; the gesture still ended.
    }
    if (!gesture.triggered) {
      if (cancelled) {
        cancelVoiceInput();
        return;
      }
      suppressVoiceClickRef.current = true;
      void startVoiceInput();
      return;
    }

    event?.preventDefault?.();
    suppressVoiceClickRef.current = true;
    setVoiceHoldActive(false);
    setVoiceHoldCancel(false);
    if (cancelled || gesture.cancelled) cancelVoiceInput();
    else void voiceSessionRef.current?.stop();
  };

  const handleVoicePointerDown = (event) => {
    if (
      (event.pointerType !== 'touch' && event.pointerType !== 'pen')
      || voiceActive
      || voiceHoldGestureRef.current
    ) return;
    suppressVoiceClickRef.current = false;
    clearVoiceHoldTimer();
    prepareVoiceInput();
    try {
      voiceButtonRef.current?.setPointerCapture?.(event.pointerId);
    } catch {
      // Document-level release listeners cover WebViews without capture.
    }
    const touch = event.pointerType === 'touch'
      ? findTouchAtPosition(
        voiceHoldTouchStartsRef.current,
        event.clientX,
        event.clientY,
        event.currentTarget,
      )
      : null;
    voiceHoldTouchStartsRef.current = [];
    voiceHoldGestureRef.current = {
      pointerId: event.pointerId,
      pointerType: event.pointerType,
      touchId: touch?.identifier,
      startX: event.clientX,
      startY: event.clientY,
      triggered: false,
      cancelled: false,
    };
    voiceHoldTimerRef.current = window.setTimeout(() => {
      const gesture = voiceHoldGestureRef.current;
      if (!gesture || gesture.pointerId !== event.pointerId) return;
      gesture.triggered = true;
      setVoiceHoldActive(true);
      setVoiceHoldCancel(false);
      globalThis.navigator?.vibrate?.(10);
      void startVoiceInput({ hold: true });
    }, VOICE_HOLD_DELAY_MS);
  };

  const handleVoicePointerMove = (event) => {
    const gesture = voiceHoldGestureRef.current;
    if (!gesture?.triggered || gesture.pointerId !== event.pointerId) return;
    const cancelled = gesture.startY - event.clientY >= VOICE_HOLD_CANCEL_DISTANCE;
    gesture.cancelled = cancelled;
    setVoiceHoldCancel(cancelled);
  };

  const handleVoiceTouchStart = (event) => {
    const touches = Array.from(event.changedTouches || []);
    const gesture = voiceHoldGestureRef.current;
    if (gesture?.pointerType === 'touch' && gesture.touchId == null) {
      const touch = findTouchAtPosition(
        touches,
        gesture.startX,
        gesture.startY,
        voiceButtonRef.current,
      );
      if (touch) gesture.touchId = touch.identifier;
      return;
    }
    if (gesture) return;
    if (voiceActive) {
      voiceHoldTouchStartsRef.current = [];
      return;
    }
    voiceHoldTouchStartsRef.current = touches;
  };

  // Pointer capture normally routes the release back to the button. Mobile
  // Safari/WebViews can send the release outside it, so keep a document-level
  // fallback for actual release and cancellation events. The touch fallback
  // verifies the touch that started this gesture before finishing it.
  voiceHoldFinishRef.current = finishVoiceHold;
  useEffect(() => {
    const finishFromDocument = (event) => {
      if (!voiceHoldGestureRef.current) return;
      voiceHoldFinishRef.current?.(event, event.type === 'pointercancel');
    };
    const finishFromTouch = (event) => {
      const gesture = voiceHoldGestureRef.current;
      if (!gesture || gesture.pointerType !== 'touch') return;
      const touches = Array.from(event.changedTouches || []);
      if (gesture.touchId == null) return;
      if (!touches.some((touch) => touch.identifier === gesture.touchId)) return;
      voiceHoldFinishRef.current?.(event, event.type === 'touchcancel');
    };

    document.addEventListener('pointerup', finishFromDocument, true);
    document.addEventListener('pointercancel', finishFromDocument, true);
    document.addEventListener('touchend', finishFromTouch, true);
    document.addEventListener('touchcancel', finishFromTouch, true);
    return () => {
      document.removeEventListener('pointerup', finishFromDocument, true);
      document.removeEventListener('pointercancel', finishFromDocument, true);
      document.removeEventListener('touchend', finishFromTouch, true);
      document.removeEventListener('touchcancel', finishFromTouch, true);
    };
  }, []);

  return (
    <div
      {...rootProps}
      ref={rootRef}
      className={`v3-composer${className ? ` ${className}` : ''}`}
    >
      {overlay}
      <div
        className={[
          'v3-composer-box',
          context ? 'has-context' : '',
          notices ? 'has-notices' : '',
          attachments.length > 0 ? 'has-attachments' : '',
          agentReplyActive ? 'is-agent-reply-active' : '',
        ].filter(Boolean).join(' ')}
        aria-busy={agentReplyActive}
      >
        {boxOverlay}
        {modelInfo && (
          <div
            className="v3-composer-model-info"
            title={modelInfo.title || undefined}
            aria-label={`${modelInfo.model || '模型'}${modelInfo.quota ? `，${modelInfo.quota}` : ''}`}
          >
            <span className="v3-composer-model-name">{modelInfo.model || '模型未知'}</span>
            {modelInfo.quota && <span className={`v3-composer-model-quota${modelInfo.tone ? ` ${modelInfo.tone}` : ''}`}>{modelInfo.quota}</span>}
          </div>
        )}
        {context && <div className="v3-composer-context">{context}</div>}
        {notices && <div className="v3-composer-notices">{notices}</div>}
        {attachments.length > 0 && (
          <div className="v3-composer-attachment-tray" aria-label="待发送附件">
            {attachments.map((attachment, index) => {
              const payload = attachment?.content?.payload || {};
              const name = attachment?.name || payload.name || `附件 ${index + 1}`;
              const isImage = attachment?.type === 'image';
              const previewUrl = payload.thumbnail || payload.url || '';
              return (
                <div
                  className={`v3-composer-attachment-chip${isImage ? ' is-image' : ' is-file'}`}
                  key={payload.file_key || `${name}-${index}`}
                  title={name}
                >
                  {isImage && previewUrl ? (
                    <button
                      type="button"
                      className="v3-composer-attachment-preview"
                      aria-label={`预览图片：${name}`}
                      onClick={(event) => {
                        previewReturnFocusRef.current = event.currentTarget;
                        setPreviewImage({ src: payload.url || previewUrl, name });
                      }}
                    >
                      <img src={previewUrl} alt="" width="56" height="56" />
                    </button>
                  ) : (
                    <>
                      <span className="v3-composer-file-icon"><FileText size={18} /></span>
                      <span className="v3-composer-file-copy">
                        <strong>{name}</strong>
                        <small>文件</small>
                      </span>
                    </>
                  )}
                  {typeof onRemoveAttachment === 'function' && (
                    <button
                      type="button"
                      className="v3-composer-attachment-remove"
                      aria-label={`移除附件：${name}`}
                      onClick={() => onRemoveAttachment(index)}
                      disabled={attachmentRemovalDisabled}
                    >
                      <X size={14} strokeWidth={2.2} />
                    </button>
                  )}
                </div>
              );
            })}
          </div>
        )}
        <div className={`v3-composer-row${stop ? ' has-stop' : (hasComposerContent ? ' has-content' : ' is-empty')}`}>
          <div ref={attachmentPickerRef} className="v3-attachment-picker">
            <button
              className="v3-tool v3-composer-plus"
              onClick={onAttachmentToggle}
              title="添加文件或图片"
              aria-label="添加文件或图片"
              aria-expanded={attachmentOpen}
              disabled={attachmentDisabled}
              type="button"
            >
              <Plus size={20} />
            </button>
            {attachmentMenu}
          </div>

          <textarea
            {...textareaProps}
            ref={setInputNode}
            data-cc-focus-group="true"
            className={`v3-composer-input${showVoicePreview ? ' is-voice-preview' : ''}${voicePreviewPending ? ' is-voice-pending' : ''}`}
            aria-label={textareaProps['aria-label'] || placeholder || '消息'}
            name={textareaProps.name || 'message'}
            autoComplete={textareaProps.autoComplete || 'off'}
            spellCheck={textareaProps.spellCheck ?? true}
            rows={1}
            placeholder={placeholder}
            value={displayedValue}
            disabled={disabled}
            readOnly={showVoicePreview || textareaProps.readOnly}
            onChange={(event) => {
              if (voiceState === 'error') setVoiceError('');
              if (voiceNotice) setVoiceNotice('');
              voiceWarningAnnouncementKeyRef.current = null;
              setVoiceLiveStatus('');
              resizeInput(event.currentTarget);
              onChange?.(event);
            }}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
          />
          <span className="oc-visually-hidden" role="status" aria-live="polite" aria-atomic="true">
            {voiceStatusText}
          </span>

          {typeof onVoiceFinal === 'function' && (
            <button
              ref={voiceButtonRef}
              type="button"
              className={`v3-tool v3-voice-button${voiceActive ? ' is-active' : ''}`}
              aria-label={!voiceInputAvailable ? '当前浏览器不支持语音输入' : (voiceActive ? '停止语音输入' : '开始语音输入')}
              title={!voiceInputAvailable ? '当前浏览器不支持麦克风音频采集' : (voiceActive ? '停止语音输入' : '语音输入')}
              aria-pressed={voiceActive}
              disabled={!voiceInputAvailable || voiceInputDisabled || disabled}
              onPointerDown={handleVoicePointerDown}
              onPointerMove={handleVoicePointerMove}
              onPointerUp={(event) => finishVoiceHold(event)}
              onPointerCancel={(event) => finishVoiceHold(event, true)}
              onTouchStart={handleVoiceTouchStart}
              onContextMenu={(event) => {
                if (voiceHoldGestureRef.current?.triggered) event.preventDefault();
              }}
              onClick={() => {
                if (suppressVoiceClickRef.current) {
                  suppressVoiceClickRef.current = false;
                  return;
                }
                void startVoiceInput();
              }}
            >
              {voiceActive ? <Square size={14} fill="currentColor" /> : <Mic size={18} />}
            </button>
          )}

          {showAgentPicker && (
            <div ref={agentPickerRef} className="v3-agent-picker">
              <button
                type="button"
                className="v3-agent-picker-button"
                onClick={onAgentToggle}
                aria-expanded={agentOpen}
                aria-label={`选择 Agent，当前为${agentName}`}
                disabled={agentDisabled}
              >
                <Bot className="v3-agent-picker-icon" size={16} aria-hidden="true" />
                <span>{agentName}</span><ChevronDown className="v3-agent-picker-chevron" size={14} />
              </button>
              {agentMenu}
            </div>
          )}
          <button
            className={`v3-send${stop ? ' stop' : ''}`}
            disabled={stop ? stopDisabled : sendDisabled}
            onClick={stop ? onStop : onSend}
            aria-label={stop ? '停止当前工作' : '发送'}
            title={stop ? '停止当前工作' : '发送'}
            type="button"
          >
            {stop ? <Square size={13} fill="currentColor" /> : <ArrowUp size={18} />}
          </button>
        </div>
      </div>
      <div
        className={`v3-composer-hint${voiceFeedbackTone ? ` ${voiceFeedbackTone}` : ''}`}
        aria-hidden={voiceNotice ? 'true' : undefined}
      >
        {voiceFeedbackText || CHAT_COMPOSER_HINT}
      </div>
      {voiceHoldActive && (
        <div
          className={`v3-voice-hold-overlay${voiceHoldCancel ? ' is-cancelling' : ''}`}
        >
          <div className="v3-voice-hold-copy">
            <div ref={voiceTranscriptRef} className="v3-voice-hold-transcript">
              {voiceHoldCancel
                ? '松开取消输入'
                : (voicePartial || (voiceState === 'starting' || voiceState === 'connecting' ? '正在连接…' : '正在听…'))}
            </div>
            {!voiceHoldCancel && voiceNotice && (
              <div className="v3-voice-hold-limit-notice" aria-hidden="true">
                {voiceNotice}
              </div>
            )}
            <div className="v3-voice-hold-instruction">
              {voiceHoldCancel ? '下滑继续录音' : '上滑取消'}
            </div>
          </div>
          <VoiceHoldWave levelRef={voiceWaveLevelRef} />
        </div>
      )}
      {previewImage && (
        <div
          ref={previewDialogRef}
          className="v3-composer-image-preview-backdrop"
          role="dialog"
          aria-modal="true"
          aria-label={`图片预览：${previewImage.name}`}
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) setPreviewImage(null);
          }}
        >
          <div className="v3-composer-image-preview">
            <img src={previewImage.src} alt={previewImage.name} />
            <button
              ref={previewCloseButtonRef}
              type="button"
              aria-label="关闭图片预览"
              onClick={() => setPreviewImage(null)}
            >
              <X size={20} />
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
