export async function copyTextToClipboard(text) {
  const value = String(text || '');
  if (!value) return;

  if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // A focused document can still succeed with the legacy fallback.
    }
  }

  if (typeof document === 'undefined') {
    throw new Error('Clipboard is unavailable');
  }
  const textarea = document.createElement('textarea');
  textarea.value = value;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.left = '-9999px';
  document.body.appendChild(textarea);
  textarea.select();
  try {
    if (document.execCommand('copy') === false) {
      throw new Error('Copy command failed');
    }
  } finally {
    textarea.remove();
  }
}
