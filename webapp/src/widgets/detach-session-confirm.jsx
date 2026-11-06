import React, { useCallback, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import useDialogBehavior from '../utils/use-dialog-behavior';

// Opening an application in its own browser tab detaches it from the
// conversation: the new tab is an ordinary page, it does not carry the
// conversation's open binding, and anything produced there no longer lands in
// the conversation that launched it. That is a real loss of context, so the
// user gets told before it happens rather than discovering it afterwards.
export const DETACH_SESSION_CONFIRM_MESSAGE = '新页面打开，则不再绑定会话';

export const DETACH_SESSION_CONFIRM_ACTION = '仍然打开';

function appLabel(app) {
  return String(app?.title || app?.id || '').trim();
}

export function DetachSessionConfirm({ app, onCancel, onConfirm }) {
  const dialogRef = useRef(null);
  const cancelRef = useRef(null);
  const descriptionID = React.useId();
  useDialogBehavior(dialogRef, { onClose: onCancel, initialFocusRef: cancelRef });
  const name = appLabel(app);
  const dialog = (
    <div className="cloud-artifact-confirm-backdrop" onClick={onCancel}>
      <div
        ref={dialogRef}
        tabIndex={-1}
        className="cloud-artifact-confirm"
        role="alertdialog"
        aria-modal="true"
        aria-label="在新页面打开应用"
        aria-describedby={descriptionID}
        onClick={(event) => event.stopPropagation()}
      >
        <h4>{name ? `在新页面打开“${name}”？` : '在新页面打开应用？'}</h4>
        <p id={descriptionID}>{DETACH_SESSION_CONFIRM_MESSAGE}</p>
        <div className="cloud-artifact-confirm-actions">
          <button ref={cancelRef} type="button" onClick={onCancel}>取消</button>
          <button type="button" onClick={onConfirm}>{DETACH_SESSION_CONFIRM_ACTION}</button>
        </div>
      </div>
    </div>
  );
  // The dialog is raised from inside the application panel, which sits under
  // several ancestors that dim, blur or clip their own children. Rendering it
  // in place let those ancestors wash the card out until the chat showed
  // through it. A portal puts the card on the document body, where only its own
  // styles decide how solid it looks.
  if (typeof document === 'undefined') return dialog;
  return createPortal(dialog, document.body);
}

// One confirmation per caller. Components that own several "open in a new tab"
// entrances share a single instance, so two dialogs can never stack up.
export function useDetachSessionConfirm() {
  const [pending, setPending] = useState(null);
  const actionRef = useRef(null);

  const requestDetach = useCallback((app, action) => {
    if (typeof action !== 'function') return;
    actionRef.current = action;
    setPending({ app: app ? { id: app.id, title: app.title } : null });
  }, []);

  const cancel = useCallback(() => {
    actionRef.current = null;
    setPending(null);
  }, []);

  const confirm = useCallback(() => {
    const action = actionRef.current;
    actionRef.current = null;
    setPending(null);
    if (action) action();
  }, []);

  const dialog = pending
    ? <DetachSessionConfirm app={pending.app} onCancel={cancel} onConfirm={confirm} />
    : null;

  return { requestDetach, dialog, pending: Boolean(pending) };
}

export default DetachSessionConfirm;
