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
    })
      .then(() => {
        setStatus('linked');
      })
      .catch((err) => {
        setStatus('error');
        setError(err.message || '设备授权绑定失败，请重新打开链接。');
      });
  };

  const title = status === 'linked'
    ? '设备授权已绑定'
    : status === 'error'
      ? '授权链接不可用'
      : status === 'linking'
        ? '正在绑定设备授权'
        : '确认绑定设备授权';
  const message = status === 'linked'
    ? `已把当前微信/飞书身份关联到 CatsCo 账号 ${user?.display_name || user?.displayName || user?.username || ''}。以后该虚拟员工需要访问你的本地设备时，会使用你已授权且在线的设备。`
    : status === 'error'
      ? error
      : status === 'linking'
        ? '请稍候，正在确认当前账号与渠道身份。'
        : `将把当前微信/飞书身份关联到 CatsCo 账号 ${user?.display_name || user?.displayName || user?.username || ''}。确认后，虚拟员工需要访问你的本地设备时，会使用你已授权且在线的设备。`;

  return (
    <div className="v3-app" style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}>
      <div style={{ width: '100%', maxWidth: 420, background: 'var(--v3-bg-sidebar)', border: '1px solid var(--v3-border)', borderRadius: 12, padding: 28, boxShadow: '0 24px 80px rgba(0,0,0,0.25)', textAlign: 'center' }}>
        <div style={{ width: 56, height: 56, borderRadius: '50%', background: status === 'error' ? 'rgba(250,81,81,0.1)' : 'rgba(16,185,129,0.14)', color: status === 'error' ? '#FA5151' : 'var(--v3-primary)', display: 'flex', alignItems: 'center', justifyContent: 'center', margin: '0 auto 18px', fontSize: 28 }}>
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
