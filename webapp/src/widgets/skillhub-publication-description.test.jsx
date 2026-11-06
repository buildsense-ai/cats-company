import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { api, getToken, isCurrentAuthSession } from '../api';
import PublicationDescription from './skillhub-publication-description';

vi.mock('../api', () => ({
  getToken: vi.fn(() => 'session'), getAuthRevision: vi.fn(() => 1), isCurrentAuthSession: vi.fn(() => true),
  api: { getSkillHubPublicationCapabilities: vi.fn(), getSkillHubPublicationDescriptionDraft: vi.fn(),
    confirmSkillHubPublicationDescriptionDraft: vi.fn() },
}));
const id = `pubentry_${'a'.repeat(32)}`;
const fixture = (state = 'awaiting_confirmation') => ({ schemaVersion: 1, entryRequestId: id, botUid: '42',
  topicId: 'grp_77', taskId: `pubrepair_${'b'.repeat(32)}`, automaticRepairEnabled: false,
  draft: { confirmationKey: 'c'.repeat(64), round: 1, location: { relativePath: 'SKILL.md', field: 'description', entryIndex: 0 },
    text: '  Read docs.\r\n<script>not HTML</script> ', state } });
const deferred = () => { let resolve; let reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
let root;
let container;
const render = async (entryRequestId = id) => act(async () => root.render(<PublicationDescription entryRequestId={entryRequestId} />));
const click = async text => act(async () => Simulate.click([...container.querySelectorAll('button')].find(b => b.textContent === text)));
const advance = async ms => act(async () => vi.advanceTimersByTimeAsync(ms));
beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.useFakeTimers(); vi.clearAllMocks();
  getToken.mockReturnValue('session'); isCurrentAuthSession.mockReturnValue(true);
  api.getSkillHubPublicationCapabilities.mockResolvedValue({ schemaVersion: 1, descriptionConfirmationUiEnabled: true });
  api.getSkillHubPublicationDescriptionDraft.mockResolvedValue(fixture());
  api.confirmSkillHubPublicationDescriptionDraft.mockResolvedValue(fixture('confirmed'));
  container = document.createElement('div'); document.body.append(container); root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.useRealTimers(); });

it('renders verbatim text safely and confirms only after a human click', async () => {
  await render();
  expect(container.querySelector('pre').textContent).toBe(fixture().draft.text);
  expect(container.querySelector('script')).toBeNull();
  expect(container.textContent).toContain('不是能力库的中文说明书');
  expect(api.confirmSkillHubPublicationDescriptionDraft).not.toHaveBeenCalled();
  await click('确认这段说明');
  expect(api.confirmSkillHubPublicationDescriptionDraft).toHaveBeenCalledWith(id, 'c'.repeat(64),
    expect.objectContaining({ signal: expect.any(AbortSignal) }));
  expect(container.textContent).toContain('此草稿已确认；尚未授权正式发布');
  await advance(120000);
  expect(api.getSkillHubPublicationDescriptionDraft).toHaveBeenCalledTimes(1);
  expect(api.confirmSkillHubPublicationDescriptionDraft).toHaveBeenCalledTimes(1);
});

it.each([{}, { schemaVersion: 1, descriptionConfirmationUiEnabled: false },
  { schemaVersion: 2, descriptionConfirmationUiEnabled: true }])('requires explicit schema-v1 opt-in: %j', async caps => {
  api.getSkillHubPublicationCapabilities.mockResolvedValue(caps);
  await render(); expect(container.textContent).toBe('');
  expect(api.getSkillHubPublicationDescriptionDraft).not.toHaveBeenCalled();
});

it('makes no calls with an invalid entry or missing login', async () => {
  await render('../bad'); getToken.mockReturnValue(null); await render();
  expect(api.getSkillHubPublicationCapabilities).not.toHaveBeenCalled();
});

it('prevents a synchronous double-click from submitting two confirmations', async () => {
  const pending = deferred(); api.confirmSkillHubPublicationDescriptionDraft.mockReturnValue(pending.promise);
  await render();
  const button = [...container.querySelectorAll('button')].find(b => b.textContent === '确认这段说明');
  await act(async () => { Simulate.click(button); Simulate.click(button); });
  expect(api.confirmSkillHubPublicationDescriptionDraft).toHaveBeenCalledTimes(1);
  expect(container.querySelector('button').disabled).toBe(true);
  await act(async () => pending.resolve(fixture('confirmed')));
});

it('recovers a committed approval through GET after a lost POST reply, never auto-replaying POST', async () => {
  api.confirmSkillHubPublicationDescriptionDraft.mockRejectedValue({ message: 'private secret', status: 503 });
  await render(); await click('确认这段说明');
  expect(container.textContent).toContain('尚无法确认草稿是否已确认');
  expect(container.textContent).not.toContain('private secret');
  expect(container.querySelector('pre')).toBeNull();
  await advance(120000);
  expect(api.confirmSkillHubPublicationDescriptionDraft).toHaveBeenCalledTimes(1);
  api.getSkillHubPublicationDescriptionDraft.mockResolvedValue(fixture('confirmed'));
  await click('重新读取草稿');
  expect(container.textContent).toContain('此草稿已确认');
  expect(api.getSkillHubPublicationDescriptionDraft).toHaveBeenCalledTimes(2);
  expect(api.confirmSkillHubPublicationDescriptionDraft).toHaveBeenCalledTimes(1);
});

it.each(['text', 'key', 'topic', 'round', 'location', 'state'])('does not accept a substituted confirmation response: %s', async field => {
  const value = fixture('confirmed');
  if (field === 'text') value.draft.text += ' changed';
  if (field === 'key') value.draft.confirmationKey = 'd'.repeat(64);
  if (field === 'topic') value.topicId = 'grp_88';
  if (field === 'round') value.draft.round = 2;
  if (field === 'location') value.draft.location.entryIndex = 1;
  if (field === 'state') value.draft.state = 'awaiting_confirmation';
  api.confirmSkillHubPublicationDescriptionDraft.mockResolvedValue(value);
  await render(); await click('确认这段说明');
  expect(container.textContent).toContain('尚无法确认草稿是否已确认');
  expect(container.textContent).not.toContain('此草稿已确认');
});

it.each(['publication.repair_not_found', 'publication.description_not_found'])('shows no fabricated draft for %s', async code => {
  api.getSkillHubPublicationDescriptionDraft.mockRejectedValue({ status: 404, data: { error: { code } } });
  await render(); expect(container.textContent).toContain('尚无待确认草稿');
  expect(container.querySelector('pre')).toBeNull();
});

it('reports capability/network failures statically and reprobes only on explicit retry', async () => {
  api.getSkillHubPublicationCapabilities.mockRejectedValueOnce(new Error('private secret'));
  await render(); expect(container.textContent).toContain('暂时无法读取草稿');
  expect(container.textContent).not.toContain('private secret');
  await advance(60000); expect(api.getSkillHubPublicationCapabilities).toHaveBeenCalledTimes(1);
  await click('重新读取草稿'); expect(container.querySelector('pre')).not.toBeNull();
});

it('rejects malformed draft data without exposing arbitrary private response text', async () => {
  api.getSkillHubPublicationDescriptionDraft.mockResolvedValue({ ...fixture(), privateBytes: 'secret' });
  await render(); expect(container.textContent).toContain('暂时无法读取草稿');
  expect(container.textContent).not.toContain('secret');
});

it('aborts and drops a late capability response at the full read deadline', async () => {
  const pending = deferred(); api.getSkillHubPublicationCapabilities.mockReturnValueOnce(pending.promise);
  await render(); const signal = api.getSkillHubPublicationCapabilities.mock.calls[0][0].signal;
  await advance(15000); expect(signal.aborted).toBe(true);
  await act(async () => pending.resolve({ schemaVersion: 1, descriptionConfirmationUiEnabled: true }));
  expect(api.getSkillHubPublicationDescriptionDraft).not.toHaveBeenCalled();
  await click('重新读取草稿'); expect(container.querySelector('pre')).not.toBeNull();
});

it('aborts a stalled confirmation body and ignores late success', async () => {
  const pending = deferred(); api.confirmSkillHubPublicationDescriptionDraft.mockReturnValueOnce(pending.promise);
  await render(); await click('确认这段说明');
  const signal = api.confirmSkillHubPublicationDescriptionDraft.mock.calls[0][2].signal;
  await advance(15000); expect(signal.aborted).toBe(true);
  await act(async () => pending.resolve(fixture('confirmed')));
  expect(container.textContent).toContain('尚无法确认草稿是否已确认');
  expect(container.textContent).not.toContain('此草稿已确认');
});

it.each(['auth', 'entry', 'unmount'])('invalidates pending approval on %s without exposing a late result', async mode => {
  const pending = deferred(); api.confirmSkillHubPublicationDescriptionDraft.mockReturnValueOnce(pending.promise);
  await render(); await click('确认这段说明');
  const signal = api.confirmSkillHubPublicationDescriptionDraft.mock.calls[0][2].signal;
  if (mode === 'auth') await act(async () => { isCurrentAuthSession.mockReturnValue(false); window.dispatchEvent(new Event('cc:auth-changed')); });
  if (mode === 'entry') { api.getSkillHubPublicationCapabilities.mockResolvedValue({ schemaVersion: 1 }); await render(`pubentry_${'d'.repeat(32)}`); }
  if (mode === 'unmount') await act(async () => root.render(null));
  expect(signal.aborted).toBe(true);
  await act(async () => pending.resolve(fixture('confirmed')));
  expect(container.textContent).toBe('');
});
