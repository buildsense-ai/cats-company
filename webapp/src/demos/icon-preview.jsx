import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { createRoot } from 'react-dom/client';
import '@fontsource-variable/inter/wght.css';
import '@fontsource-variable/noto-sans-sc/wght.css';
import '../css/auth-critical.css';
import '../views/workspace-styles';
import '../css/apps-view.css';
import '../css/skillhub-view.css';
import '../css/system-prompt-view.css';
import '../views/artifact-fullscreen-viewer.css';
import '../css/catsco-focus-policy.css';
import Icons from './icon-preview-icons';
import inventory from './icon-preview-data.json';
import VoiceHoldWave from './icon-preview-voice';
import LoadingState from '../widgets/loading-state';
import HistoryLoadingState from '../widgets/history-loading-state';
import { IdentityCat } from '../components/identity-onboarding';
import AgentCapabilityVisualization from '../widgets/agent-capability-visualization';
import Avatar from '../widgets/avatar';
import CatOrb from '../components/CatOrb/CatOrb';
import useDialogBehavior from '../utils/use-dialog-behavior';
import ButtonPolishPreview from './button-polish-preview';
import IconSystemPreview from './icon-system-preview';
import './icon-preview-source.css';
import './icon-preview.css';

const labels = Object.fromEntries(`Activity:活动 AlertCircle:圆形警告 AlertTriangle:三角警告 AppleBrand:苹果系统 AppWindow:应用窗口 ArrowDownUp:排序 ArrowLeft:返回 ArrowRight:向右 ArrowUp:发送向上 ArrowUpCircle:升级 ArrowUpRight:右上跳转 AudioLines:音频 BadgeCheck:认证 Ban:禁用 Bell:通知 BellOff:静音 BellRing:通知设置 Bookmark:标注收藏 BookOpen:知识库 BookOpenCheck:研究 Bot:虚拟员工 Brain:思考 BrainCircuit:推理 BriefcaseBusiness:商务 Cat:猫 ChartNoAxesCombined:数据图表 Check:确认 CheckCircle:圆形完成 CheckCircle2:圆形完成 CheckSquare:方形选择 ChevronDown:展开向下 ChevronLeft:上一项 ChevronRight:展开向右 ChevronUp:收起向上 Circle:圆形 CircleAlert:圆形警告 CircleDot:选中状态 Clipboard:办公剪贴板 ClipboardCheck:审核检查 Cloud:云端 CloudOff:离线云端 Code2:开发 Copy:复制 CornerUpLeft:回复 Cpu:计算资源 CreditCard:套餐支付 Download:下载 Droplets:液态主题 Expand:展开 ExternalLink:外部链接 Eye:显示 EyeOff:隐藏 FileCheck2:文件确认 FileCode2:代码文件 FileText:文档 Fingerprint:身份标识 FlaskConical:测试 Folder:项目文件夹 FolderOpen:打开项目 FolderPlus:新建项目 Frown:问题反馈 Gift:礼物邀请 Globe:网络 Globe2:浏览器 GraduationCap:教育 History:历史 Image:图片 ImageDown:导出图片 Info:信息 KeyRound:密钥 Landmark:金融 Laptop:电脑 ListChecks:任务清单 ListFilter:筛选 Loader2:加载圆环 LoaderCircle:加载圆环 Lock:锁定 LockKeyhole:权限锁 LogOut:退出 Maximize2:放大 MessageSquare:对话 MessageSquarePlus:新建反馈 MessageSquareText:消息文本 Mic:语音 Minimize2:缩小 Minus:减号 Monitor:桌面设备 MonitorCheck:通知测试 Moon:深色主题 MoreHorizontal:更多 Package:能力包 PackageMinus:移除能力 Palette:设计 PanelLeftClose:收起侧栏 PanelLeftOpen:展开侧栏 PanelsTopLeft:工作区 Pencil:编辑 PencilLine:编辑线条 PenLine:写作 Pin:置顶 Play:播放 Plus:添加 PlusCircle:圆形添加 Puzzle:扩展 QrCode:二维码 ReceiptText:账单 RefreshCw:刷新 RotateCcw:重新生成回滚 Save:保存 Scan:适应画面 Search:搜索 Send:发送 Server:服务器 Settings:设置 Settings2:参数设置 Share2:分享 ShieldAlert:安全警告 ShieldCheck:安全审核 Smartphone:手机 Sparkles:能力智能 Square:停止 Star:星标 Sun:浅色主题 Tag:标签 Terminal:终端 TextCursorInput:重命名 Trash2:删除 Undo2:撤销 Unlink:解除连接 Upload:上传 UserPlus:添加联系人 UserRound:用户 Users:多人 UsersRound:群组 UserX:移除用户 Volume2:音量 Wrench:工具 X:关闭 XCircle:圆形关闭 Zap:Agent状态 ZoomIn:放大镜 ZoomOut:缩小镜`.split(' ').map(pair=>pair.split(':')));
const moduleLabels = {
  'tinode-web':'工作台 侧栏', 'sidepanel-view':'侧栏 任务 项目 联系人', 'messages-view':'聊天 对话 文件', 'chat-message':'消息 操作 工作过程', 'chat-composer':'输入栏 语音', 'profile-editor':'设置 主题 个人', 'skillhub-content':'能力 SkillHub', 'apps-view':'应用', 'desktop-connect-modal':'电脑 连接 下载', 'catsco-desktop-shared':'电脑 系统 下载', 'cloud-artifacts-panel':'文件 产物 云端', 'system-prompt-view':'系统提示词', 'knowledge-wiki-view':'知识库', 'relay-access-modal':'套餐 密钥', 'notification-settings':'通知', 'agent-store-modal':'员工 设置',
};
const moduleName=file=>moduleLabels[file.split('/').pop().replace(/\.(jsx|js)$/,'')]||file.split('/').pop().replace(/\.(jsx|js)$/,'');
const unique=(values)=>[...new Set(values)];
function variants(icon) {
  const groups=new Map();
  for(const usage of icon.usages){
    for(const variant of usage.propVariants||[usage.props]){
      const props={size:24,strokeWidth:2,...icon.defaultProps,...variant};
      const key=JSON.stringify(props);
      if(!groups.has(key))groups.set(key,{props,count:0});groups.get(key).count++;
    }
  }
  return [...groups.values()].sort((a,b)=>b.count-a.count);
}
const themeOptions=[['light','浅色'],['dark','深色'],['liquid','液态浅色'],['liquid-green','液态深色']];
function ThemeControls({theme,setTheme}) {return <div className="ip-themes" aria-label="预览主题">{themeOptions.map(([id,label])=><button key={id} aria-pressed={theme===id} onClick={()=>setTheme(id)}>{label}</button>)}</div>;}
function Glyph({name,...props}) {const Icon=Icons[name];return Icon?<Icon {...props}/>:null;}

const spinExamples=[
  ['模型切换','Loader2','v3-model-switch-spinner',14,'900ms','正向'],
  ['刷新应用','RefreshCw','is-spinning',17,'900ms','正向','cc-apps-refresh'],
  ['刷新能力','RefreshCw','is-spinning',16,'900ms','正向'],
  ['图片历史加载','LoaderCircle','is-spinning',32,'900ms','正向'],
  ['连接桌面端','Loader2','catsco-spin',16,'900ms','正向'],
  ['重新生成消息','RotateCcw','',16,'800ms','反向','v3-message-footer','v3-action-btn v3-regenerate-action is-pending'],
  ['停止工作','LoaderCircle','cc-stop-spinner',18,'900ms','正向'],
  ['保存员工设置','LoaderCircle','cc-agent-save-spinner',15,'900ms','正向'],
  ['通知发送测试','LoaderCircle','oc-spin',15,'900ms','正向','oc-notification-test'],
  ['搜索联系人','LoaderCircle','oc-friend-search-spinner',16,'900ms','正向'],
  ['刷新员工工具','RefreshCw','',18,'900ms','正向','v3-service-agents-header-actions','is-spinning'],
  ['云端操作','RefreshCw','cc-spin',14,'900ms','正向'],
  ['搜索消息','LoaderCircle','cc-spin',18,'900ms','正向','ip-search-context'],
  ['保存系统提示词','LoaderCircle','spin',16,'900ms','正向'],
  ['搜索知识库','LoaderCircle','ip-knowledge-spin',14,'900ms','正向'],
  ['下载文件','LoaderCircle','cc-download-notice-icon is-spinning',17,'900ms','正向'],
  ['紧凑任务状态','CircleDot','',14,'900ms','正向','cc-compact-task-status running'],
];
const toggleExamples=[
  ['模型展开','ChevronDown','v3-model-status-button','v3-model-menu-chevron',14],
  ['模型选择','ChevronDown','v3-custom-model-select-trigger','v3-custom-model-select-chevron',14],
  ['输入栏附件','Plus','v3-composer-plus','',22],
  ['输入栏 Agent','ChevronDown','v3-agent-picker-button','v3-agent-picker-chevron',14],
  ['侧栏栏目','ChevronRight','cc-section-toggle','',14],
  ['员工设置栏目','ChevronDown','ip-agent-toggle','cc-agent-manage-section-chevron',16],
  ['工具详情','ChevronDown','ip-relay-toggle','',14],
  ['任务步骤展开','ChevronDown','ip-working-toggle','v3-working-chevron',14],
];
function Spin({example}) {
  const [,name,cls,size,,,outer,inner]=example;
  let content=<Glyph name={name} size={size} className={cls}/>;
  if(inner)content=outer==='v3-service-agents-header-actions'?<button type="button" className={inner} aria-label="刷新员工工具预览">{content}</button>:<span className={inner}>{content}</span>;
  return <div className={`ip-icon-context ${outer||''}`}>{content}</div>;
}
function Toggle({example}) {
  const [title,name,buttonClass,cls,size]=example;const [open,setOpen]=useState(false);
  const trailing=['模型展开','模型选择','输入栏 Agent','员工设置栏目'].includes(title);
  const control=<button className={`ip-toggle-button ${buttonClass}`} aria-expanded={open} onClick={()=>setOpen(v=>!v)}>{trailing&&<span>{open?'收起':'展开'}</span>}<Glyph name={name} size={size} className={cls}/>{!trailing&&<span>{open?'收起':'展开'}</span>}</button>;
  if(title==='员工设置栏目')return <div className={`cc-agent-manage-section ip-context-shell${open?' is-open':''}`}>{control}</div>;
  if(title==='工具详情')return <details className="relay-access-tool-disclosure ip-context-shell"><summary><span className="relay-access-tool-disclosure-label"><Glyph name={name} size={size}/></span>展开 / 收起</summary><span className="ip-tiny">工具详情</span></details>;
  if(title==='任务步骤展开')return <button className="ip-toggle-button" aria-expanded={open} onClick={()=>setOpen(v=>!v)}><Glyph name="ChevronRight" size={14} className="cc-disclosure-chevron"/><span>{open?'收起':'展开'}</span></button>;
  return control;
}
function AnimatedSurface({paused,children}) {
  const ref=useRef(null);const [snapshot,setSnapshot]=useState(null);
  useLayoutEffect(()=>{if(paused&&snapshot===null)setSnapshot(ref.current?.innerHTML||'');else if(!paused&&snapshot!==null)setSnapshot(null);},[paused,snapshot]);
  return snapshot!==null?<div className="ip-frozen" dangerouslySetInnerHTML={{__html:snapshot}}/>:<div ref={ref}>{children}</div>;
}
function VoiceExample({paused}) {const levelRef=useRef(.5);return <AnimatedSurface paused={paused}><div className="ip-voice"><VoiceHoldWave levelRef={levelRef}/></div></AnimatedSurface>;}
function SelectionExample({name,offLabel,onLabel}){const [active,setActive]=useState(false);return <button className="ip-toggle-button" aria-pressed={active} onClick={()=>setActive(v=>!v)}><Glyph name={name} size={16} fill={active?'currentColor':'none'}/>{active?onLabel:offLabel}</button>;}
function MotionCard({title,description,children,replay=true}) {
  const [revision,setRevision]=useState(0);
  return <article className="ip-motion-card"><header><h3>{title}</h3>{replay&&<button aria-label={`重播${title}`} onClick={()=>setRevision(v=>v+1)}><Glyph name="RotateCcw" size={14}/></button>}</header><div className="ip-motion-stage" key={revision}>{children}</div><p>{description}</p></article>;
}
function AnimatedIcons({paused}) {
  return <>
    <div className="ip-section-head"><div><h2>动态图标与状态</h2><p>保留各入口当前的转向、速度、尺寸与颜色。展开按钮可点击，侧栏入口可悬停。</p></div></div>
    <div className="ip-motion-grid">
      {spinExamples.map(example=><MotionCard key={example[0]} title={example[0]} description={`${example[1]} · ${example[3]}px · ${example[4]} · ${example[5]}`}><Spin example={example}/></MotionCard>)}
      <MotionCard title="全屏文件加载" description="CSS 圆环 · 22px · 900ms · 正向"><span className="artifact-fullscreen-viewer-spinner"/></MotionCard>
      {toggleExamples.map(example=><MotionCard key={example[0]} title={example[0]} description="点击展开 / 收起查看过渡" replay={false}><Toggle example={example}/></MotionCard>)}
      {[['新建任务','Plus','cc-sidebar-new-task-entry'],['Agent 能力','Package','cc-sidebar-skillhub-entry'],['应用入口','AppWindow','cc-sidebar-apps-entry']].map(([title,name,cls])=><MotionCard key={title} title={title} description="鼠标悬停或键盘聚焦" replay={false}><button className={`cc-sidebar-primary ip-hover-entry ${cls}`}><Glyph name={name} size={18}/>{title}</button></MotionCard>)}
      <MotionCard title="任务步骤完成" description="Check · 完成时短暂淡入"><span className="v3-wpi-plan-step is-just-completed"><Glyph name="Check" size={18}/></span></MotionCard>
      <MotionCard title="收藏应用" description="点击查看线框与填充切换" replay={false}><SelectionExample name="Star" offLabel="收藏" onLabel="已收藏"/></MotionCard>
      <MotionCard title="置顶任务" description="点击查看线框与填充切换" replay={false}><SelectionExample name="Pin" offLabel="置顶" onLabel="已置顶"/></MotionCard>
      <MotionCard title="Agent 正在工作" description="头像呼吸 · 1800ms"><div className="v3-message is-agent is-working ip-avatar-context"><span className="v3-avatar"><Glyph name="Bot" size={20}/></span></div></MotionCard>
      <MotionCard title="连接状态" description="在线 / 重连脉冲 / 离线"><div className="ip-status-row">{[['online','在线'],['reconnecting','重连'],['offline','离线']].map(([cls,label])=><span key={cls}><i className={`v3-status-dot ${cls}`}/>{label}</span>)}</div></MotionCard>
      <MotionCard title="正在输入" description="文字脉冲 · 1200ms"><span className="v3-peer-typing-label">正在输入…</span></MotionCard>
      <MotionCard title="流式输出光标" description="光标闪烁 · 1000ms"><span className="oc-streaming-cursor">▍</span></MotionCard>
      <MotionCard title="液态浅色工作状态" description="三点依次脉冲，仅在液态浅色中使用"><div className="ip-liquid-sample" data-theme="liquid"><span className="cc-working-dots"><i/><i/><i/></span></div></MotionCard>
      <MotionCard title="旧版输入提示" description="源码中保留的三点样式"><div className="oc-typing-dots"><span/><span/><span/></div></MotionCard>
      <MotionCard title="语音输入" description="实际输入栏波形，使用模拟音量"><VoiceExample paused={paused}/></MotionCard>
    </div>
  </>;
}
const loadingKinds=[['chat','读取对话'],['apps','应用'],['skills','能力'],['document','文件 / PDF / 表格'],['image','图片'],['connect','电脑与连接'],['tasks','历史任务'],['startup','工作台启动']];
function Loaders(){return <><div className="ip-section-head"><div><h2>加载图形</h2><p>沿用当前实际加载组件，包含刚刚确认的层叠窗口与对话气泡。</p></div><a href="/motion-preview.html">查看完成衔接 →</a></div><div className="ip-loading-grid">{loadingKinds.map(([kind,title])=><MotionCard key={kind} title={title} description="循环播放">{kind==='chat'?<HistoryLoadingState/>:kind==='startup'?<span className="cc-workspace-loading-indicator"/>:<LoadingState kind={kind}/>}</MotionCard>)}</div></>;}

function OtherGraphics({paused}) {
  const [catName,setCatName]=useState('CatsCo');
  const [orbPlaying,setOrbPlaying]=useState(false);
  return <><div className="ip-section-head"><div><h2>品牌、插画与其他标记</h2><p>这些图形与操作图标分别展示，保留各自的造型和动态效果。</p></div></div>
    <div className="ip-motion-grid ip-other-grid">
      <MotionCard title="品牌图形" description="真实主题样式"><span className="catsco-brand-mark ip-brand"/></MotionCard>
      <MotionCard title="品牌文字" description="主题自适应字标"><span className="catsco-wordmark ip-wordmark"/></MotionCard>
      {['favicon.svg','pwa-192x192.png','pwa-512x512.png','pwa-maskable-512x512.png','pwa-notification-badge-96x96.png'].map(file=><MotionCard key={file} title={file==='favicon.svg'?'浏览器图标':file.includes('badge')?'系统通知标记':file.includes('maskable')?'可遮罩应用图标':`应用图标 ${file.includes('192')?'192':'512'}`} description={`Catsco 品牌文字 · ${file}`}><div className={`ip-asset${file.includes('badge')?' is-badge':file.includes('maskable')?' is-maskable':''}`}><img src={`/${file}`} alt={file}/></div></MotionCard>)}
      <MotionCard title="身份小猫" description="实际 SVG · 摆尾、眨眼、名字变化"><AnimatedSurface paused={paused}><div className="ip-cat"><IdentityCat name={catName}/></div></AnimatedSurface><button className="ip-cat-change" onClick={()=>setCatName(v=>v==='CatsCo'?'小夏':'CatsCo')}>换个名字</button></MotionCard>
      <MotionCard title="头像回退" description="首字母 / 群 / Bot / 身份小猫"><div className="ip-avatar-row"><Avatar name="Alex" size={36}/><Avatar name="群聊" isGroup size={36}/><Avatar name="Bot" isBot size={36}/><Avatar name="CatsCo" identityFallback size={36}/></div></MotionCard>
      <MotionCard title="字符图标" description="源码中还存在这些文字形状"><div className="ip-character-row"><span>✓</span><span>!</span><span>▼</span><span>▶</span><span>▸</span><span>▾</span></div></MotionCard>
      <MotionCard title="旧内容块图标" description="工具调用 / 成功 / 失败使用 Emoji"><div className="ip-character-row"><span>🔧</span><span>✅</span><span>❌</span></div></MotionCard>
      <MotionCard title="能力结构图" description="实际 SVG · 可悬停、聚焦或点击扇区"><div className="ip-capability"><AgentCapabilityVisualization agentName="CatsCo" role="general" skills={[{name:'浏览器自动化'},{name:'资料研究'}]} compact/></div></MotionCard>
      <MotionCard title="源码保留的猫形光球" description="未接入当前业务页面 · WebGL · 可悬停" replay={false}><div className="ip-orb">{orbPlaying&&!paused?<CatOrb/>:<button className="ip-toggle-button" disabled={paused} onClick={()=>setOrbPlaying(true)}>{paused?'已暂停':'播放动效'}</button>}</div></MotionCard>
    </div>
  </>;
}

function IconDetail({icon,onClose}) {
  const ref=useRef(null);useDialogBehavior(ref,{onClose});
  const samples=variants(icon);const sizes=unique(samples.map(v=>v.props.size)).sort((a,b)=>a-b);
  return <div className="ip-dialog-backdrop" onMouseDown={e=>{if(e.target===e.currentTarget)onClose();}}><section ref={ref} tabIndex={-1} role="dialog" aria-modal="true" aria-labelledby="ip-detail-title" className="ip-dialog"><header><div><h2 id="ip-detail-title">{labels[icon.name]||icon.name}</h2><p>{icon.name}</p></div><button onClick={onClose} aria-label="关闭图标详情"><Glyph name="X" size={18}/></button></header>
    <h3>源码中的尺寸与描边</h3><div className="ip-variant-row">{samples.length?samples.map(({props,count},index)=><div key={index}><Glyph name={icon.name} {...props}/><span>{props.size}px · 描边 {props.strokeWidth}</span><small>{props.fill&&props.fill!=='none'?'含填充 · ':''}{count} 处引用</small></div>):<p>配置中的间接引用或仅保留导入，按默认图形展示。</p>}</div>
    <h3>使用位置</h3><p className="ip-tiny">图形使用原始属性；所在页面的主题、容器颜色和 CSS 尺寸覆盖，仍以实际页面为准。引用清单也包含配置表和待清理的导入。</p><div className="ip-source-list">{icon.usages.map((usage,index)=><div key={index}><span>{moduleName(usage.file)}</span><code>{usage.file}:{usage.line}</code><small>{usage.props.size??'默认 24'}px · 描边 {usage.props.strokeWidth??2}{usage.classes.length?` · ${usage.classes.join(' / ')}`:''}</small></div>)}{!icon.usages.length&&icon.files.map(file=><div key={file}><span>{moduleName(file)}</span><code>{file}</code></div>)}</div>
    <footer>{sizes.length} 种显式尺寸 · {icon.files.length} 个引用文件</footer>
  </section></div>;
}

function App(){
  const [theme,setTheme]=useState('light');const [tab,setTab]=useState(()=>{const requested=new URLSearchParams(window.location.search).get('tab');return ['system','optimized','all','special','motion','loading','other'].includes(requested)?requested:'all';});const [query,setQuery]=useState('');const [sizeMode,setSizeMode]=useState('source');const [paused,setPaused]=useState(false);const [selected,setSelected]=useState(null);const [revision,setRevision]=useState(0);
  const [reduced,setReduced]=useState(()=>window.matchMedia('(prefers-reduced-motion: reduce)').matches);
  useEffect(()=>{const media=window.matchMedia('(prefers-reduced-motion: reduce)');const update=()=>setReduced(media.matches);media.addEventListener('change',update);return()=>media.removeEventListener('change',update);},[]);
  useEffect(()=>{document.documentElement.dataset.theme=theme==='liquid-green'?'liquid':theme;document.documentElement.dataset.liquidVariant=theme==='liquid-green'?'green':'';},[theme]);
  const special=icon=>variants(icon).some(v=>v.props.strokeWidth!==2);
  const filtered=useMemo(()=>inventory.icons.filter(icon=>(tab!=='special'||special(icon))&&`${icon.name} ${labels[icon.name]||''} ${icon.files.map(moduleName).join(' ')} ${icon.files.join(' ')}`.toLowerCase().includes(query.trim().toLowerCase())),[tab,query]);
  const tabs=[['system','统一规范'],['optimized','本轮优化 4'],['all',`全部图标 ${inventory.icons.length}`],['special',`特殊描边 ${inventory.icons.filter(special).length}`],['motion','动态图标与状态'],['loading','加载图形 8'],['other','品牌与其他']];
  return <main className={`icon-preview${paused?' is-paused':''}`}>
    <header className="ip-header"><div><a href="/">CatsCo</a><h1>图标总览</h1><p>查看当前前端的图形、尺寸、描边与动态效果。</p></div><div className="ip-header-actions"><ThemeControls theme={theme} setTheme={setTheme}/><a href="/motion-preview.html">动效总览 <Glyph name="ArrowUpRight" size={14}/></a></div></header>
    {tab==='system'?<div className="ip-overview"><span><strong>{inventory.icons.length}</strong> 个图标展示</span><span><strong>3</strong> 档功能尺寸</span><span><strong>1</strong> 套功能描边</span><p>统一方案预览 · 含语义合并、交互与动效样例。原始业务属性保留在“全部图标”中。</p></div>:<div className="ip-overview"><span><strong>{inventory.icons.length}</strong> 个图标</span><span><strong>{unique(inventory.icons.flatMap(i=>i.files)).length}</strong> 个引用文件</span><span><strong>{unique(inventory.icons.flatMap(i=>variants(i).map(v=>v.props.strokeWidth))).length}</strong> 种描边值</span><p>清单来自当前业务源码，排除测试和独立演示页。点击图标可查看使用位置与原始属性。</p></div>}
    <div className="ip-toolbar"><nav aria-label="图标分组">{tabs.map(([id,label])=><button key={id} aria-pressed={tab===id} onClick={()=>setTab(id)}>{label}</button>)}</nav><div className="ip-playback"><button onClick={()=>setPaused(v=>!v)} aria-pressed={paused}><Glyph name={paused?'Play':'Square'} size={13}/>{paused?'继续播放':'暂停动效'}</button><button onClick={()=>setRevision(v=>v+1)}><Glyph name="RotateCcw" size={14}/>重播</button></div></div>
    {(tab==='all'||tab==='special')?<>
      <div className="ip-search-row"><label><Glyph name="Search" size={16}/><input type="search" placeholder="搜索图标名称、中文含义或页面" aria-label="搜索图标" value={query} onChange={e=>setQuery(e.target.value)}/>{query&&<button aria-label="清空搜索" onClick={()=>setQuery('')}><Glyph name="X" size={14}/></button>}</label><div className="ip-size-switch" aria-label="图标显示尺寸"><button aria-pressed={sizeMode==='source'} onClick={()=>setSizeMode('source')}>源码尺寸</button><button aria-pressed={sizeMode==='compare'} onClick={()=>setSizeMode('compare')}>统一 24px 对比</button></div><span>{filtered.length} 个图标</span></div>
      <div className="ip-icon-grid">{filtered.map(icon=>{
        const samples=variants(icon);const primary=samples[0]?.props||{size:24,strokeWidth:2,...icon.defaultProps};const sizes=unique(samples.map(v=>v.props.size)).sort((a,b)=>a-b);const strokes=unique(samples.map(v=>v.props.strokeWidth)).sort();
        return <button key={icon.name} className="ip-icon-card" onClick={()=>setSelected(icon)} aria-label={`查看 ${icon.name} ${labels[icon.name]||''}`}><span className="ip-icon-stage"><Glyph name={icon.name} {...primary} {...(sizeMode==='compare'?{size:24}:{})}/></span><strong>{labels[icon.name]||icon.name}</strong><span className="ip-icon-name">{icon.name}</span><span className="ip-icon-meta">{sizes.length?`${sizes.join(' / ')}px`:'间接引用 / 导入'}<br/>描边 {strokes.length?strokes.join(' / '):`默认 ${icon.defaultProps?.strokeWidth??2}`}</span><span className="ip-icon-module">{moduleName(icon.files[0])}{icon.files.length>1?` 等 ${icon.files.length} 处`:''}</span></button>;
      })}</div>{!filtered.length&&<p className="ip-empty">没有找到匹配的图标。</p>}
    </>:<div key={`${tab}:${revision}`} className="ip-examples">{tab==='system'?<IconSystemPreview inventory={inventory} labels={labels}/>:tab==='optimized'?<ButtonPolishPreview/>:tab==='motion'?<AnimatedIcons paused={paused||reduced}/>:tab==='loading'?<Loaders/>:<OtherGraphics paused={paused||reduced}/>}</div>}
    <footer className="ip-footer">源码尺寸保留当前使用属性；统一 24px 只改变展示大小。动态图标沿用业务 CSS 和组件，跟随系统“减少动态效果”偏好。</footer>
    {selected&&<IconDetail icon={selected} onClose={()=>setSelected(null)}/>}
  </main>;
}
createRoot(document.getElementById('root')).render(<App/>);
