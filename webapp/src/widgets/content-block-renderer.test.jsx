import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import ContentBlockRenderer from './content-block-renderer';

vi.mock('marked', () => ({
  marked: {
    setOptions: vi.fn(),
    parse: () => '<table><thead><tr><th>指标</th><th>当前结果</th></tr></thead><tbody><tr><td>班级平均分</td><td>82.6</td></tr></tbody></table>',
  },
}));

describe('ContentBlockRenderer', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => {
      root.unmount();
    });
    container.remove();
  });

  it('renders aligned plain text tables as bordered text table blocks', async () => {
    await act(async () => {
      root.render(
        <ContentBlockRenderer
          block={{
            type: 'text',
            text: [
              '指标        当前结果        建议',
              '班级平均分  82.6            保持节奏',
              '异常样本    2 人低于 60 分   单独复盘',
            ].join('\n'),
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-text-block.oc-plain-text-table')).not.toBeNull();
    expect(container.querySelector('table')).toBeNull();
    expect(container.textContent).toContain('班级平均分');
  });

  it('renders loose text tables as bordered plain text table blocks', async () => {
    await act(async () => {
      root.render(
        <ContentBlockRenderer
          block={{
            type: 'text',
            text: [
              '# 文件夹 项目 页面',
              '1bg-summary-test后台子任务回流测试index + about + notes',
              '2bg-group-test后台子任务组测试index + about + notes',
            ].join('\n'),
          }}
        />,
      );
      await Promise.resolve();
    });

    expect(container.querySelector('.oc-text-block.oc-plain-text-table')).not.toBeNull();
    expect(container.querySelector('table')).toBeNull();
    expect(container.textContent).toContain('bg-summary-test');
  });
});
