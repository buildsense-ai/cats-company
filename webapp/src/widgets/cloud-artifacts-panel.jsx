import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Cloud,
  Copy,
  Eye,
  FileCode2,
  FileText,
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

function fileExtension(file) {
  const value = String(file?.name || file?.url || '').split(/[?#]/, 1)[0];
  const extension = value.includes('.') ? value.slice(value.lastIndexOf('.') + 1) : '';
  return extension ? extension.toUpperCase() : '文件';
}

function formatFileSize(bytes) {
  const size = Number(bytes || 0);
  if (size <= 0) return '';
  if (size < 1024) return size + ' B';
  if (size < 1024 * 1024) return (size / 1024).toFixed(1) + ' KB';
  return (size / (1024 * 1024)).toFixed(1) + ' MB';
}

function fileMeta(file) {
  const items = [fileExtension(file)];
  const size = formatFileSize(file.size);
  if (size) items.push(size);
  if (file.topic_name) items.push(file.topic_name);
  const time = formatUpdatedAt(file.created_at);
  if (time) items.push(time);
  return items;
}

export default function CloudArtifactsPanel({
  agentUid,
  onClose,
  onPreviewArtifact,
  onPreviewFile,
}) {
  const [tab, setTab] = useState('active');
  const [artifacts, setArtifacts] = useState([]);
  const [files, setFiles] = useState([]);
  const [fileCursor, setFileCursor] = useState(0);
  const [fileHasMore, setFileHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [copiedID, setCopiedID] = useState('');
  const [pendingID, setPendingID] = useState('');
  const [confirmArtifact, setConfirmArtifact] = useState(null);
  const requestSequenceRef = useRef(0);

  const loadContent = useCallback(async ({ append = false, beforeId = 0 } = {}) => {
    const requestID = requestSequenceRef.current + 1;
    requestSequenceRef.current = requestID;
    const isCurrentRequest = () => requestSequenceRef.current === requestID;
    setLoading(true);
    setError('');
    try {
      if (tab === 'files') {
        const result = await api.getAgentFiles(agentUid, { beforeId, limit: 40 });
        if (!isCurrentRequest()) return;
        const nextFiles = Array.isArray(result?.files) ? result.files : [];
        setFiles((current) => append ? [...current, ...nextFiles] : nextFiles);
        setFileCursor(Number(result?.next_before_id || 0));
        setFileHasMore(Boolean(result?.has_more));
        return;
      }
      const result = await api.getCloudArtifacts(agentUid, tab);
      if (!isCurrentRequest()) return;
      setArtifacts(Array.isArray(result?.artifacts) ? result.artifacts : []);
    } catch (err) {
      if (!isCurrentRequest()) return;
      setError(err.message || (tab === 'files' ? '历史文件读取失败' : '云端产物读取失败'));
    } finally {
      if (isCurrentRequest()) setLoading(false);
    }
  }, [agentUid, tab]);

  useEffect(() => {
    setArtifacts([]);
    setFiles([]);
    setFileCursor(0);
    setFileHasMore(false);
    loadContent();
    return () => {
      requestSequenceRef.current += 1;
    };
  }, [loadContent]);

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

  const emptyText = tab === 'active'
    ? '还没有已部署的网页'
    : tab === 'files'
      ? '还没有找到这个 Bot 生成的文件'
      : '回收站是空的';
  const visibleCount = tab === 'files' ? files.length : artifacts.length;

  return (
    <>
      <button
        className="v3-file-preview-backdrop"
        type="button"
        aria-label="关闭产物"
        onClick={onClose}
      />
      <section className="v3-file-preview-panel cloud-artifacts-panel" aria-label="产物">
        <button
          className="v3-file-preview-drag-handle"
          type="button"
          aria-label="关闭产物"
          onClick={onClose}
        />
        <header className="cloud-artifacts-header">
          <div className="cloud-artifacts-heading">
            <span className="cloud-artifacts-heading-icon" aria-hidden="true"><Cloud size={20} /></span>
            <div>
              <h3>产物</h3>
              <p>{loading ? '正在读取' : '共 ' + visibleCount + ' 个'}</p>
            </div>
          </div>
          <div className="cloud-artifacts-header-actions">
            <button type="button" onClick={() => loadContent()} disabled={loading} aria-label="刷新产物" title="刷新">
              <RefreshCw size={18} className={loading ? 'is-spinning' : ''} />
            </button>
            <button type="button" onClick={onClose} aria-label="关闭产物" title="关闭">
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
            网页
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'files'}
            className={tab === 'files' ? 'active' : ''}
            onClick={() => setTab('files')}
          >
            文件
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
          {loading && visibleCount === 0 && (
            <div className="cloud-artifacts-status">正在读取产物...</div>
          )}
          {!loading && error && (
            <div className="cloud-artifacts-status error">
              <span>{error}</span>
              <button type="button" onClick={() => loadContent()}>重试</button>
            </div>
          )}
          {!loading && !error && visibleCount === 0 && (
            <div className="cloud-artifacts-status">{emptyText}</div>
          )}
          {tab === 'files' && files.length > 0 && (
            <>
              <div className="cloud-artifacts-list">
                {files.map((file) => (
                  <article className="cloud-artifact-item cloud-file-item" key={file.id}>
                    <button
                      type="button"
                      className="cloud-artifact-main"
                      onClick={() => onPreviewFile?.(file)}
                      aria-label={'预览文件 ' + file.name}
                    >
                      <FileSummary file={file} />
                      <Eye className="cloud-artifact-open-icon" size={17} aria-hidden="true" />
                    </button>
                  </article>
                ))}
              </div>
              {fileHasMore && (
                <button
                  type="button"
                  className="cloud-artifacts-load-more"
                  disabled={loading}
                  onClick={() => loadContent({ append: true, beforeId: fileCursor })}
                >
                  {loading ? '正在加载...' : '加载更多'}
                </button>
              )}
            </>
          )}
          {tab !== 'files' && artifacts.length > 0 && (
            <div className="cloud-artifacts-list">
              {artifacts.map((artifact) => (
                <article className="cloud-artifact-item" key={artifact.id}>
                  {tab === 'active' ? (
                    <button
                      type="button"
                      className="cloud-artifact-main"
                      onClick={() => onPreviewArtifact?.(artifact)}
                      aria-label={'预览 ' + artifact.title}
                    >
                      <ArtifactSummary artifact={artifact} />
                      <Eye className="cloud-artifact-open-icon" size={17} aria-hidden="true" />
                    </button>
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
    </>
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

function FileSummary({ file }) {
  return (
    <>
      <span className="cloud-artifact-kind-icon file" aria-hidden="true">
        <FileText size={18} />
      </span>
      <div className="cloud-artifact-copy">
        <h4>{file.name}</h4>
        <p>
          {fileMeta(file).map((item, index) => <span key={index}>{item}</span>)}
        </p>
      </div>
    </>
  );
}
