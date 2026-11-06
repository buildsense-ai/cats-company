import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { api, getToken, isCurrentAuthSession } from '../api';
import PublicationRepairAdmission from './skillhub-publication-repair-admission';

vi.mock('../api', () => ({
  getToken: vi.fn(() => 'session'), getAuthRevision: vi.fn(() => 1), isCurrentAuthSession: vi.fn(() => true),
  api: { getSkillHubPublicationCapabilities: vi.fn(), getSkillHubPublicationRepairTask: vi.fn(),
    admitSkillHubPublicationRepairTask: vi.fn() },
}));
const id = `pubentry_${'a'.repeat(32)}`;
const fixture = (state = 'queued', roundsUsed = 0) => ({ schemaVersion: 1, entryRequestId: id, botUid: '42',
  topicId: 'grp_77', taskId: `pubrepair_${'b'.repeat(32)}`, state, roundsUsed,
  maxFormatRepairRounds: 3, automaticRepairEnabled: false });
const absent = () => ({ status: 404, data: { error: { code: 'publication.repair_not_found' } } });
const deferred = () => { let resolve; let reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
let root;
let container;
const render = async (props = {}) => act(async () => root.render(
  <PublicationRepairAdmission entryRequestId={id} botUid='42' topicId='grp_77' {...props} />,
));
const button = text => [...container.querySelectorAll('button')].find(b => b.textContent === text);
const click = async text => act(async () => Simulate.click(button(text)));
const advance = async ms => act(async () => vi.advanceTimersByTimeAsync(ms));
beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers(); vi.clearAllMocks(); getToken.mockReturnValue('session'); isCurrentAuthSession.mockReturnValue(true);
  api.getSkillHubPublicationCapabilities.mockResolvedValue({ schemaVersion: 1, repairTaskAdmissionUiEnabled: true });
  api.getSkillHubPublicationRepairTask.mockRejectedValue(absent());
  api.admitSkillHubPublicationRepairTask.mockResolvedValue(fixture());
  container = document.createElement('div'); document.body.append(container); root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.useRealTimers(); });

it('reads first and only registers after an explicit human click with exact topic', async () => {
  await render();
  expect(container.textContent).toContain('尚未登记修复任务');
  expect(container.textContent).toContain('不会启动模型、修改 Skill 或授权发布');
  expect(api.getSkillHubPublicationRepairTask).toHaveBeenCalledWith(id, expect.objectContaining({ signal: expect.any(AbortSignal) }));
  expect(api.admitSkillHubPublicationRepairTask).not.toHaveBeenCalled();
  await click('登记到当前修复对话');
  expect(api.admitSkillHubPublicationRepairTask).toHaveBeenCalledWith(id, 'grp_77', expect.objectContaining({ signal: expect.any(AbortSignal) }));
  expect(container.textContent).toContain('已关联当前修复对话');
  expect(container.textContent).toContain('已使用 0 / 3 轮');
  expect(button('登记到当前修复对话')).toBeUndefined();
  await advance(120000);
  expect(api.getSkillHubPublicationRepairTask).toHaveBeenCalledTimes(1);
  expect(api.admitSkillHubPublicationRepairTask).toHaveBeenCalledTimes(1);
});

it.each([{}, { schemaVersion: 1, repairTaskAdmissionUiEnabled: false },
  { schemaVersion: 2, repairTaskAdmissionUiEnabled: true }])('requires explicit schema-v1 capability: %j', async caps => {
  api.getSkillHubPublicationCapabilities.mockResolvedValue(caps);
  await render(); expect(container.textContent).toBe('');
  expect(api.getSkillHubPublicationRepairTask).not.toHaveBeenCalled();
  expect(api.admitSkillHubPublicationRepairTask).not.toHaveBeenCalled();
});

it.each([{ entryRequestId: '../bad' }, { botUid: 42 }, { botUid: '43/other' }, { topicId: 'grp_077' },
  { topicId: undefined }, { topicId: 'grp_9223372036854775808' }])('does not probe for an invalid/missing private conversation: %j', async props => {
  await render(props);
  expect(api.getSkillHubPublicationCapabilities).not.toHaveBeenCalled();
});
it('does not probe without login', async () => {
  getToken.mockReturnValue(null); await render();
  expect(api.getSkillHubPublicationCapabilities).not.toHaveBeenCalled();
});

it('recovers the same existing task and spent budget without offering a new registration', async () => {
  api.getSkillHubPublicationRepairTask.mockResolvedValue(fixture('waiting_confirmation', 2));
  await render();
  expect(container.textContent).toContain('已使用 2 / 3 轮');
  expect(container.textContent).toContain('已停在需要确认');
  expect(button('登记到当前修复对话')).toBeUndefined();
  expect(api.admitSkillHubPublicationRepairTask).not.toHaveBeenCalled();
});

it('does not rebind a task already retained for a different topic', async () => {
  api.getSkillHubPublicationRepairTask.mockResolvedValue({ ...fixture('stopped_budget', 3), topicId: 'grp_88' });
  await render();
  expect(container.textContent).toContain('已关联另一个修复对话');
  expect(container.textContent).toContain('已使用 3 / 3 轮');
  expect(button('登记到当前修复对话')).toBeUndefined();
  await click('重新读取修复任务');
  expect(api.admitSkillHubPublicationRepairTask).not.toHaveBeenCalled();
});

it('prevents same-turn double clicks and offers no parallel read while registration is pending', async () => {
  const pending = deferred(); api.admitSkillHubPublicationRepairTask.mockReturnValueOnce(pending.promise);
  await render(); const action = button('登记到当前修复对话');
  await act(async () => { Simulate.click(action); Simulate.click(action); });
  expect(api.admitSkillHubPublicationRepairTask).toHaveBeenCalledTimes(1);
  expect(container.textContent).toContain('正在复核报告并登记');
  expect(container.querySelector('button')).toBeNull();
  await act(async () => pending.resolve(fixture()));
  expect(container.textContent).toContain('已关联当前修复对话');
});

it('recovers a lost POST response only through GET, preserving an already-spent round', async () => {
  api.admitSkillHubPublicationRepairTask.mockRejectedValueOnce({ status: 503, message: 'private secret' });
  await render(); await click('登记到当前修复对话');
  expect(container.textContent).toContain('登记结果尚无法确认');
  expect(container.textContent).not.toContain('private secret');
  expect(button('登记到当前修复对话')).toBeUndefined();
  await advance(120000);
  expect(api.admitSkillHubPublicationRepairTask).toHaveBeenCalledTimes(1);
  api.getSkillHubPublicationRepairTask.mockResolvedValue(fixture('running', 1));
  await click('重新读取修复任务');
  expect(container.textContent).toContain('已使用 1 / 3 轮');
  expect(container.textContent).toContain('不代表执行器当前仍在运行');
  expect(api.admitSkillHubPublicationRepairTask).toHaveBeenCalledTimes(1);
});

it('does not assume an idempotent POST replay must return a zero-round new task', async () => {
  api.admitSkillHubPublicationRepairTask.mockResolvedValue(fixture('validated', 2));
  await render(); await click('登记到当前修复对话');
  expect(container.textContent).toContain('已使用 2 / 3 轮');
  expect(container.textContent).toContain('校验已通过，不代表已发布');
});

it.each([
  { status: 404, data: { error: { code: 'publication.not_found' } } },
  { status: 403, data: { error: { code: 'publication.access_denied' } } },
  { status: 404, data: { error: { code: 'publication.description_not_found' } } },
  new Error('private network error'),
])('never treats generic 404, denied authority or network failure as no task: %j', async error => {
  api.getSkillHubPublicationRepairTask.mockRejectedValue(error);
  await render();
  expect(container.textContent).toContain('暂时无法读取修复任务');
  expect(button('登记到当前修复对话')).toBeUndefined();
  expect(api.admitSkillHubPublicationRepairTask).not.toHaveBeenCalled();
});

it('reprobes capability only on explicit retry and respects a disabled gate then', async () => {
  api.getSkillHubPublicationCapabilities.mockRejectedValueOnce(new Error('private capability secret'));
  await render(); expect(container.textContent).toContain('暂时无法读取修复任务');
  expect(container.textContent).not.toContain('private capability secret');
  await advance(60000); expect(api.getSkillHubPublicationCapabilities).toHaveBeenCalledTimes(1);
  api.getSkillHubPublicationCapabilities.mockResolvedValue({ schemaVersion: 1, repairTaskAdmissionUiEnabled: false });
  await click('重新读取修复任务');
  expect(container.textContent).toBe('');
  expect(api.getSkillHubPublicationRepairTask).not.toHaveBeenCalled();
});

it.each(['entry', 'bot', 'private', 'topic', 'budget'])('refuses substituted POST projection: %s', async field => {
  const task = fixture();
  if (field === 'entry') task.entryRequestId = `pubentry_${'d'.repeat(32)}`;
  if (field === 'bot') task.botUid = '43';
  if (field === 'private') task.receipt = '<script>secret</script>';
  if (field === 'topic') task.topicId = 'grp_88';
  if (field === 'budget') task.maxFormatRepairRounds = 4;
  api.admitSkillHubPublicationRepairTask.mockResolvedValueOnce(task);
  await render(); await click('登记到当前修复对话');
  expect(container.textContent).toContain('登记结果尚无法确认');
  expect(container.textContent).not.toContain('已关联当前修复对话');
  expect(container.textContent).not.toContain('secret');
});

it('refuses a wrong-Bot GET rather than showing another ledger', async () => {
  api.getSkillHubPublicationRepairTask.mockResolvedValue({ ...fixture(), botUid: '43' });
  await render(); expect(container.textContent).toContain('暂时无法读取修复任务');
  expect(button('登记到当前修复对话')).toBeUndefined();
});

it.each(['capability', 'read'])('bounds the full %s JSON read to 15 seconds and drops late responses', async mode => {
  const pending = deferred();
  if (mode === 'capability') api.getSkillHubPublicationCapabilities.mockReturnValueOnce(pending.promise);
  else api.getSkillHubPublicationRepairTask.mockReturnValueOnce(pending.promise);
  await render(); const signal = api.getSkillHubPublicationCapabilities.mock.calls[0][0].signal;
  await advance(15000); expect(signal.aborted).toBe(true);
  await act(async () => pending.resolve(mode === 'capability' ? { schemaVersion: 1, repairTaskAdmissionUiEnabled: true } : fixture()));
  expect(container.textContent).toContain('暂时无法读取修复任务');
  expect(container.textContent).not.toContain('已关联当前修复对话');
});

it('keeps admission alive past 15 seconds, bounds the complete POST body to 65 seconds, and drops late success', async () => {
  const pending = deferred(); api.admitSkillHubPublicationRepairTask.mockReturnValueOnce(pending.promise);
  await render(); await click('登记到当前修复对话');
  const signal = api.admitSkillHubPublicationRepairTask.mock.calls[0][2].signal;
  await advance(55000); expect(signal.aborted).toBe(false);
  await advance(10000); expect(signal.aborted).toBe(true);
  await act(async () => pending.resolve(fixture()));
  expect(container.textContent).toContain('登记结果尚无法确认');
  expect(container.textContent).not.toContain('已关联当前修复对话');
});

it.each(['auth', 'entry', 'bot', 'topic', 'unmount'])('aborts pending admission and hides stale results on %s change', async mode => {
  const pending = deferred(); api.admitSkillHubPublicationRepairTask.mockReturnValueOnce(pending.promise);
  await render(); await click('登记到当前修复对话');
  const signal = api.admitSkillHubPublicationRepairTask.mock.calls[0][2].signal;
  api.getSkillHubPublicationCapabilities.mockResolvedValue({ schemaVersion: 1 });
  if (mode === 'auth') await act(async () => { isCurrentAuthSession.mockReturnValue(false); window.dispatchEvent(new Event('cc:auth-changed')); });
  if (mode === 'entry') await render({ entryRequestId: `pubentry_${'d'.repeat(32)}` });
  if (mode === 'bot') await render({ botUid: '43' });
  if (mode === 'topic') await render({ topicId: 'grp_88' });
  if (mode === 'unmount') await act(async () => root.render(null));
  expect(signal.aborted).toBe(true);
  await act(async () => pending.resolve(fixture()));
  expect(container.textContent).toBe('');
  expect(api.admitSkillHubPublicationRepairTask).toHaveBeenCalledTimes(1);
});
