import React, { useEffect, useState } from 'react';
import { api } from '../api';
import '../css/openchat-theme.css';

export default function ChannelDeviceLinkView({ bindingId, linkToken, user }) {
  const [status, setStatus] = useState('ready');
  const [error, setError] = useState('');

  useEffect(() => {
    if (!bindingId || !linkToken) {
      setStatus('error');
      setError('授权链接缺少必要信息，请重新从微信/飞书打开。');
    }
  }, [bindingId, linkToken]);

  const confirmLink = () => {
    if (!bindingId || !linkToken || status === 'linking') return;
    setStatus('linking');
    setError('');
    api.linkChannelAgentBindingUser({
      binding_id: Number(bindingId),
      link_token: linkToken,
      device_access: true,
    })
      .then(() => {
        setStatus('linked');
      })
      .catch((err) => {
        setStatus('error');
        setError(err.message || '渠道身份绑定失败，请重新打开链接。');
      });
  };

  const accountName = user?.display_name || user?.displayName || user?.username || '';

  const title = status === 'linked'
    ? 'CatsCo 账号已绑定'
    : status === 'error'
      ? '授权链接不可用'
      : status === 'linking'
        ? '正在绑定 CatsCo 账号'
        : '确认绑定 CatsCo 账号';
  const message = status === 'linked'
    ? `已把当前微信/飞书身份关联到 CatsCo 账号 ${accountName}。之后可以直接使用该账号已连接的设备。`
    : status === 'error'
      ? error
      : status === 'linking'
        ? '请稍候，正在确认当前账号与渠道身份。'
        : `将把当前微信/飞书身份关联到 CatsCo 账号 ${accountName}，用于继续聊天并使用该账号已连接的设备。`;

  return (
    <div className="v3-app" style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}>
      <div style={{ width: '100%', maxWidth: 420, background: 'var(--v3-bg-sidebar)', border: '1px solid var(--v3-border)', borderRadius: 12, padding: 28, boxShadow: '0 24px 80px rgba(0,0,0,0.25)', textAlign: 'center' }}>
        <div style={{ width: 56, height: 56, borderRadius: '50%', background: status === 'error' ? 'rgba(250,81,81,0.1)' : 'color-mix(in srgb, var(--v3-primary) 14%, transparent)', color: status === 'error' ? '#FA5151' : 'var(--v3-primary)', display: 'flex', alignItems: 'center', justifyContent: 'center', margin: '0 auto 18px', fontSize: 28 }}>
          {status === 'linked' ? '✓' : status === 'error' ? '!' : '...'}
        </div>
        <div style={{ color: 'var(--v3-text-name)', fontSize: 18, fontWeight: 700, marginBottom: 8 }}>{title}</div>
        <div style={{ color: 'var(--v3-text-muted)', fontSize: 14, lineHeight: 1.6 }}>{message}</div>
        {status === 'ready' && (
          <button
            type="button"
            onClick={confirmLink}
            style={{ marginTop: 20, width: '100%', height: 42, borderRadius: 8, border: 0, background: 'var(--v3-primary)', color: '#fff', fontWeight: 700, cursor: 'pointer' }}
          >
            确认绑定
          </button>
        )}
      </div>
    </div>
  );
}
