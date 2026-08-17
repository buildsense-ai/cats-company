import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  token: 'session-token',
  sessionRevision: 1,
  wsMessage: null,
  connectWS: vi.fn(),
  disconnectWS: vi.fn(),
  setToken: vi.fn(),
}));

vi.mock('../api', () => {
  const api = {
    getRelayAdminAccess: vi.fn().mockResolvedValue({ allowed: false }),
    getRelayUsage: vi.fn().mockResolvedValue({ summary: null }),
    getRelayConfig: vi.fn().mockResolvedValue({}),
    getMe: vi.fn().mockResolvedValue({ uid: 1, username: 'cats' }),
    getAgents: vi.fn().mockResolvedValue({ agents: [] }),
    getAgentQuota: vi.fn().mockResolvedValue({}),
    getGroupInfo: vi.fn().mockResolvedValue({}),
    unsubscribePush: vi.fn().mockResolvedValue({}),
  };
  return {
    api,
    setToken: mocks.setToken,
    getToken: () => mocks.token,
    getAuthRevision: () => mocks.sessionRevision,
    isCurrentAuthSession: () => true,
    getPushCleanupRegistrationIDs: () => [],
    connectWS: mocks.connectWS,
    reconnectWS: vi.fn(),
    disconnectWS: mocks.disconnectWS,
    sendWSActiveTopic: vi.fn(),
    sendWSPageFocus: vi.fn(),
    sendWSPageVisibility: vi.fn(),
  };
});

vi.mock('../components/feedback-system', () => ({
  InlineFeedback: ({ children }) => <>{children}</>,
  useFeedback: () => ({ confirm: vi.fn() }),
}));

vi.mock('../components/auth-flow-background', () => ({ default: () => null }));
vi.mock('./agent-entry-bind-view', () => ({ default: () => <div data-testid="agent-entry" /> }));
vi.mock('../utils/push-operation', () => ({ enqueuePushOperation: vi.fn(() => Promise.resolve()) }));
vi.mock('../utils/push-tab-coordination', () => ({ pushTabCoordinator: {} }));
vi.mock('../utils/push-session-cleanup', () => ({ cleanupPushForSession: vi.fn() }));
vi.mock('../utils/theme-access', () => ({
  THEME_STORAGE_KEY: 'theme',
  isLiquidTheme: () => false,
  isLiquidThemeUnlocked: () => false,
  normalizeTheme: () => 'light',
  saveLiquidThemeUnlock: () => true,
  applyDocumentTheme: vi.fn(),
  verifyLiquidThemePassword: vi.fn(),
}));

import TinodeWeb from './tinode-web';

let container;
let root;

beforeEach(() => {
  mocks.token = 'session-token';
  mocks.sessionRevision = 1;
  mocks.wsMessage = null;
  mocks.connectWS.mockImplementation((onMessage) => {
    mocks.wsMessage = onMessage;
    return true;
  });
  mocks.connectWS.mockClear();
  mocks.disconnectWS.mockClear();
  mocks.setToken.mockImplementation((nextToken) => {
    mocks.token = nextToken;
  });
  mocks.setToken.mockClear();
  localStorage.setItem('oc_user', JSON.stringify({ uid: 1, username: 'cats' }));
  window.history.replaceState(null, '', '/e/invite-1?source=email#accept');
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  localStorage.clear();
  window.history.replaceState(null, '', '/');
});

test('keeps the current deep link when a WebSocket session expires', async () => {
  await act(async () => {
    root.render(<TinodeWeb />);
  });

  await vi.waitFor(() => expect(mocks.wsMessage).toEqual(expect.any(Function)));

  await act(async () => {
    mocks.wsMessage({ _type: 'ws_auth_expired' });
  });

  await vi.waitFor(() => {
    expect(`${window.location.pathname}${window.location.search}${window.location.hash}`)
      .toBe('/login?next=%2Fe%2Finvite-1%3Fsource%3Demail%23accept');
  });
});
