import React, { useCallback, useEffect, useState } from 'react';
import { AlertTriangle, ChevronDown, Plus, RefreshCw, Sparkles, X } from 'lucide-react';
import { api } from '../api';

// Agent 工具（CatsCo 定制 agent 的工具集管理；设计 §3.3-9"模式二：默认开放 + 个人清单"）。
// 添加/删除跟账号、跨设备生效：删除后从你的 Agent 工具集（tools/list）隐去、调用也被拒绝；
// 重新添加即恢复。变更即时同步在线 MCP 会话（服务端 list_changed）。
export default function ServiceAgentsPanel({ onClose }) {
  const [agents, setAgents] = useState([]);
  const [bots, setBots] = useState([]);
  const [scopeBotUid, setScopeBotUid] = useState('');
  const [botDefinition, setBotDefinition] = useState(null);
  const [scopeLoading, setScopeLoading] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(() => new Set());
  // 发布表单（把自己的 agent 上架成 catsco_<slug> 工具）
  const [showPublish, setShowPublish] = useState(false);
  const [publishing, setPublishing] = useState(false);
  const [publishError, setPublishError] = useState('');
  const [publishResult, setPublishResult] = useState(null);
  const [form, setForm] = useState({
    name: '',
    slug: '',
    slugTouched: false,
    visibility: 'private',
    runner: 'worker',
    url: '',
    command: '',
    price: '',
  });

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [catalogData, botsData] = await Promise.all([
        api.getServiceAgentsCatalog(),
        typeof api.getMyBots === 'function' ? api.getMyBots().catch(() => null) : Promise.resolve(null),
      ]);
      setAgents(Array.isArray(catalogData?.agents) ? catalogData.agents : []);
      const rawBots = botsData?.bots ?? botsData?.agents ?? botsData;
      setBots(Array.isArray(rawBots) ? rawBots : []);
    } catch (err) {
      setError(err?.message || 'Agent 工具加载失败，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // 切换到某个 Agent：读取它的 definition（serviceAgents 清单）。
  useEffect(() => {
    if (!scopeBotUid) {
      setBotDefinition(null);
      return undefined;
    }
    let cancelled = false;
    (async () => {
      setScopeLoading(true);
      setError('');
      try {
        const data = await api.getBotDefinitionPrompt(scopeBotUid);
        if (cancelled) return;
        const rawAgents = data?.definition?.serviceAgents;
        setBotDefinition({
          revision: Number(data?.revision ?? 0),
          serviceAgents: Array.isArray(rawAgents) ? rawAgents : undefined,
        });
      } catch (err) {
        if (cancelled) return;
        setBotDefinition(null);
        setError(err?.message || '读取该 Agent 的工具清单失败，请稍后重试。');
      } finally {
        if (!cancelled) setScopeLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [scopeBotUid]);

  const toggleSubscription = useCallback(async (agent) => {
    const slug = agent?.slug;
    if (!slug || pending.has(slug)) return;
    const nextHidden = !agent.hidden;
    setPending((prev) => new Set(prev).add(slug));
    setError('');
    try {
      const result = nextHidden
        ? await api.removeServiceAgentSubscription(slug)
        : await api.addServiceAgentSubscription(slug);
      const hidden = Boolean(result?.hidden);
      setAgents((prev) => prev.map((item) => (item.slug === slug ? { ...item, hidden } : item)));
    } catch (err) {
      setError(err?.message || (nextHidden ? '删除失败，请稍后重试。' : '添加失败，请稍后重试。'));
    } finally {
      setPending((prev) => {
        const next = new Set(prev);
        next.delete(slug);
        return next;
      });
    }
  }, [pending]);

  // 指定 Agent 作用域：把该 slug 加入/移出 bot definition.serviceAgents 并保存（带 revision）。
  const toggleBotAgent = useCallback(async (agent) => {
    const slug = agent?.slug;
    if (!slug || !scopeBotUid || !botDefinition || pending.has(slug)) return;
    const accountVisible = agents.filter((item) => !item.hidden).map((item) => item.slug);
    const current = botDefinition.serviceAgents === undefined ? accountVisible : botDefinition.serviceAgents;
    const next = current.includes(slug) ? current.filter((item) => item !== slug) : [...current, slug];
    setPending((prev) => new Set(prev).add(slug));
    setError('');
    try {
      const result = await api.updateBotDefinitionServiceAgents(scopeBotUid, botDefinition.revision, next);
      const rawAgents = result?.definition?.serviceAgents;
      setBotDefinition({
        revision: Number(result?.revision ?? botDefinition.revision + 1),
        serviceAgents: Array.isArray(rawAgents) ? rawAgents : next,
      });
    } catch (err) {
      setError(err?.message || '保存该 Agent 的工具清单失败，请稍后重试。');
    } finally {
      setPending((prev) => {
        const nextPending = new Set(prev);
        nextPending.delete(slug);
        return nextPending;
      });
    }
  }, [agents, botDefinition, pending, scopeBotUid]);

  // 发布（上架自己的 agent）：名称 → 调用名可自动生成，也可手改。
  const slugify = (value) => String(value || '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64);

  const updateForm = useCallback((patch) => {
    setForm((prev) => {
      const next = { ...prev, ...patch };
      if (patch.name !== undefined && !prev.slugTouched) {
        next.slug = slugify(patch.name);
      }
      if (patch.slug !== undefined) next.slugTouched = true;
      return next;
    });
  }, []);

  const submitPublish = useCallback(async () => {
    const name = form.name.trim();
    const slug = form.slug.trim();
    if (!name) { setPublishError('请填写应用名称'); return; }
    if (!/^[a-z0-9][a-z0-9-]{1,63}$/.test(slug)) {
      setPublishError('调用名只能用小写字母/数字/中划线，2~64 位');
      return;
    }
    const definition = { name };
    if (form.runner === 'http') {
      const url = form.url.trim();
      if (!/^https?:\/\//.test(url)) { setPublishError('选 HTTP 执行时要填 http(s):// 地址'); return; }
      definition.runner = { type: 'http', url, timeout_seconds: 60 };
    } else {
      definition.runner = { type: 'worker' };
      const command = form.command.trim();
      if (command) definition.runner.command = command;
    }
    const price = Number(form.price);
    if (form.price && Number.isFinite(price) && price > 0) {
      definition.pricing = { unit: 'token', per_1k_tokens: price, currency: 'CNY' };
    }
    setPublishing(true);
    setPublishError('');
    try {
      const result = await api.publishServiceAgent({ slug, name, visibility: form.visibility, definition });
      setPublishResult({
        slug: result?.slug || slug,
        visibility: result?.visibility || form.visibility,
        runner: form.runner,
        hasCommand: form.runner === 'worker' && Boolean(form.command.trim()),
      });
      setShowPublish(false);
      setForm({ name: '', slug: '', slugTouched: false, visibility: 'private', runner: 'worker', url: '', command: '', price: '' });
      await load();
    } catch (err) {
      setPublishError(err?.message || '发布失败，请稍后重试。');
    } finally {
      setPublishing(false);
    }
  }, [form, load]);

  return (
    <aside className="v3-service-agents-panel" aria-label="Agent 工具">      <div className="v3-service-agents-header">
        <span className="v3-service-agents-title">
          Agent 工具
          {!loading && agents.length > 0 ? (
            <span className="v3-service-agents-count" aria-label={`共 ${agents.length} 个工具`}>
              {agents.length}
            </span>
          ) : null}
        </span>
        <div className="v3-service-agents-header-actions">
          <button
            type="button"
            onClick={() => {
              setShowPublish((prev) => !prev);
              setPublishError('');
            }}
            aria-label="发布应用"
            title="发布应用（把自己的 agent 上架成工具）"
            className={showPublish ? 'is-active' : undefined}
          >
            <Plus size={16} />
          </button>
          <button
            type="button"
            onClick={load}
            aria-label="刷新 Agent 工具"
            title="刷新"
            className={loading ? 'is-spinning' : undefined}
          >
            <RefreshCw size={16} />
          </button>
          <button type="button" onClick={onClose} aria-label="关闭 Agent 工具" title="关闭">
            <X size={16} />
          </button>
        </div>
      </div>
      {showPublish ? (
        <form
          className="v3-service-agents-publish"
          onSubmit={(event) => {
            event.preventDefault();
            if (!publishing) submitPublish();
          }}
        >
          <div className="v3-service-agents-publish-title">发布应用</div>
          <label className="v3-service-agents-field">
            <span>名称</span>
            <input
              value={form.name}
              onChange={(event) => updateForm({ name: event.target.value })}
              placeholder="我的助手"
              autoFocus
            />
          </label>
          <label className="v3-service-agents-field">
            <span>调用名（工具名 catsco_&lt;调用名&gt;）</span>
            <input
              value={form.slug}
              onChange={(event) => updateForm({ slug: event.target.value })}
              placeholder="my-agent"
              spellCheck={false}
            />
          </label>
          <div className="v3-service-agents-field">
            <span>可见性</span>
            <div className="v3-service-agents-choices">
              <label>
                <input
                  type="radio"
                  name="visibility"
                  checked={form.visibility === 'private'}
                  onChange={() => updateForm({ visibility: 'private' })}
                />
                仅自己
              </label>
              <label>
                <input
                  type="radio"
                  name="visibility"
                  checked={form.visibility === 'public'}
                  onChange={() => updateForm({ visibility: 'public' })}
                />
                公开（别人也能调用）
              </label>
            </div>
          </div>
          <div className="v3-service-agents-field">
            <span>执行位置</span>
            <div className="v3-service-agents-choices">
              <label>
                <input
                  type="radio"
                  name="runner"
                  checked={form.runner === 'worker'}
                  onChange={() => updateForm({ runner: 'worker' })}
                />
                我的电脑（worker）
              </label>
              <label>
                <input
                  type="radio"
                  name="runner"
                  checked={form.runner === 'http'}
                  onChange={() => updateForm({ runner: 'http' })}
                />
                HTTP 服务
              </label>
            </div>
          </div>
          {form.runner === 'http' ? (
            <label className="v3-service-agents-field">
              <span>服务地址</span>
              <input
                value={form.url}
                onChange={(event) => updateForm({ url: event.target.value })}
                placeholder="https://your-host/run"
                spellCheck={false}
              />
            </label>
          ) : (
            <label className="v3-service-agents-field">
              <span>本机执行命令（这个应用用哪个 agent 跑）</span>
              <input
                value={form.command}
                onChange={(event) => updateForm({ command: event.target.value })}
                placeholder="claude -p {input_file}"
                spellCheck={false}
              />
              <span className="v3-service-agents-field-hint">
                留空 = 用 worker 的 -cmd；多个 agent 各填各的即可，一个 worker 能同时服务它们
              </span>
            </label>
          )}
          <label className="v3-service-agents-field">
            <span>定价（元 / 千 tokens，留空=免费）</span>
            <input
              value={form.price}
              onChange={(event) => updateForm({ price: event.target.value })}
              placeholder="2"
              inputMode="decimal"
            />
          </label>
          {publishError ? <div className="v3-service-agents-error" role="alert">{publishError}</div> : null}
          <div className="v3-service-agents-publish-actions">
            <button type="submit" className="is-primary" disabled={publishing}>
              {publishing ? '发布中…' : '发布'}
            </button>
            <button
              type="button"
              onClick={() => {
                setShowPublish(false);
                setPublishError('');
              }}
              disabled={publishing}
            >
              取消
            </button>
          </div>
        </form>
      ) : null}
      {publishResult ? (
        <div className="v3-service-agents-published" role="status">
          <div>
            已发布 <code>catsco_{publishResult.slug}</code>
            {publishResult.visibility === 'public' ? ' · 公开，别人也能调用' : ' · 仅自己可用'}
          </div>
          {publishResult.runner === 'worker' ? (
            <div className="v3-service-agents-published-hint">
              在你自己的机器上运行 worker 才会执行：
              <code>
                service-run-worker -agents {publishResult.slug}
                {publishResult.hasCommand ? '' : ` -cmd '…'`}
              </code>
            </div>
          ) : null}
          <button type="button" onClick={() => setPublishResult(null)}>知道了</button>
        </div>
      ) : null}
      <div className="v3-service-agents-scope">
        <label className="v3-service-agents-scope-label" htmlFor="v3-service-agents-scope-select">
          配置范围
        </label>
        {bots.length > 0 ? (
          <div className="v3-service-agents-scope-control">
            <select
              id="v3-service-agents-scope-select"
              value={scopeBotUid}
              onChange={(event) => setScopeBotUid(event.target.value)}
            >
              <option value="">账号（所有 Agent 共用）</option>
              {bots.map((bot) => (
                <option key={bot.uid} value={String(bot.uid)}>
                  {bot.name || bot.display_name || bot.username || bot.uid}
                </option>
              ))}
            </select>
            <ChevronDown size={14} aria-hidden="true" className="v3-service-agents-scope-chevron" />
          </div>
        ) : (
          // 没有任何 Agent 时下拉只有一个选项，纯属噪音：降级成静态说明。
          <div
            className="v3-service-agents-scope-control is-static"
            title="创建 Agent 之后可以给单个 Agent 单独裁剪工具清单"
          >
            <span className="v3-service-agents-scope-value">账号（所有 Agent 共用）</span>
          </div>
        )}
      </div>
      <p className="v3-service-agents-hint">
        {scopeBotUid
          ? '为这个 Agent 配置可调用的工具；未配置过时默认全部可见，保存后按清单裁剪（只影响该 Agent）。'
          : 'CatsCo 定制 Agent（以工具形式接入）。添加的会出现在你的账号工具集中；删除的将被移除，调用也会被拒绝。'}
      </p>
      {scopeBotUid && scopeLoading ? <div className="v3-service-agents-empty">读取该 Agent 的工具清单…</div> : null}
      {error && (
        <div className="v3-service-agents-error" role="alert">
          <AlertTriangle size={14} aria-hidden="true" />
          <span>{error}</span>
          <button type="button" onClick={load} disabled={loading}>
            重试
          </button>
        </div>
      )}
      {loading ? (
        <ul className="v3-service-agents-skeleton" aria-busy="true" aria-label="Agent 工具加载中">
          {[0, 1, 2].map((index) => (
            <li key={index} className="v3-service-agents-skeleton-row">
              <span className="v3-service-agents-skeleton-avatar" />
              <span className="v3-service-agents-skeleton-lines">
                <span className="v3-service-agents-skeleton-line is-wide" />
                <span className="v3-service-agents-skeleton-line" />
              </span>
            </li>
          ))}
        </ul>
      ) : agents.length === 0 ? (
        <div className="v3-service-agents-placeholder" role="status">
          <span className="v3-service-agents-placeholder-icon" aria-hidden="true">
            <Sparkles size={22} />
          </span>
          <p className="v3-service-agents-placeholder-title">还没有可用的 Agent 工具</p>
          <p className="v3-service-agents-placeholder-text">
            上架后的定制 Agent 会以 <code>catsco_&lt;slug&gt;</code> 的形式出现在这里；
            当前目录里还没有对你可见的应用。
          </p>
          <button type="button" className="v3-service-agents-placeholder-action" onClick={load} disabled={loading}>
            <RefreshCw size={14} />
            刷新目录
          </button>
        </div>
      ) : (
        <ul className="v3-service-agents-list">
          {agents.map((agent) => {
            const busy = pending.has(agent.slug) || (scopeBotUid && scopeLoading);
            const accountHidden = Boolean(agent.hidden);
            const selected = scopeBotUid
              ? (accountHidden
                ? false
                : botDefinition?.serviceAgents === undefined
                  ? true
                  : botDefinition.serviceAgents.includes(agent.slug))
              : !accountHidden;
            const hidden = !selected;
            const toggle = scopeBotUid ? toggleBotAgent : toggleSubscription;
            return (
              <li key={agent.slug} className={`v3-service-agent-row${hidden ? ' is-hidden' : ''}`}>
                <div className="v3-service-agent-info">
                  <div className="v3-service-agent-name">
                    {agent.name || agent.slug}
                    <span className="v3-service-agent-version">v{agent.version}</span>
                    {agent.mine ? <span className="v3-service-agent-badge">我的</span> : null}
                    {agent.visibility === 'private' ? (
                      <span className="v3-service-agent-badge is-private">私有</span>
                    ) : null}
                  </div>
                  <div className="v3-service-agent-slug">catsco_{agent.slug}</div>
                  {scopeBotUid && accountHidden ? (
                    <div className="v3-service-agent-note">账号清单中已移除</div>
                  ) : null}
                </div>
                <button
                  type="button"
                  className={`v3-service-agent-toggle${hidden ? ' is-add' : ' is-remove'}`}
                  disabled={busy || (scopeBotUid && accountHidden)}
                  onClick={() => toggle(agent)}
                  aria-label={`${hidden ? '添加' : '删除'}${agent.name || agent.slug}`}
                  title={
                    scopeBotUid && accountHidden
                      ? '先在账号清单中添加该工具'
                      : hidden
                        ? (scopeBotUid ? '添加到该 Agent 的工具清单' : '重新添加该工具')
                        : (scopeBotUid ? '从该 Agent 的工具清单移除' : '从我的清单中删除该工具')
                  }
                >
                  {hidden ? <Plus size={15} /> : <X size={15} />}
                  <span>{hidden ? '添加' : '删除'}</span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </aside>
  );
}

export { ServiceAgentsPanel };
