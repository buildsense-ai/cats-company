import React, { useEffect, useState } from 'react';
import { api } from '../api';
import { inferAttachmentType, IMAGE_UPLOAD_ACCEPT, VIDEO_UPLOAD_ACCEPT } from '../utils/upload-rules';
import '../css/openchat-theme.css';

export default function MobileUploadView({ sessionId }) {
  const [status, setStatus] = useState('');
  const [uploaded, setUploaded] = useState([]);
  const [isUploading, setIsUploading] = useState(false);

  useEffect(() => {
    let cancelled = false;
    if (!sessionId) return () => {
      cancelled = true;
    };

    api.getMobileUploadSession(sessionId).then((session) => {
      if (cancelled) return;
      setUploaded(Array.isArray(session.files) ? session.files : []);
    }).catch(() => {
      if (!cancelled) setStatus('上传入口暂时不可用，请回到电脑端重新打开二维码。');
    });

    return () => {
      cancelled = true;
    };
  }, [sessionId]);

  const handleFiles = async (event) => {
    const files = Array.from(event.target.files || []).filter(Boolean);
    event.target.value = '';
    if (files.length === 0 || !sessionId) return;

    setIsUploading(true);
    setStatus(`正在上传 ${files.length} 个文件...`);
    const nextUploaded = [];
    try {
      for (const file of files) {
        const type = inferAttachmentType(file);
        const result = await api.uploadMobileSessionFile(sessionId, file, type);
        nextUploaded.push(result);
        setUploaded((prev) => [...prev, result]);
      }
      setStatus(`本次上传 ${nextUploaded.length} 个，当前已累计 ${uploaded.length + nextUploaded.length} 个。可继续选择，全部传完后回到电脑端发送。`);
    } catch (error) {
      setStatus(error.message || '上传失败，请重试。');
    } finally {
      setIsUploading(false);
    }
  };

  return (
    <main className="mobile-upload-page">
      <section className="mobile-upload-card">
        <h1>手机上传</h1>
        <p>选择手机里的试卷图片或文件，上传后会自动追加到电脑端当前会话。微信内一次通常最多选 9 张，可分多次选择，系统会累计到同一个任务里。</p>
        <div className="mobile-upload-counter" aria-label={`已上传 ${uploaded.length} 个文件`}>
          <span>已上传</span>
          <strong>{uploaded.length}</strong>
          <span>个文件</span>
        </div>
        <label className={`mobile-upload-picker${isUploading ? ' is-disabled' : ''}`}>
          <input
            type="file"
            multiple
            accept={`${IMAGE_UPLOAD_ACCEPT},${VIDEO_UPLOAD_ACCEPT},.pdf,.doc,.docx,.xls,.xlsx,.zip`}
            onChange={handleFiles}
            disabled={isUploading}
          />
          <span>{isUploading ? '上传中...' : (uploaded.length > 0 ? '继续选择图片或文件' : '选择图片或文件')}</span>
        </label>
        <div className="mobile-upload-hint">如果系统提示最多 9 张，点“完成”上传后再回来继续选择下一批。</div>
        {status && <div className="mobile-upload-status">{status}</div>}
        {uploaded.length > 0 && (
          <ul className="mobile-upload-list">
            {uploaded.map((file) => (
              <li key={file.file_key || file.url}>{file.name || file.url}</li>
            ))}
          </ul>
        )}
      </section>
    </main>
  );
}
