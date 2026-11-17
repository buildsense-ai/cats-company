import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import CreditTopupForm, { parseTopupAmount } from './plans/credit-topup-form';
import CloudWorkerUpgrade from './cloud-worker-upgrade';
vi.mock('../api', () => ({ api: {
  getCloudWorkerUpgradeQuote: vi.fn(), upgradeCloudWorker: vi.fn(), getCloudWorkerUpgradeOperation: vi.fn(),
} }));
import { api } from '../api';

const config = { enabled: true, min_amount_fen: 100, max_amount_fen: 1000000, credits_per_cny: 100, presets_fen: [1000, 3000, 5000, 10000, 30000, 50000] };
const worker = { id: 91, tenant_name: 'tenant-a', display_name: '审查助手' };
const quote = { enabled: true, quote_id: 'quote-a', from_plan: 'light', to_plan: 'standard', cost_units: 2000000000,
  balance_units: 5000000000, units_per_credit: 1000000, expires_at: new Date(Date.now() + 300000).toISOString(), worker_expires_at: '2027-01-01T00:00:00Z' };

describe('credit commerce interactions', () => {
  let container, root;
  const render = async element => act(async () => { root.render(element); });
  const click = async text => {
    const button = [...container.querySelectorAll('button')].find(item => item.textContent === text);
    expect(button).toBeDefined();
    await act(async () => { button.click(); });
  };
  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    sessionStorage.clear(); vi.resetAllMocks();
    container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
    api.getCloudWorkerUpgradeQuote.mockResolvedValue(quote);
    api.getCloudWorkerUpgradeOperation.mockResolvedValue({ operation_id: 'op-a', status: 'reconciling' });
  });
  afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

  test.each([['1.01', 101], ['10.29', 1029], ['10000', 1000000], ['1e3', null], ['1.001', null], ['-10', null], ['', null]])('parses exact decimal %s', (input, expected) => {
    expect(parseTopupAmount(input)).toBe(expected);
  });
  test('hides topup unless the server enables it', async () => {
    await render(<CreditTopupForm config={{ ...config, enabled: false }} onSelect={vi.fn()} />);
    expect(container.querySelector('form')).toBeNull();
  });
  test('submits exact custom amount and never creates a package purchase', async () => {
    const selected = vi.fn();
    await render(<CreditTopupForm config={config} onSelect={selected} />);
    await act(async () => Simulate.change(container.querySelector('input'), { target: { value: '10.29' } }));
    await act(async () => Simulate.submit(container.querySelector('form')));
    expect(selected).toHaveBeenCalledWith(expect.objectContaining({ kind: 'credit_topup', amount_fen: 1029 }));
    await act(async () => Simulate.change(container.querySelector('input'), { target: { value: '0.99' } }));
    expect(container.querySelector('[type=submit]').disabled).toBe(true);
  });
  test('requires a quote and sufficient balance before spending points', async () => {
    api.getCloudWorkerUpgradeQuote.mockResolvedValue({ ...quote, balance_units: 1 });
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('查看升配报价');
    expect(container.textContent).toContain('余额不足');
    expect([...container.querySelectorAll('button')].find(item => item.textContent === '确认并支付点数').disabled).toBe(true);
    expect(api.upgradeCloudWorker).not.toHaveBeenCalled();
  });
  test('keeps the same request on ambiguous timeout and resumes operation polling', async () => {
    api.upgradeCloudWorker.mockRejectedValueOnce(Object.assign(new Error('timeout'), { code: 'REQUEST_TIMEOUT' }))
      .mockResolvedValueOnce({ operation_id: 'op-a', status: 'reconciling' });
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('查看升配报价');
    await click('确认并支付点数');
    expect(container.textContent).toContain('暂未确认升配结果');
    const first = api.upgradeCloudWorker.mock.calls[0];
    await click('继续查询本次升配');
    expect(api.upgradeCloudWorker.mock.calls[1].slice(0, 4)).toEqual(first.slice(0, 4));
    expect(api.getCloudWorkerUpgradeOperation).toHaveBeenCalledWith('op-a', expect.any(Object));
    expect(container.textContent).toContain('正在核实机器配置');
    expect(container.querySelector('select')).toBeNull();
  });
  test('retains the original request when a retry encounters an owner operation gate', async () => {
    api.upgradeCloudWorker.mockRejectedValueOnce(new Error('timeout'))
      .mockRejectedValueOnce(Object.assign(new Error('busy'), { status: 409, data: { code: 'cloud_worker_operation_busy' } }))
      .mockResolvedValueOnce({ operation_id: 'op-a', status: 'reconciling' });
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('查看升配报价'); await click('确认并支付点数');
    const original = api.upgradeCloudWorker.mock.calls[0].slice(0, 4);
    await click('继续查询本次升配');
    expect(container.textContent).toContain('暂未确认升配结果');
    expect(container.querySelector('select')).toBeNull();
    await click('继续查询本次升配');
    expect(api.upgradeCloudWorker.mock.calls[2].slice(0, 4)).toEqual(original);
  });
  test('recovers the operation from an error response that already contains its ID', async () => {
    api.upgradeCloudWorker.mockRejectedValueOnce(Object.assign(new Error('retry later'), {
      status: 503, data: { operation_id: 'op-a', status: 'reconciling' },
    }));
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('查看升配报价'); await click('确认并支付点数');
    expect(api.getCloudWorkerUpgradeOperation).toHaveBeenCalledWith('op-a', expect.any(Object));
    expect(container.textContent).toContain('正在核实机器配置');
  });
  test('allows a new quote after a definite rejection without an operation', async () => {
    api.upgradeCloudWorker.mockRejectedValueOnce(Object.assign(new Error('报价已过期'), {
      status: 409, data: { code: 'machine_upgrade_quote_conflict' },
    }));
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('查看升配报价'); await click('确认并支付点数');
    expect(container.textContent).toContain('报价已过期');
    expect(container.querySelector('select')).not.toBeNull();
    expect(JSON.parse(sessionStorage.getItem('catsco_machine_upgrade_v1:91:tenant-a'))).toBeNull();
  });
  test('restores an outstanding operation after reopening the panel', async () => {
    sessionStorage.setItem('catsco_machine_upgrade_v1:91:tenant-a', JSON.stringify({ operation_id: 'op-a' }));
    api.getCloudWorkerUpgradeOperation.mockResolvedValue({ operation_id: 'op-a', status: 'completed', to_plan: 'standard' });
    await render(<CloudWorkerUpgrade worker={worker} />);
    expect(api.getCloudWorkerUpgradeOperation).toHaveBeenCalledWith('op-a', expect.any(Object));
    expect(container.textContent).toContain('升配完成');
    expect(JSON.parse(sessionStorage.getItem('catsco_machine_upgrade_v1:91:tenant-a'))).toBeNull();
  });
  test('expired quotes cannot be submitted', async () => {
    api.getCloudWorkerUpgradeQuote.mockResolvedValue({ ...quote, expires_at: '2000-01-01T00:00:00Z' });
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('查看升配报价');
    expect([...container.querySelectorAll('button')].find(item => item.textContent.includes('报价已过期')).disabled).toBe(true);
    expect(api.upgradeCloudWorker).not.toHaveBeenCalled();
  });
});
