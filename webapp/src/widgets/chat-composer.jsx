import React, { useEffect, useRef, useState } from 'react';
import { ArrowUp, Bot, ChevronDown, FileText, Plus, Square, X } from 'lucide-react';

export const CHAT_COMPOSER_HINT = 'Enter 发送 · Shift+Enter 换行 · Ctrl+B 折叠侧栏 · 点击红色按钮停止生成';

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
  const attachmentPickerRef = useRef(null);
  const agentPickerRef = useRef(null);
  const previewDialogRef = useRef(null);
  const previewCloseButtonRef = useRef(null);
  const previewReturnFocusRef = useRef(null);
  const [previewImage, setPreviewImage] = useState(null);
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
            ref={textareaRef}
            className="v3-composer-input"
            aria-label={textareaProps['aria-label'] || placeholder || '消息'}
            name={textareaProps.name || 'message'}
            autoComplete={textareaProps.autoComplete || 'off'}
            spellCheck={textareaProps.spellCheck ?? true}
            rows={1}
            placeholder={placeholder}
            value={value}
            disabled={disabled}
            onChange={onChange}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
          />

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
      <div className="v3-composer-hint">{CHAT_COMPOSER_HINT}</div>
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
