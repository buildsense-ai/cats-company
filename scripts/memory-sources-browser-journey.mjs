// Requires the dev preview on port 5195 and the user's opencli browser.
// All displayed bodies are synthetic and use the real ChatMessage/Canvas UI.
import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
const base = process.env.MEMORY_PREVIEW_URL || 'http://127.0.0.1:5195/memory-sources-preview.html';
const dir = process.env.MEMORY_ARTIFACT_DIR || '/tmp/catsco-branch-sources';
const session = 'branch-sources';
const evidence = [];
mkdirSync(dir, { recursive: true });
function cli(...args) { return execFileSync('opencli', ['browser', session, ...args], { encoding: 'utf8', timeout: 30000, stdio: ['ignore', 'pipe', 'pipe'] }); }
function result(output) { return JSON.parse(output.slice(output.indexOf('{'), output.lastIndexOf('}') + 1)); }
function check(name, expression) { const value = result(cli('eval', expression)); evidence.push({ name, ...value }); if (!value.ok) throw new Error(`${name}: ${JSON.stringify(value)}`); console.log(`PASS ${name}`); return value; }
function open(query = '') { cli('open', `${base}${query}`, '--window', 'foreground'); cli('wait', 'selector', '.cc-diary-preview'); }
function shot(name, width = 1280, height = 900) { cli('screenshot', resolve(dir, name), '--width', String(width), '--height', String(height)); }
function paint() { const size = result(cli('eval', '({width:innerWidth,height:innerHeight})')); shot('canvas-current.png', size.width, size.height); }
// Targeted DOM button actions avoid opencli's background CDP input losing
// activation after emulated screenshot sizes. The real React handlers run.
function click(selector) { cli('eval', `(()=>{const node=document.querySelector(${JSON.stringify(selector)});if(!node)throw new Error('missing control');node.focus();node.click();return {clicked:true};})()`); }
function list() { click('button[aria-label="记忆列表视图"]'); }
function first() { list(); click('.cc-memory-source'); }
try {
  open(); cli('wait', 'selector', 'canvas');
  check('shared renderer and body-free overview', `({ok:document.querySelector('canvas').getAttribute('aria-label').includes('18 个节点，4 条真实关系')&&document.querySelectorAll('.cc-daily-notes-network-controls button').length===3&&window.__memoryPreviewLog.length===0&&!document.body.innerText.includes('越淡越旧'),label:document.querySelector('canvas').getAttribute('aria-label'),eagerReads:window.__memoryPreviewLog.length})`);
  shot('desktop-graph.png');
  cli('eval', '({stored:!!(window.__beforeZoom=document.querySelector("canvas").toDataURL())})'); click('button[aria-label="放大"]'); paint();
  check('shared camera zoom', `({ok:document.querySelector('canvas').toDataURL()!==window.__beforeZoom})`);
  click('button[aria-label="显示全部"]');
  shot('mobile-graph.png', 390, 844);
  check('mobile canvas has both source clusters', `(()=>{const c=document.querySelector('canvas'),d=c.getContext('2d').getImageData(0,0,c.width,c.height).data;let green=0,blue=0;for(let i=0;i<d.length;i+=4){if(d[i+1]>d[i]+20)green++;if(d[i+2]>d[i]+20&&d[i+2]>d[i+1]+5)blue++;}return {ok:green>20&&blue>20&&document.documentElement.scrollWidth<=innerWidth,green,blue,width:innerWidth,overflow:document.documentElement.scrollWidth>innerWidth};})()`);
  first(); cli('wait', 'selector', '.cc-diary-prose');
  check('authorized pinned detail includes complete qualifiers', `({ok:document.querySelector('[aria-label="记忆来源详情"]').innerText.includes('仅当旧版本仍能读取')&&document.querySelector('[aria-label="记忆来源详情"]').innerText.includes('外部系统的回滚行为尚未验证')&&window.__memoryPreviewLog.length===1&&window.__memoryPreviewLog[0].kind==='projection'&&window.__memoryPreviewLog[0].projection_ref.startsWith('ref_'),requests:window.__memoryPreviewLog})`);
  shot('mobile-reader.png', 390, 844); shot('desktop-reader.png'); cli('keys', 'Escape');
  check('keyboard close restores source focus', `({ok:!document.querySelector('.cc-diary-reader')&&document.activeElement.classList.contains('cc-memory-source'),focus:document.activeElement.className})`);
  open('?mode=saved'); cli('wait', 'selector', 'canvas');
  check('stored block replay', `({ok:document.querySelector('canvas').getAttribute('aria-label').includes('18 个节点，4 条真实关系')&&document.body.innerText.includes('已加入主 Agent 上下文')})`);
  open('?mode=combined'); cli('eval', `(()=>{const tab=document.querySelector('[role=tab][id$="-memory"]');tab.focus();tab.click();return {clicked:true};})()`);
  check('one turn sources with Diary and Memory tabs', `({ok:document.querySelector('[aria-label="本轮来源"]')&&document.querySelector('[role="tab"][aria-selected="true"]').innerText==='记忆探索'&&document.querySelector('[aria-label="记忆探索"]')&&document.querySelectorAll('[role="tab"]').length===2})`);
  shot('desktop-combined.png'); cli('eval', `(()=>{document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowLeft',bubbles:true}));return {key:'ArrowLeft'};})()`);
  check('keyboard source tab navigation', `({ok:document.querySelector('[role="tab"][aria-selected="true"]').innerText==='日记'&&!document.querySelector('[aria-label="记忆探索"]')})`);
  open('?mode=ready');
  check('read is not injection', `({ok:document.body.innerText.includes('等待主 Agent 接收')&&!document.body.innerText.includes('已加入主 Agent 上下文')&&!document.body.innerText.includes('回答中明确引用')})`);
  open('?mode=empty');
  check('no-addition compact state', `({ok:document.body.innerText.includes('本轮未补充记忆')&&!document.querySelector('canvas')})`);
  open('?mode=late');
  check('late result identifies actual consuming turn', `({ok:document.body.innerText.includes('来自上一轮')&&document.body.innerText.includes('本轮实际接收')})`);
  open('?refused=1'); first(); cli('wait', 'selector', '.cc-diary-read-error');
  check('private denial has no cached body', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('无权查看')&&!document.querySelector('.cc-diary-prose')})`); shot('refused-reader.png', 390, 844);
  open('?mismatch=1'); first(); cli('wait', 'selector', '.cc-diary-read-error');
  check('pin mismatch has no cached body', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('返回版本与引用不一致')&&!document.querySelector('.cc-diary-prose')})`);
  open('?mode=unknown');
  check('unknown schema is not rich evidence', `({ok:!document.querySelector('canvas')&&!document.querySelector('.cc-memory-source')})`);
  open('?mode=recorded');
  check('actual runtime capture final-answer marker', `({ok:document.body.innerText.includes('回答中明确引用 1 份')&&document.body.innerText.includes('The next request received the late branch result. ref_')&&document.querySelectorAll('.cc-memory-source').length===9})`);
  open('?theme=dark'); cli('wait', 'selector', 'canvas'); shot('dark-graph.png'); shot('mobile-dark-graph.png', 390, 844); first(); cli('wait', 'selector', '.cc-diary-prose'); shot('mobile-dark-reader.png', 390, 844);
  check('dark reader and responsive surface', `({ok:document.documentElement.dataset.theme==='dark'&&document.documentElement.scrollWidth<=innerWidth&&!!document.querySelector('.cc-diary-prose'),width:innerWidth})`);
  open();
} finally {
  writeFileSync(resolve(dir, 'browser-journey.json'), JSON.stringify({ preview: base, synthetic: true, evidence }, null, 2));
}
console.log(`Browser journey passed; artifacts: ${dir}`);
