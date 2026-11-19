import React, { useCallback, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Laptop, X } from 'lucide-react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import '../css/group-device-sharing.css';

const deviceKey = (device) => `${device.ownerUserId}/${device.deviceId}`;

export default function GroupDeviceSharing({ topicId, embedded = false, onOpen }) {
  const [open, setOpen] = useState(embedded);
  const [data, setData] = useState(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const sequence = useRef(0);
  const dialog = useRef(null);
  const trigger = useRef(null);

  const load = useCallback(async () => {
    const seq = ++sequence.current;
    const token = getToken(), revision = getAuthRevision();
    const current = () => seq === sequence.current && isCurrentAuthSession(token, revision);
    try {
      const [sharing, mine] = await Promise.all([api.getGroupDeviceShares(topicId), api.getDevices()]);
      const bots = sharing.members.filter(member => member.is_bot);
      const snapshots = await Promise.all(bots.map(bot => api.getConversationDevice(topicId, `usr${bot.user_id}`)));
      if (!current()) return;
      const ownDevices = (mine.devices || []).filter(device => device.runtimeRole !== 'server');
      setData({ ...sharing, bots, snapshots, ownDevices });
      setError('');
    } catch {
      if (current()) setError('暂时无法读取设备与共享状态，请刷新重试。');
    }
  }, [topicId]);

  useEffect(() => {
    void load();
    const refresh = () => void load();
    const reset = () => { ++sequence.current; setData(null); setOpen(false); setBusy(false); if (getToken()) void load(); };
    window.addEventListener('cc:devices-changed', refresh);
    window.addEventListener('cc:auth-changed', reset);
    window.addEventListener('focus', refresh);
    const timer = setInterval(() => { if (open) void load(); }, 3000);
    return () => {
      ++sequence.current;
      clearInterval(timer);
      window.removeEventListener('cc:devices-changed', refresh);
      window.removeEventListener('cc:auth-changed', reset);
      window.removeEventListener('focus', refresh);
    };
  }, [load, open]);

  useEffect(() => {
    if (!open || embedded) return undefined;
    dialog.current?.focus();
    const close = (event) => {
      if (event.key === 'Escape') setOpen(false);
      if (event.key !== 'Tab') return;
      const controls = [...(dialog.current?.querySelectorAll('button:not(:disabled), input:not(:disabled)') || [])];
      const first = controls[0], last = controls[controls.length - 1];
      if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
        event.preventDefault(); last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault(); first?.focus();
      }
    };
    document.addEventListener('keydown', close);
    return () => { document.removeEventListener('keydown', close); trigger.current?.focus(); };
  }, [open, embedded]);

  const mutate = async (operation) => {
    const token = getToken(), revision = getAuthRevision();
    setBusy(true);
    try {
      await operation();
      if (!isCurrentAuthSession(token, revision)) return;
      window.dispatchEvent(new CustomEvent('cc:devices-changed'));
      await load();
    } catch {
      if (isCurrentAuthSession(token, revision)) setError('状态已变化或操作未确认，请刷新后重试。');
    } finally {
      if (isCurrentAuthSession(token, revision)) setBusy(false);
    }
  };

  const people = data?.members.filter(member => !member.is_bot && member.user_id !== data.actor_uid) || [];
  const sharedDevices = data?.devices || [];
  const ownerName = (device) => data.members.find(member => `usr${member.user_id}` === device.ownerUserId)?.display_name || device.ownerUserId;
  const table = (devices, own) => <div className="cc-device-table-scroll" role="region" aria-label={own ? '我的设备授权表格' : '共享设备选择表格'} tabIndex={0}><table>
    <thead>
      <tr><th rowSpan={2}>电脑</th>{people.length > 0 && <th colSpan={people.length}>共享使用权</th>}{data.bots.length > 0 && <th colSpan={data.bots.length}>选择默认设备</th>}</tr>
      <tr>{people.map(person => <th key={person.user_id}>{person.display_name || person.username}</th>)}{data.bots.map(bot => <th key={bot.user_id}>{bot.display_name || bot.username}</th>)}</tr>
    </thead>
    <tbody>{devices.map(device => <tr key={deviceKey(device)}>
      <th scope="row"><strong>{device.displayName || device.deviceId}</strong><small>{ownerName(device)} · {device.deviceId}</small>
        <small>{device.routable ? '在线' : device.unavailableReason === 'group_device_share_upgrade_required' ? '需要升级 Bot 或 Connector' : '离线'}</small></th>
      {people.map(person => {
        const share = data.shares.find(item => item.owner_uid === data.actor_uid && item.grantee_uid === person.user_id && item.device_id === device.deviceId);
        return <td key={person.user_id}>{own
          ? <input type="checkbox" aria-label={`共享 ${device.displayName || device.deviceId} 给 ${person.display_name || person.username}`} checked={share?.status === 'active'} disabled={busy}
            onChange={(event) => void mutate(() => api.setGroupDeviceShare({
              action: event.target.checked ? 'share' : 'revoke', topic_id: topicId, grantee_uid: person.user_id,
              device_id: device.deviceId, expected_revision: share?.revision || 0,
            }))} />
          : <span>由 {ownerName(device)} 管理</span>}</td>;
      })}
      {data.bots.map((bot, index) => {
        const snapshot = data.snapshots[index];
        const target = snapshot.devices?.find(candidate => candidate.deviceId === device.deviceId
          && (candidate.ownerUserId || `usr${data.actor_uid}`) === device.ownerUserId);
        const canSelect = target ? target.routable : false;
        const upgradeRequired = target?.unavailableReason === 'group_device_share_upgrade_required';
        const selected = snapshot.selected_device_id === device.deviceId
          && Number(snapshot.selected_owner_uid || data.actor_uid) === Number(device.ownerUserId.replace(/^usr/, ''));
        return <td key={bot.user_id}><input type="radio" name={`device-${topicId}-${bot.user_id}`}
          aria-label={`为 ${bot.display_name || bot.username} 选择 ${device.displayName || device.deviceId}，所有者 ${ownerName(device)}`}
          title={upgradeRequired ? '需要升级此 Bot 或设备 Connector' : undefined}
          checked={selected} disabled={busy || !canSelect}
          onChange={() => void mutate(() => api.confirmConversationDevice(
            topicId, `usr${bot.user_id}`, device.deviceId, snapshot.revision, Number(device.ownerUserId.replace(/^usr/, ''))))} /></td>;
      })}
    </tr>)}</tbody>
  </table></div>;

  const content = <>
    {!embedded && <header><h2>设备与授权</h2><button type="button" aria-label="关闭" onClick={() => setOpen(false)}><X size={18} /></button></header>}
    {error && <p role="alert">{error}</p>}
    {!data && !error && <p role="status">正在读取设备…</p>}
    {data && <>
      <h3>我的设备</h3>{data.ownDevices.length ? table(data.ownDevices, true) : <p>还没有连接电脑。</p>}
      <h3>共享给我的设备</h3>{sharedDevices.length ? table(sharedDevices, false) : <p>本群暂无共享给你的设备。</p>}
      {data.snapshots.some(snapshot => snapshot.status === 'unavailable') && <p>已选设备不可用，任务会保留原目标。请重新选择后再发送指令。</p>}
      <p>共享适用于本群中的 Bot，持续至撤销。操作结果可能发送到群中。撤销只阻止后续调用。</p>
    </>}
    <button type="button" disabled={busy} onClick={() => void load()}>刷新</button>
  </>;

  if (embedded) return <section className="cc-group-devices">{content}</section>;
  return <>
    <button ref={trigger} type="button" className="cc-device-trigger" aria-label="设备与授权" onClick={() => {
      if (onOpen) onOpen(); else setOpen(true);
    }}><Laptop size={18} /></button>
    {open && createPortal(<div className="cc-device-backdrop" onMouseDown={event => { if (event.target === event.currentTarget) setOpen(false); }}>
      <section ref={dialog} className="cc-device-dialog cc-group-devices" role="dialog" aria-modal="true" aria-label="设备与授权" tabIndex={-1}>{content}</section>
    </div>, document.body)}
  </>;
}
