// A confirmation the user can actually read.
//
// `window.confirm` blocks the page, looks like a browser warning rather than part of the
// product, and on some webviews it is suppressed entirely — which silently answers "yes".
// This dialog takes the same two decisions with the document names laid out plainly, and it
// lives inside the page instead of on top of the browser's own chrome.

import React from 'react';

export default function ConfirmDialog({
  title, lines = [], hint, confirmLabel = '一起发送', cancelLabel = '只发文字',
  dismissLabel = '', onConfirm, onCancel, onDismiss,
}) {
  return (
    <div className="office-confirm-backdrop" data-office-confirm="">
      <div className="office-confirm" role="dialog" aria-modal="true" aria-label={title}>
        <div className="office-confirm-title">{title}</div>
        {lines.length > 0 ? (
          <ul className="office-confirm-list">
            {lines.map(name => <li key={name}>{name}</li>)}
          </ul>
        ) : null}
        {hint ? <p className="office-confirm-hint">{hint}</p> : null}
        <div className="office-confirm-actions">
          {dismissLabel ? (
            <button type="button" className="office-confirm-btn is-quiet"
              data-office-confirm-dismiss="" onClick={onDismiss}>
              {dismissLabel}
            </button>
          ) : null}
          <span className="office-confirm-spacer" />
          {cancelLabel ? (
            <button type="button" className="office-confirm-btn"
              data-office-confirm-cancel="" onClick={onCancel}>
              {cancelLabel}
            </button>
          ) : null}
          <button type="button" className="office-confirm-btn is-primary"
            data-office-confirm-ok="" autoFocus onClick={onConfirm}>
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
