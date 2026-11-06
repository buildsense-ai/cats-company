import React, { useEffect, useRef, useState } from 'react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import { normalizePublicationDescription, samePublicationDescription } from '../utils/skillhub-publication-description';

export default function PublicationDescription({ entryRequestId }) {
  const [state, setState] = useState(null);
  const [attempt, setAttempt] = useState(0);
  const operation = useRef(null);
  const entry = useRef(entryRequestId);
  entry.current = entryRequestId;

  useEffect(() => {
    setState(null);
    if (typeof entryRequestId !== 'string' || !/^pubentry_[a-f0-9]{32}$/.test(entryRequestId) || !getToken()) return;
    const session = { entryRequestId, token: getToken(), revision: getAuthRevision() };
    const op = { ...session, controller: new AbortController(), active: true, busy: false };
    operation.current = op;
    const current = () => op.active && operation.current === op && entry.current === entryRequestId
      && isCurrentAuthSession(op.token, op.revision);
    const display = value => { if (current()) setState({ ...session, ...value }); };
    // Both reads and explicit confirmation bound the entire JSON consumption.
    op.run = async action => {
      let timer;
      try {
        return await Promise.race([action(), new Promise((_, reject) => {
          timer = setTimeout(() => { op.controller.abort(); reject(new Error('draft timeout')); }, 15000);
        })]);
      } finally { clearTimeout(timer); }
    };
    op.current = current;
    op.display = display;
    void (async () => {
      try {
        const value = await op.run(async () => {
          const caps = await api.getSkillHubPublicationCapabilities({ signal: op.controller.signal, timeoutMs: 7000 });
          if (!current() || op.controller.signal.aborted) return null;
          if (caps?.schemaVersion !== 1 || caps.descriptionConfirmationUiEnabled !== true) return null;
          display({ loading: true });
          const response = await api.getSkillHubPublicationDescriptionDraft(entryRequestId, { signal: op.controller.signal });
          if (!current() || op.controller.signal.aborted) return null;
          return normalizePublicationDescription(response, entryRequestId);
        });
        if (value) display({ value });
      } catch (error) {
        if (error?.status === 404 && ['publication.repair_not_found', 'publication.description_not_found'].includes(error?.data?.error?.code)) {
          display({ absent: true });
        } else display({ error: true });
      }
    })();
    const invalidate = () => { op.active = false; op.controller.abort(); setState(null); };
    window.addEventListener('cc:auth-changed', invalidate);
    return () => {
      op.active = false;
      op.controller.abort();
      window.removeEventListener('cc:auth-changed', invalidate);
    };
  }, [entryRequestId, attempt]);

  const confirm = async () => {
    const op = operation.current;
    const value = state?.value;
    if (!op?.current() || op.busy || !value || value.draft.state !== 'awaiting_confirmation' || state.error) return;
    op.busy = true; // Synchronous latch prevents a double-click before React renders.
    op.display({ value, confirming: true });
    try {
      const result = await op.run(async () => {
        const response = await api.confirmSkillHubPublicationDescriptionDraft(entryRequestId, value.draft.confirmationKey,
          { signal: op.controller.signal });
        if (!op.current() || op.controller.signal.aborted) return null;
        return normalizePublicationDescription(response, entryRequestId);
      });
      if (!op.current()) return;
      if (!result || result.draft.state !== 'confirmed' || !samePublicationDescription(value, result)) throw new Error('draft changed');
      op.display({ value: result });
    } catch {
      // An uncertain POST is never replayed automatically. Only GET recovery is
      // offered, which can discover an approval committed before a lost reply.
      op.display({ error: true, uncertain: true });
    } finally { op.busy = false; }
  };

  const reload = () => {
    const op = operation.current;
    if (op) { op.active = false; op.controller.abort(); }
    setState(null);
    setAttempt(n => n + 1);
  };

  if (!state || state.entryRequestId !== entryRequestId || !isCurrentAuthSession(state.token, state.revision)) return null;
  const d = state.value?.draft;
  return <section className='cc-skillhub-description-confirmation' aria-label='模型触发说明草稿确认'>
    <strong>模型触发说明（description）</strong>
    <p>这段文字影响模型何时使用 Skill，不是能力库的中文说明书。确认仅保留对此草稿的同意，不会修改原 Skill、启动修复或发布。</p>
    {state.loading && <p role='status'>正在读取待确认草稿…</p>}
    {state.absent && <p role='status'>尚无待确认草稿；自动修复尚未启用。</p>}
    {state.error && <p role='alert'>{state.uncertain
      ? '尚无法确认草稿是否已确认。请重新读取状态，不会自动重复提交。'
      : '暂时无法读取草稿，请重新读取。'}</p>}
    {d && <>
      <p>第 {d.round} / 3 轮 · {d.location.relativePath} · description{d.location.entryIndex != null ? ` · 条目索引 ${d.location.entryIndex}` : ''}</p>
      <pre className='cc-skillhub-description-text'>{d.text}</pre>
      {d.state === 'confirmed' ? <p role='status'>此草稿已确认；尚未授权正式发布。</p>
        : <button type='button' disabled={state.confirming} onClick={confirm}>{state.confirming ? '正在确认…' : '确认这段说明'}</button>}
    </>}
    {!state.loading && !state.confirming && <button type='button' onClick={reload}>重新读取草稿</button>}
  </section>;
}
