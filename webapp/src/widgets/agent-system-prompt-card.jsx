import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import AgentPersonaEditor from './agent-persona-editor';
import SlidingSegments from './sliding-segments';
import { createPortal } from 'react-dom';
import {
  AlertTriangle,
  Check,
  CheckCircle2,
  Cloud,
  FileText,
  LoaderCircle,
  PencilLine,
  RefreshCw,
  X,
} from 'lucide-react';
import { api } from '../api';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import { readPersona, readReplyPriority } from '../utils/agent-persona';
import {
  MAX_SYSTEM_PROMPT_BYTES,
  normalizePromptDefinition,
  normalizePromptApplication,
  promptByteLength,
  resolvePromptApplicationState,
} from '../utils/system-prompt';

function agentUID(agent) {
  return String(agent?.uid || agent?.id || '');
}

function PromptStatus({ state }) {
  const Icon = state.kind === 'applied'
    ? CheckCircle2
    : state.kind === 'failed'
      ? AlertTriangle
      : state.kind === 'pending'
        ? Cloud
        : LoaderCircle;
  return (
    <span className={`cc-agent-prompt-status is-${state.kind}`} role="status" title={state.label}>
      <Icon size={14} aria-hidden="true" />
      {state.kind === 'applied' ? '已生效' : state.label}
    </span>
  );
}

export default function AgentSystemPromptCard({ agent, ownerUID, onAvatarApplied }) {
  const feedback = useFeedback();
  const uid = agentUID(agent);
  const [remote, setRemote] = useState(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [editorOpen, setEditorOpen] = useState(false);
  const [draft, setDraft] = useState('');
  const [incompleteRule, setIncompleteRule] = useState(false);
  const [tab, setTab] = useState('reply');
  const [editorVersion, setEditorVersion] = useState(0);
  const [portraitState, setPortraitState] = useState('');
  const [conflict, setConflict] = useState(null);
  const mountedRef = useRef(true);
  const requestRef = useRef(0);
  const editorDialogRef = useRef(null);
  const editorTitleRef = useRef(null);
  const editorOpenerRef = useRef(null);

  const savedPrompt = useMemo(() => normalizePromptDefinition(remote), [remote]);
  const savedPriority = useMemo(() => readReplyPriority(
    readPersona(savedPrompt.customSystemPrompt).description.split(/\r\n|\r|\n/),
  ), [savedPrompt]);
  const applyState = resolvePromptApplicationState(remote);
  const byteCount = useMemo(() => promptByteLength(draft), [draft]);
  const promptTooLarge = byteCount > MAX_SYSTEM_PROMPT_BYTES;
  const customPromptEmpty = !draft.trim();
  const editorDirty = savedPrompt.selected !== 'custom'
    || draft !== savedPrompt.customSystemPrompt;
  const editorContentDirty = draft !== savedPrompt.customSystemPrompt;

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestRef.current += 1;
    };
  }, []);

  const loadPrompt = useCallback(async ({ silent = false } = {}) => {
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    if (!uid) {
      setRemote(null);
      setLoading(false);
      return null;
    }
    if (!silent) {
      setLoading(true);
      setError('');
    }
    try {
      const [viewerResponse, ownerResponse] = await Promise.all([
        api.getAgentPrompt(uid).catch(err => {
          if (silent) throw err;
          return null;
        }),
        api.getBotDefinitionPrompt(uid),
      ]);
      if (!mountedRef.current || requestID !== requestRef.current) return null;
      const response = {
        ...ownerResponse,
        application: normalizePromptApplication(
          viewerResponse?.application ? viewerResponse : ownerResponse,
        ),
      };
      setRemote(response);
      return response;
    } catch (cause) {
      if (!mountedRef.current || requestID !== requestRef.current) return null;
      if (!silent) setError(cause?.message || '暂时无法读取行为设定');
      return null;
    } finally {
      if (mountedRef.current && requestID === requestRef.current && !silent) setLoading(false);
    }
  }, [uid]);

  useEffect(() => {
    setRemote(null);
    setEditorOpen(false);
    setDraft('');
    setConflict(null);
    setSaving(false);
    loadPrompt().catch(() => {});
  }, [loadPrompt]);

  useEffect(() => {
    if (!remote?.configured || saving || editorOpen || applyState.kind !== 'pending') return undefined;
    let cancelled = false;
    let timer;
    const poll = async () => {
      const latest = await loadPrompt({ silent: true });
      if (!cancelled && (!latest || (latest.configured && resolvePromptApplicationState(latest).kind === 'pending'))) {
        timer = window.setTimeout(poll, 4000);
      }
    };
    timer = window.setTimeout(poll, 4000);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [applyState.kind, editorOpen, loadPrompt, remote?.configured, saving]);

  const savePrompt = useCallback(async (selected, customSystemPrompt, revision = remote?.revision) => {
    if (!uid || !remote?.configured || saving) return false;
    const requestID = requestRef.current + 1;
    requestRef.current = requestID;
    const prompt = {
      selected,
      ...(customSystemPrompt.trim() ? { customSystemPrompt } : {}),
    };
    setSaving(true);
    setError('');
    try {
      const ownerResponse = await api.updateBotDefinitionPrompt(
        uid,
        Number(revision || 0),
        prompt,
      );
      if (!mountedRef.current || requestID !== requestRef.current) return false;
      const viewerResponse = await api.getAgentPrompt(uid).catch(() => null);
      if (!mountedRef.current || requestID !== requestRef.current) return false;
      const response = {
        ...ownerResponse,
        application: normalizePromptApplication(
          viewerResponse?.application ? viewerResponse : ownerResponse,
        ),
      };
      setRemote(response);
      setConflict(null);
      feedback.notify({
        tone: 'success',
        message: selected === 'custom'
          ? '助手设定已保存'
          : '已恢复使用默认设定',
      });
      return true;
    } catch (cause) {
      if (!mountedRef.current || requestID !== requestRef.current) return false;
      if (cause?.status === 409) {
        const latest = await api.getBotDefinitionPrompt(uid).catch(() => null);
        if (!mountedRef.current || requestID !== requestRef.current) return false;
        if (selected === 'custom') {
          setConflict({ latest });
        } else {
          if (latest) setRemote(latest);
          setError('设定已在别处更新，请重新选择。');
        }
      } else {
        setError(cause?.message || '保存行为设定失败');
      }
      return false;
    } finally {
      if (mountedRef.current && requestID === requestRef.current) setSaving(false);
    }
  }, [feedback, remote, saving, uid]);

  const chooseDefault = () => {
    if (loading || saving || !remote?.configured || savedPrompt.selected === 'default') return;
    savePrompt('default', savedPrompt.customSystemPrompt).catch(() => {});
  };

  const initializeDraft = (text) => {
    setPortraitState('');
    setDraft(text);
    setIncompleteRule(readPersona(text).rules.some(rule => rule.occasion.trim() && !rule.instruction.trim()));
    setTab('reply');
    setEditorVersion(version => version + 1);
  };

  const openEditor = (event) => {
    if (loading || saving || !remote?.configured) return;
    requestRef.current += 1;
    editorOpenerRef.current = event.currentTarget;
    initializeDraft(savedPrompt.customSystemPrompt);
    setConflict(null);
    setEditorOpen(true);
    setError('');
  };

  const closeEditor = useCallback(async () => {
    if (saving || portraitState === 'applying') return;
    const requestID = requestRef.current;
    if (editorContentDirty || portraitState) {
      const confirmed = await feedback.confirm({
        title: portraitState ? '关闭助手设定？' : '放弃未保存的修改？',
        message: [editorContentDirty && '尚未保存的设定将丢失。',
          portraitState === 'generating' ? '关闭会停止生成并清除形象草稿。' : portraitState && '尚未采用的形象或调整想法将丢失。'].filter(Boolean).join(''),
        confirmLabel: '放弃修改',
        cancelLabel: '继续编辑',
        tone: 'danger',
      });
      if (!confirmed || !mountedRef.current || requestID !== requestRef.current) return;
    }
    requestRef.current += 1;
    if (conflict?.latest) setRemote(conflict.latest);
    if (conflict && !conflict.latest) loadPrompt();
    setConflict(null);
    setEditorOpen(false);
  }, [conflict, editorContentDirty, feedback, loadPrompt, saving, portraitState]);

  const loadConflictingDraft = async () => {
    if (!conflict || saving || portraitState === 'applying') return;
    const requestID = ++requestRef.current;
    if ((editorContentDirty || portraitState) && !await feedback.confirm({
      title: '载入最新版？',
      message: '载入后将替换这里尚未保存的修改。' + (portraitState ? '形象草稿也会清除。' : ''),
      confirmLabel: '载入最新版',
    })) return;
    if (!mountedRef.current || requestID !== requestRef.current) return;
    setSaving(true);
    try {
      const latest = conflict.latest || await api.getBotDefinitionPrompt(uid);
      if (!mountedRef.current || requestID !== requestRef.current) return;
      setRemote(latest);
      initializeDraft(normalizePromptDefinition(latest).customSystemPrompt);
      setConflict(null);
      setError('');
    } catch (cause) {
      if (mountedRef.current && requestID === requestRef.current) setError(cause?.message || '读取最新版失败，请重试。');
    } finally {
      if (mountedRef.current && requestID === requestRef.current) setSaving(false);
    }
  };

  const saveCustomPrompt = async () => {
    if (!editorDirty || promptTooLarge || customPromptEmpty || incompleteRule || portraitState === 'applying') return;
    const requestID = requestRef.current;
    if (conflict && (!conflict.latest || !await feedback.confirm({
      title: '用当前草稿覆盖最新版？',
      message: '其他地方保存的修改将被这份草稿替换。',
      confirmLabel: '覆盖并保存',
      tone: 'danger',
    }))) return;
    if (!mountedRef.current || requestID !== requestRef.current) return;
    const saved = await savePrompt('custom', draft, conflict?.latest?.revision);
    if (saved) {
      if (portraitState) editorTitleRef.current?.focus({ preventScroll: true });
      else setEditorOpen(false);
    }
  };

  useEffect(() => {
    if (!editorOpen) return undefined;
    const frame = window.requestAnimationFrame(() => editorTitleRef.current?.focus());
    return () => {
      window.cancelAnimationFrame(frame);
      editorOpenerRef.current?.focus();
    };
  }, [editorOpen]);

  useEffect(() => {
    if (!editorOpen) return undefined;
    const handleKeyDown = (event) => {
      if (event.defaultPrevented || event.isComposing || event.keyCode === 229 || document.querySelector('.cc-confirm-overlay')) return;
      const inside = editorDialogRef.current?.contains(event.target);
      if (!inside && event.target !== document.body) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        closeEditor().catch(() => {});
        return;
      }
      if (event.key !== 'Tab' || !editorDialogRef.current) return;
      const focusable = Array.from(editorDialogRef.current.querySelectorAll(
        'button:not(:disabled), input:not(:disabled), textarea:not(:disabled), a[href], summary, [tabindex]:not([tabindex="-1"])',
      )).filter((element) => element.tabIndex >= 0 && element.getClientRects().length && element.getAttribute('aria-hidden') !== 'true');
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (!inside) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      } else if (event.shiftKey && (document.activeElement === first || document.activeElement === editorTitleRef.current)) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [closeEditor, editorOpen]);

  const modeLabel = savedPrompt.selected === 'custom' ? savedPriority?.label || '自定义设定' : '默认设定';

  return (
    <section className="cc-agent-behavior-card" aria-labelledby="cc-agent-behavior-title">
      <div className="cc-agent-behavior-heading">
        <div className="cc-agent-behavior-icon" aria-hidden="true"><FileText size={18} /></div>
        <div className="cc-agent-behavior-copy">
          <h3 id="cc-agent-behavior-title">助手设定</h3>
          <p>选择你喜欢的回复方式，也可以定制助手的性格和习惯。</p>
        </div>
        {!loading && remote?.configured && <PromptStatus state={applyState} />}
      </div>

      {error && !editorOpen && <InlineFeedback tone="error">{error}</InlineFeedback>}

      {loading ? (
        <div className="cc-agent-behavior-loading" role="status">
          <LoaderCircle className="spin" size={16} /> 正在读取行为设定
        </div>
      ) : !remote ? (
        <button type="button" className="cc-agent-behavior-retry" onClick={() => loadPrompt()}>
          <RefreshCw size={14} /> 重新读取
        </button>
      ) : !remote.configured ? (
        <div className="cc-agent-behavior-unconfigured">
          <span>启动这个 Agent 的 XiaoBa 后即可设置。</span>
          <button type="button" onClick={() => loadPrompt()} aria-label="刷新行为设定">
            <RefreshCw size={14} />
          </button>
        </div>
      ) : (
        <div className="cc-agent-behavior-controls">
          <div className="cc-agent-behavior-current">
            <span>当前使用</span>
            <strong>{modeLabel}</strong>
          </div>
          <SlidingSegments value={savedPrompt.selected} className="cc-agent-behavior-mode" role="group" aria-label="助手设定模式">
            <button
              type="button"
              className={savedPrompt.selected === 'default' ? 'active' : ''}
              aria-pressed={savedPrompt.selected === 'default'}
              disabled={saving}
              onClick={chooseDefault}
            >
              {saving && savedPrompt.selected === 'custom'
                ? <LoaderCircle className="spin" size={14} />
                : savedPrompt.selected === 'default' && <Check size={14} />}
              使用默认
            </button>
            <button
              type="button"
              className={savedPrompt.selected === 'custom' ? 'active' : ''}
              aria-pressed={savedPrompt.selected === 'custom'}
              disabled={saving}
              onClick={openEditor}
            >
              <PencilLine size={14} />
              {savedPrompt.selected === 'custom' ? '编辑设定' : '自定义'}
            </button>
          </SlidingSegments>
        </div>
      )}

      {editorOpen && typeof document !== 'undefined' && createPortal(
        <div
          className="oc-modal-overlay cc-agent-prompt-editor-overlay"
          onMouseDown={(event) => {
            event.stopPropagation();
            if (event.target === event.currentTarget) closeEditor().catch(() => {});
          }}
        >
          <section
            ref={editorDialogRef}
            className="oc-modal cc-agent-prompt-editor-dialog"
            role="dialog"
            aria-modal="true"
            aria-labelledby="cc-agent-prompt-editor-title"
            aria-describedby="cc-agent-prompt-editor-description"
            onMouseDown={(event) => event.stopPropagation()}
          >
            <header className="cc-agent-prompt-editor-header">
              <div>
                <h2 ref={editorTitleRef} tabIndex={-1} id="cc-agent-prompt-editor-title">助手设定</h2>
                <p id="cc-agent-prompt-editor-description">
                  {agent.display_name || agent.username} · 保存后用于新会话
                </p>
              </div>
              <button type="button" disabled={saving || portraitState === 'applying'} onClick={() => closeEditor()} aria-label="关闭助手设定">
                <X size={18} />
              </button>
            </header>

            {error && <InlineFeedback tone="error">{error}</InlineFeedback>}
            {conflict && <InlineFeedback tone="warning">
              设定已在别处更新，你的草稿已保留。
              <button type="button" className="cc-persona-reload" disabled={saving || portraitState === 'applying'} onClick={loadConflictingDraft}>载入最新版</button>
            </InlineFeedback>}

            <AgentPersonaEditor key={editorVersion} uid={uid} ownerUID={ownerUID} agent={agent} onAvatarApplied={onAvatarApplied} onPortraitStateChange={setPortraitState}
              saved={savedPrompt.selected === 'custom' ? savedPrompt.customSystemPrompt : ''} draft={draft} tab={tab} onTabChange={setTab} onChange={(text, incomplete) => {
              setDraft(text);
              setIncompleteRule(incomplete);
            }} saving={saving} />
            {promptTooLarge && <InlineFeedback tone="error">内容过长，请精简后再保存（最大 1 MiB）。</InlineFeedback>}

            <footer className="cc-agent-prompt-editor-actions">
              {incompleteRule ? (
                <button type="button" className="cc-persona-fix-rule" onClick={() => {
                  setTab('rules');
                  window.requestAnimationFrame(() => editorDialogRef.current?.querySelector('textarea[data-incomplete="true"]')?.focus());
                }}>补全场景约定</button>
              ) : (
                <span role="status">{saving ? '正在处理…' : editorContentDirty ? '有未保存的修改' : savedPrompt.selected === 'custom' ? '内容已保存' : customPromptEmpty ? '选一项或写一句即可保存' : '保存后启用自定义设定'}</span>
              )}
              <div>
                <button type="button" className="oc-btn oc-btn-default" disabled={saving || portraitState === 'applying'} onClick={() => closeEditor()}>
                  取消
                </button>
                <button
                  type="button"
                  className="oc-btn oc-btn-primary"
                  disabled={!editorDirty || saving || promptTooLarge || customPromptEmpty || incompleteRule || portraitState === 'applying' || Boolean(conflict && !conflict.latest)}
                  onClick={saveCustomPrompt}
                >
                  {saving ? '处理中' : conflict ? '覆盖并保存' : '保存修改'}
                </button>
              </div>
            </footer>
          </section>
        </div>,
        document.body,
      )}
    </section>
  );
}
