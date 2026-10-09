import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { getAuthRevision, setToken } from '../auth-session';
import { marketplaceApi, marketplaceImage, imageFileBase64 } from '../skillhub-marketplace-api';
import { MarketplaceProvider, MarketplaceFilters, mergeMarketplaceLibrary, useMarketplace, useMarketplaceCatalogue } from './skillhub-marketplace-state';
import { IntroductionEditor, IntroImage, MarketplaceIntroduction, RichIntroduction, validIntro } from './skillhub-marketplace-intro';

vi.mock('../skillhub-marketplace-api', () => ({
  marketplaceApi: Object.fromEntries(['capabilities', 'catalogue', 'presentation', 'draft', 'save', 'publish', 'unpublish', 'upload'].map((k) => [k, vi.fn()])),
  marketplaceImage: vi.fn(), imageFileBase64: vi.fn(), marketplaceSession: () => () => true,
}));
const target = { skillId: 'author/shimo', version: '1.0.5' };
const content = { schemaVersion: 1, summary: '读取文档，提取重点', primaryCategory: 'documents', tags: [], scenarios: [], steps: ['提供文档', '说明目标'], requirements: ['有权访问源文档'], limitations: [], details: '', example: { kind: 'document', input: '帮我整理会议记录', output: '摘要与待办' } };
const head = (revision = 0, draft = content) => ({ ...target, revision, draft, published: null });
const deferred = () => { let resolve; let reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };

let root; let container;
const flush = () => act(async () => { await Promise.resolve(); });
const render = async (ui) => { await act(async () => root.render(ui)); };
const button = (label) => [...container.querySelectorAll('button')].find((b) => b.textContent.trim() === label);
const click = async (label) => { const node = button(label); expect(node).toBeTruthy(); await act(async () => node.click()); };
const intro = (props = {}) => <MarketplaceProvider><MarketplaceIntroduction target={target} readOnly={false} {...props} /></MarketplaceProvider>;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.resetAllMocks();
  setToken('test-human');
  marketplaceApi.capabilities.mockResolvedValue({ schemaVersion: 1, enabled: true, writesEnabled: true });
  marketplaceApi.presentation.mockResolvedValue({ presentation: { ...target, content, schemaVersion: 1 } });
  marketplaceApi.draft.mockResolvedValue({ presentation: head() });
  marketplaceImage.mockResolvedValue(new Blob(['webp'], { type: 'image/webp' }));
  URL.createObjectURL = vi.fn(() => 'blob:test'); URL.revokeObjectURL = vi.fn();
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  container = document.createElement('div'); document.body.appendChild(container); root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.useRealTimers(); vi.restoreAllMocks(); setToken(null); });

it('renders concrete escaped input/output, prerequisites and steps without raw HTML', async () => {
  await render(<RichIntroduction content={{ ...content, details: '<img src=x onerror=alert(1)>' }} />);
  expect(container.textContent).toContain('示意 / 非实测');
  expect(container.textContent).toContain('摘要与待办');
  expect(container.textContent).toContain('有权访问源文档');
  expect(container.querySelector('img')).toBeNull();
  expect(container.textContent).toContain('<img src=x onerror=alert(1)>');
});

it('rejects malformed presentation fields without crashing the legacy detail', () => {
  for (const patch of [{ summary: {} }, { requirements: {} }, { scenarios: [null] }, { example: { kind: 'html' } }, { images: [{ assetId: 'https://bad', alt: 'x' }] }, { schemaVersion: 2 }]) expect(validIntro({ ...content, ...patch })).toBe(false);
});

it('does not request introductions or drafts when feature disabled', async () => {
  marketplaceApi.capabilities.mockResolvedValue({ schemaVersion: 1, enabled: false, writesEnabled: true });
  await render(intro());
  expect(marketplaceApi.presentation).not.toHaveBeenCalled(); expect(marketplaceApi.draft).not.toHaveBeenCalled();
});

it('loads the exact installed version and never substitutes latest', async () => {
  await render(intro());
  expect(marketplaceApi.presentation).toHaveBeenCalledWith(target, expect.any(AbortSignal));
  expect(button('编辑此版本介绍')).toBeTruthy();
});

it('keeps friends read-only even when the human happens to be a publisher', async () => {
  await render(intro({ readOnly: true }));
  expect(marketplaceApi.draft).not.toHaveBeenCalled(); expect(button('编辑此版本介绍')).toBeUndefined();
  expect(container.textContent).toContain(content.summary);
});

it('a bot owner cannot edit another publisher after the private read is denied', async () => {
  marketplaceApi.draft.mockRejectedValue({ status: 404 });
  await render(intro());
  expect(button('编辑此版本介绍')).toBeUndefined();
  expect(container.textContent).toContain(content.summary);
});

it('handles null or unavailable introduction without blocking other UI', async () => {
  marketplaceApi.presentation.mockResolvedValue({ presentation: null });
  await render(intro()); expect(container.textContent).toContain('尚未为这个版本');
  await render(null); marketplaceApi.presentation.mockRejectedValue(new Error('offline'));
  await render(intro()); expect(container.textContent).toContain('仍可查看原说明');
});

it('rejects wrong-version public and private responses', async () => {
  marketplaceApi.presentation.mockResolvedValue({ presentation: { ...target, version: '1.0.6', content } });
  marketplaceApi.draft.mockResolvedValue({ presentation: { ...head(), version: '1.0.6' } });
  await render(intro()); expect(container.textContent).not.toContain(content.summary); expect(button('编辑此版本介绍')).toBeUndefined();
});

it('suppresses late data and removes drafts on account change', async () => {
  const old = deferred(); marketplaceApi.draft.mockReturnValueOnce(old.promise);
  await render(intro());
  marketplaceApi.draft.mockRejectedValue({ status: 404 });
  const before = getAuthRevision(); await act(async () => setToken('different-human'));
  expect(getAuthRevision()).toBeGreaterThan(before);
  await act(async () => old.resolve({ presentation: head() }));
  expect(button('编辑此版本介绍')).toBeUndefined();
});

it('saves first, previews server draft and publishes its returned revision once', async () => {
  marketplaceApi.save.mockResolvedValue({ presentation: head(7) });
  marketplaceApi.publish.mockResolvedValue({ presentation: { ...head(8), published: { ...target, content } } });
  await render(<IntroductionEditor target={target} initialHead={head(6)} />);
  expect(button('确认发布介绍')).toBeUndefined();
  await click('保存草稿并预览');
  expect(marketplaceApi.save.mock.calls[0][0].expectedRevision).toBe(6);
  expect(container.textContent).toContain('发布前预览');
  await click('确认发布介绍');
  expect(marketplaceApi.publish).toHaveBeenCalledTimes(1);
  expect(marketplaceApi.publish.mock.calls[0][0]).toEqual({ ...target, expectedRevision: 7 });
  expect(container.textContent).toContain('介绍已发布');
});

it('409 blocks retries and preserves text until explicitly reloaded', async () => {
  marketplaceApi.save.mockRejectedValue({ status: 409 });
  await render(<IntroductionEditor target={target} initialHead={head(3)} />);
  await act(async () => Simulate.change(container.querySelector('textarea'), { target: { value: '尚未保存的内容' } }));
  await click('保存草稿并预览');
  expect(container.textContent).toContain('另一处修改'); expect(container.querySelector('textarea').value).toBe('尚未保存的内容');
  expect(button('保存草稿并预览').disabled).toBe(true); expect(marketplaceApi.save).toHaveBeenCalledTimes(1);
  marketplaceApi.draft.mockResolvedValue({ presentation: head(4) });
  await click('重新读取'); expect(window.confirm).toHaveBeenCalled(); expect(button('保存草稿并预览').disabled).toBe(false);
});

it('unknown write outcome does not automatically retry or claim publication', async () => {
  marketplaceApi.publish.mockRejectedValue(new Error('lost response'));
  await render(<IntroductionEditor target={target} initialHead={head(6)} />);
  await click('预览已保存草稿'); await click('确认发布介绍'); await flush();
  expect(marketplaceApi.publish).toHaveBeenCalledTimes(1); expect(container.textContent).toContain('不要重复提交');
  expect(button('保存草稿并预览').disabled).toBe(true);
});

it('editing a saved draft hides publication until saved and previewed again', async () => {
  await render(<IntroductionEditor target={target} initialHead={head(6)} />);
  await click('预览已保存草稿');
  await act(async () => Simulate.change(container.querySelector('textarea'), { target: { value: 'changed' } }));
  expect(button('确认发布介绍')).toBeUndefined(); expect(button('预览已保存草稿').disabled).toBe(true);
});

it('invalid local image does not lock or discard unsaved content', async () => {
  imageFileBase64.mockRejectedValue(new Error('请选择不超过 2 MiB 的静态图片'));
  await render(<IntroductionEditor target={target} initialHead={head(6)} />);
  await act(async () => Simulate.change(container.querySelector('textarea'), { target: { value: 'keep me' } }));
  await act(async () => Simulate.change(container.querySelector('input[type=file]'), { target: { files: [{ size: 3000000 }], value: '' } }));
  expect(marketplaceApi.upload).not.toHaveBeenCalled(); expect(container.querySelector('textarea').value).toBe('keep me');
  expect(container.querySelector('fieldset').disabled).toBe(false);
});

it('never falls back from public image to private preview, and revokes object URLs', async () => {
  const image = { assetId: `pa_${'a'.repeat(32)}`, alt: '说明' };
  await render(<IntroImage image={image} />);
  expect(marketplaceImage.mock.calls[0][1].preview).toBe(false);
  expect(container.querySelector('img').src).toBe('blob:test');
  await render(null); expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:test');
  marketplaceImage.mockRejectedValue(new Error('404')); await render(<IntroImage image={image} />);
  expect(container.querySelector('img')).toBeNull(); expect(marketplaceImage).toHaveBeenCalledTimes(2);
  expect(marketplaceImage.mock.calls[1][1].preview).toBe(false);
});

function CatalogueHarness({ query = '' }) {
  const market = useMarketplaceCatalogue(query);
  return <><MarketplaceFilters market={market} /><pre>{JSON.stringify(market.state?.skills)}</pre><button onClick={market.more}>more</button></>;
}
it('paginates with name-only filters, renders global category counts, restarts stale cursors', async () => {
  vi.useFakeTimers();
  marketplaceApi.catalogue.mockResolvedValueOnce({ skills: [{ skillId: 'one' }], total: 2, nextCursor: 'next', categoryCounts: [{ category: 'documents', count: 2 }] })
    .mockRejectedValueOnce({ status: 409 })
    .mockResolvedValueOnce({ skills: [{ skillId: 'new' }], total: 1, nextCursor: null });
  await render(<MarketplaceProvider><CatalogueHarness query='read' /></MarketplaceProvider>);
  await act(async () => vi.advanceTimersByTimeAsync(260));
  expect(marketplaceApi.catalogue.mock.calls[0][0]).toMatchObject({ q: 'read', search_mode: 'name', limit: '30' });
  expect(container.textContent).toContain('文档与写作2');
  await click('more'); await act(async () => vi.advanceTimersByTimeAsync(260));
  expect(marketplaceApi.catalogue.mock.calls[1][0].cursor).toBe('next'); expect(marketplaceApi.catalogue.mock.calls[2][0].cursor).toBeUndefined();
  expect(container.querySelector('pre').textContent).toContain('new'); expect(container.querySelector('pre').textContent).not.toContain('one');
});

it('ignores old catalogue responses after filter change', async () => {
  vi.useFakeTimers(); const old = deferred();
  marketplaceApi.catalogue.mockReturnValueOnce(old.promise).mockResolvedValue({ skills: [{ skillId: 'fresh' }], total: 1 });
  await render(<MarketplaceProvider><CatalogueHarness query='old' /></MarketplaceProvider>);
  await act(async () => vi.advanceTimersByTimeAsync(260));
  await render(<MarketplaceProvider><CatalogueHarness query='new' /></MarketplaceProvider>);
  await act(async () => vi.advanceTimersByTimeAsync(260));
  await act(async () => old.resolve({ skills: [{ skillId: 'stale' }], total: 1 }));
  expect(container.querySelector('pre').textContent).toContain('fresh'); expect(container.querySelector('pre').textContent).not.toContain('stale');
});

it('preserves local priority and independent same-name publishers in the full catalogue', () => {
  const local = { skillId: 'local:shimo', cloudSkillId: target.skillId, latestVersion: '1.0.5', isLocalSkill: true, canBind: true };
  const cloud = { skillId: target.skillId, latestVersion: '1.0.6' };
  const other = { skillId: 'other/shimo', latestVersion: '1.0.6' };
  expect(mergeMarketplaceLibrary([local], [cloud, other], '')).toEqual([local, other]);
  expect(mergeMarketplaceLibrary([local], [cloud], 'documents')).toEqual([cloud]);
});

it('a definite validation rejection permits correcting fields without reload', async () => {
  marketplaceApi.save.mockRejectedValue({ status: 400 });
  await render(<IntroductionEditor target={target} initialHead={head(1)} />);
  await click('保存草稿并预览');
  expect(container.querySelector('fieldset').disabled).toBe(false);
  expect(button('保存草稿并预览').disabled).toBe(false);
  expect(marketplaceApi.save).toHaveBeenCalledTimes(1);
});

it('rejects a wrong-version mutation result and never offers to publish it', async () => {
  marketplaceApi.save.mockResolvedValue({ presentation: { ...head(2), version: '2.0.0' } });
  await render(<IntroductionEditor target={target} initialHead={head(1)} />);
  await click('保存草稿并预览');
  expect(button('确认发布介绍')).toBeUndefined(); expect(button('保存草稿并预览').disabled).toBe(true);
});

it('does not double submit while a save is pending', async () => {
  const pending = deferred(); marketplaceApi.save.mockReturnValue(pending.promise);
  await render(<IntroductionEditor target={target} initialHead={head(1)} />);
  await click('保存草稿并预览'); await click('保存草稿并预览');
  expect(marketplaceApi.save).toHaveBeenCalledTimes(1);
  await act(async () => pending.resolve({ presentation: head(2) }));
});

function NavigationHarness({ action }) {
  const { navigate } = useMarketplace();
  return <><button onClick={() => navigate(action)}>切换测试 Bot</button><IntroductionEditor target={target} initialHead={head(1)} /></>;
}
it('asks before switching bots with unsaved introduction and respects cancellation', async () => {
  const navigate = vi.fn(); window.confirm.mockReturnValue(false);
  await render(<MarketplaceProvider><NavigationHarness action={navigate} /></MarketplaceProvider>);
  await act(async () => Simulate.change(container.querySelector('textarea'), { target: { value: 'unsaved' } }));
  await click('切换测试 Bot'); expect(navigate).not.toHaveBeenCalled();
  window.confirm.mockReturnValue(true); await click('切换测试 Bot'); expect(navigate).toHaveBeenCalledTimes(1);
});
