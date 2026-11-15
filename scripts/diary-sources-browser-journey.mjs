// Repeatable opencli browser journey through the actual ChatMessage with
// synthetic Diary responses. Start Vite, then run this file with Node.
import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
const base = process.env.DIARY_PREVIEW_URL || 'http://127.0.0.1:5194/diary-sources-preview.html';
const dir = process.env.DIARY_ARTIFACT_DIR || '/tmp/catsco-diary-sources';
const session = 'diary-cards';
const resume = process.env.DIARY_BROWSER_RESUME === 'clusters';
const evidence = resume ? JSON.parse(readFileSync(resolve(dir, 'browser-journey.json'), 'utf8')).evidence : [];
mkdirSync(dir, { recursive: true });
function cli(...args) {
  return execFileSync('opencli', ['browser', session, ...args], { encoding: 'utf8', timeout: 30000, stdio: ['ignore', 'pipe', 'pipe'] });
}
function check(name, expression) {
  const output = cli('eval', expression);
  const value = JSON.parse(output.slice(output.indexOf('{'), output.lastIndexOf('}') + 1));
  const index = evidence.findIndex((entry) => entry.name === name);
  if (index < 0) evidence.push({ name, ...value });
  else evidence[index] = { name, ...value };
  if (!value.ok) throw new Error(`${name}: ${JSON.stringify(value)}`);
  console.log(`PASS ${name}`);
  return value;
}
function open(query = '') { cli('open', `${base}${query}`, '--window', 'background'); cli('wait', 'selector', '.cc-diary-preview'); }
function screenshot(name, width = 1280, height = 900) { cli('screenshot', resolve(dir, name), '--width', String(width), '--height', String(height)); }
function paintCurrent() {
  const output = cli('eval', '({width:innerWidth,height:innerHeight})');
  const size = JSON.parse(output.slice(output.indexOf('{'), output.lastIndexOf('}') + 1));
  screenshot('canvas-ready.png', size.width, size.height);
}
function clickNote() {
  cli('wait', 'selector', 'canvas');
  // The background browser needs a capture to paint its current camera.
  paintCurrent();
  cli('click', '.cc-diary-tool canvas');
}
try {
  if (!resume) {
  open();
  cli('wait', 'selector', 'canvas');
  check('Agent Settings Canvas renderer', `({ok:!!document.querySelector('.cc-daily-notes-network canvas')&&document.querySelectorAll('.cc-daily-notes-network-controls button').length===3&&document.querySelectorAll('.cc-diary-node').length===2,label:document.querySelector('canvas').getAttribute('aria-label')})`);
  screenshot('desktop-graph.png');
  clickNote();
  cli('wait', 'selector', '.cc-diary-prose');
  check('exact version reader', `({ok:document.querySelector('.cc-diary-prose').innerText.includes('数据库变更向后兼容')&&window.__diaryPreviewLog.every(q=>q.revision==='akr-'+'a'.repeat(64)),requests:window.__diaryPreviewLog})`);
  screenshot('desktop-reader.png');
  screenshot('mobile-reader.png', 390, 844);
  cli('keys', 'Escape');
  check('keyboard close', `({ok:!document.querySelector('.cc-diary-reader')&&document.activeElement.tagName==='CANVAS',focus:document.activeElement.tagName})`);
  open('?mode=many');
  check('independent search hits', `({ok:document.querySelectorAll('.cc-diary-node').length===8&&window.__diaryPreviewLog.length===0,nodes:document.querySelectorAll('.cc-diary-node').length,searchOnly:document.body.innerText.includes('仅搜索命中')})`);
  screenshot('desktop-search-nodes.png');
  screenshot('mobile-search-nodes.png', 390, 844);
  cli('click', '--role', 'button', '--name', '列表视图');
  check('list alternative', `({ok:document.querySelectorAll('.cc-diary-source-list .cc-diary-node').length===8&&!document.querySelector('canvas'),listNodes:document.querySelectorAll('.cc-diary-source-list .cc-diary-node').length})`);
  open('?theme=dark&mode=many');
  screenshot('dark-search-nodes.png');
  cli('click', '--role', 'button', '--name', '列表视图');
  cli('click', '.cc-diary-tool .cc-diary-node', '--nth', '1');
  cli('wait', 'selector', '.cc-diary-prose');
  screenshot('dark-reader.png');
  open('?refused=1');
  clickNote();
  cli('wait', 'selector', '.cc-diary-read-error');
  check('refused private body', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('无权查看')&&!document.querySelector('.cc-diary-prose')})`);
  screenshot('refused-reader.png', 390, 844);
  open('?mismatch=1');
  clickNote();
  cli('wait', 'selector', '.cc-diary-read-error');
  check('revision mismatch', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('返回版本与引用不一致')&&!document.querySelector('.cc-diary-prose')})`);
  open('?mode=malformed');
  check('clipped result fallback', `({ok:!document.querySelector('.cc-diary-tool')&&!document.querySelector('canvas')})`);
  }
  open('?mode=clusters');
  cli('wait', 'selector', 'canvas');
  check('real source clusters', `({ok:document.querySelector('canvas').getAttribute('aria-label').includes('12 条笔记，按来源会话分为 2 组'),label:document.querySelector('canvas').getAttribute('aria-label')})`);
  screenshot('settings-style-clusters-desktop.png');
  paintCurrent();
  cli('eval', '({before:!!(window.__diaryCanvasBefore=document.querySelector("canvas").toDataURL())})');
  cli('click', '--role', 'button', '--name', '放大');
  paintCurrent();
  check('shared camera zoom', `({ok:document.querySelector('canvas').toDataURL()!==window.__diaryCanvasBefore})`);
  cli('click', '--role', 'button', '--name', '显示全部');
  screenshot('settings-style-clusters-mobile.png', 390, 844);
  // D10: resizing the same mounted graph must keep both source clusters
  // visible; a stale desktop camera leaves the second cluster off-canvas.
  cli('eval', '({width:(document.querySelector(".cc-diary-preview").style.width="390px")})');
  paintCurrent();
  check('narrow container keeps both clusters', `(()=>{const c=document.querySelector('canvas'),d=c.getContext('2d').getImageData(0,0,c.width,c.height).data;let green=0,blue=0;for(let i=0;i<d.length;i+=4){if(d[i+1]>d[i]+20)green++;if(d[i+2]>d[i]+20&&d[i+2]>d[i+1]+5)blue++;}return {ok:green>20&&blue>20,green,blue,overflow:document.documentElement.scrollWidth>innerWidth};})()`);
  cli('eval', '({width:(document.querySelector(".cc-diary-preview").style.width="")})');
  open('?mode=clusters&theme=dark');
  screenshot('settings-style-clusters-dark.png');
  screenshot('settings-style-clusters-mobile-dark.png', 390, 844);
  open('?mode=clusters');
  console.log(`Browser journey passed; artifacts: ${dir}`);
} finally {
  writeFileSync(resolve(dir, 'browser-journey.json'), JSON.stringify({ preview: base, synthetic: true, evidence }, null, 2));
}
