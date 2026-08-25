import React, {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';

const TOOLTIP_ID = 'cc-app-tooltip';
const TOOLTIP_SELECTOR = [
  'button[title]',
  'button[data-cc-tooltip]',
  '[role="button"][title]',
  '[role="button"][data-cc-tooltip]',
  'input[type="button"][title]',
  'input[type="button"][data-cc-tooltip]',
  'input[type="submit"][title]',
  'input[type="submit"][data-cc-tooltip]',
].join(',');
const POINTER_SHOW_DELAY_MS = 320;
const VIEWPORT_GUTTER = 8;
const TARGET_GAP = 10;
const ARROW_EDGE_GUTTER = 10;

function tooltipTarget(eventTarget) {
  if (!(eventTarget instanceof Element)) return null;
  return eventTarget.closest(TOOLTIP_SELECTOR);
}

function tooltipDisabled(target) {
  return Boolean(target?.closest('[data-cc-tooltips="off"]'));
}

function claimTooltipLabel(target) {
  const nativeTitle = target.getAttribute('title')?.trim() || '';
  if (nativeTitle) {
    target.dataset.ccTooltip = nativeTitle;
    target.removeAttribute('title');
    if (
      !target.hasAttribute('aria-label')
      && !target.hasAttribute('aria-labelledby')
      && !target.textContent?.trim()
    ) {
      target.setAttribute('aria-label', nativeTitle);
    }
  }
  return target.dataset.ccTooltip?.trim() || '';
}

export default function AppTooltip() {
  const [tooltip, setTooltip] = useState(null);
  const tooltipRef = useRef(null);
  const activeTargetRef = useRef(null);
  const describedByRef = useRef(null);
  const showTimerRef = useRef(null);
  const lastInteractionRef = useRef('keyboard');

  const clearShowTimer = useCallback(() => {
    if (showTimerRef.current) window.clearTimeout(showTimerRef.current);
    showTimerRef.current = null;
  }, []);

  const closeTooltip = useCallback(() => {
    clearShowTimer();
    const target = activeTargetRef.current;
    if (target?.isConnected) {
      const previous = describedByRef.current;
      if (previous) target.setAttribute('aria-describedby', previous);
      else target.removeAttribute('aria-describedby');
    }
    activeTargetRef.current = null;
    describedByRef.current = null;
    setTooltip(null);
  }, [clearShowTimer]);

  const openTooltip = useCallback((target) => {
    const label = claimTooltipLabel(target);
    if (!label || !target.isConnected) return;

    if (activeTargetRef.current !== target) {
      const previousTarget = activeTargetRef.current;
      if (previousTarget?.isConnected) {
        const previous = describedByRef.current;
        if (previous) previousTarget.setAttribute('aria-describedby', previous);
        else previousTarget.removeAttribute('aria-describedby');
      }
      describedByRef.current = target.getAttribute('aria-describedby');
    }

    activeTargetRef.current = target;
    const describedBy = [describedByRef.current, TOOLTIP_ID].filter(Boolean).join(' ');
    target.setAttribute('aria-describedby', describedBy);
    setTooltip({
      label,
      rect: target.getBoundingClientRect(),
      left: 0,
      top: 0,
      placement: 'top',
      positioned: false,
    });
  }, []);

  const scheduleTooltip = useCallback((target, delay) => {
    clearShowTimer();
    if (delay <= 0) {
      openTooltip(target);
      return;
    }
    showTimerRef.current = window.setTimeout(() => {
      showTimerRef.current = null;
      openTooltip(target);
    }, delay);
  }, [clearShowTimer, openTooltip]);

  useLayoutEffect(() => {
    if (!tooltip || !tooltipRef.current) return;
    const node = tooltipRef.current;
    const width = node.offsetWidth;
    const height = node.offsetHeight;
    const centeredLeft = tooltip.rect.left + (tooltip.rect.width / 2);
    const left = Math.min(
      window.innerWidth - VIEWPORT_GUTTER - (width / 2),
      Math.max(VIEWPORT_GUTTER + (width / 2), centeredLeft),
    );
    const hasRoomAbove = tooltip.rect.top >= height + TARGET_GAP + VIEWPORT_GUTTER;
    const placement = hasRoomAbove ? 'top' : 'bottom';
    const top = placement === 'top'
      ? tooltip.rect.top - TARGET_GAP
      : tooltip.rect.bottom + TARGET_GAP;
    const tooltipLeftEdge = left - (width / 2);
    const arrowLeft = Math.min(
      width - ARROW_EDGE_GUTTER,
      Math.max(ARROW_EDGE_GUTTER, centeredLeft - tooltipLeftEdge),
    );

    setTooltip((current) => current && ({
      ...current,
      left,
      top,
      arrowLeft,
      placement,
      positioned: true,
    }));
  }, [tooltip?.label, tooltip?.rect]);

  useEffect(() => {
    const handlePointerOver = (event) => {
      if (event.pointerType === 'touch') return;
      lastInteractionRef.current = 'pointer';
      const target = tooltipTarget(event.target);
      if (!target || target.contains(event.relatedTarget)) return;
      claimTooltipLabel(target);
      if (tooltipDisabled(target)) {
        closeTooltip();
        return;
      }
      scheduleTooltip(target, POINTER_SHOW_DELAY_MS);
    };
    const handlePointerDown = (event) => {
      lastInteractionRef.current = 'pointer';
      const target = tooltipTarget(event.target);
      if (target) target.dataset.ccPointerFocus = 'true';
      closeTooltip();
    };
    const handlePointerOut = (event) => {
      const target = tooltipTarget(event.target);
      if (!target || target.contains(event.relatedTarget)) return;
      if (document.activeElement === target) return;
      if (activeTargetRef.current === target || showTimerRef.current) closeTooltip();
    };
    const handleFocusIn = (event) => {
      const target = tooltipTarget(event.target);
      if (!target || lastInteractionRef.current === 'pointer') return;
      claimTooltipLabel(target);
      if (tooltipDisabled(target)) {
        closeTooltip();
        return;
      }
      scheduleTooltip(target, 0);
    };
    const handleFocusOut = (event) => {
      const target = tooltipTarget(event.target);
      if (target) delete target.dataset.ccPointerFocus;
      if (activeTargetRef.current === target) closeTooltip();
    };
    const handleKeyDown = (event) => {
      lastInteractionRef.current = 'keyboard';
      const activeTarget = tooltipTarget(document.activeElement);
      if (activeTarget) delete activeTarget.dataset.ccPointerFocus;
      if (event.key === 'Escape') closeTooltip();
    };

    document.addEventListener('pointerover', handlePointerOver, true);
    document.addEventListener('pointerdown', handlePointerDown, true);
    document.addEventListener('pointerout', handlePointerOut, true);
    document.addEventListener('focusin', handleFocusIn, true);
    document.addEventListener('focusout', handleFocusOut, true);
    document.addEventListener('keydown', handleKeyDown, true);
    window.addEventListener('resize', closeTooltip);
    window.addEventListener('scroll', closeTooltip, true);
    return () => {
      document.removeEventListener('pointerover', handlePointerOver, true);
      document.removeEventListener('pointerdown', handlePointerDown, true);
      document.removeEventListener('pointerout', handlePointerOut, true);
      document.removeEventListener('focusin', handleFocusIn, true);
      document.removeEventListener('focusout', handleFocusOut, true);
      document.removeEventListener('keydown', handleKeyDown, true);
      window.removeEventListener('resize', closeTooltip);
      window.removeEventListener('scroll', closeTooltip, true);
      closeTooltip();
    };
  }, [closeTooltip, scheduleTooltip]);

  if (!tooltip || typeof document === 'undefined' || !document.body) return null;

  return createPortal(
    <div
      ref={tooltipRef}
      id={TOOLTIP_ID}
      className={`cc-app-tooltip is-${tooltip.placement}${tooltip.positioned ? ' is-positioned' : ''}`}
      role="tooltip"
      style={{
        left: `${tooltip.left}px`,
        top: `${tooltip.top}px`,
        '--cc-tooltip-arrow-left': `${tooltip.arrowLeft || 10}px`,
      }}
    >
      {tooltip.label}
    </div>,
    document.body,
  );
}
