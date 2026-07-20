import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    createBot: vi.fn(),
    getAgents: vi.fn(),
    getFriends: vi.fn(),
    getMyBots: vi.fn(),
  },
  getWebSocketURL: vi.fn(() => 'wss://app.catsco.cc/v0/channels'),
}));

import { api } from '../api';
import AgentStoreModal from './agent-store-modal';

describe('AgentStoreModal', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.createBot.mockReset().mockResolvedValue({ uid: 91 });
    api.getAgents.mockReset().mockResolvedValue({ agents: [] });
    api.getFriends.mockReset().mockResolvedValue({ friends: [] });
    api.getMyBots.mockReset().mockResolvedValue({ bots: [] });
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  test('allows creating an assistant without a usage description', async () => {
    await act(async () => {
      root.render(React.createElement(AgentStoreModal, {
        onClose: vi.fn(),
        user: { uid: 7 },
      }));
      await Promise.resolve();
    });

    const createTab = Array.from(container.querySelectorAll('button'))
      .find((button) => button.textContent.includes('创建新助手'));

    await act(async () => {
      Simulate.click(createTab);
    });

    const form = container.querySelector('.cc-agent-create-form');
    const nameInput = form.querySelector('input[type="text"]');
    const description = form.querySelector('textarea');
    const submit = form.querySelector('button[type="submit"]');
    const roleSelect = form.querySelector('select.cc-agent-role-select');

    expect(description.required).toBe(false);
    expect(submit.disabled).toBe(false);
    expect(roleSelect).not.toBeNull();
    expect(roleSelect.nextElementSibling?.classList.contains('cc-agent-role-chevron')).toBe(true);

    await act(async () => {
      Simulate.change(nameInput, { target: { value: '测试助手' } });
    });

    await act(async () => {
      Simulate.submit(form);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createBot).toHaveBeenCalledWith(
      expect.objectContaining({ display_name: '测试助手' }),
      false,
    );
  });
});
