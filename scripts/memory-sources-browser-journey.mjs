// Browser journey through the real ChatMessage with synthetic Diary and Branch
// payloads. Start Vite in webapp/, then run this file with Node. Needs the
// user's opencli browser; no live account or model calls.
import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import assert from 'node:assert/strict';

const origin = process.env.MEMORY_PREVIEW_ORIGIN || 'http://127.0.0.1:5195';
const dir = process.env.MEMORY_ARTIFACT_DIR || '/tmp/catsco-memory-sources';
const evidence = [];
mkdirSync(dir, { recursive: true });
const cli = (...args) => execFileSync('opencli', ['browser', 'memory-sources', ...args], { encoding: 'utf8', timeout: 35_000, stdio: ['ignore', 'pipe', 'pipe'] });
function evaluate(js) {
  const out = cli('eval', js);
  return JSON.parse(out.slice(out.indexOf('{'), out.lastIndexOf('}') + 1));
}
function check(name, js) {
  const result = evaluate(js);
  evidence.push({ name, ...result });
  assert.ok(result.ok, `${name}: ${JSON.stringify(result)}`);
  console.log(`PASS ${name}`);
}
function open(page, query = '') {
  cli('open', `${origin}/${page}-sources-preview.html${query}`, '--window', 'background');
  cli('wait', 'selector', '.cc-diary-preview');
}
// Targeted DOM clicks keep focus and run the real React handlers; opencli's
// background CDP input can lose activation after emulated screenshot sizes.
function click(selector) {
  evaluate(`(()=>{const node=document.querySelector(${JSON.stringify(selector)});if(!node)throw new Error('missing control '+${JSON.stringify(selector)});node.focus();node.click();return {clicked:true}})()`);
}
function shot(file, width = 1100, height = 780) {
  cli('screenshot', join(dir, file), '--width', String(width), '--height', String(height));
}
const expand = () => click('.cc-memory-toggle[aria-expanded="false"]');
const text = 'document.querySelector(".cc-memory-panel")?.innerText||""';
const rows = type => `document.querySelectorAll(".cc-memory-source${type ? '.is-' + type : ''}").length`;

try {
  // Status line: the reply decides whether retrieval is still running.
  open('memory', '?mode=stale');
  check('final reply ends stale running snapshot', `({ok:!document.body.innerText.includes('记忆探索中')&&!document.querySelector('canvas')})`);
  open('memory', '?mode=idle');
  check('idle history never presents ongoing retrieval', `({ok:!document.body.innerText.includes('记忆探索中')})`);
  open('memory', '?mode=quiet-live');
  check('live retrieval has one lightweight status', `({ok:document.querySelectorAll('.cc-memory-status').length===1})`);
  click('[data-preview="finish"]');
  // A run without sources still says how it ended, without an empty disclosure.
  check('unfinished no-result run says so when the turn ends', `({ok:document.querySelector('.cc-memory-status')?.innerText==='记忆查找未完成'&&!document.querySelector('.cc-memory-toggle')})`);
  open('memory', '?mode=empty');
  check('empty run is one status line', `({ok:document.querySelector('.cc-memory-status')?.innerText==='未找到相关记忆'&&!document.querySelector('.cc-memory-toggle')})`);
  for (const [state, label] of [['failed', '执行失败'], ['cancelled', '已停止']]) {
    open('memory', '?mode=combined&execution=' + state);
    // Diary no longer keeps a tool step in the log, so the log needs details on.
    evaluate(`(()=>{[...document.querySelectorAll('label')].find(l=>l.textContent.includes('执行详情')).querySelector('input').click();return {details:true}})()`);
    check('collapsed tool details show ' + state, `(()=>{const t=document.querySelector('.v3-working-toggle');return {ok:t?.getAttribute('aria-expanded')==='false'&&t.innerText.includes(${JSON.stringify(label)}),text:t?.innerText}})()`);
  }

  // One list for both sources; nothing mounts or reads until opened.
  open('memory', '?mode=combined');
  check('completed turn starts compact', `({ok:document.querySelector('.cc-memory-toggle').getAttribute('aria-expanded')==='false'&&!document.querySelector('.cc-memory-panel, canvas')&&window.__memoryPreviewLog.length===0})`);
  shot('desktop-collapsed.png');
  expand();
  check('Diary and Branch share one grouped list', `(()=>{const g=[...document.querySelectorAll('.cc-memory-group')].map(n=>n.innerText);return {ok:g.join('|')==='日记|历史会话'&&${rows('diary')}===2&&${rows('branch')}===2&&!document.querySelector('canvas')&&window.__memoryPreviewLog.length===0,groups:g,summary:document.querySelector('.cc-memory-summary')?.innerText}})()`);
  check('same-name Branch rows are numbered', `(()=>{const t=[...document.querySelectorAll('.cc-memory-source.is-branch .cc-memory-source-label > span')].map(n=>n.innerText);return {ok:new Set(t).size===t.length,titles:t}})()`);
  shot('desktop-combined.png');
  click('.cc-memory-source.is-branch');
  cli('wait', 'selector', '.cc-diary-prose');
  check('Branch row opens exact pinned details with scoped qualifiers', `(()=>{const r=document.querySelector('[aria-label="记忆来源详情"]').innerText;return {ok:window.__memoryPreviewLog.length===1&&r.includes('仅当旧版本仍能读取')&&r.includes('来源范围的适用条件')}})()`);
  shot('desktop-branch-reader.png');
  cli('keys', 'Escape');
  check('escape closes reader and restores row focus', `({ok:!document.querySelector('.cc-diary-reader')&&document.activeElement.classList.contains('cc-memory-source'),focus:document.activeElement.className})`);
  click('.cc-memory-toggle');
  check('collapsing releases the list', `({ok:!document.querySelector('.cc-memory-panel')})`);

  // Production shape (2026-11-18): archive turns only, no edges, often late.
  open('memory', '?mode=missed');
  check('Branch that read nothing before the reply still says so', `({ok:document.querySelector('.cc-memory-status')?.innerText==='记忆未赶上本次回答'&&!document.querySelector('.cc-memory-toggle')})`);
  open('memory', '?mode=archive'); expand();
  check('late archive turns stay visible as unused rows', `({ok:${rows('branch')}===6&&${text}.includes('回答先完成')&&!document.querySelector('.cc-memory-badge.is-cited'),summary:document.querySelector('.cc-memory-summary')?.innerText})`);
  shot('mobile-archive.png', 390, 844);
  open('memory', '?mode=archive-used&theme=dark'); expand();
  check('cited and unused rows are tagged', `({ok:document.querySelectorAll('.cc-memory-badge.is-cited').length===1&&[...document.querySelectorAll('.cc-memory-badge')].filter(n=>n.innerText==='未采用').length===4&&${text}.includes('另有 4 条未采用')})`);
  shot('desktop-dark-archive-used.png');
  shot('mobile-dark-archive-used.png', 390, 844);
  check('dark list has no horizontal overflow', `({ok:document.documentElement.dataset.theme==='dark'&&document.documentElement.scrollWidth<=innerWidth,width:innerWidth})`);

  open('memory', '?mode=ready'); expand();
  check('read is not injection', `({ok:${text}.includes('尚未送达回答')&&!${text}.includes('回答参考了')})`);
  open('memory', '?mode=late'); expand();
  check('late result names the turn that received it', `({ok:${text}.includes('来自上一轮提问')})`);
  open('memory', '?mode=unknown');
  check('unknown schema is not rich evidence', `({ok:!document.querySelector('.cc-memory-source, canvas')})`);
  open('memory', '?refused=1'); expand(); click('.cc-memory-source');
  cli('wait', 'selector', '.cc-diary-read-error');
  check('Branch private denial shows no cached body', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('无权查看')&&!document.querySelector('.cc-diary-prose')})`);
  open('memory', '?mismatch=1'); expand(); click('.cc-memory-source');
  cli('wait', 'selector', '.cc-diary-read-error');
  check('Branch pin mismatch shows no cached body', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('返回版本与引用不一致')&&!document.querySelector('.cc-diary-prose')})`);

  // Diary-only turns use the same list; the reader reads the cited revision.
  open('diary');
  check('Diary tool result starts compact and unread', `({ok:!!document.querySelector('.cc-memory-toggle')&&!document.querySelector('.cc-memory-panel, canvas')&&window.__diaryPreviewLog.length===0})`);
  expand(); click('.cc-memory-source.is-diary');
  cli('wait', 'selector', '.cc-diary-prose');
  check('Diary row opens the exact revision', `({ok:document.querySelector('.cc-diary-prose').innerText.includes('数据库变更向后兼容')&&window.__diaryPreviewLog.every(q=>q.revision==='akr-'+'a'.repeat(64)),requests:window.__diaryPreviewLog.length})`);
  shot('desktop-diary-reader.png');
  shot('mobile-diary-reader.png', 390, 844);
  cli('keys', 'Escape');
  check('Diary reader closes back to its row', `({ok:!document.querySelector('.cc-diary-reader')&&document.activeElement.classList.contains('is-diary')})`);
  open('diary', '?mode=many'); expand();
  check('search hits are listed without reads', `({ok:${rows('diary')}>0&&window.__diaryPreviewLog.length===0,rows:${rows('diary')}})`);
  open('diary', '?refused=1'); expand(); click('.cc-memory-source.is-diary');
  cli('wait', 'selector', '.cc-diary-read-error');
  check('Diary private denial shows no cached body', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('无权查看')&&!document.querySelector('.cc-diary-prose')})`);
  open('diary', '?mismatch=1'); expand(); click('.cc-memory-source.is-diary');
  cli('wait', 'selector', '.cc-diary-read-error');
  check('Diary revision mismatch shows no cached body', `({ok:document.querySelector('.cc-diary-read-error').innerText.includes('返回版本与引用不一致')&&!document.querySelector('.cc-diary-prose')})`);
  open('diary', '?mode=malformed');
  check('clipped tool result falls back to no sources', `({ok:!document.querySelector('.cc-memory-source, canvas')})`);
  console.log(`Browser journey passed; artifacts: ${dir}`);
} finally {
  writeFileSync(join(dir, 'browser-journey.json'), `${JSON.stringify({ origin, synthetic: true, evidence }, null, 2)}\n`);
}
