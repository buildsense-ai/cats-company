import React, { useRef, useState } from 'react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';
import PasswordResetForm from './password-reset-form';

export default function ProfileEditor({ user, onClose, onSaved, onOpenRelay }) {
  const fileInputRef = useRef(null);
  const [displayName, setDisplayName] = useState(user?.display_name || '');
  const [avatarUrl, setAvatarUrl] = useState(user?.avatar_url || '');
  const [showThinking, setShowThinking] = useState(() => {
    const saved = localStorage.getItem('cc_show_thinking');
    return saved === null ? true : saved === 'true';
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [showPasswordReset, setShowPasswordReset] = useState(false);
  const resetEmail = user?.email || (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(user?.username || '') ? user.username : '');

  const handleSelectAvatar = async (event) => {
    const file = event.target.files?.[0];
    if (!file) return;

    setError('');
    try {
      const uploaded = await api.uploadFile(file, 'image');
      setAvatarUrl(uploaded.url || '');
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      event.target.value = '';
    }
  };

  const handleSave = async () => {
    setSaving(true);
    setError('');
    try {
      localStorage.setItem('cc_show_thinking', String(showThinking));
      const updated = await api.updateMe(displayName.trim(), avatarUrl || '');
      if (onSaved) onSaved(updated);
      onClose();
    } catch (err) {
      setError(err.message || t('error_server'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal oc-profile-editor-modal" onClick={(e) => e.stopPropagation()}>
        <div className="oc-profile-editor-scroll">
          <div className="oc-modal-title">{t('me_profile_edit')}</div>
          <div className="oc-settings-avatar-block">
            <Avatar name={displayName || user?.username} src={avatarUrl} size={88} />
            <button className="oc-btn oc-btn-default" onClick={() => fileInputRef.current?.click()}>
              {t('me_avatar_pick')}
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept="image/*"
              style={{ display: 'none' }}
              onChange={handleSelectAvatar}
            />
          </div>
          <input
            className="oc-auth-input"
            placeholder="显示昵称（可选）"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
          />
          <div className="oc-settings-secondary" style={{ marginTop: -10, marginBottom: 12 }}>
            登录名称：{user?.username || '-'}。这里只会改变聊天中展示的昵称，不会改变登录名称或邮箱登录。
          </div>
          <div style={{ padding: '16px 0', marginTop: '16px', borderTop: '1px solid var(--v3-border)', borderBottom: '1px solid var(--v3-border)' }}>
            <label style={{ display: 'flex', alignItems: 'center', cursor: 'pointer' }}>
              <input
                type="checkbox"
                checked={showThinking}
                onChange={(e) => setShowThinking(e.target.checked)}
                style={{ marginRight: '10px', width: '16px', height: '16px', accentColor: 'var(--v3-primary)' }}
              />
              <span style={{ color: 'var(--v3-text-main)' }}>显示 AI 思考过程 (Code Mode)</span>
            </label>
          </div>
          <div className="oc-settings-section">
            <div className="oc-settings-section-title">账号安全</div>
            {showPasswordReset ? (
              <PasswordResetForm defaultEmail={resetEmail} />
            ) : (
              <button type="button" className="oc-settings-list-item oc-settings-list-button" onClick={() => setShowPasswordReset(true)}>
                <div className="oc-settings-list-text">
                  <div>重置登录密码</div>
                  <div className="oc-settings-secondary">通过注册邮箱验证码设置新密码。</div>
                </div>
              </button>
            )}
          </div>
          {onOpenRelay && (
            <div className="oc-settings-section">
              <div className="oc-settings-section-title">开发者工具</div>
              <button
                type="button"
                className="oc-settings-list-item oc-settings-list-button"
                onClick={() => {
                  onClose();
                  onOpenRelay();
                }}
              >
                <div className="oc-settings-list-text">
                  <div>CatsCo 中转站</div>
                  <div className="oc-settings-secondary">查看 OpenAI / Anthropic 兼容接入地址。</div>
                </div>
              </button>
            </div>
          )}
          {error && <div className="oc-form-error">{error}</div>}
        </div>
        <div className="oc-settings-actions oc-profile-editor-actions">
          <button className="oc-btn oc-btn-default" onClick={onClose}>{t('cancel')}</button>
          <button className="oc-btn oc-btn-primary" onClick={handleSave} disabled={saving}>
            {saving ? t('loading') : t('save')}
          </button>
        </div>
      </div>
    </div>
  );
}
