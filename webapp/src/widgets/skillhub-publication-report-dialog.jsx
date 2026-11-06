import React, { useId, useRef } from 'react';
import { createPortal } from 'react-dom';
import { ClipboardCheck, MessageSquarePlus, X } from 'lucide-react';
import useDialogBehavior from '../utils/use-dialog-behavior';
import PublicationHistory from './skillhub-publication-history';
import PublicationDescription from './skillhub-publication-description';
import PublicationRepairAdmission from './skillhub-publication-repair-admission';

export default function PublicationReportDialog({ report, onClose, onRetry, onRepair, returnFocusRef, repairConversation }) {
  const dialogRef = useRef(null);
  const cancelRef = useRef(null);
  const titleId = useId();
  const descriptionId = useId();
  useDialogBehavior(dialogRef, { onClose, initialFocusRef: cancelRef, returnFocusRef });
  const issues = Array.isArray(report?.issues) ? report.issues : [];
  const outcome = report?.outcome === 'rejected' ? '校验未通过' : '发布需要处理';
  const stage = report?.failureStage === 'capture' ? '运行环境捕获' : report?.failureStage === 'source' ? '运行环境检查' : 'SkillHub 发布校验';
  const policyLabel = value => ({ manual: '需人工处理', confirmation_required: '需确认后修改', none: '无自动修复' })[value] || '需人工检查';
  const canRepair = Boolean(onRepair) && report?.outcome === 'rejected' && !report.loading && !report.error;
  return createPortal(
    <div className='cc-skillhub-detail-overlay' onMouseDown={event => { if (event.target === event.currentTarget) onClose(); }}>
      <section ref={dialogRef} tabIndex={-1} className='cc-skillhub-detail-dialog cc-skillhub-publication-report-dialog' role='dialog' aria-modal='true' aria-labelledby={titleId} aria-describedby={descriptionId}>
        <header className='cc-skillhub-detail-header'>
          <span className='cc-skillhub-detail-icon' aria-hidden='true'><ClipboardCheck size={19} /></span>
          <div><span>{stage}</span><h2 id={titleId}>{report?.skillName || 'Skill'} · {outcome}</h2></div>
          <button type='button' className='icon-button' aria-label='关闭发布报告' onClick={onClose}><X size={17} aria-hidden='true' /></button>
        </header>
        <p id={descriptionId} className='cc-skillhub-detail-description'>请根据下面的问题检查 Skill，修改后可回到运行工作区重新发布。</p>
        <PublicationHistory entryRequestId={report?.entryRequestId} />
        {!report?.loading && !report?.error && report?.outcome === 'rejected'
          && <PublicationRepairAdmission entryRequestId={report.entryRequestId}
            botUid={repairConversation?.botUid} topicId={repairConversation?.topicId} />}
        <PublicationDescription entryRequestId={report?.entryRequestId} />
        {report?.loading ? <p role='status' className='cc-skillhub-report-empty'>正在读取发布报告…</p>
          : report?.error ? <p role='alert' className='cc-skillhub-report-empty'>{report.error}</p> : null}
        {report?.repairError && <p role='alert' className='cc-skillhub-report-empty'>{report.repairError}</p>}
        {report?.failureCode && <div className='cc-skillhub-report-summary'><strong>失败代码</strong><code>{report.failureCode}</code></div>}
        {!report?.loading && !report?.error && <>
        {report?.ruleVersion && <div className='cc-skillhub-report-summary'><strong>校验规则</strong><span>{report.ruleVersion}</span></div>}
        {issues.length > 0 ? <ol className='cc-skillhub-publication-issues'>
          {issues.map((issue, index) => <li key={`${issue.code}:${index}`}>
            <code>{issue.code || '未命名问题'}</code>
            <span className='cc-skillhub-report-location'>{[
              issue.location?.relativePath, issue.location?.field,
              issue.location?.entryIndex != null ? `条目索引 ${issue.location.entryIndex}` : '',
            ].filter(Boolean).join(' · ')}</span>
            {issue.message && <p>{issue.message}</p>}
            <span>{issue.blocking ? '阻止发布' : '提示'} · {policyLabel(issue.repairPolicy)}</span>
          </li>)}
        </ol> : <p className='cc-skillhub-report-empty'>当前没有云端校验问题，请根据失败代码检查运行环境。</p>}
        {report?.truncated && <p className='cc-skillhub-report-empty'>问题较多，报告已截断。修复列出的问题后，请重新发布以继续检查。</p>}
        </>}
        {canRepair && <p className='cc-skillhub-report-empty'>在当前 Bot 的新对话中查看此报告。自动修复尚未启用。</p>}
        <footer className='cc-skillhub-detail-footer'>
          {canRepair && <button type='button' disabled={report.repairing} onClick={onRepair}>
            <MessageSquarePlus size={14} aria-hidden='true' />{report.repairing ? '正在创建…' : '新建修复对话'}
          </button>}
          <button type='button' disabled={report?.loading || report?.repairing} onClick={onRetry}>重新读取</button>
          <button ref={cancelRef} type='button' onClick={onClose}>关闭</button>
        </footer>
      </section>
    </div>, document.body,
  );
}
