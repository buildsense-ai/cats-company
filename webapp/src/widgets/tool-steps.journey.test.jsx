import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { mkdirSync, writeFileSync } from 'node:fs';
import ChatMessage from './chat-message';

// E2E failure modes declared before code. A tool step answers "what did the
// agent do?" collapsed, and "exactly what was called and returned?" expanded.
// T1 the collapsed row shows the raw tool name, command or JSON instead of the
//    agent's own description (or an action + target when it gave none);
// T2 expanding hides the actual call: raw tool name and every parameter;
// T3 a JSON result stays one escaped string instead of structured fields,
//    or non-JSON text is mangled by a structured view;
// T4 a large or deep result renders everything and stalls the message;
// T5 a step still waiting for its result cannot show its parameters;
// T6 a failed result is not marked as failed.
let container, root;
const evidence = [];
const settle = async () => act(async () => { for (let i = 0; i < 10; i++) await Promise.resolve(); });
const click = async (el) => { expect(el).toBeTruthy(); await act(async () => el.click()); await settle(); };
const render = async (blocks) => {
  await act(async () => root.render(<ChatMessage message={{ id: 9, from_uid: 2, content: '完成。', content_blocks: blocks, created_at: '2026-11-19T00:00:00Z' }}
    isSelf={false} isGroup={false} senderName="小八" senderIsBot showThinking />));
  await settle();
  const process = container.querySelector('.v3-working-toggle[aria-expanded="false"]');
  if (process) await click(process);
};
const use = (id, name, input) => ({ type: 'tool_use', id, name, input });
const result = (id, content, extra = {}) => ({ type: 'tool_result', tool_use_id: id, content, ...extra });
const step = () => container.querySelector('.v3-wpi-tool-step');
const header = () => step().querySelector('.v3-wpi-tool-header');
const details = () => step().querySelector('.v3-wpi-tool-details');

beforeEach(() => { container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
afterAll(() => { mkdirSync('/tmp/catsco-tool-steps', { recursive: true }); writeFileSync('/tmp/catsco-tool-steps/ui-journey.json', JSON.stringify({ evidence }, null, 2)); });

it('T1: the collapsed row leads with the agent description and keeps raw identity out', async () => {
  await render([use('s1', 'execute_shell', { command: 'git status --short && git diff --stat', description: '检查工作区改动' }), result('s1', ' M a.js')]);
  expect(header().querySelector('.v3-wpi-tool-desc').textContent).toBe('检查工作区改动');
  expect(header().querySelector('.v3-wpi-tool-name').textContent).toBe('运行命令');
  expect(header().querySelector('.v3-wpi-tool-name').getAttribute('title')).toBe('execute_shell');
  expect(header().textContent).not.toContain('git status');
  expect(header().textContent).not.toContain('execute_shell');
  expect(details()).toBeNull();
  evidence.push({ journey: 'description first', header: header().textContent });
});

it.each([
  ['read_file', { file_path: 'src/app.js' }, '读取文件', 'src/app.js'],
  ['execute_shell', { command: 'npm test' }, '运行命令', 'npm test'],
  ['spawn_subagent', { task: '审查迁移脚本', context: '很长的上下文' }, '启动子任务', '审查迁移脚本'],
  ['catslog_knowledge_recall', { action: 'search', query: '回滚流程' }, '搜索日记', '回滚流程'],
  ['custom_tool', { anything: 'x' }, 'custom_tool', 'x'],
])('T1: %s without a description falls back to action and target', async (name, input, action, target) => {
  await render([use('s1', name, input), result('s1', 'ok')]);
  expect(header().querySelector('.v3-wpi-tool-desc').textContent).toBe(target);
  expect(header().querySelector('.v3-wpi-tool-name').textContent).toBe(action);
});

it('T2+T3: expanding shows the exact call and a structured JSON result', async () => {
  const payload = { exit_code: 0, stdout: 'Tests passed', timing: { total_ms: 812 }, files: ['a.js', 'b.js'] };
  await render([use('s1', 'execute_shell', { command: 'npm test', timeout: 60000, description: '运行测试' }), result('s1', JSON.stringify(payload))]);
  await click(header());
  expect(header().getAttribute('aria-expanded')).toBe('true');
  const call = details().querySelector('.v3-wpi-call');
  expect(call.querySelector('code.v3-wpi-raw-tool').textContent).toBe('execute_shell');
  const params = Object.fromEntries([...call.querySelectorAll('.v3-wpi-field')].map(row => [row.querySelector('.v3-wpi-field-key').textContent, row.querySelector('.v3-wpi-field-value').textContent]));
  expect(params).toEqual({ command: 'npm test', timeout: '60000', description: '运行测试' });
  const ret = details().querySelector('.v3-wpi-return');
  const keys = [...ret.querySelectorAll(':scope > .v3-wpi-fields > .v3-wpi-field > .v3-wpi-field-key')].map(n => n.textContent);
  expect(keys.slice(0, 4)).toEqual(['exit_code', 'stdout', 'timing', 'files']);
  expect(ret.textContent).toContain('Tests passed');
  expect(ret.textContent).toContain('total_ms');
  expect(ret.textContent).not.toContain('{"exit_code"');
  await click(ret.querySelector('.v3-wpi-raw-toggle'));
  expect(ret.querySelector('pre').textContent).toContain('"exit_code": 0');
  evidence.push({ journey: 'structured call and return', params, keys });
});

it('T3: plain text results stay verbatim text', async () => {
  const text = 'line one\n  {not json}\nline three';
  await render([use('s1', 'execute_shell', { command: 'cat x' }), result('s1', text)]);
  await click(header());
  const ret = details().querySelector('.v3-wpi-return');
  expect(ret.querySelector('.v3-wpi-fields')).toBeNull();
  expect(ret.querySelector('pre').textContent).toBe(text);
});

it('T4: a large or deep result is capped per level with the rest counted', async () => {
  const big = Object.fromEntries(Array.from({ length: 300 }, (_, i) => [`k${i}`, i]));
  const deep = { a: { b: { c: { d: { e: 'bottom' } } } } };
  await render([use('s1', 'glob', { pattern: '**/*' }), result('s1', JSON.stringify({ big, deep, long: 'x'.repeat(20000) }))]);
  await click(header());
  const ret = details().querySelector('.v3-wpi-return');
  expect(ret.querySelectorAll('.v3-wpi-field').length).toBeLessThan(80);
  expect(ret.textContent).toContain('还有 250 项');
  expect(ret.textContent).not.toContain('bottom');
  expect(ret.textContent.length).toBeLessThan(8000);
  evidence.push({ journey: 'capped result', fields: ret.querySelectorAll('.v3-wpi-field').length, chars: ret.textContent.length });
});

it('T5: a running step can still show its parameters', async () => {
  await render([use('s1', 'web_fetch', { url: 'https://example.com/a', prompt: '摘要' })]);
  expect(header().getAttribute('aria-expanded')).toBe('false');
  await click(header());
  expect(details().querySelector('.v3-wpi-call').textContent).toContain('https://example.com/a');
  expect(details().querySelector('.v3-wpi-return').textContent).toContain('等待结果');
});

it('T6: a failed result is marked in the row and in the return section', async () => {
  await render([use('s1', 'execute_shell', { command: 'npm test', description: '运行测试' }), result('s1', 'Error: 2 failed', { is_error: true })]);
  expect(header().querySelector('.v3-wpi-tool-status.is-error').textContent).toBe('失败');
  await click(header());
  expect(details().querySelector('.v3-wpi-return').classList.contains('is-error')).toBe(true);
});
