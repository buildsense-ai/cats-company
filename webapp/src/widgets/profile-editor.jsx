import React, { useRef, useState } from 'react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';
import PasswordResetForm from './password-reset-form';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';
import { Check, Droplets, LockKeyhole, Moon, Sun, X } from 'lucide-react';

const THEME_OPTIONS = [
  { id: 'light', label: '浅色', description: '明亮、克制的工作界面。', Icon: Sun },
  { id: 'dark', label: '深色', description: '低亮度的专注工作界面。', Icon: Moon },
  { id: 'liquid', label: '液态', description: '绿色主导、蓝紫折射的液态玻璃。', Icon: Droplets },
];

export default function ProfileEditor({
  user,
  theme = 'light',
  onThemeChange,
  liquidThemeAccess = { loading: false, unlocked: false },
  onUnlockLiquidTheme,
  onClose,
  onSaved,
  onOpenRelay,
}) {
  const fileInputRef = useRef(null);
  const [displayName, setDisplayName] = useState(user?.display_name || '');
  const [avatarUrl, setAvatarUrl] = useState(user?.avatar_url || '');
  const [showThinking, setShowThinking] = useState(() => {
    const saved = localStorage.getItem('cc_show_thinking');
    return saved === null ? true : saved === 'true';
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [copyStatus, setCopyStatus] = useState('');
  const [showPasswordReset, setShowPasswordReset] = useState(false);
  const [showLiquidUnlock, setShowLiquidUnlock] = useState(false);
  const [liquidPassword, setLiquidPassword] = useState('');
  const [liquidPasswordLoading, setLiquidPasswordLoading] = useState(false);
  const [liquidPasswordError, setLiquidPasswordError] = useState('');
  const resetEmail = user?.email || (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(user?.username || '') ? user.username : '');
  const userUID = user?.uid || user?.id || '';

  const handleSelectAvatar = async (event) => {
    const file = event.target.files?.[0];
    if (!file) return;

    const validationError = validateImageUpload(file);
    if (validationError) {
      setError(validationError);
      event.target.value = '';
      return;
    }

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

  const handleCopyUID = async () => {
    if (!userUID) return;
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error('clipboard unavailable');
      }
      await navigator.clipboard.writeText(String(userUID));
      setCopyStatus('已复制 UID');
    } catch {
      setCopyStatus(`UID：${userUID}`);
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

  const handleThemeChoice = (nextTheme) => {
    if (nextTheme === 'liquid' && !liquidThemeAccess.unlocked) {
      setShowLiquidUnlock(true);
      setLiquidPasswordError('');
      return;
    }
    onThemeChange?.(nextTheme);
  };

  const handleLiquidUnlock = async (event) => {
    event.preventDefault();
    const password = liquidPassword.trim();
    if (!password) {
      setLiquidPasswordError('请输入液态主题密码。');
      return;
    }

    setLiquidPasswordLoading(true);
    setLiquidPasswordError('');
    try {
      if (!onUnlockLiquidTheme) throw new Error('密码验证暂不可用。');
      await onUnlockLiquidTheme(password);
      setLiquidPassword('');
      setShowLiquidUnlock(false);
    } catch (err) {
      setLiquidPasswordError(err.message || '密码验证失败，请稍后重试。');
    } finally {
      setLiquidPasswordLoading(false);
    }
  };

  return (
    <div className="oc-modal-overlay" onClick={onClose}>
      <div className="oc-modal oc-profile-editor-modal" onClick={(e) => e.stopPropagation()}>
        <div className="oc-profile-editor-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3>设置与资料</h3>
            <p>管理个人资料、账号安全与使用偏好。</p>
          </div>
          <button type="button" className="oc-profile-editor-close" onClick={onClose} aria-label="关闭">
            <X size={18} strokeWidth={1.8} />
          </button>
        </div>
        <div className="oc-profile-editor-scroll">
          <div className="oc-settings-avatar-block">
            <Avatar name={displayName || user?.username} src={avatarUrl} size={88} />
            <button className="oc-btn oc-btn-default" onClick={() => fileInputRef.current?.click()}>
              {t('me_avatar_pick')}
            </button>
            <input
              ref={fileInputRef}
              type="file"
              accept={IMAGE_UPLOAD_ACCEPT}
              style={{ display: 'none' }}
              onChange={handleSelectAvatar}
            />
          </div>
          <div className="oc-profile-name-field">
            <div className="oc-settings-section-title">姓名</div>
            <input
              className="oc-auth-input"
              placeholder="显示昵称（可选）"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </div>
          <div className="oc-profile-identity-card">
            <div>
              <div className="oc-profile-identity-label">我的 UID</div>
              <div className="oc-profile-identity-value">{userUID || '-'}</div>
              <div className="oc-settings-secondary">别人按 UID 搜索时，用这个数字加你或你的虚拟员工。</div>
            </div>
            <button type="button" className="oc-btn oc-btn-default" onClick={handleCopyUID} disabled={!userUID}>
              复制 UID
            </button>
          </div>
          {copyStatus && <div className="oc-settings-secondary" style={{ marginTop: -6, marginBottom: 12 }}>{copyStatus}</div>}
          {onThemeChange && (
            <div className="oc-settings-section">
              <div className="oc-settings-section-title">外观</div>
              <div className="oc-theme-picker" role="radiogroup" aria-label="界面主题">
                {THEME_OPTIONS.map(({ id, label, description, Icon }) => {
                  const locked = id === 'liquid' && !liquidThemeAccess.unlocked;
                  const selected = theme === id;
                  return (
                    <button
                      key={id}
                      type="button"
                      className={`oc-theme-option${selected ? ' is-selected' : ''}${locked ? ' is-locked' : ''}`}
                      role="radio"
                      aria-checked={selected}
                      aria-label={`${label}主题${locked ? '，需要密码' : ''}`}
                      disabled={id === 'liquid' && liquidThemeAccess.loading}
                      onClick={() => handleThemeChoice(id)}
                    >
                      <span className={`oc-theme-preview oc-theme-preview-${id}`} aria-hidden="true">
                        <Icon size={18} strokeWidth={1.8} />
                      </span>
                      <span className="oc-theme-option-copy">
                        <strong>{label}</strong>
                        <span>{description}</span>
                      </span>
                      <span className="oc-theme-option-state" aria-hidden="true">
                        {selected ? <Check size={16} /> : locked ? <LockKeyhole size={15} /> : null}
                      </span>
                    </button>
                  );
                })}
              </div>
              {showLiquidUnlock && !liquidThemeAccess.unlocked && (
                <form className="oc-liquid-unlock" onSubmit={handleLiquidUnlock}>
                  <div className="oc-liquid-unlock-heading">
                    <span className="oc-liquid-unlock-icon" aria-hidden="true"><Droplets size={17} /></span>
                    <span>
                      <strong>解锁液态主题</strong>
                      <small>密码验证成功后，当前浏览器会保存解锁状态。</small>
                    </span>
                  </div>
                  <div className="oc-liquid-unlock-row">
                    <input
                      type="password"
                      value={liquidPassword}
                      onChange={(event) => setLiquidPassword(event.target.value)}
                      placeholder="输入液态主题密码"
                      aria-label="液态主题密码"
                      autoComplete="off"
                      spellCheck="false"
                      disabled={liquidPasswordLoading}
                    />
                    <button type="submit" disabled={liquidPasswordLoading}>
                      {liquidPasswordLoading ? '验证中' : '验证并解锁'}
                    </button>
                  </div>
                  {liquidPasswordError && <div className="oc-liquid-unlock-error" role="alert">{liquidPasswordError}</div>}
                </form>
              )}
            </div>
          )}
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
          <div className="oc-profile-thinking-toggle">
            <label>
              <input
                type="checkbox"
                checked={showThinking}
                onChange={(e) => setShowThinking(e.target.checked)}
              />
              <span>显示 AI 思考过程 (Code Mode)</span>
            </label>
          </div>
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
