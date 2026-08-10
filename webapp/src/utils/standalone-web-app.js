export function isStandaloneWebApp() {
  if (typeof window === 'undefined' || typeof navigator === 'undefined') return false;
  return navigator.standalone === true
    || window.matchMedia?.('(display-mode: standalone)')?.matches === true;
}
