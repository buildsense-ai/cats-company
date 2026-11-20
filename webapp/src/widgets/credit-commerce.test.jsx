import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import CreditTopupForm, { parseTopupAmount } from './plans/credit-topup-form';
import CloudWorkerUpgrade from './cloud-worker-upgrade';
vi.mock('../api', () => ({ api: {
  getCloudWorkerUpgradeQuote: vi.fn(), upgradeCloudWorker: vi.fn(), getCloudWorkerUpgradeOperation: vi.fn(),
  getCloudWorkerUpgradeConfiguration: vi.fn(),
} }));
import { api } from '../api';

const config = { enabled: true, min_amount_fen: 100, max_amount_fen: 1000000, credits_per_cny: 100, presets_fen: [1000, 3000, 5000, 10000, 30000, 50000] };
const worker = { id: 91, tenant_name: 'tenant-a', display_name: '审查助手' };
const quote = { enabled: true, quote_id: 'quote-a', from_plan: 'light', to_plan: 'standard', cost_units: 2000000000,
  balance_units: 5000000000, units_per_credit: 1000000, expires_at: new Date(Date.now() + 300000).toISOString(),
  worker_expires_at: '2027-01-01T00:00:00Z', remaining_seconds: 15 * 86400 };

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
    api.getCloudWorkerUpgradeConfiguration.mockResolvedValue({ current_plan: 'light' });
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
  test('keeps billing and runtime tiers visible and waits for the user to open and request a quote', async () => {
    api.getCloudWorkerUpgradeQuote.mockResolvedValue({ ...quote, to_plan: 'pro' });
    await render(<CloudWorkerUpgrade worker={{ ...worker, runtime_plan: 'standard' }} />);
    expect(container.textContent).toContain('实际运行档位 标准');
    expect(container.textContent).toContain('升配计价基准 轻量');
    const toggle = container.querySelector('.cloud-upgrade-toggle');
    const body = document.getElementById(toggle.getAttribute('aria-controls'));
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(body.hidden).toBe(true);
    expect(container.querySelector('select')).toBeNull();
    await click('升配');
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(body.hidden).toBe(false);
    expect([...container.querySelectorAll('select option')].map(option => option.value)).toEqual(['pro', 'heavy']);
    expect(container.textContent).toContain('机器已运行在标准档，只能选择更高档。报价按已购权益折算，目前从轻量档补差。');
    expect(api.getCloudWorkerUpgradeQuote).not.toHaveBeenCalled();
    await click('查看升配报价');
    expect(api.getCloudWorkerUpgradeQuote).toHaveBeenCalledWith(worker.tenant_name, 'pro', expect.any(Object));
    await click('收起');
    expect(container.querySelector('.cloud-upgrade-quote')).toBeNull();
    await click('升配');
    expect(container.querySelector('.cloud-upgrade-quote')).not.toBeNull();
    expect(api.getCloudWorkerUpgradeQuote).toHaveBeenCalledTimes(1);
    expect(api.upgradeCloudWorker).not.toHaveBeenCalled();
  });

  test('offers all three higher targets to a machine running and billed at light', async () => {
    await render(<CloudWorkerUpgrade worker={{ ...worker, runtime_plan: 'light' }} />);
    await click('升配');
    expect([...container.querySelectorAll('select option')].map(option => option.value)).toEqual(['standard', 'pro', 'heavy']);
    expect(container.textContent).not.toContain('机器已运行在轻量档');
  });

  test('shows the current tier and expiry before asking for a quote', async () => {
    api.getCloudWorkerUpgradeConfiguration.mockResolvedValue({ current_plan: 'standard', upgrade_expires_at: quote.worker_expires_at });
    await render(<CloudWorkerUpgrade worker={worker} />);
    expect(container.textContent).toContain('升配计价基准 标准');
    expect(container.textContent).toContain('已购升配至');
    expect(api.getCloudWorkerUpgradeQuote).not.toHaveBeenCalled();
    await click('升配');
    expect(container.textContent).toContain('不自动续费或扣点');
    expect([...container.querySelectorAll('select option')].map(option => option.value)).toEqual(['pro', 'heavy']);
  });
  test('requires a quote and sufficient balance before spending points', async () => {
    api.getCloudWorkerUpgradeQuote.mockResolvedValue({ ...quote, balance_units: 1 });
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('升配');
    await click('查看升配报价');
    expect(container.textContent).toContain('还差');
    expect(container.textContent).toContain('一次性支付 2,000 点');
    expect(container.textContent).toContain('报价时剩余 15 天');
    expect(container.textContent).toContain('升配成功才扣点');
    expect([...container.querySelectorAll('button')].find(item => item.textContent === '确认并支付点数').disabled).toBe(true);
    expect(api.upgradeCloudWorker).not.toHaveBeenCalled();
  });
  test('keeps the same request on ambiguous timeout and resumes operation polling', async () => {
    api.upgradeCloudWorker.mockRejectedValueOnce(Object.assign(new Error('timeout'), { code: 'REQUEST_TIMEOUT' }))
      .mockResolvedValueOnce({ operation_id: 'op-a', status: 'reconciling' });
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('升配');
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
    await click('升配');
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
    await click('升配');
    await click('查看升配报价'); await click('确认并支付点数');
    expect(api.getCloudWorkerUpgradeOperation).toHaveBeenCalledWith('op-a', expect.any(Object));
    expect(container.textContent).toContain('正在核实机器配置');
  });
  test('allows a new quote after a definite rejection without an operation', async () => {
    api.upgradeCloudWorker.mockRejectedValueOnce(Object.assign(new Error('报价已过期'), {
      status: 409, data: { code: 'machine_upgrade_quote_conflict' },
    }));
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('升配');
    await click('查看升配报价'); await click('确认并支付点数');
    expect(container.textContent).toContain('报价已过期');
    expect(container.querySelector('select')).not.toBeNull();
    expect(JSON.parse(sessionStorage.getItem('catsco_machine_upgrade_v1:91:tenant-a'))).toBeNull();
  });
  test('restores an outstanding operation after reopening the panel', async () => {
    sessionStorage.setItem('catsco_machine_upgrade_v1:91:tenant-a', JSON.stringify({ operation_id: 'op-a' }));
    api.getCloudWorkerUpgradeOperation.mockResolvedValue({ operation_id: 'op-a', status: 'completed', to_plan: 'standard' });
    await render(<CloudWorkerUpgrade worker={worker} />);
    expect(container.querySelector('.cloud-upgrade-toggle').getAttribute('aria-expanded')).toBe('true');
    expect(api.getCloudWorkerUpgradeOperation).toHaveBeenCalledWith('op-a', expect.any(Object));
    expect(container.textContent).toContain('升配完成');
    expect(JSON.parse(sessionStorage.getItem('catsco_machine_upgrade_v1:91:tenant-a'))).toBeNull();
    expect([...container.querySelectorAll('select option')].map(option => option.value)).toEqual(['pro', 'heavy']);
    api.getCloudWorkerUpgradeQuote.mockResolvedValue({ ...quote, from_plan: 'standard', to_plan: 'pro' });
    await click('查看升配报价');
    expect(api.getCloudWorkerUpgradeQuote).toHaveBeenCalledWith(worker.tenant_name, 'pro', expect.any(Object));
    expect(container.textContent).toContain('升配完成');
    expect(container.textContent).toContain('期间云员工会短暂离线');
    expect(api.upgradeCloudWorker).not.toHaveBeenCalled();
  });
  test('expired quotes cannot be submitted', async () => {
    api.getCloudWorkerUpgradeQuote.mockResolvedValue({ ...quote, expires_at: '2000-01-01T00:00:00Z' });
    await render(<CloudWorkerUpgrade worker={worker} />);
    await click('升配');
    await click('查看升配报价');
    expect([...container.querySelectorAll('button')].find(item => item.textContent.includes('报价已过期')).disabled).toBe(true);
    expect(api.upgradeCloudWorker).not.toHaveBeenCalled();
  });
});
