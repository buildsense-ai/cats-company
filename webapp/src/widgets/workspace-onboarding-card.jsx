import React, { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { ChevronRight, Layers3, Ticket, X } from 'lucide-react';
import { api } from '../api';
import { describePlanInviteError } from '../utils/plan-invite';
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

function PlanInviteDialog({ onPersist, onComplete, onClose }) {
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

  const dismiss = () => {
    if (submitting) return;
    if (completed) finish();
    else onClose();
  };

  const submit = async (event) => {
    event.preventDefault();
    const code = invite.trim();
    if (!code || submitting || completed) return;

    setSubmitting(true);
    setError('');
    try {
      await api.redeemRelayInvite(code);
      onPersist();
      window.dispatchEvent(new Event('cc:data-changed'));
      setCompleted(true);
    } catch (requestError) {
      setError(describePlanInviteError(requestError));
    } finally {
      setSubmitting(false);
    }
  };

  return <dialog ref={dialogRef} className="cc-workspace-onboarding-card cc-workspace-onboarding-invite-dialog" aria-labelledby="workspace-invite-title" aria-describedby="workspace-invite-description" onCancel={(event) => { event.preventDefault(); dismiss(); }}>
    <button type="button" className="cc-workspace-onboarding-close" aria-label="关闭套餐邀请码窗口" disabled={submitting} onClick={dismiss}><X size={20} aria-hidden="true" /></button>
    <header className="cc-workspace-onboarding-heading">
      <h2 id="workspace-invite-title" ref={titleRef} tabIndex={-1} data-cc-focus-group="true">{completed ? '兑换成功' : '套餐邀请码'}</h2>
      <p id="workspace-invite-description">{completed ? '套餐权益已加入当前账号，可在「套餐与权益」中查看。' : '输入套餐邀请码，将对应权益兑换到当前账号。'}</p>
    </header>
    {completed ? <>
      <footer className="cc-workspace-onboarding-invite-footer"><button type="button" className="cc-workspace-onboarding-action" onClick={finish}>完成</button></footer>
    </> : <form className="cc-workspace-onboarding-invite-form" onSubmit={submit}>
      <label htmlFor="workspace-onboarding-invite">套餐邀请码</label>
      <input ref={inputRef} id="workspace-onboarding-invite" placeholder="粘贴套餐邀请码" value={invite} autoComplete="off" autoCapitalize="off" spellCheck={false} disabled={submitting} onChange={(event) => { setError(''); setInvite(event.target.value); }} onKeyDown={(event) => { if (event.key === 'Enter' && event.nativeEvent.isComposing) event.preventDefault(); }} />
      {error && <p className="cc-workspace-onboarding-invite-error" role="alert">{error}</p>}
      <footer className="cc-workspace-onboarding-invite-footer">
        <button type="button" disabled={submitting} onClick={dismiss}>取消</button>
        <button type="submit" className="cc-workspace-onboarding-action" disabled={submitting || !invite.trim()}>{submitting ? '兑换中…' : '兑换套餐'}</button>
      </footer>
    </form>}
  </dialog>;
}

function OnboardingHelpDialog({ kind, onClose }) {
  const dialogRef = useRef(null);
  const titleRef = useRef(null);
  const isInvite = kind === 'invite';
  const steps = isInvite ? [
    ['填写邀请码', '返回引导，点击「兑换套餐邀请码」，粘贴你收到的套餐邀请码。'],
    ['确认兑换', '点击「兑换套餐」，对应权益会加入当前登录的账号。'],
    ['查看权益', '兑换后可在「套餐与权益」中查看已获得的内容。'],
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
      <h2 id="workspace-assistant-help-title" ref={titleRef} tabIndex={-1} data-cc-focus-group="true">{isInvite ? '套餐邀请码说明' : '套餐选择说明'}</h2>
    </header>
    <div className="cc-workspace-onboarding-help-body" role="region" aria-label="使用说明正文" tabIndex={0}>
      <ol className="cc-workspace-onboarding-help-steps">
        {steps.map(([title, description]) => <li key={title}><h3>{title}</h3><p>{description}</p></li>)}
      </ol>
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
          <p id="workspace-onboarding-description">兑换或选购套餐，开始使用你的云端助手。</p>
        </header>
        <div className="cc-workspace-onboarding-methods">
          <section className="cc-workspace-onboarding-method" aria-labelledby="workspace-plan-invite-title">
            <div className="cc-workspace-onboarding-method-heading"><Ticket size={20} strokeWidth={1.8} aria-hidden="true" /><h2 id="workspace-plan-invite-title">已有邀请码</h2></div>
            <p>兑换邀请码，将套餐权益加入当前账号。</p>
            <div className="cc-workspace-onboarding-method-actions">
              <button type="button" className="cc-workspace-onboarding-help" aria-haspopup="dialog" onClick={() => setHelpKind('invite')}>使用说明</button>
              <button type="button" className="cc-workspace-onboarding-action" aria-haspopup="dialog" onClick={() => setInviteOpen(true)}>兑换套餐邀请码<ChevronRight size={14} aria-hidden="true" /></button>
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
      {helpKind && <OnboardingHelpDialog kind={helpKind} onClose={() => setHelpKind(null)} />}
      {inviteOpen && <PlanInviteDialog onPersist={markExperienced} onComplete={dismiss} onClose={() => setInviteOpen(false)} />}
    </>, document.body,
  );
}
