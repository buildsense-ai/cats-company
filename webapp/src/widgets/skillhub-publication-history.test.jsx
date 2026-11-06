import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { api, getToken, isCurrentAuthSession } from '../api';
import PublicationHistory from './skillhub-publication-history';

vi.mock('../api', () => ({
  getToken: vi.fn(() => 'human-session'), getAuthRevision: vi.fn(() => 1),
  isCurrentAuthSession: vi.fn(() => true),
  api: { getSkillHubPublicationCapabilities: vi.fn(), getSkillHubPublicationDescriptionReleaseHistory: vi.fn() },
}));
const id = `pubentry_${'a'.repeat(32)}`;
const envelope = history => ({ schemaVersion: 1, entryRequestId: id, descriptionReleaseHistory: history });
const terminal = { stage: 'published', recordedAt: '2026-10-29T04:00:00Z',
  result: { skillId: '<script>test</script>', version: '1.0.1', publishedAt: '2026-10-29T04:00:00Z' } };
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
let root;
let container;
const render = async (entryRequestId = id) => act(async () => {
  root.render(<PublicationHistory entryRequestId={entryRequestId} />);
});
const advance = async ms => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const visibility = async value => act(async () => {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value });
  document.dispatchEvent(new Event('visibilitychange'));
});

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers();
  vi.clearAllMocks();
  getToken.mockReturnValue('human-session');
  isCurrentAuthSession.mockReturnValue(true);
  api.getSkillHubPublicationCapabilities.mockResolvedValue({ schemaVersion: 1, descriptionReleaseHistoryReadsEnabled: true });
  api.getSkillHubPublicationDescriptionReleaseHistory.mockResolvedValue(envelope({ stage: 'not_recorded' }));
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.useRealTimers();
});

it('checks capabilities then polls only local unknown history every 30 seconds, stopping at terminal', async () => {
  await render();
  expect(container.textContent).toContain('尚未取得最终发布结果');
  expect(container.textContent).not.toContain('发布中');
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledWith(id,
    expect.objectContaining({ signal: expect.any(AbortSignal) }));
  await advance(29999);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
  api.getSkillHubPublicationDescriptionReleaseHistory.mockResolvedValueOnce(envelope(terminal));
  await advance(1);
  expect(container.textContent).toContain('已发布：<script>test</script> · 1.0.1');
  expect(container.querySelector('script')).toBeNull();
  await advance(120000);
  await visibility('visible');
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(2);
  expect(api.getSkillHubPublicationCapabilities).toHaveBeenCalledTimes(1);
});

it('shows static allowlisted failure evidence and does not republish or poll again', async () => {
  api.getSkillHubPublicationDescriptionReleaseHistory.mockResolvedValue(envelope({ stage: 'publish_failed',
    recordedAt: terminal.recordedAt, code: 'skillhub.publication_description_release_commit_copy_changed' }));
  await render();
  expect(container.textContent).toContain('修复副本发布失败');
  await advance(90000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
});

it.each([{}, { schemaVersion: 1, descriptionReleaseHistoryReadsEnabled: false },
  { schemaVersion: 2, descriptionReleaseHistoryReadsEnabled: true }])('does not read history on unsupported backend: %j', async caps => {
  api.getSkillHubPublicationCapabilities.mockResolvedValue(caps);
  await render();
  await advance(120000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).not.toHaveBeenCalled();
  expect(container.textContent).toBe('');
});

it('does not fetch for missing identity or invalid entry', async () => {
  await render('../invalid');
  getToken.mockReturnValue(null);
  await render();
  expect(api.getSkillHubPublicationCapabilities).not.toHaveBeenCalled();
});

it('shows capability failures without private errors and reprobes only on explicit retry', async () => {
  api.getSkillHubPublicationCapabilities.mockRejectedValueOnce({ status: 503, message: 'private capability secret' });
  await render();
  expect(container.querySelector('[role="alert"]').textContent).toContain('无法确认是否发布成功');
  expect(container.textContent).not.toContain('private capability secret');
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).not.toHaveBeenCalled();
  await advance(120000);
  await visibility('hidden');
  await visibility('visible');
  expect(api.getSkillHubPublicationCapabilities).toHaveBeenCalledTimes(1);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).not.toHaveBeenCalled();
  await act(async () => Simulate.click(container.querySelector('button')));
  expect(api.getSkillHubPublicationCapabilities).toHaveBeenCalledTimes(2);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
  expect(container.textContent).toContain('尚未取得最终发布结果');
});

it('keeps history disabled when an explicit retry finds the capability off', async () => {
  api.getSkillHubPublicationCapabilities.mockRejectedValueOnce(new Error('unavailable'))
    .mockResolvedValueOnce({ schemaVersion: 1, descriptionReleaseHistoryReadsEnabled: false });
  await render();
  expect(container.querySelector('button')).not.toBeNull();
  await act(async () => Simulate.click(container.querySelector('button')));
  expect(container.textContent).toBe('');
  await advance(120000);
  await visibility('visible');
  expect(api.getSkillHubPublicationCapabilities).toHaveBeenCalledTimes(2);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).not.toHaveBeenCalled();
});

it('bounds stalled capability probes, discards late support, and allows a fresh retry', async () => {
  const pending = deferred();
  api.getSkillHubPublicationCapabilities.mockReturnValueOnce(pending.promise);
  await render();
  const signal = api.getSkillHubPublicationCapabilities.mock.calls[0][0].signal;
  await advance(15000);
  expect(signal.aborted).toBe(true);
  expect(container.querySelector('[role="alert"]').textContent).toContain('无法确认是否发布成功');
  await act(async () => pending.resolve({ schemaVersion: 1, descriptionReleaseHistoryReadsEnabled: true }));
  await advance(90000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).not.toHaveBeenCalled();
  await act(async () => Simulate.click(container.querySelector('button')));
  const nextSignal = api.getSkillHubPublicationCapabilities.mock.calls[1][0].signal;
  expect(nextSignal).not.toBe(signal);
  expect(nextSignal.aborted).toBe(false);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
});

it('does not show stale capability failures after authentication invalidation', async () => {
  let reject;
  api.getSkillHubPublicationCapabilities.mockReturnValueOnce(new Promise((_, r) => { reject = r; }));
  await render();
  await act(async () => {
    isCurrentAuthSession.mockReturnValue(false);
    window.dispatchEvent(new Event('cc:auth-changed'));
    reject(new Error('late private failure'));
  });
  expect(container.textContent).toBe('');
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).not.toHaveBeenCalled();
});

it('pauses while hidden, resumes on visibility, and never overlaps slow reads', async () => {
  await visibility('hidden');
  await render();
  expect(api.getSkillHubPublicationCapabilities).not.toHaveBeenCalled();
  const pending = deferred();
  api.getSkillHubPublicationDescriptionReleaseHistory.mockReturnValueOnce(pending.promise);
  await visibility('visible');
  await visibility('visible');
  await advance(10000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
  await act(async () => pending.resolve(envelope({ stage: 'not_recorded' })));
  await act(async () => { await Promise.resolve(); });
  await visibility('hidden');
  await advance(60000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
  await visibility('visible');
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(2);
});

it('bounds a stalled response body, clears old content, and does not auto-retry the failure', async () => {
  const pending = deferred();
  api.getSkillHubPublicationDescriptionReleaseHistory.mockReturnValueOnce(pending.promise);
  await render();
  const signal = api.getSkillHubPublicationDescriptionReleaseHistory.mock.calls[0][1].signal;
  await advance(15000);
  expect(signal.aborted).toBe(true);
  expect(container.textContent).toContain('无法确认是否发布成功');
  await act(async () => pending.resolve(envelope(terminal)));
  expect(container.textContent).not.toContain('已发布：');
  await advance(90000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
});

it.each(['network', 'malformed', 'foreign', 'not-found'])('drops stale evidence and stops on %s until explicit retry', async mode => {
  await render();
  const read = api.getSkillHubPublicationDescriptionReleaseHistory;
  if (mode === 'network' || mode === 'not-found') read.mockRejectedValueOnce({ status: mode === 'not-found' ? 404 : 503, message: 'private secret' });
  if (mode === 'malformed') read.mockResolvedValueOnce(envelope({ ...terminal, code: 'secret' }));
  if (mode === 'foreign') read.mockResolvedValueOnce({ ...envelope(terminal), entryRequestId: 'foreign' });
  await advance(30000);
  expect(container.textContent).toContain('无法确认是否发布成功');
  expect(container.textContent).not.toContain('private secret');
  expect(container.textContent).not.toContain('尚未取得');
  await advance(90000);
  await visibility('visible');
  expect(read).toHaveBeenCalledTimes(2);
  await act(async () => Simulate.click(container.querySelector('button')));
  expect(read).toHaveBeenCalledTimes(3);
  expect(container.textContent).toContain('尚未取得');
});

it('clears on auth event, aborts the read, and discards its late response', async () => {
  const pending = deferred();
  api.getSkillHubPublicationDescriptionReleaseHistory.mockReturnValueOnce(pending.promise);
  await render();
  const signal = api.getSkillHubPublicationDescriptionReleaseHistory.mock.calls[0][1].signal;
  await act(async () => {
    isCurrentAuthSession.mockReturnValue(false);
    window.dispatchEvent(new Event('cc:auth-changed'));
    pending.resolve(envelope(terminal));
  });
  expect(signal.aborted).toBe(true);
  expect(container.textContent).toBe('');
  await advance(60000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(1);
});

it('ignores old entry responses and aborts requests on entry changes and unmount', async () => {
  const pending = deferred();
  api.getSkillHubPublicationDescriptionReleaseHistory.mockReturnValueOnce(pending.promise);
  await render();
  const oldSignal = api.getSkillHubPublicationDescriptionReleaseHistory.mock.calls[0][1].signal;
  await render(`pubentry_${'b'.repeat(32)}`);
  expect(oldSignal.aborted).toBe(true);
  await act(async () => pending.resolve(envelope(terminal)));
  expect(container.textContent).not.toContain('已发布：');
  const signal = api.getSkillHubPublicationDescriptionReleaseHistory.mock.calls[1][1].signal;
  await act(async () => root.unmount());
  expect(signal.aborted).toBe(true);
  await advance(60000);
  expect(api.getSkillHubPublicationDescriptionReleaseHistory).toHaveBeenCalledTimes(2);
  root = createRoot(container);
});
