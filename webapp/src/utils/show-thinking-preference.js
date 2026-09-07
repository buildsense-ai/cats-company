import { useSyncExternalStore } from 'react';
import { readStorageValue, writeStorageValue } from './storage-access';

const STORAGE_KEY = 'cc_show_thinking';
const CHANGE_EVENT = 'cc:show-thinking-changed';

export function readShowThinkingPreference() {
  const value = readStorageValue(STORAGE_KEY);
  return value === null || value === 'true';
}

function subscribe(onChange) {
  const handleStorage = (event) => {
    if (event.key === STORAGE_KEY || event.key === null) onChange();
  };
  window.addEventListener(CHANGE_EVENT, onChange);
  window.addEventListener('storage', handleStorage);
  return () => {
    window.removeEventListener(CHANGE_EVENT, onChange);
    window.removeEventListener('storage', handleStorage);
  };
}

export function setShowThinkingPreference(value) {
  if (!writeStorageValue(STORAGE_KEY, String(Boolean(value)))) return false;
  window.dispatchEvent(new Event(CHANGE_EVENT));
  return true;
}

export function useShowThinkingPreference() {
  return useSyncExternalStore(subscribe, readShowThinkingPreference, () => true);
}
