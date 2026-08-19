import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../widgets/create-group', () => ({
  default: function MockCreateGroup({
    mode = 'create',
    initialName = '',
    lockedMemberIds = [],
    onCreate,
    onCreated,
  }) {
    const [error, setError] = React.useState('');
    return (
      <div
        data-testid="create-group-modal"
        data-mode={mode}
        data-initial-name={initialName}
        data-locked-members={lockedMemberIds.join(',')}
      >
        创建群聊弹窗
        {onCreate && (
          <button
            type="button"
            onClick={async () => {
              try {
                const created = await onCreate(initialName, [...lockedMemberIds, 8]);
                onCreated?.(created);
              } catch (createError) {
                setError(createError?.message || '创建失败');
              }
            }}
          >
            测试升级
          </button>
        )}
        {error && <span role="alert">{error}</span>}
      </div>
    );
  },
}));

vi.mock('../widgets/add-friend', () => ({
  default: function MockAddFriend({ onClose }) {
    return (
      <div data-testid="add-friend-modal">
        <button type="button" onClick={onClose}>关闭添加好友</button>
      </div>
    );
  },
}));

vi.mock('../widgets/friend-request', () => ({
  default: function MockFriendRequest() {
    return null;
  },
}));

vi.mock('../widgets/agent-store-modal', () => ({
  default: function MockAgentStoreModal({ initialAgentId, onClose, onOpenSkillHub }) {
    return (
      <div data-testid="agent-store-modal" data-initial-agent-id={initialAgentId ?? ''}>
        <button type="button" onClick={() => onOpenSkillHub?.(initialAgentId)}>管理能力</button>
        <button type="button" onClick={onClose}>关闭助手管理</button>
      </div>
    );
  },
}));

vi.mock('../widgets/mobile-channel-bind-modal', () => ({
  default: function MockMobileChannelBindModal({ agentName, groupId, topicId, groupName, onClose }) {
    return (
      <div data-testid="mobile-channel-modal">
        <strong>移动端使用</strong>
        <span>{agentName}</span>
        <span>{groupName}</span>
        <span data-testid="mobile-channel-group-id">{groupId || ''}</span>
        <span data-testid="mobile-channel-topic-id">{topicId || ''}</span>
        <button type="button" onClick={onClose}>关闭移动端</button>
      </div>
    );
  },
}));

vi.mock('../api', () => ({
  api: {
    getConversations: vi.fn(),
    getFriends: vi.fn(),
    getGroups: vi.fn(),
    getPendingRequests: vi.fn(),
    getAgents: vi.fn(),
    getProjects: vi.fn(),
    createProject: vi.fn(),
    renameProject: vi.fn(),
    deleteProject: vi.fn(),
    assignProjectTopic: vi.fn(),
    removeProjectTopic: vi.fn(),
    openAgent: vi.fn(),
    acceptAgentFriend: vi.fn(),
    rejectAgentFriend: vi.fn(),
    acceptFriend: vi.fn(),
    rejectFriend: vi.fn(),
    removeFriend: vi.fn(),
    blockUser: vi.fn(),
    createGroup: vi.fn(),
    disbandGroup: vi.fn(),
    updateGroup: vi.fn(),
    updateConversationTitle: vi.fn(),
    setConversationNotificationsMuted: vi.fn(),
  },
  onWSMessage: vi.fn(() => vi.fn()),
  updateTopicSeq: vi.fn(),
}));

import ChatListView, { shouldAutoCollapseSidebarSection } from './sidepanel-view';
import { api, onWSMessage } from '../api';

const user = {
  uid: 7,
  username: 'bruce',
  display_name: '布鲁斯',
};

describe('ChatListView sidebar sections', () => {
  let container;
  let root;
  let onSelectTopic;
  let onStartAgentTask;
  let onOpenSearch;
  let wsHandler;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
    api.getConversations.mockResolvedValue({ conversations: [] });
    api.getFriends.mockResolvedValue({ friends: [] });
    api.getGroups.mockResolvedValue({ groups: [] });
    api.getPendingRequests.mockResolvedValue({ requests: [] });
    api.updateConversationTitle.mockResolvedValue({ ok: true });
    api.getAgents.mockResolvedValue({
      agents: [
        {
          id: 42,
          uid: 42,
          username: 'dev-agent',
          display_name: 'Dev Agent',
          avatar_url: '/uploads/dev.png',
          topic_id: 'p2p_7_42',
          is_online: true,
          relation: 'owner',
          is_owner: true,
        },
      ],
    });
    api.getProjects.mockResolvedValue({ projects: [] });
    api.createProject.mockResolvedValue({ project: { id: 1, name: 'New Project', task_count: 0 } });
    api.renameProject.mockResolvedValue({ ok: true });
    api.deleteProject.mockResolvedValue({ ok: true });
    api.assignProjectTopic.mockResolvedValue({ ok: true });
    api.removeProjectTopic.mockResolvedValue({ ok: true });
    api.openAgent.mockResolvedValue({
      agent: {
        uid: 42,
        display_name: 'Dev Agent',
        avatar_url: '/uploads/dev.png',
        topic_id: 'p2p_7_42',
      },
      topic: 'p2p_7_42',
    });
    api.acceptAgentFriend.mockResolvedValue({ ok: true });
    api.rejectAgentFriend.mockResolvedValue({ ok: true });
    api.removeFriend.mockResolvedValue({ ok: true });
    api.blockUser.mockResolvedValue({ ok: true });
    api.createGroup.mockResolvedValue({
      group_id: 77,
      topic: 'grp_77',
      name: 'New Agent Task',
      kind: 'agent_task',
      is_agent_task: true,
      group: { id: 77, name: 'New Agent Task', kind: 'agent_task', is_agent_task: true },
    });
    api.updateGroup.mockResolvedValue({ ok: true });
    api.setConversationNotificationsMuted.mockResolvedValue({ notifications_muted: false });
    wsHandler = null;
    onWSMessage.mockImplementation((handler) => {
      wsHandler = handler;
      return vi.fn();
    });
    onSelectTopic = vi.fn();
    onStartAgentTask = vi.fn();
    onOpenSearch = vi.fn();

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

  async function mount(props = {}) {
    await act(async () => {
      root.render(
        <ChatListView
          activeTopic={null}
          onSelectTopic={onSelectTopic}
          onStartAgentTask={onStartAgentTask}
          onOpenSearch={onOpenSearch}
          user={user}
          onlineUsers={{}}
          {...props}
        />
      );
      await Promise.resolve();
    });
  }

  async function remount(props = {}) {
    await act(async () => {
      root.unmount();
    });
    root = createRoot(container);
    await mount(props);
  }

  function clickSection(label) {
    const section = Array.from(container.querySelectorAll('.v3-chat-section span'))
      .find((node) => node.textContent.includes(label));
    expect(section).toBeTruthy();
    Simulate.click(section);
  }

  it('keeps only tasks, contacts, and projects as top-level sections', async () => {
    await mount();

    const sectionLabels = Array.from(container.querySelectorAll('.cc-top-level-section > .cc-section-toggle'))
      .map((button) => button.textContent.trim());
    expect(sectionLabels).toEqual(['历史任务', '联系人', '项目']);
    expect(sectionLabels).not.toContain('协作');
    expect(sectionLabels).not.toContain('好友');
    expect(sectionLabels).not.toContain('群聊');
    expect(sectionLabels).not.toContain('Agent 助手');
    Array.from(container.querySelectorAll('.cc-top-level-section')).forEach((section) => {
      expect(section.classList.contains('cc-sidebar-section-row')).toBe(true);
      expect(section.dataset.expanded).toBe(
        section.querySelector('.cc-section-toggle').getAttribute('aria-expanded'),
      );
    });

    const [tasksToggle, contactsToggle] = container.querySelectorAll('.cc-top-level-section > .cc-section-toggle');
    expect(tasksToggle.querySelector('.lucide-message-square')).toBeNull();
    expect(contactsToggle.querySelector('.lucide-user-round')).toBeNull();
  });

  it('recognizes real sticky lanes and the reachable scroll end as auto-collapse boundaries', () => {
    expect(shouldAutoCollapseSidebarSection({
      scrollTop: 260,
      scrollHeight: 1130,
      clientHeight: 691,
      scrollViewportTop: 160,
      nextSectionTop: 199.5,
      nextSectionStickyTop: 39,
    })).toBe(true);

    expect(shouldAutoCollapseSidebarSection({
      scrollTop: 439,
      scrollHeight: 1130,
      clientHeight: 691,
      scrollViewportTop: 160,
      nextSectionTop: 264.7,
      nextSectionStickyTop: 39,
    })).toBe(true);

    expect(shouldAutoCollapseSidebarSection({
      scrollTop: 300,
      scrollHeight: 1130,
      clientHeight: 691,
      scrollViewportTop: 160,
      nextSectionTop: 264.7,
      nextSectionStickyTop: 39,
    })).toBe(false);
  });

  it('adds extra section spacing only after expanded section content', async () => {
    await mount();

    const contactsSection = container.querySelector('.cc-contacts-section');
    const projectsSection = container.querySelector('.cc-project-section');
    const tasksSection = container.querySelector('.cc-conversation-section');

    expect(projectsSection.classList.contains('cc-section-after-expanded-content')).toBe(true);
    expect(tasksSection.classList.contains('cc-section-after-expanded-content')).toBe(true);

    await act(async () => {
      Simulate.click(contactsSection.querySelector('.cc-section-toggle'));
    });
    expect(projectsSection.classList.contains('cc-section-after-expanded-content')).toBe(false);
    expect(tasksSection.classList.contains('cc-section-after-expanded-content')).toBe(true);

    await act(async () => {
      Simulate.click(projectsSection.querySelector('.cc-section-toggle'));
    });
    expect(tasksSection.classList.contains('cc-section-after-expanded-content')).toBe(false);
  });

  it('places a compact friend-request count immediately before contact actions', async () => {
    api.getAgents.mockResolvedValue({ agents: [] });
    api.getPendingRequests.mockResolvedValue({
      requests: [{ id: 91, from_user_id: 8, display_name: 'Alice' }],
    });

    await mount();

    const section = container.querySelector('.cc-contacts-section');
    const toggle = section.querySelector('.cc-section-toggle');
    const badge = section.querySelector('.cc-section-request-badge');
    const moreButton = section.querySelector('.cc-contact-section-menu-trigger');

    expect(badge).toBeTruthy();
    expect(badge.textContent).toBe('1');
    expect(badge.parentElement).toBe(section);
    expect(toggle.contains(badge)).toBe(false);
    expect(badge.nextElementSibling).toBe(moreButton);
  });

  it('refreshes incoming friend requests immediately after a friend websocket event', async () => {
    api.getAgents.mockResolvedValue({ agents: [] });
    api.getPendingRequests.mockResolvedValue({ requests: [] });

    await mount();
    expect(container.querySelector('.cc-section-request-badge')).toBeFalsy();

    api.getPendingRequests.mockResolvedValue({
      requests: [{ id: 92, from_user_id: 9, display_name: 'Realtime Friend' }],
    });
    await act(async () => {
      wsHandler({
        friend: {
          action: 'request',
          from: 9,
          to: 7,
          msg: 'Hello',
        },
      });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelector('.cc-section-request-badge')?.textContent).toBe('1');
    expect(api.getFriends).toHaveBeenCalledTimes(2);
    expect(api.getPendingRequests).toHaveBeenCalledTimes(2);
    expect(api.getAgents).toHaveBeenCalledTimes(2);
  });

  it('runs one trailing friend sync when websocket events arrive during an active refresh', async () => {
    api.getAgents.mockResolvedValue({ agents: [] });
    api.getPendingRequests.mockResolvedValue({ requests: [] });
    await mount();

    let resolveFirstFriends;
    api.getFriends.mockClear();
    api.getPendingRequests.mockClear();
    api.getAgents.mockClear();
    api.getFriends
      .mockImplementationOnce(() => new Promise((resolve) => {
        resolveFirstFriends = resolve;
      }))
      .mockResolvedValue({ friends: [] });
    api.getPendingRequests
      .mockResolvedValueOnce({
        requests: [{ id: 94, from_user_id: 11, display_name: 'First Friend' }],
      })
      .mockResolvedValue({
        requests: [
          { id: 94, from_user_id: 11, display_name: 'First Friend' },
          { id: 95, from_user_id: 12, display_name: 'Second Friend' },
        ],
      });
    api.getAgents.mockResolvedValue({ agents: [] });

    await act(async () => {
      wsHandler({ friend: { action: 'request', from: 11, to: 7 } });
      wsHandler({ friend: { action: 'request', from: 12, to: 7 } });
      resolveFirstFriends({ friends: [] });
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getFriends).toHaveBeenCalledTimes(2);
    expect(api.getPendingRequests).toHaveBeenCalledTimes(2);
    expect(api.getAgents).toHaveBeenCalledTimes(2);
    expect(container.querySelector('.cc-section-request-badge')?.textContent).toBe('2');
  });

  it('recovers missed owned-Agent requests when the websocket reconnects', async () => {
    api.getPendingRequests.mockResolvedValue({ requests: [] });
    await mount();

    api.getPendingRequests.mockClear();
    api.getPendingRequests.mockImplementation((agentUID) => Promise.resolve({
      requests: agentUID === 42
        ? [{ id: 93, from_user_id: 10, display_name: 'Agent Friend' }]
        : [],
    }));

    await act(async () => {
      wsHandler({ _type: 'ws_open' });
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getPendingRequests).toHaveBeenCalledWith();
    expect(api.getPendingRequests).toHaveBeenCalledWith(42);
    expect(container.querySelector('.cc-section-request-badge')?.textContent).toBe('1');
  });

  it('keeps all group work in tasks and people in contacts', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Review Agent Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-07-18T10:00:00Z',
        },
        {
          id: 'p2p_7_8',
          friend_id: 8,
          name: 'Alice',
          is_group: false,
          is_bot: false,
          preview: '这段好友消息摘要不应显示',
          last_time: '2026-07-18T09:00:00Z',
        },
        {
          id: 'grp_9',
          group_id: 9,
          name: 'Design Team',
          is_group: true,
          last_time: '2026-07-18T08:00:00Z',
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });
    api.getGroups.mockResolvedValue({
      groups: [{ id: 9, name: 'Design Team', owner_id: 7 }],
    });

    await mount();

    const soloTask = container.querySelector('[data-task-kind="solo"]');
    expect(soloTask).toBeTruthy();
    expect(soloTask.textContent).toContain('Review Agent Task');
    expect(soloTask.querySelector('svg.lucide-zap')).toBeTruthy();
    expect(soloTask.querySelector('.cc-item-kind')).toBeFalsy();
    expect(soloTask.textContent).not.toContain('单人');

    const collaborationTask = container.querySelector('[data-task-kind="collaboration"]');
    expect(collaborationTask).toBeTruthy();
    expect(collaborationTask.textContent).toContain('Design Team');
    expect(collaborationTask.textContent).toContain('协作');
    expect(collaborationTask.querySelector('svg.lucide-zap')).toBeTruthy();

    expect(container.querySelector('[data-conversation-kind="direct"]')).toBeFalsy();
    expect(container.querySelector('[data-conversation-kind="group"]')).toBeFalsy();
    const friendContact = container.querySelector('[data-contact-kind="friend"]');
    expect(friendContact?.querySelector('svg.lucide-user-round')).toBeTruthy();
    expect(friendContact?.querySelector('.cc-item-kind')).toBeFalsy();
    expect(friendContact?.textContent).not.toContain('这段好友消息摘要不应显示');
    expect(friendContact?.textContent).not.toContain('@alice');
    expect(friendContact?.querySelector('.v3-chat-item-identity')).toBeNull();
    expect(container.querySelector('[data-contact-kind="group"]')).toBeFalsy();
    const agentContact = container.querySelector('[data-contact-kind="agent"]');
    expect(agentContact?.querySelector('svg.cc-agent-contact-icon')).toBeTruthy();
    expect(agentContact?.querySelector('.cc-item-kind')).toBeFalsy();
    expect(agentContact?.textContent).not.toContain('@dev-agent');
    expect(agentContact?.querySelector('.v3-chat-item-identity')).toBeNull();
  });

  it('uses the shared relative time rules across task and contact rows', async () => {
    const now = new Date(2026, 6, 21, 18, 30);
    const nowSpy = vi.spyOn(Date, 'now').mockReturnValue(now.getTime());
    const localTimestamp = (day, hour = 12, minute = 0) => (
      new Date(2026, 6, day, hour, minute).toISOString()
    );
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Today Task',
          is_group: false,
          is_bot: true,
          last_time: localTimestamp(21, 9, 5),
        },
        {
          id: 'p2p_7_8',
          friend_id: 8,
          name: 'Yesterday Friend',
          is_group: false,
          is_bot: false,
          last_time: localTimestamp(20),
        },
        {
          id: 'grp_9',
          group_id: 9,
          name: 'Two Days Group',
          is_group: true,
          member_count: 3,
          last_time: localTimestamp(19),
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'One Week Task',
          is_group: false,
          is_bot: true,
          last_time: localTimestamp(14),
        },
        {
          id: 'p2p_7_44',
          friend_id: 44,
          name: 'Older Task',
          is_group: false,
          is_bot: true,
          last_time: localTimestamp(13),
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'yesterday-friend', display_name: 'Yesterday Friend' }],
    });

    try {
      await mount();

      const rowFor = (name) => Array.from(container.querySelectorAll('.v3-chat-item'))
        .find((row) => row.textContent.includes(name));
      const timeFor = (name) => rowFor(name)?.querySelector('.cc-chat-row-time')?.textContent;

      expect(timeFor('Today Task')).toBe('09:05');
      expect(timeFor('Yesterday Friend')).toBe('昨天');
      expect(timeFor('Two Days Group')).toBe('两天前');
      expect(timeFor('One Week Task')).toBe('一周前');
      expect(timeFor('Older Task')).toBe('7月13日');
    } finally {
      nowSpy.mockRestore();
    }
  });

  it('shows only unassigned solo and collaboration tasks in the loose task list', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Loose Solo Task',
          is_group: false,
          is_bot: true,
          project_id: '0',
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'Filed Solo Task',
          is_group: false,
          is_bot: true,
          project_id: '12',
          project_name: 'Website',
        },
        {
          id: 'grp_20',
          group_id: 20,
          name: 'Loose Collaboration Task',
          is_group: true,
          project_id: '0',
        },
        {
          id: 'grp_21',
          group_id: 21,
          name: 'Filed Collaboration Task',
          is_group: true,
          project_id: '12',
          project_name: 'Website',
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [
        { id: 20, name: 'Loose Collaboration Task', owner_id: 7 },
        { id: 21, name: 'Filed Collaboration Task', owner_id: 7 },
      ],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website', task_count: 2 }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const looseTasks = Array.from(container.querySelectorAll('.cc-conversation-item[data-task-kind]'));
    expect(looseTasks.map((row) => row.textContent)).toEqual(expect.arrayContaining([
      expect.stringContaining('Loose Solo Task'),
      expect.stringContaining('Loose Collaboration Task'),
    ]));
    expect(looseTasks.some((row) => row.textContent.includes('Filed Solo Task'))).toBe(false);
    expect(looseTasks.some((row) => row.textContent.includes('Filed Collaboration Task'))).toBe(false);
    expect(looseTasks.map((row) => row.dataset.taskKind).sort()).toEqual(['collaboration', 'solo']);
    const projectRow = container.querySelector('.cc-project-row');
    expect(projectRow.classList.contains('cc-sidebar-item-row')).toBe(true);
    expect(projectRow.dataset.sidebarLevel).toBe('1');
    expect(projectRow.querySelector('.cc-sidebar-row-trailing')).toBeTruthy();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Website"]'));
    });

    const projectTasks = Array.from(container.querySelectorAll('.cc-project-task-item'));
    expect(projectTasks).toHaveLength(2);
    expect(projectTasks.map((row) => row.textContent).join('|')).toContain('Filed Solo Task');
    expect(projectTasks.map((row) => row.textContent).join('|')).toContain('Filed Collaboration Task');
    expect(projectTasks.map((row) => row.dataset.taskKind).sort()).toEqual(['collaboration', 'solo']);
    projectTasks.forEach((row) => {
      expect(row.classList.contains('cc-sidebar-item-row')).toBe(true);
      expect(row.dataset.sidebarLevel).toBe('2');
    });
  });

  it('keeps multiple project folders expanded independently', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Alpha Task',
          is_group: false,
          is_bot: true,
          project_id: 12,
          project_name: 'Alpha',
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'Beta Task',
          is_group: false,
          is_bot: true,
          project_id: 13,
          project_name: 'Beta',
        },
      ],
    });
    api.getProjects.mockResolvedValue({
      projects: [
        { id: 12, name: 'Alpha', task_count: 1 },
        { id: 13, name: 'Beta', task_count: 1 },
      ],
    });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Alpha"]'));
      Simulate.click(container.querySelector('[aria-label="打开项目 Beta"]'));
    });

    expect(container.querySelector('[aria-label="收起项目 Alpha"]').getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('[aria-label="收起项目 Beta"]').getAttribute('aria-expanded')).toBe('true');
    expect(container.textContent).toContain('Alpha Task');
    expect(container.textContent).toContain('Beta Task');

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="收起项目 Alpha"]'));
    });

    expect(container.querySelector('[aria-label="打开项目 Alpha"]').getAttribute('aria-expanded')).toBe('false');
    expect(container.querySelector('[aria-label="收起项目 Beta"]').getAttribute('aria-expanded')).toBe('true');
    expect(container.textContent).not.toContain('Alpha Task');
    expect(container.textContent).toContain('Beta Task');
  });

  it('allows a joined collaboration group to be assigned to a personal project', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'grp_77',
        group_id: 77,
        name: 'Member Collaboration',
        is_group: true,
        is_agent_task: true,
        has_bot: true,
        member_count: 3,
      }],
    });
    api.getGroups.mockResolvedValue({
      groups: [{
        id: 77,
        name: 'Member Collaboration',
        owner_id: 99,
        kind: 'agent_task',
        is_agent_task: true,
        has_bot: true,
        member_count: 3,
      }],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website', task_count: 0 }],
    });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Member Collaboration 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="加入项目 Member Collaboration"]'));
    });
    const dialog = document.body.querySelector('[role="dialog"][aria-label="选择项目"]');
    expect(dialog).toBeTruthy();

    await act(async () => {
      Simulate.click(Array.from(dialog.querySelectorAll('.cc-new-task-agent'))
        .find((button) => button.textContent.includes('Website')));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.assignProjectTopic).toHaveBeenCalledWith(12, 'grp_77');
  });

  it('shows a friend without conversation history in contacts and opens a synthetic direct topic', async () => {
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const contact = container.querySelector('[data-contact-kind="friend"]');
    expect(contact).toBeTruthy();
    expect(contact.textContent).toContain('Alice');
    expect(container.querySelector('[data-conversation-kind="direct"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(contact);
    });

    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'p2p_7_8',
      name: 'Alice',
      friendId: 8,
      isGroup: false,
    }));
  });

  it('shows unread friend dots while contacts are collapsed and clears them after opening the friend', async () => {
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice', topic_id: 'p2p_7_8' }],
    });
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_8',
        friend_id: 8,
        name: 'Alice',
        is_group: false,
        is_bot: false,
        last_time: '2026-07-20T07:30:00Z',
      }],
    });
    await mount();

    const contactsToggle = container.querySelector('.cc-contacts-section .cc-section-toggle');
    await act(async () => {
      Simulate.click(contactsToggle);
    });
    expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');

    await act(async () => {
      wsHandler({ data: { topic: 'p2p_7_42', from: 'usr42', content: 'Agent reply', seq: 11 } });
    });
    expect(container.querySelector('.cc-section-unread-dot')).toBeFalsy();

    await act(async () => {
      wsHandler({ data: { topic: 'p2p_7_8', from: 'usr8', content: 'New message', seq: 12 } });
    });
    const sectionUnreadDot = container.querySelector('.cc-contacts-section .cc-section-unread-dot');
    const sectionChevron = container.querySelector('.cc-contacts-section .cc-section-toggle .lucide-chevron-right');
    expect(sectionUnreadDot).toBeTruthy();
    expect(sectionChevron.compareDocumentPosition(sectionUnreadDot) & Node.DOCUMENT_POSITION_FOLLOWING)
      .toBeTruthy();
    expect(container.querySelector('[data-contact-kind="friend"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(contactsToggle);
    });
    expect(container.querySelector('.cc-contacts-section .cc-section-unread-dot')).toBeTruthy();
    const friend = container.querySelector('[data-contact-kind="friend"]');
    expect(friend.dataset.unread).toBe('true');
    expect(friend.querySelector('.cc-friend-unread-dot')).toBeTruthy();
    expect(friend.querySelector('.cc-chat-row-time')).toBeFalsy();

    await act(async () => {
      Simulate.click(friend);
    });
    expect(friend.dataset.unread).toBeUndefined();
    expect(friend.querySelector('.cc-friend-unread-dot')).toBeFalsy();
    expect(friend.querySelector('.cc-chat-row-time')).toBeTruthy();
    expect(container.querySelector('.cc-section-unread-dot')).toBeFalsy();
    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({ topicId: 'p2p_7_8' }));
  });

  it('migrates legacy AI and collaboration collapsed keys to the unified sections', async () => {
    localStorage.setItem('cc_sidebar_collapsed_v1:7', JSON.stringify({
      ai: true,
      collaboration: true,
      projects: false,
    }));
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Legacy Agent Task',
        is_group: false,
        is_bot: true,
      }],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });

    await mount();

    const sectionToggles = Array.from(container.querySelectorAll('.cc-top-level-section > .cc-section-toggle'));
    const conversationsToggle = sectionToggles.find((button) => button.textContent.includes('任务'));
    const contactsToggle = sectionToggles.find((button) => button.textContent.includes('联系人'));
    const projectsToggle = sectionToggles.find((button) => button.textContent.includes('项目'));
    expect(conversationsToggle.getAttribute('aria-expanded')).toBe('false');
    expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');
    expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
    expect(container.textContent).not.toContain('Legacy Agent Task');
    expect(container.textContent).not.toContain('Alice');
  });

  it('starts an unsent Agent task draft from the assistant roster', async () => {
    await mount();

    expect(container.textContent).toContain('联系人');
    expect(container.textContent).toContain('Dev Agent');
    const agentItem = container.querySelector('[data-contact-kind="agent"]');
    expect(agentItem).toBeTruthy();
    expect(agentItem.querySelector('svg.cc-agent-contact-icon.online')).toBeTruthy();
    expect(agentItem.querySelector('.v3-status-dot')).toBeFalsy();
    expect(agentItem.querySelector('.cc-item-kind')).toBeFalsy();

    await act(async () => {
      Simulate.click(agentItem);
      await Promise.resolve();
    });

    expect(onStartAgentTask).toHaveBeenCalledWith(expect.objectContaining({
      uid: 42,
      display_name: 'Dev Agent',
    }));
    expect(api.openAgent).not.toHaveBeenCalled();
    expect(api.createGroup).not.toHaveBeenCalled();
    expect(onSelectTopic).not.toHaveBeenCalled();
  });

  it('starts a fresh draft even when the Agent already has an existing task', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: '前端迁移复盘',
          is_group: false,
          is_bot: true,
        },
      ],
    });

    await mount({ activeTopic: 'p2p_7_42' });

    const agentItem = container.querySelector('.cc-agent-roster-item');
    expect(agentItem).toBeTruthy();
    expect(agentItem.classList.contains('active')).toBe(false);
    expect(container.querySelector('.cc-history-item.active')).toBeTruthy();

    await act(async () => {
      Simulate.click(agentItem);
      await Promise.resolve();
    });

    expect(onStartAgentTask).toHaveBeenCalledWith(expect.objectContaining({ uid: 42 }));
    expect(api.openAgent).not.toHaveBeenCalled();
    expect(onSelectTopic).not.toHaveBeenCalled();
  });

  it('opens mobile binding from an assistant row without opening the conversation', async () => {
    await mount();

    const actionButton = container.querySelector('[aria-label="Dev Agent 任务操作"]');
    expect(actionButton).toBeTruthy();
    expect(container.querySelector('[aria-label="Dev Agent 移动端使用"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(actionButton);
    });

    const actionMenu = document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]');
    const mobileButton = Array.from(actionMenu.querySelectorAll('[role="menuitem"]'))
      .find((item) => item.textContent.includes('移动端使用'));
    expect(mobileButton).toBeTruthy();
    expect(actionMenu.textContent).not.toContain('移除助手');

    await act(async () => {
      Simulate.click(mobileButton);
      await Promise.resolve();
    });

    expect(api.openAgent).not.toHaveBeenCalled();
    expect(container.querySelector('[data-testid="mobile-channel-modal"]')).toBeFalsy();
    expect(document.body.querySelector('[data-testid="mobile-channel-modal"]')?.textContent).toContain('移动端使用');
    expect(document.body.querySelector('[data-testid="mobile-channel-modal"]')?.textContent).toContain('Dev Agent');
  });

  it('opens the selected owned assistant directly in its management panel', async () => {
    await mount();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Dev Agent 任务操作"]'));
    });

    const actionMenu = document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]');
    const manageButton = Array.from(actionMenu.querySelectorAll('[role="menuitem"]'))
      .find((item) => item.textContent.includes('管理 Agent'));
    expect(manageButton).toBeTruthy();

    await act(async () => {
      Simulate.click(manageButton);
    });

    const modal = document.body.querySelector('[data-testid="agent-store-modal"]');
    expect(modal).toBeTruthy();
    expect(modal.dataset.initialAgentId).toBe('42');
    expect(onStartAgentTask).not.toHaveBeenCalled();
  });

  it('forwards the managed assistant to SkillHub and closes assistant management', async () => {
    const onOpenSkillHub = vi.fn();
    await mount({ onOpenSkillHub });

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Dev Agent 任务操作"]'));
    });
    const actionMenu = document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]');
    await act(async () => {
      Simulate.click(Array.from(actionMenu.querySelectorAll('[role="menuitem"]'))
        .find((item) => item.textContent.includes('管理 Agent')));
    });
    await act(async () => {
      Simulate.click(Array.from(document.body.querySelectorAll('[data-testid="agent-store-modal"] button'))
        .find((button) => button.textContent.includes('管理能力')));
    });

    expect(onOpenSkillHub).toHaveBeenCalledWith(42, undefined);
    expect(document.body.querySelector('[data-testid="agent-store-modal"]')).toBeNull();
  });

  it('portals the assistant task menu, supports arrow keys, and restores focus on Escape', async () => {
    await mount();

    const trigger = container.querySelector('[aria-label="Dev Agent 任务操作"]');
    trigger.getBoundingClientRect = () => ({
      bottom: 784,
      height: 32,
      left: 280,
      right: 312,
      top: 752,
      width: 32,
      x: 280,
      y: 752,
      toJSON: () => ({}),
    });
    trigger.focus();

    await act(async () => Simulate.click(trigger));

    const menu = document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]');
    expect(menu).not.toBeNull();
    expect(menu.parentElement).toBe(document.body);
    expect(menu.style.position).toBe('fixed');
    expect(menu.dataset.placement).toBe('top');
    const items = Array.from(menu.querySelectorAll('[role="menuitem"]'));
    expect(document.activeElement).toBe(items[0]);

    await act(async () => Simulate.keyDown(items[0], { key: 'ArrowDown' }));
    expect(document.activeElement).toBe(items[1]);
    await act(async () => new Promise((resolve) => window.setTimeout(resolve, 20)));
    expect(document.activeElement).toBe(items[1]);

    await act(async () => Simulate.keyDown(items[1], { key: 'Escape' }));
    expect(document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    await act(async () => Simulate.click(trigger));
    let reopenedMenu = document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]');
    await act(async () => Simulate.keyDown(reopenedMenu.querySelector('[role="menuitem"]'), { key: 'Tab' }));
    expect(document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    await act(async () => Simulate.click(trigger));
    reopenedMenu = document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]');
    await act(async () => Simulate.keyDown(
      reopenedMenu.querySelector('[role="menuitem"]'),
      { key: 'Tab', shiftKey: true },
    ));
    expect(document.body.querySelector('[role="menu"][aria-label="Dev Agent 任务操作"]')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('opens mobile binding from a group row without opening the group conversation', async () => {
    api.getGroups.mockResolvedValue({
      groups: [
        {
          id: 88,
          name: 'Virtual Team',
          topic_id: 'grp_88',
          owner_id: 7,
        },
      ],
    });

    await mount();

    const moreButton = container.querySelector('[aria-label="Virtual Team 更多操作"]');
    expect(moreButton).toBeTruthy();
    await act(async () => {
      Simulate.click(moreButton);
    });
    const mobileButton = container.querySelector('[aria-label="Virtual Team 手机扫码"]');
    expect(mobileButton).toBeTruthy();

    await act(async () => {
      Simulate.click(mobileButton);
      await Promise.resolve();
    });

    expect(onSelectTopic).not.toHaveBeenCalled();
    expect(container.querySelector('[data-testid="mobile-channel-modal"]')).toBeFalsy();
    expect(document.body.querySelector('[data-testid="mobile-channel-modal"]')?.textContent).toContain('移动端使用');
    expect(document.body.querySelector('[data-testid="mobile-channel-modal"]')?.textContent).toContain('Virtual Team');
    expect(document.body.querySelector('[data-testid="mobile-channel-group-id"]').textContent).toBe('88');
    expect(document.body.querySelector('[data-testid="mobile-channel-topic-id"]').textContent).toBe('grp_88');
  });

  it('uses task controls on a group row and keeps group actions in its task menu', async () => {
    api.getGroups.mockResolvedValue({
      groups: [{ id: 88, name: 'Virtual Team', topic_id: 'grp_88', owner_id: 7 }],
    });
    api.disbandGroup.mockResolvedValue({ ok: true });
    const onManageGroup = vi.fn();
    window.confirm = vi.fn(() => true);

    await mount({ onManageGroup });

    const row = Array.from(container.querySelectorAll('.v3-chat-item'))
      .find((node) => node.textContent.includes('Virtual Team'));
    expect(row.querySelectorAll('.cc-chat-row-actions button')).toHaveLength(2);
    expect(row.querySelector('[aria-label="置顶任务 Virtual Team"]')).toBeTruthy();

    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="Virtual Team 更多操作"]'));
    });
    expect(row.querySelector('[role="menu"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="修改任务名称 Virtual Team"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="加入项目 Virtual Team"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="Virtual Team 手机扫码"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="Virtual Team 协作管理"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="删除任务 Virtual Team"]')).toBeTruthy();

    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="Virtual Team 协作管理"]'));
    });
    expect(onManageGroup).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'grp_88',
      groupId: 88,
      name: 'Virtual Team',
      isGroup: true,
    }));
    expect(onSelectTopic).not.toHaveBeenCalled();

    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="Virtual Team 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="删除任务 Virtual Team"]'));
      await Promise.resolve();
    });
    expect(api.disbandGroup).toHaveBeenCalledWith(88);
  });

  it('keeps history tasks compact and exposes pin beside the three-dot menu', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Review Task',
        preview: 'This preview should not be rendered in history.',
        is_group: false,
        is_bot: true,
        last_time: '2026-06-08T08:00:00Z',
        task_status: {
          topic_id: 'p2p_7_42',
          state: 'running',
          updated_at: '2026-06-08T08:00:01Z',
        },
      }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });
    const onOpenMobileLink = vi.fn();
    const onDeleteHistoryTask = vi.fn().mockResolvedValue({ ok: true });
    window.confirm = vi.fn(() => true);

    await mount({ activeTopic: 'p2p_7_42', onOpenMobileLink, onDeleteHistoryTask });

    const row = container.querySelector('.cc-history-item');
    expect(row.querySelector('.cc-chat-row-preview')).toBeNull();
    expect(row.querySelectorAll('.cc-chat-row-actions button')).toHaveLength(2);
    expect(row.querySelector('[aria-label="置顶任务 Review Task"]')).toBeTruthy();
    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="Review Task 更多操作"]'));
    });
    expect(row.querySelector('[role="menu"] [aria-label="置顶任务 Review Task"]')).toBeNull();
    expect(row.querySelector('[aria-label="修改任务名称 Review Task"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="加入项目 Review Task"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="静音此会话 Review Task"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="Review Task 手机扫码"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="Review Task 协作管理"]')).toBeTruthy();
    expect(row.querySelector('[aria-label="删除任务 Review Task"]')).toBeTruthy();
    expect(row.querySelector('[role="menu"]').textContent).not.toContain('0');

    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="加入项目 Review Task"]'));
    });
    expect(document.body.textContent).toContain('将“Review Task”加入项目');
    expect(document.body.textContent).toContain('暂无可用项目');
    await act(async () => {
      Simulate.click(document.body.querySelector('[aria-label="关闭加入项目"]'));
    });

    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="Review Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="Review Task 手机扫码"]'));
    });
    expect(onOpenMobileLink).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'p2p_7_42',
      friendId: 42,
      isBot: true,
    }));
    expect(onSelectTopic).not.toHaveBeenCalled();

    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="Review Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(row.querySelector('[aria-label="删除任务 Review Task"]'));
      await Promise.resolve();
    });
    expect(onDeleteHistoryTask).toHaveBeenCalledWith(expect.objectContaining({ topicId: 'p2p_7_42' }));
    expect(onSelectTopic).toHaveBeenCalledWith(null);
  });

  it('upgrades a legacy single-Agent task after collaboration members are selected', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Review Task',
        is_group: false,
        is_bot: true,
        project_id: 5,
        project_name: 'Release',
      }],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 5, name: 'Release', task_count: 1 }],
    });
    api.createGroup.mockResolvedValue({
      group_id: 91,
      topic: 'grp_91',
      name: 'Review Task',
      has_bot: true,
      member_count: 3,
      group: {
        id: 91,
        name: 'Review Task',
        owner_id: 7,
        has_bot: true,
        member_count: 3,
        agent_ids: [42],
      },
    });
    api.disbandGroup.mockResolvedValue({ ok: true });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Release"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Review Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Review Task 协作管理"]'));
    });

    const upgradeDialog = document.body.querySelector('[data-testid="create-group-modal"]');
    expect(upgradeDialog?.dataset.mode).toBe('task_upgrade');
    expect(upgradeDialog?.dataset.initialName).toBe('Review Task');
    expect(upgradeDialog?.dataset.lockedMembers).toBe('42');

    await act(async () => {
      Simulate.click(upgradeDialog.querySelector('button'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.createGroup).toHaveBeenCalledWith('Review Task', [42, 8]);
    expect(api.assignProjectTopic).toHaveBeenCalledWith(5, 'grp_91');
    expect(api.removeProjectTopic).toHaveBeenCalledWith('p2p_7_42');
    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'grp_91',
      groupId: 91,
      isGroup: true,
      memberCount: 3,
    }));
    expect(JSON.parse(localStorage.getItem('cc_hidden_history_v1:7'))).toContain('p2p_7_42');
  });

  it('keeps the original task when collaboration members are only partially created', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Review Task',
        is_group: false,
        is_bot: true,
        project_id: 5,
        project_name: 'Release',
      }],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 5, name: 'Release', task_count: 1 }],
    });
    api.createGroup.mockResolvedValue({
      group_id: 91,
      topic: 'grp_91',
      name: 'Review Task',
      group: {
        id: 91,
        name: 'Review Task',
        owner_id: 7,
        member_count: 2,
        agent_ids: [42],
      },
    });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Release"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Review Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Review Task 协作管理"]'));
    });
    const upgradeDialog = document.body.querySelector('[data-testid="create-group-modal"]');
    await act(async () => {
      Simulate.click(upgradeDialog.querySelector('button'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(upgradeDialog.querySelector('[role="alert"]')?.textContent).toContain('部分协作成员添加失败');
    expect(api.disbandGroup).toHaveBeenCalledWith(91);
    expect(api.assignProjectTopic).not.toHaveBeenCalled();
    expect(api.removeProjectTopic).not.toHaveBeenCalled();
    expect(onSelectTopic).not.toHaveBeenCalled();
    expect(localStorage.getItem('cc_hidden_history_v1:7')).toBeNull();
  });

  it('rolls back the new collaboration task when project migration fails', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Review Task',
        is_group: false,
        is_bot: true,
        project_id: 5,
        project_name: 'Release',
      }],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 5, name: 'Release', task_count: 1 }],
    });
    api.createGroup.mockResolvedValue({
      group_id: 91,
      topic: 'grp_91',
      name: 'Review Task',
      group: {
        id: 91,
        name: 'Review Task',
        owner_id: 7,
        member_count: 3,
        agent_ids: [42],
      },
    });
    api.removeProjectTopic
      .mockRejectedValueOnce(new Error('migration failed'))
      .mockResolvedValueOnce({ ok: true });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Release"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Review Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Review Task 协作管理"]'));
    });
    const upgradeDialog = document.body.querySelector('[data-testid="create-group-modal"]');
    await act(async () => {
      Simulate.click(upgradeDialog.querySelector('button'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.assignProjectTopic).toHaveBeenCalledWith(5, 'grp_91');
    expect(api.removeProjectTopic.mock.calls).toEqual([
      ['p2p_7_42'],
      ['grp_91'],
    ]);
    expect(api.disbandGroup).toHaveBeenCalledWith(91);
    expect(onSelectTopic).not.toHaveBeenCalled();
    expect(localStorage.getItem('cc_hidden_history_v1:7')).toBeNull();
  });

  it('renames a history task from the three-dot menu and updates the active title', async () => {
    let taskName = 'Review Task';
    api.getConversations.mockImplementation(() => Promise.resolve({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: taskName,
        is_group: false,
        is_bot: true,
      }],
    }));
    api.getAgents.mockResolvedValue({ agents: [] });
    api.updateConversationTitle.mockImplementation(async (_topicId, nextName) => {
      taskName = nextName;
      return { ok: true, name: nextName };
    });

    await mount({ activeTopic: 'p2p_7_42' });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Review Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="修改任务名称 Review Task"]'));
    });

    const input = container.querySelector('input[aria-label="修改任务名称 Review Task"]');
    await act(async () => {
      Simulate.change(input, { target: { value: 'Release checklist' } });
    });
    await act(async () => {
      Simulate.submit(container.querySelector('.cc-history-rename-form'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.updateConversationTitle).toHaveBeenCalledWith('p2p_7_42', 'Release checklist');
    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'p2p_7_42',
      name: 'Release checklist',
    }));
    expect(container.textContent).toContain('Release checklist');
  });

  it('keeps a locally hidden legacy task hidden when starting a fresh Agent draft', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Local History Task',
        is_group: false,
        is_bot: true,
      }],
    });
    window.confirm = vi.fn(() => true);

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Local History Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="从列表移除 Local History Task"]'));
      await Promise.resolve();
    });

    expect(container.textContent).not.toContain('Local History Task');
    expect(JSON.parse(localStorage.getItem('cc_hidden_history_v1:7'))).toEqual(['p2p_7_42']);
    expect(api.removeFriend).not.toHaveBeenCalled();

    await remount({ compact: true });
    expect(container.textContent).not.toContain('Local History Task');

    await remount();
    await act(async () => {
      Simulate.click(container.querySelector('.cc-agent-roster-item'));
      await Promise.resolve();
    });

    expect(onStartAgentTask).toHaveBeenCalledWith(expect.objectContaining({ uid: 42 }));
    expect(api.openAgent).not.toHaveBeenCalled();
    expect(api.createGroup).not.toHaveBeenCalled();
    expect(container.textContent).not.toContain('Local History Task');
    expect(JSON.parse(localStorage.getItem('cc_hidden_history_v1:7'))).toEqual(['p2p_7_42']);
  });

  it('keeps agent tasks visible and does not offer local removal without a recovery entry', async () => {
    localStorage.setItem('cc_hidden_history_v1:7', JSON.stringify(['grp_77']));
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'grp_77',
        group_id: 77,
        name: 'Release Review Task',
        is_group: true,
        has_bot: true,
        is_agent_task: true,
      }],
    });
    api.getGroups.mockResolvedValue({
      groups: [{
        id: 77,
        name: 'Release Review Task',
        owner_id: 7,
        kind: 'agent_task',
        is_agent_task: true,
        has_bot: true,
      }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    expect(container.textContent).toContain('Release Review Task');
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Release Review Task 更多操作"]'));
    });
    expect(container.querySelector('[aria-label="从列表移除 Release Review Task"]')).toBeNull();
  });

  it('assigns a history task to an existing project from the row menu', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Project Task',
        is_group: false,
        is_bot: true,
      }],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website Launch', task_count: 0 }],
    });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Project Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="加入项目 Project Task"]'));
    });

    const dialog = document.body.querySelector('[aria-label="选择项目"]');
    expect(dialog).toBeTruthy();
    await act(async () => {
      Simulate.click(dialog.querySelector('.cc-new-task-agent'));
      await Promise.resolve();
    });

    expect(api.assignProjectTopic).toHaveBeenCalledWith(12, 'p2p_7_42');
  });

  it('expands a project and opens its assigned history task', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Project Task',
        is_group: false,
        is_bot: true,
        project_id: 12,
        project_name: 'Website Launch',
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: 'run-1',
          state: 'running',
          summary: '正在整理资料',
          updated_at: '2026-07-17T03:00:00Z',
        },
      }],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website Launch', task_count: 1 }],
    });
    const onSelectTopic = vi.fn();

    await mount({ onSelectTopic });
    expect(container.querySelector('.cc-history-item')).toBeFalsy();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Website Launch"]'));
    });

    const task = container.querySelector('[aria-label="打开项目任务 Project Task"]');
    expect(task).toBeTruthy();
    expect(task.querySelector('.cc-task-row-status.running')).toBeTruthy();
    expect(task.querySelector('[aria-label="任务进行中"]')).toBeTruthy();
    await act(async () => {
      Simulate.click(task);
    });

    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({ topicId: 'p2p_7_42', name: 'Project Task' }));
  });

  it('replaces a task time with a running spinner for active and background tasks', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Running Task',
        is_group: false,
        is_bot: true,
        last_time: '2026-07-20T04:00:00Z',
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: 'run-active',
          state: 'running',
          updated_at: '2026-07-20T04:00:05Z',
        },
      }],
    });

    await mount({ activeTopic: 'p2p_7_42' });

    const task = container.querySelector('.cc-history-item');
    expect(task.querySelector('.cc-task-row-status.running')).toBeTruthy();
    expect(task.querySelector('.cc-chat-row-time')).toBeFalsy();
    expect(task.querySelector('.cc-chat-row-actions')).toBeTruthy();
  });

  it('refreshes task status when a suspended PWA returns to the foreground', async () => {
    api.getConversations
      .mockResolvedValueOnce({
        conversations: [{
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'PWA Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-08-11T04:00:00Z',
        }],
      })
      .mockResolvedValueOnce({
        conversations: [{
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'PWA Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-08-11T04:00:00Z',
          task_status: {
            topic_id: 'p2p_7_42',
            run_id: 'run-pwa-resume',
            state: 'running',
            updated_at: '2026-08-11T04:00:05Z',
          },
        }],
      });

    await mount();

    const task = container.querySelector('.cc-history-item');
    expect(task.querySelector('.cc-task-row-status.running')).toBeFalsy();

    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getConversations).toHaveBeenCalledTimes(2);
    expect(task.querySelector('.cc-task-row-status.running')).toBeTruthy();
  });

  it('changes a background running spinner into an unread completion dot', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Background Task',
        is_group: false,
        is_bot: true,
        last_time: '2026-07-20T04:00:00Z',
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: 'run-transition',
          state: 'running',
          updated_at: '2026-07-20T04:00:05Z',
        },
      }],
    });

    await mount();

    const task = container.querySelector('.cc-history-item');
    expect(task.querySelector('.cc-task-row-status.running')).toBeTruthy();
    expect(wsHandler).toBeTypeOf('function');

    await act(async () => {
      wsHandler({
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: 'run-transition',
          state: 'completed',
          updated_at: '2026-07-20T04:01:00Z',
        },
      });
      await Promise.resolve();
    });

    expect(task.querySelector('.cc-task-row-status.running')).toBeFalsy();
    expect(task.querySelector('.cc-task-row-status.completed .cc-task-status-dot--completed')).toBeTruthy();
    expect(task.querySelector('.cc-chat-row-time')).toBeFalsy();
  });

  it('shows an unread completion dot for a completed background task and restores time after opening it', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Completed Task',
        is_group: false,
        is_bot: true,
        last_time: '2026-07-20T04:00:00Z',
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: 'run-complete',
          state: 'completed',
          updated_at: '2026-07-20T04:01:00Z',
        },
      }],
    });

    await mount();

    const task = container.querySelector('.cc-history-item');
    expect(task.querySelector('.cc-task-row-status.completed .cc-task-status-dot--completed')).toBeTruthy();
    expect(task.querySelector('.cc-chat-row-time')).toBeFalsy();

    await act(async () => {
      Simulate.click(task);
      await Promise.resolve();
    });

    expect(task.querySelector('.cc-task-row-status.completed')).toBeFalsy();
    expect(task.querySelector('.cc-chat-row-time')).toBeTruthy();
  });

  it('restores time without a completion dot when the completed task is already active', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Visible Completed Task',
        is_group: false,
        is_bot: true,
        last_time: '2026-07-20T04:00:00Z',
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: 'run-visible',
          state: 'completed',
          updated_at: '2026-07-20T04:01:00Z',
        },
      }],
    });

    await mount({ activeTopic: 'p2p_7_42' });

    const task = container.querySelector('.cc-history-item');
    expect(task.querySelector('.cc-task-row-status.completed')).toBeFalsy();
    expect(task.querySelector('.cc-chat-row-time')).toBeTruthy();
  });

  it('shows a red unread dot for a failed task', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Failed Task',
        is_group: false,
        is_bot: true,
        last_time: '2026-07-20T04:00:00Z',
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: 'run-failed',
          state: 'failed',
          error: 'Execution failed',
          updated_at: '2026-07-20T04:01:00Z',
        },
      }],
    });

    await mount();

    const task = container.querySelector('.cc-history-item');
    expect(task.querySelector('.cc-task-row-status.failed .cc-task-status-dot--failed')).toBeTruthy();
    expect(task.querySelector('.cc-task-completed-dot')).toBeFalsy();
    expect(task.querySelector('.cc-chat-row-time')).toBeFalsy();
  });

  it.each([
    ['cancelled', '任务已中止'],
    ['stale', '任务已自动中止'],
  ])('shows a yellow interruption dot for a %s task', async (state, label) => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Interrupted Task',
        is_group: false,
        is_bot: true,
        last_time: '2026-07-20T04:00:00Z',
        task_status: {
          topic_id: 'p2p_7_42',
          run_id: `run-${state}`,
          state,
          updated_at: '2026-07-20T04:01:00Z',
        },
      }],
    });

    await mount();

    const task = container.querySelector('.cc-history-item');
    expect(task.querySelector(`.cc-task-row-status.${state} .cc-task-status-dot--${state}`)).toBeTruthy();
    expect(task.querySelector(`.cc-task-row-status.${state}`).getAttribute('aria-label')).toBe(label);
    expect(task.querySelector('.cc-chat-row-time')).toBeFalsy();

    await act(async () => {
      Simulate.click(task);
      await Promise.resolve();
    });

    expect(task.querySelector(`.cc-task-row-status.${state}`)).toBeFalsy();
    expect(task.querySelector('.cc-chat-row-time')).toBeTruthy();
  });

  it('opens an assigned task menu and removes the task from its project', async () => {
    let assigned = true;
    api.getConversations.mockImplementation(() => Promise.resolve({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Project Task',
        preview: 'This project task preview should not render.',
        is_group: false,
        is_bot: true,
        last_time: '2026-07-20T04:00:00Z',
        project_id: assigned ? 12 : 0,
        project_name: assigned ? 'Website Launch' : '',
      }],
    }));
    api.getProjects.mockImplementation(() => Promise.resolve({
      projects: [{ id: 12, name: 'Website Launch', task_count: assigned ? 1 : 0 }],
    }));
    api.removeProjectTopic.mockImplementation(async () => {
      assigned = false;
      return { ok: true };
    });
    const onDeleteHistoryTask = vi.fn().mockResolvedValue({ ok: true });

    await mount({ onDeleteHistoryTask });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Website Launch"]'));
    });

    const task = container.querySelector('.cc-project-task-item');
    expect(task).toBeTruthy();
    expect(task.querySelector('.cc-chat-row-preview')).toBeNull();
    expect(task.classList.contains('cc-history-item')).toBe(true);
    expect(task.querySelector('.lucide-zap')).toBeTruthy();
    expect(task.querySelector('.cc-task-agent-icon.online')).toBeTruthy();
    expect(task.querySelectorAll('.cc-chat-row-actions button')).toHaveLength(2);
    expect(task.querySelector('[aria-label="置顶任务 Project Task"]')).toBeTruthy();
    expect(task.querySelector('.cc-chat-row-time')).toBeTruthy();
    await act(async () => {
      Simulate.click(task.querySelector('[aria-label="Project Task 更多操作"]'));
    });

    expect(task.querySelector('[aria-label="修改任务名称 Project Task"]')).toBeTruthy();
    expect(task.querySelector('[aria-label="移动到项目 Project Task"]')).toBeTruthy();
    expect(task.querySelector('[aria-label="移出当前项目 Project Task"]')).toBeTruthy();
    expect(task.querySelector('[aria-label="Project Task 手机扫码"]')).toBeTruthy();
    expect(task.querySelector('[aria-label="删除任务 Project Task"]')).toBeTruthy();

    await act(async () => {
      Simulate.click(task.querySelector('[aria-label="移出当前项目 Project Task"]'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.removeProjectTopic).toHaveBeenCalledWith('p2p_7_42');
    expect(container.querySelector('.cc-project-task-item')).toBeNull();
    expect(container.querySelector('.cc-history-item')).toBeTruthy();
  });

  it('keeps pinned project tasks above newer tasks and restores the project order', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Old Project Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-06-02T08:00:00Z',
          project_id: 12,
          project_name: 'Website Launch',
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'New Project Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-06-08T08:00:00Z',
          project_id: 12,
          project_name: 'Website Launch',
        },
      ],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website Launch', task_count: 2 }],
    });

    const projectTaskNames = () => Array.from(container.querySelectorAll('.cc-project-task-item'))
      .map((row) => row.querySelector('.v3-chat-item-label')?.textContent);
    const openProject = async () => {
      await act(async () => {
        Simulate.click(container.querySelector('.cc-project-item'));
      });
    };

    await mount();
    await openProject();

    expect(projectTaskNames()).toEqual(['New Project Task', 'Old Project Task']);
    const oldTaskRow = Array.from(container.querySelectorAll('.cc-project-task-item'))
      .find((row) => row.textContent.includes('Old Project Task'));

    await act(async () => {
      Simulate.click(oldTaskRow.querySelector('.v3-history-pin-trigger'));
      await Promise.resolve();
    });

    expect(projectTaskNames()).toEqual(['Old Project Task', 'New Project Task']);
    expect(JSON.parse(localStorage.getItem('cc_pinned_history_v1:7'))).toEqual(['p2p_7_42']);

    await remount();
    await openProject();

    expect(projectTaskNames()).toEqual(['Old Project Task', 'New Project Task']);
  });

  it('renames an assigned agent task group through updateGroup', async () => {
    let taskName = 'Agent Project Task';
    api.getConversations.mockImplementation(() => Promise.resolve({
      conversations: [{
        id: 'grp_77',
        group_id: 77,
        name: taskName,
        avatar_url: '/uploads/agent-task.png',
        is_group: true,
        is_bot: false,
        kind: 'agent_task',
        is_agent_task: true,
        project_id: 12,
        project_name: 'Website Launch',
      }],
    }));
    api.updateGroup.mockImplementation(async (_groupId, nextName) => {
      taskName = nextName;
      return { ok: true };
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website Launch', task_count: 1 }],
    });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="打开项目 Website Launch"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Agent Project Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="修改任务名称 Agent Project Task"]'));
    });

    const input = container.querySelector('input[aria-label="修改任务名称 Agent Project Task"]');
    await act(async () => {
      Simulate.change(input, { target: { value: 'Renamed Agent Task' } });
    });
    await act(async () => {
      Simulate.submit(container.querySelector('.cc-project-task-item .cc-history-rename-form'));
      await Promise.resolve();
    });

    expect(api.updateGroup).toHaveBeenCalledWith(77, 'Renamed Agent Task', '/uploads/agent-task.png');
    expect(api.updateConversationTitle).not.toHaveBeenCalled();
    expect(container.querySelector('.cc-project-task-item').textContent).toContain('Renamed Agent Task');
  });

  it('does not keep showing an expired running task status', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Expired Task',
        preview: '最后一条消息',
        is_group: false,
        is_bot: true,
        task_status: {
          topic_id: 'p2p_7_42',
          state: 'running',
          summary: '不应继续显示',
          updated_at: '2020-01-01T00:00:00Z',
          expires_at: '2020-01-01T06:00:00Z',
        },
      }],
    });

    await mount();

    const task = container.querySelector('.cc-history-item');
    expect(task.textContent).toContain('Expired Task');
    expect(task.querySelector('.cc-chat-row-preview')).toBeNull();
    expect(task.textContent).not.toContain('进行中');
    expect(task.textContent).not.toContain('不应继续显示');
  });

  it('opens global search from the compact sidebar search icon', async () => {
    await mount({ compact: true });

    const trigger = container.querySelector('[aria-label="打开全局搜索"]');
    expect(trigger).toBeTruthy();
    expect(trigger.textContent).toBe('');
    expect(trigger.querySelector('kbd')).toBeNull();

    await act(async () => {
      Simulate.click(trigger);
    });

    expect(onOpenSearch).toHaveBeenCalledTimes(1);
  });

  it('creates a project and immediately assigns the selected history task', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Fresh Project Task',
        is_group: false,
        is_bot: true,
      }],
    });
    api.createProject.mockResolvedValue({ project: { id: 18, name: 'New Workspace', task_count: 0 } });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Fresh Project Task 更多操作"]'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="加入项目 Fresh Project Task"]'));
    });
    await act(async () => {
      Simulate.click(document.body.querySelector('[aria-label="选择项目"] .oc-btn-primary'));
    });

    const createDialog = document.body.querySelector('[role="dialog"][aria-label="新建项目"]');
    const input = createDialog.querySelector('[aria-label="项目名称"]');
    await act(async () => {
      Simulate.change(input, { target: { value: 'New Workspace' } });
    });
    await act(async () => {
      Simulate.click(createDialog.querySelector('.oc-btn-primary'));
      await Promise.resolve();
    });

    expect(api.createProject).toHaveBeenCalledWith('New Workspace');
    expect(api.assignProjectTopic).toHaveBeenCalledWith(18, 'p2p_7_42');
  });

  it('starts a new task with the selected project context from the project menu', async () => {
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website', task_count: 0 }],
    });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Website 项目操作"]'));
    });
    const createTaskItem = Array.from(container.querySelectorAll('.cc-project-action-menu [role="menuitem"]'))
      .find((button) => button.textContent.includes('新建任务'));
    await act(async () => {
      Simulate.click(createTaskItem);
    });

    const dialog = document.body.querySelector('[role="dialog"][aria-label="在Website中新建任务"]');
    expect(dialog).not.toBeNull();
    expect(dialog.textContent).toContain('在“Website”中新建任务');
    await act(async () => {
      Simulate.click(dialog.querySelector('.cc-new-task-agent'));
    });

    expect(onStartAgentTask).toHaveBeenCalledWith(
      expect.objectContaining({ uid: 42 }),
      { projectId: 12, projectName: 'Website' },
    );
  });

  it('multi-selects unassigned tasks and adds them to a project in one action', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Loose Task',
          is_group: false,
          is_bot: true,
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'Second Loose Task',
          is_group: false,
          is_bot: true,
        },
        {
          id: 'grp_77',
          group_id: 77,
          name: 'Assigned Task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          project_id: 12,
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [{
        id: 77,
        name: 'Assigned Task',
        kind: 'agent_task',
        is_agent_task: true,
        has_bot: true,
        project_id: 12,
      }],
    });
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website', task_count: 1 }],
    });

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Website 项目操作"]'));
    });
    const addTaskItem = Array.from(container.querySelectorAll('.cc-project-action-menu [role="menuitem"]'))
      .find((button) => button.textContent.includes('添加已有任务'));
    await act(async () => {
      Simulate.click(addTaskItem);
    });

    const dialog = document.body.querySelector('[role="dialog"][aria-label="添加已有任务"]');
    expect(dialog).not.toBeNull();
    expect(dialog.textContent).toContain('Loose Task');
    expect(dialog.textContent).toContain('Second Loose Task');
    expect(dialog.textContent).not.toContain('Assigned Task');
    const taskOptions = Array.from(dialog.querySelectorAll('.cc-project-task-option'));
    const looseTask = taskOptions.find((button) => button.textContent.includes('Loose Task')
      && !button.textContent.includes('Second Loose Task'));
    const secondLooseTask = taskOptions.find((button) => button.textContent.includes('Second Loose Task'));
    await act(async () => {
      Simulate.click(looseTask);
      Simulate.click(secondLooseTask);
    });

    expect(looseTask.getAttribute('aria-pressed')).toBe('true');
    expect(secondLooseTask.getAttribute('aria-pressed')).toBe('true');
    expect(api.assignProjectTopic).not.toHaveBeenCalled();
    expect(dialog.querySelector('.cc-project-picker-actions .oc-btn-primary').textContent).toContain('2');

    await act(async () => {
      Simulate.click(dialog.querySelector('.cc-project-picker-actions .oc-btn-primary'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.assignProjectTopic).toHaveBeenCalledTimes(2);
    expect(api.assignProjectTopic).toHaveBeenCalledWith(12, 'p2p_7_42');
    expect(api.assignProjectTopic).toHaveBeenCalledWith(12, 'p2p_7_43');
  });

  it('renames and deletes a project from its row action menu', async () => {
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website', task_count: 0 }],
    });
    window.confirm = vi.fn(() => true);

    await mount();
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Website 项目操作"]'));
    });
    const renameItem = Array.from(container.querySelectorAll('.cc-project-action-menu [role="menuitem"]'))
      .find((button) => button.textContent.includes('更改项目名称'));
    await act(async () => {
      Simulate.click(renameItem);
    });

    const renameDialog = document.body.querySelector('[role="dialog"][aria-label="更改项目名称"]');
    expect(renameDialog).not.toBeNull();
    await act(async () => {
      Simulate.change(renameDialog.querySelector('[aria-label="新的项目名称"]'), { target: { value: 'Website Refresh' } });
    });
    await act(async () => {
      Simulate.click(renameDialog.querySelector('.oc-btn-primary'));
      await Promise.resolve();
    });
    expect(api.renameProject).toHaveBeenCalledWith(12, 'Website Refresh');

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Website 项目操作"]'));
    });
    const deleteItem = Array.from(container.querySelectorAll('.cc-project-action-menu [role="menuitem"]'))
      .find((button) => button.textContent.includes('删除项目'));
    await act(async () => {
      Simulate.click(deleteItem);
      await Promise.resolve();
    });
    expect(window.confirm).toHaveBeenCalled();
    expect(api.deleteProject).toHaveBeenCalledWith(12);
  });

  it('restores a locally hidden task when another entry point activates it again', async () => {
    localStorage.setItem('cc_hidden_history_v1:7', JSON.stringify(['p2p_7_42']));
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Reopened Agent Task',
        is_group: false,
        is_bot: true,
      }],
    });

    await mount({ activeTopic: 'p2p_7_42' });

    expect(container.textContent).toContain('Reopened Agent Task');
    expect(JSON.parse(localStorage.getItem('cc_hidden_history_v1:7'))).toEqual([]);
  });

  it('closes row menus on Escape and outside click', async () => {
    api.getGroups.mockResolvedValue({
      groups: [{ id: 88, name: 'Virtual Team', topic_id: 'grp_88', owner_id: 7 }],
    });
    await mount({ onManageGroup: vi.fn() });

    const trigger = container.querySelector('[aria-label="Virtual Team 更多操作"]');
    await act(async () => {
      Simulate.click(trigger);
    });
    expect(container.querySelector('.cc-chat-action-menu')).toBeTruthy();

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    });
    expect(container.querySelector('.cc-chat-action-menu')).toBeFalsy();

    await act(async () => {
      Simulate.click(trigger);
    });
    expect(container.querySelector('.cc-chat-action-menu')).toBeTruthy();
    await act(async () => {
      document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
    });
    expect(container.querySelector('.cc-chat-action-menu')).toBeFalsy();
  });

  it('opens a task menu upward when the sidebar has no room below it', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Bottom Task',
        is_group: false,
        is_bot: true,
      }],
    });
    await mount();

    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
    let triggerTop = 450;
    const makeRect = (top, bottom, height = bottom - top) => ({
      top,
      bottom,
      height,
      left: 0,
      right: 320,
      width: 320,
      x: 0,
      y: top,
      toJSON: () => ({}),
    });
    HTMLElement.prototype.getBoundingClientRect = vi.fn(function getBoundingClientRect() {
      if (this.classList.contains('v3-chat-list')) return makeRect(0, 500);
      if (this.classList.contains('cc-chat-action-menu')) return makeRect(0, 170, 170);
      if (this.classList.contains('v3-history-menu-trigger')) return makeRect(triggerTop, triggerTop + 28);
      return makeRect(0, 0);
    });

    try {
      const trigger = container.querySelector('.v3-history-menu-trigger');
      await act(async () => {
        Simulate.click(trigger);
      });
      expect(container.querySelector('.cc-chat-action-menu-up')).toBeTruthy();

      await act(async () => {
        Simulate.click(trigger);
      });
      triggerTop = 100;
      await act(async () => {
        Simulate.click(trigger);
      });
      expect(container.querySelector('.cc-chat-action-menu-up')).toBeFalsy();
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it('removes friend agents from the unified assistant task menu', async () => {
    api.getAgents.mockResolvedValue({
      agents: [
        {
          id: 43,
          uid: 43,
          username: 'shared-agent',
          display_name: '共享助手',
          topic_id: 'p2p_7_43',
          relation: 'friend',
          is_owner: false,
        },
      ],
    });
    window.confirm = vi.fn(() => true);

    await mount();

    const actionButton = container.querySelector('[aria-label="共享助手 任务操作"]');
    expect(actionButton).toBeTruthy();
    expect(container.querySelector('[aria-label="移除 共享助手"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(actionButton);
    });

    const actionMenu = document.body.querySelector('[role="menu"][aria-label="共享助手 任务操作"]');
    const menuItems = Array.from(actionMenu.querySelectorAll('[role="menuitem"]'));
    const mobileButton = menuItems.find((item) => item.textContent.includes('移动端使用'));
    const removeButton = menuItems.find((item) => item.textContent.includes('移除助手'));
    expect(mobileButton).toBeTruthy();
    expect(removeButton).toBeTruthy();
    expect(actionMenu.textContent).not.toContain('管理 Agent');

    await act(async () => {
      Simulate.click(removeButton);
      await Promise.resolve();
    });

    expect(api.openAgent).not.toHaveBeenCalled();
    expect(api.removeFriend).toHaveBeenCalledWith(43);
  });

  it('surfaces owned agent friend requests in the assistant section', async () => {
    api.getAgents.mockResolvedValue({
      agents: [
        {
          id: 42,
          uid: 42,
          username: 'dev-agent',
          display_name: 'Dev Agent',
          topic_id: 'p2p_7_42',
          relation: 'owner',
          is_owner: true,
        },
        {
          id: 43,
          uid: 43,
          username: 'shared-agent',
          display_name: '共享助手',
          topic_id: 'p2p_7_43',
          relation: 'friend',
          is_owner: false,
        },
      ],
    });
    api.getPendingRequests.mockImplementation((agentUid = '') => {
      if (String(agentUid) === '42') {
        return Promise.resolve({
          requests: [{ id: 9, from_user_id: 88, from_username: 'alice', display_name: 'Alice' }],
        });
      }
      return Promise.resolve({ requests: [] });
    });

    await mount();

    expect(api.getPendingRequests).toHaveBeenCalledWith(42);
    expect(api.getPendingRequests).not.toHaveBeenCalledWith(43);
    expect(container.textContent).toContain('新的助手好友申请');
    expect(container.textContent).toContain('Alice');
    expect(container.textContent).toContain('申请添加 Dev Agent');

    const rejectButton = container.querySelector('[aria-label="拒绝助手好友申请"]');
    await act(async () => {
      Simulate.click(rejectButton);
      await Promise.resolve();
    });

    expect(api.rejectAgentFriend).toHaveBeenCalledWith(42, 88);

    api.rejectAgentFriend.mockClear();

    const acceptButton = container.querySelector('[aria-label="通过助手好友申请"]');
    await act(async () => {
      Simulate.click(acceptButton);
      await Promise.resolve();
    });

    expect(api.acceptAgentFriend).toHaveBeenCalledWith(42, 88);
  });

  it('merges group metadata before classifying a multi-participant Agent task', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_9',
          group_id: 9,
          name: 'Bot Room',
          is_group: true,
          last_time: '2026-06-04T08:00:00Z',
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [{
        id: 9,
        name: 'Bot Room',
        owner_id: 7,
        kind: 'agent_task',
        is_agent_task: true,
        has_bot: true,
        member_count: 3,
        created_at: '2026-06-04T08:00:00Z',
      }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const collaborationTask = container.querySelector('[data-task-kind="collaboration"]');
    expect(collaborationTask).toBeTruthy();
    expect(collaborationTask.textContent).toContain('Bot Room');
    expect(collaborationTask.textContent).toContain('协作');
    expect(collaborationTask.querySelector('svg.lucide-zap')).toBeTruthy();
    expect(container.querySelector('[data-contact-kind="group"]')).toBeFalsy();
  });

  it('renders a one-person and one-Agent task without a collaboration label', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'grp_77',
        group_id: 77,
        name: 'Release Review Task',
        is_group: true,
        has_bot: true,
        is_agent_task: true,
        member_count: 2,
        last_time: '2026-06-04T09:00:00Z',
      }],
    });
    api.getGroups.mockResolvedValue({
      groups: [{
        id: 77,
        name: 'Release Review Task',
        owner_id: 7,
        kind: 'agent_task',
        is_agent_task: true,
        has_bot: true,
        member_count: 2,
        created_at: '2026-06-04T09:00:00Z',
      }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const historyRow = container.querySelector('.cc-history-item');
    expect(historyRow?.textContent).toContain('Release Review Task');
    expect(historyRow?.getAttribute('data-task-kind')).toBe('solo');
    expect(historyRow?.querySelector('svg.lucide-zap')).toBeTruthy();
    expect(historyRow?.querySelector('.cc-item-kind')).toBeFalsy();
    const groupRows = Array.from(container.querySelectorAll('.v3-chat-item'))
      .filter((row) => !row.classList.contains('cc-history-item'));
    expect(groupRows.some((row) => row.textContent.includes('Release Review Task'))).toBe(false);
  });

  it('selects an Agent for a new task without creating anything before the first message', async () => {
    await mount();

    await act(async () => {
      Simulate.click(container.querySelector('.cc-sidebar-primary'));
    });
    await act(async () => {
      Simulate.click(document.body.querySelector('.cc-new-task-agent'));
      await Promise.resolve();
    });

    expect(onStartAgentTask).toHaveBeenCalledWith(expect.objectContaining({
      uid: 42,
      display_name: 'Dev Agent',
    }));
    expect(api.createGroup).not.toHaveBeenCalled();
    expect(api.openAgent).not.toHaveBeenCalled();
    expect(onSelectTopic).not.toHaveBeenCalled();
    expect(document.body.querySelector('.cc-new-task-dialog')).toBeFalsy();
  });

  it('does not duplicate global search inside the expanded sidebar tools', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_8',
        friend_id: 8,
        name: 'Alice',
        is_group: false,
        is_bot: false,
      }],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();
    await act(async () => clickSection('联系人'));

    expect(container.querySelector('[aria-label="打开全局搜索"]')).toBeFalsy();
    expect(onOpenSearch).not.toHaveBeenCalled();
    expect(container.querySelector('.cc-contacts-section .cc-section-toggle').getAttribute('aria-expanded')).toBe('false');
  });

  it('temporarily collapses contacts when downward scrolling reaches the projects header', async () => {
    await mount();

    const list = container.querySelector('.v3-chat-list');
    const contactsSection = container.querySelector('.cc-contacts-section');
    const projectsSection = container.querySelector('.cc-project-section');
    const tasksSection = container.querySelector('.cc-conversation-section');
    const contactsToggle = contactsSection.querySelector('.cc-section-toggle');
    const projectsToggle = projectsSection.querySelector('.cc-section-toggle');
    projectsSection.style.top = '39px';
    tasksSection.style.top = '77px';
    expect(contactsToggle.getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('[data-contact-kind="agent"]')).toBeTruthy();
    Object.defineProperty(list, 'scrollHeight', {
      configurable: true,
      get: () => (
        contactsToggle.getAttribute('aria-expanded') === 'true' ? 700 : 560
      ),
    });
    Object.defineProperty(contactsSection, 'offsetTop', {
      configurable: true,
      get: () => list.scrollTop,
    });
    const contactAction = container.querySelector('.cc-agent-menu-trigger');
    contactAction.focus();
    expect(document.activeElement).toBe(contactAction);

    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
    const makeRect = (top, bottom) => ({
      top,
      bottom,
      height: bottom - top,
      left: 0,
      right: 260,
      width: 260,
      x: 0,
      y: top,
      toJSON: () => ({}),
    });
    HTMLElement.prototype.getBoundingClientRect = vi.fn(function getBoundingClientRect() {
      if (this === list) return makeRect(0, 500);
      if (this === contactsSection) return makeRect(0, 36);
      if (this === projectsSection) return makeRect(40, 76);
      if (this === tasksSection) return makeRect(200, 236);
      return makeRect(0, 0);
    });

    try {
      list.scrollTop = 180;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });

      expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(container.querySelector('[data-contact-kind="agent"]')).toBeFalsy();
      expect(list.scrollTop).toBe(40);
      expect(document.activeElement).toBe(contactsToggle);
      expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toBeNull();

      list.scrollTop = 24;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });
      expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(container.querySelector('[data-contact-kind="agent"]')).toBeFalsy();

      list.scrollTop = 220;
      await act(async () => {
        Simulate.click(contactsToggle);
        await Promise.resolve();
      });
      expect(contactsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(container.querySelector('[data-contact-kind="agent"]')).toBeTruthy();
      expect(list.scrollTop).toBe(0);
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toBeNull();
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it('collapses sticky sections at the real reachable scroll end without requiring header overlap', async () => {
    await mount();

    const list = container.querySelector('.v3-chat-list');
    const contactsSection = container.querySelector('.cc-contacts-section');
    const projectsSection = container.querySelector('.cc-project-section');
    const tasksSection = container.querySelector('.cc-conversation-section');
    const contactsToggle = contactsSection.querySelector('.cc-section-toggle');
    const projectsToggle = projectsSection.querySelector('.cc-section-toggle');
    projectsSection.style.top = '39px';
    tasksSection.style.top = '77px';
    Object.defineProperty(list, 'clientHeight', {
      configurable: true,
      value: 691,
    });
    Object.defineProperty(list, 'scrollHeight', {
      configurable: true,
      get: () => {
        const contactsHeight = contactsToggle.getAttribute('aria-expanded') === 'true' ? 140 : 0;
        const projectsHeight = projectsToggle.getAttribute('aria-expanded') === 'true' ? 100 : 0;
        return 890 + contactsHeight + projectsHeight;
      },
    });

    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
    const makeRect = (top, bottom) => ({
      top,
      bottom,
      height: bottom - top,
      left: 0,
      right: 260,
      width: 260,
      x: 0,
      y: top,
      toJSON: () => ({}),
    });
    HTMLElement.prototype.getBoundingClientRect = vi.fn(function getBoundingClientRect() {
      if (this === list) return makeRect(160, 851);
      if (this === contactsSection) return makeRect(162, 198);
      if (this === projectsSection) return makeRect(264.7, 300.7);
      if (this === tasksSection) return makeRect(500, 536);
      return makeRect(0, 0);
    });

    try {
      list.scrollTop = 439;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });

      expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(list.scrollTop).toBe(299);

      await act(async () => {
        list.dispatchEvent(new WheelEvent('wheel', { deltaY: -80 }));
        await Promise.resolve();
      });
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');

      await act(async () => {
        list.dispatchEvent(new WheelEvent('wheel', { ctrlKey: true, deltaY: 80 }));
        await Promise.resolve();
      });
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');

      await act(async () => {
        list.dispatchEvent(new WheelEvent('wheel', { deltaY: 80 }));
        await Promise.resolve();
      });
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(list.scrollTop).toBe(199);
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it('clears temporary scroll collapse state when compact mode changes', async () => {
    await mount();

    const list = container.querySelector('.v3-chat-list');
    const contactsSection = container.querySelector('.cc-contacts-section');
    const projectsSection = container.querySelector('.cc-project-section');
    const tasksSection = container.querySelector('.cc-conversation-section');
    const contactsToggle = contactsSection.querySelector('.cc-section-toggle');
    projectsSection.style.top = '39px';
    tasksSection.style.top = '77px';
    Object.defineProperty(list, 'clientHeight', {
      configurable: true,
      value: 500,
    });
    Object.defineProperty(list, 'scrollHeight', {
      configurable: true,
      value: 700,
    });
    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
    const makeRect = (top, bottom) => ({
      top,
      bottom,
      height: bottom - top,
      left: 0,
      right: 260,
      width: 260,
      x: 0,
      y: top,
      toJSON: () => ({}),
    });
    HTMLElement.prototype.getBoundingClientRect = vi.fn(function getBoundingClientRect() {
      if (this === list) return makeRect(0, 500);
      if (this === contactsSection) return makeRect(0, 36);
      if (this === projectsSection) return makeRect(40, 76);
      if (this === tasksSection) return makeRect(200, 236);
      return makeRect(0, 0);
    });

    try {
      list.scrollTop = 180;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });
      expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');

      await mount({ compact: true });
      expect(container.querySelector('.v3-chat-list')).toBeFalsy();

      await mount({ compact: false });
      const restoredContactsToggle = container.querySelector('.cc-contacts-section .cc-section-toggle');
      expect(restoredContactsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(container.querySelector('[data-contact-kind="agent"]')).toBeTruthy();
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it('returns to the top when manually collapsed contacts or projects are reopened', async () => {
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website', task_count: 0 }],
    });
    await mount();

    const list = container.querySelector('.v3-chat-list');
    const contactsToggle = container.querySelector('.cc-contacts-section .cc-section-toggle');
    const projectsToggle = container.querySelector('.cc-project-section .cc-section-toggle');

    await act(async () => {
      Simulate.click(contactsToggle);
    });
    expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');

    list.scrollTop = 220;
    await act(async () => {
      Simulate.click(contactsToggle);
      await Promise.resolve();
    });
    expect(contactsToggle.getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('[data-contact-kind="agent"]')).toBeTruthy();
    expect(list.scrollTop).toBe(0);

    await act(async () => {
      Simulate.click(projectsToggle);
    });
    expect(projectsToggle.getAttribute('aria-expanded')).toBe('false');

    list.scrollTop = 220;
    await act(async () => {
      Simulate.click(projectsToggle);
      await Promise.resolve();
    });
    expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('.cc-project-row')).toBeTruthy();
    expect(list.scrollTop).toBe(0);
    expect(JSON.parse(localStorage.getItem('cc_sidebar_collapsed_v1:7'))).toEqual({
      conversations: false,
      contacts: false,
      projects: false,
    });
  });

  it('does not auto-collapse contacts while scrolling upward at the sticky boundary', async () => {
    await mount();

    const list = container.querySelector('.v3-chat-list');
    const contactsSection = container.querySelector('.cc-contacts-section');
    const projectsSection = container.querySelector('.cc-project-section');
    const contactsToggle = contactsSection.querySelector('.cc-section-toggle');
    let projectsTop = 120;
    projectsSection.style.top = '39px';
    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
    const makeRect = (top, bottom) => ({
      top,
      bottom,
      height: bottom - top,
      left: 0,
      right: 260,
      width: 260,
      x: 0,
      y: top,
      toJSON: () => ({}),
    });
    HTMLElement.prototype.getBoundingClientRect = vi.fn(function getBoundingClientRect() {
      if (this === list) return makeRect(0, 500);
      if (this === contactsSection) return makeRect(0, 36);
      if (this === projectsSection) return makeRect(projectsTop, projectsTop + 36);
      return makeRect(220, 256);
    });

    try {
      list.scrollTop = 100;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });
      projectsTop = 40;
      list.scrollTop = 60;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });

      expect(contactsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(container.querySelector('[data-contact-kind="agent"]')).toBeTruthy();
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it('temporarily collapses projects after contacts while preserving real focused-row scrolling', async () => {
    api.getProjects.mockResolvedValue({
      projects: [{ id: 12, name: 'Website', task_count: 0 }],
    });
    await mount();

    const list = container.querySelector('.v3-chat-list');
    const contactsSection = container.querySelector('.cc-contacts-section');
    const projectsSection = container.querySelector('.cc-project-section');
    const tasksSection = container.querySelector('.cc-conversation-section');
    const contactsToggle = contactsSection.querySelector('.cc-section-toggle');
    const projectsToggle = projectsSection.querySelector('.cc-section-toggle');
    projectsSection.style.top = '39px';
    tasksSection.style.top = '77px';
    Object.defineProperty(list, 'scrollHeight', {
      configurable: true,
      get: () => {
        const contactsHeight = contactsToggle.getAttribute('aria-expanded') === 'true' ? 140 : 0;
        const projectsHeight = projectsToggle.getAttribute('aria-expanded') === 'true' ? 100 : 0;
        return 500 + contactsHeight + projectsHeight;
      },
    });
    expect(contactsToggle.getAttribute('aria-expanded')).toBe('true');
    expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
    expect(container.querySelector('.cc-project-row')).toBeTruthy();

    let projectsTop = 40;
    let tasksTop = 200;
    const originalGetBoundingClientRect = HTMLElement.prototype.getBoundingClientRect;
    const makeRect = (top, bottom) => ({
      top,
      bottom,
      height: bottom - top,
      left: 0,
      right: 260,
      width: 260,
      x: 0,
      y: top,
      toJSON: () => ({}),
    });
    HTMLElement.prototype.getBoundingClientRect = vi.fn(function getBoundingClientRect() {
      if (this === list) return makeRect(0, 500);
      if (this === contactsSection) return makeRect(0, 36);
      if (this === projectsSection) return makeRect(projectsTop, projectsTop + 36);
      if (this === tasksSection) return makeRect(tasksTop, tasksTop + 36);
      return makeRect(0, 0);
    });

    try {
      list.scrollTop = 180;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });

      expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(list.scrollTop).toBe(40);
      expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toBeNull();

      const projectOpenButton = container.querySelector('.cc-project-item');
      await act(async () => {
        Simulate.click(projectOpenButton);
      });
      projectOpenButton.focus();
      expect(document.activeElement).toBe(projectOpenButton);

      projectsTop = 38;
      tasksTop = 78;
      list.scrollTop = 160;
      await act(async () => {
        list.dispatchEvent(new Event('scroll'));
        await Promise.resolve();
      });

      expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(container.querySelector('.cc-project-row')).toBeFalsy();
      expect(list.scrollTop).toBe(60);
      expect(document.activeElement).toBe(projectsToggle);
      expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toBeNull();

      list.scrollTop = 220;
      await act(async () => {
        Simulate.click(projectsToggle);
        await Promise.resolve();
      });
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(container.querySelector('.cc-project-row')).toBeTruthy();
      expect(list.scrollTop).toBe(0);
      expect(contactsToggle.getAttribute('aria-expanded')).toBe('false');
      expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toBeNull();

      await act(async () => {
        Simulate.click(contactsToggle);
        await Promise.resolve();
      });
      expect(contactsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(projectsToggle.getAttribute('aria-expanded')).toBe('true');
      expect(list.scrollTop).toBe(0);
      expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toBeNull();
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalGetBoundingClientRect;
    }
  });

  it('restores collapsed sections after remounting the sidebar', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_8',
          friend_id: 8,
          name: 'Alice',
          is_group: false,
          is_bot: false,
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    expect(container.textContent).toContain('Alice');
    await act(async () => {
      clickSection('联系人');
    });
    expect(container.textContent).not.toContain('Alice');
    expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toContain('"contacts":true');

    await remount();

    expect(container.textContent).toContain('任务');
    expect(container.textContent).not.toContain('Alice');
  });

  it('keeps the contacts section title text-only', async () => {
    await mount();

    const contactsToggle = Array.from(container.querySelectorAll('.cc-section-toggle'))
      .find((button) => button.textContent.includes('联系人'));
    expect(contactsToggle).toBeTruthy();
    expect(contactsToggle.querySelector('svg.lucide-user-round')).toBeFalsy();
    expect(contactsToggle.querySelector('svg.lucide-message-square')).toBeFalsy();
  });

  it('portals the new-task dialog outside the sidebar container', async () => {
    await mount();

    await act(async () => {
      Simulate.click(container.querySelector('.cc-sidebar-primary'));
    });

    expect(container.querySelector('.cc-new-task-dialog')).toBeFalsy();
    expect(document.body.querySelector('.cc-new-task-dialog')).toBeTruthy();
  });

  it('places additional sidebar tools directly after new task', async () => {
    await mount({
      additionalSidebarTools: (
        <>
          <button type="button" className="cc-sidebar-primary cc-sidebar-skillhub-entry">
            SkillHub
          </button>
        </>
      ),
    });

    const toolLabels = Array.from(container.querySelectorAll('.cc-sidebar-tools > button'))
      .map((button) => button.textContent.trim());
    expect(toolLabels).toEqual(['新建任务', 'SkillHub']);
  });

  it('shows the four compact navigation tools and recent Agent tasks in a history menu', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Recent assistant',
          is_group: false,
          is_bot: true,
          last_time: '2026-07-16T08:00:00Z',
        },
      ],
    });

    await mount({
      compact: true,
      additionalSidebarTools: <button type="button" aria-label="打开 SkillHub">SkillHub</button>,
    });

    expect(container.querySelector('[aria-label="新建任务"]')).toBeTruthy();
    expect(container.querySelector('[aria-label="打开全局搜索"]')).toBeTruthy();
    expect(container.querySelector('[aria-label="打开 SkillHub"]')).toBeTruthy();
    const historyButton = container.querySelector('[aria-label="历史任务"]');
    expect(historyButton).toBeTruthy();
    expect(container.querySelector('.cc-sidebar-tools')).toBeFalsy();

    await act(async () => {
      Simulate.focus(historyButton);
    });
    expect(document.body.querySelector('.cc-compact-history-panel')?.textContent).toContain('Recent assistant');
    const recentButton = document.body.querySelector('[aria-label="打开任务：Recent assistant"]');
    expect(recentButton).toBeTruthy();

    const recentLabel = recentButton.querySelector('.cc-compact-history-label');
    Object.defineProperty(recentLabel, 'scrollWidth', { configurable: true, value: 260 });
    Object.defineProperty(recentLabel, 'clientWidth', { configurable: true, value: 120 });
    await act(async () => {
      Simulate.mouseEnter(recentButton);
    });
    expect(document.body.querySelector('[role="tooltip"]')?.textContent).toBe('Recent assistant');
    expect(recentButton.getAttribute('aria-describedby')).toBe('cc-compact-history-tooltip');

    await act(async () => {
      Simulate.mouseLeave(recentButton);
    });
    expect(document.body.querySelector('[role="tooltip"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(recentButton);
    });

    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'p2p_7_42',
      name: 'Recent assistant',
    }));

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="新建任务"]'));
    });

    expect(document.body.querySelector('.cc-new-task-dialog')).toBeTruthy();
  });

  it('shows project and unassigned tasks together in compact mode by global recency', async () => {
    localStorage.setItem('cc_pinned_history_v1:7', JSON.stringify(['p2p_7_43']));
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Newest project task',
          is_group: false,
          is_bot: true,
          project_id: 12,
          project_name: 'Website',
          last_time: '2026-07-20T08:03:00Z',
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'Pinned older task',
          is_group: false,
          is_bot: true,
          last_time: '2026-07-20T08:01:00Z',
        },
        {
          id: 'p2p_7_44',
          friend_id: 44,
          name: 'Middle unassigned task',
          is_group: false,
          is_bot: true,
          last_time: '2026-07-20T08:02:00Z',
        },
      ],
    });

    await mount({ compact: true });

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="历史任务"]'));
    });
    const compactTasks = Array.from(document.body.querySelectorAll('.cc-compact-history-item'));
    expect(compactTasks.map((button) => button.getAttribute('aria-label'))).toEqual([
      '打开任务：Newest project task',
      '打开任务：Middle unassigned task',
      '打开任务：Pinned older task',
    ]);
  });

  it('shows task states on compact avatars and dismisses a terminal state when opened', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_101',
          name: 'Running task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:04:00Z',
          task_status: { state: 'running', updated_at: '2026-07-20T08:04:00Z' },
        },
        {
          id: 'grp_102',
          name: 'Completed task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:03:00Z',
          task_status: { state: 'completed', updated_at: '2026-07-20T08:03:00Z' },
        },
        {
          id: 'grp_103',
          name: 'Failed task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:02:00Z',
          task_status: { state: 'failed', updated_at: '2026-07-20T08:02:00Z' },
        },
        {
          id: 'grp_104',
          name: 'Stopped task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:01:00Z',
          task_status: { state: 'stale', updated_at: '2026-07-20T08:01:00Z' },
        },
      ],
    });

    await mount({ compact: true });

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="历史任务"]'));
    });
    const buttonFor = (name) => Array.from(document.body.querySelectorAll('.cc-compact-history-item'))
      .find((button) => button.getAttribute('aria-label')?.endsWith(name));
    const runningButton = buttonFor('Running task');
    const completedButton = buttonFor('Completed task');
    const failedButton = buttonFor('Failed task');
    const stoppedButton = buttonFor('Stopped task');
    expect(runningButton).toBeTruthy();
    expect(completedButton).toBeTruthy();
    expect(failedButton).toBeTruthy();
    expect(stoppedButton).toBeTruthy();

    await act(async () => {
      Simulate.click(completedButton);
      await Promise.resolve();
    });
    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({ name: 'Completed task' }));
  });

  it('exposes task status labels in compact history entries', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_running',
          name: 'Running history task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:04:00Z',
          task_status: { state: 'running', updated_at: '2026-07-20T08:04:00Z' },
        },
        {
          id: 'grp_completed',
          name: 'Completed history task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:03:00Z',
          task_status: { state: 'completed', updated_at: '2026-07-20T08:03:00Z' },
        },
        {
          id: 'grp_failed',
          name: 'Failed history task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:02:00Z',
          task_status: { state: 'failed', updated_at: '2026-07-20T08:02:00Z' },
        },
        {
          id: 'grp_stale',
          name: 'Stale history task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:01:00Z',
          task_status: { state: 'stale', updated_at: '2026-07-20T08:01:00Z' },
        },
        {
          id: 'grp_cancelled',
          name: 'Cancelled history task',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          last_time: '2026-07-20T08:00:00Z',
          task_status: { state: 'cancelled', updated_at: '2026-07-20T08:00:00Z' },
        },
      ],
    });

    await mount({ compact: true });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="历史任务"]'));
    });

    const statusFor = (name) => Array.from(document.body.querySelectorAll('.cc-compact-history-item'))
      .find((button) => button.getAttribute('aria-label')?.endsWith(name))
      ?.querySelector('[role="status"]');
    expect(statusFor('Running history task')?.getAttribute('aria-label')).toBe('任务进行中');
    expect(statusFor('Completed history task')?.getAttribute('aria-label')).toBe('任务已完成');
    expect(statusFor('Failed history task')?.getAttribute('aria-label')).toBe('任务执行失败');
    expect(statusFor('Stale history task')?.getAttribute('aria-label')).toBe('任务已自动中止');
    expect(statusFor('Cancelled history task')?.getAttribute('aria-label')).toBe('任务已中止');
    expect(statusFor('Cancelled history task')?.className).toContain('cancelled');
  });

  it('collects contact creation actions in one accessible menu and closes it appropriately', async () => {
    await mount();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="联系人更多操作"]'));
    });

    let menu = container.querySelector('[role="menu"][aria-label="联系人操作"]');
    expect(menu).toBeTruthy();
    expect(container.querySelector('[aria-label="联系人更多操作"]').getAttribute('aria-expanded')).toBe('true');
    const menuItems = Array.from(menu.querySelectorAll('[role="menuitem"]'));
    expect(menuItems.map((item) => item.textContent.trim())).toEqual(['添加好友', '创建群组', 'Agent 助手']);
    expect(menuItems[0].querySelector('.lucide-user-plus')).toBeTruthy();
    expect(menuItems[1].querySelector('.lucide-users')).toBeTruthy();
    expect(menuItems[2].querySelector('.lucide-bot')).toBeTruthy();

    await act(async () => {
      document.body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
    });
    expect(container.querySelector('[role="menu"][aria-label="联系人操作"]')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="联系人更多操作"]'));
    });
    menu = container.querySelector('[role="menu"][aria-label="联系人操作"]');
    await act(async () => {
      Simulate.click(Array.from(menu.querySelectorAll('[role="menuitem"]')).find((item) => item.textContent.includes('添加好友')));
    });
    expect(container.querySelector('[role="menu"][aria-label="联系人操作"]')).toBeNull();
    expect(document.body.querySelector('[data-testid="add-friend-modal"]')).toBeTruthy();

    await act(async () => {
      Simulate.click(document.body.querySelector('[data-testid="add-friend-modal"] button'));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="联系人更多操作"]'));
    });
    menu = container.querySelector('[role="menu"][aria-label="联系人操作"]');
    await act(async () => {
      Simulate.click(Array.from(menu.querySelectorAll('[role="menuitem"]')).find((item) => item.textContent.includes('Agent 助手')));
    });
    expect(container.querySelector('[role="menu"][aria-label="联系人操作"]')).toBeNull();
    expect(document.body.querySelector('[data-testid="agent-store-modal"]')).toBeTruthy();

    await act(async () => {
      Simulate.click(Array.from(document.body.querySelectorAll('[data-testid="agent-store-modal"] button'))
        .find((button) => button.textContent.includes('关闭助手管理')));
    });
    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="联系人更多操作"]'));
    });
    menu = container.querySelector('[role="menu"][aria-label="联系人操作"]');
    await act(async () => {
      Simulate.click(Array.from(menu.querySelectorAll('[role="menuitem"]')).find((item) => item.textContent.includes('创建群组')));
    });
    expect(container.querySelector('[data-testid="create-group-modal"]')).toBeFalsy();
    expect(document.body.querySelector('[data-testid="create-group-modal"]')).toBeTruthy();
    expect(container.querySelector('[role="menu"][aria-label="联系人操作"]')).toBeNull();
  });

  it('removes an ordinary friend from the friend row menu', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_8',
          friend_id: 8,
          name: 'Alice',
          is_group: false,
          is_bot: false,
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });
    window.confirm = vi.fn(() => true);

    await mount({ activeTopic: 'p2p_7_8' });

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Alice 联系人操作"]'));
    });
    const removeButton = Array.from(container.querySelectorAll('[role="menuitem"]'))
      .find((node) => node.textContent.includes('删除好友'));
    expect(removeButton).toBeTruthy();

    await act(async () => {
      Simulate.click(removeButton);
      await Promise.resolve();
    });

    expect(api.removeFriend).toHaveBeenCalledWith(8);
    expect(onSelectTopic).toHaveBeenCalledWith(null);
  });

  it('blocks an ordinary friend from the friend row menu', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_9',
          friend_id: 9,
          name: 'Bob',
          is_group: false,
          is_bot: false,
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 9, username: 'bob', display_name: 'Bob' }],
    });
    window.confirm = vi.fn(() => true);

    await mount();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Bob 联系人操作"]'));
    });
    const blockButton = Array.from(container.querySelectorAll('[role="menuitem"]'))
      .find((node) => node.textContent.includes('拉黑好友'));
    expect(blockButton).toBeTruthy();

    await act(async () => {
      Simulate.click(blockButton);
      await Promise.resolve();
    });

    expect(api.blockUser).toHaveBeenCalledWith(9);
  });

  it('keeps a human-only group in tasks with collaboration styling and group actions', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_9',
          group_id: 9,
          name: '查云端log',
          is_group: true,
          latest_seq: 88,
        },
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Dev Agent',
          is_group: false,
          is_bot: true,
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [
        {
          id: 9,
          name: '查云端log',
          owner_id: 7,
          member_count: 2,
        },
      ],
    });

    const onManageGroup = vi.fn();
    await mount({ onManageGroup });

    const sections = Array.from(container.querySelectorAll('.cc-top-level-section > .cc-section-toggle'))
      .map((node) => node.textContent.trim());
    expect(sections).toEqual(['历史任务', '联系人', '项目']);

    const groupItem = container.querySelector('[data-task-kind="collaboration"]');
    expect(groupItem).toBeTruthy();
    expect(groupItem.textContent).toContain('查云端log');
    expect(groupItem.textContent).toContain('协作');
    expect(groupItem.querySelector('svg.lucide-zap')).toBeTruthy();
    expect(groupItem.querySelector('.cc-item-kind')?.textContent).toBe('协作');
    expect(container.querySelector('[data-contact-kind="group"]')).toBeFalsy();
    expect(container.querySelector('[data-conversation-kind="group"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(groupItem);
      await Promise.resolve();
    });

    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'grp_9',
      name: '查云端log',
      isGroup: true,
      groupId: 9,
      avatar_url: undefined,
      hasBot: false,
      isAgentTask: false,
      memberCount: 2,
    }));

    await act(async () => {
      Simulate.click(groupItem.querySelector('[aria-label="查云端log 更多操作"]'));
    });
    expect(groupItem.querySelector('[aria-label="查云端log 协作管理"]')).toBeTruthy();
    expect(groupItem.querySelector('[aria-label="删除任务 查云端log"]')).toBeTruthy();
  });

  it('orders group and Agent tasks together while ordering people independently', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Old Agent',
          is_group: false,
          is_bot: true,
          last_time: '2026-06-04T08:00:00Z',
          latest_seq: 999,
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'New Agent',
          is_group: false,
          is_bot: true,
          last_time: '2026-06-06T08:00:00Z',
          latest_seq: 10,
        },
        {
          id: 'p2p_7_8',
          friend_id: 8,
          name: 'Old Friend',
          is_group: false,
          is_bot: false,
          last_time: '2026-06-03T08:00:00Z',
          latest_seq: 50,
        },
        {
          id: 'p2p_7_9',
          friend_id: 9,
          name: 'New Friend',
          is_group: false,
          is_bot: false,
          last_time: '2026-06-05T08:00:00Z',
          latest_seq: 1,
        },
        {
          id: 'grp_20',
          group_id: 20,
          name: 'Old Group',
          is_group: true,
          last_time: '2026-06-02T08:00:00Z',
          latest_seq: 1000,
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [
        {
          id: 20,
          name: 'Old Group',
          owner_id: 7,
          created_at: '2026-06-02T08:00:00Z',
        },
        {
          id: 21,
          name: 'New Empty Group',
          owner_id: 7,
          created_at: '2026-06-07T08:00:00Z',
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [
        { id: 8, username: 'old-friend', display_name: 'Old Friend' },
        { id: 9, username: 'new-friend', display_name: 'New Friend' },
      ],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const taskText = Array.from(container.querySelectorAll('[data-conversation-kind="agent"]'))
      .map((row) => row.textContent)
      .join('|');
    const expectedTaskOrder = ['New Empty Group', 'New Agent', 'Old Agent', 'Old Group'];
    expectedTaskOrder.slice(1).forEach((name, index) => {
      expect(taskText.indexOf(expectedTaskOrder[index])).toBeLessThan(taskText.indexOf(name));
    });

    const contactText = Array.from(container.querySelectorAll('.cc-contact-item'))
      .map((row) => row.textContent)
      .join('|');
    const expectedContactOrder = ['New Friend', 'Old Friend'];
    expectedContactOrder.slice(1).forEach((name, index) => {
      expect(contactText.indexOf(expectedContactOrder[index])).toBeLessThan(contactText.indexOf(name));
    });
  });

  it('keeps human contacts above agents and distinguishes contact kinds with leading icons', async () => {
    api.getFriends.mockResolvedValue({
      friends: [
        { id: 8, username: 'zoe', display_name: 'Zoe Friend' },
        { id: 9, username: 'alice', display_name: 'Alice Friend' },
      ],
    });
    api.getAgents.mockResolvedValue({
      agents: [
        { id: 42, uid: 42, username: 'alpha-agent', display_name: 'Alpha Agent', relation: 'owner', is_owner: true },
        { id: 43, uid: 43, username: 'beta-agent', display_name: 'Beta Agent', relation: 'friend' },
      ],
    });

    await mount();

    const contactRows = Array.from(container.querySelectorAll('.cc-contact-item'));
    expect(contactRows.map((row) => row.getAttribute('data-contact-kind'))).toEqual([
      'friend',
      'friend',
      'agent',
      'agent',
    ]);
    contactRows.forEach((row) => {
      expect(row.classList.contains('cc-sidebar-item-row')).toBe(true);
      expect(row.dataset.sidebarLevel).toBe('1');
      expect(row.querySelector('.cc-item-kind')).toBeFalsy();
      expect(row.querySelector('.cc-sidebar-row-trailing')).toBeTruthy();
    });
    contactRows.filter((row) => row.dataset.contactKind === 'friend').forEach((row) => {
      expect(row.querySelector('svg.cc-friend-contact-icon')).toBeTruthy();
      expect(row.querySelector('.v3-status-dot')).toBeFalsy();
    });
    contactRows.filter((row) => row.dataset.contactKind === 'agent').forEach((row) => {
      expect(row.querySelector('.cc-agent-row-trailing > .cc-sidebar-row-actions.v3-agent-row-actions')).toBeTruthy();
    });
  });

  it('does not render an Agent as a friend when the friends payload omits its bot marker', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'Virtual Catsco',
          is_group: false,
          is_bot: false,
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [
        { id: 8, username: 'alice', display_name: 'Alice' },
        { id: 43, username: 'virtual-catsco', display_name: 'Virtual Catsco' },
        { id: 44, username: 'saturday', display_name: 'Saturday', is_bot: true },
      ],
    });
    api.getAgents.mockResolvedValue({
      agents: [
        { id: 43, uid: 43, username: 'virtual-catsco', display_name: 'Virtual Catsco', relation: 'friend' },
        { id: 44, uid: 44, username: 'saturday', display_name: 'Saturday', relation: 'friend' },
      ],
    });

    await mount();

    const contactRows = Array.from(container.querySelectorAll('.cc-contact-item'));
    expect(contactRows.map((row) => row.getAttribute('data-contact-kind'))).toEqual([
      'friend',
      'agent',
      'agent',
    ]);
    expect(contactRows.filter((row) => row.textContent.includes('Virtual Catsco'))).toHaveLength(1);
    expect(contactRows.filter((row) => row.textContent.includes('Saturday'))).toHaveLength(1);
    expect(contactRows.find((row) => row.textContent.includes('Virtual Catsco'))?.dataset.contactKind).toBe('agent');
    expect(contactRows.find((row) => row.textContent.includes('Saturday'))?.dataset.contactKind).toBe('agent');
  });

  it('keeps pinned group chats above newer group activity and persists the choice', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_20',
          group_id: 20,
          name: 'Old Group',
          is_group: true,
          last_time: '2026-06-02T08:00:00Z',
          latest_seq: 1,
        },
        {
          id: 'grp_21',
          group_id: 21,
          name: 'Busy Group',
          is_group: true,
          last_time: '2026-06-08T08:00:00Z',
          latest_seq: 20,
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [
        { id: 20, name: 'Old Group', owner_id: 7, created_at: '2026-06-02T08:00:00Z' },
        { id: 21, name: 'Busy Group', owner_id: 7, created_at: '2026-06-08T08:00:00Z' },
      ],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    expect(container.textContent.indexOf('Busy Group')).toBeLessThan(container.textContent.indexOf('Old Group'));

    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="Old Group 更多操作"]'));
    });
    const pinOldGroup = container.querySelector('button[aria-label="置顶任务 Old Group"]');
    expect(pinOldGroup).toBeTruthy();
    await act(async () => {
      Simulate.click(pinOldGroup);
      await Promise.resolve();
    });

    expect(container.textContent.indexOf('Old Group')).toBeLessThan(container.textContent.indexOf('Busy Group'));

    await remount();

    expect(container.textContent.indexOf('Old Group')).toBeLessThan(container.textContent.indexOf('Busy Group'));
    await act(async () => {
      Simulate.click(container.querySelector('button[aria-label="Old Group 更多操作"]'));
    });
    expect(container.querySelector('button[aria-label="取消置顶任务 Old Group"]')).toBeTruthy();
  });

  it('keeps pinned history tasks above newer tasks and persists the choice independently', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Old Agent Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-06-02T08:00:00Z',
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'Busy Agent Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-06-08T08:00:00Z',
        },
      ],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    expect(container.textContent.indexOf('Busy Agent Task')).toBeLessThan(container.textContent.indexOf('Old Agent Task'));
    const pinOldTask = container.querySelector('button[aria-label="置顶任务 Old Agent Task"]');
    expect(pinOldTask).toBeTruthy();

    await act(async () => {
      Simulate.click(pinOldTask);
      await Promise.resolve();
    });

    expect(onSelectTopic).not.toHaveBeenCalled();
    expect(container.textContent.indexOf('Old Agent Task')).toBeLessThan(container.textContent.indexOf('Busy Agent Task'));
    expect(JSON.parse(localStorage.getItem('cc_pinned_history_v1:7'))).toEqual(['p2p_7_42']);
    expect(localStorage.getItem('cc_pinned_groups_v1:7')).toBeNull();

    await remount();

    expect(container.textContent.indexOf('Old Agent Task')).toBeLessThan(container.textContent.indexOf('Busy Agent Task'));
    expect(container.querySelector('button[aria-label="取消置顶任务 Old Agent Task"]')).toBeTruthy();
  });

  it('groups row times and controls in shared trailing and actions containers', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Agent Task',
          is_group: false,
          is_bot: true,
          last_time: '2026-06-08T08:00:00Z',
        },
        {
          id: 'p2p_7_8',
          friend_id: 8,
          name: 'Alice',
          is_group: false,
          is_bot: false,
          last_time: '2026-06-07T08:00:00Z',
        },
        {
          id: 'grp_20',
          group_id: 20,
          name: 'Team Room',
          is_group: true,
          last_time: '2026-06-06T08:00:00Z',
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [{ id: 20, name: 'Team Room', owner_id: 7, created_at: '2026-06-06T08:00:00Z' }],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const historyRow = container.querySelector('.cc-history-item');
    const friendRow = container.querySelector('.v3-friend-chat-item');
    const groupRow = Array.from(container.querySelectorAll('.v3-chat-item'))
      .find((row) => row.textContent.includes('Team Room'));

    [historyRow, friendRow, groupRow].forEach((row) => {
      expect(row).toBeTruthy();
      expect(row.classList.contains('cc-sidebar-item-row')).toBe(true);
      expect(row.dataset.sidebarLevel).toBe('1');
      const trailing = row.querySelector('.cc-chat-row-trailing');
      expect(trailing).toBeTruthy();
      expect(trailing.classList.contains('cc-sidebar-row-trailing')).toBe(true);
      expect(trailing.querySelector('.cc-chat-row-time')).toBeTruthy();
      expect(trailing.querySelector('.cc-sidebar-row-actions')).toBeTruthy();
    });

    expect(historyRow.querySelector('.cc-chat-row-actions .v3-history-menu-trigger')).toBeTruthy();
    expect(friendRow.querySelector('.cc-chat-row-actions .v3-friend-menu-trigger')).toBeTruthy();
    expect(groupRow.querySelector('.cc-chat-row-actions .v3-history-menu-trigger')).toBeTruthy();

    await act(async () => {
      Simulate.click(friendRow.querySelector('.v3-friend-menu-trigger'));
    });
    const friendMenu = friendRow.querySelector('.v3-friend-action-menu');
    expect(friendMenu).toBeTruthy();
    expect(friendRow.querySelector('.cc-chat-row-trailing').contains(friendMenu)).toBe(false);
  });

  it('falls back to created_at when direct conversations have no last_time', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_42',
          friend_id: 42,
          name: 'Older Agent',
          is_group: false,
          is_bot: true,
          created_at: '2026-06-04T08:00:00Z',
          latest_seq: 999,
        },
        {
          id: 'p2p_7_43',
          friend_id: 43,
          name: 'Newer Agent',
          is_group: false,
          is_bot: true,
          created_at: '2026-06-06T08:00:00Z',
          latest_seq: 1,
        },
      ],
    });
    api.getGroups.mockResolvedValue({ groups: [] });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const text = container.textContent;
    expect(text.indexOf('Newer Agent')).toBeLessThan(text.indexOf('Older Agent'));
  });

  it('falls back to latest_seq when timestamps are equal', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'p2p_7_8',
          friend_id: 8,
          name: 'Higher Seq Friend',
          is_group: false,
          is_bot: false,
          last_time: '2026-06-05T08:00:00Z',
          latest_seq: 20,
        },
        {
          id: 'p2p_7_9',
          friend_id: 9,
          name: 'Lower Seq Friend',
          is_group: false,
          is_bot: false,
          last_time: '2026-06-05T08:00:00Z',
          latest_seq: 10,
        },
      ],
    });
    api.getFriends.mockResolvedValue({
      friends: [
        { id: 8, username: 'higher-seq', display_name: 'Higher Seq Friend' },
        { id: 9, username: 'lower-seq', display_name: 'Lower Seq Friend' },
      ],
    });
    api.getGroups.mockResolvedValue({ groups: [] });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const text = container.textContent;
    expect(text.indexOf('Higher Seq Friend')).toBeLessThan(text.indexOf('Lower Seq Friend'));
  });

  it('preserves group metadata time when conversation payload has no usable timestamp', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_20',
          group_id: 20,
          name: 'Older Group',
          is_group: true,
          latest_seq: 999,
          last_time: 'not-a-date',
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [
        {
          id: 20,
          name: 'Older Group',
          owner_id: 7,
          created_at: '2026-06-02T08:00:00Z',
        },
        {
          id: 21,
          name: 'Newer Empty Group',
          owner_id: 7,
          created_at: '2026-06-07T08:00:00Z',
        },
      ],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const text = container.textContent;
    expect(text.indexOf('Newer Empty Group')).toBeLessThan(text.indexOf('Older Group'));
  });

  it('lets live offline status override stale agent API online state', async () => {
    await mount({ onlineUsers: { 42: false } });

    const agentItem = Array.from(container.querySelectorAll('.v3-chat-item'))
      .find((node) => node.textContent.includes('Dev Agent'));
    expect(agentItem).toBeTruthy();
    expect(agentItem.querySelector('svg.cc-agent-contact-icon.offline')).toBeTruthy();
    expect(agentItem.querySelector('.v3-status-dot')).toBeFalsy();
  });

  it('uses live Agent presence for a direct task icon', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_42',
        friend_id: 42,
        name: 'Live Agent Task',
        is_group: false,
        is_bot: true,
        is_online: true,
        last_time: '2026-07-20T04:00:00Z',
      }],
    });

    await mount({ onlineUsers: { 42: false } });

    let taskIcon = container.querySelector('[data-task-kind="solo"] .cc-task-agent-icon');
    expect(taskIcon?.classList.contains('offline')).toBe(true);
    expect(taskIcon?.getAttribute('title')).toBe('Agent 离线');

    await mount({ onlineUsers: { 42: true } });

    taskIcon = container.querySelector('[data-task-kind="solo"] .cc-task-agent-icon');
    expect(taskIcon?.classList.contains('online')).toBe(true);
    expect(taskIcon?.getAttribute('title')).toBe('Agent 在线');
  });

  it('keeps group Agent identities while merging metadata and uses any-online semantics', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_80',
          group_id: 80,
          name: 'Multi Agent Review',
          is_group: true,
          has_bot: true,
          is_agent_task: true,
          member_count: 3,
          last_time: '2026-07-20T04:00:00Z',
        },
        {
          id: 'grp_81',
          group_id: 81,
          name: 'Human Planning',
          is_group: true,
          member_count: 3,
          last_time: '2026-07-20T03:00:00Z',
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [
        {
          id: 80,
          name: 'Multi Agent Review',
          owner_id: 7,
          kind: 'agent_task',
          has_bot: true,
          member_count: 3,
          agent_ids: [42, 43],
        },
        {
          id: 81,
          name: 'Human Planning',
          owner_id: 7,
          member_count: 3,
        },
      ],
    });
    api.getAgents.mockResolvedValue({
      agents: [
        { id: 42, uid: 42, display_name: 'Dev Agent', is_online: false },
        { id: 43, uid: 43, display_name: 'Review Agent', is_online: false },
        { id: 99, uid: 99, display_name: 'Unrelated Agent', is_online: true },
      ],
    });

    await mount({ onlineUsers: { 42: false, 43: true, 99: true } });

    const taskRows = Array.from(container.querySelectorAll('[data-task-kind="collaboration"]'));
    const agentTask = taskRows.find((row) => row.textContent.includes('Multi Agent Review'));
    const humanTask = taskRows.find((row) => row.textContent.includes('Human Planning'));
    expect(agentTask?.querySelector('.cc-task-agent-icon.online')?.getAttribute('title')).toBe('1/2 个 Agent 在线');
    expect(humanTask?.querySelector('.cc-task-agent-icon.offline')?.getAttribute('title')).toBe('未关联 Agent');

    await mount({ onlineUsers: { 42: false, 43: false, 99: true } });

    const offlineAgentTask = Array.from(container.querySelectorAll('[data-task-kind="collaboration"]'))
      .find((row) => row.textContent.includes('Multi Agent Review'));
    expect(offlineAgentTask?.querySelector('.cc-task-agent-icon.offline')?.getAttribute('title')).toBe('0/2 个 Agent 在线');
  });

  it('uses the server-returned mute state for a contact conversation', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [{
        id: 'p2p_7_8',
        friend_id: 8,
        name: 'Alice',
        is_group: false,
        notifications_muted: false,
      }],
    });
    api.getFriends.mockResolvedValue({
      friends: [{ id: 8, username: 'alice', display_name: 'Alice' }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });
    api.setConversationNotificationsMuted.mockResolvedValue({
      topic_id: 'p2p_7_8',
      notifications_muted: false,
    });

    await mount();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Alice 联系人操作"]'));
    });
    const muteAction = container.querySelector('[aria-label="静音此会话 Alice"]');
    expect(muteAction).toBeTruthy();

    await act(async () => {
      Simulate.click(muteAction);
      await Promise.resolve();
    });

    expect(api.setConversationNotificationsMuted).toHaveBeenCalledWith('p2p_7_8', true);
    expect(container.querySelector('[aria-label="Alice 已静音"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="Alice 联系人操作"]'));
    });
    expect(container.querySelector('[aria-label="静音此会话 Alice"]')).toBeTruthy();
  });
});
