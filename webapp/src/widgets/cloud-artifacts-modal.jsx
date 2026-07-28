import React, { useCallback, useEffect, useState } from 'react';
import {
  Cloud,
  Copy,
  ExternalLink,
  FileCode2,
  RefreshCw,
  RotateCcw,
  Trash2,
  X,
} from 'lucide-react';
import { api } from '../api';

const CLOUD_ARTIFACTS_CHANGED_EVENT = 'cc:cloud-artifacts-changed';

function notifyArtifactsChanged(agentUid) {
  window.dispatchEvent(new CustomEvent(CLOUD_ARTIFACTS_CHANGED_EVENT, {
    detail: { agentUid: Number(agentUid) || 0 },
  }));
}

function formatUpdatedAt(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date);
}

function artifactMeta(artifact) {
  const items = [artifact.kind === 'mini_app' ? '小应用' : '网页'];
  if (artifact.publish_version) items.push('发布 v' + artifact.publish_version);
  if (artifact.agent_name) items.push(artifact.agent_name);
  if (artifact.source_title) items.push(artifact.source_title);
  const time = formatUpdatedAt(artifact.status === 'deleted' ? artifact.deleted_at : artifact.updated_at);
  if (time) items.push(artifact.status === 'deleted' ? '删除于 ' + time : time);
  return items;
}

export default function CloudArtifactsModal({ agentUid, onClose }) {
  const [tab, setTab] = useState('active');
  const [artifacts, setArtifacts] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [copiedID, setCopiedID] = useState('');
  const [pendingID, setPendingID] = useState('');
  const [confirmArtifact, setConfirmArtifact] = useState(null);

  const loadArtifacts = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const result = await api.getCloudArtifacts(agentUid, tab);
      setArtifacts(Array.isArray(result?.artifacts) ? result.artifacts : []);
    } catch (err) {
      setError(err.message || '云端产物读取失败');
    } finally {
      setLoading(false);
    }
  }, [agentUid, tab]);

  useEffect(() => {
    setArtifacts([]);
    loadArtifacts();
  }, [loadArtifacts]);

  useEffect(() => {
    const handleKeyDown = (event) => {
      if (event.key !== 'Escape') return;
      if (confirmArtifact) setConfirmArtifact(null);
      else onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [confirmArtifact, onClose]);

  const copyURL = async (artifact) => {
    try {
      await navigator.clipboard.writeText(artifact.url);
      setCopiedID(artifact.id);
      window.setTimeout(() => setCopiedID(''), 1600);
    } catch {
      setError('链接复制失败，请直接打开后从地址栏复制');
    }
  };

  const deleteArtifact = async () => {
    if (!confirmArtifact || pendingID) return;
    const artifact = confirmArtifact;
    setPendingID(artifact.id);
    setError('');
    try {
      await api.deleteCloudArtifact(agentUid, artifact.id);
      setArtifacts((current) => current.filter((item) => item.id !== artifact.id));
      setConfirmArtifact(null);
      notifyArtifactsChanged(agentUid);
    } catch (err) {
      setError(err.message || '删除失败，请稍后重试');
    } finally {
      setPendingID('');
    }
  };

  const restoreArtifact = async (artifact) => {
    if (pendingID) return;
    setPendingID(artifact.id);
    setError('');
    try {
      await api.restoreCloudArtifact(agentUid, artifact.id);
      setArtifacts((current) => current.filter((item) => item.id !== artifact.id));
      notifyArtifactsChanged(agentUid);
    } catch (err) {
      setError(err.message || '恢复失败，请稍后重试');
    } finally {
      setPendingID('');
    }
  };

  const emptyText = tab === 'active' ? '还没有已部署的云端产物' : '回收站是空的';

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <section
        className="oc-modal cloud-artifacts-modal"
        role="dialog"
        aria-modal="true"
        aria-label="云端产物"
        onClick={(event) => event.stopPropagation()}
      >
        <header className="cloud-artifacts-header">
          <div className="cloud-artifacts-heading">
            <span className="cloud-artifacts-heading-icon" aria-hidden="true"><Cloud size={20} /></span>
            <div>
              <h3>云端产物</h3>
              <p>{loading ? '正在读取' : '共 ' + artifacts.length + ' 个'}</p>
            </div>
          </div>
          <div className="cloud-artifacts-header-actions">
            <button type="button" onClick={loadArtifacts} disabled={loading} aria-label="刷新云端产物" title="刷新">
              <RefreshCw size={18} className={loading ? 'is-spinning' : ''} />
            </button>
            <button type="button" onClick={onClose} aria-label="关闭云端产物" title="关闭">
              <X size={18} />
            </button>
          </div>
        </header>

        <div className="cloud-artifacts-tabs" role="tablist" aria-label="产物状态">
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'active'}
            className={tab === 'active' ? 'active' : ''}
            onClick={() => setTab('active')}
          >
            生成物
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'deleted'}
            className={tab === 'deleted' ? 'active' : ''}
            onClick={() => setTab('deleted')}
          >
            回收站
          </button>
        </div>

        <div className="cloud-artifacts-body">
          {loading && artifacts.length === 0 && (
            <div className="cloud-artifacts-status">正在读取云端产物...</div>
          )}
          {!loading && error && (
            <div className="cloud-artifacts-status error">
              <span>{error}</span>
              <button type="button" onClick={loadArtifacts}>重试</button>
            </div>
          )}
          {!loading && !error && artifacts.length === 0 && (
            <div className="cloud-artifacts-status">{emptyText}</div>
          )}
          {artifacts.length > 0 && (
            <div className="cloud-artifacts-list">
              {artifacts.map((artifact) => (
                <article className="cloud-artifact-item" key={artifact.id}>
                  {tab === 'active' ? (
                    <a
                      className="cloud-artifact-main"
                      href={artifact.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      aria-label={'打开 ' + artifact.title}
                    >
                      <ArtifactSummary artifact={artifact} />
                      <ExternalLink className="cloud-artifact-open-icon" size={17} aria-hidden="true" />
                    </a>
                  ) : (
                    <div className="cloud-artifact-main is-deleted">
                      <ArtifactSummary artifact={artifact} />
                    </div>
                  )}
                  <div className="cloud-artifact-actions">
                    {tab === 'active' && (
                      <>
                        <button
                          type="button"
                          onClick={() => copyURL(artifact)}
                          disabled={pendingID === artifact.id}
                          aria-label={'复制 ' + artifact.title + ' 链接'}
                          title={copiedID === artifact.id ? '已复制' : '复制链接'}
                        >
                          <Copy size={17} />
                        </button>
                        {artifact.can_delete && (
                          <button
                            type="button"
                            className="danger"
                            onClick={() => setConfirmArtifact(artifact)}
                            disabled={pendingID === artifact.id}
                            aria-label={'删除 ' + artifact.title}
                            title="删除"
                          >
                            <Trash2 size={17} />
                          </button>
                        )}
                      </>
                    )}
                    {tab === 'deleted' && artifact.can_restore && (
                      <button
                        type="button"
                        onClick={() => restoreArtifact(artifact)}
                        disabled={pendingID === artifact.id}
                        aria-label={'恢复 ' + artifact.title}
                        title="恢复"
                      >
                        <RotateCcw size={17} className={pendingID === artifact.id ? 'is-spinning' : ''} />
                      </button>
                    )}
                  </div>
                </article>
              ))}
            </div>
          )}
        </div>

        {confirmArtifact && (
          <div className="cloud-artifact-confirm-backdrop" onClick={() => !pendingID && setConfirmArtifact(null)}>
            <div
              className="cloud-artifact-confirm"
              role="alertdialog"
              aria-modal="true"
              aria-label="确认删除云端产物"
              onClick={(event) => event.stopPropagation()}
            >
              <h4>删除“{confirmArtifact.title}”？</h4>
              <p>这个链接会立即失效，之后可以从回收站恢复。</p>
              <div className="cloud-artifact-confirm-actions">
                <button type="button" onClick={() => setConfirmArtifact(null)} disabled={Boolean(pendingID)}>
                  取消
                </button>
                <button type="button" className="danger" onClick={deleteArtifact} disabled={Boolean(pendingID)}>
                  {pendingID ? '正在删除...' : '删除'}
                </button>
              </div>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

function ArtifactSummary({ artifact }) {
  return (
    <>
      <span className={'cloud-artifact-kind-icon ' + artifact.kind} aria-hidden="true">
        {artifact.kind === 'mini_app' ? <Cloud size={18} /> : <FileCode2 size={18} />}
      </span>
      <div className="cloud-artifact-copy">
        <h4>{artifact.title}</h4>
        <p>
          {artifactMeta(artifact).map((item, index) => <span key={index}>{item}</span>)}
        </p>
      </div>
    </>
  );
}
