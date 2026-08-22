import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

vi.mock('../api', () => ({
  api: {
    getRelayConfig: vi.fn(),
    getRelayKey: vi.fn(),
    getRelayCommercial: vi.fn(),
    getCommercialCatalog: vi.fn(),
    getCommercialOrders: vi.fn(),
    createCommercialOrder: vi.fn(),
    cancelCommercialOrder: vi.fn(),
    confirmCommercialTestPayment: vi.fn(),
    claimCommercialTrial: vi.fn(),
    getRelayUsage: vi.fn(),
    createRelaySession: vi.fn(),
    createRelayKey: vi.fn(),
    rotateRelayKey: vi.fn(),
    revealRelayKey: vi.fn(),
    revokeRelayKey: vi.fn(),
    redeemRelayInvite: vi.fn(),
  },
}));

import RelayAccessModal from './relay-access-modal';
import { api } from '../api';

describe('RelayAccessModal commercial rollout', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    window.sessionStorage.clear();
    api.getRelayConfig.mockResolvedValue({
      base_url: 'https://relay.catsco.cc',
      default_model: 'MiniMax-M3',
      self_service_enabled: false,
      endpoints: [
        { protocol: 'Anthropic-compatible', base_url: 'https://relay.catsco.cc/anthropic' },
        { protocol: 'OpenAI-compatible', base_url: 'https://relay.catsco.cc/v1' },
      ],
    });
    api.getRelayKey.mockResolvedValue({ key: null });
    api.getRelayUsage.mockResolvedValue({
      configured: true,
      summary: {
        source: 'relay',
        model: 'MiniMax-M3',
        quota_configured: true,
        percent: 25,
        remaining_percent: 75,
        status: 'normal',
        reset_duration: '1M',
        last_reset: '2026-06-08T03:29:30Z',
      },
    });
    api.getRelayCommercial.mockResolvedValue({
      enabled: false,
      note: '套餐和邀请码仍在内部测试。',
      summary: {
        uid: 38,
        models: ['MiniMax-M3', 'deepseek-v4-flash', 'gpt-5.6-luna'],
        entitlements: [],
        plans: [],
      },
    });
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    window.confirm = vi.fn(() => true);
    window.open = vi.fn(() => null);
    api.getCommercialCatalog.mockResolvedValue({ enabled: false, plans: [], channels: [], trial_available: false });
    api.getCommercialOrders.mockResolvedValue({ orders: [] });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    vi.clearAllMocks();
  });

  async function renderModal() {
    await act(async () => {
      root.render(<RelayAccessModal onClose={vi.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  function findButton(label) {
    return [...container.querySelectorAll('button')].find(button => button.textContent.includes(label));
  }

  async function clickButton(label) {
    const button = findButton(label);
    expect(button).not.toBeUndefined();
    await act(async () => {
      button.click();
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it('creates, displays, and copies a relay key', async () => {
    api.getRelayConfig.mockResolvedValue({
      base_url: 'https://relay.catsco.cc',
      default_model: 'MiniMax-M3',
      self_service_enabled: true,
      endpoints: [],
    });
    api.createRelayKey.mockResolvedValue({
      key: { id: 'key-1', name: 'CatsCo relay key', state: 'active', hint: '...1111' },
      plain_key: 'sk-bf-created-secret',
    });

    await renderModal();
    await clickButton('生成我的 Key');

    expect(api.createRelayKey).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('CatsCo API Key');
    expect(container.textContent).not.toContain('CatsCo relay key');
    expect(container.textContent).toContain('sk-bf-created-secret');
    await clickButton('复制 Key');
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('sk-bf-created-secret');
  });

  it('reveals, rotates, and revokes an existing relay key', async () => {
    api.getRelayConfig.mockResolvedValue({
      base_url: 'https://relay.catsco.cc',
      default_model: 'MiniMax-M3',
      self_service_enabled: true,
      endpoints: [],
    });
    api.getRelayKey.mockResolvedValue({
      key: { id: 'key-1', name: 'CatsCo relay key', state: 'active', hint: '...1111' },
    });
    api.revealRelayKey.mockResolvedValue({
      key: { id: 'key-1', name: 'CatsCo relay key', state: 'active', hint: '...1111' },
      plain_key: 'sk-bf-revealed-secret',
    });
    api.rotateRelayKey.mockResolvedValue({
      key: { id: 'key-2', name: 'CatsCo relay key', state: 'active', hint: '...2222' },
      plain_key: 'sk-bf-rotated-secret',
    });
    api.revokeRelayKey.mockResolvedValue({});

    await renderModal();
    await clickButton('显示并复制');
    expect(api.revealRelayKey).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('sk-bf-revealed-secret');
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('sk-bf-revealed-secret');

    await clickButton('重新生成');
    expect(window.confirm).toHaveBeenCalledTimes(1);
    expect(api.rotateRelayKey).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('sk-bf-rotated-secret');

    await clickButton('撤销');
    expect(window.confirm).toHaveBeenCalledTimes(2);
    expect(api.revokeRelayKey).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('还没有 API Key');
    expect(container.textContent).not.toContain('sk-bf-rotated-secret');
  });

  it('keeps invite redemption hidden while commercial rollout is disabled', async () => {
    await renderModal();

    expect(container.textContent).toContain('套餐与账单');
    expect(container.textContent).toContain('未开放');
    expect(container.textContent).toContain('当前额度和 API Key 不受影响');
    expect(container.textContent).toContain('套餐和邀请码仍在内部测试');
    expect(container.querySelector('.relay-access-invite-form')).toBeNull();
  });

  it('shows invite redemption and shared-pool benefits without exposing model entitlements', async () => {
    api.getRelayUsage.mockImplementation(({ model } = {}) => Promise.resolve({
      configured: true,
      summary: {
        source: 'relay',
        model: model || 'MiniMax-M3',
        quota_configured: true,
        percent: model === 'deepseek-v4-flash' ? 12.5 : 25,
        remaining_percent: model === 'deepseek-v4-flash' ? 87.5 : 75,
        status: 'normal',
        reset_duration: '1M',
        last_reset: '2026-06-08T03:29:30Z',
      },
    }));
    api.getRelayCommercial.mockResolvedValue({
      enabled: true,
      summary: {
        uid: 38,
        models: ['MiniMax-M3', 'deepseek-v4-flash', 'gpt-5.6-luna'],
        entitlements: [
          {
            state: 'active',
            plan_name: '教师试用包',
            source: 'invite',
            starts_at: '2026-06-29T00:00:00Z',
            expires_at: '2026-07-29T00:00:00Z',
          },
          { state: 'expired', plan_name: '旧套餐' },
        ],
      },
    });

    await renderModal();

    expect(container.textContent).toContain('内测开放');
    expect(container.textContent).toContain('本周期总用量');
    expect(container.textContent).toContain('购买记录不会自动改变已有模型额度');
    expect(container.textContent).toContain('当前有效套餐');
    expect(container.textContent).toContain('套餐最近到期');
    expect(container.textContent).toContain('每月重置');
    expect(container.textContent).toContain('下次');
    expect(container.textContent).toContain('不是自然月');
    expect(container.textContent).toContain('当前权益');
    expect(container.textContent).toContain('1 个共享额度池 · 按套餐权益统一扣减');
    expect(container.textContent).not.toContain('2 个模型额度可用');
    expect(container.textContent).not.toContain('gpt-5.6-luna');
    expect(container.textContent).toContain('教师试用包');
    expect(container.textContent).toContain('邀请码兑换');
    expect(container.textContent).toContain('MiniMax-M3');
    expect(container.textContent).not.toContain('deepseek-v4-flash');
    expect(container.textContent).toContain('剩余 75%');
    expect(api.getRelayUsage).toHaveBeenCalledWith({ scope: 'total' });
    expect(api.getRelayUsage.mock.calls.every(([options]) => options?.scope === 'total' && !options?.model)).toBe(true);
    expect(container.textContent).not.toContain('CNY');
    expect(container.textContent).not.toContain('¥');
    expect(container.textContent).not.toContain('￥');
    expect(container.textContent).not.toContain('禁用套餐');
    expect(container.querySelector('.relay-access-invite-form')).not.toBeNull();
  });

  it('turns an invite into its bound package instead of a separate invite product', async () => {
    api.getRelayCommercial.mockResolvedValue({
      enabled: true,
      summary: { uid: 38, models: [], entitlements: [], plans: [] },
    });
    api.redeemRelayInvite.mockResolvedValue({
      summary: {
        uid: 38,
        models: ['gpt-5.6-terra', 'gpt-5.6-sol'],
        entitlements: [{
          id: 12,
          plan_id: 2,
          plan_name: '个人版',
          source: 'invite',
          state: 'active',
          starts_at: '2026-08-12T00:00:00Z',
          expires_at: '2026-09-11T00:00:00Z',
        }],
      },
    });

    await renderModal();
    const input = container.querySelector('input[placeholder="输入邀请码"]');
    expect(input).not.toBeNull();
    await act(async () => {
      const valueSetter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set;
      valueSetter.call(input, 'PERSONAL-2026');
      input.dispatchEvent(new Event('input', { bubbles: true }));
      await Promise.resolve();
    });
    await clickButton('兑换');

    expect(api.redeemRelayInvite).toHaveBeenCalledWith('PERSONAL-2026');
    expect(container.textContent).toContain('个人版');
    expect(container.textContent).toContain('邀请码兑换');
    expect(container.textContent).not.toContain('邀请码套餐');
  });

  it('labels enforced commercial usage as one shared package pool', async () => {
    api.getRelayUsage.mockResolvedValue({
      configured: true,
      summary: {
        source: 'relay', model: '套餐总额度', quota_configured: true,
        percent: 16.5, remaining_percent: 83.5, status: 'normal', reset_duration: '1M',
      },
    });
    api.getRelayCommercial.mockResolvedValue({
      enabled: true,
      enforce_enabled: true,
      summary: {
        uid: 38,
        models: ['MiniMax-M2.7', 'MiniMax-M3', 'deepseek-v4-flash', 'gpt-5.6-terra', 'gpt-5.6-sol'],
        entitlements: [{ state: 'active', plan_name: '专业版', expires_at: '2026-09-13T07:32:08Z' }],
      },
    });

    await renderModal();

    expect(api.getRelayUsage).toHaveBeenCalledWith({ scope: 'total' });
    expect(container.textContent).toContain('共享额度池');
    expect(container.textContent).toContain('套餐总额度');
    expect(container.textContent).toContain('本周期总用量');
    expect(container.textContent).toContain('套餐内模型共用同一额度池');
  });

  it('shows explicit no-package state for enabled users without active entitlements', async () => {
    api.getRelayCommercial.mockResolvedValue({
      enabled: true,
      summary: {
        uid: 38,
        models: [],
        entitlements: [],
        plans: [],
      },
    });

    await renderModal();

    expect(container.textContent).toContain('无套餐');
    expect(container.textContent).toContain('当前没有有效套餐');
  });

  it('keeps the package total independent from a custom startup model', async () => {
    api.getRelayUsage.mockResolvedValue({
      configured: true,
      summary: {
        source: 'relay',
        model: '套餐总额度',
        quota_configured: true,
        percent: 30,
        remaining_percent: 70,
        status: 'normal',
      },
    });

    await renderModal();

    expect(container.textContent).toContain('套餐总额度');
    expect(container.textContent).toContain('剩余 70%');
  });

  it('shows explicit over-limit warning for the total package quota', async () => {
    api.getRelayUsage.mockResolvedValue({
      configured: true,
      summary: {
        source: 'relay',
        model: 'MiniMax-M3',
        quota_configured: true,
        percent: 149.1,
        remaining_percent: 0,
        status: 'over_limit',
        reset_duration: '1M',
        last_reset: '2026-06-08T03:29:30Z',
      },
    });

    await renderModal();

    expect(container.textContent).toContain('套餐额度已超额');
    expect(container.textContent).toContain('剩余 0%');
    expect(container.textContent).toContain('已用 100%+');
    expect(container.textContent).not.toContain('CNY');
    expect(container.textContent).toContain('请等待额度重置或联系管理员');
  });

  it('does not present zero relay limit as a real remaining quota', async () => {
    api.getRelayUsage.mockResolvedValue({
      configured: true,
      summary: {
        source: 'relay',
        model: 'MiniMax-M3',
        quota_configured: false,
        percent: 0,
        remaining_percent: 0,
        status: 'normal',
      },
    });

    await renderModal();

    expect(container.textContent).toContain('总额度待同步');
    expect(container.textContent).toContain('等待套餐额度同步');
  });

  it('shows gray purchase plans and the configured payment channel', async () => {
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: true,
      trial_available: true,
      channels: [{ id: 'test', label: '灰度测试支付', test_mode: true }],
      plans: [{
        id: 9,
        slug: 'gray-plan',
        name: '灰度标准包',
        description: '用于内部支付冒烟',
        price_fen: 2990,
        currency: 'CNY',
        sale_state: 'test',
        duration_days: 30,
        model_budgets: { 'MiniMax-M3': 500 },
      }],
    });

    await renderModal();

    expect(container.textContent).toContain('选一档，开始你的协作节奏');
    expect(container.textContent).toContain('灰度标准包');
    expect(container.textContent).toContain('¥29.9');
    expect(container.textContent).toContain('灰度测试支付');
    expect(container.textContent).toContain('领取体验包');
  });

  it('renders the current Free, Personal and Pro catalog without exposing internal quota values', async () => {
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: true,
      trial_available: false,
      channels: [],
      plans: [
        {
          id: 21,
          slug: 'catsco-personal',
          name: '个人版',
          description: '适合将 XiaoBa 作为日常个人助手。',
          price_fen: 39900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M2.7': 1750, 'MiniMax-M3': 1750, 'deepseek-v4-flash': 1750, 'gpt-5.6-terra': 1750, 'gpt-5.6-sol': 1750, 'gpt-5.6-luna': 1750 },
        },
        {
          id: 22,
          slug: 'catsco-pro',
          name: '专业版',
          description: '适合高频、多任务并行或复杂工作。',
          price_fen: 79900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M2.7': 5250, 'MiniMax-M3': 5250, 'deepseek-v4-flash': 5250, 'gpt-5.6-terra': 5250, 'gpt-5.6-sol': 5250, 'gpt-5.6-luna': 5250 },
        },
      ],
    });

    await renderModal();

    expect(container.textContent).toContain('选择适合你的工作强度');
    expect(container.textContent).toContain('免费版');
    expect(container.textContent).toContain('个人版');
    expect(container.textContent).toContain('专业版');
    expect(container.textContent).toContain('¥0');
    expect(container.textContent).toContain('¥399');
    expect(container.textContent).toContain('¥799');
    expect(container.textContent).toContain('约为个人版 3 倍的任务容量');
    expect(container.textContent).toContain('个人版用量 · 30 天有效');
    expect(container.textContent).toContain('专业版用量 · 30 天有效');
    expect(container.textContent).toContain('支付通道暂未开放');
    expect(container.textContent).not.toContain('200000000');
    expect(container.textContent).not.toContain('10500');
    expect(container.textContent).not.toContain('31500');
    expect(container.textContent).not.toContain('gpt-5.6-luna');
    expect(container.querySelector('.relay-access-plan-row.recommended')?.textContent).toContain('专业版');
    expect(container.querySelectorAll('.relay-access-plan-row')).toHaveLength(3);
    expect(Array.from(container.querySelectorAll('.relay-access-plan-row button')).every((button) => button.disabled)).toBe(true);
  });

  it('keeps an allowlisted gray plan visible alongside the official catalog', async () => {
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: false,
      trial_available: false,
      channels: [{ id: 'alipay_page', label: '支付宝支付', test_mode: false }],
      plans: [
        {
          id: 21,
          slug: 'catsco-personal',
          name: '个人版',
          price_fen: 39900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M2.7': 1750 },
        },
        {
          id: 22,
          slug: 'catsco-pro',
          name: '专业版',
          price_fen: 79900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M2.7': 5250 },
        },
        {
          id: 184,
          slug: 'uid38-internal-5cny-20260822',
          name: 'UID38 内测 ¥5',
          description: '仅供 UID 38 验证支付链路。',
          price_fen: 500,
          sale_state: 'test',
          duration_days: 1,
          model_budgets: { 'MiniMax-M2.7': 0.05 },
        },
      ],
    });

    await renderModal();

    expect(container.textContent).toContain('UID38 内测 ¥5');
    expect(container.textContent).toContain('内测套餐');
    expect(container.textContent).toContain('内测套餐按卡片标注的有效期执行');
    expect(container.textContent).toContain('¥5');
    expect(container.querySelectorAll('.relay-access-plan-row')).toHaveLength(4);
  });

  it('marks an active Pro plan and blocks both repeat and lower-tier purchases', async () => {
    const plans = [
      { id: 21, slug: 'catsco-personal', name: '个人版', price_fen: 39900, duration_days: 30, model_budgets: { 'gpt-5.6-terra': 100 } },
      { id: 22, slug: 'catsco-pro', name: '专业版', price_fen: 79900, duration_days: 30, model_budgets: { 'gpt-5.6-terra': 300 } },
    ];
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      channels: [{ id: 'alipay_page', label: '支付宝支付' }],
      plans,
    });
    api.getRelayCommercial.mockResolvedValue({
      enabled: true,
      summary: {
        uid: 38,
        models: ['gpt-5.6-terra'],
        entitlements: [{
          plan_id: 22, plan_name: '专业版', source: 'invite', state: 'active',
          starts_at: '2026-08-14T00:00:00Z', expires_at: '2026-09-13T00:00:00Z',
        }],
      },
    });

    await renderModal();

    const rows = Array.from(container.querySelectorAll('.relay-access-plan-row'));
    const personalButton = rows.find(row => row.textContent.includes('个人版'))?.querySelector('button');
    const proButton = rows.find(row => row.textContent.includes('专业版'))?.querySelector('button');
    expect(personalButton?.textContent).toContain('已包含');
    expect(proButton?.textContent).toContain('当前套餐');
    expect(personalButton?.disabled).toBe(true);
    expect(proButton?.disabled).toBe(true);
    expect(api.createCommercialOrder).not.toHaveBeenCalled();
  });

  it('offers Personal users an immediate Pro upgrade with concise reset copy', async () => {
    const plans = [
      { id: 21, slug: 'catsco-personal', name: '个人版', price_fen: 39900, duration_days: 30, model_budgets: { 'gpt-5.6-terra': 100 } },
      { id: 22, slug: 'catsco-pro', name: '专业版', price_fen: 79900, duration_days: 30, model_budgets: { 'gpt-5.6-terra': 300 } },
    ];
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      channels: [{ id: 'alipay_page', label: '支付宝支付' }],
      plans,
    });
    api.getRelayCommercial.mockResolvedValue({
      enabled: true,
      summary: {
        uid: 38,
        models: ['gpt-5.6-terra'],
        entitlements: [{
          plan_id: 21, plan_slug: 'catsco-personal', plan_name: '个人版', source: 'order', state: 'active',
          starts_at: '2026-08-14T00:00:00Z', expires_at: '2026-09-13T00:00:00Z',
        }],
      },
    });

    await renderModal();

    expect(container.textContent).toContain('升级后立即切换套餐，额度按专业版重置，不与个人版叠加。');
    const rows = Array.from(container.querySelectorAll('.relay-access-plan-row'));
    const personalButton = rows.find(row => row.textContent.includes('个人版'))?.querySelector('button');
    const proButton = rows.find(row => row.textContent.includes('专业版'))?.querySelector('button');
    expect(personalButton?.textContent).toContain('当前套餐');
    expect(personalButton?.disabled).toBe(true);
    expect(proButton?.textContent).toContain('升级至专业版');
    expect(proButton?.disabled).toBe(false);

    await clickButton('升级至专业版');
    expect(container.querySelector('.relay-access-purchase-confirm')?.textContent).toContain('升级后立即生效，额度按新套餐重置，不叠加。');
    expect(api.createCommercialOrder).not.toHaveBeenCalled();
  });

  it('shows a complete, filterable order history without hiding older orders', async () => {
    const orders = [
      { order_no: 'CCORDER01', plan_name: '待支付套餐', amount_fen: 9900, status: 'pending', created_at: '2026-08-10T01:00:00Z' },
      { order_no: 'CCORDER02', plan_name: '已生效套餐 2', amount_fen: 4900, status: 'fulfilled', created_at: '2026-08-09T01:00:00Z' },
      { order_no: 'CCORDER03', plan_name: '已生效套餐 3', amount_fen: 4900, status: 'fulfilled', created_at: '2026-08-08T01:00:00Z' },
      { order_no: 'CCORDER04', plan_name: '已生效套餐 4', amount_fen: 4900, status: 'fulfilled', created_at: '2026-08-07T01:00:00Z' },
      { order_no: 'CCORDER05', plan_name: '已生效套餐 5', amount_fen: 4900, status: 'fulfilled', created_at: '2026-08-06T01:00:00Z' },
      { order_no: 'CCORDER06', plan_name: '已生效套餐 6', amount_fen: 4900, status: 'fulfilled', created_at: '2026-08-05T01:00:00Z' },
      { order_no: 'CCORDER07', plan_name: '已生效套餐 7', amount_fen: 4900, status: 'fulfilled', created_at: '2026-08-04T01:00:00Z' },
      { order_no: 'CCORDER08', plan_name: '已生效套餐 8', amount_fen: 4900, status: 'fulfilled', created_at: '2026-08-03T01:00:00Z' },
      { order_no: 'CCORDER09', plan_name: '已退款套餐', amount_fen: 4900, status: 'refunded', created_at: '2026-08-02T01:00:00Z' },
      { order_no: 'CCORDER10', plan_name: '已关闭套餐', amount_fen: 4900, status: 'closed', created_at: '2026-08-01T01:00:00Z' },
    ];
    api.getCommercialOrders.mockResolvedValue({ orders });

    await renderModal();
    await clickButton('订单记录');

    expect(container.querySelectorAll('.relay-access-order-list > button')).toHaveLength(8);
    expect(container.textContent).toContain('查看其余 2 条');
    await clickButton('查看其余 2 条');
    expect(container.querySelectorAll('.relay-access-order-list > button')).toHaveLength(10);

    await clickButton('待处理');
    const pendingRows = container.querySelectorAll('.relay-access-order-list > button');
    expect(pendingRows).toHaveLength(1);
    expect(pendingRows[0].textContent).toContain('待支付套餐');

    await clickButton('退款 / 关闭');
    expect(container.querySelectorAll('.relay-access-order-list > button')).toHaveLength(2);
    expect(container.textContent).toContain('已退款套餐');
    expect(container.textContent).toContain('已关闭套餐');
  });

  it('creates and confirms a gray payment from the user purchase flow', async () => {
    const plan = {
      id: 9,
      slug: 'gray-plan',
      name: '灰度标准包',
      price_fen: 2990,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
      model_budgets: { 'MiniMax-M3': 500 },
    };
    const pendingOrder = {
      order_no: 'CCWEBTEST0001',
      plan_name: plan.name,
      amount_fen: plan.price_fen,
      currency: 'CNY',
      channel: 'test',
      status: 'pending',
      created_at: '2026-07-14T06:00:00Z',
    };
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: true,
      trial_available: false,
      channels: [{ id: 'test', label: '灰度测试支付', test_mode: true }],
      plans: [plan],
    });
    api.createCommercialOrder.mockResolvedValue({ order: pendingOrder });
    api.confirmCommercialTestPayment.mockResolvedValue({
      ok: true,
      order: { ...pendingOrder, status: 'fulfilled' },
      summary: { uid: 38, total_cny: 500, totals_by_model: { 'MiniMax-M3': 500 } },
    });

    await renderModal();
    const purchaseButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent.includes('购买'));
    await act(async () => {
      purchaseButton.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createCommercialOrder).not.toHaveBeenCalled();
    expect(container.textContent).toContain('确认购买');
    await clickButton('确认购买');

    expect(api.createCommercialOrder).toHaveBeenCalledWith(9, 'test', expect.stringMatching(/^order_/), { timeoutMs: 40_000 });
    expect(container.textContent).toContain('待支付');
    const confirmButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent.includes('完成灰度测试支付'));
    const usageCallsBeforeConfirm = api.getRelayUsage.mock.calls.length;
    await act(async () => {
      confirmButton.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.confirmCommercialTestPayment).toHaveBeenCalledWith('CCWEBTEST0001');
    expect(api.getRelayUsage.mock.calls.length).toBeGreaterThan(usageCallsBeforeConfirm);
    expect(container.textContent).toContain('支付成功');
    expect(container.textContent).toContain('套餐已生效');
  });

  it('renders an Alipay page checkout without exposing the test confirmation action', async () => {
    const plan = {
      id: 10,
      slug: 'alipay-plan',
      name: '支付宝灰度包',
      price_fen: 990,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
      model_budgets: { 'MiniMax-M3': 100 },
    };
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: false,
      trial_available: false,
      channels: [{ id: 'alipay_page', label: '支付宝支付', test_mode: false }],
      plans: [plan],
    });
    api.createCommercialOrder.mockResolvedValue({
      order: {
        order_no: 'CCALIPAYWEB0001',
        plan_name: plan.name,
        amount_fen: plan.price_fen,
        currency: 'CNY',
        channel: 'alipay_page',
        status: 'pending',
        checkout_url: 'https://openapi.alipay.test/gateway.do',
        created_at: '2026-07-14T06:00:00Z',
      },
    });
    const paymentWindow = {
      opener: window,
      location: { replace: vi.fn() },
      document: { title: '', body: { textContent: '' } },
      close: vi.fn(),
    };
    window.open.mockReturnValue(paymentWindow);

    await renderModal();
    const purchaseButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent.includes('购买'));
    await act(async () => {
      purchaseButton.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createCommercialOrder).not.toHaveBeenCalled();
    await clickButton('确认并前往支付宝');

    expect(api.createCommercialOrder).toHaveBeenCalledWith(10, 'alipay_page', expect.stringMatching(/^order_/), { timeoutMs: 40_000 });
    expect(window.open).toHaveBeenCalledWith('about:blank', '_blank');
    expect(paymentWindow.location.replace).toHaveBeenCalledWith('https://openapi.alipay.test/gateway.do');
    expect(container.textContent).toContain('支付宝支付 ¥9.9');
    const paymentLink = container.querySelector('.relay-access-payment-redirect a');
    expect(paymentLink?.getAttribute('href')).toBe('https://openapi.alipay.test/gateway.do');
    expect(paymentLink?.getAttribute('target')).toBe('_blank');
    expect(paymentLink?.getAttribute('rel')).toContain('noopener');
    expect(container.textContent).not.toContain('完成灰度测试支付');
    expect(container.textContent).toContain('订单金额');
    expect(container.textContent).toContain('支付方式');
    expect(container.textContent).toContain('创建时间');
    const copyOrderButton = container.querySelector('button[aria-label="复制订单号"]');
    await act(async () => {
      copyOrderButton.click();
      await Promise.resolve();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('CCALIPAYWEB0001');
  });

  it('keeps a usable payment link when the browser blocks the Alipay popup', async () => {
    const plan = {
      id: 18,
      slug: 'popup-blocked-plan',
      name: '弹窗拦截测试包',
      price_fen: 100,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
    };
    const checkoutURL = 'https://openapi.alipay.test/popup-blocked';
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: false,
      channels: [{ id: 'alipay_page', label: '支付宝支付', test_mode: false }],
      plans: [plan],
    });
    api.createCommercialOrder.mockResolvedValue({
      order: {
        order_no: 'CCPOPUPBLOCKED0001',
        plan_id: plan.id,
        plan_name: plan.name,
        amount_fen: plan.price_fen,
        currency: 'CNY',
        channel: 'alipay_page',
        status: 'pending',
        checkout_url: checkoutURL,
        created_at: new Date().toISOString(),
      },
    });
    window.open.mockReturnValue(null);

    await renderModal();
    await clickButton('购买');
    await clickButton('确认并前往支付宝');

    expect(api.createCommercialOrder).toHaveBeenCalledTimes(1);
    expect(container.textContent).toContain('浏览器拦截了新窗口');
    const paymentLink = container.querySelector('.relay-access-payment-redirect a');
    expect(paymentLink?.getAttribute('href')).toBe(checkoutURL);
    expect(paymentLink?.textContent).toContain('前往支付宝付款');
  });

  it('suppresses rapid duplicate purchase confirmation clicks', async () => {
    const plan = {
      id: 19,
      slug: 'rapid-click-plan',
      name: '连续点击测试包',
      price_fen: 100,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
    };
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: true,
      channels: [{ id: 'test', label: '灰度测试支付', test_mode: true }],
      plans: [plan],
    });
    let resolveCreate;
    api.createCommercialOrder.mockReturnValue(new Promise((resolve) => {
      resolveCreate = resolve;
    }));

    await renderModal();
    await clickButton('购买');
    const confirmButton = findButton('确认购买');
    await act(async () => {
      confirmButton.click();
      confirmButton.click();
      await Promise.resolve();
    });

    expect(api.createCommercialOrder).toHaveBeenCalledTimes(1);
    await act(async () => {
      resolveCreate({
        order: {
          order_no: 'CCRAPIDCLICK0001', plan_id: plan.id, plan_name: plan.name,
          amount_fen: plan.price_fen, currency: 'CNY', channel: 'test', status: 'pending',
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });
  });

  it('keeps the Alipay label on a pending order after the channel is disabled', async () => {
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: false,
      trial_available: false,
      channels: [],
      plans: [],
    });
    api.getCommercialOrders.mockResolvedValue({
      orders: [{
        order_no: 'CCALIPAYWEB0002',
        plan_name: '历史支付宝订单',
        amount_fen: 990,
        currency: 'CNY',
        channel: 'alipay_page',
        status: 'pending',
        checkout_url: 'https://openapi.alipay.test/gateway.do',
        created_at: '2026-07-14T06:00:00Z',
      }],
    });

    await renderModal();
    expect(container.textContent).toContain('支付宝支付 ¥9.9');
  });

  it('reuses the client request id across modal remount after an uncertain server failure', async () => {
    const plan = {
      id: 11,
      slug: 'network-retry-plan',
      name: '网络重试包',
      price_fen: 990,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
    };
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: true,
      channels: [{ id: 'test', label: '灰度测试支付', test_mode: true }],
      plans: [plan],
    });
    const networkError = new Error('服务暂时不可用');
    networkError.status = 500;
    api.createCommercialOrder
      .mockRejectedValueOnce(networkError)
      .mockResolvedValueOnce({
        order: {
          order_no: 'CCRETRY0001',
          plan_name: plan.name,
          amount_fen: plan.price_fen,
          channel: 'test',
          status: 'pending',
        },
      });

    await renderModal();
    await clickButton('购买');
    await clickButton('确认购买');
    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await renderModal();
    await clickButton('购买');
    await clickButton('确认购买');

    expect(api.createCommercialOrder).toHaveBeenCalledTimes(2);
    expect(api.createCommercialOrder.mock.calls[0][2]).toBe(api.createCommercialOrder.mock.calls[1][2]);
    expect(api.createCommercialOrder.mock.calls[0][3]).toEqual({ timeoutMs: 40_000 });
  });

  it('keeps the client request id after a successful pending response when order reload fails', async () => {
    const plan = {
      id: 14,
      slug: 'reload-failure-plan',
      name: '重载保护包',
      price_fen: 990,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
    };
    const pending = {
      order_no: 'CCRELOADSAFE0001',
      plan_id: plan.id,
      plan_name: plan.name,
      amount_fen: plan.price_fen,
      channel: 'test',
      status: 'pending',
    };
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: true,
      channels: [{ id: 'test', label: '灰度测试支付', test_mode: true }],
      plans: [plan],
    });
    api.createCommercialOrder.mockResolvedValue({ order: pending });

    await renderModal();
    await clickButton('购买');
    await clickButton('确认购买');
    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    api.getCommercialOrders.mockRejectedValue(new Error('temporary order list failure'));
    await renderModal();
    await clickButton('购买');
    await clickButton('确认购买');

    expect(api.createCommercialOrder).toHaveBeenCalledTimes(2);
    expect(api.createCommercialOrder.mock.calls[0][2]).toBe(api.createCommercialOrder.mock.calls[1][2]);
    expect(container.textContent).toContain('CCRELOADSAFE0001');
  });

  it('opens an existing pending order instead of creating another payable order', async () => {
    const plan = {
      id: 12,
      slug: 'resume-plan',
      name: '恢复订单包',
      price_fen: 1990,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
    };
    const pending = {
      order_no: 'CCRESUME0001',
      plan_id: plan.id,
      plan_name: plan.name,
      amount_fen: plan.price_fen,
      channel: 'alipay_page',
      status: 'pending',
      checkout_url: '',
    };
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: false,
      channels: [{ id: 'alipay_page', label: '支付宝支付', test_mode: false }],
      plans: [plan],
    });
    api.getCommercialOrders.mockResolvedValue({ orders: [pending] });

    await renderModal();
    expect(container.textContent).toContain('正在恢复支付宝收银台链接');
    await clickButton('继续支付');

    expect(api.createCommercialOrder).not.toHaveBeenCalled();
    expect(container.textContent).toContain('CCRESUME0001');
  });

  it('shows the payment countdown and lets the user cancel an unpaid order', async () => {
    const plan = {
      id: 16,
      slug: 'cancel-plan',
      name: '可取消套餐',
      price_fen: 39900,
      currency: 'CNY',
      sale_state: 'test',
      duration_days: 30,
    };
    const pending = {
      order_no: 'CCCANCELWEB0001',
      plan_id: plan.id,
      plan_name: plan.name,
      amount_fen: plan.price_fen,
      currency: 'CNY',
      channel: 'alipay_page',
      status: 'pending',
      checkout_url: 'https://openapi.alipay.test/cancel-me',
      expires_at: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
      created_at: new Date().toISOString(),
    };
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: false,
      channels: [{ id: 'alipay_page', label: '支付宝支付', test_mode: false }],
      plans: [plan],
    });
    api.getCommercialOrders.mockResolvedValue({ orders: [pending] });
    api.cancelCommercialOrder.mockResolvedValue({
      ok: true,
      order: { ...pending, status: 'closed', checkout_url: '', closed_at: new Date().toISOString() },
    });

    await renderModal();
    await clickButton('继续支付');

    expect(container.textContent).toMatch(/剩余 [45]:\d{2}/);
    expect(container.textContent).toContain('支付剩余时间');
    await clickButton('取消订单');

    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining('取消这笔订单'));
    expect(api.cancelCommercialOrder).toHaveBeenCalledWith('CCCANCELWEB0001', { timeoutMs: 25_000 });
    expect(container.textContent).toContain('已关闭');
    expect(container.textContent).not.toContain('支付剩余时间');
  });

  it('actively recovers a recently closed Alipay order when the user opens it', async () => {
    const closed = {
      order_no: 'CCCLOSEDWEB0001',
      plan_id: 13,
      plan_name: '关闭订单恢复包',
      amount_fen: 990,
      channel: 'alipay_page',
      status: 'closed',
      closed_at: new Date().toISOString(),
      created_at: new Date().toISOString(),
    };
    api.getCommercialOrders.mockImplementation((orderNo) => Promise.resolve(
      orderNo
        ? { order: { ...closed, status: 'fulfilled', paid_at: new Date().toISOString() } }
        : { orders: [closed] },
    ));

    await renderModal();
    await clickButton('订单记录');
    await clickButton('关闭订单恢复包');
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getCommercialOrders).toHaveBeenCalledWith('CCCLOSEDWEB0001', {
      signal: expect.any(AbortSignal),
      timeoutMs: 20_000,
    });
    expect(container.textContent).toContain('已生效');
  });

  it('retries a recently closed Alipay order instead of stopping after one unchanged query', async () => {
    vi.useFakeTimers();
    try {
      const closed = {
        order_no: 'CCCLOSEDRETRY0001',
        plan_id: 15,
        plan_name: '关闭订单重试包',
        amount_fen: 990,
        channel: 'alipay_page',
        status: 'closed',
        closed_at: new Date().toISOString(),
        created_at: new Date().toISOString(),
      };
      api.getCommercialOrders.mockImplementation((orderNo) => Promise.resolve(
        orderNo ? { order: closed } : { orders: [closed] },
      ));

      await renderModal();
      await clickButton('订单记录');
      await clickButton('关闭订单重试包');
      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });
      const orderCalls = () => api.getCommercialOrders.mock.calls.filter(([orderNo]) => orderNo === closed.order_no).length;
      expect(orderCalls()).toBe(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(11_000);
      });
      expect(orderCalls()).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it('refreshes fulfilled quota with a controller independent from payment polling', async () => {
    const pending = {
      order_no: 'CCFULFILLEDREFRESH0001',
      plan_id: 16,
      plan_name: '到账刷新包',
      amount_fen: 990,
      channel: 'alipay_page',
      status: 'pending',
      created_at: new Date().toISOString(),
    };
    api.getCommercialOrders.mockImplementation((orderNo) => Promise.resolve(
      orderNo ? { order: { ...pending, status: 'fulfilled' } } : { orders: [pending] },
    ));

    await renderModal();
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getRelayCommercial.mock.calls.length).toBeGreaterThanOrEqual(2);
    const refreshOptions = api.getRelayCommercial.mock.calls.at(-1)[0];
    expect(refreshOptions.signal).toBeInstanceOf(AbortSignal);
    expect(refreshOptions.signal.aborted).toBe(false);
    expect(container.textContent).toContain('已生效');
  });
});
