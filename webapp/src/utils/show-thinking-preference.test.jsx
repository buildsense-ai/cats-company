import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import {
  readShowThinkingPreference,
  setShowThinkingPreference,
  useShowThinkingPreference,
} from './show-thinking-preference';

describe('show thinking preference', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    localStorage.clear();
    container = document.createElement('div');
    document.body.append(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.restoreAllMocks();
    localStorage.clear();
  });

  function Consumer() {
    return <output>{String(useShowThinkingPreference())}</output>;
  }

  it('defaults to visible and updates all mounted readers without remounting', async () => {
    expect(readShowThinkingPreference()).toBe(true);
    await act(async () => root.render(<><Consumer /><Consumer /></>));
    await act(async () => expect(setShowThinkingPreference(false)).toBe(true));
    expect([...container.querySelectorAll('output')].map((node) => node.textContent)).toEqual(['false', 'false']);
    expect(localStorage.getItem('cc_show_thinking')).toBe('false');
  });

  it('responds to another tab and to clearing storage', async () => {
    await act(async () => root.render(<Consumer />));
    await act(async () => {
      localStorage.setItem('cc_show_thinking', 'false');
      window.dispatchEvent(new StorageEvent('storage', { key: 'cc_show_thinking' }));
    });
    expect(container.textContent).toBe('false');
    await act(async () => {
      localStorage.clear();
      window.dispatchEvent(new StorageEvent('storage', { key: null }));
    });
    expect(container.textContent).toBe('true');
  });

  it('reports a failed persistence instead of presenting an unsaved preference as saved', async () => {
    vi.spyOn(localStorage, 'setItem').mockImplementation(() => { throw new Error('storage denied'); });
    await act(async () => root.render(<Consumer />));
    await act(async () => expect(setShowThinkingPreference(false)).toBe(false));
    expect(container.textContent).toBe('true');
  });
});
