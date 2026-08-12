import React, { useEffect, useMemo, useRef, useState } from 'react';
import { AlertTriangle, BadgeCheck, CalendarDays, Check, Copy, CreditCard, ExternalLink, Gift, History, KeyRound, LayoutGrid, ReceiptText, RotateCcw, Server, Sparkles, Trash2, X } from 'lucide-react';
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
  'catsco-personal': {
    kicker: 'CATSCO PERSONAL',
    tagline: '标准任务容量',
    audience: '稳定日用 · 个人自动化',
    usageLabel: '个人版用量',
    features: [
      '云端 XiaoBa 与持续会话',
      '在授权设备、文件和工具间执行任务',
      '后台任务与主动协作',
      '个人 Skill 与使用偏好持续沉淀',
      '常规响应优先级',
    ],
  },
  'catsco-pro': {
    kicker: 'CATSCO PRO',
    tagline: '约 3 倍任务容量',
    audience: '高频使用 · 复杂工作流',
    usageLabel: '专业版用量',
    recommended: true,
    pro: true,
    features: [
      '包含个人版全部能力',
      '约为个人版 3 倍的任务容量',
      '更高并发与后台任务容量',
      '复杂任务优先获得更强执行能力',
      '高峰期更高响应优先级',
      '更宽松的公平使用边界',
    ],
  },
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

const FREE_PLAN_PRESENTATION = {
  kicker: 'CATSCO FREE',
  name: '免费版',
  tagline: '基础体验范围',
  description: '适合先体验 XiaoBa 的基础工作方式。',
  features: [
    '云端 XiaoBa 与基础会话',
    '在授权范围内尝试基础任务',
    '查看任务过程与交付结果',
  ],
};

const CURRENT_COMMERCIAL_PLAN_SLUGS = new Set(['catsco-personal', 'catsco-pro']);

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
  return `¥${formatPriceAmountFen(value)}`;
}

function formatPriceAmountFen(value) {
  return (Number(value || 0) / 100).toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: 2 });
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
    paid: '支付成功',
    fulfilled: '已生效',
    closed: '已关闭',
    failed: '创建失败',
    refunding: '退款中',
    refunded: '已退款',
    cancelled: '已取消',
  }[status] || status || '未知状态';
}

function commercialOrderTone(status) {
  if (status === 'fulfilled') return 'success';
  if (['created', 'pending', 'paid', 'refunding'].includes(status)) return 'pending';
  if (status === 'refunded') return 'refunded';
  return 'muted';
}

function commercialOrderEventTime(order) {
  if (order?.status === 'fulfilled') return order.fulfilled_at || order.paid_at || order.updated_at;
  if (order?.status === 'paid') return order.paid_at || order.updated_at;
  return order?.updated_at || order?.created_at;
}

function newCommercialClientRequestID() {
  const random = globalThis.crypto?.randomUUID?.().replaceAll('-', '') || Math.random().toString(36).slice(2);
  return `order_${Date.now()}_${random}`.slice(0, 64);
}

const COMMERCIAL_PAYMENT_REQUEST_STORAGE_KEY = 'catsco_commercial_payment_request_ids_v1';

function commercialClientRequestIDValid(value) {
  return typeof value === 'string' && /^[a-zA-Z0-9][a-zA-Z0-9_-]{7,63}$/.test(value);
}

function loadCommercialPaymentRequestIDs() {
  try {
    const parsed = JSON.parse(window.sessionStorage.getItem(COMMERCIAL_PAYMENT_REQUEST_STORAGE_KEY) || '{}');
    const freshAfter = Date.now() - (30 * 60 * 1000);
    return new Map(Object.entries(parsed).flatMap(([key, value]) => {
      const id = typeof value === 'string' ? value : value?.id;
      const createdAt = typeof value === 'string' ? Date.now() : Number(value?.created_at || 0);
      return key && commercialClientRequestIDValid(id) && createdAt >= freshAfter ? [[key, id]] : [];
    }));
  } catch {
    return new Map();
  }
}

function saveCommercialPaymentRequestIDs(requestIDs) {
  try {
    window.sessionStorage.setItem(
      COMMERCIAL_PAYMENT_REQUEST_STORAGE_KEY,
      JSON.stringify(Object.fromEntries(
        [...requestIDs].map(([key, id]) => [key, { id, created_at: Date.now() }]),
      )),
    );
  } catch {
    // In-memory idempotency still applies when storage is unavailable.
  }
}

function reconcileCommercialPaymentRequestIDs(requestIDs, orders) {
  const activeKeys = new Set((orders || []).filter((order) => ['created', 'pending'].includes(order?.status)).map(
    (order) => `${order.plan_id}:${order.channel}`,
  ));
  const terminalKeys = new Set((orders || []).filter((order) => (
    order?.plan_id && order?.channel && !['created', 'pending'].includes(order.status)
  )).map((order) => `${order.plan_id}:${order.channel}`));
  let changed = false;
  for (const key of requestIDs.keys()) {
    if (!activeKeys.has(key) && terminalKeys.has(key)) {
      requestIDs.delete(key);
      changed = true;
    }
  }
  if (changed) saveCommercialPaymentRequestIDs(requestIDs);
}

function clearCommercialPaymentRequestIDForOrder(requestIDs, order) {
  if (!order?.plan_id || !order?.channel) return;
  if (requestIDs.delete(`${order.plan_id}:${order.channel}`)) {
    saveCommercialPaymentRequestIDs(requestIDs);
  }
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

function modelServiceKeyName(name) {
  const value = String(name || '').trim();
  if (!value || /^catsco\s+relay\s+key$/i.test(value)) return 'CatsCo 模型服务 Key';
  return value;
}

function summarizeCommercial(summary) {
  const models = commercialModels(summary);
  if (!models.length) return '暂无已发放额度';
  return `1 个共享额度池 · 覆盖 ${models.length} 个模型 · 各模型按自身倍率扣减`;
}

function commercialUsageTextForUser(plan, presentation) {
  return `${presentation?.usageLabel || '套餐用量'} · ${Number(plan?.duration_days || 30)} 天有效`;
}

function activeEntitlements(summary) {
  return (summary?.entitlements || []).filter((item) => item.state === 'active');
}

function commercialModels(summary) {
  return [...(summary?.models || [])]
    .filter((model) => model && String(model).toLowerCase() !== 'gpt-5.6-luna')
    .sort((a, b) => a.localeCompare(b));
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
      meta: '正在读取当前模型',
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
      meta: commercialEnabled ? '模型用量暂未同步' : '暂未接入套餐',
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
    meta: usedLabel,
    detail: remainingLabel,
    percent,
    note: overLimit
      ? '共享模型额度已用完，后续调用将暂停；请联系管理员补额或等待额度重置。'
      : '按当前启动模型展示，切换模型后可能延迟几分钟刷新。',
  };
}

function budgetUsageDisplay(model, usageByModel) {
  const { loading, summary: usage } = usageStateForModel(usageByModel, model);
  if (loading) return { label: '读取中', meta: '用量读取中' };
  if (!usage) return { label: '待同步', meta: '模型用量暂未同步' };
  if (usage.source === 'custom' || usage.status === 'custom') {
    return { label: '自备额度', meta: '自定义模型不计入模型服务套餐' };
  }
  if (!usage.model || usage.quota_configured !== true) return { label: '待同步', meta: '模型用量暂未同步' };
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
  if (commercial?.enforce_enabled) return '套餐已启用';
  if (commercial?.enabled) return '内测开放';
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
  const [commercialView, setCommercialView] = useState('plans');
  const [orderFilter, setOrderFilter] = useState('all');
  const [showAllOrders, setShowAllOrders] = useState(false);
  const [paymentChannel, setPaymentChannel] = useState('');
  const [checkoutOrder, setCheckoutOrder] = useState(null);
  const [paymentLoading, setPaymentLoading] = useState('');
  const [paymentPollError, setPaymentPollError] = useState('');
  const [trialLoading, setTrialLoading] = useState(false);
  const [currentUsage, setCurrentUsage] = useState(undefined);
  const [usageByModel, setUsageByModel] = useState({});
  const [inviteCode, setInviteCode] = useState('');
  const [inviteLoading, setInviteLoading] = useState(false);
  const [error, setError] = useState('');
  const [warning, setWarning] = useState('');
  const [copied, setCopied] = useState('');
  const paymentRequestIDs = useRef(loadCommercialPaymentRequestIDs());

  useEffect(() => {
    let cancelled = false;
    const controller = new AbortController();
    const requestOptions = { signal: controller.signal, timeoutMs: 20_000 };
    async function load() {
      setLoading(true);
      setError('');
      let nextConfig = FALLBACK_CONFIG;
      try {
        const data = await api.getRelayConfig(requestOptions);
        nextConfig = {
          ...FALLBACK_CONFIG,
          ...data,
          endpoints: Array.isArray(data.endpoints) && data.endpoints.length > 0
            ? data.endpoints
            : FALLBACK_CONFIG.endpoints,
        };
        if (!cancelled) setConfig(nextConfig);
      } catch (err) {
        if (!cancelled && err.code !== 'REQUEST_ABORTED') {
          console.warn('Failed to load relay config:', err);
          setError('配置读取失败，已显示默认配置');
        }
      }
      if (nextConfig.self_service_enabled) {
        try {
          setKeyLoading(true);
          const keyData = await api.getRelayKey(requestOptions);
          if (!cancelled) setRelayKey(keyData.key || null);
        } catch {
          if (!cancelled) setRelayKey(null);
        } finally {
          if (!cancelled) setKeyLoading(false);
        }
      }
      const [commercialResult, catalogResult, ordersResult] = await Promise.allSettled([
        api.getRelayCommercial(requestOptions),
        api.getCommercialCatalog(requestOptions),
        api.getCommercialOrders('', requestOptions),
      ]);
      if (cancelled) return;
      setCommercial(commercialResult.status === 'fulfilled' ? commercialResult.value : null);
      if (catalogResult.status === 'fulfilled') {
        const catalogData = catalogResult.value;
        setCommercialCatalog(catalogData);
        const channels = Array.isArray(catalogData?.channels) ? catalogData.channels : [];
        setPaymentChannel(channels[0]?.id || '');
      } else {
        setCommercialCatalog(null);
      }
      if (ordersResult.status === 'fulfilled') {
        const orders = Array.isArray(ordersResult.value?.orders) ? ordersResult.value.orders : [];
        setCommercialOrders(orders);
        reconcileCommercialPaymentRequestIDs(paymentRequestIDs.current, orders);
        setCheckoutOrder((current) => current || orders.find((order) => ['created', 'pending'].includes(order.status)) || null);
      } else {
        setCommercialOrders([]);
        setError('订单状态读取失败，请关闭后重试。');
      }
      if (!cancelled) setLoading(false);
    }
    load();
    return () => {
      cancelled = true;
      controller.abort();
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
  const catalogPlans = Array.isArray(commercialCatalog?.plans) ? commercialCatalog.plans : [];
  const hasCurrentCommercialPlans = catalogPlans.some(plan => CURRENT_COMMERCIAL_PLAN_SLUGS.has(plan?.slug));
  const salePlans = hasCurrentCommercialPlans
    ? catalogPlans.filter(plan => CURRENT_COMMERCIAL_PLAN_SLUGS.has(plan?.slug))
    : catalogPlans;
  const paymentChannels = Array.isArray(commercialCatalog?.channels) ? commercialCatalog.channels : [];
  const checkoutPaymentLabel = paymentChannelLabel(paymentChannels, checkoutOrder?.channel);
  const activePackages = activeEntitlements(commercialSummary);
  const packageModels = commercialModels(commercialSummary);
  const packageExpiry = nearestPackageExpiry(activePackages);
  const packageExpiryText = activePackages.length > 0 ? formatShortDate(packageExpiry) : '无套餐';
  const currentResetInfo = usageResetInfo(currentUsage);
  const currentQuota = currentQuotaDisplay(currentUsage, config.default_model, commercialEnabled);
  const openOrders = commercialOrders.filter(order => ['created', 'pending', 'paid'].includes(order.status));
  const fulfilledOrders = commercialOrders.filter(order => order.status === 'fulfilled');
  const closedOrders = commercialOrders.filter(order => (
    ['closed', 'failed', 'refunding', 'refunded', 'cancelled'].includes(order.status)
  ));
  const orderFilters = [
    { id: 'all', label: '全部', count: commercialOrders.length },
    { id: 'open', label: '待处理', count: openOrders.length },
    { id: 'fulfilled', label: '已生效', count: fulfilledOrders.length },
    { id: 'closed', label: '退款 / 关闭', count: closedOrders.length },
  ];
  const filteredOrders = commercialOrders.filter((order) => {
    if (orderFilter === 'open') return ['created', 'pending', 'paid'].includes(order.status);
    if (orderFilter === 'fulfilled') return order.status === 'fulfilled';
    if (orderFilter === 'closed') {
      return ['closed', 'failed', 'refunding', 'refunded', 'cancelled'].includes(order.status);
    }
    return true;
  });
  const visibleOrders = showAllOrders ? filteredOrders : filteredOrders.slice(0, 8);

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

  const refreshCommercialState = async (options = {}) => {
    const [commercialResult, catalogResult, ordersResult] = await Promise.allSettled([
      api.getRelayCommercial(options),
      api.getCommercialCatalog(options),
      api.getCommercialOrders('', options),
    ]);
    if (commercialResult.status === 'fulfilled') setCommercial(commercialResult.value);
    if (catalogResult.status === 'fulfilled') {
      const catalogData = catalogResult.value;
      setCommercialCatalog(catalogData);
      const channels = Array.isArray(catalogData?.channels) ? catalogData.channels : [];
      setPaymentChannel((current) => channels.some((channel) => channel.id === current) ? current : (channels[0]?.id || ''));
    }
    if (ordersResult.status === 'fulfilled') {
      const orders = Array.isArray(ordersResult.value?.orders) ? ordersResult.value.orders : [];
      setCommercialOrders(orders);
      reconcileCommercialPaymentRequestIDs(paymentRequestIDs.current, orders);
    }
    const failed = [commercialResult, catalogResult, ordersResult].find((result) => result.status === 'rejected');
    if (failed) throw failed.reason;
  };

  const createCommercialOrder = async (plan) => {
    if (!paymentChannel) {
      setError('支付通道尚未配置。');
      return;
    }
    const existing = commercialOrders.find((order) => (
      ['created', 'pending'].includes(order.status) && order.plan_id === plan.id && order.channel === paymentChannel
    ));
    if (existing) {
      setPaymentPollError('');
      setCheckoutOrder(existing);
      setError('');
      return;
    }
    setPaymentLoading(`create:${plan.id}`);
    setError('');
    setWarning('');
    const requestKey = `${plan.id}:${paymentChannel}`;
    const clientRequestID = paymentRequestIDs.current.get(requestKey) || newCommercialClientRequestID();
    paymentRequestIDs.current.set(requestKey, clientRequestID);
    saveCommercialPaymentRequestIDs(paymentRequestIDs.current);
    try {
      const data = await api.createCommercialOrder(plan.id, paymentChannel, clientRequestID, { timeoutMs: 40_000 });
      if (data.order && !['created', 'pending'].includes(data.order.status)) {
        paymentRequestIDs.current.delete(requestKey);
        saveCommercialPaymentRequestIDs(paymentRequestIDs.current);
      }
      setPaymentPollError('');
      setCheckoutOrder(data.order || null);
      setCommercialOrders((current) => {
        const next = (current || []).filter((item) => item.order_no !== data.order?.order_no);
        return data.order ? [data.order, ...next] : next;
      });
    } catch (err) {
      if (Number(err.status) >= 400 && Number(err.status) < 500 && Number(err.status) !== 408) {
        paymentRequestIDs.current.delete(requestKey);
        saveCommercialPaymentRequestIDs(paymentRequestIDs.current);
      }
      if (['NETWORK_ERROR', 'REQUEST_TIMEOUT'].includes(err.code) || Number(err.status) >= 500) {
        setError('暂时无法确认订单是否创建成功。请勿重复发起新订单；再次点击同一套餐会继续恢复原订单。');
      } else {
        setError(err.message || '创建订单失败');
      }
    } finally {
      setPaymentLoading('');
    }
  };

  const confirmCommercialTestPayment = async () => {
    if (!checkoutOrder?.order_no) return;
    setPaymentLoading(`confirm:${checkoutOrder.order_no}`);
    setError('');
    setWarning('');
    try {
      const data = await api.confirmCommercialTestPayment(checkoutOrder.order_no);
      setCheckoutOrder(data.order || checkoutOrder);
      try {
        await refreshCommercialState();
      } catch {
        setWarning('测试支付已完成，但页面状态暂未全部刷新，请稍后重试。');
      }
    } catch (err) {
      setError(err.message || '测试支付失败');
    } finally {
      setPaymentLoading('');
    }
  };

  const refreshCheckoutOrder = async () => {
    if (!checkoutOrder?.order_no) return;
    setPaymentLoading(`refresh:${checkoutOrder.order_no}`);
    setPaymentPollError('');
    setError('');
    try {
      const data = await api.getCommercialOrders(checkoutOrder.order_no, { timeoutMs: 20_000 });
      if (!data?.order) throw new Error('暂未读取到订单状态');
      setCheckoutOrder(data.order);
      setCommercialOrders((current) => {
        const next = (current || []).filter((item) => item.order_no !== data.order.order_no);
        return [data.order, ...next];
      });
      if (!['created', 'pending'].includes(data.order.status)) {
        clearCommercialPaymentRequestIDForOrder(paymentRequestIDs.current, data.order);
      }
      if (data.order.status === 'fulfilled') {
        await refreshCommercialState({ timeoutMs: 20_000 });
      }
    } catch (err) {
      setPaymentPollError(err.message || '订单状态刷新失败，请稍后重试。');
    } finally {
      setPaymentLoading('');
    }
  };

  const claimCommercialTrial = async () => {
    setTrialLoading(true);
    setError('');
    setWarning('');
    try {
      const data = await api.claimCommercialTrial();
      setCommercial({ ...(commercial || {}), enabled: true, summary: data.summary });
      try {
        await refreshCommercialState();
      } catch {
        setWarning('体验包已领取，但页面状态暂未全部刷新，请稍后重试。');
      }
    } catch (err) {
      setError(err.message || '体验包领取失败');
    } finally {
      setTrialLoading(false);
    }
  };

  useEffect(() => {
    if (!checkoutOrder?.order_no || !['created', 'pending', 'closed'].includes(checkoutOrder.status)) return undefined;
    let cancelled = false;
    let timer = null;
    let failures = 0;
    let shouldContinue = true;
    let closedAttemptsRemaining = checkoutOrder.status === 'closed' ? 3 : 0;
    let nextDelayMs = checkoutOrder.status === 'closed' ? 11_000 : 2500;
    const controller = new AbortController();
    const poll = async () => {
      try {
        const data = await api.getCommercialOrders(checkoutOrder.order_no, {
          signal: controller.signal,
          timeoutMs: 20_000,
        });
        if (cancelled || !data?.order) return;
        failures = 0;
        setPaymentPollError('');
        setCheckoutOrder(data.order);
        setCommercialOrders((current) => {
          const next = (current || []).filter((item) => item.order_no !== data.order.order_no);
          return [data.order, ...next];
        });
        if (data.order.status === 'closed') {
          closedAttemptsRemaining -= 1;
          shouldContinue = closedAttemptsRemaining > 0;
          nextDelayMs = 11_000;
        } else {
          shouldContinue = ['created', 'pending'].includes(data.order.status);
          nextDelayMs = 2500;
        }
        if (!shouldContinue) {
          clearCommercialPaymentRequestIDForOrder(paymentRequestIDs.current, data.order);
        }
      } catch (err) {
        if (cancelled || err.code === 'REQUEST_ABORTED') return;
        failures += 1;
        if (checkoutOrder.status === 'closed') {
          closedAttemptsRemaining -= 1;
          shouldContinue = closedAttemptsRemaining > 0;
          nextDelayMs = 11_000;
        }
        if (failures >= 2) {
          setPaymentPollError('支付状态暂时未更新，订单已保留，正在继续查询。');
        }
      } finally {
        if (!cancelled && shouldContinue) timer = window.setTimeout(poll, nextDelayMs);
      }
    };
    poll();
    return () => {
      cancelled = true;
      controller.abort();
      if (timer) window.clearTimeout(timer);
    };
  }, [checkoutOrder?.order_no, checkoutOrder?.status]);

  useEffect(() => {
    if (!checkoutOrder?.order_no || checkoutOrder.status !== 'fulfilled') return undefined;
    let cancelled = false;
    let timer = null;
    let attempts = 0;
    const controller = new AbortController();
    const refresh = async () => {
      attempts += 1;
      try {
        await refreshCommercialState({ signal: controller.signal, timeoutMs: 20_000 });
        if (!cancelled) {
          setPaymentPollError('');
          setWarning('');
        }
      } catch (err) {
        if (cancelled || err.code === 'REQUEST_ABORTED') return;
        if (attempts < 3) {
          timer = window.setTimeout(refresh, 5000);
        } else {
          setPaymentPollError('支付已经确认，但额度状态暂未刷新。订单已保留，请稍后重新打开查看。');
        }
      }
    };
    refresh();
    return () => {
      cancelled = true;
      controller.abort();
      if (timer) window.clearTimeout(timer);
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
          {loading && <div className="oc-settings-secondary">正在读取模型服务配置...</div>}
          {error && <InlineFeedback tone="error">{error}</InlineFeedback>}
          {warning && <InlineFeedback tone="warning">{warning}</InlineFeedback>}

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
                <div className="relay-access-title">套餐与账单</div>
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

            <div className="relay-access-billing-tabs" role="tablist" aria-label="套餐与账单">
              <button
                type="button"
                role="tab"
                aria-selected={commercialView === 'plans'}
                className={commercialView === 'plans' ? 'active' : ''}
                onClick={() => setCommercialView('plans')}
              >
                <LayoutGrid size={15} />
                套餐
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={commercialView === 'orders'}
                className={commercialView === 'orders' ? 'active' : ''}
                onClick={() => setCommercialView('orders')}
              >
                <History size={15} />
                订单记录
                {commercialOrders.length > 0 && <span>{commercialOrders.length}</span>}
              </button>
            </div>

            {checkoutOrder && (
              <div className={`relay-access-checkout ${checkoutOrder.status}`}>
                <div className="relay-access-checkout-head">
                  <div>
                    <strong>{checkoutOrder.plan_name}</strong>
                    <span className="relay-access-checkout-order-no">
                      订单 {checkoutOrder.order_no}
                      <button
                        type="button"
                        aria-label="复制订单号"
                        title="复制订单号"
                        onClick={() => copyText(`order:${checkoutOrder.order_no}`, checkoutOrder.order_no)}
                      >
                        {copied === `order:${checkoutOrder.order_no}` ? <Check size={12} /> : <Copy size={12} />}
                      </button>
                    </span>
                  </div>
                  <div className="relay-access-checkout-head-actions">
                    <em className={commercialOrderTone(checkoutOrder.status)}>{commercialOrderStatus(checkoutOrder.status)}</em>
                    <button type="button" aria-label="关闭订单详情" title="关闭订单详情" onClick={() => setCheckoutOrder(null)}>
                      <X size={14} />
                    </button>
                  </div>
                </div>
                <div className="relay-access-checkout-meta">
                  <span><strong>{formatPriceFen(checkoutOrder.amount_fen)}</strong>订单金额</span>
                  <span><strong>{paymentChannelLabel(paymentChannels, checkoutOrder.channel)}</strong>支付方式</span>
                  <span><strong>{formatShortDateTime(checkoutOrder.created_at) || '-'}</strong>创建时间</span>
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
                {checkoutOrder.status === 'pending' && checkoutOrder.channel !== 'test' && !checkoutOrder.checkout_url && (
                  <div className="relay-access-period-note">正在恢复支付宝收银台链接，请稍候。</div>
                )}
                {checkoutOrder.status === 'created' && (
                  <div className="relay-access-period-note">订单已保存，正在恢复支付链接。</div>
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
                {checkoutOrder.status === 'paid' && (
                  <div className="relay-access-payment-success pending"><RotateCcw size={16} />支付已确认，正在发放套餐权益。</div>
                )}
                {checkoutOrder.status === 'fulfilled' && (
                  <div className="relay-access-payment-success"><BadgeCheck size={16} />支付成功，套餐已生效。用量数据可能延迟几分钟刷新。</div>
                )}
                {paymentPollError && <InlineFeedback tone="warning">{paymentPollError}</InlineFeedback>}
                <div className="relay-access-checkout-actions">
                  <span>{formatShortDateTime(commercialOrderEventTime(checkoutOrder))}</span>
                  <button
                    type="button"
                    onClick={refreshCheckoutOrder}
                    disabled={Boolean(paymentLoading)}
                  >
                    <RotateCcw size={14} />
                    {paymentLoading === `refresh:${checkoutOrder.order_no}` ? '刷新中...' : '刷新状态'}
                  </button>
                </div>
              </div>
            )}

            {commercialView === 'plans' && (
              <div className="relay-access-billing-panel" role="tabpanel">
                {commercialEnabled && activePackages.length > 0 && (
                  <div className="relay-access-entitlement-band">
                    <div className="relay-access-mini-title"><BadgeCheck size={15} />当前权益</div>
                    <div className="relay-access-package-list">
                      {activePackages.map((item) => (
                        <div className="relay-access-package-row" key={`${item.id || item.plan_id}-${item.source_ref || item.starts_at}`}>
                          <span>
                            <strong>{item.plan_name || item.plan_slug || '套餐'}</strong>
                            <em>{item.starts_at ? `${formatShortDate(item.starts_at)} 生效` : '已生效'}</em>
                          </span>
                          <strong>{formatShortDate(item.expires_at)} 到期</strong>
                        </div>
                      ))}
                    </div>
                    {packageModels.length > 0 && (
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
                  </div>
                )}

                {commercialEnabled && activePackages.length === 0 && (
                  <div className="relay-access-token-note">
                    <Gift size={16} />
                    <span>当前没有有效套餐。你可以选购套餐、兑换邀请码，或联系管理员发放额度。</span>
                  </div>
                )}

                {commercialCatalog?.trial_available && (
                  <div className="relay-access-trial-row">
                    <div>
                      <strong>新用户体验包</strong>
                      <span>领取后直接生效，每个账号限一次。</span>
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
                        {hasCurrentCommercialPlans && <span className="relay-access-storefront-eyebrow">个人方案</span>}
                        <div className="relay-access-mini-title">
                          {hasCurrentCommercialPlans ? '选择适合你的工作强度' : '选一档，开始你的协作节奏'}
                        </div>
                        <span>{hasCurrentCommercialPlans ? '按实际工作频率选择，套餐到期前不会自动续费。' : '按使用频率选择，套餐到期前不会自动续费。'}</span>
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
                    <div className={`relay-access-plan-list${hasCurrentCommercialPlans ? ' current-catalog' : ''}`}>
                      {hasCurrentCommercialPlans && (
                        <article className="relay-access-plan-row official free">
                          <div className="relay-access-plan-heading">
                            <span className="relay-access-plan-kicker">{FREE_PLAN_PRESENTATION.kicker}</span>
                          </div>
                          <strong className="relay-access-plan-name">{FREE_PLAN_PRESENTATION.name}</strong>
                          <div className="relay-access-plan-price">
                            <span className="relay-access-plan-currency">¥</span>
                            <strong>0</strong>
                            <span>/ 月</span>
                          </div>
                          <span className="relay-access-plan-tagline">{FREE_PLAN_PRESENTATION.tagline}</span>
                          <p>{FREE_PLAN_PRESENTATION.description}</p>
                          <div className="relay-access-plan-action">
                            <button type="button" className="secondary" disabled>无需购买</button>
                          </div>
                          <div className="relay-access-plan-divider" />
                          <span className="relay-access-plan-includes">包含</span>
                          <ul className="relay-access-plan-features">
                            {FREE_PLAN_PRESENTATION.features.map(feature => (
                              <li key={feature}><Check size={14} />{feature}</li>
                            ))}
                          </ul>
                        </article>
                      )}
                      {salePlans.map((plan) => {
                        const presentation = commercialPlanPresentation(plan);
                        const pendingOrder = openOrders.find(order => order.plan_id === plan.id && order.channel === paymentChannel);
                        const isActivePlan = activePackages.some(item => item.plan_slug === plan.slug);
                        return (
                          <article
                            className={`relay-access-plan-row${hasCurrentCommercialPlans ? ' official' : ''}${presentation.recommended ? ' recommended' : ''}${presentation.pro ? ' pro' : ''}${presentation.wide ? ' wide' : ''}`}
                            key={plan.id}
                          >
                            <div className="relay-access-plan-heading">
                              <span className="relay-access-plan-kicker">{presentation.kicker}</span>
                              {presentation.recommended && <span className="relay-access-plan-badge">推荐</span>}
                            </div>
                            <strong className="relay-access-plan-name">{plan.name}</strong>
                            <div className="relay-access-plan-price">
                              {hasCurrentCommercialPlans && <span className="relay-access-plan-currency">¥</span>}
                              <strong>{hasCurrentCommercialPlans ? formatPriceAmountFen(plan.price_fen) : formatPriceFen(plan.price_fen)}</strong>
                              <span>{commercialPlanCycle(plan)}</span>
                            </div>
                            <span className="relay-access-plan-tagline">{presentation.tagline}</span>
                            <p>{plan.description || `${plan.duration_days} 天有效`}</p>
                            <em>{commercialUsageTextForUser(plan, presentation)}</em>
                            <span className="relay-access-plan-audience">适合：{presentation.audience}</span>
                            <div className="relay-access-plan-action">
                              <button
                                type="button"
                                onClick={() => {
                                  if (pendingOrder) {
                                    setPaymentPollError('');
                                    setCheckoutOrder(pendingOrder);
                                    return;
                                  }
                                  createCommercialOrder(plan);
                                }}
                                disabled={!paymentChannel || Boolean(paymentLoading)}
                              >
                                <CreditCard size={15} />
                                {paymentLoading === `create:${plan.id}`
                                  ? '创建中...'
                                  : paymentChannels.length === 0
                                    ? '暂未开放'
                                    : pendingOrder?.status === 'paid'
                                      ? '查看进度'
                                      : pendingOrder
                                        ? '继续支付'
                                        : isActivePlan
                                          ? '续购'
                                          : hasCurrentCommercialPlans ? `选择${plan.name}` : '购买'}
                              </button>
                            </div>
                            {Array.isArray(presentation.features) && presentation.features.length > 0 && (
                              <>
                                <div className="relay-access-plan-divider" />
                                <span className="relay-access-plan-includes">包含</span>
                                <ul className="relay-access-plan-features">
                                  {presentation.features.map(feature => (
                                    <li key={feature}><Check size={14} />{feature}</li>
                                  ))}
                                </ul>
                              </>
                            )}
                          </article>
                        );
                      })}
                    </div>
                    {paymentChannels.length === 0 && (
                      <div className="relay-access-period-note">支付通道暂未开放，当前套餐可通过邀请码或管理员发放。</div>
                    )}
                  </div>
                )}

                {commercialEnabled ? (
                  <div className="relay-access-invite-section">
                    <div>
                      <strong>有邀请码？</strong>
                      <span>兑换成功后，套餐权益会直接加入当前账号。</span>
                    </div>
                    <div className="relay-access-invite-form">
                      <input
                        value={inviteCode}
                        onChange={(event) => setInviteCode(event.target.value)}
                        placeholder="输入邀请码"
                        disabled={inviteLoading}
                      />
                      <button type="button" disabled={inviteLoading} onClick={redeemInvite}>
                        {inviteLoading ? '兑换中...' : copied === 'invite' ? '已兑换' : '兑换'}
                      </button>
                    </div>
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
                      ? '套餐内模型共用同一额度池，并按各模型倍率扣减；切换模型后数据可能延迟几分钟。'
                      : (commercial?.note || '当前为内测账单，购买记录不会自动改变已有模型额度。')}
                  </div>
                )}
              </div>
            )}

            {commercialView === 'orders' && (
              <div className="relay-access-billing-panel" role="tabpanel">
                <div className="relay-access-order-summary">
                  <div><strong>{commercialOrders.length}</strong><span>全部订单</span></div>
                  <div><strong>{openOrders.length}</strong><span>待处理</span></div>
                  <div><strong>{fulfilledOrders.length}</strong><span>已生效</span></div>
                </div>
                <div className="relay-access-order-filters" role="group" aria-label="筛选订单">
                  {orderFilters.map((filter) => (
                    <button
                      type="button"
                      key={filter.id}
                      className={orderFilter === filter.id ? 'active' : ''}
                      onClick={() => {
                        setOrderFilter(filter.id);
                        setShowAllOrders(false);
                      }}
                    >
                      {filter.label}
                      <span>{filter.count}</span>
                    </button>
                  ))}
                </div>
                {visibleOrders.length > 0 ? (
                  <div className="relay-access-order-list">
                    {visibleOrders.map((order) => (
                      <button
                        type="button"
                        key={order.order_no}
                        className={checkoutOrder?.order_no === order.order_no ? 'active' : ''}
                        onClick={() => {
                          setPaymentPollError('');
                          setCheckoutOrder(order);
                        }}
                      >
                        <ReceiptText size={16} />
                        <span>
                          <strong>{order.plan_name}</strong>
                          <em>{order.order_no} · {formatShortDateTime(order.created_at)}</em>
                        </span>
                        <span>
                          <strong>{formatPriceFen(order.amount_fen)}</strong>
                          <em className={commercialOrderTone(order.status)}>{commercialOrderStatus(order.status)}</em>
                        </span>
                      </button>
                    ))}
                  </div>
                ) : (
                  <div className="relay-access-order-empty">
                    <ReceiptText size={18} />
                    <strong>{commercialOrders.length > 0 ? '没有符合筛选条件的订单' : '还没有购买记录'}</strong>
                    <span>{commercialOrders.length > 0 ? '换一个状态看看。' : '购买套餐后，订单号、金额和状态会保存在这里。'}</span>
                  </div>
                )}
                {filteredOrders.length > 8 && (
                  <button type="button" className="relay-access-order-more" onClick={() => setShowAllOrders(value => !value)}>
                    {showAllOrders ? '收起' : `查看其余 ${filteredOrders.length - 8} 条`}
                  </button>
                )}
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
                    <strong>{modelServiceKeyName(relayKey.name)}</strong>
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
