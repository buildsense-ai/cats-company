import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
vi.mock('../../api', () => ({ api: { getModelCreditAccount: vi.fn() } }));
import { api } from '../../api';
import useModelCreditAccount, { modelCreditQuotaDisplay } from './use-model-credit-account';

function Probe({ revision = 0, enabled = true }) {
  const state = useModelCreditAccount(revision, enabled);
  return <output>{state.error || (state.account ? JSON.stringify(modelCreditQuotaDisplay(state.account, 4_000_000_000)) : 'loading')}</output>;
}
describe('credit account content', () => {
  let container, root;
  const account = { monthly_units: 3_999_123_456, gift_units: 20_000_000, paid_units: 7_000_000, debt_units: 2_500_000, units_per_credit: 1_000_000 };
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    vi.clearAllMocks();
    api.getModelCreditAccount.mockResolvedValue(account);
    container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
  it('keeps gifts, paid balance and debt separate', async () => {
    await act(async () => root.render(<Probe />));
    const content = JSON.parse(container.textContent);
    expect(content.meta).toBe('4,026.123456 credit');
    expect(content.detail).toContain('欠款 2.5 credit');
    expect(content.note).toContain('月额度 3,999.123456 · 邀请赠送 20 · 充值余额 7 · 欠款 2.5 credit');
    expect(content.className).toBe('exhausted');
  });
  it('shows a load error without fabricating a zero and retries after revision changes', async () => {
    api.getModelCreditAccount.mockRejectedValueOnce(new Error('offline'));
    await act(async () => root.render(<Probe />));
    expect(container.textContent).toContain('credit 余额读取失败');
    await act(async () => root.render(<Probe revision={1} />));
    expect(container.textContent).toContain('4,026.123456 credit');
    expect(api.getModelCreditAccount).toHaveBeenCalledTimes(2);
  });
  it('does not call the credit endpoint for legacy-only views', async () => {
    await act(async () => root.render(<Probe enabled={false} />));
    expect(api.getModelCreditAccount).not.toHaveBeenCalled();
  });
});
