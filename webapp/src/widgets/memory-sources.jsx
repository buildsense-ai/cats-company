import React, { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { ArrowRight, BookOpen, Brain, ChevronRight, History, RotateCcw } from 'lucide-react';
import { api } from '../api';
import { renderSafeMarkdown } from './markdown-utils';
import { DIARY_RELATIONS, DIARY_STATUS } from './diary-sources';
import { MEMORY_NODE_LABELS, MEMORY_RELATIONS, verifiedMemoryDetail } from './memory-branch';
import './diary-sources.css';
import './memory-sources.css';

// One answer to "what memory informed this reply?": Diary entries the agent
// read itself and Branch sources recalled in the background share one list.
// Rows come from validated, body-free metadata; a row expands its verified
// body in place, one row at a time, and only then reads it.
const VISIBLE_ROWS = 6;
const IN_PROGRESS = new Set(['running', 'ready']);

function diaryRows(views) {
  const byRef = new Map(), relations = [];
  for (const view of views) {
    for (const source of view.sources) {
      const known = byRef.get(source.ref);
      // A later read of a search hit keeps the stronger disclosure.
      if (!known || known.disclosure === 'metadata' && source.disclosure === 'text') byRef.set(source.ref, source);
    }
    for (const relation of view.relations) {
      if (!relations.some(r => r.from === relation.from && r.to === relation.to && r.kind === relation.kind)) relations.push(relation);
    }
  }
  return { sources: [...byRef.values()], relations };
}

// The snapshot is per node; a reader is the unit a person can actually open.
function branchUnits(events) {
  const injected = new Set(events.flatMap(event => event.injected_refs));
  const cited = new Set(events.flatMap(event => event.answer_refs));
  const units = new Map();
  for (const event of events) {
    for (const node of event.nodes) {
      const key = JSON.stringify(node.reader);
      const unit = units.get(key) || { key, node, size: 0, refs: new Set(), ids: new Set() };
      unit.size += 1;
      unit.refs.add(node.ref);
      unit.ids.add(node.id);
      units.set(key, unit);
    }
  }
  const list = [...units.values()].map(unit => ({
    ...unit,
    injected: [...unit.refs].some(ref => injected.has(ref)),
    cited: [...unit.refs].some(ref => cited.has(ref)),
  }));
  const days = new Map();
  for (const unit of list) {
    const day = unit.node.reader.archive?.day;
    if (day) days.set(day, [...(days.get(day) || []), unit]);
  }
  for (const group of days.values()) {
    group.sort((a, b) => a.node.reader.archive.offset - b.node.reader.archive.offset);
    group.forEach((unit, index) => { unit.part = group.length > 1 ? index + 1 : 0; });
  }
  const labelled = list.map(unit => ({ ...unit, ...branchLabel(unit) }));
  // Two readers can share a label (two projections of nine points each);
  // number them in read order so rows stay distinguishable.
  const groups = new Map();
  for (const unit of labelled) groups.set(unit.title + unit.meta, [...(groups.get(unit.title + unit.meta) || []), unit]);
  for (const group of groups.values()) if (group.length > 1) group.forEach((unit, index) => { unit.title = `${unit.title} ${index + 1}`; });
  return labelled;
}

function branchLabel(unit) {
  const reader = unit.node.reader;
  const points = unit.size > 1 ? `${unit.size} 个要点` : '';
  if (reader.kind === 'archive_turn') return { title: '会话摘录', meta: [reader.archive.day, unit.part ? `第 ${unit.part} 段` : ''].filter(Boolean).join(' · ') };
  if (reader.kind === 'projection') return { title: '会话记忆', meta: points };
  if (reader.kind === 'assertion') return { title: '记忆断言', meta: points };
  return { title: MEMORY_NODE_LABELS[reader.anchor.kind] || '记忆节点', meta: points };
}

function emptyStatus({ diaryPending, events, active, finished }) {
  const statuses = new Set(events.map(event => event.status));
  if (diaryPending) return finished ? '日记查阅未完成' : '正在查阅日记…';
  if (events.some(event => event.observation_id && event.consumed_turn_id)) return '后台回忆的摘要已用于回答 · 没有可打开的来源';
  if (statuses.has('running')) return active || !finished ? '正在后台回忆…' : '后台回忆未完成';
  if (statuses.has('deferred') || statuses.has('ready')) return '后台回忆未赶上本次回答';
  if (statuses.has('empty')) return '后台回忆未找到相关记忆';
  if (['failed', 'cancelled', 'expired'].some(status => statuses.has(status))) return '后台回忆未完成';
  if (statuses.has('unavailable')) return '后台回忆暂不可用';
  return null;
}

// Each origin states only its own counts: the agent's Diary reads went into
// its context through its own tool calls; Branch sources only count once the
// background recall injected them.
function diaryLine(diary) {
  const read = diary.sources.filter(source => source.disclosure === 'text').length;
  const searched = diary.sources.length - read;
  return [read && `读取 ${read} 条`, searched && `搜索到 ${searched} 条未读取`].filter(Boolean).join('，');
}

function branchLine({ units, events, active }) {
  const used = units.filter(unit => unit.injected).length, unused = units.length - used;
  if (used > 0) return `${used} 条用于回答${unused ? `，另有 ${unused} 条未采用` : ''}`;
  const statuses = new Set(events.map(event => event.status));
  if (active && statuses.has('running')) return `正在回忆，已找到 ${units.length} 条`;
  if (statuses.has('ready') && !statuses.has('deferred')) return `已找到 ${units.length} 条，尚未送达回答`;
  if (statuses.has('deferred')) return `回答先完成，${units.length} 条未用于本次回答`;
  if (['failed', 'cancelled', 'expired'].some(status => statuses.has(status))) return `回忆中断，${units.length} 条未用于本次回答`;
  return `${units.length} 条未用于本次回答`;
}

function MemoryRow({ row, open, detailId, onToggle }) {
  const Icon = row.type === 'diary' ? BookOpen : History;
  return (
    <button type="button" className={`cc-memory-source is-${row.type}`} aria-expanded={open} aria-controls={open ? detailId : undefined}
      onClick={() => onToggle(row)}>
      <Icon size={15} aria-hidden="true" />
      <span className="cc-memory-source-label"><span>{row.title}</span>{row.meta && <small>{row.meta}</small>}</span>
      {row.badge && <span className={`cc-memory-badge${row.cited ? ' is-cited' : ''}`}>{row.badge}</span>}
    </button>
  );
}

export default function MemorySources({ diaryViews = [], events = [], active = false, finished = false, agentUid, agentName }) {
  const [open, setOpen] = useState(false);
  const panelId = useId();
  const buttonRef = useRef(null);
  const diary = useMemo(() => diaryRows(diaryViews.filter(view => !view.pending)), [diaryViews]);
  const units = useMemo(() => branchUnits(events), [events]);
  const diaryPending = diaryViews.some(view => view.pending);
  const total = diary.sources.length + units.length;

  if (!total) {
    const status = emptyStatus({ diaryPending, events, active, finished });
    return status && <span className="cc-memory-status" role="status"><Brain size={14} aria-hidden="true" />{status}</span>;
  }
  const recalling = active && events.some(event => event.status === 'running') && !units.some(unit => unit.injected);
  return (
    <div className="cc-memory-disclosure" onKeyDown={event => {
      if (!open || event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation();
      setOpen(false);
      buttonRef.current?.focus({ preventScroll: true });
    }}>
      <button ref={buttonRef} type="button" className="cc-memory-toggle" aria-expanded={open} aria-controls={open ? panelId : undefined}
        onClick={() => setOpen(value => !value)}>
        <Brain size={14} aria-hidden="true" />
        <span>{recalling ? '正在回忆' : '记忆'}</span>
        <span className="cc-activity-count">{total}</span>
        <ChevronRight size={14} className="cc-disclosure-chevron" aria-hidden="true" />
      </button>
      {open && <MemoryPanel id={panelId} diary={diary} units={units} events={events} active={active} agentUid={agentUid} agentName={agentName}
        incomplete={diaryViews.some(view => view.incomplete)} />}
    </div>
  );
}

function MemoryPanel({ id, diary, units, events, active, agentUid, agentName, incomplete }) {
  const [selected, setSelected] = useState(null);
  const [expanded, setExpanded] = useState({});
  const panelRef = useRef(null);
  const detailId = `${id}-detail`;
  const anyInjected = units.some(unit => unit.injected);
  const diaryRows = diary.sources.map(source => ({
      type: 'diary', key: source.ref, source, title: source.title || '未命名笔记',
      meta: [source.day, DIARY_STATUS[source.status], source.status === 'draft' ? '尚未核验' : '', source.disclosure === 'metadata' ? '搜索结果' : ''].filter(Boolean).join(' · '),
    }));
  const branchRows = units.map(unit => ({
    type: 'branch', key: unit.key, unit, title: unit.title, meta: unit.meta, cited: unit.cited,
    badge: unit.cited ? '回答引用' : !unit.injected && anyInjected ? '未采用' : '',
  }));
  const carried = events.some(event => event.consumed_turn_id && event.consumed_turn_id !== event.origin_turn_id);
  const close = useCallback(() => {
    const button = panelRef.current?.querySelector('.cc-memory-source[aria-expanded="true"]');
    setSelected(null);
    button?.focus({ preventScroll: true });
  }, []);
  // Escape closes the open row before the list, only while focus is here.
  useEffect(() => {
    if (!selected) return undefined;
    const escape = event => {
      if (event.key !== 'Escape') return;
      const focus = document.activeElement;
      if (focus && focus !== document.body && !panelRef.current?.contains(focus)) return;
      event.preventDefault(); event.stopImmediatePropagation(); close();
    };
    document.addEventListener('keydown', escape, true);
    window.addEventListener('cc:auth-changed', close);
    return () => { document.removeEventListener('keydown', escape, true); window.removeEventListener('cc:auth-changed', close); };
  }, [selected, close]);
  const toggle = row => setSelected(current => current === row.key ? null : row.key);
  const selectDiary = source => setSelected(source.ref);
  // The agent's own Diary reads and the background Branch recall are two
  // origins: each gets its own heading, state and row limit.
  const origin = (type, who, what, line, rows, note) => {
    const openIndex = rows.findIndex(row => row.key === selected);
    const visible = expanded[type] || openIndex >= VISIBLE_ROWS ? rows : rows.slice(0, VISIBLE_ROWS);
    return (
      <section className={`cc-memory-origin is-${type}`} aria-label={who}>
        <h4 className="cc-memory-group"><span>{who}</span><span className="cc-memory-group-meta" role="status">{what}{line && ` · ${line}`}</span></h4>
        {note}
        <div className="cc-memory-list">
          {visible.map(row => (
            <React.Fragment key={row.key}>
              <MemoryRow row={row} open={selected === row.key} detailId={detailId} onToggle={toggle} />
              {selected === row.key && (row.type === 'diary'
                ? <DiaryDetail id={detailId} agentUid={agentUid} source={row.source} view={diary} onSelect={selectDiary} />
                : <MemoryDetail id={detailId} agentUid={agentUid} unit={row.unit} />)}
            </React.Fragment>
          ))}
        </div>
        {rows.length > visible.length && <button type="button" className="cc-memory-more" onClick={() => setExpanded(current => ({ ...current, [type]: true }))}>再显示 {rows.length - visible.length} 条</button>}
      </section>
    );
  };
  return (
    <section ref={panelRef} id={id} className="cc-memory-panel" aria-label="记忆来源" onClick={event => event.stopPropagation()}>
      {diaryRows.length > 0 && origin('diary', `${agentName || '助手'}主动查阅`, '日记', diaryLine(diary), diaryRows,
        incomplete && <p className="cc-memory-note">日记还有更多结果，未在此列出。</p>)}
      {branchRows.length > 0 && origin('branch', '后台自动回忆', '历史会话', branchLine({ units, events, active }), branchRows,
        carried && <p className="cc-memory-note">这些记忆来自上一轮提问，在本轮才送达。</p>)}
    </section>
  );
}

function readError(error, refused) {
  return [403, 404].includes(error?.status) ? refused : error?.status === 429 ? '读取繁忙，请稍后重试。' : error?.status ? '暂时无法读取这个版本，请重试。' : error?.message || '读取失败，请重试。';
}

function ReaderBody({ state, label, onRetry, children }) {
  return (
    <section className="cc-diary-reader-body" aria-live="polite" aria-busy={state.status === 'loading'}>
      {state.status === 'loading' && <div className="cc-diary-skeleton" role="status" aria-label={label}><span /><span /><span /></div>}
      {state.status === 'ready' && children}
      {state.status === 'error' && <div className="cc-diary-read-error" role="alert"><p>{state.error}</p><button type="button" onClick={onRetry}><RotateCcw size={14} aria-hidden="true" />重试读取</button></div>}
    </section>
  );
}

const prose = text => <div className="cc-diary-prose" dangerouslySetInnerHTML={{ __html: renderSafeMarkdown(text) }} />;

const POINT_TAGS = { assertion: '断言', condition: '条件', omission: '缺口', tool_call: '工具', correction: '更正' };
// These relation labels name the linking point's role, so they read on the
// point being limited or supported ("适用条件：条件 1"); the rest
// ("来源于", "延续", "使用") read on the point they start from.
const TARGET_RELATIONS = new Set(['supports', 'qualifies', 'omits', 'corrects', 'supplements', 'requires']);

// Points of one kind read as one list under one heading, in document order;
// a point with an unlinked source scope never joins a bound group. Links
// between points (often many-to-many) show once, as tags on the linked
// point, instead of a graph. Hit marks show only when the run hit some,
// not all, of the source's points.
function pointGroups(nodes, edges = [], hits = new Set()) {
  const counts = new Map();
  const points = nodes.map((item, index) => {
    const kind = POINT_TAGS[item.kind] || MEMORY_NODE_LABELS[item.kind] || '要点';
    counts.set(kind, (counts.get(kind) || 0) + 1);
    return { id: item.id, key: item.id || index, text: item.text, tag: `${kind} ${counts.get(kind)}`, links: new Map(),
      heading: (MEMORY_NODE_LABELS[item.kind] || '记忆内容') + (item.association === 'member_scope_unlinked' ? ' · 来源范围，未绑定到此断言' : '') };
  });
  const byId = new Map();
  for (const point of points) if (point.id != null && !byId.has(point.id)) byId.set(point.id, point);
  const linked = edges.filter(edge => edge && edge.from !== edge.to && byId.has(edge.from) && byId.has(edge.to));
  for (const edge of linked) {
    const onTarget = TARGET_RELATIONS.has(edge.kind);
    const holder = byId.get(onTarget ? edge.to : edge.from), other = byId.get(onTarget ? edge.from : edge.to);
    const label = MEMORY_RELATIONS[edge.kind] || '关联', names = holder.links.get(label) || [];
    if (!names.includes(other.tag)) holder.links.set(label, [...names, other.tag]);
  }
  const hitCount = points.filter(point => hits.has(point.id)).length;
  const markHits = hitCount > 0 && hitCount < points.length;
  const groups = new Map();
  for (const point of points) groups.set(point.heading, [...(groups.get(point.heading) || []), {
    key: point.key, text: point.text, tag: linked.length ? point.tag : undefined, hit: markHits && hits.has(point.id),
    links: point.links.size ? [...point.links].map(([label, names]) => `${label}：${names.join('、')}`).join(' · ') : undefined }]);
  return [...groups].map(([heading, list]) => ({ heading, points: list }));
}

function Points({ heading, points }) {
  const tagged = points.some(point => point.tag || point.hit);
  return (
    <section className="cc-memory-detail-node">
      <h3>{heading}</h3>
      <ul className={`cc-memory-points${tagged ? ' is-tagged' : ''}`}>
        {points.map(point => (
          <li key={point.key}>
            {tagged && <span className="cc-memory-point-head">
              {point.tag && <span className="cc-memory-point-tag">{point.tag}</span>}
              {point.hit && <span className="cc-memory-point-hit">命中</span>}
            </span>}
            <div className="cc-memory-point-body">
              {prose(point.text)}
              {point.links && <p className="cc-memory-point-links">{point.links}</p>}
            </div>
          </li>
        ))}
      </ul>
    </section>
  );
}

function RelatedRow({ source, onSelect }) {
  return (
    <button type="button" className="cc-diary-source" onClick={event => onSelect(source, event.currentTarget)}>
      <BookOpen size={15} aria-hidden="true" />
      <span className="cc-diary-source-label"><span>{source.title || '未命名笔记'}</span><small>{source.day} · {DIARY_STATUS[source.status]}{source.status === 'draft' ? ' · 尚未核验' : ''}</small></span>
      <ArrowRight size={14} aria-hidden="true" />
    </button>
  );
}

function DiaryDetail({ id, agentUid, source, view, onSelect }) {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState({ status: 'loading' });
  useEffect(() => {
    const controller = new AbortController();
    setState({ status: 'loading', ref: source.ref });
    if (!agentUid) {
      setState({ status: 'error', ref: source.ref, error: '无法确认来源助手，请重新打开这段对话。' });
      return () => controller.abort();
    }
    api.queryDailyNotes(agentUid, { operation: 'read', document_id: source.anchor.document_id, entry_id: source.anchor.id, revision: source.anchor.revision }, { signal: controller.signal })
      .then((page) => {
        if (controller.signal.aborted) return;
        if (page?.document_id !== source.anchor.document_id || page?.revision !== source.anchor.revision) throw new Error('返回版本与引用不一致，未显示正文。');
        const entry = page.entries?.find((e) => e.id === source.anchor.id);
        if (!entry || typeof entry.text !== 'string' || typeof entry.title !== 'string' || !DIARY_STATUS[entry.status]) throw new Error('该版本中未找到完整条目，未显示正文。');
        setState({ status: 'ready', ref: source.ref, entry });
      })
      .catch((error) => {
        if (controller.signal.aborted) return;
        setState({ status: 'error', ref: source.ref, error: readError(error, '无权查看或该笔记已不可用。') });
      });
    return () => controller.abort();
  }, [agentUid, source.ref, attempt]);
  const current = state.ref === source.ref ? state : { status: 'loading' };
  const linked = (s) => view.relations.filter((r) => r.from === source.ref && r.to === s.ref || r.to === source.ref && r.from === s.ref);
  const related = view.sources.filter((s) => s.ref !== source.ref && linked(s).length > 0);
  return (
    <div id={id} className="cc-memory-detail" role="region" aria-label="日记引用详情">
      <p className="cc-memory-detail-meta">
        <span title={source.anchor.revision}>引用时版本 <code>{source.anchor.revision.slice(4, 12)}</code></span>
        {source.currentStatus !== source.status && <span>当前状态已变为{DIARY_STATUS[source.currentStatus]}，这里展示引用时的版本</span>}
      </p>
      <ReaderBody state={current} label="正在读取引用版本" onRetry={() => setAttempt((n) => n + 1)}>
        {current.entry?.text ? prose(current.entry.text) : <p>这个条目的正文为空。</p>}
      </ReaderBody>
      {related.length > 0 && <section className="cc-diary-related"><h3>同次读取的关联笔记</h3>{related.map((s) => <div key={s.ref}><p>{linked(s).map((r) => `${r.from === source.ref ? '' : '被'}${DIARY_RELATIONS[r.kind]}`).join(' · ')}</p><RelatedRow source={s} onSelect={onSelect} /></div>)}</section>}
      <details className="cc-diary-reference"><summary>引用标识</summary><code>{source.ref}</code></details>
    </div>
  );
}

function MemoryDetail({ id, agentUid, unit }) {
  const [attempt, setAttempt] = useState(0), [state, setState] = useState({ status: 'loading' });
  const { node } = unit;
  const requestKey = `${agentUid}:${unit.key}`;
  useEffect(() => {
    const controller = new AbortController();
    setState({ status: 'loading', key: requestKey });
    if (!agentUid) { setState({ status: 'error', key: requestKey, error: '无法确认来源助手，请重新打开这段对话。' }); return () => controller.abort(); }
    api.readMemorySource(agentUid, node.reader, { signal: controller.signal })
      .then(page => {
        if (controller.signal.aborted) return;
        setState({ status: 'ready', key: requestKey, detail: verifiedMemoryDetail(page, node.reader) });
      }).catch(error => {
        if (controller.signal.aborted) return;
        setState({ status: 'error', key: requestKey, error: readError(error, '无权查看或来源已不可用。') });
      });
    return () => controller.abort();
  }, [requestKey, attempt]);
  const current = state.key === requestKey ? state : { status: 'loading' };
  const detail = current.detail;
  return (
    <div id={id} className="cc-memory-detail" role="region" aria-label="记忆来源详情">
      {detail?.goal && <p className="cc-memory-detail-title">{detail.goal}</p>}
      <p className="cc-memory-detail-meta"><span>{unit.cited ? '回答中引用' : unit.injected ? '已用于回答' : '未用于本次回答'}</span></p>
      <ReaderBody state={current} label="正在核验记忆来源" onRetry={() => setAttempt(value => value + 1)}>
        {detail && <>
          {pointGroups(detail.nodes, detail.edges, unit.ids).map((group, index) => <Points key={index} {...group} />)}
          {detail.conditions.length > 0 && <Points heading="来源范围的适用条件" points={detail.conditions.map((text, key) => ({ key, text }))} />}
          {detail.omissions.length > 0 && <Points heading="来源范围的已知缺口" points={detail.omissions.map((text, key) => ({ key, text }))} />}
          {detail.gaps && <Points heading="尚未核验" points={[{ key: 0, text: detail.gaps }]} />}
          {detail.coverage !== 'complete' && <p className="cc-memory-detail-note">这是本次读取的局部记忆，不代表完整历史。</p>}
        </>}
      </ReaderBody>
      <details className="cc-diary-reference"><summary>引用标识</summary><code>{node.ref}</code></details>
    </div>
  );
}
