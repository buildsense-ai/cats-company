jest.mock('marked', () => ({
  marked: {
    setOptions: jest.fn(),
    parse: (text) => `<p>${text}</p>`,
  },
}));

const {
  hasPlainTextTableLikeBlock,
  normalizePlainTextTables,
  renderSafeMarkdown,
} = require('./markdown-utils');

describe('markdown-utils plain text table detection', () => {
  it('detects aligned model-generated text table messages', () => {
    const text = [
      '#   文件夹                    项目                    页面',
      '1bg-summary-test             后台子任务回流测试       index + about + notes',
      '2bg-group-test               后台子任务组测试         index + about + notes',
      '3bg-clean-test               后台清理机制测试         index + about + notes',
    ].join('\n');

    expect(hasPlainTextTableLikeBlock(text)).toBe(true);
    expect(normalizePlainTextTables(text)).toBe(text);
  });

  it('detects HR workflow text tables from model replies', () => {
    const text = [
      '场景                    谁来填                    典型情况',
      '情况一                  HR/管理人员直接录入        现场入职、紧急录入、补录老员工',
      '情况二                  新员工自己上传材料，HR审核补齐  批量校招、异地入职、线上采集',
    ].join('\n');

    expect(hasPlainTextTableLikeBlock(text)).toBe(true);
  });

  it('detects tab and full-width-space separated text table messages', () => {
    expect(hasPlainTextTableLikeBlock([
      '任务\t状态\t说明',
      '上传入口\t完成\t支持 CSV 和 HTML',
      '预览面板\t进行中\t补充 PDF 预览',
    ].join('\n'))).toBe(true);

    expect(hasPlainTextTableLikeBlock([
      '指标　当前值　建议',
      '平均分　82.6　保持节奏',
      '缺失数据　3 个字段　补齐后再发送',
    ].join('\n'))).toBe(true);
  });

  it('detects loose numbered text tables as bordered fallback candidates', () => {
    const text = [
      '序号 名称 数量 状态',
      '1 苹果 12 正常',
      '2 香蕉 0 缺货',
      '3 橙子 8 正常',
    ].join('\n');

    expect(hasPlainTextTableLikeBlock(text)).toBe(true);
  });

  it('does not treat prose, key-value blocks, numbered steps, logs, or code as table messages', () => {
    const prose = [
      '部门 负责人 进度',
      '产品 张三 80%',
      '研发 李四 60%',
    ].join('\n');
    const keyValue = [
      'name:   Alice',
      'email:  a@example.com',
      'role:   admin',
    ].join('\n');
    const numberedList = [
      '1.  初始化项目',
      '2.  安装依赖',
      '3.  运行测试',
    ].join('\n');
    const logs = [
      'INFO  2026-07-07  user login',
      'WARN  2026-07-07  retrying',
      'ERROR 2026-07-07  failed',
    ].join('\n');
    const fenced = [
      '```',
      '序号 名称 数量 状态',
      '1 苹果 12 正常',
      '2 香蕉 0 缺货',
      '```',
    ].join('\n');

    expect(hasPlainTextTableLikeBlock(prose)).toBe(false);
    expect(hasPlainTextTableLikeBlock(keyValue)).toBe(false);
    expect(hasPlainTextTableLikeBlock(numberedList)).toBe(false);
    expect(hasPlainTextTableLikeBlock(logs)).toBe(false);
    expect(hasPlainTextTableLikeBlock(fenced)).toBe(false);
  });

  it('does not rewrite plain text tables inside safe markdown rendering', () => {
    const text = [
      '#   文件夹                    项目                    页面',
      '1bg-summary-test             后台子任务回流测试       index + about + notes',
      '2bg-group-test               后台子任务组测试         index + about + notes',
    ].join('\n');

    expect(renderSafeMarkdown(text)).toContain('bg-summary-test');
    expect(renderSafeMarkdown(text)).not.toContain('| # | 文件夹 | 项目 | 页面 |');
  });
});
