import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ClipboardCheck, RefreshCw } from 'lucide-react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import { normalizePublicationRepairContext, readPublicationRepairHint } from '../utils/skillhub-publication-repair';
import PublicationReportDialog from './skillhub-publication-report-dialog';
import '../css/skillhub-view.css';

export default function PublicationRepairPanel({ userUid, topic, isGroup, groupId }) {
  const [state, setState] = useState(null);
  const [open, setOpen] = useState(false);
  const requestRef = useRef(null);
  const buttonRef = useRef(null);
  const scope = `${userUid}:${topic}:${groupId}`;
  const scopeRef = useRef(scope);
  scopeRef.current = scope;

  const load = useCallback(async () => {
    const hint = isGroup && topic === `grp_${groupId}` ? readPublicationRepairHint(userUid, topic) : null;
    if (!hint) { requestRef.current = null; setState(null); return; }
    const token = getToken();
    const revision = getAuthRevision();
    const request = {};
    requestRef.current = request;
    const current = () => requestRef.current === request && scopeRef.current === scope
      && isCurrentAuthSession(token, revision);
    if (!current()) { setState(null); return; }
    setState({ scope, token, revision, loading: true });
    try {
      // Browser hints cannot establish a conversation's Bot identity. Read the
      // current task membership before requesting any publication report.
      const response = await api.getGroupInfo(groupId);
      if (!current()) return;
      const group = response?.group;
      const members = Array.isArray(response?.members) ? response.members : [];
      const bots = members.filter(member => member.is_bot === true);
      if (String(group?.id) !== String(groupId) || group.kind !== 'agent_task'
        || String(group.owner_id) !== String(userUid)
        || members.length !== 2 || bots.length !== 1 || String(bots[0].user_id) !== hint.botUid
        || !members.some(member => member.is_bot !== true && String(member.user_id) === String(userUid))) {
        throw new Error('task mismatch');
      }
      const context = await api.getSkillHubPublicationRepairContext(hint.entryRequestId);
      if (!current()) return;
      const { report } = normalizePublicationRepairContext(context, hint.entryRequestId, hint.botUid);
      setState({ scope, token, revision, report, repairConversation: { botUid: hint.botUid, topicId: topic } });
    } catch {
      if (current()) setState({ scope, token, revision,
        error: '无法读取此修复报告，请确认当前仍拥有该 Bot，或回到运行工作区重新读取报告。' });
    }
  }, [groupId, isGroup, scope, topic, userUid]);

  useEffect(() => {
    setOpen(false);
    void load();
    const invalidate = () => { requestRef.current = null; setState(null); setOpen(false); };
    window.addEventListener('cc:auth-changed', invalidate);
    return () => {
      requestRef.current = null;
      window.removeEventListener('cc:auth-changed', invalidate);
    };
  }, [load]);

  if (!state || state.scope !== scope || !isCurrentAuthSession(state.token, state.revision)) return null;
  return <>
    <section className='cc-publication-repair-panel' aria-label='Skill 发布修复报告'>
      <ClipboardCheck size={17} aria-hidden='true' />
      <div>
        <strong>Skill 发布修复</strong>
        <p role={state.error ? 'alert' : 'status'}>{state.error || (state.loading
          ? '正在复核当前 Bot 与发布报告…' : '发布拒绝报告已关联到此对话，尚未启动自动修复。')}</p>
      </div>
      <button ref={buttonRef} type='button' disabled={state.loading} onClick={() => {
        setOpen(true);
        void load();
      }}><RefreshCw size={14} aria-hidden='true' />{state.error ? '重新读取' : '查看发布报告'}</button>
    </section>
    {open && <PublicationReportDialog report={state.report || state} returnFocusRef={buttonRef}
      repairConversation={state.repairConversation}
      onRetry={load} onClose={() => setOpen(false)} />}
  </>;
}
