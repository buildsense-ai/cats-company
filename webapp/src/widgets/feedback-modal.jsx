import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Cat, Plus, X } from 'lucide-react';
import { api } from '../api';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';
import {
  readStorageValue,
  removeStorageValue,
  writeStorageValue,
} from '../utils/storage-access';

const MAX_ATTACHMENTS = 5;
const FEEDBACK_DRAFT_VERSION = 1;

function getDraftKey(user) {
  return `cats_feedback_draft_v${FEEDBACK_DRAFT_VERSION}_${user?.uid || user?.username || 'guest'}`;
}

function readDraft(draftKey) {
  const saved = readStorageValue(draftKey);
  if (!saved) return null;
  try {
    const draft = JSON.parse(saved);
    return draft?.version === FEEDBACK_DRAFT_VERSION ? draft : null;
  } catch (error) {
    console.warn('Failed to restore feedback draft:', error);
    removeStorageValue(draftKey);
    return null;
  }
}

function isEmptyDraft({ category, title, description }) {
  return category === 'bug' && title.trim() === '' && description.trim() === '';
}

export default function FeedbackModal({ onClose, user }) {
  const draftKey = useMemo(() => getDraftKey(user), [user]);
  const initialDraft = useMemo(() => readDraft(draftKey), [draftKey]);
  const category = initialDraft?.category || 'bug';
  const title = initialDraft?.title || '';
  const [description, setDescription] = useState(initialDraft?.description || '');
  const [contact, setContact] = useState('');
  const [includeLogs, setIncludeLogs] = useState(true);
  const [attachments, setAttachments] = useState([]);
  const [isDragging, setIsDragging] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const attachmentsRef = useRef([]);

  const remainingSlots = MAX_ATTACHMENTS - attachments.length;
  const canSubmit = useMemo(() => description.trim().length > 0 && !submitting, [description, submitting]);

  useEffect(() => {
    attachmentsRef.current = attachments;
  }, [attachments]);

  useEffect(() => {
    if (submitted) return;

    const draft = {
      version: FEEDBACK_DRAFT_VERSION,
      category,
      title,
      description,
      saved_at: new Date().toISOString(),
      page_url: window.location.href,
    };

    if (isEmptyDraft(draft)) {
      removeStorageValue(draftKey);
    } else {
      writeStorageValue(draftKey, JSON.stringify(draft));
    }
  }, [category, description, draftKey, submitted, title]);

  useEffect(() => {
    return () => {
      attachmentsRef.current.forEach((item) => URL.revokeObjectURL(item.previewUrl));
    };
  }, []);

  const addFiles = (fileList) => {
    const files = Array.from(fileList || []);
    if (files.length === 0) return;

    const validImages = [];
    let rejectedMessage = '';
    for (const file of files) {
      const validationError = validateImageUpload(file, {
        unsupportedTypeMessage: '请上传图片截图，支持 PNG、JPG、GIF、WebP。',
      });
      if (validationError) {
        rejectedMessage = rejectedMessage || validationError;
        continue;
      }
      validImages.push(file);
    }
    if (validImages.length === 0) {
      setError(rejectedMessage || '请上传图片截图，支持 PNG、JPG、GIF、WebP。');
      return;
    }

    const nextImages = validImages.slice(0, remainingSlots);
    if (nextImages.length === 0) {
      setError(`最多上传 ${MAX_ATTACHMENTS} 张截图。`);
      return;
    }

    setAttachments((prev) => [
      ...prev,
      ...nextImages.map((file) => ({
        id: `${file.name}-${file.lastModified}-${Math.random().toString(16).slice(2)}`,
        file,
        previewUrl: URL.createObjectURL(file),
      })),
    ]);
    setError(validImages.length > nextImages.length ? `最多上传 ${MAX_ATTACHMENTS} 张截图，已保留前 ${MAX_ATTACHMENTS} 张。` : rejectedMessage);
  };

  const removeAttachment = (id) => {
    setAttachments((prev) => {
      const target = prev.find((item) => item.id === id);
      if (target) URL.revokeObjectURL(target.previewUrl);
      return prev.filter((item) => item.id !== id);
    });
  };

  const handleSubmit = async (event) => {
    event.preventDefault();
    if (!description.trim()) {
      setError('请先写一下问题或建议描述。');
      return;
    }

    setSubmitting(true);
    setError('');
    try {
      const uploaded = [];
      for (const item of attachments) {
        const result = await api.uploadFeedbackImage(item.file);
        uploaded.push({
          file_key: result.file_key,
          url: result.url,
          name: result.name || item.file.name,
          size: result.size || item.file.size,
          type: 'image',
        });
      }

      await api.submitFeedback({
        category,
        title: title.trim(),
        description: description.trim(),
        contact: contact.trim(),
        include_logs: includeLogs,
        page_url: window.location.href,
        user_agent: navigator.userAgent,
        attachments: uploaded,
      });

      removeStorageValue(draftKey);
      setSubmitted(true);
    } catch (err) {
      setError(err.message || '提交失败，请稍后再试。');
    } finally {
      setSubmitting(false);
    }
  };

  const handleDrop = (event) => {
    event.preventDefault();
    setIsDragging(false);
    addFiles(event.dataTransfer.files);
  };

  const handlePaste = (event) => {
    const clipboardItems = Array.from(event.clipboardData?.items || []);
    let pastedImages = clipboardItems
      .filter((item) => item.kind === 'file' && item.type.startsWith('image/'))
      .map((item) => item.getAsFile())
      .filter(Boolean);

    if (pastedImages.length === 0) {
      pastedImages = Array.from(event.clipboardData?.files || [])
        .filter((file) => file.type.startsWith('image/'));
    }

    if (pastedImages.length === 0) return;
    event.preventDefault();
    addFiles(pastedImages);
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <section className="oc-modal oc-feedback-modal cc-settings-secondary-surface" role="dialog" aria-modal="true" aria-label="意见反馈" onClick={(event) => event.stopPropagation()}>
        <header className="oc-modal-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3>意见反馈</h3>
            <p>分享你的使用体验、问题或改进建议。</p>
          </div>
          <button type="button" aria-label="关闭" onClick={onClose}><X aria-hidden="true" /></button>
        </header>

        {submitted ? (
          <div className="oc-modal-body">
            <div className="oc-feedback-success">
              <div className="oc-feedback-success-title">已收到，谢谢你的反馈</div>
              <div className="oc-feedback-success-text">我们会结合截图和描述尽快排查处理。</div>
              <button type="button" className="oc-btn oc-btn-primary" onClick={onClose}>关闭</button>
            </div>
          </div>
        ) : (
          <form className="oc-modal-body" onSubmit={handleSubmit}>
            <div className="oc-feedback-intro">
              <span className="oc-feedback-intro-icon" aria-hidden="true"><Cat /></span>
              <div><h4>你的每一条反馈，都在帮助 CatsCo 变得更好</h4><p>请描述遇到的问题或建议，我们会认真查看。</p></div>
            </div>

            <label className={`oc-feedback-message-field ${isDragging ? 'dragging' : ''}`} onDragOver={(event) => { event.preventDefault(); setIsDragging(true); }} onDragLeave={() => setIsDragging(false)} onDrop={handleDrop}>
              <textarea value={description} onChange={(event) => setDescription(event.target.value)} onPaste={handlePaste} maxLength={800} rows={8} required placeholder="描述你遇到的问题或建议" />
              <div className="oc-feedback-media-tools">
                {attachments.length > 0 && <div className="oc-feedback-preview-grid">{attachments.map((item) => <div className="oc-feedback-preview" key={item.id}><img src={item.previewUrl} alt={item.file.name} /><button type="button" aria-label={`移除 ${item.file.name}`} onClick={() => removeAttachment(item.id)}><X /></button></div>)}</div>}
                <span className="oc-feedback-upload-button"><Plus aria-hidden="true" /><span>上传图片/录屏</span><input type="file" accept={IMAGE_UPLOAD_ACCEPT} multiple onChange={(event) => addFiles(event.target.files)} /></span>
              </div>
              <span className="oc-feedback-counter">{description.length}/800</span>
            </label>

            <label className="oc-feedback-contact-field"><span>联系方式 <small>可选</small></span><input value={contact} onChange={(event) => setContact(event.target.value)} maxLength={100} placeholder="手机号、邮箱或其他联系方式，方便后续跟进" /></label>

            {error && <div className="oc-bot-error compact">{error}</div>}

            <div className="oc-feedback-footer-row">
              <label className="oc-feedback-log-consent"><input type="checkbox" checked={includeLogs} onChange={(event) => setIncludeLogs(event.target.checked)} /><span>允许附带基础运行日志，帮助定位问题</span></label>
              <div className="oc-modal-footer">
                <button type="button" className="oc-btn oc-btn-default" onClick={onClose} disabled={submitting}>取消</button>
                <button type="submit" className="oc-btn oc-btn-primary" disabled={!canSubmit}>{submitting ? '提交中...' : '提交'}</button>
              </div>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
