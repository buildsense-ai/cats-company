import React, { useCallback, useEffect, useId, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import CustomSelect from './custom-select';
import SlidingSegments from './sliding-segments';
import LoadingState from './loading-state';
import useDialogBehavior from '../utils/use-dialog-behavior';
import { RefreshCw, ShieldCheck, X } from 'lucide-react';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import '../css/group-device-sharing.css';
import { knownShareScope } from './group-device-share-scope';

const deviceKey = device => `${device.ownerUserId}/${device.deviceId}`;
const ownerUid = device => Number(device.ownerUserId.replace(/^usr/, ''));
const READ_ERROR = '暂时无法读取设备状态，请刷新重试。';

export default function GroupDeviceSharing({ topicId, embedded = false, onOpen }) {
  const [open, setOpen] = useState(embedded);
  const [data, setData] = useState(null);
  const [botId, setBotId] = useState(null);
  const [sharingOpen, setSharingOpen] = useState(false);
  const sharingId = useId();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [needsRefresh, setNeedsRefresh] = useState(false);
  const [shareDrafts, setShareDrafts] = useState({});
  const [defaultDrafts, setDefaultDrafts] = useState({});
  const activeRead = useRef(null);
  const saving = useRef(false);
  const dialog = useRef(null);
  const trigger = useRef(null);

  const cancelRead = useCallback(() => {
    activeRead.current?.abort();
    activeRead.current = null;
  }, []);

  const load = useCallback(async () => {
    const token = getToken(), revision = getAuthRevision();
    if (!token || activeRead.current) return;
    const controller = new AbortController();
    activeRead.current = controller;
    const options = { signal: controller.signal, timeoutMs: 15_000 };
    const current = () => !controller.signal.aborted && isCurrentAuthSession(token, revision);
    try {
      const [sharingResult, mineResult] = await Promise.allSettled([api.getGroupDeviceShares(topicId, options), api.getDevices(options)]);
      if (!current()) return;
      if (mineResult.status !== 'fulfilled') throw mineResult.reason;
      const mine = mineResult.value;
      const ownDevices = (mine.devices || []).filter(device => device.runtimeRole !== 'server');
      let sharing, sharingUnavailable = false;
      if (sharingResult.status === 'fulfilled') sharing = sharingResult.value;
      else {
        sharingUnavailable = true;
        const available = await api.getConversationDevice(topicId, '', options);
        if (!current()) return;
        const actor = ownDevices.length ? ownerUid(ownDevices[0]) : 0;
        sharing = { actor_uid: actor, shares: [], devices: [], members: [
          ...(actor ? [{ user_id: actor }] : []),
          ...(available.agents || []).map(bot => ({ user_id: Number(bot.uid.replace(/^usr/, '')), display_name: bot.display_name, is_bot: true })),
        ] };
      }
      const bots = sharing.members.filter(member => member.is_bot);
      const results = await Promise.allSettled(bots.map(bot => api.getConversationDevice(topicId, `usr${bot.user_id}`, options)));
      if (!current()) return;
      const snapshots = results.map(result => result.status === 'fulfilled' ? result.value : { status: 'bot_unavailable', devices: [] });
      setData({ ...sharing, bots, snapshots, ownDevices, sharingUnavailable });
      setError(previous => previous === READ_ERROR ? '' : previous);
    } catch {
      if (current()) setError(previous => previous || READ_ERROR);
    } finally {
      if (activeRead.current === controller) activeRead.current = null;
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
      cancelRead(); saving.current = false; setData(null); setOpen(embedded); setBusy(false);
      clearDrafts(); setBotId(null); setSharingOpen(false); setError(''); setNeedsRefresh(false);
      if (embedded || !open) void load();
    };
    window.addEventListener('cc:devices-changed', refreshBackground);
    window.addEventListener('cc:auth-changed', reset);
    window.addEventListener('focus', refreshBackground);
    const timer = setInterval(() => { if (open) refreshBackground(); }, 3000);
    return () => {
      cancelRead(); clearInterval(timer);
      window.removeEventListener('cc:devices-changed', refreshBackground);
      window.removeEventListener('cc:auth-changed', reset);
      window.removeEventListener('focus', refreshBackground);
    };
  }, [cancelRead, embedded, load, open]);

  useDialogBehavior(dialog, {
    open: open && !embedded, returnFocusRef: trigger,
    onClose: () => { if (!saving.current) { setOpen(false); clearDrafts(); } },
  });

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
    saving.current = operation; cancelRead(); setBusy(true); setError('');
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
      setError(noticePending ? '设置已保存，对话提示暂未确认，请刷新对话核对。' : '');
      await load();
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
  const memberName = person => person.display_name || person.username || `usr${person.user_id}`;
  const bot = data?.bots.find(person => person.user_id === botId) || data?.bots[0];
  const snapshot = bot && data.snapshots[data.bots.indexOf(bot)];
  const draft = bot && defaultDrafts[bot.user_id];
  const blocked = busy || needsRefresh;
  const deviceLabel = device => device.displayName || device.deviceId;
  const deviceName = device => devices.some(other => deviceKey(other) !== deviceKey(device) && deviceLabel(other) === deviceLabel(device))
    ? `${deviceLabel(device)} · ${device.deviceId}` : deviceLabel(device);
  const content = <>
    {!embedded && <header><h2>设备与授权</h2><button type="button" aria-label="关闭" disabled={busy} onClick={() => { setOpen(false); clearDrafts(); }}><X size={18} /></button></header>}
    <div className="cc-group-device-heading">
      <SlidingSegments className="cc-group-device-tabs" role="tablist" aria-label="设备与授权" keyboardTabs value={sharingOpen}>
        {['助手设备', '成员授权'].map((label, index) => (
          <button key={label} type="button" role="tab" id={`${sharingId}-tab-${index}`}
            aria-selected={sharingOpen === Boolean(index)} aria-controls={sharingId} onClick={() => setSharingOpen(Boolean(index))}>{label}</button>
        ))}
      </SlidingSegments>
      {(data || error) && <button type="button" className="cc-device-refresh" aria-label="刷新设备列表" title="刷新设备列表" disabled={busy} onClick={() => void refresh()}><RefreshCw size={16} aria-hidden="true" /></button>}
    </div>
    <div className="cc-group-device-panel" role="tabpanel" id={sharingId} aria-labelledby={`${sharingId}-tab-${Number(sharingOpen)}`}>
    {error && <p role="alert">{error}</p>}
    {!data && !error && <LoadingState kind="connect" label="正在读取设备" compact />}
    {data && <>
      {!sharingOpen && (bot ? <>
        {data.bots.length > 1 ? <CustomSelect ariaLabel="选择群内助手" value={bot.user_id} onValueChange={value => setBotId(Number(value))} disabled={busy}>
          {data.bots.map(person => <option key={person.user_id} value={person.user_id}>{memberName(person)}</option>)}
        </CustomSelect> : <span className="cc-group-bot-name">{memberName(bot)} 使用的设备</span>}
        <fieldset className="cc-group-device-options" aria-label={`${memberName(bot)} 的设备`}>
          <label className="cc-group-device-option">
            <input type="radio" name={`device-${topicId}-${bot.user_id}`} aria-label={`为 ${memberName(bot)} 切换到云端`}
              checked={snapshot.status !== 'bot_unavailable' && (draft ? draft.device === null : !snapshot.selected_device_id)}
              disabled={blocked || snapshot.status === 'bot_unavailable'} onChange={() => stageDefault(null, bot, snapshot)} />
            <span>云端</span>
          </label>
          {devices.map(device => {
            const target = snapshot.devices?.find(candidate => candidate.deviceId === device.deviceId && (candidate.ownerUserId || `usr${data.actor_uid}`) === device.ownerUserId);
            const selected = draft ? !!draft.device && deviceKey(draft.device) === deviceKey(device) : snapshot.selected_device_id === device.deviceId && Number(snapshot.selected_owner_uid || data.actor_uid) === ownerUid(device);
            const status = target?.routable ? '在线' : target?.unavailableReason === 'group_device_share_upgrade_required' ? '需要升级助手' : device.routable ? '不可用' : '离线';
            return <label className="cc-group-device-option" key={deviceKey(device)}>
              <input type="radio" name={`device-${topicId}-${bot.user_id}`}
                aria-label={`为 ${memberName(bot)} 选择 ${deviceLabel(device)}，所有者 ${ownerName(device)}`}
                title={status} checked={selected} disabled={blocked || !target?.routable} onChange={() => stageDefault(device, bot, snapshot)} />
              <span>{deviceName(device)}{ownerUid(device) !== data.actor_uid && <small>由 {ownerName(device)} 授权</small>}</span>
              <small>{status}</small>
            </label>;
          })}
        </fieldset>
        {snapshot.status === 'unavailable' && <p role="status">已选设备不可用</p>}
        {snapshot.status === 'bot_unavailable' && <p role="status">暂时无法读取这位助手的设备</p>}
      </> : <p>群内暂无助手</p>)}
      {sharingOpen && <div className="cc-group-device-shares">
        {data.sharingUnavailable ? <p role="status">共享暂不可用</p>
          : !people.length ? <p>群内没有其他成员可授权</p>
          : !data.ownDevices.length ? <p>尚未连接自己的电脑</p>
          : data.ownDevices.map(device => <fieldset className="cc-group-device-options" key={deviceKey(device)}>
            <legend>可使用「{deviceName(device)}」的成员</legend>
            {people.map(person => {
              const share = data.shares.find(item => item.owner_uid === data.actor_uid && item.grantee_uid === person.user_id && item.device_id === device.deviceId);
              const change = shareDrafts[`${device.deviceId}/${person.user_id}`];
              return <label key={person.user_id} className="cc-group-device-option">
                <input type="checkbox" aria-label={`共享 ${deviceLabel(device)} 给 ${memberName(person)}`}
                  checked={change?.checked ?? (share?.status === 'active')} disabled={blocked}
                  onChange={event => stageShare(device, person, event.target.checked)} />
                <span>{memberName(person)}</span>
              </label>;
            })}
          </fieldset>)}
      </div>}
    </>}
    </div>
    {invalidShareScope && <p role="alert">授权范围暂不可确认，请刷新后重试。</p>}
    <div className="cc-device-confirm-actions">
      <button type="button" className="oc-btn oc-btn-default" disabled={!pendingCount || busy} onClick={clearDrafts}>取消更改</button>
      <button type="button" className="oc-btn oc-btn-primary" disabled={!pendingCount || blocked || invalidShareScope} onClick={() => void confirm()}>确认</button>
    </div>
  </>;
  if (embedded) return <section className="cc-group-devices">{content}</section>;
  return <>
    <button ref={trigger} type="button" className="cc-device-trigger" aria-label="设备与授权" onClick={() => { if (onOpen) onOpen(); else setOpen(true); }}><ShieldCheck size={18} /></button>
    {open && createPortal(<div className="cc-device-backdrop" onMouseDown={event => { if (event.target === event.currentTarget && !saving.current) { setOpen(false); clearDrafts(); } }}>
      <section ref={dialog} className="cc-device-dialog cc-group-devices" role="dialog" aria-modal="true" aria-label="设备与授权" tabIndex={-1}>{content}</section>
    </div>, document.body)}
  </>;
}
