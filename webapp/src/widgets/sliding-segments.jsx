import React, { forwardRef, useImperativeHandle, useLayoutEffect, useRef } from 'react';
import './sliding-segments.css';

const SlidingSegments = forwardRef(function SlidingSegments({ children, className = '', value, ...props }, forwardedRef) {
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

  return (
    <div {...props} ref={containerRef} className={`cc-sliding-segments ${className}`}>
      <span ref={indicatorRef} className="cc-segment-indicator" aria-hidden="true" />
      {children}
    </div>
  );
});

export default SlidingSegments;
