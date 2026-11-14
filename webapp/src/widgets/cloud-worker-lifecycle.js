// 机器到期 / 数据保留的展示口径。
//
// 三件事分别来自后端三个字段，彼此不能替代：
//   package_expires_at  这台机器自己的计费截止
//   delete_after        平台真正会清掉这台机器数据的时间（缺省 = 没有自动清理截止）
//   retention_days      这一行创建时的保留窗口，只能说明「通常多久」，不是这台机器的截止
//
// 前端不虚构截止：后端没给 delete_after 就不显示一个编出来的日期。剩余时间一律
// 非负（过期就是 0，不出现「还剩 -3 天」），并按分钟重算。

export const MINUTE_MS = 60_000;

const MS_PER_DAY = 86_400_000;
const MS_PER_HOUR = 3_600_000;

export function parseTimestamp(value) {
  if (!value) return null;
  const time = new Date(value).getTime();
  return Number.isNaN(time) ? null : time;
}

export function formatDate(value) {
  const time = parseTimestamp(value);
  if (time === null) return '';
  const date = new Date(time);
  const mm = String(date.getMonth() + 1).padStart(2, '0');
  const dd = String(date.getDate()).padStart(2, '0');
  return `${date.getFullYear()}-${mm}-${dd}`;
}

// 剩余时间按「天 + 小时」给出，且永不为负。
export function remainingUntil(value, nowMs = Date.now()) {
  const target = parseTimestamp(value);
  if (target === null) return { known: false, expired: false, days: 0, hours: 0, ms: 0 };
  const ms = target - nowMs;
  if (ms <= 0) return { known: true, expired: true, days: 0, hours: 0, ms: 0 };
  return {
    known: true,
    expired: false,
    days: Math.floor(ms / MS_PER_DAY),
    hours: Math.floor((ms % MS_PER_DAY) / MS_PER_HOUR),
    ms,
  };
}

export function remainingLabel(span) {
  if (!span?.known) return '';
  if (span.expired) return '已过期';
  return `还剩 ${span.days} 天 ${span.hours} 小时`;
}

// 7 天内标红：这是用户真正需要去续费的时间点。
export function isUrgentExpiry(span) {
  return Boolean(span?.known && !span.expired && span.days < 7);
}

// 只翻译后端确实会给出的生命周期状态；未知值不猜，直接不显示，
// 免得把内部英文状态码漏给用户。
const LIFECYCLE_STATE_LABELS = {
  active: '计费期内',
  delete_pending: '已在删除流程中',
  deleted: '已删除',
};

export function lifecycleStateLabel(state) {
  const key = String(state || '').trim().toLowerCase();
  if (!key) return '';
  return LIFECYCLE_STATE_LABELS[key] || '';
}

// expiry_mode 说明这一台按什么口径到期，和「什么时候到期」是两件事，
// 所以单独翻译；缺字段时什么都不显示。
const EXPIRY_MODE_LABELS = {
  fixed: '按本机期限',
  follow_plan: '随套餐期限',
  permanent: '长期有效',
  legacy: '存量机器规则',
};

export function expiryModeLabel(mode) {
  const key = String(mode || '').trim().toLowerCase();
  if (!key) return '';
  return EXPIRY_MODE_LABELS[key] || '';
}


