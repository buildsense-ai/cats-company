import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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
  Save,
  X,
} from 'lucide-react';
import { api } from '../api';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import {
  MAX_SYSTEM_PROMPT_BYTES,
  normalizePromptDefinition,
  promptByteLength,
  resolvePromptApplyState,
} from '../utils/system-prompt';

function agentUID(agent) {
  return String(agent?.uid || agent?.id || '');
}

function PromptStatus({ state }) {
  const Icon = state.kind === 'applied'
    ? CheckCircle2
    : state.kind === 'error'
      ? AlertTriangle
      : state.kind === 'pending'
        ? Cloud
        : LoaderCircle;
  return (
    <span className={`cc-agent-prompt-status is-${state.kind}`} role="status">
      <Icon size={14} aria-hidden="true" />
      {state.label}
    </span>
  );
}

export default function AgentSystemPromptCard({ agent }) {
  const feedback = useFeedback();
  const uid = agentUID(agent);
  const [remote, setRemote] = useState(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [editorOpen, setEditorOpen] = useState(false);
  const [draft, setDraft] = useState('');
  const mountedRef = useRef(true);
  const requestRef = useRef(0);
  const editorDialogRef = useRef(null);
  const editorTextareaRef = useRef(null);
  const editorOpenerRef = useRef(null);

  const savedPrompt = useMemo(() => normalizePromptDefinition(remote), [remote]);
  const applyState = resolvePromptApplyState(remote);
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
      const response = await api.getBotDefinitionPrompt(uid);
      if (!mountedRef.current || requestID !== requestRef.current) return null;
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
    loadPrompt().catch(() => {});
  }, [loadPrompt]);

  useEffect(() => {
    if (!remote?.configured || saving || editorOpen || applyState.kind !== 'pending') return undefined;
    const timer = window.setTimeout(() => loadPrompt({ silent: true }).catch(() => {}), 4000);
    return () => window.clearTimeout(timer);
  }, [applyState.kind, editorOpen, loadPrompt, remote?.configured, saving]);

  const savePrompt = useCallback(async (selected, customSystemPrompt) => {
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
      const response = await api.updateBotDefinitionPrompt(
        uid,
        Number(remote?.revision || 0),
        prompt,
      );
      if (!mountedRef.current || requestID !== requestRef.current) return false;
      setRemote(response);
      feedback.notify({
        tone: 'success',
        message: selected === 'custom'
          ? '自定义系统提示词已保存'
          : '已恢复使用默认系统提示词',
      });
      return true;
    } catch (cause) {
      if (!mountedRef.current || requestID !== requestRef.current) return false;
      if (cause?.status === 409) {
        setError('行为设定已在别处更新，已重新读取最新版本。请核对后再保存。');
        const latest = await api.getBotDefinitionPrompt(uid).catch(() => null);
        if (latest && mountedRef.current && requestID === requestRef.current) setRemote(latest);
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

  const openEditor = (event) => {
    if (loading || saving || !remote?.configured) return;
    editorOpenerRef.current = event.currentTarget;
    setDraft(savedPrompt.customSystemPrompt);
    setEditorOpen(true);
    setError('');
  };

  const closeEditor = useCallback(async () => {
    if (saving) return;
    if (editorContentDirty) {
      const confirmed = await feedback.confirm({
        title: '放弃未保存的修改？',
        message: '关闭编辑器后，本次系统提示词修改不会保留。',
        confirmLabel: '放弃修改',
        tone: 'danger',
      });
      if (!confirmed) return;
    }
    setEditorOpen(false);
  }, [editorContentDirty, feedback, saving]);

  const saveCustomPrompt = async () => {
    if (!editorDirty || promptTooLarge || customPromptEmpty) return;
    const saved = await savePrompt('custom', draft);
    if (saved) setEditorOpen(false);
  };

  useEffect(() => {
    if (!editorOpen) return undefined;
    const frame = window.requestAnimationFrame(() => editorTextareaRef.current?.focus());
    const handleKeyDown = (event) => {
      if (!event.target.closest?.('.cc-agent-prompt-editor-dialog')) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopPropagation();
        closeEditor().catch(() => {});
        return;
      }
      if (event.key !== 'Tab' || !editorDialogRef.current) return;
      const focusable = Array.from(editorDialogRef.current.querySelectorAll(
        'button:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      )).filter((element) => !element.hidden && element.getAttribute('aria-hidden') !== 'true');
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      window.cancelAnimationFrame(frame);
      document.removeEventListener('keydown', handleKeyDown);
      if (editorOpenerRef.current instanceof HTMLElement) editorOpenerRef.current.focus();
    };
  }, [closeEditor, editorOpen]);

  const modeLabel = savedPrompt.selected === 'custom' ? '已设置自定义提示词' : '使用 XiaoBa 默认提示词';

  return (
    <section className="cc-agent-behavior-card" aria-labelledby="cc-agent-behavior-title">
      <div className="cc-agent-behavior-heading">
        <div className="cc-agent-behavior-icon" aria-hidden="true"><FileText size={18} /></div>
        <div className="cc-agent-behavior-copy">
          <h3 id="cc-agent-behavior-title">行为设定</h3>
          <p>设置这个 Agent 在每次新会话中遵循的长期角色、边界与工作规则。</p>
        </div>
        {!loading && remote?.configured && <PromptStatus state={applyState} />}
      </div>

      {error && <InlineFeedback tone="error">{error}</InlineFeedback>}

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
            <span>系统提示词</span>
            <strong>{modeLabel}</strong>
          </div>
          <div className="cc-agent-behavior-mode" role="group" aria-label="系统提示词模式">
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
              {savedPrompt.selected === 'custom' ? '编辑内容' : '自定义'}
            </button>
          </div>
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
                <span><FileText size={15} aria-hidden="true" /> 行为设定</span>
                <h2 id="cc-agent-prompt-editor-title">编辑系统提示词</h2>
                <p id="cc-agent-prompt-editor-description">
                  这些规则会在这个 Agent 的每次新会话中生效。
                </p>
              </div>
              <button type="button" onClick={() => closeEditor()} aria-label="关闭系统提示词编辑器">
                <X size={18} />
              </button>
            </header>

            {error && <InlineFeedback tone="error">{error}</InlineFeedback>}

            <div className="cc-agent-prompt-editor-field">
              <div>
                <label htmlFor="cc-agent-prompt-editor-text">自定义内容</label>
                <span className={promptTooLarge ? 'is-error' : ''}>
                  {byteCount.toLocaleString()} / {MAX_SYSTEM_PROMPT_BYTES.toLocaleString()} 字节
                </span>
              </div>
              <textarea
                ref={editorTextareaRef}
                id="cc-agent-prompt-editor-text"
                value={draft}
                disabled={saving}
                onChange={(event) => setDraft(event.target.value)}
                placeholder="输入这个 Agent 应遵循的角色、边界、工作方式与限制..."
                spellCheck="false"
              />
              {customPromptEmpty && <p className="is-error">自定义系统提示词不能为空。</p>}
              {promptTooLarge && <p className="is-error">内容超过后端允许的 1 MiB 限制。</p>}
            </div>

            <footer className="cc-agent-prompt-editor-actions">
              <span>{editorDirty ? '有未保存的修改' : '内容已保存'}</span>
              <div>
                <button type="button" className="oc-btn oc-btn-default" disabled={saving} onClick={() => closeEditor()}>
                  取消
                </button>
                <button
                  type="button"
                  className="oc-btn oc-btn-primary"
                  disabled={!editorDirty || saving || promptTooLarge || customPromptEmpty}
                  onClick={saveCustomPrompt}
                >
                  {saving ? <LoaderCircle className="spin" size={15} /> : <Save size={15} />}
                  {saving ? '保存中' : '保存修改'}
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
