import React, { useEffect, useRef, useState } from 'react';
import Icons from './icon-preview-icons';
import LoadingState from '../widgets/loading-state';
import HistoryLoadingState from '../widgets/history-loading-state';
import './icon-system-preview.css';

// Proposed semantic mapping. Production usage remains in the source inventory.
const aliases = { AlertCircle: 'CircleAlert', CheckCircle2: 'CheckCircle', PencilLine: 'Pencil', PenLine: 'Pencil', Users: 'UsersRound' };
const roles = [[16, '辅助操作', '消息操作、菜单、状态'], [20, '常用操作', '侧栏、顶部工具栏'], [24, '页面入口', '模块入口、独立操作']];
const semantic = [['编辑', 'Pencil', 'Pencil · PencilLine · PenLine'], ['多人', 'UsersRound', 'Users · UsersRound'], ['警告', 'CircleAlert', 'AlertCircle · CircleAlert'], ['完成', 'CheckCircle', 'CheckCircle · CheckCircle2'], ['展开', 'ChevronRight', '树形详情固定左侧'], ['下拉', 'ChevronDown', '选择控件固定右侧']];
function Glyph({ name, size = 20, ...props }) {
  const resolved = aliases[name] || name;
  const Icon = Icons[resolved];
  return Icon ? <Icon size={size} strokeWidth={resolved === 'AppleBrand' ? 1.5 : 2} fill="none" aria-hidden="true" {...props} /> : null;
}
function Section({ title, description, children }) {
  return <section className="ds-section"><header><h3>{title}</h3><p>{description}</p></header>{children}</section>;
}
function InteractionSamples() {
  const [selected, setSelected] = useState('对话');
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState('');
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [pinned, setPinned] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const timers = useRef([]);
  useEffect(() => () => timers.current.forEach(clearTimeout), []);
  const later = (fn, ms) => timers.current.push(setTimeout(fn, ms));
  async function copy() {
    try {
      await navigator.clipboard.writeText('CatsCo · 统一图标规范');
      setCopyError(''); setCopied(true); later(() => setCopied(false), 1500);
    } catch { setCopyError('复制未完成，请重试'); }
  }
  function save() {
    setSaving(true); setSaved(false);
    later(() => { setSaving(false); setSaved(true); later(() => setSaved(false), 1500); }, 1200);
  }
  return <div className="ds-scenarios">
    <article className="ds-surface"><h4>侧栏入口 · 20px</h4><div className="ds-navigation" aria-label="侧栏交互示例">
      {[['对话', 'MessageSquare'], ['历史任务', 'History'], ['项目', 'Folder'], ['联系人', 'UsersRound']].map(([label, name]) => <button type="button" key={label} className="ds-nav" aria-pressed={selected === label} onClick={() => setSelected(label)}><Glyph name={name} /><span>{label}</span></button>)}
    </div><p>同级等大；悬停、选中使用中性背景。</p></article>
    <article className="ds-surface"><h4>消息工具栏 · 16px</h4><div className="ds-actions">
      <button type="button" className="ds-icon-button" onClick={copy} aria-label={copied ? '已复制样例' : '复制样例'} title="复制"><Glyph name={copied ? 'Check' : 'Copy'} size={16} className={copied ? 'ds-feedback' : ''} /></button>
      <button type="button" className="ds-icon-button" onClick={() => setPinned(v => !v)} aria-pressed={pinned} aria-label="置顶样例" title="置顶"><Glyph name="Pin" size={16} /></button>
      <button type="button" className="ds-icon-button" aria-label="分享样例不可用" title="当前不可分享" disabled><Glyph name="Share2" size={16} /></button>
      <button type="button" className="ds-button" onClick={save} disabled={saving} aria-busy={saving}><Glyph name={saving ? 'LoaderCircle' : saved ? 'Check' : 'Save'} size={16} className={saving ? 'ds-spin' : saved ? 'ds-feedback' : ''} /><span>{saving ? '保存中' : saved ? '已保存' : '模拟保存'}</span></button>
    </div><div className="ds-live" role="status">{copyError || (copied ? '已复制样例文字' : saved ? '保存反馈演示完成' : '可体验复制、置顶、禁用与加载状态。')}</div><p>状态切换保留原尺寸，手机触控区至少 44px。</p></article>
    <article className="ds-surface"><h4>工具过程 · 16px</h4><button type="button" className="ds-disclosure" aria-expanded={expanded} aria-controls="ds-tool-result" onClick={() => setExpanded(v => !v)}><Glyph name="ChevronRight" size={16} className="cc-disclosure-chevron" /><Glyph name="Wrench" size={16} /><span>读取项目资料</span><span className="ds-result-label"><Glyph name="CheckCircle" size={16} />已完成</span></button>
      {expanded && <div id="ds-tool-result" className="ds-tool-result">项目资料已整理完成。工具、完成状态与展开箭头均使用矢量图形。</div>}
      <p>同一枚箭头旋转 180ms，工具结果紧随操作。</p></article>
    <article className="ds-surface"><h4>状态反馈 · 16px</h4><div className="ds-status-list">{[['CheckCircle', '已完成', 'success'], ['CircleAlert', '需要处理', 'warning'], ['XCircle', '执行失败', 'danger'], ['LoaderCircle', '正在处理', 'neutral']].map(([name, text, tone]) => <span key={name} className={`ds-status ds-tone-${tone}`}><Glyph name={name} size={16} className={name === 'LoaderCircle' ? 'ds-spin' : ''} />{text}</span>)}</div><p>颜色表达真实状态，同时保留图形和文字。</p></article>
  </div>;
}
export default function IconSystemPreview({ inventory, labels }) {
  const [size, setSize] = useState(20);
  const [query, setQuery] = useState('');
  const [motion, setMotion] = useState('chat');
  const filtered = inventory.icons.filter(i => `${i.name} ${aliases[i.name] || ''} ${labels[i.name] || ''}`.toLowerCase().includes(query.trim().toLowerCase()));
  const compare = ['Search', 'Check', 'Plus', 'Bell', 'X', 'AppWindow'].map(name => {
    const usages = inventory.icons.find(i => i.name === name)?.usages || [];
    const props = usages.flatMap(u => u.propVariants || [u.props]).find(p => p.strokeWidth && p.strokeWidth !== 2) || {};
    return [name, props.size || 24, props.strokeWidth || 2];
  });
  return <div className="ds-preview">
    <div className="ds-intro"><div><span className="ds-eyebrow">CATSCO / ICON SYSTEM</span><h2>一套轮廓，清楚的层级。</h2><p>统一尺寸、描边、语义与反馈，先在完整图标集和交互样例中检视。</p></div><span className="ds-preview-label">方案预览</span></div>
    <div className="ds-rules">{roles.map(([s, title, detail]) => <article key={s}><div className="ds-size-sample"><Glyph name="Folder" size={s} /><Glyph name="Search" size={s} /><Glyph name="MessageSquare" size={s} /></div><h3>{s}px · {title}</h3><p>{detail}</p></article>)}<article><div className="ds-size-sample"><Glyph name="Pencil" size={24} /><Glyph name="AppleBrand" size={24} /></div><h3>2 · 圆角描边</h3><p>功能图标描边 2；苹果品牌轮廓补偿为 1.5。</p></article></div>
    <Section title="01 / 同级控件，保持同样的视觉重量" description="普通图标使用主题内的中性色，圆角端点、圆角连接，不随悬停倾斜、放大或移位。">
      <div className="ds-comparison"><div><h4>当前源码 · 存在多种尺寸与粗细</h4><div className="ds-comparison-icons">{compare.map(([name, s, stroke]) => { const Icon = Icons[name]; return <span key={name}><Icon size={s} strokeWidth={stroke} aria-hidden="true" /><small>{s} / {stroke}</small></span>; })}</div></div><div><h4>统一方案 · 常用操作 20px / 描边 2</h4><div className="ds-comparison-icons">{compare.map(([name]) => <span key={name}><Glyph name={name} /><small>{labels[name]}</small></span>)}</div></div></div>
    </Section>
    <Section title="02 / 一个操作，对应一个图形" description="合并相同含义的近似图标；展开与下拉按控件结构保持方向和位置。">
      <div className="ds-semantics">{semantic.map(([label, name, note]) => <article key={label}><Glyph name={name} /><div><strong>{label}</strong><small>{note}</small></div></article>)}</div>
      <div className="ds-symbol-comparison"><div><span>旧内容块</span><span className="ds-old-symbols">🔧　✅　❌　▶　▼</span></div><div><span>统一矢量</span><span className="ds-vector-symbols">{['Wrench', 'CheckCircle', 'XCircle', 'ChevronRight', 'ChevronDown'].map(name => <Glyph name={name} size={16} key={name} />)}</span></div></div>
    </Section>
    <Section title="03 / 放进界面，体验真实尺寸" description="点击、键盘聚焦和手机轻触都能操作。悬停、选中、禁用与进行中使用固定的反馈规则。"><InteractionSamples /></Section>
    <Section title="04 / 动效沿用同一套节奏" description="圆环 900ms；展开 180ms；反馈淡入 180ms。大块加载沿用已确认的对话读取与层叠窗口风格。">
      <div className="ds-motion-layout"><div className="ds-motion-stage" role="group" aria-label="加载图形示例">{motion === 'chat' ? <HistoryLoadingState /> : <LoadingState kind={motion} />}</div><div className="ds-motion-copy"><div className="ds-segment" aria-label="加载场景">{[['chat','对话'],['apps','应用'],['document','文件']].map(([value, label]) => <button type="button" key={value} aria-pressed={motion === value} onClick={() => setMotion(value)}>{label}</button>)}</div><h4>轻量、有反馈、位置稳定</h4><p>小按钮使用圆环表达进行中；内容区域保留对应场景的图形。减少动态效果时停止循环与旋转。</p><div className="ds-actions"><span className="ds-status"><Glyph name="LoaderCircle" size={16} className="ds-spin" />900ms</span><span className="ds-status"><Glyph name="Check" size={16} />180ms</span></div></div></div>
    </Section>
    <Section title={`05 / 完整图标集 · ${inventory.icons.length}`} description="下方全部按统一方案渲染。可切换尺寸，检查细节、留白与同级图标的视觉重量。">
      <div className="ds-gallery-toolbar"><label><Glyph name="Search" size={16} /><input type="search" aria-label="搜索统一图标" placeholder="搜索名称或中文含义" value={query} onChange={e => setQuery(e.target.value)} /></label><div className="ds-segment" aria-label="统一图标尺寸">{roles.map(([s]) => <button type="button" key={s} aria-pressed={s === size} onClick={() => setSize(s)}>{s}px</button>)}</div><span>{filtered.length} 个图标</span></div>
      <div className="ds-gallery">{filtered.map(icon => <article key={icon.name}><div><Glyph name={icon.name} size={size} /></div><strong>{labels[icon.name] || icon.name}</strong><small>{aliases[icon.name] ? `统一为 ${aliases[icon.name]}` : icon.name}</small></article>)}</div>{!filtered.length && <p className="ds-no-results">没有匹配的图标。</p>}
    </Section>
    <aside className="ds-brand-note"><span className="ds-brand-wordmark" role="img" aria-label="Catsco" /><div><h4>品牌与插画保留独立身份</h4><p>应用图标沿用灰底圆角白字；身份小猫保留插画造型。此页是统一方案预览，现有业务页面尚未批量替换。</p></div><a href="/icon-preview.html?tab=other">查看品牌资源 →</a></aside>
  </div>;
}
