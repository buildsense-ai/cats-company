import React, { useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { AppWindow, X } from 'lucide-react';
import { api } from '../api';
import useDialogBehavior from '../utils/use-dialog-behavior';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';

export default function AppMetadataEditor({ app, onClose, onSaved }) {
  const [title, setTitle] = useState(app.title || app.name || app.id);
  const [description, setDescription] = useState(app.description || '');
  const [iconURL, setIconURL] = useState(app.icon_url || '');
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState('');
  const dialogRef = useRef(null);
  const titleRef = useRef(null);
  const fileRef = useRef(null);
  const busyRef = useRef(false);
  const activeRef = useRef(true);
  useEffect(() => {
    activeRef.current = true;
    return () => { activeRef.current = false; };
  }, []);
  const busy = saving || uploading;
  const close = () => { if (!busyRef.current) onClose(); };
  useDialogBehavior(dialogRef, { onClose: close, initialFocusRef: titleRef });

  const upload = async (event) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (!file || busyRef.current) return;
    const validation = validateImageUpload(file);
    if (validation) { setError(validation); return; }
    busyRef.current = true;
    setUploading(true);
    setError('');
    try {
      const result = await api.uploadFile(file, 'image');
      if (!result.url) throw new Error('empty upload');
      setIconURL(result.url);
    } catch {
      setError('头像上传失败，请重试。');
    } finally {
      busyRef.current = false;
      setUploading(false);
    }
  };

  const save = async (event) => {
    event.preventDefault();
    if (busyRef.current || !title.trim()) return;
    busyRef.current = true;
    setSaving(true);
    setError('');
    try {
      const updated = await api.updateArtifactApp(app.id, {
        title: title.trim(), description: description.trim(), icon_url: iconURL,
      });
      if (activeRef.current) onSaved(updated);
    } catch (failure) {
      setError(failure.status === 403
        ? '你已没有编辑权限，请关闭后刷新应用列表。'
        : '保存失败，修改内容已保留，请重试。');
    } finally {
      busyRef.current = false;
      setSaving(false);
    }
  };

  return createPortal(
    <div className="cc-app-editor-backdrop" onClick={(event) => { if (event.target === event.currentTarget) close(); }}>
      <section className="cc-app-editor" ref={dialogRef} role="dialog" aria-modal="true" aria-labelledby="cc-app-editor-title" tabIndex={-1}>
        <header>
          <h2 id="cc-app-editor-title">编辑应用信息</h2>
          <button type="button" className="cc-app-editor-close" aria-label="关闭编辑" onClick={close} disabled={busy}><X size={18} /></button>
        </header>
        <p className="cc-app-editor-hint">保存后，所有人看到的应用信息会同步更新。</p>
        <form onSubmit={save}>
          <div className="cc-app-editor-avatar-row">
            <span className="cc-app-card-avatar" aria-hidden="true">{iconURL ? <img src={iconURL} alt="" /> : <AppWindow size={22} />}</span>
            <button type="button" onClick={() => fileRef.current?.click()} disabled={busy}>{uploading ? '正在上传…' : '更换头像'}</button>
            {iconURL && <button type="button" onClick={() => setIconURL('')} disabled={busy}>移除头像</button>}
            <input ref={fileRef} type="file" accept={IMAGE_UPLOAD_ACCEPT} onChange={upload} aria-label="上传应用头像" hidden />
          </div>
          <label>名称<input ref={titleRef} value={title} onChange={(event) => setTitle(event.target.value)} maxLength={128} required disabled={busy} /></label>
          <label>介绍<textarea value={description} onChange={(event) => setDescription(event.target.value)} maxLength={1000} rows={4} placeholder="这个应用可以帮助大家做什么？" disabled={busy} /></label>
          {error && <p className="cc-app-editor-error" role="alert">{error}</p>}
          <footer>
            <button type="button" onClick={close} disabled={busy}>取消</button>
            <button type="submit" disabled={busy || !title.trim()}>{saving ? '正在保存…' : '保存'}</button>
          </footer>
        </form>
      </section>
    </div>, document.body,
  );
}
