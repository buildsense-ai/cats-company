import { useLayoutEffect, useRef } from 'react';

const dialogStack = [];
const inertOwners = new WeakMap();
const FOCUSABLE = 'a[href],button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex]:not([tabindex="-1"])';

function visible(element) {
  if (element.closest('[hidden], [inert], [aria-hidden="true"]')) return false;
  for (let node = element; node instanceof HTMLElement; node = node.parentElement) {
    const style = getComputedStyle(node);
    if (style.display === 'none' || style.visibility === 'hidden') return false;
  }
  return true;
}

function containsFocus(dialog, target) {
  if (dialog.contains(target)) return true;
  // A select/menu can live in a body portal while still belonging to this dialog.
  return [...dialog.querySelectorAll('[aria-controls]')].some((control) => (
    control.getAttribute('aria-controls').split(/\s+/).some((id) => document.getElementById(id)?.contains(target))
  ));
}

function isolateBackground(dialog) {
  const isolated = [];
  for (let branch = dialog; branch.parentElement; branch = branch.parentElement) {
    [...branch.parentElement.children].forEach((sibling) => {
      if (sibling === branch || !(sibling instanceof HTMLElement)
        || ['SCRIPT', 'STYLE', 'LINK'].includes(sibling.tagName)) return;
      const record = inertOwners.get(sibling) || { count: 0, originallyInert: sibling.hasAttribute('inert') };
      record.count += 1;
      inertOwners.set(sibling, record);
      sibling.setAttribute('inert', '');
      isolated.push(sibling);
    });
    if (branch.parentElement === document.body) break;
  }
  return () => isolated.forEach((element) => {
    const record = inertOwners.get(element);
    if (!record || --record.count > 0) return;
    if (!record.originallyInert) element.removeAttribute('inert');
    inertOwners.delete(element);
  });
}

// Shared behavior only: callers keep their existing dialog markup and visual styles.
export default function useDialogBehavior(dialogRef, { onClose, initialFocusRef, returnFocusRef, open = true } = {}) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useLayoutEffect(() => {
    const dialog = dialogRef.current;
    if (!open || !dialog) return undefined;
    const opener = document.activeElement;
    const releaseBackground = isolateBackground(dialog);
    const token = {};
    dialogStack.push(token);
    const isTopmost = () => dialogStack.at(-1) === token;
    const focusables = () => [...dialog.querySelectorAll(FOCUSABLE)].filter(visible).sort((left, right) => (
      left.compareDocumentPosition(right) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1
    ));
    const focusFirst = () => (initialFocusRef?.current || focusables()[0] || dialog).focus({ preventScroll: true });
    focusFirst();

    const handleFocus = (event) => {
      if (isTopmost() && !containsFocus(dialog, event.target)) focusFirst();
    };
    const handleKey = (event) => {
      if (!isTopmost() || event.defaultPrevented || event.isComposing || event.keyCode === 229) return;
      if (event.key === 'Escape') {
        event.preventDefault();
        event.stopImmediatePropagation();
        closeRef.current?.();
      } else if (event.key === 'Tab') {
        const nodes = focusables();
        const active = document.activeElement;
        if (!nodes.length) {
          event.preventDefault();
          dialog.focus();
        } else if (!containsFocus(dialog, active)
          || (!event.shiftKey && active === nodes.at(-1))
          || (event.shiftKey && active === nodes[0])) {
          event.preventDefault();
          (event.shiftKey ? nodes.at(-1) : nodes[0]).focus();
        }
      }
    };
    document.addEventListener('focusin', handleFocus);
    document.addEventListener('keydown', handleKey);
    return () => {
      document.removeEventListener('focusin', handleFocus);
      document.removeEventListener('keydown', handleKey);
      dialogStack.splice(dialogStack.indexOf(token), 1);
      releaseBackground();
      // A menu item may unmount before the dialog opens. Prefer its stable trigger.
      const returnTarget = [returnFocusRef?.current, opener].find((element) => (
        element instanceof HTMLElement && element.isConnected && !element.matches(':disabled') && visible(element)
      ));
      if (returnTarget) {
        returnTarget.focus({ preventScroll: true });
      }
    };
  }, [dialogRef, initialFocusRef, returnFocusRef, open]);
}
