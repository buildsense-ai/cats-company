import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    createConversationShare: vi.fn(),
    revokeConversationShare: vi.fn(),
  },
}));

import { api } from '../api';
import ConversationShareReview from './conversation-share-review';

describe('ConversationShareReview', () => {
  let container;
  let root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    api.createConversationShare.mockResolvedValue({
      id: 'share-1',
      url: 'https://app.example.test/share/capability',
      message_count: 2,
    });
    api.revokeConversationShare.mockResolvedValue({ revoked: true });
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn(() => Promise.resolve()) },
    });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it('creates a short-lived exact-message share and lets its owner revoke it', async () => {
    const onComplete = vi.fn();
    await act(async () => {
      root.render(
        <ConversationShareReview
          topicId="p2p_1_2"
          messageIds={[17, 23]}
          onClose={vi.fn()}
          onComplete={onComplete}
        />,
      );
    });

    await act(async () => {
      Simulate.submit(container.querySelector('form'));
      await Promise.resolve();
    });

    expect(api.createConversationShare).toHaveBeenCalledWith({
      topicId: 'p2p_1_2',
      messageIds: [17, 23],
      title: '会话片段',
      expiresIn: 604800,
    });
    expect(container.textContent).toContain('分享链接已创建');

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="撤销此分享"]'));
      await Promise.resolve();
    });
    expect(api.revokeConversationShare).toHaveBeenCalledWith('share-1');
    expect(container.textContent).toContain('已撤销分享链接');

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('button'))
        .find((button) => button.textContent.includes('关闭')));
    });
    expect(onComplete).toHaveBeenCalledTimes(1);
  });
});
