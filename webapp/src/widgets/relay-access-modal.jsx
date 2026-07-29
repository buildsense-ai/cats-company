import React, { useEffect, useMemo, useState } from 'react';
import { AlertTriangle, CalendarDays, Check, Copy, CreditCard, ExternalLink, Gift, KeyRound, ReceiptText, RotateCcw, Server, Sparkles, Trash2, X } from 'lucide-react';
import { api } from '../api';
import { InlineFeedback, useFeedback } from '../components/feedback-system';

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

const COMMERCIAL_PLAN_PRESENTATION = {
  'catsco-trial-3d': {
    kicker: '先感受',
    tagline: '先跑一次真实任务',
    audience: '首次体验 · 每位用户限购一次',
    usageLabel: '体验用量',
  },
  'catsco-plus-minus': {
    kicker: '轻松开始',
    tagline: '轻量日常助手',
    audience: '轻度使用 · 从体验转向月度使用',
    usageLabel: '轻量用量',
  },
  'catsco-plus': {
    kicker: '日常主力',
    tagline: '稳定日常协作',
    audience: '稳定日用 · 默认推荐',
    usageLabel: '标准用量',
    recommended: true,
  },
  'catsco-plus-plus': {
    kicker: '高频推进',
    tagline: '复杂任务与高频推进',
    audience: '个人高频 · 专业工作流',
    usageLabel: '高频用量',
  },
  'catsco-team-monthly': {
    kicker: '多人协作',
    tagline: '多人共享与并行协作',
    audience: '多人使用 · 重度协作',
    usageLabel: '团队用量',
    wide: true,
  },
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

function formatShortDate(value) {
  if (!value) return '长期有效';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleDateString('zh-CN', { month: 'short', day: 'numeric' });
}

function formatShortDateTime(value) {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('zh-CN', {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  });
}

function formatPriceFen(value) {
  return `¥${(Number(value || 0) / 100).toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: 2 })}`;
}

function commercialPlanPresentation(plan) {
  return COMMERCIAL_PLAN_PRESENTATION[plan?.slug] || {
    kicker: '协作套餐',
    tagline: plan?.name || '稳定协作',
    audience: `${Number(plan?.duration_days || 30)} 天有效`,
    usageLabel: '套餐用量',
  };
}

function commercialPlanCycle(plan) {
  const durationDays = Number(plan?.duration_days || 30);
  if (durationDays === 3) return '/ 3天';
  if (durationDays === 30) return '/ 月';
  return `/ ${durationDays}天`;
}

function commercialOrderStatus(status) {
  return {
    created: '创建中',
    pending: '待支付',
    paid: '已支付',
    fulfilled: '已到账',
    closed: '已关闭',
    failed: '创建失败',
    refunding: '退款中',
    refunded: '已退款',
  }[status] || status || '未知状态';
}

function newCommercialClientRequestID() {
  const random = globalThis.crypto?.randomUUID?.().replaceAll('-', '') || Math.random().toString(36).slice(2);
  return `order_${Date.now()}_${random}`.slice(0, 64);
}

function formatPercent(value) {
  const number = Number(value || 0);
  if (!Number.isFinite(number)) return '0%';
  return `${Math.max(0, number).toFixed(number > 0 && number < 1 ? 2 : 1).replace(/\.0$/, '')}%`;
}

function modelBudgetLabel(model) {
  if (!model || model === '*') return '通用额度';
  return model;
}

function summarizeCommercial(summary) {
  const models = commercialModels(summary);
  if (!models.length) return '暂无已发放额度';
  return `${models.length} 个模型额度可用 · 具体容量按用量百分比展示`;
}

function commercialUsageTextForUser(plan, presentation) {
  return `${presentation?.usageLabel || '套餐用量'} · ${Number(plan?.duration_days || 30)} 天有效`;
}

function activeEntitlements(summary) {
  return (summary?.entitlements || []).filter((item) => item.state === 'active');
}

function commercialModels(summary) {
  return [...(summary?.models || [])].filter(Boolean).sort((a, b) => a.localeCompare(b));
}

function modelUsageKey(model) {
  return String(model || '').trim();
}

function resetDurationLabel(value) {
  const raw = String(value || '').trim();
  if (!raw) return '重置周期未同步';
  const match = raw.match(/^(\d+)([dDwWmMyY])$/);
  if (!match) return `按 ${raw} 重置`;
  const amount = Number(match[1]);
  const unit = match[2].toLowerCase();
  if (amount === 1) {
    const oneUnitLabel = {
      d: '每天重置',
      w: '每周重置',
      m: '每月重置',
      y: '每年重置',
    }[unit];
    if (oneUnitLabel) return oneUnitLabel;
  }
  const unitLabel = {
    d: '天',
    w: '周',
    m: '个月',
    y: '年',
  }[unit] || '';
  return `每 ${amount} ${unitLabel}重置`;
}

function addResetDuration(lastReset, duration) {
  const date = new Date(lastReset || '');
  if (Number.isNaN(date.getTime())) return null;
  const match = String(duration || '').trim().match(/^(\d+)([dDwWmMyY])$/);
  if (!match) return null;
  const amount = Number(match[1]);
  const unit = match[2].toLowerCase();
  const next = new Date(date.getTime());
  if (unit === 'd') next.setDate(next.getDate() + amount);
  if (unit === 'w') next.setDate(next.getDate() + amount * 7);
  if (unit === 'm') next.setMonth(next.getMonth() + amount);
  if (unit === 'y') next.setFullYear(next.getFullYear() + amount);
  return next.toISOString();
}

function usageResetInfo(summary) {
  if (typeof summary === 'undefined') {
    return {
      title: '额度周期读取中',
      detail: '等待后台同步',
      note: '同步后会显示当前额度周期、上次重置和预计下次重置时间。',
    };
  }
  if (!summary) {
    return {
      title: '额度周期未同步',
      detail: '暂未拿到用量数据',
      note: '当前暂未拿到额度周期；套餐到期时间仍以上方套餐为准。',
    };
  }
  if (summary.source === 'custom' || summary.status === 'custom') {
    return {
      title: '自定义模型',
      detail: '不使用 CatsCo 模型服务额度',
      note: '自定义模型的额度和重置时间由你自己的服务商决定。',
    };
  }
  const label = resetDurationLabel(summary.reset_duration);
  const lastReset = formatShortDateTime(summary.last_reset);
  const nextReset = formatShortDateTime(addResetDuration(summary.last_reset, summary.reset_duration));
  if (!summary.reset_duration && !summary.last_reset) {
    return {
      title: '额度周期读取中',
      detail: '等待后台同步',
      note: '同步后会显示当前额度周期、上次重置和预计下次重置时间。',
    };
  }
  return {
    title: label,
    detail: nextReset ? `下次 ${nextReset}` : '重置时间同步中',
    note: lastReset
      ? `当前显示的是本周期额度；上次重置 ${lastReset}，不是自然月。`
      : '当前显示的是本周期额度；重置时间同步中，不影响当前额度使用。',
  };
}

function usageStateForModel(usageByModel, model) {
  const key = modelUsageKey(model);
  if (!Object.prototype.hasOwnProperty.call(usageByModel, key)) {
    return { loading: true, summary: null };
  }
  return { loading: false, summary: usageByModel[key] || null };
}

function currentModelText(summary, fallbackModel) {
  if (typeof summary === 'undefined') return '当前模型读取中';
  if (summary?.source === 'custom' || summary?.status === 'custom') return '当前使用自定义模型';
  if (summary?.model) return `当前模型：${summary.model}`;
  return `默认模型：${fallbackModel}`;
}

function currentQuotaDisplay(summary, fallbackModel, commercialEnabled) {
  if (typeof summary === 'undefined') {
    return {
      className: 'loading',
      model: '读取中',
      title: '当前模型额度',
      meta: '正在读取 relay 当前模型',
      detail: '等待后台同步',
      percent: 0,
      note: '会按 CatsCo 当前启动模型展示对应额度。',
    };
  }
  if (!summary) {
    return {
      className: 'inactive',
      model: fallbackModel,
      title: '当前模型额度',
      meta: commercialEnabled ? 'relay 用量暂未同步' : '暂未接入套餐',
      detail: commercialEnabled ? '暂无用量数据' : '套餐兑换后显示额度',
      percent: 0,
      note: commercialEnabled
        ? '如果刚切换模型，数据可能延迟几分钟刷新。'
        : '当前仍可使用管理员默认模型服务额度或自定义模型。',
    };
  }
  if (summary.source === 'custom' || summary.status === 'custom') {
    return {
      className: 'custom',
      model: '自定义模型',
      title: '当前使用自定义模型',
      meta: '不消耗 CatsCo 模型服务套餐',
      detail: '额度由你自己的服务商决定',
      percent: 0,
      note: '切回 CatsCo 模型服务后，这里会显示对应模型的剩余额度。',
    };
  }

  if (summary.quota_configured !== true) {
    return {
      className: 'inactive',
      model: summary.model || fallbackModel,
      title: '当前模型未设置额度',
      meta: `${summary.provider ? `${summary.provider} · ` : ''}用量待同步`,
      detail: '等待模型限额同步',
      percent: 0,
      note: '管理员同步模型额度后，这里会显示剩余额度和用量百分比。',
    };
  }
  const rawPercent = Number(summary.percent || 0);
  const percent = Math.min(100, Math.max(0, rawPercent));
  const remainingPercent = Math.max(0, Number(summary.remaining_percent ?? (100 - percent)));
  const overLimit = summary.status === 'over_limit';
  const high = !overLimit && (summary.status === 'high' || percent >= 90);
  const usedLabel = overLimit ? '已用 100%+' : `已用 ${formatPercent(percent)}`;
  const remainingLabel = overLimit ? '剩余 0%' : `剩余 ${formatPercent(remainingPercent)}`;
  return {
    className: overLimit ? 'danger' : high ? 'warning' : 'active',
    model: summary.model || fallbackModel,
    title: overLimit ? '当前模型已超额' : high ? '当前模型接近上限' : '当前模型额度',
    meta: `${summary.provider ? `${summary.provider} · ` : ''}${usedLabel}`,
    detail: remainingLabel,
    percent,
    note: overLimit
      ? '这组模型额度已超出，后续调用应被 relay 拦截；请联系管理员补额或重置。'
      : '按当前启动模型展示，切换模型后可能延迟几分钟刷新。',
  };
}

function budgetUsageDisplay(model, usageByModel) {
  const { loading, summary: usage } = usageStateForModel(usageByModel, model);
  if (loading) return { label: '读取中', meta: '用量读取中' };
  if (!usage) return { label: '待同步', meta: '未同步到 relay' };
  if (usage.source === 'custom' || usage.status === 'custom') {
    return { label: '自备额度', meta: '自定义模型不计入模型服务套餐' };
  }
  if (!usage.model || usage.quota_configured !== true) return { label: '待同步', meta: '未同步到 relay' };
  const overLimit = usage.status === 'over_limit';
  const rawPercent = Number(usage.percent || 0);
  const percent = Math.min(100, Math.max(0, rawPercent));
  const remainingPercent = Math.max(0, Number(usage.remaining_percent ?? (100 - percent)));
  const resetLabel = usage.reset_duration ? ` · ${resetDurationLabel(usage.reset_duration)}` : '';
  return {
    label: overLimit ? '已用 100%+' : `已用 ${formatPercent(percent)}`,
    meta: `${overLimit ? '已超额 · ' : ''}剩余 ${formatPercent(overLimit ? 0 : remainingPercent)}${resetLabel}`,
  };
}

function nearestPackageExpiry(packages) {
  const dates = packages
    .map((item) => new Date(item.expires_at || '').getTime())
    .filter((time) => Number.isFinite(time));
  if (!dates.length) return '';
  return new Date(Math.min(...dates)).toISOString();
}

function commercialRolloutLabel(commercial) {
  if (commercial?.enforce_enabled) return '已接管';
  if (commercial?.enabled) return '账本灰度';
  return '未开放';
}

function paymentChannelLabel(channels, channel) {
  const configured = channels.find(item => item.id === channel)?.label;
  if (configured) return configured;
  if (channel === 'alipay_page') return '支付宝支付';
  if (channel === 'test') return '灰度测试支付';
  return '在线支付';
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
  const feedback = useFeedback();
  const [config, setConfig] = useState(FALLBACK_CONFIG);
  const [relayKey, setRelayKey] = useState(null);
  const [plainKey, setPlainKey] = useState('');
  const [loading, setLoading] = useState(true);
  const [keyLoading, setKeyLoading] = useState(false);
  const [actionLoading, setActionLoading] = useState('');
  const [commercial, setCommercial] = useState(null);
  const [commercialCatalog, setCommercialCatalog] = useState(null);
  const [commercialOrders, setCommercialOrders] = useState([]);
  const [paymentChannel, setPaymentChannel] = useState('');
  const [checkoutOrder, setCheckoutOrder] = useState(null);
  const [paymentLoading, setPaymentLoading] = useState('');
  const [trialLoading, setTrialLoading] = useState(false);
  const [currentUsage, setCurrentUsage] = useState(undefined);
  const [usageByModel, setUsageByModel] = useState({});
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
        try {
          const [catalogData, ordersData] = await Promise.all([
            api.getCommercialCatalog(),
            api.getCommercialOrders(),
          ]);
          if (!cancelled) {
            setCommercialCatalog(catalogData);
            setCommercialOrders(Array.isArray(ordersData?.orders) ? ordersData.orders : []);
            const channels = Array.isArray(catalogData?.channels) ? catalogData.channels : [];
            setPaymentChannel(channels[0]?.id || '');
          }
        } catch {
          if (!cancelled) {
            setCommercialCatalog(null);
            setCommercialOrders([]);
          }
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
      portalWindow.document.title = '正在打开 CatsCo 模型服务';
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
      throw new Error('模型服务登录链接生成失败');
    } catch (err) {
      const fallback = config.docs_url || config.base_url || FALLBACK_CONFIG.docs_url;
      setError(err.message || '自动登录模型服务失败，已打开普通页面');
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
    const confirmed = await feedback.confirm({
      title: '重新生成 Key？',
      message: '重新生成后，旧 Key 会立即失效，使用旧 Key 的客户端需要重新配置。',
      confirmLabel: '重新生成',
      tone: 'danger',
    });
    if (!confirmed) return;
    setActionLoading('rotate');
    setError('');
    setPlainKey('');
    try {
      applyKeyResponse(await api.rotateRelayKey());
      feedback.notify({ tone: 'success', message: 'Key 已重新生成' });
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
    const confirmed = await feedback.confirm({
      title: '撤销当前 Key？',
      message: '撤销后，当前 Key 会立即失效。',
      confirmLabel: '撤销 Key',
      tone: 'danger',
    });
    if (!confirmed) return;
    setActionLoading('revoke');
    setError('');
    try {
      await api.revokeRelayKey();
      setRelayKey(null);
      setPlainKey('');
      feedback.notify({ tone: 'success', message: 'Key 已撤销' });
    } catch (err) {
      setError(err.message || '撤销 Key 失败');
    } finally {
      setActionLoading('');
    }
  };

  const busy = Boolean(actionLoading);
  const commercialSummary = commercial?.summary;
  const commercialEnabled = commercial?.enabled === true && commercialSummary;
  const commercialEnforced = commercial?.enforce_enabled === true;
  const salePlans = Array.isArray(commercialCatalog?.plans) ? commercialCatalog.plans : [];
  const paymentChannels = Array.isArray(commercialCatalog?.channels) ? commercialCatalog.channels : [];
  const checkoutPaymentLabel = paymentChannelLabel(paymentChannels, checkoutOrder?.channel);
  const activePackages = activeEntitlements(commercialSummary);
  const packageModels = commercialModels(commercialSummary);
  const packageExpiry = nearestPackageExpiry(activePackages);
  const packageExpiryText = activePackages.length > 0 ? formatShortDate(packageExpiry) : '无套餐';
  const currentResetInfo = usageResetInfo(currentUsage);
  const currentQuota = currentQuotaDisplay(currentUsage, config.default_model, commercialEnabled);

  useEffect(() => {
    let cancelled = false;
    api.getRelayUsage()
      .then((data) => {
        if (!cancelled) setCurrentUsage(data?.summary || null);
      })
      .catch(() => {
        if (!cancelled) setCurrentUsage(null);
      });
    return () => {
      cancelled = true;
    };
  }, [relayKey?.prefix, JSON.stringify(commercial?.summary?.models || [])]);

  useEffect(() => {
    let cancelled = false;
    const models = packageModels.map(modelUsageKey).filter(Boolean);
    if (!commercialEnabled || models.length === 0) {
      setUsageByModel({});
      return () => {
        cancelled = true;
      };
    }

    setUsageByModel((prev) => {
      const next = {};
      models.forEach((model) => {
        if (prev[model]) next[model] = prev[model];
      });
      return next;
    });

    Promise.all(models.map(async (model) => {
      try {
        const data = await api.getRelayUsage({ model });
        return [model, data?.summary || null];
      } catch {
        return [model, null];
      }
    })).then((entries) => {
      if (cancelled) return;
      const next = {};
      entries.forEach(([model, summary]) => {
        next[model] = summary;
      });
      setUsageByModel(next);
    });

    return () => {
      cancelled = true;
    };
  }, [commercialEnabled, JSON.stringify(packageModels)]);

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

  const refreshCommercialState = async () => {
    const [commercialData, catalogData, ordersData] = await Promise.all([
      api.getRelayCommercial(),
      api.getCommercialCatalog(),
      api.getCommercialOrders(),
    ]);
    setCommercial(commercialData);
    setCommercialCatalog(catalogData);
    setCommercialOrders(Array.isArray(ordersData?.orders) ? ordersData.orders : []);
    const channels = Array.isArray(catalogData?.channels) ? catalogData.channels : [];
    setPaymentChannel((current) => channels.some((channel) => channel.id === current) ? current : (channels[0]?.id || ''));
  };

  const createCommercialOrder = async (plan) => {
    if (!paymentChannel) {
      setError('支付通道尚未配置。');
      return;
    }
    setPaymentLoading(`create:${plan.id}`);
    setError('');
    try {
      const data = await api.createCommercialOrder(plan.id, paymentChannel, newCommercialClientRequestID());
      setCheckoutOrder(data.order || null);
      setCommercialOrders((current) => {
        const next = (current || []).filter((item) => item.order_no !== data.order?.order_no);
        return data.order ? [data.order, ...next] : next;
      });
    } catch (err) {
      setError(err.message || '创建订单失败');
    } finally {
      setPaymentLoading('');
    }
  };

  const confirmCommercialTestPayment = async () => {
    if (!checkoutOrder?.order_no) return;
    setPaymentLoading(`confirm:${checkoutOrder.order_no}`);
    setError('');
    try {
      const data = await api.confirmCommercialTestPayment(checkoutOrder.order_no);
      setCheckoutOrder(data.order || checkoutOrder);
      await refreshCommercialState();
    } catch (err) {
      setError(err.message || '测试支付失败');
    } finally {
      setPaymentLoading('');
    }
  };

  const claimCommercialTrial = async () => {
    setTrialLoading(true);
    setError('');
    try {
      const data = await api.claimCommercialTrial();
      setCommercial({ ...(commercial || {}), enabled: true, summary: data.summary });
      await refreshCommercialState();
    } catch (err) {
      setError(err.message || '体验包领取失败');
    } finally {
      setTrialLoading(false);
    }
  };

  useEffect(() => {
    if (!checkoutOrder?.order_no || checkoutOrder.status !== 'pending') return undefined;
    let cancelled = false;
    const poll = async () => {
      try {
        const data = await api.getCommercialOrders(checkoutOrder.order_no);
        if (cancelled || !data?.order) return;
        setCheckoutOrder(data.order);
        if (data.order.status === 'fulfilled') {
          await refreshCommercialState();
        }
      } catch {
        // Keep the payment panel open; the next poll or callback can recover.
      }
    };
    const timer = window.setInterval(poll, 2500);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [checkoutOrder?.order_no, checkoutOrder?.status]);

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal relay-access-modal" onClick={(event) => event.stopPropagation()}>
        <div className="oc-modal-header relay-access-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3>CatsCo 模型服务</h3>
            <p>
              {config.self_service_enabled
                ? '生成并管理自己的模型服务 Key，接到第三方客户端或 CatsCo 自定义模型。'
                : '查看模型服务连接地址，并使用管理员发放的访问凭证。'}
            </p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>

        <div className="relay-access-body">
          {loading && <div className="oc-settings-secondary">正在读取中转配置...</div>}
          {error && <InlineFeedback tone="error">{error}</InlineFeedback>}

          <div className="relay-access-hero">
            <div className="relay-access-hero-main">
              <span className="relay-access-summary-icon"><Server size={18} /></span>
              <div>
                <div className="relay-access-eyebrow">模型服务</div>
                <div className="relay-access-title">{config.base_url}</div>
                <div className="oc-settings-secondary">{currentModelText(currentUsage, config.default_model)}</div>
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
                  {actionLoading === 'portal' ? '登录中...' : '打开模型服务'}
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
                    : '套餐兑换暂未开放；当前仍使用默认模型服务额度和现有 Key。'}
                </div>
              </div>
              <span className={`relay-access-state ${commercialEnabled ? 'active' : 'inactive'}`}>
                {commercialRolloutLabel(commercial)}
              </span>
            </div>

            <div className={`relay-access-current-quota ${currentQuota.className}`}>
              <div className="relay-access-current-quota-head">
                <div>
                  <span>{currentQuota.title}</span>
                  <strong>{currentQuota.model}</strong>
                </div>
                <em>{currentQuota.detail}</em>
              </div>
              <div className="relay-access-current-quota-meta">{currentQuota.meta}</div>
              <div className="relay-access-quota-bar" aria-label={`当前模型用量 ${formatPercent(currentQuota.percent)}`}>
                <i style={{ width: `${Math.min(100, Math.max(0, currentQuota.percent))}%` }} />
              </div>
              <div className="relay-access-period-note">{currentQuota.note}</div>
            </div>

            <div className="relay-access-commerce-grid">
              <div className="relay-access-commerce-card">
                <Sparkles size={17} />
                <div>
                  <strong>{commercialEnabled && currentUsage ? formatPercent(currentQuota.percent) : '待同步'}</strong>
                  <span>当前模型用量</span>
                </div>
              </div>
              <div className="relay-access-commerce-card">
                <Gift size={17} />
                <div>
                  <strong>{activePackages.length > 0 ? activePackages.length : '无套餐'}</strong>
                  <span>当前有效套餐</span>
                </div>
              </div>
              <div className="relay-access-commerce-card">
                <CalendarDays size={17} />
                <div>
                  <strong>{packageExpiryText}</strong>
                  <span>套餐最近到期</span>
                </div>
              </div>
              <div className="relay-access-commerce-card">
                <RotateCcw size={17} />
                <div>
                  <strong>{currentResetInfo.title}</strong>
                  <span>{currentResetInfo.detail}</span>
                </div>
              </div>
            </div>
            <div className="relay-access-period-note">{currentResetInfo.note}</div>

            {commercialCatalog?.trial_available && (
              <div className="relay-access-trial-row">
                <div>
                  <strong>新用户体验包</strong>
                  <span>领取后会直接加入当前模型额度，每个账号限一次。</span>
                </div>
                <button type="button" onClick={claimCommercialTrial} disabled={trialLoading}>
                  {trialLoading ? '领取中...' : '领取体验包'}
                </button>
              </div>
            )}

            {salePlans.length > 0 && (
              <div className="relay-access-storefront">
                <div className="relay-access-storefront-head">
                  <div>
                    <div className="relay-access-mini-title">选一档，开始你的协作节奏</div>
                    <span>先选与你当前节奏匹配的一档，需要更多时再升级。</span>
                  </div>
                  {paymentChannels.length > 1 ? (
                    <select value={paymentChannel} onChange={(event) => setPaymentChannel(event.target.value)}>
                      {paymentChannels.map((channel) => (
                        <option value={channel.id} key={channel.id}>{channel.label}</option>
                      ))}
                    </select>
                  ) : paymentChannels.length === 1 ? (
                    <span className="relay-access-payment-channel">{paymentChannels[0].label}</span>
                  ) : null}
                </div>
                <div className="relay-access-plan-list">
                  {salePlans.map((plan) => {
                    const presentation = commercialPlanPresentation(plan);
                    return (
                      <article
                        className={`relay-access-plan-row${presentation.recommended ? ' recommended' : ''}${presentation.wide ? ' wide' : ''}`}
                        key={plan.id}
                      >
                        <div className="relay-access-plan-heading">
                          <span className="relay-access-plan-kicker">{presentation.kicker}</span>
                          {presentation.recommended && <span className="relay-access-plan-badge">推荐</span>}
                        </div>
                        <strong className="relay-access-plan-name">{plan.name}</strong>
                        <div className="relay-access-plan-price">
                          <strong>{formatPriceFen(plan.price_fen)}</strong>
                          <span>{commercialPlanCycle(plan)}</span>
                        </div>
                        <span className="relay-access-plan-tagline">{presentation.tagline}</span>
                        <p>{plan.description || `${plan.duration_days} 天有效`}</p>
                        <em>{commercialUsageTextForUser(plan, presentation)}</em>
                        <span className="relay-access-plan-audience">适合：{presentation.audience}</span>
                        <div className="relay-access-plan-action">
                          <button
                            type="button"
                            onClick={() => createCommercialOrder(plan)}
                            disabled={!paymentChannel || Boolean(paymentLoading)}
                          >
                            <CreditCard size={15} />
                            {paymentLoading === `create:${plan.id}`
                              ? '创建中...'
                              : paymentChannels.length === 0 ? '暂未开放' : '购买'}
                          </button>
                        </div>
                      </article>
                    );
                  })}
                </div>
                {paymentChannels.length === 0 && (
                  <div className="relay-access-period-note">支付宝材料与支付通道仍在准备，当前套餐只能通过邀请码或后台发放。</div>
                )}
              </div>
            )}

            {checkoutOrder && (
              <div className={`relay-access-checkout ${checkoutOrder.status}`}>
                <div className="relay-access-checkout-head">
                  <div>
                    <strong>{checkoutOrder.plan_name}</strong>
                    <span>订单 {checkoutOrder.order_no}</span>
                  </div>
                  <em>{commercialOrderStatus(checkoutOrder.status)}</em>
                </div>
                {checkoutOrder.status === 'pending' && checkoutOrder.checkout_url && (
                  <div className="relay-access-payment-redirect">
                    <div>
                      <strong>{checkoutPaymentLabel} {formatPriceFen(checkoutOrder.amount_fen)}</strong>
                      <span>将在支付宝官方收银台完成付款；支付成功后此处会自动更新。</span>
                    </div>
                    <a
                      href={checkoutOrder.checkout_url}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      前往支付宝付款
                      <ExternalLink size={15} />
                    </a>
                  </div>
                )}
                {checkoutOrder.status === 'pending' && checkoutOrder.channel === 'test' && commercialCatalog?.test_mode && (
                  <button
                    type="button"
                    className="relay-access-test-payment"
                    onClick={confirmCommercialTestPayment}
                    disabled={Boolean(paymentLoading)}
                  >
                    {paymentLoading === `confirm:${checkoutOrder.order_no}` ? '入账中...' : '完成灰度测试支付'}
                  </button>
                )}
                {checkoutOrder.status === 'fulfilled' && (
                  <div className="relay-access-payment-success"><Check size={16} />支付成功，套餐额度已到账。</div>
                )}
              </div>
            )}

            {commercialOrders.length > 0 && (
              <div className="relay-access-order-list">
                <div className="relay-access-mini-title"><ReceiptText size={15} />最近订单</div>
                {commercialOrders.slice(0, 5).map((order) => (
                  <button type="button" key={order.order_no} onClick={() => setCheckoutOrder(order)}>
                    <span>
                      <strong>{order.plan_name}</strong>
                      <em>{formatShortDateTime(order.created_at)}</em>
                    </span>
                    <span>
                      <strong>{formatPriceFen(order.amount_fen)}</strong>
                      <em>{commercialOrderStatus(order.status)}</em>
                    </span>
                  </button>
                ))}
              </div>
            )}

            {commercialEnabled && activePackages.length > 0 && (
              <div className="relay-access-package-list">
                <div className="relay-access-mini-title">当前套餐</div>
                {activePackages.map((item) => (
                  <div className="relay-access-package-row" key={`${item.id || item.plan_id}-${item.source_ref || item.starts_at}`}>
                    <span>{item.plan_name || item.plan_slug || '套餐'}</span>
                    <strong>{formatShortDate(item.expires_at)}</strong>
                  </div>
                ))}
              </div>
            )}

            {commercialEnabled && activePackages.length === 0 && (
              <div className="relay-access-token-note">
                <Gift size={16} />
                <span>当前没有有效套餐。可以输入邀请码兑换，或联系管理员发放额度。</span>
              </div>
            )}

            {commercialEnabled && packageModels.length > 0 && (
              <div className="relay-access-budget-list">
                {packageModels.map((model) => {
                  const usageDisplay = budgetUsageDisplay(model, usageByModel);
                  return (
                    <div key={model}>
                      <span>
                        <strong>{modelBudgetLabel(model)}</strong>
                        <em>{usageDisplay.meta}</em>
                      </span>
                      <strong>{usageDisplay.label}</strong>
                    </div>
                  );
                })}
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
                <span>{commercial?.note || '套餐和邀请码仍在内部测试。现在不影响你的默认模型服务额度、Key 和模型调用。'}</span>
              </div>
            )}
            {commercialEnabled && (
              <div className="oc-settings-secondary">
                {commercialEnforced
                  ? '套餐额度已接入模型限额；管理员仍可在后台手动调额或重置用量。'
                  : (commercial?.note || '套餐额度先记录在账本里；需要管理员后台对账/同步后，才会成为 relay 真实模型限额。')}
              </div>
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
                    ? '每个账号一把模型服务 Key，用于第三方客户端或 CatsCo 自定义模型。'
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
                  <div className="relay-access-title">还没有模型服务 Key</div>
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
