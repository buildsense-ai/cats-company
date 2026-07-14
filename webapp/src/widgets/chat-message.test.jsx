import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

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
      return `<p>${text}</p>`;
    },
  },
}));

vi.mock('../api', () => ({
  resolveMediaURL: (url) => url,
}));

vi.mock('./avatar', () => ({
  default: function MockAvatar() {
    return <div data-testid="avatar" />;
  },
}));

vi.mock('read-excel-file/browser', () => ({
  __esModule: true,
  default: vi.fn(),
}));

import ChatMessage, { FilePreviewPanel } from './chat-message';
import { markdownPreviewDocument } from './markdown-utils';
import readExcelFile from 'read-excel-file/browser';

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

  it('preserves line breaks in group plain text messages', async () => {
    await act(async () => {
      root.render(
        <ChatMessage
          message={{
            id: 21,
            from_uid: 2,
            content: '第一段\n\n第二段\n第三段',
            created_at: '2026-06-09T00:00:00Z',
          }}
          isSelf={false}
          isGroup={true}
          senderName="CatsCo"
        />,
      );
      await Promise.resolve();
    });

    const textNode = Array.from(container.querySelectorAll('span'))
      .find((node) => node.textContent === '第一段\n\n第二段\n第三段');
    expect(textNode).not.toBeUndefined();
    expect(textNode.style.whiteSpace).toBe('pre-wrap');
    expect(textNode.style.overflowWrap).toBe('anywhere');
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

    expect(global.fetch).toHaveBeenCalledWith('/uploads/files/grade.xlsx');
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

    expect(global.fetch).toHaveBeenCalledWith('/uploads/files/large.xlsx');
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

  it('uses the hover action toolbar for reply and more actions without dead reaction buttons', async () => {
    const onReply = vi.fn();
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
