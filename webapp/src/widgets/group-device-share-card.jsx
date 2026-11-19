import React, { useEffect, useRef, useState } from 'react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import '../css/group-device-sharing.css';

const labels = { pending: '等待设备所有者处理', approved: '已授权', rejected: '已拒绝', invalidated: '申请已失效' };

export default function GroupDeviceShareCard({ reference, sourceTopicId, sourceMessageId }) {
  const [snapshot, setSnapshot] = useState(null);
  const [deviceId, setDeviceId] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [authVersion, setAuthVersion] = useState(0);
  const sequence = useRef(0);

  useEffect(() => {
    let stopped = false;
    const token = getToken(), revision = getAuthRevision();
    const current = () => !stopped && isCurrentAuthSession(token, revision);
    const refresh = async () => {
      const seq = ++sequence.current;
      try {
        const next = await api.getGroupDeviceShareRequest(reference.topic_id, reference.request_id);
        if (current() && seq === sequence.current) {
          setSnapshot(next);
          setError('');
        }
      } catch {
        if (current() && seq === sequence.current) setError('申请状态暂时不可用。');
      }
    };
    if (reference.topic_id === sourceTopicId && sourceMessageId > 0) void refresh();
    const timer = setInterval(() => { if (reference.topic_id === sourceTopicId && sourceMessageId > 0) void refresh(); }, 3000);
    const reset = () => { ++sequence.current; setSnapshot(null); setDeviceId(''); setBusy(false); setAuthVersion(version => version + 1); };
    window.addEventListener('cc:auth-changed', reset);
    return () => { stopped = true; clearInterval(timer); window.removeEventListener('cc:auth-changed', reset); };
  }, [reference.topic_id, reference.request_id, authVersion, sourceTopicId, sourceMessageId]);

  const originalCard = snapshot?.request.topic_id === sourceTopicId && snapshot?.request.message_id === sourceMessageId;
  const resolve = async (action) => {
    if (!originalCard) return;
    const token = getToken(), revision = getAuthRevision();
    ++sequence.current;
    setBusy(true);
    try {
      await api.resolveGroupDeviceShareRequest({
        action, topic_id: reference.topic_id, request_id: reference.request_id,
        expected_revision: snapshot.request.revision, ...(action === 'approve' ? { device_id: deviceId } : {}),
      });
      if (!isCurrentAuthSession(token, revision)) return;
      const next = await api.getGroupDeviceShareRequest(reference.topic_id, reference.request_id);
      if (!isCurrentAuthSession(token, revision)) return;
      setSnapshot(next);
      setError('');
      window.dispatchEvent(new CustomEvent('cc:devices-changed'));
    } catch {
      if (isCurrentAuthSession(token, revision)) setError('申请已变化或操作未确认，请刷新核对后重试。');
    } finally {
      if (isCurrentAuthSession(token, revision)) setBusy(false);
    }
  };

  if (reference.topic_id !== sourceTopicId || !sourceMessageId) {
    return <p role="alert">请在申请所属群聊中打开原始授权卡片。</p>;
  }
  return <section className="cc-share-card" aria-label="设备使用授权申请">
    <strong>设备使用授权申请</strong>
    {error && <p role="alert">{error}</p>}
    {snapshot && <>
      <p>{snapshot.request.description}</p>
      <p role="status">{snapshot.share_status === 'revoked' ? '授权已撤销' : labels[snapshot.request.status] || '申请状态未知'}</p>
      {!originalCard && <p>请使用原始授权卡片处理申请。</p>}
      {originalCard && snapshot.can_resolve && snapshot.request.status === 'pending' && <>
        <label>选择要授权的电脑
          <select aria-label="选择要授权的电脑" value={deviceId} disabled={busy} onChange={event => setDeviceId(event.target.value)}>
            <option value="">请选择自己的电脑</option>
            {(snapshot.devices || []).map(device => <option key={device.deviceId} value={device.deviceId}>
              {device.displayName || device.deviceId} · {device.deviceId}
            </option>)}
          </select>
        </label>
        <button type="button" disabled={busy || !deviceId} onClick={() => void resolve('approve')}>授权所选设备</button>
        <button type="button" disabled={busy} onClick={() => void resolve('reject')}>拒绝</button>
        <p>许可适用于本群中的 Bot，持续至撤销。执行结果可能发送到本群。</p>
      </>}
    </>}
  </section>;
}
