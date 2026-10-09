import React, { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ChevronDown, Cloud, RefreshCw } from 'lucide-react';
import { api } from '../api';
import '../css/artifact-quick-picker.css';

function recentItems(items) {
  return [...items].sort((a, b) => (Date.parse(b.updated_at || b.created_at) || 0)
    - (Date.parse(a.updated_at || a.created_at) || 0));
}

export default function ArtifactQuickPicker({ agentUid, topicId, onSelect, className, mobile = false, onChoose }) {
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const timerRef = useRef(null);
  const pinnedRef = useRef(false);
  const id = useId();
  const [open, setOpen] = useState(false);
  const [expanded, setExpanded] = useState('');
  const [position, setPosition] = useState(null);
  const [retry, setRetry] = useState(0);
  const [content, setContent] = useState({ loading: false, items: [], error: '', hasMore: false });
  const enabled = Boolean(onSelect);
  const contextKey = `${topicId || ''}:${agentUid || 0}`;

  const cancelTimer = () => { window.clearTimeout(timerRef.current); timerRef.current = null; };
  const close = (restoreFocus = false) => {
    cancelTimer();
    pinnedRef.current = false;
    setOpen(false);
    if (restoreFocus) triggerRef.current?.focus({ preventScroll: true });
  };
  const openPicker = (keyboard = false) => {
    cancelTimer();
    if (!enabled) return;
    setOpen(true);
    if (keyboard) window.requestAnimationFrame(() => panelRef.current?.querySelector('button')?.focus());
  };
  const scheduleClose = () => {
    cancelTimer();
    if (pinnedRef.current) return;
    timerRef.current = window.setTimeout(() => {
      if (!panelRef.current?.contains(document.activeElement)) setOpen(false);
    }, 220);
  };

  useEffect(() => {
    cancelTimer();
    pinnedRef.current = false;
    setOpen(false);
    setExpanded('');
    setContent({ loading: false, items: [], error: '', hasMore: false });
    return cancelTimer;
  }, [contextKey, enabled]);

  useLayoutEffect(() => {
    if (!open) return undefined;
    const measure = () => {
      const rect = triggerRef.current?.getBoundingClientRect();
      if (!rect) return;
      const width = Math.min(150, window.innerWidth - 24);
      const top = Math.min(rect.bottom + 8, window.innerHeight - 120);
      setPosition({ width, top, left: Math.max(12, Math.min(rect.right - width, window.innerWidth - width - 12)), maxHeight: Math.max(100, window.innerHeight - top - 12) });
    };
    measure();
    const outside = event => {
      if (!triggerRef.current?.contains(event.target) && !panelRef.current?.contains(event.target)) close();
    };
    window.addEventListener('resize', measure);
    window.addEventListener('scroll', measure, true);
    document.addEventListener('pointerdown', outside);
    return () => {
      window.removeEventListener('resize', measure);
      window.removeEventListener('scroll', measure, true);
      document.removeEventListener('pointerdown', outside);
    };
  }, [open]);

  useEffect(() => {
    if (!open || !expanded) return undefined;
    let cancelled = false;
    if ((expanded === 'files' && !topicId) || (expanded === 'gateway' && Number(agentUid || 0) <= 0)) {
      setContent({ loading: false, items: [], error: '', hasMore: false });
      return undefined;
    }
    setContent({ loading: true, items: [], error: '', hasMore: false });
    const timeout = window.setTimeout(() => {
      cancelled = true;
      setContent({ loading: false, items: [], error: '加载超时，请重试', hasMore: false });
    }, 12000);
    const load = async () => {
      try {
        const response = expanded === 'files'
          ? await api.getTopicFiles(topicId, { limit: 12 })
          : await api.listArtifactApps(agentUid);
        if (cancelled) return;
        const items = expanded === 'files' ? response?.files : (Array.isArray(response) ? response : response?.apps);
        setContent({ loading: false, items: recentItems(Array.isArray(items) ? items : []), error: '', hasMore: Boolean(response?.has_more) });
      } catch {
        if (!cancelled) setContent({ loading: false, items: [], error: expanded === 'files' ? '历史文件加载失败' : '应用加载失败', hasMore: false });
      } finally {
        window.clearTimeout(timeout);
      }
    };
    load();
    return () => { cancelled = true; window.clearTimeout(timeout); };
  }, [open, expanded, agentUid, topicId, retry]);

  const choose = (section, item) => {
    onSelect?.({ initialTab: section, ...(item ? section === 'files' ? { file: item } : { app: item } : {}) });
    close();
    onChoose?.();
  };
  const openFullPanel = () => {
    onSelect?.({ initialTab: topicId ? 'files' : 'active' });
    close();
    onChoose?.();
  };
  const keyDown = event => {
    if (event.key === 'Tab') { event.stopPropagation(); return; }
    if (event.key === 'Escape') { event.preventDefault(); event.stopPropagation(); close(true); return; }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const buttons = [...panelRef.current?.querySelectorAll('button:not(:disabled)') || []];
    if (!buttons.length) return;
    event.preventDefault();
    const current = buttons.indexOf(document.activeElement);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1
      : (current + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length;
    buttons[next]?.focus();
  };

  return (
    <>
      <button ref={triggerRef} type="button" role={mobile ? 'menuitem' : undefined} className={className}
        disabled={!enabled} aria-label={enabled ? '打开产物' : '产物暂不可用'} aria-haspopup="dialog"
        aria-expanded={open} aria-controls={open ? id : undefined} title={!enabled ? '选择 Agent 后可查看产物' : undefined}
        onPointerEnter={event => {
          if (event.pointerType !== 'mouse' || mobile || !enabled) return;
          cancelTimer(); timerRef.current = window.setTimeout(() => openPicker(), 140);
        }}
        onPointerLeave={scheduleClose}
        onClick={openFullPanel}
        onKeyDown={event => {
          if (event.key === 'ArrowDown') { event.preventDefault(); pinnedRef.current = true; openPicker(true); }
          if (event.key === 'Escape') close(true);
        }}>
        <Cloud size={mobile ? 16 : 17} aria-hidden="true" />
        {mobile && <span>{enabled ? '打开产物' : '产物暂不可用'}</span>}
      </button>
      {open && position && createPortal(
        <section ref={panelRef} id={id} className="cc-artifact-picker" style={position} role="dialog" aria-label="选择产物"
          onPointerEnter={cancelTimer} onPointerLeave={scheduleClose} onPointerDown={event => event.stopPropagation()}
          onKeyDown={keyDown} onBlur={event => {
            if (!event.currentTarget.contains(event.relatedTarget) && event.relatedTarget !== triggerRef.current) close();
          }}>
          {[{ key: 'files', label: '历史文件' }, { key: 'gateway', label: '应用' }].map(({ key, label }) => {
            const active = expanded === key;
            const unavailable = key === 'files' ? !topicId : Number(agentUid || 0) <= 0;
            return <div className="cc-artifact-picker-group" key={key}>
              <button type="button" className="cc-artifact-picker-heading" aria-expanded={active} aria-controls={`${id}-${key}`}
                onClick={() => { setContent({ loading: true, items: [], error: '', hasMore: false }); setExpanded(active ? '' : key); }}>
                <span>{label}</span><ChevronDown size={15} aria-hidden="true" />
              </button>
              {active && <div id={`${id}-${key}`} className="cc-artifact-picker-content">
                {unavailable ? <p>{key === 'files' ? '进入会话后查看历史文件' : '当前会话没有关联 Agent'}</p>
                  : content.loading ? <p role="status"><RefreshCw size={14} className="is-spinning" aria-hidden="true" />正在加载…</p>
                  : content.error ? <div className="cc-artifact-picker-error"><p role="alert">{content.error}</p><button type="button" onClick={() => setRetry(value => value + 1)}>重试</button></div>
                  : <>
                    {content.items.length === 0 ? <p>{key === 'files' ? '当前会话还没有文件' : '这个 Agent 还没有共享应用'}</p>
                      : <div className="cc-artifact-picker-list">{content.items.map((item, index) => <button type="button" key={item.id || `${item.url}:${index}`}
                        className="cc-artifact-picker-item" disabled={!item.url || (key === 'gateway' && !item.id)}
                        onClick={() => choose(key, item)} title={item.title || item.name || item.id}>
                        <span>{item.title || item.name || item.id || '未命名文件'}</span>
                      </button>)}</div>}
                    <button type="button" className="cc-artifact-picker-all" onClick={() => choose(key)}>{content.hasMore ? '查看全部历史文件' : `查看全部${label === '应用' ? '应用' : '历史文件'}`}</button>
                  </>}
              </div>}
            </div>;
          })}
        </section>, document.body,
      )}
    </>
  );
}
