import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

let mockWSHandler;

jest.mock('../api', () => ({
  api: {
    getMe: jest.fn(() => Promise.resolve({
      uid: 7,
      username: 'alice',
      display_name: 'Alice',
      account_type: 'human',
    })),
    login: jest.fn(),
    register: jest.fn(),
    createRelaySession: jest.fn(),
  },
  setToken: jest.fn(),
  getToken: jest.fn(),
  connectWS: jest.fn((handler) => {
    mockWSHandler = handler;
  }),
  disconnectWS: jest.fn(),
}));

jest.mock('./sidepanel-view', () => function MockChatListView() {
  return null;
});

jest.mock('./messages-view', () => function MockMessagesView() {
  return null;
});

jest.mock('../widgets/profile-editor', () => function MockProfileEditor() {
  return null;
});

jest.mock('../widgets/feedback-modal', () => function MockFeedbackModal() {
  return null;
});

jest.mock('../widgets/catsco-download-modal', () => function MockDownloadModal() {
  return null;
});

jest.mock('../widgets/relay-access-modal', () => function MockRelayModal() {
  return null;
});

jest.mock('../widgets/password-reset-form', () => function MockPasswordResetForm() {
  return null;
});

jest.mock('../widgets/avatar', () => function MockAvatar() {
  return null;
});

jest.mock('../components/CatOrb/CatOrb', () => function MockCatOrb() {
  return null;
});

const TinodeWeb = require('./tinode-web').default;
const { api, setToken, getToken, connectWS, disconnectWS } = require('../api');
const t = require('../i18n').default;

describe('TinodeWeb force logout', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    mockWSHandler = null;
    localStorage.clear();
    connectWS.mockImplementation((handler) => {
      mockWSHandler = handler;
    });
    getToken.mockReturnValue(null);
    api.getMe.mockResolvedValue({
      uid: 7,
      username: 'alice',
      display_name: 'Alice',
      account_type: 'human',
    });
    api.login.mockResolvedValue({
      token: 'token-1',
      uid: 7,
      username: 'alice',
      display_name: 'Alice',
      account_type: 'human',
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

  it('clears session state and shows the revoked-session notice', async () => {
    await act(async () => {
      root.render(<TinodeWeb />);
      await Promise.resolve();
    });

    const inputs = container.querySelectorAll('input.oc-auth-input');
    await act(async () => {
      inputs[0].value = 'alice';
      Simulate.change(inputs[0], { target: { value: 'alice' } });
      inputs[1].value = 'pass123456';
      Simulate.change(inputs[1], { target: { value: 'pass123456' } });
      Simulate.submit(container.querySelector('form.oc-auth-card'));
      await Promise.resolve();
      await Promise.resolve();
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(connectWS).toHaveBeenCalled();
    expect(mockWSHandler).toEqual(expect.any(Function));
    localStorage.setItem('v3_last_topic:7', JSON.stringify({ topicId: 'p2p_7_8', name: 'Bob' }));

    await act(async () => {
      mockWSHandler({ _type: 'force_logout', reason: 'account_disabled' });
      await Promise.resolve();
    });

    expect(disconnectWS).toHaveBeenCalled();
    expect(setToken).toHaveBeenCalledWith(null);
    expect(localStorage.getItem('oc_user')).toBeNull();
    expect(localStorage.getItem('v3_last_topic:7')).toBeNull();
    expect(container.textContent).toContain(t('session_revoked'));
  });
});
