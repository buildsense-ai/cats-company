import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { ArrowUp, Bot, ChevronDown, FileText, Mic, Plus, Square, X } from 'lucide-react';

import { createStreamingSTTSession, isStreamingSTTSupported } from '../stt-client';

export const CHAT_COMPOSER_HINT = 'Enter 发送 · Shift+Enter 换行 · Ctrl+B 折叠侧栏 · 点击红色按钮停止生成';
const COMPOSER_INPUT_MIN_HEIGHT = 40;
const COMPOSER_INPUT_MAX_HEIGHT = 200;
const VOICE_HOLD_DELAY_MS = 280;
const VOICE_HOLD_CANCEL_DISTANCE = 72;

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
  const voiceSessionRef = useRef(null);
  const voiceInsertionRef = useRef(null);
  const voiceTranscriptRef = useRef(null);
  const voiceHoldTimerRef = useRef(null);
  const voiceHoldGestureRef = useRef(null);
  const suppressVoiceClickRef = useRef(false);
  const [voiceHoldActive, setVoiceHoldActive] = useState(false);
  const [voiceHoldCancel, setVoiceHoldCancel] = useState(false);
  const [voiceWave, setVoiceWave] = useState({ level: 0, phase: 0 });
  const showAgentPicker = agentPickerVisible && (typeof onAgentToggle === 'function' || Boolean(agentMenu));
  const anyMenuOpen = attachmentOpen || (showAgentPicker && agentOpen);

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
  }, [clearVoiceHoldTimer]);

  useEffect(() => {
    voiceSessionRef.current?.cancel();
    voiceSessionRef.current = null;
    voiceInsertionRef.current = null;
    clearVoiceHoldTimer();
    voiceHoldGestureRef.current = null;
    setVoiceHoldActive(false);
    setVoiceHoldCancel(false);
    setVoiceWave({ level: 0, phase: 0 });
    setVoiceState('idle');
    setVoicePartial('');
    setVoiceError('');
  }, [clearVoiceHoldTimer, voiceSessionKey]);

  const voiceActive = ['starting', 'connecting', 'recording', 'finalizing'].includes(voiceState);

  useLayoutEffect(() => {
    const transcript = voiceTranscriptRef.current;
    if (!transcript || !voiceHoldActive) return;
    transcript.scrollTop = transcript.scrollHeight;
  }, [voiceHoldActive, voiceHoldCancel, voicePartial, voiceState]);

  const startVoiceInput = async ({ hold = false } = {}) => {
    if (voiceActive) {
      if (!hold) await voiceSessionRef.current?.stop();
      return;
    }
    setVoiceError('');
    setVoicePartial('');
    const textarea = textareaRef?.current;
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
        if (voiceSessionRef.current === session) setVoicePartial(text);
      },
      onAudioLevel: (level) => {
        if (voiceSessionRef.current !== session) return;
        setVoiceWave((current) => ({
          level: current.level + ((level - current.level) * (level > current.level ? 0.4 : 0.15)),
          phase: current.phase + 0.15,
        }));
      },
      onFinal: (text) => {
        if (voiceSessionRef.current !== session) return;
        const insertion = voiceInsertionRef.current;
        voiceSessionRef.current = null;
        voiceInsertionRef.current = null;
        setVoiceState('idle');
        setVoicePartial('');
        setVoiceWave({ level: 0, phase: 0 });
        onVoiceFinal?.(text, insertion);
      },
      onError: (error) => {
        if (voiceSessionRef.current !== session) return;
        voiceSessionRef.current = null;
        voiceInsertionRef.current = null;
        setVoiceState('error');
        setVoicePartial('');
        setVoiceWave({ level: 0, phase: 0 });
        setVoiceError(error.message || '语音识别失败');
      },
    });
    voiceSessionRef.current = session;
    await session.start();
  };

  const cancelVoiceInput = () => {
    const session = voiceSessionRef.current;
    voiceSessionRef.current = null;
    voiceInsertionRef.current = null;
    session?.cancel();
    setVoiceState('idle');
    setVoicePartial('');
    setVoiceWave({ level: 0, phase: 0 });
  };

  let voicePreviewText = '';
  if (voiceState === 'starting' || voiceState === 'connecting') voicePreviewText = '正在连接…';
  if (voiceState === 'recording') voicePreviewText = voicePartial || '正在听…';
  if (voiceState === 'finalizing') voicePreviewText = voicePartial || '正在整理文字…';
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
    resizeInput();
  }, [displayedValue, resizeInput]);

  const finishVoiceHold = (event, cancelled = false) => {
    const gesture = voiceHoldGestureRef.current;
    if (!gesture || gesture.pointerId !== event.pointerId) return;
    clearVoiceHoldTimer();
    voiceHoldGestureRef.current = null;
    voiceButtonRef.current?.releasePointerCapture?.(event.pointerId);
    if (!gesture.triggered) return;

    event.preventDefault();
    suppressVoiceClickRef.current = true;
    setVoiceHoldActive(false);
    setVoiceHoldCancel(false);
    if (cancelled || gesture.cancelled) cancelVoiceInput();
    else void voiceSessionRef.current?.stop();
  };

  const handleVoicePointerDown = (event) => {
    if ((event.pointerType !== 'touch' && event.pointerType !== 'pen') || voiceActive) return;
    suppressVoiceClickRef.current = false;
    clearVoiceHoldTimer();
    voiceButtonRef.current?.setPointerCapture?.(event.pointerId);
    voiceHoldGestureRef.current = {
      pointerId: event.pointerId,
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
        <div className="v3-composer-row">
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
              resizeInput(event.currentTarget);
              onChange?.(event);
            }}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
          />
          <span className="oc-visually-hidden" role="status" aria-live="polite">
            {showVoicePreview ? voicePreviewText : (voiceState === 'error' ? voiceError : '')}
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
      <div className={`v3-composer-hint${voiceState === 'error' ? ' is-error' : ''}`}>
        {voiceState === 'error' ? voiceError : CHAT_COMPOSER_HINT}
      </div>
      {voiceHoldActive && (
        <div
          className={`v3-voice-hold-overlay${voiceHoldCancel ? ' is-cancelling' : ''}`}
          role="status"
          aria-live="polite"
        >
          <div className="v3-voice-hold-copy">
            <div ref={voiceTranscriptRef} className="v3-voice-hold-transcript">
              {voiceHoldCancel
                ? '松开取消输入'
                : (voicePartial || (voiceState === 'starting' || voiceState === 'connecting' ? '正在连接…' : '正在听…'))}
            </div>
            <div className="v3-voice-hold-instruction">
              {voiceHoldCancel ? '下滑继续录音' : '上滑取消'}
            </div>
          </div>
          <div className="v3-voice-hold-wave" aria-hidden="true">
            <svg viewBox="0 0 1000 260" preserveAspectRatio="none" focusable="false">
              <path
                className="v3-voice-hold-wave-rim"
                d={voiceWavePath(voiceWave.level * 0.72, voiceWave.phase + 0.42, 58)}
              />
              <path
                className="v3-voice-hold-wave-fill"
                d={voiceWavePath(voiceWave.level, voiceWave.phase, 72)}
              />
            </svg>
            <div className="v3-voice-hold-mic"><Mic size={38} strokeWidth={2.1} /></div>
          </div>
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
