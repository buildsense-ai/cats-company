// Demonstration model only. Integer microcredits avoid floating point drift.
// Six-decimal rounding and price fixtures are proposals, not live billing rules.
export const SCALE = 1_000_000;
export const micros = value => {
  const text = String(value);
  if (!/^\d+(\.\d{1,6})?$/.test(text)) throw new Error('请输入非负数，最多 6 位小数');
  const [whole, fraction = ''] = text.split('.');
  const amount = Number(BigInt(whole) * 1_000_000n + BigInt(fraction.padEnd(6, '0')));
  if (!Number.isSafeInteger(amount)) throw new Error('数值超出演示范围');
  return amount;
};
export const credit = (amount, digits = 4) => (amount / SCALE).toLocaleString('zh-CN', { minimumFractionDigits: 0, maximumFractionDigits: digits });
export function calculateCall(usage, prices, rateBps) {
  for (const value of [...Object.values(usage), ...Object.values(prices), rateBps]) {
    if (!Number.isSafeInteger(value) || value < 0) throw new Error('计量、价格及折扣必须为非负整数');
  }
  if (rateBps > 10000) throw new Error('无效费率');
  const numerator = BigInt(usage.input) * BigInt(prices.input) + BigInt(usage.cached) * BigInt(prices.cached) + BigInt(usage.output) * BigInt(prices.output);
  const denominator = 1_000_000n * 10_000n;
  return Number((numerator * BigInt(rateBps) + denominator / 2n) / denominator);
}
export function canStart(state, hasPermission = true) {
  return hasPermission && state.debt === 0 && state.monthly + state.gift + state.recharge > 0;
}
export function consume(state, amount, continuing = false) {
  if (!Number.isSafeInteger(amount) || amount < 0) throw new Error('无效消费额');
  if (!continuing && !canStart(state)) throw new Error('欠款未结清或无可用额度，不能启动新任务');
  const after = { ...state }, allocation = { monthly: 0, gift: 0, recharge: 0, debt: 0 };
  let remaining = amount;
  for (const bucket of ['monthly', 'gift', 'recharge']) {
    allocation[bucket] = Math.min(after[bucket], remaining);
    after[bucket] -= allocation[bucket]; remaining -= allocation[bucket];
  }
  allocation.debt = remaining; after.debt += remaining;
  return { after, allocation };
}
export function grant(state, kind, amount) {
  if (!['monthly', 'gift', 'recharge'].includes(kind) || !Number.isSafeInteger(amount) || amount < 0) throw new Error('无效入账');
  const after = { ...state };
  const debtPaid = kind === 'gift' ? 0 : Math.min(after.debt, amount);
  after.debt -= debtPaid;
  // Monthly grant starts a new cycle. Previous cycle's monthly amount expires.
  if (kind === 'monthly') after.monthly = amount - debtPaid;
  else after[kind] += amount - debtPaid;
  return { after, debtPaid };
}
export function survivors(running) {
  // Deterministic task-id tie-break is a proposed implementation detail.
  return [...running].filter(task => task.state === 'running').sort((a, b) => b.startedAt - a.startedAt || a.id.localeCompare(b.id)).slice(0, 1);
}
export function nextCycle(iso, anchorDay) {
  const source = new Date(iso);
  const shanghai = new Date(source.getTime() + 8 * 3600_000);
  const year = shanghai.getUTCFullYear(), month = shanghai.getUTCMonth() + 1;
  const day = Math.min(anchorDay, new Date(Date.UTC(year, month + 1, 0)).getUTCDate());
  return new Date(Date.UTC(year, month, day, shanghai.getUTCHours() - 8, shanghai.getUTCMinutes(), shanghai.getUTCSeconds())).toISOString();
}
export function proratedUpgrade(from, to, start, end, now) {
  const duration = new Date(end) - new Date(start), remaining = new Date(end) - new Date(now);
  if (duration <= 0 || remaining < 0 || remaining > duration || to.price < from.price || to.quota < from.quota) throw new Error('无效升级周期');
  return { price: Math.round((to.price - from.price) * remaining / duration), credits: Math.round((to.quota - from.quota) * remaining / duration) };
}
