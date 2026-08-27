import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Copy, Download, ExternalLink, Laptop, Monitor, RefreshCw, Trash2, X } from 'lucide-react';
import { api, getApiBaseURL, getWebSocketURL } from '../api';
import PwaDownloadLink from './pwa-download-link';

// Kept as the standalone Device Connector pairing surface for compatibility.
// The main WebApp entry now uses DesktopConnectModal, which combines the
// working desktop-connect flow with these shared downloads/device helpers.

export {
  FALLBACK_RELEASE_VERSION,
  auditDescription,
  auditMeta,
  auditTitle,
  buildDownloadOptions,
  deviceStatusLabel,
  DOWNLOAD_OPTIONS,
  releaseVersion,
  visibleDeviceAuditEvents,
} from './catsco-desktop-shared';

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

export function buildDeviceConnectorDeepLink(pairing) {
  const code = String(pairing?.pairing_code || '').trim();
  if (!code) return '';
  const params = new URLSearchParams({
    code,
    http_base_url: getApiBaseURL(),
    server_url: getWebSocketURL(),
  });
  return `catsco://device-connector/pair?${params.toString()}`;
}

function pairCommand(pairing) {
  const code = String(pairing?.pairing_code || '').trim();
  return code ? `catsco device-connector --pair ${code}` : '';
}

export function openDeviceConnectorDeepLink(deepLink) {
  if (!deepLink) return;
  if (typeof document === 'undefined') {
    window.location.href = deepLink;
    return;
  }
  const link = document.createElement('a');
  link.href = deepLink;
  link.rel = 'noopener noreferrer';
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  link.remove();
}

export default function CatsCoDownloadModal({ onClose }) {
  const [pairing, setPairing] = useState(null);
  const [devices, setDevices] = useState([]);
  const [audit, setAudit] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [launchMessage, setLaunchMessage] = useState('');
  const [desktopRelease, setDesktopRelease] = useState({ version: FALLBACK_RELEASE_VERSION });
  const downloadOptions = useMemo(() => buildDownloadOptions(desktopRelease), [desktopRelease]);

  const loadDeviceState = useCallback(async () => {
    try {
      const [deviceResp, auditResp] = await Promise.all([
        api.getDevices(),
        api.getDeviceAudit(8),
      ]);
      setDevices(deviceResp.devices || []);
      setAudit(auditResp.events || []);
    } catch (err) {
      setError(err.message || '设备状态读取失败');
    }
  }, []);

  useEffect(() => {
    loadDeviceState();
  }, [loadDeviceState]);

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

  useEffect(() => {
    if (!pairing?.pairing_id || pairing.status === 'consumed') return undefined;
    const timer = setInterval(async () => {
      try {
        const next = await api.getDeviceConnectorPairing(pairing.pairing_id);
        if (!next) return;
        setPairing((prev) => ({
          ...(prev || {}),
          ...next,
          pairing_code: next.status === 'consumed' ? '' : (prev?.pairing_code || ''),
        }));
        if (next.status === 'consumed') {
          setLaunchMessage('本机设备已连接，桌面端会在后台保持运行。');
          loadDeviceState();
        } else if (next.status === 'expired') {
          setLaunchMessage('配对码已过期，请重新连接。');
        }
      } catch {
        // Pairing may have expired; the next manual refresh will create a fresh one.
      }
    }, 3000);
    return () => clearInterval(timer);
  }, [pairing?.pairing_id, pairing?.status, loadDeviceState]);

  const handleOpenConnector = async () => {
    setLoading(true);
    setError('');
    try {
      let activePairing = pairing;
      if (!activePairing?.pairing_code || activePairing.status === 'expired' || activePairing.status === 'consumed') {
        activePairing = await api.createDeviceConnectorPairing();
        activePairing = { ...activePairing, status: 'pending' };
        setPairing(activePairing);
      }

      const deepLink = buildDeviceConnectorDeepLink(activePairing);
      if (!deepLink) throw new Error('配对码生成失败，请重试');
      setLaunchMessage('正在打开 CatsCo 桌面端...');
      openDeviceConnectorDeepLink(deepLink);
      window.setTimeout(() => {
        setLaunchMessage('如果桌面端没有弹出，请先安装并打开一次 CatsCo 桌面端；已安装时也可以复制备用命令。');
      }, 500);
    } catch (err) {
      setError(err.message || '连接本机设备失败');
    } finally {
      setLoading(false);
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

  const copyPairCommand = () => {
    const command = pairCommand(pairing);
    if (!command) return;
    navigator.clipboard?.writeText(command).catch(() => {});
    setLaunchMessage('已复制备用命令。');
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div
        className="oc-modal catsco-download-modal cc-settings-secondary-surface"
        role="dialog"
        aria-modal="true"
        aria-labelledby="catsco-download-dialog-title"
        aria-describedby="catsco-download-dialog-description"
        onClick={(event) => event.stopPropagation()}
      >
        <div className="oc-modal-header catsco-download-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3 id="catsco-download-dialog-title">CatsCo 本机设备</h3>
            <p id="catsco-download-dialog-description">当前版本 v{releaseVersion(desktopRelease)}</p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>

        <div className="catsco-download-body">
        <div className="catsco-download-list">
          <div className="catsco-download-card" style={{ alignItems: 'flex-start' }}>
            <span className="catsco-download-icon">
              <Laptop size={20} />
            </span>
            <span className="catsco-download-copy">
              <span className="catsco-download-title">连接这台电脑</span>
              <span className="catsco-download-desc">
                {pairing?.status === 'consumed'
                  ? '这台电脑已连接，桌面端会在后台保持运行'
                  : pairing?.pairing_code
                  ? `配对码 ${pairing.pairing_code} · ${pairing.status || 'pending'}`
                  : '一键打开 CatsCo 桌面端并完成设备配对'}
              </span>
              {pairing?.pairing_code && pairing.status !== 'consumed' && (
                <span className="catsco-download-meta">备用命令：{pairCommand(pairing)}</span>
              )}
              {launchMessage && <span className="catsco-download-meta">{launchMessage}</span>}
              {error && <span className="catsco-download-meta">{error}</span>}
            </span>
            <span className="catsco-download-actions">
              <button type="button" className="catsco-download-action" onClick={handleOpenConnector} disabled={loading} title="打开 CatsCo 桌面端连接">
                {loading ? <RefreshCw size={16} /> : <ExternalLink size={16} />}
              </button>
              {pairing?.pairing_code && (
                <button type="button" className="catsco-download-action" onClick={copyPairCommand} title="复制备用命令">
                  <Copy size={16} />
                </button>
              )}
            </span>
          </div>

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
              <button type="button" className="catsco-download-action" onClick={() => handleUnlinkDevice(device.deviceId)}>
                <Trash2 size={16} />
              </button>
            </div>
          ))}

          {visibleDeviceAuditEvents(audit).map((event) => (
            <div key={event.id} className="catsco-download-card">
              <span className="catsco-download-icon">
                <RefreshCw size={18} />
              </span>
              <span className="catsco-download-copy">
                <span className="catsco-download-title">{auditTitle(event)}</span>
                <span className="catsco-download-desc">{auditDescription(event)}</span>
              </span>
              <span className="catsco-download-meta">{auditMeta(event)}</span>
            </div>
          ))}
        </div>

        <h4 className="catsco-download-section-title">可下载版本</h4>
        <div className="catsco-download-list catsco-download-release-list">
          {downloadOptions.map((option) => {
            const Icon = option.icon;
            return (
              <PwaDownloadLink
                key={option.key}
                className="catsco-download-card"
                href={option.href}
                target="_blank"
                rel="noopener noreferrer"
                download
              >
                <span className="catsco-download-icon">
                  <Icon size={20} />
                </span>
                <span className="catsco-download-copy">
                  <span className="catsco-download-title">{option.title}</span>
                  <span className="catsco-download-desc">{option.description}</span>
                </span>
                <span className="catsco-download-meta">{option.meta}</span>
                <span className="catsco-download-action">
                  <Download size={16} />
                </span>
              </PwaDownloadLink>
            );
          })}
        </div>
        </div>
      </div>
    </div>
  );
}
