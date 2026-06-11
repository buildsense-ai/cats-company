import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

jest.mock('../api', () => ({
  api: {
    linkChannelAgentBindingUser: jest.fn(),
  },
}));

const ChannelDeviceLinkView = require('./channel-device-link-view').default;
const { api } = require('../api');

const user = {
  uid: 7,
  username: 'annika',
  display_name: 'Annika',
};

function view(props) {
  return React.createElement(ChannelDeviceLinkView, {
    bindingId: '',
    linkToken: '',
    user,
    ...props,
  });
}

describe('ChannelDeviceLinkView', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    api.linkChannelAgentBindingUser.mockReset();
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

  test('does not link automatically before explicit confirmation', async () => {
    api.linkChannelAgentBindingUser.mockResolvedValue({ status: 'linked' });

    await act(async () => {
      root.render(view({ bindingId: '12', linkToken: 'token-1' }));
      await Promise.resolve();
    });

    expect(api.linkChannelAgentBindingUser).not.toHaveBeenCalled();
    expect(container.textContent).toContain('确认绑定设备授权');

    const button = container.querySelector('button');
    expect(button).not.toBeNull();
    await act(async () => {
      Simulate.click(button);
      await Promise.resolve();
    });

    expect(api.linkChannelAgentBindingUser).toHaveBeenCalledWith({
      binding_id: 12,
      link_token: 'token-1',
    });
    expect(container.textContent).toContain('设备授权已绑定');
  });

  test('shows an error when required link parameters are missing', async () => {
    await act(async () => {
      root.render(view());
      await Promise.resolve();
    });

    expect(api.linkChannelAgentBindingUser).not.toHaveBeenCalled();
    expect(container.textContent).toContain('授权链接缺少必要信息');
  });

  test('shows backend link failures after confirmation', async () => {
    api.linkChannelAgentBindingUser.mockRejectedValue(new Error('invalid or expired link token'));

    await act(async () => {
      root.render(view({ bindingId: '12', linkToken: 'bad-token' }));
      await Promise.resolve();
    });

    const button = container.querySelector('button');
    await act(async () => {
      Simulate.click(button);
      await Promise.resolve();
    });

    expect(container.textContent).toContain('invalid or expired link token');
  });
});
