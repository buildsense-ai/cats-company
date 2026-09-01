import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

vi.mock('marked', () => ({
  marked: {
    setOptions: vi.fn(),
    parse: (text) => {
      if (String(text).includes('javascript:')) {
        return '<p><a href="javascript:alert(1)" onclick="alert(2)">bad</a></p>';
      }
      if (String(text).includes('bg-summary-test')) {
        return [
          '<p>下面是本次数据的简版结论。</p>',
          '<table><thead><tr><th>#</th><th>文件夹</th><th>项目</th><th>页面</th></tr></thead><tbody><tr><td>1</td><td>bg-summary-test</td><td>后台子任务回流测试</td><td>index + about + notes</td></tr></tbody></table>',
          '<p>表格之外的补充说明。</p>',
        ].join('');
      }
      if (/^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)+\|?\s*$/m.test(String(text))) {
        return '<table><thead><tr><th>场景</th><th>谁来填</th></tr></thead><tbody><tr><td>情况一</td><td>HR</td></tr></tbody></table>';
      }
      if (String(text).includes('[[toc-preview-fixture]]')) {
        return [
          '<nav><a href="#章节一">章节一</a><a href="#external">外部说明</a></nav>',
          '<h2>章节一</h2>',
          '<p><a href="https://example.com/docs">外部链接</a></p>',
        ].join('');
      }
      if (String(text).includes('artifact-link-test')) {
        return '<p>已发布：<a href="https://artifacts.example.test/by-agent/440/lesson-game/latest/">artifact-link-test</a></p>';
      }
      return `<p>${text}</p>`;
    },
  },
}));

vi.mock('../api', () => ({
  resolveMediaURL: vi.fn((url) => url),
  getApiBaseURL: () => window.location.origin,
}));

vi.mock('./avatar', () => ({
  default: function MockAvatar() {
    return <div data-testid="avatar" />;
  },
}));

vi.mock('./mobile-pdf-preview', () => ({
  default: function MockMobilePdfPreview({ url }) {
    return <div className="v3-mobile-pdf-preview-mock" data-url={url}>PDF reader</div>;
  },
}));

vi.mock('read-excel-file/browser', () => ({
  __esModule: true,
  default: vi.fn(),
}));

import ChatMessage, { createCloudArtifactPreviewFile, FilePreviewPanel, previewFileDescriptor } from './chat-message';
import { resolveMediaURL } from '../api';
import { markdownPreviewDocument } from './markdown-utils';
import readExcelFile from 'read-excel-file/browser';

const catscoUiSystemCss = readFileSync(
  resolve(process.cwd(), 'src/css/catsco-ui-system.css'),
  'utf8',
);

function PreviewHarness({
  message,
  knownArtifacts = [],
  isSelf = false,
  onOpenRemoteArtifactFullscreen = vi.fn(),
}) {
  const [previewFile, setPreviewFile] = React.useState(null);
  const chatColumnRef = React.useRef(null);
  return (
    <div className={`v3-message-workspace${previewFile ? ' has-preview' : ''}`}>
      <div ref={chatColumnRef} className="v3-chat-column">
        <ChatMessage
          message={message}
          isSelf={isSelf}
          isGroup={false}
          senderName="CatsCo"
          onPreviewFile={setPreviewFile}
          activePreviewFile={previewFile}
          knownArtifacts={knownArtifacts}
        />
      </div>
      {previewFile && (
        <div className="v3-file-preview-shell">
          <FilePreviewPanel
            file={previewFile}
            onClose={() => setPreviewFile(null)}
            backgroundRef={chatColumnRef}
            onOpenRemoteArtifactFullscreen={onOpenRemoteArtifactFullscreen}
          />
        </div>
      )}
    </div>
  );
}

async function flushAsync(times = 6) {
  for (let index = 0; index < times; index += 1) {
    await Promise.resolve();
  }
}

function utf8ArrayBuffer(text) {
  const buffer = Buffer.from(text, 'utf8');
  return buffer.buffer.slice(buffer.byteOffset, buffer.byteOffset + buffer.byteLength);
}

describe('ChatMessage rich file rendering', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    window.open = vi.fn();
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: false });
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: vi.fn(() => Promise.resolve()),
      },
    });
    global.fetch = vi.fn(() => Promise.resolve({
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
    vi.clearAllMocks();
    vi.useRealTimers();
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
    expect(container.querySelector('.v3-attachment-size').textContent).toBe('HTML · 2.0 KB');
    expect(container.querySelector('.v3-message').classList.contains('has-file-only')).toBe(true);

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(global.fetch).toHaveBeenCalledWith(
      '/uploads/files/report.html',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(container.querySelector('.v3-file-preview-panel')).not.toBeNull();
    const frame = container.querySelector('iframe.v3-file-preview-frame');
    expect(frame).not.toBeNull();
    expect(frame.getAttribute('sandbox')).toContain('allow-scripts');
    expect(frame.getAttribute('sandbox')).toContain('allow-forms');
    expect(frame.getAttribute('sandbox')).not.toContain('allow-same-origin');
    expect(frame.getAttribute('srcdoc')).toContain('<h1>Report</h1>');
  });

  it('describes a failed preview as a temporary service problem when the media endpoint returns a gateway error', async () => {
    global.fetch = vi.fn().mockResolvedValue({ ok: false, status: 502 });

    await act(async () => {
      root.render(
        <FilePreviewPanel
          file={{
            name: 'notes.txt',
            url: '/uploads/files/notes.txt',
            size: 128,
            mime_type: 'text/plain',
          }}
          onClose={vi.fn()}
        />,
      );
      await flushAsync();
    });

    expect(container.querySelector('.v3-file-preview-state.error')?.textContent).toBe(
      '预览加载失败：服务暂时不可用，请稍后重试',
    );
  });

  it('previews an image file in the side panel without fetching it as text', async () => {
    const image = {
      type: 'image',
      name: '课堂照片.jpg',
      url: '/uploads/images/classroom.jpg',
      mime_type: 'image/jpeg',
      size: 2048,
    };
    const descriptor = previewFileDescriptor(image);
    expect(descriptor?.isImage).toBe(true);
    expect(descriptor?.canPreview).toBe(true);

    await act(async () => {
      root.render(<FilePreviewPanel file={image} onClose={vi.fn()} />);
      await flushAsync();
    });

    expect(global.fetch).not.toHaveBeenCalled();
    const panel = container.querySelector('.v3-file-preview-panel');
    expect(panel).not.toBeNull();
    const preview = panel.querySelector('.v3-file-preview-image img');
    expect(preview?.getAttribute('src')).toBe('/uploads/images/classroom.jpg');
    expect(preview?.getAttribute('alt')).toBe('课堂照片.jpg');
    expect(panel.querySelector('a[download]')?.getAttribute('href')).toBe('/uploads/images/classroom.jpg?download=1');

    await act(async () => {
      Simulate.error(preview);
      await Promise.resolve();
    });
    expect(panel.textContent).toContain('图片加载失败');
  });

  it('normalizes a disconnected media endpoint instead of showing the browser error', async () => {
    global.fetch = vi.fn().mockRejectedValue(new TypeError('Failed to fetch'));

    await act(async () => {
      root.render(
        <FilePreviewPanel
          file={{
            name: 'notes.txt',
            url: '/uploads/files/notes.txt',
            size: 128,
            mime_type: 'text/plain',
          }}
          onClose={vi.fn()}
        />,
      );
      await flushAsync();
    });

    expect(container.querySelector('.v3-file-preview-state.error')?.textContent).toBe(
      '预览加载失败：暂时无法连接服务，请稍后重试',
    );
  });

  it('renders an Agent delivery artifact before text from the same message', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 20,
            from_uid: 2,
            content: '交付说明',
            content_blocks: [
              { type: 'text', text: '交付说明' },
              {
                type: 'file',
                payload: {
                  name: 'game.zip',
                  url: '/uploads/files/game.zip',
                  size: 4096,
                  mime_type: 'application/zip',
                },
              },
            ],
            created_at: '2026-06-09T00:00:00Z',
          }}
          artifactsFirst
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          senderIsBot
        />,
      );
      await Promise.resolve();
    });

    const content = container.querySelector('.v3-message-content');
    const artifactSection = content.querySelector('.v3-message-deliverables');
    const summarySection = content.querySelector('.v3-message-followup-text');
    const artifact = artifactSection?.querySelector('.v3-attachment-card');
    expect(container.querySelector('.v3-message').classList.contains('artifacts-first')).toBe(true);
    expect(artifactSection?.dataset.messagePart).toBe('artifacts');
    expect(summarySection?.dataset.messagePart).toBe('summary');
    expect(artifact).not.toBeNull();
    expect(summarySection?.textContent).toBe('交付说明');
    expect(artifactSection.compareDocumentPosition(summarySection) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(container.querySelectorAll('.v3-message')).toHaveLength(1);
    expect(container.querySelectorAll('.v3-msg-time')).toHaveLength(1);
  });

  it('removes a redundant artifact delivery announcement from the result summary', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 21,
            from_uid: 2,
            content: 'market-report.pdf 已发出。\n\n报告覆盖近期公开资讯。',
            content_blocks: [
              {
                type: 'file',
                payload: {
                  name: 'market-report.pdf',
                  url: '/uploads/files/market-report.pdf',
                  size: 4096,
                  mime_type: 'application/pdf',
                },
              },
              {
                type: 'text',
                text: 'market-report.pdf 已发出。\n\n报告覆盖近期公开资讯。',
                presentation_role: 'result',
              },
            ],
            created_at: '2026-06-09T00:00:00Z',
          }}
          artifactsFirst
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          senderIsBot
        />,
      );
      await Promise.resolve();
    });

    const summary = container.querySelector('.v3-message-followup-text');
    expect(summary?.textContent).toBe('报告覆盖近期公开资讯。');
    expect(container.textContent).not.toContain('market-report.pdf 已发出。');
    expect(container.querySelector('.v3-attachment-name')?.textContent).toBe('market-report.pdf');
  });

  it('moves Agent process text into the completed tool trace and keeps only the result below the artifact', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 23,
            from_uid: 2,
            content: '已完成验收。\n\n文件已经发送。',
            content_blocks: [
              {
                type: 'file',
                payload: {
                  name: 'game.zip',
                  url: '/uploads/files/game.zip',
                  size: 4096,
                  mime_type: 'application/zip',
                },
              },
              { type: 'text', text: '已完成验收。', presentation_role: 'process' },
              {
                type: 'tool_use',
                id: 'verify-1',
                name: 'execute_shell',
                input: { command: 'npm test' },
              },
              {
                type: 'tool_result',
                tool_use_id: 'verify-1',
                content: 'Tests passed',
              },
              { type: 'text', text: '文件已经发送。', presentation_role: 'result' },
            ],
            created_at: '2026-06-09T00:00:00Z',
          }}
          artifactsFirst
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          senderIsBot
        />,
      );
      await Promise.resolve();
    });

    const sections = container.querySelectorAll('.v3-message-followup-section');
    expect(sections).toHaveLength(1);
    expect(sections[0].dataset.messagePart).toBe('result');
    expect(sections[0].textContent).toBe('文件已经发送。');
    expect(container.querySelector('.v3-working-label')?.textContent).toBe('已完成');
    expect(container.querySelector('.v3-working-toggle')?.getAttribute('aria-expanded')).toBe('false');

    await act(async () => {
      Simulate.click(container.querySelector('.v3-working-toggle'));
      await Promise.resolve();
    });

    const toolStep = container.querySelector('.v3-wpi-tool-step');
    const narrative = toolStep?.querySelector('.v3-wpi-narrative');
    const tool = toolStep?.querySelector('.v3-wpi-tool');
    expect(narrative?.textContent).toBe('已完成验收。');
    expect(tool?.querySelector('.v3-wpi-tool-name')?.textContent).toBe('execute_shell');
    expect(narrative?.compareDocumentPosition(tool) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(container.querySelector('.v3-message-followup-text')?.textContent).not.toContain('已完成验收。');
    expect(container.querySelectorAll('.v3-message')).toHaveLength(1);
    expect(container.querySelectorAll('.v3-msg-time')).toHaveLength(1);
  });

  it('uses compact paragraphs while preserving intra-paragraph line breaks in group messages', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 21,
            from_uid: 2,
            content: '第一段\n\n第二段 @usr535\n第三段',
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={true}
          senderName="CatsCo"
          mentionDisplayNames={{ 535: '自迭代测试' }}
        />,
      );
      await Promise.resolve();
    });

    const paragraphs = container.querySelectorAll('.oc-plain-text-paragraph');
    expect(paragraphs).toHaveLength(2);
    expect(paragraphs[0].textContent).toBe('第一段');
    expect(paragraphs[1].textContent).toBe('第二段 @自迭代测试\n第三段');
    expect(container.querySelector('.oc-mention')?.dataset.mentionUid).toBe('535');
    expect(container.querySelector('.v3-message-deliverables')).toBeNull();
    expect(container.querySelector('.v3-message-followup-text')).toBeNull();
  });

  it('renders a structured bot mention with the bot display name while retaining its uid', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 23,
            from_uid: 2,
            content: '请让 @usr535 回顾这个任务',
            created_at: '2026-08-18T00:00:00Z',
          }}
          isSelf={false}
          isGroup={true}
          senderName="布鲁斯"
          mentionDisplayNames={{ 535: '自迭代测试' }}
        />,
      );
      await Promise.resolve();
    });

    const mention = container.querySelector('.oc-mention');
    expect(mention?.textContent).toBe('@自迭代测试');
    expect(mention?.dataset.mentionUid).toBe('535');
    expect(container.textContent).not.toContain('@usr535');
  });

  it('uses compact paragraph spacing for direct plain text messages', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 22,
            from_uid: 2,
            content: 'First paragraph.\n\nSecond paragraph.',
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    const paragraphs = container.querySelectorAll('.oc-plain-text-paragraph');
    expect(paragraphs).toHaveLength(2);
    expect(paragraphs[0].textContent).toBe('First paragraph.');
    expect(paragraphs[1].textContent).toBe('Second paragraph.');
    expect(container.querySelector('.oc-markdown')).toBeNull();
  });

  it('previews uploaded XLSX files as a spreadsheet artifact', async () => {
    readExcelFile.mockResolvedValue([
      {
        sheet: '名单',
        data: [
          ['姓名', '分数', '状态'],
          ['张三', 88, '正常'],
          ['李四', 54, '复核'],
        ],
      },
      {
        sheet: '统计',
        data: [
          ['指标', '值'],
          ['平均分', 71],
        ],
      },
    ]);
    global.fetch = vi.fn(() => Promise.resolve({
      ok: true,
      arrayBuffer: () => Promise.resolve(new ArrayBuffer(16)),
    }));

    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 31,
            from_uid: 2,
            content: '[文件] grade.xlsx',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'grade.xlsx',
                url: '/uploads/files/grade.xlsx',
                size: 4096,
                mime_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    expect(container.querySelector('.v3-attachment-size').textContent).toContain('Excel');

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync(12);
    });

    expect(global.fetch).toHaveBeenCalledWith(
      '/uploads/files/grade.xlsx',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(readExcelFile).toHaveBeenCalledWith(expect.any(ArrayBuffer));
    expect(container.querySelector('.v3-spreadsheet-preview')).not.toBeNull();
    expect(container.textContent).toContain('名单');
    expect(container.textContent).toContain('张三');
    expect(container.textContent).toContain('复核');

    await act(async () => {
      Simulate.click(Array.from(container.querySelectorAll('.v3-spreadsheet-tabs button')).find((button) => button.textContent === '统计'));
      await flushAsync();
    });

    expect(container.textContent).toContain('平均分');
  });

  it('previews CSV files with the same spreadsheet grid', async () => {
    global.fetch = vi.fn(() => Promise.resolve({
      ok: true,
      arrayBuffer: () => Promise.resolve(utf8ArrayBuffer('姓名,分数,状态\n张三,88,正常\n李四,54,复核\n')),
    }));

    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 32,
            from_uid: 2,
            content: '[文件] grade.csv',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'grade.csv',
                url: '/uploads/files/grade.csv',
                size: 128,
                mime_type: 'text/csv',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync(16);
    });

    expect(readExcelFile).not.toHaveBeenCalled();
    expect(container.querySelector('.v3-spreadsheet-preview')).not.toBeNull();
    expect(container.textContent).toContain('CSV');
    expect(container.textContent).toContain('张三');
    expect(container.textContent).toContain('复核');
  });

  it('previews CSV files by MIME type even when the filename has no CSV extension', async () => {
    global.fetch = vi.fn(() => Promise.resolve({
      ok: true,
      arrayBuffer: () => Promise.resolve(utf8ArrayBuffer('姓名,分数\n张三,88\n')),
    }));

    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 321,
            from_uid: 2,
            content: '[文件] grade-data',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'grade-data',
                url: '/uploads/files/grade-data',
                size: 64,
                mime_type: 'text/csv',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync(12);
    });

    expect(readExcelFile).not.toHaveBeenCalled();
    expect(container.querySelector('.v3-spreadsheet-preview')).not.toBeNull();
    expect(container.textContent).toContain('张三');
  });

  it('truncates CSV previews while retaining total row and column counts', async () => {
    const header = Array.from({ length: 60 }, (_, index) => `C${index + 1}`).join(',');
    const rows = Array.from({ length: 205 }, (_, rowIndex) => (
      Array.from({ length: 60 }, (_, columnIndex) => `R${rowIndex + 1}C${columnIndex + 1}`).join(',')
    ));
    global.fetch = vi.fn(() => Promise.resolve({
      ok: true,
      arrayBuffer: () => Promise.resolve(utf8ArrayBuffer([header, ...rows].join('\n'))),
    }));

    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 322,
            from_uid: 2,
            content: '[文件] wide.csv',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'wide.csv',
                url: '/uploads/files/wide.csv',
                size: 4096,
                mime_type: 'text/csv',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync(12);
    });

    expect(container.querySelector('.v3-spreadsheet-preview')).not.toBeNull();
    expect(container.textContent).toContain('206 行 · 60 列');
    expect(container.textContent).toContain('仅预览前 200 行、50 列');
    expect(container.textContent).toContain('R199C50');
    expect(container.textContent).not.toContain('R200C1');
    expect(container.textContent).not.toContain('C51');
  });

  it('keeps legacy XLS files download-only until browser parsing is supported', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 33,
            from_uid: 2,
            content: '[文件] legacy.xls',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'legacy.xls',
                url: '/uploads/files/legacy.xls',
                size: 2048,
                mime_type: 'application/vnd.ms-excel',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    expect(container.querySelector('.v3-attachment-size').textContent).toContain('Excel');
    expect(container.querySelector('.v3-artifact-action[disabled]')).not.toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync();
    });

    expect(window.open).toHaveBeenCalledWith('/uploads/files/legacy.xls', '_blank');
    expect(container.querySelector('.v3-file-preview-panel')).toBeNull();
  });

  it('blocks oversized spreadsheet previews before reading the full response body', async () => {
    const arrayBuffer = vi.fn(() => Promise.resolve(new ArrayBuffer(16)));
    global.fetch = vi.fn(() => Promise.resolve({
      ok: true,
      headers: {
        get: (name) => (name.toLowerCase() === 'content-length' ? String(13 * 1024 * 1024) : ''),
      },
      arrayBuffer,
    }));

    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 34,
            from_uid: 2,
            content: '[文件] large.xlsx',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'large.xlsx',
                url: '/uploads/files/large.xlsx',
                mime_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync(8);
    });

    expect(global.fetch).toHaveBeenCalledWith(
      '/uploads/files/large.xlsx',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
    expect(arrayBuffer).not.toHaveBeenCalled();
    expect(container.textContent).toContain('当前最多预览');
    expect(container.querySelector('.v3-spreadsheet-preview')).toBeNull();
  });

  it('blocks oversized spreadsheet previews after reading the response body when the header is missing', async () => {
    global.fetch = vi.fn(() => Promise.resolve({
      ok: true,
      arrayBuffer: () => Promise.resolve(new ArrayBuffer(13 * 1024 * 1024)),
    }));

    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 35,
            from_uid: 2,
            content: '[文件] large.xlsx',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'large.xlsx',
                url: '/uploads/files/large.xlsx',
                mime_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync(8);
    });

    expect(readExcelFile).not.toHaveBeenCalled();
    expect(container.textContent).toContain('当前最多预览');
    expect(container.querySelector('.v3-spreadsheet-preview')).toBeNull();
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

  it('renders pure markdown tables in assistant replies with the table styling hook', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 22,
            from_uid: 2,
            content: [
              '| 场景 | 谁来填 |',
              '| -- | -- |',
              '| 情况一 | HR/管理人员直接录入 |',
            ].join('\n'),
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-markdown-table')).not.toBeNull();
    expect(container.querySelector('.oc-markdown table')).not.toBeNull();
    expect(container.querySelectorAll('.oc-markdown th')).toHaveLength(2);
    expect(container.querySelector('.oc-markdown').textContent).toContain('情况一');
  });

  it('renders aligned plain text tables in assistant replies as bordered text table blocks', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 23,
            from_uid: 2,
            content: [
              '下面是本次数据的简版结论。',
              '',
              '#   文件夹            项目              页面',
              '1   bg-summary-test   后台子任务回流测试   index + about + notes',
              '2   bg-group-test     后台子任务组测试     index + about + notes',
              '',
              '表格之外的补充说明。',
            ].join('\n'),
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-plain-text-table')).not.toBeNull();
    expect(container.querySelector('.oc-markdown table')).toBeNull();
    expect(container.querySelector('.oc-plain-text-table').textContent).toContain('bg-summary-test');
  });

  it('renders loose model-generated text tables as bordered plain text table blocks', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 24,
            from_uid: 2,
            content: [
              '# 文件夹 项目 页面',
              '1bg-summary-test后台子任务回流测试index + about + notes',
              '2bg-group-test后台子任务组测试index + about + notes',
            ].join('\n'),
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-plain-text-table')).not.toBeNull();
    expect(container.querySelector('.oc-markdown table')).toBeNull();
    expect(container.querySelector('.oc-plain-text-table').textContent).toContain('bg-summary-test');
  });

  it('renders numbered single-space text tables as bordered plain text table blocks', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 25,
            from_uid: 2,
            content: [
              '序号 名称 数量 状态',
              '1 苹果 12 正常',
              '2 香蕉 0 缺货',
              '3 橙子 8 正常',
            ].join('\n'),
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-plain-text-table')).not.toBeNull();
    expect(container.querySelector('.oc-markdown table')).toBeNull();
    expect(container.querySelector('.oc-plain-text-table').textContent).toContain('香蕉');
  });

  it('renders message actions at the lower left and time at the lower right', async () => {
    const onReply = vi.fn();
    const onRegenerate = vi.fn(() => Promise.resolve());
    const onCreateConversationShare = vi.fn();
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
          onRegenerate={onRegenerate}
          onCreateConversationShare={onCreateConversationShare}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('[aria-label="Add Reaction"]')).toBeNull();
    const footer = container.querySelector('.v3-message-footer');
    expect(footer).not.toBeNull();
    expect(footer.previousElementSibling?.classList.contains('v3-message-bubble')).toBe(true);
    expect(container.querySelector('.v3-message-bubble')?.contains(footer)).toBe(false);
    expect(Array.from(footer.children).map((node) => node.className)).toEqual([
      'v3-message-actions',
      'v3-msg-time',
    ]);
    expect(container.querySelector('.v3-msg-header .v3-msg-time')).toBeNull();
    expect(footer.querySelector('time.v3-msg-time')?.getAttribute('datetime')).toBe('2026-06-09T00:00:00Z');

    const actionButtons = Array.from(footer.querySelectorAll(':scope > .v3-message-actions .v3-action-btn'));
    expect(actionButtons.map((button) => button.getAttribute('aria-label'))).toEqual([
      '复制',
      '重新生成',
      '回复',
      '更多操作',
    ]);

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="复制"]'));
      await Promise.resolve();
    });
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith('这是一条可以复制的消息');
    expect(container.querySelector('[aria-label="已复制"]')).not.toBeNull();
    expect(container.querySelector('[aria-label="点赞"]')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="重新生成"]'));
      await Promise.resolve();
    });
    expect(onRegenerate).toHaveBeenCalledTimes(1);
    expect(onRegenerate).toHaveBeenCalledWith(expect.objectContaining({ id: 20 }));

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="回复"]'));
      await Promise.resolve();
    });
    expect(onReply).toHaveBeenCalledTimes(1);

    const moreActionsButton = container.querySelector('[aria-label="更多操作"]');
    await act(async () => {
      Simulate.click(moreActionsButton);
      await Promise.resolve();
    });
    const moreActionsMenu = container.querySelector('.v3-message-action-menu');
    expect(moreActionsButton.getAttribute('aria-expanded')).toBe('true');
    expect(moreActionsMenu?.getAttribute('role')).toBe('menu');
    expect(moreActionsMenu?.textContent).toContain('制作分享图');
    expect(moreActionsButton.parentElement?.classList.contains('v3-message-more-actions')).toBe(true);
    expect(moreActionsMenu?.parentElement).toBe(moreActionsButton.parentElement);

    await act(async () => {
      Simulate.click(moreActionsMenu.querySelector('[role="menuitem"]'));
      await Promise.resolve();
    });
    expect(onCreateConversationShare).toHaveBeenCalledTimes(1);
    expect(container.querySelector('.v3-message-action-menu')).toBeNull();
  });

  it('opens message actions after a mobile tap and keeps buttons clickable', async () => {
    const onReply = vi.fn();
    await act(async () => {
      root.render(
        <ChatMessage
          message={{ id: 21, from_uid: 2, content: '点击查看操作', created_at: '2026-06-09T00:00:00Z' }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          onReply={onReply}
          onCreateConversationShare={vi.fn()}
        />,
      );
      await Promise.resolve();
    });

    const bubble = container.querySelector('.v3-message-bubble');
    const actions = container.querySelector('.v3-message-actions');
    expect(actions?.classList.contains('open')).toBe(false);

    window.matchMedia = vi.fn().mockReturnValue({ matches: true, addListener: vi.fn(), removeListener: vi.fn() });
    await act(async () => {
      Simulate.click(bubble);
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-message-actions')?.classList.contains('open')).toBe(true);

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="更多操作"]'));
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-message-action-menu')).not.toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('[aria-label="回复"]'));
      await Promise.resolve();
    });
    expect(onReply).toHaveBeenCalledTimes(1);
    expect(container.querySelector('.v3-message-actions')?.classList.contains('open')).toBe(true);
    expect(container.querySelector('.v3-message-action-menu')).toBeNull();
  });

  it('shows a direct edit action for the current user message', async () => {
    const onEdit = vi.fn();
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 26,
            from_uid: 1,
            content: 'Edit this instruction',
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf
          isGroup={false}
          senderName="Me"
          questionAnchorKey="question-26"
          onEdit={onEdit}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('[aria-label="更多操作"]')).toBeNull();
    const editButton = container.querySelector('[aria-label="修改后重新发送（原消息保留）"]');
    expect(editButton).not.toBeNull();
    expect(container.querySelector('[data-conversation-question="question-26"]')).not.toBeNull();
    await act(async () => {
      Simulate.click(editButton);
      await Promise.resolve();
    });
    expect(onEdit).toHaveBeenCalledWith(expect.objectContaining({ id: 26 }));
  });

  it('writes an internal attachment token when a chat image is dragged', async () => {
    const setData = vi.fn();
    const dataTransfer = { setData, effectAllowed: 'none' };
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 27,
            from_uid: 1,
            content: '拖动图片',
            content_blocks: [
              { type: 'text', text: '拖动图片' },
              { type: 'image', payload: { file_key: 'cat.png', url: '/uploads/images/cat.png', name: 'cat.png' } },
            ],
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf
          isGroup={false}
          senderName="Me"
        />,
      );
      await Promise.resolve();
    });

    const image = container.querySelector('img.oc-rich-image-thumb');
    expect(image).not.toBeNull();
    expect(image.draggable).toBe(true);
    await act(async () => {
      Simulate.dragStart(image, { dataTransfer });
    });

    expect(setData).toHaveBeenCalledWith(
      'application/x-catsco-chat-attachment',
      expect.stringMatching(/^(?:[0-9a-f-]{36}|[0-9a-f]{48})$/i),
    );
    expect(dataTransfer.effectAllowed).toBe('copy');
  });

  it('opens a keyboard-accessible image dialog outside the message layout', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 2701,
            from_uid: 1,
            content: 'Preview image',
            content_blocks: [
              {
                type: 'image',
                payload: {
                  file_key: 'poster.png',
                  url: '/uploads/images/poster.png',
                  name: 'poster.png',
                },
              },
            ],
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf
          isGroup={false}
          senderName="Me"
        />,
      );
      await Promise.resolve();
    });

    const trigger = container.querySelector('button.oc-rich-image-trigger');
    expect(trigger.getAttribute('aria-label')).toBe('预览图片 poster.png');
    trigger.focus();
    await act(async () => {
      Simulate.keyDown(trigger, { key: 'Enter' });
      await Promise.resolve();
    });

    const preview = document.body.querySelector('.oc-rich-image-preview');
    const previewImage = preview?.querySelector('.oc-rich-image-preview-media');
    const closeButton = preview?.querySelector('button.oc-rich-image-preview-close');
    const download = preview?.querySelector('a.oc-rich-media-preview-download');
    expect(preview).not.toBeNull();
    expect(container.contains(preview)).toBe(false);
    expect(preview.getAttribute('role')).toBe('dialog');
    expect(preview.getAttribute('aria-modal')).toBe('true');
    expect(preview.getAttribute('aria-label')).toBe('图片预览 poster.png');
    expect(previewImage?.getAttribute('src')).toBe('/uploads/images/poster.png');
    expect(previewImage?.getAttribute('alt')).toBe('poster.png preview');
    expect(closeButton?.getAttribute('aria-label')).toBe('关闭图片预览');
    expect(download?.getAttribute('aria-label')).toBe('下载图片 poster.png');
    expect(download?.getAttribute('href')).toBe('/uploads/images/poster.png?download=1');
    expect(download?.getAttribute('download')).toBe('poster.png');
    expect(document.activeElement).toBe(closeButton);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
      await Promise.resolve();
    });
    expect(document.activeElement).toBe(closeButton);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await Promise.resolve();
    });
    expect(document.body.querySelector('.oc-rich-image-preview')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    await act(async () => {
      Simulate.click(trigger);
      await Promise.resolve();
    });
    const reopenedPreview = document.body.querySelector('.oc-rich-image-preview');
    const reopenedCloseButton = reopenedPreview.querySelector('button.oc-rich-image-preview-close');
    await act(async () => {
      Simulate.click(reopenedCloseButton);
      await Promise.resolve();
    });
    expect(document.body.querySelector('.oc-rich-image-preview')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('opens a gallery image when its render index differs from the gallery index', async () => {
    const onOpenImage = vi.fn();
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 2702,
            from_uid: 1,
            content: 'Preview image',
            content_blocks: [{
              type: 'image',
              payload: {
                file_key: 'fallback.png',
                url: '/uploads/images/fallback.png',
                name: 'fallback.png',
              },
            }],
          }}
          imageGallery={[{
            id: 'stable-gallery-id',
            payload: { url: '/uploads/images/fallback.png', name: 'fallback.png' },
          }]}
          onOpenImage={onOpenImage}
          isSelf
          isGroup={false}
          senderName="Me"
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('button.oc-rich-image-trigger'));
      await Promise.resolve();
    });

    expect(onOpenImage).toHaveBeenCalledWith(
      'stable-gallery-id',
      expect.anything(),
      expect.objectContaining({ url: '/uploads/images/fallback.png' }),
    );
  });

  it('does not match an earlier gallery image when both thumbnails are absent', async () => {
    const onOpenImage = vi.fn();
    const selectedPayload = {
      file_key: 'selected.png',
      url: '/uploads/images/selected.png',
      name: 'selected.png',
    };
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 2703,
            from_uid: 1,
            content: 'Preview selected image',
            content_blocks: [{ type: 'image', payload: selectedPayload }],
          }}
          imageGallery={[
            {
              id: 'earlier-image-id',
              payload: {
                file_key: 'earlier.png',
                url: '/uploads/images/earlier.png',
                name: 'earlier.png',
              },
            },
            { id: 'selected-image-id', payload: selectedPayload },
          ]}
          imageId="selected-image-id"
          onOpenImage={onOpenImage}
          isSelf
          isGroup={false}
          senderName="Me"
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('button.oc-rich-image-trigger'));
      await Promise.resolve();
    });

    expect(onOpenImage).toHaveBeenCalledWith(
      'selected-image-id',
      expect.anything(),
      selectedPayload,
    );
  });

  it('writes an internal attachment token when a system file is dragged', async () => {
    const setData = vi.fn();
    const dataTransfer = { setData, effectAllowed: 'none' };
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 28,
            from_uid: 1,
            content: '[文件] report.pdf',
            content_blocks: [{ type: 'file', payload: { file_key: 'report.pdf', url: '/uploads/files/report.pdf', name: 'report.pdf' } }],
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf
          isGroup={false}
          senderName="Me"
        />,
      );
      await Promise.resolve();
    });

    const card = container.querySelector('.v3-attachment-card');
    expect(card.draggable).toBe(true);
    await act(async () => {
      Simulate.dragStart(card, { dataTransfer });
    });
    expect(setData).toHaveBeenCalledWith('application/x-catsco-chat-attachment', expect.any(String));
  });

  it('does not expose URL-only images as reusable chat attachments', async () => {
    const setData = vi.fn();
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 29,
            from_uid: 2,
            content: '[图片] remote.png',
            content_blocks: [{ type: 'image', payload: { file_key: 'forged-key', url: 'https://example.com/remote.png', name: 'remote.png' } }],
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="Other"
        />,
      );
      await Promise.resolve();
    });

    const image = container.querySelector('img.oc-rich-image-thumb');
    expect(image.draggable).toBe(false);
    await act(async () => {
      Simulate.dragStart(image, { dataTransfer: { setData, effectAllowed: 'none' } });
    });
    expect(setData).not.toHaveBeenCalled();
  });

  it('does not create a hidden avatar column for current-user messages', async () => {
    await act(async () => {
      root.render(
        <>
          <ChatMessage
            message={{
              id: 2601,
              from_uid: 1,
              content: 'Current-user message',
              created_at: '2026-06-09T00:00:00Z',
            }}
            isSelf
            isGroup={false}
            senderName="Me"
          />
          <ChatMessage
            message={{
              id: 2602,
              from_uid: 2,
              content: 'Peer message',
              created_at: '2026-06-09T00:01:00Z',
            }}
            isSelf={false}
            isGroup={false}
            senderName="CatsCo"
          />
        </>,
      );
      await Promise.resolve();
    });

    const selfMessage = container.querySelector('.v3-message.is-self');
    const peerMessage = container.querySelector('.v3-message.is-peer');
    expect(selfMessage.querySelector('.v3-avatar-col')).toBeNull();
    expect(selfMessage.querySelector('[data-testid="avatar"]')).toBeNull();
    expect(peerMessage.querySelector('.v3-avatar-col')).not.toBeNull();
    expect(peerMessage.querySelector('[data-testid="avatar"]')).not.toBeNull();
  });

  it('keeps the larger current-user bubble shrink-wrapped with balanced padding', () => {
    expect(catscoUiSystemCss).toContain('.v3-message.is-self .v3-message-bubble');
    const messageRule = catscoUiSystemCss.match(
      /\.v3-message\.is-self\s*\{[^}]*\}/,
    )?.[0];
    const bubbleRule = catscoUiSystemCss.match(
      /\.v3-message\.is-self \.v3-message-bubble\s*\{[^}]*\}/,
    )?.[0];

    expect(messageRule).toContain('max-width: min(74%, 700px);');
    expect(bubbleRule).toContain('width: fit-content;');
    expect(bubbleRule).toContain('max-width: 100%;');
    expect(bubbleRule).toContain('padding-block: 10px;');
    expect(bubbleRule).toContain('padding-inline: 14px;');
  });

  it('uses the larger shared body text for both user and Agent messages', async () => {
    await act(async () => {
      root.render(
        <>
          <ChatMessage
            message={{ id: 27, from_uid: 1, content: 'User message', created_at: '2026-06-09T00:00:00Z' }}
            isSelf
            isGroup={false}
            senderName="Me"
          />
          <ChatMessage
            message={{ id: 28, from_uid: 2, content: 'Agent message', created_at: '2026-06-09T00:01:00Z' }}
            isSelf={false}
            isGroup={false}
            senderName="CatsCo"
            senderIsBot
          />
        </>,
      );
      await Promise.resolve();
    });

    const messageContents = container.querySelectorAll('.v3-message-content');
    expect(messageContents).toHaveLength(2);
    expect(messageContents[0].textContent).toContain('User message');
    expect(messageContents[1].textContent).toContain('Agent message');

    const contentRule = catscoUiSystemCss.match(
      /\.v3-message \.v3-message-content\s*\{[^}]*\}/,
    )?.[0];
    expect(contentRule).toContain('font-size: 15px;');
    expect(contentRule).toContain('line-height: 1.62;');
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
          workingOnly
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-wpi-plan')).not.toBeNull();
    expect(container.querySelector('.v3-wpi-plan-title').textContent).toBe('计划');
    expect(container.querySelector('.v3-wpi-plan').textContent).toContain('创建临时工作目录');
    expect(container.querySelector('.v3-wpi-plan').textContent).toContain('设计 analyzeReply 函数');
    expect(container.querySelector('.v3-working-status')).not.toBeNull();
    expect(container.querySelector('.v3-working-toggle')).toBeNull();
    expect(container.querySelector('.v3-working-steps')).toBeNull();
    expect(container.querySelector('.v3-wpi-tool-name')).toBeNull();
    expect(container.querySelector('.v3-message-footer')).toBeNull();
  });

  it('replaces earlier plan snapshots in place and marks the completed working process once', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 22,
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
                    { status: 'in_progress', step: '实现功能' },
                    { status: 'pending', step: '运行测试' },
                  ],
                },
              },
            },
            {
              type: 'tool_result',
              content: '计划已更新：0/2 已完成',
              metadata: { tool_use_id: 'plan-1' },
            },
            {
              type: 'tool_use',
              content: 'execute_shell',
              metadata: {
                id: 'shell-1',
                input: { command: 'npm test' },
              },
            },
            {
              type: 'tool_result',
              content: 'Tests passed',
              metadata: { tool_use_id: 'shell-1' },
            },
            {
              type: 'tool_use',
              content: 'update_plan',
              metadata: {
                id: 'plan-2',
                input: {
                  steps: [
                    { status: 'completed', step: '实现功能' },
                    { status: 'completed', step: '运行测试' },
                  ],
                },
              },
            },
            {
              type: 'tool_result',
              content: '计划已更新：2/2 已完成',
              metadata: { tool_use_id: 'plan-2' },
            },
          ]}
          workingOnly
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          senderIsBot
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-working-label')?.textContent).toBe('已完成');
    expect(container.querySelector('.v3-working-summary')?.textContent).toBe('2/2');
    expect(container.querySelector('.v3-message')?.classList.contains('is-working')).toBe(false);
    expect(container.querySelector('.v3-message')?.classList.contains('is-complete')).toBe(true);
    expect(container.querySelectorAll('.v3-wpi-plan')).toHaveLength(1);
    expect(container.querySelector('.v3-wpi-plan-count')?.textContent).toBe('2/2');
    expect(container.querySelectorAll('.v3-wpi-plan-step.completed')).toHaveLength(2);
    expect(container.querySelector('.v3-working-steps')).toBeNull();
    expect(container.querySelector('.v3-working-plan')?.classList.contains('is-after-details')).toBe(false);
    expect(container.querySelector('.v3-working-hint')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('.v3-working-toggle'));
      await Promise.resolve();
    });

    expect(container.querySelectorAll('.v3-wpi-plan')).toHaveLength(1);
    const persistentPlan = container.querySelector('.v3-working-plan');
    const inlineDetails = container.querySelector('.v3-working-details-inline');
    expect(inlineDetails).not.toBeNull();
    expect(inlineDetails?.parentElement).toBe(container.querySelector('.v3-working-process'));
    expect(inlineDetails?.compareDocumentPosition(persistentPlan) & Node.DOCUMENT_POSITION_FOLLOWING)
      .toBeTruthy();
    expect(persistentPlan?.classList.contains('is-after-details')).toBe(true);
    expect(inlineDetails?.querySelectorAll('.v3-wpi-tool-name')).toHaveLength(1);
    expect(inlineDetails?.querySelector('.v3-wpi-tool-name')?.textContent).toBe('execute_shell');
  });

  it('summarizes working steps and mounts large tool results only on demand', async () => {
    const longResult = 'result line\n'.repeat(1200);
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 29,
            from_uid: 2,
            content: '',
            created_at: '2026-06-09T00:00:00Z',
          }}
          workingMessages={[
            {
              type: 'tool_use',
              content: 'execute_shell',
              metadata: {
                id: 'shell-1',
                input: { command: 'npm test' },
              },
            },
            {
              type: 'tool_result',
              content: longResult,
              metadata: { tool_use_id: 'shell-1' },
            },
          ]}
          workingOnly
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          senderIsBot
        />,
      );
      await Promise.resolve();
    });

    const message = container.querySelector('.v3-message');
    const processToggle = container.querySelector('.v3-working-toggle');
    expect(message.classList.contains('is-working')).toBe(true);
    expect(container.querySelector('.v3-working-label')?.textContent).toBe('正在执行');
    expect(processToggle.getAttribute('aria-expanded')).toBe('false');
    expect(processToggle.getAttribute('aria-label')).toContain('展开任务步骤');
    expect(processToggle.getAttribute('aria-controls')).toMatch(/^working-steps-/);
    expect(container.querySelector('.v3-working-summary')?.textContent).toContain('execute_shell');
    expect(container.querySelector('.v3-wpi-tool-result')).toBeNull();

    await act(async () => {
      Simulate.click(processToggle);
      await Promise.resolve();
    });

    const inlineDetails = container.querySelector('.v3-working-details-inline');
    const resultToggle = inlineDetails?.querySelector('.v3-wpi-tool-header.is-toggle');
    const workingSteps = inlineDetails?.querySelector('.v3-working-steps');
    expect(resultToggle).not.toBeNull();
    expect(workingSteps).not.toBeNull();
    expect(inlineDetails?.parentElement).toBe(container.querySelector('.v3-working-process'));
    expect(inlineDetails?.id).toBe(processToggle.getAttribute('aria-controls'));
    expect(resultToggle.getAttribute('aria-expanded')).toBe('false');
    expect(inlineDetails?.querySelector('.v3-wpi-tool-result')).toBeNull();

    await act(async () => {
      Simulate.click(resultToggle);
      await Promise.resolve();
    });

    expect(resultToggle.getAttribute('aria-expanded')).toBe('true');
    expect(inlineDetails?.querySelector('.v3-wpi-tool-result')?.textContent)
      .toContain('result line');

    await act(async () => {
      inlineDetails
        .querySelector('.v3-wpi-code-block.result pre')
        .dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY: 120 }));
      await Promise.resolve();
    });

    expect(resultToggle.getAttribute('aria-expanded')).toBe('true');
    expect(inlineDetails?.querySelector('.v3-wpi-tool-result')).not.toBeNull();

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-working-details-inline')).toBeNull();
    expect(document.activeElement).toBe(processToggle);
  });

  it('uses an explicit completed turn signal and strips the process protocol prefix', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 30,
            from_uid: 2,
            content: '',
            created_at: '2026-06-09T00:00:00Z',
          }}
          workingMessages={[
            {
              type: 'text',
              content: 'AI文本:Checking the implementation.',
              _display_text_role: 'process',
            },
            {
              type: 'tool_use',
              content: 'execute_shell',
              metadata: { id: 'shell-1', input: { command: 'npm test' } },
            },
            {
              type: 'tool_result',
              content: 'Tests passed',
              metadata: { tool_use_id: 'shell-1' },
            },
          ]}
          workingOnly
          workingComplete
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          senderIsBot
        />,
      );
      await Promise.resolve();
    });

    const message = container.querySelector('.v3-message');
    expect(message.classList.contains('is-working')).toBe(false);
    expect(message.classList.contains('is-complete')).toBe(true);
    expect(container.querySelector('.v3-working-label')?.textContent).toBe('已完成');

    await act(async () => {
      Simulate.click(container.querySelector('.v3-working-toggle'));
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-wpi-narrative')?.textContent)
      .toBe('Checking the implementation.');
    expect(container.textContent).not.toContain('AI文本:');
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

  it('constructs the first managed Artifact preview from its immutable version URL', () => {
    const preview = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      title: '课堂小游戏',
      url: 'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/latest/',
      publish_version: 2,
      agent_uid: 440,
    });
    expect(preview.url).toBe(
      'https://agent-440.artifacts.catsco.fun:19991/artifacts/lesson-game/v2/',
    );
  });

  it('renders a registry-matched Artifact URL as a card and previews the remote page in the side panel', async () => {
    const artifact = {
      id: 'lesson-game',
      title: '课堂小游戏',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      publish_version: 2,
    };
    const onOpenRemoteArtifactFullscreen = vi.fn();
    const previewURL = 'https://artifacts.example.test/by-agent/440/lesson-game/v2/';
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 30,
            from_uid: 440,
            content: `[artifact-link-test](${artifact.url})`,
            created_at: '2026-07-27T00:00:00Z',
          }}
          knownArtifacts={[artifact]}
          onOpenRemoteArtifactFullscreen={onOpenRemoteArtifactFullscreen}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-message-artifact-list')).not.toBeNull();
    expect(container.querySelector('.v3-attachment-name').textContent).toBe('课堂小游戏');
    expect(container.querySelector('.v3-attachment-size').textContent).toContain('v2');
    expect(container.querySelector('.oc-artifact-source-link')).not.toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
    });

    expect(global.fetch).not.toHaveBeenCalled();
    const panel = container.querySelector('.v3-file-preview-panel');
    expect(panel).not.toBeNull();
    const frame = panel.querySelector('iframe.v3-file-preview-frame');
    expect(frame.getAttribute('src')).toBe(previewURL);
    expect(frame.getAttribute('sandbox')).toContain('allow-same-origin');
    expect(frame.hasAttribute('credentialless')).toBe(true);
    const fullscreenButton = panel.querySelector('button[aria-label="在新标签页打开"]');
    expect(fullscreenButton).not.toBeNull();
    expect(container.querySelector('.v3-artifact-action[href]')).toBeNull();
    expect(panel.querySelector('.v3-remote-artifact-preview-state').textContent).toContain('正在加载');

    await act(async () => {
      Simulate.load(frame);
      await Promise.resolve();
    });
    expect(panel.querySelector('.v3-remote-artifact-preview-state')).toBeNull();

    await act(async () => {
      Simulate.error(frame);
      await Promise.resolve();
    });
    expect(panel.querySelector('.v3-remote-artifact-preview-state.error').textContent).toContain('预览加载失败');
    await act(async () => Simulate.click(
      panel.querySelector('.v3-remote-artifact-preview-state.error button'),
    ));
    expect(onOpenRemoteArtifactFullscreen).toHaveBeenCalledWith(expect.objectContaining({
      artifact_id: 'lesson-game',
      publish_version: 2,
      url: previewURL,
    }));
  });

  it('keeps the current Artifact visible until the hidden refresh frame answers through the page bridge', async () => {
    const current = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      agent_uid: 440,
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      publish_version: 2,
    });
    const pending = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      agent_uid: 440,
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/?artifact_version=3',
      publish_version: 3,
    });
    const onReady = vi.fn();
    const onFailed = vi.fn();
    function RefreshHarness() {
      const [activeFile, setActiveFile] = React.useState(current);
      const [pendingFile, setPendingFile] = React.useState(pending);
      return (
        <FilePreviewPanel
          file={activeFile}
          pendingRemoteArtifactFile={pendingFile}
          onRemoteArtifactRefreshReady={(candidate) => {
            onReady(candidate);
            setActiveFile(candidate);
            setPendingFile(null);
          }}
          onRemoteArtifactRefreshFailed={onFailed}
          onClose={vi.fn()}
        />
      );
    }

    await act(async () => {
      root.render(<RefreshHarness />);
      await flushAsync();
    });

    const visibleFrame = container.querySelector('iframe[title="Cloud Artifact Preview"]');
    const refreshFrame = container.querySelector('iframe[title="Cloud Artifact Refresh Check"]');
    expect(visibleFrame?.getAttribute('src')).toBe(current.url);
    expect(refreshFrame?.getAttribute('src')).toBe(pending.url);

    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe('https://artifacts.example.test');
        const event = new Event('message');
        Object.defineProperties(event, {
          source: { value: frameWindow },
          origin: { value: 'https://artifacts.example.test' },
          data: {
            value: {
              type: 'catsco.artifact.context.response.v1',
              request_id: message.request_id,
              context: {
                contract_version: 'catsco.artifact-page-context.v1',
                observed_at: '2026-08-07T12:00:00Z',
                title: '课堂小游戏',
                location: { pathname: '/by-agent/440/lesson-game/latest/' },
              },
            },
          },
        });
        window.dispatchEvent(event);
      },
    };
    Object.defineProperty(refreshFrame, 'contentWindow', {
      configurable: true,
      value: frameWindow,
    });

    await act(async () => {
      Simulate.load(refreshFrame);
      await flushAsync();
    });

    expect(onReady).toHaveBeenCalledWith(pending);
    expect(onFailed).not.toHaveBeenCalled();
    const promotedFrame = container.querySelector('iframe[title="Cloud Artifact Preview"]');
    expect(promotedFrame).toBe(refreshFrame);
    expect(promotedFrame.getAttribute('src')).toBe(pending.url);
    expect(visibleFrame.isConnected).toBe(false);
    expect(container.querySelector('iframe[title="Cloud Artifact Refresh Check"]')).toBeNull();
  });

  it('replays a refresh-frame load that occurs before the passive attempt effect', async () => {
    const current = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      agent_uid: 440,
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      publish_version: 2,
    });
    const pending = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      agent_uid: 440,
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/?artifact_version=3',
      publish_version: 3,
    });
    const onReady = vi.fn();
    const onFailed = vi.fn();

    function EarlyLoadTrigger({ enabled }) {
      React.useLayoutEffect(() => {
        if (!enabled) return;
        const refreshFrame = container.querySelector('iframe[title="Cloud Artifact Refresh Check"]');
        const frameWindow = {
          postMessage(message, targetOrigin) {
            expect(targetOrigin).toBe('https://artifacts.example.test');
            const event = new Event('message');
            Object.defineProperties(event, {
              source: { value: frameWindow },
              origin: { value: 'https://artifacts.example.test' },
              data: {
                value: {
                  type: 'catsco.artifact.context.response.v1',
                  request_id: message.request_id,
                  context: {
                    contract_version: 'catsco.artifact-page-context.v1',
                    observed_at: '2026-08-10T12:00:00Z',
                    title: '课堂小游戏',
                  },
                },
              },
            });
            window.dispatchEvent(event);
          },
        };
        Object.defineProperty(refreshFrame, 'contentWindow', {
          configurable: true,
          value: frameWindow,
        });
        Simulate.load(refreshFrame);
      }, [enabled]);
      return null;
    }

    function EarlyLoadHarness() {
      const [pendingFile, setPendingFile] = React.useState(null);
      React.useEffect(() => {
        setPendingFile(pending);
      }, []);
      return (
        <>
          <FilePreviewPanel
            file={current}
            pendingRemoteArtifactFile={pendingFile}
            onRemoteArtifactRefreshReady={onReady}
            onRemoteArtifactRefreshFailed={onFailed}
            onClose={vi.fn()}
          />
          <EarlyLoadTrigger enabled={Boolean(pendingFile)} />
        </>
      );
    }

    await act(async () => {
      root.render(<EarlyLoadHarness />);
      await flushAsync();
    });

    expect(onReady).toHaveBeenCalledWith(pending);
    expect(onFailed).not.toHaveBeenCalled();
  });

  it('does not let an old same-key refresh attempt settle its replacement', async () => {
    const current = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      agent_uid: 440,
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
      publish_version: 2,
    });
    const pendingA = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      agent_uid: 440,
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/?artifact_version=3',
      publish_version: 3,
    });
    const pendingB = createCloudArtifactPreviewFile({
      id: 'lesson-game',
      agent_uid: 440,
      title: '课堂小游戏',
      url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/?artifact_version=3',
      publish_version: 3,
    });
    const onReady = vi.fn();
    const onFailed = vi.fn();
    const responders = [];
    const frameWindow = {
      postMessage(message, targetOrigin) {
        expect(targetOrigin).toBe('https://artifacts.example.test');
        responders.push(() => {
          const event = new Event('message');
          Object.defineProperties(event, {
            source: { value: frameWindow },
            origin: { value: 'https://artifacts.example.test' },
            data: {
              value: {
                type: 'catsco.artifact.context.response.v1',
                request_id: message.request_id,
                context: {
                  contract_version: 'catsco.artifact-page-context.v1',
                  observed_at: '2026-08-10T12:00:00Z',
                  title: '课堂小游戏',
                },
              },
            },
          });
          window.dispatchEvent(event);
        });
      },
    };
    let replacePending = null;
    function RefreshHarness() {
      const [pendingFile, setPendingFile] = React.useState(pendingA);
      replacePending = () => setPendingFile(pendingB);
      return (
        <FilePreviewPanel
          file={current}
          pendingRemoteArtifactFile={pendingFile}
          onRemoteArtifactRefreshReady={onReady}
          onRemoteArtifactRefreshFailed={onFailed}
          onClose={vi.fn()}
        />
      );
    }

    await act(async () => {
      root.render(<RefreshHarness />);
      await flushAsync();
    });
    let refreshFrame = container.querySelector('iframe[title="Cloud Artifact Refresh Check"]');
    Object.defineProperty(refreshFrame, 'contentWindow', {
      configurable: true,
      value: frameWindow,
    });
    await act(async () => {
      Simulate.load(refreshFrame);
      await flushAsync();
    });
    expect(responders).toHaveLength(1);

    await act(async () => {
      replacePending();
      await flushAsync();
    });
    refreshFrame = container.querySelector('iframe[title="Cloud Artifact Refresh Check"]');
    await act(async () => {
      Simulate.load(refreshFrame);
      await flushAsync();
    });
    expect(responders).toHaveLength(2);

    await act(async () => {
      responders[0]();
      await flushAsync();
    });
    expect(onReady).not.toHaveBeenCalled();
    expect(onFailed).not.toHaveBeenCalled();

    await act(async () => {
      responders[1]();
      await flushAsync();
    });
    expect(onReady).toHaveBeenCalledTimes(1);
    expect(onReady.mock.calls[0][0]).toBe(pendingB);
    expect(onFailed).not.toHaveBeenCalled();
  });

  it('rejects an onLoad-only refresh frame when the Artifact page bridge does not answer', async () => {
    vi.useFakeTimers();
    try {
      const current = createCloudArtifactPreviewFile({
        id: 'lesson-game',
        agent_uid: 440,
        title: '课堂小游戏',
        url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/',
        publish_version: 2,
      });
      const pending = createCloudArtifactPreviewFile({
        id: 'lesson-game',
        agent_uid: 440,
        title: '课堂小游戏',
        url: 'https://artifacts.example.test/by-agent/440/lesson-game/latest/?artifact_version=3',
        publish_version: 3,
      });
      const onReady = vi.fn();
      const onFailed = vi.fn();

      await act(async () => {
        root.render(
          <FilePreviewPanel
            file={current}
            pendingRemoteArtifactFile={pending}
            onRemoteArtifactRefreshReady={onReady}
            onRemoteArtifactRefreshFailed={onFailed}
            onClose={vi.fn()}
          />,
        );
        await flushAsync();
      });

      const visibleFrame = container.querySelector('iframe[title="Cloud Artifact Preview"]');
      const refreshFrame = container.querySelector('iframe[title="Cloud Artifact Refresh Check"]');
      Object.defineProperty(refreshFrame, 'contentWindow', {
        configurable: true,
        value: { postMessage: vi.fn() },
      });

      await act(async () => {
        Simulate.load(refreshFrame);
        await vi.advanceTimersByTimeAsync(1200);
        await flushAsync();
      });

      expect(onReady).not.toHaveBeenCalled();
      expect(onFailed).toHaveBeenCalledWith(pending);
      expect(visibleFrame.getAttribute('src')).toBe(current.url);
    } finally {
      vi.useRealTimers();
    }
  });

  it('reuses the side preview external-open action and returns to the managed Artifact list', async () => {
    const artifact = {
      id: 'managed-game',
      title: 'Managed Game',
      url: 'https://artifacts.example.test/by-agent/440/managed-game/latest/',
      publish_version: 3,
    };
    const onBack = vi.fn();
    const onClose = vi.fn();
    const onOpenRemoteArtifactFullscreen = vi.fn();

    await act(async () => {
      root.render(
        <FilePreviewPanel
          file={createCloudArtifactPreviewFile(artifact)}
          onBack={onBack}
          onClose={onClose}
          onOpenRemoteArtifactFullscreen={onOpenRemoteArtifactFullscreen}
        />,
      );
      await Promise.resolve();
    });

    const panel = container.querySelector('.v3-file-preview-panel');
    const externalButton = panel.querySelector('button[aria-label="在新标签页打开"]');
    expect(externalButton).not.toBeNull();
    expect(panel.querySelector('a[download]')).toBeNull();

    await act(async () => Simulate.click(externalButton));
    expect(onOpenRemoteArtifactFullscreen).toHaveBeenCalledWith(expect.objectContaining({
      artifact_id: 'managed-game',
      publish_version: 3,
      url: 'https://artifacts.example.test/by-agent/440/managed-game/v3/',
    }));

    await act(async () => {
      Simulate.click(panel.querySelector('button[aria-label="返回云文件"]'));
    });
    expect(onBack).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();
  });

  it('previews a same-origin registry Artifact in the side panel with an opaque sandbox', async () => {
    const artifactURL = new URL('/artifacts/by-agent/440/same-origin/latest/', window.location.origin).toString();
    const previewURL = 'http://localhost:3000/artifacts/by-agent/440/same-origin/v1/';
    const artifact = {
      id: 'same-origin',
      title: 'Same-origin artifact',
      kind: 'html',
      url: artifactURL,
      publish_version: 1,
    };
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 37,
            from_uid: 440,
            content: `Published: ${artifactURL}`,
            created_at: '2026-07-27T00:00:00Z',
          }}
          knownArtifacts={[artifact]}
        />,
      );
      await Promise.resolve();
    });

    const previewButton = container.querySelector('.v3-artifact-actions button');
    expect(previewButton.disabled).toBe(false);

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
    });

    expect(window.open).not.toHaveBeenCalled();
    const panel = container.querySelector('.v3-file-preview-panel');
    expect(panel).not.toBeNull();
    const frame = panel.querySelector('iframe.v3-file-preview-frame');
    const frameURL = new URL(frame?.getAttribute('src'));
    expect(`${frameURL.origin}${frameURL.pathname}${frameURL.search}`).toBe(previewURL);
    expect(frameURL.hash).toContain('catsco_bridge_nonce=');
    expect(frame?.getAttribute('srcdoc')).toBeNull();
    expect(frame?.getAttribute('sandbox')).toBe('allow-scripts allow-forms allow-popups allow-modals');
    expect(frame?.hasAttribute('credentialless')).toBe(true);
  });

  it('invalidates the opaque bridge when the preview iframe loads a second document', async () => {
    const artifactURL = new URL('/artifacts/by-agent/440/reloadable/latest/', window.location.origin).toString();
    const onBindingChange = vi.fn();
    await act(async () => {
      root.render(
        <FilePreviewPanel
          file={createCloudArtifactPreviewFile({
            id: 'reloadable',
            agent_uid: 440,
            title: 'Reloadable artifact',
            url: artifactURL,
            publish_version: 1,
          })}
          onRemoteArtifactFrameChange={onBindingChange}
          onClose={vi.fn()}
        />,
      );
      await flushAsync();
    });

    const frame = container.querySelector('iframe.v3-file-preview-frame');
    await act(async () => {
      Simulate.load(frame);
      await flushAsync();
    });
    const firstBinding = onBindingChange.mock.calls.at(-1)?.[0];
    expect(firstBinding?.bridge).toBe('catsco.artifact-frame-bridge.v1');
    expect(firstBinding?.bridgeReady).toBe(true);
    expect(firstBinding?.signal?.aborted).toBe(false);

    await act(async () => {
      Simulate.load(frame);
      await flushAsync();
    });
    expect(firstBinding.signal.aborted).toBe(true);
    expect(onBindingChange.mock.calls.at(-1)?.[0]).toBeNull();
    expect(container.querySelector('.v3-remote-artifact-preview-state.error')).not.toBeNull();
  });

  it('aborts the active opaque bridge when the preview unmounts', async () => {
    const artifactURL = new URL('/artifacts/by-agent/440/unmountable/latest/', window.location.origin).toString();
    const onBindingChange = vi.fn();
    await act(async () => {
      root.render(
        <FilePreviewPanel
          file={createCloudArtifactPreviewFile({
            id: 'unmountable',
            agent_uid: 440,
            title: 'Unmountable artifact',
            url: artifactURL,
            publish_version: 1,
          })}
          onRemoteArtifactFrameChange={onBindingChange}
          onClose={vi.fn()}
        />,
      );
      await flushAsync();
    });

    const frame = container.querySelector('iframe.v3-file-preview-frame');
    await act(async () => {
      Simulate.load(frame);
      await flushAsync();
    });
    const binding = onBindingChange.mock.calls.at(-1)?.[0];
    expect(binding?.signal?.aborted).toBe(false);

    await act(async () => {
      root.render(null);
      await flushAsync();
    });

    expect(binding.signal.aborted).toBe(true);
  });

  it('keeps an unknown external URL as an ordinary link', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 31,
            from_uid: 440,
            content: '[artifact-link-test](https://artifacts.example.test/by-agent/440/lesson-game/latest/)',
            created_at: '2026-07-27T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          knownArtifacts={[]}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-message-artifact-list')).toBeNull();
    expect(container.querySelector('.oc-markdown a:not(.oc-artifact-source-link)')).not.toBeNull();
  });

  it('does not trust a message payload that declares itself to be a remote Artifact', async () => {
    const externalURL = 'https://example.com/forged-artifact.html';
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 34,
            from_uid: 440,
            content: {
              type: 'file',
              payload: {
                name: 'forged-artifact.html',
                url: externalURL,
                mime_type: 'text/html',
                preview_mode: 'remote-static-artifact',
              },
            },
            created_at: '2026-07-27T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-file-preview-panel')).toBeNull();
    expect(window.open).toHaveBeenCalledWith(externalURL, '_blank');
  });

  it('does not match a known Artifact URL used as the prefix of a different URL', async () => {
    const artifact = {
      id: 'query-game',
      title: 'Query Game',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/query-game/latest/',
    };
    const differentURL = `${artifact.url}?mode=test`;
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 35,
            from_uid: 440,
            content: `调试地址：${differentURL}`,
            created_at: '2026-07-27T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          knownArtifacts={[artifact]}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-message-artifact-list')).toBeNull();
    expect(container.querySelector('.v3-message-content').textContent).toContain(differentURL);
  });

  it('requires a URL token boundary and preserves fragment-specific deep links', async () => {
    const artifact = {
      id: 'boundary-game',
      title: 'Boundary Game',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/boundary-game/latest/',
    };
    const prefixedURL = `prefix${artifact.url}`;
    const fragmentURL = `${artifact.url}#step-2`;
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 36,
            from_uid: 440,
            content: `${prefixedURL}\n${fragmentURL}`,
            created_at: '2026-07-27T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          knownArtifacts={[artifact]}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-message-artifact-list')).toBeNull();
    expect(container.querySelector('.v3-message-content').textContent).toContain(prefixedURL);
    expect(container.querySelector('.v3-message-content').textContent).toContain(fragmentURL);
  });

  it('turns an exact bare Artifact URL into a card', async () => {
    const artifact = {
      id: 'bare-game',
      title: 'Bare URL Game',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/bare-game/latest/',
      publish_version: 1,
    };
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 32,
            from_uid: 440,
            content: `已发布：${artifact.url}。`,
            created_at: '2026-07-27T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          knownArtifacts={[artifact]}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.v3-message-artifact-list')).not.toBeNull();
    expect(container.querySelector('.v3-message-content').textContent).not.toContain(artifact.url);
  });

  it('turns an Artifact URL inside a plain text table into a card', async () => {
    const artifact = {
      id: 'table-game',
      title: 'Table URL Game',
      kind: 'html',
      url: 'https://artifacts.example.test/by-agent/440/table-game/latest/',
    };
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 33,
            from_uid: 440,
            content: [
              '序号 名称 地址 状态',
              `1 游戏 ${artifact.url} 已发布`,
              '2 说明 无 正常',
            ].join('\n'),
            created_at: '2026-07-27T00:00:00Z',
          }}
          isSelf={false}
          isGroup={false}
          senderName="CatsCo"
          knownArtifacts={[artifact]}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-plain-text-table')).not.toBeNull();
    expect(container.querySelector('.oc-plain-text-table').textContent).not.toContain(artifact.url);
    expect(container.querySelector('.v3-message-artifact-list')).not.toBeNull();
  });

  it('previews PDF files without fetching their content', async () => {
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
    expect(panel.querySelector('.v3-mobile-pdf-preview-mock').dataset.url).toBe('/uploads/files/report.pdf');
    const downloadLink = panel.querySelector('.v3-file-preview-actions a');
    expect(downloadLink.getAttribute('href')).toBe('/uploads/files/report.pdf?download=1');
    expect(downloadLink.getAttribute('download')).toBe('report.pdf');
    expect(downloadLink.getAttribute('target')).toBe('_blank');
  });

  it('keeps PDF sharing inside the preview and shares the inline URL', async () => {
    const originalShare = Object.getOwnPropertyDescriptor(navigator, 'share');
    const share = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'share', {
      configurable: true,
      value: share,
    });

    try {
      await act(async () => {
        root.render(
          <PreviewHarness
            message={{
              id: 44,
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
        await flushAsync();
      });

      await act(async () => {
        Simulate.click(container.querySelector('.v3-artifact-main'));
        await flushAsync();
      });

      const panel = container.querySelector('.v3-file-preview-panel');
      const shareButton = panel.querySelector('button[aria-label="分享 PDF"]');
      expect(shareButton).not.toBeNull();
      expect(container.querySelector('.v3-artifact-actions button[aria-label="分享 PDF"]')).toBeNull();
      expect(panel.querySelector('a[download]').getAttribute('href'))
        .toBe('/uploads/files/report.pdf?download=1');

      await act(async () => {
        Simulate.click(shareButton);
        await flushAsync();
      });

      expect(share).toHaveBeenCalledWith({
        title: 'report.pdf',
        url: new URL('/uploads/files/report.pdf?preview=1&name=report.pdf', window.location.href).toString(),
      });
    } finally {
      if (originalShare) Object.defineProperty(navigator, 'share', originalShare);
      else delete navigator.share;
    }
  });

  it('keeps HTML sharing inside the preview while preserving the download action', async () => {
    const originalShare = Object.getOwnPropertyDescriptor(navigator, 'share');
    const share = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'share', {
      configurable: true,
      value: share,
    });

    try {
      await act(async () => {
        root.render(
          <PreviewHarness
            message={{
              id: 45,
              from_uid: 2,
              content: '[文件] report.html',
              content_blocks: [{
                type: 'file',
                payload: {
                  name: 'report.html',
                  url: '/uploads/files/report.html',
                  size: 2048,
                  mime_type: 'text/html',
                },
              }],
              created_at: '2026-06-09T00:00:00Z',
            }}
          />,
        );
        await flushAsync();
      });

      await act(async () => {
        Simulate.click(container.querySelector('.v3-artifact-main'));
        await flushAsync();
      });

      const panel = container.querySelector('.v3-file-preview-panel');
      const shareButton = panel.querySelector('button[aria-label="分享 HTML"]');
      expect(shareButton).not.toBeNull();
      expect(container.querySelector('.v3-artifact-actions button[aria-label^="分享"]')).toBeNull();
      expect(panel.querySelector('a[download]').getAttribute('href'))
        .toBe('/uploads/files/report.html?download=1');

      await act(async () => {
        Simulate.click(shareButton);
        await flushAsync();
      });

      expect(share).toHaveBeenCalledWith({
        title: 'report.html',
        url: new URL('/uploads/files/report.html?preview=1&name=report.html', window.location.href).toString(),
      });
    } finally {
      if (originalShare) Object.defineProperty(navigator, 'share', originalShare);
      else delete navigator.share;
    }
  });

  it('closes the file preview from its backdrop and the Escape key', async () => {
    const message = {
      id: 41,
      from_uid: 2,
      content: '[文件] mobile-report.pdf',
      content_blocks: [{
        type: 'file',
        payload: {
          name: 'mobile-report.pdf',
          url: '/uploads/files/mobile-report.pdf',
          size: 2048,
          mime_type: 'application/pdf',
        },
      }],
      created_at: '2026-06-09T00:00:00Z',
    };

    await act(async () => {
      root.render(<PreviewHarness message={message} />);
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-file-preview-panel')).not.toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('.v3-file-preview-backdrop'));
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-file-preview-panel')).toBeNull();

    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-file-preview-panel')).not.toBeNull();

    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
      await Promise.resolve();
    });
    expect(container.querySelector('.v3-file-preview-panel')).toBeNull();
  });

  it('makes the narrow file preview modal, traps focus, and restores the chat on close', async () => {
    const originalMatchMedia = window.matchMedia;
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn((query) => ({
        matches: query === '(max-width: 1024px)',
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    });
    const message = {
      id: 42,
      from_uid: 2,
      content: '[文件] accessible-report.pdf',
      content_blocks: [{
        type: 'file',
        payload: {
          name: 'accessible-report.pdf',
          url: '/uploads/files/accessible-report.pdf',
          size: 2048,
          mime_type: 'application/pdf',
        },
      }],
      created_at: '2026-06-09T00:00:00Z',
    };

    try {
      await act(async () => {
        root.render(<PreviewHarness message={message} />);
        await Promise.resolve();
      });
      const opener = container.querySelector('.v3-artifact-main');
      opener.focus();

      await act(async () => {
        Simulate.click(opener);
        await flushAsync();
      });

      const panel = container.querySelector('.v3-file-preview-panel');
      const chatColumn = container.querySelector('.v3-chat-column');
      const closeButton = panel.querySelector('button[aria-label="关闭预览"]');
      expect(panel.getAttribute('role')).toBe('dialog');
      expect(panel.getAttribute('aria-modal')).toBe('true');
      expect(panel.querySelector('.v3-mobile-pdf-preview-mock').dataset.url).toBe('/uploads/files/accessible-report.pdf');
      expect(panel.querySelector('iframe.v3-file-preview-frame')).toBeNull();
      expect(chatColumn.hasAttribute('inert')).toBe(true);
      expect(chatColumn.getAttribute('aria-hidden')).toBe('true');
      expect(document.activeElement).toBe(closeButton);

      const focusable = panel.querySelectorAll('button:not([disabled]), [href], [tabindex]:not([tabindex="-1"])');
      focusable[focusable.length - 1].focus();
      await act(async () => {
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
        await Promise.resolve();
      });
      expect(document.activeElement).toBe(panel.querySelector('.v3-file-preview-drag-handle'));

      const dragHandle = panel.querySelector('.v3-file-preview-drag-handle');
      expect(document.activeElement).toBe(dragHandle);
      await act(async () => {
        Simulate.keyDown(dragHandle, { key: 'Enter' });
        await Promise.resolve();
      });
      expect(panel.className).toContain('is-dismissing');

      await act(async () => {
        Simulate.transitionEnd(panel, { propertyName: 'transform' });
        await Promise.resolve();
      });
      expect(container.querySelector('.v3-file-preview-panel')).toBeNull();
      expect(chatColumn.hasAttribute('inert')).toBe(false);
      expect(chatColumn.hasAttribute('aria-hidden')).toBe(false);
      expect(document.activeElement).toBe(opener);

      const committedFrames = [];
      const observer = new MutationObserver((records) => {
        records.forEach((record) => record.addedNodes.forEach((node) => {
          if (node.nodeType !== Node.ELEMENT_NODE) return;
          if (node.matches?.('iframe.v3-file-preview-frame')) committedFrames.push(node);
          committedFrames.push(...node.querySelectorAll?.('iframe.v3-file-preview-frame') || []);
        }));
      });
      observer.observe(container, { childList: true, subtree: true });
      await act(async () => {
        Simulate.click(opener);
        await flushAsync();
      });
      observer.disconnect();

      expect(committedFrames).toHaveLength(0);
      expect(container.querySelector('.v3-mobile-pdf-preview-mock')).not.toBeNull();
      expect(container.querySelector('iframe.v3-file-preview-frame')).toBeNull();
    } finally {
      Object.defineProperty(window, 'matchMedia', { configurable: true, value: originalMatchMedia });
    }
  });

  it('dismisses the mobile file preview immediately when reduced motion is requested', async () => {
    const originalMatchMedia = window.matchMedia;
    const matchMedia = vi.fn((query) => ({
      matches: query === '(max-width: 1024px)' || query === '(prefers-reduced-motion: reduce)',
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    }));
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: matchMedia,
    });

    try {
      await act(async () => {
        root.render(
          <PreviewHarness
            message={{
              id: 43,
              from_uid: 2,
              content: '[文件] reduced-motion-report.pdf',
              content_blocks: [{
                type: 'file',
                payload: {
                  name: 'reduced-motion-report.pdf',
                  url: '/uploads/files/reduced-motion-report.pdf',
                  size: 2048,
                  mime_type: 'application/pdf',
                },
              }],
              created_at: '2026-06-09T00:00:00Z',
            }}
          />,
        );
        await flushAsync();
      });

      await act(async () => {
        Simulate.click(container.querySelector('.v3-artifact-main'));
        await flushAsync();
      });
      const handle = container.querySelector('.v3-file-preview-drag-handle');
      expect(handle).not.toBeNull();

      await act(async () => {
        Simulate.keyDown(handle, { key: 'Enter' });
        await Promise.resolve();
      });

      expect(matchMedia).toHaveBeenCalledWith('(prefers-reduced-motion: reduce)');
      expect(container.querySelector('.v3-file-preview-panel')).toBeNull();
    } finally {
      Object.defineProperty(window, 'matchMedia', { configurable: true, value: originalMatchMedia });
    }
  });

  it('keeps the desktop file preview as a non-modal side panel', async () => {
    const originalMatchMedia = window.matchMedia;
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn(() => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    });

    try {
      await act(async () => {
        root.render(
          <FilePreviewPanel
            file={{
              name: 'desktop-report.pdf',
              url: '/uploads/files/desktop-report.pdf',
              size: 2048,
              mime_type: 'application/pdf',
            }}
            onClose={vi.fn()}
          />,
        );
        await flushAsync();
      });
      const panel = container.querySelector('.v3-file-preview-panel');
      expect(panel.hasAttribute('role')).toBe(false);
      expect(panel.hasAttribute('aria-modal')).toBe(false);
      expect(panel.querySelector('iframe.v3-file-preview-frame').getAttribute('src')).toBe('/uploads/files/desktop-report.pdf');
      expect(panel.querySelector('.v3-mobile-pdf-preview-mock')).toBeNull();
    } finally {
      Object.defineProperty(window, 'matchMedia', { configurable: true, value: originalMatchMedia });
    }
  });

  it('closes the mobile file preview when its handle is dragged down past the threshold', async () => {
    const onClose = vi.fn();

    await act(async () => {
      root.render(
        <FilePreviewPanel
          file={{
            name: 'mobile-report.pdf',
            url: '/uploads/files/mobile-report.pdf',
            size: 2048,
            mime_type: 'application/pdf',
          }}
          onClose={onClose}
        />,
      );
      await Promise.resolve();
    });

    const handle = container.querySelector('.v3-file-preview-drag-handle');
    expect(handle).not.toBeNull();

    await act(async () => {
      Simulate.pointerDown(handle, { pointerId: 1, pointerType: 'touch', clientY: 100 });
      Simulate.pointerMove(handle, { pointerId: 1, pointerType: 'touch', clientY: 160 });
      Simulate.pointerUp(handle, { pointerId: 1, pointerType: 'touch', clientY: 160 });
      await Promise.resolve();
    });

    const panel = container.querySelector('.v3-file-preview-panel');
    expect(onClose).not.toHaveBeenCalled();
    expect(panel.style.getPropertyValue('--v3-preview-drag-offset')).toBe('0px');

    await act(async () => {
      Simulate.pointerDown(handle, { pointerId: 1, pointerType: 'touch', clientY: 100 });
      Simulate.pointerMove(handle, { pointerId: 1, pointerType: 'touch', clientY: 180 });
      Simulate.pointerUp(handle, { pointerId: 1, pointerType: 'touch', clientY: 180 });
      await Promise.resolve();
    });

    expect(panel.className).toContain('is-dismissing');
    expect(onClose).not.toHaveBeenCalled();

    await act(async () => {
      Simulate.transitionEnd(panel, { propertyName: 'transform' });
      await Promise.resolve();
    });
    expect(onClose).toHaveBeenCalledTimes(1);
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
    expect(global.fetch).toHaveBeenCalledWith(
      '/uploads/files/legacy-report.html',
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    );
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
                name: '【电商带货主播_广州 4-6K】何荧 25年应届生.pdf',
                url: '/uploads/files/20260715_f547bf132d510e621877d89214098db5.pdf',
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
    expect(actions[1].getAttribute('href')).toBe('/uploads/files/20260715_f547bf132d510e621877d89214098db5.pdf?download=1');
    expect(actions[1].hasAttribute('download')).toBe(true);
    expect(actions[1].getAttribute('download')).toBe('【电商带货主播_广州 4-6K】何荧 25年应届生.pdf');
    expect(actions[1].getAttribute('target')).toBe('_blank');
  });

  it('keeps CatsCo OSS downloads in the current context only in an installed PWA', async () => {
    Object.defineProperty(navigator, 'standalone', { configurable: true, value: true });
    const file = {
      name: 'report.pdf',
      url: '/uploads/files/report.pdf',
      size: 2048,
      mime_type: 'application/pdf',
    };

    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 71,
            from_uid: 2,
            content: '[文件] report.pdf',
            content_blocks: [{ type: 'file', payload: file }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await flushAsync();
    });

    expect(container.querySelector('.v3-artifact-action[download]').getAttribute('target')).toBeNull();
    await act(async () => {
      Simulate.click(container.querySelector('.v3-artifact-main'));
      await flushAsync();
    });
    expect(container.querySelector('.v3-file-preview-actions a[download]').getAttribute('target')).toBeNull();
    expect(container.querySelector('.v3-file-preview-mobile-actions a[download]').getAttribute('target')).toBeNull();
  });

  it('renders MP4 attachments as image-sized thumbnails that open a video preview', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          isSelf
          message={{
            id: 8,
            from_uid: 2,
            content: '[文件] product-demo.mp4',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'product-demo.mp4',
                url: '/uploads/files/20260727_1234567890abcdef1234567890abcdef.mp4',
                size: 4096,
                mime_type: 'video/mp4',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    const thumbnail = container.querySelector('video.oc-rich-video-thumb');
    const trigger = container.querySelector('button.oc-rich-video-trigger');
    expect(thumbnail).not.toBeNull();
    expect(thumbnail.getAttribute('src')).toBe('/uploads/files/20260727_1234567890abcdef1234567890abcdef.mp4');
    expect(thumbnail.muted).toBe(true);
    expect(thumbnail.playsInline).toBe(true);
    expect(thumbnail.preload).toBe('metadata');
    expect(trigger.getAttribute('aria-label')).toBe('预览视频 product-demo.mp4');
    expect(container.querySelector('.v3-message').classList.contains('has-file-only')).toBe(false);
    expect(container.querySelector('video.oc-rich-video-player')).toBeNull();
    expect(container.querySelector('.v3-attachment-card')).toBeNull();
    expect(container.querySelector('.v3-file-preview-panel')).toBeNull();

    Object.defineProperties(thumbnail, {
      videoHeight: { configurable: true, value: 540 },
      videoWidth: { configurable: true, value: 1920 },
    });
    await act(async () => {
      thumbnail.dispatchEvent(new Event('loadedmetadata', { bubbles: true }));
      await Promise.resolve();
    });
    expect(container.querySelector('.oc-rich-video').classList.contains('is-ultrawide')).toBe(false);

    trigger.focus();
    await act(async () => {
      Simulate.click(trigger);
      await Promise.resolve();
    });

    const preview = container.querySelector('video.oc-rich-video-player');
    const closeButton = container.querySelector('button.oc-rich-video-preview-close');
    const download = container.querySelector('a.oc-rich-media-preview-download');
    expect(preview).not.toBeNull();
    expect(preview.getAttribute('src')).toBe('/uploads/files/20260727_1234567890abcdef1234567890abcdef.mp4');
    expect(preview.controls).toBe(true);
    expect(preview.autoplay).toBe(true);
    expect(preview.getAttribute('aria-label')).toBe('product-demo.mp4');
    expect(download.getAttribute('aria-label')).toBe('下载视频 product-demo.mp4');
    expect(download.getAttribute('href')).toBe('/uploads/files/20260727_1234567890abcdef1234567890abcdef.mp4?download=1');
    expect(download.getAttribute('download')).toBe('product-demo.mp4');
    expect(container.querySelector('.oc-rich-video-preview').getAttribute('role')).toBe('dialog');
    expect(document.activeElement).toBe(closeButton);

    preview.focus();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }));
      await Promise.resolve();
    });
    expect(document.activeElement).toBe(closeButton);

    closeButton.focus();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true }));
      await Promise.resolve();
    });
    expect(document.activeElement).toBe(preview);

    await act(async () => {
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-rich-video-preview')).toBeNull();
    expect(document.activeElement).toBe(trigger);

    await act(async () => {
      Simulate.click(trigger);
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('button.oc-rich-video-preview-close'));
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-rich-video-preview')).toBeNull();
    expect(document.activeElement).toBe(trigger);
  });

  it('shares a video metadata preview URL while preserving the download action', async () => {
    const originalShare = Object.getOwnPropertyDescriptor(navigator, 'share');
    const share = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'share', {
      configurable: true,
      value: share,
    });

    try {
      await act(async () => {
        root.render(
          <PreviewHarness
            message={{
              id: 81,
              from_uid: 2,
              content: '[文件] product-demo.mp4',
              content_blocks: [{
                type: 'file',
                payload: {
                  name: 'product-demo.mp4',
                  url: '/uploads/files/20260727_1234567890abcdef1234567890abcdef.mp4',
                  size: 4096,
                  mime_type: 'video/mp4',
                },
              }],
              created_at: '2026-06-09T00:00:00Z',
            }}
          />,
        );
        await flushAsync();
      });

      await act(async () => {
        Simulate.click(container.querySelector('button.oc-rich-video-trigger'));
        await flushAsync();
      });

      const preview = container.querySelector('.oc-rich-video-preview');
      const shareButton = preview.querySelector('button[aria-label="分享视频"]');
      const download = preview.querySelector('a.oc-rich-media-preview-download');
      expect(shareButton).not.toBeNull();
      expect(download.getAttribute('href')).toBe('/uploads/files/20260727_1234567890abcdef1234567890abcdef.mp4?download=1');

      await act(async () => {
        Simulate.click(shareButton);
        await flushAsync();
      });

      expect(share).toHaveBeenCalledWith({
        title: 'product-demo.mp4',
        url: new URL(
          '/uploads/files/20260727_1234567890abcdef1234567890abcdef.mp4?preview=1&name=product-demo.mp4',
          window.location.href,
        ).toString(),
      });
    } finally {
      if (originalShare) Object.defineProperty(navigator, 'share', originalShare);
      else delete navigator.share;
    }
  });

  it.each([
    'product-demo.webm',
    'product-demo.ogv',
    'product-demo.m4v',
    'product-demo.mov',
  ])('embeds %s attachments by extension', async (fileName) => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 9,
            from_uid: 2,
            content: `[文件] ${fileName}`,
            content_blocks: [{
              type: 'file',
              payload: {
                name: fileName,
                url: `/uploads/files/20260727_abcdef1234567890abcdef1234567890-${fileName}`,
                size: 4096,
                mime_type: 'application/octet-stream',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    const video = container.querySelector('video.oc-rich-video-thumb');
    expect(video).not.toBeNull();
    expect(video.getAttribute('src')).toContain(fileName);
    expect(container.querySelector('button.oc-rich-video-trigger').getAttribute('aria-label')).toBe(`预览视频 ${fileName}`);
  });

  it('recognizes signed video URLs when name and MIME metadata are absent', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 91,
            from_uid: 2,
            content: '[文件] signed video',
            content_blocks: [{
              type: 'file',
              payload: {
                url: 'https://media.example.com/product-demo.mp4?token=abc123#preview',
                size: 4096,
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('video.oc-rich-video-thumb')).not.toBeNull();
    expect(container.querySelector('.v3-attachment-card')).toBeNull();
  });

  it('recognizes Ogg video by MIME and falls back to the file card after a playback error', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 10,
            from_uid: 2,
            content: '[文件] product-demo.ogg',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'product-demo.ogg',
                url: '/uploads/files/20260727_fedcba0987654321fedcba0987654321.ogg',
                size: 4096,
                mime_type: 'video/ogg',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    const video = container.querySelector('video.oc-rich-video-thumb');
    expect(video).not.toBeNull();

    await act(async () => {
      Simulate.error(video);
      await Promise.resolve();
    });

    expect(container.querySelector('video.oc-rich-video-thumb')).toBeNull();
    expect(container.querySelector('.v3-attachment-name').textContent).toBe('product-demo.ogg');
    expect(container.querySelector('a.v3-artifact-action').getAttribute('href')).toContain('download=1');
  });

  it('moves focus to the download fallback and announces a preview playback error', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 101,
            from_uid: 2,
            content: '[文件] broken-preview.mp4',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'broken-preview.mp4',
                url: '/uploads/files/broken-preview.mp4',
                size: 4096,
                mime_type: 'video/mp4',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    await act(async () => {
      Simulate.click(container.querySelector('.oc-rich-video-trigger'));
      await Promise.resolve();
    });
    const player = container.querySelector('.oc-rich-video-player');
    player.focus();

    await act(async () => {
      Simulate.error(player);
      await Promise.resolve();
    });

    const fallbackAction = container.querySelector('.v3-artifact-main');
    expect(document.activeElement).toBe(fallbackAction);
    expect(container.querySelector('[role="status"]').textContent).toContain('视频无法播放');
    expect(container.querySelector('.oc-rich-video-preview')).toBeNull();
  });

  it('recognizes Ogg video MIME types with codec parameters', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 102,
            from_uid: 2,
            content: '[文件] product-demo.ogg',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'product-demo.ogg',
                url: '/uploads/files/20260727_fedcba0987654321fedcba0987654321.ogg',
                size: 4096,
                mime_type: 'video/ogg; codecs=theora',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('video.oc-rich-video-thumb')).not.toBeNull();
    expect(container.querySelector('.v3-attachment-card')).toBeNull();
  });

  it('plays browser-supported audio attachments inline and preserves an explicit download', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 11,
            from_uid: 2,
            content: '[文件] recording.ogg',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'recording.ogg',
                url: '/uploads/files/20260727_00112233445566778899aabbccddeeff.ogg',
                size: 4096,
                mime_type: 'audio/ogg',
              },
            }],
            created_at: '2026-06-09T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    const player = container.querySelector('audio.oc-rich-audio-player');
    const download = container.querySelector('a.oc-rich-audio-download');

    expect(container.querySelector('video.oc-rich-video-player')).toBeNull();
    expect(player).not.toBeNull();
    expect(player.getAttribute('src')).toBe('/uploads/files/20260727_00112233445566778899aabbccddeeff.ogg');
    expect(player.controls).toBe(true);
    expect(player.preload).toBe('metadata');
    expect(player.getAttribute('aria-label')).toBe('播放音频 recording.ogg');
    expect(container.querySelector('.oc-rich-audio-name').textContent).toBe('recording.ogg');
    expect(download.getAttribute('href')).toContain('download=1');
    expect(download.getAttribute('download')).toBe('recording.ogg');
    expect(download.getAttribute('target')).toBe('_blank');
    expect(download.getAttribute('rel')).toBe('noopener noreferrer');
  });

  it.each([
    ['recording.mp3', 'audio/mpeg'],
    ['recording.wav', 'audio/wav'],
  ])('plays %s attachments inline', async (name, mimeType) => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: `audio-${name}`,
            from_uid: 2,
            content: `[文件] ${name}`,
            content_blocks: [{
              type: 'file',
              payload: {
                name,
                url: `/uploads/files/20260812_00112233445566778899aabbccddeeff-${name}`,
                size: 4096,
                mime_type: mimeType,
              },
            }],
            created_at: '2026-08-12T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('audio.oc-rich-audio-player')).not.toBeNull();
    expect(container.querySelector('.v3-attachment-card')).toBeNull();
  });

  it('accepts explicit voice blocks for inline playback and forced download without a filename', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 111,
            from_uid: 2,
            content: '[语音]',
            content_blocks: [{
              type: 'voice',
              payload: {
                url: '/uploads/files/20260812_00112233445566778899aabbccddeeff.ogg',
                size: 4096,
                mime_type: 'audio/ogg; codecs=opus',
              },
            }],
            created_at: '2026-08-12T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('audio.oc-rich-audio-player')).not.toBeNull();
    expect(container.querySelector('a.oc-rich-audio-download').getAttribute('href'))
      .toBe('/uploads/files/20260812_00112233445566778899aabbccddeeff.ogg?download=1');
  });

  it('keeps the forced-download query when the API uses a relative path prefix', async () => {
    const originalImplementation = resolveMediaURL.getMockImplementation();
    resolveMediaURL.mockImplementation((url) => `/api${url}`);
    try {
      await act(async () => {
        root.render(
          <PreviewHarness
            message={{
              id: 1111,
              from_uid: 2,
              content: '[语音]',
              content_blocks: [{
                type: 'voice',
                payload: {
                  url: '/uploads/files/20260812_00112233445566778899aabbccddeeff.ogg',
                  size: 4096,
                  mime_type: 'audio/ogg; codecs=opus',
                },
              }],
              created_at: '2026-08-12T00:00:00Z',
            }}
          />,
        );
        await Promise.resolve();
      });

      expect(container.querySelector('a.oc-rich-audio-download').getAttribute('href'))
        .toBe('/api/uploads/files/20260812_00112233445566778899aabbccddeeff.ogg?download=1');
    } finally {
      resolveMediaURL.mockImplementation(originalImplementation);
    }
  });

  it('falls back to a downloadable file card when audio playback fails', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 112,
            from_uid: 2,
            content: '[文件] broken.ogg',
            content_blocks: [{
              type: 'file',
              payload: {
                name: 'broken.ogg',
                url: '/uploads/files/20260812_ffeeddccbbaa99887766554433221100.ogg',
                size: 4096,
                mime_type: 'audio/ogg',
              },
            }],
            created_at: '2026-08-12T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    const player = container.querySelector('audio.oc-rich-audio-player');
    await act(async () => {
      Simulate.error(player);
      await Promise.resolve();
    });

    expect(container.querySelector('audio.oc-rich-audio-player')).toBeNull();
    expect(container.querySelector('[role="status"]').textContent).toContain('音频无法播放');
    expect(container.querySelector('.v3-attachment-name').textContent).toBe('broken.ogg');
    expect(container.querySelector('a.v3-artifact-action').getAttribute('href')).toContain('download=1');
  });

  it('keeps unsupported explicit voice formats as a download card', async () => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: 113,
            from_uid: 2,
            content: '[语音] legacy.amr',
            content_blocks: [{
              type: 'voice',
              payload: {
                name: 'legacy.amr',
                url: '/uploads/files/20260812_00112233445566778899aabbccddeeff.amr',
                size: 4096,
                mime_type: 'audio/amr',
              },
            }],
            created_at: '2026-08-12T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('audio.oc-rich-audio-player')).toBeNull();
    expect(container.querySelector('.v3-attachment-name').textContent).toBe('legacy.amr');
    expect(container.querySelector('a.v3-artifact-action').getAttribute('href')).toContain('download=1');
  });

  it.each([
    ['legacy.opus', 'audio/opus'],
    ['legacy.opus', 'audio/ogg'],
    ['mislabelled.ogg', 'audio/opus'],
  ])('keeps %s download-only when MIME is %s', async (name, mimeType) => {
    await act(async () => {
      root.render(
        <PreviewHarness
          message={{
            id: `opus-${name}-${mimeType}`,
            from_uid: 2,
            content: `[语音] ${name}`,
            content_blocks: [{
              type: 'voice',
              payload: {
                name,
                url: `/uploads/files/20260812_00112233445566778899aabbccddeeff.${name.split('.').pop()}`,
                size: 4096,
                mime_type: mimeType,
              },
            }],
            created_at: '2026-08-12T00:00:00Z',
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('audio.oc-rich-audio-player')).toBeNull();
    expect(container.querySelector('.v3-attachment-name').textContent).toBe(name);
    expect(container.querySelector('a.v3-artifact-action').getAttribute('href')).toContain('download=1');
  });

  it('restores the video thumbnail when a reused attachment changes URL', async () => {
    const renderVideo = async (url) => {
      await act(async () => {
        root.render(
          <PreviewHarness
            message={{
              id: 12,
              from_uid: 2,
              content: '[文件] product-demo.mp4',
              content_blocks: [{
                type: 'file',
                payload: {
                  name: 'product-demo.mp4',
                  url,
                  size: 4096,
                  mime_type: 'video/mp4',
                },
              }],
              created_at: '2026-06-09T00:00:00Z',
            }}
          />,
        );
        await Promise.resolve();
      });
    };

    await renderVideo('/uploads/files/first.mp4');
    await act(async () => {
      Simulate.error(container.querySelector('video.oc-rich-video-thumb'));
      await Promise.resolve();
    });
    expect(container.querySelector('video.oc-rich-video-thumb')).toBeNull();

    await renderVideo('/uploads/files/second.mp4');
    expect(container.querySelector('video.oc-rich-video-thumb').getAttribute('src')).toBe('/uploads/files/second.mp4');
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
    expect(container.querySelector('a.v3-artifact-action').getAttribute('href')).toBe('/uploads/files/handout.docx?download=1');
  });
});
