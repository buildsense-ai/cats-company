import React, { act } from 'react';
import { createRoot } from 'react-dom/client';

jest.mock('../api', () => ({
  api: {
    getRelayConfig: jest.fn(),
    getRelayKey: jest.fn(),
    getRelayCommercial: jest.fn(),
    createRelaySession: jest.fn(),
    createRelayKey: jest.fn(),
    rotateRelayKey: jest.fn(),
    revealRelayKey: jest.fn(),
    revokeRelayKey: jest.fn(),
    redeemRelayInvite: jest.fn(),
  },
}));

const RelayAccessModal = require('./relay-access-modal').default;
const { api } = require('../api');

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
    api.getRelayCommercial.mockResolvedValue({
      enabled: false,
      note: '套餐和邀请码仍在内部测试。',
      summary: {
        uid: 38,
        total_cny: 0,
        totals_by_model: {},
        entitlements: [],
      },
    });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
    jest.clearAllMocks();
  });

  async function renderModal() {
    await act(async () => {
      root.render(<RelayAccessModal onClose={jest.fn()} />);
      await Promise.resolve();
      await Promise.resolve();
    });
  }

  it('keeps invite redemption hidden while commercial rollout is disabled', async () => {
    await renderModal();

    expect(container.textContent).toContain('套餐与邀请码');
    expect(container.textContent).toContain('未开放');
    expect(container.textContent).toContain('当前仍使用默认 relay 额度和现有 Key');
    expect(container.textContent).toContain('套餐和邀请码仍在内部测试');
    expect(container.querySelector('.relay-access-invite-form')).toBeNull();
  });

  it('shows invite redemption and per-model budgets when commercial rollout is enabled', async () => {
    api.getRelayCommercial.mockResolvedValue({
      enabled: true,
      summary: {
        uid: 38,
        total_cny: 600,
        totals_by_model: {
          'MiniMax-M3': 500,
          'deepseek-v4-flash': 100,
        },
        entitlements: [
          { state: 'active', plan_name: '教师试用包' },
          { state: 'expired', plan_name: '旧套餐' },
        ],
      },
    });

    await renderModal();

    expect(container.textContent).toContain('灰度开启');
    expect(container.textContent).toContain('当前有效套餐');
    expect(container.textContent).toContain('MiniMax-M3');
    expect(container.textContent).toContain('deepseek-v4-flash');
    expect(container.querySelector('.relay-access-invite-form')).not.toBeNull();
  });
});
