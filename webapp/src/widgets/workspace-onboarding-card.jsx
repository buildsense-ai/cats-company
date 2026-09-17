import React, { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Cloud, ChevronRight, Download, Laptop, X } from 'lucide-react';
import { api } from '../api';
import { readStorageValue, writeStorageValue } from '../utils/storage-access';
import './workspace-onboarding-card.css';

const ONBOARDING_DISMISSED_VALUE = 'dismissed';

export function workspaceOnboardingStorageKey(userId) {
  return `cc_workspace_onboarding_v1:${userId}`;
}

function openDialog(dialog) {
  if (!dialog?.open) {
    if (typeof dialog?.showModal === 'function') dialog.showModal();
    else dialog?.setAttribute('open', '');
  }
}

function closeDialog(dialog) {
  if (dialog?.open && typeof dialog.close === 'function') dialog.close();
  else dialog?.removeAttribute('open');
}

function inviteErrorMessage(error) {
  const message = String(error?.message || '').trim();
  if (
    !message
    || /bot invite code is invalid or expired/i.test(message)
    || /invalid or unavailable bot invite code/i.test(message)
  ) {
    return '邀请码无效或已失效';
  }
  return message;
}

function AssistantInviteDialog({ onComplete, onClose }) {
  const dialogRef = useRef(null);
  const inputRef = useRef(null);
  const titleRef = useRef(null);
  const [invite, setInvite] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [completed, setCompleted] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    const opener = document.activeElement;
    const dialog = dialogRef.current;
    openDialog(dialog);
    inputRef.current?.focus();
    return () => {
      closeDialog(dialog);
      if (opener?.isConnected) opener.focus();
    };
  }, []);

  useEffect(() => {
    if (completed) titleRef.current?.focus();
  }, [completed]);

  const finish = () => {
    onComplete();
    onClose();
  };

  const submit = async (event) => {
    event.preventDefault();
    const code = invite.trim();
    if (!code || submitting || completed) return;

    setSubmitting(true);
    setError('');
    try {
      await api.redeemBotInviteCode(code);
      window.dispatchEvent(new Event('cc:data-changed'));
      setCompleted(true);
    } catch (requestError) {
      setError(inviteErrorMessage(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return <dialog ref={dialogRef} className="cc-workspace-onboarding-card cc-workspace-onboarding-invite-dialog" aria-labelledby="workspace-invite-title" aria-describedby="workspace-invite-description" onCancel={(event) => { event.preventDefault(); completed ? finish() : onClose(); }}>
    <button type="button" className="cc-workspace-onboarding-close" aria-label={completed ? '开始创建任务' : '关闭邀请码窗口'} onClick={completed ? finish : onClose}><X size={20} aria-hidden="true" /></button>
    <header className="cc-workspace-onboarding-heading">
      <h2 id="workspace-invite-title" ref={titleRef} tabIndex={-1} data-cc-focus-group="true">{completed ? '云端助手已添加' : '添加云端 AI 助手'}</h2>
      <p id="workspace-invite-description">{completed ? '现在可以创建任务，并在输入框中选择这个助手开始对话。' : '输入助手所有者或官方分享的邀请码。'}</p>
    </header>
    {completed ? <>
      <p className="cc-workspace-onboarding-invite-note" role="status">助手已加入你的可用列表。</p>
      <footer className="cc-workspace-onboarding-invite-footer"><button type="button" className="cc-workspace-onboarding-action" onClick={finish}>开始创建任务</button></footer>
    </> : <form className="cc-workspace-onboarding-invite-form" onSubmit={submit}>
      <label htmlFor="workspace-onboarding-invite">邀请码</label>
      <input ref={inputRef} id="workspace-onboarding-invite" placeholder="输入或粘贴邀请码" value={invite} autoComplete="off" autoCapitalize="off" spellCheck={false} aria-describedby="workspace-invite-note" onChange={(event) => { setError(''); setInvite(event.target.value.toUpperCase()); }} onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }} />
      <p id="workspace-invite-note" className="cc-workspace-onboarding-invite-note">邀请码由助手所有者或官方提供。</p>
      {error && <p className="cc-workspace-onboarding-invite-error" role="alert">{error}</p>}
      <footer className="cc-workspace-onboarding-invite-footer">
        <button type="button" onClick={onClose}>取消</button>
        <button type="submit" className="cc-workspace-onboarding-action" disabled={submitting || !invite.trim()}>{submitting ? '添加中…' : '添加助手'}</button>
      </footer>
    </form>}
  </dialog>;
}

function AssistantHelpDialog({ kind, onClose }) {
  const dialogRef = useRef(null);
  const titleRef = useRef(null);
  const isCloud = kind === 'cloud';
  const steps = isCloud ? [
    ['获取邀请码', '邀请码由其他云端 Agent 的所有者或官方分享。获取想要使用的助手的邀请码后，即可添加。'],
    ['添加助手', '返回引导，点击「使用邀请码」，输入邀请码并添加助手。'],
    ['创建任务并选择助手', '添加完成后，在创建任务时选择这个 Agent，即可开始对话或让它执行其他操作。'],
  ] : [
    ['下载 Dashboard', '返回引导，点击「下载桌面端」，选择与你的电脑系统对应的 Dashboard 安装包。'],
    ['安装并登录', '安装并打开 Dashboard，使用当前账号登录。'],
    ['激活并开始使用', '登录后即可激活系统自动分配的本地助手。保持桌面端打开，即可与助手聊天或让它操作电脑。'],
  ];

  useEffect(() => {
    const opener = document.activeElement;
    const dialog = dialogRef.current;
    openDialog(dialog);
    titleRef.current?.focus();
    return () => {
      closeDialog(dialog);
      if (opener?.isConnected) opener.focus();
    };
  }, []);

  return <dialog ref={dialogRef} id="workspace-assistant-help" className="cc-workspace-onboarding-card cc-workspace-onboarding-help-dialog" aria-labelledby="workspace-assistant-help-title" onCancel={(event) => { event.preventDefault(); onClose(); }}>
    <button type="button" className="cc-workspace-onboarding-close" aria-label="关闭使用说明" onClick={onClose}><X size={20} aria-hidden="true" /></button>
    <header className="cc-workspace-onboarding-heading">
      <h2 id="workspace-assistant-help-title" ref={titleRef} tabIndex={-1} data-cc-focus-group="true">{isCloud ? '云端助手使用说明' : '本地助手使用说明'}</h2>
    </header>
    <div className="cc-workspace-onboarding-help-body" role="region" aria-label="使用说明正文" tabIndex={0}>
      <ol className="cc-workspace-onboarding-help-steps">
        {steps.map(([title, description]) => <li key={title}><h3>{title}</h3><p>{description}</p></li>)}
      </ol>
      {isCloud ? <>
        <section className="cc-workspace-onboarding-help-section" aria-labelledby="workspace-help-computer">
          <h3 id="workspace-help-computer">操作电脑与远程使用</h3>
          <p>云端助手也可以处理电脑上的文件、操作电脑。同样需要先下载 Dashboard 桌面端，并使用当前账号登录。</p>
          <ul>
            <li><strong>电脑开启时：</strong>保持电脑联网、桌面端运行并登录，即可通过移动设备上的微信或飞书，让云端助手远程操作电脑。</li>
            <li><strong>电脑关闭时：</strong>仍可通过移动设备与云端助手聊天，但无法操作这台电脑。需要操作电脑时，请先开机并打开桌面端。</li>
          </ul>
        </section>
        <section className="cc-workspace-onboarding-help-section" aria-labelledby="workspace-help-model">
          <h3 id="workspace-help-model">模型由谁调整？</h3>
          <p>云端 Agent 的模型只有所有者可以调整。添加他人或官方分享的助手后，你可以使用它，但不能修改它的模型。</p>
        </section>
      </> : <section className="cc-workspace-onboarding-help-section" aria-labelledby="workspace-help-availability">
        <h3 id="workspace-help-availability">本地助手什么时候可以使用？</h3>
        <p>本地 AI 助手依赖你的电脑运行。只有电脑开启、Dashboard 桌面端打开并登录后，才能聊天或操作电脑。</p>
        <p>电脑关机或桌面端退出后，本地助手将无法使用；再次开机并打开、登录桌面端后即可继续。</p>
      </section>}
      <section className="cc-workspace-onboarding-help-section" aria-labelledby="workspace-help-create">
        <h3 id="workspace-help-create">也可以创建自己的助手</h3>
        <p>除了添加分享的云端助手、激活系统分配的本地助手，你也可以自行创建本地或云端 AI 助手。</p>
      </section>
    </div>
    <footer className="cc-workspace-onboarding-help-footer"><button type="button" onClick={onClose}>返回引导</button></footer>
  </dialog>;
}

export default function WorkspaceOnboardingCard({ userId, onDownloadDashboard, dashboardDownloadOpen = false, forceVisible = false }) {
  const [eligible, setEligible] = useState(false);
  const [visible, setVisible] = useState(true);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [helpKind, setHelpKind] = useState(null);
  const dialogRef = useRef(null);
  const headingRef = useRef(null);
  const downloadRef = useRef(null);
  const restoreDownloadFocusRef = useRef(false);

  useEffect(() => {
    let cancelled = false;
    const loadEligibility = async () => {
      if (forceVisible) {
        if (!cancelled) setEligible(true);
        return;
      }
      if (!userId || readStorageValue(workspaceOnboardingStorageKey(userId)) === ONBOARDING_DISMISSED_VALUE) return;
      try {
        const response = await api.getAgents();
        if (!cancelled) setEligible(Array.isArray(response?.agents) && response.agents.length === 0);
      } catch {
        // A failed roster request must not block the workspace with a modal.
        if (!cancelled) setEligible(false);
      }
    };
    loadEligibility();
    return () => { cancelled = true; };
  }, [forceVisible, userId]);

  useEffect(() => {
    if (!eligible || !visible || dashboardDownloadOpen) return undefined;
    const dialog = dialogRef.current;
    openDialog(dialog);
    (restoreDownloadFocusRef.current ? downloadRef : headingRef).current?.focus();
    restoreDownloadFocusRef.current = false;
    return () => closeDialog(dialog);
  }, [eligible, visible, dashboardDownloadOpen]);

  const dismiss = () => {
    if (!forceVisible && userId) writeStorageValue(workspaceOnboardingStorageKey(userId), ONBOARDING_DISMISSED_VALUE);
    closeDialog(dialogRef.current);
    setVisible(false);
    requestAnimationFrame(() => document.querySelector('.cc-empty-composer-wrap textarea')?.focus());
  };

  if (!eligible || !visible) return null;
  return createPortal(
    <>
      <dialog ref={dialogRef} className="cc-workspace-onboarding-card" aria-labelledby="workspace-onboarding-title" aria-describedby="workspace-onboarding-description" onCancel={(event) => { event.preventDefault(); dismiss(); }}>
        <button type="button" className="cc-workspace-onboarding-close" aria-label="稍后设置助手" onClick={dismiss}><X size={20} aria-hidden="true" /></button>
        <header className="cc-workspace-onboarding-heading">
          <p className="cc-workspace-onboarding-greeting">欢迎来到 <span className="cc-workspace-onboarding-brand">CatsCo</span></p>
          <h1 id="workspace-onboarding-title" ref={headingRef} tabIndex={-1} data-cc-focus-group="true">开始使用你的助手</h1>
          <p id="workspace-onboarding-description">选择一种方式，让助手开始为你工作。之后也可以继续添加。</p>
        </header>
        <div className="cc-workspace-onboarding-methods">
          <section className="cc-workspace-onboarding-method" aria-labelledby="workspace-cloud-title">
            <div className="cc-workspace-onboarding-method-heading"><Cloud size={20} aria-hidden="true" /><h2 id="workspace-cloud-title">添加云端 AI 助手</h2></div>
            <p>使用邀请码，添加他人分享的云端 AI 助手，开始对话和任务。</p>
            <div className="cc-workspace-onboarding-method-actions">
              <button type="button" className="cc-workspace-onboarding-help" aria-haspopup="dialog" onClick={() => setHelpKind('cloud')}>使用说明</button>
              <button type="button" className="cc-workspace-onboarding-action" aria-haspopup="dialog" onClick={() => setInviteOpen(true)}>使用邀请码<ChevronRight size={14} aria-hidden="true" /></button>
            </div>
          </section>
          <section className="cc-workspace-onboarding-method" aria-labelledby="workspace-local-title">
            <div className="cc-workspace-onboarding-method-heading"><Laptop size={20} aria-hidden="true" /><h2 id="workspace-local-title">激活本地 AI 助手</h2></div>
            <p>下载 Dashboard 并登录，系统会自动关联你的本地助手。</p>
            <div className="cc-workspace-onboarding-method-actions">
              <button type="button" className="cc-workspace-onboarding-help" aria-haspopup="dialog" onClick={() => setHelpKind('local')}>使用说明</button>
              <button ref={downloadRef} type="button" className="cc-workspace-onboarding-action" onClick={() => { restoreDownloadFocusRef.current = true; onDownloadDashboard?.(); }}>下载桌面端<Download size={14} aria-hidden="true" /></button>
            </div>
          </section>
        </div>
        <footer className="cc-workspace-onboarding-footer">
          <button type="button" className="cc-workspace-onboarding-start" onClick={dismiss}>稍后再说</button>
        </footer>
      </dialog>
      {helpKind && <AssistantHelpDialog kind={helpKind} onClose={() => setHelpKind(null)} />}
      {inviteOpen && <AssistantInviteDialog onComplete={dismiss} onClose={() => setInviteOpen(false)} />}
    </>, document.body,
  );
}
