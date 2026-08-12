import React, { useEffect, useState } from 'react';
import { api } from '../api';
import { isValidEmailFormat } from '../utils/email-format';

function formatResetError(message) {
  const text = String(message || '');
  if (text.includes('email required')) return '请输入邮箱地址';
  if (text.includes('email and code required')) return '请输入邮箱和验证码';
  if (text.includes('invalid or expired verification code')) return '验证码无效或已过期';
  if (text.includes('password min 6')) return '密码至少 6 位';
  if (text.includes('failed to send verification code')) return '发送验证码失败，请稍后再试';
  return message || '操作失败，请稍后再试';
}

export default function PasswordResetForm({ defaultEmail = '', onDone }) {
  const [email, setEmail] = useState(defaultEmail);
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [countdown, setCountdown] = useState(0);
  const [codeSent, setCodeSent] = useState(false);
  const [status, setStatus] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    setEmail(defaultEmail);
  }, [defaultEmail]);

  useEffect(() => {
    if (countdown <= 0) return undefined;
    const timer = setTimeout(() => setCountdown(countdown - 1), 1000);
    return () => clearTimeout(timer);
  }, [countdown]);

  const handleSendCode = async () => {
    const trimmedEmail = email.trim();
    if (!trimmedEmail || !isValidEmailFormat(trimmedEmail)) {
      setError('请输入有效的邮箱地址（请检查域名拼写，如 qq.com）');
      return;
    }

    setError('');
    setStatus('');
    try {
      await api.sendPasswordResetCode(trimmedEmail);
      setCodeSent(true);
      setCountdown(60);
      setStatus('如果该邮箱已注册，验证码会发送到对应邮箱。');
    } catch (err) {
      setError(formatResetError(err.message));
    }
  };

  const handleSubmit = async (event) => {
    event.preventDefault();
    const trimmedEmail = email.trim();
    setError('');
    setStatus('');

    if (!trimmedEmail) {
      setError('请输入邮箱地址');
      return;
    }
    if (!code.trim()) {
      setError('请输入邮箱验证码');
      return;
    }
    if (password.length < 6) {
      setError('密码至少 6 位');
      return;
    }
    if (password !== confirmPassword) {
      setError('两次输入的密码不一致');
      return;
    }

    setSubmitting(true);
    try {
      await api.resetPassword({
        email: trimmedEmail,
        code: code.trim(),
        password,
      });
      setStatus('密码已重置，请使用新密码登录。');
      setCode('');
      setPassword('');
      setConfirmPassword('');
      if (onDone) onDone(trimmedEmail);
    } catch (err) {
      setError(formatResetError(err.message));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <form onSubmit={handleSubmit}>
      <input
        className="oc-auth-input"
        type="email"
        placeholder="邮箱地址"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
      />
      <div className="oc-password-reset-code-row">
        <input
          className="oc-auth-input"
          placeholder="邮箱验证码"
          value={code}
          onChange={(e) => setCode(e.target.value)}
        />
        <button
          type="button"
          className="oc-auth-btn"
          onClick={handleSendCode}
          disabled={countdown > 0}
        >
          {countdown > 0 ? `${countdown}秒` : (codeSent ? '重新发送' : '发送验证码')}
        </button>
      </div>
      <input
        className="oc-auth-input"
        type="password"
        placeholder="新密码（至少6位）"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
      />
      <input
        className="oc-auth-input"
        type="password"
        placeholder="确认新密码"
        value={confirmPassword}
        onChange={(e) => setConfirmPassword(e.target.value)}
      />
      {error && <div className="oc-form-error">{error}</div>}
      {status && <div className="oc-settings-secondary" style={{ marginTop: -4, marginBottom: 12 }}>{status}</div>}
      <button className="oc-auth-btn" type="submit" disabled={submitting}>
        {submitting ? '处理中...' : '重置密码'}
      </button>
    </form>
  );
}
