import React, { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { CircleAlert, Download, LoaderCircle, X } from 'lucide-react';
import { isStandaloneWebApp } from '../utils/standalone-web-app';

const DOWNLOAD_NOTICE_HOST_ID = 'catsco-pwa-download-notices';

function ensureDownloadNoticeHost() {
  let host = document.getElementById(DOWNLOAD_NOTICE_HOST_ID);
  if (host) return host;
  host = document.createElement('div');
  host.id = DOWNLOAD_NOTICE_HOST_ID;
  host.className = 'catsco-pwa-download-notice-host';
  host.setAttribute('aria-label', '下载通知');
  document.body.appendChild(host);
  return host;
}

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

function isSameOriginURL(url) {
  if (!url || typeof window === 'undefined') return false;
  try {
    return new URL(url, window.location.href).origin === window.location.origin;
  } catch {
    return false;
  }
}

export async function downloadPwaFile(url, fileName) {
  if (!url) throw new Error('下载地址为空');
  const requestURL = new URL(url, window.location.href);
  if (requestURL.origin !== window.location.origin) throw new Error('跨域文件应使用浏览器原生下载');
  const response = await fetch(url, { credentials: 'include' });
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
  onDownloadStart,
  children,
  ...props
}) {
  const [state, setState] = useState('idle');
  const [downloading, setDownloading] = useState(false);
  const noticeHostRef = useRef(null);
  const inFlightRef = useRef(false);
  const mountedRef = useRef(true);
  const dismissedRef = useRef(false);
  const noticeTimerRef = useRef(null);
  const standalone = isStandaloneWebApp();
  const useBlobDownload = standalone && isSameOriginURL(href);
  const fileName = typeof download === 'string' ? download : fileNameFromURL(href);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      window.clearTimeout(noticeTimerRef.current);
    };
  }, []);

  const showNotice = (nextState) => {
    if (!mountedRef.current || dismissedRef.current) return;
    window.clearTimeout(noticeTimerRef.current);
    noticeHostRef.current = ensureDownloadNoticeHost();
    setState(nextState);
    if (nextState === 'started') {
      noticeTimerRef.current = window.setTimeout(() => setState('idle'), 2400);
    }
  };

  const dismissNotice = () => {
    dismissedRef.current = true;
    window.clearTimeout(noticeTimerRef.current);
    setState('idle');
  };

  const handleClick = async (event) => {
    onClick?.(event);
    if (event.defaultPrevented || !href) return;
    if (!useBlobDownload) {
      dismissedRef.current = false;
      showNotice('started');
      onDownloadStart?.();
      return;
    }
    event.preventDefault();
    if (inFlightRef.current) return;
    inFlightRef.current = true;
    dismissedRef.current = false;
    setDownloading(true);
    showNotice('downloading');
    try {
      await downloadPwaFile(href, fileName);
    } catch {
      showNotice('failed');
      return;
    } finally {
      inFlightRef.current = false;
      if (mountedRef.current) setDownloading(false);
    }
    showNotice('started');
    onDownloadStart?.();
  };

  return (
    <>
      <a
        {...props}
        href={href}
        download={download}
        target={useBlobDownload ? undefined : target}
        rel={useBlobDownload ? undefined : rel}
        onClick={handleClick}
        aria-busy={downloading || undefined}
      >
        {children}
      </a>
      {noticeHostRef.current && state !== 'idle' && createPortal(
        <div className={`catsco-pwa-download-notice${state === 'failed' ? ' is-error' : ''}`} role={state === 'failed' ? 'alert' : 'status'}>
          {state === 'downloading'
            ? <LoaderCircle size={17} className="cc-download-notice-icon is-spinning" aria-hidden="true" />
            : state === 'failed'
              ? <CircleAlert size={17} className="cc-download-notice-icon" aria-hidden="true" />
              : <Download size={17} className="cc-download-notice-icon" aria-hidden="true" />}
          <div className="cc-download-notice-message">
            <span>{state === 'failed' ? '下载未开始' : state === 'started' ? '已发起下载' : '正在准备下载…'}</span>
            <span className="cc-download-notice-filename" title={fileName}>{fileName}</span>
          </div>
          {state === 'failed' && (
            <div className="cc-download-notice-actions">
              <a href={href} target="_blank" rel="noopener noreferrer">在新窗口尝试</a>
              <a href="/">返回 CatsCo</a>
            </div>
          )}
          <button type="button" onClick={dismissNotice} aria-label="关闭下载提示" title="关闭">
            <X size={15} />
          </button>
        </div>,
        noticeHostRef.current,
      )}
    </>
  );
}
