import React, { useEffect, useMemo, useState } from 'react';
import { api } from '../api';
import Avatar from '../widgets/avatar';
import '../css/openchat-theme.css';

export default function AgentEntryBindView({ sceneKey }) {
  const [preview, setPreview] = useState(null);
  const [loading, setLoading] = useState(true);
  const [binding, setBinding] = useState(false);
  const [status, setStatus] = useState('');
  const [error, setError] = useState('');
  const params = useMemo(() => new URLSearchParams(window.location.search), []);
  const [channelUserId, setChannelUserId] = useState(
    params.get('channel_user_id')
      || params.get('openid')
      || params.get('open_id')
      || params.get('user_id')
      || '',
  );

  useEffect(() => {
    let cancelled = false;
    api.getChannelAgentEntryPreview(sceneKey)
      .then((res) => {
        if (!cancelled) setPreview(res);
      })
      .catch((err) => {
        if (!cancelled) setError(err.message || '入口不存在或已失效');
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [sceneKey]);

  const handleConfirm = async () => {
    if (preview?.entry?.channel === 'feishu' && !channelUserId.trim()) {
      window.location.href = `/api/channel-agent-bindings/oauth/feishu/start?scene_key=${encodeURIComponent(sceneKey)}`;
      return;
    }
    const userId = channelUserId.trim();
    if (!userId) {
      setError('当前渠道身份缺失，请从微信/飞书入口打开，或填写测试身份。');
      return;
    }
    try {
      setError('');
      setBinding(true);
      await api.confirmChannelAgentBinding({
        scene_key: sceneKey,
        channel: preview?.entry?.channel,
        channel_app_id: preview?.entry?.channel_app_id || params.get('channel_app_id') || '',
        channel_user_id: userId,
        channel_conversation_id: params.get('channel_conversation_id') || '',
        channel_conversation_type: params.get('channel_conversation_type') || 'p2p',
      });
      setStatus('已进入该虚拟员工，请回到微信/飞书聊天框继续提问。');
    } catch (err) {
      setError(err.message || '绑定失败');
    } finally {
      setBinding(false);
    }
  };

  const agent = preview?.agent || {};
  const channelName = preview?.entry?.channel === 'feishu' ? '飞书' : '微信';

  return (
    <div className="v3-app" style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 24 }}>
      <div style={{ width: '100%', maxWidth: 420, background: 'var(--v3-bg-sidebar)', border: '1px solid var(--v3-border)', borderRadius: 12, padding: 28, boxShadow: '0 24px 80px rgba(0,0,0,0.25)' }}>
        {loading ? (
          <div style={{ color: 'var(--v3-text-muted)', textAlign: 'center' }}>正在打开入口...</div>
        ) : error && !preview ? (
          <div style={{ textAlign: 'center' }}>
            <div style={{ color: 'var(--v3-text-name)', fontSize: 18, fontWeight: 700, marginBottom: 8 }}>入口不可用</div>
            <div style={{ color: 'var(--v3-text-muted)', fontSize: 14 }}>{error}</div>
          </div>
        ) : status ? (
          <div style={{ textAlign: 'center' }}>
            <div style={{ width: 56, height: 56, borderRadius: '50%', background: 'rgba(16,185,129,0.14)', color: 'var(--v3-primary)', display: 'flex', alignItems: 'center', justifyContent: 'center', margin: '0 auto 18px', fontSize: 28 }}>✓</div>
            <div style={{ color: 'var(--v3-text-name)', fontSize: 18, fontWeight: 700, marginBottom: 8 }}>绑定完成</div>
            <div style={{ color: 'var(--v3-text-muted)', fontSize: 14, lineHeight: 1.6 }}>{status}</div>
          </div>
        ) : (
          <>
            <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 22 }}>
              <Avatar name={agent.display_name || agent.username} src={agent.avatar_url} size={56} isBot />
              <div style={{ minWidth: 0 }}>
                <div style={{ color: 'var(--v3-text-muted)', fontSize: 12, marginBottom: 4 }}>{channelName}虚拟员工</div>
                <div style={{ color: 'var(--v3-text-name)', fontSize: 20, fontWeight: 700, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {agent.display_name || agent.username}
                </div>
              </div>
            </div>

            {!channelUserId && preview?.entry?.channel !== 'feishu' && (
              <div style={{ marginBottom: 16 }}>
                <label style={{ display: 'block', color: 'var(--v3-text-muted)', fontSize: 12, marginBottom: 8 }}>渠道用户 ID</label>
                <input
                  value={channelUserId}
                  onChange={(event) => setChannelUserId(event.target.value)}
                  className="oc-auth-input"
                  placeholder="测试时填写 openid / open_id"
                  style={{ width: '100%', padding: '12px 14px', fontSize: 14 }}
                />
              </div>
            )}

            {error && (
              <div style={{ background: 'rgba(250,81,81,0.1)', color: '#FA5151', padding: 12, borderRadius: 8, marginBottom: 16, fontSize: 13 }}>
                {error}
              </div>
            )}

            <button
              className="oc-btn oc-btn-primary"
              style={{ width: '100%', padding: '13px 0', borderRadius: 8, fontSize: 15 }}
              onClick={handleConfirm}
              disabled={binding}
            >
              {binding ? '正在确认...' : preview?.entry?.channel === 'feishu' && !channelUserId.trim() ? '用飞书授权进入' : `进入${agent.display_name || '虚拟员工'}`}
            </button>
          </>
        )}
      </div>
    </div>
  );
}
