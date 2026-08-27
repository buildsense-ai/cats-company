import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AlertCircle, Check, CheckCircle2, ChevronDown, ChevronUp, Cloud, Download, Laptop, Loader2, Monitor, RefreshCw, Trash2, X } from 'lucide-react';
import { api } from '../api';
import PwaDownloadLink from './pwa-download-link';
import {
  FALLBACK_RELEASE_VERSION,
  auditDescription,
  auditMeta,
  auditTitle,
  buildDownloadOptions,
  deviceStatusLabel,
  deviceRuntimeRole,
  isCloudRuntimeDevice,
  isDesktopDevice,
  isRoutableDesktopDevice,
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

function desktopPreferenceStorageKey(userId) {
  const normalizedUserId = String(userId || '').trim();
  return normalizedUserId ? `catsco_preferred_desktop_device:v1:${normalizedUserId}` : '';
}

function readPreferredDesktopDeviceId(userId) {
  const key = desktopPreferenceStorageKey(userId);
  if (!key) return '';
  try {
    return String(window.localStorage.getItem(key) || '').trim();
  } catch {
    return '';
  }
}

function writePreferredDesktopDeviceId(userId, deviceId) {
  const key = desktopPreferenceStorageKey(userId);
  if (!key) return;
  try {
    if (deviceId) window.localStorage.setItem(key, deviceId);
    else window.localStorage.removeItem(key);
  } catch {
    // Storage is an ergonomic preference only; device identity still comes from the server.
  }
}

export function resolveConnectedDesktopDevice(devices, claimedDeviceId, preferredDeviceId) {
  const availableDesktops = (devices || []).filter(isRoutableDesktopDevice);
  const normalizedClaimedDeviceId = String(claimedDeviceId || '').trim();
  if (normalizedClaimedDeviceId) {
    return availableDesktops.find((device) => device.deviceId === normalizedClaimedDeviceId) || null;
  }
  const normalizedPreferredDeviceId = String(preferredDeviceId || '').trim();
  return availableDesktops.find((device) => device.deviceId === normalizedPreferredDeviceId)
    || (availableDesktops.length === 1 ? availableDesktops[0] : null);
}

function openDesktopDeepLink(href) {
  const link = document.createElement('a');
  link.href = href;
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  link.remove();
}

export default function DesktopConnectModal({ userId, onClose, onConnected, onStatusChange, initialMode = 'connect' }) {
  const [state, setState] = useState('idle');
  const [error, setError] = useState('');
  const [showDownloads, setShowDownloads] = useState(initialMode === 'download');
  const [showAdvancedDownloads, setShowAdvancedDownloads] = useState(false);
  const [devices, setDevices] = useState([]);
  const [devicesLoaded, setDevicesLoaded] = useState(false);
  const [audit, setAudit] = useState([]);
  const [preferredDesktopDeviceId, setPreferredDesktopDeviceId] = useState(() => readPreferredDesktopDeviceId(userId));
  const [selectingDesktopDevice, setSelectingDesktopDevice] = useState(false);
  const [launchDetected, setLaunchDetected] = useState(false);
  const sessionRef = useRef(null);
  const connectedRef = useRef(false);
  const launchDetectedRef = useRef(false);
  const launchCleanupRef = useRef(null);
  const installHintTimerRef = useRef(null);
  const pollTimerRef = useRef(null);
  const startInFlightRef = useRef(false);
  const selectionRequiredRef = useRef(false);
  const desktopSectionRef = useRef(null);
  const [desktopRelease, setDesktopRelease] = useState({ version: FALLBACK_RELEASE_VERSION });
  const downloadOptions = useMemo(() => buildDownloadOptions(desktopRelease), [desktopRelease]);
  const recommendedDownload = useMemo(() => detectRecommendedOption(downloadOptions), [downloadOptions]);
  const otherDownloads = useMemo(
    () => downloadOptions.filter((option) => option.key !== recommendedDownload?.key),
    [downloadOptions, recommendedDownload?.key],
  );

  const loadDevices = useCallback(async () => {
    try {
      const deviceResp = await api.getDevices();
      const nextDevices = deviceResp?.devices || [];
      setDevices(nextDevices);
      setDevicesLoaded(true);
      return nextDevices;
    } catch (err) {
      console.warn('Failed to load CatsCo device state:', err);
      setDevicesLoaded(true);
      return null;
    }
  }, []);

  const loadAudit = useCallback(async () => {
    try {
      const auditResp = await api.getDeviceAudit(8);
      setAudit(auditResp?.events || []);
    } catch (err) {
      // Audit history is supplementary. Do not block device detection when it
      // is unavailable on an older server or during a transient failure.
      console.warn('Failed to load CatsCo device audit:', err);
    }
  }, []);

  useEffect(() => {
    loadDevices();
    loadAudit();
    const refreshTimer = window.setInterval(loadDevices, 5000);
    return () => window.clearInterval(refreshTimer);
  }, [loadAudit, loadDevices]);

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

  const desktopDevices = useMemo(() => devices.filter(isDesktopDevice), [devices]);
  const cloudDevices = useMemo(() => devices.filter(isCloudRuntimeDevice), [devices]);
  const otherDevices = useMemo(
    () => devices.filter((device) => deviceRuntimeRole(device) === 'unknown'),
    [devices],
  );
  const routableDesktopDevices = useMemo(
    () => desktopDevices.filter(isRoutableDesktopDevice),
    [desktopDevices],
  );
  const preferredDesktopDevice = useMemo(
    () => desktopDevices.find((device) => device.deviceId === preferredDesktopDeviceId) || null,
    [desktopDevices, preferredDesktopDeviceId],
  );
  const preferredDesktopConnected = Boolean(preferredDesktopDevice?.routable);
  const anyDesktopConnected = routableDesktopDevices.length > 0;

  const chooseDesktopDevice = useCallback((device, { completeConnection = false } = {}) => {
    const deviceId = String(device?.deviceId || '').trim();
    if (!deviceId) return;
    setPreferredDesktopDeviceId(deviceId);
    writePreferredDesktopDeviceId(userId, deviceId);
    setSelectingDesktopDevice(false);
    selectionRequiredRef.current = false;
    if (!completeConnection) {
      setState((current) => (current === 'selection' ? 'idle' : current));
      onStatusChange?.(device.routable ? 'connected' : 'disconnected');
      return;
    }
    if (connectedRef.current) return;
    connectedRef.current = true;
    launchCleanupRef.current?.();
    if (installHintTimerRef.current) window.clearTimeout(installHintTimerRef.current);
    if (pollTimerRef.current) window.clearInterval(pollTimerRef.current);
    setShowDownloads(false);
    setState('connected');
    onStatusChange?.('connected');
    window.setTimeout(() => onConnected?.(device), 600);
  }, [onConnected, onStatusChange, userId]);

  useEffect(() => {
    if (!devicesLoaded) return;
    if (!preferredDesktopDeviceId && routableDesktopDevices.length === 1) {
      chooseDesktopDevice(routableDesktopDevices[0]);
      return;
    }
    if (state === 'opening' || state === 'waiting' || state === 'waiting_download') return;
    onStatusChange?.(anyDesktopConnected ? 'connected' : 'disconnected');
  }, [
    anyDesktopConnected,
    chooseDesktopDevice,
    devicesLoaded,
    onStatusChange,
    preferredDesktopDevice,
    preferredDesktopDeviceId,
    routableDesktopDevices,
    state,
  ]);

  const beginDesktopSelection = () => {
    setSelectingDesktopDevice(true);
    window.setTimeout(() => desktopSectionRef.current?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }), 0);
  };

  const markState = (next) => {
    setState(next);
    if (next === 'connected') onStatusChange?.('connected');
    else if (next === 'waiting' || next === 'opening') onStatusChange?.('checking');
    else onStatusChange?.(anyDesktopConnected ? 'connected' : 'disconnected');
  };

  const finishIfConnected = async (claimedDeviceId = '') => {
    const res = await api.getDevices();
    const nextDevices = res?.devices || [];
    setDevices(nextDevices);
    setDevicesLoaded(true);
    const availableDesktops = nextDevices.filter(isRoutableDesktopDevice);
    const connected = resolveConnectedDesktopDevice(nextDevices, claimedDeviceId, preferredDesktopDeviceId);
    if (!connected) {
      selectionRequiredRef.current = !claimedDeviceId && availableDesktops.length > 1;
      if (selectionRequiredRef.current) {
        setSelectingDesktopDevice(true);
        markState('selection');
      }
      return false;
    }
    if (connectedRef.current) return true;
    chooseDesktopDevice(connected, { completeConnection: true });
    return true;
  };

  const pollForConnection = () => {
    let attempts = 0;
    let claimObserved = false;
    let claimedDeviceId = '';
    if (pollTimerRef.current) window.clearInterval(pollTimerRef.current);
    pollTimerRef.current = window.setInterval(async () => {
      attempts += 1;
      try {
        const session = sessionRef.current;
        if (session?.code) {
          const status = await api.getDesktopConnectStatus(session.code).catch(() => null);
          if (status?.state === 'claimed') {
            claimObserved = true;
            claimedDeviceId = String(status.device_id || '').trim();
            markState('waiting');
          }
        }
        if (claimObserved) {
          const connected = await finishIfConnected(claimedDeviceId);
          if (connected) return;
          if (selectionRequiredRef.current) {
            window.clearInterval(pollTimerRef.current);
            pollTimerRef.current = null;
            return;
          }
        }
      } catch (err) {
        console.warn('Desktop connect poll failed:', err);
      }
      if (attempts >= 8) {
        window.clearInterval(pollTimerRef.current);
        pollTimerRef.current = null;
        if (!connectedRef.current) {
          if (selectionRequiredRef.current) {
            setSelectingDesktopDevice(true);
            markState('selection');
          } else if (claimObserved && claimedDeviceId) {
            setError('桌面端已确认连接，但对应设备尚未上线。请确认该电脑的 CatsCo 连接服务正在运行后重试。');
            markState('failed');
          } else {
            setShowDownloads(true);
            markState('download');
          }
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
    if (startInFlightRef.current) return;
    startInFlightRef.current = true;
    connectedRef.current = false;
    selectionRequiredRef.current = false;
    setError('');
    setShowDownloads(false);
    setLaunchDetected(false);
    if (installHintTimerRef.current) window.clearTimeout(installHintTimerRef.current);
    markState('opening');
    try {
      const session = await api.createDesktopConnectSession();
      sessionRef.current = session;
      markState('waiting');
      watchForLaunchSignal();
      openDesktopDeepLink(session.deeplink_url || `catsco://connect?code=${encodeURIComponent(session.code)}`);
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
      if (deviceId === preferredDesktopDeviceId) {
        setPreferredDesktopDeviceId('');
        writePreferredDesktopDeviceId(userId, '');
        connectedRef.current = false;
        markState('idle');
      }
      await Promise.all([loadDevices(), loadAudit()]);
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

  const renderDeviceCard = (device, role) => {
    const name = device.displayName || device.deviceId;
    const isDesktop = role === 'desktop';
    const isPreferred = isDesktop && device.deviceId === preferredDesktopDeviceId;
    const DeviceIcon = role === 'server' ? Cloud : Monitor;
    return (
      <div key={device.deviceId} className={`catsco-download-card catsco-device-card${isPreferred ? ' is-preferred' : ''}`}>
        <span className="catsco-download-icon">
          <DeviceIcon size={20} />
        </span>
        <span className="catsco-download-copy">
          <span className="catsco-device-title-row">
            <span className="catsco-download-title">{name}</span>
            <span className={`catsco-device-role${isPreferred ? ' is-preferred' : ''}`}>
              {isPreferred ? '本机' : role === 'desktop' ? '桌面' : role === 'server' ? '云端' : '其他'}
            </span>
          </span>
          <span className="catsco-download-desc">{deviceStatusLabel(device)}</span>
          {(device.capabilities || []).length > 0 && (
            <span className="catsco-device-capabilities" aria-label="设备能力">
              {device.capabilities.map((capability, index) => (
                <span key={`${capability}-${index}`}>{capability}</span>
              ))}
            </span>
          )}
        </span>
        <span className="catsco-device-actions">
          {isDesktop && (selectingDesktopDevice || !preferredDesktopDevice) && (
            <button
              type="button"
              className={`catsco-download-action catsco-device-select${isPreferred ? ' is-selected' : ''}`}
              onClick={() => chooseDesktopDevice(device, {
                completeConnection: state === 'selection' && selectionRequiredRef.current,
              })}
              aria-label={`设为本机 ${name}`}
              title={isPreferred ? '当前本机' : '设为本机'}
              disabled={isPreferred || !device.routable}
            >
              <Check size={16} />
            </button>
          )}
          <button
            type="button"
            className="catsco-download-action"
            onClick={() => handleUnlinkDevice(device.deviceId)}
            aria-label={`解绑设备 ${name}`}
            title="解绑设备"
          >
            <Trash2 size={16} />
          </button>
        </span>
      </div>
    );
  };

  const renderDeviceSection = (title, sectionDevices, role, sectionRef) => {
    if (sectionDevices.length === 0) return null;
    return (
      <div className="catsco-device-section" ref={sectionRef}>
        <div className="catsco-section-heading">
          <h4 className="catsco-download-section-title">{title}</h4>
          <span className="catsco-section-meta">{sectionDevices.length} 台</span>
        </div>
        <div className="catsco-download-list">
          {sectionDevices.map((device) => renderDeviceCard(device, role))}
        </div>
      </div>
    );
  };

  const busy = state === 'opening' || state === 'waiting' || state === 'waiting_download';
  const preferredDesktopUnavailable = Boolean(devicesLoaded && preferredDesktopDeviceId && !preferredDesktopDevice);
  const selectionRequired = selectingDesktopDevice
    || state === 'selection'
    || (devicesLoaded && !preferredDesktopDevice && routableDesktopDevices.length > 1);
  const connectionReady = preferredDesktopConnected && !selectionRequired;
  const statusLabel = busy
      ? '连接中'
      : selectionRequired
        ? '待选择'
      : connectionReady
        ? '已连接'
      : state === 'failed'
        ? '连接失败'
        : state === 'download'
          ? '需要安装'
          : preferredDesktopUnavailable
            ? '本机不可用'
          : preferredDesktopDevice
            ? '本机离线'
          : '未连接';
  const statusTone = connectionReady ? 'is-success' : busy || selectionRequired ? 'is-pending' : state === 'failed' ? 'is-error' : 'is-neutral';
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
            {connectionReady ? <CheckCircle2 size={20} color="#0BA36D" /> : busy ? <Loader2 className="catsco-spin" size={20} /> : <Laptop size={20} />}
            <div className="catsco-connect-copy">
              <div className="catsco-connect-title-row">
                <strong>
                  {busy
                      ? '正在等待桌面端确认'
                      : selectionRequired
                        ? '选择本机桌面设备'
                        : connectionReady
                          ? `本机：${preferredDesktopDevice.displayName || preferredDesktopDevice.deviceId}`
                        : preferredDesktopUnavailable
                          ? '默认本机暂不可用'
                        : preferredDesktopDevice
                          ? `本机：${preferredDesktopDevice.displayName || preferredDesktopDevice.deviceId}`
                          : '连接我的电脑助手'}
                </strong>
                <span className={`catsco-connect-status ${statusTone}`} aria-live="polite">{statusLabel}</span>
              </div>
              <div className="catsco-connect-description">
                {selectionRequired
                  ? '检测到多台本地桌面设备，请选择当前浏览器所在的电脑。'
                  : connectionReady
                  ? '这台桌面设备在线，将作为当前浏览器默认使用的本机。'
                  : preferredDesktopUnavailable
                  ? '之前选择的本机当前不在设备列表中。系统不会自动改选其他电脑，请重新选择或打开原本机。'
                  : state === 'download' || state === 'waiting_download'
                  ? '没有检测到已连接的桌面端。若尚未安装，请下载推荐版本；安装后再点击打开。'
                  : launchDetected
                  ? '已检测到浏览器正在尝试打开 CatsCo，连接完成后这里会自动更新。'
                  : '点击后浏览器可能会询问是否允许打开 CatsCo，请选择允许。'}
              </div>
              {preferredDesktopDeviceId && (desktopDevices.length > 1 || preferredDesktopUnavailable) && (
                <button type="button" className="catsco-connect-reselect" onClick={beginDesktopSelection}>
                  重新选择本机
                </button>
              )}
            </div>
          </div>

          {state === 'waiting_download' && (
            <div className="catsco-connect-hint">
              <AlertCircle size={16} />
              <span>如果浏览器没有弹出打开确认，通常表示这台电脑还没安装新版 CatsCo，或当前版本不支持快捷连接。</span>
            </div>
          )}

          {error && <div className="catsco-connect-feedback is-error" role="alert">{error}</div>}

          {state === 'connected' && connectionReady && (
            <div className="catsco-connect-feedback is-success">
              已连接到本地 CatsCo 桌面助手，正在为你打开对话。
            </div>
          )}

          <div className="catsco-connect-actions">
            <button className="oc-btn oc-btn-primary" type="button" onClick={startConnect} disabled={busy}>
              {busy && <Loader2 className="catsco-spin" size={16} />}
              {!busy && <Laptop size={16} />}
              {busy ? '等待连接...' : connectionReady ? '重新打开本机' : '打开 CatsCo 桌面端'}
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

          {renderDeviceSection('本地桌面设备', desktopDevices, 'desktop', desktopSectionRef)}
          {renderDeviceSection('云端运行环境', cloudDevices, 'server')}
          {renderDeviceSection('其他设备', otherDevices, 'unknown')}

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
