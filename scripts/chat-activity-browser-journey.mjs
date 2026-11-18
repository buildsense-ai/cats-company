// Browser regression through the real ChatMessage and the captured Branch payload.
// Requires the local Vite preview. No live account or model calls.
import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import assert from 'node:assert/strict';

const base = process.env.ACTIVITY_PREVIEW_URL || 'http://127.0.0.1:5198/memory-sources-preview.html';
const dir = process.env.ACTIVITY_ARTIFACT_DIR || '/tmp/catsco-activity-ux';
const evidence = [];
mkdirSync(dir, { recursive: true });
const cli = (...args) => execFileSync('opencli', ['browser', 'chat-activity', ...args], { encoding: 'utf8', timeout: 35_000, stdio: ['ignore', 'pipe', 'pipe'] });
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
function open(mode, options = '') {
  cli('open', `${base}?mode=${mode}${options}`, '--window', 'background');
  cli('wait', 'selector', '.cc-diary-preview');
}
function click(selector) {
  evaluate(`(()=>{const button=document.querySelector(${JSON.stringify(selector)});button.focus();button.click();return {clicked:true}})()`);
}
function shot(file, width = 1100, height = 780) {
  cli('screenshot', join(dir, file), '--width', String(width), '--height', String(height));
}
function mobileProbe() {
  return evaluate(`(async()=>{
    const frame=document.createElement('iframe');
    frame.style.cssText='position:fixed;inset:0;width:390px;height:844px;border:0;z-index:9999';
    const loaded=new Promise(resolve=>frame.addEventListener('load',resolve,{once:true}));
    frame.src=location.href;
    document.body.append(frame);
    try {
      await loaded;
      const doc=frame.contentDocument;
      for(let i=0;i<30&&!doc.querySelector('.cc-memory-toggle');i++) await new Promise(resolve=>setTimeout(resolve,50));
      doc.querySelector('.cc-memory-toggle').click();
      for(let i=0;i<30&&!doc.querySelector('canvas');i++) await new Promise(resolve=>setTimeout(resolve,50));
      const width=frame.contentWindow.innerWidth, scroll=doc.documentElement.scrollWidth;
      return {ok:width===390&&scroll<=width&&!!doc.querySelector('canvas'),width,scroll};
    } finally { frame.remove(); }
  })()`);
}
try {
  open('stale');
  check('final reply ends stale running snapshot', `({ok:!document.body.innerText.includes('记忆探索中')&&!document.querySelector('canvas'), text:document.querySelector('.v3-working-process').innerText})`);
  open('idle');
  check('idle history never presents ongoing retrieval', `({ok:!document.body.innerText.includes('记忆探索中')&&!document.querySelector('canvas')})`);
  open('finished-ready');
  check('read source remains inspectable when the turn has finished', `({ok:!!document.querySelector('.cc-memory-toggle')&&!document.body.innerText.includes('等待主 Agent 接收')&&!document.querySelector('canvas')})`);
  click('.cc-memory-toggle');
  check('finished reading is not injection', `({ok:document.body.innerText.includes('暂无注入记录')&&!document.body.innerText.includes('已加入主 Agent 上下文')&&!!document.querySelector('canvas')})`);
  open('quiet-live');
  check('live retrieval has one lightweight status and no canvas', `({ok:document.querySelectorAll('.cc-memory-status').length===1&&document.body.innerText.includes('记忆探索中')&&!document.querySelector('canvas')})`);
  click('[data-preview="finish"]');
  check('live status disappears when a no-result turn ends', `({ok:!document.querySelector('.cc-memory-status')&&!document.querySelector('.cc-memory-toggle')})`);
  for (const [state, label] of [['failed', '执行失败'], ['cancelled', '已停止']]) {
    open('combined', '&execution=' + state);
    check('collapsed tool details show ' + state, `(()=>{
      const toggle = document.querySelector('.v3-working-toggle');
      return {ok:toggle?.getAttribute('aria-expanded') === 'false' && toggle.innerText.includes(${JSON.stringify(label)}), text:toggle?.innerText};
    })()`);
  }
  open('combined');
  check('completed turn starts compact with both entry points', `({ok:!!document.querySelector('.v3-working-toggle')&&!!document.querySelector('.cc-memory-toggle')&&!document.querySelector('canvas')&&window.__memoryPreviewLog.length===0})`);
  shot('desktop-collapsed.png');
  click('.v3-working-toggle');
  check('opening the tool log leaves both graphs unmounted', `({ok:!!document.querySelector('.v3-wpi-tool-header.is-toggle')&&!document.querySelector('canvas')})`);
  click('.v3-wpi-tool-header.is-toggle');
  check('only the selected Diary tool mounts its graph', `({ok:!!document.querySelector('[aria-label="日记查阅"]')&&document.querySelectorAll('canvas').length===1})`);
  click('.cc-memory-toggle');
  check('Branch graph mounts only by request', `({ok:document.querySelectorAll('canvas').length===2&&document.body.innerText.includes('已加入主 Agent 上下文')&&window.__memoryPreviewLog.length===0})`);
  shot('desktop-expanded.png');
  click('.cc-memory-toggle');
  check('closing Branch removes its graph and keeps Diary open', `({ok:!document.querySelector('[aria-label="记忆探索"]')&&document.querySelectorAll('canvas').length===1})`);
  open('combined', '&theme=dark');
  shot('mobile-dark-collapsed.png', 390, 844);
  click('.cc-memory-toggle');
  shot('mobile-dark-sources.png', 390, 844);
  const mobile = mobileProbe();
  evidence.push({ name: 'dark mobile graph fits without horizontal overflow', ...mobile });
  assert.ok(mobile.ok, JSON.stringify(mobile));
  console.log('PASS dark mobile graph fits without horizontal overflow');
  cli('keys', 'Escape');
  check('escape closes Branch and returns focus', `({ok:document.activeElement===document.querySelector('.cc-memory-toggle')&&!document.querySelector('canvas')})`);
  open('saved');
  click('.cc-memory-toggle');
  click('button[aria-label="记忆列表视图"]');
  click('.cc-memory-source');
  cli('wait', 'selector', '.cc-diary-prose');
  check('history opens exact pinned details, including conditions', `({ok:window.__memoryPreviewLog.length===1&&document.querySelector('.cc-diary-reader').innerText.includes('仅当旧版本仍能读取')})`);
} finally {
  writeFileSync(join(dir, 'activity-journey.json'), `${JSON.stringify({ preview: base, synthetic: true, evidence }, null, 2)}\n`);
}
