import { useEffect, useState } from 'react';
import { api } from '../../api';

export default function useModelCreditAccount(revision, enabled) {
  const [state, setState] = useState({ account: null, error: '', loading: true });
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    const controller = new AbortController();
    setState(previous => ({ ...previous, loading: true }));
    api.getModelCreditAccount({ signal: controller.signal, timeoutMs: 20_000 })
      .then(account => { if (!cancelled) setState({ account, error: '', loading: false }); })
      .catch(() => { if (!cancelled) setState({ account: null, error: 'credit 余额读取失败，请稍后重试。', loading: false }); });
    return () => { cancelled = true; controller.abort(); };
  }, [revision, enabled]);
  return state;
}

export function modelCreditQuotaDisplay(account, allowance = 0) {
  const scale = Number(account.units_per_credit) || 1_000_000;
  const value = key => Number(account[`${key}_units`] || 0);
  const format = units => (units / scale).toLocaleString('zh-CN', { maximumFractionDigits: 6 });
  const available = value('monthly') + value('gift') + value('paid');
  const debt = value('debt');
  return {
    className: debt > 0 || available <= 0 ? 'exhausted' : 'normal',
    title: '可用 credit',
    meta: `${format(available)} credit`,
    detail: debt > 0 ? `欠款 ${format(debt)} credit · 邀请赠送不抵欠款` : '按月额度 → 邀请赠送 → 充值余额扣减',
    percent: allowance > 0 ? Math.max(0, Math.min(100, (1 - value('monthly') / allowance) * 100)) : 0,
    note: `月额度 ${format(value('monthly'))} · 邀请赠送 ${format(value('gift'))} · 充值余额 ${format(value('paid'))} · 欠款 ${format(debt)} credit。月额度当期有效，不结转。`,
  };
}
