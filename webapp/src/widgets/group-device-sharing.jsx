import React, { useCallback, useEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { Laptop, X } from 'lucide-react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import '../css/group-device-sharing.css';
import { describeShareScope, knownShareScope } from './group-device-share-scope';

const deviceKey = device => `${device.ownerUserId}/${device.deviceId}`;
const ownerUid = device => Number(device.ownerUserId.replace(/^usr/, ''));

export default function GroupDeviceSharing({ topicId, embedded = false, onOpen }) {
  const [open, setOpen] = useState(embedded);
  const [data, setData] = useState(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [shareDrafts, setShareDrafts] = useState({});
  const [defaultDrafts, setDefaultDrafts] = useState({});
  const sequence = useRef(0);
  const saving = useRef(false);
  const dialog = useRef(null);
  const trigger = useRef(null);

  const load = useCallback(async () => {
    const seq = ++sequence.current;
    const token = getToken(), revision = getAuthRevision();
    const current = () => seq === sequence.current && isCurrentAuthSession(token, revision);
    try {
      const [sharingResult, mineResult] = await Promise.allSettled([api.getGroupDeviceShares(topicId), api.getDevices()]);
      if (mineResult.status !== 'fulfilled') throw mineResult.reason;
      const mine = mineResult.value;
      const ownDevices = (mine.devices || []).filter(device => device.runtimeRole !== 'server');
      let sharing, sharingUnavailable = false;
      if (sharingResult.status === 'fulfilled') sharing = sharingResult.value;
      else {
        sharingUnavailable = true;
        const available = await api.getConversationDevice(topicId, '');
        const actor = ownDevices.length ? ownerUid(ownDevices[0]) : 0;
        sharing = { actor_uid: actor, shares: [], devices: [], members: [
          ...(actor ? [{ user_id: actor }] : []),
          ...(available.agents || []).map(bot => ({ user_id: Number(bot.uid.replace(/^usr/, '')), display_name: bot.display_name, is_bot: true })),
        ] };
      }
      const bots = sharing.members.filter(member => member.is_bot);
      const results = await Promise.allSettled(bots.map(bot => api.getConversationDevice(topicId, `usr${bot.user_id}`)));
      const snapshots = results.map(result => result.status === 'fulfilled' ? result.value : { status: 'bot_unavailable', devices: [] });
      if (current()) setData({ ...sharing, bots, snapshots, ownDevices, sharingUnavailable });
      return current();
    } catch {
      if (current()) setError('暂时无法读取设备状态，请刷新重试。');
      return false;
    }
  }, [topicId]);

  const clearDrafts = () => { setShareDrafts({}); setDefaultDrafts({}); };
  const refresh = async () => {
    if (saving.current) return;
    clearDrafts(); setNeedsRefresh(false); setError('');
    await load();
  };

  useEffect(() => {
    void load();
    const refreshBackground = () => { if (!saving.current) void load(); };
    const reset = () => {
      ++sequence.current; saving.current = false; setData(null); setOpen(false); setBusy(false);
      clearDrafts(); setError(''); setNeedsRefresh(false);
      if (getToken()) void load();
    };
    window.addEventListener('cc:devices-changed', refreshBackground);
    window.addEventListener('cc:auth-changed', reset);
    window.addEventListener('focus', refreshBackground);
    const timer = setInterval(() => { if (open) refreshBackground(); }, 3000);
    return () => {
      ++sequence.current; clearInterval(timer);
      window.removeEventListener('cc:devices-changed', refreshBackground);
      window.removeEventListener('cc:auth-changed', reset);
      window.removeEventListener('focus', refreshBackground);
    };
  }, [load, open]);

  useEffect(() => {
    if (!open || embedded) return undefined;
    dialog.current?.focus();
    const close = event => {
      if (event.key === 'Escape' && !saving.current) { setOpen(false); clearDrafts(); }
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

  const stageShare = (device, person, checked) => {
    const key = `${device.deviceId}/${person.user_id}`;
    const share = data.shares.find(item => item.owner_uid === data.actor_uid && item.grantee_uid === person.user_id && item.device_id === device.deviceId);
    setShareDrafts(previous => {
      const next = { ...previous };
      if (checked === (share?.status === 'active')) delete next[key];
      else next[key] = { checked, deviceId: device.deviceId, grantee: person.user_id, operations: Array.isArray(data.share_operations?.[device.deviceId]) ? [...data.share_operations[device.deviceId]] : undefined, expectedRevision: previous[key]?.expectedRevision ?? share?.revision ?? 0 };
      return next;
    });
  };
  const stageDefault = (device, bot, snapshot) => {
    setDefaultDrafts(previous => {
      const next = { ...previous };
      if (device ? snapshot.status !== 'automatic' && snapshot.selected_device_id === device.deviceId && Number(snapshot.selected_owner_uid || data.actor_uid) === ownerUid(device) : !snapshot.selected_device_id) delete next[bot.user_id];
      else next[bot.user_id] = { device, expectedRevision: previous[bot.user_id]?.expectedRevision ?? snapshot.revision };
      return next;
    });
  };

  const confirm = async () => {
    if (saving.current || needsRefresh) return;
    const token = getToken(), revision = getAuthRevision();
    const current = () => isCurrentAuthSession(token, revision);
    const operation = Symbol('device-confirm');
    saving.current = operation; ++sequence.current; setBusy(true); setError('');
    let completed = 0, noticePending = false;
    try {
      for (const [key, draft] of Object.entries(shareDrafts)) {
        if (!current()) return;
        const result = await api.setGroupDeviceShare({
          action: draft.checked ? 'share' : 'revoke', topic_id: topicId, grantee_uid: draft.grantee,
          device_id: draft.deviceId, expected_revision: draft.expectedRevision,
          ...(draft.checked ? { expected_operations: draft.operations } : {}),
        });
        if (!current()) return;
        noticePending ||= result?.notice?.pending === true;
        completed++;
        setShareDrafts(previous => { const next = { ...previous }; delete next[key]; return next; });
      }
      for (const [bot, draft] of Object.entries(defaultDrafts)) {
        if (!current()) return;
        const result = await api.confirmConversationDevice(topicId, `usr${bot}`, draft.device?.deviceId || '', draft.expectedRevision, draft.device ? ownerUid(draft.device) : undefined);
        if (!current()) return;
        noticePending ||= result?.notice?.pending === true;
        completed++;
        setDefaultDrafts(previous => { const next = { ...previous }; delete next[bot]; return next; });
      }
      if (!current()) return;
      await load();
      setError(noticePending ? '设置已保存，对话提示暂未确认，请刷新对话核对。' : '');
    } catch {
      if (current()) {
        setNeedsRefresh(true);
        setError(`已确认 ${completed} 项，其余结果尚未确认。请刷新核对后重新选择；不会自动重试。`);
        await load();
      }
    } finally {
      if (saving.current === operation) saving.current = false;
      if (current()) {
        setBusy(false);
        window.dispatchEvent(new CustomEvent('cc:devices-changed'));
      }
    }
  };

  const people = data?.members.filter(member => !member.is_bot && member.user_id !== data.actor_uid) || [];
  const ownerName = device => {
    const member = data.members.find(member => `usr${member.user_id}` === device.ownerUserId);
    return member?.display_name || member?.username || device.ownerUserId;
  };
  const pendingCount = Object.keys(shareDrafts).length + Object.keys(defaultDrafts).length;
  const devices = [...(data?.ownDevices || []), ...(data?.devices || [])];
  const newShares = Object.values(shareDrafts).filter(draft => draft.checked);
  const invalidShareScope = newShares.some(draft => !knownShareScope(draft.operations));
  const table = () => <div className="cc-device-table-scroll" role="region" aria-label="设备与授权表格" tabIndex={0}><table>
    <thead>
      <tr><th rowSpan={2} scope="col" aria-label="设备" />{people.length > 0 && <th colSpan={people.length}>共享使用权</th>}{data.bots.length > 0 && <th colSpan={data.bots.length}>默认设备</th>}</tr>
      <tr>{people.map(person => <th key={person.user_id}>{person.display_name || person.username}</th>)}{data.bots.map((bot, index) => <th key={bot.user_id}>{bot.display_name || bot.username}{data.snapshots[index].status === 'bot_unavailable' && <small>暂不可用</small>}</th>)}</tr>
    </thead>
    <tbody>
      <tr>
        <th scope="row">未指定</th>
        {people.length > 0 && <td colSpan={people.length} />}
        {data.bots.map((bot, index) => {
          const snapshot = data.snapshots[index], draft = defaultDrafts[bot.user_id];
          return <td key={bot.user_id}><input type="radio" name={`device-${topicId}-${bot.user_id}`}
            aria-label={`为 ${bot.display_name || bot.username} 清除默认设备`}
            checked={draft ? draft.device === null : !snapshot.selected_device_id}
            disabled={busy || needsRefresh || snapshot.status === 'bot_unavailable'}
            onChange={() => stageDefault(null, bot, snapshot)} /></td>;
        })}
      </tr>
      {devices.map(device => {
      const own = ownerUid(device) === data.actor_uid;
      return <tr key={deviceKey(device)}>
        <th scope="row" title={device.deviceId}><strong>{device.displayName || device.deviceId}</strong>
          {devices.some(other => deviceKey(other) !== deviceKey(device) && (other.displayName || other.deviceId) === (device.displayName || device.deviceId)) && <small>{device.deviceId}</small>}
          <small>{device.routable ? '在线' : device.unavailableReason === 'group_device_share_upgrade_required' ? '需要升级 Bot' : '离线'}</small></th>
        {own ? people.map(person => {
          const share = data.shares.find(item => item.owner_uid === data.actor_uid && item.grantee_uid === person.user_id && item.device_id === device.deviceId);
          const draft = shareDrafts[`${device.deviceId}/${person.user_id}`];
          return <td key={person.user_id}><input type="checkbox" aria-label={`共享 ${device.displayName || device.deviceId} 给 ${person.display_name || person.username}`}
              checked={draft?.checked ?? (share?.status === 'active')} disabled={busy || needsRefresh || data.sharingUnavailable}
              onChange={event => stageShare(device, person, event.target.checked)} /></td>;
        }) : people.length > 0 && <td colSpan={people.length}><span>由 {ownerName(device)} 授权</span></td>}
        {data.bots.map((bot, index) => {
          const snapshot = data.snapshots[index];
          const target = snapshot.devices?.find(candidate => candidate.deviceId === device.deviceId && (candidate.ownerUserId || `usr${data.actor_uid}`) === device.ownerUserId);
          const draft = defaultDrafts[bot.user_id];
          const selected = draft ? !!draft.device && deviceKey(draft.device) === deviceKey(device) : snapshot.selected_device_id === device.deviceId && Number(snapshot.selected_owner_uid || data.actor_uid) === ownerUid(device);
          return <td key={bot.user_id}><input type="radio" name={`device-${topicId}-${bot.user_id}`}
            aria-label={`为 ${bot.display_name || bot.username} 选择 ${device.displayName || device.deviceId}，所有者 ${ownerName(device)}`}
            title={target?.unavailableReason === 'group_device_share_upgrade_required' ? '需要升级此 Bot' : undefined}
            checked={selected} disabled={busy || needsRefresh || !target?.routable}
            onChange={() => stageDefault(device, bot, snapshot)} /></td>;
        })}
      </tr>;
    })}</tbody>
  </table></div>;

  const content = <>
    {!embedded && <header><h2>设备与授权</h2><button type="button" aria-label="关闭" disabled={busy} onClick={() => { setOpen(false); clearDrafts(); }}><X size={18} /></button></header>}
    <div className="cc-device-confirm-actions">
      <button type="button" disabled={busy} onClick={() => void refresh()}>刷新</button>
      <button type="button" disabled={busy || !pendingCount} onClick={clearDrafts}>取消更改</button>
      <button type="button" disabled={busy || needsRefresh || !pendingCount || invalidShareScope} onClick={() => void confirm()}>确认</button>
    </div>
    {error && <p role="alert">{error}</p>}
    {!data && !error && <p role="status">正在读取设备…</p>}
    {data && <>
      {data.sharingUnavailable && <p role="status">共享暂不可用</p>}
      {devices.length || data.bots.length ? table() : <p>暂无设备</p>}
      {data.snapshots.some(snapshot => snapshot.status === 'unavailable') && <p role="status">已选设备不可用</p>}
    </>}
    {newShares.map(draft => <p key={`${draft.deviceId}/${draft.grantee}`} aria-label="共享授权范围">
      {devices.find(device => device.deviceId === draft.deviceId && ownerUid(device) === data.actor_uid)?.displayName || draft.deviceId}
      {' → '}{data.members.find(member => member.user_id === draft.grantee)?.display_name || data.members.find(member => member.user_id === draft.grantee)?.username || `usr${draft.grantee}`}：{describeShareScope(draft.operations)}
    </p>)}
    {pendingCount > 0 && <p role="status">{pendingCount} 项待确认</p>}
  </>;
  if (embedded) return <section className="cc-group-devices">{content}</section>;
  return <>
    <button ref={trigger} type="button" className="cc-device-trigger" aria-label="设备与授权" onClick={() => { if (onOpen) onOpen(); else setOpen(true); }}><Laptop size={18} /></button>
    {open && createPortal(<div className="cc-device-backdrop" onMouseDown={event => { if (event.target === event.currentTarget && !saving.current) { setOpen(false); clearDrafts(); } }}>
      <section ref={dialog} className="cc-device-dialog cc-group-devices" role="dialog" aria-modal="true" aria-label="设备与授权" tabIndex={-1}>{content}</section>
    </div>, document.body)}
  </>;
}
