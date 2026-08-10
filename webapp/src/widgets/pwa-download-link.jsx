import React, { useState } from 'react';
import { createPortal } from 'react-dom';
import { X } from 'lucide-react';
import { isStandaloneWebApp } from '../utils/standalone-web-app';

function fileNameFromURL(url) {
  if (!url) return 'download';
  try {
    const pathname = new URL(url, window.location.origin).pathname;
    const name = pathname.split('/').filter(Boolean).pop() || 'download';
    return decodeURIComponent(name);
  } catch {
    return 'download';
  }
}

export async function downloadPwaFile(url, fileName) {
  if (!url) throw new Error('下载地址为空');
  const requestURL = new URL(url, window.location.href);
  const credentials = requestURL.origin === window.location.origin ? 'include' : 'omit';
  const response = await fetch(url, { credentials });
  if (!response.ok) throw new Error(`下载失败（HTTP ${response.status}）`);
  const blob = await response.blob();
  const objectURL = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = objectURL;
  link.download = fileName || fileNameFromURL(url);
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(objectURL), 60_000);
}

export default function PwaDownloadLink({
  href,
  download = true,
  target,
  rel,
  onClick,
  children,
  ...props
}) {
  const [state, setState] = useState('idle');
  const standalone = isStandaloneWebApp();
  const fileName = typeof download === 'string' ? download : fileNameFromURL(href);

  const handleClick = async (event) => {
    onClick?.(event);
    if (event.defaultPrevented || !standalone || !href) return;
    event.preventDefault();
    if (state === 'downloading') return;
    setState('downloading');
    try {
      await downloadPwaFile(href, fileName);
      setState('idle');
    } catch {
      setState('failed');
    }
  };

  return (
    <>
      <a
        {...props}
        href={href}
        download={download}
        target={standalone ? undefined : target}
        rel={standalone ? undefined : rel}
        onClick={handleClick}
        aria-busy={state === 'downloading' || undefined}
      >
        {children}
      </a>
      {typeof document !== 'undefined' && state !== 'idle' && createPortal(
        <div className={`catsco-pwa-download-notice${state === 'failed' ? ' is-error' : ''}`} role={state === 'failed' ? 'alert' : 'status'}>
          <span>{state === 'failed' ? '下载未开始' : '正在准备下载…'}</span>
          {state === 'failed' && (
            <>
              <a href={href} target="_blank" rel="noopener noreferrer">在新窗口尝试</a>
              <a href="/">返回 CatsCo</a>
            </>
          )}
          <button type="button" onClick={() => setState('idle')} aria-label="关闭下载提示" title="关闭">
            <X size={15} />
          </button>
        </div>,
        document.body,
      )}
    </>
  );
}
