window.DOMPurify = window.DOMPurify || { sanitize: html => html };
window.hljs = window.hljs || {
  getLanguage: () => false,
  highlight: code => ({ value: code }),
  highlightAuto: code => ({ value: code }),
  highlightElement: () => {}
};
window.marked = window.marked || {
  setOptions: () => {},
  parse(text) {
    const esc = String(text || '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
    return esc
      .replace(/```([a-zA-Z0-9_-]+)?\n([\s\S]*?)```/g, '<pre><code>$2</code></pre>')
      .replace(/^### (.*)$/gm, '<h3>$1</h3>')
      .replace(/^## (.*)$/gm, '<h2>$1</h2>')
      .replace(/^# (.*)$/gm, '<h1>$1</h1>')
      .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
      .replace(/`([^`]+)`/g, '<code>$1</code>')
      .replace(/\n{2,}/g, '</p><p>')
      .replace(/\n/g, '<br>')
      .replace(/^(?!<(pre|h1|h2|h3|p)\b)([\s\S]*)$/, '<p>$2</p>');
  }
};
