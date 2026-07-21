import React, { useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, Check, ChevronDown, ChevronRight, Loader2, Settings2 } from 'lucide-react';

import { api } from '../api';
import {
  formatRelayUsagePill,
  relayUsageTone,
  resolveConversationModelDisplay,
} from '../utils/relay-usage';

const APPLY_POLL_MS = 2000;
const APPLY_SLOW_POLL_MS = 15000;
const APPLY_WAIT_TIMEOUT_MS = 45000;

const EMPTY_CUSTOM_MODEL = {
  protocol: 'anthropic',
  api_base: '',
  model: '',
  api_key: '',
  context_window_tokens: '128000',
  max_tokens: '',
  temperature: '',
  reasoning_effort: '',
};

const CUSTOM_PROTOCOLS = [
  { value: 'anthropic', label: 'Anthropic Messages' },
  { value: 'openai-chat-completions', label: 'OpenAI Chat Completions' },
  { value: 'openai-responses', label: 'OpenAI Responses' },
];

const CUSTOM_REASONING_EFFORTS = ['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'disabled'];

function customDraftFromConfig(config) {
  const custom = config?.custom;
  if (!custom) return { ...EMPTY_CUSTOM_MODEL };
  return {
    protocol: custom.protocol || 'anthropic',
    api_base: custom.api_base || '',
    model: custom.model || '',
    api_key: '',
    context_window_tokens: String(custom.context_window_tokens || 128000),
    max_tokens: custom.max_tokens ? String(custom.max_tokens) : '',
    temperature: custom.temperature == null ? '' : String(custom.temperature),
    reasoning_effort: custom.reasoning_effort === 'default' ? '' : custom.reasoning_effort || '',
  };
}

function modelQuotaLabel(quota, state) {
  if (state === 'error') return '额度暂不可用';
  if (state !== 'loaded') return '额度同步中';
  if (!quota) return '额度未配置';
  if (quota.status === 'over_limit') return '额度已用尽';
  const limit = Number(quota.limit_cny);
  const remaining = Number(quota.remaining_cny);
  const percent = Number(quota.percent);
  if (!Number.isFinite(limit) || limit <= 0) return '额度未配置';
  const remainingPercent = Number.isFinite(percent)
    ? Math.max(0, Math.min(100, 100 - percent))
    : 0;
  const remainingAmount = Number.isFinite(remaining) ? Math.max(0, remaining) : 0;
  return `剩余 ${Math.round(remainingPercent)}% · ¥${remainingAmount.toFixed(2)}`;
}

function modelQuotaTone(quota) {
  if (quota?.status === 'over_limit') return 'danger';
  if (quota?.status === 'high' || Number(quota?.percent) >= 90) return 'warning';
  return '';
}

function reasoningEffortLabel(effort) {
  const labels = {
    none: 'none · 关闭推理',
    minimal: 'minimal · 极低',
    low: 'low · 低',
    medium: 'medium · 中',
    high: 'high · 高',
    xhigh: 'xhigh · 极高',
    max: 'max · 最大',
    disabled: 'disabled · 关闭推理',
  };
  return labels[effort] || effort;
}

export function describeModelConfigRequestError(error, action = '切换') {
  if (error?.code === 'NETWORK_ERROR') {
    if (action === '查询') return '暂时无法查询切换进度，页面会自动重试。';
    if (action === '加载') return '网络连接中断，模型列表没有加载成功。请检查网络后重试。';
    return '网络连接中断，模型没有切换。请检查网络后重试。';
  }
  const status = Number(error?.status);
  const detail = String(error?.message || '');
  if (status === 400) {
    if (/api key|required|custom model|自定义/i.test(detail)) return '请完整填写自定义模型地址、模型名称和 API Key。';
    return '该模型或推理强度暂不受支持，请刷新列表后重试。';
  }
  if (status === 401) return '登录状态已失效，请重新登录后再管理模型。';
  if (status === 403) return '只有机器人创建者可以切换模型，请确认当前登录账号。';
  if (status === 404) return '没有找到这个机器人，它可能已被删除或解绑。';
  if (status === 409) return '机器人配置刚刚发生变化，请重新打开列表后再试。';
  if (status === 429) return '操作过于频繁，请稍等片刻再切换。';
  if (status === 503 && /encrypt|密钥加密|custom model/i.test(detail)) {
    return '云端自定义模型尚未启用安全密钥存储，请联系管理员完成配置。';
  }
  if (status >= 500) return '模型配置服务暂时不可用，当前模型不会改变，请稍后重试。';
  if (action === '加载') return '模型列表加载失败，请稍后重试。';
  if (action === '查询') return '暂时无法查询切换进度，页面会自动重试。';
  return '模型切换请求未完成，当前模型不会改变，请重试。';
}

export function describeModelApplyError(rawError) {
  const detail = String(rawError || '').trim();
  if (!detail) return '机器人应用新模型失败，当前仍使用原模型，请稍后重试。';
  if (/401|403|unauthori[sz]ed|api[ _-]?key|authentication|鉴权|凭证/i.test(detail)) {
    return '机器人连接模型服务时鉴权失败，请检查模型访问凭证。';
  }
  if (/402|429|rate[ _-]?limit|quota|insufficient|balance|额度|余额|限流/i.test(detail)) {
    return '所选模型当前额度不足或请求过于频繁，请检查模型额度后重试。';
  }
  if (/timeout|timed out|network|fetch failed|econn|enotfound|socket|网络|连接超时/i.test(detail)) {
    return '机器人连接模型服务超时，请检查机器人网络与模型服务。';
  }
  if (/unsupported (model|reasoning)|model not found|invalid (model|reasoning)|不支持.*(模型|推理)/i.test(detail)) {
    return '机器人无法使用所选模型或推理强度，请换一个配置重试。';
  }
  return '机器人应用新模型失败，当前仍使用原模型，请稍后重试。';
}

export default function BotModelSelector({ currentModelName, agentModelState, activeAgent }) {
  const menuRef = useRef(null);
  const activeBotUIDRef = useRef(0);
  const activeBotUID = Number(activeAgent?.uid) || 0;
  const isModelOwner = activeBotUID > 0 && activeAgent?.isOwner === true;
  const [modelConfig, setModelConfig] = useState(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [expandedModelID, setExpandedModelID] = useState('');
  const [customEditorOpen, setCustomEditorOpen] = useState(false);
  const [customDraft, setCustomDraft] = useState(EMPTY_CUSTOM_MODEL);
  const [usageState, setUsageState] = useState('idle');
  const [loading, setLoading] = useState(false);
  const [savingKey, setSavingKey] = useState('');
  const [error, setError] = useState('');
  const [applyWaitExpired, setApplyWaitExpired] = useState(false);
  const canManageModel = isModelOwner && Number(modelConfig?.uid) === activeBotUID && modelConfig?.management_enabled !== false;

  const loadConfig = async (uid, includeUsage, action = '加载') => {
    try {
      if (!modelConfig) setLoading(true);
      if (includeUsage) setUsageState('loading');
      const config = await api.getBotModelConfig(uid, { includeUsage });
      if (activeBotUIDRef.current !== uid) return null;
      setModelConfig(config);
      if (includeUsage) setUsageState('loaded');
      setError('');
      return config;
    } catch (requestError) {
      if (activeBotUIDRef.current === uid) {
        if (includeUsage) setUsageState('error');
        setError(describeModelConfigRequestError(requestError, action));
      }
      return null;
    } finally {
      if (activeBotUIDRef.current === uid) setLoading(false);
    }
  };

  useEffect(() => {
    activeBotUIDRef.current = activeBotUID;
    setMenuOpen(false);
    setExpandedModelID('');
    setCustomEditorOpen(false);
    setCustomDraft({ ...EMPTY_CUSTOM_MODEL });
    setUsageState('idle');
    setError('');
    setApplyWaitExpired(false);
    setSavingKey('');
    setModelConfig(null);
    if (!isModelOwner) {
      setLoading(false);
      return undefined;
    }
    loadConfig(activeBotUID, false);
    return undefined;
  }, [activeBotUID, isModelOwner]);

  useEffect(() => {
    if (!menuOpen) return undefined;
    const closeOnOutsidePointer = (event) => {
      if (!menuRef.current?.contains(event.target)) setMenuOpen(false);
    };
    const closeOnEscape = (event) => {
      if (event.key === 'Escape') setMenuOpen(false);
    };
    document.addEventListener('pointerdown', closeOnOutsidePointer);
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('pointerdown', closeOnOutsidePointer);
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [menuOpen]);

  const pendingRevision = modelConfig?.status === 'pending'
    ? Number(modelConfig?.desired?.revision) || 0
    : 0;

  useEffect(() => {
    if (!canManageModel || !pendingRevision) {
      setApplyWaitExpired(false);
      return undefined;
    }
    setApplyWaitExpired(false);
    const timer = window.setTimeout(() => setApplyWaitExpired(true), APPLY_WAIT_TIMEOUT_MS);
    return () => window.clearTimeout(timer);
  }, [activeBotUID, canManageModel, pendingRevision]);

  useEffect(() => {
    if (!canManageModel || !pendingRevision) return undefined;
    let cancelled = false;
    let timer;
    const delay = applyWaitExpired ? APPLY_SLOW_POLL_MS : APPLY_POLL_MS;
    const poll = async () => {
      if (cancelled) return;
      await loadConfig(activeBotUID, false, '查询');
      if (!cancelled) timer = window.setTimeout(poll, delay);
    };
    timer = window.setTimeout(poll, delay);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [activeBotUID, applyWaitExpired, canManageModel, pendingRevision]);

  const modelApplyPending = modelConfig?.status === 'pending';
  const transitioning = Boolean(savingKey) || (modelApplyPending && !applyWaitExpired);
  const desiredKind = modelConfig?.desired?.kind || (modelConfig?.configured ? 'catalog' : 'local');
  const desiredModelID = modelConfig?.desired?.model_id || 'local';
  const desiredModel = modelConfig?.models?.find((model) => model.id === desiredModelID);
  const modelApplyError = modelConfig?.status === 'failed'
    ? describeModelApplyError(modelConfig.last_error)
    : '';
  const feedback = error || modelApplyError || (savingKey
    ? '正在保存模型设置，请稍候。'
    : modelApplyPending
      ? applyWaitExpired
        ? '设置已保存，机器人可能正在处理任务或暂时离线；空闲或上线后会自动应用。'
        : '正在等待机器人应用新模型，期间不可重复切换。'
      : modelConfig?.quota_error || '');
  const feedbackIsError = Boolean(error || modelApplyError);

  const display = useMemo(
    () => resolveConversationModelDisplay(currentModelName, agentModelState),
    [agentModelState, currentModelName],
  );
  if (!display) return null;

  const saveSelection = async (payload, key) => {
    if (!canManageModel || transitioning || !key) return;
    const uid = activeBotUID;
    setError('');
    setSavingKey(key);
    try {
      const config = await api.updateBotModelConfig(uid, payload);
      if (activeBotUIDRef.current !== uid) return;
      setModelConfig(config);
      setMenuOpen(false);
      setExpandedModelID('');
      setCustomEditorOpen(false);
      setCustomDraft((current) => ({ ...current, api_key: '' }));
    } catch (requestError) {
      if (activeBotUIDRef.current === uid) setError(describeModelConfigRequestError(requestError));
    } finally {
      if (activeBotUIDRef.current === uid) setSavingKey('');
    }
  };

  const saveCatalogSelection = (modelID, reasoningEffort = '') => saveSelection({
    kind: modelID === 'local' ? 'local' : 'catalog',
    model_id: modelID,
    reasoning_effort: reasoningEffort,
  }, `${modelID}:${reasoningEffort}`);

  const openCustomEditor = () => {
    setExpandedModelID('');
    setCustomDraft(customDraftFromConfig(modelConfig));
    setCustomEditorOpen(true);
  };

  const saveCustomModel = (event) => {
    event.preventDefault();
    const contextWindow = Number(customDraft.context_window_tokens);
    const maxTokens = customDraft.max_tokens === '' ? 0 : Number(customDraft.max_tokens);
    const temperature = customDraft.temperature === '' ? undefined : Number(customDraft.temperature);
    if (!customDraft.api_base.trim() || !customDraft.model.trim() || !Number.isFinite(contextWindow)) {
      setError('请完整填写 API Base、模型名称和上下文长度。');
      return;
    }
    if (!modelConfig?.custom?.api_key_configured && !customDraft.api_key.trim()) {
      setError('首次保存自定义模型时需要填写 API Key。');
      return;
    }
    const custom = {
      protocol: customDraft.protocol,
      api_base: customDraft.api_base.trim(),
      model: customDraft.model.trim(),
      api_key: customDraft.api_key.trim(),
      context_window_tokens: contextWindow,
      max_tokens: Number.isFinite(maxTokens) ? maxTokens : 0,
      reasoning_effort: customDraft.reasoning_effort,
    };
    if (Number.isFinite(temperature)) custom.temperature = temperature;
    saveSelection({ kind: 'custom', model_id: 'custom', custom }, `custom:${custom.model}`);
  };

  const openMenu = () => {
    if (transitioning) return;
    const next = !menuOpen;
    setMenuOpen(next);
    if (next && canManageModel) loadConfig(activeBotUID, true);
  };

  const headerQuota = formatRelayUsagePill(agentModelState?.summary, {
    customLabel: '自备模型',
    showModel: false,
  }) || display.meta;
  const headerTone = agentModelState?.summary
    ? relayUsageTone(agentModelState.summary)
    : agentModelState?.isBot && agentModelState.state === 'unavailable' ? 'muted' : '';
  const applyState = savingKey
    ? '保存中'
    : modelConfig?.status === 'failed'
      ? '切换失败'
      : modelApplyPending
        ? applyWaitExpired ? '待应用' : '切换中'
        : '';
  const statusContents = (
    <>
      <span className="v3-current-model-name">{display.model}</span>
      {headerQuota && <span className={`v3-model-quota ${headerTone}`.trim()}>{headerQuota}</span>}
      {applyState && <span className={`v3-model-apply-state ${modelApplyError ? 'error' : ''}`}>{applyState}</span>}
      {canManageModel && (transitioning
        ? <Loader2 className="v3-model-switch-spinner" size={14} aria-hidden="true" />
        : <ChevronDown className="v3-model-menu-chevron" size={14} aria-hidden="true" />)}
    </>
  );

  return (
    <div ref={menuRef} className="v3-model-menu-anchor">
      {canManageModel ? (
        <button
          type="button"
          className="v3-local-assistant-status v3-model-status-button"
          aria-label={`${display.title}，切换模型`}
          aria-haspopup="menu"
          aria-expanded={menuOpen}
          aria-busy={transitioning}
          title={feedback || display.title}
          onClick={openMenu}
          disabled={transitioning}
        >
          {statusContents}
        </button>
      ) : (
        <div className="v3-local-assistant-status" aria-label={display.title} title={display.title}>
          {statusContents}
        </div>
      )}

      {canManageModel && menuOpen && (
        <div className={`v3-model-menu ${customEditorOpen ? 'custom-open' : ''}`} role="menu" aria-label="选择运行模型">
          {loading && !modelConfig ? (
            <div className="v3-model-menu-message">正在加载模型...</div>
          ) : customEditorOpen ? (
            <form className="v3-custom-model-editor" onSubmit={saveCustomModel}>
              <div className="v3-custom-model-heading">
                <span><Settings2 size={15} /> 自定义模型</span>
                <button type="button" onClick={() => setCustomEditorOpen(false)}>返回列表</button>
              </div>
              <label>
                <span>API 协议</span>
                <select value={customDraft.protocol} onChange={(event) => setCustomDraft({ ...customDraft, protocol: event.target.value })}>
                  {CUSTOM_PROTOCOLS.map((protocol) => <option key={protocol.value} value={protocol.value}>{protocol.label}</option>)}
                </select>
              </label>
              <label>
                <span>API Base</span>
                <input type="url" required placeholder="https://api.example.com/v1" value={customDraft.api_base} onChange={(event) => setCustomDraft({ ...customDraft, api_base: event.target.value })} />
              </label>
              <label>
                <span>模型名称</span>
                <input required placeholder="model-name" value={customDraft.model} onChange={(event) => setCustomDraft({ ...customDraft, model: event.target.value })} />
              </label>
              <label>
                <span>API Key</span>
                <input
                  type="password"
                  autoComplete="new-password"
                  placeholder={modelConfig?.custom?.api_key_configured ? `已配置 ${modelConfig.custom.api_key_hint || ''}，留空不修改` : '首次配置必填'}
                  value={customDraft.api_key}
                  onChange={(event) => setCustomDraft({ ...customDraft, api_key: event.target.value })}
                />
              </label>
              <div className="v3-custom-model-grid">
                <label>
                  <span>上下文 Token</span>
                  <input type="number" min="1024" max="4000000" required value={customDraft.context_window_tokens} onChange={(event) => setCustomDraft({ ...customDraft, context_window_tokens: event.target.value })} />
                </label>
                <label>
                  <span>最大输出 Token</span>
                  <input type="number" min="0" max="1000000" placeholder="使用服务默认" value={customDraft.max_tokens} onChange={(event) => setCustomDraft({ ...customDraft, max_tokens: event.target.value })} />
                </label>
                <label>
                  <span>温度</span>
                  <input type="number" min="0" max="2" step="0.1" placeholder="使用服务默认" value={customDraft.temperature} onChange={(event) => setCustomDraft({ ...customDraft, temperature: event.target.value })} />
                </label>
                <label>
                  <span>推理强度</span>
                  <select value={customDraft.reasoning_effort} onChange={(event) => setCustomDraft({ ...customDraft, reasoning_effort: event.target.value })}>
                    {CUSTOM_REASONING_EFFORTS.map((effort) => <option key={effort || 'default'} value={effort}>{effort ? reasoningEffortLabel(effort) : '使用接口默认'}</option>)}
                  </select>
                </label>
              </div>
              <button type="submit" className="v3-custom-model-save" disabled={transitioning}>
                {savingKey ? <Loader2 className="v3-model-switch-spinner" size={14} /> : <Check size={14} />}
                保存并切换
              </button>
            </form>
          ) : (
            <>
              <button
                type="button"
                className={`v3-model-menu-item ${desiredKind === 'local' ? 'selected' : ''}`}
                role="menuitem"
                onClick={() => saveCatalogSelection('local')}
                disabled={transitioning}
              >
                <span><strong>设备本地配置</strong><small>沿用桌面端当前模型</small></span>
                {desiredKind === 'local' && <Check size={15} aria-hidden="true" />}
              </button>
              {(modelConfig?.models || []).map((model) => {
                const efforts = model.reasoning_efforts || [];
                const hasReasoning = efforts.length > 0;
                const selected = desiredKind === 'catalog' && desiredModelID === model.id;
                return (
                  <div
                    key={model.id}
                    className="v3-model-menu-branch"
                    onMouseEnter={() => hasReasoning && setExpandedModelID(model.id)}
                    onMouseLeave={() => hasReasoning && setExpandedModelID((current) => current === model.id ? '' : current)}
                  >
                    <button
                      type="button"
                      className={`v3-model-menu-item ${selected ? 'selected' : ''}`}
                      role="menuitem"
                      aria-haspopup={hasReasoning ? 'menu' : undefined}
                      aria-expanded={hasReasoning ? expandedModelID === model.id : undefined}
                      onFocus={() => hasReasoning && setExpandedModelID(model.id)}
                      onClick={() => hasReasoning ? setExpandedModelID(model.id) : saveCatalogSelection(model.id)}
                      disabled={transitioning}
                    >
                      <span>
                        <strong>{model.label}</strong>
                        <small>{model.description}</small>
                        <small className={`v3-model-menu-quota ${modelQuotaTone(model.quota)}`.trim()}>{modelQuotaLabel(model.quota, modelConfig?.quota_error ? 'error' : usageState)}</small>
                      </span>
                      {hasReasoning ? <ChevronRight size={15} /> : selected && <Check size={15} />}
                    </button>
                    {hasReasoning && expandedModelID === model.id && (
                      <div className="v3-model-reasoning-menu" role="menu" aria-label={`${model.label} 推理强度`}>
                        <div className="v3-model-reasoning-title">推理强度</div>
                        {efforts.map((effort) => {
                          const effortSelected = selected && modelConfig?.desired?.reasoning_effort === effort;
                          return (
                            <button
                              type="button"
                              key={effort}
                              className={`v3-model-reasoning-item ${effortSelected ? 'selected' : ''}`}
                              role="menuitem"
                              onClick={() => saveCatalogSelection(model.id, effort)}
                              disabled={transitioning}
                            >
                              <span>{reasoningEffortLabel(effort)}</span>
                              {effortSelected && <Check size={14} />}
                            </button>
                          );
                        })}
                      </div>
                    )}
                  </div>
                );
              })}
              <button
                type="button"
                className={`v3-model-menu-item v3-custom-model-entry ${desiredKind === 'custom' ? 'selected' : ''}`}
                role="menuitem"
                onClick={openCustomEditor}
                disabled={transitioning || modelConfig?.custom_supported === false}
              >
                <span>
                  <strong>自定义模型</strong>
                  <small>{modelConfig?.custom_unavailable_reason
                    ? modelConfig.custom_unavailable_reason
                    : modelConfig?.custom_supported === false
                      ? '云端自定义模型暂不可用'
                    : modelConfig?.custom?.api_key_configured
                      ? `${modelConfig.custom.model} · 凭证 ${modelConfig.custom.api_key_hint}`
                      : '配置自己的 API 地址、模型和密钥'}</small>
                </span>
                <Settings2 size={15} />
              </button>
            </>
          )}
          {feedback && (
            <div className={`v3-model-menu-feedback ${feedbackIsError ? 'error' : ''}`} role="status">
              {feedbackIsError && <AlertCircle size={14} />}
              <span>{feedback}</span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
