import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { QrCode, RefreshCw, X } from 'lucide-react';
import { api } from '../api';

export default function WeixinChannelModal({ agent, onClose, onBound }) {
  const agentUid = agent?.uid || agent?.id;
  const agentName = agent?.display_name || agent?.username || `Agent ${agentUid}`;
  const [loading, setLoading] = useState(true);
  const [requesting, setRequesting] = useState(false);
  const [polling, setPolling] = useState(false);
  const [error, setError] = useState('');
  const [channel, setChannel] = useState(null);
  const [qr, setQr] = useState(null);
  const [message, setMessage] = useState('');
  const onBoundRef = useRef(onBound);

  useEffect(() => {
    onBoundRef.current = onBound;
  }, [onBound]);

  const qrLink = useMemo(() => {
    if (!qr) return '';
    return qr.qrcode_img_content || qr.qrcode_url || qr.url || '';
  }, [qr]);

  const loadStatus = useCallback(async () => {
    if (!agentUid) return;
    setLoading(true);
    setError('');
    try {
      const res = await api.getAgentChannels(agentUid);
      setChannel(res.channels?.weixin || null);
    } catch (err) {
      setError(err.message || '无法读取微信通道状态');
    } finally {
      setLoading(false);
    }
  }, [agentUid]);

  const requestQRCode = useCallback(async () => {
    if (!agentUid) return;
    setRequesting(true);
    setPolling(false);
    setError('');
    setMessage('');
    setQr(null);
    try {
      const res = await api.getWeixinChannelQRCode(agentUid);
      setQr(res);
      if (!res.qrcode) {
        setError('微信没有返回二维码，请稍后重试。');
      } else {
        setMessage('请用微信扫描二维码授权。授权成功后，这个微信通道会属于当前 agent。');
        setPolling(true);
      }
    } catch (err) {
      setError(err.message || '获取微信二维码失败');
    } finally {
      setRequesting(false);
    }
  }, [agentUid]);

  useEffect(() => {
    loadStatus();
    requestQRCode();
  }, [loadStatus, requestQRCode]);

  useEffect(() => {
    if (!polling || !qr?.qrcode || !agentUid) return undefined;
    let cancelled = false;
    const timer = window.setInterval(async () => {
      try {
        const res = await api.getWeixinChannelQRCodeStatus(agentUid, qr.qrcode);
        if (cancelled) return;
        if (res.status === 'confirmed' && res.token_saved) {
          window.clearInterval(timer);
          setPolling(false);
          setQr(null);
          setChannel(res.binding || null);
          setMessage('微信通道已绑定到当前 agent。agent body 同步 token 后即可作为运行时通道使用。');
          if (onBoundRef.current) onBoundRef.current(res.binding);
        } else if (res.status) {
          setMessage(res.status === 'scanned' ? '已扫码，等待确认授权。' : '等待微信扫码确认。');
        }
      } catch (err) {
        if (!cancelled) {
          setPolling(false);
          setError(err.message || '检查微信授权状态失败');
        }
      }
    }, 2000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [polling, qr, agentUid]);

  return (
    <div className="oc-modal-overlay" onClick={onClose} style={{ zIndex: 1200 }}>
      <div className="oc-modal" onClick={(event) => event.stopPropagation()} style={{ width: 520, maxWidth: '94vw' }}>
        <div className="oc-modal-header" style={{ padding: '18px 22px', borderBottom: '1px solid var(--v3-border)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--v3-text-name)', fontWeight: 800, fontSize: 18 }}>
              <QrCode size={18} /> 微信通道绑定
            </div>
            <div style={{ marginTop: 6, color: 'var(--v3-text-muted)', fontSize: 13 }}>
              当前 agent：{agentName}
            </div>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭" style={{ background: 'transparent', border: 0, color: 'var(--v3-text-muted)', cursor: 'pointer' }}>
            <X size={18} />
          </button>
        </div>

        <div className="oc-modal-body" style={{ padding: 22 }}>
          <div style={{ border: '1px solid var(--v3-border)', borderRadius: 8, padding: 14, marginBottom: 16, background: 'rgba(255,255,255,0.03)' }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12, color: 'var(--v3-text-main)', fontWeight: 700 }}>
              <span>平台记录</span>
              <span style={{ color: channel?.configured ? '#22c55e' : 'var(--v3-text-muted)' }}>
                {loading ? '读取中' : channel?.configured ? '已绑定' : '未绑定'}
              </span>
            </div>
            {channel?.configured && (
              <div style={{ marginTop: 8, color: 'var(--v3-text-muted)', fontSize: 13 }}>
                Token 尾号 {channel.token_last4 || '----'}。浏览器不会显示完整 token。
              </div>
            )}
          </div>

          {error && (
            <div style={{ border: '1px solid rgba(255, 99, 99, 0.35)', borderRadius: 8, padding: 12, color: '#ff8a8a', marginBottom: 16 }}>
              {error}
            </div>
          )}

          {message && (
            <div style={{ color: 'var(--v3-text-muted)', fontSize: 14, lineHeight: 1.6, marginBottom: 16 }}>
              {message}
            </div>
          )}

          <div style={{ display: 'grid', placeItems: 'center', minHeight: 180, border: '1px dashed var(--v3-border)', borderRadius: 8, padding: 18, background: 'rgba(255,255,255,0.025)' }}>
            {requesting ? (
              <div style={{ color: 'var(--v3-text-muted)', fontSize: 14 }}>正在获取二维码...</div>
            ) : qr?.qrcode ? (
              <div style={{ textAlign: 'center' }}>
                {qrLink ? (
                  <img src={qrLink} alt="微信授权二维码" style={{ maxWidth: 220, width: '100%', borderRadius: 8, background: '#fff', padding: 8 }} />
                ) : (
                  <div style={{ color: 'var(--v3-text-muted)', fontSize: 13 }}>微信已返回二维码，请点击下方按钮打开。</div>
                )}
                {qrLink && (
                  <div style={{ marginTop: 12 }}>
                    <a href={qrLink} target="_blank" rel="noreferrer" className="oc-btn oc-btn-default" style={{ display: 'inline-flex', textDecoration: 'none', padding: '8px 14px', borderRadius: 8 }}>
                      打开二维码
                    </a>
                  </div>
                )}
              </div>
            ) : (
              <button type="button" className="oc-btn oc-btn-primary" onClick={requestQRCode} style={{ borderRadius: 8, padding: '10px 16px' }}>
                获取二维码
              </button>
            )}
          </div>

          <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', marginTop: 18 }}>
            <button type="button" className="oc-btn oc-btn-default" onClick={requestQRCode} disabled={requesting} style={{ borderRadius: 8, display: 'inline-flex', alignItems: 'center', gap: 6 }}>
              <RefreshCw size={14} /> 重新获取
            </button>
            <button type="button" className="oc-btn oc-btn-primary" onClick={onClose} style={{ borderRadius: 8 }}>
              完成
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
