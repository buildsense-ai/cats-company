import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  Bot,
  Check,
  CheckCircle2,
  Cloud,
  FileText,
  LoaderCircle,
  RefreshCw,
  Save,
} from 'lucide-react';
import { api } from '../api';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import { normalizeOwnedBots } from '../utils/owned-bots';
import '../css/system-prompt-view.css';

export const MAX_SYSTEM_PROMPT_BYTES = 1024 * 1024;
const SELECTED_BOT_STORAGE_PREFIX = 'catsco.systemPrompt.selectedBot';

function botUID(bot) {
  return bot?.uid ?? bot?.id ?? '';
}

function botLabel(bot) {
  return bot?.display_name || bot?.displayName || bot?.username || `Agent ${botUID(bot)}`;
}

function storageKey(userUID) {
  const uid = String(userUID || '').trim();
  return uid ? `${SELECTED_BOT_STORAGE_PREFIX}.${uid}` : '';
}

function browserStorage() {
  try {
    return globalThis.localStorage;
  } catch {
    return null;
  }
}

export function promptByteLength(value) {
  const text = String(value || '');
  if (typeof TextEncoder === 'function') return new TextEncoder().encode(text).length;
  return unescape(encodeURIComponent(text)).length;
}

export function normalizePromptDefinition(response) {
  const prompt = response?.definition?.prompt || {};
  const selected = prompt.selected === 'custom' ? 'custom' : 'default';
  return {
    selected,
    customSystemPrompt: String(prompt.customSystemPrompt || ''),
  };
}

export function resolvePromptApplyState(response) {
  if (!response?.configured) return { kind: 'unconfigured', label: '等待初始化' };
  const revision = Number(response?.revision || 0);
  const runtime = response?.runtime || {};
  const attemptedRevision = Number(runtime.lastAttemptRevision || 0);
  const appliedRevision = Number(runtime.appliedRevision || 0);
  const hasApplicationEvidence = Boolean(
    runtime.appliedAt || runtime.appliedKind || runtime.appliedModelId,
  );
  if (attemptedRevision === revision && runtime.lastError) {
    return { kind: 'error', label: '应用失败', detail: String(runtime.lastError) };
  }
  if (appliedRevision === revision && (revision > 0 || hasApplicationEvidence)) {
    return { kind: 'applied', label: '已生效', detail: runtime.appliedAt || '' };
  }
  return { kind: 'pending', label: '待应用' };
}

function readRememberedBotUID(userUID, storage = browserStorage()) {
  const key = storageKey(userUID);
  if (!key || !storage) return '';
  try {
    return String(storage.getItem(key) || '').trim();
  } catch {
    return '';
  }
}

function rememberBotUID(userUID, uid, storage = browserStorage()) {
  const key = storageKey(userUID);
  if (!key || !storage) return;
  try {
    if (uid) storage.setItem(key, String(uid));
    else storage.removeItem(key);
  } catch {
    // Storage is optional in hardened browser contexts.
  }
}

function StatusBadge({ state }) {
  const Icon = state.kind === 'applied'
    ? CheckCircle2
    : state.kind === 'error'
      ? AlertTriangle
      : state.kind === 'pending'
        ? Cloud
        : LoaderCircle;
  return (
    <span className={`cc-system-prompt-status is-${state.kind}`} role="status">
      <Icon size={15} aria-hidden="true" />
      <span>{state.label}</span>
    </span>
  );
}

export default function SystemPromptView({ user, onDirtyChange, onSavingChange }) {
  const feedback = useFeedback();
  const [bots, setBots] = useState([]);
  const [selectedBotUID, setSelectedBotUID] = useState('');
  const [loadedBotUID, setLoadedBotUID] = useState('');
  const [remote, setRemote] = useState(null);
  const [mode, setMode] = useState('default');
  const [customPrompt, setCustomPrompt] = useState('');
  const [loadingBots, setLoadingBots] = useState(true);
  const [loadingPrompt, setLoadingPrompt] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [conflict, setConflict] = useState(false);
  const selectedBotUIDRef = useRef('');
  const dirtyRef = useRef(false);
  const loadRequestRef = useRef(0);
  const saveRequestRef = useRef(0);
  const mountedRef = useRef(true);
  const remoteVersionRef = useRef({ botUID: '', revision: 0 });

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      loadRequestRef.current += 1;
      saveRequestRef.current += 1;
    };
  }, [onDirtyChange]);

  useEffect(() => {
    selectedBotUIDRef.current = selectedBotUID;
  }, [selectedBotUID]);

  const savedPrompt = useMemo(() => normalizePromptDefinition(remote), [remote]);
  const dirty = Boolean(loadedBotUID && loadedBotUID === selectedBotUID) && (
    mode !== savedPrompt.selected
    || customPrompt !== savedPrompt.customSystemPrompt
  );
  const byteCount = useMemo(() => promptByteLength(customPrompt), [customPrompt]);
  const promptTooLarge = byteCount > MAX_SYSTEM_PROMPT_BYTES;
  const customPromptEmpty = mode === 'custom' && !customPrompt.trim();
  const selectedBot = bots.find((bot) => String(botUID(bot)) === selectedBotUID) || null;
  const applyState = resolvePromptApplyState(remote);
  const ready = Boolean(selectedBotUID && loadedBotUID === selectedBotUID && remote && !loadingPrompt);
  dirtyRef.current = dirty;

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  useEffect(() => {
    onSavingChange?.(saving);
  }, [onSavingChange, saving]);

  const applyRemote = useCallback((response, botUIDValue, { preserveDraft = false } = {}) => {
    const normalized = normalizePromptDefinition(response);
    remoteVersionRef.current = {
      botUID: botUIDValue,
      revision: Number(response?.revision || 0),
    };
    setRemote(response);
    setLoadedBotUID(botUIDValue);
    if (!preserveDraft) {
      setMode(normalized.selected);
      setCustomPrompt(normalized.customSystemPrompt);
    }
  }, []);

  const loadPrompt = useCallback(async (uid, options = {}) => {
    const requestedUID = String(uid || '');
    const silent = options.silent === true;
    const requestID = loadRequestRef.current + 1;
    loadRequestRef.current = requestID;
    if (!requestedUID) {
      remoteVersionRef.current = { botUID: '', revision: 0 };
      setRemote(null);
      setLoadedBotUID('');
      setLoadingPrompt(false);
      return null;
    }
    if (!silent) {
      setLoadingPrompt(true);
      setError('');
    }
    try {
      const response = await api.getBotDefinitionPrompt(requestedUID);
      if (!mountedRef.current
        || requestID !== loadRequestRef.current
        || selectedBotUIDRef.current !== requestedUID) return null;
      if (silent && dirtyRef.current) return null;
      const currentVersion = remoteVersionRef.current;
      if (currentVersion.botUID === requestedUID
        && Number(response?.revision || 0) < currentVersion.revision) return null;
      applyRemote(response, requestedUID, options);
      return response;
    } catch (cause) {
      if (!mountedRef.current
        || requestID !== loadRequestRef.current
        || selectedBotUIDRef.current !== requestedUID) return null;
      if (!silent) {
        setError(cause?.message || (options.preserveDraft
          ? '无法读取最新云端 revision，当前草稿仍保留'
          : '无法读取系统提示词配置'));
      }
      return null;
    } finally {
      if (mountedRef.current
        && !silent
        && requestID === loadRequestRef.current
        && selectedBotUIDRef.current === requestedUID) {
        setLoadingPrompt(false);
      }
    }
  }, [applyRemote]);

  useEffect(() => {
    let active = true;
    setLoadingBots(true);
    api.getMyBots()
      .then((response) => {
        if (!active) return;
        const owned = normalizeOwnedBots(response, user?.uid);
        setBots(owned);
        const remembered = readRememberedBotUID(user?.uid);
        const preferred = remembered && owned.some((bot) => String(botUID(bot)) === remembered)
          ? remembered
          : String(botUID(owned[0]) || '');
        selectedBotUIDRef.current = preferred;
        setSelectedBotUID(preferred);
      })
      .catch((cause) => active && setError(cause?.message || '无法读取 Agent 列表'))
      .finally(() => active && setLoadingBots(false));
    return () => {
      active = false;
    };
  }, [user?.uid]);

  useEffect(() => {
    setConflict(false);
    loadPrompt(selectedBotUID).catch(() => {});
  }, [loadPrompt, selectedBotUID]);

  useEffect(() => {
    if (!dirty) return undefined;
    const preventClose = (event) => {
      event.preventDefault();
      event.returnValue = '';
    };
    window.addEventListener('beforeunload', preventClose);
    return () => window.removeEventListener('beforeunload', preventClose);
  }, [dirty]);

  useEffect(() => {
    if (!ready || dirty || saving || applyState.kind !== 'pending') return undefined;
    let cancelled = false;
    let timer = null;
    const poll = async () => {
      await loadPrompt(selectedBotUID, { silent: true }).catch(() => {});
      if (!cancelled) timer = window.setTimeout(poll, 4000);
    };
    timer = window.setTimeout(poll, 4000);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [applyState.kind, dirty, loadPrompt, ready, saving, selectedBotUID]);

  const chooseBot = async (nextUID) => {
    if (saving || nextUID === selectedBotUID) return;
    if (dirty) {
      const confirmed = await feedback.confirm({
        title: '放弃未保存的修改？',
        message: '切换 Agent 后，当前系统提示词草稿不会保留。',
        confirmLabel: '放弃并切换',
        tone: 'danger',
      });
      if (!confirmed) return;
    }
    rememberBotUID(user?.uid, nextUID);
    loadRequestRef.current += 1;
    remoteVersionRef.current = { botUID: nextUID, revision: 0 };
    selectedBotUIDRef.current = nextUID;
    setSelectedBotUID(nextUID);
  };

  const save = async () => {
    if (!ready || saving || !dirty || promptTooLarge || customPromptEmpty) return;
    const requestedUID = selectedBotUIDRef.current;
    const expectedRevision = Number(remote?.revision || 0);
    const draft = {
      selected: mode,
      ...(customPrompt.trim() ? { customSystemPrompt: customPrompt } : {}),
    };
    const requestID = saveRequestRef.current + 1;
    saveRequestRef.current = requestID;
    // Ignore status requests that started before this newer write.
    loadRequestRef.current += 1;
    setSaving(true);
    setError('');
    setConflict(false);
    try {
      const response = await api.updateBotDefinitionPrompt(requestedUID, expectedRevision, draft);
      if (!mountedRef.current
        || requestID !== saveRequestRef.current
        || requestedUID !== selectedBotUIDRef.current) return;
      applyRemote(response, requestedUID);
      feedback.notify({ tone: 'success', message: '系统提示词已保存，正在等待 XiaoBa 应用。' });
    } catch (cause) {
      if (!mountedRef.current
        || requestID !== saveRequestRef.current
        || requestedUID !== selectedBotUIDRef.current) return;
      if (cause?.status === 409) {
        setConflict(true);
        const refreshed = await loadPrompt(requestedUID, { preserveDraft: true });
        if (!mountedRef.current || requestID !== saveRequestRef.current) return;
        setError(refreshed
          ? '云端配置已被其他操作更新。你的草稿仍保留，请核对后重新保存。'
          : '云端配置已更新，但暂时无法读取最新 revision。你的草稿仍保留，请稍后重新保存。');
      } else {
        setError(cause?.message || '保存系统提示词失败');
      }
    } finally {
      if (mountedRef.current && requestID === saveRequestRef.current) {
        setSaving(false);
      }
    }
  };

  return (
    <main className="cc-system-prompt-page">
      <div className="cc-system-prompt-shell">
        <header className="cc-system-prompt-header">
          <div>
            <span className="cc-system-prompt-kicker"><FileText size={14} /> Agent 配置</span>
            <h1>系统提示词</h1>
            <p>设置 Agent 在每次新会话中遵循的长期行为与工作边界。</p>
          </div>
          <label className="cc-system-prompt-agent-picker">
            <span><Bot size={15} /> Agent</span>
            <select
              value={selectedBotUID}
              disabled={loadingBots || saving || bots.length === 0}
              onChange={(event) => chooseBot(event.target.value)}
              aria-label="选择要配置的 Agent"
            >
              {bots.length === 0 && <option value="">暂无可管理的 Agent</option>}
              {bots.map((bot) => (
                <option key={botUID(bot)} value={String(botUID(bot))}>{botLabel(bot)}</option>
              ))}
            </select>
          </label>
        </header>

        {error && (
          <InlineFeedback tone={conflict ? 'warning' : 'error'} title={conflict ? '检测到配置冲突' : '操作未完成'}>
            {error}
          </InlineFeedback>
        )}

        {loadingBots || loadingPrompt ? (
          <div className="cc-system-prompt-loading" role="status">
            <LoaderCircle className="spin" size={20} /> 正在读取配置
          </div>
        ) : bots.length === 0 ? (
          <section className="cc-system-prompt-empty">
            <Bot size={24} />
            <h2>暂无可管理的 Agent</h2>
            <p>创建或绑定属于你的 Agent 后，即可在这里设置系统提示词。</p>
          </section>
        ) : selectedBotUID && !ready ? (
          <section className="cc-system-prompt-empty">
            <AlertTriangle size={24} />
            <h2>无法读取 Agent 配置</h2>
            <p>请检查网络连接后重试。已有云端配置不会受到影响。</p>
            <button
              type="button"
              className="cc-system-prompt-refresh"
              onClick={() => loadPrompt(selectedBotUID)}
            >
              <RefreshCw size={15} /> 重试
            </button>
          </section>
        ) : ready && !remote?.configured ? (
          <section className="cc-system-prompt-empty">
            <Cloud size={24} />
            <h2>Agent 配置尚未初始化</h2>
            <p>请先启动这个 Agent 的 XiaoBa，待云端配置初始化后刷新再设置系统提示词。</p>
            <button
              type="button"
              className="cc-system-prompt-refresh"
              onClick={() => loadPrompt(selectedBotUID)}
            >
              <RefreshCw size={15} /> 刷新
            </button>
          </section>
        ) : ready ? (
          <section className="cc-system-prompt-workspace">
            <div className="cc-system-prompt-summary">
              <div>
                <span>当前 Agent</span>
                <strong>{botLabel(selectedBot)}</strong>
              </div>
              <div>
                <span>云端 revision</span>
                <strong>{Number(remote?.revision || 0)}</strong>
              </div>
              <div>
                <span>运行状态</span>
                <StatusBadge state={applyState} />
              </div>
              <button
                type="button"
                className="cc-system-prompt-refresh"
                onClick={() => loadPrompt(selectedBotUID)}
                disabled={saving || dirty}
                title={dirty ? '请先保存或放弃当前修改' : '刷新配置'}
              >
                <RefreshCw size={15} /> 刷新
              </button>
            </div>

            {applyState.kind === 'error' && (
              <InlineFeedback tone="error" title="XiaoBa 未能应用当前 revision">
                已保留上一个可用配置。请检查 XiaoBa 运行状态后重试。
              </InlineFeedback>
            )}

            <div className="cc-system-prompt-mode" role="radiogroup" aria-label="系统提示词模式">
              <button
                type="button"
                role="radio"
                aria-checked={mode === 'default'}
                className={mode === 'default' ? 'active' : ''}
                disabled={saving}
                onClick={() => setMode('default')}
              >
                <span className="cc-system-prompt-mode-check">{mode === 'default' && <Check size={15} />}</span>
                <span><strong>默认提示词</strong><small>使用当前 XiaoBa 版本内置的系统提示词</small></span>
              </button>
              <button
                type="button"
                role="radio"
                aria-checked={mode === 'custom'}
                className={mode === 'custom' ? 'active' : ''}
                disabled={saving}
                onClick={() => setMode('custom')}
              >
                <span className="cc-system-prompt-mode-check">{mode === 'custom' && <Check size={15} />}</span>
                <span><strong>自定义提示词</strong><small>为这个 Agent 保存独立的纯文本或 Markdown</small></span>
              </button>
            </div>

            <div className={`cc-system-prompt-editor${mode === 'default' ? ' is-disabled' : ''}`}>
              <div className="cc-system-prompt-editor-heading">
                <label htmlFor="cc-system-prompt-text">自定义内容</label>
                <span className={promptTooLarge ? 'is-error' : ''}>
                  {byteCount.toLocaleString()} / {MAX_SYSTEM_PROMPT_BYTES.toLocaleString()} 字节
                </span>
              </div>
              <textarea
                id="cc-system-prompt-text"
                value={customPrompt}
                disabled={mode === 'default' || saving}
                onChange={(event) => setCustomPrompt(event.target.value)}
                placeholder="输入这个 Agent 在每次新会话中都应遵循的角色、边界与工作规则..."
                spellCheck="false"
              />
              {mode === 'default' && <p>当前使用 XiaoBa 内置默认提示词，自定义内容不会参与运行。</p>}
              {customPromptEmpty && <p className="is-error">自定义模式下提示词不能为空。</p>}
              {promptTooLarge && <p className="is-error">内容超过后端允许的 1 MiB 限制。</p>}
            </div>

            <footer className="cc-system-prompt-actions">
              <div>
                <strong>{dirty ? '有未保存的修改' : '云端配置已同步'}</strong>
                <span>{mode === 'custom' ? '保存后由 XiaoBa 拉取并应用到新会话。' : '保存后新会话将恢复使用内置默认提示词。'}</span>
              </div>
              <button
                type="button"
                className="primary"
                onClick={save}
                disabled={!dirty || saving || promptTooLarge || customPromptEmpty}
              >
                {saving ? <LoaderCircle className="spin" size={16} /> : <Save size={16} />}
                {saving ? '保存中' : '保存修改'}
              </button>
            </footer>
          </section>
        ) : null}
      </div>
    </main>
  );
}
