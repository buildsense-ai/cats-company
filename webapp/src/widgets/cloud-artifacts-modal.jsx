import React, { useCallback, useEffect, useState } from 'react';
import { Cloud, Copy, ExternalLink, FileCode2, RefreshCw, X } from 'lucide-react';
import { api } from '../api';

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

export default function CloudArtifactsModal({ onClose }) {
  const [artifacts, setArtifacts] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [copiedID, setCopiedID] = useState('');

  const loadArtifacts = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const result = await api.getCloudArtifacts();
      setArtifacts(Array.isArray(result?.artifacts) ? result.artifacts : []);
    } catch (err) {
      setError(err.message || '云端产物读取失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    loadArtifacts();
  }, [loadArtifacts]);

  useEffect(() => {
    const handleKeyDown = (event) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  const copyURL = async (artifact) => {
    try {
      await navigator.clipboard.writeText(artifact.url);
      setCopiedID(artifact.id);
      window.setTimeout(() => setCopiedID(''), 1600);
    } catch {
      setError('链接复制失败，请直接打开后从地址栏复制');
    }
  };

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
              <p>{loading ? '正在读取' : `共 ${artifacts.length} 个`}</p>
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
            <div className="cloud-artifacts-status">还没有已部署的云端产物</div>
          )}
          {artifacts.length > 0 && (
            <div className="cloud-artifacts-list">
              {artifacts.map((artifact) => (
                <article className="cloud-artifact-item" key={artifact.id}>
                  <a className="cloud-artifact-main" href={artifact.url} target="_blank" rel="noopener noreferrer" aria-label={`打开 ${artifact.title}`}>
                    <span className={`cloud-artifact-kind-icon ${artifact.kind}`} aria-hidden="true">
                      {artifact.kind === 'mini_app' ? <Cloud size={18} /> : <FileCode2 size={18} />}
                    </span>
                    <div className="cloud-artifact-copy">
                      <h4>{artifact.title}</h4>
                      <p>
                        <span>{artifact.kind === 'mini_app' ? '小应用' : '网页'}</span>
                        {formatUpdatedAt(artifact.updated_at) && <span>{formatUpdatedAt(artifact.updated_at)}</span>}
                      </p>
                    </div>
                    <ExternalLink className="cloud-artifact-open-icon" size={17} aria-hidden="true" />
                  </a>
                  <div className="cloud-artifact-actions">
                    <button type="button" onClick={() => copyURL(artifact)} aria-label={`复制 ${artifact.title} 链接`} title={copiedID === artifact.id ? '已复制' : '复制链接'}>
                      <Copy size={17} />
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
