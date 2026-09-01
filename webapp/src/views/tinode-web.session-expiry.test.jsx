import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  token: 'session-token',
  sessionRevision: 1,
  wsMessage: null,
  connectWS: vi.fn(),
  disconnectWS: vi.fn(),
  getMe: vi.fn(),
  setToken: vi.fn(),
}));

vi.mock('../api', () => {
  const api = {
    getRelayAdminAccess: vi.fn().mockResolvedValue({ allowed: false }),
    getRelayUsage: vi.fn().mockResolvedValue({ summary: null }),
    getRelayConfig: vi.fn().mockResolvedValue({}),
    getMe: mocks.getMe,
    getAgents: vi.fn().mockResolvedValue({ agents: [] }),
    getDevices: vi.fn().mockResolvedValue({ devices: [] }),
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

import TinodeWeb, { resetComposerDraftStore } from './tinode-web';
import { COMPOSER_DRAFT_STORAGE_PREFIX } from '../utils/composer-draft-storage';

let container;
let root;

beforeEach(() => {
  mocks.token = 'session-token';
  mocks.sessionRevision = 1;
  mocks.wsMessage = null;
  mocks.getMe.mockResolvedValue({ uid: 1, username: 'cats' });
  mocks.getMe.mockClear();
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
  sessionStorage.clear();
  localStorage.setItem('oc_user', JSON.stringify({ uid: 1, username: 'cats' }));
  window.history.replaceState(null, '', '/e/invite-1?source=email#accept');
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  sessionStorage.clear();
  localStorage.clear();
  window.history.replaceState(null, '', '/');
});

test('replaces draft maps so stale callbacks cannot restore a closed session draft', () => {
  const staleStore = {
    inputDrafts: new Map([['p2p_1_2', 'old session draft']]),
    structuredMentionDrafts: new Map(),
    attachmentDrafts: new Map(),
  };
  const draftStoreRef = { current: staleStore };

  const activeStore = resetComposerDraftStore(draftStoreRef);
  staleStore.inputDrafts.set('p2p_1_2', 'late-send draft');
  staleStore.structuredMentionDrafts.set('p2p_1_2', [{ target: 'usr2' }]);
  staleStore.attachmentDrafts.set('p2p_1_2', [{ name: 'late-upload.png' }]);

  expect(draftStoreRef.current).toBe(activeStore);
  expect(activeStore).not.toBe(staleStore);
  expect(activeStore.inputDrafts.get('p2p_1_2')).toBeUndefined();
  expect(activeStore.structuredMentionDrafts.get('p2p_1_2')).toBeUndefined();
  expect(activeStore.attachmentDrafts.get('p2p_1_2')).toBeUndefined();
});

test('keeps the current deep link when a WebSocket session expires', async () => {
  const staleDraftKey = `${COMPOSER_DRAFT_STORAGE_PREFIX}99`;
  sessionStorage.setItem(staleDraftKey, JSON.stringify({
    inputDrafts: [['p2p_99_1', 'stale draft']],
  }));

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
  expect(sessionStorage.getItem(staleDraftKey)).toBeNull();
});

test('recovers a valid session when its cached profile is missing', async () => {
  localStorage.removeItem('oc_user');

  await act(async () => {
    root.render(<TinodeWeb location={{ pathname: '/e/invite-1', search: '', hash: '' }} />);
  });

  await vi.waitFor(() => {
    expect(container.querySelector('[data-testid="agent-entry"]')).toBeTruthy();
  });
  expect(mocks.getMe).toHaveBeenCalledTimes(1);
  expect(JSON.parse(localStorage.getItem('oc_user'))).toMatchObject({ uid: 1, username: 'cats' });
  expect(mocks.setToken).not.toHaveBeenCalled();
});

test('recovers a valid session when caching the recovered profile fails', async () => {
  localStorage.removeItem('oc_user');
  const originalSetItem = Storage.prototype.setItem;
  const setItemSpy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(function setItem(key, value) {
    if (key === 'oc_user') throw new DOMException('Storage quota exceeded', 'QuotaExceededError');
    return originalSetItem.call(this, key, value);
  });

  try {
    await act(async () => {
      root.render(<TinodeWeb location={{ pathname: '/e/invite-1', search: '', hash: '' }} />);
    });

    await vi.waitFor(() => {
      expect(container.querySelector('[data-testid="agent-entry"]')).toBeTruthy();
    });
    expect(mocks.getMe).toHaveBeenCalled();
  } finally {
    setItemSpy.mockRestore();
  }
});

test('keeps a deep link stable while a missing cached profile is recovered', async () => {
  localStorage.removeItem('oc_user');
  let resolveProfile;
  mocks.getMe.mockImplementation(() => new Promise((resolve) => {
    resolveProfile = resolve;
  }));

  await act(async () => {
    root.render(<TinodeWeb />);
  });

  await vi.waitFor(() => expect(mocks.getMe).toHaveBeenCalledTimes(1));
  expect(`${window.location.pathname}${window.location.search}${window.location.hash}`)
    .toBe('/e/invite-1?source=email#accept');

  await act(async () => {
    resolveProfile({ uid: 1, username: 'cats' });
  });

  await vi.waitFor(() => {
    expect(container.querySelector('[data-testid="agent-entry"]')).toBeTruthy();
  });
  expect(`${window.location.pathname}${window.location.search}${window.location.hash}`)
    .toBe('/e/invite-1?source=email#accept');
});

test('clears a missing-profile session only after the server rejects its token', async () => {
  localStorage.removeItem('oc_user');
  const unauthorized = Object.assign(new Error('登录状态已失效'), { status: 401 });
  mocks.getMe.mockRejectedValue(unauthorized);

  await act(async () => {
    root.render(<TinodeWeb location={{ pathname: '/e/invite-1', search: '', hash: '' }} />);
  });

  await vi.waitFor(() => {
    expect(mocks.setToken).toHaveBeenCalledWith(null);
  });
});

test('clears a missing-profile session when the account no longer exists', async () => {
  localStorage.removeItem('oc_user');
  const missingUser = Object.assign(new Error('用户不存在'), { status: 404 });
  mocks.getMe.mockRejectedValue(missingUser);

  await act(async () => {
    root.render(<TinodeWeb location={{ pathname: '/e/invite-1', search: '', hash: '' }} />);
  });

  await vi.waitFor(() => {
    expect(mocks.setToken).toHaveBeenCalledWith(null);
  });
});
