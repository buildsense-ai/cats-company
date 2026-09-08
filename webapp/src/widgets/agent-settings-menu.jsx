import React, { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { Bot, ChevronRight, UserPlus } from 'lucide-react';

export default function AgentSettingsMenu({ onManage, onAdd, inMenu = true }) {
  const [open, setOpen] = useState(false);
  const [inline, setInline] = useState(false);
  const rootRef = useRef(null);
  const triggerRef = useRef(null);
  const menuRef = useRef(null);
  const closeTimer = useRef(null);
  const focusOnOpen = useRef(false);
  const menuId = useId();
  const cancelClose = () => window.clearTimeout(closeTimer.current);

  useEffect(() => () => window.clearTimeout(closeTimer.current), []);
  useEffect(() => {
    if (!open) return;
    const onOutside = (event) => {
      if (!rootRef.current?.contains(event.target)) {
        window.clearTimeout(closeTimer.current);
        setOpen(false);
      }
    };
    document.addEventListener('pointerdown', onOutside);
    return () => document.removeEventListener('pointerdown', onOutside);
  }, [open]);
  useLayoutEffect(() => {
    if (!open) return;
    const position = () => {
      const rect = rootRef.current?.getBoundingClientRect();
      if (rect) setInline(window.innerWidth <= 560 || rect.right + 212 > window.innerWidth - 8);
    };
    position();
    window.addEventListener('resize', position);
    if (focusOnOpen.current) {
      menuRef.current?.querySelector('button')?.focus();
      focusOnOpen.current = false;
    }
    return () => window.removeEventListener('resize', position);
  }, [open]);

  const close = (restoreFocus = false) => {
    cancelClose();
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus();
  };
  const handleMenuKeyDown = (event) => {
    if (event.key === 'Escape' || event.key === 'ArrowLeft') {
      event.preventDefault();
      event.stopPropagation();
      close(true);
    } else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) {
      event.preventDefault();
      const items = [...menuRef.current.querySelectorAll('button')];
      const index = items.indexOf(document.activeElement);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1
        : (index + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
      items[next]?.focus();
    }
  };

  return (
    <div
      ref={rootRef}
      className={`cc-agent-settings-menu${inline ? ' is-inline' : ''}`}
      onMouseEnter={cancelClose}
      onMouseLeave={() => {
        cancelClose();
        closeTimer.current = window.setTimeout(() => {
          if (!menuRef.current?.contains(document.activeElement)) setOpen(false);
        }, 160);
      }}
      onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget)) close(); }}
    >
      <button
        ref={triggerRef}
        type="button"
        className="v3-popover-item cc-agent-settings-trigger"
        role={inMenu ? 'menuitem' : undefined}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onMouseEnter={() => { cancelClose(); setOpen(true); }}
        onClick={() => { cancelClose(); setOpen(true); }}
        onKeyDown={(event) => {
          if (event.key === 'ArrowRight' || event.key === 'ArrowDown') {
            event.preventDefault();
            cancelClose();
            if (open) menuRef.current?.querySelector('button')?.focus();
            else { focusOnOpen.current = true; setOpen(true); }
          } else if (event.key === 'Escape' && open) {
            event.preventDefault();
            event.stopPropagation();
            close(true);
          }
        }}
      >
        <Bot size={16} aria-hidden="true" />
        <span>Agent 助手</span>
        <ChevronRight size={15} className="cc-agent-settings-chevron" aria-hidden="true" />
      </button>
      {open && (
        <div ref={menuRef} id={menuId} role="menu" aria-label="Agent 助手" className="cc-agent-settings-submenu" onKeyDown={handleMenuKeyDown}>
          <button type="button" role="menuitem" onClick={() => { close(); onManage(); }}><Bot size={16} aria-hidden="true" /><span>Agent 助手管理</span></button>
          <button type="button" role="menuitem" onClick={() => { close(); onAdd(); }}><UserPlus size={16} aria-hidden="true" /><span>添加AI 助手</span></button>
        </div>
      )}
    </div>
  );
}
