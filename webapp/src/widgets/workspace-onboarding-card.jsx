import React, { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ChevronRight, Layers3, Ticket, X } from 'lucide-react';
import { api } from '../api';
import { describeBotInviteCodeError } from '../utils/bot-invite-error';
import { writeStorageValue } from '../utils/storage-access';
import {
  WORKSPACE_ONBOARDING_DISMISSED_VALUE,
  workspaceOnboardingStorageKey,
} from '../utils/workspace-onboarding';
import './workspace-onboarding-card.css';

export { workspaceOnboardingStorageKey };

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

function AssistantInviteDialog({ onPersist, onComplete, onClose }) {
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
      onPersist();
      window.dispatchEvent(new Event('cc:data-changed'));
      setCompleted(true);
    } catch (requestError) {
      setError(describeBotInviteCodeError(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return <dialog ref={dialogRef} className="cc-workspace-onboarding-card cc-workspace-onboarding-invite-dialog" aria-labelledby="workspace-invite-title" aria-describedby="workspace-invite-description" onCancel={(event) => { event.preventDefault(); completed ? finish() : onClose(); }}>
    <button type="button" className="cc-workspace-onboarding-close" aria-label={completed ? '开始创建任务' : '关闭邀请码窗口'} onClick={completed ? finish : onClose}><X size={20} aria-hidden="true" /></button>
    <header className="cc-workspace-onboarding-heading">
      <h2 id="workspace-invite-title" ref={titleRef} tabIndex={-1} data-cc-focus-group="true">{completed ? '云端助手已添加' : '添加云端 AI 助手'}</h2>
      <p id="workspace-invite-description">{completed ? '现在可以创建任务，并在输入框中选择这个助手开始对话。' : '粘贴助手所有者或官方提供的邀请码。'}</p>
    </header>
    {completed ? <>
      <p className="cc-workspace-onboarding-invite-note" role="status">助手已加入你的可用列表。</p>
      <footer className="cc-workspace-onboarding-invite-footer"><button type="button" className="cc-workspace-onboarding-action" onClick={finish}>开始创建任务</button></footer>
    </> : <form className="cc-workspace-onboarding-invite-form" onSubmit={submit}>
      <label htmlFor="workspace-onboarding-invite">邀请码</label>
      <input ref={inputRef} id="workspace-onboarding-invite" placeholder="粘贴邀请码" value={invite} autoComplete="off" autoCapitalize="off" spellCheck={false} onChange={(event) => { setError(''); setInvite(event.target.value.toUpperCase()); }} onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }} />
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
    ['获取邀请码', '向助手所有者或官方获取邀请码，用于添加分享的云端助手。'],
    ['添加助手', '返回引导，点击「使用邀请码」，输入邀请码并添加助手。'],
    ['创建任务并选择助手', '添加完成后，创建任务并选择这个助手，即可开始对话或执行任务。'],
  ] : [
    ['查看套餐', '返回引导，点击「查看套餐」，浏览个人、团队与企业方案。'],
    ['比较权益', '根据使用需要，比较各套餐的 Token 额度、云端资源与协作权益。'],
    ['确认购买', '选择套餐后，在确认页面核对价格与权益，再继续付款。'],
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
      <h2 id="workspace-assistant-help-title" ref={titleRef} tabIndex={-1} data-cc-focus-group="true">{isCloud ? '云端助手使用说明' : '套餐选择说明'}</h2>
    </header>
    <div className="cc-workspace-onboarding-help-body" role="region" aria-label="使用说明正文" tabIndex={0}>
      <ol className="cc-workspace-onboarding-help-steps">
        {steps.map(([title, description]) => <li key={title}><h3>{title}</h3><p>{description}</p></li>)}
      </ol>
      {isCloud ? <>
        <section className="cc-workspace-onboarding-help-section" aria-labelledby="workspace-help-computer">
          <h3 id="workspace-help-computer">操作电脑与远程使用</h3>
          <p>需要让云端助手处理这台电脑上的文件或操作电脑时，请先下载 CatsCo 桌面端，并使用当前账号登录。</p>
          <ul>
            <li><strong>电脑开启时：</strong>保持电脑联网、桌面端运行并登录，即可通过移动设备上的微信或飞书，让云端助手远程操作电脑。</li>
            <li><strong>电脑关闭时：</strong>仍可通过移动设备与云端助手聊天，但无法操作这台电脑。需要操作电脑时，请先开机并打开桌面端。</li>
          </ul>
        </section>
        <section className="cc-workspace-onboarding-help-section" aria-labelledby="workspace-help-model">
          <h3 id="workspace-help-model">模型由谁调整？</h3>
          <p>云端助手的模型由所有者调整。添加他人或官方分享的助手后，你可以使用它，但不能修改它的模型。</p>
        </section>
      </> : null}
      {isCloud && <section className="cc-workspace-onboarding-help-section" aria-labelledby="workspace-help-create">
        <h3 id="workspace-help-create">想使用自己的助手？</h3>
        <p>返回引导，点击「查看套餐」，比较 Token 额度、云端资源与套餐权益。</p>
      </section>}
    </div>
    <footer className="cc-workspace-onboarding-help-footer"><button type="button" onClick={onClose}>返回引导</button></footer>
  </dialog>;
}

export default function WorkspaceOnboardingCard({
  userId,
  visible: initiallyVisible = false,
  onDismiss,
  onChoosePlan,
  secondaryModalOpen = false,
}) {
  const [visible, setVisible] = useState(initiallyVisible);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [helpKind, setHelpKind] = useState(null);
  const dialogRef = useRef(null);
  const headingRef = useRef(null);

  useEffect(() => {
    setVisible(initiallyVisible);
  }, [initiallyVisible, userId]);

  useEffect(() => {
    if (!initiallyVisible || !visible || secondaryModalOpen || inviteOpen || helpKind) return undefined;
    const dialog = dialogRef.current;
    openDialog(dialog);
    headingRef.current?.focus();
    return () => closeDialog(dialog);
  }, [initiallyVisible, visible, secondaryModalOpen, inviteOpen, helpKind]);

  const markExperienced = () => {
    if (userId) writeStorageValue(workspaceOnboardingStorageKey(userId), WORKSPACE_ONBOARDING_DISMISSED_VALUE);
  };

  const dismiss = () => {
    markExperienced();
    closeDialog(dialogRef.current);
    setVisible(false);
    onDismiss?.();
    requestAnimationFrame(() => {
      const composer = document.querySelector(
        '.cc-empty-composer-wrap textarea:not([disabled]), textarea.v3-composer-input:not([disabled]), .cc-sidebar-skillhub-entry[aria-current="page"]:not([disabled])',
      );
      if (composer?.isConnected) composer.focus();
    });
  };

  const openPlans = () => {
    markExperienced();
    closeDialog(dialogRef.current);
    setVisible(false);
    onDismiss?.();
    onChoosePlan?.();
  };

  if (!initiallyVisible || !visible) return null;
  return createPortal(
    <>
      <dialog ref={dialogRef} className="cc-workspace-onboarding-card cc-workspace-onboarding-choice" aria-labelledby="workspace-onboarding-title" aria-describedby="workspace-onboarding-description" onCancel={(event) => { event.preventDefault(); dismiss(); }}>
        <button type="button" className="cc-workspace-onboarding-close" aria-label="稍后设置助手" onClick={dismiss}><X size={20} aria-hidden="true" /></button>
        <header className="cc-workspace-onboarding-heading">
          <p className="cc-workspace-onboarding-greeting">欢迎来到 <span className="cc-workspace-onboarding-brand">CatsCo</span></p>
          <h1 id="workspace-onboarding-title" ref={headingRef} tabIndex={-1} data-cc-focus-group="true">开始使用你的助手</h1>
          <p id="workspace-onboarding-description">添加共享助手，或选择套餐获取自己的助手。</p>
        </header>
        <div className="cc-workspace-onboarding-methods">
          <section className="cc-workspace-onboarding-method" aria-labelledby="workspace-cloud-title">
            <div className="cc-workspace-onboarding-method-heading"><Ticket size={20} strokeWidth={1.8} aria-hidden="true" /><h2 id="workspace-cloud-title">添加共享助手</h2></div>
            <p>使用邀请码，添加他人或官方分享的云端助手。</p>
            <div className="cc-workspace-onboarding-method-actions">
              <button type="button" className="cc-workspace-onboarding-help" aria-haspopup="dialog" onClick={() => setHelpKind('cloud')}>使用说明</button>
              <button type="button" className="cc-workspace-onboarding-action" aria-haspopup="dialog" onClick={() => setInviteOpen(true)}>使用邀请码<ChevronRight size={14} aria-hidden="true" /></button>
            </div>
          </section>
          <section className="cc-workspace-onboarding-method" aria-labelledby="workspace-plans-title">
            <div className="cc-workspace-onboarding-method-heading"><Layers3 size={20} strokeWidth={1.8} aria-hidden="true" /><h2 id="workspace-plans-title">获取专属助手</h2></div>
            <p>获取自己的云端助手，按需选择 Token 额度。</p>
            <div className="cc-workspace-onboarding-method-actions">
              <button type="button" className="cc-workspace-onboarding-help" aria-haspopup="dialog" onClick={() => setHelpKind('plans')}>套餐说明</button>
              <button type="button" className="cc-workspace-onboarding-action" aria-haspopup="dialog" onClick={openPlans}>查看套餐<ChevronRight size={14} aria-hidden="true" /></button>
            </div>
          </section>
        </div>
        <footer className="cc-workspace-onboarding-footer">
          <button type="button" className="cc-workspace-onboarding-start" onClick={dismiss}>稍后再说</button>
        </footer>
      </dialog>
      {helpKind && <AssistantHelpDialog kind={helpKind} onClose={() => setHelpKind(null)} />}
      {inviteOpen && <AssistantInviteDialog onPersist={markExperienced} onComplete={dismiss} onClose={() => setInviteOpen(false)} />}
    </>, document.body,
  );
}
