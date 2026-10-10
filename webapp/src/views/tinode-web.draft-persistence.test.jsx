import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { afterEach, beforeEach, expect, test, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  token: 'session-token',
  sessionRevision: 1,
  connectWS: vi.fn(),
  disconnectWS: vi.fn(),
  getMe: vi.fn(),
  redeemBotInviteCode: vi.fn(),
  updateMe: vi.fn(),
}));

vi.mock('../api', () => {
  const api = {
    createRelaySession: vi.fn(),
    getAgentQuota: vi.fn().mockResolvedValue({}),
    getAgents: vi.fn().mockResolvedValue({ agents: [] }),
    getConversations: vi.fn().mockResolvedValue({ conversations: [] }),
    getDevices: vi.fn().mockResolvedValue({ devices: [] }),
    getGroupInfo: vi.fn().mockResolvedValue({}),
    getMe: mocks.getMe,
    getRelayAdminAccess: vi.fn().mockResolvedValue({ allowed: false }),
    getRelayConfig: vi.fn().mockResolvedValue({}),
    getRelayUsage: vi.fn().mockResolvedValue({ summary: null }),
    login: vi.fn(),
    openAgent: vi.fn(),
    createGroup: vi.fn(),
    assignProjectTopic: vi.fn().mockResolvedValue({}),
    disbandGroup: vi.fn().mockResolvedValue({}),
    sendMessage: vi.fn(),
    redeemBotInviteCode: mocks.redeemBotInviteCode,
    unsubscribePush: vi.fn().mockResolvedValue({}),
    updateConversationTitle: vi.fn(),
    updateGroup: vi.fn(),
    updateMe: mocks.updateMe,
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
  default: ({ additionalSidebarTools, onSelectTopic, onStartAgentTask, onOpenCloudArtifacts }) => (
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
      <button type="button" onClick={() => onOpenCloudArtifacts(7)}>打开新任务应用</button>
      <button
        type="button"
        onClick={() => onStartAgentTask({ uid: 7, display_name: '测试 Agent' })}
      >
        选择 Agent 返回新任务
      </button>
    </nav>
  ),
}));

vi.mock('./skillhub-view', () => ({
  default: () => <main data-testid="skillhub-view">SkillHub</main>,
}));

vi.mock('./messages-view', () => ({
  default: (props) => {
    mocks.messageProps = props;
    const { composerDraftStore, topic } = props;
    return (
    <textarea
      className="v3-composer-input"
      aria-label="消息草稿"
      data-topic={topic}
      defaultValue={composerDraftStore.inputDrafts.get(topic) || ''}
      onChange={(event) => {
        const value = event.target.value;
        if (value) composerDraftStore.inputDrafts.set(topic, value);
        else composerDraftStore.inputDrafts.delete(topic);
        composerDraftStore.persist?.();
      }}
    />
    );
  },
}));

vi.mock('../widgets/standalone-cloud-artifacts-panel', () => ({
  default: (props) => {
    mocks.standaloneProps = props;
    return <button type="button" onClick={() => props.onStartAnnotation({
      id: 'board', title: '看板', url: 'https://artifact.catsco.cc/board/', agent: '7',
    }).catch(error => { mocks.annotationError = error; })}>新建任务并标注</button>;
  },
}));

vi.mock('../widgets/empty-task-composer', () => {
  return {
  default: (props) => {
    mocks.emptyProps = props;
    const { composerDraftStore, draftKey = 'new-task' } = props;
    const key = String(draftKey || 'new-task');
    const inputDrafts = composerDraftStore?.inputDrafts;
    return (
      <div className="cc-empty-composer-wrap">
        <textarea
          className="v3-composer-input"
          aria-label="新任务草稿"
          defaultValue={inputDrafts?.get?.(key) || ''}
          onChange={(event) => {
            const value = event.target.value;
            if (value) inputDrafts?.set?.(key, value);
            else inputDrafts?.delete?.(key);
            composerDraftStore?.persist?.();
          }}
        />
      </div>
    );
  },
  };
});
vi.mock('../widgets/catsco-download-modal', () => ({ default: () => null }));
vi.mock('../widgets/desktop-connect-modal', () => ({
  default: ({ initialMode, onClose, onConnected, onOpenOnboardingGuide }) => (
    <section data-testid="desktop-connect-modal" data-mode={initialMode}>
      <button type="button" onClick={onClose}>关闭桌面端</button>
      <button type="button" onClick={() => onConnected({})}>桌面端连接成功</button>
      <button type="button" onClick={onOpenOnboardingGuide}>新手指引</button>
    </section>
  ),
}));
vi.mock('../widgets/feedback-modal', () => ({ default: () => null }));
vi.mock('../widgets/relay-access-modal', () => ({
  default: ({ onClose }) => (
    <section data-testid="relay-access-modal">
      <button type="button" onClick={onClose}>关闭套餐与权益</button>
    </section>
  ),
}));

import TinodeWeb from './tinode-web';
import { api } from '../api';
import { readComposerInputDraft, readComposerAttachmentDraft, writeComposerAttachmentDraft } from '../utils/composer-draft-storage';
import { workspaceOnboardingStorageKey } from '../utils/workspace-onboarding';

let container;
let root;

function renderWorkspace(location = { pathname: '/', search: '', hash: '' }) {
  root.render(<TinodeWeb location={location} />);
}

async function selectTestConversation() {
  await act(async () => {
    Simulate.click([...container.querySelectorAll('button')]
      .find((button) => button.textContent === '打开测试会话'));
    await Promise.resolve();
  });
}

async function openOnboardingReplayFromDesktopEntry() {
  await selectTestConversation();
  await vi.waitFor(() => expect(container.querySelector('[aria-label="消息草稿"][data-topic="p2p_1_2"]')).not.toBeNull());
  const profileTrigger = container.querySelector('[aria-label="cats，打开个人菜单"]');
  await act(async () => profileTrigger.click());
  const desktopEntry = [...document.querySelectorAll('[role="menuitem"]')]
    .find((item) => item.textContent.includes('CatsCo 桌面端'));
  await act(async () => desktopEntry.click());

  const desktopModal = await vi.waitFor(() => {
    const modal = container.querySelector('[data-testid="desktop-connect-modal"]');
    expect(modal).not.toBeNull();
    return modal;
  });
  await act(async () => {
    [...desktopModal.querySelectorAll('button')]
      .find((button) => button.textContent === '新手指引').click();
  });

  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
  expect(container.querySelector('[aria-label="消息草稿"][data-topic="p2p_1_2"]')).not.toBeNull();
}

beforeEach(() => {
  mocks.token = 'session-token';
  mocks.sessionRevision = 1;
  mocks.annotationError = null;
  mocks.messageProps = null;
  mocks.emptyProps = null;
  mocks.standaloneProps = null;
  api.createGroup.mockReset().mockResolvedValue({ group: { id: 901, name: '标注任务', member_count: 2 }, topic: 'grp_901' });
  api.sendMessage.mockReset();
  api.openAgent.mockReset();
  mocks.getMe.mockReset().mockResolvedValue({ uid: 1, username: 'cats', created_at: '2026-01-01T00:00:00Z' });
  mocks.redeemBotInviteCode.mockReset().mockResolvedValue({});
  mocks.updateMe.mockReset().mockResolvedValue({ uid: 1, username: 'cats', display_name: 'Cats' });
  window.matchMedia = vi.fn(() => ({ matches: false }));
  localStorage.setItem('oc_user', JSON.stringify({ uid: 1, username: 'cats' }));
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

function setCachedUser(createdAt) {
  const profile = { uid: 1, username: 'cats', created_at: createdAt };
  mocks.getMe.mockResolvedValue(profile);
  localStorage.setItem('oc_user', JSON.stringify(profile));
}

test('shows the onboarding card once for a new account entering the workspace', async () => {
  setCachedUser('2026-09-18T00:00:01Z');
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
    await Promise.resolve();
  });

  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
  expect(container.querySelector('[data-testid="desktop-connect-modal"]')).toBeNull();
});

test('preserves new-account eligibility after completing the name onboarding', async () => {
  setCachedUser('2026-09-18T00:00:01Z');
  mocks.updateMe.mockResolvedValue({ uid: 1, username: 'cats', display_name: 'Alex' });

  await act(async () => {
    renderWorkspace({ pathname: '/onboarding/name', search: '', hash: '' });
    await Promise.resolve();
    await Promise.resolve();
  });

  const input = container.querySelector('#catsco-display-name');
  const valueSetter = Object.getOwnPropertyDescriptor(
    window.HTMLInputElement.prototype,
    'value',
  ).set;
  await act(async () => {
    valueSetter.call(input, 'Alex');
    input.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await act(async () => {
    container.querySelector('form').dispatchEvent(new Event('submit', {
      bubbles: true,
      cancelable: true,
    }));
    await Promise.resolve();
  });

  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });

  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
  expect(JSON.parse(localStorage.getItem('oc_user'))).toMatchObject({
    display_name: 'Alex',
    created_at: '2026-09-18T00:00:01Z',
  });
});

test('shows onboarding for an eligible new account over a restored active conversation', async () => {
  setCachedUser('2026-09-18T00:00:01Z');
  localStorage.setItem('v3_last_topic:1', JSON.stringify({
    topicId: 'p2p_1_2',
    name: 'Restored conversation',
    isGroup: false,
  }));

  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
    await Promise.resolve();
  });

  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
  expect(container.querySelector('[aria-label="消息草稿"]')?.dataset.topic).toBe('p2p_1_2');
  expect(container.querySelector('[aria-label="新任务草稿"]')).toBeNull();
});

test('restores empty-workspace composer focus after automatic onboarding closes', async () => {
  setCachedUser('2026-09-18T00:00:01Z');
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
    await Promise.resolve();
  });

  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
  const composer = container.querySelector('[aria-label="新任务草稿"]');
  await act(async () => {
    document.querySelector('[aria-label="稍后设置助手"]').click();
    await new Promise((resolve) => requestAnimationFrame(resolve));
  });

  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  expect(document.activeElement).toBe(composer);
});

test('does not repeat onboarding after its local download handoff closes', async () => {
  setCachedUser('2026-09-18T00:00:01Z');

  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
    await Promise.resolve();
  });

  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
  const downloadButton = [...document.querySelectorAll('button')]
    .find((button) => button.textContent.includes('下载桌面端'));
  await act(async () => {
    downloadButton.click();
  });

  await vi.waitFor(() => expect(container.querySelector('[data-testid="desktop-connect-modal"]')?.dataset.mode).toBe('download'));
  expect(localStorage.getItem(workspaceOnboardingStorageKey(1))).toBe('dismissed');

  await act(async () => {
    container.querySelector('[data-testid="desktop-connect-modal"] button').click();
  });
  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
});

test('does not automatically show onboarding for an existing account', async () => {
  setCachedUser('2026-09-17T23:59:59Z');

  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
    await Promise.resolve();
  });

  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
});

test('keeps download mode and defers onboarding when a new account arrives through the download deep link', async () => {
  setCachedUser('2026-09-18T00:00:01Z');

  await act(async () => {
    renderWorkspace({ pathname: '/', search: '?open=download', hash: '' });
    await Promise.resolve();
    await Promise.resolve();
  });

  await vi.waitFor(() => expect(container.querySelector('[data-testid="desktop-connect-modal"]')?.dataset.mode).toBe('download'));
  expect(document.querySelector('.cc-workspace-onboarding-card[open]')).toBeNull();

  await act(async () => {
    container.querySelector('[data-testid="desktop-connect-modal"] button').click();
  });
  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
});

test('shows automatic onboarding after a download deep link connects a desktop without an Agent', async () => {
  setCachedUser('2026-09-18T00:00:01Z');

  await act(async () => {
    renderWorkspace({ pathname: '/', search: '?open=download', hash: '' });
    await Promise.resolve();
    await Promise.resolve();
  });

  const desktopModal = container.querySelector('[data-testid="desktop-connect-modal"]');
  expect(desktopModal).not.toBeNull();
  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  await act(async () => {
    [...desktopModal.querySelectorAll('button')]
      .find((button) => button.textContent === '桌面端连接成功').click();
    await Promise.resolve();
  });

  expect(container.querySelector('[data-testid="desktop-connect-modal"]')).toBeNull();
  await vi.waitFor(() => expect(document.querySelectorAll('#workspace-onboarding-title')).toHaveLength(1));
});

test('opens the same onboarding card from the DesktopConnectModal New User Guide entry', async () => {
  setCachedUser('2026-09-18T00:00:01Z');

  await act(async () => {
    renderWorkspace({ pathname: '/', search: '?open=download', hash: '' });
    await Promise.resolve();
    await Promise.resolve();
  });

  const desktopModal = container.querySelector('[data-testid="desktop-connect-modal"]');
  expect(desktopModal).not.toBeNull();
  await act(async () => {
    [...desktopModal.querySelectorAll('button')]
      .find((button) => button.textContent === '新手指引').click();
  });

  expect(container.querySelector('[data-testid="desktop-connect-modal"]')).toBeNull();
  await vi.waitFor(() => expect(document.querySelectorAll('#workspace-onboarding-title')).toHaveLength(1));
});

test('keeps exactly one onboarding card when replaying the local onboarding preview', async () => {
  setCachedUser('2026-09-17T23:59:59Z');
  localStorage.setItem('v3_last_topic:1', JSON.stringify({
    topicId: 'p2p_restored_preview',
    name: 'Restored preview conversation',
    isGroup: false,
  }));
  await act(async () => {
    renderWorkspace({ pathname: '/', search: '?onboarding_preview=1&open=download', hash: '' });
    await Promise.resolve();
    await Promise.resolve();
  });

  // The preview flag must replace the restored message view with the new-task workspace.
  expect(container.querySelector('[aria-label="消息草稿"]')).toBeNull();
  expect(container.querySelector('[aria-label="新任务草稿"]')).not.toBeNull();

  const desktopModal = await vi.waitFor(() => {
    const modal = container.querySelector('[data-testid="desktop-connect-modal"]');
    expect(modal).not.toBeNull();
    return modal;
  });
  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  await act(async () => {
    [...desktopModal.querySelectorAll('button')]
      .find((button) => button.textContent === '新手指引').click();
  });

  expect(container.querySelector('[data-testid="desktop-connect-modal"]')).toBeNull();
  await vi.waitFor(() => expect(document.querySelectorAll('#workspace-onboarding-title')).toHaveLength(1));
  await act(async () => {
    document.querySelector('[aria-label="稍后设置助手"]').click();
  });
  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
});

test('replays onboarding from the computer entry over an active conversation and redeems invites', async () => {
  setCachedUser('2026-09-17T23:59:59Z');
  await act(async () => renderWorkspace());
  await openOnboardingReplayFromDesktopEntry();

  expect(container.querySelector('[data-testid="desktop-connect-modal"]')).toBeNull();
  expect(document.querySelectorAll('#workspace-onboarding-title')).toHaveLength(1);
  await act(async () => {
    Simulate.click([...document.querySelectorAll('button')]
      .find((button) => button.textContent.includes('使用邀请码')));
  });
  const input = document.querySelector('#workspace-onboarding-invite');
  await act(async () => {
    Simulate.change(input, { target: { value: 'replay-123' } });
  });
  await act(async () => {
    Simulate.submit(input.closest('form'));
    await Promise.resolve();
    await Promise.resolve();
  });

  expect(mocks.redeemBotInviteCode).toHaveBeenCalledWith('REPLAY-123');
  expect(document.querySelector('#workspace-invite-title')?.textContent).toBe('云端助手已添加');
});

test.each(['Escape', 'close button'])('restores active conversation composer focus when replay onboarding closes via %s', async (method) => {
  setCachedUser('2026-09-17T23:59:59Z');
  await act(async () => renderWorkspace());
  await openOnboardingReplayFromDesktopEntry();
  const composer = container.querySelector('[aria-label="消息草稿"]');
  expect(composer).not.toBeNull();

  const dialog = document.querySelector('dialog.cc-workspace-onboarding-card');
  await act(async () => {
    if (method === 'Escape') {
      dialog.dispatchEvent(new Event('cancel', { bubbles: true, cancelable: true }));
    } else {
      dialog.querySelector('[aria-label="稍后设置助手"]').click();
    }
    await new Promise((resolve) => requestAnimationFrame(resolve));
  });

  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  expect(document.activeElement).toBe(composer);
});

test('restores SkillHub entry focus when onboarding replay closes over SkillHub', async () => {
  setCachedUser('2026-09-17T23:59:59Z');
  await act(async () => renderWorkspace());
  const skillHubEntry = container.querySelector('[aria-label="打开 SkillHub"]');
  await act(async () => skillHubEntry.click());
  expect(container.querySelector('[data-testid="skillhub-view"]')).not.toBeNull();

  const profileTrigger = container.querySelector('[aria-label="cats，打开个人菜单"]');
  await act(async () => profileTrigger.click());
  const desktopEntry = [...document.querySelectorAll('[role="menuitem"]')]
    .find((item) => item.textContent.includes('CatsCo 桌面端'));
  await act(async () => desktopEntry.click());
  const desktopModal = await vi.waitFor(() => {
    const modal = container.querySelector('[data-testid="desktop-connect-modal"]');
    expect(modal).not.toBeNull();
    return modal;
  });
  await act(async () => {
    [...desktopModal.querySelectorAll('button')]
      .find((button) => button.textContent === '新手指引').click();
  });
  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());

  await act(async () => {
    document.querySelector('[aria-label="稍后设置助手"]').click();
    await new Promise((resolve) => requestAnimationFrame(resolve));
  });

  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  expect(container.querySelector('[data-testid="skillhub-view"]')).not.toBeNull();
  expect(document.activeElement).toBe(skillHubEntry);
});

test('replays onboarding from the computer entry over an active conversation and hands off download', async () => {
  setCachedUser('2026-09-17T23:59:59Z');
  await act(async () => renderWorkspace());
  await openOnboardingReplayFromDesktopEntry();

  await act(async () => {
    [...document.querySelectorAll('button')]
      .find((button) => button.textContent.includes('下载桌面端')).click();
  });

  await vi.waitFor(() => expect(container.querySelector('[data-testid="desktop-connect-modal"]')?.dataset.mode).toBe('download'));
  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();
  expect(localStorage.getItem(workspaceOnboardingStorageKey(1))).toBe('dismissed');
});

test('shows the new-account onboarding card after the relay deep link is closed', async () => {
  setCachedUser('2026-09-18T00:00:01Z');

  await act(async () => {
    renderWorkspace({ pathname: '/', search: '?open=relay', hash: '' });
    await Promise.resolve();
    await Promise.resolve();
  });

  const relay = container.querySelector('[data-testid="relay-access-modal"]');
  expect(relay).not.toBeNull();
  expect(document.querySelector('#workspace-onboarding-title')).toBeNull();

  await act(async () => {
    relay.querySelector('button').click();
  });

  await vi.waitFor(() => expect(document.querySelector('#workspace-onboarding-title')).not.toBeNull());
});

test.each(['Escape', 'close button', 'cancel button'])('returns focus to the desktop profile entry after settings closes via %s', async (method) => {
  await act(async () => renderWorkspace());
  const trigger = container.querySelector('[aria-label="cats，打开个人菜单"]');
  expect(trigger).not.toBeNull();
  trigger.focus();
  await act(async () => trigger.click());
  const settings = [...document.querySelectorAll('[role="menuitem"]')]
    .find((item) => item.textContent.includes('设置与资料'));
  settings.focus();
  await act(async () => settings.click());
  expect(settings.isConnected).toBe(false);
  const dialog = container.querySelector('.oc-profile-editor-modal');
  expect(dialog).not.toBeNull();
  expect(dialog.contains(document.activeElement)).toBe(true);
  await act(async () => {
    if (method === 'Escape') {
      document.activeElement.dispatchEvent(new KeyboardEvent('keydown', {
        key: 'Escape', bubbles: true, cancelable: true,
      }));
    } else {
      dialog.querySelector(method === 'close button'
        ? '.oc-profile-editor-close' : '.oc-profile-editor-actions .oc-btn-default').click();
    }
  });
  expect(container.querySelector('.oc-profile-editor-modal')).toBeNull();
  expect(document.activeElement).toBe(trigger);
  expect(trigger.closest('[inert]')).toBeNull();
  expect(document.querySelector('[aria-label="账号菜单"]')).toBeNull();
});

test.each([{ ctrlKey: true }, { metaKey: true }])('keeps search shortcuts inside an active modal workflow (%j)', async (modifier) => {
  await act(async () => renderWorkspace());
  const modal = document.createElement('section');
  modal.setAttribute('role', 'dialog');
  modal.setAttribute('aria-modal', 'true');
  document.body.appendChild(modal);
  try {
    const blocked = new KeyboardEvent('keydown', { key: 'k', ...modifier, bubbles: true, cancelable: true });
    await act(async () => document.dispatchEvent(blocked));
    expect(blocked.defaultPrevented).toBe(true);
    expect(container.querySelector('.cc-global-search')).toBeNull();
  } finally {
    modal.remove();
  }
  await act(async () => document.dispatchEvent(new KeyboardEvent('keydown', {
    key: 'k', ...modifier, bubbles: true, cancelable: true,
  })));
  expect(container.querySelector('.cc-global-search')).not.toBeNull();
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

test('restores a new-task draft when returning from SkillHub before a session exists', async () => {
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });

  const textarea = container.querySelector('textarea[aria-label="新任务草稿"]');
  expect(textarea).not.toBeNull();
  await act(async () => {
    textarea.value = 'new task draft survives SkillHub navigation';
    Simulate.change(textarea, { target: { value: textarea.value } });
  });

  expect(JSON.parse(sessionStorage.getItem('catsco_composer_drafts:v1:1')))
    .toMatchObject({
      inputDrafts: [['new-task', 'new task draft survives SkillHub navigation']],
    });

  await act(async () => {
    Simulate.click(container.querySelector('[aria-label="打开 SkillHub"]'));
    await Promise.resolve();
  });
  expect(container.querySelector('[data-testid="skillhub-view"]')).not.toBeNull();

  await act(async () => {
    Simulate.click([...container.querySelectorAll('button')]
      .find((button) => button.textContent === '选择 Agent 返回新任务'));
    await Promise.resolve();
  });

  expect(container.querySelector('textarea[aria-label="新任务草稿"]').value)
    .toBe('new task draft survives SkillHub navigation');

  await act(async () => root.unmount());
  root = createRoot(container);
  await act(async () => {
    renderWorkspace();
    await Promise.resolve();
  });

  expect(container.querySelector('textarea[aria-label="新任务草稿"]').value)
    .toBe('new task draft survives SkillHub navigation');
});

async function openUnboundApplication() {
  await act(async () => { renderWorkspace(); await Promise.resolve(); });
  await act(async () => {
    [...container.querySelectorAll('button')].find(b => b.textContent === '选择 Agent 返回新任务').click();
  });
  await act(async () => {
    [...container.querySelectorAll('button')].find(b => b.textContent === '打开新任务应用').click();
  });
}

async function startAnnotation() {
  await act(async () => {
    [...container.querySelectorAll('button')].find(b => b.textContent === '新建任务并标注').click();
    await Promise.resolve();
  });
}

test('creates a real task only on annotation intent, carries drafts and sends no placeholder', async () => {
  await openUnboundApplication();
  expect(api.createGroup).not.toHaveBeenCalled();
  const store = mocks.emptyProps.composerDraftStore;
  const textarea = container.querySelector('[aria-label="新任务草稿"]');
  await act(async () => {
    Simulate.change(textarea, { target: { value: 'unsent instructions' } });
    writeComposerAttachmentDraft(store, 'new-task', [{ name: 'draft.pdf', type: 'file' }]);
  });
  await startAnnotation();
  expect(api.createGroup).toHaveBeenCalledTimes(1);
  expect(api.createGroup).toHaveBeenCalledWith('unsent instructions', [7], { kind: 'agent_task' });
  expect(mocks.messageProps.topic).toBe('grp_901');
  expect(mocks.messageProps.cloudArtifactsRequest).toMatchObject({
    topicId: 'grp_901', agentUid: 7, startAnnotation: true, app: { id: 'board' },
  });
  expect(readComposerInputDraft(store, 'grp_901')).toBe('unsent instructions');
  expect(readComposerAttachmentDraft(store, 'grp_901')).toEqual([{ name: 'draft.pdf', type: 'file' }]);
  expect(readComposerInputDraft(store, 'new-task')).toBe('');
  expect(api.sendMessage).not.toHaveBeenCalled();
  expect(api.openAgent).not.toHaveBeenCalled();
});

test('normal send owns the same transition as new-task annotation', async () => {
  await openUnboundApplication();
  const lease = mocks.emptyProps.onAcquireTaskTransition();
  await startAnnotation();
  expect(api.createGroup).not.toHaveBeenCalled();
  expect(mocks.annotationError.message).toContain('正在创建任务或发送消息');
  lease.release();
  await startAnnotation();
  expect(api.createGroup).toHaveBeenCalledTimes(1);
});

test('switching topics while creation is pending cannot activate the old annotation task', async () => {
  await openUnboundApplication();
  let finish;
  api.createGroup.mockImplementation(() => new Promise(resolve => { finish = resolve; }));
  await startAnnotation();
  await selectTestConversation();
  await act(async () => {
    finish({ group: { id: 902 }, topic: 'grp_902' });
    await Promise.resolve();
  });
  expect(mocks.messageProps.topic).toBe('p2p_1_2');
  expect(api.sendMessage).not.toHaveBeenCalled();
});

test('task creation failure keeps the application and permits retry', async () => {
  await openUnboundApplication();
  api.createGroup.mockRejectedValueOnce(new Error('offline'));
  await startAnnotation();
  expect(mocks.annotationError.message).toBe('offline');
  expect(container.querySelector('[aria-label="新任务草稿"]')).not.toBeNull();
  await startAnnotation();
  expect(api.createGroup).toHaveBeenCalledTimes(2);
  expect(mocks.messageProps.topic).toBe('grp_901');
});
