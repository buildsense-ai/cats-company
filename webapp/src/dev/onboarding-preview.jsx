import React, { useEffect } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '../css/auth-critical.css';
import '../css/catsco-focus-policy.css';
import '../css/catsco-input-states.css';
import IdentityOnboarding from '../components/identity-onboarding';
import { applyDocumentTheme, THEME_STORAGE_KEY } from '../utils/theme-access';
import { readStorageValue } from '../utils/storage-access';
import './onboarding-preview.css';

const previewTheme = applyDocumentTheme(readStorageValue(THEME_STORAGE_KEY));
const workspacePreviewUrl = `/?theme_preview=${previewTheme}&onboarding_preview=1`;
function OnboardingPreview() {
  const assistantStep = new URLSearchParams(location.search).get('step') === 'assistant';
  useEffect(() => {
    if (assistantStep) window.location.replace(workspacePreviewUrl);
  }, [assistantStep]);
  if (assistantStep) return <a href={workspacePreviewUrl}>进入助手引导预览</a>;
  return <>
    <aside className="cc-onboarding-preview-bar" aria-label="本地测试工具">
      <span>本地引导预览 · 添加与激活均为模拟</span>
      <button type="button" onClick={() => window.location.reload()}>重新体验</button>
      <a href="/register">返回注册页</a>
    </aside>
    <IdentityOnboarding initialName="" onComplete={async () => { window.location.assign(workspacePreviewUrl); }} />
  </>;
}
createRoot(document.getElementById('root')).render(<OnboardingPreview />);
