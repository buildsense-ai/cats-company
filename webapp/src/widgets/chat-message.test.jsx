import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

jest.mock('marked', () => ({
  marked: {
    setOptions: jest.fn(),
    parse: (text) => {
      if (String(text).includes('javascript:')) {
        return '<p><a href="javascript:alert(1)" onclick="alert(2)">bad</a></p>';
      }
      if (String(text).includes('[[toc-preview-fixture]]')) {
        return [
          '<nav><a href="#章节一">章节一</a><a href="#external">外部说明</a></nav>',
          '<h2>章节一</h2>',
          '<p><a href="https://example.com/docs">外部链接</a></p>',
        ].join('');
      }
      return `<p>${text}</p>`;
    },
  },
}));

jest.mock('../api', () => ({
  resolveMediaURL: (url) => url,
}));

jest.mock('./avatar', () => function MockAvatar() {
  return <div data-testid="avatar" />;
});

const chatMessageModule = require('./chat-message');
const ChatMessage = chatMessageModule.default;
const { FilePreviewPanel } = chatMessageModule;
const { markdownPreviewDocument } = require('./markdown-utils');

function PreviewHarness({ message }) {
  const [previewFile, setPreviewFile] = React.useState(null);
  return (
    <div className={`v3-message-workspace${previewFile ? ' has-preview' : ''}`}>
      <div className="v3-chat-column">
        <ChatMessage
          message={message}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          onPreviewFile={setPreviewFile}
          activePreviewFile={previewFile}
        />
      </div>
      {previewFile && <FilePreviewPanel file={previewFile} onClose={() => setPreviewFile(null)} />}
    </div>
  );
}

describe('ChatMessage rich file rendering', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    window.open = jest.fn();
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: jest.fn(() => Promise.resolve()),
      },
    });
    global.fetch = jest.fn(() => Promise.resolve({
      ok: true,
      text: () => Promise.resolve('<!doctype html><h1>Report</h1><script>window.evil=true</script>'),
    }));

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

  it('previews uploaded HTML as a sandboxed workflow report artifact', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 1,
            from_uid: 2,
            content: '[文件] report.html',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'report.html',
                url: 'https://app.catsco.cc/uploads/files/report.html',
                size: 2048,
                mime_type: 'text/html',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-attachment-name').textContent).toBe('report.html');
    expect(container.querySelector('.v3-attachment-size').textContent).toContain('HTML');

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(global.fetch).toHaveBeenCalledWith('/uploads/files/report.html');
    expect(container.querySelector('.v3-file-preview-panel')).not.toBeNull();
    const frame = container.querySelector('iframe.v3-file-preview-frame');
    expect(frame).not.toBeNull();
    expect(frame.getAttribute('sandbox')).toContain('allow-scripts');
    expect(frame.getAttribute('sandbox')).toContain('allow-forms');
    expect(frame.getAttribute('sandbox')).not.toContain('allow-same-origin');
    expect(frame.getAttribute('srcdoc')).toContain('<h1>Report</h1>');
  });

  it('keeps markdown preview table-of-contents links inside the preview frame', () => {
    const html = markdownPreviewDocument('[[toc-preview-fixture]]');
    const doc = new DOMParser().parseFromString(html, 'text/html');

    expect(doc.querySelector('base').getAttribute('href')).toBe('about:srcdoc');
    const tocLink = doc.querySelector('a[href="#章节一"]');
    expect(tocLink).not.toBeNull();
    expect(tocLink.getAttribute('target')).toBe('_self');
    expect(tocLink.getAttribute('rel')).toBeNull();
    expect(doc.querySelector('h2#章节一')).not.toBeNull();

    const externalLink = doc.querySelector('a[href="https://example.com/docs"]');
    expect(externalLink).not.toBeNull();
    expect(externalLink.getAttribute('target')).toBe('_blank');
    expect(externalLink.getAttribute('rel')).toBe('noopener noreferrer');
  });

  it('does not leave javascript links active in markdown message rendering', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 2,
            from_uid: 2,
            content: '[bad](javascript:alert(1))',
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    const link = container.querySelector('a');
    expect(link).not.toBeNull();
    expect(link.getAttribute('href')).toBeNull();
    expect(link.getAttribute('onclick')).toBeNull();
  });

  it('uses the hover action toolbar for reply and more actions without dead reaction buttons', async () => {
    const onReply = jest.fn();
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 20,
            from_uid: 2,
            content: '这是一条可以复制的消息',
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          onReply={onReply}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('[aria-label="Add Reaction"]')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="回复"]'));
      await Promise.resolve();
    });
    expect(onReply).toHaveBeenCalledTimes(1);

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="更多操作"]'));
      await Promise.resolve();
    });
    const menu = container.querySelector('.v3-message-action-menu');
    expect(menu).not.toBeNull();

    await act(async () => {
      Simulate.click(menu.querySelector('[role="menuitem"]'));
      await Promise.resolve();
    });

    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('这是一条可以复制的消息');
    expect(menu.textContent).toContain('已复制');
  });

  it('renders update_plan working tools as a plan card fallback', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 21,
            from_uid: 2,
            content: '',
            created_at: '2026-06-09T00:00:00Z',
          }}
          workingMessages={[
            {
              type: 'tool_use',
              content: 'update_plan',
              metadata: {
                id: 'plan-1',
                input: {
                  steps: [
                    { status: 'in_progress', step: '创建临时工作目录' },
                    { status: 'pending', step: '设计 analyzeReply 函数' },
                    { status: 'pending', step: '写测试用例' },
                  ],
                },
              },
            },
            {
              type: 'tool_result',
              content: '计划已更新：0/3 已完成\n进行中：创建临时工作目录\n1. 进行中 - 创建临时工作目录\n2. 待处理 - 设计 analyzeReply 函数\n3. 待处理 - 写测试用例',
              metadata: {
                tool_use_id: 'plan-1',
              },
            },
          ]}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-working-toggle'));
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-wpi-plan')).not.toBeNull();
    expect(container.querySelector('.v3-wpi-plan').textContent).toContain('计划已更新');
    expect(container.querySelector('.v3-wpi-plan').textContent).toContain('创建临时工作目录');
    expect(container.querySelector('.v3-wpi-plan').textContent).toContain('设计 analyzeReply 函数');
    expect(container.querySelector('.v3-wpi-tool-name')).toBeNull();
  });

  it('opens external HTML files instead of fetching them into the preview panel', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 3,
            from_uid: 2,
            content: '[文件] report.html',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'report.html',
                url: 'https://example.com/report.html',
                size: 2048,
                mime_type: 'text/html',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
    });

    expect(global.fetch).not.toHaveBeenCalled();
    expect(window.open).toHaveBeenCalledWith('https://example.com/report.html', '_blank');
    expect(container.querySelector('.v3-file-preview-panel')).toBeNull();
  });

  it('previews PDF files in the side panel without fetching their content', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 4,
            from_uid: 2,
            content: '[文件] report.pdf',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'report.pdf',
                url: '/uploads/files/report.pdf',
                size: 2048,
                mime_type: 'application/pdf',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
    });

    expect(global.fetch).not.toHaveBeenCalled();
    const panel = container.querySelector('.v3-file-preview-panel');
    expect(panel).not.toBeNull();
    expect(panel.querySelector('iframe.v3-file-preview-frame').getAttribute('src')).toBe('/uploads/files/report.pdf');
  });

  it('switches the side preview when another file card is clicked', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 5,
            from_uid: 2,
            content: '[文件] report.html, summary.md',
            content_blocks: [
              {
                type: 'file',
                payload: {
                  name: 'report.html',
                  url: '/uploads/files/report.html',
                  size: 2048,
                  mime_type: 'text/html',
                },
              },
              {
                type: 'file',
                payload: {
                  name: 'summary.md',
                  url: '/uploads/files/summary.md',
                  size: 1024,
                  mime_type: 'text/markdown',
                },
              },
            ],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    const cards = container.querySelectorAll('.v3-attachment-card');
    await act(async () => {
      Simulate.click(cards[0].querySelector('.v3-artifact-main'));
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-file-preview-title h3').textContent).toBe('report.html');

    await act(async () => {
      Simulate.click(cards[1].querySelector('.v3-artifact-main'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(container.querySelectorAll('.v3-file-preview-panel')).toHaveLength(1);
    expect(container.querySelector('.v3-file-preview-title h3').textContent).toBe('summary.md');
  });

  it('uses the side preview for legacy JSON file messages', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 6,
            from_uid: 2,
            content: {
              type: 'file',
              payload: {
                name: 'legacy-report.html',
                url: '/uploads/files/legacy-report.html',
                size: 2048,
                mime_type: 'text/html',
              },
            },
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(window.open).not.toHaveBeenCalled();
    expect(global.fetch).toHaveBeenCalledWith('/uploads/files/legacy-report.html');
    expect(container.querySelectorAll('.v3-file-preview-panel')).toHaveLength(1);
    expect(container.querySelector('.v3-file-preview-title h3').textContent).toBe('legacy-report.html');
  });

  it('shows separate preview and download actions on file cards', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 7,
            from_uid: 2,
            content: '[文件] report.pdf',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'report.pdf',
                url: '/uploads/files/report.pdf',
                size: 2048,
                mime_type: 'application/pdf',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    const actions = container.querySelectorAll('.v3-artifact-action');
    expect(actions).toHaveLength(2);
    expect(actions[0].textContent).toContain('预览');
    expect(actions[1].textContent).toContain('下载');
    expect(actions[1].getAttribute('href')).toBe('/uploads/files/report.pdf');
    expect(actions[1].hasAttribute('download')).toBe(true);
  });

  it('marks DOCX as downloadable without claiming browser preview support', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 8,
            from_uid: 2,
            content: '[文件] handout.docx',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'handout.docx',
                url: '/uploads/files/handout.docx',
                size: 2048,
                mime_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-attachment-name').textContent).toBe('handout.docx');
    expect(container.querySelector('.v3-attachment-size').textContent).toContain('Word');
    const previewButton = container.querySelector('button.v3-artifact-action');
    expect(previewButton.disabled).toBe(true);
    expect(container.querySelector('a.v3-artifact-action').getAttribute('href')).toBe('/uploads/files/handout.docx');
  });
});
