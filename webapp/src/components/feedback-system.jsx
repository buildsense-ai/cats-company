import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import {
  AlertTriangle,
  CheckCircle2,
  Info,
  X,
} from 'lucide-react';

const FeedbackContext = createContext(null);
const DEFAULT_TOAST_DURATION = 4200;
let feedbackSequence = 0;

function nextFeedbackId(prefix) {
  feedbackSequence += 1;
  return `${prefix}-${feedbackSequence}`;
}

function normalizeToast(input, options = {}) {
  const source = typeof input === 'string' ? { message: input } : (input || {});
  return {
    id: nextFeedbackId('toast'),
    tone: source.tone || options.tone || 'info',
    title: source.title || options.title || '',
    message: source.message || options.message || '',
    duration: Number.isFinite(source.duration)
      ? source.duration
      : (Number.isFinite(options.duration) ? options.duration : DEFAULT_TOAST_DURATION),
  };
}

function normalizeConfirmation(input) {
  const source = typeof input === 'string' ? { message: input } : (input || {});
  return {
    title: source.title || '确认操作',
    message: source.message || '',
    confirmLabel: source.confirmLabel || '确认',
    cancelLabel: source.cancelLabel || '取消',
    tone: source.tone || 'default',
  };
}

function fallbackConfirm(input) {
  const confirmation = normalizeConfirmation(input);
  const text = [confirmation.title, confirmation.message].filter(Boolean).join('\n\n');
  return Promise.resolve(typeof window !== 'undefined' && typeof window.confirm === 'function'
    ? window.confirm(text)
    : false);
}

function fallbackNotify(input, options) {
  const toast = normalizeToast(input, options);
  if (typeof window !== 'undefined' && typeof window.alert === 'function' && toast.tone === 'error') {
    window.alert(toast.message || toast.title);
  }
  return toast.id;
}

const FALLBACK_FEEDBACK = {
  confirm: fallbackConfirm,
  notify: fallbackNotify,
  dismissToast: () => {},
};

function ToastIcon({ tone }) {
  if (tone === 'success') return <CheckCircle2 aria-hidden="true" />;
  if (tone === 'error' || tone === 'warning') return <AlertTriangle aria-hidden="true" />;
  return <Info aria-hidden="true" />;
}

export function InlineFeedback({
  tone = 'info',
  title = '',
  children,
  className = '',
}) {
  const role = tone === 'error' ? 'alert' : 'status';
  return (
    <div
      className={`cc-inline-feedback cc-inline-feedback-${tone} ${className}`.trim()}
      role={role}
      aria-live={tone === 'error' ? 'assertive' : 'polite'}
    >
      <ToastIcon tone={tone} />
      <div>
        {title && <strong>{title}</strong>}
        {children && <span>{children}</span>}
      </div>
    </div>
  );
}

function ToastViewport({ toasts, onDismiss }) {
  return (
    <div className="cc-toast-viewport" aria-label="操作通知">
      {toasts.map((toast) => (
        <div
          key={toast.id}
          className={`cc-toast cc-toast-${toast.tone}`}
          role={toast.tone === 'error' ? 'alert' : 'status'}
          aria-live={toast.tone === 'error' ? 'assertive' : 'polite'}
        >
          <span className="cc-toast-icon">
            <ToastIcon tone={toast.tone} />
          </span>
          <div className="cc-toast-copy">
            {toast.title && <strong>{toast.title}</strong>}
            {toast.message && <span>{toast.message}</span>}
          </div>
          <button
            type="button"
            className="cc-toast-close"
            onClick={() => onDismiss(toast.id)}
            aria-label="关闭通知"
          >
            <X size={16} />
          </button>
        </div>
      ))}
    </div>
  );
}

function ConfirmDialog({ confirmation, onResolve }) {
  const dialogRef = useRef(null);
  const cancelButtonRef = useRef(null);
  const titleId = useMemo(() => nextFeedbackId('confirm-title'), []);
  const descriptionId = useMemo(() => nextFeedbackId('confirm-description'), []);

  useEffect(() => {
    const previousFocus = document.activeElement;
    cancelButtonRef.current?.focus();
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onResolve(false);
        return;
      }
      if (event.key === 'Tab') {
        const focusable = dialogRef.current?.querySelectorAll(
          'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
        );
        if (!focusable?.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      if (previousFocus instanceof HTMLElement) previousFocus.focus();
    };
  }, [onResolve]);

  return (
    <div
      className="oc-modal-overlay cc-confirm-overlay"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onResolve(false);
      }}
    >
      <section
        ref={dialogRef}
        className="oc-modal cc-confirm-dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={confirmation.message ? descriptionId : undefined}
      >
        <div className="cc-confirm-copy">
          <h2 id={titleId}>{confirmation.title}</h2>
          {confirmation.message && <p id={descriptionId}>{confirmation.message}</p>}
        </div>
        <button
          type="button"
          className="cc-confirm-close"
          onClick={() => onResolve(false)}
          aria-label="关闭确认"
        >
          <X size={18} />
        </button>
        <div className="cc-confirm-actions">
          <button
            ref={cancelButtonRef}
            type="button"
            className="oc-btn cc-confirm-cancel"
            onClick={() => onResolve(false)}
          >
            {confirmation.cancelLabel}
          </button>
          <button
            type="button"
            className={`oc-btn cc-confirm-submit ${confirmation.tone === 'danger' ? 'is-danger' : ''}`.trim()}
            onClick={() => onResolve(true)}
          >
            {confirmation.confirmLabel}
          </button>
        </div>
      </section>
    </div>
  );
}

export function FeedbackProvider({ children }) {
  const [toasts, setToasts] = useState([]);
  const [confirmation, setConfirmation] = useState(null);
  const timersRef = useRef(new Map());
  const confirmationRef = useRef(null);

  const dismissToast = useCallback((toastId) => {
    const timer = timersRef.current.get(toastId);
    if (timer) window.clearTimeout(timer);
    timersRef.current.delete(toastId);
    setToasts((current) => current.filter((toast) => toast.id !== toastId));
  }, []);

  const notify = useCallback((input, options = {}) => {
    const toast = normalizeToast(input, options);
    setToasts((current) => {
      const next = [...current, toast];
      next.slice(0, -4).forEach((dropped) => {
        const timer = timersRef.current.get(dropped.id);
        if (timer) window.clearTimeout(timer);
        timersRef.current.delete(dropped.id);
      });
      return next.slice(-4);
    });
    if (toast.duration > 0) {
      const timer = window.setTimeout(() => dismissToast(toast.id), toast.duration);
      timersRef.current.set(toast.id, timer);
    }
    return toast.id;
  }, [dismissToast]);

  const resolveConfirmation = useCallback((accepted) => {
    const active = confirmationRef.current;
    if (!active) return;
    confirmationRef.current = null;
    setConfirmation(null);
    active.resolve(Boolean(accepted));
  }, []);

  const confirm = useCallback((input) => {
    const nextConfirmation = normalizeConfirmation(input);
    if (confirmationRef.current) {
      confirmationRef.current.resolve(false);
    }
    return new Promise((resolve) => {
      const active = { ...nextConfirmation, resolve };
      confirmationRef.current = active;
      setConfirmation(active);
    });
  }, []);

  useEffect(() => () => {
    timersRef.current.forEach((timer) => window.clearTimeout(timer));
    timersRef.current.clear();
    if (confirmationRef.current) confirmationRef.current.resolve(false);
    confirmationRef.current = null;
  }, []);

  const value = useMemo(() => ({
    confirm,
    notify,
    dismissToast,
  }), [confirm, dismissToast, notify]);

  const canPortal = typeof document !== 'undefined' && document.body;

  return (
    <FeedbackContext.Provider value={value}>
      {children}
      {canPortal && createPortal(
        <ToastViewport toasts={toasts} onDismiss={dismissToast} />,
        document.body,
      )}
      {canPortal && confirmation && createPortal(
        <ConfirmDialog confirmation={confirmation} onResolve={resolveConfirmation} />,
        document.body,
      )}
    </FeedbackContext.Provider>
  );
}

export function useFeedback() {
  return useContext(FeedbackContext) || FALLBACK_FEEDBACK;
}
