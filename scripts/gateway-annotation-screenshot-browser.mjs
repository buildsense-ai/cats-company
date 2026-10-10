// Real-browser regression for issue #598. No mock canvas or renderer:
// the cross-origin iframe loads the canonical SDK and SRI-pinned UMD bundle,
// and the production host validates the two JPEGs before pixel assertions.
// Run: node scripts/gateway-annotation-screenshot-browser.mjs
// Open the printed URL in an ACTIVE tab, click "Capture" inside the frame,
// then inspect window.result or the status text. Background tabs can throttle
// capture timers and font/iframe readiness. Nothing is uploaded or sent to an Agent.
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const port = Number(process.env.CATSCO_SCREENSHOT_TEST_PORT || 18762);
if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error('Invalid fixture port');
const hostOrigin = `http://127.0.0.1:${port}`;
const appOrigin = `http://localhost:${port}`;
const resources = new Map([
  ['/_catsco/runtime/annotations-v1.js', new URL('../webapp/public/catsco-annotations.js', import.meta.url)],
  ['/_catsco/runtime/html2canvas-pro-1.6.7.min.js', new URL('../webapp/public/catsco-runtime/html2canvas-pro-1.6.7.min.js', import.meta.url)],
  ['/gateway-annotations.js', new URL('../webapp/src/gateway-annotations.js', import.meta.url)],
]);
const hostHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Screenshot regression #598</title></head><body>
<p id="status">Connecting…</p><button id="run">Capture in app</button><iframe id="app" title="Modern CSS fixture" width="640" height="300"></iframe>
<div id="previews"></div><script type="module" src="/host.js"></script></body></html>`;
const appHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Modern CSS capture fixture</title>
<link rel="stylesheet" href="/app.css"><script src="/app.js"></script>
<script src="/_catsco/runtime/annotations-v1.js" data-catsco-parent-origins='["${hostOrigin}"]'></script></head><body>
<div class="card" id="srgb"></div><div class="card" id="mix"></div><div class="card" id="oklab"></div><div class="card" id="oklch"></div><div class="card" id="gradient"></div>
<div class="private" id="sensitive" data-catsco-annotation-sensitive>private fixture text</div>
<input class="private" id="input" value="fixture-input-secret">
<div class="private" id="shadow"></div><div class="private" id="preserved-shadow"></div><div class="private" id="editable" contenteditable="true">private fixture draft</div>
<button class="card" id="target">Capture</button><div id="pseudo"></div>
</body></html>`;
const appCSS = `body{margin:0;background:white;font:16px sans-serif}
.card{position:absolute;width:100px;height:60px;top:20px;box-sizing:border-box}
#srgb{left:20px;background:color(srgb 0.2 0.4 0.6)}
#mix{left:140px;background:color-mix(in srgb,rgb(0 255 0) 50%,rgb(0 0 255))}
#oklab{left:260px;background:oklab(0.7 0.08 0.06)}
#oklch{left:380px;background:oklch(70% 0.12 150)}
#gradient{left:500px;background:linear-gradient(90deg,color(srgb 1 0 0),oklch(70% 0.12 150));box-shadow:0 0 5px oklab(0.7 0.08 0.06)}
.private{position:absolute;top:120px;width:100px;height:60px;box-sizing:border-box;border:0;background:magenta;color:black}
#sensitive{left:20px} #sensitive:before{content:'private';color:color(srgb 1 0 0)}
#input{left:140px} #shadow{left:260px;background:white} #preserved-shadow{left:140px;top:200px} #editable{left:380px}
#target{left:500px;top:120px;border:0;background:color(srgb 0.2 0.4 0.6);color:white}
#pseudo{position:absolute;top:200px;left:20px;width:100px;height:60px}
#pseudo:before{content:'probe';display:block;width:100px;height:60px;background:color(srgb 0.6 0.2 0.4);color:oklch(90% 0.01 100)}`;
const appJS = `window.html2canvas = window.appRendererSentinel = { marker: 'app-owned' };
window.addEventListener('DOMContentLoaded', () => {
  const shadow = document.querySelector('#shadow').attachShadow({mode:'open'});
  shadow.innerHTML = '<div data-catsco-annotation-sensitive style="height:60px;background:magenta"><input value="shadow-fixture-secret"></div>';
  const preserved=document.querySelector('#preserved-shadow').attachShadow({mode:'open',clonable:true});
  preserved.innerHTML = '<div style="height:60px;background:magenta"><input value="preserved-shadow-secret"></div>';
});
window.addEventListener('message', event => {
  if(event.origin !== ${JSON.stringify(hostOrigin)} || event.source !== parent) return;
  if(event.data?.type === 'fixture.capture') {
    const target=document.querySelector('#target'),rect=target.getBoundingClientRect();
    for(const type of ['mousedown','mouseup','click']) target.dispatchEvent(new MouseEvent(type,{bubbles:true,cancelable:true,button:0,clientX:rect.left+10,clientY:rect.top+10}));
    return;
  }
  if(event.data?.type !== 'fixture.audit.request') return;
  parent.postMessage({type:'fixture.audit.result',mode:window.CatsCoAnnotations.create({parentOrigin:${JSON.stringify(hostOrigin)}})?.mode(),globalRestored:window.html2canvas===window.appRendererSentinel,
    inputPreserved:document.querySelector('#input').value==='fixture-input-secret',
    shadowPreserved:document.querySelector('#shadow').shadowRoot.querySelector('input').value==='shadow-fixture-secret' && document.querySelector('#preserved-shadow').shadowRoot.querySelector('input').value==='preserved-shadow-secret'},${JSON.stringify(hostOrigin)});
});`;
const hostJS = `import {createGatewayAnnotationHost} from '/gateway-annotations.js';
const frame=document.querySelector('#app');
const binding={frame,url:${JSON.stringify(appOrigin + '/app')}+window.location.search,appId:'modern-css-fixture',agentUid:1};
const status=document.querySelector('#status');
document.querySelector('#run').onclick=()=>frame.contentWindow.postMessage({type:'fixture.capture'},${JSON.stringify(appOrigin)});
window.result={status:'connecting'};
const assert=(condition,message)=>{if(!condition)throw new Error(message)};
const near=(actual,expected)=>actual.slice(0,3).every((v,i)=>Math.abs(v-expected[i])<=8);
const decode=async image=>{const img=new Image();img.src=image.data_url;await img.decode();const canvas=document.createElement('canvas');canvas.width=img.naturalWidth;canvas.height=img.naturalHeight;const ctx=canvas.getContext('2d');ctx.drawImage(img,0,0);return {img,ctx};};
let auditResolve;
window.addEventListener('message', event=>{
  if(event.origin===${JSON.stringify(appOrigin)} && event.source===frame.contentWindow && event.data?.type==='fixture.audit.result') auditResolve?.(event.data);
});
const host=createGatewayAnnotationHost({getBinding:()=>binding,
  onReady(){host.setMode('select');window.result={status:'ready'};status.textContent='Click Capture in the frame';},
  async onSelection(selection,page){
    window.result={status:'capturing'};status.textContent='Capturing…';
    try{
      const result=await host.captureScreenshot({selectionId:selection.id,page});
      const full=await decode(result.screenshots[0]),crop=await decode(result.screenshots[1]);
      const image=result.screenshots[0];const sx=image.width/640,sy=image.height/300;
      const pixel=(x,y)=>Array.from(full.ctx.getImageData(Math.floor(x*sx),Math.floor(y*sy),1,1).data);
      const samples={srgb:pixel(30,30),mix:pixel(150,30),oklab:pixel(270,30),oklch:pixel(390,30),pseudo:pixel(30,250),
        sensitive:pixel(30,150),input:pixel(150,150),shadow:pixel(270,150),preservedShadow:pixel(150,240),editable:pixel(390,150)};
      for(const [key,expected] of Object.entries({srgb:[51,102,153],mix:[0,128,128],oklab:[212,136,114],oklch:[99,179,118],pseudo:[153,51,102],sensitive:[255,255,255],input:[255,255,255],shadow:[255,255,255],preservedShadow:[255,255,255],editable:[255,255,255]})) assert(near(samples[key],expected),key+' pixels: '+samples[key]);
      const privacyMaxDeviation={};
      for(const [key,x,y] of [['sensitive',22,122],['input',142,122],['shadow',262,122],['editable',382,122],['preservedShadow',142,202]]) {
        const pixels=full.ctx.getImageData(Math.round(x*sx),Math.round(y*sy),Math.round(96*sx),Math.round(56*sy)).data;
        let deviation=0;for(let i=0;i<pixels.length;i+=4) deviation=Math.max(deviation,255-pixels[i],255-pixels[i+1],255-pixels[i+2]);
        privacyMaxDeviation[key]=deviation;assert(deviation<=8,key+' contains unmasked pixels');
      }
      assert(image.width<=2048 && image.height<=2048,'dimension budget');
      assert(crop.img.naturalWidth===result.screenshots[1].width,'decoded crop dimensions');
      assert(result.warnings.includes('sensitive-content-masked') && result.warnings.includes('embedded-content'),'masking warnings');
      const red=pixel(501,135);assert(red[0]>190 && red[1]<100,'red target frame');
      const cropEdge=Array.from(crop.ctx.getImageData(Math.round(17*sx),Math.round(31*sy),1,1).data);
      assert(near(cropEdge,[51,102,153]),'crop must not contain the full-image red frame');
      const audit=await new Promise((resolve,reject)=>{const timer=setTimeout(()=>reject(new Error('audit timeout')),3000);auditResolve=value=>{clearTimeout(timer);resolve(value)};frame.contentWindow.postMessage({type:'fixture.audit.request'},${JSON.stringify(appOrigin)})});
      assert(audit.globalRestored && audit.inputPreserved && audit.shadowPreserved,'original application state');
      window.result={status:'passed',screenshots:result.screenshots.map(({role,width,height,data_url})=>({role,width,height,encodedChars:data_url.length})),samples,privacyMaxDeviation,warnings:result.warnings,audit};
      document.querySelector('#previews').replaceChildren(full.img,crop.img);status.textContent='PASS: modern colors, pseudo-elements, masking, geometry and app global';
    }catch(error){window.result={status:'failed',error:String(error),code:error.code};status.textContent='FAIL: '+error;}
  }
});
window.addEventListener('message',host.handleWindowMessage);
frame.addEventListener('load',()=>host.connect());frame.src=binding.url;
window.addEventListener('pagehide',()=>host.dispose(),{once:true});`;

const inline = new Map([
  ['/', ['text/html', hostHTML]], ['/app', ['text/html', appHTML]],
  ['/app.css', ['text/css', appCSS]], ['/app.js', ['application/javascript', appJS]],
  ['/host.js', ['application/javascript', hostJS]],
]);
const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, hostOrigin);
    const path = url.pathname;
    const entry = inline.get(path);
    const file = resources.get(path);
    if (req.method !== 'GET' || (!entry && !file)) { res.writeHead(404); res.end(); return; }
    res.writeHead(200, { 'Content-Type': entry?.[0] || 'application/javascript',
      'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff',
      'Content-Security-Policy': `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-src 'self' ${appOrigin}` });
    let body = entry?.[1] || await readFile(file);
    if (url.searchParams.has('plain')) {
      if (path === '/app') body = body.replace('href="/app.css"', 'href="/app.css?plain"');
      if (path === '/app.css') {
        for (const [modern, rgb] of [
          ['color(srgb 0.2 0.4 0.6)', 'rgb(51 102 153)'],
          ['color-mix(in srgb,rgb(0 255 0) 50%,rgb(0 0 255))', 'rgb(0 128 128)'],
          ['oklab(0.7 0.08 0.06)', 'rgb(212 136 114)'],
          ['oklch(70% 0.12 150)', 'rgb(99 179 118)'],
          ['color(srgb 1 0 0)', 'rgb(255 0 0)'],
          ['color(srgb 0.6 0.2 0.4)', 'rgb(153 51 102)'],
          ['oklch(90% 0.01 100)', 'rgb(225 225 225)'],
        ]) body = body.replaceAll(modern, rgb);
      }
    }
    res.end(body);
  } catch (error) { res.destroy(error); }
});
server.listen(port, '127.0.0.1', () => console.log(`Browser regression: ${hostOrigin}/ (${fileURLToPath(import.meta.url)})`));
