import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { AlertCircle, Apple, Check, Copy, Database, Download, ExternalLink, Laptop, Monitor, RefreshCw, Trash2, X } from 'lucide-react';
import { api, getApiBaseURL, getWebSocketURL, requestExternalHistory } from '../api';
import PwaDownloadLink from './pwa-download-link';

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

export function externalHistoryDuration(windowMode, customDays) {
  if (windowMode === 'none') return '';
  if (windowMode === 'custom') {
    const days = Math.max(1, Math.min(365, Math.floor(Number(customDays) || 7)));
    return `${days}d`;
  }
  return '7d';
}

export function summarizeExternalHistoryProgress(byProvider = {}) {
  // Aggregate exact processed/total across providers. When any provider reports
  // total=null (discovering/indeterminate), the aggregate is indeterminate so
  // the modal shows discovering copy rather than a fake 0%.
  let indeterminate = false;
  const totals = Object.values(byProvider).reduce((summary, item) => {
    const rawTotal = item?.total;
    if (rawTotal === null || rawTotal === undefined) {
      indeterminate = true;
      return summary;
    }
    const total = Math.max(0, Math.floor(Number(rawTotal) || 0));
    const processed = Math.max(0, Math.min(total, Math.floor(Number(item?.processed) || 0)));
    return {
      processed: summary.processed + processed,
      total: summary.total + total,
    };
  }, { processed: 0, total: 0 });
  return {
    ...totals,
    indeterminate,
    percentage: !indeterminate && totals.total > 0 ? Math.round((totals.processed / totals.total) * 100) : 0,
  };
}

function idleImportProgress() {
  return { phase: 'idle', provider: '', byProvider: {}, received: false };
}

function importStatusMap(status) {
  return Object.fromEntries((status?.imports || []).map(item => [item.provider, item]));
}

function previewProgress(entries) {
  return Object.fromEntries(entries.map(([provider, result]) => [provider, {
    processed: Math.max(0, Math.floor(Number(result?.processedResources) || 0)),
    total: Math.max(0, Math.floor(Number(result?.selectedCount) || 0)),
    reported: Number(result?.processedResources) > 0,
  }]));
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
  const [importProgress, setImportProgress] = useState(idleImportProgress);
  const [importHistory, setImportHistory] = useState({});

  const supported = Boolean(device && (device.capabilities || []).includes('external_history'));
  const ready = Boolean(device?.routable && supported);
  const duration = externalHistoryDuration(windowMode, customDays);
  const progress = summarizeExternalHistoryProgress(importProgress.byProvider);
  const progressPercentage = importProgress.phase === 'complete' ? 100 : progress.percentage;
  // The modal must remain indeterminate when any running provider reports
  // total=null (discovering), showing discovering copy rather than a fake 0%.
  const progressDeterminate = importProgress.phase !== 'checking'
    && (importProgress.phase !== 'running'
      || (importProgress.received && !progress.indeterminate));
  const progressRatio = progressPercentage / 100;
  const providerLabel = (provider) => provider === 'codex' ? 'Codex' : provider === 'pi' ? 'Pi' : provider;
  const progressCopy = (() => {
    switch (importProgress.phase) {
      case 'checking':
        return ['正在检查', `正在确认 ${providers.map(providerLabel).join('、')} 的可导入范围`];
      case 'ready':
        return ['等待确认', '范围已确认，等待开始导入'];
      case 'running':
        return [
          importProgress.received && !progress.indeterminate ? `${progressPercentage}%` : '正在确认范围',
          importProgress.received
            ? (progress.indeterminate
              ? `正在确认 ${providerLabel(importProgress.provider)} 的可导入范围`
              : `正在导入 ${providerLabel(importProgress.provider)}，已处理 ${progress.processed} / ${progress.total}`)
            : `正在等待 ${providerLabel(importProgress.provider)} 返回进度`,
        ];
      case 'paused':
        return [`${progressPercentage}%`, '已达到安全限制，可以继续导入剩余历史'];
      case 'complete':
        return ['100%', '所选历史已全部导入'];
      case 'error':
        return ['导入中断', '已保留当前结果，可以重新检查或继续'];
      default:
        return duration
          ? ['未开始', '检查范围后开始导入']
          : ['无需导入', '当前仅保存持续学习来源，不补充历史'];
    }
  })();

  useEffect(() => {
    if (!ready) return undefined;
    let cancelled = false;
    requestExternalHistory(device.deviceId, { action: 'status' })
      .then((status) => {
        if (cancelled) return;
        const selected = (status.providers || []).filter(item => item.enabled).map(item => item.provider);
        if (selected.length > 0) setProviders(selected);
        const imports = importStatusMap(status);
        setImportHistory(imports);

        const resumableEntries = selected
          .map(provider => [provider, imports[provider]])
          .filter(([, item]) => item?.resumable);
        if (resumableEntries.length > 0) {
          const restoredPreviews = Object.fromEntries(resumableEntries.map(([provider, item]) => [provider, {
            ...item,
            cutoff: duration,
            existingOperation: true,
          }]));
          setPreviews(restoredPreviews);
          setResults(Object.fromEntries(resumableEntries));
          setImportProgress({
            phase: 'paused',
            provider: '',
            byProvider: previewProgress(resumableEntries),
            received: true,
          });
          return;
        }

        const selectedImports = selected.map(provider => imports[provider]).filter(Boolean);
        if (selected.length > 0 && selectedImports.length === selected.length
          && selectedImports.every(item => item.status === 'completed')) {
          setImportProgress({
            phase: 'complete',
            provider: '',
            byProvider: previewProgress(selected.map(provider => [provider, imports[provider]])),
            received: true,
          });
        }
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [device?.deviceId, ready]);

  const toggleProvider = (provider) => {
    setPreviews({});
    setResults({});
    setConfirming(false);
    setImportProgress(idleImportProgress());
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
      const configured = await requestExternalHistory(device.deviceId, { action: 'configure', providers });
      setNotice(configured.appliedImmediately
        ? '选择已保存，持续学习已启用。'
        : '选择已保存，重启本地助手后生效。');
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
    setImportProgress({ phase: 'checking', provider: '', byProvider: {}, received: false });
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
      setResults(Object.fromEntries(entries.filter(([, result]) => result?.resumable)));
      const restoredProgress = previewProgress(entries);
      setImportProgress({
        phase: 'ready',
        provider: '',
        byProvider: restoredProgress,
        received: Object.values(restoredProgress).some(item => item.processed > 0),
      });
    } catch (err) {
      setPreviews({});
      setImportProgress(current => ({ ...current, phase: 'error' }));
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
    const importProviders = providers.filter(provider => previews[provider]);
    const startingProgress = previewProgress(importProviders.map(provider => [provider, previews[provider]]));
    setImportProgress({
      phase: 'running',
      provider: importProviders[0] || '',
      byProvider: startingProgress,
      received: Object.values(startingProgress).some(item => item.processed > 0),
    });
    try {
      const next = { ...results };
      for (const provider of importProviders) {
        const previewResult = previews[provider];
        setImportProgress(current => ({ ...current, phase: 'running', provider }));
        const executionResult = await requestExternalHistory(device.deviceId, {
          action: 'execute',
          provider,
          updatedSince: previewResult.cutoff,
          operationId: previewResult.operationId,
        }, {
          onProgress: (providerProgress) => {
            setImportProgress(current => ({
              ...current,
              received: true,
              provider,
              byProvider: {
                ...current.byProvider,
                [provider]: { ...providerProgress, reported: true },
              },
            }));
          },
        });
        const refreshedStatus = await requestExternalHistory(device.deviceId, { action: 'status' })
          .catch(() => null);
        const refreshedImports = importStatusMap(refreshedStatus);
        const durableResult = refreshedImports[provider];
        const result = durableResult ? { ...executionResult, ...durableResult } : executionResult;
        next[provider] = result;
        setResults({ ...next });
        setImportHistory(current => durableResult
          ? { ...current, ...refreshedImports }
          : {
              ...current,
              [provider]: {
                ...current[provider],
                ...result,
                provider,
                operationId: previewResult.operationId,
                selectedCount: previewResult.selectedCount,
              },
            });
        setImportProgress(current => {
          const currentProvider = current.byProvider[provider] || { processed: 0, total: 0, reported: false };
          const reportedProcessed = Math.max(0, Math.floor(Number(result?.processedResources) || 0));
          const completed = result?.status === 'completed' && !result?.quotaReached;
          const durableTotal = Math.max(0, Math.floor(Number(durableResult?.selectedCount) || 0));
          const total = durableResult
            ? durableTotal
            : Math.max(currentProvider.total, completed ? reportedProcessed : 0);
          const processed = durableResult
            ? Math.min(total, reportedProcessed)
            : completed
            ? total
            : currentProvider.reported
              ? currentProvider.processed
              : Math.max(currentProvider.processed, Math.min(total, reportedProcessed));
          return {
            ...current,
            received: current.received || completed || reportedProcessed > 0,
            byProvider: {
              ...current.byProvider,
              [provider]: { ...currentProvider, processed, total },
            },
          };
        });
      }
      const resumable = Object.values(next).some(result => result?.quotaReached || result?.resumable);
      setImportProgress(current => ({
        ...current,
        phase: resumable ? 'paused' : 'complete',
        provider: '',
        received: true,
        byProvider: resumable ? current.byProvider : Object.fromEntries(
          Object.entries(current.byProvider).map(([provider, value]) => [provider, { ...value, processed: value.total }]),
        ),
      }));
      setNotice(resumable ? '本轮已达到安全预算，可以继续导入。' : '所选历史已导入完成。');
      setConfirming(false);
    } catch (err) {
      setImportProgress(current => ({ ...current, phase: 'error' }));
      // Distinguish oversized record, generic source failure, and device
      // timeout/offline. Respect details.resumable for oversized records so
      // the copy does not encourage an immediate retry that deterministically
      // fails; durable prior progress is preserved.
      if (err?.oversized) {
        setError(err.message || '历史记录超过安全限制，该条记录目前无法导入；已完成的历史进度已保留。');
        setNotice(err.resumable
          ? '已保留已完成的导入进度，可以继续导入剩余历史。'
          : '该条记录目前无法导入；已完成的历史进度已保留。');
      } else if (err?.sourceFailed) {
        setError(err.message || '外部历史来源执行失败，请检查来源状态后重试。');
      } else if (err?.timeout) {
        setError(err.message || '本地设备暂时离线，请检查连接后重试。');
      } else {
        setError(err.message || '导入失败');
      }
    } finally {
      setBusy('');
    }
  };
  const resumable = Object.values(results).some(result => result?.quotaReached || result?.resumable);

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
                  <span className="external-history-provider-copy">
                    <span>{provider === 'codex' ? 'Codex' : 'Pi'}</span>
                    {importHistory[provider] && (
                      <small>
                        {importHistory[provider].status === 'completed' ? '已完成' : '已导入'}
                        {' '}{importHistory[provider].processedResources || 0} / {importHistory[provider].selectedCount || 0}
                      </small>
                    )}
                  </span>
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
                  <input type="radio" name="history-window" value={value} checked={windowMode === value} onChange={() => {
                    setWindowMode(value);
                    setPreviews({});
                    setResults({});
                    setConfirming(false);
                    setImportProgress(idleImportProgress());
                  }} />
                  <span>{label}</span>
                </label>
              ))}
            </div>
            {windowMode === 'custom' && (
              <label className="external-history-days">
                <span>天数</span>
                <input type="number" min="1" max="365" value={customDays} onChange={event => {
                  setCustomDays(event.target.value);
                  setPreviews({});
                  setResults({});
                  setConfirming(false);
                  setImportProgress(idleImportProgress());
                }} />
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

          <div className={`external-history-progress is-${importProgress.phase}${progressDeterminate ? '' : ' is-indeterminate'}`} aria-live="polite">
            <div className="external-history-progress-copy">
              <span>导入进度</span>
              <strong>{progressCopy[0]}</strong>
            </div>
            <div
              className="external-history-progress-track"
              role="progressbar"
              aria-label="历史导入进度"
              aria-valuemin="0"
              aria-valuemax="100"
              aria-valuenow={progressDeterminate ? progressPercentage : undefined}
              aria-valuetext={progressCopy[1]}
            >
              <span
                className="external-history-progress-value"
                style={{ '--external-history-progress': progressRatio }}
              />
            </div>
            <small>{progressCopy[1]}</small>
          </div>

          {Object.keys(previews).length > 0 && (
            <div className="external-history-preview" aria-live="polite">
              {providers.map(provider => previews[provider] && (
                <div key={provider}>
                  <span>{provider === 'codex' ? 'Codex' : 'Pi'}</span>
                  <span className="external-history-provider-result">
                    <strong>{previews[provider].selectedCount} 个对话</strong>
                    {results[provider] && (
                      <small>
                        已处理 {results[provider].processedResources || 0}
                        {results[provider].quotaReached ? '，可继续' : results[provider].status === 'completed' ? '，已完成' : `，${results[provider].status || '已结束'}`}
                      </small>
                    )}
                  </span>
                </div>
              ))}
              <p>将导入这些对话的可用历史记录，并按安全限制分批处理。</p>
              <button
                type="button"
                className={confirming ? 'oc-btn oc-btn-danger' : 'oc-btn oc-btn-primary'}
                disabled={busy}
                onClick={() => resumable ? execute(true) : execute()}
              >
                {busy === 'execute' ? <RefreshCw size={16} className="spin" /> : null}
                {resumable ? '继续导入' : confirming ? '确认开始导入' : '开始导入'}
              </button>
            </div>
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
      const deviceResp = await api.getDevices();
      setDevices(deviceResp.devices || []);
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
              <button
                type="button"
                className="catsco-download-action"
                onClick={() => handleUnlinkDevice(device.deviceId)}
                aria-label={`解除 ${device.displayName || device.deviceId} 的连接`}
                title="解除设备连接"
              >
                <Trash2 size={16} />
              </button>
            </div>
          ))}

          </div>

          <ExternalHistoryPanel device={externalHistoryDevice} />

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
