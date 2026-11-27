import React, { createContext, useContext, useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { ChevronDown, LoaderCircle, WandSparkles } from 'lucide-react';
import { api, getAuthRevision } from '../api';
import Avatar from './avatar';
import { personaFields } from '../utils/agent-persona';
import { readPortraitHistory, storePortrait } from '../utils/persona-portrait-history';

export const PortraitSampleContext = createContext('');
const directions = ['自由发挥', '人物', '动物'];
const visualFields = personaFields.person.filter(field => field.title !== '怎么称呼我');

function portraitPrompt(description, direction, note, editing) {
  const traits = description.split(/\r?\n/).filter(line =>
    visualFields.some(({ title }) => line.startsWith(title + '：') || line.startsWith(title + '（自定义）：')));
  return [
    editing ? '根据参考头像和下方调整要求修改。保留未提及的面部特征、画风、配色和构图。'
      : '为一个虚拟助手设计原创聊天头像。正方形肩部以上特写，主体在中心，背景简洁，在32像素仍然清晰。无文字、无边框。',
    direction === '自由发挥' ? '形象类型由你选择。' : '形象类型：' + direction,
    ...traits,
    note.trim() && '本次要求：' + note.trim(),
  ].filter(Boolean).join('\n');
}

// Normalize any supported provider output to a small, square JPEG for both edits and uploads.
async function normalizePortrait(source) {
  const image = new Image();
  image.src = source;
  await image.decode();
  const canvas = document.createElement('canvas');
  canvas.width = canvas.height = 512;
  const ctx = canvas.getContext('2d');
  ctx.fillStyle = '#ffffff';
  ctx.fillRect(0, 0, 512, 512);
  const side = Math.min(image.naturalWidth, image.naturalHeight);
  ctx.drawImage(image, (image.naturalWidth - side) / 2, (image.naturalHeight - side) / 2, side, side, 0, 0, 512, 512);
  return canvas.toDataURL('image/jpeg', 0.9);
}

export default function AgentPersonaPortrait({ agent, ownerUID, description, hidden, saving, onApplied, onStateChange }) {
  const sample = useContext(PortraitSampleContext);
  const id = useId();
  const [open, setOpen] = useState(false);
  const [direction, setDirection] = useState(directions[0]);
  const [note, setNote] = useState('');
  const [candidate, setCandidate] = useState(null);
  const [history, setHistory] = useState([]);
  const [historyError, setHistoryError] = useState('');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const pending = useRef(null);
  const uid = agent.uid || agent.id;
  const scope = ownerUID && uid ? JSON.stringify([sample ? 'preview' : 'account', String(ownerUID), String(uid)]) : '';
  const name = agent.newDisplayName || agent.display_name || agent.username || '助手';
  const avatar = agent.newAvatarUrl || agent.avatar_url;
  const editing = candidate?.direction === direction;
  const waiting = ['generate', 'refine', 'sample'].includes(busy);
  const waitingLabel = busy === 'sample' ? '正在读取示例图…' : busy === 'refine' ? '正在微调形象…' : '正在生成形象…';
  const portraitState = busy === 'apply' ? 'applying' : waiting ? 'generating'
    : (candidate && !candidate.applied && !history.some(item => item.id === candidate.id)) || note.trim() ? 'draft' : '';
  useLayoutEffect(() => { onStateChange?.(portraitState); }, [onStateChange, portraitState]);
  useEffect(() => () => { pending.current?.abort(); }, [uid]);
  useEffect(() => {
    if (!open || !scope) return;
    let active = true;
    readPortraitHistory(scope).then(rows => {
      if (active) setHistory(current => [...current, ...rows.filter(row => !current.some(item => item.id === row.id))]);
    }).catch(() => { if (active) setHistoryError('未能读取此浏览器的形象历史。'); });
    return () => { active = false; };
  }, [open, scope]);

  function cancel() {
    pending.current?.abort();
    pending.current = null;
    setBusy('');
  }

  async function generate(useSample = false) {
    if (pending.current) return;
    const controller = new AbortController();
    const revision = getAuthRevision();
    pending.current = controller;
    const refine = !useSample && editing && Boolean(note.trim());
    setBusy(useSample ? 'sample' : refine ? 'refine' : 'generate');
    setError('');
    try {
      let source = sample;
      if (!useSample) {
        const result = await api.generateAssistantPortrait(portraitPrompt(description, direction, note, refine),
          refine ? candidate.image : null, controller.signal);
        const bytes = result?.data?.find(item => item.b64_json)?.b64_json;
        if (!bytes) throw new Error('没有收到图片，请重试。');
        source = 'data:image/jpeg;base64,' + bytes;
      }
      const image = await normalizePortrait(source);
      if (controller.signal.aborted || revision !== getAuthRevision()) return;
      if (useSample) setDirection('动物');
      const next = { id: crypto.randomUUID(), created: Date.now(), image, direction: useSample ? '动物' : direction, sample: useSample };
      setCandidate(next);
      setNote('');
      if (!useSample) {
        try {
          if (!scope) throw new Error();
          await storePortrait(scope, next);
          if (controller.signal.aborted || revision !== getAuthRevision()) return;
          setHistory(current => [next, ...current.filter(item => item.id !== next.id)]);
          setHistoryError('');
        } catch {
          if (!controller.signal.aborted) setHistoryError('未能保存到此浏览器。当前图片仍可用作头像。');
        }
      }
    } catch (err) {
      if (!controller.signal.aborted) setError(err.status === 401 ? '请登录后生成自己的形象。' : err.message || '生成失败，请重试。');
    } finally {
      if (pending.current === controller) { pending.current = null; setBusy(''); }
    }
  }

  async function apply() {
    if (!candidate || pending.current) return;
    const controller = new AbortController();
    const revision = getAuthRevision();
    pending.current = controller;
    setBusy('apply');
    setError('');
    try {
      let url = candidate.uploaded;
      if (!url) {
        const blob = await (await fetch(candidate.image)).blob();
        if (controller.signal.aborted || getAuthRevision() !== revision) return;
        const uploaded = await api.uploadFile(new File([blob], 'assistant-avatar.jpg', { type: 'image/jpeg' }), 'image');
        url = uploaded?.url;
        if (!url) throw new Error('头像上传失败，请重试。');
        if (controller.signal.aborted || getAuthRevision() !== revision) return;
        setCandidate({ ...candidate, uploaded: url });
      }
      await api.updateBot(uid, { avatar_url: url });
      if (getAuthRevision() !== revision) return;
      onApplied?.(uid, url);
      if (!controller.signal.aborted) setCandidate({ ...candidate, uploaded: url, applied: true });
      if (scope && !candidate.sample) {
        const item = { ...candidate, uploaded: url };
        delete item.applied;
        try {
          await storePortrait(scope, item);
          if (!controller.signal.aborted) setHistory(current => [item, ...current.filter(row => row.id !== item.id)].sort((a, b) => b.created - a.created));
        } catch {
          if (!controller.signal.aborted) setHistoryError('头像已更新，但未能更新此浏览器的形象历史。');
        }
      }
    } catch (err) {
      if (!controller.signal.aborted) setError(err.message || '头像保存失败，请重试。');
    } finally {
      if (pending.current === controller) { pending.current = null; setBusy(''); }
    }
  }

  return <section className="cc-persona-portrait" hidden={hidden} aria-label="助手形象">
    <div className="cc-portrait-header">
      <Avatar name={name} src={avatar} size={42} />
      <strong>助手形象</strong>
      <button type="button" className="oc-btn oc-btn-default" aria-expanded={open} aria-controls={id}
        onClick={() => setOpen(!open)}>{open ? <ChevronDown size={15} /> : waiting ? <LoaderCircle className="cc-spin" size={15} /> : <WandSparkles size={15} />}{open ? '收起' : waiting ? waitingLabel : '生成形象'}</button>
    </div>
    {open && <div id={id} className="cc-portrait-editor">
      <fieldset className="cc-portrait-directions" disabled={Boolean(busy) || saving}>
        <legend>形象类型</legend>
        {directions.map(value => <label key={value}><input type="radio" name={id} checked={value === direction}
          onChange={() => { setDirection(value); setError(''); }} />{value}</label>)}
      </fieldset>
      {(candidate || waiting) && <figure className="cc-portrait-preview">
        <div className="cc-portrait-frame" aria-busy={waiting}>
          {candidate && <img width="132" height="132" src={candidate.image} alt="候选头像大图" />}
          {waiting && <span className="cc-portrait-loader" aria-hidden="true"><LoaderCircle className="cc-spin" size={20} /></span>}
        </div>
        <figcaption><small role="status">{waiting ? waitingLabel : candidate.sample ? '示例图 · 聊天效果' : '聊天效果'}</small>
          {candidate && <span><img width="32" height="32" src={candidate.image} alt="" />{name}</span>}</figcaption>
      </figure>}
      <label className="cc-portrait-note">{editing ? '想调整哪里？' : '补充想法（选填）'}
        <textarea rows={2} maxLength={500} value={note} disabled={Boolean(busy) || saving}
          placeholder={editing ? '比如：微微笑，把相机拿在手里' : '比如：穿深蓝色工装，肩上挂一台相机'} onChange={event => setNote(event.target.value)} />
      </label>
      {error && <p role="alert">{error}</p>}
      <div className="cc-portrait-actions">
        {sample && !candidate && <button type="button" className="oc-btn oc-btn-default" disabled={Boolean(busy) || saving} onClick={() => generate(true)}>看示例图</button>}
        {waiting ? <button type="button" className="oc-btn oc-btn-default" onClick={cancel}>{busy === 'sample' ? '取消' : '停止生成'}</button>
          : <button type="button" className={'oc-btn ' + (candidate ? 'oc-btn-default' : 'oc-btn-primary')}
            disabled={Boolean(busy) || saving} onClick={() => generate()}>
            {editing ? (note.trim() ? '按描述微调' : '换一张') : candidate ? '按新类型生成' : '按设定生成'}</button>}
        {candidate && <button type="button" className="oc-btn oc-btn-primary" disabled={Boolean(busy) || saving || candidate.applied || !editing}
          onClick={apply}>{candidate.applied ? '已用作头像' : busy === 'apply' ? '正在保存…' : '用作头像'}</button>}
      </div>
      {!waiting && <small role="status">{candidate?.applied ? '头像已更新。' :
        history.some(item => item.id === candidate?.id) ? '已存入此浏览器的形象历史。' :
        candidate ? '采用后会立即更新头像。' : '生成时会参考助手的性格、爱好和外观设定。'}</small>}
      {historyError && <small role="status">{historyError}</small>}
      {history.length > 0 && <details className="cc-persona-disclosure">
        <summary>历史形象（{history.length}）</summary>
        <div className="cc-portrait-history">{history.map((item, index) => {
          const applied = Boolean(item.uploaded && item.uploaded === avatar);
          return <button key={item.id} type="button" disabled={Boolean(busy) || saving}
            aria-label={'查看形象 ' + (history.length - index) + (applied ? '，使用中' : '')}
            aria-pressed={candidate?.id === item.id}
            onClick={() => { setCandidate({ ...item, applied }); setDirection(item.direction); setError(''); }}>
            <img width="52" height="52" src={item.image} alt="" />{applied && <small>使用中</small>}
          </button>;
        })}</div>
      </details>}
    </div>}
  </section>;
}
