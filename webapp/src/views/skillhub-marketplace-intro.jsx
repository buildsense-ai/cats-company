import React, { useEffect, useRef, useState } from 'react';
import { ArrowRight, FileText, Image as ImageIcon, MessageSquare, Pencil, ShieldCheck } from 'lucide-react';
import { imageFileBase64, marketplaceApi, marketplaceImage, marketplaceSession } from '../skillhub-marketplace-api';
import { useFeedback } from '../components/feedback-system';
import { MARKET_CATEGORIES, useMarketplace } from './skillhub-marketplace-state';
import '../css/skillhub-marketplace.css';

const ids = MARKET_CATEGORIES.map(([id]) => id);
const plain = (value, max) => typeof value === 'string' && value.length <= max;
const strings = (value, count, max) => value === undefined || (Array.isArray(value) && value.length <= count && value.every((s) => plain(s, max)));
export function validIntro(c) {
  return Boolean(c && c.schemaVersion === 1 && plain(c.summary, 240) && c.summary.trim() && ids.includes(c.primaryCategory)
    && strings(c.tags, 5, 40) && strings(c.steps, 6, 400) && strings(c.requirements, 8, 300) && strings(c.limitations, 8, 300)
    && (c.details === undefined || plain(c.details, 8000))
    && (c.scenarios === undefined || (Array.isArray(c.scenarios) && c.scenarios.length <= 3 && c.scenarios.every((s) => s && plain(s.title, 80) && plain(s.description, 400))))
    && (!c.example || (['document', 'image', 'generic'].includes(c.example.kind) && plain(c.example.input, 2000) && plain(c.example.output, 2000)))
    && (c.images === undefined || (Array.isArray(c.images) && c.images.length <= 6 && c.images.every((i) => i && /^pa_[a-f0-9]{32}$/.test(i.assetId) && plain(i.alt, 240) && i.alt.trim() && (i.caption === undefined || plain(i.caption, 500))))));
}
export const matchesTarget = (value, target) => value?.skillId === target.skillId && value?.version === target.version;
const validHead = (head, target) => matchesTarget(head, target) && Number.isSafeInteger(head.revision) && head.revision >= 0
  && (!head.draft || validIntro(head.draft)) && (!head.published || (matchesTarget(head.published, target) && validIntro(head.published.content)));

export function IntroImage({ image, preview = false }) {
  const [url, setURL] = useState('');
  const [error, setError] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 30000);
    let objectURL = '';
    let active = true;
    setURL(''); setError(false);
    marketplaceImage(image.assetId, { preview, signal: controller.signal }).then((blob) => {
      if (controller.signal.aborted) return;
      objectURL = URL.createObjectURL(blob);
      setURL(objectURL);
    }).catch(() => { if (active) setError(true); });
    return () => { active = false; controller.abort(); window.clearTimeout(timeout); if (objectURL) URL.revokeObjectURL(objectURL); };
  }, [image.assetId, preview]);
  return <figure className='cc-market-figure'>
    {url ? <img src={url} alt={image.alt} /> : <div role='status'>{error ? '图片暂不可用，文字介绍仍可查看。' : '正在读取图片…'}</div>}
    <figcaption>{image.caption || image.alt}<small>作者提供 · 示意 / 非实测</small></figcaption>
  </figure>;
}

export function RichIntroduction({ content, preview = false }) {
  if (!validIntro(content)) return null;
  const { example, images = [], scenarios = [], steps = [], requirements = [], limitations = [] } = content;
  const ResultIcon = example?.kind === 'image' ? ImageIcon : FileText;
  return <div className='cc-market-intro'>
    <div className='cc-market-intro-heading'>
      <small>{MARKET_CATEGORIES.find(([id]) => id === content.primaryCategory)?.[1]}</small>
      <h3>{content.summary}</h3>
      {!!content.tags?.length && <div className='cc-market-tags'>{content.tags.map((tag, i) => <span key={i}>{tag}</span>)}</div>}
    </div>
    {(example || images.length > 0) && <section className='cc-market-example' aria-label='输入与结果示例'>
      <div className='cc-market-section-title'><h3>看懂一个例子</h3><span>示意 / 非实测</span></div>
      {example && <div className={`cc-market-io is-${example.kind}`}>
        <div><MessageSquare size={21} aria-hidden='true' /><small>你提供 / 你对 Bot 说</small><p>{example.input}</p></div>
        <ArrowRight className='cc-market-arrow' size={22} aria-hidden='true' />
        <div><ResultIcon size={23} aria-hidden='true' /><small>期望得到的结果</small><p>{example.output}</p>
          {example.kind === 'image' && !images.length && <small>作者尚未提供效果图；这里是文字说明。</small>}
        </div>
      </div>}
      {images.map((item) => <IntroImage key={item.assetId} image={item} preview={preview} />)}
    </section>}
    {!!requirements.length && <section className='cc-market-requirements'><h3><ShieldCheck size={17} aria-hidden='true' /> 使用前先确认</h3><ul>{requirements.map((s, i) => <li key={i}>{s}</li>)}</ul></section>}
    {!!scenarios.length && <section><h3>什么时候值得用</h3><div className='cc-market-scenarios'>{scenarios.map((s, i) => <article key={i}><small>0{i + 1}</small><h4>{s.title}</h4><p>{s.description}</p></article>)}</div></section>}
    {!!steps.length && <section><h3>如何开始使用</h3><ol className='cc-market-steps'>{steps.map((s, i) => <li key={i}><span>{i + 1}</span><p>{s}</p></li>)}</ol></section>}
    {!!limitations.length && <section><h3>能力边界</h3><ul>{limitations.map((s, i) => <li key={i}>{s}</li>)}</ul></section>}
    {content.details && <section><h3>作者补充</h3><p>{content.details}</p></section>}
    <small className='cc-market-disclosure'>以上为介绍资料，不是效果认证；实际效果取决于 Skill、模型和运行环境。</small>
  </div>;
}

// Mount this component with an exact-target key. No latest-version substitution.
export function MarketplaceIntroduction({ target, readOnly, onEditorState }) {
  const { enabled, writesEnabled } = useMarketplace();
  const [publicInfo, setPublicInfo] = useState(null);
  const [head, setHead] = useState(null);
  const [editing, setEditing] = useState(false);
  const [status, setStatus] = useState('');
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setPublicInfo(null); setHead(null);
    if (!enabled) return undefined;
    setStatus('正在读取图文介绍…');
    marketplaceApi.presentation(target, controller.signal).then(({ presentation }) => {
      if (controller.signal.aborted) return;
      if (presentation && (!matchesTarget(presentation, target) || !validIntro(presentation.content))) throw new Error('无效介绍');
      setPublicInfo(presentation);
      setStatus(presentation ? '' : '作者尚未为这个版本补充图文介绍，不影响添加或使用。');
    }).catch(() => { if (!controller.signal.aborted) setStatus('图文介绍暂不可用，仍可查看原说明并使用原有功能。'); });
    // Only a successful exact-version private read grants an editor entry.
    // A Bot owner / same display name is never sufficient authority.
    if (writesEnabled && !readOnly) marketplaceApi.draft(target, controller.signal).then(({ presentation }) => {
      if (!controller.signal.aborted && validHead(presentation, target)) setHead(presentation);
    }).catch(() => {});
    return () => controller.abort();
  }, [enabled, writesEnabled, readOnly, target.skillId, target.version, reload]);
  if (!enabled) return null;
  if (editing && head && writesEnabled && !readOnly) return <IntroductionEditor key={`${target.skillId}:${target.version}`} target={target} initialHead={head}
    onState={onEditorState} onDone={() => { setEditing(false); setReload((n) => n + 1); }} />;
  return <>
    {publicInfo && <RichIntroduction content={publicInfo.content} />}
    {status && <p className='cc-market-fallback' role='status'>{status}</p>}
    {head && writesEnabled && !readOnly && <button type='button' className='cc-market-edit-entry' onClick={() => setEditing(true)}><Pencil size={14} aria-hidden='true' /> 编辑此版本介绍</button>}
  </>;
}

const emptyContent = () => ({ schemaVersion: 1, summary: '', primaryCategory: 'other', tags: [], scenarios: [], steps: [], requirements: [], limitations: [], details: '' });
const lines = (value) => value.split('\n').map((s) => s.trim()).filter(Boolean);

export function IntroductionEditor({ target, initialHead, onState, onDone }) {
  const feedback = useFeedback();
  const { registerLeaveGuard } = useMarketplace();
  const [head, setHead] = useState(initialHead);
  const [content, setContent] = useState(() => initialHead.draft || initialHead.published?.content || emptyContent());
  const [preview, setPreview] = useState(false);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [reason, setReason] = useState('');
  const pending = useRef(null);
  const lock = useRef(false);
  const alive = useRef(true);
  const dirty = JSON.stringify(content) !== JSON.stringify(head.draft || head.published?.content || emptyContent());
  const leaveState = useRef({ dirty, busy });
  leaveState.current = { dirty, busy };
  useEffect(() => registerLeaveGuard?.(async () => {
    if (leaveState.current.busy) return false;
    if (!leaveState.current.dirty) return true;
    return feedback.confirm({ title: '离开介绍编辑？', message: '尚未保存的修改将丢失。', confirmLabel: '放弃修改并离开' });
  }), [registerLeaveGuard, feedback]);
  useEffect(() => {
    const warn = (event) => { if (leaveState.current.dirty || leaveState.current.busy) { event.preventDefault(); event.returnValue = ''; } };
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, []);
  useEffect(() => { onState?.({ dirty, busy }); }, [dirty, busy, onState]);
  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; pending.current?.abort(); onState?.({ dirty: false, busy: false }); };
  }, [onState]);
  const field = (name, value) => { setContent((c) => ({ ...c, [name]: value })); setPreview(false); setNotice(''); };
  const run = async (action, { uploadOnly = false } = {}) => {
    if (lock.current) return;
    lock.current = true; setBusy(true); setError(''); setNotice('');
    const controller = new AbortController(); pending.current = controller;
    const current = marketplaceSession();
    try { await action(controller.signal, () => alive.current && !controller.signal.aborted && current()); }
    catch (err) {
      if (alive.current && current()) {
        const rejected = [400, 413, 415, 422, 429].includes(err.status);
        setUncertain(!uploadOnly && !rejected); setPreview(false);
        setError(uploadOnly ? '图片未能确认上传成功；未保存的介绍已保留。请检查格式、大小或稍后重试。'
          : rejected ? '提交未被接受，请检查字段长度、图片说明及管理员修改原因；操作过于频繁时请稍后再试。'
            : err.status === 409 ? '介绍已被另一处修改。请重新读取，确认后再编辑；不会自动覆盖。'
              : '操作未能确认成功。请先重新读取服务器状态，不要重复提交。');
      }
    } finally { lock.current = false; if (alive.current && current()) setBusy(false); }
  };
  const applyHead = (value) => {
    if (!validHead(value, target)) throw new Error('无效介绍响应');
    setHead(value); setContent(value.draft || value.published?.content || emptyContent()); setUncertain(false);
  };
  const body = () => ({ ...target, expectedRevision: head.revision, ...(reason.trim() ? { reason: reason.trim() } : {}) });
  const save = () => run(async (signal, current) => {
    const normalized = { ...content };
    ['tags', 'steps', 'requirements', 'limitations'].forEach((name) => { normalized[name] = lines((content[name] || []).join('\n')); });
    const result = await marketplaceApi.save({ ...body(), content: normalized }, signal);
    if (!current()) return;
    applyHead(result.presentation); setPreview(true); setNotice('草稿已保存，尚未公开。请预览后确认发布。');
  });
  const publish = () => run(async (signal, current) => {
    const result = await marketplaceApi.publish(body(), signal);
    if (!current()) return;
    applyHead(result.presentation); setPreview(false); setNotice('介绍已发布；Skill 包、版本和 Agent 配置均未改变。');
  });
  const reload = async () => {
    if (busy) return;
    if (dirty && !await feedback.confirm({ title: '重新读取介绍？', message: '这会丢弃本页未保存的编辑，读取服务器当前草稿。', confirmLabel: '重新读取' })) return;
    if (!alive.current) return;
    run(async (signal, current) => { const result = await marketplaceApi.draft(target, signal); if (current()) { applyHead(result.presentation); setPreview(false); } });
  };
  const unpublish = async () => {
    if (!await feedback.confirm({ title: '撤回公开介绍？', message: '仅撤回图文介绍，保留草稿和历史图片；不会下架 Skill，也不会移除 Agent 的能力。', confirmLabel: '撤回介绍' })) return;
    if (!alive.current) return;
    run(async (signal, current) => { const result = await marketplaceApi.unpublish(body(), signal); if (current()) { applyHead(result.presentation); setPreview(false); setNotice('公开介绍已撤回，Skill 仍可正常使用。'); } });
  };
  const upload = async (file) => {
    // File validation is not a server mutation; never lock out unsaved text
    // simply because the user picked an unsupported or oversized file.
    let dataBase64;
    const session = marketplaceSession();
    try { dataBase64 = await imageFileBase64(file); }
    catch (err) { if (alive.current && session()) setError(err.message); return; }
    if (!alive.current || !session()) return;
    run(async (signal, current) => {
    const result = await marketplaceApi.upload({ ...target, dataBase64, ...(reason.trim() ? { reason: reason.trim() } : {}) }, signal);
    if (current()) {
      if (!/^pa_[a-f0-9]{32}$/.test(result.asset?.id)) throw new Error('无效图片响应');
      setContent((c) => ({ ...c, images: [...(c.images || []).filter((i) => i.assetId !== result.asset.id), { assetId: result.asset.id, alt: 'Skill 使用示意', caption: '' }] }));
      setPreview(false); setNotice('图片已上传，尚未保存到介绍。请补充图片说明，再保存草稿。');
    }
    }, { uploadOnly: true });
  };
  return <section className='cc-market-editor' aria-label='编辑版本介绍'>
    <h3>让大家看懂这个 Skill</h3><p>仅编辑 {target.version} 的介绍，不改动 Skill 文件。选填内容可稍后补充。</p>
    {error && <p role='alert'>{error}</p>}{notice && <p role='status'>{notice}</p>}
    <fieldset disabled={busy || uncertain}>
      <label>一句话用途<textarea maxLength={240} value={content.summary} onChange={(e) => field('summary', e.target.value)} /></label>
      <label>用途分类<select value={content.primaryCategory} onChange={(e) => field('primaryCategory', e.target.value)}>{MARKET_CATEGORIES.map(([id, label]) => <option key={id} value={id}>{label}</option>)}</select></label>
      <details><summary>补充示例、使用方法与图片（选填）</summary>
        <label>标签（每行一个，最多 5 个）<textarea value={(content.tags || []).join('\n')} onChange={(e) => field('tags', e.target.value.split('\n'))} /></label>
        <label>示例类型<select value={content.example?.kind || ''} onChange={(e) => {
          const kind = e.target.value;
          const next = { ...content }; if (kind) next.example = { input: '', output: '', ...next.example, kind }; else delete next.example;
          setContent(next); setPreview(false);
        }}><option value=''>暂不添加示例</option><option value='document'>文档 / 信息整理</option><option value='image'>图像 / 视觉</option><option value='generic'>通用输入与结果</option></select></label>
        {content.example && <><label>示例输入<textarea maxLength={2000} value={content.example.input} onChange={(e) => field('example', { ...content.example, input: e.target.value })} /></label><label>示例结果（示意 / 非实测）<textarea maxLength={2000} value={content.example.output} onChange={(e) => field('example', { ...content.example, output: e.target.value })} /></label></>}
        {(content.scenarios || []).map((s, i) => <div className='cc-market-scenario-edit' key={i}>
          <label>场景 {i + 1} 标题<input maxLength={80} value={s.title} onChange={(e) => field('scenarios', content.scenarios.map((v, n) => n === i ? { ...v, title: e.target.value } : v))} /></label>
          <label>场景 {i + 1} 好处<textarea maxLength={400} value={s.description} onChange={(e) => field('scenarios', content.scenarios.map((v, n) => n === i ? { ...v, description: e.target.value } : v))} /></label>
          <button type='button' onClick={() => field('scenarios', content.scenarios.filter((_, n) => n !== i))}>移除此场景</button>
        </div>)}
        {(content.scenarios || []).length < 3 && <button type='button' onClick={() => field('scenarios', [...(content.scenarios || []), { title: '', description: '' }])}>补充适用场景</button>}
        {[['steps', '使用步骤（最多 6 行，每行 400 字）'], ['requirements', '使用前提（最多 8 行，每行 300 字）'], ['limitations', '限制与注意事项（最多 8 行，每行 300 字）']].map(([name, label]) => <label key={name}>{label}<textarea value={(content[name] || []).join('\n')} onChange={(e) => field(name, e.target.value.split('\n'))} /></label>)}
        <label>作者补充<textarea maxLength={8000} value={content.details || ''} onChange={(e) => field('details', e.target.value)} /></label>
        {(content.images || []).map((item, i) => <div key={item.assetId} className='cc-market-image-edit'>
          <IntroImage image={item} preview />
          <label>图片 {i + 1} 内容说明（必填）<input maxLength={240} value={item.alt} onChange={(e) => field('images', content.images.map((v, n) => n === i ? { ...v, alt: e.target.value } : v))} /></label>
          <label>图片 {i + 1} 补充说明<input maxLength={500} value={item.caption || ''} onChange={(e) => field('images', content.images.map((v, n) => n === i ? { ...v, caption: e.target.value } : v))} /></label>
          <button type='button' onClick={() => field('images', content.images.filter((_, n) => n !== i))}>从本草稿移除图片</button>
        </div>)}
        <label>添加图片（最多 6 张，每张 2 MiB）<input type='file' accept='image/png,image/jpeg,image/webp' disabled={(content.images || []).length >= 6} onChange={(e) => { const file = e.target.files?.[0]; e.target.value = ''; if (file) upload(file); }} /></label>
        <small>图片上传后仍需保存和确认发布。历史图片保留，移除引用不等于删除文件。不支持外部图片链接或 HTML。</small>
      </details>
      <details><summary>管理员代编辑说明（普通发布者无需填写）</summary><label>修改原因<input maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} /></label></details>
    </fieldset>
    <div className='cc-market-editor-actions'>
      <button type='button' disabled={busy || uncertain || !validIntro(content)} onClick={save}>保存草稿并预览</button>
      <button type='button' disabled={busy || uncertain || dirty || !head.draft} onClick={() => setPreview(true)}>预览已保存草稿</button>
      {preview && <button type='button' className='primary' disabled={busy || uncertain || dirty || !head.draft} onClick={publish}>确认发布介绍</button>}
      {head.published && <button type='button' disabled={busy || uncertain || dirty} onClick={unpublish}>撤回公开介绍</button>}
      <button type='button' disabled={busy} onClick={reload}>重新读取</button>
      <button type='button' disabled={busy} onClick={async () => {
        if (dirty && !await feedback.confirm({ title: '离开编辑？', message: '未保存的修改将丢失。', confirmLabel: '放弃修改' })) return;
        if (alive.current) onDone();
      }}>返回介绍</button>
    </div>
    {!validIntro(content) && <p>请填写一句话用途，并检查选填内容的数量和长度；图片说明不能为空。</p>}
    {preview && head.draft && <div className='cc-market-draft-preview'><h3>发布前预览 · 尚未确认发布</h3><RichIntroduction content={head.draft} preview /></div>}
  </section>;
}
