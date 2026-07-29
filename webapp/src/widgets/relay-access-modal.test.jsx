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
        models: ['MiniMax-M3', 'deepseek-v4-flash'],
        entitlements: [],
        plans: [],
      },
    });
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    });
    window.confirm = vi.fn(() => true);
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
    expect(container.textContent).toContain('还没有模型服务 Key');
    expect(container.textContent).not.toContain('sk-bf-rotated-secret');
  });

  it('keeps invite redemption hidden while commercial rollout is disabled', async () => {
    await renderModal();

    expect(container.textContent).toContain('套餐与邀请码');
    expect(container.textContent).toContain('未开放');
    expect(container.textContent).toContain('当前仍使用默认模型服务额度和现有 Key');
    expect(container.textContent).toContain('套餐和邀请码仍在内部测试');
    expect(container.querySelector('.relay-access-invite-form')).toBeNull();
  });

  it('shows invite redemption and per-model budgets when commercial rollout is enabled', async () => {
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
        models: ['MiniMax-M3', 'deepseek-v4-flash'],
        entitlements: [
          { state: 'active', plan_name: '教师试用包', expires_at: '2026-07-29T00:00:00Z' },
          { state: 'expired', plan_name: '旧套餐' },
        ],
      },
    });

    await renderModal();

    expect(container.textContent).toContain('账本灰度');
    expect(container.textContent).toContain('当前模型用量');
    expect(container.textContent).toContain('需要管理员后台对账/同步后');
    expect(container.textContent).toContain('当前有效套餐');
    expect(container.textContent).toContain('套餐最近到期');
    expect(container.textContent).toContain('每月重置');
    expect(container.textContent).toContain('下次');
    expect(container.textContent).toContain('不是自然月');
    expect(container.textContent).toContain('当前套餐');
    expect(container.textContent).toContain('教师试用包');
    expect(container.textContent).toContain('MiniMax-M3');
    expect(container.textContent).toContain('deepseek-v4-flash');
    expect(container.textContent).toContain('剩余 75%');
    expect(container.textContent).toContain('剩余 87.5%');
    expect(container.textContent).not.toContain('CNY');
    expect(container.textContent).not.toContain('¥');
    expect(container.textContent).not.toContain('￥');
    expect(container.textContent).not.toContain('禁用套餐');
    expect(container.querySelector('.relay-access-invite-form')).not.toBeNull();
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

  it('shows custom model as outside relay package quota', async () => {
    api.getRelayUsage.mockResolvedValue({
      configured: true,
      summary: {
        source: 'custom',
        model: '自定义模型',
        status: 'custom',
      },
    });

    await renderModal();

    expect(container.textContent).toContain('当前使用自定义模型');
    expect(container.textContent).toContain('不消耗 CatsCo 模型服务套餐');
  });

  it('shows explicit over-limit warning for the current relay model', async () => {
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

    expect(container.textContent).toContain('当前模型已超额');
    expect(container.textContent).toContain('剩余 0%');
    expect(container.textContent).toContain('已用 100%+');
    expect(container.textContent).not.toContain('CNY');
    expect(container.textContent).toContain('请联系管理员补额或重置');
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

    expect(container.textContent).toContain('当前模型未设置额度');
    expect(container.textContent).toContain('等待模型限额同步');
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

  it('renders the CatsCo five-tier catalog with the reviewed pricing copy', async () => {
    api.getCommercialCatalog.mockResolvedValue({
      enabled: true,
      test_mode: true,
      trial_available: false,
      channels: [],
      plans: [
        {
          id: 1,
          slug: 'catsco-trial-3d',
          name: '3天体验',
          description: '用三天跑一遍真实任务。',
          price_fen: 990,
          duration_days: 3,
          model_budgets: { 'MiniMax-M3': 10 },
        },
        {
          id: 2,
          slug: 'catsco-plus-minus',
          name: 'Plus−',
          price_fen: 4900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M3': 20 },
        },
        {
          id: 3,
          slug: 'catsco-plus',
          name: 'Plus',
          price_fen: 9900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M3': 30 },
        },
        {
          id: 4,
          slug: 'catsco-plus-plus',
          name: 'Plus+',
          price_fen: 19900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M3': 40 },
        },
        {
          id: 5,
          slug: 'catsco-team-monthly',
          name: '团队月卡',
          price_fen: 39900,
          duration_days: 30,
          model_budgets: { 'MiniMax-M3': 50 },
        },
      ],
    });

    await renderModal();

    expect(container.textContent).toContain('选一档，开始你的协作节奏');
    expect(container.textContent).toContain('¥9.9');
    expect(container.textContent).toContain('¥49');
    expect(container.textContent).toContain('¥99');
    expect(container.textContent).toContain('¥199');
    expect(container.textContent).toContain('¥399');
    expect(container.textContent).toContain('首次体验 · 每位用户限购一次');
    expect(container.textContent).toContain('稳定日用 · 默认推荐');
    expect(container.textContent).toContain('体验用量 · 3 天有效');
    expect(container.textContent).toContain('标准用量 · 30 天有效');
    expect(container.textContent).toContain('支付宝材料与支付通道仍在准备');
    expect(container.textContent).not.toContain('MiniMax-M3 10.00');
    expect(container.textContent).not.toContain('MiniMax-M3 30.00');
    expect(container.querySelector('.relay-access-plan-row.recommended')?.textContent).toContain('Plus');
    expect(container.querySelector('.relay-access-plan-row.wide')?.textContent).toContain('团队月卡');
    expect(Array.from(container.querySelectorAll('.relay-access-plan-row button')).every((button) => button.disabled)).toBe(true);
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

    expect(api.createCommercialOrder).toHaveBeenCalledWith(9, 'test', expect.stringMatching(/^order_/));
    expect(container.textContent).toContain('待支付');
    const confirmButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent.includes('完成灰度测试支付'));
    await act(async () => {
      confirmButton.click();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.confirmCommercialTestPayment).toHaveBeenCalledWith('CCWEBTEST0001');
    expect(container.textContent).toContain('支付成功');
    expect(container.textContent).toContain('已到账');
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

    await renderModal();
    const purchaseButton = Array.from(container.querySelectorAll('button')).find((button) => button.textContent.includes('购买'));
    await act(async () => {
      purchaseButton.click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createCommercialOrder).toHaveBeenCalledWith(10, 'alipay_page', expect.stringMatching(/^order_/));
    expect(container.textContent).toContain('支付宝支付 ¥9.9');
    const paymentLink = container.querySelector('.relay-access-payment-redirect a');
    expect(paymentLink?.getAttribute('href')).toBe('https://openapi.alipay.test/gateway.do');
    expect(paymentLink?.getAttribute('target')).toBe('_blank');
    expect(paymentLink?.getAttribute('rel')).toContain('noopener');
    expect(container.textContent).not.toContain('完成灰度测试支付');
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
    const orderButton = Array.from(container.querySelectorAll('button'))
      .find(button => button.textContent.includes('历史支付宝订单'));
    await act(async () => {
      orderButton.click();
      await Promise.resolve();
    });

    expect(container.textContent).toContain('支付宝支付 ¥9.9');
  });
});
