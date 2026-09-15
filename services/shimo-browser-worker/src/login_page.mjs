import { LOGIN_VIEWPORT } from './login_viewport.mjs';

export function loginHTML(token) {
  const base = `/shimo-login/${token}`;
  return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>连接石墨</title>
  <style>
    :root{color-scheme:light;--green:#287a3e;--ink:#142019;--muted:#526158;--line:#c9d5cc}
    *{box-sizing:border-box}
    body{margin:0;background:#f4f7f4;color:var(--ink);font-family:system-ui,-apple-system,"Segoe UI",sans-serif}
    main{width:min(1320px,100%);margin:auto;padding:18px}
    header{display:flex;align-items:center;justify-content:space-between;gap:16px;flex-wrap:wrap;margin-bottom:12px}
    h1{font-size:22px;margin:0}
    #status{display:flex;align-items:center;gap:8px;color:var(--muted);font-size:14px}
    #dot{width:9px;height:9px;border-radius:50%;background:#d99b26;box-shadow:0 0 0 3px #f7e8c9}
    #home{border:1px solid var(--line);background:#fff;color:var(--ink);font:inherit;font-size:13px;padding:6px 12px;border-radius:999px;cursor:pointer}
    #home:hover{background:#eef4ef}
    #home:disabled{color:var(--muted);cursor:default}
    #surface{position:relative;overflow:hidden;border:1px solid var(--line);border-radius:12px;background:#fff;box-shadow:0 6px 24px #173b2112}
    canvas{display:block;width:100%;height:auto;outline:none;cursor:default;touch-action:none}
    canvas:focus-visible{box-shadow:inset 0 0 0 3px #4c9b60}
    #notice{position:absolute;inset:0;display:none;place-items:center;background:#fffffff2;text-align:center;padding:24px}
    #notice strong{display:block;font-size:22px;margin-bottom:8px}
    #keyboard{position:fixed;left:-100px;top:0;width:1px;height:1px;opacity:0;pointer-events:none}
    .help{margin:10px 2px 0;color:var(--muted);font-size:13px}
    #inputError{display:none;margin:10px 2px 0;color:#a33b3b;font-size:13px}
  </style>
</head>
<body>
<main>
  <header><h1>连接石墨</h1><div id="status"><span id="dot"></span><span id="statusText">正在连接安全登录窗口…</span><button id="home" type="button">回到登录页</button></div></header>
  <section id="surface" aria-label="石墨登录页面">
    <canvas id="screen" width="${LOGIN_VIEWPORT.width}" height="${LOGIN_VIEWPORT.height}" tabindex="0"></canvas>
    <div id="notice"><div><strong id="noticeTitle"></strong><span id="noticeText"></span></div></div>
  </section>
  <textarea id="keyboard" aria-label="远程登录键盘输入" autocomplete="off" autocapitalize="off" spellcheck="false"></textarea>
  <p class="help">直接点击页面中的输入框并键入内容；支持中文输入法、粘贴、回车、退格和滚轮。登录信息只发送到本次隔离的石墨登录会话。如果画面停在了服务条款、隐私政策这类页面，点右上角「回到登录页」即可回到登录表单。</p>
  <p id="inputError" role="status" aria-live="polite"></p>
</main>
<script>
const base=${JSON.stringify(base)};
const viewport=${JSON.stringify(LOGIN_VIEWPORT)};
const canvas=document.getElementById('screen');
const ctx=canvas.getContext('2d');
const keyboard=document.getElementById('keyboard');
const statusText=document.getElementById('statusText');
const dot=document.getElementById('dot');
const notice=document.getElementById('notice');
const noticeTitle=document.getElementById('noticeTitle');
const noticeText=document.getElementById('noticeText');
const inputError=document.getElementById('inputError');
const home=document.getElementById('home');
let socket=null,finished=false,fallbackTimer=null,wheelTimer=null,wheelX=0,wheelY=0,composing=false,pendingClick=null,inputErrorTimer=null;

function setStatus(message,state='waiting'){
  statusText.textContent=message||'正在等待登录';
  const colors={connected:['#2f8a47','#d9f0df'],expired:['#b84242','#f6dddd'],failed:['#b84242','#f6dddd'],waiting:['#d99b26','#f7e8c9'],opening:['#d99b26','#f7e8c9']};
  const pair=colors[state]||colors.waiting;dot.style.background=pair[0];dot.style.boxShadow='0 0 0 3px '+pair[1];
  if(['connected','expired','failed','closed'].includes(state)){
    finished=true;notice.style.display='grid';noticeTitle.textContent=state==='connected'?'石墨连接成功':'登录未完成';noticeText.textContent=message||'请回到聊天重新发起';
  }
}

function showInputError(message){
  inputError.textContent=message||'这次操作没有生效，请重试';inputError.style.display='block';
  clearTimeout(inputErrorTimer);inputErrorTimer=setTimeout(()=>{inputError.style.display='none'},5000);
}

async function drawFrame(blob){
  const bitmap=await createImageBitmap(blob);
  // The remote page is captured above the logical viewport (see login_viewport.mjs).
  // Match the backing store to the frame so the extra detail survives: drawing a
  // 2x bitmap into a 1280px canvas would discard it.  CSS scales it down.
  if(canvas.width!==bitmap.width||canvas.height!==bitmap.height){canvas.width=bitmap.width;canvas.height=bitmap.height;}
  ctx.drawImage(bitmap,0,0,bitmap.width,bitmap.height);bitmap.close();
}

async function api(path,options){
  const response=await fetch(base+path,options);const result=await response.json();
  if(!response.ok){const error=new Error(result.error?.message||'操作失败');error.code=result.error?.code;error.status=response.status;throw error;}return result.data;
}

function sendAction(action){
  if(finished)return;
  if(action.action!=='click') flushPendingClick();
  if(socket&&socket.readyState===WebSocket.OPEN){socket.send(JSON.stringify(action));return;}
  const {type:_type,...input}=action;
  api('/input',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify(input)}).catch(error=>{
    if(error.code==='INVALID_ARGUMENTS' || error.status===400) showInputError(error.message);
    else setStatus(error.message,'failed');
  });
}

function sendText(value){
  const chars=[...String(value||'')];
  for(let offset=0;offset<chars.length;offset+=200) sendAction({type:'input',action:'text',value:chars.slice(offset,offset+200).join('')});
}

function clampWheel(value){return Math.max(-5000,Math.min(5000,Number(value)||0));}

function sendClick(point,count){
  sendAction({type:'input',action:'click',x:point.x,y:point.y,count});
}

function flushPendingClick(){
  if(!pendingClick)return;
  clearTimeout(pendingClick.timer);const click=pendingClick;pendingClick=null;
  sendClick(click.point,1);keyboard.focus({preventScroll:true});
}

function coordinates(event){
  // Pointer positions are reported in logical viewport pixels, not bitmap pixels:
  // the worker validates and clicks against the logical viewport it renders.
  const rect=canvas.getBoundingClientRect();return{x:(event.clientX-rect.left)*viewport.width/rect.width,y:(event.clientY-rect.top)*viewport.height/rect.height};
}

canvas.addEventListener('click',event=>{
  const point=coordinates(event);
  if(pendingClick){
    const dx=point.x-pendingClick.point.x,dy=point.y-pendingClick.point.y;
    if(Math.hypot(dx,dy)>8)flushPendingClick();
    else clearTimeout(pendingClick.timer);
  }
  pendingClick={point,timer:setTimeout(flushPendingClick,250)};
});
canvas.addEventListener('dblclick',event=>{
  if(pendingClick){clearTimeout(pendingClick.timer);pendingClick=null;}
  sendClick(coordinates(event),2);keyboard.focus({preventScroll:true});
});
canvas.addEventListener('contextmenu',event=>event.preventDefault());
canvas.addEventListener('wheel',event=>{
  event.preventDefault();wheelX=clampWheel(wheelX+event.deltaX);wheelY=clampWheel(wheelY+event.deltaY);
  clearTimeout(wheelTimer);wheelTimer=setTimeout(()=>{sendAction({type:'input',action:'wheel',delta_x:wheelX,delta_y:wheelY});wheelX=0;wheelY=0},40);
},{passive:false});
canvas.addEventListener('focus',()=>keyboard.focus({preventScroll:true}));

const specialKeys=new Set(['Enter','Tab','Escape','Backspace','Delete','ArrowLeft','ArrowRight','ArrowUp','ArrowDown','Home','End']);
keyboard.addEventListener('keydown',event=>{
  if(specialKeys.has(event.key)){event.preventDefault();sendAction({type:'input',action:'key',value:event.key});}
});
keyboard.addEventListener('compositionstart',()=>{composing=true});
keyboard.addEventListener('compositionend',event=>{
  composing=false;const value=keyboard.value||event.data||'';if(value)sendText(value);keyboard.value='';
});
keyboard.addEventListener('input',()=>{
  if(!composing&&keyboard.value){sendText(keyboard.value);keyboard.value='';}
});
keyboard.addEventListener('paste',event=>{
  event.preventDefault();const value=event.clipboardData?.getData('text')||'';if(value)sendText(value);
});

home.addEventListener('click',async()=>{
  if(finished)return;
  home.disabled=true;
  try{const state=await api('/reset',{method:'POST'});setStatus(state.message,state.state);keyboard.focus({preventScroll:true});}
  catch(error){setStatus(error.message,error.status===410?'expired':'failed');}
  finally{setTimeout(()=>{home.disabled=false},1000);}
});

async function fallback(){
  if(finished)return;
  try{
    const state=await api('/status');setStatus(state.message,state.state);
    if(!finished){const response=await fetch(base+'/screenshot?t='+Date.now(),{cache:'no-store'});if(!response.ok)throw new Error('登录画面暂时不可用');await drawFrame(await response.blob());fallbackTimer=setTimeout(fallback,900);}
  }catch(error){setStatus(error.message,'failed')}
}

function connect(){
  const protocol=location.protocol==='https:'?'wss:':'ws:';
  socket=new WebSocket(protocol+'//'+location.host+base+'/stream');
  socket.binaryType='blob';
  socket.onmessage=event=>{
    if(typeof event.data==='string'){
      const message=JSON.parse(event.data);
      if(message.type==='state')setStatus(message.message,message.state);
      if(message.type==='input_error')showInputError(message.message);
      if(message.type==='error')setStatus(message.message,'failed');
    }else drawFrame(event.data).catch(()=>{});
  };
  socket.onerror=()=>socket.close();
  socket.onclose=()=>{if(!finished&&!fallbackTimer)fallback()};
}
connect();
</script>
</body>
</html>`;
}
