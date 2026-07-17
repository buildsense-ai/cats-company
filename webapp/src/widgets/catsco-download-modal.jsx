import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { AlertCircle, Apple, Check, Copy, Database, Download, ExternalLink, Laptop, Monitor, RefreshCw, Trash2, X } from 'lucide-react';
import { api, getApiBaseURL, getWebSocketURL, requestExternalHistory } from '../api';

export const FALLBACK_RELEASE_VERSION = '1.4.1';
const TOS_BASE_URL = 'https://github-release.tos-cn-guangzhou.volces.com/update';

const DOWNLOAD_OPTION_DEFS = [
  {
    key: 'windows',
    title: 'Windows',
    description: '适用于 Windows 10/11 的安装程序',
    icon: Monitor,
    hrefForVersion: (version) => `${TOS_BASE_URL}/CatsCo-${version}-win.exe`,
    meta: 'x64 / arm64 由安装包自动适配',
  },
  {
    key: 'mac-arm',
    title: 'macOS Apple Silicon',
    description: '适用于 M 系列芯片 Mac',
    icon: Apple,
    hrefForVersion: (version) => `${TOS_BASE_URL}/macos-arm64/CatsCo-${version}-mac-arm64.dmg`,
    meta: 'arm64',
  },
  {
    key: 'mac-intel',
    title: 'macOS Intel',
    description: '适用于 Intel 芯片 Mac',
    icon: Apple,
    hrefForVersion: (version) => `${TOS_BASE_URL}/macos-x64/CatsCo-${version}-mac-x64.dmg`,
    meta: 'x64',
  },
  {
    key: 'linux-appimage',
    title: 'Linux AppImage',
    description: '无需安装，下载后赋予执行权限运行',
    icon: Laptop,
    hrefForVersion: (version) => `${TOS_BASE_URL}/CatsCo-${version}-linux.AppImage`,
    meta: 'x64',
  },
  {
    key: 'linux-deb',
    title: 'Linux Debian / Ubuntu',
    description: '适用于 Debian、Ubuntu 等发行版',
    icon: Laptop,
    hrefForVersion: (version) => `${TOS_BASE_URL}/CatsCo-${version}-linux.deb`,
    meta: 'deb',
  },
];

function safeReleaseHref(value) {
  const href = String(value || '').trim();
  return /^https?:\/\//i.test(href) ? href : '';
}

export function releaseVersion(release) {
  const version = String(release?.version || '').trim();
  return version || FALLBACK_RELEASE_VERSION;
}

export function buildDownloadOptions(release = {}) {
  const version = releaseVersion(release);
  const downloads = release?.downloads && typeof release.downloads === 'object' ? release.downloads : {};
  return DOWNLOAD_OPTION_DEFS.map(({ hrefForVersion, ...option }) => ({
    ...option,
    href: safeReleaseHref(downloads[option.key]) || hrefForVersion(version),
  }));
}

export const DOWNLOAD_OPTIONS = buildDownloadOptions({ version: FALLBACK_RELEASE_VERSION });

function deviceStatusLabel(device) {
  if (device.routable) return '可用';
  if (device.routeConnected) return '已连接';
  if (device.active) return '活跃';
  return device.unavailableReason || device.status || '离线';
}

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

const HIDDEN_AUDIT_PHASES = new Set(['pairing_created']);

const AUDIT_PHASE_LABELS = {
  device_enrolled: '设备已连接',
  device_unlinked: '设备已解绑',
  rpc_forwarded: '任务已发送到设备',
  rpc_result: '设备任务完成',
  rpc_rejected: '设备任务未执行',
  rpc_result_rejected: '设备结果未接收',
};

const AUDIT_RESULT_LABELS = {
  denied: '已拒绝',
  duplicate: '重复请求',
  gone: '会话已断开',
  offline: '设备离线',
  rate_limited: '请求过多',
  unavailable: '设备不可用',
};

export function visibleDeviceAuditEvents(events) {
  return (Array.isArray(events) ? events : [])
    .filter((event) => event && !HIDDEN_AUDIT_PHASES.has(event.phase))
    .slice(0, 3);
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

function auditTitle(event) {
  return AUDIT_PHASE_LABELS[event.phase] || event.phase || '设备活动';
}

function auditDescription(event) {
  return event.device_id || event.operation || event.reason || AUDIT_RESULT_LABELS[event.result] || '设备活动';
}

function auditMeta(event) {
  if (!event.result || event.result === 'ok') return '';
  return AUDIT_RESULT_LABELS[event.result] || event.result;
}

export function externalHistoryDuration(windowMode, customDays) {
  if (windowMode === 'none') return '';
  if (windowMode === 'custom') {
    const days = Math.max(1, Math.min(365, Math.floor(Number(customDays) || 7)));
    return `${days}d`;
  }
  return '7d';
}

function ExternalHistoryPanel({ device }) {
  const [providers, setProviders] = useState(['codex', 'pi']);
  const [windowMode, setWindowMode] = useState('7d');
  const [customDays, setCustomDays] = useState(7);
  const [previews, setPreviews] = useState({});
  const [results, setResults] = useState({});
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [confirming, setConfirming] = useState(false);

  const supported = Boolean(device && (device.capabilities || []).includes('external_history'));
  const ready = Boolean(device?.routable && supported);
  const duration = externalHistoryDuration(windowMode, customDays);

  useEffect(() => {
    if (!ready) return undefined;
    let cancelled = false;
    requestExternalHistory(device.deviceId, { action: 'status' })
      .then((status) => {
        if (cancelled) return;
        const selected = (status.providers || []).filter(item => item.enabled).map(item => item.provider);
        if (selected.length > 0) setProviders(selected);
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [device?.deviceId, ready]);

  const toggleProvider = (provider) => {
    setPreviews({});
    setResults({});
    setConfirming(false);
    setProviders(current => current.includes(provider)
      ? current.filter(item => item !== provider)
      : [...current, provider]);
  };

  const saveProviders = async () => {
    if (!ready || providers.length === 0) return;
    setBusy('configure');
    setError('');
    setNotice('');
    try {
      await requestExternalHistory(device.deviceId, { action: 'configure', providers });
      setNotice('选择已保存，下次启动本地助手时生效。');
    } catch (err) {
      setError(err.message || '保存失败');
    } finally {
      setBusy('');
    }
  };

  const preview = async () => {
    if (!ready || providers.length === 0 || !duration) return;
    setBusy('preview');
    setError('');
    setNotice('');
    setConfirming(false);
    try {
      const entries = await Promise.all(providers.map(async provider => [
        provider,
        await requestExternalHistory(device.deviceId, {
          action: 'preview',
          provider,
          updatedSince: duration,
        }),
      ]));
      setPreviews(Object.fromEntries(entries));
      setResults({});
    } catch (err) {
      setPreviews({});
      setError(err.message || '检查历史范围失败');
    } finally {
      setBusy('');
    }
  };

  const execute = async (confirmed = false) => {
    if (!confirming && !confirmed) {
      setConfirming(true);
      return;
    }
    setBusy('execute');
    setError('');
    setNotice('');
    try {
      const next = { ...results };
      for (const provider of providers) {
        const previewResult = previews[provider];
        if (!previewResult) continue;
        next[provider] = await requestExternalHistory(device.deviceId, {
          action: 'execute',
          provider,
          updatedSince: previewResult.cutoff,
          operationId: previewResult.operationId,
        });
        setResults({ ...next });
      }
      const resumable = Object.values(next).some(result => result?.quotaReached);
      setNotice(resumable ? '本轮已达到安全预算，可以继续导入。' : '所选历史已导入完成。');
      setConfirming(false);
    } catch (err) {
      setError(err.message || '导入失败');
    } finally {
      setBusy('');
    }
  };

  const continueImport = async () => {
    await execute(true);
  };

  return (
    <section className="external-history-section" aria-labelledby="external-history-title">
      <div className="external-history-heading">
        <span className="catsco-download-icon"><Database size={20} /></span>
        <span>
          <strong id="external-history-title">对话历史来源</strong>
          <small>选择本地助手可以持续学习的来源</small>
        </span>
      </div>

      {!device && <p className="external-history-empty">连接本机设备后即可配置。</p>}
      {device && !supported && (
        <p className="external-history-empty"><AlertCircle size={16} /> 当前设备版本不支持历史管理，请更新桌面端。</p>
      )}
      {device && supported && !device.routable && (
        <p className="external-history-empty"><AlertCircle size={16} /> 本地助手离线，连接后再试。</p>
      )}

      {ready && (
        <>
          <fieldset className="external-history-fieldset">
            <legend>来源</legend>
            <div className="external-history-provider-options">
              {['codex', 'pi'].map(provider => (
                <label key={provider} className="external-history-check">
                  <input
                    type="checkbox"
                    checked={providers.includes(provider)}
                    onChange={() => toggleProvider(provider)}
                  />
                  <span>{provider === 'codex' ? 'Codex' : 'Pi'}</span>
                </label>
              ))}
            </div>
          </fieldset>

          <fieldset className="external-history-fieldset">
            <legend>首次导入</legend>
            <div className="external-history-segments">
              {[
                ['none', '不补历史'],
                ['7d', '最近 7 天'],
                ['custom', '自定义'],
              ].map(([value, label]) => (
                <label key={value} className={windowMode === value ? 'selected' : ''}>
                  <input type="radio" name="history-window" value={value} checked={windowMode === value} onChange={() => { setWindowMode(value); setPreviews({}); }} />
                  <span>{label}</span>
                </label>
              ))}
            </div>
            {windowMode === 'custom' && (
              <label className="external-history-days">
                <span>天数</span>
                <input type="number" min="1" max="365" value={customDays} onChange={event => { setCustomDays(event.target.value); setPreviews({}); }} />
              </label>
            )}
          </fieldset>

          <div className="external-history-actions">
            <button type="button" className="oc-btn oc-btn-default" disabled={busy || providers.length === 0} onClick={saveProviders}>
              {busy === 'configure' ? <RefreshCw size={16} className="spin" /> : <Check size={16} />} 保存选择
            </button>
            {duration && (
              <button type="button" className="oc-btn oc-btn-primary" disabled={busy || providers.length === 0} onClick={preview}>
                {busy === 'preview' ? <RefreshCw size={16} className="spin" /> : <Database size={16} />} 检查范围
              </button>
            )}
          </div>

          {Object.keys(previews).length > 0 && (
            <div className="external-history-preview" aria-live="polite">
              {providers.map(provider => previews[provider] && (
                <div key={provider}>
                  <span>{provider === 'codex' ? 'Codex' : 'Pi'}</span>
                  <span className="external-history-provider-result">
                    <strong>{previews[provider].selectedCount} 个 thread</strong>
                    {results[provider] && (
                      <small>
                        已处理 {results[provider].processedResources || 0}
                        {results[provider].quotaReached ? '，可继续' : results[provider].status === 'completed' ? '，已完成' : `，${results[provider].status || '已结束'}`}
                      </small>
                    )}
                  </span>
                </div>
              ))}
              <p>将读取这些 thread 的完整稳定历史，导入按安全预算分批进行。</p>
              <button type="button" className={confirming ? 'oc-btn oc-btn-danger' : 'oc-btn oc-btn-primary'} disabled={busy} onClick={() => execute()}>
                {busy === 'execute' ? <RefreshCw size={16} className="spin" /> : null}
                {confirming ? '确认开始导入' : '开始导入'}
              </button>
            </div>
          )}

          {Object.keys(results).length > 0 && Object.values(results).some(result => result?.quotaReached) && (
            <button type="button" className="oc-btn oc-btn-primary" disabled={busy} onClick={continueImport}>继续导入</button>
          )}
          {notice && <p className="external-history-notice" aria-live="polite">{notice}</p>}
          {error && <p className="external-history-error" role="alert">{error}</p>}
        </>
      )}
    </section>
  );
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
  const externalHistoryDevice = devices.find(device => device.routable && (device.capabilities || []).includes('external_history'))
    || devices.find(device => (device.capabilities || []).includes('external_history'))
    || devices[0];

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
      <div className="oc-modal catsco-download-modal" onClick={(event) => event.stopPropagation()}>
        <div className="oc-modal-header catsco-download-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3>CatsCo 本机设备</h3>
            <p>当前版本 v{releaseVersion(desktopRelease)}</p>
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

          <ExternalHistoryPanel device={externalHistoryDevice} />

          <div className="catsco-download-list">
          {downloadOptions.map((option) => {
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
    </div>
  );
}
