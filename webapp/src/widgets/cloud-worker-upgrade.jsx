import React, { useEffect, useRef, useState } from 'react';
import './plans/credit-commerce.css';
import { api } from '../api';
import { readStorageValue, writeStorageValue } from '../utils/storage-access';

const plans = { light: '轻量', standard: '标准', pro: '专业', heavy: '重载' };
const terminal = status => ['completed', 'failed'].includes(status);
const labels = { pending: '等待升配', running: '正在升配', reconciling: '正在核实机器配置', completed: '升配完成', failed: '升配未完成' };
const formatPoints = (units, scale = 1_000_000) => (Number(units || 0) / scale).toLocaleString('zh-CN', { maximumFractionDigits: 6 });

export default function CloudWorkerUpgrade({ worker, disabled = false }) {
  const storageKey = `catsco_machine_upgrade_v1:${worker.id || worker.uid}:${worker.tenant_name}`;
  const [record, setRecord] = useState(() => {
    try { return JSON.parse(readStorageValue(storageKey, 'sessionStorage') || 'null'); } catch { return null; }
  });
  const [target, setTarget] = useState('standard');
  const [quote, setQuote] = useState(null);
  const [operation, setOperation] = useState(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [clock, setClock] = useState(Date.now());
  const submitting = useRef(false);
  const saveRecord = value => {
    writeStorageValue(storageKey, JSON.stringify(value), 'sessionStorage');
    setRecord(value);
  };

  useEffect(() => {
    if (!quote) return undefined;
    const timer = window.setInterval(() => setClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [quote]);

  useEffect(() => {
    if (!record?.operation_id) return undefined;
    let stopped = false;
    let timer;
    const controller = new AbortController();
    const poll = async () => {
      let done = false;
      try {
        const result = await api.getCloudWorkerUpgradeOperation(record.operation_id, { signal: controller.signal, timeoutMs: 20_000 });
        if (stopped) return;
        const next = result.operation || result;
        setOperation(next);
        setError('');
        done = terminal(next.status);
        if (done) saveRecord(null);
      } catch (err) {
        if (!stopped) setError(err.message || '暂时无法读取升配进度，请稍后继续查看。');
      }
      if (!stopped && !done) timer = window.setTimeout(poll, 4000);
    };
    poll();
    return () => { stopped = true; controller.abort(); window.clearTimeout(timer); };
  }, [record?.operation_id]);

  const getQuote = async () => {
    if (busy || record) return;
    setBusy(true); setError(''); setQuote(null); setOperation(null);
    try {
      const result = await api.getCloudWorkerUpgradeQuote(worker.tenant_name, target, { timeoutMs: 20_000 });
      if (result.enabled !== true || !result.quote_id) throw new Error(result.reason || '这台机器暂不可升配。');
      setClock(Date.now()); setQuote(result);
    } catch (err) { setError(err.message || '报价读取失败，请稍后重试。'); }
    finally { setBusy(false); }
  };

  const submit = async () => {
    if (submitting.current || disabled || (record?.operation_id)) return;
    const request = record || (quote && {
      name: worker.tenant_name, plan: quote.to_plan, quote_id: quote.quote_id,
      client_request_id: `upgrade_${globalThis.crypto?.randomUUID?.().replaceAll('-', '') || `${Date.now()}_${Math.random().toString(36).slice(2)}`}`,
    });
    if (!request) return;
    submitting.current = true; setBusy(true); setError('');
    saveRecord(request);
    try {
      const result = await api.upgradeCloudWorker(request.name, request.plan, request.quote_id, request.client_request_id, { timeoutMs: 40_000 });
      const next = result.operation || result;
      if (!next.operation_id || !next.status) throw new Error('升配请求已发送，暂未取得操作状态。请继续查询同一请求。');
      setOperation(next); setQuote(null);
      saveRecord(terminal(next.status) ? null : { ...request, operation_id: next.operation_id });
    } catch (err) {
      const result = err.data;
      if (result?.operation_id && result?.status) {
        setOperation(result); setQuote(null);
        saveRecord(terminal(result.status) ? null : { ...request, operation_id: result.operation_id });
        setError(err.message || '正在核实本次升配。');
      } else {
        const status = Number(err.status);
        const code = err.code || result?.code;
        const busy = code === 'cloud_worker_operation_busy';
        const definite = status >= 400 && status < 500 && ![408, 429].includes(status) && !busy;
        if (definite) { saveRecord(null); setQuote(null); }
        setError(definite ? (err.message || '升配请求未被接受，请重新报价。') : '暂未确认升配结果，请继续查询同一请求；不要重复创建升配。');
      }
    } finally { submitting.current = false; setBusy(false); }
  };

  const expired = !quote?.expires_at || new Date(quote.expires_at).getTime() <= clock;
  const enough = quote && Number.isSafeInteger(Number(quote.balance_units)) && Number(quote.balance_units) >= Number(quote.cost_units);
  const active = record || (operation && !terminal(operation.status));
  return (
    <section className="cloud-upgrade-card" aria-label={`${worker.display_name || worker.tenant_name}机器升配`}>
      <div className="cloud-upgrade-heading"><strong>机器配置升配</strong><span>点数支付</span></div>
      {!active && operation?.status !== 'completed' && <div className="cloud-upgrade-select">
        <label>目标配置 <select value={target} disabled={disabled || busy} onChange={event => { setTarget(event.target.value); setQuote(null); setError(''); }}>
          {Object.entries(plans).map(([id, title]) => <option key={id} value={id}>{title}（{id}）</option>)}
        </select></label>
        <button type="button" disabled={disabled || busy} onClick={getQuote}>{busy ? '读取中…' : '查看升配报价'}</button>
      </div>}
      {quote && !active && <div className="cloud-upgrade-quote">
        <strong>{plans[quote.from_plan] || quote.from_plan} → {plans[quote.to_plan] || quote.to_plan}</strong>
        <p>本次支付 {formatPoints(quote.cost_units, quote.units_per_credit)} 点 · 可用 {formatPoints(quote.balance_units, quote.units_per_credit)} 点</p>
        <p>按剩余租期补差价，到期日保持 {new Date(quote.worker_expires_at).toLocaleDateString('zh-CN')}。升配只调整机器配置，模型按实际用量另扣点数。</p>
        <p>使用套餐月额度和充值余额支付，邀请赠送点数仅用于模型调用。</p>
        {quote.needs_migration && <p>当前容量不足，需要迁移后才能升配，请联系支持。</p>}
        {!enough && <p>余额不足，请先到「套餐与点数」充值，再重新报价。</p>}
        <button type="button" disabled={disabled || busy || expired || !enough || quote.needs_migration} onClick={submit}>
          {expired ? '报价已过期，请重新获取' : '确认并支付点数'}
        </button>
      </div>}
      {operation && <div role="status" className="cloud-upgrade-progress">
        <strong>{labels[operation.status] || '正在查询升配状态'}</strong>
        {operation.message && <p>{operation.message}</p>}
        {operation.status === 'completed' && <p>当前配置：{plans[operation.to_plan] || operation.to_plan}，机器到期日保持不变。</p>}
        {!terminal(operation.status) && <p>操作已保存，重新打开面板可继续查看。结果待核实时请勿重复支付。</p>}
      </div>}
      {record && !record.operation_id && <button type="button" disabled={busy || disabled} onClick={submit}>{busy ? '查询中…' : '继续查询本次升配'}</button>}
      {error && <p role="alert" className="cloud-upgrade-error">{error}</p>}
    </section>
  );
}
