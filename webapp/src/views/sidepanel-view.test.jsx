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

  async function mount() {
    await act(async () => {
      root.render(
        <ChatListView
          activeTopic={null}
          onSelectTopic={onSelectTopic}
          user={user}
          onlineUsers={{}}
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

  it('classifies server-confirmed bot groups into the AI conversations section', async () => {
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
    expect(text.indexOf('AI 对话')).toBeLessThan(text.indexOf('Bot Room'));
    expect(text.indexOf('Bot Room')).toBeLessThan(text.indexOf('好友'));
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
});
