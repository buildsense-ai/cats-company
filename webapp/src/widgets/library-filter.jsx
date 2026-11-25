import React, { useEffect, useId, useRef, useState } from 'react';
import { ListFilter } from 'lucide-react';
import CustomSelect from './custom-select';
import './library-filter.css';

function isInsideFilter(root, target) {
  if (!root || !(target instanceof Node)) return false;
  if (root.contains(target)) return true;
  const listbox = target instanceof Element ? target.closest('[role="listbox"]') : null;
  return Boolean(listbox?.id && Array.from(root.querySelectorAll('button[aria-haspopup="listbox"][aria-controls]'))
    .some(button => button.getAttribute('aria-controls') === listbox.id));
}

export function LibraryFilterSelect({ label, children, ...props }) {
  return (
    <div className="cc-library-filter-field">
      <span>{label}</span>
      <CustomSelect {...props} ariaLabel={label} listboxAriaLabel={label}
        density="comfortable" triggerClassName="cc-library-filter-select-trigger">
        {children}
      </CustomSelect>
    </div>
  );
}

export default function LibraryFilter({ label, children, triggerRef: providedTriggerRef }) {
  const id = useId();
  const rootRef = useRef(null);
  const localTriggerRef = useRef(null);
  const triggerRef = providedTriggerRef || localTriggerRef;
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return undefined;
    const closeOutside = (event) => {
      if (!isInsideFilter(rootRef.current, event.target)) setOpen(false);
    };
    document.addEventListener('pointerdown', closeOutside);
    return () => document.removeEventListener('pointerdown', closeOutside);
  }, [open]);

  return (
    <div ref={rootRef} className="cc-library-filter"
      onBlur={(event) => {
        if (event.relatedTarget && !isInsideFilter(event.currentTarget, event.relatedTarget)) setOpen(false);
      }}
      onKeyDown={(event) => {
        if (event.key !== 'Escape' || !open || event.defaultPrevented) return;
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
        triggerRef.current?.focus();
      }}>
      <button type="button" ref={triggerRef} className="cc-library-list-button"
        aria-expanded={open} aria-controls={id} onClick={() => setOpen(value => !value)}>
        <ListFilter size={16} aria-hidden="true" /> 筛选
      </button>
      <div id={id} className="cc-library-filter-panel" role="group" aria-label={label} hidden={!open}>
        {open && children}
      </div>
    </div>
  );
}
