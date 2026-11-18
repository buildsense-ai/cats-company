import React, { forwardRef, useImperativeHandle, useLayoutEffect, useRef } from 'react';
import './sliding-segments.css';

const SlidingSegments = forwardRef(function SlidingSegments({ children, className = '', value, keyboardTabs = false, onKeyDown, ...props }, forwardedRef) {
  const containerRef = useRef(null);
  const indicatorRef = useRef(null);
  useImperativeHandle(forwardedRef, () => containerRef.current);

  useLayoutEffect(() => {
    const container = containerRef.current;
    const indicator = indicatorRef.current;
    const measure = () => {
      const active = Array.from(container.children).find(child => child.matches(
        'button[aria-selected="true"], button[aria-pressed="true"], button.active, button[aria-checked="true"]',
      ));
      indicator.hidden = !active || active.offsetWidth === 0;
      if (!active) return;
      indicator.style.width = `${active.offsetWidth}px`;
      indicator.style.transform = `translateX(${active.offsetLeft}px)`;
    };
    measure();
    const readyFrame = requestAnimationFrame(() => { container.dataset.segmentsReady = 'true'; });
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(measure) : null;
    observer?.observe(container);
    Array.from(container.children).forEach(child => {
      if (child.tagName === 'BUTTON') observer?.observe(child);
    });
    window.addEventListener('resize', measure);
    return () => {
      cancelAnimationFrame(readyFrame);
      observer?.disconnect();
      window.removeEventListener('resize', measure);
    };
  }, [children, value]);

  const handleKeyDown = (event) => {
    onKeyDown?.(event);
    if (event.defaultPrevented || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    const tabs = [...event.currentTarget.querySelectorAll('button[role="tab"]:not(:disabled)')];
    const index = tabs.indexOf(event.target);
    if (index < 0) return;

    const nextIndex = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1
      : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    event.preventDefault();
    tabs[nextIndex].focus();
    if (nextIndex !== index) tabs[nextIndex].click();
  };

  const items = keyboardTabs ? React.Children.map(children, child => (
    React.isValidElement(child) && child.props.role === 'tab'
      ? React.cloneElement(child, { tabIndex: child.props['aria-selected'] ? 0 : -1 })
      : child
  )) : children;

  return (
    <div {...props} onKeyDown={keyboardTabs ? handleKeyDown : onKeyDown} ref={containerRef} className={`cc-sliding-segments ${className}`}>
      <span ref={indicatorRef} className="cc-segment-indicator" aria-hidden="true" />
      {items}
    </div>
  );
});

export default SlidingSegments;
