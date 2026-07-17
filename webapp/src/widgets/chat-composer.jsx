import React, { useEffect, useRef } from 'react';
import { ArrowUp, Plus, Square } from 'lucide-react';

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
  onAttachmentToggle,
  attachmentOpen = false,
  attachmentDisabled = false,
  attachmentMenu,
  onSend,
  sendDisabled = false,
  stop = false,
  onStop,
  stopDisabled = false,
  onStop,
  onCloseMenus,
  notices,
  overlay,
  boxOverlay,
  rootProps = {},
}) {
  const rootRef = useRef(null);
  const attachmentPickerRef = useRef(null);

  useEffect(() => {
    if (!attachmentOpen || !onCloseMenus) return undefined;

    const handlePointerDown = (event) => {
      const clickedOpenAttachmentPicker = attachmentOpen && attachmentPickerRef.current?.contains(event.target);
      if (!clickedOpenAttachmentPicker) onCloseMenus();
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
  }, [attachmentOpen, onCloseMenus]);

  return (
    <div
      {...rootProps}
      ref={rootRef}
      className={`v3-composer${className ? ` ${className}` : ''}`}
    >
      {overlay}
      {notices && <div className="v3-composer-notices">{notices}</div>}
      <div className="v3-composer-box">
        {boxOverlay}
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
            ref={textareaRef}
            className="v3-composer-input"
            rows={1}
            placeholder={placeholder}
            value={value}
            disabled={disabled}
            onChange={onChange}
            onKeyDown={onKeyDown}
            onPaste={onPaste}
          />

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
    </div>
  );
}
