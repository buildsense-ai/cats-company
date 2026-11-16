// A textarea whose visible value is local until a frame or blur flushes it upward.
//
// Typing must repaint immediately without re-running the canvas model on every key. The
// commit is coalesced to one animation frame, while blur and IME completion flush soon
// enough that a selection change cannot strand the last character.

import React, { useCallback, useEffect, useRef, useState } from 'react';

function textValue(value) {
  return value === null || value === undefined ? '' : String(value);
}

export default function LocalDraftTextarea({
  value,
  onCommit,
  disabled = false,
  rows = 2,
  className,
  ...rest
}) {
  const [local, setLocal] = useState(() => textValue(value));
  const localRef = useRef(local);
  const elementRef = useRef(null);
  const frameRef = useRef(null);
  const composingRef = useRef(false);
  const committedRef = useRef(textValue(value));
  const commitRef = useRef(onCommit);
  commitRef.current = onCommit;

  const flush = useCallback(() => {
    if (frameRef.current !== null) {
      cancelAnimationFrame(frameRef.current);
      frameRef.current = null;
    }
    if (composingRef.current) return;
    const next = localRef.current;
    if (next === committedRef.current) return;
    committedRef.current = next;
    commitRef.current?.(next);
  }, []);

  const schedule = useCallback(() => {
    if (frameRef.current !== null || composingRef.current) return;
    frameRef.current = requestAnimationFrame(() => {
      frameRef.current = null;
      flush();
    });
  }, [flush]);

  useEffect(() => {
    const next = textValue(value);
    const focused = typeof document !== 'undefined' && document.activeElement === elementRef.current;
    // While focused, local input is authoritative. Once focus leaves, adopt external model
    // changes (undo, AI update, page reload) without overwriting an in-progress composition.
    if (!focused && !composingRef.current && next !== localRef.current) {
      localRef.current = next;
      committedRef.current = next;
      setLocal(next);
    }
  }, [value]);

  useEffect(() => () => {
    if (frameRef.current !== null) cancelAnimationFrame(frameRef.current);
    // A selection change can unmount this field before the coalesced frame runs. Flush the
    // last local value upward so switching elements never strands a character.
    if (!composingRef.current && localRef.current !== committedRef.current) {
      committedRef.current = localRef.current;
      commitRef.current?.(localRef.current);
    }
  }, []);

  return (
    <textarea
      {...rest}
      ref={elementRef}
      rows={rows}
      className={className}
      disabled={disabled}
      value={local}
      data-office-text-input=""
      onChange={(event) => {
        const next = event.target.value;
        localRef.current = next;
        setLocal(next);
        schedule();
      }}
      onCompositionStart={() => { composingRef.current = true; }}
      onCompositionEnd={(event) => {
        composingRef.current = false;
        localRef.current = event.currentTarget.value;
        setLocal(event.currentTarget.value);
        schedule();
      }}
      onBlur={flush}
    />
  );
}

export { textValue as localDraftTextValue };
