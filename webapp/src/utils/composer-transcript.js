export function insertTranscriptAtSelection(transcript, insertion, textarea, fallbackValue = '') {
  const text = String(transcript || '').trim();
  if (!text) return null;

  const baseValue = insertion?.baseValue ?? (textarea ? textarea.value : String(fallbackValue || ''));
  const rawStart = insertion?.start ?? (textarea ? textarea.selectionStart : baseValue.length);
  const rawEnd = insertion?.end ?? (textarea ? textarea.selectionEnd : rawStart);
  const start = Math.min(baseValue.length, Math.max(0, Number(rawStart) || 0));
  const end = Math.min(baseValue.length, Math.max(start, Number(rawEnd) || start));

  return {
    baseValue,
    value: baseValue.slice(0, start) + text + baseValue.slice(end),
    caret: start + text.length,
  };
}
