import React, { useEffect, useMemo, useState } from 'react';
import { AlertTriangle, Check, Copy, ExternalLink, Gift, KeyRound, RotateCcw, Server, Sparkles, Trash2, X } from 'lucide-react';
import { api } from '../api';

const FALLBACK_CONFIG = {
  base_url: 'https://relay.catsco.cc',
  default_model: 'MiniMax-M2.7',
  endpoints: [
    { protocol: 'OpenAI-compatible', base_url: 'https://relay.catsco.cc/v1' },
    { protocol: 'Anthropic-compatible', base_url: 'https://relay.catsco.cc/anthropic' },
  ],
  key_hint: '访问凭证由 CatsCo 管理员发放。请妥善保存，泄露后可联系管理员撤销并重建。',
  docs_url: 'https://relay.catsco.cc',
  self_service_enabled: false,
};

function protocolLabel(protocol) {
  if (/anthropic/i.test(protocol)) return 'Anthropic SDK';
  if (/openai/i.test(protocol)) return 'OpenAI SDK';
  return protocol;
}

function endpointFor(config, pattern, fallbackPath) {
  const endpoint = config.endpoints?.find((item) => pattern.test(item.protocol));
  return endpoint?.base_url || `${config.base_url}${fallbackPath}`;
}

function configSnippet(config, plainKey) {
  const openAIBaseURL = endpointFor(config, /openai/i, '/v1');
  const anthropicBaseURL = endpointFor(config, /anthropic/i, '/anthropic');
  const keyLine = plainKey ? `API Key: ${plainKey}` : 'API Key: sk-...（在“我的 Key”里生成后复制）';
  return [
    'OpenAI 兼容',
    `Base URL: ${openAIBaseURL}`,
    `Model: ${config.default_model}`,
    keyLine,
    '',
    'Anthropic 兼容',
    `Base URL: ${anthropicBaseURL}`,
    `Model: ${config.default_model}`,
    keyLine,
  ].join('\n');
}

function relayStateLabel(relayKey, selfServiceEnabled, keyLoading) {
  if (!selfServiceEnabled) return '管理员发放';
  if (keyLoading) return '读取中';
  if (!relayKey) return '未生成 Key';
  if (relayKey.state === 'active') return 'Key 可用';
  if (relayKey.state === 'revoked') return 'Key 已撤销';
  if (relayKey.state === 'inactive') return 'Key 未启用';
  return relayKey.state || 'Key 可用';
}

function relayStateClass(relayKey, selfServiceEnabled) {
  if (!selfServiceEnabled || relayKey?.state === 'active') return 'active';
  return relayKey?.state || 'inactive';
}

function formatTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}

function formatCNY(value) {
  const number = Number(value || 0);
  return number.toLocaleString('zh-CN', {
    minimumFractionDigits: number > 0 && number < 1 ? 4 : 2,
    maximumFractionDigits: 4,
  });
}

function modelBudgetLabel(model) {
  if (!model || model === '*') return '通用额度';
  return model;
}

function summarizeCommercial(summary) {
  const totals = summary?.totals_by_model || {};
  const entries = Object.entries(totals)
    .filter(([, amount]) => Number(amount) > 0)
    .sort(([a], [b]) => a.localeCompare(b));
  if (!entries.length) return '暂无已发放额度';
  return entries.map(([model, amount]) => `${modelBudgetLabel(model)} ${formatCNY(amount)} CNY`).join(' · ');
}

function activeEntitlements(summary) {
  return (summary?.entitlements || []).filter((item) => item.state === 'active');
}

function commercialTotals(summary) {
  return Object.entries(summary?.totals_by_model || {})
    .filter(([, amount]) => Number(amount) > 0)
    .sort(([a], [b]) => a.localeCompare(b));
}

function extractPlainRelayKey(data) {
  const key = data?.key;
  const candidates = typeof key === 'string'
    ? [key]
    : [
        key?.key,
        key?.value,
        key?.plain_key,
        key?.api_key,
        key?.token,
        data?.plain_key,
        data?.api_key,
        data?.token,
        data?.key_value,
      ];
  const value = candidates.find(item => typeof item === 'string' && item.trim().startsWith('sk-bf-'));
  return value ? value.trim() : '';
}

export default function RelayAccessModal({ onClose }) {
  const [config, setConfig] = useState(FALLBACK_CONFIG);
  const [relayKey, setRelayKey] = useState(null);
  const [plainKey, setPlainKey] = useState('');
  const [loading, setLoading] = useState(true);
  const [keyLoading, setKeyLoading] = useState(false);
  const [actionLoading, setActionLoading] = useState('');
  const [commercial, setCommercial] = useState(null);
  const [inviteCode, setInviteCode] = useState('');
  const [inviteLoading, setInviteLoading] = useState(false);
  const [error, setError] = useState('');
  const [copied, setCopied] = useState('');

  useEffect(() => {
    let cancelled = false;
    async function load() {
      setLoading(true);
      setError('');
      try {
        const data = await api.getRelayConfig();
        if (cancelled) return;
        const nextConfig = {
          ...FALLBACK_CONFIG,
          ...data,
          endpoints: Array.isArray(data.endpoints) && data.endpoints.length > 0
            ? data.endpoints
            : FALLBACK_CONFIG.endpoints,
        };
        setConfig(nextConfig);
        if (nextConfig.self_service_enabled) {
          setKeyLoading(true);
          try {
            const keyData = await api.getRelayKey();
            if (!cancelled) setRelayKey(keyData.key || null);
          } finally {
            if (!cancelled) setKeyLoading(false);
          }
        }
        try {
          const commercialData = await api.getRelayCommercial();
          if (!cancelled) setCommercial(commercialData);
        } catch (err) {
          if (!cancelled) setCommercial(null);
        }
      } catch (err) {
        if (!cancelled) {
          console.warn('Failed to load relay config:', err);
          setError('配置读取失败，已显示默认配置');
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    }
    load();
    return () => {
      cancelled = true;
    };
  }, []);

  const snippet = useMemo(() => configSnippet(config, plainKey), [config, plainKey]);
  const stateText = relayStateLabel(relayKey, config.self_service_enabled, keyLoading);
  const stateClass = relayStateClass(relayKey, config.self_service_enabled);

  const copyText = async (key, text) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(key);
      window.setTimeout(() => setCopied(''), 1400);
    } catch (err) {
      setError('复制失败，请手动复制');
    }
  };

  const openRelayPortal = async () => {
    const portalWindow = window.open('about:blank', '_blank');
    if (portalWindow) {
      portalWindow.opener = null;
      portalWindow.document.title = '正在打开 CatsCo 中转站';
    }
    const navigatePortal = (url) => {
      if (portalWindow) {
        portalWindow.location.href = url;
        return;
      }
      window.open(url, '_blank', 'noopener,noreferrer');
    };

    setActionLoading('portal');
    setError('');
    try {
      const session = await api.createRelaySession();
      if (session?.url) {
        navigatePortal(session.url);
        return;
      }
      throw new Error('中转站登录链接生成失败');
    } catch (err) {
      const fallback = config.docs_url || config.base_url || FALLBACK_CONFIG.docs_url;
      setError(err.message || '自动登录中转站失败，已打开普通页面');
      navigatePortal(fallback);
    } finally {
      setActionLoading('');
    }
  };

  const applyKeyResponse = (data) => {
    const nextKey = data?.key || null;
    setRelayKey(nextKey);
    setPlainKey(extractPlainRelayKey(data));
  };

  const createKey = async () => {
    setActionLoading('create');
    setError('');
    setPlainKey('');
    try {
      applyKeyResponse(await api.createRelayKey());
    } catch (err) {
      setError(err.message || '生成 Key 失败');
    } finally {
      setActionLoading('');
    }
  };

  const rotateKey = async () => {
    if (!window.confirm('重新生成后，旧 Key 会立即失效。确定继续吗？')) return;
    setActionLoading('rotate');
    setError('');
    setPlainKey('');
    try {
      applyKeyResponse(await api.rotateRelayKey());
    } catch (err) {
      setError(err.message || '重新生成 Key 失败');
    } finally {
      setActionLoading('');
    }
  };

  const revealKey = async () => {
    setActionLoading('reveal');
    setError('');
    try {
      const data = await api.revealRelayKey();
      applyKeyResponse(data);
      const revealed = extractPlainRelayKey(data);
      if (revealed) {
        await copyText('plain-key', revealed);
      }
    } catch (err) {
      setError(err.message || '显示 Key 失败');
    } finally {
      setActionLoading('');
    }
  };

  const revokeKey = async () => {
    if (!window.confirm('撤销后，当前 Key 会失效。确定撤销吗？')) return;
    setActionLoading('revoke');
    setError('');
    try {
      await api.revokeRelayKey();
      setRelayKey(null);
      setPlainKey('');
    } catch (err) {
      setError(err.message || '撤销 Key 失败');
    } finally {
      setActionLoading('');
    }
  };

  const busy = Boolean(actionLoading);
  const commercialSummary = commercial?.summary;
  const commercialEnabled = commercial?.enabled === true && commercialSummary;
  const activePackages = activeEntitlements(commercialSummary);
  const modelTotals = commercialTotals(commercialSummary);

  const redeemInvite = async () => {
    const code = inviteCode.trim();
    if (!code) {
      setError('请输入邀请码。');
      return;
    }
    setInviteLoading(true);
    setError('');
    try {
      const data = await api.redeemRelayInvite(code);
      setCommercial({ ...(commercial || {}), enabled: true, summary: data.summary, note: data.note || commercial?.note });
      setInviteCode('');
      setCopied('invite');
      window.setTimeout(() => setCopied(''), 1400);
    } catch (err) {
      setError(err.message || '邀请码兑换失败');
    } finally {
      setInviteLoading(false);
    }
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal relay-access-modal" onClick={(event) => event.stopPropagation()}>
        <div className="oc-modal-header relay-access-header">
          <div>
            <h3>CatsCo 中转站</h3>
            <p>
              {config.self_service_enabled
                ? '生成并管理自己的中转 Key，接到第三方客户端或 CatsCo 自定义模型。'
                : '查看中转连接地址，并使用管理员发放的访问凭证。'}
            </p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>

        <div className="relay-access-body">
          {loading && <div className="oc-settings-secondary">正在读取中转配置...</div>}
          {error && <div className="oc-form-error">{error}</div>}

          <div className="relay-access-hero">
            <div className="relay-access-hero-main">
              <span className="relay-access-summary-icon"><Server size={18} /></span>
              <div>
                <div className="relay-access-eyebrow">当前中转</div>
                <div className="relay-access-title">{config.base_url}</div>
                <div className="oc-settings-secondary">默认模型：{config.default_model}</div>
              </div>
            </div>
            <div className="relay-access-hero-actions">
              <span className={`relay-access-state ${stateClass}`}>{stateText}</span>
              <button
                type="button"
                className="relay-access-primary-btn"
                onClick={() => copyText('snippet', snippet)}
                title="复制快速配置"
              >
                {copied === 'snippet' ? <Check size={15} /> : <Copy size={15} />}
                复制配置
              </button>
              {config.docs_url && (
                <button
                  type="button"
                  className="relay-access-open-btn"
                  onClick={openRelayPortal}
                  disabled={actionLoading === 'portal'}
                >
                  {actionLoading === 'portal' ? '登录中...' : '打开中转站'}
                  <ExternalLink size={14} />
                </button>
              )}
            </div>
          </div>

          <section className="relay-access-commerce">
            <div className="relay-access-section-head">
              <div>
                <div className="relay-access-title">套餐与邀请码</div>
                <div className="oc-settings-secondary">
                  {commercialEnabled
                    ? summarizeCommercial(commercialSummary)
                    : '套餐兑换暂未开放；当前仍使用默认 relay 额度和现有 Key。'}
                </div>
              </div>
              <span className={`relay-access-state ${commercialEnabled ? 'active' : 'inactive'}`}>
                {commercialEnabled ? '灰度开启' : '未开放'}
              </span>
            </div>

            <div className="relay-access-commerce-grid">
              <div className="relay-access-commerce-card">
                <Sparkles size={17} />
                <div>
                  <strong>{formatCNY(commercialSummary?.total_cny)} CNY</strong>
                  <span>已发放测试账本额度</span>
                </div>
              </div>
              <div className="relay-access-commerce-card">
                <Gift size={17} />
                <div>
                  <strong>{activePackages.length}</strong>
                  <span>当前有效套餐</span>
                </div>
              </div>
            </div>

            {commercialEnabled && modelTotals.length > 0 && (
              <div className="relay-access-budget-list">
                {modelTotals.map(([model, amount]) => (
                  <div key={model}>
                    <span>{modelBudgetLabel(model)}</span>
                    <strong>{formatCNY(amount)} CNY</strong>
                  </div>
                ))}
              </div>
            )}

            {commercialEnabled ? (
              <div className="relay-access-invite-form">
                <input
                  value={inviteCode}
                  onChange={(event) => setInviteCode(event.target.value)}
                  placeholder="输入邀请码兑换套餐额度"
                  disabled={inviteLoading}
                />
                <button type="button" disabled={inviteLoading} onClick={redeemInvite}>
                  {inviteLoading ? '兑换中...' : copied === 'invite' ? '已兑换' : '兑换'}
                </button>
              </div>
            ) : (
              <div className="relay-access-token-note">
                <Gift size={16} />
                <span>{commercial?.note || '套餐和邀请码仍在内部测试。现在不影响你的默认 relay 额度、Key 和模型调用。'}</span>
              </div>
            )}
            {commercialEnabled && (
              <div className="oc-settings-secondary">{commercial?.note || '管理员也可以在后台手动发放或调整用户额度。'}</div>
            )}
          </section>

          <div className="relay-access-connect">
            <div className="relay-access-section-head relay-access-section-head-compact">
              <div>
                <div className="relay-access-title">连接地址</div>
                <div className="oc-settings-secondary">按客户端 SDK 类型选择一个 Base URL。</div>
              </div>
            </div>
            <div className="relay-access-list">
              {config.endpoints.map((endpoint) => (
                <div className="relay-access-card" key={`${endpoint.protocol}:${endpoint.base_url}`}>
                  <div className="relay-access-card-copy">
                    <div className="relay-access-title">{protocolLabel(endpoint.protocol)}</div>
                    <div className="relay-access-url">{endpoint.base_url}</div>
                  </div>
                  <button
                    type="button"
                    className="relay-access-copy-btn"
                    aria-label={`复制 ${protocolLabel(endpoint.protocol)} 地址`}
                    title={`复制 ${protocolLabel(endpoint.protocol)} 地址`}
                    onClick={() => copyText(endpoint.protocol, endpoint.base_url)}
                  >
                    {copied === endpoint.protocol ? <Check size={16} /> : <Copy size={16} />}
                  </button>
                </div>
              ))}
            </div>
          </div>

          <section className="relay-access-key-panel">
            <div className="relay-access-section-head">
              <div>
                <div className="relay-access-title">我的 Key</div>
                <div className="oc-settings-secondary">
                  {config.self_service_enabled
                    ? '每个账号一把中转 Key，用于第三方客户端或 CatsCo 自定义模型。'
                    : '如需访问凭证，请联系管理员发放或重置。'}
                </div>
              </div>
              <span className={`relay-access-state ${stateClass}`}>{stateText}</span>
            </div>

            {!config.self_service_enabled && (
              <div className="relay-access-token-note">
                <KeyRound size={16} />
                <span>{config.key_hint}</span>
              </div>
            )}

            {config.self_service_enabled && keyLoading && (
              <div className="oc-settings-secondary">正在读取你的 Key...</div>
            )}

            {config.self_service_enabled && !keyLoading && !relayKey && (
              <div className="relay-access-empty-key">
                <KeyRound size={18} />
                <div>
                  <div className="relay-access-title">还没有中转 Key</div>
                  <div className="oc-settings-secondary">生成后只显示一次明文，请立刻复制到需要使用的客户端。</div>
                </div>
                <button type="button" className="relay-access-primary-btn" disabled={busy} onClick={createKey}>
                  {actionLoading === 'create' ? '生成中...' : '生成我的 Key'}
                </button>
              </div>
            )}

            {config.self_service_enabled && relayKey && (
              <div className="relay-access-key-card">
                <div className="relay-access-key-meta">
                  <div>
                    <span>名称</span>
                    <strong>{relayKey.name || 'CatsCo relay key'}</strong>
                  </div>
                  <div>
                    <span>前缀</span>
                    <strong>{relayKey.prefix || 'sk-...'}</strong>
                  </div>
                  <div>
                    <span>更新时间</span>
                    <strong>{formatTime(relayKey.updated_at) || '-'}</strong>
                  </div>
                </div>

                {plainKey && (
                  <div className="relay-access-secret-box">
                    <AlertTriangle size={16} />
                    <div>
                      <div>Key 明文已显示并可复制，请只放在你信任的客户端里。</div>
                      <code>{plainKey}</code>
                    </div>
                    <button type="button" onClick={() => copyText('plain-key', plainKey)}>
                      {copied === 'plain-key' ? <Check size={15} /> : <Copy size={15} />}
                      复制 Key
                    </button>
                  </div>
                )}

                <div className="relay-access-key-actions">
                  <button type="button" disabled={busy} onClick={revealKey}>
                    <Copy size={15} />
                    {actionLoading === 'reveal' ? '显示中...' : '显示并复制'}
                  </button>
                  <button type="button" disabled={busy} onClick={rotateKey}>
                    <RotateCcw size={15} />
                    {actionLoading === 'rotate' ? '重新生成中...' : '重新生成'}
                  </button>
                  <button type="button" className="danger" disabled={busy} onClick={revokeKey}>
                    <Trash2 size={15} />
                    {actionLoading === 'revoke' ? '撤销中...' : '撤销'}
                  </button>
                </div>
              </div>
            )}
          </section>

          <div className="relay-access-snippet">
            <div className="relay-access-snippet-head">
              <span>快速配置</span>
              <button type="button" onClick={() => copyText('snippet', snippet)} aria-label="复制快速配置" title="复制快速配置">
                {copied === 'snippet' ? <Check size={15} /> : <Copy size={15} />}
                复制
              </button>
            </div>
            <pre>{snippet}</pre>
          </div>
        </div>
      </div>
    </div>
  );
}
