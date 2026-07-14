import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../widgets/create-group', () => ({
  default: function MockCreateGroup() {
    return null;
  },
}));

vi.mock('../widgets/add-friend', () => ({
  default: function MockAddFriend() {
    return null;
  },
}));

vi.mock('../widgets/friend-request', () => ({
  default: function MockFriendRequest() {
    return null;
  },
}));

vi.mock('../widgets/agent-store-modal', () => ({
  default: function MockAgentStoreModal() {
    return null;
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
    openAgent: vi.fn(),
    acceptAgentFriend: vi.fn(),
    rejectAgentFriend: vi.fn(),
    acceptFriend: vi.fn(),
    rejectFriend: vi.fn(),
    removeFriend: vi.fn(),
    disbandGroup: vi.fn(),
  },
  onWSMessage: vi.fn(() => vi.fn()),
  updateTopicSeq: vi.fn(),
}));

import ChatListView from './sidepanel-view';
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

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
    api.getConversations.mockResolvedValue({ conversations: [] });
    api.getFriends.mockResolvedValue({ friends: [] });
    api.getGroups.mockResolvedValue({ groups: [] });
    api.getPendingRequests.mockResolvedValue({ requests: [] });
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
    onWSMessage.mockImplementation(() => vi.fn());
    onSelectTopic = vi.fn();

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

  it('opens an agent conversation from the assistant roster', async () => {
    await mount();

    expect(container.textContent).toContain('Agent 助手');
    expect(container.textContent).toContain('Dev Agent');
    const agentItem = Array.from(container.querySelectorAll('.v3-chat-item'))
      .find((node) => node.textContent.includes('Dev Agent'));
    expect(agentItem).toBeTruthy();

    await act(async () => {
      Simulate.click(agentItem);
      await Promise.resolve();
    });

    expect(api.openAgent).toHaveBeenCalledWith(42);
    expect(onSelectTopic).toHaveBeenCalledWith(expect.objectContaining({
      topicId: 'p2p_7_42',
      name: 'Dev Agent',
      friendId: 42,
      isBot: true,
    }));
  });

  it('opens mobile binding from an assistant row without opening the conversation', async () => {
    await mount();

    const mobileButton = container.querySelector('[aria-label="Dev Agent 移动端使用"]');
    expect(mobileButton).toBeTruthy();
    expect(container.querySelector('[aria-label="移除 Dev Agent"]')).toBeFalsy();

    await act(async () => {
      Simulate.click(mobileButton);
      await Promise.resolve();
    });

    expect(api.openAgent).not.toHaveBeenCalled();
    expect(container.textContent).toContain('移动端使用');
    expect(container.textContent).toContain('Dev Agent');
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

    const mobileButton = container.querySelector('[aria-label="Virtual Team 移动端使用"]');
    expect(mobileButton).toBeTruthy();

    await act(async () => {
      Simulate.click(mobileButton);
      await Promise.resolve();
    });

    expect(onSelectTopic).not.toHaveBeenCalled();
    expect(container.textContent).toContain('移动端使用');
    expect(container.textContent).toContain('Virtual Team');
    expect(container.querySelector('[data-testid="mobile-channel-group-id"]').textContent).toBe('88');
    expect(container.querySelector('[data-testid="mobile-channel-topic-id"]').textContent).toBe('grp_88');
  });

  it('removes friend agents directly from the assistant row', async () => {
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

    const removeButton = container.querySelector('[aria-label="移除 共享助手"]');
    expect(removeButton).toBeTruthy();
    expect(container.querySelector('[aria-label="共享助手 移动端使用"]')).toBeTruthy();

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

  it('keeps server-confirmed bot groups in the groups section', async () => {
    api.getConversations.mockResolvedValue({
      conversations: [
        {
          id: 'grp_9',
          group_id: 9,
          name: 'Bot Room',
          is_group: true,
          has_bot: true,
          last_time: '2026-06-04T08:00:00Z',
        },
      ],
    });
    api.getGroups.mockResolvedValue({
      groups: [{ id: 9, name: 'Bot Room', owner_id: 7, created_at: '2026-06-04T08:00:00Z' }],
    });
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const text = container.textContent;
    expect(text).toContain('Bot Room');
    expect(text.indexOf('群聊')).toBeLessThan(text.indexOf('Bot Room'));
    expect(text.indexOf('Bot Room')).toBeLessThan(text.indexOf('Agent 助手'));
  });

  it('shows matches from collapsed sections while searching', async () => {
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
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    expect(container.textContent).toContain('Alice');
    await act(async () => {
      clickSection('好友');
    });
    expect(container.textContent).not.toContain('Alice');

    const input = container.querySelector('input');
    await act(async () => {
      input.value = 'Alice';
      Simulate.change(input, { target: { value: 'Alice' } });
    });

    expect(container.textContent).toContain('Alice');
    expect(container.textContent).not.toContain('没有匹配结果');
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
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    expect(container.textContent).toContain('Alice');
    await act(async () => {
      clickSection('好友');
    });
    expect(container.textContent).not.toContain('Alice');
    expect(localStorage.getItem('cc_sidebar_collapsed_v1:7')).toContain('"friends":true');

    await remount();

    expect(container.textContent).toContain('好友');
    expect(container.textContent).not.toContain('Alice');
  });

  it('keeps group conversations in the groups section by default', async () => {
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
          owner_id: 11,
        },
      ],
    });

    await mount();

    const sections = Array.from(container.querySelectorAll('.v3-chat-section')).map((node) => node.textContent);
    expect(sections.join(' | ')).toContain('群聊');
    expect(sections.findIndex((text) => text.includes('群聊'))).toBeLessThan(
      sections.findIndex((text) => text.includes('Agent 助手'))
    );

    const groupItem = Array.from(container.querySelectorAll('.v3-chat-item'))
      .find((node) => node.textContent.includes('查云端log'));
    expect(groupItem).toBeTruthy();

    await act(async () => {
      Simulate.click(groupItem);
      await Promise.resolve();
    });

    expect(onSelectTopic).toHaveBeenCalledWith({
      topicId: 'grp_9',
      name: '查云端log',
      isGroup: true,
      groupId: 9,
      avatar_url: undefined,
    });
  });

  it('orders each chat section by recent activity and new group creation time', async () => {
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
    api.getAgents.mockResolvedValue({ agents: [] });

    await mount();

    const text = container.textContent;
    expect(text.indexOf('New Agent')).toBeLessThan(text.indexOf('Old Agent'));
    expect(text.indexOf('New Friend')).toBeLessThan(text.indexOf('Old Friend'));
    expect(text.indexOf('New Empty Group')).toBeLessThan(text.indexOf('Old Group'));
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

    const pinOldGroup = container.querySelector('button[aria-label="置顶 Old Group"]');
    expect(pinOldGroup).toBeTruthy();
    await act(async () => {
      Simulate.click(pinOldGroup);
      await Promise.resolve();
    });

    expect(container.textContent.indexOf('Old Group')).toBeLessThan(container.textContent.indexOf('Busy Group'));

    await remount();

    expect(container.textContent.indexOf('Old Group')).toBeLessThan(container.textContent.indexOf('Busy Group'));
    expect(container.querySelector('button[aria-label="取消置顶 Old Group"]')).toBeTruthy();
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
    expect(agentItem.querySelector('[aria-label="Offline"]')).toBeTruthy();
    expect(agentItem.querySelector('[aria-label="Online"]')).toBeFalsy();
  });
});
