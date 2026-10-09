// Start webapp Vite on 127.0.0.1:5193 first. All API IO is mocked; no real
// accounts, Skills, BotDefinition writes, or production flags are used.
import puppeteer from 'puppeteer';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';
import assert from 'node:assert/strict';

const base = process.env.MARKETPLACE_PREVIEW_URL || 'http://127.0.0.1:5193';
const output = path.resolve(process.env.MARKETPLACE_SCREENSHOT_DIR || 'output/skillhub-marketplace-m2b');
await mkdir(output, { recursive: true });
const html = String.raw`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"></head><body><div id="root"></div>
<script type="module">
import RefreshRuntime from '/@react-refresh';
RefreshRuntime.injectIntoGlobalHook(window); window.$RefreshReg$=()=>{}; window.$RefreshSig$=()=>type=>type; window.__vite_plugin_react_preamble_installed__=true;
await import('/@vite/client');
const {default:React}=await import('/node_modules/.vite/deps/react.js');
const ReactDOM=await import('/node_modules/.vite/deps/react-dom_client.js');
const createRoot=ReactDOM.createRoot||ReactDOM.default.createRoot;
await import('/src/css/auth-critical.css');
await import('/src/views/workspace-styles.js');
const {applyDocumentTheme}=await import('/src/utils/theme-access.js');
applyDocumentTheme(new URLSearchParams(location.search).get('theme')||'dark');
const {setToken}=await import('/src/auth-session.js');
setToken('preview.'+btoa(JSON.stringify({userId:85,exp:4102444800}))+'.not-real');
const {api}=await import('/src/api.js');
const {marketplaceApi}=await import('/src/skillhub-marketplace-api.js');
const {FeedbackProvider}=await import('/src/components/feedback-system.jsx');
const {default:SkillHubView}=await import('/src/views/skillhub-view.jsx');
const id='author/shimo';
const imageId='pa_'+'a'.repeat(32);
const content={schemaVersion:1,summary:'把长文档，变成你真正需要的答案。',primaryCategory:'documents',tags:['文档摘要','待办提取'],scenarios:[{title:'快速读懂长文',description:'先看结论与重点，再按需回到原文。'},{title:'把会议变成行动',description:'整理记录中已经明确的任务与截止日期。'}],steps:['提供你有权访问的石墨文档链接。','告诉 Bot 你要摘要、行动项，还是针对文档提问。','检查答案，重要信息回到原文核对。'],requirements:['需要可访问的文档链接；私有文档需具备相应访问权限。'],limitations:['不能据此认为所有文档格式均已支持。'],details:'可按项目、会议或研究主题整理；具体支持范围请以 Skill 说明为准。',example:{kind:'document',input:'读取这份会议记录，告诉我本周需要完成什么。',output:'周三：完成方案初稿。\n周四：确认配图。\n周五：交付终稿。'}};
const skills=[{skillId:id,displayName:'石墨文档读取',latestVersion:'1.0.6',description:'读取文档并提取信息。',contentHash:'a'.repeat(64),author:{displayName:'arrowhaken',catsCoUid:'85'},primaryCategory:'documents',presentationSummary:{summary:content.summary},publishedAt:'2026-10-08T08:00:00Z'}, {skillId:'author/image',displayName:'image-generation',latestVersion:'1.0.2',description:'把画面想法变成看得见的配图。',contentHash:'b'.repeat(64),author:{displayName:'arrowhaken',catsCoUid:'85'},primaryCategory:'media',publishedAt:'2026-10-07T08:00:00Z'}];
const originals={getMyBots:()=>({bots:[{uid:42,display_name:'David · 一个很长的测试 Agent 名称',relation:'owner',is_owner:true},{uid:43,display_name:'Saturday（好友）',relation:'friend',is_owner:false}]}),getBotDefinitionSkills:()=>({skills:[{skillId:id,version:'1.0.5',contentHash:'c'.repeat(64)}],revision:1}),getAgentSkills:()=>({skills:[{skillId:id,version:'1.0.5',source:'skillhub'}]}),getDevices:()=>({devices:[]}),getBotBodyStatus:()=>({bound:false}),getLocalSkills:()=>({skills:[]}),syncSkillHubPublisherProfile:()=>({synced:true}),searchSkillHubSkills:()=>({skills}),getSkillHubSkill:()=>({skill:skills[0]}),getSkillHubVersions:()=>({versions:[]}),getAgentSkillVersions:()=>({versions:[]})};
for(const name of Object.keys(api)) api[name]=async(...args)=>{if(originals[name])return originals[name](...args);throw new Error('Preview blocks '+name);};
const imageContent={...content,summary:'从一段想法，到一张活动配图。',primaryCategory:'media',tags:['视觉创作'],example:{kind:'image',input:'为城市漫步活动制作一张清爽的植物主题配图。',output:'用于活动页面的视觉配图，实际输出由模型和 Skill 决定。'},images:[{assetId:imageId,alt:'绿色植物主题活动卡片示意',caption:'版式示意，不是 Skill 的实测输出'}]};
const canvas=document.createElement('canvas'); canvas.width=920;canvas.height=400;const ctx=canvas.getContext('2d');ctx.fillStyle='#e4eedf';ctx.fillRect(0,0,920,400);ctx.fillStyle='#184f43';ctx.font='bold 46px sans-serif';ctx.fillText('WEEKEND / FIELD NOTES',42,95);ctx.font='24px sans-serif';ctx.fillText('城市漫步 · 给日常留一点绿色',45,155);ctx.beginPath();ctx.ellipse(710,210,95,145,0.5,0,Math.PI*2);ctx.fill();ctx.strokeStyle='#f5eed0';ctx.lineWidth=5;ctx.beginPath();ctx.moveTo(650,360);ctx.lineTo(755,90);ctx.stroke();
const blob=await new Promise(resolve=>canvas.toBlob(resolve,'image/webp'));
window.fetch=async(url)=>{if(String(url).includes('/api/skillhub/marketplace/assets/'+imageId))return new Response(blob,{headers:{'Content-Type':'image/webp'}});throw new Error('Preview blocks network '+url);};
let revision=0;let draft=content;let published=content;
marketplaceApi.capabilities=async()=>({schemaVersion:1,enabled:true,writesEnabled:true});
marketplaceApi.catalogue=async({category,q})=>{const filtered=skills.filter(s=>(!category||s.primaryCategory===category)&&(!q||s.displayName.toLowerCase().includes(q.toLowerCase())));return {skills:filtered,total:filtered.length,nextCursor:null,categoryCounts:[{category:'documents',count:1},{category:'media',count:1}]};};
marketplaceApi.presentation=async target=>({presentation:{...target,schemaVersion:1,revision,content:target.skillId===id?published:imageContent}});
marketplaceApi.draft=async target=>({presentation:{...target,revision,draft,published:published?{...target,content:published}:null}});
marketplaceApi.save=async body=>{assertRevision(body);draft=body.content;return {presentation:{skillId:body.skillId,version:body.version,revision:++revision,draft,published:{skillId:body.skillId,version:body.version,content:published}}};};
marketplaceApi.publish=async body=>{assertRevision(body);published=draft;return {presentation:{skillId:body.skillId,version:body.version,revision:++revision,draft,published:{skillId:body.skillId,version:body.version,content:published}}};};
function assertRevision(body){if(body.expectedRevision!==revision)throw Object.assign(new Error('conflict'),{status:409});}
document.body.style.margin='0';document.getElementById('root').style.height='100vh';
createRoot(document.getElementById('root')).render(React.createElement(FeedbackProvider,null,React.createElement(SkillHubView,{user:{uid:85},initialAgentId:42})));
</script></body></html>`;

const browser = await puppeteer.launch({ headless: true });
try {
  const page = await browser.newPage();
  await page.emulateMediaFeatures([{ name: 'prefers-reduced-motion', value: 'reduce' }]);
  const errors = []; page.on('pageerror', (error) => { errors.push(error.message); console.error('Browser:', error.message); });
  page.on('requestfailed', (request) => console.error('Failed request:', request.url(), request.failure()?.errorText));
  await page.setRequestInterception(true);
  page.on('request', (request) => {
    const url = new URL(request.url());
    if (url.origin !== base && !['data:', 'blob:'].includes(url.protocol)) return request.abort();
    if (url.pathname === '/__marketplace_browser') return request.respond({ status: 200, contentType: 'text/html; charset=utf-8', body: html });
    return request.continue();
  });
  const clickText = async (text) => {
    await page.waitForFunction((text) => [...document.querySelectorAll('button')].some(b => b.textContent.trim() === text), {}, text);
    await page.evaluate((text) => [...document.querySelectorAll('button')].find(b => b.textContent.trim() === text).click(), text);
  };
  for (const theme of ['dark', 'light']) {
    await page.setViewport({ width: 1440, height: 1050 });
    await page.goto(base + '/__marketplace_browser?theme=' + theme);
    await clickText('能力库');
    await page.waitForSelector('.cc-market-tags');
    await page.screenshot({ path: path.join(output, theme + '-catalogue.png'), fullPage: true });
    await page.click('[aria-label="查看 石墨文档读取 详情"]');
    await page.waitForSelector('.cc-market-io');
    await page.screenshot({ path: path.join(output, theme + '-document.png'), fullPage: true });
    await clickText('编辑此版本介绍');
    await clickText('保存草稿并预览');
    await page.waitForSelector('.cc-market-draft-preview');
    await clickText('确认发布介绍');
    await page.waitForFunction(() => document.body.textContent.includes('介绍已发布'));
    await clickText('返回介绍'); await clickText('完成');
    await page.click('[aria-label="查看 image-generation 详情"]');
    await page.waitForSelector('.cc-market-figure img');
    await page.screenshot({ path: path.join(output, theme + '-image.png'), fullPage: true });
    await page.setViewport({ width: 390, height: 844 });
    await page.screenshot({ path: path.join(output, theme + '-mobile.png'), fullPage: true });
    const overflow = await page.evaluate(() => {
      const dialog = document.querySelector('[role=dialog]');
      return { dialog: dialog.scrollWidth > dialog.clientWidth + 1, page: document.documentElement.scrollWidth > innerWidth + 1 };
    });
    assert.deepEqual(overflow, { dialog: false, page: false });
  }
  assert.deepEqual(errors, []);
  console.log('Browser checks passed: light/dark, document/image, 390px, save-preview-publish. Screenshots: ' + output);
} finally { await browser.close(); }
