import React, { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import { createRoot } from 'react-dom/client';
import { Check, CheckCheck, ChevronDown, Copy, FileText, Monitor, Moon, Pause, Pencil, Play, RotateCcw, Settings, Sun } from 'lucide-react';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '../css/auth-critical.css';
import '../views/workspace-styles';
import '../css/catsco-focus-policy.css';
import '../css/apps-view.css';
import '../css/skillhub-view.css';
import '../css/artifact-quick-picker.css';
import LoadingState from '../widgets/loading-state';
import HistoryLoadingState from '../widgets/history-loading-state';
import CustomSelect from '../widgets/custom-select';
import AgentSettingsMenu from '../widgets/agent-settings-menu';
import { FeedbackProvider, useFeedback } from '../components/feedback-system';
import useLoadingHandoff from '../utils/use-loading-handoff';
import './motion-preview.css';

const loaders = [
  { id: 'chat', title: '读取对话', description: '本次统一的风格基准' },
  { id: 'apps', title: '应用', description: '应用列表、云端应用', label: '正在读取应用' },
  { id: 'skills', title: '能力', description: 'SkillHub、Agent 能力、运行工作区', label: '正在读取能力' },
  { id: 'document', title: '文档', description: '文件、PDF、表格、知识库、系统提示词', label: '正在打开文件' },
  { id: 'image', title: '图片', description: '图片预览', label: '正在加载图片' },
  { id: 'connect', title: '电脑与连接', description: '电脑列表、连接过程', label: '正在读取电脑' },
  { id: 'tasks', title: '任务', description: '侧栏历史任务', label: '正在读取任务' },
  { id: 'startup', title: '工作台启动', description: '启动工作台、恢复登录状态' },
];

const menus = [
  ['模型菜单', 'v3-custom-model-select-options'],
  ['能力菜单', 'cc-skillhub-action-menu'],
  ['消息菜单', 'v3-message-action-menu'],
  ['个人菜单', 'v3-profile-popover'],
  ['产物菜单', 'cc-artifact-picker'],
  ['筛选菜单', 'cloud-artifact-filter-popover'],
  ['Agent 子菜单', 'cc-agent-settings-submenu'],
];
const themes = [['light', '浅色', Sun], ['dark', '深色', Moon], ['liquid', '液态浅色', Sun], ['liquid-green', '液态深色', Moon]];

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme === 'liquid-green' ? 'liquid' : theme;
  document.documentElement.dataset.liquidVariant = theme === 'liquid-green' ? 'green' : '';
}

function Result({ kind }) {
  return <div className="mp-result" data-loading-content="">
    <span className="mp-result-icon"><Check size={19} /></span>
    <strong>{kind === 'chat' ? '对话已就绪' : '内容已就绪'}</strong>
    <span>内容会接着加载图形自然出现</span>
    <div className="mp-result-lines"><i /><i /><i /></div>
  </div>;
}

function StartupLoading() {
  const ref = useLoadingHandoff();
  return <div ref={ref} className="mp-startup" role="status"><span className="cc-workspace-loading-indicator" aria-hidden="true" /><span>正在加载工作台…</span></div>;
}

function LoaderCard({ item, allComplete, revision }) {
  const [complete, setComplete] = useState(false);
  const [localRevision, setLocalRevision] = useState(0);
  useEffect(() => { setComplete(false); }, [revision]);
  useEffect(() => { if (allComplete) setComplete(true); }, [allComplete]);
  const replay = () => { setComplete(false); setLocalRevision(value => value + 1); };
  return <article className="mp-card">
    <div className="mp-card-heading"><div><h3>{item.title}</h3><p>{item.description}</p></div>
      <button className="mp-icon-button" onClick={replay} aria-label={`重播${item.title}`} title="重播"><RotateCcw size={15} /></button>
    </div>
    <div className="mp-loader-stage" data-loading-region="">
      {complete ? <Result kind={item.id} /> : item.id === 'chat'
        ? <HistoryLoadingState key={`${revision}:${localRevision}`} />
        : item.id === 'startup'
          ? <StartupLoading key={`${revision}:${localRevision}`} />
          : <LoadingState key={`${revision}:${localRevision}`} kind={item.id} label={item.label} compact={item.id === 'tasks'} />}
    </div>
    <div className="mp-card-footer"><span>{complete ? '已完成' : '循环播放中'}</span><button onClick={complete ? replay : () => setComplete(true)}>{complete ? '重新加载' : '模拟完成'}</button></div>
  </article>;
}

function ReplayCard({ title, description, children, revision }) {
  const [tick, setTick] = useState(0);
  return <article className="mp-card mp-entry-card">
    <div className="mp-card-heading"><div><h3>{title}</h3><p>{description}</p></div><button className="mp-icon-button" onClick={() => setTick(value => value + 1)} aria-label={`重播${title}`} title="重播"><RotateCcw size={15} /></button></div>
    <div className="mp-entry-stage" key={`${revision}:${tick}`}>{children}</div>
  </article>;
}

// Reuse production panel and item styles; only the gallery anchor is local.
function MenuPopover({ index }) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState(null);
  const [tags, setTags] = useState([]);
  const triggerRef = useRef(null);
  const panelRef = useRef(null);
  const id = useId();
  const [title, panelClass] = menus[index];
  const dialog = index === 4 || index === 5;
  const close = () => { setOpen(false); triggerRef.current?.focus(); };

  useLayoutEffect(() => {
    if (!open) return undefined;
    const place = () => {
      const anchor = triggerRef.current.getBoundingClientRect();
      const panel = panelRef.current;
      const width = Math.min(index === 5 ? 260 : 220, window.innerWidth - 16);
      const available = window.innerHeight - 16;
      const height = Math.min(panel.scrollHeight, available);
      const below = window.innerHeight - anchor.bottom - 12;
      const above = anchor.top - 12;
      const top = below >= height || below >= above
        ? Math.min(anchor.bottom + 4, window.innerHeight - height - 8)
        : Math.max(8, anchor.top - height - 4);
      setPosition({ position: 'fixed', inset: 'auto', left: Math.max(8, Math.min(anchor.left, window.innerWidth - width - 8)), top, width, maxHeight: available, zIndex: 2400, visibility: 'visible' });
    };
    place();
    const observer = new ResizeObserver(place);
    observer.observe(panelRef.current);
    window.addEventListener('resize', place);
    window.addEventListener('scroll', place, true);
    return () => { observer.disconnect(); window.removeEventListener('resize', place); window.removeEventListener('scroll', place, true); };
  }, [open, index]);

  const positioned = Boolean(position);
  useLayoutEffect(() => {
    if (open && positioned) panelRef.current?.querySelector('button, input')?.focus();
  }, [open, positioned]);

  useEffect(() => {
    if (!open) return undefined;
    const outside = event => {
      if (!triggerRef.current?.contains(event.target) && !panelRef.current?.contains(event.target)) setOpen(false);
    };
    document.addEventListener('pointerdown', outside);
    return () => document.removeEventListener('pointerdown', outside);
  }, [open]);

  const onKeyDown = event => {
    if (event.key === 'Escape') { event.preventDefault(); close(); }
    if (event.key === 'Tab') { setOpen(false); triggerRef.current?.focus(); }
    if (dialog || !['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const items = [...panelRef.current.querySelectorAll('button')];
    const current = items.indexOf(document.activeElement);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? items.length - 1 : (current + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length;
    items[next]?.focus();
  };
  const action = (label, Icon, className = '') => <button type="button" role={dialog ? undefined : 'menuitem'} className={className} onClick={close}><Icon size={16} aria-hidden="true" /><span>{label}</span></button>;

  return <>
    <button ref={triggerRef} className="v3-custom-model-select-trigger" aria-haspopup={dialog ? 'dialog' : 'menu'} aria-expanded={open} aria-controls={open ? id : undefined}
      onClick={() => { setPosition(null); setOpen(value => !value); }}
      onKeyDown={event => { if (event.key === 'ArrowDown') { event.preventDefault(); setOpen(true); } }}>
      <span>展开{title}</span><ChevronDown size={14} className="v3-custom-model-select-chevron" aria-hidden="true" />
    </button>
    {open && createPortal(<div ref={panelRef} id={id} role={dialog ? 'dialog' : 'menu'} aria-label={title} className={index === 6 ? 'v3-profile-popover' : panelClass}
      style={position || { position: 'fixed', width: Math.min(index === 5 ? 260 : 220, window.innerWidth - 16), visibility: 'hidden' }} onKeyDown={onKeyDown}>
      {index === 1 && <>{action('查看能力', FileText)}{action('编辑能力', Pencil)}</>}
      {index === 2 && <>{action('制作分享图', FileText)}{action('复制对话', Copy)}</>}
      {index === 3 && <>{action('个人设置', Settings, 'v3-popover-item')}<AgentSettingsMenu onManage={close} onAdd={close} /></>}
      {index === 4 && <>{action('项目资料.pdf', FileText, 'cc-artifact-picker-item')}{action('会议记录.md', FileText, 'cc-artifact-picker-item')}{action('查看全部历史文件', FileText, 'cc-artifact-picker-all')}</>}
      {index === 5 && <fieldset className="cloud-artifact-filter-section cloud-artifact-filter-tags-section"><legend>标签筛选</legend><div className="cloud-artifact-filter-tag-list">
        {['项目资料', '会议记录'].map((tag, i) => <div key={tag} className={`cloud-artifact-filter-tag-item${tags.includes(tag) ? ' is-selected' : ''}`}><label className="cloud-artifact-filter-tag-choice"><input type="checkbox" checked={tags.includes(tag)} onChange={() => setTags(value => value.includes(tag) ? value.filter(item => item !== tag) : [...value, tag])} /><span className="cloud-artifact-filter-check" aria-hidden="true">{tags.includes(tag) && <Check size={12} />}</span><span className="cloud-artifact-filter-tag-name">{tag}</span><span className="cloud-artifact-filter-tag-count">{i + 2}</span></label></div>)}
      </div></fieldset>}
      {index === 6 && <AgentSettingsMenu onManage={close} onAdd={close} />}
    </div>, document.body)}
  </>;
}

function MenuExamples() {
  const [menu, setMenu] = useState('0');
  const [model, setModel] = useState('default');
  return <div className="mp-menu-controls">
    <CustomSelect ariaLabel="选择菜单动效" value={menu} onValueChange={setMenu} density="compact">
      {menus.map(([title], index) => <option key={title} value={index}>{title}</option>)}
    </CustomSelect>
    {menu === '0' ? <CustomSelect ariaLabel="模型菜单预览" value={model} onValueChange={setModel}>
      <option value="default">默认模型</option><option value="fast">快速模型</option><option value="reasoning">推理模型</option>
    </CustomSelect> : <MenuPopover key={menu} index={Number(menu)} />}
  </div>;
}

function EntryExamples({ revision, theme }) {
  const feedback = useFeedback();
  return <div className="mp-entry-grid">
    <ReplayCard title="菜单与浮层" description="选择类型，再点击下方入口展开" revision={revision}>
      <MenuExamples key={theme} />
    </ReplayCard>
    <ReplayCard title="确认弹窗与通知" description="打开真实弹窗，查看通知入场" revision={revision}>
      <button className="mp-button" onClick={() => { void feedback.confirm({ title: '这是确认弹窗', message: '观察弹窗出现时的轻微上浮与淡入。', confirmLabel: '看到了', cancelLabel: '关闭' }); }}>打开确认弹窗</button>
      <button className="mp-button" onClick={() => feedback.notify({ tone: 'success', message: '内容已更新', duration: 3000 })}>显示通知</button>
    </ReplayCard>
    <ReplayCard title="详情弹窗" description="SkillHub 详情与发布弹窗" revision={revision}>
      <div className="cc-skillhub-detail-dialog mp-detail"><FileText size={20} /><strong>能力详情</strong><p>内容轻轻出现，保持原有位置。</p><span className="mp-detail-line" /></div>
    </ReplayCard>
    <ReplayCard title="手机文件面板" description="窄屏下的文件预览入场" revision={revision}>
      <iframe className="mp-sheet-frame" src={`/motion-preview.html?surface=sheet&theme=${theme}`} title="手机文件面板动效" />
    </ReplayCard>
    <ReplayCard title="内容切换" description="应用结果与 Agent 设置内容" revision={revision}>
      <div className="cc-apps-results mp-content-example"><span className="mp-content-icon"><FileText size={20} /></span><strong>项目资料</strong><p>内容在原位置淡入</p><div className="mp-result-lines"><i /><i /></div></div>
    </ReplayCard>
    <ReplayCard title="消息与任务完成" description="新文字出现、步骤完成反馈" revision={revision}>
      <div className="mp-message-example"><span className="cc-reply-new-text">资料已经整理好了，你可以继续查看。</span><div className="v3-wpi-plan-step completed is-just-completed"><CheckCheck size={18} /><span>整理项目资料</span></div></div>
    </ReplayCard>
    <ReplayCard title="下载反馈" description="下载提示出现时的轻淡入" revision={revision}>
      <div className="catsco-pwa-download-notice"><Check size={17} className="cc-download-notice-icon" /><span className="cc-download-notice-message"><strong>文件已准备好</strong><span className="cc-download-notice-filename">项目资料.pdf</span></span></div>
    </ReplayCard>
    <ReplayCard title="悬浮提示" description="上下两个方向的提示入场" revision={revision}>
      <div className="mp-tooltip-stage"><span className="cc-app-tooltip is-positioned is-top">上方提示</span><span className="mp-tooltip-anchor"><Monitor size={19} /></span><span className="cc-app-tooltip is-positioned is-bottom">下方提示</span></div>
    </ReplayCard>
  </div>;
}

function MobileSheet() {
  applyTheme(new URLSearchParams(window.location.search).get('theme') || 'light');
  return <div className="mp-mobile-scene"><section className="v3-file-preview-panel mp-mobile-sheet"><div className="mp-sheet-handle" /><strong><FileText size={17} /> 项目资料.pdf</strong><div className="mp-result-lines"><i /><i /><i /></div></section></div>;
}

function App() {
  const [theme, setTheme] = useState('light');
  const [paused, setPaused] = useState(false);
  const [revision, setRevision] = useState(0);
  const [allComplete, setAllComplete] = useState(0);
  useEffect(() => { applyTheme(theme); }, [theme]);
  const replayAll = () => { setAllComplete(0); setRevision(value => value + 1); };
  return <main className={`motion-preview${paused ? ' is-paused' : ''}`}>
    <header className="mp-header"><div><a href="/">CatsCo</a><h1>动效总览</h1><p>以“正在读取对话”为基准，查看加载、完成衔接与界面入场。 <a href="/icon-preview.html">查看图标总览 →</a> · <a href="/icon-preview.html?tab=optimized">查看本轮按钮优化 →</a></p></div>
      <div className="mp-theme-controls" aria-label="预览主题">{themes.map(([value, label, Icon]) => <button key={value} aria-pressed={theme === value} onClick={() => setTheme(value)}><Icon size={14} />{label}</button>)}</div>
    </header>
    <section aria-labelledby="mp-loading-title"><div className="mp-section-heading"><div><h2 id="mp-loading-title">加载与完成</h2><p>点击“模拟完成”，查看加载图形如何交接给内容。</p></div><div className="mp-actions"><button className="mp-button" onClick={() => setPaused(value => !value)} aria-pressed={paused}>{paused ? <Play size={14} /> : <Pause size={14} />}{paused ? '继续循环' : '暂停循环'}</button><button className="mp-button" onClick={replayAll}><RotateCcw size={14} />全部重播</button><button className="mp-button" onClick={() => setAllComplete(value => value + 1)}><Check size={14} />全部完成</button></div></div>
      <div className="mp-loader-grid">{loaders.map(item => <LoaderCard key={item.id} item={item} allComplete={allComplete} revision={revision} />)}</div>
    </section>
    <section aria-labelledby="mp-entry-title"><div className="mp-section-heading"><div><h2 id="mp-entry-title">界面入场与反馈</h2><p>点击各卡片右上角重播，可以反复比较。</p></div></div><EntryExamples revision={revision} theme={theme} /></section>
    <footer className="mp-footer">跟随系统的“减少动态效果”偏好。预览中的内容仅用于展示。</footer>
  </main>;
}

const sheet = new URLSearchParams(window.location.search).get('surface') === 'sheet';
if (sheet) document.documentElement.classList.add('mp-sheet-document');
createRoot(document.getElementById('root')).render(sheet ? <MobileSheet /> : <FeedbackProvider><App /></FeedbackProvider>);
