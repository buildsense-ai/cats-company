import React, { useEffect, useId, useRef, useState } from 'react';
import { ChevronDown } from 'lucide-react';
import './plans/credit-commerce.css';
import { api } from '../api';
import { readStorageValue, writeStorageValue } from '../utils/storage-access';

const plans = { light: '轻量', standard: '标准', pro: '专业', heavy: '重载' };
const terminal = status => ['completed', 'failed'].includes(status);
const labels = { pending: '等待升配', running: '正在升配', reconciling: '正在核实机器配置', completed: '升配完成', failed: '升配未完成' };
const formatPoints = (units, scale = 1_000_000) => (Number(units || 0) / scale).toLocaleString('zh-CN', { maximumFractionDigits: 6 });
const planIds = Object.keys(plans);
const planLabel = id => plans[id] || id;
const formatDate = value => new Date(value).toLocaleString('zh-CN', { dateStyle: 'medium', timeStyle: 'short' });

function formatRemaining(seconds) {
  if (!Number.isFinite(seconds) || seconds <= 0) return '';
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor(seconds % 86400 / 3600);
  const minutes = Math.floor(seconds % 3600 / 60);
  return `${days ? `${days} 天 ` : ''}${hours ? `${hours} 小时 ` : ''}${minutes ? `${minutes} 分钟` : ''}`.trim() || '不到 1 分钟';
}

export default function CloudWorkerUpgrade({ worker, disabled = false }) {
  const storageKey = `catsco_machine_upgrade_v1:${worker.id || worker.uid}:${worker.tenant_name}`;
  const [record, setRecord] = useState(() => {
    try { return JSON.parse(readStorageValue(storageKey, 'sessionStorage') || 'null'); } catch { return null; }
  });
  const [expanded, setExpanded] = useState(() => Boolean(record));
  const panelId = useId();
  const [targetChoice, setTarget] = useState('standard');
  const [configuration, setConfiguration] = useState(null);
  const [configurationLoading, setConfigurationLoading] = useState(true);
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

  const currentPlan = configuration?.current_plan;
  const runtimePlan = operation?.status === 'completed' ? operation.to_plan : worker.runtime_plan || configuration?.runtime_plan;
  const targets = planIds.slice(1 + Math.max(0, planIds.indexOf(currentPlan), planIds.indexOf(runtimePlan)));
  const target = targets.includes(targetChoice) ? targetChoice : targets[0];

  const showOperation = (next) => {
    setOperation(next);
    if (next.status === 'completed' && next.to_plan) {
      setConfiguration({ current_plan: next.to_plan, upgrade_expires_at: next.worker_expires_at });
    }
  };

  useEffect(() => {
    const controller = new AbortController();
    async function loadConfiguration() {
      try {
        const result = await api.getCloudWorkerUpgradeConfiguration(worker.tenant_name, { signal: controller.signal, timeoutMs: 10_000 });
        if (!controller.signal.aborted && planIds.includes(result.current_plan)) {
          setConfiguration(current => current ?? result);
        }
      } catch {
        // A quote can still supply the current pricing tier if this read fails.
      } finally {
        if (!controller.signal.aborted) setConfigurationLoading(false);
      }
    }
    loadConfiguration();
    return () => controller.abort();
  }, [worker.tenant_name]);

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
        showOperation(next);
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
    if (busy || record || !target) return;
    setBusy(true); setError(''); setQuote(null);
    setOperation(current => current?.status === 'completed' ? current : null);
    try {
      const result = await api.getCloudWorkerUpgradeQuote(worker.tenant_name, target, { timeoutMs: 20_000 });
      if (planIds.includes(result.from_plan)) {
        setConfiguration(current => ({
          current_plan: result.from_plan,
          upgrade_expires_at: current?.current_plan === result.from_plan ? current.upgrade_expires_at : undefined,
        }));
      }
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
      showOperation(next); setQuote(null);
      saveRecord(terminal(next.status) ? null : { ...request, operation_id: next.operation_id });
    } catch (err) {
      const result = err.data;
      if (result?.operation_id && result?.status) {
        showOperation(result); setQuote(null);
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
  const remaining = formatRemaining(quote?.remaining_seconds);
  const canChooseTarget = !active && !configurationLoading;
  const runtimeExceedsPricing = planIds.includes(currentPlan) && planIds.indexOf(runtimePlan) > planIds.indexOf(currentPlan);
  return (
    <section className="cloud-upgrade-card" aria-label={`${worker.display_name || worker.tenant_name}机器升配`}>
      <div className="cloud-upgrade-heading">
        <div className="cloud-upgrade-configuration">
          <span>实际运行档位 <strong>{planLabel(runtimePlan) || '暂未读取'}</strong></span>
          <span>升配计价基准 <strong>{planLabel(currentPlan) || (configurationLoading ? '读取中…' : '暂未读取')}</strong></span>
          {configuration?.upgrade_expires_at && <span>已购升配至 <strong>{formatDate(configuration.upgrade_expires_at)}</strong></span>}
          {!expanded && operation && <span role="status">{labels[operation.status] || '正在查询升配状态'}</span>}
          {!expanded && record && !operation && <span role="status">待确认升配结果</span>}
        </div>
        <button type="button" className="cloud-upgrade-toggle" aria-expanded={expanded} aria-controls={panelId} onClick={() => setExpanded(value => !value)}>
          {expanded ? '收起' : active || operation ? '查看升配' : canChooseTarget && !target ? '升配规则' : '升配'}
          <ChevronDown size={14} aria-hidden="true" />
        </button>
      </div>
      <div id={panelId} className="cloud-upgrade-body" hidden={!expanded}>
        {expanded && <>
          {runtimeExceedsPricing && <p>机器已运行在{planLabel(runtimePlan)}档，只能选择更高档。报价按已购权益折算，目前从{planLabel(currentPlan)}档补差。</p>}
          {canChooseTarget && target && <div className="cloud-upgrade-select">
            <label>目标配置 <select value={target} disabled={disabled || busy} onChange={event => { setTarget(event.target.value); setQuote(null); setError(''); }}>
              {targets.map(id => <option key={id} value={id}>{plans[id]}</option>)}
            </select></label>
            <button type="button" disabled={disabled || busy} onClick={getQuote}>{busy ? '读取中…' : '查看升配报价'}</button>
          </div>}
          {configurationLoading && !active && <p role="status">正在读取机器配置…</p>}
          {canChooseTarget && !target && <p>已是最高档位。</p>}
          {quote && !active && <div className="cloud-upgrade-quote">
            <strong>升至 {planLabel(quote.to_plan)} · 一次性支付 {formatPoints(quote.cost_units, quote.units_per_credit)} 点</strong>
            <p>升配至 {formatDate(quote.worker_expires_at)}（机器原到期时间）{remaining && ` · 报价时剩余 ${remaining}`}</p>
            <p>确认后预留点数，升配成功才扣点。容量不足时可能迁移，期间云员工会短暂离线。</p>
            <p>可用 {formatPoints(quote.balance_units, quote.units_per_credit)} 点{!enough && ` · 还差 ${formatPoints(Number(quote.cost_units) - Number(quote.balance_units), quote.units_per_credit)} 点，请到「套餐与权益」充值后重新报价。`}</p>
            {quote.needs_migration && <p>当前容量不足，请联系支持。</p>}
            <button type="button" disabled={disabled || busy || expired || !enough || quote.needs_migration} onClick={submit}>
              {expired ? '报价已过期，请重新获取' : '确认并支付点数'}
            </button>
          </div>}
          {operation && <div role="status" className="cloud-upgrade-progress">
            <strong>{labels[operation.status] || '正在查询升配状态'}</strong>
            {operation.message && <p>{operation.message}</p>}
            {operation.status === 'completed' && <p>已结算 {formatPoints(operation.cost_units)} 点{operation.worker_expires_at && ` · 升配至 ${formatDate(operation.worker_expires_at)}`}。</p>}
            {!terminal(operation.status) && <p>操作已保存，重新打开面板可继续查看。结果待核实时请勿重复支付。</p>}
          </div>}
          {record && !record.operation_id && <button type="button" disabled={busy || disabled} onClick={submit}>{busy ? '查询中…' : '继续查询本次升配'}</button>}
          {error && <p role="alert" className="cloud-upgrade-error">{error}</p>}
          <p>按剩余机器租期补差价。已购升配到期后不自动续费或扣点。</p>
          <details className="cloud-upgrade-rules">
            <summary>计费与续租规则</summary>
            <p>目标档位须高于实际运行档位和已购档位。差价按已购权益计算，以 30 天档位差价为基准，按剩余秒数折算。例如剩 15 天就付一半。提前续租只延长机器使用期限，已购升配仍按原日期到期。再次升档时，已覆盖和未覆盖的时段分别补差。</p>
            <p>可用套餐月额度和充值点数付款。赠送点数和模型用量单独计算。升配明确失败会释放预留，结果待核实时保留预留。</p>
            <p>后续按续租权益计费，继续购买高档权益需重新报价。平台目前不自动缩减容器配置。</p>
          </details>
        </>}
      </div>
    </section>
  );
}
