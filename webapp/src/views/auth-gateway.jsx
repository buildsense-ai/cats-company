import React, { useEffect, useState } from 'react';
import { Eye, EyeOff } from 'lucide-react';
import { api, getToken, setToken } from '../api';
import { InlineFeedback } from '../components/feedback-system';
import AuthFlowBackground from '../components/auth-flow-background';
import t from '../i18n';
import { isValidEmailFormat } from '../utils/email-format';
import { formatSharedAuthError } from '../utils/auth-error';
import { writeStoredUserProfile } from '../utils/user-profile';
import {
  authModeForPathname,
  authPathForMode,
  authenticationRedirectPath,
  navigateBrowserPath,
  postAuthenticationPathFromSearch,
} from '../utils/auth-routes';
import PasswordResetForm from '../widgets/password-reset-form';

const passwordToggleStyle = {
  position: 'absolute',
  right: 4,
  top: '40%',
  transform: 'translateY(-50%)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  width: 32,
  height: 32,
  margin: 0,
  padding: 0,
  border: 0,
  background: 'transparent',
  color: '#888',
  cursor: 'pointer',
  userSelect: 'none',
};

function formatAuthError(message) {
  const text = String(message || '').toLowerCase();
  if (text.includes('user not found')) return '账号不存在，请检查用户名或邮箱';
  if (text.includes('password mismatch')) return '密码错误，请重试';
  if (text.includes('username taken')) return '登录名称已被占用，请换一个';
  if (text.includes('email already')) return '该邮箱已经注册，请直接登录';
  if (text.includes('username min 3')) return '登录名称至少 3 个字符';
  if (text.includes('invalid email format')) return '邮箱格式无效，请检查域名拼写（如 qq.com）';
  const sharedMessage = formatSharedAuthError(message);
  if (sharedMessage) return sharedMessage;
  return message || '操作失败，请稍后再试';
}

function PasswordField({ autoComplete, placeholder, value, onChange }) {
  const [showPassword, setShowPassword] = useState(false);
  const toggleLabel = showPassword ? '隐藏密码' : '显示密码';

  return (
    <div style={{ position: 'relative' }}>
      <input
        className="oc-auth-input"
        type={showPassword ? 'text' : 'password'}
        placeholder={placeholder}
        aria-label={placeholder}
        autoComplete={autoComplete}
        value={value}
        onChange={onChange}
        style={{ paddingRight: 48 }}
      />
      <button
        type="button"
        aria-label={toggleLabel}
        aria-pressed={showPassword}
        title={toggleLabel}
        onClick={() => setShowPassword((current) => !current)}
        style={passwordToggleStyle}
      >
        {showPassword ? <EyeOff size={16} /> : <Eye size={16} />}
      </button>
    </div>
  );
}

export function AuthView({
  mode,
  nextPath = '/',
  onAuthenticationIntent,
  onNavigate,
  onLogin,
  onRegister,
}) {
  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [loginName, setLoginName] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [codeSent, setCodeSent] = useState(false);
  const [countdown, setCountdown] = useState(0);
  const [sentHint, setSentHint] = useState('');

  useEffect(() => {
    if (countdown > 0) {
      const timer = setTimeout(() => setCountdown(countdown - 1), 1000);
      return () => clearTimeout(timer);
    }
  }, [countdown]);

  const handleSendCode = async () => {
    if (!email || !isValidEmailFormat(email)) {
      setError('请输入有效的邮箱地址（请检查域名拼写，如 qq.com）');
      return;
    }
    try {
      await api.sendVerificationCode(email);
      setCodeSent(true);
      setCountdown(60);
      setError('');
      setSentHint('验证码已发送，请使用最新邮件中的验证码（旧验证码将失效）');
    } catch (err) {
      setSentHint('');
      setError(err.message || '发送验证码失败，请稍后再试');
    }
  };

  const handleSubmit = async (event) => {
    event.preventDefault();
    setError('');
    try {
      if (mode === 'login') {
        await onLogin(username, password);
      } else {
        await onRegister(email, password, loginName, code);
      }
    } catch (err) {
      setError(formatAuthError(err.message));
    }
  };

  const authShell = (content) => (
    <main className="oc-auth" onFocusCapture={onAuthenticationIntent}>
      <AuthFlowBackground />
      {content}
    </main>
  );

  const authPath = (nextMode) => authPathForMode(nextMode, nextPath);
  const handleAuthLink = (event, nextMode) => {
    if (event.defaultPrevented || event.button !== 0
      || event.metaKey || event.altKey || event.ctrlKey || event.shiftKey
      || typeof onNavigate !== 'function') return;
    event.preventDefault();
    onNavigate(nextMode);
  };

  if (mode === 'reset') {
    return authShell(
      <div className="oc-auth-card">
        <div className="oc-auth-logo">CatsCo</div>
        <div className="oc-settings-secondary" style={{ marginBottom: 14 }}>
          输入注册邮箱，验证后设置新密码。
        </div>
        <PasswordResetForm />
        <div className="oc-auth-link">
          <span>
            想起密码了？
            <a href={authPath('login')} onClick={(event) => handleAuthLink(event, 'login')}>返回登录</a>
          </span>
        </div>
      </div>
    );
  }

  return authShell(
    <form className="oc-auth-card" onSubmit={handleSubmit}>
      <div className="oc-auth-logo">CatsCo</div>
      {error && <InlineFeedback tone="error" className="oc-auth-feedback">{error}</InlineFeedback>}

      {mode === 'login' ? (
        <>
          <input
            className="oc-auth-input"
            type="text"
            placeholder={t('username')}
            aria-label={t('username')}
            autoComplete="username"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
          <PasswordField
            autoComplete="current-password"
            placeholder={t('password')}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </>
      ) : (
        <>
          <input
            className="oc-auth-input"
            type="email"
            placeholder="邮箱地址"
            aria-label="邮箱地址"
            autoComplete="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
          <div className="oc-auth-code-row">
            <input
              className="oc-auth-input"
              placeholder="邮箱验证码"
              aria-label="邮箱验证码"
              autoComplete="one-time-code"
              value={code}
              onChange={(event) => setCode(event.target.value)}
            />
            <button
              type="button"
              className="oc-auth-btn"
              onClick={handleSendCode}
              disabled={countdown > 0}
            >
              {countdown > 0 ? `${countdown}秒` : '发送验证码'}
            </button>
          </div>
          {sentHint && (
            <div className="oc-auth-hint" style={{ color: '#2e8b57', fontSize: 12, marginTop: 6 }}>{sentHint}</div>
          )}
          <input
            className="oc-auth-input"
            placeholder="登录名称（可用于登录）"
            aria-label="登录名称（可用于登录）"
            autoComplete="username"
            value={loginName}
            onChange={(event) => setLoginName(event.target.value)}
          />
          <PasswordField
            autoComplete="new-password"
            placeholder="设置密码（至少6位）"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </>
      )}

      <button className="oc-auth-btn" type="submit">
        {mode === 'login' ? t('login') : t('register')}
      </button>
      <div className="oc-auth-link">
        {mode === 'login' ? (
          <>
            <span>
              还没有账号？
              <a href={authPath('register')} onClick={(event) => handleAuthLink(event, 'register')}>立即注册</a>
            </span>
            <span style={{ marginLeft: 12 }}>
              <a href={authPath('reset')} onClick={(event) => handleAuthLink(event, 'reset')}>忘记密码？</a>
            </span>
          </>
        ) : (
          <span>
            已有账号？
            <a href={authPath('login')} onClick={(event) => handleAuthLink(event, 'login')}>立即登录</a>
          </span>
        )}
      </div>
    </form>
  );
}

export default function AuthGateway({
  location = window.location,
  onAuthenticationIntent,
} = {}) {
  const { pathname = '/', search = '', hash = '' } = location;
  const mode = authModeForPathname(pathname);

  useEffect(() => {
    const redirectPath = authenticationRedirectPath({
      authenticated: Boolean(getToken()),
      location: { pathname, search, hash },
    });
    if (redirectPath) navigateBrowserPath(redirectPath, { replace: true });
  }, [hash, pathname, search]);

  const navigateToAuthMode = (nextMode) => {
    navigateBrowserPath(authPathForMode(nextMode, postAuthenticationPathFromSearch(search)));
  };

  const handleLogin = async (account, password) => {
    const response = await api.login({ account, password });
    if (!writeStoredUserProfile(response)) {
      throw new Error('登录响应缺少有效的用户资料');
    }
    setToken(response.token);
    navigateBrowserPath(postAuthenticationPathFromSearch(search), { replace: true });
  };

  const handleRegister = async (email, password, loginName, code) => {
    const username = loginName.trim();
    if (!username) throw new Error('请输入登录名称');
    if (username.length < 3) throw new Error('登录名称至少 3 个字符');
    await api.register({ email, username, password, code });
    await handleLogin(email, password);
  };

  return (
    <AuthView
      mode={mode}
      nextPath={postAuthenticationPathFromSearch(search)}
      onAuthenticationIntent={onAuthenticationIntent}
      onNavigate={navigateToAuthMode}
      onLogin={handleLogin}
      onRegister={handleRegister}
    />
  );
}
