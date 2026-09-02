import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  ArrowLeft,
  Check,
  Cloud,
  Copy,
  Download,
  Eye,
  ExternalLink,
  FileText,
  Image as ImageIcon,
  RefreshCw,
  RotateCcw,
  Tag,
  Upload,
  UsersRound,
  Trash2,
  X,
} from 'lucide-react';
import { api, resolveMediaURL } from '../api';
import { useFeedback } from '../components/feedback-system';
import { previewFileDescriptor } from './chat-message';
import PwaDownloadLink from './pwa-download-link';
import CustomSelect from './custom-select';

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
  const creatorType = String(artifact.creator_type || '').trim();
  const creatorName = String(artifact.creator_name || '').trim();
  const uploaderName = String(artifact.uploader_name || '').trim();
  if (creatorType === 'unknown') {
    items.push('来源未知');
  } else if (creatorType === 'agent') {
    const agentName = creatorName || String(artifact.agent_name || '').trim();
    items.push(`${agentName || 'Agent'} 生成`);
  } else if (creatorType === 'user') {
    items.push(creatorName || uploaderName || '上传用户未知');
  } else if (creatorName) {
    items.push(creatorName);
  } else if (uploaderName) {
    items.push(uploaderName);
  } else {
    items.push('上传用户未知');
  }
  if (artifact.source_title) items.push(artifact.source_title);
  const time = formatUpdatedAt(artifact.status === 'deleted' ? artifact.deleted_at : artifact.updated_at);
  if (time) items.push(artifact.status === 'deleted' ? '删除于 ' + time : time);
  return items;
}

function publishArtifactKind(file) {
  const name = String(file?.name || '').toLowerCase();
  if (name.endsWith('.html') || name.endsWith('.htm')) return 'html';
  if (name.endsWith('.zip')) return 'mini_app';
  return '';
}

function publishArtifactTitle(file) {
  return String(file?.name || '新成果').replace(/\.(?:html?|zip)$/i, '') || '新成果';
}

function fileExtension(file) {
  const value = String(file?.name || file?.url || '').split(/[?#]/, 1)[0];
  const extension = value.includes('.') ? value.slice(value.lastIndexOf('.') + 1) : '';
  return extension ? extension.toUpperCase() : '文件';
}

const IMAGE_FILE_EXTENSIONS = new Set(['AVIF', 'BMP', 'GIF', 'HEIC', 'JPEG', 'JPG', 'PNG', 'SVG', 'WEBP']);

function isImageFile(file) {
  const type = String(file?.type || '').trim().toLowerCase();
  const mime = String(file?.mime_type || file?.mime || file?.content_type || '')
    .split(';', 1)[0]
    .trim()
    .toLowerCase();
  const name = String(file?.name || file?.url || '').split(/[?#]/, 1)[0];
  const extension = name.includes('.') ? name.slice(name.lastIndexOf('.') + 1).toUpperCase() : '';
  const url = String(file?.url || '').split(/[?#]/, 1)[0];
  return type === 'image'
    || mime.startsWith('image/')
    || IMAGE_FILE_EXTENSIONS.has(extension)
    || /\/uploads\/images\//.test(url);
}

function formatFileSize(bytes) {
  const size = Number(bytes || 0);
  if (size <= 0) return '';
  if (size < 1024) return size + ' B';
  if (size < 1024 * 1024) return (size / 1024).toFixed(1) + ' KB';
  return (size / (1024 * 1024)).toFixed(1) + ' MB';
}

function fileMeta(file) {
  const items = [{ key: 'type', value: isImageFile(file) ? '图片' : fileExtension(file) }];
  const size = formatFileSize(file.size);
  if (size) items.push({ key: 'size', value: size });
  if (file.topic_name) items.push({ key: 'source', value: file.topic_name });
  const time = formatUpdatedAt(file.created_at);
  if (time) items.push({ key: 'time', value: time });
  return items;
}

function fileTimestamp(file) {
  const value = Date.parse(String(file?.created_at || ''));
  return Number.isFinite(value) ? value : 0;
}

function sortFilesByTime(files) {
  return [...files].sort((left, right) => {
    const timeDelta = fileTimestamp(right) - fileTimestamp(left);
    if (timeDelta !== 0) return timeDelta;
    const messageDelta = Number(right?.message_id || 0) - Number(left?.message_id || 0);
    if (messageDelta !== 0) return messageDelta;
    return Number(left?.block_index || 0) - Number(right?.block_index || 0);
  });
}

function artifactTagList(artifact) {
  return Array.isArray(artifact?.tags) ? artifact.tags : [];
}

export default function CloudArtifactsPanel({
  agentUid,
  topicId,
  initialTab = 'files',
  tab: controlledTab,
  onTabChange,
  onClose,
  onPreviewArtifact,
  onPreviewFile,
}) {
  const feedback = useFeedback();
  const normalizedInitialTab = ['active', 'deleted', 'files'].includes(initialTab)
    ? initialTab
    : 'files';
  const safeInitialTab = !topicId && normalizedInitialTab === 'files' ? 'active' : normalizedInitialTab;
  const [localTab, setLocalTab] = useState(safeInitialTab);
  const requestedTab = controlledTab ?? localTab;
  const tab = !topicId && requestedTab === 'files' ? 'active' : requestedTab;
  const [artifacts, setArtifacts] = useState([]);
  const [files, setFiles] = useState([]);
  const [viewerRelation, setViewerRelation] = useState('');
  const [canPublish, setCanPublish] = useState(false);
  const [tagCounts, setTagCounts] = useState([]);
  const [selectedTags, setSelectedTags] = useState([]);
  const [tagEditorID, setTagEditorID] = useState('');
  const [pendingTagID, setPendingTagID] = useState('');
  const tagCountsRequestSeqRef = useRef(0);
  const [fileCursor, setFileCursor] = useState({ beforeId: 0, beforeCreatedAt: '' });
  const [fileHasMore, setFileHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [copiedID, setCopiedID] = useState('');
  const [pendingID, setPendingID] = useState('');
  const [confirmArtifact, setConfirmArtifact] = useState(null);
  const [artifactScope, setArtifactScope] = useState('current');
  const requestSequenceRef = useRef(0);
  const publishInputRef = useRef(null);

  const selectTab = (nextTab) => {
    if (nextTab === 'files' && !topicId) return;
    if (controlledTab == null) setLocalTab(nextTab);
    onTabChange?.(nextTab);
  };

  const loadContent = useCallback(async ({ append = false, beforeId = 0, beforeCreatedAt = '' } = {}) => {
    const requestID = requestSequenceRef.current + 1;
    requestSequenceRef.current = requestID;
    const isCurrentRequest = () => requestSequenceRef.current === requestID;
    setLoading(true);
    setError('');
    try {
      if (tab === 'files') {
        if (!topicId) {
          if (!isCurrentRequest()) return;
          setFiles([]);
          setFileCursor({ beforeId: 0, beforeCreatedAt: '' });
          setFileHasMore(false);
          setError('进入会话后才能查看历史文件');
          return;
        }
        const fileRequest = { beforeId, limit: 40 };
        if (beforeCreatedAt) fileRequest.beforeCreatedAt = beforeCreatedAt;
        const result = await api.getTopicFiles(topicId, fileRequest);
        if (!isCurrentRequest()) return;
        const nextFiles = Array.isArray(result?.files) ? result.files : [];
        setFiles((current) => sortFilesByTime(append ? [...current, ...nextFiles] : nextFiles));
        setFileCursor({
          beforeId: Number(result?.next_before_id || 0),
          beforeCreatedAt: String(result?.next_before_created_at || ''),
        });
        setFileHasMore(Boolean(result?.has_more));
        return;
      }
      const result = await api.getCloudArtifacts(agentUid, tab);
      if (!isCurrentRequest()) return;
      setArtifacts(Array.isArray(result?.artifacts) ? result.artifacts : []);
      setViewerRelation(String(result?.viewer_relation || ''));
      setCanPublish(Boolean(result?.can_publish) && result?.publish_mode === 'immediate');
      api.getCloudArtifactTags(agentUid).then((tagResult) => {
        if (isCurrentRequest()) {
          setTagCounts(Array.isArray(tagResult?.tags) ? tagResult.tags : []);
        }
      }).catch(() => {
        // 标签计数加载失败不阻塞成果列表。
      });
    } catch (err) {
      if (!isCurrentRequest()) return;
      setError(err.message || (tab === 'files' ? '聊天文件读取失败' : '成果读取失败'));
    } finally {
      if (isCurrentRequest()) setLoading(false);
    }
  }, [agentUid, tab, topicId]);

  useEffect(() => {
    setArtifacts([]);
    setFiles([]);
    setViewerRelation('');
    setCanPublish(false);
    setTagCounts([]);
    setSelectedTags([]);
    setTagEditorID('');
    setFileCursor({ beforeId: 0, beforeCreatedAt: '' });
    setFileHasMore(false);
    loadContent();
    return () => {
      requestSequenceRef.current += 1;
    };
  }, [loadContent]);

  useEffect(() => {
    setArtifactScope(topicId ? 'current' : 'all');
  }, [agentUid, topicId]);

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
      feedback.notify({ tone: 'success', message: '已下架共享成果' });
    } catch (err) {
      setError(err.message || '下架失败，请稍后重试');
    } finally {
      setPendingID('');
    }
  };

  const publishArtifact = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file || pendingID) return;
    const kind = publishArtifactKind(file);
    if (!kind) {
      setError('当前支持发布 HTML 网页或 ZIP 小应用');
      return;
    }
    setPendingID('__publish__');
    setError('');
    try {
      const uploaded = await api.uploadFile(file, 'file');
      const operation = await api.publishCloudArtifact(agentUid, {
        title: publishArtifactTitle(file),
        kind,
        url: new URL(resolveMediaURL(uploaded?.url), window.location.origin).toString(),
        source_topic_id: String(topicId || ''),
      });
      if (operation?.artifact) {
        setArtifacts((current) => [operation.artifact, ...current.filter(
          (artifact) => artifact.id !== operation.artifact.id,
        )]);
      } else {
        await loadContent();
      }
      setArtifactScope('current');
      notifyArtifactsChanged(agentUid);
      feedback.notify({ tone: 'success', message: '已共享内容到云端' });
    } catch (err) {
      setError(err.message || '成果发布失败，请稍后重试');
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
      feedback.notify({ tone: 'success', message: '已恢复共享成果' });
    } catch (err) {
      setError(err.message || '恢复失败，请稍后重试');
    } finally {
      setPendingID('');
    }
  };

  const toggleTagFilter = (tag) => {
    setSelectedTags((current) => current.includes(tag)
      ? current.filter((item) => item !== tag)
      : [...current, tag]);
  };

  const refreshTagCounts = async () => {
    if (!(Number(agentUid || 0) > 0)) return;
    const seq = ++tagCountsRequestSeqRef.current;
    try {
      const result = await api.getCloudArtifactTags(agentUid);
      if (seq !== tagCountsRequestSeqRef.current) return; // 过期响应不覆盖新状态
      const counts = Array.isArray(result?.tags) ? result.tags : [];
      setTagCounts(counts);
      // 已被删除的标签不再作为筛选条件，避免残留筛选把列表清空且无法恢复。
      setSelectedTags((current) => {
        if (current.length === 0) return current;
        const valid = new Set(counts.map((entry) => entry?.tag));
        const next = current.filter((tag) => valid.has(tag));
        return next.length === current.length ? current : next;
      });
    } catch {
      // 标签计数是辅助信息，失败时保持现状即可。
    }
  };

  const saveArtifactTags = async (artifact, nextTags) => {
    if (pendingTagID) return;
    const normalized = [];
    for (const value of nextTags) {
      const tag = String(value || '').trim();
      if (tag && !normalized.includes(tag)) normalized.push(tag);
    }
    setPendingTagID(artifact.id);
    setError('');
    try {
      const result = await api.setCloudArtifactTags(agentUid, artifact.id, normalized);
      const tags = Array.isArray(result?.tags) ? result.tags : normalized;
      setArtifacts((current) => current.map((item) => item.id === artifact.id ? { ...item, tags } : item));
      await refreshTagCounts();
    } catch (err) {
      setError(err.message || '标签保存失败，请稍后重试');
    } finally {
      setPendingTagID('');
    }
  };

  const canFilterArtifactsByTask = Boolean(topicId) && (artifacts.length === 0 || artifacts.some(
    (artifact) => String(artifact?.source_topic_id || '').trim(),
  ));
  const effectiveArtifactScope = topicId && canFilterArtifactsByTask ? artifactScope : 'all';
  const scopedArtifacts = tab === 'active' && effectiveArtifactScope === 'current'
    ? artifacts.filter((artifact) => {
        const sourceTopicID = String(artifact?.source_topic_id || '').trim();
        return !sourceTopicID || sourceTopicID === String(topicId || '').trim();
      })
    : artifacts;
  const visibleArtifacts = selectedTags.length === 0 ? scopedArtifacts : scopedArtifacts.filter(
    (artifact) => selectedTags.every((tag) => artifactTagList(artifact).includes(tag)),
  );
  const emptyText = tab === 'active'
    ? effectiveArtifactScope === 'current'
      ? '当前任务还没有共享成果'
      : selectedTags.length > 0 && scopedArtifacts.length > 0
        ? '没有匹配所选标签的成果'
        : '这个 Agent 还没有共享成果'
    : tab === 'files'
      ? '当前聊天还没有文件'
      : '回收站是空的';
  const visibleCount = tab === 'files'
    ? files.length
    : visibleArtifacts.length;
  const artifactTabSelected = tab === 'active' || tab === 'deleted';
  const isOwner = viewerRelation === 'owner';
  const canManageTags = viewerRelation === 'owner' || viewerRelation === 'friend';
  const artifactRoleLabel = isOwner ? '所有者' : viewerRelation ? '好友' : '';
  const artifactAccessText = !viewerRelation
    ? '正在读取成果权限…'
    : isOwner
      ? canPublish
        ? '成员可查看和上传 · 你可管理全部成果'
        : '成员可查看 · 你可管理全部成果'
      : canPublish
        ? '你可以查看和上传成果，并可管理成果标签'
        : '你可以查看成果，并可管理成果标签';
  const hasAgent = Number(agentUid || 0) > 0;

  return (
    <>
      <button
        className="v3-file-preview-backdrop"
        type="button"
        aria-label="关闭云文件"
        onClick={onClose}
      />
      <section className="v3-file-preview-panel cloud-artifacts-panel" aria-label="云文件">
        <button
          className="v3-file-preview-drag-handle"
          type="button"
          aria-label="关闭云文件"
          onClick={onClose}
        />
        <header className="cloud-artifacts-header">
          <div className="cloud-artifacts-tabs" role="tablist" aria-label="云文件">
            <button
              type="button"
              role="tab"
              aria-selected={tab === 'files'}
              className={tab === 'files' ? 'active' : ''}
              disabled={!topicId}
              title={topicId ? '当前会话文件' : '进入会话后查看文件'}
              onClick={() => selectTab('files')}
            >
              文件
            </button>
            {hasAgent && (
              <button
                type="button"
                role="tab"
                aria-selected={artifactTabSelected}
                className={artifactTabSelected ? 'active' : ''}
                onClick={() => selectTab('active')}
              >
                应用
              </button>
            )}
          </div>
          <div className="cloud-artifacts-header-actions">
            {tab === 'active' && canPublish && (
              <>
                <button
                  type="button"
                  onClick={() => publishInputRef.current?.click()}
                  disabled={Boolean(pendingID)}
                  aria-label="上传成果"
                  title="上传成果"
                >
                  <Upload size={18} className={pendingID === '__publish__' ? 'is-publishing' : ''} />
                </button>
                <input
                  ref={publishInputRef}
                  className="cloud-artifacts-publish-input"
                  type="file"
                  accept=".html,.htm,.zip,text/html,application/zip"
                  onChange={publishArtifact}
                  tabIndex={-1}
                  aria-hidden="true"
                />
              </>
            )}
            {tab === 'active' && isOwner && (
              <button type="button" onClick={() => selectTab('deleted')} aria-label="打开回收站" title="回收站">
                <Trash2 size={18} />
              </button>
            )}
            {tab === 'deleted' && (
              <button type="button" onClick={() => selectTab('active')} aria-label="返回成果列表" title="返回成果">
                <ArrowLeft size={18} />
              </button>
            )}
            <button type="button" onClick={() => loadContent()} disabled={loading} aria-label="刷新当前栏目" title="刷新">
              <RefreshCw size={18} className={loading ? 'is-spinning' : ''} />
            </button>
            <button type="button" onClick={onClose} aria-label="关闭云文件" title="关闭">
              <X size={18} />
            </button>
          </div>
        </header>

        <div className="cloud-artifacts-body">
          {artifactTabSelected && tab !== 'deleted' && (
            <div className="cloud-artifacts-context-note">
              <UsersRound size={17} aria-hidden="true" />
              <div className="cloud-artifacts-context-copy">
                <div className="cloud-artifacts-context-title">
                  <strong>共享成果</strong>
                  {artifactRoleLabel && (
                    <span className="cloud-artifacts-role-badge">{artifactRoleLabel}</span>
                  )}
                </div>
                <span>{artifactAccessText}</span>
              </div>
              <ArtifactScopeSelect
                value={effectiveArtifactScope}
                canSelectCurrent={canFilterArtifactsByTask}
                onChange={setArtifactScope}
              />
            </div>
          )}
          {artifactTabSelected && tab === 'active' && (tagCounts.length > 0 || selectedTags.length > 0) && (
            <div className="cloud-artifacts-tag-filter" role="group" aria-label="按标签筛选">
              {tagCounts.map(({ tag, count }) => (
                <button
                  type="button"
                  key={tag}
                  className={'cloud-artifact-tag-chip' + (selectedTags.includes(tag) ? ' active' : '')}
                  aria-pressed={selectedTags.includes(tag)}
                  onClick={() => toggleTagFilter(tag)}
                >
                  {tag}
                  <span>{count}</span>
                </button>
              ))}
              {selectedTags.length > 0 && (
                <button
                  type="button"
                  className="cloud-artifact-tag-clear"
                  onClick={() => setSelectedTags([])}
                >
                  清空筛选
                </button>
              )}
            </div>
          )}
          {loading && visibleCount === 0 && (
            <div className="cloud-artifacts-status" role="status" aria-live="polite">
              {tab === 'files'
                ? '正在读取文件…'
                : '正在读取成果…'}
            </div>
          )}
          {!loading && error && (
            <div className="cloud-artifacts-status error" role="alert">
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
                  <HistoricalFileItem
                    file={file}
                    key={file.id}
                    onPreviewFile={onPreviewFile}
                  />
                ))}
              </div>
              {fileHasMore && (
                <button
                  type="button"
                  className="cloud-artifacts-load-more"
                  disabled={loading}
                  onClick={() => loadContent({ append: true, ...fileCursor })}
                >
                  {loading ? '正在加载...' : '加载更多'}
                </button>
              )}
            </>
          )}
          {artifactTabSelected && visibleArtifacts.length > 0 && (
            <div className="cloud-artifacts-list">
              {visibleArtifacts.map((artifact) => (
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
                        {canManageTags && (
                          <button
                            type="button"
                            onClick={() => setTagEditorID(tagEditorID === artifact.id ? '' : artifact.id)}
                            disabled={Boolean(pendingTagID)}
                            aria-label={'编辑 ' + artifact.title + ' 的标签'}
                            aria-expanded={tagEditorID === artifact.id}
                            title="标签"
                          >
                            <Tag size={17} />
                          </button>
                        )}
                        <button
                          type="button"
                          onClick={() => copyURL(artifact)}
                          disabled={pendingID === artifact.id}
                          aria-label={'复制 ' + artifact.title + ' 链接'}
                          title={copiedID === artifact.id ? '已复制' : '复制链接'}
                        >
                          <Copy size={17} />
                        </button>
                        {(isOwner || artifact.can_delete) && (
                          <button
                            type="button"
                            className="danger"
                            onClick={() => setConfirmArtifact(artifact)}
                            disabled={pendingID === artifact.id}
                            aria-label={'下架 ' + artifact.title}
                            title="下架"
                          >
                            <Trash2 size={17} />
                          </button>
                        )}
                      </>
                    )}
                    {tab === 'deleted' && (isOwner || artifact.can_restore) && (
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
                  {tab === 'active' && (artifactTagList(artifact).length > 0 || tagEditorID === artifact.id) && (
                    <div className="cloud-artifact-tags-row">
                      {tagEditorID !== artifact.id && artifactTagList(artifact).map((tag) => (
                        <span className="cloud-artifact-tag" key={tag}>
                          {tag}
                          {canManageTags && (
                            <button
                              type="button"
                              onClick={() => saveArtifactTags(artifact, artifactTagList(artifact).filter((item) => item !== tag))}
                              disabled={pendingTagID === artifact.id}
                              aria-label={'移除标签 ' + tag}
                              title="移除标签"
                            >
                              <X size={11} />
                            </button>
                          )}
                        </span>
                      ))}
                      {tagEditorID === artifact.id && (
                        <ArtifactTagEditor
                          artifact={artifact}
                          suggestions={tagCounts}
                          pending={pendingTagID === artifact.id}
                          onAdd={(tag) => saveArtifactTags(artifact, [...artifactTagList(artifact), tag])}
                          onRemove={(tag) => saveArtifactTags(artifact, artifactTagList(artifact).filter((item) => item !== tag))}
                          onRemoveMany={(tags) => saveArtifactTags(artifact, artifactTagList(artifact).filter((item) => !tags.includes(item)))}
                          onClose={() => setTagEditorID('')}
                        />
                      )}
                    </div>
                  )}
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
              aria-label="确认下架成果"
              onClick={(event) => event.stopPropagation()}
            >
              <h4>下架“{confirmArtifact.title}”？</h4>
              <p>下架后其他成员将无法打开，Agent 所有者可以从回收站恢复。</p>
              <div className="cloud-artifact-confirm-actions">
                <button type="button" onClick={() => setConfirmArtifact(null)} disabled={Boolean(pendingID)}>
                  取消
                </button>
                <button type="button" className="danger" onClick={deleteArtifact} disabled={Boolean(pendingID)}>
                  {pendingID ? '正在下架...' : '下架'}
                </button>
              </div>
            </div>
          </div>
        )}
      </section>
    </>
  );
}

function ArtifactScopeSelect({ value, canSelectCurrent, onChange }) {
  return (
    <div className="cloud-artifacts-scope">
      <CustomSelect
        ariaLabel="筛选成果范围"
        className="cloud-artifacts-scope-select"
        density="compact"
        menuClassName="cloud-artifacts-scope-options"
        triggerClassName="cloud-artifacts-scope-trigger"
        value={value}
        onValueChange={onChange}
      >
        <option value="current" disabled={!canSelectCurrent}>当前任务</option>
        <option value="all">全部</option>
      </CustomSelect>
    </div>
  );
}

function ArtifactTagEditor({ artifact, suggestions, pending, onAdd, onRemove, onRemoveMany, onClose }) {
  const [draft, setDraft] = React.useState('');
  const [selected, setSelected] = React.useState(() => new Set());
  const [multi, setMulti] = React.useState(false);
  const currentTags = artifactTagList(artifact);
  const trimmed = draft.trim().slice(0, 32);
  const suggestionItems = suggestions
    .map((item) => item?.tag || item)
    .filter((tag) => tag && !currentTags.includes(tag))
    .slice(0, 5);
  const toggleSelected = (tag) => {
    setSelected((current) => {
      const next = new Set(current);
      if (next.has(tag)) {
        next.delete(tag);
      } else {
        next.add(tag);
      }
      return next;
    });
  };
  const removeSelected = () => {
    if (pending || selected.size === 0) return;
    onRemoveMany([...selected]);
    setSelected(new Set());
  };
  const toggleMulti = () => {
    setSelected(new Set());
    setMulti((current) => !current);
  };
  const submit = () => {
    if (!trimmed || pending) return;
    if (currentTags.includes(trimmed)) {
      setDraft('');
      return;
    }
    onAdd(trimmed);
    setDraft('');
  };
  return (
    <div className="cloud-artifact-tag-editor">
      {currentTags.map((tag) => (
        <span
          className={'cloud-artifact-tag is-editor' + (selected.has(tag) ? ' is-selected' : '') + (multi ? ' is-multi' : '')}
          key={tag}
          role={multi ? 'checkbox' : undefined}
          aria-checked={multi ? selected.has(tag) : undefined}
          aria-label={multi ? '选择标签 ' + tag : undefined}
          tabIndex={multi ? 0 : undefined}
          title={multi ? (selected.has(tag) ? '取消选择' : '选择以批量删除') : undefined}
          onClick={multi ? () => toggleSelected(tag) : undefined}
          onKeyDown={multi ? (event) => {
            if (event.key === 'Enter' || event.key === ' ') {
              event.preventDefault();
              toggleSelected(tag);
            }
          } : undefined}
        >
          {multi && (
            <span className="cloud-artifact-tag-check" aria-hidden="true">
              {selected.has(tag) && <Check size={10} />}
            </span>
          )}
          {tag}
          {!multi && (
            <button
              type="button"
              onClick={(event) => {
                event.stopPropagation();
                onRemove(tag);
              }}
              disabled={pending}
              aria-label={'移除标签 ' + tag}
              title="移除标签"
            >
              <X size={11} />
            </button>
          )}
        </span>
      ))}
      <input
        value={draft}
        placeholder="输入标签，回车添加"
        aria-label={'为 ' + artifact.title + ' 添加标签'}
        maxLength={32}
        disabled={pending}
        onChange={(event) => setDraft(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            submit();
          } else if (event.key === 'Escape') {
            event.stopPropagation();
            onClose();
          }
        }}
      />
      <button type="button" onClick={submit} disabled={pending || !trimmed}>添加</button>
      <button
        type="button"
        onClick={toggleMulti}
        aria-pressed={multi}
        disabled={pending}
      >
        {multi ? '退出多选' : '多选'}
      </button>
      {multi && selected.size > 0 && (
        <button
          type="button"
          className="is-danger"
          onClick={removeSelected}
          disabled={pending}
        >
          删除所选（{selected.size}）
        </button>
      )}
      <button type="button" onClick={onClose} disabled={pending}>完成</button>
      {suggestionItems.length > 0 && (
        <div className="cloud-artifact-tag-suggestions">
          {suggestionItems.map((tag) => (
            <button
              type="button"
              key={tag}
              disabled={pending || currentTags.length >= 12}
              onClick={() => onAdd(tag)}
            >
              {tag}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function ArtifactSummary({ artifact }) {
  return (
    <>
      <span className={'cloud-artifact-kind-icon application ' + artifact.kind} aria-hidden="true">
        <Cloud size={22} />
      </span>
      <div className="cloud-artifact-copy">
        <h4>{artifact.title}</h4>
        <p>
          {artifactMeta(artifact)
            .map((item, index) => <span key={index}>{item}</span>)}
        </p>
      </div>
    </>
  );
}

function FileSummary({ file, thumbnailURL = '' }) {
  const image = isImageFile(file);
  return (
    <>
      <span className={'cloud-artifact-kind-icon file' + (image ? ' image' : '')} aria-hidden="true">
        {image && <ImageIcon className="cloud-file-image-fallback" size={18} />}
        {image && thumbnailURL ? (
          <img
            src={thumbnailURL}
            alt=""
            loading="lazy"
            decoding="async"
            onError={(event) => {
              event.currentTarget.style.visibility = 'hidden';
            }}
          />
        ) : !image ? <FileText size={18} /> : null}
      </span>
      <div className="cloud-artifact-copy">
        <h4>{file.name}</h4>
        <p>
          {fileMeta(file).map((item) => (
            <span className={'cloud-file-meta-' + item.key} key={item.key}>{item.value}</span>
          ))}
        </p>
      </div>
    </>
  );
}

function HistoricalFileItem({ file, onPreviewFile }) {
  const descriptor = previewFileDescriptor(file);
  const canPreview = Boolean(descriptor?.canPreview);
  const image = isImageFile(file);
  const itemLabel = image ? '图片' : '文件';
  const thumbnailDescriptor = image
    ? previewFileDescriptor({ ...file, url: file.thumbnail || file.url })
    : null;
  const thumbnailURL = thumbnailDescriptor?.canPreview
    ? thumbnailDescriptor.url
    : '';
  const openURL = descriptor?.url || file.url || '';
  const downloadURL = descriptor?.downloadURL || openURL;

  return (
    <article className="cloud-artifact-item cloud-file-item">
      {canPreview ? (
        <button
          type="button"
          className="cloud-artifact-main"
          onClick={() => onPreviewFile?.(file)}
          aria-label={'预览' + itemLabel + ' ' + file.name}
        >
          <FileSummary file={file} thumbnailURL={thumbnailURL} />
          <Eye className="cloud-artifact-open-icon" size={17} aria-hidden="true" />
        </button>
      ) : (
        <a
          className="cloud-artifact-main"
          href={openURL}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={'在新窗口打开 ' + file.name}
        >
          <FileSummary file={file} thumbnailURL={thumbnailURL} />
          <ExternalLink className="cloud-artifact-open-icon" size={17} aria-hidden="true" />
        </a>
      )}
      {!canPreview && downloadURL && (
        <div className="cloud-artifact-actions">
          <PwaDownloadLink
            href={downloadURL}
            download={file.name || true}
            target="_blank"
            rel="noopener noreferrer"
            aria-label={'下载 ' + file.name}
            title="下载"
          >
            <Download size={17} />
          </PwaDownloadLink>
        </div>
      )}
    </article>
  );
}
