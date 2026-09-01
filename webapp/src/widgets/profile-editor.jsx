import React, { useEffect, useRef, useState } from 'react';
import { api } from '../api';
import t from '../i18n';
import Avatar from './avatar';
import PasswordResetForm from './password-reset-form';
import NotificationSettings from './notification-settings';
import { IMAGE_UPLOAD_ACCEPT, validateImageUpload } from '../utils/upload-rules';
import { readStorageValue, writeStorageValue } from '../utils/storage-access';
import {
  ArrowLeft,
  Bell,
  BrainCircuit,
  Check,
  ChevronRight,
  CreditCard,
  Download,
  Droplets,
  Fingerprint,
  Frown,
  KeyRound,
  Laptop,
  LockKeyhole,
  LogOut,
  Moon,
  Pencil,
  ShieldCheck,
  Sun,
  UserRound,
  X,
} from 'lucide-react';

const THEME_OPTIONS = [
  { id: 'light', label: '浅色', description: '明亮、克制的工作界面。', Icon: Sun },
  { id: 'dark', label: '深色', description: '低亮度的专注工作界面。', Icon: Moon },
  { id: 'liquid', label: '液态浅色', description: '浅色玻璃、蓝紫折射的通透界面。', Icon: Droplets },
  { id: 'liquid-green', label: '液态绿色', description: '深色玻璃、绿色高光的经典界面。', Icon: Droplets },
];

const isLiquidThemeOption = (theme) => theme === 'liquid' || theme === 'liquid-green';
const MOBILE_PANE_LABELS = {
  home: '设置',
  profile: '个人资料',
  account: '账号安全',
  appearance: '外观',
  notifications: '消息通知',
  behavior: 'AI 行为',
};

export default function ProfileEditor({
  user,
  theme = 'light',
  onThemeChange,
  liquidThemeAccess = { loading: false, unlocked: false },
  onUnlockLiquidTheme,
  onClose,
  onSaved,
  onOpenRelay,
  onOpenFeedback,
  onOpenDownload,
  onOpenDesktopConnect,
  onLogout,
}) {
  const fileInputRef = useRef(null);
  const modalRef = useRef(null);
  const mobileBackRef = useRef(null);
  const [displayName, setDisplayName] = useState(user?.display_name || '');
  const [avatarUrl, setAvatarUrl] = useState(user?.avatar_url || '');
  const [showThinking, setShowThinking] = useState(() => {
    const saved = readStorageValue('cc_show_thinking');
    return saved === null ? true : saved === 'true';
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [copyStatus, setCopyStatus] = useState('');
  const [showPasswordReset, setShowPasswordReset] = useState(false);
  const [showLiquidUnlock, setShowLiquidUnlock] = useState(false);
  const [pendingLiquidTheme, setPendingLiquidTheme] = useState('liquid');
  const [liquidPassword, setLiquidPassword] = useState('');
  const [liquidPasswordLoading, setLiquidPasswordLoading] = useState(false);
  const [liquidPasswordError, setLiquidPasswordError] = useState('');
  const [mobilePane, setMobilePane] = useState('home');
  const resetEmail = user?.email || (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(user?.username || '') ? user.username : '');
  const userUID = user?.uid || user?.id || '';
  const currentThemeLabel = THEME_OPTIONS.find(({ id }) => id === theme)?.label || '浅色';

  useEffect(() => {
    if (window.matchMedia?.('(max-width: 768px)').matches) {
      if (mobilePane === 'home') modalRef.current?.focus({ preventScroll: true });
      else mobileBackRef.current?.focus({ preventScroll: true });
    }
  }, [mobilePane]);

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
      writeStorageValue('cc_show_thinking', String(showThinking));
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
    if (isLiquidThemeOption(nextTheme) && !liquidThemeAccess.unlocked) {
      setPendingLiquidTheme(nextTheme);
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
      await onUnlockLiquidTheme(password, pendingLiquidTheme);
      setLiquidPassword('');
      setShowLiquidUnlock(false);
    } catch (err) {
      setLiquidPasswordError(err.message || '密码验证失败，请稍后重试。');
    } finally {
      setLiquidPasswordLoading(false);
    }
  };

  const openMobileDestination = (callback) => {
    onClose();
    callback?.();
  };

  return (
    <div className="oc-modal-overlay oc-profile-editor-overlay" onClick={onClose}>
      <div
        ref={modalRef}
        className="oc-modal oc-profile-editor-modal cc-settings-secondary-surface"
        role="dialog"
        tabIndex={-1}
        data-mobile-pane={mobilePane}
        aria-modal="true"
        aria-labelledby="profile-editor-dialog-title"
        aria-describedby="profile-editor-dialog-description"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="oc-profile-editor-header cc-settings-secondary-header">
          <div className="cc-settings-secondary-header-copy">
            <h3 id="profile-editor-dialog-title">设置与资料</h3>
            <p id="profile-editor-dialog-description">管理个人资料、账号安全与使用偏好。</p>
          </div>
          <button
            type="button"
            className="oc-profile-editor-close"
            onClick={onClose}
            aria-label="关闭"
          >
            <X size={18} strokeWidth={1.8} />
          </button>
        </div>
        <div className="oc-profile-mobile-page-header">
          {mobilePane !== 'home' && (
            <button
              ref={mobileBackRef}
              type="button"
              className="oc-profile-mobile-back"
              onClick={() => setMobilePane('home')}
              aria-label="返回设置"
            >
              <ArrowLeft size={24} aria-hidden="true" />
            </button>
          )}
          <strong>{MOBILE_PANE_LABELS[mobilePane]}</strong>
        </div>
        <div className="oc-profile-editor-scroll">
          <div className="oc-profile-mobile-home">
            <section className="oc-profile-mobile-home-section" aria-labelledby="mobile-settings-account-title">
              <h4 id="mobile-settings-account-title">账户</h4>
              <div className="oc-profile-mobile-home-card">
                <button type="button" onClick={() => setMobilePane('profile')}>
                  <span className="oc-profile-mobile-home-icon" aria-hidden="true"><UserRound size={24} /></span>
                  <span>个人资料</span>
                  <ChevronRight size={21} aria-hidden="true" />
                </button>
                <button type="button" onClick={() => setMobilePane('account')}>
                  <span className="oc-profile-mobile-home-icon" aria-hidden="true"><ShieldCheck size={24} /></span>
                  <span>账号安全</span>
                  <ChevronRight size={21} aria-hidden="true" />
                </button>
                {onOpenRelay && (
                  <button type="button" onClick={() => openMobileDestination(onOpenRelay)}>
                    <span className="oc-profile-mobile-home-icon" aria-hidden="true"><KeyRound size={24} /></span>
                    <span>套餐与权益</span>
                    <ChevronRight size={21} aria-hidden="true" />
                  </button>
                )}
              </div>
            </section>

            <section className="oc-profile-mobile-home-section" aria-labelledby="mobile-settings-app-title">
              <h4 id="mobile-settings-app-title">应用</h4>
              <div className="oc-profile-mobile-home-card">
                <button type="button" onClick={() => setMobilePane('notifications')}>
                  <span className="oc-profile-mobile-home-icon" aria-hidden="true"><Bell size={24} /></span>
                  <span>消息通知</span>
                  <ChevronRight size={21} aria-hidden="true" />
                </button>
                {onThemeChange && (
                  <button type="button" onClick={() => setMobilePane('appearance')}>
                    <span className="oc-profile-mobile-home-icon" aria-hidden="true"><Sun size={24} /></span>
                    <span>外观</span>
                    <span className="oc-profile-mobile-home-value">{currentThemeLabel}</span>
                    <ChevronRight size={21} aria-hidden="true" />
                  </button>
                )}
                <button type="button" onClick={() => setMobilePane('behavior')}>
                  <span className="oc-profile-mobile-home-icon" aria-hidden="true"><BrainCircuit size={24} /></span>
                  <span>AI 行为</span>
                  <ChevronRight size={21} aria-hidden="true" />
                </button>
              </div>
            </section>

            {(onOpenDesktopConnect || onOpenDownload || onOpenFeedback) && (
              <section className="oc-profile-mobile-home-section" aria-labelledby="mobile-settings-support-title">
                <h4 id="mobile-settings-support-title">连接与支持</h4>
                <div className="oc-profile-mobile-home-card">
                  {onOpenDesktopConnect && (
                    <button type="button" onClick={() => openMobileDestination(onOpenDesktopConnect)}>
                      <span className="oc-profile-mobile-home-icon" aria-hidden="true"><Laptop size={24} /></span>
                      <span>连接我的电脑助手</span>
                      <ChevronRight size={21} aria-hidden="true" />
                    </button>
                  )}
                  {onOpenDownload && (
                    <button type="button" onClick={() => openMobileDestination(onOpenDownload)}>
                      <span className="oc-profile-mobile-home-icon" aria-hidden="true"><Download size={24} /></span>
                      <span>下载 CatsCo 桌面端</span>
                      <ChevronRight size={21} aria-hidden="true" />
                    </button>
                  )}
                  {onOpenFeedback && (
                    <button type="button" onClick={() => openMobileDestination(onOpenFeedback)}>
                      <span className="oc-profile-mobile-home-icon" aria-hidden="true"><Frown size={24} /></span>
                      <span>意见反馈</span>
                      <ChevronRight size={21} aria-hidden="true" />
                    </button>
                  )}
                </div>
              </section>
            )}

            {onLogout && (
              <button
                type="button"
                className="oc-profile-mobile-logout"
                onClick={() => openMobileDestination(onLogout)}
              >
                <LogOut size={24} aria-hidden="true" />
                <span>退出登录</span>
              </button>
            )}
          </div>

          <div className="oc-profile-mobile-pane oc-profile-mobile-pane-profile">
            <div className="oc-settings-avatar-block">
              <div className="oc-profile-avatar-wrap">
                <Avatar name={displayName || user?.username} src={avatarUrl} size={96} identityFallback className="oc-profile-hero-avatar" />
                <button
                  type="button"
                  className="oc-profile-avatar-edit"
                  onClick={() => fileInputRef.current?.click()}
                  aria-label="选择头像"
                >
                  <Pencil size={18} aria-hidden="true" />
                </button>
              </div>
              <strong className="oc-profile-mobile-name">{displayName || user?.username || 'CatsCo 用户'}</strong>
              <button
                className="oc-btn oc-btn-default oc-profile-avatar-desktop-action"
                onClick={() => fileInputRef.current?.click()}
              >
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
            <div className="oc-profile-details-section">
              <div className="oc-settings-section-title oc-profile-mobile-section-title">个人资料</div>
              <div className="oc-profile-mobile-group-card">
                <div className="oc-profile-name-field oc-profile-mobile-settings-row">
                  <span className="oc-profile-mobile-row-icon" aria-hidden="true"><UserRound size={22} /></span>
                  <div className="oc-profile-mobile-row-copy">
                    <label className="oc-settings-section-title" htmlFor="profile-display-name">姓名</label>
                    <input
                      id="profile-display-name"
                      className="oc-auth-input"
                      placeholder="显示昵称（可选）"
                      value={displayName}
                      onChange={(e) => setDisplayName(e.target.value)}
                    />
                  </div>
                </div>
                <div className="oc-profile-identity-card oc-profile-mobile-settings-row">
                  <span className="oc-profile-mobile-row-icon" aria-hidden="true"><Fingerprint size={22} /></span>
                  <div className="oc-profile-mobile-row-copy">
                    <div className="oc-profile-identity-label">我的 UID</div>
                    <div className="oc-profile-identity-value">{userUID || '-'}</div>
                    <div className="oc-settings-secondary">别人按 UID 搜索时，用这个数字加你或你的虚拟员工。</div>
                  </div>
                  <button type="button" className="oc-btn oc-btn-default" onClick={handleCopyUID} disabled={!userUID}>
                    复制
                  </button>
                </div>
              </div>
            </div>
            {copyStatus && <div className="oc-settings-secondary" style={{ marginTop: -6, marginBottom: 12 }}>{copyStatus}</div>}
          </div>
          {onThemeChange && (
            <div className="oc-settings-section oc-profile-theme-section oc-profile-mobile-pane oc-profile-mobile-pane-appearance">
              <div className="oc-settings-section-title">外观</div>
              <div className="oc-theme-picker" role="radiogroup" aria-label="界面主题">
                {THEME_OPTIONS.map(({ id, label, description, Icon }) => {
                  const locked = isLiquidThemeOption(id) && !liquidThemeAccess.unlocked;
                  const selected = theme === id;
                  return (
                    <button
                      key={id}
                      type="button"
                      className={`oc-theme-option${selected ? ' is-selected' : ''}${locked ? ' is-locked' : ''}`}
                      role="radio"
                      aria-checked={selected}
                      aria-label={`${label}主题${locked ? '，需要密码' : ''}`}
                      disabled={isLiquidThemeOption(id) && liquidThemeAccess.loading}
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
          <div className="oc-profile-mobile-pane oc-profile-mobile-pane-notifications">
            <NotificationSettings user={user} />
          </div>
          <div className="oc-profile-account-section oc-profile-mobile-pane oc-profile-mobile-pane-account">
            <div className="oc-settings-section-title oc-profile-mobile-section-title">账号</div>
            <div className="oc-profile-mobile-group-card">
              <div className="oc-settings-section oc-profile-account-item">
                <div className="oc-settings-section-title">账号安全</div>
                {showPasswordReset ? (
                  <PasswordResetForm defaultEmail={resetEmail} />
                ) : (
                  <button type="button" className="oc-settings-list-item oc-settings-list-button" onClick={() => setShowPasswordReset(true)}>
                    <span className="oc-profile-mobile-row-icon" aria-hidden="true"><ShieldCheck size={22} /></span>
                    <div className="oc-settings-list-text">
                      <div>重置登录密码</div>
                      <div className="oc-settings-secondary">通过注册邮箱验证码设置新密码。</div>
                    </div>
                    <ChevronRight className="oc-profile-mobile-row-chevron" size={20} aria-hidden="true" />
                  </button>
                )}
              </div>
              {onOpenRelay && (
                <div className="oc-settings-section oc-profile-account-item">
                  <div className="oc-settings-section-title">开发者工具</div>
                  <button
                    type="button"
                    className="oc-settings-list-item oc-settings-list-button"
                    onClick={() => {
                      onClose();
                      onOpenRelay();
                    }}
                  >
                    <span className="oc-profile-mobile-row-icon" aria-hidden="true"><CreditCard size={22} /></span>
                    <div className="oc-settings-list-text">
                      <div>套餐与权益</div>
                      <div className="oc-settings-secondary">查看当前权益、用量、套餐和订单记录。</div>
                    </div>
                    <ChevronRight className="oc-profile-mobile-row-chevron" size={20} aria-hidden="true" />
                  </button>
                </div>
              )}
            </div>
          </div>
          <div className="oc-profile-behavior-section oc-profile-mobile-pane oc-profile-mobile-pane-behavior">
            <div className="oc-settings-section-title oc-profile-mobile-section-title">AI 行为</div>
            <div className="oc-profile-mobile-group-card">
              <div className="oc-profile-thinking-toggle oc-profile-mobile-settings-row">
                <span className="oc-profile-mobile-row-icon" aria-hidden="true"><BrainCircuit size={22} /></span>
                <span className="oc-profile-thinking-label">显示 AI 思考过程</span>
                <button
                  type="button"
                  className="oc-settings-switch"
                  role="switch"
                  aria-checked={showThinking}
                  aria-label="显示 AI 思考过程"
                  onClick={() => setShowThinking((value) => !value)}
                >
                  <span aria-hidden="true" />
                </button>
              </div>
            </div>
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
