import React, { useCallback, useEffect, useState } from 'react';
import { Apple, Copy, Download, Laptop, Monitor, RefreshCw, Trash2, X } from 'lucide-react';
import { api } from '../api';

const RELEASE_VERSION = '1.2.0';
const TOS_BASE_URL = 'https://github-release.tos-cn-guangzhou.volces.com/update';

const DOWNLOAD_OPTIONS = [
  {
    key: 'windows',
    title: 'Windows',
    description: '适用于 Windows 10/11 的安装程序',
    icon: Monitor,
    href: `${TOS_BASE_URL}/CatsCo-${RELEASE_VERSION}-win.exe`,
    meta: 'x64 / arm64 由安装包自动适配',
  },
  {
    key: 'mac-arm',
    title: 'macOS Apple Silicon',
    description: '适用于 M 系列芯片 Mac',
    icon: Apple,
    href: `${TOS_BASE_URL}/macos-arm64/CatsCo-${RELEASE_VERSION}-mac-arm64.dmg`,
    meta: 'arm64',
  },
  {
    key: 'mac-intel',
    title: 'macOS Intel',
    description: '适用于 Intel 芯片 Mac',
    icon: Apple,
    href: `${TOS_BASE_URL}/macos-x64/CatsCo-${RELEASE_VERSION}-mac-x64.dmg`,
    meta: 'x64',
  },
  {
    key: 'linux-appimage',
    title: 'Linux AppImage',
    description: '无需安装，下载后赋予执行权限运行',
    icon: Laptop,
    href: `${TOS_BASE_URL}/CatsCo-${RELEASE_VERSION}-linux.AppImage`,
    meta: 'x64',
  },
  {
    key: 'linux-deb',
    title: 'Linux Debian / Ubuntu',
    description: '适用于 Debian、Ubuntu 等发行版',
    icon: Laptop,
    href: `${TOS_BASE_URL}/CatsCo-${RELEASE_VERSION}-linux.deb`,
    meta: 'deb',
  },
];

function deviceStatusLabel(device) {
  if (device.routable) return '可用';
  if (device.routeConnected) return '已连接';
  if (device.active) return '活跃';
  return device.unavailableReason || device.status || '离线';
}

export default function CatsCoDownloadModal({ onClose }) {
  const [pairing, setPairing] = useState(null);
  const [devices, setDevices] = useState([]);
  const [audit, setAudit] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

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
    if (!pairing?.pairing_id || pairing.status === 'consumed') return undefined;
    const timer = setInterval(async () => {
      try {
        const next = await api.getDeviceConnectorPairing(pairing.pairing_id);
        setPairing((prev) => ({ ...(prev || {}), ...next }));
        if (next.status === 'consumed') {
          loadDeviceState();
        }
      } catch {
        // Pairing may have expired; the next manual refresh will create a fresh one.
      }
    }, 3000);
    return () => clearInterval(timer);
  }, [pairing?.pairing_id, pairing?.status, loadDeviceState]);

  const handleCreatePairing = async () => {
    setLoading(true);
    setError('');
    try {
      const next = await api.createDeviceConnectorPairing();
      setPairing({ ...next, status: 'pending' });
    } catch (err) {
      setError(err.message || '配对码生成失败');
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
    if (!pairing?.pairing_code) return;
    navigator.clipboard?.writeText(`catsco device-connector --pair ${pairing.pairing_code}`).catch(() => {});
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal catsco-download-modal" onClick={(event) => event.stopPropagation()}>
        <div className="oc-modal-header catsco-download-header">
          <div>
            <h3>CatsCo 本机设备</h3>
            <p>当前版本 v{RELEASE_VERSION}</p>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭">
            <X size={18} />
          </button>
        </div>

        <div className="catsco-download-list">
          <div className="catsco-download-card" style={{ alignItems: 'flex-start' }}>
            <span className="catsco-download-icon">
              <Laptop size={20} />
            </span>
            <span className="catsco-download-copy">
              <span className="catsco-download-title">Device Connector</span>
              <span className="catsco-download-desc">
                {pairing?.pairing_code
                  ? `配对码 ${pairing.pairing_code} · ${pairing.status || 'pending'}`
                  : '生成一次性配对码'}
              </span>
              {pairing?.pairing_code && (
                <span className="catsco-download-meta">catsco device-connector --pair {pairing.pairing_code}</span>
              )}
              {error && <span className="catsco-download-meta">{error}</span>}
            </span>
            <button type="button" className="catsco-download-action" onClick={pairing?.pairing_code ? copyPairCommand : handleCreatePairing} disabled={loading}>
              {pairing?.pairing_code ? <Copy size={16} /> : <RefreshCw size={16} />}
            </button>
          </div>

          {devices.map((device) => (
            <div key={device.deviceId} className="catsco-download-card">
              <span className="catsco-download-icon">
                <Monitor size={20} />
              </span>
              <span className="catsco-download-copy">
                <span className="catsco-download-title">{device.displayName || device.deviceId}</span>
                <span className="catsco-download-desc">{deviceStatusLabel(device)}</span>
              </span>
              <span className="catsco-download-meta">{(device.capabilities || []).join(', ')}</span>
              <button type="button" className="catsco-download-action" onClick={() => handleUnlinkDevice(device.deviceId)}>
                <Trash2 size={16} />
              </button>
            </div>
          ))}

          {audit.slice(0, 3).map((event) => (
            <div key={event.id} className="catsco-download-card">
              <span className="catsco-download-icon">
                <RefreshCw size={18} />
              </span>
              <span className="catsco-download-copy">
                <span className="catsco-download-title">{event.phase}</span>
                <span className="catsco-download-desc">{event.device_id || event.operation || event.result || '-'}</span>
              </span>
              <span className="catsco-download-meta">{event.result || ''}</span>
            </div>
          ))}
        </div>

        <div className="catsco-download-list">
          {DOWNLOAD_OPTIONS.map((option) => {
            const Icon = option.icon;
            return (
              <a
                key={option.key}
                className="catsco-download-card"
                href={option.href}
                target="_blank"
                rel="noopener noreferrer"
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
              </a>
            );
          })}
        </div>
      </div>
    </div>
  );
}
