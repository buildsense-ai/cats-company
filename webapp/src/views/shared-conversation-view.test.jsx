import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    getConversationShare: vi.fn(),
  },
}));

vi.mock('../widgets/chat-message', () => ({
  __esModule: true,
  default: function MockChatMessage({ message, senderName, onPreviewFile }) {
    const file = message.content_blocks?.find((block) => block.type === 'file')?.payload;
    return (
      <article className="shared-message" data-created-at={message.created_at || ''} data-sender={senderName}>
        <span>{message.content}</span>
        {file && <button type="button" onClick={() => onPreviewFile(file)}>预览文件</button>}
      </article>
    );
  },
  FilePreviewPanel: function MockFilePreviewPanel({ file, onClose }) {
    return (
      <aside className="shared-file-preview" data-url={file?.url || ''}>
        <button type="button" aria-label="关闭预览" onClick={onClose}>关闭</button>
      </aside>
    );
  },
}));

import { api } from '../api';
import SharedConversationView from './shared-conversation-view';

describe('SharedConversationView', () => {
  let container;
  let root;

  beforeEach(() => {
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
    api.getConversationShare.mockResolvedValue({
      title: '已分享片段',
      items: [{
        id: 'shared-message-1',
        speaker: 'assistant',
        created_at: '2026-08-17T08:30:00Z',
        content: '仅此一段内容',
        content_blocks: [{
          type: 'file',
          payload: {
            name: 'report.pdf',
            url: '/api/shared-conversations/capability/assets/asset-1',
            mime_type: 'application/pdf',
            size: 128,
          },
        }],
      }],
    });
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  it('renders only the shared transcript and keeps its file preview local to the visitor view', async () => {
    await act(async () => {
      root.render(<SharedConversationView token="capability" />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(api.getConversationShare).toHaveBeenCalledWith('capability', expect.any(Object));
    expect(container.querySelector('.shared-message')?.textContent).toContain('仅此一段内容');
    expect(container.querySelector('.shared-message')?.dataset.sender).toBe('CatsCo');
    expect(container.querySelector('.shared-message')?.dataset.createdAt).toBe('2026-08-17T08:30:00Z');
    expect(container.querySelector('textarea')).toBeNull();
    expect(container.querySelector('[aria-label="发送"]')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('button:not([aria-label])'));
    });
    expect(container.querySelector('.shared-file-preview')?.dataset.url)
      .toBe('/api/shared-conversations/capability/assets/asset-1');
  });
});
