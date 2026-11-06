import React, { useEffect, useState } from 'react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import { normalizePublicationHistory } from '../utils/skillhub-publication-history';

export default function PublicationHistory({ entryRequestId }) {
  const [state, setState] = useState(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    setState(null);
    if (!/^pubentry_[a-f0-9]{32}$/.test(entryRequestId || '')) return;
    const token = getToken();
    const revision = getAuthRevision();
    let active = Boolean(token);
    let timer;
    let deadline;
    let reading = false;
    let supported = false;
    let terminal = false;
    let failed = false;
    const controller = new AbortController();
    const current = () => active && isCurrentAuthSession(token, revision);
    const display = value => setState({ entryRequestId, token, revision, ...value });
    const read = async () => {
      if (!current() || reading || terminal || failed || document.visibilityState === 'hidden') return;
      reading = true;
      try {
        // Bound the entire response (including JSON consumption), not merely
        // fetch headers. The legacy API request helper only times out fetch.
        const history = await Promise.race([
          (async () => {
            if (!supported) {
              const caps = await api.getSkillHubPublicationCapabilities({ signal: controller.signal, timeoutMs: 7000 });
              if (!current() || controller.signal.aborted) return null;
              if (caps?.schemaVersion !== 1 || caps.descriptionReleaseHistoryReadsEnabled !== true) {
                terminal = true; // No repeated capability probes until reopen.
                return null;
              }
              supported = true;
            }
            const result = await api.getSkillHubPublicationDescriptionReleaseHistory(entryRequestId,
              { signal: controller.signal });
            if (!current() || controller.signal.aborted) return null;
            return normalizePublicationHistory(result, entryRequestId);
          })(),
          new Promise((_, reject) => {
            deadline = setTimeout(() => { controller.abort(); reject(new Error('history timeout')); }, 15000);
          }),
        ]);
        if (!current() || !history) return;
        terminal = history.stage !== 'not_recorded';
        display({ history });
        if (!terminal) timer = setTimeout(() => { void read(); }, 30000);
      } catch {
        failed = true;
        // An unsuccessful capability read is unknown, not an explicit opt-out.
        // Keep a static retry affordance; only a successful disabled/unsupported
        // capability response may silently omit this section.
        if (current()) {
          display({ error: true });
        }
      } finally { clearTimeout(deadline); reading = false; }
    };
    const visibility = () => {
      clearTimeout(timer);
      if (document.visibilityState !== 'hidden') void read();
    };
    const invalidate = () => {
      active = false;
      clearTimeout(timer);
      clearTimeout(deadline);
      controller.abort();
      setState(null);
    };
    void read();
    document.addEventListener('visibilitychange', visibility);
    window.addEventListener('cc:auth-changed', invalidate);
    return () => {
      active = false;
      clearTimeout(timer);
      clearTimeout(deadline);
      controller.abort();
      document.removeEventListener('visibilitychange', visibility);
      window.removeEventListener('cc:auth-changed', invalidate);
    };
  }, [entryRequestId, attempt]);
  if (!state || state.entryRequestId !== entryRequestId
    || !isCurrentAuthSession(state.token, state.revision)) return null;
  const h = state.history;
  return <div className='cc-skillhub-report-summary' aria-label='修复副本发布结果'>
    <strong>修复副本发布结果</strong>
    <p role={state.error ? 'alert' : 'status'}>{state.error
      ? '暂时无法读取最终发布结果，无法确认是否发布成功。'
      : h.stage === 'published' ? `已发布：${h.result.skillId} · ${h.result.version}`
        : h.stage === 'publish_failed' ? '修复副本发布失败。'
          : '尚未取得最终发布结果；此窗口打开时会自动刷新。'}</p>
    {h?.code && <code>{h.code}</code>}
    {state.error && <button type='button' onClick={() => setAttempt(value => value + 1)}>重试读取发布结果</button>}
  </div>;
}
