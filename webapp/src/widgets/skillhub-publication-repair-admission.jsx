import React, { useEffect, useRef, useState } from 'react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import { normalizePublicationRepairTask, repairTaskStateLabel, validRepairScope } from '../utils/skillhub-publication-repair-task';

export default function PublicationRepairAdmission({ entryRequestId, botUid, topicId }) {
  const [state, setState] = useState(null);
  const [attempt, setAttempt] = useState(0);
  const operation = useRef(null);
  const scope = JSON.stringify([entryRequestId, botUid, topicId]);
  const scopeRef = useRef(scope);
  scopeRef.current = scope;
  useEffect(() => {
    setState(null);
    if (!validRepairScope(entryRequestId, botUid, topicId) || !getToken()) return;
    const session = { scope, token: getToken(), revision: getAuthRevision() };
    const op = { ...session, active: true, busy: false, controller: new AbortController() };
    operation.current = op;
    const current = () => op.active && operation.current === op && scopeRef.current === scope
      && isCurrentAuthSession(op.token, op.revision);
    const display = value => { if (current()) setState({ ...session, ...value }); };
    op.current = current;
    op.display = display;
    op.run = async (action, timeoutMs) => {
      let timer;
      try {
        return await Promise.race([action(), new Promise((_, reject) => {
          timer = setTimeout(() => { op.controller.abort(); reject(new Error('repair task timeout')); }, timeoutMs);
        })]);
      } finally { clearTimeout(timer); }
    };
    void (async () => {
      try {
        const task = await op.run(async () => {
          const caps = await api.getSkillHubPublicationCapabilities({ signal: op.controller.signal, timeoutMs: 7000 });
          if (!current() || op.controller.signal.aborted) return null;
          if (caps?.schemaVersion !== 1 || caps.repairTaskAdmissionUiEnabled !== true) return null;
          display({ loading: true });
          const response = await api.getSkillHubPublicationRepairTask(entryRequestId, { signal: op.controller.signal });
          if (!current() || op.controller.signal.aborted) return null;
          return normalizePublicationRepairTask(response, entryRequestId, botUid);
        }, 15000);
        if (task) display({ task });
      } catch (error) {
        // Only this exact human API result means a new registration can be
        // offered. A missing route, disabled gate, 403 or network error cannot.
        if (!op.controller.signal.aborted && error?.status === 404 && error?.data?.error?.code === 'publication.repair_not_found') {
          display({ absent: true });
        } else display({ error: true });
      }
    })();
    const invalidate = () => { op.active = false; op.controller.abort(); setState(null); };
    window.addEventListener('cc:auth-changed', invalidate);
    return () => {
      op.active = false; op.controller.abort();
      window.removeEventListener('cc:auth-changed', invalidate);
    };
  }, [entryRequestId, botUid, topicId, scope, attempt]);

  const register = async () => {
    const op = operation.current;
    if (!op?.current() || op.controller.signal.aborted || op.busy || !state?.absent || state.scope !== scope) return;
    op.busy = true;
    op.display({ registering: true });
    try {
      const task = await op.run(async () => {
        const response = await api.admitSkillHubPublicationRepairTask(entryRequestId, topicId, { signal: op.controller.signal });
        if (!op.current() || op.controller.signal.aborted) return null;
        return normalizePublicationRepairTask(response, entryRequestId, botUid);
      }, 65000);
      if (!op.current()) return;
      // Existing identical admissions can already have spent rounds. Never
      // require zero or treat a recovered task as a newly reset budget.
      if (!task || task.topicId !== topicId) throw new Error('repair task mismatch');
      op.display({ task });
    } catch {
      op.display({ error: true, uncertain: true });
    } finally { op.busy = false; }
  };
  const reload = () => {
    const op = operation.current;
    if (op) { op.active = false; op.controller.abort(); }
    setState(null); setAttempt(n => n + 1);
  };

  if (!state || state.scope !== scope || !isCurrentAuthSession(state.token, state.revision)) return null;
  const otherTopic = state.task && state.task.topicId !== topicId;
  return <section className='cc-skillhub-repair-admission' aria-label='登记修复任务'>
    <strong>修复任务</strong>
    <p>登记会把此发布报告与当前私有修复对话绑定。最多三轮，仅格式问题可自动修，功能、身份或安全问题需停下；description 须确认后再处理。</p>
    <p>当前登记不会启动模型、修改 Skill 或授权发布。</p>
    {state.loading && <p role='status'>正在读取已有修复任务…</p>}
    {state.registering && <p role='status'>正在复核报告并登记；无需重复点击…</p>}
    {state.absent && <>
      <p role='status'>尚未登记修复任务，登记不会消耗修复轮次。</p>
      <button type='button' onClick={register}>登记到当前修复对话</button>
    </>}
    {state.error && <p role='alert'>{state.uncertain
      ? '登记结果尚无法确认，请重新读取已有任务。不会自动重复登记。'
      : '暂时无法读取修复任务，请重新读取。'}</p>}
    {state.task && <>
      <p role='status'>{otherTopic ? '此发布记录已关联另一个修复对话，不能在这里重复登记或更换关联。' : '已关联当前修复对话。'}</p>
      <p>已使用 {state.task.roundsUsed} / {state.task.maxFormatRepairRounds} 轮 · {repairTaskStateLabel(state.task.state)}</p>
    </>}
    {!state.loading && !state.registering && <button type='button' onClick={reload}>重新读取修复任务</button>}
  </section>;
}
