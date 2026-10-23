import React, { useCallback, useEffect, useState } from 'react';
import { AlertTriangle, ChevronDown, Plus, RefreshCw, Sparkles, Undo2, X } from 'lucide-react';
import { api } from '../api';

// Agent 工具（CatsCo 定制 agent 的工具集管理；设计 §3.3-9"模式二：默认开放 + 个人清单"）。
// 添加/删除跟账号、跨设备生效：删除后从你的 Agent 工具集（tools/list）隐去、调用也被拒绝；
// 重新添加即恢复。变更即时同步在线 MCP 会话（服务端 list_changed）。

// 凭据时间只用于「最近使用」提示，解析失败就不显示。
const formatCredentialTime = (value) => {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return date.toLocaleString();
};

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
  // 收回分享：二次确认（避免误点）+ 逐行进行中状态 + 成功通知。
  const [unpublishTarget, setUnpublishTarget] = useState('');
  const [unpublishing, setUnpublishing] = useState(() => new Set());
  const [notice, setNotice] = useState(null);
  // 本机 worker 凭据（per-user）：只能领自己名下应用的 run，明文只显示一次。
  const [credentials, setCredentials] = useState([]);
  const [issued, setIssued] = useState(null);
  const [credentialBusy, setCredentialBusy] = useState(false);
  const [form, setForm] = useState({
    name: '',
    slug: '',
    slugTouched: false,
    // 默认公开：上架就是为了分享给别人调，选「仅自己」要显式选。
    visibility: 'public',
    runner: 'worker',
    url: '',
    tool: '',
    command: '',
    price: '',
  });

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [catalogData, botsData, credentialData] = await Promise.all([
        api.getServiceAgentsCatalog(),
        typeof api.getMyBots === 'function' ? api.getMyBots().catch(() => null) : Promise.resolve(null),
        typeof api.listServiceWorkerCredentials === 'function'
          ? api.listServiceWorkerCredentials().catch(() => null)
          : Promise.resolve(null),
      ]);
      setAgents(Array.isArray(catalogData?.agents) ? catalogData.agents : []);
      setCredentials(Array.isArray(credentialData?.credentials) ? credentialData.credentials : []);
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
    } else if (form.runner === 'mcp') {
      const url = form.url.trim();
      if (!/^https?:\/\//.test(url)) { setPublishError('选 MCP 服务时要填 http(s):// 地址'); return; }
      definition.runner = { type: 'mcp', url, timeout_seconds: 60 };
      const tool = form.tool.trim();
      if (tool) definition.runner.tool = tool;
    } else {
      definition.runner = { type: 'worker' };
      const command = form.command.trim();
      if (command) definition.runner.command = command;
    }
    const price = Number(form.price);
    if (form.price && Number.isFinite(price) && price > 0) {
      // 按次计价：用户只关心「调一次多少钱」，比按千 tokens 直观。
      definition.pricing = { unit: 'run', per_run: price, currency: 'CNY' };
    }
    setPublishing(true);
    setPublishError('');
    try {
      const result = await api.publishServiceAgent({ slug, name, visibility: form.visibility, definition });
      // 第一次发布「我的电脑」应用时顺手把连接密钥生成好：用户只需复制下面那一行命令，
      // 不用先去理解/寻找「凭据」。
      let workerToken = '';
      if (form.runner === 'worker' && credentials.length === 0
        && typeof api.createServiceWorkerCredential === 'function') {
        try {
          const credential = await api.createServiceWorkerCredential('发布时自动生成');
          workerToken = credential?.token || '';
        } catch {
          // 自动生成失败不影响发布；用户仍可在下面「让我的电脑接单」里手动生成。
        }
      }
      setPublishResult({
        slug: result?.slug || slug,
        visibility: result?.visibility || form.visibility,
        runner: form.runner,
        hasCommand: form.runner === 'worker' && Boolean(form.command.trim()),
        token: workerToken,
      });
      setShowPublish(false);
      setForm({ name: '', slug: '', slugTouched: false, visibility: 'public', runner: 'worker', url: '', tool: '', command: '', price: '' });
      await load();
    } catch (err) {
      setPublishError(err?.message || '发布失败，请稍后重试。');
    } finally {
      setPublishing(false);
    }
  }, [form, credentials, load]);

  // 收回分享：下架后目录与别人的 tools/list 立即不可见，已缓存工具名的客户端再调用
  // 会拿到 "该应用已被发布者收回"。重新发布同一调用名即可恢复。
  const submitUnpublish = useCallback(async (agent) => {
    const slug = agent?.slug;
    if (!slug || unpublishing.has(slug)) return;
    setUnpublishing((prev) => new Set(prev).add(slug));
    setError('');
    setNotice(null);
    try {
      await api.unpublishServiceAgent(slug);
      setAgents((prev) => prev.filter((item) => item.slug !== slug));
      setNotice({ slug, name: agent?.name || slug, alreadyGone: false });
    } catch (err) {
      if (err?.status === 404) {
        // 已经不在了（可能刚被收回）：按成功处理，顺手刷掉这一行。
        setAgents((prev) => prev.filter((item) => item.slug !== slug));
        setNotice({ slug, name: agent?.name || slug, alreadyGone: true });
      } else if (err?.status === 403) {
        setError('只有发布者本人能收回该应用。');
      } else {
        setError(err?.message || '收回失败，请稍后重试。');
      }
    } finally {
      setUnpublishing((prev) => {
        const next = new Set(prev);
        next.delete(slug);
        return next;
      });
      setUnpublishTarget('');
    }
  }, [unpublishing]);

  // 本机 worker 凭据：生成 → 一次性明文；撤销即时生效（下次 claim 就 401）。
  const createCredential = useCallback(async () => {
    if (credentialBusy) return;
    setCredentialBusy(true);
    setError('');
    try {
      const result = await api.createServiceWorkerCredential('本机 worker');
      setIssued({
        token: result?.token || '',
        prefix: result?.prefix || '',
        id: result?.id,
        createdAt: result?.created_at || '',
      });
      setCredentials((prev) => [
        {
          id: result?.id,
          prefix: result?.prefix || '',
          label: result?.label || '',
          created_at: result?.created_at || '',
        },
        ...prev.filter((item) => item.id !== result?.id),
      ]);
    } catch (err) {
      setError(err?.message || '生成凭据失败，请稍后重试。');
    } finally {
      setCredentialBusy(false);
    }
  }, [credentialBusy]);

  const revokeCredential = useCallback(async (credential) => {
    const id = credential?.id;
    if (!id || credentialBusy) return;
    setCredentialBusy(true);
    setError('');
    try {
      await api.revokeServiceWorkerCredential(id);
      setCredentials((prev) => prev.filter((item) => item.id !== id));
      setIssued((prev) => (prev?.id === id ? null : prev));
    } catch (err) {
      setError(err?.message || '撤销凭据失败，请稍后重试。');
    } finally {
      setCredentialBusy(false);
    }
  }, [credentialBusy]);

  const copyCredential = useCallback(async (token) => {
    try {
      await navigator.clipboard.writeText(token);
      setError('');
    } catch {
      setError('复制失败，请手动选中上面的凭据复制。');
    }
  }, []);

  // 发布成功后的「一行命令」：有密钥就直接给可粘贴的完整命令，
  // 老用户已有密钥时留占位（明文只在生成时显示一次）。
  const workerCommand = publishResult
    ? [
      'service-run-worker',
      `-agents ${publishResult.slug}`,
      `-token ${publishResult.token || '<你的连接密钥>'}`,
      publishResult.hasCommand ? '' : `-cmd '你的命令 {input_file}'`,
    ].filter(Boolean).join(' ')
    : '';

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
            <span className="v3-service-agents-field-hint">
              你的 bot 里就这样叫它：catsco_{form.slug || 'my-agent'}
            </span>
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
                我的电脑（推荐）
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
              <label>
                <input
                  type="radio"
                  name="runner"
                  checked={form.runner === 'mcp'}
                  onChange={() => updateForm({ runner: 'mcp' })}
                />
                MCP 服务
              </label>
            </div>
          </div>
          {form.runner === 'http' || form.runner === 'mcp' ? (
            <label className="v3-service-agents-field">
              <span>{form.runner === 'mcp' ? 'MCP 服务地址' : '服务地址'}</span>
              <input
                value={form.url}
                onChange={(event) => updateForm({ url: event.target.value })}
                placeholder={form.runner === 'mcp' ? 'https://your-host/mcp' : 'https://your-host/run'}
                spellCheck={false}
              />
              {form.runner === 'mcp' ? (
                <span className="v3-service-agents-field-hint">
                  平台会当 MCP 客户端连它并调用工具，不需要你自己的 worker
                </span>
              ) : null}
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
                留空也行：不填就用启动 worker 时的 -cmd；多个 agent 各填各的即可
              </span>
            </label>
          )}
          {form.runner === 'mcp' ? (
            <label className="v3-service-agents-field">
              <span>工具名（留空=对端只有一个工具时自动选）</span>
              <input
                value={form.tool}
                onChange={(event) => updateForm({ tool: event.target.value })}
                placeholder="run"
                spellCheck={false}
              />
            </label>
          ) : null}
          <label className="v3-service-agents-field">
            <span>每次调用多少钱（元，留空=免费）</span>
            <input
              value={form.price}
              onChange={(event) => updateForm({ price: event.target.value })}
              placeholder="0.1"
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
              <div>让你这台电脑接单：复制下面这行，在你电脑上执行一次就行。</div>
              <code title={workerCommand}>{workerCommand}</code>
              <button type="button" onClick={() => copyCredential(workerCommand)}>复制命令</button>
            </div>
          ) : null}
          <div className="v3-service-agents-published-hint">
            之后直接跟 bot 说「用 catsco_{publishResult.slug} 帮我…」就能用。
          </div>
          <button type="button" onClick={() => setPublishResult(null)}>知道了</button>
        </div>
      ) : null}
      {notice ? (
        <div className="v3-service-agents-notice" role="status">
          <div className="v3-service-agents-notice-body">
            <div className="v3-service-agents-notice-title">
              已收回 <code title={`catsco_${notice.slug}`}>catsco_{notice.slug}</code>
            </div>
            <div className="v3-service-agents-notice-hint">
              {notice.alreadyGone
                ? '该应用此前已不在目录中。'
                : '别人的工具列表里会立即消失，旧名字再调用会失败（unknown tool）。'}
              重新发布同一调用名即可恢复分享。
            </div>
          </div>
          <button type="button" onClick={() => setNotice(null)}>知道了</button>
        </div>
      ) : null}
      <div className="v3-service-worker-credentials">
        <div className="v3-service-worker-credentials-head">
          <span>让我的电脑接单</span>
          <button type="button" onClick={createCredential} disabled={credentialBusy}>
            {credentialBusy ? '处理中…' : '生成'}
          </button>
        </div>
        {issued ? (
          <div className="v3-service-worker-credentials-issued" role="status">
            <div className="v3-service-worker-credentials-issued-title">
              已生成，<strong>只显示这一次</strong>，请立即复制：
            </div>
            <code title={issued.token}>{issued.token}</code>
            <div className="v3-service-worker-credentials-issued-actions">
              <button type="button" onClick={() => copyCredential(issued.token)}>复制</button>
              <button type="button" onClick={() => setIssued(null)}>我已保存</button>
            </div>
          </div>
        ) : null}
        {credentials.length === 0 ? (
          <p className="v3-service-worker-credentials-empty">
            还没有「连接密钥」。点「生成」拿一把，粘到你自己电脑上的 worker 命令里就行；
            它<strong>只能替你的应用接单</strong>，随时可撤销。
          </p>
        ) : (
          <ul className="v3-service-worker-credentials-list">
            {credentials.map((credential) => (
              <li key={credential.id}>
                <code title={credential.prefix}>{credential.prefix}…</code>
                <span className="v3-service-worker-credentials-meta">
                  {credential.last_used_at ? `最近使用 ${formatCredentialTime(credential.last_used_at)}` : '尚未使用'}
                </span>
                <button type="button" onClick={() => revokeCredential(credential)} disabled={credentialBusy}>
                  撤销
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
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
            点右上角 ＋ 把你电脑上的 agent 上架，它就会以 <code>catsco_&lt;名字&gt;</code>
            出现，你的 bot 直接就能调用。
          </p>
          <button
            type="button"
            className="v3-service-agents-placeholder-action"
            onClick={() => { setShowPublish(true); setPublishError(''); }}
          >
            <Plus size={14} />
            发布我的 agent
          </button>
          <button type="button" className="v3-service-agents-placeholder-action" onClick={load} disabled={loading}>
            <RefreshCw size={14} />
            刷新目录
          </button>
        </div>
      ) : (
        <ul className="v3-service-agents-list">
          {agents.map((agent) => {
            const busy = pending.has(agent.slug)
              || unpublishing.has(agent.slug)
              || (scopeBotUid && scopeLoading);
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
                <div className="v3-service-agent-actions">
                  {agent.mine ? (
                    unpublishTarget === agent.slug ? (
                      <>
                        <button
                          type="button"
                          className="v3-service-agent-unpublish is-confirm"
                          disabled={unpublishing.has(agent.slug)}
                          onClick={() => submitUnpublish(agent)}
                          title="确认从目录下架该应用"
                        >
                          {unpublishing.has(agent.slug) ? '收回中…' : '确认收回'}
                        </button>
                        <button
                          type="button"
                          className="v3-service-agent-unpublish"
                          disabled={unpublishing.has(agent.slug)}
                          onClick={() => setUnpublishTarget('')}
                        >
                          取消
                        </button>
                      </>
                    ) : (
                      <button
                        type="button"
                        className="v3-service-agent-unpublish"
                        disabled={busy}
                        onClick={() => setUnpublishTarget(agent.slug)}
                        aria-label={`收回${agent.name || agent.slug}的分享`}
                        title="收回分享：下架后别人就调不到了（重新发布同一调用名即可恢复）"
                      >
                        <Undo2 size={15} />
                        <span>收回</span>
                      </button>
                    )
                  ) : null}
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
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </aside>
  );
}

export { ServiceAgentsPanel };
