import React, { useCallback, useEffect, useState } from 'react';
import { AlertTriangle, ChevronDown, Pencil, Plus, RefreshCw, Sparkles, Undo2, X } from 'lucide-react';
import { api } from '../api';

// Agent 工具（CatsCo 定制 agent 的工具集管理；设计 §3.3-9"模式二：默认开放 + 个人清单"）。
// 添加/删除跟账号、跨设备生效：删除后从你的 Agent 工具集（tools/list）隐去、调用也被拒绝；
// 重新添加即恢复。变更即时同步在线 MCP 会话（服务端 list_changed）。
//
// 这里刻意不出现任何「密钥 / token / 命令」：本机接单由桌面端自己完成，
// 用户只需要登录那台电脑。凭证对普通用户是不可理解的负担，不该出现在界面上。

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
  // 编辑已有应用：发布是全量覆盖，所以必须带着原 definition 合并，
  // 否则只改描述会把执行方式/价格抹掉（应用当场跑不起来）。
  const [editingSlug, setEditingSlug] = useState('');
  const [editingDefinition, setEditingDefinition] = useState(null);
  // 旧应用可能还是「平台直连 http/mcp」。面板不再提供这种接入方式，
  // 但编辑时绝不能把它改写成 worker —— 那会让应用当场跑不起来。
  const [lockedRunner, setLockedRunner] = useState(null);
  // 收回分享：二次确认（避免误点）+ 逐行进行中状态 + 成功通知。
  const [unpublishTarget, setUnpublishTarget] = useState('');
  const [unpublishing, setUnpublishing] = useState(() => new Set());
  // 自述建议：应用自己建议的对外描述，等人确认。丢弃是逐行进行中。
  const [dismissingIntro, setDismissingIntro] = useState(() => new Set());
  const [notice, setNotice] = useState(null);
  // 目录搜索：应用一多（同事分享的 + 自己的）就靠名称/描述找。纯前端过滤，不发请求。
  const [query, setQuery] = useState('');
  const [form, setForm] = useState({
    // 分享哪个 bot：被分享出去的是 bot 本身。
    //
    // 为什么没有「填一条命令」这个选项：调教好的 bot 就是执行者。
    // 让用户去写 `claude -p {input_file}` 这种模板，等于把工程细节当产品语言 ——
    // 而且大部分人根本不知道自己的「命令」是什么。
    botUid: '',
    name: '',
    // 工具描述：调用你的是另一个 Agent，它看不到包内容也没有你的对话上下文，
    // 只能靠这段文字判断「要不要用、怎么传参」。公开应用必须写。
    description: '',
    slug: '',
    slugTouched: false,
    // 默认公开：上架就是为了分享给别人调，选「仅自己」要显式选。
    visibility: 'public',
    price: '',
    priceUnit: 'token',
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
    const description = form.description.trim();
    if (!name) { setPublishError('请填写应用名称'); return; }
    if ((form.visibility === 'public' || form.visibility === 'friends') && !description) {
      // 分享给别人（公开或好友）却不写描述：别人（另一个 Agent）只看到名字，
      // 等于不知道能不能用。「仅自己」不强制 —— 写不写都不影响别人。
      setPublishError('分享给别人的应用必须写「能做什么」：调用你的是另一个 Agent，它只能靠这段文字判断要不要用');
      return;
    }
    if (!/^[a-z0-9][a-z0-9-]{1,63}$/.test(slug)) {
      setPublishError('调用名只能用小写字母/数字/中划线，2~64 位');
      return;
    }
    if (!lockedRunner && !Number(form.botUid)) {
      // 没选 bot 就没有执行方，发布了也调不通。
      setPublishError('请选择要分享的 bot');
      return;
    }
    const definition = editingDefinition && typeof editingDefinition === 'object'
      ? { ...editingDefinition } : {};
    definition.name = name;
    if (description) definition.description = description;
    else delete definition.description;
    if (lockedRunner) {
      // 老应用的执行方式面板不再提供（http/mcp/worker）：原样保留。
      // 默默改成 bot 会让它当场跑不起来 —— 原有的执行方根本没变。
      definition.runner = lockedRunner;
    } else {
      // 分享 bot：执行权交给它自己（它有自己的判断力与所在机器的能力）。
      // 不写 description 时后端会自动用 bot 自己的自我介绍兜底，
      // 但表单这里已经预填了，人看过再发更稳。
      definition.runner = { type: 'bot', bot_uid: Number(form.botUid) };
    }
    const price = Number(form.price);
    if (form.price && Number.isFinite(price) && price > 0) {
      // 不按调用次数收费：次数和实际开销无关（一次可能只是打个招呼，也可能跑十分钟）。
      // 只允许「按实际用量」计价：token 用量，或平台可观测的运行时长。
      definition.pricing = form.priceUnit === 'second'
        ? { unit: 'second', per_second: price, currency: 'CNY' }
        : { unit: 'token', per_1k_tokens: price, currency: 'CNY' };
    } else {
      delete definition.pricing;
    }
    setPublishing(true);
    setPublishError('');
    try {
      const result = await api.publishServiceAgent({ slug, name, visibility: form.visibility, definition });
      setPublishResult({
        slug: result?.slug || slug,
        visibility: result?.visibility || form.visibility,
        runner: lockedRunner ? lockedRunner.type : 'bot',
      });
      setShowPublish(false);
      setEditingSlug('');
      setEditingDefinition(null);
      setLockedRunner(null);
      setForm({ name: '', description: '', slug: '', slugTouched: false, visibility: 'public', price: '', priceUnit: 'token', botUid: '' });
      await load();
    } catch (err) {
      setPublishError(err?.message || '发布失败，请稍后重试。');
    } finally {
      setPublishing(false);
    }
  }, [editingDefinition, form, load, lockedRunner]);

  // 打开编辑：把已发布的 definition 回填进表单。
  // 目录只对 owner 本人返回 definition（别人的执行命令拿不到也用不上）。
  const startEdit = useCallback((agent, options = {}) => {
    const definition = agent?.definition && typeof agent.definition === 'object' ? agent.definition : {};
    const runner = definition.runner && typeof definition.runner === 'object' ? definition.runner : {};
    const pricing = definition.pricing && typeof definition.pricing === 'object' ? definition.pricing : {};
    // 老应用的执行方式面板不再提供（http / mcp / worker）：只锁住原配置，
    // 防止保存时被改成 bot —— 那会让应用当场跑不起来。
    const locked = runner.type === 'http' || runner.type === 'mcp' || runner.type === 'worker'
      ? runner : null;
    // bot 型（分享 bot）：回填目标 bot，编辑时也能看到自己在分享哪一个。
    const shareBot = runner.type === 'bot' && Number(runner.bot_uid) > 0;
    const priceUnit = pricing.unit === 'second' ? 'second' : 'token';
    const priceValue = priceUnit === 'second' ? pricing.per_second : pricing.per_1k_tokens;
    // 「采纳自述」不走独立写入路径：把建议填进表单，由人确认后照常发布。
    const suggested = options.useSelfIntro ? (agent?.self_intro?.description || '') : '';
    setEditingSlug(agent.slug);
    setEditingDefinition(definition);
    setLockedRunner(locked);
    setPublishError('');
    setPublishResult(null);
    setForm({
      name: agent.name || agent.slug,
      description: suggested || agent.description || '',
      slug: agent.slug,
      slugTouched: true,
      visibility: agent.visibility === 'friends'
        ? 'friends'
        : (agent.visibility === 'private' ? 'private' : 'public'),
      price: typeof priceValue === 'number' && priceValue > 0 ? String(priceValue) : '',
      priceUnit,
      botUid: shareBot ? String(runner.bot_uid) : '',
    });
    setShowPublish(true);
  }, []);

  // 丢弃应用建议的自述（不想采纳）。
  const dismissSelfIntro = useCallback(async (agent) => {
    const slug = agent?.slug;
    if (!slug) return;
    setDismissingIntro((prev) => new Set(prev).add(slug));
    setError('');
    try {
      await api.dismissServiceAgentSelfIntro(slug);
      setAgents((prev) => prev.map((item) => (
        item.slug === slug ? { ...item, self_intro: undefined } : item
      )));
    } catch (err) {
      setError(err?.message || '忽略失败，请稍后重试。');
    } finally {
      setDismissingIntro((prev) => {
        const next = new Set(prev);
        next.delete(slug);
        return next;
      });
    }
  }, []);

  const resetPublishForm = useCallback(() => {
    setEditingSlug('');
    setEditingDefinition(null);
    setLockedRunner(null);
    setPublishError('');
    setForm({ name: '', description: '', slug: '', slugTouched: false, visibility: 'public', price: '', priceUnit: 'token', botUid: '' });
  }, []);

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

  // 复制工具名：用户接下来要跟 bot 说「用 catsco_xxx 帮我…」，名字比描述有用。
  const copyText = useCallback(async (text) => {
    try {
      await navigator.clipboard.writeText(text);
      setError('');
    } catch {
      setError('复制失败，请手动选中复制。');
    }
  }, []);

  // 搜索：名称 / 调用名 / 描述任一命中即保留（描述里往往写着使用场景，最常被拿来搜）。
  const keyword = query.trim().toLowerCase();
  const visibleAgents = keyword
    ? agents.filter((agent) => [agent.name, agent.slug, agent.description]
      .filter(Boolean)
      .some((field) => String(field).toLowerCase().includes(keyword)))
    : agents;

  // 「谁在干」：执行方类型决定别人的期待（要不要等、离线会不会失败、能不能动本机）。
  const runnerLabelOf = (agent) => {
    switch (agent?.runner_kind) {
      case 'bot':
        return '另一个 bot 在处理';
      case 'worker':
        return '在作者自己的电脑上跑';
      case 'http':
      case 'mcp':
        return '平台直连作者的服务';
      default:
        return '';
    }
  };

  // 「要传什么」：把作者声明的 schema 压成一行，别再让调用方从散文里猜字段名。
  // 只列字段名 + 必填标记，描述放 title —— 列表里空间宝贵。
  const inputSummaryOf = (agent) => {
    const schema = agent?.input_schema;
    const props = schema && typeof schema === 'object' ? schema.properties : null;
    if (!props || typeof props !== 'object') return '';
    const names = Object.keys(props);
    if (names.length === 0) return '';
    const required = Array.isArray(schema.required) ? schema.required : [];
    const shown = names.slice(0, 3).map((name) => (required.includes(name) ? `${name}*` : name));
    const more = names.length > 3 ? ` 等 ${names.length} 个` : '';
    return `要传：${shown.join('、')}${more}`;
  };

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
              const next = !showPublish;
              setShowPublish(next);
              setPublishError('');
              if (next && !editingSlug) setPublishResult(null);
              if (!next) resetPublishForm();
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
          <div className="v3-service-agents-publish-title">{editingSlug ? '编辑应用' : '发布应用'}</div>
          {lockedRunner ? null : (
            <label className="v3-service-agents-field">
              <span>分享哪个 bot</span>
              <select
                value={form.botUid}
                onChange={(event) => {
                  const uid = event.target.value;
                  const picked = bots.find((bot) => String(bot.uid ?? bot.user_id ?? bot.id) === uid);
                  updateForm({
                    botUid: uid,
                    // 用 bot 自己的介绍预填：标签就该是它自己的话，人看过再发。
                    description: form.description || String(picked?.description || ''),
                    name: form.name || String(picked?.display_name || picked?.name || ''),
                  });
                }}
              >
                <option value="">— 选一个 —</option>
                {bots.map((bot) => {
                  const uid = String(bot.uid ?? bot.user_id ?? bot.id);
                  return (
                    <option key={uid} value={uid}>
                      {bot.display_name || bot.name || bot.username || `bot ${uid}`}
                    </option>
                  );
                })}
              </select>
              <span className="v3-service-agents-field-hint">
                {bots.length === 0
                  ? '你还没有 bot。先在「我的机器人」里创建一个 —— 你调教好的 bot 就是能接活的那个。'
                  : '别人调的是这个 bot 本身：它用自己的判断力干活。它在线（开着）时才接活，不在线会立刻报「不在线」。'}
              </span>
            </label>
          )}
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
            <span>能做什么（公开应用必填）</span>
            <textarea
              value={form.description}
              onChange={(event) => updateForm({ description: event.target.value })}
              placeholder="例：把中文技术文档润色成对外可发布的英文稿，保留代码块与专有名词。适合发布前的文稿；不处理图片。input.text 是要处理的正文。"
              rows={4}
            />
            <span className="v3-service-agents-field-hint">
              调用你的是另一个 Agent，它看不到你的内容、也没有你的对话上下文，
              只能靠这段文字判断要不要用。写清：做什么 / 什么情况下该用 / 不该用 / 参数怎么填。
            </span>
          </label>
          <label className="v3-service-agents-field">
            <span>调用名（工具名 catsco_&lt;调用名&gt;）</span>
            <input
              value={form.slug}
              onChange={(event) => updateForm({ slug: event.target.value })}
              placeholder="my-agent"
              spellCheck={false}
              readOnly={Boolean(editingSlug)}
            />
            <span className="v3-service-agents-field-hint">
              {editingSlug
                ? '调用名是分享出去的名字，编辑时不能改（改了就是另一个应用）。'
                : `你的 bot 里就这样叫它：catsco_${form.slug || 'my-agent'}`}
            </span>
          </label>
          <details className="v3-service-agents-advanced">
            <summary className="v3-service-agents-advanced-head">
              更多设置（默认：公开 · 在我的环境跑 · 免费分享）
            </summary>            <div className="v3-service-agents-field">
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
                    checked={form.visibility === 'friends'}
                    onChange={() => updateForm({ visibility: 'friends' })}
                  />
                  好友（只有我的好友能看到、能调）
                </label>
                <label>
                  <input
                    type="radio"
                    name="visibility"
                    checked={form.visibility === 'public'}
                    onChange={() => updateForm({ visibility: 'public' })}
                  />
                  公开（谁都能找到、能调）
                </label>
              </div>
            </div>
            <div className="v3-service-agents-field">
              {/* 面板只提供「分享 bot」这一种方式。历史上还有 http/mcp（要公网地址）
                  与 worker（要用户自己写命令模板）—— 两条路都要求用户懂工程细节，
                  而且调教好的 bot 本来就能干那些活。后端仍保留它们，老应用原样保存。 */}
              <span>怎么接活</span>
              {lockedRunner ? (
                <div className="v3-service-agents-locked" role="status">
                  {lockedRunner.type === 'worker' ? (
                    <>
                      这个应用用的是旧方式：在你自己的环境里跑一条固定命令
                      （{lockedRunner.command || '未填命令'}）。面板已不再提供这种方式；
                      本次保存只改名称/说明/价格，执行方式保持原样。
                    </>
                  ) : (
                    <>
                      这个应用用的是「平台直连
                      {lockedRunner.type === 'mcp' ? ' MCP ' : ' HTTP '}
                      服务」（{lockedRunner.url}）。这种接入方式需要公网可达地址，面板已不再提供；
                      本次保存只改名称/说明/价格，连接方式保持原样。
                    </>
                  )}
                </div>
              ) : (
                <div className="v3-service-agents-field-hint">
                  平台会把请求发给你选的那个 bot，由它自己处理。它不在线时调用会立刻失败
                  —— 不需要公网 IP，也不用开端口。
                </div>
              )}
            </div>
            <div className="v3-service-agents-field">
              <span>计费方式（留空=免费分享）</span>
              <div className="v3-service-agents-choices">
                <label>
                  <input
                    type="radio"
                    name="priceUnit"
                    checked={form.priceUnit === 'token'}
                    onChange={() => updateForm({ priceUnit: 'token' })}
                  />
                  按用量（每千 tokens）
                </label>
                <label>
                  <input
                    type="radio"
                    name="priceUnit"
                    checked={form.priceUnit === 'second'}
                    onChange={() => updateForm({ priceUnit: 'second' })}
                  />
                  按运行时长（每秒）
                </label>
              </div>
              <span className="v3-service-agents-field-hint">
                {form.priceUnit === 'second'
                  ? '按实际跑了多少秒算，平台自己就能测，不依赖 agent 上报。'
                  : '要你的 agent 上报 token 用量才计费（多数 CLI 不上报，那就按运行时长更靠谱）。'}
              </span>
            </div>
            <label className="v3-service-agents-field">
              <span>{form.priceUnit === 'second' ? '每秒多少钱（元）' : '每千 tokens 多少钱（元）'}</span>
              <input
                value={form.price}
                onChange={(event) => updateForm({ price: event.target.value })}
                placeholder={form.priceUnit === 'second' ? '0.01' : '2'}
                inputMode="decimal"
              />
            </label>
          </details>
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
            {publishResult.visibility === 'public'
              ? ' · 公开，谁都能找到并调用'
              : publishResult.visibility === 'friends'
                ? ' · 仅好友可见，他们能在目录里找到并加进清单'
                : ' · 仅自己可用'}
          </div>
          {publishResult.runner === 'worker' ? (
            <div className="v3-service-agents-published-hint">
              <div>
                这个应用还在用旧方式（在你自己的环境里跑固定命令）。面板已不再提供这种方式；
                想换成分享 bot，重新发布时选一个 bot 即可。
              </div>
            </div>
          ) : null}
          {publishResult.runner === 'bot' ? (
            <div className="v3-service-agents-published-hint">
              <div>
                谁来调都行，由那个 bot 自己处理。它<strong>在线时</strong>才接活；
                不在线时调用会立刻报「不在线」（不会让你白等）。
              </div>
              <div>
                别人调它可能要等一会儿（它要自己思考、可能还要动手）—— 平台会先把「已收到」回给调用方，
                结果出来再补上，所以对方的对话不会被卡住。
              </div>
            </div>
          ) : null}
          <div className="v3-service-agents-published-hint">
            之后直接跟 bot 说「用 catsco_{publishResult.slug} 帮我…」就能用。
          </div>
          <div className="v3-service-agents-published-actions">
            <button type="button" onClick={() => copyText(`catsco_${publishResult.slug}`)}>复制工具名</button>
            <button type="button" onClick={() => setPublishResult(null)}>知道了</button>
          </div>
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
      {!loading && agents.length > 1 ? (
        <div className="v3-service-agents-search">
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索应用（名称或用途）"
            aria-label="搜索应用"
            spellCheck={false}
          />
        </div>
      ) : null}
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
      ) : (visibleAgents.length === 0 ? (
        <div className="v3-service-agents-placeholder" role="status">
          <p className="v3-service-agents-placeholder-title">没有匹配的应用</p>
          <p className="v3-service-agents-placeholder-text">
            换个关键词试试，或者清空搜索看全部。
          </p>
          <button
            type="button"
            className="v3-service-agents-placeholder-action"
            onClick={() => setQuery('')}
          >
            清空搜索
          </button>
        </div>
      ) : (
        <ul className="v3-service-agents-list">
          {visibleAgents.map((agent) => {
            const busy = pending.has(agent.slug)
              || unpublishing.has(agent.slug)
              || dismissingIntro.has(agent.slug)
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
            const runnerLabel = runnerLabelOf(agent);
            const inputSummary = inputSummaryOf(agent);
            // 自己分享的不写「某某分享」：那是我自己，写上去只是噪音。
            const showSharer = Boolean(agent.owner_name) && !agent.mine;
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
                    {agent.visibility === 'friends' ? (
                      <span className="v3-service-agent-badge is-friends">好友</span>
                    ) : null}
                  </div>
                  <div className="v3-service-agent-slug">catsco_{agent.slug}</div>
                  {agent.description ? (
                    // 这段就是调用方 Agent 看到的文字：写清楚才有人会用。
                    <div className="v3-service-agent-desc" title={agent.description}>
                      {agent.description}
                    </div>
                  ) : agent.mine ? (
                    <div className="v3-service-agent-warn">
                      没写「能做什么」：别的 Agent 只能靠名字猜，基本不会用它
                      {/* 平台不会替应用生成描述；唯一的来源是应用自己在 run 结果里带 self_intro。
                          所以这里告诉人「该怎么让应用学会」—— 而不是让人凭空编一段。 */}
                      <div className="v3-service-agent-warn-hint">
                        可以让它自己写：在你的 agent 提示词里加一句「跑完后在结果里带上
                        self_intro」，它下次跑完就会在这里给出建议。
                      </div>
                    </div>
                  ) : null}
                  {agent.mine && agent.self_intro?.description ? (
                    // 应用自己建议的对外描述。写这段的是模型，所以只建议 ——
                    // 点「用它」会把内容填进编辑表单，人再确认发布。
                    <div className="v3-service-agent-intro">
                      <div className="v3-service-agent-intro-label">这个应用建议这样介绍自己</div>
                      <div className="v3-service-agent-intro-text" title={agent.self_intro.description}>
                        {agent.self_intro.description}
                      </div>
                      <div className="v3-service-agent-intro-actions">
                        <button
                          type="button"
                          className="v3-service-agent-intro-accept"
                          disabled={dismissingIntro.has(agent.slug)}
                          onClick={() => startEdit(agent, { useSelfIntro: true })}
                          aria-label={`用${agent.name || agent.slug}的自述`}
                          title="把这段填进编辑表单，你确认后才会生效"
                        >
                          用它
                        </button>
                        <button
                          type="button"
                          className="v3-service-agent-intro-dismiss"
                          disabled={dismissingIntro.has(agent.slug)}
                          onClick={() => dismissSelfIntro(agent)}
                          aria-label={`忽略${agent.name || agent.slug}的自述`}
                        >
                          {dismissingIntro.has(agent.slug) ? '忽略中…' : '忽略'}
                        </button>
                      </div>
                    </div>
                  ) : null}
                  {scopeBotUid && accountHidden ? (
                    <div className="v3-service-agent-note">账号清单中已移除</div>
                  ) : null}
                  {/* 「谁分享的 / 要传什么 / 谁在干」——别人判断该不该用，全靠这三行。
                      只有名字 + 一段描述时，陌生人只能凭感觉；好友看到名字就敢试。
                      三样都没有（老应用）就整块不渲染，别留一条空行占地方。 */}
                  {showSharer || runnerLabel || inputSummary ? (
                    <div className="v3-service-agent-meta">
                      {showSharer ? (
                        <span className="v3-service-agent-meta-item" title="分享这个应用的人">
                          {agent.owner_name} 分享
                        </span>
                      ) : null}
                      {runnerLabel ? (
                        <span className="v3-service-agent-meta-item" title="谁在执行这个应用">
                          {runnerLabel}
                        </span>
                      ) : null}
                      {inputSummary ? (
                        <span
                          className="v3-service-agent-meta-item is-input"
                          title="调用时要传的字段（调用方 Agent 也是照这个填）"
                        >
                          {inputSummary}
                        </span>
                      ) : null}
                    </div>
                  ) : null}
                </div>
                <div className="v3-service-agent-actions">
                  {agent.mine ? (
                    <button
                      type="button"
                      className="v3-service-agent-edit"
                      disabled={busy}
                      onClick={() => startEdit(agent)}
                      aria-label={`编辑${agent.name || agent.slug}`}
                      title="编辑描述/执行方式后重新发布"
                    >
                      <Pencil size={15} />
                      <span>编辑</span>
                    </button>
                  ) : null}
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
      ))}
    </aside>
  );
}

export { ServiceAgentsPanel };
