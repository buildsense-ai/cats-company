import React, { createContext, useContext, useEffect, useRef, useState } from 'react';
import { api } from '../api';
import { promptByteLength } from '../utils/system-prompt';

export const PersonaTrialDemo = createContext(false);

export default function AgentPersonaTrial({ uid, saved, draft, disabled, hidden }) {
  const demo = useContext(PersonaTrialDemo);
  const [question, setQuestion] = useState('');
  const [answers, setAnswers] = useState([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const pending = useRef(null);
  const tooLong = promptByteLength(saved) > 64000 || promptByteLength(draft) > 64000;
  const resume = answers.length === 1 && saved !== draft;

  useEffect(() => {
    setAnswers([]);
    setError('');
    setBusy(false);
    return () => { pending.current?.abort(); pending.current = null; };
  }, [question, saved, draft, uid]);

  useEffect(() => {
    if (hidden && pending.current) stop();
  }, [hidden]);

  async function run() {
    if (pending.current || !question.trim()) return;
    const controller = new AbortController();
    pending.current = controller;
    setBusy(true);
    setError('');
    const versions = saved === draft ? [['当前设定', draft]]
      : [[saved ? '已保存的设定' : '未加自定义设定', saved], ['当前草稿', draft]];
    const completed = answers.length < versions.length ? answers.length : 0;
    if (!completed) setAnswers([]);
    try {
      for (const [label, prompt] of versions.slice(completed)) {
        const result = await api.previewBotPrompt(uid, prompt, question.trim(), controller.signal);
        if (controller.signal.aborted) return;
        if (!result?.answer) throw new Error('试答服务暂未返回文字');
        setAnswers(list => [...list, { label, text: result.answer }]);
      }
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause?.message || '试答失败，请重试');
    } finally {
      if (pending.current === controller) { pending.current = null; setBusy(false); }
    }
  }

  function stop() {
    pending.current?.abort();
    pending.current = null;
    setBusy(false);
  }

  return <details hidden={hidden} className="cc-persona-disclosure cc-persona-trial" onToggle={event => {
    if (!event.currentTarget.open && pending.current) stop();
  }}>
    <summary>用我的问题试试</summary>
    {demo ? <p>这里展示设定效果。打开<a href="/">我的助手</a>，在「行为与能力」中可以用自己的问题试答。</p> : <>
    <label>你的问题<textarea rows={2} maxLength={1500} value={question}
      placeholder="比如：周末想约朋友见面，哪天还没定，帮我写一句邀约。"
      onChange={event => setQuestion(event.target.value)} /></label>
    <div className="cc-persona-heading">
      <small>{tooLong ? '设定超过试答长度上限（64 KB）。' : <>GLM 5.3 Flash 纯文本试答，按模型用量计费。{saved !== draft && (resume ? '继续时只生成草稿答复。' : '将分别生成两次。')}</>}</small>
      {busy ? <button type="button" onClick={stop}>停止试答</button>
        : <button type="button" disabled={disabled || tooLong || !question.trim()} onClick={run}>{resume ? '继续试答草稿' : error ? '重新试答' : '开始试答'}</button>}
    </div>
    <div aria-live="polite">{answers.map(a => <div className="cc-persona-trial-answer" key={a.label}>
      <strong>{a.label}</strong><p>{a.text}</p>
    </div>)}{busy && <small>正在生成回答…</small>}{error && <p role="alert">{error}</p>}</div>
    </>}
  </details>;
}
