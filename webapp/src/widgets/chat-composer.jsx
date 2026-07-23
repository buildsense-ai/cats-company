import React, { useEffect, useRef } from 'react';
import { ArrowUp, Bot, ChevronDown, Plus, Square } from 'lucide-react';

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
  onCloseMenus,
  notices,
  overlay,
  boxOverlay,
  rootProps = {},
}) {
  const rootRef = useRef(null);
  const attachmentPickerRef = useRef(null);
  const agentPickerRef = useRef(null);
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
            {...textareaProps}
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
    </div>
  );
}
