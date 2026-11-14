import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
vi.mock('../../api', () => ({ api: { getModelCreditAccount: vi.fn() } }));
import { api } from '../../api';
import useModelCreditAccount, {
  MODEL_CREDIT_LEGACY_NOTICE,
  MODEL_CREDIT_PAID_LABEL,
  MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE,
  MODEL_CREDIT_TOPUP_NOTICE,
  MODEL_CREDIT_UNAVAILABLE_NOTICE,
  modelCreditIdentity,
  modelCreditPeriodDisplay,
  modelCreditQuotaDisplay,
} from './use-model-credit-account';

function Probe({ revision = 0 }) {
  const state = useModelCreditAccount(revision);
  const identity = modelCreditIdentity(state.account);
  return <output>{state.error
    || (state.account
      ? JSON.stringify({ ...modelCreditQuotaDisplay(state.account, 4_000_000_000, identity), identity })
      : 'loading')}</output>;
}
describe('credit account content', () => {
  let container, root;
  const account = {
    enabled: true,
    allowed: true,
    monthly_units: 3_999_123_456,
    gift_units: 20_000_000,
    paid_units: 7_000_000,
    debt_units: 2_500_000,
    units_per_credit: 1_000_000,
  };
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.clearAllMocks();
    api.getModelCreditAccount.mockResolvedValue(account);
    container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

  it('keeps monthly, gift, paid balance and debt separate', async () => {
    await act(async () => root.render(<Probe />));
    const content = JSON.parse(container.textContent);
    expect(content.meta).toBe('4,026.123456 credit');
    // 付费余额桶不叫「充值余额」：独立充值未开放，桶名不能让人以为现在能充。
    expect(MODEL_CREDIT_PAID_LABEL).toBe('付费余额');
    expect(content.buckets).toBe('月额度 3,999.123456 credit · 邀请赠送 20 credit · 付费余额 7 credit · 欠款 2.5 credit');
    expect(content.note).toContain('月额度 3,999.123456 · 邀请赠送 20 · 付费余额 7 · 欠款 2.5 credit');
    expect(content.buckets).not.toContain('充值余额');
    // Month allowance is anchored to the subscription and never rolls over;
    // an early renewal must not re-fill the current month.
    expect(content.note).toContain('月额度按订阅生效日锚定的自然月刷新，不结转；提前续费不会提前补额。');
    expect(content.className).toBe('danger');
  });

  it('shows enabled and allowed separately and states that top-up is not open yet', async () => {
    await act(async () => root.render(<Probe />));
    const content = JSON.parse(container.textContent);
    expect(content.status).toBe(`${'credit 账户：已启用 · 当期可用：是'} · ${MODEL_CREDIT_TOPUP_NOTICE}`);
    expect(MODEL_CREDIT_TOPUP_NOTICE).toBe('独立余额充值暂未开放');
    expect(content.identity).toMatchObject({ known: true, enabled: true, allowed: true, state: 'active' });
  });

  it('shows the backend period, subscription deadline and next refresh in the balance area', async () => {
    api.getModelCreditAccount.mockResolvedValue({
      ...account,
      monthly_period_starts_at: '2026-11-01T12:00:00Z',
      monthly_period_ends_at: '2026-12-01T12:00:00Z',
      subscription_expires_at: '2026-12-01T12:00:00Z',
      next_refresh_at: '2026-12-01T12:00:00Z',
    });
    await act(async () => root.render(<Probe />));
    const content = JSON.parse(container.textContent);
    expect(content.period).toBe('本期 2026-11-01 至 2026-12-01 · 订阅截止 2026-12-01 · 下次刷新 2026-12-01');
    expect(content.periodLines).toHaveLength(3);
    expect(content.period).not.toContain(MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE);
  });

  it('asks for a renewal without inventing the next period when the backend sends no next refresh', async () => {
    api.getModelCreditAccount.mockResolvedValue({
      ...account,
      monthly_period_starts_at: '2026-11-01T12:00:00Z',
      monthly_period_ends_at: '2026-12-01T12:00:00Z',
      subscription_expires_at: '2026-12-01T12:00:00Z',
    });
    await act(async () => root.render(<Probe />));
    const content = JSON.parse(container.textContent);
    expect(content.period).toBe(`本期 2026-11-01 至 2026-12-01 · 订阅截止 2026-12-01 · ${MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE}`);
    // 前端不按 +1 个月推算下一期：没有后端字段就只说「需续费」。
    expect(content.period).not.toContain('下次刷新');
    expect(MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE).toBe('本期结束后需续费才会有下一期额度');
  });

  it('keeps an unavailable credit balance visible without claiming the plan expired', async () => {
    api.getModelCreditAccount.mockResolvedValue({ ...account, allowed: false, monthly_units: 0 });
    await act(async () => root.render(<Probe />));
    const content = JSON.parse(container.textContent);
    expect(content.title).toBe('credit 余额（当前模型权限不可用）');
    expect(content.meta).toBe('27 credit');
    // allowed=false 只能说明「当期不能用」：前端不许替用户断定套餐到期，
    // 也不许承诺续费后自动恢复，只能让他自己核对有效期与权限状态。
    expect(MODEL_CREDIT_UNAVAILABLE_NOTICE).toBe('credit 账户仍在，当前模型权限不可用；账本余额仍可查看，请检查套餐有效期及权限状态。');
    expect(content.detail).toBe(MODEL_CREDIT_UNAVAILABLE_NOTICE);
    expect(content.status).toContain('当期可用：否（当前模型权限不可用）');
    expect(content.identity).toMatchObject({ enabled: true, allowed: false, state: 'blocked' });
    expect(JSON.stringify(content)).not.toMatch(/套餐已到期|套餐到期|续费后恢复|自动恢复|会恢复可用/);
  });

  it('treats a backend-disabled account as legacy and still reads the credit endpoint', async () => {
    api.getModelCreditAccount.mockResolvedValue({ enabled: false, allowed: false, units_per_credit: 1_000_000 });
    await act(async () => root.render(<Probe />));
    const content = JSON.parse(container.textContent);
    expect(content.identity).toMatchObject({ known: true, enabled: false, allowed: false, state: 'legacy' });
    // No product-level inference: the request happens for every account.
    expect(api.getModelCreditAccount).toHaveBeenCalledTimes(1);
    expect(MODEL_CREDIT_LEGACY_NOTICE).toBe('当前使用旧套餐额度，尚未启用 credit 账户');
  });

  it('shows a load error without fabricating a zero and retries after revision changes', async () => {
    api.getModelCreditAccount.mockRejectedValueOnce(new Error('offline'));
    await act(async () => root.render(<Probe />));
    expect(container.textContent).toContain('credit 余额读取失败');
    await act(async () => root.render(<Probe revision={1} />));
    expect(container.textContent).toContain('4,026.123456 credit');
    expect(api.getModelCreditAccount).toHaveBeenCalledTimes(2);
    expect(api.getModelCreditAccount).toHaveBeenCalledWith(expect.objectContaining({ timeoutMs: 20_000 }));
  });

  it('never reports an identity when the account is unknown', () => {
    expect(modelCreditIdentity(null)).toEqual({ known: false, enabled: false, allowed: false, state: 'unknown' });
    expect(modelCreditIdentity(undefined).known).toBe(false);
  });
});

// 周期行只翻译后端字段；任何缺口都不许编日期，也不许按 +1 个月推算。
describe('credit monthly period display', () => {
  const empty = { known: false, period: '', lines: [] };

  it('never fabricates a monthly period for legacy, invite or free accounts', () => {
    expect(modelCreditPeriodDisplay(null)).toEqual(empty);
    expect(modelCreditPeriodDisplay({ enabled: false, allowed: false })).toEqual(empty);
    // 邀请码 / 免费账号：有 credit 身份但没有月订阅，四个周期字段整体缺省。
    expect(modelCreditPeriodDisplay({ enabled: true, allowed: true, gift_units: 20_000_000 })).toEqual(empty);
  });

  it('does not invent a subscription deadline for a no-term subscription', () => {
    const display = modelCreditPeriodDisplay({
      enabled: true,
      monthly_period_starts_at: '2026-11-01T12:00:00Z',
      monthly_period_ends_at: '2026-12-01T12:00:00Z',
      next_refresh_at: '2026-12-01T12:00:00Z',
    });
    expect(display.known).toBe(true);
    expect(display.period).toBe('本期 2026-11-01 至 2026-12-01 · 下次刷新 2026-12-01');
    expect(display.period).not.toContain('订阅截止');
    expect(display.period).not.toContain(MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE);
  });

  it('ignores a timestamp it cannot parse instead of printing a made-up date', () => {
    const display = modelCreditPeriodDisplay({
      enabled: true,
      monthly_period_starts_at: 'not-a-date',
      monthly_period_ends_at: '2026-12-01T12:00:00Z',
    });
    expect(display.known).toBe(true);
    // 解析不了的时间不显示成编造的日期，只用占位符，并保留「需续费」提示。
    expect(display.period).toBe(`本期 — 至 2026-12-01 · ${MODEL_CREDIT_RENEWAL_REQUIRED_NOTICE}`);
  });
});
