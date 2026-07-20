import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';

vi.mock('../api', () => ({
  api: {
    updateMe: vi.fn(),
    uploadFile: vi.fn(),
  },
}));

import ProfileEditor from './profile-editor';

describe('ProfileEditor appearance settings', () => {
  let container;
  let root;

  beforeEach(() => {
    global.IS_REACT_ACT_ENVIRONMENT = true;
    container = document.createElement('div');
    document.body.appendChild(container);
    root = createRoot(container);
  });

  afterEach(async () => {
    await act(async () => root.unmount());
    container.remove();
    vi.clearAllMocks();
  });

  async function renderEditor(props = {}) {
    await act(async () => {
      root.render(
        <ProfileEditor
          user={{ uid: 7, username: 'bruce', display_name: 'Bruce' }}
          theme="light"
          onClose={vi.fn()}
          onSaved={vi.fn()}
          {...props}
        />,
      );
    });
  }

  it('shows the current theme in settings and invokes the theme toggle', async () => {
    const onToggleTheme = vi.fn();
    await renderEditor({ onToggleTheme });

    const toggle = container.querySelector('button[aria-label="切换日夜模式"]');
    expect(toggle).toBeTruthy();
    expect(toggle.querySelector('.oc-settings-theme-value').textContent).toBe('浅色');

    await act(async () => Simulate.click(toggle));
    expect(onToggleTheme).toHaveBeenCalledTimes(1);
  });

  it('labels the active dark theme correctly', async () => {
    await renderEditor({ theme: 'dark', onToggleTheme: vi.fn() });
    expect(container.querySelector('.oc-settings-theme-value').textContent).toBe('深色');
  });
});
