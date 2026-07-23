import React, { useEffect, useRef, useState } from 'react';

function titleInputSize(value) {
  const units = Array.from(String(value || '')).reduce((total, character) => (
    total + (character.codePointAt(0) > 0xff ? 2 : 1)
  ), 0);
  return Math.min(40, Math.max(6, units + 2));
}

export default function EditableConversationTitle({ title, onSave, editable = false }) {
  const normalizedTitle = String(title || '');
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(normalizedTitle);
  const [saving, setSaving] = useState(false);
  const inputRef = useRef(null);
  const savingRef = useRef(false);
  const cancellingRef = useRef(false);

  useEffect(() => {
    if (!editing) setDraft(normalizedTitle);
  }, [editing, normalizedTitle]);

  useEffect(() => {
    if (!editing || !inputRef.current) return;
    inputRef.current.focus();
    inputRef.current.select();
  }, [editing]);

  const cancelEditing = () => {
    cancellingRef.current = true;
    setDraft(normalizedTitle);
    setEditing(false);
  };

  const saveTitle = async () => {
    if (savingRef.current) return;
    if (cancellingRef.current) {
      cancellingRef.current = false;
      return;
    }

    const nextTitle = draft.trim();
    if (!nextTitle || nextTitle === normalizedTitle.trim() || typeof onSave !== 'function') {
      setDraft(normalizedTitle);
      setEditing(false);
      return;
    }

    savingRef.current = true;
    setSaving(true);
    try {
      await onSave(nextTitle);
      setEditing(false);
    } catch {
      inputRef.current?.focus();
      inputRef.current?.select();
    } finally {
      savingRef.current = false;
      setSaving(false);
    }
  };

  if (!editable) {
    return <strong className="v3-shell-title">{normalizedTitle}</strong>;
  }

  if (editing) {
    return (
      <input
        ref={inputRef}
        className="v3-shell-title-input"
        value={draft}
        size={titleInputSize(draft)}
        onChange={(event) => setDraft(event.target.value)}
        onBlur={saveTitle}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            saveTitle();
          } else if (event.key === 'Escape') {
            event.preventDefault();
            cancelEditing();
          }
        }}
        aria-label={`修改对话标题 ${normalizedTitle}`}
        maxLength={80}
        disabled={saving}
      />
    );
  }

  return (
    <button
      type="button"
      className="v3-shell-title v3-shell-title-button"
      onClick={() => {
        cancellingRef.current = false;
        setDraft(normalizedTitle);
        setEditing(true);
      }}
      aria-label={`修改对话标题 ${normalizedTitle}`}
      title="点击修改对话标题"
    >
      {normalizedTitle}
    </button>
  );
}
