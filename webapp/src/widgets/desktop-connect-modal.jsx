import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, CheckCircle2, ChevronDown, ChevronUp, Download, Laptop, Loader2, Monitor, RefreshCw, Trash2, X } from 'lucide-react';
import { api } from '../api';
import PwaDownloadLink from './pwa-download-link';
import {
  FALLBACK_RELEASE_VERSION,
  auditDescription,
  auditMeta,
  auditTitle,
  buildDownloadOptions,
  deviceStatusLabel,
  releaseVersion,
  visibleDeviceAuditEvents,
} from './catsco-desktop-shared';

function detectRecommendedOption(downloadOptions) {
  const platform = `${navigator.platform || ''} ${navigator.userAgent || ''}`.toLowerCase();
  if (platform.includes('win')) return downloadOptions.find((option) => option.key === 'windows');
  if (platform.includes('mac')) {
    if (platform.includes('arm') || platform.includes('apple')) {
      return downloadOptions.find((option) => option.key === 'mac-arm');
    }
    return downloadOptions.find((option) => option.key === 'mac-intel');
  }
  if (platform.includes('linux')) return downloadOptions.find((option) => option.key === 'linux-appimage');
  return downloadOptions.find((option) => option.key === 'windows');
}

function findConnectedLocalAgent(agents) {
  return (agents || []).find((agent) => agent.relation === 'owner' && agent.is_online);
}

export default function DesktopConnectModal({ onClose, onConnected, onStatusChange, initialMode = 'connect' }) {
  const [state, setState] = useState('idle');
  const [error, setError] = useState('');
  const [showDownloads, setShowDownloads] = useState(initialMode === 'download');
  const [showAdvancedDownloads, setShowAdvancedDownloads] = useState(false);
  const [devices, setDevices] = useState([]);
  const [audit, setAudit] = useState([]);
  const [launchDetected, setLaunchDetected] = useState(false);
  const sessionRef = useRef(null);
  const connectedRef = useRef(false);
  const launchDetectedRef = useRef(false);
  const launchCleanupRef = useRef(null);
  const installHintTimerRef = useRef(null);
  const pollTimerRef = useRef(null);
  const startInFlightRef = useRef(false);
  const [desktopRelease, setDesktopRelease] = useState({ version: FALLBACK_RELEASE_VERSION });
  const downloadOptions = useMemo(() => buildDownloadOptions(desktopRelease), [desktopRelease]);
  const recommendedDownload = useMemo(() => detectRecommendedOption(downloadOptions), [downloadOptions]);
  const otherDownloads = useMemo(
    () => downloadOptions.filter((option) => option.key !== recommendedDownload?.key),
    [downloadOptions, recommendedDownload?.key],
  );

  const loadDeviceState = useCallback(async () => {
    try {
      const [deviceResp, auditResp] = await Promise.all([
        api.getDevices(),
        api.getDeviceAudit(8),
      ]);
      setDevices(deviceResp?.devices || []);
      setAudit(auditResp?.events || []);
    } catch (err) {
      // Device inventory is supplementary to the connection flow. Keep the
      // modal usable when an older server does not expose these endpoints.
      console.warn('Failed to load CatsCo device state:', err);
    }
  }, []);

  useEffect(() => {
    loadDeviceState();
  }, [loadDeviceState]);

  useEffect(() => () => {
    launchCleanupRef.current?.();
    if (installHintTimerRef.current) window.clearTimeout(installHintTimerRef.current);
    if (pollTimerRef.current) window.clearInterval(pollTimerRef.current);
  }, []);

  useEffect(() => {
    let cancelled = false;
    api.getCatsCoDesktopReleases()
      .then((release) => {
        if (!cancelled && release) setDesktopRelease(release);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const markState = (next) => {
    setState(next);
    if (next === 'connected') onStatusChange?.('connected');
    else if (next === 'waiting' || next === 'opening') onStatusChange?.('checking');
    else onStatusChange?.('disconnected');
  };

  const finishIfConnected = async () => {
    const res = await api.getAgents();
    const connected = findConnectedLocalAgent(res.agents || []);
    if (!connected) return false;
    if (connectedRef.current) return true;
    connectedRef.current = true;
    launchCleanupRef.current?.();
    if (installHintTimerRef.current) window.clearTimeout(installHintTimerRef.current);
    if (pollTimerRef.current) window.clearInterval(pollTimerRef.current);
    setShowDownloads(false);
    markState('connected');
    window.setTimeout(() => {
      if (onConnected) onConnected(connected);
    }, 600);
    return true;
  };

  const pollForConnection = () => {
    let attempts = 0;
    if (pollTimerRef.current) window.clearInterval(pollTimerRef.current);
    pollTimerRef.current = window.setInterval(async () => {
      attempts += 1;
      try {
        const session = sessionRef.current;
        if (session?.code) {
          const status = await api.getDesktopConnectStatus(session.code).catch(() => null);
          if (status?.state === 'claimed') {
            markState('waiting');
          }
        }
        const connected = await finishIfConnected();
        if (connected) {
          return;
        }
      } catch (err) {
        console.warn('Desktop connect poll failed:', err);
      }
      if (attempts >= 8) {
        window.clearInterval(pollTimerRef.current);
        pollTimerRef.current = null;
        if (!connectedRef.current) {
          setShowDownloads(true);
          markState('download');
        }
      }
    }, 2000);
  };

  const watchForLaunchSignal = () => {
    launchCleanupRef.current?.();
    launchDetectedRef.current = false;
    setLaunchDetected(false);
    const markLaunched = () => {
      if (launchDetectedRef.current) return;
      launchDetectedRef.current = true;
      setLaunchDetected(true);
      setState((current) => (
        current === 'opening' || current === 'waiting' || current === 'waiting_download'
          ? 'waiting'
          : current
      ));
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === 'hidden') markLaunched();
    };
    window.addEventListener('blur', markLaunched);
    window.addEventListener('pagehide', markLaunched);
    document.addEventListener('visibilitychange', onVisibilityChange);
    launchCleanupRef.current = () => {
      window.removeEventListener('blur', markLaunched);
      window.removeEventListener('pagehide', markLaunched);
      document.removeEventListener('visibilitychange', onVisibilityChange);
      launchCleanupRef.current = null;
    };
  };

  const startConnect = async () => {
    if (startInFlightRef.current || connectedRef.current) return;
    startInFlightRef.current = true;
    setError('');
    setShowDownloads(false);
    setLaunchDetected(false);
    if (installHintTimerRef.current) window.clearTimeout(installHintTimerRef.current);
    markState('opening');
    try {
      const alreadyConnected = await finishIfConnected();
      if (alreadyConnected) return;

      const session = await api.createDesktopConnectSession();
      sessionRef.current = session;
      markState('waiting');
      watchForLaunchSignal();
      window.location.href = session.deeplink_url || `catsco://connect?code=${encodeURIComponent(session.code)}`;
      pollForConnection();
      installHintTimerRef.current = window.setTimeout(() => {
        if (connectedRef.current) return;
        if (launchDetectedRef.current) return;
        setShowDownloads(true);
        setState((current) => (current === 'waiting' || current === 'opening' ? 'waiting_download' : current));
      }, 1500);
    } catch (err) {
      setError(err.message || '连接失败，请稍后重试。');
      setShowDownloads(true);
      markState('failed');
    } finally {
      startInFlightRef.current = false;
    }
  };

  const handleUnlinkDevice = async (deviceId) => {
    setError('');
    try {
      await api.unlinkDevice(deviceId);
      await loadDeviceState();
    } catch (err) {
      setError(err.message || '设备解绑失败');
    }
  };

  const renderDownload = (option, primary = false) => {
    const Icon = option.icon;
    return (
      <PwaDownloadLink
        key={option.key}
        className={`catsco-download-card ${primary ? 'catsco-download-card-primary' : ''}`}
        href={option.href}
        target="_blank"
        rel="noopener noreferrer"
        download
      >
        <span className="catsco-download-icon"><Icon size={20} /></span>
        <span className="catsco-download-copy">
          <span className="catsco-download-title">{option.title}</span>
          <span className="catsco-download-desc">{option.description}</span>
        </span>
        <span className="catsco-download-action"><Download size={16} /></span>
      </PwaDownloadLink>
    );
  };

  const busy = state === 'opening' || state === 'waiting' || state === 'waiting_download';
  const statusLabel = state === 'connected'
    ? '已连接'
    : busy
      ? '连接中'
      : state === 'failed'
        ? '连接失败'
        : state === 'download'
          ? '需要安装'
          : '未连接';
  const statusTone = state === 'connected' ? 'is-success' : busy ? 'is-pending' : state === 'failed' ? 'is-error' : 'is-neutral';
  const hasAudit = visibleDeviceAuditEvents(audit).length > 0;

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div
        className="oc-modal catsco-download-modal cc-settings-secondary-surface"
        role="dialog"
        aria-modal="true"
        aria-labelledby="catsco-desktop-modal-title"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="oc-modal-header catsco-download-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3 id="catsco-desktop-modal-title">CatsCo 桌面端</h3>
            <p>连接电脑、查看设备，或下载当前版本。</p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>

        <div className="catsco-connect-body">
          <div className="catsco-connect-summary">
            {state === 'connected' ? <CheckCircle2 size={20} color="#0BA36D" /> : busy ? <Loader2 className="catsco-spin" size={20} /> : <Laptop size={20} />}
            <div className="catsco-connect-copy">
              <div className="catsco-connect-title-row">
                <strong>
                  {state === 'connected' ? '已连接本地助手' : busy ? '正在等待桌面端确认' : '连接我的电脑助手'}
                </strong>
                <span className={`catsco-connect-status ${statusTone}`} aria-live="polite">{statusLabel}</span>
              </div>
              <div className="catsco-connect-description">
                {state === 'download' || state === 'waiting_download'
                  ? '没有检测到已连接的桌面端。若尚未安装，请下载推荐版本；安装后再点击打开。'
                  : launchDetected
                  ? '已检测到浏览器正在尝试打开 CatsCo，连接完成后这里会自动更新。'
                  : '点击后浏览器可能会询问是否允许打开 CatsCo，请选择允许。'}
              </div>
            </div>
          </div>

          {state === 'waiting_download' && (
            <div className="catsco-connect-hint">
              <AlertCircle size={16} />
              <span>如果浏览器没有弹出打开确认，通常表示这台电脑还没安装新版 CatsCo，或当前版本不支持快捷连接。</span>
            </div>
          )}

          {error && <div className="catsco-connect-feedback is-error" role="alert">{error}</div>}

          {state === 'connected' && (
            <div className="catsco-connect-feedback is-success">
              已连接到本地 CatsCo 桌面助手，正在为你打开对话。
            </div>
          )}

          <div className="catsco-connect-actions">
            <button className="oc-btn oc-btn-primary" type="button" onClick={startConnect} disabled={state === 'connected' || busy}>
              {busy && <Loader2 className="catsco-spin" size={16} />}
              {!busy && state !== 'connected' && <Laptop size={16} />}
              {busy ? '等待连接...' : state === 'connected' ? '已连接' : '打开 CatsCo 桌面端'}
            </button>

            <button
              type="button"
              className="oc-btn oc-btn-default catsco-download-toggle"
              onClick={() => setShowDownloads((value) => !value)}
              aria-expanded={showDownloads}
            >
              <Download size={16} />
              {showDownloads ? '收起下载' : '下载桌面端'}
            </button>
          </div>

          {showDownloads && (
            <div className="catsco-desktop-download-section">
              <div className="catsco-section-heading">
                <h4 className="catsco-download-section-title">可下载版本</h4>
                <span className="catsco-section-meta">v{releaseVersion(desktopRelease)}</span>
              </div>
              <div className="catsco-download-list">
                {recommendedDownload && renderDownload(recommendedDownload, true)}
                <button
                  type="button"
                  className="oc-btn oc-btn-default catsco-download-more"
                  onClick={() => setShowAdvancedDownloads((value) => !value)}
                  aria-expanded={showAdvancedDownloads}
                >
                  {showAdvancedDownloads ? <ChevronUp size={15} /> : <ChevronDown size={15} />}
                  {showAdvancedDownloads ? '收起其他版本' : '其他系统版本'}
                </button>
                {showAdvancedDownloads && otherDownloads.map((option) => renderDownload(option))}
              </div>
            </div>
          )}

          {devices.length > 0 && (
            <div className="catsco-device-section">
              <div className="catsco-section-heading">
                <h4 className="catsco-download-section-title">已连接设备</h4>
                <span className="catsco-section-meta">{devices.length} 台</span>
              </div>
              <div className="catsco-download-list">
                {devices.map((device) => (
                  <div key={device.deviceId} className="catsco-download-card catsco-device-card">
                    <span className="catsco-download-icon">
                      <Monitor size={20} />
                    </span>
                    <span className="catsco-download-copy">
                      <span className="catsco-download-title">{device.displayName || device.deviceId}</span>
                      <span className="catsco-download-desc">{deviceStatusLabel(device)}</span>
                      {(device.capabilities || []).length > 0 && (
                        <span className="catsco-device-capabilities" aria-label="设备能力">
                          {device.capabilities.map((capability, index) => (
                            <span key={`${capability}-${index}`}>{capability}</span>
                          ))}
                        </span>
                      )}
                    </span>
                    <button
                      type="button"
                      className="catsco-download-action"
                      onClick={() => handleUnlinkDevice(device.deviceId)}
                      aria-label={`解绑设备 ${device.displayName || device.deviceId}`}
                      title="解绑设备"
                    >
                      <Trash2 size={16} />
                    </button>
                  </div>
                ))}
              </div>
            </div>
          )}

          {hasAudit && (
            <div className="catsco-audit-section">
              <div className="catsco-section-heading">
                <h4 className="catsco-download-section-title">最近活动</h4>
                <span className="catsco-section-meta">最多 3 条</span>
              </div>
              <div className="catsco-download-list">
                {visibleDeviceAuditEvents(audit).map((event) => (
                  <div key={event.id} className="catsco-download-card">
                    <span className="catsco-download-icon">
                      <RefreshCw size={18} />
                    </span>
                    <span className="catsco-download-copy">
                      <span className="catsco-download-title">{auditTitle(event)}</span>
                      <span className="catsco-download-desc">{auditDescription(event)}</span>
                    </span>
                    {auditMeta(event) && (
                      <span className="catsco-download-meta">{auditMeta(event)}</span>
                    )}
                  </div>
                ))}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
