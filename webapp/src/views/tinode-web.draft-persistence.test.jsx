import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  token: 'session-token',
  sessionRevision: 1,
  connectWS: vi.fn(),
  disconnectWS: vi.fn(),
}));

vi.mock('../api', () => {
  const api = {
    createRelaySession: vi.fn(),
    getAgentQuota: vi.fn().mockResolvedValue({}),
    getAgents: vi.fn().mockResolvedValue({ agents: [] }),
    getConversations: vi.fn().mockResolvedValue({ conversations: [] }),
    getDevices: vi.fn().mockResolvedValue({ devices: [] }),
    getGroupInfo: vi.fn().mockResolvedValue({}),
    getMe: vi.fn().mockResolvedValue({ uid: 1, username: 'cats' }),
    getRelayAdminAccess: vi.fn().mockResolvedValue({ allowed: false }),
    getRelayConfig: vi.fn().mockResolvedValue({}),
    getRelayUsage: vi.fn().mockResolvedValue({ summary: null }),
    login: vi.fn(),
    openAgent: vi.fn(),
    unsubscribePush: vi.fn().mockResolvedValue({}),
    updateConversationTitle: vi.fn(),
    updateGroup: vi.fn(),
    updateMe: vi.fn(),
  };
  return {
    api,
    setToken: vi.fn((nextToken) => { mocks.token = nextToken; }),
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
  useFeedback: () => ({ confirm: vi.fn(), notify: vi.fn() }),
}));

vi.mock('../utils/push-operation', () => ({ enqueuePushOperation: vi.fn(() => Promise.resolve()) }));
vi.mock('../utils/push-tab-coordination', () => ({ pushTabCoordinator: {} }));
vi.mock('../utils/push-session-cleanup', () => ({ cleanupPushForSession: vi.fn() }));
vi.mock('../utils/theme-access', () => ({
  THEME_STORAGE_KEY: 'theme',
  applyDocumentTheme: vi.fn(),
  isLiquidTheme: () => false,
  isLiquidThemeUnlocked: () => false,
  normalizeTheme: () => 'light',
  saveLiquidThemeUnlock: vi.fn(),
  verifyLiquidThemePassword: vi.fn(),
}));

vi.mock('./sidepanel-view', () => ({
  default: ({ additionalSidebarTools, onSelectTopic }) => (
    <nav>
      {additionalSidebarTools}
      <button
        type="button"
        onClick={() => onSelectTopic({
          topicId: 'p2p_1_2',
          name: 'Draft test',
          isGroup: false,
          groupId: undefined,
        })}
      >
        打开测试会话
      </button>
    </nav>
  ),
}));

vi.mock('./skillhub-view', () => ({
  default: () => <main data-testid="skillhub-view">SkillHub</main>,
}));

vi.mock('./messages-view', () => ({
  default: ({ composerDraftStore, topic }) => (
    <>
      <textarea
        aria-label="消息草稿"
        defaultValue={composerDraftStore.inputDrafts.get(topic) || ''}
        onChange={(event) => {
          const value = event.target.value;
          if (value) composerDraftStore.inputDrafts.set(topic, value);
          else composerDraftStore.inputDrafts.delete(topic);
          composerDraftStore.persist?.();
        }}
      />
      <button
        type="button"
        aria-label="保存提及草稿"
        onClick={() => {
          composerDraftStore.structuredMentionDrafts.set(topic, [
            { target: 'usr2', label: '助手', start: 0, end: 3 },
          ]);
          composerDraftStore.persist?.();
        }}
      >
        保存提及
      </button>
      <button
        type="button"
        aria-label="保存附件草稿"
        onClick={() => {
          composerDraftStore.attachmentDrafts.set(topic, [{
            name: 'report.pdf',
            type: 'file',
            size: 24,
            content: { type: 'file', payload: { file_key: 'report.pdf' } },
          }]);
          composerDraftStore.persist?.();
        }}
      >
        保存附件
      </button>
      <output aria-label="已恢复提及">
        {composerDraftStore.structuredMentionDrafts.get(topic)?.[0]?.label || ''}
      </output>
      <output aria-label="已恢复附件">
        {composerDraftStore.attachmentDrafts.get(topic)?.[0]?.name || ''}
      </output>
    </>
  ),
}));

vi.mock('../widgets/empty-task-composer', () => ({ default: () => <div /> }));
vi.mock('../widgets/catsco-download-modal', () => ({ default: () => null }));
vi.mock('../widgets/desktop-connect-modal', () => ({ default: () => null }));
vi.mock('../widgets/feedback-modal', () => ({ default: () => null }));
vi.mock('../widgets/profile-editor', () => ({ default: () => null }));
vi.mock('../widgets/relay-access-modal', () => ({ default: () => null }));

import TinodeWeb from './tinode-web';

let container;
let root;

function renderWorkspace() {
  root.render(<TinodeWeb location={{ pathname: '/', search: '', hash: '' }} />);
}

async function selectTestConversation() {
  await act(async () => {
    Simulate.click([...container.querySelectorAll('button')]
      .find((button) => button.textContent === '打开测试会话'));
    await Promise.resolve();
  });
}

beforeEach(() => {
  mocks.token = 'session-token';
  mocks.sessionRevision = 1;
  window.matchMedia = vi.fn(() => ({ matches: false }));
  localStorage.setItem('oc_user', JSON.stringify({ uid: 1, username: 'cats' }));
  sessionStorage.clear();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  localStorage.clear();
  sessionStorage.clear();
});

test('restores a draft when returning from SkillHub after the workspace remounts', async () => {
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });
  await selectTestConversation();

  const textarea = container.querySelector('textarea[aria-label="消息草稿"]');
  await act(async () => {
    textarea.value = 'draft survives SkillHub navigation';
    Simulate.change(textarea, { target: { value: textarea.value } });
  });

  await act(async () => {
    Simulate.click(container.querySelector('[aria-label="打开 SkillHub"]'));
    await Promise.resolve();
  });
  expect(container.querySelector('[data-testid="skillhub-view"]')).not.toBeNull();

  await act(async () => root.unmount());
  root = createRoot(container);
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });
  await selectTestConversation();

  expect(container.querySelector('textarea[aria-label="消息草稿"]').value)
    .toBe('draft survives SkillHub navigation');
});

test('round-trips text, mentions, and attachments across a workspace remount', async () => {
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });
  await selectTestConversation();

  const textarea = container.querySelector('textarea[aria-label="消息草稿"]');
  await act(async () => {
    textarea.value = 'structured draft';
    Simulate.change(textarea, { target: { value: textarea.value } });
    Simulate.click(container.querySelector('[aria-label="保存提及草稿"]'));
    Simulate.click(container.querySelector('[aria-label="保存附件草稿"]'));
  });

  const stored = JSON.parse(sessionStorage.getItem('catsco_composer_drafts:v1:1'));
  expect(stored.inputDrafts).toEqual([['p2p_1_2', 'structured draft']]);
  expect(stored.structuredMentionDrafts).toEqual([[
    'p2p_1_2',
    [{ target: 'usr2', label: '助手', start: 0, end: 3 }],
  ]]);
  expect(stored.attachmentDrafts).toEqual([[
    'p2p_1_2',
    [{
      name: 'report.pdf',
      type: 'file',
      size: 24,
      content: { type: 'file', payload: { file_key: 'report.pdf' } },
    }],
  ]]);

  await act(async () => root.unmount());
  root = createRoot(container);
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });
  await selectTestConversation();

  expect(container.querySelector('textarea[aria-label="消息草稿"]').value)
    .toBe('structured draft');
  expect(container.querySelector('[aria-label="已恢复提及"]').textContent).toBe('助手');
  expect(container.querySelector('[aria-label="已恢复附件"]').textContent).toBe('report.pdf');
});

test('does not restore another account\'s composer drafts', async () => {
  sessionStorage.setItem('catsco_composer_drafts:v1:2', JSON.stringify({
    inputDrafts: [['p2p_1_2', 'another account draft']],
    structuredMentionDrafts: [],
    attachmentDrafts: [],
  }));

  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });
  await selectTestConversation();

  expect(container.querySelector('textarea[aria-label="消息草稿"]').value).toBe('');
  expect(sessionStorage.getItem('catsco_composer_drafts:v1:2')).not.toBeNull();
});
