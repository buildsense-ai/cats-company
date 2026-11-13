import React, { useEffect, useRef, useState } from 'react';
import { Search, X } from 'lucide-react';

export default function ToolbarSearch({
  id, label, clearLabel, className, value = '', onChange, onSubmit,
  submitting = false, submitLabel = '搜索',
}) {
  const [expanded, setExpanded] = useState(Boolean(value));
  const inputRef = useRef(null);
  const triggerRef = useRef(null);
  const open = expanded || Boolean(value);

  useEffect(() => {
    if (open) inputRef.current?.focus();
  }, [open]);

  const collapse = () => {
    if (value) onChange('');
    setExpanded(false);
    requestAnimationFrame(() => triggerRef.current?.focus());
  };

  if (!open) return (
    <button ref={triggerRef} type='button' className='icon-button cc-skillhub-search-toggle'
      aria-label={label} title={label} aria-expanded={false} aria-controls={id}
      onClick={() => setExpanded(true)}>
      <Search size={16} aria-hidden='true' />
    </button>
  );

  return (
    <form className={className} role='search'
      onKeyDown={event => {
        if (event.key === 'Escape') {
          event.preventDefault();
          event.stopPropagation();
          collapse();
        }
      }}
      onBlur={event => {
        if (!event.currentTarget.contains(event.relatedTarget) && !value) setExpanded(false);
      }}
      onSubmit={event => { event.preventDefault(); onSubmit?.(value); }}>
      <Search size={16} aria-hidden='true' />
      <div className='cc-skillhub-search-field'>
        <input ref={inputRef} id={id} type='search' autoComplete='off' aria-label={label}
          value={value} onChange={event => onChange(event.target.value)} placeholder={`${label}…`} />
        <button type='button' className='cc-skillhub-search-clear'
          aria-label={value ? clearLabel : '收起搜索'} title={value ? '清除' : '收起搜索'}
          onClick={() => {
            if (value) { onChange(''); inputRef.current?.focus(); } else collapse();
          }}><X size={14} aria-hidden='true' /></button>
      </div>
      {onSubmit && <button type='submit' className='cc-skillhub-search-submit' disabled={submitting}
        aria-label={submitLabel} title={submitLabel}><Search size={16} aria-hidden='true' /></button>}
    </form>
  );
}
