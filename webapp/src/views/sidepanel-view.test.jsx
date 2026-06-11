import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

jest.mock('../widgets/create-group', () => function MockCreateGroup() {
  return null;
});

jest.mock('../widgets/add-friend', () => function MockAddFriend() {
  return null;
});

jest.mock('../widgets/friend-request', () => function MockFriendRequest() {
  return null;
});

jest.mock('../widgets/agent-store-modal', () => function MockAgentStoreModal() {
  return null;
});

jest.mock('../api', () => ({
  api: {
    getConversations: jest.fn(),
    getFriends: jest.fn(),
    getGroups: jest.fn(),
    getPendingRequests: jest.fn(),
    getAgents: jest.fn(),
    openAgent: jest.fn(),
    acceptFriend: jest.fn(),
    rejectFriend: jest.fn(),
    disbandGroup: jest.fn(),
  },
  onWSMessage: jest.fn(() => jest.fn()),
  updateTopicSeq: jest.fn(),
}));

const ChatListView = require('./sidepanel-view').default;
const { api, onWSMessage } = require('../api');

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
    onWSMessage.mockImplementation(() => jest.fn());
    onSelectTopic = jest.fn();

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

  function clickSection(label) {
    const section = Array.from(container.querySelectorAll('.v3-chat-section span'))
      .find((node) => node.textContent.includes(label));
    expect(section).toBeTruthy();
    Simulate.click(section);
  }

  it('renders agents as a non-chat assistant roster', async () => {
    await mount();

    expect(container.textContent).toContain('AI 助手');
    expect(container.textContent).toContain('Dev Agent');

    await act(async () => {
      Simulate.click(container.querySelector('.v3-chat-item'));
      await Promise.resolve();
    });

    expect(api.openAgent).not.toHaveBeenCalled();
    expect(onSelectTopic).not.toHaveBeenCalled();
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
    expect(text.indexOf('Bot Room')).toBeLessThan(text.indexOf('AI 助手'));
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
      sections.findIndex((text) => text.includes('AI 助手'))
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
