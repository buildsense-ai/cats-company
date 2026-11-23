import React, { useCallback, useEffect, useId, useRef, useState } from 'react';
import GroupDeviceSharing from './group-device-sharing';
import { createPortal } from 'react-dom';
import { Laptop, RefreshCw, X } from './desktop-icons';
import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import LoadingState from './loading-state';
import '../css/conversation-device-picker.css';

export default function ConversationDevicePicker({ topicId, activeAgent, onOpen, embedded = false }) {
  const agentId = activeAgent?.uid || activeAgent?.id;
  if (!topicId) return null;
  if (topicId.startsWith('grp_')) return <GroupDeviceSharing key={topicId} topicId={topicId} onOpen={onOpen} embedded={embedded} />;
  if (!agentId) return null;
  return <DevicePicker key={`${topicId}:${agentId}`} topicId={topicId} agentId={agentId} onOpen={onOpen} embedded={embedded}
    agentName={activeAgent?.display_name || activeAgent?.name || activeAgent?.username || String(agentId)} />;
}

function DevicePicker({ topicId, agentId, agentName, onAgentPick, initialOpen = false, onOpen, embedded = false }) {
  const [snapshot, setSnapshot] = useState(null);
  const [draft, setDraft] = useState('');
  const [open, setOpen] = useState(initialOpen);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState('');
  const sequence = useRef(0);
  const deviceChangeSource = useRef({});
  const trigger = useRef(null);
  const dialog = useRef(null);
  const titleId = useId();

  const load = useCallback(async () => {
    const seq = ++sequence.current;
    const token = getToken(), authRevision = getAuthRevision();
    const current = () => sequence.current === seq && isCurrentAuthSession(token, authRevision);
    setBusy(true);
    setError('');
    try {
      const next = await api.getConversationDevice(topicId, agentId);
      if (!current()) return;
      setSnapshot(next);
      setDraft(next.selected_device_id || '');
    } catch {
      if (!current()) return;
      setSnapshot(null);
      setDraft('');
      setError('暂时无法读取设备状态，请刷新重试。');
    } finally {
      if (current()) setBusy(false);
    }
  }, [topicId, agentId]);

  useEffect(() => {
    void load();
    const onAuth = () => {
      ++sequence.current;
      setSnapshot(null); setDraft(''); setOpen(false); setBusy(false); setError('');
      if (getToken()) void load();
    };
    const onDevicesChanged = (event) => {
      // The confirming picker already has the authoritative response. Notify
      // other pickers without reloading and overwriting its own selection.
      if (event.detail?.source !== deviceChangeSource.current) void load();
    };
    window.addEventListener('cc:auth-changed', onAuth);
    window.addEventListener('cc:devices-changed', onDevicesChanged);
    return () => {
      ++sequence.current;
      window.removeEventListener('cc:auth-changed', onAuth);
      window.removeEventListener('cc:devices-changed', onDevicesChanged);
    };
  }, [load]);

  useEffect(() => {
    if (open || busy || embedded) return undefined;
    const refresh = () => { if (getToken()) void load(); };
    window.addEventListener('focus', refresh);
    return () => window.removeEventListener('focus', refresh);
  }, [open, busy, embedded, load]);

  useEffect(() => {
    if (!open || embedded || onOpen) return undefined;
    dialog.current?.focus();
    const onKey = (event) => {
      if (event.key === 'Escape') { event.preventDefault(); setOpen(false); }
      if (event.key !== 'Tab') return;
      const controls = [...(dialog.current?.querySelectorAll('button:not(:disabled), input:not(:disabled)') || [])];
      const first = controls[0], last = controls[controls.length - 1];
      if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) {
        event.preventDefault(); last?.focus();
      } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.current)) {
        event.preventDefault(); first?.focus();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => { document.removeEventListener('keydown', onKey); trigger.current?.focus(); };
  }, [open, embedded, onOpen]);

  const confirm = async () => {
    if (busy || !snapshot || snapshot.status === 'choose_agent' || (draft && !snapshot.devices?.some((device) => device.deviceId === draft && device.routable))) return;
    const seq = ++sequence.current;
    const token = getToken(), authRevision = getAuthRevision();
    const current = () => sequence.current === seq && isCurrentAuthSession(token, authRevision);
    setBusy(true); setError('');
    try {
      const next = await api.confirmConversationDevice(topicId, agentId, draft, snapshot.revision);
      if (!current()) return;
      setSnapshot(next); setDraft(next.selected_device_id || ''); setOpen(next.notice?.pending === true);
      if (next.notice?.pending) setError('设备已保存，对话提示暂未确认，请刷新对话核对。');
      if (embedded) window.dispatchEvent(new CustomEvent('cc:devices-changed', { detail: { source: deviceChangeSource.current } }));
    } catch {
      if (!current()) return;
      // A timeout may have committed, or another tab may have selected a
      // different computer. Never resubmit a switch without user confirmation.
      setSnapshot(null);
      setError('未能确认选择结果，可能设备状态或选择已变化。请先刷新，再确认；不会自动重试切换。');
    } finally {
      if (current()) setBusy(false);
    }
  };

  const selected = snapshot?.devices?.find((device) => device.deviceId === snapshot.selected_device_id);
  const label = !snapshot ? (busy ? '读取中' : '状态待确认')
    : snapshot.status === 'choose_agent' ? '按 Bot 选择'
    : snapshot.status === 'unspecified' ? '未指定'
    : snapshot.status === 'unavailable' ? `${selected?.displayName || snapshot.selected_device_id} · 离线`
      : snapshot.status === 'needs_selection' ? '未指定'
        : selected?.displayName || selected?.deviceId || '请选择';
  const description = `默认设备${agentId ? `（${snapshot?.agent_name || agentName}）` : ''}：${label}`;
  const changed = !!snapshot && (draft !== (snapshot.selected_device_id || '') || (draft !== '' && snapshot.status === 'automatic'));
  const content = <>
    <div className="cc-device-selection-heading">
      <span>{agentId ? snapshot?.agent_name || agentName : '选择 Agent'}</span>
      {snapshot?.status !== 'choose_agent' && <button type="button" disabled={busy || !changed || (draft !== '' && !snapshot?.devices?.some((device) => device.deviceId === draft && device.routable))} onClick={() => void confirm()}>确认</button>}
      <button type="button" disabled={busy} aria-label="刷新设备列表" title="刷新" onClick={() => void load()}><RefreshCw size={16} aria-hidden="true" /></button>
    </div>
    {onAgentPick && agentId && <button type="button" disabled={busy} onClick={() => onAgentPick(null)}>更换 Agent</button>}
    <div className="cc-computer-list-region" data-loading-region="" aria-busy={busy}>
      {busy && !snapshot
        ? <LoadingState kind="connect" className="cc-computer-list-loading" label="正在读取设备" compact />
        : <div data-loading-content="">
          {snapshot?.status === 'choose_agent' && <div className="cc-device-list">
            {(snapshot.agents || []).map((agent) => <button type="button" key={agent.uid} disabled={busy} onClick={() => onAgentPick(agent)}>{agent.display_name} · {agent.uid}</button>)}
            {!snapshot.agents?.length && <p>当前对话中没有可选择的 Agent。</p>}
          </div>}
          {error && <p role="alert" className="cc-device-error">{error}</p>}
          <div className="cc-device-list" role="group" aria-label="我的设备">
            {snapshot && snapshot.status !== 'choose_agent' && <label className="cc-device-option">
              <input type="radio" name={titleId} value="" aria-label="清除默认设备" checked={draft === ''}
                disabled={busy} onChange={() => setDraft('')} />
              <span><strong>未指定</strong></span>
            </label>}
            {(snapshot?.devices || []).map((device) => {
              const name = device.displayName || device.deviceId;
              const duplicate = snapshot.devices.some(other => other.deviceId !== device.deviceId && (other.displayName || other.deviceId) === name);
              return <label key={device.deviceId} className="cc-device-option" title={device.deviceId}>
                <input type="radio" name={titleId} value={device.deviceId} checked={draft === device.deviceId}
                  disabled={busy || !device.routable} onChange={() => setDraft(device.deviceId)} />
                <span><strong>{name}</strong>{duplicate && <small>{device.deviceId}</small>}</span>
                <span>{device.routable ? '在线' : '离线'}</span>
              </label>;
            })}
          </div>
          {snapshot && snapshot.status !== 'choose_agent' && !snapshot.devices?.length && <p>暂无设备</p>}
          {snapshot?.status === 'unavailable' && <p className="cc-device-note" role="status">已选设备不可用</p>}
        </div>}
    </div>
  </>;
  if (embedded) return <section className="cc-device-selection" aria-label="当前对话的设备">{content}</section>;
  return <>
    <button ref={trigger} type="button" className="v3-action-btn cc-device-trigger" aria-haspopup="dialog" aria-expanded={onOpen ? undefined : open}
      title={description} aria-label={description} onClick={() => { if (onOpen) onOpen(); else { setOpen(true); void load(); } }}>
      <Laptop size={16} aria-hidden="true" />
    </button>
    {open && createPortal(<div className="cc-device-backdrop" onClick={(event) => { if (event.target === event.currentTarget) setOpen(false); }}>
      <section ref={dialog} tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby={titleId} className="cc-device-dialog">
        <header><h2 id={titleId}>设备</h2><button type="button" aria-label="关闭设备选择" onClick={() => setOpen(false)}><X size={18} /></button></header>
        {content}
      </section>
    </div>, document.body)}
  </>;
}
