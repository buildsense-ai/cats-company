import React, { useEffect, useMemo, useRef, useState } from 'react';
import { AlertTriangle, ArrowUpRight, BadgeCheck, Check, ChevronDown, Copy, CreditCard, ExternalLink, Gift, History, KeyRound, ReceiptText, RotateCcw, Trash2, X } from 'lucide-react';
import { api } from '../api';
import { InlineFeedback, useFeedback } from '../components/feedback-system';
import {
  readStorageValue,
  writeStorageValue,
} from '../utils/storage-access';

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
      '含 1 次云托管员工创建权益',
      '云托管员工随套餐有效；到期后保留 15 天，期间续费可恢复',
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
      '含 1 次云托管员工创建权益',
      '云托管员工随套餐有效；到期后保留 15 天，期间续费可恢复',
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
const COMMERCIAL_PLAN_TIERS = {
  'catsco-personal': 1,
  'catsco-pro': 2,
};

function commercialPlanTier(slug) {
  return COMMERCIAL_PLAN_TIERS[slug] || 0;
}

function entitlementMatchesPlan(entitlement, plan) {
  if (!entitlement || !plan) return false;
  if (entitlement.plan_slug && entitlement.plan_slug === plan.slug) return true;
  return Number(entitlement.plan_id || 0) > 0 && Number(entitlement.plan_id) === Number(plan.id);
}

function activeCommercialPlanTier(entitlements, plans) {
  return entitlements.reduce((highest, entitlement) => {
    const matchedPlan = plans.find(plan => entitlementMatchesPlan(entitlement, plan));
    return Math.max(highest, commercialPlanTier(entitlement.plan_slug || matchedPlan?.slug));
  }, 0);
}

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
  const keyLine = plainKey ? `API Key: ${plainKey}` : 'API Key: sk-…（在“我的 Key”里生成后复制）';
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
    kicker: plan?.sale_state === 'test' ? '内测套餐' : '协作套餐',
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

function commercialOrderCountdown(order, now = Date.now()) {
  if (!order?.expires_at || !['created', 'pending'].includes(order.status)) return '';
  const expiresAt = new Date(order.expires_at).getTime();
  if (!Number.isFinite(expiresAt)) return '';
  const remainingSeconds = Math.max(0, Math.ceil((expiresAt - now) / 1000));
  if (remainingSeconds <= 0) return '已到期，正在确认';
  const minutes = Math.floor(remainingSeconds / 60);
  const seconds = remainingSeconds % 60;
  return `剩余 ${minutes}:${String(seconds).padStart(2, '0')}`;
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
    const parsed = JSON.parse(readStorageValue(COMMERCIAL_PAYMENT_REQUEST_STORAGE_KEY, 'sessionStorage') || '{}');
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
    writeStorageValue(
      COMMERCIAL_PAYMENT_REQUEST_STORAGE_KEY,
      JSON.stringify(Object.fromEntries(
        [...requestIDs].map(([key, id]) => [key, { id, created_at: Date.now() }]),
      )),
      'sessionStorage',
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

function modelServiceKeyName(name) {
  const value = String(name || '').trim();
  if (!value || /^catsco\s+(?:relay|模型服务)\s+key$/i.test(value)) return 'CatsCo API Key';
  return value;
}

function summarizeCommercial(summary) {
  const packages = activeEntitlements(summary);
  if (!packages.length) return '当前没有有效套餐';
  const expiry = nearestPackageExpiry(packages);
  const expiryText = expiry ? `${formatShortDate(expiry)} 到期` : '长期有效';
  return `${activePackageName(packages)} · ${expiryText}`;
}

function commercialUsageTextForUser(plan, presentation) {
  return `${presentation?.usageLabel || '套餐用量'} · ${Number(plan?.duration_days || 30)} 天有效`;
}

function activeEntitlements(summary) {
  return (summary?.entitlements || []).filter((item) => item.state === 'active');
}

function activePackageName(packages) {
  const names = [...new Set((packages || [])
    .map((item) => String(item?.plan_name || item?.plan_slug || '').trim())
    .filter(Boolean))];
  if (!names.length) return '尚未开通套餐';
  if (names.length === 1) return names[0];
  return `${names[0]} 等 ${names.length} 个套餐`;
}

function commercialAccountState(commercial, packages) {
  if ((packages || []).length > 0) return { label: '当前有效', className: 'active' };
  if (commercial?.enabled) return { label: '尚未开通', className: 'neutral' };
  return { label: '未开放', className: 'inactive' };
}

function handleTabListKeyDown(event) {
  if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
  const tabs = [...event.currentTarget.querySelectorAll('[role="tab"]:not(:disabled)')];
  if (!tabs.length) return;
  const currentIndex = tabs.indexOf(document.activeElement);
  let nextIndex = currentIndex;
  if (event.key === 'Home') nextIndex = 0;
  if (event.key === 'End') nextIndex = tabs.length - 1;
  if (event.key === 'ArrowLeft') nextIndex = (Math.max(currentIndex, 0) - 1 + tabs.length) % tabs.length;
  if (event.key === 'ArrowRight') nextIndex = (Math.max(currentIndex, -1) + 1) % tabs.length;
  event.preventDefault();
  tabs[nextIndex].focus();
  tabs[nextIndex].click();
}

function commercialEntitlementSourceLabel(source) {
  return ({
    order: '支付购买',
    invite: '邀请码兑换',
    trial: '体验领取',
    manual: '管理员发放',
  })[String(source || '').trim()] || '套餐发放';
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
      detail: '不使用 CatsCo 套餐额度',
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

function currentQuotaDisplay(summary, commercialEnabled) {
  if (typeof summary === 'undefined') {
    return {
      className: 'loading',
      model: '共享额度池',
      title: '套餐总额度',
      meta: '正在读取总用量',
      detail: '等待后台同步',
      percent: 0,
      note: '套餐内模型共用这一额度池，并按各自倍率扣减。',
    };
  }
  if (!summary) {
    return {
      className: 'inactive',
      model: '共享额度池',
      title: '套餐总额度',
      meta: commercialEnabled ? '总用量暂未同步' : '暂未开通套餐',
      detail: commercialEnabled ? '暂无用量数据' : '购买或兑换后显示',
      percent: 0,
      note: commercialEnabled
        ? '总用量由模型服务汇总，可能延迟几分钟刷新。'
        : '自定义模型不消耗 CatsCo 套餐额度。',
    };
  }

  if (summary.quota_configured !== true) {
    return {
      className: 'inactive',
      model: '共享额度池',
      title: '套餐总额度',
      meta: '总额度待同步',
      detail: '等待套餐额度同步',
      percent: 0,
      note: '套餐额度同步后，这里会显示总剩余额度和本周期总用量。',
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
    model: '共享额度池',
    title: overLimit ? '套餐额度已超额' : high ? '套餐额度接近上限' : '套餐总额度',
    meta: usedLabel,
    detail: remainingLabel,
    percent,
      note: overLimit
      ? '套餐总额度已用完，后续调用将暂停；请等待额度重置或联系管理员。'
      : '所有模型共享总额度，按模型倍率扣减，数据可能延迟。',
  };
}

function nearestPackageExpiry(packages) {
  const dates = packages
    .map((item) => new Date(item.expires_at || '').getTime())
    .filter((time) => Number.isFinite(time));
  if (!dates.length) return '';
  return new Date(Math.min(...dates)).toISOString();
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

function CommercialPlanCards({
  salePlans,
  hasCurrentCommercialPlans,
  activeOfficialPlanTier,
  activePackages,
  openOrders,
  paymentChannel,
  paymentChannels,
  paymentLoading,
  onOpenOrder,
  onSelectPlan,
  compact = false,
  className = '',
}) {
  return (
    <div className={`relay-access-plan-list${hasCurrentCommercialPlans ? ' current-catalog' : ''}${className ? ` ${className}` : ''}`}>
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
        const planTier = commercialPlanTier(plan.slug);
        const isActivePlan = planTier > 0
          ? activeOfficialPlanTier === planTier
          : activePackages.some(item => entitlementMatchesPlan(item, plan));
        const isIncludedPlan = planTier > 0 && activeOfficialPlanTier > planTier;
        const isUpgradePlan = planTier > activeOfficialPlanTier && activeOfficialPlanTier > 0;
        const purchaseBlocked = isIncludedPlan;
        const pendingOrder = purchaseBlocked ? null : openOrders.find(order => order.plan_id === plan.id && order.channel === paymentChannel);
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
            {!compact && <em>{commercialUsageTextForUser(plan, presentation)}</em>}
            {!compact && <span className="relay-access-plan-audience">适合：{presentation.audience}</span>}
            <div className="relay-access-plan-action">
              <button
                type="button"
                onClick={() => (pendingOrder ? onOpenOrder(pendingOrder) : onSelectPlan(plan))}
                disabled={!paymentChannel || Boolean(paymentLoading) || purchaseBlocked}
              >
                <CreditCard size={15} />
                {isActivePlan
                  ? '续费'
                  : isIncludedPlan
                    ? '已包含'
                    : paymentLoading === `create:${plan.id}`
                      ? '创建中…'
                      : paymentChannels.length === 0
                        ? '暂未开放'
                        : pendingOrder?.status === 'paid'
                          ? '查看进度'
                          : pendingOrder
                            ? '继续支付'
                            : isUpgradePlan
                              ? `升级至${plan.name}`
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
  );
}

function EnterprisePlanCard() {
  return (
    <section className="relay-access-enterprise-card" aria-labelledby="relay-access-enterprise-title">
      <div className="relay-access-enterprise-intro">
        <span>企业服务</span>
        <h3 id="relay-access-enterprise-title">Business Start</h3>
        <p>帮助一个团队或一个明确业务场景真正开始使用 AI 员工，不是单纯出售企业账号。</p>
        <div className="relay-access-enterprise-price">
          <span>¥</span>
          <strong>4,999</strong>
          <small>/ 月起</small>
        </div>
        <a href="/contact?topic=enterprise&amp;service=business-start&amp;source=pricing">
          咨询企业落地
          <ArrowUpRight size={16} />
        </a>
      </div>
      <div className="relay-access-enterprise-scope">
        <div>
          <span>基础服务范围</span>
          <h4>从环境准备到首个场景上线</h4>
        </div>
        <ul>
          {[
            '企业使用环境的基础配置、账号与运行检查',
            '面向负责人和实际使用者的启动培训',
            '梳理一个边界清晰、可验证的首批业务场景',
            '初始化约定范围内的首批 Skill',
            '上线初期反馈处理与约定范围内的小幅调整',
          ].map(item => (
            <li key={item}><Check size={16} />{item}</li>
          ))}
        </ul>
        <div className="relay-access-enterprise-custom">
          <strong>高级企业服务需另行咨询</strong>
          <p>多部门流程改造、复杂系统集成、专属数据治理、私有化、安全合规、长期驻场和大规模 Skill 开发，均按范围与交付结果单独确认。</p>
        </div>
      </div>
    </section>
  );
}

export default function RelayAccessModal({ onClose, initialPlanSlug = '' }) {
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
  const [purchasePlan, setPurchasePlan] = useState(null);
  const [checkoutOrder, setCheckoutOrder] = useState(null);
  const [checkoutClock, setCheckoutClock] = useState(Date.now());
  const [paymentLoading, setPaymentLoading] = useState('');
  const [planChooserOpen, setPlanChooserOpen] = useState(false);
  const [planChooserAudience, setPlanChooserAudience] = useState('personal');
  const [paymentPollError, setPaymentPollError] = useState('');
  const [trialLoading, setTrialLoading] = useState(false);
  const [currentUsage, setCurrentUsage] = useState(undefined);
  const [usageRevision, setUsageRevision] = useState(0);
  const [inviteCode, setInviteCode] = useState('');
  const [inviteLoading, setInviteLoading] = useState(false);
  const [error, setError] = useState('');
  const [warning, setWarning] = useState('');
  const [copied, setCopied] = useState('');
  const paymentRequestIDs = useRef(loadCommercialPaymentRequestIDs());
  const purchaseSubmittingRef = useRef(false);
  const purchaseConfirmRef = useRef(null);
  const checkoutRef = useRef(null);
  const modalRef = useRef(null);
  const closeButtonRef = useRef(null);
  const restoreFocusRef = useRef(null);

  useEffect(() => {
    restoreFocusRef.current = document.activeElement;
    const focusTimer = window.setTimeout(() => closeButtonRef.current?.focus(), 0);
    return () => {
      window.clearTimeout(focusTimer);
      const target = restoreFocusRef.current;
      if (target instanceof HTMLElement && target.isConnected) target.focus();
    };
  }, []);

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

  // Public pricing links can carry a plan slug through the authenticated
  // workspace. Selecting it here keeps the user on the real payment flow
  // without exposing credentials or creating an order from the public site.
  useEffect(() => {
    const requestedSlug = String(initialPlanSlug || '').trim();
    const slug = ({ personal: 'catsco-personal', pro: 'catsco-pro' })[requestedSlug] || requestedSlug;
    if (!slug || !Array.isArray(commercialCatalog?.plans) || purchasePlan) return;
    const plan = commercialCatalog.plans.find((item) => String(item?.slug || '') === slug);
    if (plan) setPurchasePlan(plan);
  }, [commercialCatalog?.plans, initialPlanSlug, purchasePlan]);

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
      portalWindow.document.title = '正在打开开发者接入';
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
      throw new Error('开发者接入登录链接生成失败');
    } catch (err) {
      const fallback = config.docs_url || config.base_url || FALLBACK_CONFIG.docs_url;
      setError(err.message || '自动登录开发者接入失败，已打开普通页面');
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
  // The backend already filters the catalog by UID and sale state. Keep all
  // returned plans here so gray/internal plans remain visible to their
  // allowlisted users alongside the official plans.
  const salePlans = catalogPlans;
  const allSalePlansAreMonthly = salePlans.length > 0 && salePlans.every(
    plan => Number(plan?.duration_days || 30) === 30,
  );
  const paymentChannels = Array.isArray(commercialCatalog?.channels) ? commercialCatalog.channels : [];
  const checkoutPaymentLabel = paymentChannelLabel(paymentChannels, checkoutOrder?.channel);
  const activePackages = activeEntitlements(commercialSummary);
  const activeOfficialPlanTier = activeCommercialPlanTier(activePackages, salePlans);
  const commercialState = commercialAccountState(commercial, activePackages);
  const currentResetInfo = usageResetInfo(currentUsage);
  const currentQuota = currentQuotaDisplay(currentUsage, commercialEnabled);
  const checkoutCountdown = commercialOrderCountdown(checkoutOrder, checkoutClock);
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
  const availableOrderFilters = orderFilters.filter((filter) => (
    filter.id === 'all' || filter.count > 0 || filter.id === orderFilter
  ));
  const filteredOrders = commercialOrders.filter((order) => {
    if (orderFilter === 'open') return ['created', 'pending', 'paid'].includes(order.status);
    if (orderFilter === 'fulfilled') return order.status === 'fulfilled';
    if (orderFilter === 'closed') {
      return ['closed', 'failed', 'refunding', 'refunded', 'cancelled'].includes(order.status);
    }
    return true;
  });
  const visibleOrders = showAllOrders ? filteredOrders : filteredOrders.slice(0, 8);
  const purchaseIsUpgrade = Boolean(
    purchasePlan && activeOfficialPlanTier > 0 && commercialPlanTier(purchasePlan.slug) > activeOfficialPlanTier,
  );
  const purchaseIsRenewal = Boolean(
    purchasePlan && activeOfficialPlanTier > 0 && commercialPlanTier(purchasePlan.slug) === activeOfficialPlanTier,
  );
  const purchaseIncludesCloudWorker = Boolean(
    purchasePlan && CURRENT_COMMERCIAL_PLAN_SLUGS.has(purchasePlan.slug),
  );

  useEffect(() => {
    let cancelled = false;
    api.getRelayUsage({ scope: 'total' })
      .then((data) => {
        if (!cancelled) setCurrentUsage(data?.summary || null);
      })
      .catch(() => {
        if (!cancelled) setCurrentUsage(null);
      });
    return () => {
      cancelled = true;
    };
  }, [relayKey?.prefix, usageRevision, JSON.stringify(commercial?.summary?.entitlements || [])]);

  const redeemInvite = async () => {
    const code = inviteCode.trim();
    if (!code) {
      setError('请输入邀请码。');
      return;
    }
    setInviteLoading(true);
    setError('');
    try {
      const previousEntitlementIDs = new Set(
        (commercial?.summary?.entitlements || []).map((item) => String(item?.id || '')).filter(Boolean),
      );
      const data = await api.redeemRelayInvite(code);
      setCommercial({ ...(commercial || {}), enabled: true, summary: data.summary, note: data.note || commercial?.note });
      setUsageRevision((current) => current + 1);
      setInviteCode('');
      setCopied('invite');
      const inviteEntitlements = (data?.summary?.entitlements || []).filter((item) => item?.source === 'invite');
      const redeemed = inviteEntitlements.find((item) => item?.id && !previousEntitlementIDs.has(String(item.id)))
        || [...inviteEntitlements].sort((left, right) => (
          new Date(right?.starts_at || 0).getTime() - new Date(left?.starts_at || 0).getTime()
        ))[0];
      feedback.notify({
        tone: 'success',
        message: `${redeemed?.plan_name || '套餐'}已通过邀请码生效`,
      });
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
    if (commercialResult.status === 'fulfilled') {
      setUsageRevision((current) => current + 1);
    }
    const failed = [commercialResult, catalogResult, ordersResult].find((result) => result.status === 'rejected');
    if (failed) throw failed.reason;
  };

  const createCommercialOrder = async (plan, paymentWindow = null) => {
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
      if (paymentWindow && existing.checkout_url) paymentWindow.location.replace(existing.checkout_url);
      else if (paymentWindow) paymentWindow.close();
      return existing;
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
      if (paymentWindow && data.order?.checkout_url) {
        paymentWindow.location.replace(data.order.checkout_url);
      } else if (paymentWindow) {
        paymentWindow.close();
      }
      return data.order || null;
    } catch (err) {
      if (paymentWindow) paymentWindow.close();
      if (Number(err.status) >= 400 && Number(err.status) < 500 && Number(err.status) !== 408) {
        paymentRequestIDs.current.delete(requestKey);
        saveCommercialPaymentRequestIDs(paymentRequestIDs.current);
      }
      if (['NETWORK_ERROR', 'REQUEST_TIMEOUT'].includes(err.code) || Number(err.status) >= 500) {
        setError('暂时无法确认订单是否创建成功。请勿重复发起新订单；再次点击同一套餐会继续恢复原订单。');
      } else {
        setError(err.message || '创建订单失败');
      }
      return null;
    } finally {
      setPaymentLoading('');
    }
  };

  const confirmCommercialPurchase = async () => {
    if (!purchasePlan || !paymentChannel || purchaseSubmittingRef.current) return;
    purchaseSubmittingRef.current = true;
    let paymentWindow = null;
    let popupBlocked = false;
    if (paymentChannel === 'alipay_page') {
      paymentWindow = window.open('about:blank', '_blank');
      if (paymentWindow) {
        paymentWindow.opener = null;
        try {
          paymentWindow.document.title = '正在打开支付宝';
          paymentWindow.document.body.textContent = '正在创建订单并打开支付宝官方收银台…';
        } catch {
          // The checkout still opens even when the placeholder document cannot be styled.
        }
      } else {
        popupBlocked = true;
      }
    }
    try {
      const order = await createCommercialOrder(purchasePlan, paymentWindow);
      if (order) {
        setPurchasePlan(null);
        if (popupBlocked) setWarning('浏览器拦截了新窗口。请点击“前往支付宝付款”继续。');
      }
    } finally {
      purchaseSubmittingRef.current = false;
    }
  };

  const openCommercialOrder = (order) => {
    setPurchasePlan(null);
    setPaymentPollError('');
    setCheckoutOrder(order);
    if (order?.checkout_url) {
      const opened = window.open(order.checkout_url, '_blank');
      if (opened) opened.opener = null;
      else setWarning('浏览器拦截了支付宝窗口，请在订单详情中点击付款按钮。');
    }
  };

  const selectCommercialPlan = (plan) => {
    setPlanChooserOpen(false);
    setCheckoutOrder(null);
    setPurchasePlan(plan);
  };

  const openCommercialOrderFromChooser = (order) => {
    setPlanChooserOpen(false);
    openCommercialOrder(order);
  };

  useEffect(() => {
    if (!planChooserOpen) return undefined;
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') setPlanChooserOpen(false);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [planChooserOpen]);

  const cancelCommercialOrder = async () => {
    if (!checkoutOrder?.order_no || !['created', 'pending', 'failed'].includes(checkoutOrder.status)) return;
    const confirmed = await feedback.confirm({
      title: '取消这笔订单？',
      message: '未支付订单将被关闭。如果付款刚刚完成，系统仍会以支付宝结果为准并发放套餐。',
      confirmLabel: '取消订单',
      tone: 'danger',
    });
    if (!confirmed) return;
    setPaymentLoading(`cancel:${checkoutOrder.order_no}`);
    setError('');
    setWarning('');
    try {
      const data = await api.cancelCommercialOrder(checkoutOrder.order_no, { timeoutMs: 25_000 });
      if (!data?.order) throw new Error('暂未读取到取消结果');
      setCheckoutOrder(data.order);
      setCommercialOrders((current) => [data.order, ...(current || []).filter((item) => item.order_no !== data.order.order_no)]);
      clearCommercialPaymentRequestIDForOrder(paymentRequestIDs.current, data.order);
      feedback.notify({ tone: 'success', message: '订单已取消' });
    } catch (err) {
      setError(err.message || '取消订单失败，请刷新状态后重试。');
      try {
        const data = await api.getCommercialOrders(checkoutOrder.order_no, { timeoutMs: 20_000 });
        if (data?.order) setCheckoutOrder(data.order);
      } catch {
        // Preserve the original cancellation error.
      }
    } finally {
      setPaymentLoading('');
    }
  };

  useEffect(() => {
    setCheckoutClock(Date.now());
    const timer = window.setInterval(() => setCheckoutClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    if (purchasePlan) purchaseConfirmRef.current?.scrollIntoView?.({ behavior: 'smooth', block: 'nearest' });
  }, [purchasePlan?.id]);

  useEffect(() => {
    if (checkoutOrder) checkoutRef.current?.scrollIntoView?.({ behavior: 'smooth', block: 'nearest' });
  }, [checkoutOrder?.order_no]);

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

  const handleModalKeyDown = (event) => {
    if (event.key === 'Escape') {
      event.stopPropagation();
      if (purchasePlan) setPurchasePlan(null);
      else if (checkoutOrder) setCheckoutOrder(null);
      else onClose();
      return;
    }
    if (event.key !== 'Tab' || !modalRef.current) return;
    const focusable = [...modalRef.current.querySelectorAll(
      'button:not([disabled]), input:not([disabled]), a[href], summary, [tabindex]:not([tabindex="-1"])',
    )].filter((element) => {
      if (element.getAttribute('aria-hidden') === 'true') return false;
      const closedDisclosure = element.closest('details:not([open])');
      return !closedDisclosure || element === closedDisclosure.querySelector('summary');
    });
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

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="oc-modal relay-access-modal cc-settings-secondary-surface"
        role="dialog"
        aria-modal="true"
        aria-labelledby="relay-access-dialog-title"
        aria-describedby="relay-access-dialog-description"
        onClick={(event) => event.stopPropagation()}
        onKeyDown={handleModalKeyDown}
      >
        <div className="oc-modal-header relay-access-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3 id="relay-access-dialog-title">套餐与权益</h3>
            <p id="relay-access-dialog-description">查看当前用量、套餐权益与订单记录。</p>
          </div>
          <button ref={closeButtonRef} type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>

        <div className="relay-access-body">
          {loading && (
            <div className="oc-settings-secondary" role="status" aria-live="polite">
              正在读取套餐与权益…
            </div>
          )}
          {error && <InlineFeedback tone="error">{error}</InlineFeedback>}
          {warning && <InlineFeedback tone="warning">{warning}</InlineFeedback>}

          <section className="relay-access-commerce">
            <div className="relay-access-section-head">
              <div>
                <div className="relay-access-title">套餐概览</div>
                <div className="oc-settings-secondary">
                  {activePackages.length > 0
                    ? summarizeCommercial(commercialSummary)
                    : commercialEnabled
                      ? '当前没有有效套餐'
                    : '套餐兑换暂未开放；当前额度和 API Key 不受影响。'}
                </div>
              </div>
              <span className={`relay-access-state ${commercialState.className}`}>
                {commercialState.label}
              </span>
            </div>

            <div className={`relay-access-current-quota ${currentQuota.className}`}>
              <div className="relay-access-current-quota-head">
                <div className="relay-access-current-quota-heading">
                  <span>{currentQuota.title}</span>
                  <strong>{currentQuota.meta}</strong>
                </div>
                <div className="relay-access-current-quota-actions">
                  <em>{currentQuota.detail}</em>
                  {salePlans.length > 0 && (
                    <button
                      type="button"
                      className="relay-access-upgrade-button"
                      aria-haspopup="dialog"
                      aria-expanded={planChooserOpen}
                      aria-controls="relay-access-plan-chooser"
                      onClick={() => {
                        setPlanChooserAudience('personal');
                        setPlanChooserOpen(true);
                      }}
                    >
                      升级套餐
                    </button>
                  )}
                </div>
              </div>
              <div
                className="relay-access-quota-bar"
                role="progressbar"
                aria-label="套餐总用量"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={Math.min(100, Math.max(0, currentQuota.percent))}
                aria-valuetext={currentQuota.meta}
              >
                <i style={{ width: `${Math.min(100, Math.max(0, currentQuota.percent))}%` }} />
              </div>
              <div className="relay-access-current-quota-cycle">
                <span className="relay-access-current-quota-note">{currentQuota.note}</span>
                <span>下次重置时间：{currentResetInfo.detail.replace(/^下次\s*/, '')}</span>
              </div>
            </div>

            <div
              className="relay-access-billing-tabs"
              role="tablist"
              aria-label="套餐信息"
              onKeyDown={handleTabListKeyDown}
            >
              <button
                id="relay-access-entitlements-tab"
                type="button"
                role="tab"
                aria-selected={commercialView === 'plans'}
                aria-controls="relay-access-entitlements-panel"
                tabIndex={commercialView === 'plans' ? 0 : -1}
                className={commercialView === 'plans' ? 'active' : ''}
                onClick={() => setCommercialView('plans')}
              >
                <BadgeCheck size={15} />
                当前权益
              </button>
              <button
                id="relay-access-orders-tab"
                type="button"
                role="tab"
                aria-selected={commercialView === 'orders'}
                aria-controls="relay-access-orders-panel"
                tabIndex={commercialView === 'orders' ? 0 : -1}
                className={commercialView === 'orders' ? 'active' : ''}
                onClick={() => setCommercialView('orders')}
              >
                <History size={15} />
                订单记录
                {commercialOrders.length > 0 && <span>{commercialOrders.length}</span>}
              </button>
            </div>

            {purchasePlan && (
              <div ref={purchaseConfirmRef} className="relay-access-purchase-confirm" role="dialog" aria-label="确认购买套餐">
                <div>
                  <span>{purchaseIsRenewal ? '确认续费' : '确认购买'}</span>
                  <strong>{purchasePlan.name}</strong>
                  <p>{purchaseIsRenewal
                    ? `${formatPriceFen(purchasePlan.price_fen)}，续费后从当前套餐到期日顺延 ${Number(purchasePlan.duration_days || 30)} 天；处于到期保留期的云托管员工会自动恢复，已释放的实例无法恢复；不会自动扣款。`
                    : purchaseIsUpgrade
                    ? `${formatPriceFen(purchasePlan.price_fen)}，支付成功后立即切换，旧套餐剩余时间不顺延；新套餐从支付时刻重新计算 ${Number(purchasePlan.duration_days || 30)} 天，额度按新套餐重置。`
                    : `${formatPriceFen(purchasePlan.price_fen)}，有效期 ${Number(purchasePlan.duration_days || 30)} 天。套餐到期前不会自动续费。${purchaseIncludesCloudWorker ? '已创建的云托管员工到期后保留 15 天，期间续费即可恢复。' : ''}`}</p>
                </div>
                <div className="relay-access-purchase-confirm-actions">
                  <button type="button" className="secondary" onClick={() => setPurchasePlan(null)} disabled={Boolean(paymentLoading)}>返回</button>
                  <button type="button" onClick={confirmCommercialPurchase} disabled={Boolean(paymentLoading)}>
                    <CreditCard size={15} />
                    {paymentLoading === `create:${purchasePlan.id}` ? '正在打开…' : paymentChannel === 'alipay_page' ? '确认并前往支付宝' : '确认购买'}
                  </button>
                </div>
              </div>
            )}

            {checkoutOrder && (
              <div ref={checkoutRef} className={`relay-access-checkout ${checkoutOrder.status}`}>
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
                  <span><strong>{checkoutCountdown || formatShortDateTime(checkoutOrder.created_at) || '-'}</strong>{checkoutCountdown ? '支付剩余时间' : '创建时间'}</span>
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
                    {paymentLoading === `confirm:${checkoutOrder.order_no}` ? '入账中…' : '完成灰度测试支付'}
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
                  <div>
                    {['created', 'pending', 'failed'].includes(checkoutOrder.status) && (
                      <button type="button" className="danger" onClick={cancelCommercialOrder} disabled={Boolean(paymentLoading)}>
                        <X size={14} />
                        {paymentLoading === `cancel:${checkoutOrder.order_no}` ? '取消中…' : '取消订单'}
                      </button>
                    )}
                    <button type="button" onClick={refreshCheckoutOrder} disabled={Boolean(paymentLoading)}>
                      <RotateCcw size={14} />
                      {paymentLoading === `refresh:${checkoutOrder.order_no}` ? '刷新中…' : '刷新状态'}
                    </button>
                  </div>
                </div>
              </div>
            )}

            {commercialView === 'plans' && (
              <div
                id="relay-access-entitlements-panel"
                className="relay-access-billing-panel"
                role="tabpanel"
                aria-labelledby="relay-access-entitlements-tab"
              >
                {commercialEnabled && activePackages.length > 0 && (
                  <div className="relay-access-entitlement-band">
                    <div className="relay-access-package-list">
                      {activePackages.map((item) => (
                        <div className="relay-access-package-row" key={`${item.id || item.plan_id}-${item.source_ref || item.starts_at}`}>
                          <span>
                            <strong>{item.plan_name || item.plan_slug || '套餐'}</strong>
                            <em>{commercialEntitlementSourceLabel(item.source)} · {item.starts_at ? `${formatShortDate(item.starts_at)} 生效` : '已生效'}</em>
                          </span>
                          <strong>{item.expires_at ? `${formatShortDate(item.expires_at)} 到期` : '长期有效'}</strong>
                        </div>
                      ))}
                    </div>
                  </div>
                )}

                {commercialEnabled && activePackages.length === 0 && (
                  <div className="relay-access-token-note">
                    <Gift size={16} />
                    <span>当前没有有效套餐。你可以选购套餐或兑换套餐邀请码；特殊灰度由管理员配置。</span>
                  </div>
                )}

                {commercialEnabled ? (
                  <details className="relay-access-redeem-disclosure">
                    <summary>
                      <span>
                        <strong>兑换邀请码</strong>
                        <em>已有邀请码时使用</em>
                      </span>
                      <span>填写</span>
                    </summary>
                    <div className="relay-access-invite-section">
                      <span id="relay-access-invite-help">兑换成功后，套餐权益会直接加入当前账号。</span>
                      <form
                        className="relay-access-invite-form"
                        onSubmit={(event) => {
                          event.preventDefault();
                          redeemInvite();
                        }}
                      >
                        <label className="oc-visually-hidden" htmlFor="relay-access-invite-code">邀请码</label>
                        <input
                          id="relay-access-invite-code"
                          name="invite_code"
                          value={inviteCode}
                          onChange={(event) => setInviteCode(event.target.value)}
                          placeholder="输入邀请码…"
                          autoComplete="off"
                          spellCheck={false}
                          aria-describedby="relay-access-invite-help"
                          disabled={inviteLoading}
                        />
                        <button type="submit" disabled={inviteLoading}>
                          {inviteLoading ? '兑换中…' : copied === 'invite' ? '已兑换' : '兑换'}
                        </button>
                      </form>
                    </div>
                  </details>
                ) : (
                  <div className="relay-access-token-note">
                    <Gift size={16} />
                    <span>{commercial?.note || '套餐和邀请码仍在内部测试。当前额度、API Key 和模型调用不受影响。'}</span>
                  </div>
                )}
                {commercialEnabled && !commercialEnforced && (
                  <div className="oc-settings-secondary">
                    {commercial?.note || '当前为内测账单，购买记录不会自动改变已有模型额度。'}
                  </div>
                )}
              </div>
            )}

            {commercialView === 'orders' && (
              <div
                id="relay-access-orders-panel"
                className="relay-access-billing-panel"
                role="tabpanel"
                aria-labelledby="relay-access-orders-tab"
              >
                {commercialOrders.length > 0 && availableOrderFilters.length > 1 && (
                  <div className="relay-access-order-filters" role="group" aria-label="筛选订单">
                    {availableOrderFilters.map((filter) => (
                      <button
                        type="button"
                        key={filter.id}
                        className={orderFilter === filter.id ? 'active' : ''}
                        aria-pressed={orderFilter === filter.id}
                        onClick={() => {
                          setOrderFilter(filter.id);
                          setShowAllOrders(false);
                        }}
                      >
                        {filter.label}
                      </button>
                    ))}
                  </div>
                )}
                {visibleOrders.length > 0 ? (
                  <div className="relay-access-order-list">
                    {visibleOrders.map((order) => (
                      <button
                        type="button"
                        key={order.order_no}
                        className={checkoutOrder?.order_no === order.order_no ? 'active' : ''}
                        aria-pressed={checkoutOrder?.order_no === order.order_no}
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
                          <em className={commercialOrderTone(order.status)}>
                            {commercialOrderStatus(order.status)}
                            {commercialOrderCountdown(order, checkoutClock) ? ` · ${commercialOrderCountdown(order, checkoutClock)}` : ''}
                          </em>
                        </span>
                      </button>
                    ))}
                  </div>
                ) : (
                  <div className="relay-access-order-empty">
                    <strong>{commercialOrders.length > 0 ? '没有符合条件的订单' : '暂无订单记录'}</strong>
                    <span>{commercialOrders.length > 0 ? '可以切换其他状态查看。' : '购买套餐后，订单会显示在这里。'}</span>
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

          <div className="relay-access-developer-sections">
            <details className="relay-access-tool-disclosure relay-access-key-disclosure" open>
              <summary>
                <span>
                  <KeyRound size={17} />
                  <span>
                    <strong>我的 Key</strong>
                    <em>
                      {config.self_service_enabled
                        ? '用于第三方客户端或 CatsCo 自定义模型'
                        : '由管理员发放和管理访问凭证'}
                    </em>
                  </span>
                </span>
                <span className="relay-access-tool-disclosure-meta">
                  <span className={`relay-access-state ${stateClass}`}>{stateText}</span>
                  <span className="relay-access-tool-disclosure-label" aria-hidden="true"><ChevronDown size={16} /></span>
                </span>
              </summary>
              <section className="relay-access-key-panel relay-access-tool-disclosure-content">
                {!config.self_service_enabled && (
                  <div className="relay-access-token-note">
                    <KeyRound size={16} />
                    <span>{config.key_hint}</span>
                  </div>
                )}

                {config.self_service_enabled && keyLoading && (
                  <div className="oc-settings-secondary" role="status" aria-live="polite">正在读取你的 Key…</div>
                )}

                {config.self_service_enabled && !keyLoading && !relayKey && (
                  <div className="relay-access-empty-key">
                    <KeyRound size={18} />
                    <div>
                      <div className="relay-access-title">还没有 API Key</div>
                      <div className="oc-settings-secondary">生成后只显示一次明文，请立刻复制到需要使用的客户端。</div>
                    </div>
                    <button type="button" className="relay-access-primary-btn" disabled={busy} onClick={createKey}>
                      {actionLoading === 'create' ? '生成中…' : '生成我的 Key'}
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
                        <strong>{relayKey.prefix || 'sk-…'}</strong>
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
                        {actionLoading === 'reveal' ? '显示中…' : '显示并复制'}
                      </button>
                      <button type="button" disabled={busy} onClick={rotateKey}>
                        <RotateCcw size={15} />
                        {actionLoading === 'rotate' ? '重新生成中…' : '重新生成'}
                      </button>
                      <button type="button" className="relay-access-danger-action" disabled={busy} onClick={revokeKey}>
                        <Trash2 size={15} />
                        {actionLoading === 'revoke' ? '撤销中…' : '撤销'}
                      </button>
                    </div>
                  </div>
                )}
              </section>
            </details>

            <details className="relay-access-tool-disclosure relay-access-connect-disclosure">
              <summary>
                <span>
                  <ExternalLink size={17} />
                  <span>
                    <strong>开发者接入</strong>
                    <em>接口地址与开发者控制台</em>
                  </span>
                </span>
                <span className="relay-access-tool-disclosure-label" aria-hidden="true"><ChevronDown size={16} /></span>
              </summary>
              <div className="relay-access-connect relay-access-tool-disclosure-content">
                {config.docs_url && (
                  <div className="relay-access-developer-toolbar">
                    <button
                      type="button"
                      className="relay-access-open-btn"
                      onClick={openRelayPortal}
                      disabled={actionLoading === 'portal'}
                    >
                      {actionLoading === 'portal' ? '登录中…' : '打开开发者控制台'}
                      <ExternalLink size={14} />
                    </button>
                  </div>
                )}
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
            </details>

            <details className="relay-access-tool-disclosure relay-access-snippet-disclosure">
              <summary>
                <span>
                  <Copy size={17} />
                  <span>
                    <strong>快速配置</strong>
                    <em>复制 OpenAI 与 Anthropic 兼容配置</em>
                  </span>
                </span>
                <span className="relay-access-tool-disclosure-label" aria-hidden="true"><ChevronDown size={16} /></span>
              </summary>
              <div className="relay-access-snippet relay-access-tool-disclosure-content">
                <div className="relay-access-snippet-code">
                  <pre>{snippet}</pre>
                  <button
                    type="button"
                    className="relay-access-snippet-copy-button"
                    onClick={() => copyText('snippet', snippet)}
                    aria-label="复制快速配置"
                  >
                    {copied === 'snippet' ? <Check size={15} /> : <Copy size={15} />}
                    复制
                  </button>
                </div>
              </div>
            </details>
          </div>
        </div>
      </div>
      {planChooserOpen && (
        <div
          className="relay-access-plan-overlay"
          role="presentation"
          onClick={(event) => {
            event.stopPropagation();
            if (event.target === event.currentTarget) setPlanChooserOpen(false);
          }}
        >
          <section
            id="relay-access-plan-chooser"
            className="relay-access-plan-modal"
            role="dialog"
            aria-modal="true"
            aria-label="升级套餐"
            onClick={(event) => event.stopPropagation()}
          >
            <header className="relay-access-plan-modal-header">
              <div>
                <h3>选择适合你的套餐</h3>
                <p>按实际工作频率选择，套餐到期前不会自动续费。</p>
              </div>
              <button type="button" onClick={() => setPlanChooserOpen(false)} aria-label="关闭升级套餐">
                <X size={18} />
              </button>
            </header>
            <div className="relay-access-plan-modal-body">
              <div
                className="relay-access-plan-audience-tabs"
                role="tablist"
                aria-label="选择方案类型"
                onKeyDown={handleTabListKeyDown}
              >
                <button
                  id="relay-access-personal-plan-tab"
                  type="button"
                  role="tab"
                  aria-selected={planChooserAudience === 'personal'}
                  aria-controls="relay-access-personal-plan-panel"
                  tabIndex={planChooserAudience === 'personal' ? 0 : -1}
                  className={planChooserAudience === 'personal' ? 'active' : ''}
                  onClick={() => setPlanChooserAudience('personal')}
                >
                  个人
                </button>
                <button
                  id="relay-access-enterprise-plan-tab"
                  type="button"
                  role="tab"
                  aria-selected={planChooserAudience === 'enterprise'}
                  aria-controls="relay-access-enterprise-plan-panel"
                  tabIndex={planChooserAudience === 'enterprise' ? 0 : -1}
                  className={planChooserAudience === 'enterprise' ? 'active' : ''}
                  onClick={() => setPlanChooserAudience('enterprise')}
                >
                  企业
                </button>
              </div>
              {planChooserAudience === 'personal' ? (
                <div
                  id="relay-access-personal-plan-panel"
                  role="tabpanel"
                  aria-labelledby="relay-access-personal-plan-tab"
                >
                  <CommercialPlanCards
                    salePlans={salePlans}
                    hasCurrentCommercialPlans={hasCurrentCommercialPlans}
                    activeOfficialPlanTier={activeOfficialPlanTier}
                    activePackages={activePackages}
                    openOrders={openOrders}
                    paymentChannel={paymentChannel}
                    paymentChannels={paymentChannels}
                    paymentLoading={paymentLoading}
                    onOpenOrder={openCommercialOrderFromChooser}
                    onSelectPlan={selectCommercialPlan}
                    compact
                    className="relay-access-plan-list-chooser"
                  />
                </div>
              ) : (
                <div
                  id="relay-access-enterprise-plan-panel"
                  role="tabpanel"
                  aria-labelledby="relay-access-enterprise-plan-tab"
                >
                  <EnterprisePlanCard />
                </div>
              )}
            </div>
          </section>
        </div>
      )}
    </div>
  );
}
