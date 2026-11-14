import { useEffect, useState } from 'react';
import { api } from '../../api';

// 身份只认后端：`enabled` 表示这个账号进过 credit 体系，`allowed` 表示当期可用。
// 前端不再用「商品是否带 model_credit_units」推断身份 —— 真正的 legacy 老用户
// 卖的是旧额度商品，那种推断既会把老账号误标成 credit 账户，也会让一次商品
// 配置改动改变别人看到的身份。所以每个账号都要读一次 GET /api/relay/credits，
// 由后端字段决定显示哪套额度。
export const MODEL_CREDIT_LEGACY_NOTICE = '当前使用旧套餐额度，尚未启用 credit 账户';
// allowed=false 只能证明「当期不能用」，不能证明是套餐到期（也可能是权限被收回）。
// 所以文案只报当前模型权限不可用，并让用户自己去核对有效期与权限状态。
export const MODEL_CREDIT_UNAVAILABLE_NOTICE = 'credit 账户仍在，当前模型权限不可用；账本余额仍可查看，请检查套餐有效期及权限状态。';
export const MODEL_CREDIT_TOPUP_NOTICE = '独立余额充值暂未开放';
export const MODEL_CREDIT_CYCLE_NOTE = '月额度按订阅生效日锚定的自然月刷新，不结转；提前续费不会提前补额。';
// 桶名用「付费余额」而不是「充值余额」：这个桶里的钱是既有已入账额度，
// 独立充值入口并未开放，写「充值」会让人以为现在就能充。
export const MODEL_CREDIT_PAID_LABEL = '付费余额';
// 后端只在「下一期已被已付订阅覆盖」时才给 next_refresh_at。没有它就说明刷新要等
// 续费，前端只能这么提示，不能自己按 +1 个月推算下一期日期。
export const MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE = '本期结束后需续费才会有下一期额度';

export default function useModelCreditAccount(revision) {
  const [state, setState] = useState({ account: null, error: '', loading: true, loaded: false });
  useEffect(() => {
    let cancelled = false;
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    api.getModelCreditAccount({ signal: controller.signal, timeoutMs: 20_000 })
      .then((account) => {
        if (!cancelled) setState({ account: account || null, error: '', loading: false, loaded: true });
      })
      .catch(() => {
        if (!cancelled) {
          // A failed read is not a legacy account: keep `account` null and let the
          // caller fall back to the old usage quota with the error surfaced.
          setState({ account: null, error: 'credit 余额读取失败，请稍后重试。', loading: false, loaded: true });
        }
      });
    return () => { cancelled = true; controller.abort(); };
  }, [revision]);
  return state;
}

// 后端字段 → 展示身份。三态：
//   unknown  没读到（读取失败/未返回），不做任何身份判断
//   legacy   enabled=false，用旧套餐额度，不给 credit 余额页
//   blocked  enabled=true 但 allowed=false：余额照常显示，只说当期不可用
export function modelCreditIdentity(account) {
  if (!account) return { known: false, enabled: false, allowed: false, state: 'unknown' };
  const enabled = account.enabled === true;
  const allowed = enabled && account.allowed === true;
  return {
    known: true,
    enabled,
    allowed,
    state: !enabled ? 'legacy' : (allowed ? 'active' : 'blocked'),
  };
}

const parseCreditTimestamp = (value) => {
  if (!value) return null;
  const time = new Date(value).getTime();
  return Number.isNaN(time) ? null : time;
};

// 只把后端给的 RFC3339 转成日期；月份边界由后端按订阅锚点算，前端不推算。
const formatCreditDate = (time) => {
  const date = new Date(time);
  const mm = String(date.getMonth() + 1).padStart(2, '0');
  const dd = String(date.getDate()).padStart(2, '0');
  return `${date.getFullYear()}-${mm}-${dd}`;
};

// 余额区域的周期行：真实当前自然月窗口、订阅截止、下一次确定刷新。
//   - legacy / 邀请码 / 免费账号没有月周期，四个字段整体缺省 → known=false，
//     一行都不显示，绝不编日期。
//   - subscription_expires_at 缺省表示没有固定期限（internal/admin 长期订阅），
//     这时不写「订阅截止」也不催续费。
//   - next_refresh_at 缺省表示没有已付订阅覆盖下一期 → 只提示「本期结束后需续费」。
export function modelCreditPeriodDisplay(account) {
  const safe = account || {};
  const unknown = { known: false, period: '', lines: [] };
  if (safe.enabled !== true) return unknown;
  const startsAt = parseCreditTimestamp(safe.monthly_period_starts_at);
  const endsAt = parseCreditTimestamp(safe.monthly_period_ends_at);
  const expiresAt = parseCreditTimestamp(safe.subscription_expires_at);
  const nextAt = parseCreditTimestamp(safe.next_refresh_at);
  if (startsAt === null && endsAt === null && expiresAt === null && nextAt === null) return unknown;
  const lines = [];
  if (startsAt !== null || endsAt !== null) {
    const start = startsAt !== null ? formatCreditDate(startsAt) : '—';
    const end = endsAt !== null ? formatCreditDate(endsAt) : '—';
    lines.push(`本期 ${start} 至 ${end}`);
  }
  if (expiresAt !== null) lines.push(`订阅截止 ${formatCreditDate(expiresAt)}`);
  if (nextAt !== null) lines.push(`下次刷新 ${formatCreditDate(nextAt)}`);
  else if (endsAt !== null || expiresAt !== null) lines.push(MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE);
  return { known: true, period: lines.join(' · '), lines };
}

export function modelCreditQuotaDisplay(account, allowance = 0, identity = modelCreditIdentity(account)) {
  const safe = account || {};
  const scale = Number(safe.units_per_credit) || 1_000_000;
  const value = key => Number(safe[`${key}_units`] || 0);
  const format = units => (units / scale).toLocaleString('zh-CN', { maximumFractionDigits: 6 });
  const buckets = ['monthly', 'gift', 'paid', 'debt'].map(key => value(key));
  const [monthly, gift, paid, debt] = buckets;
  const available = Math.max(0, monthly + gift + paid);
  const unavailable = identity.state === 'blocked';
  const period = modelCreditPeriodDisplay(safe);
  const statusParts = [
    `credit 账户：${identity.enabled ? '已启用' : '未启用'}`,
    `当期可用：${identity.allowed ? '是' : (unavailable ? '否（当前模型权限不可用）' : '否')}`,
  ];
  return {
    className: debt > 0 ? 'danger' : (unavailable || available <= 0 ? 'warning' : 'normal'),
    title: unavailable ? 'credit 余额（当前模型权限不可用）' : '可用 credit',
    meta: `${format(available)} credit`,
    detail: unavailable ? MODEL_CREDIT_UNAVAILABLE_NOTICE : `按月额度 → 邀请赠送 → ${MODEL_CREDIT_PAID_LABEL}扣减`,
    // 四个桶必须各自说清楚：只报一个合计时，用户没法判断扣的是哪一笔。
    note: `月额度 ${format(monthly)} · 邀请赠送 ${format(gift)} · ${MODEL_CREDIT_PAID_LABEL} ${format(paid)} · 欠款 ${format(debt)} credit。${MODEL_CREDIT_CYCLE_NOTE}`,
    buckets: `月额度 ${format(monthly)} credit · 邀请赠送 ${format(gift)} credit · ${MODEL_CREDIT_PAID_LABEL} ${format(paid)} credit · 欠款 ${format(debt)} credit`,
    status: `${statusParts.join(' · ')} · ${MODEL_CREDIT_TOPUP_NOTICE}`,
    // 周期只用后端字段：legacy / 邀请码账号这里恒为空串。
    period: period.period,
    periodLines: period.lines,
    percent: allowance > 0 ? Math.max(0, Math.min(100, (1 - monthly / allowance) * 100)) : 0,
    identity,
  };
}
