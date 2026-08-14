import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  Bot,
  Check,
  CheckCircle2,
  Cloud,
  Eye,
  FileText,
  LoaderCircle,
  Lock,
  RefreshCw,
  Save,
} from 'lucide-react';
import { api } from '../api';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
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

export function normalizePromptBots(response) {
  const agents = Array.isArray(response) ? response : (response?.agents || []);
  return agents.filter((agent) => (
    agent
    && botUID(agent) !== ''
    && agent?.is_bot !== false
    && agent?.isBot !== false
  ));
}

export function normalizeAgentPrompt(response) {
  const selected = response?.selected === 'custom' ? 'custom' : 'default';
  const visibility = response?.promptVisibility ?? response?.prompt_visibility;
  const promptVisibility = visibility === 'friends' ? 'friends' : 'owner';
  return {
    ...response,
    canEdit: (response?.canEdit ?? response?.can_edit) === true,
    content: String(response?.content || ''),
    contentAvailable: (response?.contentAvailable ?? response?.content_available) !== false,
    defaultContent: String(response?.defaultContent ?? response?.default_content ?? ''),
    defaultContentAvailable: (
      response?.defaultContentAvailable ?? response?.default_content_available
    ) !== false,
    defaultSnapshot: response?.defaultSnapshot || response?.default_snapshot || null,
    application: normalizePromptApplication(response),
    promptVisibility,
    relation: response?.relation === 'owner' ? 'owner' : 'friend',
    revision: Number(response?.revision || 0),
    selected,
  };
}

function numberOrZero(value) {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : 0;
}

function firstValue(...values) {
  return values.find((value) => value !== undefined && value !== null && value !== '') ?? '';
}

/**
 * Normalize the viewer application contract while retaining a fallback for
 * older BotDefinition responses that expose only the runtime acknowledgement.
 */
export function normalizePromptApplication(response) {
  const application = response?.application
    || response?.applyStatus
    || response?.apply_status
    || {};
  const runtime = response?.runtime || {};
  const desiredRevision = numberOrZero(firstValue(
    application.desired_revision,
    application.desiredRevision,
    response?.desired_revision,
    response?.desiredRevision,
    runtime.desiredRevision,
    response?.revision,
  ));
  const appliedRevision = numberOrZero(firstValue(
    application.applied_revision,
    application.appliedRevision,
    runtime.appliedRevision,
  ));
  const lastAttemptRevision = numberOrZero(firstValue(
    application.last_attempt_revision,
    application.lastAttemptRevision,
    runtime.lastAttemptRevision,
  ));
  const appliedAt = String(firstValue(
    application.applied_at,
    application.appliedAt,
    runtime.appliedAt,
  ));
  const lastAttemptAt = String(firstValue(
    application.last_attempt_at,
    application.lastAttemptAt,
    runtime.lastAttemptAt,
  ));
  const lastError = String(firstValue(
    application.last_error,
    application.lastError,
    runtime.lastError,
  ));
  const onlineValue = firstValue(
    application.is_online,
    application.isOnline,
    response?.is_online,
    response?.isOnline,
  );
  const isOnline = onlineValue === true || onlineValue === false ? onlineValue : null;
  const explicitStatus = String(firstValue(
    application.status,
    application.state,
    response?.application_status,
    response?.applicationStatus,
  )).trim().toLowerCase();

  let status = ['saved', 'pending', 'applied', 'failed'].includes(explicitStatus)
    ? explicitStatus
    : '';
  if (!status) {
    // Applied is authoritative for the desired revision. A later failed retry
    // must not downgrade an already-applied configuration in legacy responses.
    if (desiredRevision > 0 && appliedRevision === desiredRevision
      && (appliedAt || appliedRevision > 0)) {
      status = 'applied';
    } else if (desiredRevision > 0 && lastError && lastAttemptRevision === desiredRevision) {
      status = 'failed';
    } else if (desiredRevision > 0 && (
      isOnline === true
      || lastAttemptRevision === desiredRevision
      || Boolean(lastAttemptAt)
    )) {
      status = 'pending';
    } else {
      status = 'saved';
    }
  }

  return {
    status,
    desiredRevision,
    appliedRevision,
    lastAttemptRevision,
    appliedAt,
    lastAttemptAt,
    isOnline,
    lastError,
  };
}

export function resolvePromptApplicationState(response) {
  const application = normalizePromptApplication(response);
  const status = application.status || 'saved';
  switch (status) {
    case 'applied': {
      const revision = application.appliedRevision || application.desiredRevision;
      return {
        ...application,
        kind: 'applied',
        label: revision > 0 ? `Agent 已应用 revision ${revision}` : 'Agent 已应用',
      };
    }
    case 'pending':
      return { ...application, kind: 'pending', label: '等待 Agent 应用' };
    case 'failed':
      return {
        ...application,
        kind: 'failed',
        label: '应用失败，请重启或检查 Agent',
      };
    default:
      return { ...application, kind: 'saved', label: '已保存到云端' };
  }
}

export function normalizePromptDefinition(response) {
  const prompt = response?.definition?.prompt || {};
  return {
    selected: prompt.selected === 'custom' ? 'custom' : 'default',
    customSystemPrompt: String(prompt.customSystemPrompt || ''),
  };
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

function formatReportedAt(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);
  return date.toLocaleString();
}

function relationLabel(remote) {
  if (remote?.canEdit) return '我创建的 Agent';
  return '联系人 Agent';
}

function StatusBadge({ state }) {
  const Icon = state.kind === 'applied'
    ? CheckCircle2
    : state.kind === 'failed'
      ? AlertTriangle
      : state.kind === 'pending'
        ? LoaderCircle
        : Cloud;
  return (
    <span
      className={`cc-system-prompt-status is-${state.kind}`}
      role="status"
      aria-label={state.label}
      title={state.lastAttemptAt || state.appliedAt || undefined}
    >
      <Icon className={state.kind === 'pending' ? 'spin' : undefined} size={15} aria-hidden="true" />
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
  const [defaultPrompt, setDefaultPrompt] = useState('');
  const [defaultSnapshot, setDefaultSnapshot] = useState(null);
  const [mode, setMode] = useState('default');
  const [customPrompt, setCustomPrompt] = useState('');
  const [loadingBots, setLoadingBots] = useState(true);
  const [loadingPrompt, setLoadingPrompt] = useState(false);
  const [saving, setSaving] = useState(false);
  const [visibilitySaving, setVisibilitySaving] = useState(false);
  const [error, setError] = useState('');
  const [errorStatus, setErrorStatus] = useState(0);
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
  }, []);

  useEffect(() => {
    selectedBotUIDRef.current = selectedBotUID;
  }, [selectedBotUID]);

  const canEdit = remote?.canEdit === true;
  const savedMode = remote?.selected || 'default';
  const savedCustomPrompt = String(remote?.customSystemPrompt || '');
  const dirty = Boolean(canEdit && loadedBotUID && loadedBotUID === selectedBotUID) && (
    mode !== savedMode
    || customPrompt !== savedCustomPrompt
  );
  const byteCount = useMemo(() => promptByteLength(customPrompt), [customPrompt]);
  const promptTooLarge = byteCount > MAX_SYSTEM_PROMPT_BYTES;
  const customPromptEmpty = mode === 'custom' && !customPrompt.trim();
  const selectedBot = bots.find((bot) => String(botUID(bot)) === selectedBotUID) || null;
  const applicationState = resolvePromptApplicationState(remote || {});
  const ready = Boolean(selectedBotUID && loadedBotUID === selectedBotUID && remote && !loadingPrompt);
  const busySaving = saving || visibilitySaving;
  const displayedContent = mode === 'default' ? defaultPrompt : customPrompt;
  const snapshotMissing = mode === 'default'
    && (remote?.defaultContentAvailable === false
      || !defaultSnapshot
      || !defaultPrompt);
  dirtyRef.current = dirty;

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  useEffect(() => {
    onSavingChange?.(busySaving);
  }, [busySaving, onSavingChange]);

  const applyRemote = useCallback((response, botUIDValue, { preserveDraft = false } = {}) => {
    const normalized = normalizeAgentPrompt(response);
    remoteVersionRef.current = {
      botUID: botUIDValue,
      revision: normalized.revision,
    };
    setRemote(normalized);
    setLoadedBotUID(botUIDValue);
    setDefaultPrompt(String(normalized.defaultContent || ''));
    setDefaultSnapshot(normalized.defaultSnapshot || null);
    if (!preserveDraft) {
      setMode(normalized.selected);
      setCustomPrompt(normalized.customSystemPrompt || '');
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
      setErrorStatus(0);
    }
    try {
      const rosterBot = bots.find((bot) => String(botUID(bot)) === requestedUID);
      const owner = rosterBot?.relation === 'owner';
      const [viewerResponse, ownerResponse] = await Promise.all([
        api.getAgentPrompt(requestedUID),
        owner
          ? api.getBotDefinitionPrompt(requestedUID).then(
            (response) => ({ response }),
            (error) => ({ error }),
          )
          : Promise.resolve(null),
      ]);
      if (!mountedRef.current
        || requestID !== loadRequestRef.current
        || selectedBotUIDRef.current !== requestedUID) return null;
      if (silent && dirtyRef.current) return null;
      const viewer = normalizeAgentPrompt(viewerResponse);
      if (ownerResponse?.error) throw ownerResponse.error;
      const ownerPayload = ownerResponse?.response || null;
      const ownerDefinition = ownerPayload ? normalizePromptDefinition(ownerPayload) : null;
      const normalized = {
        ...viewer,
        canEdit: owner ? viewer.canEdit : false,
        revision: owner ? Number(ownerPayload?.revision || viewer.revision || 0) : viewer.revision,
        selected: ownerDefinition?.selected || viewer.selected,
        customSystemPrompt: owner
          ? ownerDefinition?.customSystemPrompt || ''
          : (viewer.selected === 'custom' ? viewer.content : ''),
        defaultContent: viewer.defaultContent
          || (viewer.selected === 'default' ? viewer.content : ''),
        defaultContentAvailable: viewer.defaultSnapshot
          ? viewer.defaultContentAvailable
          : (viewer.selected === 'default' ? viewer.contentAvailable : false),
        application: viewerResponse?.application
          ? viewer.application
          : normalizePromptApplication({
            revision: owner ? ownerPayload?.revision : viewer.revision,
            runtime: owner ? ownerPayload?.runtime : viewerResponse?.runtime,
            is_online: rosterBot?.is_online ?? rosterBot?.isOnline,
          }),
      };
      const currentVersion = remoteVersionRef.current;
      if (currentVersion.botUID === requestedUID
        && normalized.revision < currentVersion.revision) return null;
      applyRemote(normalized, requestedUID, options);
      return normalized;
    } catch (cause) {
      if (!mountedRef.current
        || requestID !== loadRequestRef.current
        || selectedBotUIDRef.current !== requestedUID) return null;
      if (!silent) {
        setErrorStatus(Number(cause?.status || 0));
        setError(cause?.message || (options.preserveDraft
          ? '无法读取最新云端 revision，当前草稿仍保留'
          : '无法读取系统提示词'));
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
  }, [applyRemote, bots]);

  useEffect(() => {
    let active = true;
    setLoadingBots(true);
    api.getAgents()
      .then((response) => {
        if (!active) return;
        const available = normalizePromptBots(response);
        setBots(available);
        const remembered = readRememberedBotUID(user?.uid);
        const preferred = remembered && available.some((bot) => String(botUID(bot)) === remembered)
          ? remembered
          : String(botUID(available[0]) || '');
        selectedBotUIDRef.current = preferred;
        setSelectedBotUID(preferred);
      })
      .catch((cause) => {
        if (!active) return;
        setErrorStatus(Number(cause?.status || 0));
        setError(cause?.message || '无法读取 Agent 列表');
      })
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

  const chooseBot = async (nextUID) => {
    if (busySaving || nextUID === selectedBotUID) return;
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

  const chooseMode = (nextMode) => {
    if (!canEdit || busySaving || nextMode === mode) return;
    setMode(nextMode);
  };

  const save = async () => {
    if (!ready || !canEdit || busySaving || !dirty || promptTooLarge || customPromptEmpty) return;
    const requestedUID = selectedBotUIDRef.current;
    const expectedRevision = Number(remote?.revision || 0);
    const draft = mode === 'custom'
      ? { selected: 'custom', customSystemPrompt: customPrompt }
      : {
        selected: 'default',
        ...(customPrompt.trim() ? { customSystemPrompt: customPrompt } : {}),
      };
    const requestID = saveRequestRef.current + 1;
    saveRequestRef.current = requestID;
    loadRequestRef.current += 1;
    setSaving(true);
    setError('');
    setErrorStatus(0);
    setConflict(false);
    try {
      await api.updateBotDefinitionPrompt(requestedUID, expectedRevision, draft);
      if (!mountedRef.current
        || requestID !== saveRequestRef.current
        || requestedUID !== selectedBotUIDRef.current) return;
      const refreshed = await loadPrompt(requestedUID);
      if (!mountedRef.current || requestID !== saveRequestRef.current) return;
      if (refreshed) {
        feedback.notify({ tone: 'success', message: '系统提示词已保存，XiaoBa 将在新会话中使用最新配置。' });
      } else {
        setError('系统提示词已保存，但暂时无法读取保存后的云端内容，请刷新重试。');
      }
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
        setErrorStatus(Number(cause?.status || 0));
        setError(cause?.message || '保存系统提示词失败');
      }
    } finally {
      if (mountedRef.current && requestID === saveRequestRef.current) {
        setSaving(false);
      }
    }
  };

  const updateVisibility = async (nextVisibility) => {
    if (!ready || !canEdit || busySaving || nextVisibility === remote?.promptVisibility) return;
    const requestedUID = selectedBotUIDRef.current;
    setVisibilitySaving(true);
    setError('');
    setErrorStatus(0);
    try {
      await api.updateBotPromptVisibility(requestedUID, nextVisibility);
      if (!mountedRef.current || requestedUID !== selectedBotUIDRef.current) return;
      setRemote((current) => ({ ...current, promptVisibility: nextVisibility }));
      feedback.notify({
        tone: 'success',
        message: nextVisibility === 'friends'
          ? '好友现在可以只读查看这个 Agent 的系统提示词。'
          : '系统提示词已改为仅自己可见。',
      });
    } catch (cause) {
      if (!mountedRef.current || requestedUID !== selectedBotUIDRef.current) return;
      setErrorStatus(Number(cause?.status || 0));
      setError(cause?.message || '提示词可见范围保存失败');
    } finally {
      if (mountedRef.current) setVisibilitySaving(false);
    }
  };

  const accessDenied = !ready && (errorStatus === 403 || errorStatus === 404);

  return (
    <main className="cc-system-prompt-page">
      <div className="cc-system-prompt-shell">
        <header className="cc-system-prompt-header">
          <div>
            <span className="cc-system-prompt-kicker"><FileText size={14} /> Agent 配置</span>
            <h1>系统提示词</h1>
            <p>查看 Agent 当前使用的基础提示词；只有创建者可以修改。</p>
          </div>
          <label className="cc-system-prompt-agent-picker">
            <span><Bot size={15} /> Agent</span>
            <select
              value={selectedBotUID}
              disabled={loadingBots || busySaving || bots.length === 0}
              onChange={(event) => chooseBot(event.target.value)}
              aria-label="选择要查看的 Agent"
            >
              {bots.length === 0 && <option value="">暂无联系人 Agent</option>}
              {bots.map((bot) => (
                <option key={botUID(bot)} value={String(botUID(bot))}>
                  {botLabel(bot)}{bot?.relation === 'friend' ? ' · 联系人' : ''}
                </option>
              ))}
            </select>
          </label>
        </header>

        {error && ready && (
          <InlineFeedback tone={conflict ? 'warning' : 'error'} title={conflict ? '检测到配置冲突' : '操作未完成'}>
            {error}
          </InlineFeedback>
        )}

        {loadingBots || loadingPrompt ? (
          <div className="cc-system-prompt-loading" role="status">
            <LoaderCircle className="spin" size={20} /> 正在读取系统提示词
          </div>
        ) : bots.length === 0 ? (
          <section className="cc-system-prompt-empty">
            <Bot size={24} />
            <h2>暂无联系人 Agent</h2>
            <p>创建 Agent 或添加机器人好友后，即可在这里查看其系统提示词。</p>
          </section>
        ) : selectedBotUID && !ready ? (
          <section className="cc-system-prompt-empty">
            {accessDenied ? <Lock size={24} /> : <AlertTriangle size={24} />}
            <h2>{accessDenied ? '系统提示词未向好友开放' : '无法读取系统提示词'}</h2>
            <p>
              {accessDenied
                ? '该 Agent 的创建者尚未允许好友查看系统提示词。'
                : (error || '请检查网络连接后重试。')}
            </p>
            <button
              type="button"
              className="cc-system-prompt-refresh"
              onClick={() => loadPrompt(selectedBotUID)}
            >
              <RefreshCw size={15} /> 重试
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
                <span>访问权限</span>
                <strong className="cc-system-prompt-access">
                  {canEdit ? <Cloud size={14} /> : <Eye size={14} />}
                  {relationLabel(remote)}
                </strong>
              </div>
              <div>
                <span>云端 revision</span>
                <strong>{Number(remote?.revision || 0)}</strong>
              </div>
              <div>
                <span>Agent 应用状态</span>
                <StatusBadge state={applicationState} />
              </div>
              <button
                type="button"
                className="cc-system-prompt-refresh"
                onClick={() => loadPrompt(selectedBotUID)}
                disabled={busySaving || dirty}
                title={dirty ? '请先保存或放弃当前修改' : '刷新提示词'}
              >
                <RefreshCw size={15} /> 刷新
              </button>
            </div>

            {applicationState.kind === 'failed' && (
              <InlineFeedback tone="error" title="Agent 尚未应用当前 revision">
                云端配置仍然保留。请重启或检查该 Agent，恢复后再刷新此页面确认应用状态。
              </InlineFeedback>
            )}

            {!canEdit && (
              <InlineFeedback tone="info" title="只读查看">
                这是联系人 Agent。只有创建者可以修改提示词和可见范围。
              </InlineFeedback>
            )}

            {canEdit && (
              <section className="cc-system-prompt-visibility" aria-labelledby="cc-system-prompt-visibility-title">
                <div>
                  <h2 id="cc-system-prompt-visibility-title">提示词可见范围</h2>
                  <p>好友只能查看当前启用的正文，不能修改，也不会看到未启用的自定义提示词。</p>
                </div>
                <div className="cc-system-prompt-visibility-options" role="group" aria-label="提示词可见范围">
                  <button
                    type="button"
                    className={remote.promptVisibility === 'owner' ? 'is-selected' : ''}
                    aria-pressed={remote.promptVisibility === 'owner'}
                    disabled={busySaving}
                    onClick={() => updateVisibility('owner')}
                  >
                    <Lock size={15} /> 仅自己
                  </button>
                  <button
                    type="button"
                    className={remote.promptVisibility === 'friends' ? 'is-selected' : ''}
                    aria-pressed={remote.promptVisibility === 'friends'}
                    disabled={busySaving}
                    onClick={() => updateVisibility('friends')}
                  >
                    <Eye size={15} /> 好友可查看
                  </button>
                  {visibilitySaving && <span role="status"><LoaderCircle className="spin" size={14} /> 保存中</span>}
                </div>
              </section>
            )}

            <div className="cc-system-prompt-mode" role="radiogroup" aria-label="系统提示词模式">
              <button
                type="button"
                role="radio"
                aria-checked={mode === 'default'}
                className={mode === 'default' ? 'active' : ''}
                disabled={!canEdit || busySaving}
                onClick={() => chooseMode('default')}
              >
                <span className="cc-system-prompt-mode-check">{mode === 'default' && <Check size={15} />}</span>
                <span><strong>默认提示词</strong><small>当前 XiaoBa 版本上报的默认基础提示词</small></span>
              </button>
              <button
                type="button"
                role="radio"
                aria-checked={mode === 'custom'}
                className={mode === 'custom' ? 'active' : ''}
                disabled={!canEdit || busySaving}
                onClick={() => chooseMode('custom')}
              >
                <span className="cc-system-prompt-mode-check">{mode === 'custom' && <Check size={15} />}</span>
                <span><strong>自定义提示词</strong><small>由创建者为这个 Agent 设置的独立正文</small></span>
              </button>
            </div>

            <div className={`cc-system-prompt-editor${canEdit && mode === 'custom' ? '' : ' is-readonly'}`}>
              <div className="cc-system-prompt-editor-heading">
                <label htmlFor="cc-system-prompt-text">
                  {mode === 'default' ? '默认基础提示词' : '自定义提示词'}
                </label>
                {canEdit && mode === 'custom' && (
                  <span className={promptTooLarge ? 'is-error' : ''}>
                    {byteCount.toLocaleString()} / {MAX_SYSTEM_PROMPT_BYTES.toLocaleString()} 字节
                  </span>
                )}
              </div>

              {snapshotMissing ? (
                <div className="cc-system-prompt-snapshot-empty" role="status">
                  <Cloud size={22} />
                  <strong>默认提示词尚未同步</strong>
                  <span>请启动或升级该 Agent 的 XiaoBa，默认基础提示词会在运行时上报到云端。</span>
                </div>
              ) : (
                <textarea
                  id="cc-system-prompt-text"
                  value={displayedContent}
                  readOnly={!canEdit || mode === 'default'}
                  disabled={busySaving}
                  onChange={(event) => setCustomPrompt(event.target.value)}
                  placeholder={mode === 'custom' ? '输入这个 Agent 在每次新会话中都应遵循的角色、边界与工作规则...' : ''}
                  spellCheck="false"
                />
              )}

              {mode === 'default' && remote?.defaultSnapshot && (
                <div className="cc-system-prompt-snapshot-meta">
                  <span>这是默认基础提示词，不包含日期、平台和设备等运行时上下文。</span>
                  {remote.defaultSnapshot.xiaobaVersion && <span>XiaoBa {remote.defaultSnapshot.xiaobaVersion}</span>}
                  {remote.defaultSnapshot.runtimeVersion && <span>Runtime {remote.defaultSnapshot.runtimeVersion}</span>}
                  {remote.defaultSnapshot.reportedAt && <span>同步于 {formatReportedAt(remote.defaultSnapshot.reportedAt)}</span>}
                </div>
              )}
              {customPromptEmpty && canEdit && <p className="is-error">自定义模式下提示词不能为空。</p>}
              {promptTooLarge && canEdit && <p className="is-error">内容超过后端允许的 1 MiB 限制。</p>}
            </div>

            {canEdit && (
              <footer className="cc-system-prompt-actions">
                <div>
                  <strong>{dirty ? '有未保存的修改' : '云端配置已同步'}</strong>
                  <span>
                    {mode === 'custom'
                      ? '保存后，好友只能看到这份当前启用的正文。'
                      : '保存后，新会话将使用 XiaoBa 上报的默认基础提示词。'}
                  </span>
                </div>
                <button
                  type="button"
                  className="primary"
                  onClick={save}
                  disabled={!dirty || busySaving || promptTooLarge || customPromptEmpty}
                >
                  {saving ? <LoaderCircle className="spin" size={16} /> : <Save size={16} />}
                  {saving ? '保存中' : '保存修改'}
                </button>
              </footer>
            )}
          </section>
        ) : null}
      </div>
    </main>
  );
}
