import React, { useState } from 'react';
import { AppWindow, ChevronDown, ChevronRight, LoaderCircle, Package, Plus } from 'lucide-react';
import ChatMessage from '../widgets/chat-message';
import CustomSelect from '../widgets/custom-select';
import './button-polish-preview.css';

function Comparison({ title, description, before, after }) {
  return <article className="bp-comparison"><header><h3>{title}</h3><p>{description}</p></header><div className="bp-columns">
    <section className="bp-before" aria-label={`${title}修改前`}><h4>修改前</h4>{before}</section>
    <section className="bp-after" aria-label={`${title}优化后`}><h4>优化后 · 已接入</h4>{after}</section>
  </div></article>;
}

function BusyExamples({ before = false }) {
  return <div className="bp-busy-examples">{[
    ['模型切换', 'v3-model-switch-spinner', 14, '850ms'],
    ['保存设置', 'cc-agent-save-spinner', 15, '800ms'],
    ['下载文件', 'cc-download-notice-icon is-spinning', 17, '900ms'],
  ].map(([label, className, size, duration], i) => <div key={label}><span className="bp-busy-icon"><LoaderCircle size={size} className={before ? 'bp-legacy-spinner' : className} style={before ? { animationDuration: duration, animationDirection: i === 2 ? 'reverse' : 'normal', color: i === 1 ? 'var(--cc-text-secondary)' : 'var(--cc-accent)' } : undefined} /></span><span>{label}</span><small>{before ? `${duration} · ${i === 2 ? '反向' : '正向'}` : '900ms · 正向'}</small></div>)}</div>;
}

function DisclosureExample({ before = false }) {
  const [open, setOpen] = useState(false);
  const [value, setValue] = useState('default');
  return <div className="bp-disclosures">
    {before ? <button type="button" className="v3-custom-model-select-trigger" aria-expanded={open} onClick={() => setOpen(v => !v)}><span>默认模型</span><ChevronDown size={14} style={{ transform: open ? 'rotate(180deg)' : 'none', transition: 'transform 140ms ease' }} /></button>
      : <CustomSelect ariaLabel="优化后的模型选择" value={value} onValueChange={setValue}><option value="default">默认模型</option><option value="fast">快速模型</option><option value="reasoning">推理模型</option></CustomSelect>}
    <button type="button" className="v3-wpi-tool-header is-toggle" aria-expanded={open} onClick={() => setOpen(v => !v)}>
      {before ? (open ? <ChevronDown size={14} /> : <ChevronRight size={14} />) : <ChevronRight size={14} className="cc-disclosure-chevron" aria-hidden="true" />}
      <span>执行详情</span>
    </button>
    {open && <div className="v3-wpi-tool-result bp-disclosure-result">已完成项目资料整理</div>}
  </div>;
}

function SidebarExamples({ before = false }) {
  return <div className="bp-sidebar-examples">{[
    ['新建任务', Plus, 'cc-sidebar-new-task-entry'],
    ['Agent 能力', Package, 'cc-sidebar-skillhub-entry'],
    ['应用', AppWindow, 'cc-sidebar-apps-entry'],
  ].map(([label, Icon, cls], i) => <button key={label} type="button" className={`cc-sidebar-primary ${cls}${before ? ` bp-legacy-hover bp-legacy-hover-${i}` : ''}`}><Icon size={18} aria-hidden="true" />{label}</button>)}</div>;
}

const message = { id: 'button-polish-copy', from_uid: 2, content: '项目资料已整理好。', created_at: '2026-10-27T06:00:00Z' };
const previousMessage = { ...message, id: 'button-polish-copy-before' };
function CopyExample({ before = false }) {
  return <div className="bp-copy-sample"><ChatMessage message={before ? previousMessage : message} isSelf={false} isGroup={false} senderName="CatsCo" /></div>;
}
export default function ButtonPolishPreview() {
  return <div className="bp-preview"><div className="ip-section-head"><div><h2>本轮按钮优化</h2><p>优化后使用当前业务组件与样式。可点击展开和复制，悬停或聚焦侧栏入口。</p></div></div>
    <div className="bp-comparisons">
      <Comparison title="1. 按钮加载" description="模型切换、保存、搜索、连接与下载等圆环统一为 900ms、顺时针。普通加载使用中性色，按钮内的尺寸随所在控件保留。" before={<BusyExamples before />} after={<BusyExamples />} />
      <Comparison title="2. 展开与收起" description="箭头统一使用 180ms 的平滑过渡。下拉选择保留右侧箭头，树形详情保留左侧箭头，切换时不替换图形。" before={<DisclosureExample before />} after={<DisclosureExample />} />
      <Comparison title="3. 侧栏入口" description="新建任务、Agent 能力和应用统一用中性背景与文字明暗反馈，去掉图标倾斜、旋转、上移和放大。" before={<SidebarExamples before />} after={<SidebarExamples />} />
      <Comparison title="4. 复制成功" description="复制与成功图标保持 15px，成功勾轻轻淡入后恢复。两侧均使用消息组件；手机先轻触消息，再点击复制，触控区域保持 44px。" before={<CopyExample before />} after={<CopyExample />} />
    </div>
    <p className="bp-note">刷新与重新生成保留箭头对应的旋转方向；发送、停止、收藏、置顶和连接状态保留各自的含义。页面加载继续使用已确认的对话读取风格。</p>
  </div>;
}
